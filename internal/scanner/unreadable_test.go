package scanner

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func folders(r UnreadableReport) string {
	var parts []string
	for _, f := range r.Folders {
		what := fmt.Sprintf("%d items", f.Items)
		if f.Self {
			what = "self"
		}
		parts = append(parts, fmt.Sprintf("%s (%s, %s)", f.Path, f.Kind, what))
	}
	return strings.Join(parts, "; ")
}

func TestUnreadableFoldersAreTheFoldersNotTheFiles(t *testing.T) {
	var p Progress
	set := &p.unreadable
	// A folder that could not be opened.
	set.add(ErrRec{Path: "locked", Kind: ErrPermission, Dir: true})
	// A folder that could be listed but whose files could not be examined: one entry for the folder, not one per file.
	set.add(ErrRec{Path: "noexec/a.txt", Kind: ErrPermission})
	set.add(ErrRec{Path: "noexec/b.txt", Kind: ErrPermission})
	set.add(ErrRec{Path: "noexec/c.txt", Kind: ErrIO})
	// A file in the top folder of the share, and one deeper down.
	set.add(ErrRec{Path: "top.txt", Kind: ErrVanished})
	set.add(ErrRec{Path: "a/b/c/deep.txt", Kind: ErrPermission})

	got := folders(p.Unreadable(0))
	want := "/ (vanished, 1 items); /a/b/c (permission, 1 items); /locked (permission, self); /noexec (permission, 3 items)"
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
	// Compared as whole values: a folder that could not be opened has no count of items, whatever came in for it.
	set.add(ErrRec{Path: "locked/x.txt", Kind: ErrIO})
	for _, f := range p.Unreadable(0).Folders {
		if f.Path == "/locked" && (f != UnreadableFolder{Path: "/locked", Kind: ErrPermission, Self: true, Items: 0}) {
			t.Errorf("an item of a folder that could not be opened must not count: %+v", f)
		}
	}
	if r := p.Unreadable(0); r.Total != 4 || r.Truncated {
		t.Errorf("total = %d, truncated = %v", r.Total, r.Truncated)
	}
}

func TestAFolderThatCannotBeOpenedSwallowsTheProblemsOfItsFiles(t *testing.T) {
	// The order the errors arrive in is not the order they happened in: workers report at the same time.
	for _, order := range [][]ErrRec{
		{{Path: "d/x", Kind: ErrIO}, {Path: "d", Kind: ErrPermission, Dir: true}},
		{{Path: "d", Kind: ErrPermission, Dir: true}, {Path: "d/x", Kind: ErrIO}},
	} {
		var p Progress
		for _, e := range order {
			p.unreadable.add(e)
		}
		if got, want := p.Unreadable(0).Folders, []UnreadableFolder{{Path: "/d", Kind: ErrPermission, Self: true, Items: 0}}; len(got) != 1 || got[0] != want[0] {
			t.Errorf("got %+v, want %+v", got, want)
		}
	}
}

func TestUnreadableListIsShortButTheTotalIsNot(t *testing.T) {
	var p Progress
	for i := 0; i < 120; i++ {
		p.unreadable.add(ErrRec{Path: fmt.Sprintf("d%03d", i), Kind: ErrPermission, Dir: true})
	}
	r := p.Unreadable(50)
	if len(r.Folders) != 50 || r.Total != 120 || r.Folders[0].Path != "/d000" || r.Folders[49].Path != "/d049" {
		t.Errorf("listed %d, total %d, first %q, last %q", len(r.Folders), r.Total, r.Folders[0].Path, r.Folders[len(r.Folders)-1].Path)
	}
	if all := p.Unreadable(0); len(all.Folders) != 120 {
		t.Errorf("0 means all: %d", len(all.Folders))
	}
}

func TestUnreadableTrackingIsBounded(t *testing.T) {
	var p Progress
	p.unreadable.add(ErrRec{Path: "aaa/first.txt", Kind: ErrPermission}) // tracked before the bound is reached
	for i := 0; i < maxTrackedUnreadable+500; i++ {
		p.unreadable.add(ErrRec{Path: fmt.Sprintf("d%d", i), Kind: ErrPermission, Dir: true})
	}
	r := p.Unreadable(0)
	if r.Total != maxTrackedUnreadable || !r.Truncated {
		t.Errorf("total = %d, truncated = %v, want %d and true", r.Total, r.Truncated, maxTrackedUnreadable)
	}
	// A folder that is already tracked keeps counting after the bound is reached; a new one is not tracked.
	p.unreadable.add(ErrRec{Path: "aaa/second.txt", Kind: ErrPermission})
	p.unreadable.add(ErrRec{Path: "zzz/new.txt", Kind: ErrPermission})
	r = p.Unreadable(0)
	if got := r.Folders[0]; got.Path != "/aaa" || got.Items != 2 {
		t.Errorf("a tracked folder must keep counting: %+v", got)
	}
	if r.Total != maxTrackedUnreadable {
		t.Errorf("a new folder past the bound must not be tracked: total %d", r.Total)
	}
}

func TestUnreadableIsSafeForTheWorkersThatReportAtTheSameTime(t *testing.T) {
	var p Progress
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				p.unreadable.add(ErrRec{Path: fmt.Sprintf("d%d/f%d", i%25, i), Kind: ErrPermission})
				_ = p.Unreadable(10)
			}
		}()
	}
	wg.Wait()
	r := p.Unreadable(0)
	items := 0
	for _, f := range r.Folders {
		items += f.Items
	}
	if r.Total != 25 || items != 8*500 {
		t.Errorf("total = %d, items = %d, want 25 and %d", r.Total, items, 8*500)
	}
}

// The walker reports into it: a folder that cannot be opened by itself, and the files of a folder that can be listed but
// not examined by their folder, with kinds as classified. This is the same run as the scan in production.
func TestWalkNamesTheUnreadableFolders(t *testing.T) {
	root := t.TempDir()
	write(t, root+"/ok/a.txt", 1)
	write(t, root+"/locked/b.txt", 1)
	write(t, root+"/locked/inner/c.txt", 1)
	write(t, root+"/noexec/d.txt", 1)
	write(t, root+"/noexec/e.txt", 1)
	write(t, root+"/noexec/f.txt", 1)
	write(t, root+"/vanish/gone.txt", 1)

	ffs := &faultFS{
		FS:      mustOSFS(t, root),
		openErr: map[string]error{"locked": &os.PathError{Op: "open", Path: "locked", Err: syscall.EACCES}},
		lstatErr: map[string]error{
			"noexec/d.txt": syscall.EACCES, "noexec/e.txt": syscall.EACCES, "vanish/gone.txt": syscall.ENOENT,
		},
	}
	o := walkOpts(t, ffs, &memSink{})
	o.Progress = &Progress{}
	if err := Walk(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	r := o.Progress.Unreadable(0)
	want := "/locked (permission, self); /noexec (permission, 2 items); /vanish (vanished, 1 items)"
	if got := folders(r); got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
	// The folder below the one that could not be opened was never visited, so it is not named: the folder above it is the thing to fix.
	for _, f := range r.Folders {
		if strings.HasPrefix(f.Path, "/locked/") {
			t.Errorf("a folder below an unreadable one must not be listed: %s", f.Path)
		}
	}
	if int(o.Progress.Errors.Load()) != 4 {
		t.Errorf("the error count still counts every error: %d", o.Progress.Errors.Load())
	}
}
