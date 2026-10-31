package query

import (
	"context"
	"time"
)

// HistoryPoint is the state of the share at the end of one scan.
type HistoryPoint struct {
	ScanID     int64           `json:"scan_id"`
	At         time.Time       `json:"at"`
	Size       int64           `json:"size"`
	Alloc      int64           `json:"alloc"`
	Files      int64           `json:"files"`
	Dirs       int64           `json:"dirs"`
	Categories []HistoryAmount `json:"categories"`
}

// HistoryAmount is a size within a history point.
type HistoryAmount struct {
	ID    int16 `json:"id"`
	Size  int64 `json:"size"`
	Files int64 `json:"files"`
}

// History returns the snapshots of a share, oldest first, newest 1000 at most.
// since limits it to snapshots at or after that time.
func (q *DB) History(ctx context.Context, share int64, since *time.Time) ([]HistoryPoint, error) {
	if _, err := q.ShareByID(ctx, share); err != nil {
		return nil, err
	}
	rows, err := q.pool.Query(ctx, `
		SELECT * FROM (SELECT scan_id, taken_at, size, alloc, files, dirs FROM snapshots
			WHERE share_id = $1 AND ($2::timestamptz IS NULL OR taken_at >= $2) ORDER BY taken_at DESC LIMIT 1000) s
		ORDER BY taken_at`, share, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryPoint{}
	idx := map[int64]int{}
	var ids []int64
	for rows.Next() {
		var p HistoryPoint
		if err := rows.Scan(&p.ScanID, &p.At, &p.Size, &p.Alloc, &p.Files, &p.Dirs); err != nil {
			return nil, err
		}
		p.Categories = []HistoryAmount{}
		idx[p.ScanID] = len(out)
		ids = append(ids, p.ScanID)
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	crows, err := q.pool.Query(ctx, `SELECT scan_id, category_id, size, files FROM snapshot_category WHERE scan_id = ANY($1) ORDER BY scan_id, category_id`, ids)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	for crows.Next() {
		var scan int64
		var a HistoryAmount
		if err := crows.Scan(&scan, &a.ID, &a.Size, &a.Files); err != nil {
			return nil, err
		}
		out[idx[scan]].Categories = append(out[idx[scan]].Categories, a)
	}
	return out, crows.Err()
}

// DirHistoryPoint is a folder's size at the end of one scan.
type DirHistoryPoint struct {
	ScanID int64     `json:"scan_id"`
	At     time.Time `json:"at"`
	Size   int64     `json:"size"`
	Files  int64     `json:"files"`
}

// DirHistory returns the size of one folder over time. Only large folders near
// the top of the tree are kept in snapshots; for others the list is empty.
func (q *DB) DirHistory(ctx context.Context, share, dir int64) ([]DirHistoryPoint, error) {
	m, err := q.share(ctx, share)
	if err != nil {
		return nil, err
	}
	if dir == 0 {
		dir = m.RootID
	}
	rows, err := q.pool.Query(ctx, `
		SELECT sd.scan_id, s.taken_at, sd.size, sd.files FROM snapshot_dir sd JOIN snapshots s ON s.scan_id = sd.scan_id
		WHERE s.share_id = $1 AND sd.dir_id = $2 ORDER BY s.taken_at`, share, dir)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DirHistoryPoint{}
	for rows.Next() {
		var p DirHistoryPoint
		if err := rows.Scan(&p.ScanID, &p.At, &p.Size, &p.Files); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if dir == m.RootID && len(out) == 0 { // the root is not in snapshot_dir; use the totals
		hist, err := q.History(ctx, share, nil)
		if err != nil {
			return nil, err
		}
		for _, h := range hist {
			out = append(out, DirHistoryPoint{ScanID: h.ScanID, At: h.At, Size: h.Size, Files: h.Files})
		}
	}
	return out, rows.Err()
}
