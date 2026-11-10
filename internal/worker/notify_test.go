package worker_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	"github.com/helmerznl/nas-storage-analyzer/internal/notify"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

// hook is a webhook receiver: it keeps the messages it was sent.
type hook struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	got    []map[string]any
}

func newHook(t *testing.T, status int) *hook {
	t.Helper()
	h := &hook{status: status}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(body, &m)
		h.mu.Lock()
		h.got = append(h.got, m)
		h.mu.Unlock()
		w.WriteHeader(h.status)
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *hook) messages() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]any(nil), h.got...)
}

// wait returns the messages once there are n of them.
func (h *hook) wait(t *testing.T, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if got := h.messages(); len(got) >= n {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected %d messages, got %d: %v", n, len(h.messages()), h.messages())
	return nil
}

func (e *env) target(t *testing.T, name, url string, mut func(*notify.Input)) {
	t.Helper()
	in := notify.Defaults()
	in.Name, in.URL = name, &url
	if mut != nil {
		mut(&in)
	}
	if _, err := e.notify.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
}

// What the worker writes in the audit log for a scan is what it tells the targets: the same outcome, the same moment.
func TestAScanThatNeedsAttentionIsAnnounced(t *testing.T) {
	e := newEnv(t, nil, nil)
	h := newHook(t, 200)
	e.target(t, "Home Assistant", h.URL+"/api/webhook/secret-id", func(in *notify.Input) { in.Lang = "nl" })

	// A share whose folder is not there: the scan fails.
	gone := e.share(t, "/nonexistent/path", func(in *shares.Input) { in.Name = "weg" })
	j, _ := e.q.Enqueue(ctx, jobs.KindScan, gone, "auto", "schedule", "")
	e.waitJob(t, j.ID, jobs.StateFailed)
	m := h.wait(t, 1)[0]
	msg, _ := m["message"].(string)
	if m["event"] != "scan.failed" || m["share"] != "weg" || m["title"] != "NASSA: scan van weg mislukt" || m["trigger"] != "schedule" ||
		m["job_id"] != float64(j.ID) || m["share_id"] != float64(gone) || !strings.Contains(msg, "mounted into the container") {
		t.Fatalf("the message about a failed scan: %v", m)
	}

	// An empty folder: the scan succeeds and finds nothing, which is what an unmounted share looks like.
	empty := e.share(t, t.TempDir(), func(in *shares.Input) { in.Name = "leeg" })
	if j := e.scan(t, empty, "auto", "manual"); j.State != jobs.StateDone {
		t.Fatalf("scan of an empty folder: %+v", j)
	}
	m = h.wait(t, 2)[1]
	if m["event"] != "scan.empty" || m["share"] != "leeg" || m["files"] != float64(0) || m["scan_id"] == nil || m["priority"] != float64(8) {
		t.Fatalf("the message about a scan that found nothing: %v", m)
	}

	// A scan with nothing to report is not announced: the target did not ask for those.
	root := t.TempDir()
	ts.Write(t, root+"/a.txt", 10)
	fine := e.share(t, root, func(in *shares.Input) { in.Name = "goed" })
	if j := e.scan(t, fine, "auto", "manual"); j.State != jobs.StateDone {
		t.Fatalf("scan: %+v", j)
	}
	// Another failure follows it; had the good scan been announced, it would be the third message.
	j, _ = e.q.Enqueue(ctx, jobs.KindScan, gone, "auto", "manual", "")
	e.waitJob(t, j.ID, jobs.StateFailed)
	if got := h.wait(t, 3); len(got) != 3 || got[2]["event"] != "scan.failed" {
		t.Fatalf("after a good scan and another failure: %v", got)
	}
	// Delivered: nothing about notifications in the audit log, and the address is in none of its entries.
	for _, a := range e.audit(t) {
		if strings.HasPrefix(a.Action, "notification.") || strings.Contains(fmt.Sprint(a.Detail), "secret-id") {
			t.Errorf("audit entry %s %v", a.Action, a.Detail)
		}
	}
}

func TestAFinishedScanIsAnnouncedToATargetThatAsks(t *testing.T) {
	e := newEnv(t, nil, nil)
	h := newHook(t, 200)
	e.target(t, "Everything", h.URL+"/hook", func(in *notify.Input) { in.Events = notify.Events{Finished: true} })
	root := t.TempDir()
	ts.Write(t, root+"/a.txt", 1000)
	ts.Write(t, root+"/sub/b.txt", 24)
	id := e.share(t, root, nil)
	if j := e.scan(t, id, "auto", "manual"); j.State != jobs.StateDone {
		t.Fatalf("scan: %+v", j)
	}
	m := h.wait(t, 1)[0]
	if m["event"] != "scan.finished" || m["files"] != float64(2) || m["bytes"] != float64(1024) || m["errors"] != float64(0) || m["priority"] != float64(2) {
		t.Fatalf("the message about a finished scan: %v", m)
	}
	// What a scan found is counted, never named: no file or folder name is in the message.
	for _, name := range []string{"a.txt", "b.txt", "sub"} {
		if strings.Contains(fmt.Sprint(m), name) {
			t.Errorf("the message names %q: %v", name, m)
		}
	}
}

// A receiver that refuses changes nothing about the scan, and the audit log says the message did not arrive: by the name of the
// target, never with its address.
func TestAMessageThatDoesNotArriveIsInTheAuditLog(t *testing.T) {
	e := newEnv(t, nil, nil)
	h := newHook(t, 401)
	e.target(t, "ntfy", h.URL+"/topic-secret-token", nil)
	id := e.share(t, "/nonexistent/path", nil)
	j, _ := e.q.Enqueue(ctx, jobs.KindScan, id, "auto", "manual", "")
	if done := e.waitJob(t, j.ID, jobs.StateFailed); done.Error == nil {
		t.Fatalf("the scan itself ended as it would have: %+v", done)
	}
	h.wait(t, 1)
	var entry *auditRow
	deadline := time.Now().Add(10 * time.Second)
	for entry == nil && time.Now().Before(deadline) {
		for _, a := range e.audit(t) {
			if a.Action == "notification.failed" {
				a := a
				entry = &a
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if entry == nil {
		t.Fatal("no notification.failed entry in the audit log")
	}
	text := fmt.Sprint(entry.Detail)
	if entry.Target != "ntfy" || entry.Detail["event"] != "scan.failed" || entry.Detail["status"] != float64(401) || entry.Detail["share"] != "media" ||
		strings.Contains(text, "secret-token") || strings.Contains(text, h.URL) {
		t.Errorf("the entry: target %q, %v", entry.Target, entry.Detail)
	}
}

// A scan that ends because the worker itself is stopping is not announced: nothing is wrong with the share.
func TestAScanInterruptedByAShutdownIsNotAnnounced(t *testing.T) {
	e := newEnv(t, nil, nil)
	h := newHook(t, 200)
	e.target(t, "Home Assistant", h.URL+"/hook", nil)
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		ts.Write(t, fmt.Sprintf("%s/f%03d.txt", root, i), 1)
	}
	id := e.share(t, root, func(in *shares.Input) { in.MaxOpsPerSec = 20 })
	j, _ := e.q.Enqueue(ctx, jobs.KindScan, id, "auto", "manual", "")
	e.waitJob(t, j.ID, jobs.StateRunning)
	time.Sleep(200 * time.Millisecond)
	e.stop()
	var state string
	e.st.Pool.QueryRow(ctx, `SELECT state FROM jobs WHERE id = $1`, j.ID).Scan(&state)
	if state != jobs.StateFailed {
		t.Fatalf("the job after the shutdown: %s", state)
	}
	time.Sleep(300 * time.Millisecond)
	if got := h.messages(); len(got) != 0 {
		t.Errorf("a scan that the shutdown interrupted was announced: %v", got)
	}
}
