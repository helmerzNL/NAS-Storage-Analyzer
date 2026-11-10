// Package notify tells the owner when a scan needs attention, by a POST to an address they chose: a webhook of Home Assistant,
// ntfy, Gotify, or anything else that takes one. Nothing is sent anywhere unless an administrator added a target.
//
// What is sent is the outcome of a scan (which share, what happened, the counts and the worker's reason) and never a file name or a
// folder listing. The address of a target is a secret of the receiving service (its token is in the path or the query), so it is
// stored sealed, shown masked, and never written to a log or the audit log.
package notify

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/helmerznl/nas-storage-analyzer/internal/secrets"
)

// MaxTargets is how many targets an installation can have. One or two is the use; the limit keeps a scan from fanning out.
const MaxTargets = 5

// Formats of the request body.
const (
	FormatJSON = "json" // one JSON object: Home Assistant, Gotify (it reads title, message and priority), anything generic
	FormatText = "text" // the message as plain text, the title and priority in headers: ntfy
)

var (
	ErrNotFound = errors.New("notification target not found")
	ErrConflict = errors.New("a notification target with this name already exists")
	ErrTooMany  = fmt.Errorf("there can be at most %d notification targets", MaxTargets)
)

// ValidationError names the field that is wrong.
type ValidationError struct{ Field, Msg string }

func (e *ValidationError) Error() string { return e.Field + ": " + e.Msg }

func bad(field, format string, a ...any) error {
	return &ValidationError{Field: field, Msg: fmt.Sprintf(format, a...)}
}

// Events says for which outcomes of a scan a target wants a message.
type Events struct {
	Failed   bool `json:"failed"`   // the scan failed, or was refused because the share looked unmounted
	Empty    bool `json:"empty"`    // the scan finished and found no files
	Errors   bool `json:"errors"`   // the scan finished, and some folders or files could not be read
	Finished bool `json:"finished"` // the scan finished with nothing to report
}

// Target is where messages go, as the settings screen sees it: the address only as a hint (scheme and host).
type Target struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	URLHint    string     `json:"url_hint"`
	Format     string     `json:"format"`
	Lang       string     `json:"lang"`
	Events     Events     `json:"events"`
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	LastAt     *time.Time `json:"last_at"`
	LastOK     *bool      `json:"last_ok"`
	LastStatus *int       `json:"last_status"`
	LastError  *string    `json:"last_error"`
}

// Input is what an administrator enters. URL nil keeps the stored address (an update that changes something else).
type Input struct {
	Name    string  `json:"name"`
	URL     *string `json:"url"`
	Format  string  `json:"format"`
	Lang    string  `json:"lang"`
	Events  Events  `json:"events"`
	Enabled bool    `json:"enabled"`
}

// Defaults is a new target before the administrator changes anything: every outcome that needs attention, not the quiet ones.
func Defaults() Input {
	return Input{Format: FormatJSON, Lang: "en", Events: Events{Failed: true, Empty: true, Errors: true}, Enabled: true}
}

// Service stores the targets and delivers messages to them.
type Service struct {
	pool *pgxpool.Pool
	box  *secrets.Box
	send *sender
}

func NewService(pool *pgxpool.Pool, box *secrets.Box) *Service {
	return &Service{pool: pool, box: box, send: newSender()}
}

// SetRetryWaits replaces how long to wait before each attempt of a message about a scan (by default at once, after 2 s and after
// 8 s: three attempts). Tests use it to not wait.
func (s *Service) SetRetryWaits(waits ...time.Duration) { s.send.backoff = waits }

// Hint is what of an address may be shown: its scheme and host. The path and the query hold the token; a user name or password in
// the address is left out too.
func Hint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// CheckURL holds an address to the rules: http or https, a host, nothing odd. It does not connect.
func CheckURL(raw string) error {
	if raw == "" {
		return bad("url", "an address is required, for example https://ntfy.sh/my-topic")
	}
	if len(raw) > 2000 {
		return bad("url", "the address is longer than 2000 characters")
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return bad("url", "the address contains a space or a line break")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return bad("url", "this is not an address")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return bad("url", "the address must start with http:// or https://")
	}
	if u.Hostname() == "" {
		return bad("url", "the address has no host")
	}
	if u.Fragment != "" {
		return bad("url", "leave out the part after #: it is never sent to a server")
	}
	return nil
}

func (in *Input) clean(needURL bool) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 60 {
		return bad("name", "a name is 1-60 characters")
	}
	if in.Format != FormatJSON && in.Format != FormatText {
		return bad("format", "the format is json or text")
	}
	if in.Lang != "nl" && in.Lang != "en" {
		return bad("lang", "the language is nl or en")
	}
	if in.URL != nil {
		*in.URL = strings.TrimSpace(*in.URL)
		if err := CheckURL(*in.URL); err != nil {
			return err
		}
	} else if needURL {
		return bad("url", "an address is required, for example https://ntfy.sh/my-topic")
	}
	return nil
}

const cols = `id, name, url_enc, format, lang, on_failed, on_empty, on_errors, on_finished, enabled, created_at, updated_at, last_at, last_ok, last_status, last_error`

// stored is a target with its address. The address leaves this package only as a hint.
type stored struct {
	Target
	url string
	// unreadable is set when the address does not open with the installation's key (a wrong NASSA_ENCRYPTION_KEY).
	unreadable bool
}

func (s *Service) scan(row pgx.Row) (*stored, error) {
	var t stored
	var enc []byte
	if err := row.Scan(&t.ID, &t.Name, &enc, &t.Format, &t.Lang, &t.Events.Failed, &t.Events.Empty, &t.Events.Errors, &t.Events.Finished, &t.Enabled,
		&t.CreatedAt, &t.UpdatedAt, &t.LastAt, &t.LastOK, &t.LastStatus, &t.LastError); err != nil {
		return nil, err
	}
	plain, err := s.box.Open(enc)
	if err != nil {
		t.unreadable = true
		return &t, nil
	}
	t.url, t.URLHint = string(plain), Hint(string(plain))
	return &t, nil
}

func (s *Service) all(ctx context.Context) ([]*stored, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+cols+` FROM notification_targets ORDER BY lower(name), id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*stored
	for rows.Next() {
		t, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Get returns one target.
func (s *Service) Get(ctx context.Context, id int64) (*Target, error) {
	t, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &t.Target, nil
}

// List returns the targets, by name.
func (s *Service) List(ctx context.Context) ([]Target, error) {
	all, err := s.all(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Target, 0, len(all))
	for _, t := range all {
		out = append(out, t.Target)
	}
	return out, nil
}

func (s *Service) get(ctx context.Context, id int64) (*stored, error) {
	t, err := s.scan(s.pool.QueryRow(ctx, `SELECT `+cols+` FROM notification_targets WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// Create adds a target.
func (s *Service) Create(ctx context.Context, in Input) (*Target, error) {
	if err := in.clean(true); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Counting and adding are one step, or two requests at once could both fit under the limit.
	if _, err := tx.Exec(ctx, `LOCK TABLE notification_targets IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return nil, err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM notification_targets`).Scan(&n); err != nil {
		return nil, err
	}
	if n >= MaxTargets {
		return nil, ErrTooMany
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO notification_targets (name, url_enc, format, lang, on_failed, on_empty, on_errors, on_finished, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		in.Name, s.box.Seal([]byte(*in.URL)), in.Format, in.Lang, in.Events.Failed, in.Events.Empty, in.Events.Errors, in.Events.Finished, in.Enabled).Scan(&id)
	if isUnique(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	t, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &t.Target, nil
}

// Update replaces what the administrator entered. The address is kept when in.URL is nil, and the outcome of the last delivery is
// forgotten when the address changes: it was about another receiver.
func (s *Service) Update(ctx context.Context, id int64, in Input) (*Target, error) {
	if err := in.clean(false); err != nil {
		return nil, err
	}
	var tag pgconn.CommandTag
	var err error
	if in.URL != nil {
		tag, err = s.pool.Exec(ctx, `UPDATE notification_targets SET name = $2, url_enc = $3, format = $4, lang = $5, on_failed = $6, on_empty = $7,
			on_errors = $8, on_finished = $9, enabled = $10, updated_at = now(), last_at = NULL, last_ok = NULL, last_status = NULL, last_error = NULL WHERE id = $1`,
			id, in.Name, s.box.Seal([]byte(*in.URL)), in.Format, in.Lang, in.Events.Failed, in.Events.Empty, in.Events.Errors, in.Events.Finished, in.Enabled)
	} else {
		tag, err = s.pool.Exec(ctx, `UPDATE notification_targets SET name = $2, format = $3, lang = $4, on_failed = $5, on_empty = $6,
			on_errors = $7, on_finished = $8, enabled = $9, updated_at = now() WHERE id = $1`,
			id, in.Name, in.Format, in.Lang, in.Events.Failed, in.Events.Empty, in.Events.Errors, in.Events.Finished, in.Enabled)
	}
	if isUnique(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	t, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &t.Target, nil
}

// Delete removes a target and returns its name.
func (s *Service) Delete(ctx context.Context, id int64) (string, error) {
	var name string
	err := s.pool.QueryRow(ctx, `DELETE FROM notification_targets WHERE id = $1 RETURNING name`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

// Delivery is how sending one message to one target went.
type Delivery struct {
	TargetID int64  `json:"target_id"`
	Target   string `json:"target"`
	OK       bool   `json:"ok"`
	Status   int    `json:"status"`   // the HTTP status of the last attempt, 0 when there was no answer
	Error    string `json:"error"`    // why it failed, without the address
	Attempts int    `json:"attempts"` // how many times it was tried
}

func (s *Service) record(ctx context.Context, d Delivery) {
	var status *int
	if d.Status != 0 {
		status = &d.Status
	}
	var msg *string
	if d.Error != "" {
		msg = &d.Error
	}
	// Recording must never change what happened; the caller has the delivery itself.
	_, _ = s.pool.Exec(ctx, `UPDATE notification_targets SET last_at = now(), last_ok = $2, last_status = $3, last_error = $4 WHERE id = $1`, d.TargetID, d.OK, status, msg)
}

func (s *Service) deliver(ctx context.Context, t *stored, ev Event, attempts []time.Duration) Delivery {
	d := Delivery{TargetID: t.ID, Target: t.Name}
	if t.unreadable {
		d.Error = "the stored address does not open with NASSA_ENCRYPTION_KEY: enter the address again"
	} else {
		d.OK, d.Status, d.Error, d.Attempts = s.send.deliver(ctx, t.url, t.Format, compose(ev, t.Lang), attempts)
	}
	rec, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.record(rec, d)
	return d
}

// Test sends a test message to one target now, once, and says how it went. Enabled or not: the test is how one finds out.
func (s *Service) Test(ctx context.Context, id int64) (*Delivery, error) {
	t, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	d := s.deliver(ctx, t, Event{Kind: KindTest, At: time.Now()}, []time.Duration{0})
	return &d, nil
}

// Notify sends the event to every enabled target that asked for its kind, one after the other, and returns how each went. A target
// that does not answer is tried three times. It returns nothing when no target wants the event, which is the usual case.
func (s *Service) Notify(ctx context.Context, ev Event) ([]Delivery, error) {
	all, err := s.all(ctx)
	if err != nil {
		return nil, err
	}
	var out []Delivery
	for _, t := range all {
		if !t.Enabled || !t.Events.wants(ev.Kind) {
			continue
		}
		out = append(out, s.deliver(ctx, t, ev, s.send.backoff))
	}
	return out, nil
}

func (e Events) wants(kind string) bool {
	switch kind {
	case KindFailed:
		return e.Failed
	case KindEmpty:
		return e.Empty
	case KindErrors:
		return e.Errors
	case KindFinished:
		return e.Finished
	}
	return false
}
