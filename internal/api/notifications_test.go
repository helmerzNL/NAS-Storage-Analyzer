package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// webhook is a receiver for the test message: it keeps what it was sent.
type webhook struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	got    []map[string]any
}

func newWebhook(t *testing.T, status int) *webhook {
	t.Helper()
	h := &webhook{status: status}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(body, &m)
		h.mu.Lock()
		h.got = append(h.got, m)
		h.mu.Unlock()
		w.WriteHeader(h.status)
		io.WriteString(w, "what the receiver answers stays with the server")
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *webhook) messages() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]any(nil), h.got...)
}

func TestNotificationTargetsAreAdminOnly(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	v, _ := e.newUser(admin, "viewer1", "viewer")
	body := map[string]any{"name": "Phone", "url": "https://ntfy.example/topic"}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/notifications"}, {"POST", "/api/v1/notifications"}, {"PATCH", "/api/v1/notifications/1"},
		{"DELETE", "/api/v1/notifications/1"}, {"POST", "/api/v1/notifications/1/test"},
	} {
		var b any
		if c.method == "POST" && !strings.HasSuffix(c.path, "/test") || c.method == "PATCH" {
			b = body
		}
		expect(t, e.client().do(c.method, c.path, b), 401, "unauthenticated")
		expect(t, v.do(c.method, c.path, b), 403, "forbidden")
	}
	expect(t, admin.do("POST", "/api/v1/notifications", body, "X-CSRF-Token", ""), 403, "csrf")
	if n := e.rows("notification_targets"); n != 0 {
		t.Errorf("a refused request made %d targets", n)
	}
}

func TestNotificationTargetsThroughTheAPI(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	hook := newWebhook(t, 200)
	const secretPath = "/api/webhook/the-secret-id"

	// Nothing to begin with.
	r := admin.do("GET", "/api/v1/notifications", nil)
	expect(t, r, 200, "")
	if len(r.Body["items"].([]any)) != 0 || r.Body["max"] != float64(5) {
		t.Fatalf("an installation starts without targets: %s", r.Raw)
	}

	// What is wrong is said by field.
	for field, body := range map[string]map[string]any{
		"url":    {"name": "x", "url": "ftp://host/x"},
		"name":   {"name": " ", "url": "https://host/x"},
		"format": {"name": "x", "url": "https://host/x", "format": "xml"},
		"lang":   {"name": "x", "url": "https://host/x", "lang": "de"},
	} {
		if r := admin.do("POST", "/api/v1/notifications", body); r.Status != 400 || r.Body["field"] != field {
			t.Errorf("%v: got %d field %v, want 400 field %s", body, r.Status, r.Body["field"], field)
		}
	}
	if r := admin.do("POST", "/api/v1/notifications", map[string]any{"name": "No address"}); r.Status != 400 || r.Body["field"] != "url" {
		t.Errorf("a new target without an address: %d %s", r.Status, r.Raw)
	}

	// A target with only a name and an address gets the defaults: every outcome that needs attention.
	r = admin.do("POST", "/api/v1/notifications", map[string]any{"name": "Home Assistant", "url": hook.URL + secretPath, "lang": "nl"})
	expect(t, r, 201, "")
	events, _ := r.Body["events"].(map[string]any)
	if r.Body["format"] != "json" || r.Body["enabled"] != true || events["failed"] != true || events["empty"] != true || events["errors"] != true || events["finished"] != false ||
		!strings.HasSuffix(r.Body["url_hint"].(string), "/…") || r.Body["last_at"] != nil {
		t.Fatalf("a new target: %s", r.Raw)
	}
	url := fmt.Sprintf("/api/v1/notifications/%d", int64(r.Body["id"].(float64)))
	expect(t, admin.do("POST", "/api/v1/notifications", map[string]any{"name": "Home Assistant", "url": "https://other/x"}), 409, "conflict")

	// The address went in and does not come out: not in any answer, and not in the audit log.
	leaks := func(where, text string) {
		t.Helper()
		if strings.Contains(text, "the-secret-id") || strings.Contains(text, secretPath) {
			t.Errorf("%s gives the address away: %s", where, text)
		}
	}
	leaks("the answer to creating it", r.Raw)
	leaks("the list", admin.do("GET", "/api/v1/notifications", nil).Raw)

	// The test message arrives, in the target's language, and the answer says how it went.
	d := admin.do("POST", url+"/test", nil)
	expect(t, d, 200, "")
	if d.Body["ok"] != true || d.Body["status"] != float64(200) || d.Body["error"] != "" || d.Body["target"] != "Home Assistant" {
		t.Fatalf("the test delivery: %s", d.Raw)
	}
	leaks("the answer to the test", d.Raw)
	if got := hook.messages(); len(got) != 1 || got[0]["event"] != "test" || got[0]["title"] != "NASSA: testmelding" {
		t.Fatalf("what the receiver got: %v", got)
	}
	list := admin.do("GET", "/api/v1/notifications", nil).Body["items"].([]any)[0].(map[string]any)
	if list["last_ok"] != true || list["last_status"] != float64(200) || list["last_error"] != nil || list["last_at"] == nil {
		t.Errorf("the outcome is kept: %v", list)
	}

	// Changing one thing keeps the rest, the address included.
	r = admin.do("PATCH", url, map[string]any{"events": map[string]any{"finished": true}})
	expect(t, r, 200, "")
	events, _ = r.Body["events"].(map[string]any)
	if events["finished"] != true || events["failed"] != true || r.Body["name"] != "Home Assistant" || r.Body["lang"] != "nl" || r.Body["last_ok"] != true {
		t.Fatalf("a change of one field: %s", r.Raw)
	}
	if d := admin.do("POST", url+"/test", nil); d.Body["ok"] != true || len(hook.messages()) != 2 {
		t.Fatalf("the address is still the stored one: %s", d.Raw)
	}
	// A receiver that refuses: the delivery failed, the request did not; the receiver's own answer is not passed on.
	refusing := newWebhook(t, 403)
	r = admin.do("PATCH", url, map[string]any{"url": refusing.URL + secretPath, "enabled": false})
	expect(t, r, 200, "")
	if r.Body["enabled"] != false || r.Body["last_at"] != nil {
		t.Fatalf("a new address forgets the outcome of the old one: %s", r.Raw)
	}
	d = admin.do("POST", url+"/test", nil)
	expect(t, d, 200, "")
	if d.Body["ok"] != false || d.Body["status"] != float64(403) || d.Body["error"] == "" || strings.Contains(d.Raw, "stays with the server") {
		t.Fatalf("a refused test: %s", d.Raw)
	}
	leaks("the answer to a failed test", d.Raw)

	// The audit log has every step, by the name of the target and its host, without the address.
	rows, err := e.st.Pool.Query(t.Context(), `SELECT action, target, detail::text FROM audit_log WHERE action LIKE 'notification.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action, target, detail string
		if err := rows.Scan(&action, &target, &detail); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
		if target != "Home Assistant" {
			t.Errorf("%s names %q", action, target)
		}
		leaks("the audit entry "+action, detail)
	}
	if got := strings.Join(actions, " "); got != "notification.create notification.test notification.update notification.test notification.update notification.test" {
		t.Errorf("audit entries: %s", got)
	}

	// Unknown targets, and removing one.
	expect(t, admin.do("PATCH", "/api/v1/notifications/999", map[string]any{"name": "x"}), 404, "not_found")
	expect(t, admin.do("POST", "/api/v1/notifications/999/test", nil), 404, "not_found")
	expect(t, admin.do("DELETE", url, nil), 204, "")
	expect(t, admin.do("DELETE", url, nil), 404, "not_found")
	expect(t, admin.do("POST", url+"/test", nil), 404, "not_found")

	// At most five.
	for i := 0; i < 5; i++ {
		expect(t, admin.do("POST", "/api/v1/notifications", map[string]any{"name": fmt.Sprint("t", i), "url": "https://host/x"}), 201, "")
	}
	expect(t, admin.do("POST", "/api/v1/notifications", map[string]any{"name": "one too many", "url": "https://host/x"}), 409, "limit_reached")
}
