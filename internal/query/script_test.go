package query_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/query"
	"github.com/helmerznl/nas-storage-analyzer/internal/scanrun"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

// scriptFixture scans a tree that has the names a delete script has to be careful with.
func scriptFixture(t *testing.T) (f *fixture, idOf map[string]int64) {
	t.Helper()
	ctx := context.Background()
	st := ts.NewStore(t)
	root := t.TempDir()
	for p, size := range map[string]int{
		"plain.txt":         10,
		"Foo.txt":           20, // a case twin of foo.txt
		"foo.txt":           30,
		"Mixed/x.txt":       40, // the folder has a case twin: everything below is ambiguous
		"mixed/y.txt":       50,
		"Bar":               60, // a file and a folder whose names differ only in case: not a twin, see dirTwins
		"bar/z.txt":         70,
		"deep/a/b/c.txt":    80,
		"deep/a/b/other.md": 90,
		"unicode/é.txt":     100, // alone in its folder under that name: no twin
		"unicode/ÉCOLE.txt": 110,
		"unicode/école.txt": 120, // a twin of ÉCOLE.txt: only a Unicode-aware lower() sees it
	} {
		ts.Write(t, root+"/"+p, size)
	}
	if err := os.Link(root+"/plain.txt", root+"/deep/plain-link.txt"); err != nil {
		t.Fatal(err)
	}
	mt := time.Date(2020, 5, 6, 7, 8, 9, 0, time.UTC)
	if err := os.Chtimes(root+"/plain.txt", mt, mt); err != nil {
		t.Fatal(err)
	}
	sh, err := st.EnsureShare(ctx, "script", root, nil, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanrun.Run(ctx, st, sh, scanrun.Options{}); err != nil {
		t.Fatal(err)
	}
	f = &fixture{st: st, q: query.New(st.Pool), share: sh.ID, root: root}
	page, err := f.q.Files(ctx, query.FileQuery{ShareID: sh.ID, Recursive: true, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	idOf = map[string]int64{}
	for _, it := range page.Items {
		idOf[it.Path] = it.ID
	}
	return f, idOf
}

func TestFilesForScriptTellsTheFilesAndTheirTwins(t *testing.T) {
	f, id := scriptFixture(t)
	ctx := context.Background()

	ask := []string{"/deep/a/b/c.txt", "/plain.txt", "/Foo.txt", "/foo.txt", "/Mixed/x.txt", "/mixed/y.txt", "/Bar", "/bar/z.txt",
		"/unicode/é.txt", "/unicode/ÉCOLE.txt", "/unicode/école.txt", "/deep/a/b/other.md"}
	var ids []int64
	for _, p := range ask {
		if id[p] == 0 {
			t.Fatalf("%s is not in the index", p)
		}
		ids = append(ids, id[p])
	}
	sel, err := f.q.FilesForScript(ctx, f.share, ids)
	if err != nil {
		t.Fatal(err)
	}
	if sel.ScanID == 0 || sel.ScanTime.IsZero() || len(sel.Missing) != 0 || len(sel.Files) != len(ask) {
		t.Fatalf("selection = %+v", sel)
	}
	twin := map[string]bool{
		"/Foo.txt": true, "/foo.txt": true, "/Mixed/x.txt": true, "/mixed/y.txt": true,
		"/unicode/ÉCOLE.txt": true, "/unicode/école.txt": true,
	}
	for i, got := range sel.Files {
		if got.Path != ask[i] || got.ID != ids[i] {
			t.Errorf("file %d = %s (%d), want %s (%d): the order of the ids is kept", i, got.Path, got.ID, ask[i], ids[i])
		}
		if got.Twin != twin[got.Path] {
			t.Errorf("%s: Twin = %v, want %v", got.Path, got.Twin, twin[got.Path])
		}
	}
	plain := sel.Files[1]
	if plain.Size != 10 || plain.Nlink != 2 || !plain.Mtime.Equal(time.Date(2020, 5, 6, 7, 8, 9, 0, time.UTC)) {
		t.Errorf("plain.txt = %+v", plain)
	}
}

func TestFilesForScriptSeparatesWhatIsNotThere(t *testing.T) {
	f, id := scriptFixture(t)
	ctx := context.Background()

	// A file of another share must not come back, and must not be told apart from an id that does not exist.
	otherRoot := t.TempDir()
	ts.Write(t, otherRoot+"/secret-name.txt", 5)
	other, err := f.st.EnsureShare(ctx, "other", otherRoot, nil, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanrun.Run(ctx, f.st, other, scanrun.Options{}); err != nil {
		t.Fatal(err)
	}
	oq := query.New(f.st.Pool)
	op, err := oq.Files(ctx, query.FileQuery{ShareID: other.ID, Recursive: true, Limit: 10})
	if err != nil || len(op.Items) != 1 {
		t.Fatalf("other share: %v %+v", err, op)
	}
	foreign := op.Items[0].ID

	sel, err := f.q.FilesForScript(ctx, f.share, []int64{id["/plain.txt"], foreign, 987654321, id["/plain.txt"], id["/Foo.txt"]})
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Files) != 2 || sel.Files[0].Path != "/plain.txt" || sel.Files[1].Path != "/Foo.txt" {
		t.Errorf("files = %+v, want plain.txt once and Foo.txt", sel.Files)
	}
	if len(sel.Missing) != 2 || sel.Missing[0] != foreign || sel.Missing[1] != 987654321 {
		t.Errorf("missing = %v, want the foreign id and the unknown one", sel.Missing)
	}

	none, err := f.q.FilesForScript(ctx, f.share, []int64{foreign})
	if err != nil || len(none.Files) != 0 || len(none.Missing) != 1 {
		t.Errorf("only a foreign id: %v %+v", err, none)
	}
	empty, err := f.q.FilesForScript(ctx, f.share, nil)
	if err != nil || empty.ScanID == 0 || len(empty.Files) != 0 {
		t.Errorf("no ids: %v %+v", err, empty)
	}
}

func TestFilesForScriptErrors(t *testing.T) {
	f, id := scriptFixture(t)
	ctx := context.Background()
	if _, err := f.q.FilesForScript(ctx, 987654, []int64{1}); !errors.Is(err, query.ErrNotFound) {
		t.Errorf("unknown share: %v", err)
	}
	for _, bad := range [][]int64{{0}, {-5}, {id["/plain.txt"], 0}} {
		if _, err := f.q.FilesForScript(ctx, f.share, bad); !isInvalid(err) {
			t.Errorf("ids %v: err = %v, want InvalidError", bad, err)
		}
	}
	unscanned, err := f.st.EnsureShare(ctx, "unscanned", t.TempDir(), nil, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.FilesForScript(ctx, unscanned.ID, []int64{1}); !errors.Is(err, query.ErrNoData) {
		t.Errorf("share without a scan: %v", err)
	}
}
