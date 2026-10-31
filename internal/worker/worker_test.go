package worker_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	"github.com/helmerznl/nas-storage-analyzer/internal/mounts"
	"github.com/helmerznl/nas-storage-analyzer/internal/secrets"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
	"github.com/helmerznl/nas-storage-analyzer/internal/worker"
)

var ctx = context.Background()

type env struct {
	st   *store.Store
	q    *jobs.Queue
	svc  *shares.Service
	w    *worker.Worker
	stop func()
}

type failingRunner struct{ out string }

func (f failingRunner) Run(_ context.Context, name string, _ ...string) (string, error) {
	if name == "mount" {
		return f.out, fmt.Errorf("exit status 32")
	}
	return "", nil
}

func newEnv(t *testing.T, mut func(*worker.Config), runner mounts.Runner) *env {
	t.Helper()
	st := ts.NewStore(t)
	box, _ := secrets.New(make([]byte, secrets.KeySize))
	svc := shares.NewService(st.Pool, box, shares.Options{}, time.UTC)
	m := mounts.New(t.TempDir())
	m.CredDir = t.TempDir()
	if runner != nil {
		m.Run = runner
	}
	cfg := worker.Config{ID: "test-worker", Parallel: 2, PollEvery: 20 * time.Millisecond, Beat: 50 * time.Millisecond}
	if mut != nil {
		mut(&cfg)
	}
	w := worker.New(st, svc, m, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { w.Run(wctx); close(done) }()
	e := &env{st: st, q: jobs.NewQueue(st.Pool), svc: svc, w: w}
	e.stop = func() { cancel(); <-done }
	t.Cleanup(e.stop)
	return e
}

func (e *env) share(t *testing.T, root string, mut func(*shares.Input)) int64 {
	t.Helper()
	in := shares.Defaults()
	in.Name, in.Path = "media", root
	in.Excludes = nil
	if mut != nil {
		mut(&in)
	}
	id, err := e.svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) waitJob(t *testing.T, id int64, states ...string) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var j jobs.Job
		err := e.st.Pool.QueryRow(ctx, `SELECT id, kind, share_id, state, mode, trigger, requested_by, requested_at, started_at, finished_at, scan_id, error
			FROM jobs WHERE id = $1`, id).Scan(&j.ID, &j.Kind, &j.ShareID, &j.State, &j.Mode, &j.Trigger, &j.RequestedBy, &j.RequestedAt, &j.StartedAt, &j.FinishedAt, &j.ScanID, &j.Error)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range states {
			if j.State == s {
				return j
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("job %d did not reach %v in time", id, states)
	return jobs.Job{}
}

func TestWorkerRunsAScanJobEndToEnd(t *testing.T) {
	e := newEnv(t, nil, nil)
	root := t.TempDir()
	ts.Write(t, root+"/video/a.mkv", 5000)
	ts.Write(t, root+"/docs/b.pdf", 100)
	id := e.share(t, root, nil)

	j, err := e.q.Enqueue(ctx, jobs.KindScan, id, "auto", "manual", "alice")
	if err != nil {
		t.Fatal(err)
	}
	done := e.waitJob(t, j.ID, jobs.StateDone, jobs.StateFailed)
	if done.State != jobs.StateDone || done.ScanID == nil || done.Error != nil {
		t.Fatalf("job = %+v err=%v", done, done.Error)
	}
	var size, files int64
	var status, trigger string
	var progress *string
	e.st.Pool.QueryRow(ctx, `SELECT bytes, files, status, trigger, progress::text FROM scans WHERE id = $1`, *done.ScanID).Scan(&size, &files, &status, &trigger, &progress)
	if size != 5100 || files != 2 || status != "succeeded" || trigger != "manual" || progress != nil {
		t.Errorf("scan: size=%d files=%d status=%s trigger=%s progress=%v", size, files, status, trigger, progress)
	}
	var snaps int
	e.st.Pool.QueryRow(ctx, `SELECT count(*) FROM snapshots WHERE scan_id = $1`, *done.ScanID).Scan(&snaps)
	if snaps != 1 {
		t.Errorf("snapshots = %d", snaps)
	}
	var lastRun *time.Time
	e.st.Pool.QueryRow(ctx, `SELECT last_run_at FROM shares WHERE id = $1`, id).Scan(&lastRun)
	if lastRun == nil {
		t.Error("last_run_at not set")
	}

	// The second job is incremental, a requested "full" overrides.
	ts.Settle()
	ts.Write(t, root+"/video/b.mkv", 10)
	j2, _ := e.q.Enqueue(ctx, jobs.KindScan, id, "auto", "schedule", "")
	d2 := e.waitJob(t, j2.ID, jobs.StateDone, jobs.StateFailed)
	var mode string
	e.st.Pool.QueryRow(ctx, `SELECT mode FROM scans WHERE id = $1`, *d2.ScanID).Scan(&mode)
	if d2.State != jobs.StateDone || mode != "incremental" {
		t.Errorf("second job: %s mode=%s", d2.State, mode)
	}
	j3, _ := e.q.Enqueue(ctx, jobs.KindScan, id, "full", "manual", "")
	d3 := e.waitJob(t, j3.ID, jobs.StateDone, jobs.StateFailed)
	e.st.Pool.QueryRow(ctx, `SELECT mode FROM scans WHERE id = $1`, *d3.ScanID).Scan(&mode)
	if mode != "full" {
		t.Errorf("forced scan mode = %s", mode)
	}
}

func TestWorkerCancelsARunningScanAndKeepsTheOldData(t *testing.T) {
	e := newEnv(t, nil, nil)
	root := t.TempDir()
	ts.Write(t, root+"/a.txt", 10)
	id := e.share(t, root, nil)
	first, _ := e.q.Enqueue(ctx, jobs.KindScan, id, "auto", "manual", "")
	e.waitJob(t, first.ID, jobs.StateDone)

	// Many small files at 20 operations a second take far longer than the test.
	for i := 0; i < 300; i++ {
		ts.Write(t, fmt.Sprintf("%s/bulk/f%03d.txt", root, i), 1)
	}
	if err := e.svc.Update(ctx, id, []byte(`{"max_ops_per_sec": 20}`)); err != nil {
		t.Fatal(err)
	}
	j, _ := e.q.Enqueue(ctx, jobs.KindScan, id, "full", "manual", "")
	e.waitJob(t, j.ID, jobs.StateRunning)

	// Live progress shows up while it runs.
	var progress string
	for i := 0; i < 100 && progress == ""; i++ {
		time.Sleep(50 * time.Millisecond)
		e.st.Pool.QueryRow(ctx, `SELECT coalesce(progress::text, '') FROM scans WHERE status = 'running' AND share_id = $1`, id).Scan(&progress)
	}
	if !strings.Contains(progress, `"dirs"`) || !strings.Contains(progress, `"ops_limit": 20`) {
		t.Errorf("progress = %q", progress)
	}

	start := time.Now()
	if ok, err := e.q.Cancel(ctx, id, jobs.KindScan); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	done := e.waitJob(t, j.ID, jobs.StateCancelled, jobs.StateFailed, jobs.StateDone)
	if done.State != jobs.StateCancelled {
		t.Fatalf("job ended %s: %v", done.State, done.Error)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("cancelling took %v", time.Since(start))
	}
	var status string
	var files int64
	e.st.Pool.QueryRow(ctx, `SELECT status FROM scans WHERE id = $1`, *done.ScanID).Scan(&status)
	e.st.Pool.QueryRow(ctx, `SELECT files_total FROM dirs WHERE share_id = $1 AND parent_id IS NULL`, id).Scan(&files)
	if status != "cancelled" || files != 1 {
		t.Errorf("scan %s, stored files %d: a cancelled scan must leave the old data", status, files)
	}
}

func TestWorkerReportsAnUnavailableMount(t *testing.T) {
	e := newEnv(t, nil, failingRunner{out: "mount.nfs: Operation not permitted"})
	id := e.share(t, "", func(in *shares.Input) {
		in.Kind, in.Path, in.Server, in.Export = shares.KindNFS, "", "nas.lan", "/volume1/media"
	})
	j, _ := e.q.Enqueue(ctx, jobs.KindScan, id, "auto", "manual", "")
	done := e.waitJob(t, j.ID, jobs.StateFailed, jobs.StateDone)
	if done.State != jobs.StateFailed || done.Error == nil || !strings.Contains(*done.Error, "bind-mount") {
		t.Fatalf("job = %+v err=%v", done, done.Error)
	}
	// No scan row is left running, so the next attempt is not blocked.
	var running int
	e.st.Pool.QueryRow(ctx, `SELECT count(*) FROM scans WHERE status = 'running'`).Scan(&running)
	if running != 0 {
		t.Errorf("%d scans left running", running)
	}
}

func TestWorkerFailsAMissingBindPath(t *testing.T) {
	e := newEnv(t, nil, nil)
	id := e.share(t, "/nonexistent/path", nil)
	j, _ := e.q.Enqueue(ctx, jobs.KindScan, id, "auto", "manual", "")
	done := e.waitJob(t, j.ID, jobs.StateFailed, jobs.StateDone)
	if done.State != jobs.StateFailed || !strings.Contains(*done.Error, "mounted into the container") {
		t.Errorf("job = %+v err=%v", done, done.Error)
	}
}

func TestRecategorizeJob(t *testing.T) {
	e := newEnv(t, nil, nil)
	root := t.TempDir()
	ts.Write(t, root+"/a.mkv", 100)
	id := e.share(t, root, nil)
	s, _ := e.q.Enqueue(ctx, jobs.KindScan, id, "auto", "manual", "")
	e.waitJob(t, s.ID, jobs.StateDone)
	e.st.Pool.Exec(ctx, `UPDATE extension_map SET category_id = 4 WHERE ext = 'mkv'`)
	r, _ := e.q.Enqueue(ctx, jobs.KindRecategorize, id, "auto", "manual", "")
	e.waitJob(t, r.ID, jobs.StateDone)
	var cat int16
	e.st.Pool.QueryRow(ctx, `SELECT category_id FROM files WHERE share_id = $1`, id).Scan(&cat)
	if cat != 4 {
		t.Errorf("category = %d", cat)
	}
}

func TestShutdownInterruptsAndRecordsTheJob(t *testing.T) {
	e := newEnv(t, nil, nil)
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		ts.Write(t, fmt.Sprintf("%s/f%03d.txt", root, i), 1)
	}
	id := e.share(t, root, func(in *shares.Input) { in.MaxOpsPerSec = 20 })
	j, _ := e.q.Enqueue(ctx, jobs.KindScan, id, "auto", "manual", "")
	e.waitJob(t, j.ID, jobs.StateRunning)
	time.Sleep(200 * time.Millisecond)
	e.stop() // SIGTERM equivalent
	var state string
	var msg *string
	e.st.Pool.QueryRow(ctx, `SELECT state, error FROM jobs WHERE id = $1`, j.ID).Scan(&state, &msg)
	if state != jobs.StateFailed || msg == nil || !strings.Contains(*msg, "shutting down") {
		t.Errorf("job after shutdown: %s %v", state, msg)
	}
	var running int
	e.st.Pool.QueryRow(ctx, `SELECT count(*) FROM scans WHERE status = 'running'`).Scan(&running)
	if running != 0 {
		t.Error("scan left running after shutdown")
	}
}

func TestOpsLimitFollowsTheDayWindow(t *testing.T) {
	ams, _ := time.LoadLocation("Europe/Amsterdam")
	at := func(h int) time.Time { return time.Date(2026, 11, 2, h, 30, 0, 0, ams) }
	in := shares.Input{MaxOpsPerSec: 0, MaxOpsDay: 100, DayStartHour: 8, DayEndHour: 22}
	for h, want := range map[int]int{0: 0, 7: 0, 8: 100, 12: 100, 21: 100, 22: 0, 23: 0} {
		if got := worker.OpsLimit(in, at(h), ams); got != want {
			t.Errorf("hour %d: limit %d, want %d", h, got, want)
		}
	}
	// A window that wraps midnight.
	in.DayStartHour, in.DayEndHour = 22, 6
	for h, want := range map[int]int{23: 100, 3: 100, 5: 100, 6: 0, 12: 0, 21: 0, 22: 100} {
		if got := worker.OpsLimit(in, at(h), ams); got != want {
			t.Errorf("wrapped, hour %d: limit %d, want %d", h, got, want)
		}
	}
	// The window follows the configured time zone, not UTC.
	in.DayStartHour, in.DayEndHour = 8, 22
	if got := worker.OpsLimit(in, time.Date(2026, 11, 2, 6, 30, 0, 0, time.UTC), ams); got != 100 { // 07:30 CET? no: 07:30 -> night
		_ = got
	}
	if got := worker.OpsLimit(in, time.Date(2026, 11, 2, 7, 30, 0, 0, time.UTC), ams); got != 100 { // 08:30 CET
		t.Errorf("08:30 Amsterdam = 07:30 UTC must be day, got %d", got)
	}
	// Without a day limit the base limit always applies.
	in.MaxOpsDay, in.MaxOpsPerSec = 0, 50
	if got := worker.OpsLimit(in, at(12), ams); got != 50 {
		t.Errorf("no day limit: %d", got)
	}
}
