package api_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

// scriptShare creates a share through the API, has the worker scan it, and returns its URL and the file ids by path.
func scriptShare(t *testing.T, e *authEnv, admin *client, name, root string) (url string, ids map[string]int64) {
	t.Helper()
	r := admin.do("POST", "/api/v1/shares", shareBody(name, root))
	expect(t, r, 201, "")
	url = fmt.Sprintf("/api/v1/shares/%d", int64(r.Body["id"].(float64)))
	expect(t, admin.do("POST", url+"/scan", nil), 202, "")
	waitFor(t, "the scan to finish", func() bool {
		sh := admin.do("GET", url, nil)
		return sh.Body["active_job"] == nil && sh.Body["last_scan"] != nil
	})
	fl := admin.do("GET", url+"/files?recursive=true&limit=500", nil)
	expect(t, fl, 200, "")
	ids = map[string]int64{}
	for _, it := range fl.Body["items"].([]any) {
		m := it.(map[string]any)
		ids[m["path"].(string)] = int64(m["id"].(float64))
	}
	return url, ids
}

func idList(ids map[string]int64, paths ...string) []int64 {
	var out []int64
	for _, p := range paths {
		out = append(out, ids[p])
	}
	return out
}

func TestDeleteScriptIsAdminOnly(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	v, _ := e.newUser(admin, "viewer1", "viewer")
	body := map[string]any{"ids": []int64{1}, "script": "sh", "client_root": "/mnt/x"}
	const path = "/api/v1/shares/1/delete-script"

	expect(t, e.client().do("POST", path, body), 401, "unauthenticated")
	expect(t, v.do("POST", path, body), 403, "forbidden")
	expect(t, admin.do("POST", path, body, "X-CSRF-Token", ""), 403, "csrf")
	expect(t, admin.do("POST", path, body, "Origin", "https://evil.example"), 403, "csrf")
}

// The whole way: a share is scanned, files are picked by id, the endpoint writes a script, and running that script
// removes those files and no others. The application itself deletes nothing.
func TestDeleteScriptDeletesExactlyTheSelectionWhenRun(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		if strings.Contains(os.Getenv("NASA_REQUIRE_SHELLS"), "sh") {
			t.Fatalf("sh is required by NASA_REQUIRE_SHELLS but is not installed")
		}
		t.Skip("sh is not installed")
	}
	e := newAuthEnv(t, nil)
	e.startWorker()
	admin, _, _ := e.setupAdmin()
	root := t.TempDir()
	const odd = "docs/it's & $(echo x) `y` 100%.txt"
	for p, size := range map[string]int{
		"movies/old/a.mkv": 100, "movies/old/b.mkv": 200, "movies/keep.mkv": 300, "movies/old/a.mkv.bak": 400,
		odd: 50, "docs/other.txt": 60,
	} {
		ts.Write(t, filepath.Join(root, p), size)
	}
	s, ids := scriptShare(t, e, admin, "media", root)
	pick := []string{"/movies/old/a.mkv", "/movies/old/b.mkv", "/" + odd}

	r := admin.do("POST", s+"/delete-script", map[string]any{"ids": idList(ids, pick...), "script": "sh", "client_root": root + "/"})
	expect(t, r, 200, "")
	if ct := r.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("content type = %q", ct)
	}
	if got := []string{r.Header.Get("X-Files"), r.Header.Get("X-Skipped"), r.Header.Get("X-Bytes")}; strings.Join(got, ",") != "3,0,350" {
		t.Errorf("X-Files, X-Skipped, X-Bytes = %v", got)
	}
	if cd := r.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment; filename=delete-media-") || !strings.HasSuffix(cd, ".sh") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if !strings.HasPrefix(r.Raw, "#!/bin/sh\n") || strings.Contains(r.Raw, "\r") {
		t.Errorf("not a sh script with LF line ends: %.80q", r.Raw)
	}
	// Nothing was deleted by asking for a script.
	for p := range ids {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Fatalf("%s is gone after only asking for a script: %v", p, err)
		}
	}

	file := filepath.Join(t.TempDir(), "delete.sh")
	if err := os.WriteFile(file, []byte(r.Raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, file)
	cmd.Stdin = strings.NewReader("DELETE 3\n")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Done: 3 deleted, 0 kept, 0 already gone, 0 failed.") {
		t.Fatalf("running the script: %v\n%s", err, out)
	}
	for p := range ids {
		_, statErr := os.Stat(filepath.Join(root, p))
		wantGone := p == "/movies/old/a.mkv" || p == "/movies/old/b.mkv" || p == "/"+odd
		if wantGone != os.IsNotExist(statErr) {
			t.Errorf("%s: gone=%v, want %v", p, os.IsNotExist(statErr), wantGone)
		}
	}

	// The log says what was handed out, in numbers and a hash, and never a name.
	sum := sha256.Sum256([]byte(r.Raw))
	a := admin.do("GET", "/api/v1/audit?action=script.", nil)
	expect(t, a, 200, "")
	for _, want := range []string{"script.generate", hex.EncodeToString(sum[:]), `"files":3`, `"script":"sh"`, "media"} {
		if !strings.Contains(a.Raw, want) {
			t.Errorf("audit lacks %s: %s", want, a.Raw)
		}
	}
	for _, leak := range []string{"a.mkv", "movies", "docs", "echo"} {
		if strings.Contains(a.Raw, leak) {
			t.Errorf("audit holds part of a file name (%q): %s", leak, a.Raw)
		}
	}
}

func TestDeleteScriptUsesTheShareBasePathAndTheTargetKind(t *testing.T) {
	e := newAuthEnv(t, nil)
	e.startWorker()
	admin, _, _ := e.setupAdmin()
	root := t.TempDir()
	ts.Write(t, root+"/a/one.bin", 10)
	ts.Write(t, root+"/a/two.bin", 20)
	s, ids := scriptShare(t, e, admin, "Films é", root) // a name that needs the encoded form of the file name
	both := idList(ids, "/a/one.bin", "/a/two.bin")
	post := func(script, clientRoot, lang string) reply {
		b := map[string]any{"ids": both, "script": script}
		if clientRoot != "" {
			b["client_root"] = clientRoot
		}
		if lang != "" {
			b["lang"] = lang
		}
		return admin.do("POST", s+"/delete-script", b)
	}

	// No base path anywhere: the request says so, and names the field.
	r := post("sh", "", "")
	expect(t, r, 400, "invalid_request")
	if r.Body["field"] != "client_root" {
		t.Errorf("field = %v", r.Body["field"])
	}

	// The share's own base paths, one per kind of machine.
	expect(t, admin.do("PATCH", s, map[string]any{"client_posix": "/mnt/user//films/", "client_windows": "//NAS/Films"}), 200, "")
	r = post("sh", "", "")
	expect(t, r, 200, "")
	if !strings.Contains(r.Raw, "\nR|/mnt/user/films\n") {
		t.Errorf("the share's Linux base path is not in the script:\n%.600s", r.Raw)
	}
	if cd := r.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment; filename*=utf-8''delete-Films-%C3%A9-") || !strings.HasSuffix(cd, ".sh") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	r = post("powershell", "", "")
	expect(t, r, 200, "")
	if !strings.HasPrefix(r.Raw, "\xef\xbb\xbf") || !strings.Contains(r.Raw, "R|\\\\NAS\\Films\r\n") || !strings.HasSuffix(r.Header.Get("Content-Disposition"), ".ps1") {
		t.Errorf("PowerShell script: %.300q", r.Raw)
	}
	r = post("cmd", "z:", "")
	expect(t, r, 200, "")
	if !strings.Contains(r.Raw, "R|Z:\\\r\n") || !strings.Contains(r.Raw, "\r\n") || !strings.HasSuffix(r.Header.Get("Content-Disposition"), ".cmd") {
		t.Errorf("cmd script: %.300q", r.Raw)
	}
	// What is typed in the dialog wins over the share's, and the language is the caller's.
	r = post("sh", "/srv/elsewhere", "nl")
	expect(t, r, 200, "")
	if !strings.Contains(r.Raw, "\nR|/srv/elsewhere\n") || !strings.Contains(r.Raw, "verwijderscript") || strings.Contains(r.Raw, "/mnt/user/films") {
		t.Errorf("override or language not applied:\n%.600s", r.Raw)
	}

	// A base path of the wrong kind of machine is refused, as is one that could break out of a script.
	for _, c := range []struct{ script, root string }{
		{"sh", `\\nas\films`}, {"cmd", "/mnt/user/films"}, {"powershell", "/mnt/user/films"},
		{"sh", "/mnt/a\nrm -rf ~"}, {"sh", "relative"}, {"sh", "/mnt/../etc"}, {"cmd", `\\nas\100%`}, {"powershell", `\\?\C:\x`},
	} {
		r := post(c.script, c.root, "")
		if r.Status != 400 || r.Body["field"] != "client_root" {
			t.Errorf("%s with base path %q: %d %s", c.script, c.root, r.Status, r.Raw)
		}
	}
	// The share's own base paths are checked when they are saved, not only when a script is made.
	r = admin.do("PATCH", s, map[string]any{"client_windows": "/not/windows"})
	if r.Status != 400 || r.Body["field"] != "client_windows" {
		t.Errorf("a Linux path as the Windows base path: %d %s", r.Status, r.Raw)
	}
}

func TestDeleteScriptRefusesBadRequests(t *testing.T) {
	e := newAuthEnv(t, nil)
	e.startWorker()
	admin, _, _ := e.setupAdmin()
	root := t.TempDir()
	ts.Write(t, root+"/a.bin", 10)
	s, ids := scriptShare(t, e, admin, "media", root)
	one := idList(ids, "/a.bin")

	tooMany := make([]int64, 2001)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	for name, c := range map[string]struct {
		body  map[string]any
		field string
	}{
		"no ids":        {map[string]any{"script": "sh", "client_root": "/x"}, "ids"},
		"empty ids":     {map[string]any{"ids": []int64{}, "script": "sh", "client_root": "/x"}, "ids"},
		"2001 ids":      {map[string]any{"ids": tooMany, "script": "sh", "client_root": "/x"}, "ids"},
		"no script":     {map[string]any{"ids": one, "client_root": "/x"}, "script"},
		"unknown":       {map[string]any{"ids": one, "script": "bash", "client_root": "/x"}, "script"},
		"unknown lang":  {map[string]any{"ids": one, "script": "sh", "client_root": "/x", "lang": "fr"}, "lang"},
		"id zero":       {map[string]any{"ids": []int64{0}, "script": "sh", "client_root": "/x"}, "ids"},
		"id negative":   {map[string]any{"ids": append([]int64{-3}, one...), "script": "sh", "client_root": "/x"}, "ids"},
		"id not number": {map[string]any{"ids": []string{"a"}, "script": "sh", "client_root": "/x"}, "ids"},
	} {
		r := admin.do("POST", s+"/delete-script", c.body)
		if r.Status != 400 {
			t.Errorf("%s: status %d: %s", name, r.Status, r.Raw)
			continue
		}
		if c.field != "ids" || name == "no ids" || name == "empty ids" || name == "2001 ids" {
			if r.Body["field"] != c.field {
				t.Errorf("%s: field = %v, want %s", name, r.Body["field"], c.field)
			}
		}
	}
	// The limit is exactly 2000: the same ids, one fewer, get past the check on their number.
	unknown := make([]int64, 2000)
	for i := range unknown {
		unknown[i] = 900_000_000 + int64(i)
	}
	r := admin.do("POST", s+"/delete-script", map[string]any{"ids": unknown, "script": "sh", "client_root": "/x"})
	if r.Status != 400 || !strings.Contains(r.Raw, "none of the selected files") {
		t.Errorf("2000 ids that are all unknown: %d %s", r.Status, r.Raw)
	}

	expect(t, admin.do("POST", "/api/v1/shares/987654/delete-script", map[string]any{"ids": one, "script": "sh", "client_root": "/x"}), 404, "")
	expect(t, admin.do("POST", "/api/v1/shares/abc/delete-script", map[string]any{"ids": one, "script": "sh", "client_root": "/x"}), 400, "")

	// A share that has not been scanned has no files to name.
	fresh := admin.do("POST", "/api/v1/shares", shareBody("fresh", t.TempDir()))
	expect(t, fresh, 201, "")
	expect(t, admin.do("POST", fmt.Sprintf("/api/v1/shares/%d/delete-script", int64(fresh.Body["id"].(float64))),
		map[string]any{"ids": one, "script": "sh", "client_root": "/x"}), 409, "no_data")
}

// Ids that are not files of this share are told apart from nothing: they are "gone", whatever share they belong to,
// so the answer says nothing about another share.
func TestDeleteScriptLeavesOutWhatCannotBeNamedSafely(t *testing.T) {
	e := newAuthEnv(t, nil)
	e.startWorker()
	admin, _, _ := e.setupAdmin()
	root, otherRoot := t.TempDir(), t.TempDir()
	for p, size := range map[string]int{"ok/plain.txt": 10, "ok/Twin.txt": 20, "ok/twin.txt": 30, "ok/new\nline.txt": 40, "ok/empty.bin": 0} {
		ts.Write(t, filepath.Join(root, p), size)
	}
	ts.Write(t, otherRoot+"/private-name.txt", 5)
	s, ids := scriptShare(t, e, admin, "media", root)
	_, otherIDs := scriptShare(t, e, admin, "other", otherRoot)
	foreign := otherIDs["/private-name.txt"]

	post := func(script, rootPath string, picks ...int64) reply {
		return admin.do("POST", s+"/delete-script", map[string]any{"ids": picks, "script": script, "client_root": rootPath})
	}
	all := append(idList(ids, "/ok/plain.txt", "/ok/Twin.txt", "/ok/twin.txt", "/ok/new\nline.txt", "/ok/empty.bin"), foreign, 987654321)

	r := post("sh", "/mnt/x", all...)
	expect(t, r, 200, "")
	// plain.txt and the empty file go in; the twins, the name with a line break and the two unknown ids stay out.
	if got := []string{r.Header.Get("X-Files"), r.Header.Get("X-Skipped")}; strings.Join(got, ",") != "2,5" {
		t.Errorf("X-Files, X-Skipped = %v\n%s", got, r.Raw)
	}
	for _, want := range []string{"F|10|", "|/ok/plain.txt\n", "|/ok/empty.bin\n", "id " + strconv.FormatInt(foreign, 10), "id 987654321", "differs only in upper or lower case"} {
		if !strings.Contains(r.Raw, want) {
			t.Errorf("script lacks %q:\n%s", want, r.Raw)
		}
	}
	if strings.Contains(r.Raw, "private-name") {
		t.Error("the script names a file of another share")
	}
	// The name with a line break is shown escaped in a data line, and is nowhere as a line of its own.
	if strings.Contains(r.Raw, "\nline.txt") || !strings.Contains(r.Raw, `new\x0Aline.txt`) {
		t.Errorf("the line break in a name is not neutralised:\n%s", r.Raw)
	}

	// For cmd an empty file cannot be told from a folder, so it is left out there.
	r = post("cmd", `Z:\`, idList(ids, "/ok/plain.txt", "/ok/empty.bin")...)
	expect(t, r, 200, "")
	if got := []string{r.Header.Get("X-Files"), r.Header.Get("X-Skipped")}; strings.Join(got, ",") != "1,1" {
		t.Errorf("cmd: X-Files, X-Skipped = %v", got)
	}

	// Nothing left to put in a script is a refusal that says why, not an empty script.
	r = post("sh", "/mnt/x", idList(ids, "/ok/Twin.txt", "/ok/twin.txt")...)
	if r.Status != 400 || r.Body["field"] != "ids" || !strings.Contains(r.Raw, "ambiguous 2") {
		t.Errorf("only twins: %d %s", r.Status, r.Raw)
	}
	r = post("sh", "/mnt/x", foreign)
	if r.Status != 400 || !strings.Contains(r.Raw, "gone 1") || strings.Contains(r.Raw, "private-name") {
		t.Errorf("only a foreign id: %d %s", r.Status, r.Raw)
	}
}
