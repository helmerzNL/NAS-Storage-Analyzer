package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func write(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWalkBasic(t *testing.T) {
	root := t.TempDir()
	write(t, root+"/a.mkv", 1000)
	write(t, root+"/sub/b.FLAC", 200)
	write(t, root+"/sub/deep/c", 3)
	write(t, root+"/.snapshot/hidden.bin", 99)
	write(t, root+"/sub/@eaDir/thumb.jpg", 99)
	write(t, root+"/.Trash-1000/x.txt", 5)
	write(t, root+"/target.txt", 10)
	if err := os.Symlink(root+"/target.txt", root+"/link-file"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, root+"/sub/link-loop"); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(root+"/a.mkv", root+"/sub/a-hardlink.mkv"); err != nil {
		t.Fatal(err)
	}
	special := []string{"with space.txt", "new\nline.txt", "ünïcødé ☃.pdf", "-dash.txt", "semi;colon&.txt", "quote'\".txt"}
	for _, n := range special {
		write(t, root+"/special/"+n, 1)
	}
	// Invalid UTF-8 in a name.
	if err := os.WriteFile(root+"/special/bad\xff\xfe.txt", []byte("x"), 0o644); err != nil {
		t.Skip("file system rejects invalid UTF-8 names:", err)
	}

	ex, err := NewExcluder([]string{".snapshot", "@eaDir", ".Trash-*", "#recycle"})
	if err != nil {
		t.Fatal(err)
	}
	sink := &memSink{}
	o := walkOpts(t, mustOSFS(t, root), sink)
	o.Excludes = ex
	if err := Walk(context.Background(), o); err != nil {
		t.Fatal(err)
	}

	dirs := sink.byRel()
	for _, rel := range []string{"", "sub", "sub/deep", "special"} {
		if dirs[rel] == nil {
			t.Errorf("directory %q not visited", rel)
		}
	}
	for _, rel := range []string{".snapshot", "sub/@eaDir", ".Trash-1000"} {
		if dirs[rel] != nil {
			t.Errorf("excluded directory %q was visited", rel)
		}
	}
	if len(dirs) != 4 {
		t.Errorf("visited %d directories, want 4: %v", len(dirs), keys(dirs))
	}

	names := strings.Join(sink.fileNames(), "|")
	if strings.Contains(names, "link-file") || strings.Contains(names, "link-loop") {
		t.Errorf("symlinks must not be listed as files: %s", names)
	}
	for _, want := range []string{"new\nline.txt", "ünïcødé ☃.pdf", "bad\uFFFD.txt"} {
		if !strings.Contains(names, want) {
			t.Errorf("file %q missing from %q", want, names)
		}
	}

	root0 := dirs[""]
	var a *FileRec
	for i := range root0.Files {
		if root0.Files[i].Name == "a.mkv" {
			a = &root0.Files[i]
		}
	}
	if a == nil {
		t.Fatal("a.mkv missing")
	}
	if a.Size != 1000 || a.Ext != "mkv" || a.Alloc < 1000 || a.Alloc%512 != 0 {
		t.Errorf("a.mkv = %+v", *a)
	}
	if a.Nlink != 2 || a.Dev == 0 || a.Ino == 0 {
		t.Errorf("hardlink identity not recorded: %+v", *a)
	}
	for _, f := range dirs["sub/deep"].Files {
		if f.Ext != "" || f.Category != 8 {
			t.Errorf("file without extension should be Other: %+v", f)
		}
	}
	// Ltree paths: each directory's path is its parent's path plus its own id.
	sub := dirs["sub"]
	if want := dirs[""].LPath + "." + itoa(sub.ID); sub.LPath != want || sub.Depth != 1 {
		t.Errorf("sub lpath=%q depth=%d, want %q depth 1", sub.LPath, sub.Depth, want)
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestWalkCountsSymlinksAndSkipsThem(t *testing.T) {
	root := t.TempDir()
	write(t, root+"/real/f.txt", 7)
	_ = os.Symlink(root+"/real", root+"/dirlink")
	_ = os.Symlink("nonexistent", root+"/dangling")
	sink := &memSink{}
	o := walkOpts(t, mustOSFS(t, root), sink)
	o.Progress = &Progress{}
	if err := Walk(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if got := o.Progress.Symlinks.Load(); got != 2 {
		t.Errorf("symlinks = %d, want 2", got)
	}
	if got := len(sink.dirs); got != 2 {
		t.Errorf("visited %d dirs, want 2 (symlinked dir must not be followed)", got)
	}
}

func TestWalkVeryLongPath(t *testing.T) {
	root := t.TempDir()
	// 17 components of 250 bytes: ~4250 bytes, beyond PATH_MAX.
	comp := strings.Repeat("d", 250)
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	cur := fd
	for i := 0; i < 17; i++ {
		if err := unix.Mkdirat(cur, comp, 0o755); err != nil {
			t.Fatal(err)
		}
		next, err := unix.Openat(cur, comp, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			t.Fatal(err)
		}
		unix.Close(cur)
		cur = next
	}
	leaf, err := unix.Openat(cur, "leaf.txt", unix.O_CREAT|unix.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	unix.Write(leaf, []byte("hello"))
	unix.Close(leaf)
	unix.Close(cur)

	sink := &memSink{}
	if err := Walk(context.Background(), walkOpts(t, mustOSFS(t, root), sink)); err != nil {
		t.Fatal(err)
	}
	if len(sink.dirs) != 18 {
		t.Fatalf("visited %d dirs, want 18; errors: %v", len(sink.dirs), sink.errs)
	}
	if got := strings.Join(sink.fileNames(), ","); got != "leaf.txt" {
		t.Errorf("files = %q", got)
	}
	if len(sink.errs) != 0 {
		t.Errorf("unexpected errors: %v", sink.errs)
	}
}

func TestWalkContinuesAfterErrors(t *testing.T) {
	root := t.TempDir()
	write(t, root+"/ok/a.txt", 1)
	write(t, root+"/locked/b.txt", 1)
	write(t, root+"/locked/inner/c.txt", 1)
	write(t, root+"/vanish/gone.txt", 1)
	write(t, root+"/vanish/stays.txt", 1)
	write(t, root+"/io/d.txt", 1)

	ffs := &faultFS{
		FS:       mustOSFS(t, root),
		openErr:  map[string]error{"locked": &os.PathError{Op: "open", Path: "locked", Err: syscall.EACCES}, "io": syscall.EIO},
		lstatErr: map[string]error{"vanish/gone.txt": syscall.ENOENT},
	}
	sink := &memSink{}
	if err := Walk(context.Background(), walkOpts(t, ffs, sink)); err != nil {
		t.Fatalf("walk must survive recoverable errors: %v", err)
	}
	kinds := sink.errKinds()
	if kinds[ErrPermission] != 1 || kinds[ErrIO] != 1 || kinds[ErrVanished] != 1 {
		t.Errorf("error kinds = %v", kinds)
	}
	dirs := sink.byRel()
	if d := dirs["locked"]; d == nil || !d.Failed {
		t.Errorf("locked dir should be reported as failed: %+v", d)
	}
	if dirs["locked/inner"] != nil {
		t.Error("children of an unreadable directory must not be visited")
	}
	if got := strings.Join(sink.fileNames(), ","); got != "a.txt,stays.txt" {
		t.Errorf("files = %q", got)
	}
}

func TestWalkRealPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks; covered via faultFS")
	}
	root := t.TempDir()
	write(t, root+"/locked/x.txt", 1)
	write(t, root+"/ok/y.txt", 1)
	if err := os.Chmod(root+"/locked", 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(root+"/locked", 0o755) })
	sink := &memSink{}
	if err := Walk(context.Background(), walkOpts(t, mustOSFS(t, root), sink)); err != nil {
		t.Fatal(err)
	}
	if sink.errKinds()[ErrPermission] != 1 {
		t.Errorf("errors = %v", sink.errs)
	}
}

func TestWalkUnreadableRootIsFatal(t *testing.T) {
	root := t.TempDir()
	ffs := &faultFS{FS: mustOSFS(t, root), openErr: map[string]error{"": syscall.EACCES}}
	err := Walk(context.Background(), walkOpts(t, ffs, &memSink{}))
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("err = %v, want root unreadable", err)
	}
}

func TestWalkTimeoutOnHungDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root+"/hung/a.txt", 1)
	write(t, root+"/fine/b.txt", 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ffs := &faultFS{FS: mustOSFS(t, root), hang: map[string]chan struct{}{"hung": release}}
	sink := &memSink{}
	o := walkOpts(t, ffs, sink)
	o.DirTimeout = 150 * time.Millisecond
	within(t, 5*time.Second, func() {
		if err := Walk(context.Background(), o); err != nil {
			t.Error(err)
		}
	})
	if sink.errKinds()[ErrTimeout] != 1 {
		t.Errorf("errors = %v", sink.errs)
	}
	if got := strings.Join(sink.fileNames(), ","); got != "b.txt" {
		t.Errorf("files = %q", got)
	}
}

func TestThrottleDoesNotTriggerWatchdog(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 30; i++ {
		write(t, root+"/f"+string(rune('a'+i%26))+string(rune('a'+i/26))+".txt", 1)
	}
	sink := &memSink{}
	o := walkOpts(t, mustOSFS(t, root), sink)
	o.Throttle = NewThrottle(40) // ~0.75 s for 31 ops beyond the initial burst
	o.DirTimeout = 100 * time.Millisecond
	if err := Walk(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if len(sink.errs) != 0 {
		t.Errorf("throttle waits must not count as hung calls: %v", sink.errs)
	}
	if len(sink.fileNames()) != 30 {
		t.Errorf("got %d files", len(sink.fileNames()))
	}
}

func TestThrottleLimitsRate(t *testing.T) {
	th := NewThrottle(100)
	start := time.Now()
	if err := th.Wait(context.Background(), 100); err != nil { // burst
		t.Fatal(err)
	}
	if err := th.Wait(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el < 400*time.Millisecond {
		t.Errorf("50 extra ops at 100/s took only %v", el)
	}
	var none *Throttle
	if err := none.Wait(context.Background(), 1_000_000); err != nil {
		t.Fatal(err)
	}
}

func TestIncrementalReusesUnchangedDirectories(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		write(t, root+"/dir"+string(rune('a'+i))+"/one.txt", 10)
		write(t, root+"/dir"+string(rune('a'+i))+"/two.txt", 10)
	}
	first := &memSink{}
	if err := Walk(context.Background(), walkOpts(t, mustOSFS(t, root), first)); err != nil {
		t.Fatal(err)
	}
	firstIDs := map[string]int64{}
	for rel, d := range first.byRel() {
		firstIDs[rel] = d.ID
	}

	// Change one directory structurally (new file bumps its mtime and count).
	time.Sleep(20 * time.Millisecond)
	write(t, root+"/dirc/three.txt", 10)

	second := &memSink{}
	ffs := &faultFS{FS: mustOSFS(t, root)}
	o := walkOpts(t, ffs, second)
	o.Incremental = true
	o.Prior = priorFrom(first)
	o.Progress = &Progress{}
	if err := Walk(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	for rel, d := range second.byRel() {
		if d.ID != firstIDs[rel] {
			t.Errorf("directory %q changed id %d -> %d", rel, firstIDs[rel], d.ID)
		}
		wantReused := rel != "dirc"
		if rel == "" {
			wantReused = true // root entries are unchanged
		}
		if d.Reused != wantReused {
			t.Errorf("directory %q reused=%v, want %v", rel, d.Reused, wantReused)
		}
		if d.Reused && len(d.Files) != 0 {
			t.Errorf("reused directory %q must not carry files", rel)
		}
	}
	if got := ffs.lstats.Load(); got != 3 {
		t.Errorf("lstat calls = %d, want 3 (only the changed directory)", got)
	}
	if o.Progress.ReusedDirs.Load() != 5 {
		t.Errorf("reused dirs = %d, want 5", o.Progress.ReusedDirs.Load())
	}
}

func TestFullScanKeepsIdsButStatsEverything(t *testing.T) {
	root := t.TempDir()
	write(t, root+"/a/one.txt", 1)
	first := &memSink{}
	if err := Walk(context.Background(), walkOpts(t, mustOSFS(t, root), first)); err != nil {
		t.Fatal(err)
	}
	ffs := &faultFS{FS: mustOSFS(t, root)}
	second := &memSink{}
	o := walkOpts(t, ffs, second)
	o.Prior = priorFrom(first)
	o.Incremental = false
	if err := Walk(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if ffs.lstats.Load() != 1 {
		t.Errorf("full scan lstat calls = %d, want 1", ffs.lstats.Load())
	}
	for rel, d := range second.byRel() {
		if d.Reused {
			t.Errorf("%q reused in a full scan", rel)
		}
		if d.ID != first.byRel()[rel].ID {
			t.Errorf("%q id not stable", rel)
		}
	}
}

func TestInPlaceModificationIsNotSeenIncrementally(t *testing.T) {
	// Documents the known limitation: changing a file's content does not
	// change its directory's mtime, so only a full scan notices.
	root := t.TempDir()
	write(t, root+"/d/log.txt", 10)
	first := &memSink{}
	if err := Walk(context.Background(), walkOpts(t, mustOSFS(t, root), first)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(root+"/d/log.txt", make([]byte, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	inc := &memSink{}
	o := walkOpts(t, mustOSFS(t, root), inc)
	o.Incremental, o.Prior = true, priorFrom(first)
	if err := Walk(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if !inc.byRel()["d"].Reused {
		t.Fatal("in-place edit unexpectedly invalidated the directory")
	}
}

func TestWalkCancel(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 50; i++ {
		write(t, root+"/d"+string(rune('a'+i%26))+string(rune('a'+i/26))+"/f.txt", 1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ffs := &faultFS{FS: mustOSFS(t, root)}
	n := 0
	ffs.onOpen = func(string) {
		if n++; n == 5 {
			cancel()
		}
	}
	o := walkOpts(t, ffs, &memSink{})
	o.Workers = 1
	within(t, 5*time.Second, func() {
		if err := Walk(ctx, o); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

func TestExcluder(t *testing.T) {
	ex, err := NewExcluder([]string{".snapshot", "@eaDir", "#recycle", ".Trash-*", "media/tmp", "*/cache", " ", "/lead/"})
	if err != nil {
		t.Fatal(err)
	}
	yes := [][2]string{{".snapshot", ".snapshot"}, {"@eaDir", "a/b/@eaDir"}, {"#recycle", "#recycle"},
		{".Trash-1000", ".Trash-1000"}, {"tmp", "media/tmp"}, {"cache", "x/cache"}, {"lead", "lead"}}
	no := [][2]string{{"tmp", "other/tmp"}, {"snapshot", "snapshot"}, {"cache", "a/b/cache"}, {"Trash", "Trash"}}
	for _, c := range yes {
		if !ex.Match(c[0], c[1]) {
			t.Errorf("%v should match", c)
		}
	}
	for _, c := range no {
		if ex.Match(c[0], c[1]) {
			t.Errorf("%v should not match", c)
		}
	}
	if _, err := NewExcluder([]string{"[unclosed"}); err == nil {
		t.Error("invalid pattern must be rejected")
	}
	var nilEx *Excluder
	if nilEx.Match("x", "x") {
		t.Error("nil excluder matches nothing")
	}
}

func TestSanitizeAndClamp(t *testing.T) {
	if got := sanitizeName("a\xffb"); got != "a�b" {
		t.Errorf("got %q", got)
	}
	if got := sanitizeName("a\x00b"); got != "ab" {
		t.Errorf("got %q", got)
	}
	far := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := clampTime(far); got.Year() != 2200 {
		t.Errorf("got %v", got)
	}
	if got := clampTime(time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC)); got.Year() != 1900 {
		t.Errorf("got %v", got)
	}
	ts := time.Date(2024, 5, 6, 7, 8, 9, 123456789, time.UTC)
	if got := clampTime(ts); got.Nanosecond() != 123456000 {
		t.Errorf("not truncated to microseconds: %v", got.Nanosecond())
	}
}
