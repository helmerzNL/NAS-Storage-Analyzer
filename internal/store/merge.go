package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// mergeScan applies the staged rows to the share's tables. $1 = share, $2 = scan.
func mergeScan(ctx context.Context, tx pgx.Tx, share, scan int64) error {
	steps := []struct{ name, sql string }{
		{"upsert visited dirs", `
			INSERT INTO dirs (share_id, id, parent_id, name, depth, path, mtime, entry_count, last_seen_scan)
			SELECT $1, id, parent_id, name, depth, path::ltree, mtime, entry_count, $2 FROM stage_dirs WHERE NOT failed
			ON CONFLICT (share_id, id) DO UPDATE SET
				parent_id = EXCLUDED.parent_id, name = EXCLUDED.name, depth = EXCLUDED.depth, path = EXCLUDED.path,
				mtime = EXCLUDED.mtime, entry_count = EXCLUDED.entry_count, last_seen_scan = EXCLUDED.last_seen_scan`},
		// An unreadable directory keeps its stored data, and so does its subtree
		// (its children were not visited this time).
		{"insert unreadable new dirs", `
			INSERT INTO dirs (share_id, id, parent_id, name, depth, path, mtime, entry_count, last_seen_scan)
			SELECT $1, id, parent_id, name, depth, path::ltree, mtime, entry_count, $2 FROM stage_dirs WHERE failed
			ON CONFLICT (share_id, id) DO NOTHING`},
		{"keep subtree of unreadable dirs", `
			UPDATE dirs d SET last_seen_scan = $2
			FROM stage_dirs s JOIN dirs f ON f.share_id = $1 AND f.id = s.id
			WHERE s.failed AND d.share_id = $1 AND d.path <@ f.path AND d.last_seen_scan < $2`},
		{"replace files of read dirs", `
			DELETE FROM files f USING stage_dirs s
			WHERE f.share_id = $1 AND f.dir_id = s.id AND NOT s.reused AND NOT s.failed`},
		{"insert files", `
			INSERT INTO files (share_id, dir_id, name, ext, category_id, size, alloc, mtime, atime, ctime, nlink, dev, ino)
			SELECT $1, dir_id, name, ext, category_id, size, alloc, mtime, atime, ctime, nlink, dev, ino FROM stage_files`},
		{"list vanished dirs", `
			CREATE TEMP TABLE gone ON COMMIT DROP AS
			SELECT id FROM dirs WHERE share_id = $1 AND last_seen_scan < $2`},
		{"delete vanished files", `DELETE FROM files f USING gone g WHERE f.share_id = $1 AND f.dir_id = g.id`},
		{"delete vanished category aggregates", `DELETE FROM dir_category_agg a USING gone g WHERE a.share_id = $1 AND a.dir_id = g.id`},
		{"delete vanished dirs", `DELETE FROM dirs d USING gone g WHERE d.share_id = $1 AND d.id = g.id`},
	}
	for _, s := range steps {
		args := []any{share}
		if containsParam2(s.sql) {
			args = append(args, scan)
		}
		if _, err := tx.Exec(ctx, s.sql, args...); err != nil {
			return fmt.Errorf("merge: %s: %w", s.name, err)
		}
	}
	return nil
}

func containsParam2(sql string) bool {
	for i := 0; i+1 < len(sql); i++ {
		if sql[i] == '$' && sql[i+1] == '2' {
			return true
		}
	}
	return false
}

// aggregate recomputes everything derived from files: hardlink dedup, direct
// per-directory sums (only where files changed), recursive totals (always,
// bottom-up level by level) and the per-extension table.
//
// With all set every directory counts as affected, which makes the result
// independent of any earlier incremental state (recategorize, repair, seeding).
func aggregate(ctx context.Context, tx pgx.Tx, share int64, all bool) error {
	exec := func(name, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, append([]any{share}, args...)...); err != nil {
			return fmt.Errorf("aggregate: %s: %w", name, err)
		}
		return nil
	}

	// Hardlinks: the first file (lowest dir_id, id) of each (dev, ino) group is
	// summed, the others are listed but not counted.
	if err := exec("hardlink dedup", `
		UPDATE files f SET counted = (r.rn = 1)
		FROM (SELECT id, row_number() OVER (PARTITION BY dev, ino ORDER BY dir_id, id) AS rn
		      FROM files WHERE share_id = $1 AND nlink > 1) r
		WHERE f.share_id = $1 AND f.id = r.id AND f.counted IS DISTINCT FROM (r.rn = 1)`); err != nil {
		return err
	}

	// Directories whose direct numbers may have changed: every directory that
	// was read this scan plus every directory holding a hardlinked file.
	affectedSQL := `
		CREATE TEMP TABLE affected ON COMMIT DROP AS
			SELECT id FROM stage_dirs WHERE NOT reused AND NOT failed
			UNION SELECT dir_id FROM files WHERE share_id = $1 AND nlink > 1`
	if all {
		affectedSQL = `CREATE TEMP TABLE affected ON COMMIT DROP AS SELECT id FROM dirs WHERE share_id = $1`
	}
	if err := exec("affected dirs", affectedSQL); err != nil {
		return err
	}
	if err := exec("direct sums", `
		UPDATE dirs d SET size_direct = a.s, alloc_direct = a.al, files_direct = a.c,
			d_oldest_mtime = a.omt, d_newest_mtime = a.nmt, d_oldest_atime = a.oat, d_newest_atime = a.nat
		FROM (SELECT af.id,
		             coalesce(sum(f.size)  FILTER (WHERE f.counted), 0) AS s,
		             coalesce(sum(f.alloc) FILTER (WHERE f.counted), 0) AS al,
		             count(f.id)           FILTER (WHERE f.counted)     AS c,
		             min(f.mtime) AS omt, max(f.mtime) AS nmt, min(f.atime) AS oat, max(f.atime) AS nat
		      FROM affected af LEFT JOIN files f ON f.share_id = $1 AND f.dir_id = af.id
		      GROUP BY af.id) a
		WHERE d.share_id = $1 AND d.id = a.id`); err != nil {
		return err
	}
	if err := exec("reset direct subdir counts", `UPDATE dirs SET dirs_direct = 0 WHERE share_id = $1 AND dirs_direct <> 0`); err != nil {
		return err
	}
	if err := exec("direct subdir counts", `
		UPDATE dirs d SET dirs_direct = c.n
		FROM (SELECT parent_id, count(*) AS n FROM dirs WHERE share_id = $1 AND parent_id IS NOT NULL GROUP BY parent_id) c
		WHERE d.share_id = $1 AND d.id = c.parent_id`); err != nil {
		return err
	}

	// Direct per-directory-per-category rows for the affected directories.
	if err := exec("category direct: clear", `
		DELETE FROM dir_category_agg a USING affected af WHERE a.share_id = $1 AND a.dir_id = af.id`); err != nil {
		return err
	}
	if err := exec("category direct: insert", `
		INSERT INTO dir_category_agg (share_id, dir_id, category_id, size_direct, alloc_direct, files_direct)
		SELECT $1, f.dir_id, f.category_id, sum(f.size), sum(f.alloc), count(*)
		FROM files f JOIN affected af ON af.id = f.dir_id
		WHERE f.share_id = $1 AND f.counted GROUP BY f.dir_id, f.category_id`); err != nil {
		return err
	}

	// Recursive totals, deepest level first. Totals start at the direct values
	// and each level adds its children's (already final) totals.
	if err := exec("reset dir totals", `
		UPDATE dirs SET size_total = size_direct, alloc_total = alloc_direct, files_total = files_direct, dirs_total = 0,
			oldest_mtime = d_oldest_mtime, newest_mtime = d_newest_mtime,
			oldest_atime = d_oldest_atime, newest_atime = d_newest_atime
		WHERE share_id = $1`); err != nil {
		return err
	}
	if err := exec("reset category totals", `
		UPDATE dir_category_agg SET size_total = size_direct, alloc_total = alloc_direct, files_total = files_direct
		WHERE share_id = $1`); err != nil {
		return err
	}
	var maxDepth int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(depth), 0) FROM dirs WHERE share_id = $1`, share).Scan(&maxDepth); err != nil {
		return err
	}
	for parent := maxDepth - 1; parent >= 0; parent-- {
		if err := exec("dir totals level", `
			UPDATE dirs p SET size_total = p.size_total + c.s, alloc_total = p.alloc_total + c.al,
				files_total = p.files_total + c.f, dirs_total = p.dirs_total + c.dc,
				oldest_mtime = LEAST(p.oldest_mtime, c.omt), newest_mtime = GREATEST(p.newest_mtime, c.nmt),
				oldest_atime = LEAST(p.oldest_atime, c.oat), newest_atime = GREATEST(p.newest_atime, c.nat)
			FROM (SELECT parent_id, sum(size_total) AS s, sum(alloc_total) AS al, sum(files_total) AS f,
			             sum(dirs_total + 1) AS dc, min(oldest_mtime) AS omt, max(newest_mtime) AS nmt,
			             min(oldest_atime) AS oat, max(newest_atime) AS nat
			      FROM dirs WHERE share_id = $1 AND depth = $2 + 1 GROUP BY parent_id) c
			WHERE p.share_id = $1 AND p.id = c.parent_id`, parent); err != nil {
			return err
		}
		if err := exec("category totals level", `
			INSERT INTO dir_category_agg AS t (share_id, dir_id, category_id, size_total, alloc_total, files_total)
			SELECT $1, d.parent_id, a.category_id, sum(a.size_total), sum(a.alloc_total), sum(a.files_total)
			FROM dir_category_agg a JOIN dirs d ON d.share_id = $1 AND d.id = a.dir_id
			WHERE a.share_id = $1 AND d.depth = $2 + 1 GROUP BY d.parent_id, a.category_id
			ON CONFLICT (share_id, dir_id, category_id) DO UPDATE SET
				size_total = t.size_total + EXCLUDED.size_total, alloc_total = t.alloc_total + EXCLUDED.alloc_total,
				files_total = t.files_total + EXCLUDED.files_total`, parent); err != nil {
			return err
		}
	}
	if err := exec("drop empty category rows", `DELETE FROM dir_category_agg WHERE share_id = $1 AND files_total = 0`); err != nil {
		return err
	}

	if err := exec("extension totals: clear", `DELETE FROM share_ext_agg WHERE share_id = $1`); err != nil {
		return err
	}
	return exec("extension totals: insert", `
		INSERT INTO share_ext_agg (share_id, ext, size, alloc, files)
		SELECT $1, ext, sum(size), sum(alloc), count(*) FROM files WHERE share_id = $1 AND counted GROUP BY ext`)
}
