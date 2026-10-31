package query

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ScanInfo is one row of the scans table.
type ScanInfo struct {
	ID           int64           `json:"id"`
	ShareID      int64           `json:"share_id"`
	Status       string          `json:"status"`
	Mode         string          `json:"mode"`
	Trigger      string          `json:"trigger"`
	StartedAt    time.Time       `json:"started_at"`
	FinishedAt   *time.Time      `json:"finished_at"`
	Dirs         int64           `json:"dirs"`
	ReusedDirs   int64           `json:"reused_dirs"`
	Files        int64           `json:"files"`
	Bytes        int64           `json:"bytes"`
	AllocBytes   int64           `json:"alloc_bytes"`
	ErrorCount   int64           `json:"error_count"`
	FsTotalBytes *int64          `json:"fs_total_bytes"`
	FsFreeBytes  *int64          `json:"fs_free_bytes"`
	AtimeQuality json.RawMessage `json:"atime_quality"`
	Error        *string         `json:"error"`
}

const scanCols = `sc.id, sc.share_id, sc.status, sc.mode, sc.trigger, sc.started_at, sc.finished_at, sc.dirs, sc.reused_dirs,
	sc.files, sc.bytes, sc.alloc_bytes, sc.error_count, sc.fs_total_bytes, sc.fs_free_bytes, sc.atime_quality, sc.error`

func scanScan(row pgx.Row) (*ScanInfo, error) {
	var s ScanInfo
	var aq []byte
	if err := row.Scan(&s.ID, &s.ShareID, &s.Status, &s.Mode, &s.Trigger, &s.StartedAt, &s.FinishedAt, &s.Dirs,
		&s.ReusedDirs, &s.Files, &s.Bytes, &s.AllocBytes, &s.ErrorCount, &s.FsTotalBytes, &s.FsFreeBytes, &aq, &s.Error); err != nil {
		return nil, err
	}
	if len(aq) > 0 {
		s.AtimeQuality = aq
	}
	return &s, nil
}

// ActiveJob is the queued or running job of a share, with live progress.
type ActiveJob struct {
	ID          int64           `json:"id"`
	Kind        string          `json:"kind"`
	State       string          `json:"state"` // queued | running
	Trigger     string          `json:"trigger"`
	RequestedBy string          `json:"requested_by"`
	RequestedAt time.Time       `json:"requested_at"`
	ScanID      *int64          `json:"scan_id"`
	Progress    json.RawMessage `json:"progress"`
}

// ShareInfo describes a share, its settings and the state of its scans.
type ShareInfo struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	LastUsedBasis string    `json:"last_used_basis"`
	Excludes      []string  `json:"excludes"`
	Workers       int       `json:"workers"`
	MaxOpsPerSec  int       `json:"max_ops_per_sec"`
	CreatedAt     time.Time `json:"created_at"`
	RootDirID     *int64    `json:"root_dir_id"`

	// Config holds the kind-specific, non-secret settings (path, server, export,
	// share, username, domain, version, mount_options).
	Config         map[string]string `json:"config"`
	HasPassword    bool              `json:"has_password"`
	Cron           string            `json:"cron"`
	NextRunAt      *time.Time        `json:"next_run_at"`
	LastRunAt      *time.Time        `json:"last_run_at"`
	FullScanEvery  int               `json:"full_scan_every"`
	MaxOpsDay      int               `json:"max_ops_day"`
	DayStartHour   int               `json:"day_start_hour"`
	DayEndHour     int               `json:"day_end_hour"`
	RetentionScans int               `json:"retention_scans"`
	RetentionDays  int               `json:"retention_days"`
	SnapshotDepth  int               `json:"snapshot_depth"`

	// LastScan is the scan whose data is published; LatestScan is
	// the most recent attempt (it may be running or have failed).
	LastScan   *ScanInfo  `json:"last_scan"`
	LatestScan *ScanInfo  `json:"latest_scan"`
	ActiveJob  *ActiveJob `json:"active_job"`
}

const shareSelect = `
	SELECT s.id, s.name, s.kind, s.last_used_basis, s.excludes, s.workers, s.max_ops_per_sec, s.created_at,
	       (SELECT d.id FROM dirs d WHERE d.share_id = s.id AND d.parent_id IS NULL),
	       s.config, s.secret_enc IS NOT NULL, s.cron, s.next_run_at, s.last_run_at, s.full_scan_every, s.max_ops_day,
	       s.day_start_hour, s.day_end_hour, s.retention_scans, s.retention_days, s.snapshot_depth,
	       s.last_scan_id, (SELECT max(id) FROM scans WHERE share_id = s.id)
	FROM shares s`

func scanShare(row pgx.Row) (s ShareInfo, last, latest *int64, err error) {
	var cfg []byte
	err = row.Scan(&s.ID, &s.Name, &s.Kind, &s.LastUsedBasis, &s.Excludes, &s.Workers, &s.MaxOpsPerSec, &s.CreatedAt, &s.RootDirID,
		&cfg, &s.HasPassword, &s.Cron, &s.NextRunAt, &s.LastRunAt, &s.FullScanEvery, &s.MaxOpsDay, &s.DayStartHour, &s.DayEndHour,
		&s.RetentionScans, &s.RetentionDays, &s.SnapshotDepth, &last, &latest)
	s.Config = map[string]string{}
	_ = json.Unmarshal(cfg, &s.Config)
	if s.Excludes == nil {
		s.Excludes = []string{}
	}
	return
}

func (q *DB) scanByID(ctx context.Context, id int64) (*ScanInfo, error) {
	s, err := scanScan(q.pool.QueryRow(ctx, `SELECT `+scanCols+` FROM scans sc WHERE sc.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func (q *DB) activeJob(ctx context.Context, shareID int64) (*ActiveJob, error) {
	var a ActiveJob
	err := q.pool.QueryRow(ctx, `
		SELECT j.id, j.kind, j.state, j.trigger, j.requested_by, j.requested_at, j.scan_id, sc.progress
		FROM jobs j LEFT JOIN scans sc ON sc.id = j.scan_id AND sc.status = 'running'
		WHERE j.share_id = $1 AND j.state IN ('queued', 'running') ORDER BY j.id DESC LIMIT 1`, shareID).
		Scan(&a.ID, &a.Kind, &a.State, &a.Trigger, &a.RequestedBy, &a.RequestedAt, &a.ScanID, &a.Progress)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}

// complete fills in the scans and the active job of a share.
func (q *DB) complete(ctx context.Context, s *ShareInfo, last, latest *int64) error {
	var err error
	if last != nil {
		if s.LastScan, err = q.scanByID(ctx, *last); err != nil {
			return err
		}
	}
	if latest != nil {
		if s.LatestScan, err = q.scanByID(ctx, *latest); err != nil {
			return err
		}
	}
	s.ActiveJob, err = q.activeJob(ctx, s.ID)
	return err
}

// Shares lists all shares ordered by name.
func (q *DB) Shares(ctx context.Context) ([]ShareInfo, error) {
	rows, err := q.pool.Query(ctx, shareSelect+` ORDER BY lower(s.name)`)
	if err != nil {
		return nil, err
	}
	type raw struct {
		s            ShareInfo
		last, latest *int64
	}
	var raws []raw
	for rows.Next() {
		s, last, latest, err := scanShare(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		raws = append(raws, raw{s, last, latest})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ShareInfo, 0, len(raws))
	for _, r := range raws {
		if err := q.complete(ctx, &r.s, r.last, r.latest); err != nil {
			return nil, err
		}
		out = append(out, r.s)
	}
	return out, nil
}

// ShareByID returns one share.
func (q *DB) ShareByID(ctx context.Context, id int64) (*ShareInfo, error) {
	s, last, latest, err := scanShare(q.pool.QueryRow(ctx, shareSelect+` WHERE s.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := q.complete(ctx, &s, last, latest); err != nil {
		return nil, err
	}
	return &s, nil
}

// ScanPage is a page of scans, newest first.
type ScanPage struct {
	Items      []ScanInfo `json:"items"`
	NextBefore *int64     `json:"next_before"`
}

// Scans lists the scans of a share, newest first. before is an exclusive
// upper bound on the scan id (0 = from the newest).
func (q *DB) Scans(ctx context.Context, share, before int64, limit int) (*ScanPage, error) {
	limit, err := clampLimit(limit)
	if err != nil {
		return nil, err
	}
	if _, err := q.ShareByID(ctx, share); err != nil {
		return nil, err
	}
	rows, err := q.pool.Query(ctx, `SELECT `+scanCols+` FROM scans sc
		WHERE sc.share_id = $1 AND ($2 = 0 OR sc.id < $2) ORDER BY sc.id DESC LIMIT $3`, share, before, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &ScanPage{Items: []ScanInfo{}}
	for rows.Next() {
		s, err := scanScan(rows)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, *s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1].ID
		page.NextBefore = &last
	}
	return page, nil
}

// ScanError is one logged problem of a scan.
type ScanError struct {
	ID      int64     `json:"id"`
	Path    string    `json:"path"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// ScanErrorPage is a page of scan errors in id order.
type ScanErrorPage struct {
	Items     []ScanError `json:"items"`
	NextAfter *int64      `json:"next_after"`
}

// ScanErrors lists the errors of a scan, optionally of one kind.
func (q *DB) ScanErrors(ctx context.Context, share, scan int64, kind string, after int64, limit int) (*ScanErrorPage, error) {
	limit, err := clampLimit(limit)
	if err != nil {
		return nil, err
	}
	var ok bool
	if err := q.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM scans WHERE id = $1 AND share_id = $2)`, scan, share).Scan(&ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	rows, err := q.pool.Query(ctx, `SELECT id, path, kind, message, at FROM scan_errors
		WHERE scan_id = $1 AND id > $2 AND ($3 = '' OR kind = $3) ORDER BY id LIMIT $4`, scan, after, kind, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &ScanErrorPage{Items: []ScanError{}}
	for rows.Next() {
		var e ScanError
		if err := rows.Scan(&e.ID, &e.Path, &e.Kind, &e.Message, &e.At); err != nil {
			return nil, err
		}
		page.Items = append(page.Items, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1].ID
		page.NextAfter = &last
	}
	return page, nil
}

// CategoryTotal is the size of one category inside some scope.
type CategoryTotal struct {
	ID    int16   `json:"id"`
	Key   string  `json:"key"`
	Size  int64   `json:"size"`
	Alloc int64   `json:"alloc"`
	Files int64   `json:"files"`
	Pct   float64 `json:"pct"` // share of the scope's total size, 0..100
}

func pct(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) * 100 / float64(whole)
}

// Categories returns the category breakdown of a directory (0 = share root),
// largest first. Categories without files are omitted.
func (q *DB) Categories(ctx context.Context, share, dir int64) ([]CategoryTotal, error) {
	m, err := q.share(ctx, share)
	if err != nil {
		return nil, err
	}
	if dir == 0 {
		dir = m.RootID
	}
	if _, err := q.dirRef(ctx, share, dir); err != nil {
		return nil, err
	}
	rows, err := q.pool.Query(ctx, `
		SELECT c.id, c.key, a.size_total, a.alloc_total, a.files_total
		FROM dir_category_agg a JOIN categories c ON c.id = a.category_id
		WHERE a.share_id = $1 AND a.dir_id = $2 ORDER BY a.size_total DESC, c.id`, share, dir)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CategoryTotal{}
	var total int64
	for rows.Next() {
		var c CategoryTotal
		if err := rows.Scan(&c.ID, &c.Key, &c.Size, &c.Alloc, &c.Files); err != nil {
			return nil, err
		}
		total += c.Size
		out = append(out, c)
	}
	for i := range out {
		out[i].Pct = pct(out[i].Size, total)
	}
	return out, rows.Err()
}

// ExtensionTotal is the size of one extension across a share.
type ExtensionTotal struct {
	Ext        string  `json:"ext"` // "" = files without extension
	CategoryID int16   `json:"category_id"`
	Size       int64   `json:"size"`
	Alloc      int64   `json:"alloc"`
	Files      int64   `json:"files"`
	Pct        float64 `json:"pct"`
}

// Extensions lists extension totals of a share, largest first. categories
// restricts the result to those categories (current extension_map).
func (q *DB) Extensions(ctx context.Context, share int64, categories []int16, limit int) ([]ExtensionTotal, error) {
	limit, err := clampLimit(limit)
	if err != nil {
		return nil, err
	}
	m, err := q.share(ctx, share)
	if err != nil {
		return nil, err
	}
	var total int64
	if err := q.pool.QueryRow(ctx, `SELECT size_total FROM dirs WHERE share_id = $1 AND id = $2`, share, m.RootID).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := q.pool.Query(ctx, `
		SELECT e.ext, coalesce(x.category_id, 8::smallint), e.size, e.alloc, e.files
		FROM share_ext_agg e LEFT JOIN extension_map x ON x.ext = e.ext
		WHERE e.share_id = $1 AND (coalesce(cardinality($2::smallint[]), 0) = 0 OR coalesce(x.category_id, 8::smallint) = ANY($2))
		ORDER BY e.size DESC, e.ext LIMIT $3`, share, categories, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExtensionTotal{}
	for rows.Next() {
		var e ExtensionTotal
		if err := rows.Scan(&e.Ext, &e.CategoryID, &e.Size, &e.Alloc, &e.Files); err != nil {
			return nil, err
		}
		e.Pct = pct(e.Size, total)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Category is a row of the categories table.
type Category struct {
	ID  int16  `json:"id"`
	Key string `json:"key"`
}

// CategoryList returns all categories.
func (q *DB) CategoryList(ctx context.Context) ([]Category, error) {
	rows, err := q.pool.Query(ctx, `SELECT id, key FROM categories ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Key); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ExtensionMapping is a row of extension_map.
type ExtensionMapping struct {
	Ext        string `json:"ext"`
	CategoryID int16  `json:"category_id"`
}

// ExtensionMap returns the extension -> category table.
func (q *DB) ExtensionMap(ctx context.Context) ([]ExtensionMapping, error) {
	rows, err := q.pool.Query(ctx, `SELECT ext, category_id FROM extension_map ORDER BY ext`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExtensionMapping{}
	for rows.Next() {
		var e ExtensionMapping
		if err := rows.Scan(&e.Ext, &e.CategoryID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
