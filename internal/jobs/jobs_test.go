package jobs_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

var ctx = context.Background()

func setup(t *testing.T) (*store.Store, *jobs.Queue, *store.Share) {
	t.Helper()
	st := ts.NewStore(t)
	sh, err := st.EnsureShare(ctx, "media", t.TempDir(), nil, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	return st, jobs.NewQueue(st.Pool), sh
}

func TestOneActiveJobPerShareAndKind(t *testing.T) {
	_, q, sh := setup(t)
	j, err := q.Enqueue(ctx, jobs.KindScan, sh.ID, "auto", "manual", "alice")
	if err != nil || j.State != jobs.StateQueued || j.RequestedBy != "alice" {
		t.Fatalf("enqueue: %+v %v", j, err)
	}
	if _, err := q.Enqueue(ctx, jobs.KindScan, sh.ID, "full", "manual", ""); !errors.Is(err, jobs.ErrAlreadyActive) {
		t.Errorf("second scan job: %v", err)
	}
	// Another kind is independent.
	if _, err := q.Enqueue(ctx, jobs.KindRecategorize, sh.ID, "auto", "manual", ""); err != nil {
		t.Errorf("recategorize job: %v", err)
	}
	// Once the first is finished a new one is possible.
	c, _ := q.Claim(ctx, "w1")
	q.Finish(ctx, c.ID, jobs.StateDone, "")
	if _, err := q.Enqueue(ctx, jobs.KindScan, sh.ID, "auto", "manual", ""); err != nil {
		t.Errorf("after finishing: %v", err)
	}
}

func TestClaimGivesEachJobToExactlyOneWorker(t *testing.T) {
	st, q, _ := setup(t)
	for i := 0; i < 12; i++ {
		sh, err := st.EnsureShare(ctx, "s"+string(rune('a'+i)), t.TempDir(), nil, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.Enqueue(ctx, jobs.KindScan, sh.ID, "auto", "manual", ""); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[int64]string{}
	var dup atomic.Int32
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			for {
				j, err := q.Claim(ctx, name)
				if err != nil {
					t.Error(err)
					return
				}
				if j == nil {
					return
				}
				mu.Lock()
				if _, ok := seen[j.ID]; ok {
					dup.Add(1)
				}
				seen[j.ID] = name
				mu.Unlock()
			}
		}("w" + string(rune('0'+w)))
	}
	wg.Wait()
	if dup.Load() != 0 || len(seen) != 12 {
		t.Errorf("claimed %d distinct jobs, %d claimed twice; want 12 and 0", len(seen), dup.Load())
	}
	if j, _ := q.Claim(ctx, "late"); j != nil {
		t.Error("claimed a job from an empty queue")
	}
}

func TestCancel(t *testing.T) {
	_, q, sh := setup(t)
	q.Enqueue(ctx, jobs.KindScan, sh.ID, "auto", "manual", "")
	ok, err := q.Cancel(ctx, sh.ID, jobs.KindScan)
	if err != nil || !ok {
		t.Fatalf("cancel queued: %v %v", ok, err)
	}
	if j, _ := q.Claim(ctx, "w"); j != nil {
		t.Error("a cancelled job was claimed")
	}
	// A running job is asked to stop, and the worker sees it at its heartbeat.
	q.Enqueue(ctx, jobs.KindScan, sh.ID, "auto", "manual", "")
	j, _ := q.Claim(ctx, "w")
	if c, _ := q.Heartbeat(ctx, j.ID); c {
		t.Error("cancel flag set before cancelling")
	}
	if ok, _ := q.Cancel(ctx, sh.ID, jobs.KindScan); !ok {
		t.Error("nothing cancelled")
	}
	if c, _ := q.Heartbeat(ctx, j.ID); !c {
		t.Error("worker does not see the cancel request")
	}
	q.Finish(ctx, j.ID, jobs.StateCancelled, "")
	if ok, _ := q.Cancel(ctx, sh.ID, jobs.KindScan); ok {
		t.Error("cancelled a share with no active job")
	}
	// A job that no longer exists tells the worker to stop.
	if c, _ := q.Heartbeat(ctx, j.ID); !c {
		t.Error("heartbeat of a finished job must say stop")
	}
}

func TestReapFailsJobsOfDeadWorkers(t *testing.T) {
	st, q, sh := setup(t)
	q.Enqueue(ctx, jobs.KindScan, sh.ID, "auto", "manual", "")
	j, _ := q.Claim(ctx, "crashed")
	scanID, _ := st.BeginScan(ctx, sh.ID, "full", "manual", "")
	q.SetScan(ctx, j.ID, scanID)
	if n, _ := q.Reap(ctx, time.Minute); n != 0 {
		t.Errorf("reaped a fresh job")
	}
	st.Pool.Exec(ctx, `UPDATE jobs SET heartbeat_at = now() - interval '10 minutes'`)
	n, err := q.Reap(ctx, time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("reap: %d %v", n, err)
	}
	var state, scanState string
	st.Pool.QueryRow(ctx, `SELECT state FROM jobs WHERE id = $1`, j.ID).Scan(&state)
	st.Pool.QueryRow(ctx, `SELECT status FROM scans WHERE id = $1`, scanID).Scan(&scanState)
	if state != jobs.StateFailed || scanState != "failed" {
		t.Errorf("job %s, scan %s", state, scanState)
	}
	// The share is free for a new scan again.
	if _, err := q.Enqueue(ctx, jobs.KindScan, sh.ID, "auto", "manual", ""); err != nil {
		t.Errorf("share still blocked: %v", err)
	}
	if _, err := st.BeginScan(ctx, sh.ID, "full", "manual", ""); err != nil {
		t.Errorf("scan still blocked: %v", err)
	}
}

func TestNextRun(t *testing.T) {
	ams, _ := time.LoadLocation("Europe/Amsterdam")
	after := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	next, err := jobs.NextRun("0 3 * * *", after, ams)
	if err != nil {
		t.Fatal(err)
	}
	// 03:00 Amsterdam on 31 Oct 2026 is 02:00 UTC (summer time ends on 25 Oct 2026, so CET = UTC+1).
	if want := time.Date(2026, 10, 31, 2, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Errorf("next = %v, want %v", next.UTC(), want)
	}
	if n, err := jobs.NextRun("", after, ams); n != nil || err != nil {
		t.Errorf("empty expression: %v %v", n, err)
	}
	for _, bad := range []string{"* * *", "61 * * * *", "every day", "0 3 * * * *"} {
		if err := jobs.ValidateCron(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"*/15 * * * *", "0 3 * * 1-5", "30 2 1 * *", "@daily"} {
		if err := jobs.ValidateCron(good); err != nil {
			t.Errorf("%q rejected: %v", good, err)
		}
	}
}

func TestSchedulerEnqueuesOnceAndPlansTheNextRun(t *testing.T) {
	st, q, sh := setup(t)
	s := jobs.NewScheduler(st.Pool, q, time.UTC, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Nothing is due.
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if j, _ := q.Claim(ctx, "w"); j != nil {
		t.Fatal("a job appeared without a schedule")
	}
	st.Pool.Exec(ctx, `UPDATE shares SET cron = '0 3 * * *', next_run_at = now() - interval '1 minute' WHERE id = $1`, sh.ID)
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	j, _ := q.Claim(ctx, "w")
	if j == nil || j.Trigger != "schedule" || j.Mode != "auto" || j.ShareID != sh.ID {
		t.Fatalf("scheduled job = %+v", j)
	}
	var next *time.Time
	st.Pool.QueryRow(ctx, `SELECT next_run_at FROM shares WHERE id = $1`, sh.ID).Scan(&next)
	if next == nil || !next.After(time.Now()) || next.UTC().Hour() != 3 {
		t.Errorf("next run = %v", next)
	}
	// Ticking again before the next run does nothing, even though the job still runs.
	s.Tick(ctx)
	if j2, _ := q.Claim(ctx, "w"); j2 != nil {
		t.Error("scheduled twice")
	}
	// A due schedule while a scan is still running does not pile up jobs.
	st.Pool.Exec(ctx, `UPDATE shares SET next_run_at = now() - interval '1 second' WHERE id = $1`, sh.ID)
	if err := s.Tick(ctx); err != nil {
		t.Errorf("tick with a running job: %v", err)
	}
	// An invalid stored expression disables the schedule instead of failing every tick.
	q.Finish(ctx, j.ID, jobs.StateDone, "")
	st.Pool.Exec(ctx, `UPDATE shares SET cron = 'garbage', next_run_at = now() - interval '1 second' WHERE id = $1`, sh.ID)
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	st.Pool.QueryRow(ctx, `SELECT next_run_at FROM shares WHERE id = $1`, sh.ID).Scan(&next)
	if next != nil {
		t.Errorf("a broken schedule keeps firing: %v", next)
	}
}

func TestOnlyOneSchedulerLeads(t *testing.T) {
	st, q, sh := setup(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st.Pool.Exec(ctx, `UPDATE shares SET cron = '* * * * *', next_run_at = now() - interval '1 minute' WHERE id = $1`, sh.ID)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i := 0; i < 4; i++ {
		s := jobs.NewScheduler(st.Pool, q, time.UTC, log)
		s.Every = 50 * time.Millisecond
		go s.Run(cctx)
	}
	time.Sleep(600 * time.Millisecond)
	cancel()
	var n int
	st.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE share_id = $1`, sh.ID).Scan(&n)
	if n != 1 {
		t.Errorf("%d jobs created by four schedulers in a moment, want exactly 1", n)
	}
}

// The file-type map is read when a re-categorisation runs. An edit made while one is RUNNING is not in it, so a job has to be able to follow:
// refusing it (because "one is active") lost the edit, and nobody was told. This is the failure of TestEditingTheExtensionMapRecategorizes
// that turned CI red on beta, as a test that does not depend on timing.
func TestAnEditWhileARecategorizationRunsGetsAnotherOneAfterIt(t *testing.T) {
	_, q, sh := setup(t)
	first, err := q.Enqueue(ctx, jobs.KindRecategorize, sh.ID, "auto", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	if c, err := q.Claim(ctx, "w1"); err != nil || c == nil || c.ID != first.ID {
		t.Fatalf("claim: %+v %v", c, err)
	}
	// The map is edited now, while the first job runs: the follow-up is accepted.
	second, err := q.Enqueue(ctx, jobs.KindRecategorize, sh.ID, "auto", "manual", "")
	if err != nil {
		t.Fatalf("an edit while a re-categorisation runs must queue another one: %v", err)
	}
	// A second WAITING one adds nothing (a waiting job reads the map when it starts), so that refusal stays, and is safe to ignore.
	if _, err := q.Enqueue(ctx, jobs.KindRecategorize, sh.ID, "auto", "manual", ""); !errors.Is(err, jobs.ErrAlreadyActive) {
		t.Errorf("two waiting re-categorisations: %v", err)
	}
	// The waiting one does not start next to the running one, however many workers ask...
	var wg sync.WaitGroup
	var started atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, _ := q.Claim(ctx, "w"+string(rune('2'+i))); c != nil {
				started.Add(1)
			}
		}()
	}
	wg.Wait()
	if started.Load() != 0 {
		t.Fatalf("%d workers started a second re-categorisation of the same share next to the running one", started.Load())
	}
	// ...and starts once the running one is finished.
	if err := q.Finish(ctx, first.ID, jobs.StateDone, ""); err != nil {
		t.Fatal(err)
	}
	if c, err := q.Claim(ctx, "w9"); err != nil || c == nil || c.ID != second.ID {
		t.Fatalf("the follow-up did not start after the first: %+v %v", c, err)
	}
}

func TestAScanStillCannotBeQueuedWhileOneRuns(t *testing.T) {
	_, q, sh := setup(t)
	if _, err := q.Enqueue(ctx, jobs.KindScan, sh.ID, "auto", "manual", ""); err != nil {
		t.Fatal(err)
	}
	if c, _ := q.Claim(ctx, "w1"); c == nil {
		t.Fatal("no scan to claim")
	}
	if _, err := q.Enqueue(ctx, jobs.KindScan, sh.ID, "full", "manual", ""); !errors.Is(err, jobs.ErrAlreadyActive) {
		t.Errorf("a second scan while one runs: %v (the old rule holds for scans: a second \"scan now\" is refused)", err)
	}
}

func TestARunningRecategorizationOfOneShareDoesNotHoldBackAnotherShare(t *testing.T) {
	st, q, a := setup(t)
	b, err := st.EnsureShare(ctx, "other", t.TempDir(), nil, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a.ID, b.ID} {
		if _, err := q.Enqueue(ctx, jobs.KindRecategorize, id, "auto", "manual", ""); err != nil {
			t.Fatal(err)
		}
	}
	c1, _ := q.Claim(ctx, "w1")
	c2, _ := q.Claim(ctx, "w2")
	if c1 == nil || c2 == nil || c1.ShareID == c2.ShareID {
		t.Fatalf("each share's re-categorisation runs on its own: %+v %+v", c1, c2)
	}
}

// A follow-up that has to wait must not hold back the work behind it: if Claim kept choosing it (and failing), every other job would wait
// until the running re-categorisation of that one share was done.
func TestAWaitingFollowUpDoesNotHoldBackOtherJobs(t *testing.T) {
	st, q, a := setup(t)
	b, err := st.EnsureShare(ctx, "other", t.TempDir(), nil, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(ctx, jobs.KindRecategorize, a.ID, "auto", "manual", ""); err != nil {
		t.Fatal(err)
	}
	if c, _ := q.Claim(ctx, "w1"); c == nil {
		t.Fatal("no first job")
	}
	if _, err := q.Enqueue(ctx, jobs.KindRecategorize, a.ID, "auto", "manual", ""); err != nil { // the follow-up, waiting, with the lower id
		t.Fatal(err)
	}
	scan, err := q.Enqueue(ctx, jobs.KindScan, b.ID, "auto", "manual", "") // work for another share, behind it
	if err != nil {
		t.Fatal(err)
	}
	c, err := q.Claim(ctx, "w2")
	if err != nil || c == nil || c.ID != scan.ID {
		t.Fatalf("the scan of another share was held back by a follow-up that has to wait: %+v %v", c, err)
	}
}
