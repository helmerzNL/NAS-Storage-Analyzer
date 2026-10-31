// Package testsupport holds helpers shared by the database integration tests.
package testsupport

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
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
)

var dbCounter atomic.Int64

// NewEmptyDB creates a throwaway database on the server named by NASA_TEST_DSN and
// returns its connection string. It is dropped again when the test ends. Without
// NASA_TEST_DSN the test is skipped, unless NASA_REQUIRE_DB=1 turns that into a failure
// so CI cannot silently skip the integration tests.
func NewEmptyDB(t *testing.T) string {
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
	t.Cleanup(func() {
		admin.Exec(ctx, `DROP DATABASE `+name+` WITH (FORCE)`)
		admin.Close(ctx)
	})
	return u.String()
}

// NewStore creates a throwaway database (see NewEmptyDB) and migrates it.
func NewStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, NewEmptyDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close) // runs before the database is dropped: cleanups run last to first
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

func Write(t *testing.T, p string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// BuildTree creates a tree that exercises hardlinks, symlinks, excluded
// directories, special names and empty entries.
func BuildTree(t *testing.T, root string) {
	t.Helper()
	Write(t, root+"/a.mkv", 1000)
	Write(t, root+"/movies/b.mp4", 5000)
	Write(t, root+"/movies/c.MKV", 70000)
	Write(t, root+"/music/d.flac", 300)
	Write(t, root+"/music/sub/e.mp3", 4096)
	Write(t, root+"/docs/f.pdf", 12)
	Write(t, root+"/docs/noext", 7)
	Write(t, root+"/docs/empty.txt", 0)
	Write(t, root+"/photos/g.CR2", 10000)
	Write(t, root+"/photos/h.jpg", 2000)
	if err := os.MkdirAll(root+"/empty", 0o755); err != nil {
		t.Fatal(err)
	}
	// Hardlinks: same directory and across directories.
	MustLink(t, root+"/a.mkv", root+"/a-link.mkv")
	if err := os.MkdirAll(root+"/photos/backup", 0o755); err != nil {
		t.Fatal(err)
	}
	MustLink(t, root+"/photos/h.jpg", root+"/photos/backup/h-copy.jpg")
	// Symlinks must neither be followed nor counted.
	_ = os.Symlink(root+"/movies/c.MKV", root+"/link-to-file")
	_ = os.Symlink(root+"/movies", root+"/link-to-dir")
	_ = os.Symlink(root, root+"/music/loop")
	// Excluded.
	Write(t, root+"/.snapshot/big.iso", 10000)
	Write(t, root+"/movies/@eaDir/thumb.jpg", 999)
	Write(t, root+"/#recycle/x.txt", 5)
	Write(t, root+"/.Trash-1000/y.txt", 5)
	// Special names.
	for _, n := range []string{"with space.txt", "new\nline.TXT", "ünï ☃.pdf", "-dash", "q'\"uote.md"} {
		Write(t, root+"/special/"+n, 3)
	}
	_ = os.WriteFile(root+"/special/bad\xff.txt", []byte("zz"), 0o644)
}

func MustLink(t *testing.T, a, b string) {
	t.Helper()
	if err := os.Link(a, b); err != nil {
		t.Fatal(err)
	}
}

var TestExcludes = []string{".snapshot", "@eaDir", "#recycle", ".Trash-*"}

// Expectation is computed straight from the file system, independently of
// the scanner.
type Expectation struct {
	Dirs, Files, Bytes, Alloc int64
	Ext                       map[string][3]int64 // size, alloc, files
}

func FSExpect(t *testing.T, root string, excludes []string) Expectation {
	t.Helper()
	e := Expectation{Dirs: 1, Ext: map[string][3]int64{}}
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
				e.Dirs++
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
				e.Files++
				e.Bytes += fi.Size()
				e.Alloc += st.Blocks * 512
				x := e.Ext[categorize.Ext(en.Name())]
				x[0] += fi.Size()
				x[1] += st.Blocks * 512
				x[2]++
				e.Ext[categorize.Ext(en.Name())] = x
			}
		}
	}
	walk(root)
	return e
}

// Settle waits long enough for directory mtimes to differ between two scans.
func Settle() { time.Sleep(25 * time.Millisecond) }

// DBHandle wraps a Store so tests of other packages can share a helper type.
type DBHandle struct{ Store *store.Store }
