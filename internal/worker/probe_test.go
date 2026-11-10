package worker_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
	"github.com/helmerznl/nas-storage-analyzer/internal/worker"
)

// probe asks for a connection test of a share and waits for the worker's outcome.
func (e *env) probe(t *testing.T, shareID int64) *jobs.Probe {
	t.Helper()
	probes := e.q.Probes()
	asked, err := probes.Request(ctx, shareID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		p, err := probes.Latest(ctx, shareID)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil || p.ID != asked.ID {
			t.Fatalf("the latest test of share %d is %+v, asked for %d", shareID, p, asked.ID)
		}
		if p.State == jobs.ProbeOK || p.State == jobs.ProbeFailed {
			if p.StartedAt == nil || p.FinishedAt == nil {
				t.Errorf("a finished test has its times: %+v", p)
			}
			return p
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the connection test of share %d did not finish in time", shareID)
	return nil
}

func count(t *testing.T, e *env, table string) int {
	t.Helper()
	var n int
	if err := e.st.Pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAConnectionTestCountsTheTopFolderAndLeavesNoTrace(t *testing.T) {
	e := newEnv(t, nil, nil)
	root := t.TempDir()
	ts.Write(t, root+"/a.txt", 10)
	ts.Write(t, root+"/b.txt", 10)
	ts.Write(t, root+"/films/deep/c.mkv", 10) // one entry at the top, whatever is below it
	p := e.probe(t, e.share(t, root, nil))
	if p.State != jobs.ProbeOK || p.Error != nil || p.Entries == nil || *p.Entries != 3 || p.More {
		t.Fatalf("test = %+v (entries %v, error %v)", p, p.Entries, p.Error)
	}
	// A test is not a scan: nothing in the job queue, no scan, no index, no audit entry of the worker.
	for _, table := range []string{"jobs", "scans", "dirs", "files", "audit_log"} {
		if n := count(t, e, table); n != 0 {
			t.Errorf("the test left %d rows in %s", n, table)
		}
	}
}

// An empty top folder is what a wrong NASSA_SHARES_DIR looks like. The test succeeds and says zero, so the interface can warn.
func TestAConnectionTestOfAnEmptyFolderSaysZero(t *testing.T) {
	e := newEnv(t, nil, nil)
	p := e.probe(t, e.share(t, t.TempDir(), nil))
	if p.State != jobs.ProbeOK || p.Entries == nil || *p.Entries != 0 || p.More {
		t.Fatalf("test = %+v (entries %v)", p, p.Entries)
	}
}

func TestAConnectionTestStopsCountingAtItsLimit(t *testing.T) {
	e := newEnv(t, nil, nil)
	root := t.TempDir()
	for i := 0; i < 1000; i++ {
		ts.Write(t, fmt.Sprintf("%s/f%04d", root, i), 0)
	}
	id := e.share(t, root, nil)
	if p := e.probe(t, id); p.State != jobs.ProbeOK || *p.Entries != 1000 || p.More {
		t.Fatalf("exactly the limit: %+v (entries %v)", p, p.Entries)
	}
	ts.Write(t, root+"/one-more", 0)
	if p := e.probe(t, id); p.State != jobs.ProbeOK || *p.Entries != 1000 || !p.More {
		t.Fatalf("one past the limit: %+v (entries %v)", p, p.Entries)
	}
}

func TestAConnectionTestOfAMissingPathFails(t *testing.T) {
	e := newEnv(t, nil, nil)
	p := e.probe(t, e.share(t, "/nonexistent/path", nil))
	if p.State != jobs.ProbeFailed || p.Error == nil || !strings.Contains(*p.Error, "mounted into the container") || p.Entries != nil {
		t.Fatalf("test = %+v (error %v)", p, p.Error)
	}
}

func TestAConnectionTestOfAFolderTheAppMayNotReadSaysWhichUser(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	e := newEnv(t, nil, nil)
	root := t.TempDir()
	ts.Write(t, root+"/a.txt", 10)
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(root, 0o755) })
	p := e.probe(t, e.share(t, root, nil))
	if p.State != jobs.ProbeFailed || p.Error == nil || !strings.Contains(*p.Error, "permission denied") || !strings.Contains(*p.Error, shares.PermissionHint()) {
		t.Fatalf("test = %+v (error %v)", p, p.Error)
	}
}

// recordingRunner notes what the mount manager runs, and succeeds: the mount point then stays the empty folder it is.
type recordingRunner struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return "", nil
}

func (r *recordingRunner) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func nfsShare(in *shares.Input) {
	in.Kind, in.Path, in.Server, in.Export = shares.KindNFS, "", "nas.lan", "/volume1/media"
}

// The mount point of a test is its own. On the mount point of a scan, clearing it first would pull the share away from under a
// scan of the same share that is running.
func TestAConnectionTestMountsOnItsOwnMountPointAndUnmounts(t *testing.T) {
	r := &recordingRunner{}
	e := newEnv(t, nil, r)
	id := e.share(t, "", nfsShare)
	p := e.probe(t, id)
	if p.State != jobs.ProbeOK || *p.Entries != 0 {
		t.Fatalf("test = %+v (error %v)", p, p.Error)
	}
	calls := r.all()
	own := string(filepath.Separator) + fmt.Sprintf("probe-%d", id)
	var mounted, unmountedAfter bool
	for _, c := range calls {
		if !strings.HasSuffix(c, own) {
			t.Errorf("the test ran %q, which is not about its own mount point", c)
		}
		switch {
		case strings.HasPrefix(c, "mount "):
			if !strings.Contains(c, "-o ro,") || !strings.Contains(c, "nas.lan:/volume1/media") {
				t.Errorf("mount command = %q", c)
			}
			mounted = true
		case strings.HasPrefix(c, "umount ") && mounted:
			unmountedAfter = true
		}
	}
	if !mounted || !unmountedAfter {
		t.Errorf("the test must mount and then unmount: %q", calls)
	}
}

func TestAConnectionTestReportsWhyTheMountFailed(t *testing.T) {
	e := newEnv(t, nil, failingRunner{out: "mount.nfs: Connection timed out"})
	p := e.probe(t, e.share(t, "", nfsShare))
	if p.State != jobs.ProbeFailed || p.Error == nil || !strings.Contains(*p.Error, "Connection timed out") {
		t.Fatalf("test = %+v (error %v)", p, p.Error)
	}
	// The capability failure of mount.cifs, as reported from a Home Assistant host, is named in the test as it is in a scan.
	e = newEnv(t, nil, failingRunner{out: "Unable to apply new capability set."})
	p = e.probe(t, e.share(t, "", func(in *shares.Input) {
		in.Kind, in.Path, in.Server, in.Share = shares.KindSMB, "", "nas.lan", "media"
	}))
	if p.State != jobs.ProbeFailed || p.Error == nil || !strings.Contains(*p.Error, "CAP_DAC_READ_SEARCH") {
		t.Fatalf("test = %+v (error %v)", p, p.Error)
	}
}

// A test answers in seconds; a scan takes hours. With every slot taken by a scan the test still runs, on the same share too, and
// the scan goes on.
func TestAConnectionTestDoesNotWaitForAFreeSlot(t *testing.T) {
	e := newEnv(t, func(c *worker.Config) { c.Parallel = 1 }, nil)
	root := t.TempDir()
	for i := 0; i < 300; i++ {
		ts.Write(t, fmt.Sprintf("%s/bulk/f%03d.txt", root, i), 1)
	}
	id := e.share(t, root, func(in *shares.Input) { in.MaxOpsPerSec = 20 }) // far longer than the test
	j, err := e.q.Enqueue(ctx, jobs.KindScan, id, "full", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(t, j.ID, jobs.StateRunning)
	p := e.probe(t, id)
	if p.State != jobs.ProbeOK || *p.Entries != 1 {
		t.Fatalf("test = %+v (error %v)", p, p.Error)
	}
	if still := e.waitJob(t, j.ID, jobs.StateRunning, jobs.StateDone, jobs.StateFailed, jobs.StateCancelled); still.State != jobs.StateRunning {
		t.Fatalf("the scan ended %s while the test ran: %v", still.State, still.Error)
	}
	if ok, err := e.q.Cancel(ctx, id, jobs.KindScan); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	e.waitJob(t, j.ID, jobs.StateCancelled)
}

// Stopping the worker in the middle of a test closes the test with the reason, not with a timeout that blames the share.
func TestAConnectionTestInterruptedByAShutdownSaysSo(t *testing.T) {
	block := make(chan struct{})
	r := blockingRunner{entered: make(chan struct{}, 1), release: block}
	e := newEnv(t, nil, r)
	id := e.share(t, "", nfsShare)
	probes := e.q.Probes()
	asked, err := probes.Request(ctx, id, "admin")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the worker did not start mounting")
	}
	e.stop()
	close(block)
	p, err := probes.Latest(ctx, id)
	if err != nil || p == nil || p.ID != asked.ID {
		t.Fatalf("latest = %+v, %v", p, err)
	}
	if p.State != jobs.ProbeFailed || p.Error == nil || !strings.Contains(*p.Error, "worker was stopped") {
		t.Fatalf("test = %+v (error %v)", p, p.Error)
	}
}

// blockingRunner hangs in "mount" until the context ends, as a mount of a server that does not answer does.
type blockingRunner struct {
	entered chan struct{}
	release chan struct{}
}

func (b blockingRunner) Run(ctx context.Context, name string, _ ...string) (string, error) {
	if name != "mount" {
		return "", nil
	}
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-b.release:
		return "", nil
	}
}
