package scanrun

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/helmerznl/nas-storage-analyzer/internal/categorize"
	"github.com/helmerznl/nas-storage-analyzer/internal/scanner"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
)

var dbCounter atomic.Int64

// newStore creates a throwaway database on the server named by NASA_TEST_DSN,
// migrates it and drops it again when the test ends. Without NASA_TEST_DSN the
// test is skipped, unless NASA_REQUIRE_DB=1 turns that into a failure so CI
// cannot silently skip the integration tests.
func newStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("NASA_TEST_DSN")
	if dsn == "" {
		if os.Getenv("NASA_REQUIRE_DB") == "1" {
			t.Fatal("NASA_TEST_DSN is required when NASA_REQUIRE_DB=1")
		}
		t.Skip("set NASA_TEST_DSN to run database integration tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("nasa_it_%d_%d", os.Getpid(), dbCounter.Add(1))
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		admin.Exec(ctx, `DROP DATABASE `+name+` WITH (FORCE)`)
		admin.Close(ctx)
	})
	return st
}

func write(t *testing.T, p string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildTree creates a tree that exercises hardlinks, symlinks, excluded
// directories, special names and empty entries.
func buildTree(t *testing.T, root string) {
	t.Helper()
	write(t, root+"/a.mkv", 1000)
	write(t, root+"/movies/b.mp4", 5000)
	write(t, root+"/movies/c.MKV", 70000)
	write(t, root+"/music/d.flac", 300)
	write(t, root+"/music/sub/e.mp3", 4096)
	write(t, root+"/docs/f.pdf", 12)
	write(t, root+"/docs/noext", 7)
	write(t, root+"/docs/empty.txt", 0)
	write(t, root+"/photos/g.CR2", 10000)
	write(t, root+"/photos/h.jpg", 2000)
	if err := os.MkdirAll(root+"/empty", 0o755); err != nil {
		t.Fatal(err)
	}
	// Hardlinks: same directory and across directories.
	mustLink(t, root+"/a.mkv", root+"/a-link.mkv")
	if err := os.MkdirAll(root+"/photos/backup", 0o755); err != nil {
		t.Fatal(err)
	}
	mustLink(t, root+"/photos/h.jpg", root+"/photos/backup/h-copy.jpg")
	// Symlinks must neither be followed nor counted.
	_ = os.Symlink(root+"/movies/c.MKV", root+"/link-to-file")
	_ = os.Symlink(root+"/movies", root+"/link-to-dir")
	_ = os.Symlink(root, root+"/music/loop")
	// Excluded.
	write(t, root+"/.snapshot/big.iso", 10000)
	write(t, root+"/movies/@eaDir/thumb.jpg", 999)
	write(t, root+"/#recycle/x.txt", 5)
	write(t, root+"/.Trash-1000/y.txt", 5)
	// Special names.
	for _, n := range []string{"with space.txt", "new\nline.TXT", "ünï ☃.pdf", "-dash", "q'\"uote.md"} {
		write(t, root+"/special/"+n, 3)
	}
	_ = os.WriteFile(root+"/special/bad\xff.txt", []byte("zz"), 0o644)
}

func mustLink(t *testing.T, a, b string) {
	t.Helper()
	if err := os.Link(a, b); err != nil {
		t.Fatal(err)
	}
}

var testExcludes = []string{".snapshot", "@eaDir", "#recycle", ".Trash-*"}

// expectation is computed straight from the file system, independently of
// the scanner.
type expectation struct {
	dirs, files, bytes, alloc int64
	ext                       map[string][3]int64 // size, alloc, files
}

func fsExpect(t *testing.T, root string, excludes []string) expectation {
	t.Helper()
	e := expectation{dirs: 1, ext: map[string][3]int64{}}
	seen := map[[2]uint64]bool{}
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
	next:
		for _, en := range entries {
			for _, p := range excludes {
				if ok, _ := path.Match(p, en.Name()); ok {
					continue next
				}
			}
			full := filepath.Join(dir, en.Name())
			fi, err := os.Lstat(full)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case fi.IsDir():
				e.dirs++
				walk(full)
			case fi.Mode().IsRegular():
				st := fi.Sys().(*syscall.Stat_t)
				if st.Nlink > 1 {
					k := [2]uint64{uint64(st.Dev), uint64(st.Ino)}
					if seen[k] {
						continue
					}
					seen[k] = true
				}
				e.files++
				e.bytes += fi.Size()
				e.alloc += st.Blocks * 512
				x := e.ext[categorize.Ext(en.Name())]
				x[0] += fi.Size()
				x[1] += st.Blocks * 512
				x[2]++
				e.ext[categorize.Ext(en.Name())] = x
			}
		}
	}
	walk(root)
	return e
}

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

func settle() { time.Sleep(25 * time.Millisecond) } // let directory mtimes differ
