package scanrun

import (
	"context"
	"testing"

	"github.com/helmerznl/nas-storage-analyzer/internal/scanner"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
)

type dirTotals struct{ Size, Alloc, Files, Dirs int64 }

// dirTotalsByPath returns the stored recursive totals keyed by relative path.
func dirTotalsByPath(t *testing.T, st *store.Store, share int64) map[string]dirTotals {
	t.Helper()
	rows, err := st.Pool.Query(context.Background(), `
		WITH RECURSIVE t AS (
			SELECT id, name AS p, size_total, alloc_total, files_total, dirs_total FROM dirs
			WHERE share_id = $1 AND parent_id IS NULL
			UNION ALL
			SELECT d.id, CASE WHEN t.p = '' THEN d.name ELSE t.p || '/' || d.name END,
			       d.size_total, d.alloc_total, d.files_total, d.dirs_total
			FROM dirs d JOIN t ON d.parent_id = t.id WHERE d.share_id = $1)
		SELECT p, size_total, alloc_total, files_total, dirs_total FROM t`, share)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]dirTotals{}
	for rows.Next() {
		var p string
		var d dirTotals
		if err := rows.Scan(&p, &d.Size, &d.Alloc, &d.Files, &d.Dirs); err != nil {
			t.Fatal(err)
		}
		out[p] = d
	}
	return out
}

func extTotals(t *testing.T, st *store.Store, share int64) map[string][3]int64 {
	t.Helper()
	rows, err := st.Pool.Query(context.Background(), `SELECT ext, size, alloc, files FROM share_ext_agg WHERE share_id = $1`, share)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][3]int64{}
	for rows.Next() {
		var ext string
		var v [3]int64
		if err := rows.Scan(&ext, &v[0], &v[1], &v[2]); err != nil {
			t.Fatal(err)
		}
		out[ext] = v
	}
	return out
}

// checkConsistency verifies the stored aggregates against a differently shaped
// query: for every directory, sum the files below it via the ltree path.
func checkConsistency(t *testing.T, st *store.Store, share int64) {
	t.Helper()
	ctx := context.Background()
	rows, err := st.Pool.Query(ctx, `
		SELECT x.id, x.size_total, x.alloc_total, x.files_total, x.dirs_total, x.size_direct, x.files_direct,
		       (SELECT coalesce(sum(f.size) FILTER (WHERE f.counted), 0) FROM dirs s JOIN files f
		          ON f.share_id = $1 AND f.dir_id = s.id WHERE s.share_id = $1 AND s.path <@ x.path),
		       (SELECT coalesce(sum(f.alloc) FILTER (WHERE f.counted), 0) FROM dirs s JOIN files f
		          ON f.share_id = $1 AND f.dir_id = s.id WHERE s.share_id = $1 AND s.path <@ x.path),
		       (SELECT count(f.id) FILTER (WHERE f.counted) FROM dirs s JOIN files f
		          ON f.share_id = $1 AND f.dir_id = s.id WHERE s.share_id = $1 AND s.path <@ x.path),
		       (SELECT count(*) FROM dirs s WHERE s.share_id = $1 AND s.path <@ x.path AND s.id <> x.id),
		       (SELECT coalesce(sum(f.size) FILTER (WHERE f.counted), 0) FROM files f WHERE f.share_id = $1 AND f.dir_id = x.id),
		       (SELECT count(f.id) FILTER (WHERE f.counted) FROM files f WHERE f.share_id = $1 AND f.dir_id = x.id),
		       x.newest_mtime = (SELECT max(f.mtime) FROM dirs s JOIN files f
		          ON f.share_id = $1 AND f.dir_id = s.id WHERE s.share_id = $1 AND s.path <@ x.path)
		FROM dirs x WHERE x.share_id = $1`, share)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, size, alloc, files, dirs, sizeD, filesD, wSize, wAlloc, wFiles, wDirs, wSizeD, wFilesD int64
		var mtimeOK *bool
		if err := rows.Scan(&id, &size, &alloc, &files, &dirs, &sizeD, &filesD, &wSize, &wAlloc, &wFiles, &wDirs, &wSizeD, &wFilesD, &mtimeOK); err != nil {
			t.Fatal(err)
		}
		n++
		if size != wSize || alloc != wAlloc || files != wFiles || dirs != wDirs || sizeD != wSizeD || filesD != wFilesD {
			t.Errorf("dir %d: stored size/alloc/files/dirs/sizeD/filesD = %d/%d/%d/%d/%d/%d, recomputed %d/%d/%d/%d/%d/%d",
				id, size, alloc, files, dirs, sizeD, filesD, wSize, wAlloc, wFiles, wDirs, wSizeD, wFilesD)
		}
		if mtimeOK != nil && !*mtimeOK {
			t.Errorf("dir %d: newest_mtime does not match the newest file below it", id)
		}
	}
	if n == 0 {
		t.Error("no directories stored")
	}

	// Per-category totals against the same subtree sums.
	var bad int
	err = st.Pool.QueryRow(ctx, `
		SELECT count(*) FROM dir_category_agg a JOIN dirs x ON x.share_id = $1 AND x.id = a.dir_id
		WHERE a.share_id = $1 AND (a.size_total, a.files_total, a.alloc_total) IS DISTINCT FROM (
		  (SELECT coalesce(sum(f.size) FILTER (WHERE f.counted), 0) FROM dirs s JOIN files f
		     ON f.share_id = $1 AND f.dir_id = s.id AND f.category_id = a.category_id
		   WHERE s.share_id = $1 AND s.path <@ x.path),
		  (SELECT count(f.id) FILTER (WHERE f.counted) FROM dirs s JOIN files f
		     ON f.share_id = $1 AND f.dir_id = s.id AND f.category_id = a.category_id
		   WHERE s.share_id = $1 AND s.path <@ x.path),
		  (SELECT coalesce(sum(f.alloc) FILTER (WHERE f.counted), 0) FROM dirs s JOIN files f
		     ON f.share_id = $1 AND f.dir_id = s.id AND f.category_id = a.category_id
		   WHERE s.share_id = $1 AND s.path <@ x.path))`, share).Scan(&bad)
	if err != nil {
		t.Fatal(err)
	}
	if bad != 0 {
		t.Errorf("%d dir_category_agg rows disagree with the files table", bad)
	}
	// Every (dir, category) with files below it must have a row.
	var missing int
	err = st.Pool.QueryRow(ctx, `
		SELECT count(*) FROM (
		  SELECT DISTINCT x.id, f.category_id FROM dirs x
		  JOIN dirs s ON s.share_id = $1 AND s.path <@ x.path
		  JOIN files f ON f.share_id = $1 AND f.dir_id = s.id AND f.counted
		  WHERE x.share_id = $1) q
		WHERE NOT EXISTS (SELECT 1 FROM dir_category_agg a
		  WHERE a.share_id = $1 AND a.dir_id = q.id AND a.category_id = q.category_id)`, share).Scan(&missing)
	if err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Errorf("%d (dir, category) combinations lack an aggregate row", missing)
	}
}

// guardFS wraps an FS to deny directories and to run a hook on every open.
type guardFS struct {
	scanner.FS
	deny   map[string]error
	onOpen func(rel string)
}

func (g *guardFS) Open(rel string) (scanner.Dir, error) {
	if g.onOpen != nil {
		g.onOpen(rel)
	}
	if err := g.deny[rel]; err != nil {
		return nil, err
	}
	return g.FS.Open(rel)
}

func newShare(t *testing.T, st *store.Store, name, root string, excludes []string) *store.Share {
	t.Helper()
	sh, err := st.EnsureShare(context.Background(), name, root, excludes, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	return sh
}
