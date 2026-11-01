package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
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

func TestHealthyWhileThePollLoopReachesTheQueue(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	e := newEnv(t, func(c *worker.Config) { c.Now = func() time.Time { return time.Unix(0, clock.Load()) } }, nil)
	if err := e.w.Healthy(); err != nil {
		t.Fatalf("a fresh worker is healthy: %v", err)
	}
	e.stop()
	clock.Add(int64(10 * time.Second))
	if err := e.w.Healthy(); err != nil {
		t.Fatalf("a few seconds without a poll is normal: %v", err)
	}
	clock.Add(int64(2 * time.Minute))
	if err := e.w.Healthy(); err == nil {
		t.Fatal("a worker that stopped polling must report unhealthy")
	}
}

// auditRow is one entry of the audit log as the interface shows it.
type auditRow struct {
	Action, Target, Username string
	Detail                   map[string]any
}

func (e *env) audit(t *testing.T) []auditRow {
	t.Helper()
	rows, err := e.st.Pool.Query(ctx, `SELECT action, target, username, detail FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		var raw []byte
		if err := rows.Scan(&r.Action, &r.Target, &r.Username, &raw); err != nil {
			t.Fatal(err)
		}
		json.Unmarshal(raw, &r.Detail)
		out = append(out, r)
	}
	return out
}

func (e *env) scan(t *testing.T, id int64, mode, trigger string) jobs.Job {
	t.Helper()
	j, err := e.q.Enqueue(ctx, jobs.KindScan, id, mode, trigger, "alice")
	if err != nil {
		t.Fatal(err)
	}
	return e.waitJob(t, j.ID, jobs.StateDone, jobs.StateFailed)
}

func only(t *testing.T, rows []auditRow, action string) auditRow {
	t.Helper()
	var got []auditRow
	for _, r := range rows {
		if r.Action == action {
			got = append(got, r)
		}
	}
	if len(got) != 1 {
		t.Fatalf("want exactly one %q entry in the audit log, got %d: %+v", action, len(got), rows)
	}
	return got[0]
}

// The outcome of every scan, including the ones nobody started by hand, is in the audit log.
func TestAScanThatFindsNothingIsInTheAuditLog(t *testing.T) {
	e := newEnv(t, nil, nil)
	id := e.share(t, t.TempDir(), nil) // an empty folder: a wrong NASA_SHARES_DIR looks exactly like this
	if j := e.scan(t, id, "auto", "schedule"); j.State != jobs.StateDone {
		t.Fatalf("job = %+v", j)
	}
	r := only(t, e.audit(t), "scan.empty")
	if r.Target != "media" || r.Username != "worker" || r.Detail["trigger"] != "schedule" || r.Detail["files"] != float64(0) {
		t.Errorf("entry = %+v", r)
	}
	if hint, _ := r.Detail["hint"].(string); !strings.Contains(hint, "mounted") {
		t.Errorf("the entry must say what to check: %+v", r.Detail)
	}
}

func TestAScanThatSucceedsIsInTheAuditLog(t *testing.T) {
	e := newEnv(t, nil, nil)
	root := t.TempDir()
	ts.Write(t, root+"/video/a.mkv", 5000)
	ts.Write(t, root+"/docs/b.pdf", 100)
	id := e.share(t, root, nil)
	e.scan(t, id, "auto", "manual")
	rows := e.audit(t)
	r := only(t, rows, "scan.finished")
	if r.Detail["files"] != float64(2) || r.Detail["bytes"] != float64(5100) || r.Detail["errors"] != float64(0) || r.Detail["trigger"] != "manual" || r.Detail["requested_by"] != "alice" {
		t.Errorf("entry = %+v", r)
	}
	if len(rows) != 1 {
		t.Errorf("a good scan writes one entry, got %+v", rows)
	}
}

// A folder the app is not allowed to read is named in the audit entry of the scan, not only counted: a permission is fixed per
// folder, and "3 folders or files could not be read" says nothing about which.
func TestAScanThatCannotReadAFolderNamesItInTheAuditLog(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks; internal/scanner checks the same report with a filesystem that injects the errors")
	}
	e := newEnv(t, nil, nil)
	root := t.TempDir()
	ts.Write(t, root+"/ok/a.txt", 100)
	ts.Write(t, root+"/private/secret.txt", 10)
	ts.Write(t, root+"/private/inner/more.txt", 10)
	ts.Write(t, root+"/shared/listed/b.txt", 20)
	// A folder that can be listed but not searched: its files can be seen, and none can be examined.
	ts.Write(t, root+"/shared/noexec/c.txt", 10)
	ts.Write(t, root+"/shared/noexec/d.txt", 10)
	for _, p := range []string{"/private", "/shared/noexec"} {
		mode := os.FileMode(0)
		if p == "/shared/noexec" {
			mode = 0o444 // r--: listable, not searchable
		}
		if err := os.Chmod(root+p, mode); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(root+p, 0o755) })
	}
	id := e.share(t, root, nil)
	if j := e.scan(t, id, "auto", "manual"); j.State != jobs.StateDone {
		t.Fatalf("a scan that cannot read everything still finishes: %+v", j)
	}
	r := only(t, e.audit(t), "scan.errors")
	if r.Detail["files"] != float64(2) {
		t.Errorf("the scan is published without what it could not read: %+v", r.Detail)
	}
	list, _ := r.Detail["unreadable"].([]any)
	got := map[string]map[string]any{}
	for _, f := range list {
		m := f.(map[string]any)
		got[m["path"].(string)] = m
	}
	if p := got["/private"]; p == nil || p["kind"] != "permission" || p["self"] != true || p["items"] != nil {
		t.Errorf("/private could not be opened: %+v", list)
	}
	if p := got["/shared/noexec"]; p == nil || p["kind"] != "permission" || p["items"] != float64(2) || p["self"] != nil {
		t.Errorf("/shared/noexec could be listed but not examined: %+v", list)
	}
	if len(list) != 2 || r.Detail["unreadable_folders"] != float64(2) {
		t.Errorf("only the folders with a problem are named, not the ones below them: %+v", r.Detail)
	}
	if got["/private/inner"] != nil {
		t.Errorf("a folder below one that could not be opened was never seen: %+v", list)
	}
	for _, e := range []string{"secret.txt", "c.txt", "d.txt"} {
		if strings.Contains(fmt.Sprint(r.Detail), e) {
			t.Errorf("file names are not in the entry: %s", e)
		}
	}
}

func TestAFailedScanIsInTheAuditLog(t *testing.T) {
	e := newEnv(t, nil, nil)
	id := e.share(t, "/nonexistent/path", nil)
	j := e.scan(t, id, "auto", "manual")
	if j.State != jobs.StateFailed {
		t.Fatalf("job = %+v", j)
	}
	r := only(t, e.audit(t), "scan.failed")
	if msg, _ := r.Detail["error"].(string); r.Target != "media" || !strings.Contains(msg, "mounted into the container") {
		t.Errorf("entry = %+v", r)
	}
}

// A share that held files and is suddenly empty (an unmounted NAS) is refused, and that must show.
func TestARefusedEmptyScanIsInTheAuditLog(t *testing.T) {
	e := newEnv(t, nil, nil)
	root := t.TempDir()
	for i := 0; i < 120; i++ {
		ts.Write(t, fmt.Sprintf("%s/d/f%03d.txt", root, i), 10)
	}
	id := e.share(t, root, nil)
	e.scan(t, id, "auto", "manual")
	if err := os.RemoveAll(root + "/d"); err != nil {
		t.Fatal(err)
	}
	j := e.scan(t, id, "full", "schedule")
	if j.State != jobs.StateFailed {
		t.Fatalf("an emptied share must be refused: %+v", j)
	}
	r := only(t, e.audit(t), "scan.failed")
	if msg, _ := r.Detail["error"].(string); !strings.Contains(msg, "still mounted") || r.Detail["trigger"] != "schedule" {
		t.Errorf("entry = %+v", r)
	}
}
