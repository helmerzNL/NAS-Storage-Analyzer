package query_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/helmerznl/nas-storage-analyzer/internal/query"
	"github.com/helmerznl/nas-storage-analyzer/internal/scanrun"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

// dirFixture scans a tree with a folder that holds hard links, folders whose names differ only in case, and a folder below one of them.
func dirFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st := ts.NewStore(t)
	root := t.TempDir()
	for p, size := range map[string]int{
		"keep/a.txt":            10,
		"proj/a.txt":            5,
		"proj/sub/b.txt":        7,
		"proj/sub/deeper/c.txt": 9,
		"proj/dup1.txt":         20,
		"Caps/x.txt":            1,
		"caps/y.txt":            2,
		"Caps/inner/z.txt":      3,
	} {
		ts.Write(t, root+"/"+p, size)
	}
	for _, d := range []string{"proj/empty", "other"} {
		if err := os.MkdirAll(root+"/"+d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// One file with three names: two in proj, one outside it. The index sums it once, in whichever folder it meets first.
	for _, l := range []string{"proj/sub/dup2.txt", "other/dup3.txt"} {
		if err := os.Link(root+"/proj/dup1.txt", root+"/"+l); err != nil {
			t.Fatal(err)
		}
	}
	sh, err := st.EnsureShare(ctx, "dirs", root, nil, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanrun.Run(ctx, st, sh, scanrun.Options{}); err != nil {
		t.Fatal(err)
	}
	return &fixture{st: st, q: query.New(st.Pool), share: sh.ID, root: root}
}

// dirID finds a folder by its name, which the fixture keeps unique where it matters.
func dirID(t *testing.T, f *fixture, name string) int64 {
	t.Helper()
	var id int64
	if err := f.st.Pool.QueryRow(context.Background(), `SELECT id FROM dirs WHERE share_id = $1 AND name = $2 ORDER BY id LIMIT 1`, f.share, name).Scan(&id); err != nil {
		t.Fatalf("folder %q: %v", name, err)
	}
	return id
}

func TestDirsForScriptCountsEverythingBelowAFolder(t *testing.T) {
	f := dirFixture(t)
	sel, err := f.q.DirsForScript(context.Background(), f.share, []int64{dirID(t, f, "proj"), dirID(t, f, "sub"), dirID(t, f, "empty"), dirID(t, f, "keep")})
	if err != nil {
		t.Fatal(err)
	}
	if sel.ScanID == 0 || sel.ScanTime.IsZero() || len(sel.Missing) != 0 || len(sel.Dirs) != 4 {
		t.Fatalf("selection = %+v", sel)
	}
	by := map[string]query.ScriptDir{}
	for _, d := range sel.Dirs {
		by[d.Path] = d
	}
	// proj holds a, b, c, dup1 and dup2: five names, although the index sums the hard-linked file once, and in whichever folder it met it first.
	// What the script finds when it counts is five files, so five is what it must be told.
	if p := by["/proj"]; p.Files != 5 || p.Dirs != 3 || (p.Bytes != 21 && p.Bytes != 41) || p.Root || p.Twin {
		t.Errorf("proj = %+v, want 5 files and 3 folders (sub, deeper, empty)", p)
	}
	if s := by["/proj/sub"]; s.Files != 3 || s.Dirs != 1 {
		t.Errorf("proj/sub = %+v, want b, c and dup2 and one folder", s)
	}
	if e := by["/proj/empty"]; e.Files != 0 || e.Dirs != 0 || e.Bytes != 0 {
		t.Errorf("an empty folder = %+v", e)
	}
	if k := by["/keep"]; k.Files != 1 || k.Dirs != 0 || k.Bytes != 10 {
		t.Errorf("keep = %+v", k)
	}
	// The order of the ids is kept.
	if sel.Dirs[0].Path != "/proj" || sel.Dirs[3].Path != "/keep" {
		t.Errorf("order: %s ... %s", sel.Dirs[0].Path, sel.Dirs[3].Path)
	}
}

func TestDirsForScriptKnowsTheShareItselfAndCaseTwins(t *testing.T) {
	f := dirFixture(t)
	ctx := context.Background()
	var rootID int64
	if err := f.st.Pool.QueryRow(ctx, `SELECT id FROM dirs WHERE share_id = $1 AND parent_id IS NULL`, f.share).Scan(&rootID); err != nil {
		t.Fatal(err)
	}
	sel, err := f.q.DirsForScript(ctx, f.share, []int64{rootID, dirID(t, f, "Caps"), dirID(t, f, "caps"), dirID(t, f, "inner"), dirID(t, f, "proj")})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]query.ScriptDir{}
	for _, d := range sel.Dirs {
		got[d.Path] = d
	}
	if r := got[""]; !r.Root {
		t.Errorf("the share's own folder must say so, or a script could be made for it: %+v", sel.Dirs)
	}
	for _, p := range []string{"/Caps", "/caps", "/Caps/inner"} {
		if !got[p].Twin {
			t.Errorf("%s must be ambiguous: a client that ignores case cannot tell /Caps and /caps apart (%+v)", p, got[p])
		}
	}
	if got["/proj"].Twin || got["/proj"].Root {
		t.Errorf("proj has no twin: %+v", got["/proj"])
	}
}

func TestDirsForScriptSeparatesWhatIsNotThere(t *testing.T) {
	f := dirFixture(t)
	ctx := context.Background()
	other := t.TempDir()
	ts.Write(t, other+"/secret-folder/x.txt", 5)
	osh, err := f.st.EnsureShare(ctx, "other", other, nil, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanrun.Run(ctx, f.st, osh, scanrun.Options{}); err != nil {
		t.Fatal(err)
	}
	var foreign int64
	if err := f.st.Pool.QueryRow(ctx, `SELECT id FROM dirs WHERE share_id = $1 AND name = 'secret-folder'`, osh.ID).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	proj := dirID(t, f, "proj")
	sel, err := f.q.DirsForScript(ctx, f.share, []int64{proj, foreign, 987654321, proj})
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Dirs) != 1 || sel.Dirs[0].Path != "/proj" {
		t.Errorf("dirs = %+v", sel.Dirs)
	}
	// Another share's folder is not told apart from one that does not exist, and no name comes back.
	if len(sel.Missing) != 2 || sel.Missing[0] != foreign || sel.Missing[1] != 987654321 {
		t.Errorf("missing = %v", sel.Missing)
	}
	if only, err := f.q.DirsForScript(ctx, f.share, []int64{foreign}); err != nil || len(only.Dirs) != 0 || len(only.Missing) != 1 {
		t.Errorf("only a foreign id: %v %+v", err, only)
	}
	if none, err := f.q.DirsForScript(ctx, f.share, nil); err != nil || none.ScanID == 0 || len(none.Dirs) != 0 {
		t.Errorf("no ids: %v %+v", err, none)
	}
}

func TestDirsForScriptErrors(t *testing.T) {
	f := dirFixture(t)
	ctx := context.Background()
	if _, err := f.q.DirsForScript(ctx, 987654, []int64{1}); !errors.Is(err, query.ErrNotFound) {
		t.Errorf("unknown share: %v", err)
	}
	for _, bad := range [][]int64{{0}, {-5}, {dirID(t, f, "proj"), 0}} {
		if _, err := f.q.DirsForScript(ctx, f.share, bad); !isInvalid(err) {
			t.Errorf("ids %v: err = %v, want an invalid request", bad, err)
		}
	}
	// A share that was never scanned has no index to make a script from.
	empty, err := f.st.EnsureShare(ctx, "never-scanned", t.TempDir(), nil, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.DirsForScript(ctx, empty.ID, []int64{1}); !errors.Is(err, query.ErrNoData) {
		t.Errorf("no scan: %v", err)
	}
}
