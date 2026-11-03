package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
)

// Errors of the service.
var (
	// ErrNoStage: the token is not one the person staged (or it is used, or it is older than an hour). The file has to be chosen again.
	ErrNoStage = errors.New("there is no staged backup with this token; choose the file again")
	// ErrRunning: a restore is already running.
	ErrRunning = errors.New("a restore is already running")
	// ErrNoJob: there is no such restore.
	ErrNoJob = errors.New("there is no such restore")
	// ErrBusy: scans are queued or running. A restore changes the shares, so it waits until they are done or cancelled. Start refuses with it
	// before it takes the staged file; the swap refuses with it, as a failed job, if a scan was queued in between.
	ErrBusy = errors.New("a scan is queued or running; wait until it is done or cancel it, then restore")
)

var tokenRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Auditor writes the audit log (auth.Service does).
type Auditor interface {
	Audit(ctx context.Context, a auth.Actor, action, target string, detail map[string]any) error
}

// Service makes and restores backups.
type Service struct {
	pool    *pgxpool.Pool
	shares  *shares.Service
	auth    *auth.Service
	audit   Auditor
	queue   *jobs.Queue
	log     *slog.Logger
	version string

	// Now is the clock (tests). Go starts the restore's work: a goroutine, or inline in tests that want to look at the result at once.
	Now func() time.Time
	Go  func(func())
	// hook runs between the steps of the swap, inside the transaction; a test makes it fail to prove that a failure halfway changes nothing.
	hook func(step string) error
}

// New builds the service on a read-write pool. version is the version of this build, written into every file.
func New(pool *pgxpool.Pool, sh *shares.Service, au *auth.Service, q *jobs.Queue, log *slog.Logger, version string) *Service {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{pool: pool, shares: sh, auth: au, audit: au, queue: q, log: log, version: version}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ---- the file ----------------------------------------------------------------------------------

// Export builds the backup file: what the owner entered, and nothing that unlocks the application. It never decrypts anything.
func (s *Service) Export(ctx context.Context) ([]byte, error) {
	sh, err := s.shares.Export(ctx)
	if err != nil {
		return nil, err
	}
	ft, err := s.shares.ExportFileTypes(ctx)
	if err != nil {
		return nil, err
	}
	pr, err := s.auth.ExportPrefs(ctx)
	if err != nil {
		return nil, err
	}
	f := File{Format: Format, FormatVersion: FormatVersion, AppVersion: s.version, CreatedAt: s.now().UTC().Truncate(time.Second),
		Shares: sh, FileTypes: ft, Preferences: pr}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// FileName is the name a downloaded backup gets.
func (s *Service) FileName() string {
	return "nas-storage-analyzer-backup-" + s.now().UTC().Format("2006-01-02") + ".json"
}

// ---- what a restore would do ----------------------------------------------------------------------

// Changes says what a restore does (or did) to what is there now. It is what the confirmation dialog shows and what a finished job keeps.
type Changes struct {
	SharesAdded   []string `json:"shares_added"`
	SharesUpdated []string `json:"shares_updated"`
	// SharesRemoved are shares the file does not have: they go, with all of their scan data.
	SharesRemoved       []string `json:"shares_removed"`
	FileTypesChanged    int      `json:"file_types_changed"`
	PreferencesApplied  int      `json:"preferences_applied"`
	PreferencesSkipped  []string `json:"preferences_skipped"`  // people the file names who have no account here
	PasswordsUnreadable []string `json:"passwords_unreadable"` // shares restored without their SMB password
	// RecategorizeShares is how many shares are re-categorised in the background because the file-type map changed.
	RecategorizeShares int `json:"recategorize_shares"`
}

// Counts is how many entries each section of the file has.
type Counts struct {
	Shares      int `json:"shares"`
	FileTypes   int `json:"file_types"`
	Preferences int `json:"preferences"`
}

// Summary describes a staged file.
type Summary struct {
	FormatVersion int       `json:"format_version"`
	AppVersion    string    `json:"app_version"`
	CreatedAt     time.Time `json:"created_at"`
	Counts        Counts    `json:"counts"`
	// Missing lists the sections the file does not have: a restore leaves those as they are.
	Missing []string `json:"missing"`
	Changes Changes  `json:"changes"`
	// ScansActive is true while scans are queued or running: starting the restore is refused until they are done.
	ScansActive bool `json:"scans_active"`
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// describe works out what restoring c would do now, without changing anything.
func (s *Service) describe(ctx context.Context, q auth.Querier, c *Checked) (Changes, []string, error) {
	ch := Changes{SharesAdded: []string{}, SharesUpdated: []string{}, SharesRemoved: []string{}, PreferencesSkipped: []string{},
		PasswordsUnreadable: nonNil(c.Unreadable)}
	var missing []string
	if c.File.Shares != nil {
		p, err := s.shares.PlanFor(ctx, c.File.Shares)
		if err != nil {
			return ch, nil, err
		}
		ch.SharesAdded, ch.SharesUpdated, ch.SharesRemoved = p.Added, p.Updated, p.Removed
	} else {
		missing = append(missing, "shares")
	}
	if c.File.FileTypes != nil {
		n, err := s.shares.FileTypesChanged(ctx, c.File.FileTypes)
		if err != nil {
			return ch, nil, err
		}
		ch.FileTypesChanged = n
	} else {
		missing = append(missing, "file_types")
	}
	if c.File.Preferences != nil {
		for _, p := range c.Prefs {
			exists, differs, err := auth.PrefsStatus(ctx, q, p.Username, p.Prefs)
			if err != nil {
				return ch, nil, err
			}
			switch {
			case !exists:
				ch.PreferencesSkipped = append(ch.PreferencesSkipped, p.Username)
			case differs:
				ch.PreferencesApplied++
			}
		}
	} else {
		missing = append(missing, "preferences")
	}
	return ch, nonNil(missing), nil
}

func (s *Service) scansActive(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (bool, error) {
	var busy bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE state IN ('queued', 'running'))`).Scan(&busy)
	return busy, err
}

// ---- staging -----------------------------------------------------------------------------------------

// Staged is a file that has been uploaded and checked and waits for the owner to confirm. Nothing in the database has changed.
type Staged struct {
	Token   string  `json:"token"`
	Summary Summary `json:"summary"`
}

// Stage reads an uploaded file, checks it completely and keeps it for the owner's confirmation. A refused file is not kept. One person has one
// staged file at a time: choosing another replaces it, and a file that is never used is removed after an hour, the next time one is staged.
func (s *Service) Stage(ctx context.Context, by string, r io.Reader) (*Staged, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read the upload: %w", err)
	}
	c, err := s.Check(ctx, data)
	if err != nil {
		return nil, err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(b[:])
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `DELETE FROM backup_stages WHERE created_at < now() - make_interval(secs => $1::float8) OR created_by = $2`, StageTTL.Seconds(), by); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO backup_stages (token, created_by, size, data) VALUES ($1, $2, $3, $4)`, token, by, len(data), data); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	sum, err := s.summarize(ctx, c)
	if err != nil {
		return nil, err
	}
	return &Staged{Token: token, Summary: sum}, nil
}

func (s *Service) summarize(ctx context.Context, c *Checked) (Summary, error) {
	ch, missing, err := s.describe(ctx, s.pool, c)
	if err != nil {
		return Summary{}, err
	}
	active, err := s.scansActive(ctx, s.pool)
	if err != nil {
		return Summary{}, err
	}
	return Summary{FormatVersion: c.File.FormatVersion, AppVersion: c.File.AppVersion, CreatedAt: c.File.CreatedAt,
		Counts:  Counts{Shares: len(c.File.Shares), FileTypes: len(c.File.FileTypes), Preferences: len(c.File.Preferences)},
		Missing: missing, Changes: ch, ScansActive: active}, nil
}

// Unstage removes a staged file the person changed their mind about. It is not an error when it is already gone.
func (s *Service) Unstage(ctx context.Context, by, token string) error {
	if !tokenRE.MatchString(token) {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM backup_stages WHERE token = $1 AND created_by = $2`, token, by)
	return err
}

// ---- jobs ------------------------------------------------------------------------------------------------

// Job is one restore, as the screen reads it.
type Job struct {
	ID          int64             `json:"id"`
	Status      string            `json:"status"` // running | done | failed
	Phase       string            `json:"phase"`  // checking | replacing
	Step        int               `json:"step"`
	Steps       int               `json:"steps"` // 0: cannot be counted
	ErrorKey    string            `json:"error_key"`
	ErrorParams map[string]string `json:"error_params"`
	Summary     *Changes          `json:"summary"`
	CheckingMS  *int              `json:"checking_ms"`
	ReplacingMS *int              `json:"replacing_ms"`
	StartedBy   string            `json:"started_by"`
	StartedAt   time.Time         `json:"started_at"`
	FinishedAt  *time.Time        `json:"finished_at"`
}

const jobColumns = `id, status, phase, step, steps, error_key, error_params, summary, checking_ms, replacing_ms, started_by, started_at, finished_at`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.Status, &j.Phase, &j.Step, &j.Steps, &j.ErrorKey, &j.ErrorParams, &j.Summary, &j.CheckingMS, &j.ReplacingMS,
		&j.StartedBy, &j.StartedAt, &j.FinishedAt)
	if j.ErrorParams == nil {
		j.ErrorParams = map[string]string{}
	}
	return &j, err
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// expireStale gives up a running job that has not moved for JobStale (the container restarted under it). That is safe, because the swap is one
// transaction: an interrupted restore changed nothing, and the message says so.
func expireStale(ctx context.Context, db execer) error {
	_, err := db.Exec(ctx, `UPDATE backup_jobs SET status = 'failed', error_key = 'interrupted', finished_at = now()
		WHERE status = 'running' AND updated_at < now() - make_interval(secs => $1::float8)`, JobStale.Seconds())
	return err
}

// Start turns a staged file into a restore that runs on the server and answers at once. The token is used up: it works once.
func (s *Service) Start(ctx context.Context, actor auth.Actor, token string) (*Job, error) {
	if !tokenRE.MatchString(token) {
		return nil, ErrNoStage
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if err := expireStale(ctx, tx); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM backup_jobs WHERE finished_at < now() - interval '30 days'`); err != nil {
		return nil, err
	}
	// Scans queued or running: refuse now, and keep the staged file, so the person can press the button again when they are done. This is the
	// friendly early answer; the swap checks again inside its transaction, which is the one that cannot be raced.
	if busy, err := s.scansActive(ctx, tx); err != nil {
		return nil, err
	} else if busy {
		return nil, ErrBusy
	}
	var data []byte
	err = tx.QueryRow(ctx, `DELETE FROM backup_stages WHERE token = $1 AND created_by = $2 AND created_at > now() - make_interval(secs => $3::float8) RETURNING data`,
		token, actor.Username, StageTTL.Seconds()).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoStage
	}
	if err != nil {
		return nil, err
	}
	j, err := scanJob(tx.QueryRow(ctx, `INSERT INTO backup_jobs (started_by) VALUES ($1) RETURNING `+jobColumns, actor.Username))
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return nil, ErrRunning // the staged file stays: the transaction rolls back
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	run := func() { s.run(j.ID, actor, data) }
	if s.Go != nil {
		s.Go(run)
	} else {
		go run()
	}
	return j, nil
}

// Latest is the running job, or the last one that has not been dismissed: nil when there is none.
func (s *Service) Latest(ctx context.Context) (*Job, error) {
	if err := expireStale(ctx, s.pool); err != nil {
		return nil, err
	}
	j, err := scanJob(s.pool.QueryRow(ctx, `SELECT `+jobColumns+` FROM backup_jobs WHERE NOT dismissed ORDER BY id DESC LIMIT 1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return j, err
}

// Dismiss is what the screen calls once it has shown the outcome. A job that is still running cannot be dismissed.
func (s *Service) Dismiss(ctx context.Context, id int64) error {
	if err := expireStale(ctx, s.pool); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE backup_jobs SET dismissed = true WHERE id = $1 AND status <> 'running'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var running bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM backup_jobs WHERE id = $1 AND status = 'running')`, id).Scan(&running); err != nil {
			return err
		}
		if running {
			return ErrRunning
		}
		return ErrNoJob
	}
	return nil
}
