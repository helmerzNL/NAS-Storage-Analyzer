package jobs_test

import (
	"testing"

	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
)

// A connection test goes queued -> running -> ok or failed, and a share has at most one under way.
func TestAProbeIsRequestedClaimedAndClosed(t *testing.T) {
	st, _, sh := setup(t)
	p := jobs.NewProbes(st.Pool)
	if got, err := p.Latest(ctx, sh.ID); err != nil || got != nil {
		t.Fatalf("a share that was never tested has no test: %+v %v", got, err)
	}
	first, err := p.Request(ctx, sh.ID, "alice")
	if err != nil || first.State != jobs.ProbeQueued || first.RequestedBy != "alice" || first.Entries != nil {
		t.Fatalf("request: %+v %v", first, err)
	}
	// Pressing the button again is the same test, not a second one.
	if again, err := p.Request(ctx, sh.ID, "bob"); err != nil || again.ID != first.ID || again.RequestedBy != "alice" {
		t.Fatalf("a second request while one waits: %+v %v", again, err)
	}
	claimed, err := p.Claim(ctx, "w1")
	if err != nil || claimed == nil || claimed.ID != first.ID || claimed.State != jobs.ProbeRunning || claimed.StartedAt == nil {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if none, err := p.Claim(ctx, "w2"); err != nil || none != nil {
		t.Errorf("a second worker claimed the same test: %+v %v", none, err)
	}
	if again, _ := p.Request(ctx, sh.ID, "bob"); again.ID != first.ID {
		t.Errorf("a request while one runs made a second test: %+v", again)
	}
	if err := p.Finish(ctx, first.ID, 7, true, ""); err != nil {
		t.Fatal(err)
	}
	done, _ := p.Latest(ctx, sh.ID)
	if done.State != jobs.ProbeOK || done.Entries == nil || *done.Entries != 7 || !done.More || done.Error != nil || done.FinishedAt == nil {
		t.Errorf("an ended test: %+v", done)
	}
	// Once it has ended, a new one can be asked for; a failure keeps its reason and no count.
	second, err := p.Request(ctx, sh.ID, "alice")
	if err != nil || second.ID == first.ID || second.State != jobs.ProbeQueued {
		t.Fatalf("a new test after the first ended: %+v %v", second, err)
	}
	c, _ := p.Claim(ctx, "w1")
	if err := p.Finish(ctx, c.ID, 0, false, "mount failed: no route to host"); err != nil {
		t.Fatal(err)
	}
	failed, _ := p.Latest(ctx, sh.ID)
	if failed.ID != second.ID || failed.State != jobs.ProbeFailed || failed.Error == nil || *failed.Error != "mount failed: no route to host" || failed.Entries != nil {
		t.Errorf("a failed test: %+v", failed)
	}
}

// Nobody is left waiting for ever: a test no worker picks up says that no worker is running, a test whose worker went away says so,
// and neither blocks the next one.
func TestAProbeThatCannotEndIsClosed(t *testing.T) {
	st, _, sh := setup(t)
	p := jobs.NewProbes(st.Pool)
	waiting, _ := p.Request(ctx, sh.ID, "alice")
	if got, _ := p.Latest(ctx, sh.ID); got.State != jobs.ProbeQueued {
		t.Fatalf("a fresh test is not closed: %+v", got)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE share_probes SET requested_at = now() - interval '61 seconds' WHERE id = $1`, waiting.ID); err != nil {
		t.Fatal(err)
	}
	got, err := p.Latest(ctx, sh.ID)
	if err != nil || got.State != jobs.ProbeFailed || got.Error == nil || *got.Error != jobs.ProbeNoWorker {
		t.Fatalf("a test that waited longer than the limit: %+v %v", got, err)
	}
	if c, _ := p.Claim(ctx, "w1"); c != nil {
		t.Errorf("a worker that comes back late still ran a test that was already answered: %+v", c)
	}

	running, err := p.Request(ctx, sh.ID, "alice")
	if err != nil || running.ID == waiting.ID {
		t.Fatalf("the closed test blocks the next one: %+v %v", running, err)
	}
	if c, _ := p.Claim(ctx, "w1"); c == nil || c.ID != running.ID {
		t.Fatalf("claim: %+v", c)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE share_probes SET started_at = now() - interval '181 seconds' WHERE id = $1`, running.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.Latest(ctx, sh.ID); got.State != jobs.ProbeFailed || *got.Error != jobs.ProbeWorkerGone {
		t.Fatalf("a test whose worker went away: %+v", got)
	}
	// The worker finishing late does not turn the answer that was already given around.
	if err := p.Finish(ctx, running.ID, 3, false, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.Latest(ctx, sh.ID); got.State != jobs.ProbeFailed || got.Entries != nil {
		t.Errorf("a late outcome replaced the one already shown: %+v", got)
	}
}

// Outcomes are kept for a week, a share that is deleted takes its tests with it, and a test never counts as a job: it does not make
// a share "busy" for a scan, a restore or a reset.
func TestProbesArePrunedAndStayOutOfTheJobQueue(t *testing.T) {
	st, q, sh := setup(t)
	p := jobs.NewProbes(st.Pool)
	old, _ := p.Request(ctx, sh.ID, "alice")
	c, _ := p.Claim(ctx, "w1")
	_ = p.Finish(ctx, c.ID, 1, false, "")
	if _, err := st.Pool.Exec(ctx, `UPDATE share_probes SET finished_at = now() - interval '8 days' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	fresh, _ := p.Request(ctx, sh.ID, "alice")
	var n int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM share_probes`).Scan(&n); err != nil || n != 1 {
		t.Errorf("after a week the old outcome is gone and the new test is there: %d rows, %v", n, err)
	}
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a connection test made a job: %d", n)
	}
	if j, err := q.Enqueue(ctx, jobs.KindScan, sh.ID, "auto", "manual", "alice"); err != nil || j == nil {
		t.Errorf("a waiting test blocks a scan: %v", err)
	}
	if j, _ := q.Claim(ctx, "w1"); j == nil || j.Kind != jobs.KindScan {
		t.Errorf("the job queue handed out something else than the scan: %+v", j)
	}
	if got, _ := q.Probes().Latest(ctx, sh.ID); got == nil || got.ID != fresh.ID {
		t.Errorf("the probes of a queue are the same ones: %+v", got)
	}
	if _, err := st.Pool.Exec(ctx, `DELETE FROM shares WHERE id = $1`, sh.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM share_probes`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the tests of a deleted share stayed behind: %d", n)
	}
}
