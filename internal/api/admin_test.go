package api_test

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/api"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
	"github.com/helmerznl/nas-storage-analyzer/internal/updates"
)

func shareBody(name, path string) map[string]any {
	return map[string]any{"name": name, "kind": "bind", "path": path}
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestShareManagementRequiresAdmin(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	expect(t, admin.do("POST", "/api/v1/users", map[string]string{"username": "viewer1", "password": pw, "role": "viewer"}), 201, "")
	v := e.client()
	v.do("POST", "/api/v1/auth/login", map[string]string{"username": "viewer1", "password": pw})
	sec := v.do("POST", "/api/v1/auth/totp/begin", nil).Body["secret"].(string)
	v.do("POST", "/api/v1/auth/totp/confirm", map[string]string{"code": codeFor(sec)})

	root := t.TempDir()
	for _, r := range []reply{
		v.do("POST", "/api/v1/shares", shareBody("x", root)),
		v.do("PATCH", "/api/v1/shares/1", map[string]any{"workers": 2}),
		v.do("DELETE", "/api/v1/shares/1", nil),
		v.do("POST", "/api/v1/shares/1/scan", nil),
		v.do("DELETE", "/api/v1/shares/1/scan", nil),
		v.do("POST", "/api/v1/shares/1/recategorize", nil),
		v.do("PUT", "/api/v1/extension-map/xyz", map[string]any{"category_id": 1}),
		v.do("DELETE", "/api/v1/extension-map/mkv", nil),
	} {
		expect(t, r, 403, "forbidden")
	}
	// Viewers read status, history and settings.
	expect(t, v.do("GET", "/api/v1/settings", nil), 200, "")
	expect(t, admin.do("POST", "/api/v1/shares", shareBody("media", root)), 201, "")
	expect(t, v.do("GET", "/api/v1/shares/1/history", nil), 200, "")
	expect(t, v.do("GET", "/api/v1/shares/1/jobs", nil), 200, "")
}

func TestFolderPickerIsAdminOnlyAndStaysInsideTheRoot(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "shares"), filepath.Join(base, "outside")
	for _, d := range []string{"media/Movies", "media/music", "backups"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	e := newAuthEnv(t, func(o *api.Options) { o.SharesRoot = root })
	browse := func(c *client, p string) reply {
		q := ""
		if p != "" {
			q = "?path=" + url.QueryEscape(p)
		}
		return c.do("GET", "/api/v1/fs/dirs"+q, nil)
	}

	// Nobody signed in, and a viewer, are refused.
	expect(t, browse(e.client(), ""), 401, "unauthenticated")
	admin, _, _ := e.setupAdmin()
	expect(t, admin.do("POST", "/api/v1/users", map[string]string{"username": "viewer1", "password": pw, "role": "viewer"}), 201, "")
	v := e.client()
	v.do("POST", "/api/v1/auth/login", map[string]string{"username": "viewer1", "password": pw})
	sec := v.do("POST", "/api/v1/auth/totp/begin", nil).Body["secret"].(string)
	v.do("POST", "/api/v1/auth/totp/confirm", map[string]string{"code": codeFor(sec)})
	expect(t, browse(v, ""), 403, "forbidden")

	// The root: folders only, sorted, no parent, and the link that leaves the root is not offered.
	r := browse(admin, "")
	expect(t, r, 200, "")
	dirs, _ := r.Body["dirs"].([]any)
	var got []string
	for _, d := range dirs {
		got = append(got, d.(map[string]any)["name"].(string))
	}
	if strings.Join(got, ",") != "backups,media" || r.Body["root"] != root || r.Body["path"] != root || r.Body["truncated"] != false {
		t.Errorf("root listing: %s", r.Raw)
	}
	if _, has := r.Body["parent"]; has {
		t.Errorf("the root has no parent: %s", r.Raw)
	}

	// A sub-folder names its parent, and the entries carry paths a share can use as they are.
	r = browse(admin, root+"/media")
	expect(t, r, 200, "")
	if r.Body["parent"] != root {
		t.Errorf("parent: %s", r.Raw)
	}
	dirs, _ = r.Body["dirs"].([]any)
	if len(dirs) != 2 || dirs[0].(map[string]any)["path"] != root+"/media/Movies" {
		t.Errorf("sub-folder listing: %s", r.Raw)
	}

	// Outside the root, through a link, and malformed paths are 400 with the field; a missing folder is 404.
	for _, p := range []string{"/etc", outside, root + "/../outside", root + "/escape", "media", root + "/notes.txt"} {
		r := browse(admin, p)
		if r.Status != 400 || r.Body["field"] != "path" {
			t.Errorf("%q: got %d %s", p, r.Status, r.Raw)
		}
	}
	expect(t, browse(admin, root+"/missing"), 404, "")
	// "/escape/.." is tidied to the root before anything is opened, so it lists the root.
	if r := browse(admin, root+"/escape/.."); r.Status != 200 || r.Body["path"] != root {
		t.Errorf("a path that tidies to the root: %d %s", r.Status, r.Raw)
	}
}

func TestFolderPickerWithoutAConfiguredRoot(t *testing.T) {
	e := newAuthEnv(t, nil) // no shares root
	admin, _, _ := e.setupAdmin()
	expect(t, admin.do("GET", "/api/v1/fs/dirs", nil), 409, "not_configured")
}

func TestCreateEditAndDeleteAShare(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	root := t.TempDir()

	r := admin.do("POST", "/api/v1/shares", shareBody("media", root))
	expect(t, r, 201, "")
	id := int64(r.Body["id"].(float64))
	if r.Body["workers"].(float64) != 16 || r.Body["last_used_basis"] != "atime" || len(r.Body["excludes"].([]any)) != 4 {
		t.Errorf("defaults not applied: %s", r.Raw)
	}
	if cfg := r.Body["config"].(map[string]any); cfg["path"] != root {
		t.Errorf("config = %v", cfg)
	}
	expect(t, admin.do("POST", "/api/v1/shares", shareBody("media", root)), 409, "conflict")

	bad := admin.do("POST", "/api/v1/shares", map[string]any{"name": "x", "kind": "bind", "path": "relative"})
	expect(t, bad, 400, "invalid_request")
	if bad.Body["field"] != "path" {
		t.Errorf("field = %v", bad.Body["field"])
	}
	expect(t, admin.do("POST", "/api/v1/shares", map[string]any{"name": "n", "kind": "nfs", "server": "nas", "export": "/x", "mount_options": "rw"}), 400, "invalid_request")
	expect(t, admin.do("POST", "/api/v1/shares", map[string]any{"name": "n2", "kind": "bind", "path": root, "cron": "nonsense"}), 400, "invalid_request")

	s := fmt.Sprintf("/api/v1/shares/%d", id)
	r = admin.do("PATCH", s, map[string]any{"workers": 4, "cron": "0 3 * * *", "last_used_basis": "mtime", "excludes": []string{"tmp"}})
	expect(t, r, 200, "")
	if r.Body["workers"].(float64) != 4 || r.Body["cron"] != "0 3 * * *" || r.Body["next_run_at"] == nil || r.Body["last_used_basis"] != "mtime" {
		t.Errorf("patched share = %s", r.Raw)
	}
	expect(t, admin.do("PATCH", s, map[string]any{"workers": 0}), 400, "invalid_request")
	expect(t, admin.do("PATCH", "/api/v1/shares/999", map[string]any{"workers": 2}), 404, "not_found")
	expect(t, admin.do("DELETE", s, nil), 204, "")
	expect(t, admin.do("GET", s, nil), 404, "")
	expect(t, admin.do("DELETE", s, nil), 404, "")

	// The audit log records it, without secrets.
	a := admin.do("GET", "/api/v1/audit?action=share.", nil)
	for _, want := range []string{"share.create", "share.update", "share.delete"} {
		if !strings.Contains(a.Raw, want) {
			t.Errorf("audit lacks %s", want)
		}
	}
}

func TestScanFromTheAPIThroughTheWorkerWithHistory(t *testing.T) {
	e := newAuthEnv(t, nil)
	e.startWorker()
	admin, _, _ := e.setupAdmin()
	root := t.TempDir()
	ts.Write(t, root+"/video/a.mkv", 5000)
	ts.Write(t, root+"/docs/b.pdf", 100)
	r := admin.do("POST", "/api/v1/shares", shareBody("media", root))
	id := int64(r.Body["id"].(float64))
	s := fmt.Sprintf("/api/v1/shares/%d", id)

	// Nothing scanned yet.
	expect(t, admin.do("GET", s+"/dirs/root/children", nil), 409, "no_data")
	r = admin.do("POST", s+"/scan", map[string]string{"mode": "auto"})
	expect(t, r, 202, "")
	if r.Body["state"] != "queued" || r.Body["requested_by"] != "admin" {
		t.Errorf("job = %s", r.Raw)
	}
	expect(t, admin.do("POST", s+"/scan", nil), 409, "already_active")
	expect(t, admin.do("POST", s+"/scan", map[string]string{"mode": "weird"}), 400, "")

	waitFor(t, "the scan to finish", func() bool {
		sh := admin.do("GET", s, nil)
		return sh.Body["active_job"] == nil && sh.Body["last_scan"] != nil
	})
	sh := admin.do("GET", s, nil)
	last := sh.Body["last_scan"].(map[string]any)
	if last["status"] != "succeeded" || last["bytes"].(float64) != 5100 || last["trigger"] != "manual" {
		t.Errorf("last scan = %v", last)
	}
	ch := admin.do("GET", s+"/dirs/root/children", nil)
	expect(t, ch, 200, "")
	if items := ch.Body["items"].([]any); len(items) != 2 || items[0].(map[string]any)["name"] != "video" {
		t.Errorf("children = %s", ch.Raw)
	}

	// A second scan adds a history point; growth is visible.
	ts.Settle()
	ts.Write(t, root+"/video/c.mkv", 7000)
	expect(t, admin.do("POST", s+"/scan", map[string]string{"mode": "full"}), 202, "")
	waitFor(t, "the second scan", func() bool {
		h := admin.do("GET", s+"/history", nil)
		return len(h.Body["items"].([]any)) == 2
	})
	h := admin.do("GET", s+"/history", nil).Body["items"].([]any)
	if h[0].(map[string]any)["size"].(float64) != 5100 || h[1].(map[string]any)["size"].(float64) != 12100 {
		t.Errorf("history = %v", h)
	}
	if dh := admin.do("GET", s+"/dirs/root/history", nil).Body["items"].([]any); len(dh) != 2 {
		t.Errorf("root history = %v", dh)
	}
	jl := admin.do("GET", s+"/jobs", nil).Body["items"].([]any)
	if len(jl) != 2 || jl[0].(map[string]any)["state"] != "done" {
		t.Errorf("jobs = %v", jl)
	}
	// Cancelling with nothing running is a 404.
	expect(t, admin.do("DELETE", s+"/scan", nil), 404, "")
}

func TestCancelAQueuedScan(t *testing.T) {
	e := newAuthEnv(t, nil) // no worker: the job stays queued
	admin, _, _ := e.setupAdmin()
	r := admin.do("POST", "/api/v1/shares", shareBody("media", t.TempDir()))
	s := fmt.Sprintf("/api/v1/shares/%d", int64(r.Body["id"].(float64)))
	expect(t, admin.do("POST", s+"/scan", nil), 202, "")
	sh := admin.do("GET", s, nil)
	if aj, _ := sh.Body["active_job"].(map[string]any); aj == nil || aj["state"] != "queued" {
		t.Errorf("active job = %v", sh.Body["active_job"])
	}
	expect(t, admin.do("DELETE", s, nil), 409, "busy") // cannot delete while queued
	expect(t, admin.do("DELETE", s+"/scan", nil), 204, "")
	if sh = admin.do("GET", s, nil); sh.Body["active_job"] != nil {
		t.Errorf("job still active: %v", sh.Body["active_job"])
	}
	expect(t, admin.do("DELETE", s, nil), 204, "")
}

func TestEditingTheExtensionMapRecategorizes(t *testing.T) {
	e := newAuthEnv(t, nil)
	e.startWorker()
	admin, _, _ := e.setupAdmin()
	root := t.TempDir()
	ts.Write(t, root+"/a.mkv", 1000)
	ts.Write(t, root+"/b.zzz", 10)
	r := admin.do("POST", "/api/v1/shares", shareBody("media", root))
	s := fmt.Sprintf("/api/v1/shares/%d", int64(r.Body["id"].(float64)))
	admin.do("POST", s+"/scan", nil)
	waitFor(t, "first scan", func() bool { return admin.do("GET", s, nil).Body["last_scan"] != nil })
	cats := func() map[string]float64 {
		out := map[string]float64{}
		for _, c := range admin.do("GET", s+"/categories", nil).Body["items"].([]any) {
			m := c.(map[string]any)
			out[m["key"].(string)] = m["size"].(float64)
		}
		return out
	}
	if c := cats(); c["video"] != 1000 || c["other"] != 10 {
		t.Fatalf("categories = %v", c)
	}
	expect(t, admin.do("PUT", "/api/v1/extension-map/ZZZ", map[string]any{"category_id": 4}), 200, "") // case-insensitive
	expect(t, admin.do("PUT", "/api/v1/extension-map/mkv", map[string]any{"category_id": 5}), 200, "")
	waitFor(t, "recategorize", func() bool { c := cats(); return c["archives"] == 1000 && c["documents"] == 10 })
	if c := cats(); c["video"] != 0 || c["other"] != 0 {
		t.Errorf("categories after recategorize = %v", c)
	}
	// Removing a mapping puts files back in "other".
	r = admin.do("DELETE", "/api/v1/extension-map/zzz", nil)
	expect(t, r, 200, "")
	waitFor(t, "second recategorize", func() bool { return cats()["other"] == 10 })
	expect(t, admin.do("DELETE", "/api/v1/extension-map/zzz", nil), 404, "")
	expect(t, admin.do("PUT", "/api/v1/extension-map/bad.ext", map[string]any{"category_id": 1}), 400, "")
	expect(t, admin.do("PUT", "/api/v1/extension-map/xyz", map[string]any{"category_id": 99}), 400, "")
}

func TestPreferences(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	r := admin.do("PUT", "/api/v1/auth/prefs", map[string]string{"units": "decimal", "language": "en"})
	expect(t, r, 200, "")
	me := admin.do("GET", "/api/v1/auth/me", nil).Body["user"].(map[string]any)["prefs"].(map[string]any)
	if me["units"] != "decimal" || me["language"] != "en" {
		t.Errorf("prefs = %v", me)
	}
	admin.do("PUT", "/api/v1/auth/prefs", map[string]string{"theme": "dark"})
	me = admin.do("GET", "/api/v1/auth/me", nil).Body["user"].(map[string]any)["prefs"].(map[string]any)
	if me["theme"] != "dark" || me["units"] != "decimal" {
		t.Errorf("prefs not merged: %v", me)
	}
	expect(t, admin.do("PUT", "/api/v1/auth/prefs", map[string]string{"units": "furlongs"}), 400, "")
	expect(t, admin.do("PUT", "/api/v1/auth/prefs", map[string]string{"colour": "red"}), 400, "")
}

// The accent colour is a free #rrggbb that ends up in a CSS custom property, so only that shape is stored, and in lower case.
func TestAccentPreference(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	prefs := func() map[string]any {
		return admin.do("GET", "/api/v1/auth/me", nil).Body["user"].(map[string]any)["prefs"].(map[string]any)
	}
	expect(t, admin.do("PUT", "/api/v1/auth/prefs", map[string]string{"accent": "#B0213F"}), 200, "")
	if prefs()["accent"] != "#b0213f" {
		t.Errorf("accent = %v, want it stored in lower case", prefs()["accent"])
	}
	for _, bad := range []string{"red", "#fff", "#12345", "#1234567", "#gggggg", "b0213f", "#b0213f;color:red", "url(//evil)", "#b0213f ", " #b0213f", "#b0213f\n"} {
		expect(t, admin.do("PUT", "/api/v1/auth/prefs", map[string]string{"accent": bad}), 400, "")
	}
	if prefs()["accent"] != "#b0213f" {
		t.Errorf("a refused value changed the stored accent: %v", prefs()["accent"])
	}
	expect(t, admin.do("PUT", "/api/v1/auth/prefs", map[string]string{"accent": ""}), 200, "") // back to the default
	if prefs()["accent"] != "" {
		t.Errorf("accent = %v after reset, want empty", prefs()["accent"])
	}
	// One bad value refuses the whole request: nothing of it is stored.
	expect(t, admin.do("PUT", "/api/v1/auth/prefs", map[string]string{"theme": "dark", "accent": "nope"}), 400, "")
	if _, stored := prefs()["theme"]; stored {
		t.Errorf("a refused request stored part of itself: %v", prefs())
	}
}

func TestLiveStatusStream(t *testing.T) {
	e := newAuthEnv(t, nil)
	e.startWorker()
	admin, _, _ := e.setupAdmin()
	root := t.TempDir()
	for i := 0; i < 60; i++ { // slowed to 10 operations a second, about 5 s
		ts.Write(t, fmt.Sprintf("%s/f%02d.txt", root, i), 1)
	}
	body := shareBody("media", root)
	body["max_ops_per_sec"] = 10
	r := admin.do("POST", "/api/v1/shares", body)
	s := fmt.Sprintf("/api/v1/shares/%d", int64(r.Body["id"].(float64)))

	// Unauthenticated clients get no stream.
	expect(t, e.client().do("GET", "/api/v1/events", nil), 401, "unauthenticated")

	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/events", nil)
	req.Header.Set("Accept", "text/event-stream")
	u := admin.hc.Jar
	hc := &http.Client{Jar: u}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	lines := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	next := func() string {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("stream closed")
			}
			return l
		case <-time.After(10 * time.Second):
			t.Fatal("no event in time")
		}
		return ""
	}
	if l := next(); l != "event: status" {
		t.Fatalf("first line %q", l)
	}
	if l := next(); !strings.Contains(l, `"latest_scan_id":null`) {
		t.Errorf("initial state = %s", l)
	}
	admin.do("POST", s+"/scan", nil)
	sawJob, sawProgress, sawDone := false, false, false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !sawDone {
		l := next()
		if strings.HasPrefix(l, "data: ") {
			if strings.Contains(l, `"job":{`) {
				sawJob = true
			}
			if strings.Contains(l, `"progress":{`) && strings.Contains(l, `"files_per_sec"`) {
				sawProgress = true
			}
			if strings.Contains(l, `"latest_scan_status":"succeeded"`) && !strings.Contains(l, `"job":{`) {
				sawDone = true
			}
		}
	}
	if !sawJob || !sawProgress || !sawDone {
		t.Errorf("job event seen=%v, progress seen=%v, completion event seen=%v", sawJob, sawProgress, sawDone)
	}
}

// fakeGitHubFor answers like GitHub does for /releases/latest: a redirect to the tag page.
func fakeGitHubFor(t *testing.T, tag string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Location", "/"+updates.DefaultRepo+"/releases/tag/"+tag)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The update check tells the administrator, and only the administrator; it announces and does nothing else.
func TestUpdateCheckIsForTheAdministratorOnly(t *testing.T) {
	var hits atomic.Int32
	gh := fakeGitHubFor(t, "v0.9.0", &hits)
	checker := updates.New("0.1.0", true, nil)
	checker.BaseURL = gh.URL
	e := newAuthEnv(t, func(o *api.Options) { o.Updates = checker })

	// Nobody signed in: refused, and GitHub was not asked.
	anon := e.client()
	expect(t, anon.do("GET", "/api/v1/system/update", nil), 401, "unauthenticated")
	expect(t, anon.do("POST", "/api/v1/system/update/check", nil), 401, "unauthenticated")

	admin, _, _ := e.setupAdmin()
	expect(t, admin.do("POST", "/api/v1/users", map[string]string{"username": "viewer1", "password": pw, "role": "viewer"}), 201, "")
	v := e.client()
	v.do("POST", "/api/v1/auth/login", map[string]string{"username": "viewer1", "password": pw})
	sec := v.do("POST", "/api/v1/auth/totp/begin", nil).Body["secret"].(string)
	v.do("POST", "/api/v1/auth/totp/confirm", map[string]string{"code": codeFor(sec)})
	expect(t, v.do("GET", "/api/v1/system/update", nil), 403, "forbidden")
	expect(t, v.do("POST", "/api/v1/system/update/check", nil), 403, "forbidden")
	if hits.Load() != 0 {
		t.Fatalf("a refused request made GitHub get asked %d times", hits.Load())
	}

	// The administrator gets the status (the client also validates every answer against the OpenAPI document).
	r := admin.do("GET", "/api/v1/system/update", nil)
	expect(t, r, 200, "")
	if r.Body["enabled"] != true || r.Body["current"] != "0.1.0" || r.Body["latest"] != "0.9.0" || r.Body["available"] != true ||
		r.Body["error"] != "" || r.Body["repo"] != updates.DefaultRepo || r.Body["url"] != gh.URL+"/"+updates.DefaultRepo+"/releases/tag/v0.9.0" {
		t.Errorf("status = %s", r.Raw)
	}
	admin.do("GET", "/api/v1/system/update", nil)
	if hits.Load() != 1 {
		t.Errorf("a page view called GitHub %d times, want 1 (the answer is remembered)", hits.Load())
	}
	// "Check now" inside the 30 second floor answers with the remembered status instead of asking again.
	expect(t, admin.do("POST", "/api/v1/system/update/check", nil), 200, "")
	if hits.Load() != 1 {
		t.Errorf("check now went through inside the floor: %d requests", hits.Load())
	}
	// A changing request needs the CSRF token like every other one.
	expect(t, admin.do("POST", "/api/v1/system/update/check", nil, "X-CSRF-Token", ""), 403, "csrf")
}

// Switched off is reported as off, not as a failure, and without a single request.
func TestUpdateCheckCanBeSwitchedOff(t *testing.T) {
	var hits atomic.Int32
	gh := fakeGitHubFor(t, "v0.9.0", &hits)
	checker := updates.New("0.1.0", false, nil)
	checker.BaseURL = gh.URL
	e := newAuthEnv(t, func(o *api.Options) { o.Updates = checker })
	admin, _, _ := e.setupAdmin()
	r := admin.do("GET", "/api/v1/system/update", nil)
	expect(t, r, 200, "")
	if r.Body["enabled"] != false || r.Body["error"] != "" || r.Body["available"] != false || hits.Load() != 0 {
		t.Errorf("status = %s, GitHub asked %d times", r.Raw, hits.Load())
	}
}

// Without a checker the routes do not exist at all.
func TestUpdateRoutesAreAbsentWithoutAChecker(t *testing.T) {
	e := newAuthEnv(t, func(o *api.Options) { o.Updates = nil })
	for _, path := range []string{"/api/v1/system/update", "/api/v1/system/update/check"} {
		resp, err := http.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s = %d, want 404 or 405", path, resp.StatusCode)
		}
	}
}
