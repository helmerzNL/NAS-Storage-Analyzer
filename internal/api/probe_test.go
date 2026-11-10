package api_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

func TestAConnectionTestIsAdminOnly(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	v, _ := e.newUser(admin, "viewer1", "viewer")
	r := admin.do("POST", "/api/v1/shares", shareBody("media", t.TempDir()))
	expect(t, r, 201, "")
	path := fmt.Sprintf("/api/v1/shares/%d/probe", int64(r.Body["id"].(float64)))

	for _, method := range []string{"POST", "GET"} {
		expect(t, e.client().do(method, path, nil), 401, "unauthenticated")
		expect(t, v.do(method, path, nil), 403, "forbidden") // the outcome names paths and servers: not for a viewer
	}
	expect(t, admin.do("POST", path, nil, "X-CSRF-Token", ""), 403, "csrf")
	if n := e.rows("share_probes"); n != 0 {
		t.Errorf("a refused request asked for %d tests", n)
	}
	// A share that does not exist, for both.
	expect(t, admin.do("POST", "/api/v1/shares/999/probe", nil), 404, "")
	expect(t, admin.do("GET", "/api/v1/shares/999/probe", nil), 404, "")
}

// The whole way: asked through the API, done by a real worker, read back through the API.
func TestAConnectionTestThroughTheWorker(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	root := t.TempDir()
	ts.Write(t, root+"/a.txt", 10)
	ts.Write(t, root+"/films/b.mkv", 10)
	r := admin.do("POST", "/api/v1/shares", shareBody("media", root))
	expect(t, r, 201, "")
	path := fmt.Sprintf("/api/v1/shares/%d/probe", int64(r.Body["id"].(float64)))

	// Never tested: there is nothing to show.
	expect(t, admin.do("GET", path, nil), 404, "")

	// No worker runs yet, so the test waits. Pressing the button again is the same test.
	first := admin.do("POST", path, nil)
	expect(t, first, 202, "")
	if first.Body["state"] != jobs.ProbeQueued || first.Body["requested_by"] != "admin" || first.Body["entries"] != nil || first.Body["error"] != nil {
		t.Fatalf("a test that was just asked for: %s", first.Raw)
	}
	again := admin.do("POST", path, nil)
	expect(t, again, 202, "")
	if again.Body["id"] != first.Body["id"] || e.rows("share_probes") != 1 {
		t.Fatalf("asking twice made a second test: %s", again.Raw)
	}
	if got := admin.do("GET", path, nil); got.Status != 200 || got.Body["id"] != first.Body["id"] || got.Body["state"] != jobs.ProbeQueued {
		t.Fatalf("the waiting test: %d %s", got.Status, got.Raw)
	}

	e.startWorker()
	var done reply
	waitFor(t, "the connection test to finish", func() bool {
		done = admin.do("GET", path, nil)
		return done.Body["state"] == jobs.ProbeOK || done.Body["state"] == jobs.ProbeFailed
	})
	if done.Body["state"] != jobs.ProbeOK || done.Body["entries"] != float64(2) || done.Body["more"] != false || done.Body["error"] != nil ||
		done.Body["id"] != first.Body["id"] || done.Body["finished_at"] == nil {
		t.Fatalf("the outcome: %s", done.Raw)
	}

	// Asking is in the audit log, by name; the test itself leaves no scan and no job.
	var who, target, probe string
	if err := e.st.Pool.QueryRow(t.Context(), `SELECT username, target, detail->>'probe' FROM audit_log WHERE action = 'share.test' ORDER BY id LIMIT 1`).
		Scan(&who, &target, &probe); err != nil {
		t.Fatal(err)
	}
	if who != "admin" || target != "media" || probe != fmt.Sprint(int64(first.Body["id"].(float64))) {
		t.Errorf("audit entry: by %q on %q, test %q", who, target, probe)
	}
	if e.rows("jobs") != 0 || e.rows("scans") != 0 {
		t.Errorf("a test is not a scan: %d jobs, %d scans", e.rows("jobs"), e.rows("scans"))
	}

	// The next test is a new one, and it tells what is wrong: here the folder is gone.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	second := admin.do("POST", path, nil)
	expect(t, second, 202, "")
	if second.Body["id"] == first.Body["id"] {
		t.Fatalf("a test after a finished one must be a new test: %s", second.Raw)
	}
	waitFor(t, "the second test to finish", func() bool {
		done = admin.do("GET", path, nil)
		return done.Body["state"] == jobs.ProbeOK || done.Body["state"] == jobs.ProbeFailed
	})
	msg, _ := done.Body["error"].(string)
	if done.Body["state"] != jobs.ProbeFailed || !strings.Contains(msg, "mounted into the container") || done.Body["entries"] != nil {
		t.Fatalf("the outcome of a share whose folder is gone: %s", done.Raw)
	}
}

// Without a worker nothing would ever answer. The test is closed after a minute with the reason, by whoever reads it.
func TestAConnectionTestThatNoWorkerPicksUpSaysSo(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	r := admin.do("POST", "/api/v1/shares", shareBody("media", t.TempDir()))
	expect(t, r, 201, "")
	path := fmt.Sprintf("/api/v1/shares/%d/probe", int64(r.Body["id"].(float64)))
	expect(t, admin.do("POST", path, nil), 202, "")
	if got := admin.do("GET", path, nil); got.Body["state"] != jobs.ProbeQueued {
		t.Fatalf("within the minute the test still waits: %s", got.Raw)
	}
	if _, err := e.st.Pool.Exec(t.Context(), `UPDATE share_probes SET requested_at = now() - interval '61 seconds'`); err != nil {
		t.Fatal(err)
	}
	got := admin.do("GET", path, nil)
	msg, _ := got.Body["error"].(string)
	if got.Status != 200 || got.Body["state"] != jobs.ProbeFailed || !strings.Contains(msg, "no worker picked the test up") || got.Body["finished_at"] == nil {
		t.Fatalf("a test nobody picked up: %s", got.Raw)
	}
	// And the share can be tested again.
	next := admin.do("POST", path, nil)
	expect(t, next, 202, "")
	if next.Body["id"] == got.Body["id"] || next.Body["state"] != jobs.ProbeQueued {
		t.Fatalf("a new test after the closed one: %s", next.Raw)
	}
}
