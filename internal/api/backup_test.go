package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/helmerznl/nas-storage-analyzer/internal/api"
	"github.com/helmerznl/nas-storage-analyzer/internal/backup"
)

// rawBody sends a body as it is (a file that is not JSON cannot go through client.do, which encodes its argument) and still checks the answer
// against the OpenAPI document.
func (c *client) rawBody(method, path string, body []byte) reply {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	validateExchange(c.t, method, c.base+path, body, req.Header, resp.StatusCode, resp.Header, raw)
	r := reply{Status: resp.StatusCode, Raw: string(raw), Header: resp.Header}
	json.Unmarshal(raw, &r.Body)
	return r
}

func TestBackupRoutesAreForAdministratorsOnly(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	v, _ := e.newUser(admin, "viewer1", "viewer")
	anon := e.client()
	calls := func(c *client) []reply {
		return []reply{
			c.do("GET", "/api/v1/backup", nil),
			c.do("POST", "/api/v1/backup/stage", json.RawMessage(`{}`)),
			c.do("DELETE", "/api/v1/backup/stage/"+strings.Repeat("a", 32), nil),
			c.do("POST", "/api/v1/backup/jobs", map[string]string{"token": strings.Repeat("a", 32)}),
			c.do("GET", "/api/v1/backup/jobs/latest", nil),
			c.do("DELETE", "/api/v1/backup/jobs/1", nil),
		}
	}
	for _, r := range calls(anon) {
		expect(t, r, 401, "unauthenticated")
	}
	for _, r := range calls(v) {
		expect(t, r, 403, "forbidden")
	}
	// A change needs the CSRF token like every other change.
	admin.csrf = ""
	expect(t, admin.do("POST", "/api/v1/backup/jobs", map[string]string{"token": strings.Repeat("a", 32)}), 403, "csrf")
}

func TestBackupRoutesAreAbsentWithoutAService(t *testing.T) {
	e := newAuthEnv(t, func(o *api.Options) { o.Backup = nil })
	for _, p := range e.routes() {
		if strings.Contains(p, "/backup") {
			t.Errorf("registered without a service: %s", p)
		}
	}
}

func TestBackupAndRestoreOverHTTP(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	root := t.TempDir()
	expect(t, admin.do("POST", "/api/v1/shares", shareBody("media", root)), 201, "")
	expect(t, admin.do("POST", "/api/v1/shares", shareBody("photos", root)), 201, "")
	expect(t, admin.do("PUT", "/api/v1/extension-map/xyz", map[string]any{"category_id": 1}), 200, "")
	expect(t, admin.do("PUT", "/api/v1/auth/prefs", map[string]string{"language": "nl", "accent": "#0c9aa6"}), 200, "")

	// The download: a file, announced as one, whose size is known before the body starts, and that nothing in between keeps.
	dl := admin.do("GET", "/api/v1/backup", nil)
	expect(t, dl, 200, "")
	if cd := dl.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="nas-storage-analyzer-backup-`) || !strings.HasSuffix(cd, `.json"`) {
		t.Errorf("Content-Disposition: %q", cd)
	}
	if n, _ := strconv.Atoi(dl.Header.Get("Content-Length")); n != len(dl.Raw) || n == 0 {
		t.Errorf("Content-Length %q for %d bytes", dl.Header.Get("Content-Length"), len(dl.Raw))
	}
	if dl.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control: %q", dl.Header.Get("Cache-Control"))
	}
	for _, secret := range []string{"password", "totp", "recovery", "session", "csrf", pw} {
		if strings.Contains(strings.ToLower(dl.Raw), secret) {
			t.Errorf("the download contains %q", secret)
		}
	}

	// Change things, then restore: choose the file, see what it does, confirm.
	expect(t, admin.do("DELETE", "/api/v1/shares/2", nil), 204, "")
	expect(t, admin.do("DELETE", "/api/v1/extension-map/xyz", nil), 200, "")
	st := admin.do("POST", "/api/v1/backup/stage", json.RawMessage(dl.Raw))
	expect(t, st, 200, "")
	token, _ := st.Body["token"].(string)
	sum := st.Body["summary"].(map[string]any)
	ch := sum["changes"].(map[string]any)
	if len(token) != 32 || ch["shares_added"].([]any)[0] != "photos" || ch["file_types_changed"].(float64) != 1 || sum["scans_active"] != false {
		t.Fatalf("what the confirmation says: %s", st.Raw)
	}
	if n := admin.do("GET", "/api/v1/shares", nil).Body["items"].([]any); len(n) != 1 {
		t.Fatalf("choosing the file changed the shares: %d", len(n))
	}
	started := admin.do("POST", "/api/v1/backup/jobs", map[string]string{"token": token})
	expect(t, started, 202, "")
	id := int64(started.Body["id"].(float64))
	var job map[string]any
	waitFor(t, "the restore to finish", func() bool {
		r := admin.do("GET", "/api/v1/backup/jobs/latest", nil)
		expect(t, r, 200, "")
		job, _ = r.Body["job"].(map[string]any)
		return job != nil && job["status"] != "running"
	})
	if job["status"] != "done" || int64(job["id"].(float64)) != id || job["summary"] == nil || job["error_key"] != "" {
		t.Fatalf("%v", job)
	}
	if list := admin.do("GET", "/api/v1/shares", nil).Body["items"].([]any); len(list) != 2 {
		t.Errorf("the restore did not bring the share back: %d", len(list))
	}
	if r := admin.do("GET", "/api/v1/extension-map", nil); !strings.Contains(r.Raw, `"xyz"`) {
		t.Error("the restore did not bring the file type back")
	}

	// The token is used up, and the outcome is dismissed once it has been shown.
	expect(t, admin.do("POST", "/api/v1/backup/jobs", map[string]string{"token": token}), 404, "no_stage")
	expect(t, admin.do("DELETE", "/api/v1/backup/jobs/"+strconv.FormatInt(id, 10), nil), 204, "")
	if r := admin.do("GET", "/api/v1/backup/jobs/latest", nil); r.Body["job"] != nil {
		t.Errorf("a dismissed job is shown again: %s", r.Raw)
	}
	// Dismissing twice is fine: a screen whose first answer was lost must not be stuck on it.
	expect(t, admin.do("DELETE", "/api/v1/backup/jobs/"+strconv.FormatInt(id, 10), nil), 204, "")

	// Every step is in the audit log, with counts and no content.
	actions := map[string]bool{}
	list, err := e.svc.AuditPage(t.Context(), 0, 100, "backup.")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		actions[a.Action] = true
		if d, _ := json.Marshal(a.Detail); strings.Contains(string(d), "media") || strings.Contains(string(d), "photos") {
			t.Errorf("%s names a share: %s", a.Action, d)
		}
	}
	for _, want := range []string{"backup.create", "backup.restore.start", "backup.restore"} {
		if !actions[want] {
			t.Errorf("%s is not in the audit log (%v)", want, actions)
		}
	}
}

func TestARefusedBackupIsAnsweredWithTheReason(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	hdr := `"format":"nas-storage-analyzer-backup"`

	r := admin.rawBody("POST", "/api/v1/backup/stage", []byte(`this is not json`))
	expect(t, r, 400, "backup_not_backup")

	r = admin.rawBody("POST", "/api/v1/backup/stage", []byte(`{`+hdr+`,"format_version":2,"app_version":"7.0.0"}`))
	expect(t, r, 400, "backup_newer")
	if p, _ := r.Body["params"].(map[string]any); p["app_version"] != "7.0.0" {
		t.Errorf("the version that wrote the file must be in the answer: %s", r.Raw)
	}

	r = admin.rawBody("POST", "/api/v1/backup/stage", []byte(`{`+hdr+`,"format_version":1,"file_types":[{"ext":"x","category":"fruit"}]}`))
	expect(t, r, 400, "backup_invalid")
	if r.Body["field"] != "file_types[0].category" || r.Body["detail"] == "" {
		t.Errorf("a refusal says where in the file: %s", r.Raw)
	}

	big := append([]byte(`{`+hdr+`,"format_version":1}`), bytes.Repeat([]byte(" "), backup.MaxFileBytes)...)
	expect(t, admin.rawBody("POST", "/api/v1/backup/stage", big), 413, "backup_too_big")

	// A refused file is not kept, and nothing was started.
	expect(t, admin.do("POST", "/api/v1/backup/jobs", map[string]string{"token": strings.Repeat("0", 32)}), 404, "no_stage")
	expect(t, admin.do("POST", "/api/v1/backup/jobs", map[string]string{"token": "../../etc/passwd"}), 404, "no_stage")
	expect(t, admin.do("DELETE", "/api/v1/backup/stage/not-a-token", nil), 204, "")
	expect(t, admin.do("DELETE", "/api/v1/backup/jobs/99", nil), 404, "not_found")
	expect(t, admin.do("DELETE", "/api/v1/backup/jobs/zero", nil), 400, "")
	if r := admin.do("GET", "/api/v1/backup/jobs/latest", nil); r.Body["job"] != nil {
		t.Errorf("%s", r.Raw)
	}
}

func TestARestoreIsRefusedWhileAScanIsQueuedAndTheFileIsKept(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	expect(t, admin.do("POST", "/api/v1/shares", shareBody("media", t.TempDir())), 201, "")
	dl := admin.do("GET", "/api/v1/backup", nil)
	st := admin.do("POST", "/api/v1/backup/stage", json.RawMessage(dl.Raw))
	expect(t, st, 200, "")
	token := st.Body["token"].(string)
	expect(t, admin.do("POST", "/api/v1/shares/1/scan", map[string]string{"mode": "auto"}), 202, "") // nobody works the queue in this test
	expect(t, admin.do("POST", "/api/v1/backup/jobs", map[string]string{"token": token}), 409, "busy")
	expect(t, admin.do("DELETE", "/api/v1/shares/1/scan", nil), 204, "")
	expect(t, admin.do("POST", "/api/v1/backup/jobs", map[string]string{"token": token}), 202, "")
}
