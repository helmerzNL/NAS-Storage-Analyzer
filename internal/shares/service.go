package shares

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	"github.com/helmerznl/nas-storage-analyzer/internal/secrets"
)

// Errors of the service.
var (
	ErrNotFound = errors.New("share not found")
	ErrConflict = errors.New("a share with this name already exists")
	ErrBusy     = errors.New("the share has a scan queued or running; cancel it first")
)

// Service stores shares.
type Service struct {
	pool *pgxpool.Pool
	box  *secrets.Box
	opt  Options
	loc  *time.Location
}

// NewService builds the service. loc is the time zone cron schedules use.
func NewService(pool *pgxpool.Pool, box *secrets.Box, opt Options, loc *time.Location) *Service {
	if loc == nil {
		loc = time.UTC
	}
	return &Service{pool: pool, box: box, opt: opt, loc: loc}
}

// Full is a share as the worker needs it, with the SMB password decrypted.
type Full struct {
	ID       int64
	Input    Input
	Password string
}

func configOf(in Input) []byte {
	m := map[string]string{}
	set := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	set("path", in.Path)
	set("server", in.Server)
	set("export", in.Export)
	set("share", in.Share)
	set("username", in.Username)
	set("domain", in.Domain)
	set("version", in.Version)
	set("mount_options", in.MountOptions)
	b, _ := json.Marshal(m)
	return b
}

func applyConfig(in *Input, raw []byte) {
	var m map[string]string
	_ = json.Unmarshal(raw, &m)
	in.Path, in.Server, in.Export, in.Share = m["path"], m["server"], m["export"], m["share"]
	in.Username, in.Domain, in.Version, in.MountOptions = m["username"], m["domain"], m["version"], m["mount_options"]
}

const loadSQL = `SELECT id, name, kind, config, excludes, workers, max_ops_per_sec, max_ops_day, day_start_hour, day_end_hour, cron,
	last_used_basis, full_scan_every, retention_scans, retention_days, snapshot_depth, secret_enc FROM shares`

func scanFull(row pgx.Row) (*Full, []byte, error) {
	var f Full
	var cfg, sec []byte
	in := &f.Input
	err := row.Scan(&f.ID, &in.Name, &in.Kind, &cfg, &in.Excludes, &in.Workers, &in.MaxOpsPerSec, &in.MaxOpsDay, &in.DayStartHour,
		&in.DayEndHour, &in.Cron, &in.LastUsedBasis, &in.FullScanEvery, &in.RetentionScans, &in.RetentionDays, &in.SnapshotDepth, &sec)
	applyConfig(in, cfg)
	return &f, sec, err
}

// Load reads a share for the worker, with its password decrypted.
func (s *Service) Load(ctx context.Context, id int64) (*Full, error) {
	f, sec, err := scanFull(s.pool.QueryRow(ctx, loadSQL+` WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(sec) > 0 {
		pw, err := s.box.Open(sec)
		if err != nil {
			return nil, err
		}
		f.Password = string(pw)
	}
	return f, nil
}

func (s *Service) nextRun(cron string) (*time.Time, error) {
	return jobs.NextRun(cron, time.Now(), s.loc)
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// Create validates and stores a new share and returns its id.
func (s *Service) Create(ctx context.Context, in Input) (int64, error) {
	if err := Validate(&in, s.opt); err != nil {
		return 0, err
	}
	var sec []byte
	if in.Kind == KindSMB && in.Password != nil && *in.Password != "" {
		sec = s.box.Seal([]byte(*in.Password))
	}
	next, err := s.nextRun(in.Cron)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.pool.QueryRow(ctx, `
		INSERT INTO shares (name, kind, config, excludes, workers, max_ops_per_sec, max_ops_day, day_start_hour, day_end_hour, cron,
			next_run_at, last_used_basis, full_scan_every, retention_scans, retention_days, snapshot_depth, secret_enc)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17) RETURNING id`,
		in.Name, in.Kind, configOf(in), in.Excludes, in.Workers, in.MaxOpsPerSec, in.MaxOpsDay, in.DayStartHour, in.DayEndHour,
		in.Cron, next, in.LastUsedBasis, in.FullScanEvery, in.RetentionScans, in.RetentionDays, in.SnapshotDepth, sec).Scan(&id)
	if isUnique(err) {
		return 0, ErrConflict
	}
	if err != nil {
		return 0, err
	}
	if _, err := s.pool.Exec(ctx, `SELECT ensure_share_partitions($1)`, id); err != nil {
		return 0, err
	}
	return id, nil
}

// Update overlays a JSON object on the stored share: fields that are absent
// keep their value, so a client can send only what changed. "password" sets a
// new SMB password, an empty string removes it.
func (s *Service) Update(ctx context.Context, id int64, patch []byte) error {
	cur, sec, err := scanFull(s.pool.QueryRow(ctx, loadSQL+` WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	in := cur.Input
	if err := json.Unmarshal(patch, &in); err != nil {
		return bad("body", "not valid JSON for a share")
	}
	if err := Validate(&in, s.opt); err != nil {
		return err
	}
	switch {
	case in.Kind != KindSMB:
		sec = nil
	case in.Password != nil && *in.Password == "":
		sec = nil
	case in.Password != nil:
		sec = s.box.Seal([]byte(*in.Password))
	}
	next, err := s.nextRun(in.Cron)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE shares SET name = $2, kind = $3, config = $4, excludes = $5, workers = $6, max_ops_per_sec = $7, max_ops_day = $8,
			day_start_hour = $9, day_end_hour = $10, cron = $11, next_run_at = $12, last_used_basis = $13, full_scan_every = $14,
			retention_scans = $15, retention_days = $16, snapshot_depth = $17, secret_enc = $18 WHERE id = $1`,
		id, in.Name, in.Kind, configOf(in), in.Excludes, in.Workers, in.MaxOpsPerSec, in.MaxOpsDay, in.DayStartHour, in.DayEndHour,
		in.Cron, next, in.LastUsedBasis, in.FullScanEvery, in.RetentionScans, in.RetentionDays, in.SnapshotDepth, sec)
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

// Delete removes a share with all of its scan data.
func (s *Service) Delete(ctx context.Context, id int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE share_id = $1 AND state IN ('queued', 'running'))`, id).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return ErrBusy
	}
	tag, err := tx.Exec(ctx, `DELETE FROM shares WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `SELECT drop_share_partitions($1)`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetExtension maps an extension to a category (creating or changing the
// mapping). It reports whether the extension is new.
func (s *Service) SetExtension(ctx context.Context, ext string, category int16) (isNew bool, err error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM categories WHERE id = $1)`, category).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, bad("category_id", "unknown category")
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO extension_map (ext, category_id) VALUES ($1, $2)
		ON CONFLICT (ext) DO UPDATE SET category_id = EXCLUDED.category_id RETURNING (xmax = 0)`, ext, category).Scan(&isNew)
	return isNew, err
}

// DeleteExtension removes a mapping; the extension falls back to "other".
func (s *Service) DeleteExtension(ctx context.Context, ext string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM extension_map WHERE ext = $1`, ext)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
