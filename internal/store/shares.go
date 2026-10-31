package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/helmerznl/nas-storage-analyzer/internal/scanner"
)

// Share is the subset of the shares table the scan CLI needs.
type Share struct {
	ID           int64
	Name         string
	Kind         string
	Path         string
	Excludes     []string
	Workers      int
	MaxOpsPerSec int
	LastScanID   *int64
}

// EnsureShare creates or updates a bind-mount share by name and makes sure
// its partitions exist. An existing share keeps the settings in its config that
// this call does not know about (the base paths of the delete scripts).
func (s *Store) EnsureShare(ctx context.Context, name, path string, excludes []string, workers, maxOps int) (*Share, error) {
	cfg, _ := json.Marshal(map[string]string{"path": path})
	if excludes == nil {
		excludes = []string{}
	}
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO shares (name, kind, config, excludes, workers, max_ops_per_sec)
		VALUES ($1, 'bind', $2, $3, $4, $5)
		ON CONFLICT (name) DO UPDATE SET config = shares.config || EXCLUDED.config, excludes = EXCLUDED.excludes,
			workers = EXCLUDED.workers, max_ops_per_sec = EXCLUDED.max_ops_per_sec
		RETURNING id`, name, cfg, excludes, workers, maxOps).Scan(&id)
	if err != nil {
		return nil, err
	}
	if _, err := s.Pool.Exec(ctx, `SELECT ensure_share_partitions($1)`, id); err != nil {
		return nil, err
	}
	return &Share{ID: id, Name: name, Kind: "bind", Path: path, Excludes: excludes, Workers: workers, MaxOpsPerSec: maxOps}, nil
}

// ScanPlan says how the next scan of a share should run.
type ScanPlan struct {
	Mode     string // "full" or "incremental"
	HasPrior bool   // an earlier successful scan exists, so directory ids are reusable
}

// PlanScan chooses incremental mode only when a successful earlier scan exists
// that used the same exclude patterns and the caller did not ask for full.
func (s *Store) PlanScan(ctx context.Context, shareID int64, forceFull bool, excludesHash string) (ScanPlan, error) {
	var prevHash string
	err := s.Pool.QueryRow(ctx, `
		SELECT excludes_hash FROM scans
		WHERE share_id = $1 AND status = 'succeeded' ORDER BY id DESC LIMIT 1`, shareID).Scan(&prevHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return ScanPlan{Mode: "full"}, nil
	}
	if err != nil {
		return ScanPlan{}, err
	}
	if forceFull || prevHash != excludesHash {
		return ScanPlan{Mode: "full", HasPrior: true}, nil
	}
	return ScanPlan{Mode: "incremental", HasPrior: true}, nil
}

// BeginScan inserts the scan row. It fails when another scan of the share is
// still marked running, which enforces one scan per share.
func (s *Store) BeginScan(ctx context.Context, shareID int64, mode, trigger, excludesHash string) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO scans (share_id, mode, trigger, excludes_hash) VALUES ($1, $2, $3, $4) RETURNING id`,
		shareID, mode, trigger, excludesHash).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return 0, ErrScanRunning
	}
	return id, err
}

// ErrScanRunning means the share already has a running scan.
var ErrScanRunning = errors.New("a scan of this share is already running")

// FailScan marks a scan failed or cancelled; stored data is untouched.
func (s *Store) FailScan(ctx context.Context, scanID int64, cancelled bool, cause error, errorCount int64) error {
	status := "failed"
	if cancelled {
		status = "cancelled"
	}
	_, err := s.Pool.Exec(ctx, `
		UPDATE scans SET status = $2, finished_at = now(), error = $3, error_count = $4, progress = NULL WHERE id = $1`,
		scanID, status, cause.Error(), errorCount)
	return err
}

// StaleRunningScans marks scans left 'running' by a crashed process as failed.
func (s *Store) StaleRunningScans(ctx context.Context, shareID int64) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE scans SET status = 'failed', finished_at = now(), error = 'interrupted', progress = NULL
		WHERE share_id = $1 AND status = 'running'`, shareID)
	return tag.RowsAffected(), err
}

// ScanTotals describes the stored state of a share after a scan.
type ScanTotals struct {
	Dirs, Files, Bytes, Alloc int64
}

// PreviousTotals returns the totals of the last successful scan, used for
// progress and ETA. ok is false for a first scan.
func (s *Store) PreviousTotals(ctx context.Context, shareID int64) (t ScanTotals, ok bool, err error) {
	err = s.Pool.QueryRow(ctx, `
		SELECT dirs, files, bytes, alloc_bytes FROM scans
		WHERE share_id = $1 AND status = 'succeeded' ORDER BY id DESC LIMIT 1`, shareID).
		Scan(&t.Dirs, &t.Files, &t.Bytes, &t.Alloc)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, false, nil
	}
	return t, err == nil, err
}

// DirIDs allocates directory ids from the database sequence in blocks.
type DirIDs struct {
	s     *Store
	mu    sync.Mutex
	next  []int64
	block int
}

func (s *Store) NewDirIDs() *DirIDs { return &DirIDs{s: s, block: 1000} }

func (d *DirIDs) Next(ctx context.Context) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.next) == 0 {
		rows, err := d.s.Pool.Query(ctx, `SELECT nextval('dir_id_seq') FROM generate_series(1, $1)`, d.block)
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return 0, err
			}
			d.next = append(d.next, id)
		}
		if err := rows.Err(); err != nil {
			return 0, err
		}
	}
	id := d.next[0]
	d.next = d.next[1:]
	return id, nil
}

// PGPrior serves the previous directory table of a share to the scanner.
type PGPrior struct {
	s       *Store
	shareID int64
}

func (s *Store) NewPrior(shareID int64) *PGPrior { return &PGPrior{s: s, shareID: shareID} }

func (p *PGPrior) Root(ctx context.Context) (*scanner.PriorDir, error) {
	var d scanner.PriorDir
	var mtime time.Time
	err := p.s.Pool.QueryRow(ctx, `
		SELECT id, mtime, entry_count, path::text, depth FROM dirs WHERE share_id = $1 AND parent_id IS NULL`,
		p.shareID).Scan(&d.ID, &mtime, &d.EntryCount, &d.LPath, &d.Depth)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	d.Mtime = mtime
	return &d, err
}

func (p *PGPrior) Children(ctx context.Context, dirID int64) (map[string]scanner.PriorDir, error) {
	rows, err := p.s.Pool.Query(ctx, `
		SELECT name, id, mtime, entry_count, path::text, depth FROM dirs WHERE share_id = $1 AND parent_id = $2`,
		p.shareID, dirID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]scanner.PriorDir{}
	for rows.Next() {
		var name string
		var d scanner.PriorDir
		if err := rows.Scan(&name, &d.ID, &d.Mtime, &d.EntryCount, &d.LPath, &d.Depth); err != nil {
			return nil, err
		}
		out[name] = d
	}
	return out, rows.Err()
}

// PreviousAtimeQuality returns the stored atime verdict of the last
// successful scan, as JSON. ok is false when there is none.
func (s *Store) PreviousAtimeQuality(ctx context.Context, shareID int64) (raw []byte, ok bool, err error) {
	err = s.Pool.QueryRow(ctx, `
		SELECT atime_quality FROM scans
		WHERE share_id = $1 AND status = 'succeeded' AND atime_quality IS NOT NULL ORDER BY id DESC LIMIT 1`,
		shareID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return raw, err == nil && len(raw) > 0, err
}
