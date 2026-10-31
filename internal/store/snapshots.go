package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// maxSnapshotDirs caps the directory rows kept per scan, so the history of a
// share with millions of directories cannot grow without bound.
const maxSnapshotDirs = 50_000

// writeSnapshot records the aggregates of the scan that was just published.
// It runs in the publishing transaction, so a published scan always has its
// snapshot. Directories are kept down to the share's snapshot_depth, and below
// depth 2 only those holding at least 0.1 % of the share.
func writeSnapshot(ctx context.Context, tx pgx.Tx, share, scan int64) error {
	steps := []struct{ name, sql string }{
		{"totals", `
			INSERT INTO snapshots (scan_id, share_id, taken_at, size, alloc, files, dirs)
			SELECT $2, $1, now(), size_total, alloc_total, files_total, 1 + dirs_total FROM dirs
			WHERE share_id = $1 AND parent_id IS NULL`},
		{"categories", `
			INSERT INTO snapshot_category (scan_id, category_id, size, files)
			SELECT $2, a.category_id, a.size_total, a.files_total FROM dir_category_agg a
			JOIN dirs r ON r.share_id = a.share_id AND r.id = a.dir_id AND r.parent_id IS NULL
			WHERE a.share_id = $1`},
		{"extensions", `
			INSERT INTO snapshot_ext (scan_id, ext, size, files) SELECT $2, ext, size, files FROM share_ext_agg WHERE share_id = $1`},
		{"directories", fmt.Sprintf(`
			WITH RECURSIVE t AS (
				SELECT id, ''::text AS p, depth, size_total, files_total FROM dirs WHERE share_id = $1 AND parent_id IS NULL
				UNION ALL
				SELECT d.id, t.p || '/' || d.name, d.depth, d.size_total, d.files_total FROM dirs d JOIN t ON d.parent_id = t.id
				WHERE d.share_id = $1 AND d.depth <= (SELECT snapshot_depth FROM shares WHERE id = $1)),
			root AS (SELECT size_total AS total FROM t WHERE depth = 0)
			INSERT INTO snapshot_dir (scan_id, dir_id, path, size, files)
			SELECT $2, id, p, size_total, files_total FROM t, root
			WHERE depth >= 1 AND (depth <= 2 OR size_total * 1000 >= root.total) ORDER BY size_total DESC LIMIT %d`, maxSnapshotDirs)},
	}
	for _, s := range steps {
		if _, err := tx.Exec(ctx, s.sql, share, scan); err != nil {
			return fmt.Errorf("snapshot %s: %w", s.name, err)
		}
	}
	return nil
}

// PruneHistory applies the share's retention: scans (and with them their
// snapshots and logged errors) beyond the newest retention_scans, or older
// than retention_days, are deleted. The published scan and running scans are
// never touched. It returns the number of scans removed.
func (s *Store) PruneHistory(ctx context.Context, shareID int64) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `
		WITH keep AS (SELECT retention_scans AS n, retention_days AS d, last_scan_id AS last FROM shares WHERE id = $1)
		DELETE FROM scans WHERE share_id = $1 AND status <> 'running'
			AND id <> coalesce((SELECT last FROM keep), 0)
			AND ((SELECT n FROM keep) > 0 AND id NOT IN (SELECT id FROM scans WHERE share_id = $1 ORDER BY id DESC LIMIT (SELECT n FROM keep))
			  OR (SELECT d FROM keep) > 0 AND started_at < now() - make_interval(days => (SELECT d FROM keep)))`, shareID)
	return tag.RowsAffected(), err
}

// ShouldForceFull says whether the next automatic scan of a share must be a
// full one: every Nth scan is (full_scan_every), which bounds how long changes
// that a directory's mtime cannot reveal (a file edited in place) stay unseen.
func (s *Store) ShouldForceFull(ctx context.Context, shareID int64) (bool, error) {
	var every, since int
	err := s.Pool.QueryRow(ctx, `
		SELECT sh.full_scan_every, (SELECT count(*) FROM scans c WHERE c.share_id = sh.id AND c.status = 'succeeded'
			AND c.id > coalesce((SELECT max(f.id) FROM scans f WHERE f.share_id = sh.id AND f.status = 'succeeded' AND f.mode = 'full'), 0))
		FROM shares sh WHERE sh.id = $1`, shareID).Scan(&every, &since)
	if err != nil {
		return false, err
	}
	return every > 0 && since >= every-1, nil
}
