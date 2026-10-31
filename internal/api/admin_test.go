package api_test

import (
	"bufio"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
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
