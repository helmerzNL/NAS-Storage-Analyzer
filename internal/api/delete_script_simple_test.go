package api_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

// The simple script through the API: the same selection, asked for as a .txt with one command per file or folder. Running it, as a person does
// who pastes it into an SSH session, removes what was picked and nothing else.
func TestSimpleDeleteScriptIsWhatAPersonPastesIntoAnSSHSession(t *testing.T) {
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
	const odd = "docs/it's & $(echo x) `y` 100% !.txt"
	for p, size := range map[string]int{
		"movies/Heat (1995).mkv": 100, "movies/keep.mkv": 300, "movies/Heat (1995).mkv.bak": 400, odd: 50, "docs/other.txt": 60,
		"series/old/s01/a.mkv": 10, "series/old/s02/b.mkv": 20, "series/old2/keep.mkv": 30,
	} {
		ts.Write(t, filepath.Join(root, p), size)
	}
	s, files := scriptShare(t, e, admin, "media", root)
	dirs, _ := scriptDirs(t, admin, s)

	r := admin.do("POST", s+"/delete-script", map[string]any{
		"ids": idList(files, "/movies/Heat (1995).mkv", "/"+odd), "dir_ids": []int64{dirs["/series/old"]},
		"script": "sh", "client_root": root, "style": "simple",
	})
	expect(t, r, 200, "")
	if cd := r.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment; filename=delete-media-") || !strings.HasSuffix(cd, ".txt") {
		t.Errorf("Content-Disposition = %q, want a .txt", cd)
	}
	// Three commands: two files and a folder. A simple script asks nothing, so the number is not one to type.
	if got := []string{r.Header.Get("X-Files"), r.Header.Get("X-Folders"), r.Header.Get("X-Items"), r.Header.Get("X-Skipped")}; strings.Join(got, ",") != "2,1,3,0" {
		t.Errorf("X-Files, X-Folders, X-Items, X-Skipped = %v, want 2,1,3,0", got)
	}
	if strings.HasPrefix(r.Raw, "#!") || strings.Contains(r.Raw, "\r") || strings.Contains(r.Raw, "DELETE") || strings.Contains(r.Raw, "\nN|") {
		t.Errorf("not a plain list of commands with LF line ends: %.200q", r.Raw)
	}
	for _, want := range []string{
		"rm -f -- '" + root + "/movies/Heat (1995).mkv'\n",
		"rm -f -- '" + root + "/docs/it'\\''s & $(echo x) `y` 100% !.txt'\n",
		"rm -rf -- '" + root + "/series/old'\n",
	} {
		if !strings.Contains(r.Raw, want) {
			t.Errorf("the script lacks the line %q:\n%s", want, r.Raw)
		}
	}
	for p := range files { // nothing was deleted by asking
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Fatalf("%s is gone after only asking for a script: %v", p, err)
		}
	}

	// Pasted: the text goes to the shell's standard input, the way a terminal gets it.
	cmd := exec.Command(sh)
	cmd.Stdin = strings.NewReader(r.Raw)
	if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("pasting the script: %v\n%s", err, out)
	}
	for _, gone := range []string{"movies/Heat (1995).mkv", odd, "series/old"} {
		if _, err := os.Stat(filepath.Join(root, gone)); !os.IsNotExist(err) {
			t.Errorf("%s must be gone: %v", gone, err)
		}
	}
	for _, kept := range []string{"movies/keep.mkv", "movies/Heat (1995).mkv.bak", "docs/other.txt", "series/old2/keep.mkv", "series"} {
		if _, err := os.Stat(filepath.Join(root, kept)); err != nil {
			t.Errorf("%s must survive: %v", kept, err)
		}
	}

	// The log says what was handed out, which style it was, and never a name.
	a := admin.do("GET", "/api/v1/audit?action=script.", nil)
	expect(t, a, 200, "")
	for _, want := range []string{"script.generate", `"style":"simple"`, `"files":2`, `"folders":1`, `"script":"sh"`} {
		if !strings.Contains(a.Raw, want) {
			t.Errorf("audit lacks %s: %s", want, a.Raw)
		}
	}
	for _, leak := range []string{"Heat", "movies", "series", "echo", "docs"} {
		if strings.Contains(a.Raw, leak) {
			t.Errorf("audit holds part of a name (%q): %s", leak, a.Raw)
		}
	}
}

func TestDeleteScriptStyleIsChosenPerRequestAndDefaultsToAdvanced(t *testing.T) {
	e := newAuthEnv(t, nil)
	e.startWorker()
	admin, _, _ := e.setupAdmin()
	root := t.TempDir()
	ts.Write(t, root+"/a/one.bin", 10)
	ts.Write(t, root+"/a/100%.bin", 20)
	s, ids := scriptShare(t, e, admin, "media", root)
	both := idList(ids, "/a/one.bin", "/a/100%.bin")
	post := func(script string, style any) reply {
		b := map[string]any{"ids": both, "script": script, "client_root": root}
		if script != "sh" {
			b["client_root"] = `\\nas\films`
		}
		if style != nil {
			b["style"] = style
		}
		return admin.do("POST", s+"/delete-script", b)
	}
	ext := map[string]string{"sh": ".sh", "powershell": ".ps1", "cmd": ".cmd"}

	// Nothing and "advanced" are the script this application always wrote.
	for _, style := range []any{nil, "advanced"} {
		for script, want := range ext {
			r := post(script, style)
			expect(t, r, 200, "")
			if !strings.HasSuffix(r.Header.Get("Content-Disposition"), want) || !strings.Contains(r.Raw, "DELETE") {
				t.Errorf("%s with style %v is not the advanced script: %q %.80q", script, style, r.Header.Get("Content-Disposition"), r.Raw)
			}
		}
	}
	// "simple" is a .txt for all three, with the commands of the language.
	for script, want := range map[string]string{"sh": "rm -f -- ", "powershell": "Remove-Item -LiteralPath ", "cmd": "del /f /q "} {
		r := post(script, "simple")
		expect(t, r, 200, "")
		if !strings.HasSuffix(r.Header.Get("Content-Disposition"), ".txt") || !strings.Contains(r.Raw, want) || strings.Contains(r.Raw, "DELETE") {
			t.Errorf("%s: not a simple script: %q\n%s", script, r.Header.Get("Content-Disposition"), r.Raw)
		}
		if script == "cmd" {
			// cmd would read the percent sign as a variable: that file is left out, and the script says so.
			if r.Header.Get("X-Files") != "1" || r.Header.Get("X-Skipped") != "1" || strings.Contains(r.Raw, "100%.bin\"") ||
				!strings.Contains(r.Raw, "100?.bin: cmd could read a character") {
				t.Errorf("cmd: files %s skipped %s\n%s", r.Header.Get("X-Files"), r.Header.Get("X-Skipped"), r.Raw)
			}
		}
	}
	// Anything else is refused, naming the field.
	for _, bad := range []any{"Simple", "easy", "txt", "simple ", 7, true} {
		r := post("sh", bad)
		if r.Status != 400 || (r.Body["field"] != "style" && fmt.Sprint(bad) != "7" && fmt.Sprint(bad) != "true") {
			t.Errorf("style %v: %d %s", bad, r.Status, r.Raw)
		}
	}
}

func TestSimpleDeleteScriptIsForASmallNumber(t *testing.T) {
	e := newAuthEnv(t, nil)
	e.startWorker()
	admin, _, _ := e.setupAdmin()
	root := t.TempDir()
	const n = 201
	for i := 0; i < n; i++ {
		ts.Write(t, fmt.Sprintf("%s/many/f%03d.bin", root, i), 1)
	}
	s, ids := scriptShare(t, e, admin, "media", root)
	var all []int64
	for i := 0; i < n; i++ {
		all = append(all, ids[fmt.Sprintf("/many/f%03d.bin", i)])
	}
	post := func(style string, picked []int64) reply {
		return admin.do("POST", s+"/delete-script", map[string]any{"ids": picked, "script": "sh", "client_root": root, "style": style})
	}
	r := post("simple", all[:200])
	expect(t, r, 200, "")
	if r.Header.Get("X-Items") != "200" {
		t.Errorf("X-Items = %s", r.Header.Get("X-Items"))
	}
	r = post("simple", all)
	expect(t, r, 400, "invalid_request")
	if r.Body["field"] != "ids" || !strings.Contains(r.Raw, "at most 200") || !strings.Contains(r.Raw, "advanced script") {
		t.Errorf("the refusal must name the field and the way out: %s", r.Raw)
	}
	// The advanced script has no such limit; the limit belongs to the style.
	expect(t, post("advanced", all), 200, "")
}

func TestSimpleDeleteScriptKeepsTheRulesOfTheAdvancedOne(t *testing.T) {
	e := newAuthEnv(t, nil)
	e.startWorker()
	admin, _, _ := e.setupAdmin()
	root := t.TempDir()
	ts.Write(t, root+"/a/one.bin", 10)
	ts.Write(t, root+"/b/two.bin", 20)
	s, files := scriptShare(t, e, admin, "media", root)
	dirs, rootID := scriptDirs(t, admin, s)
	post := func(body map[string]any) reply {
		body["script"], body["client_root"], body["style"] = "sh", root, "simple"
		return admin.do("POST", s+"/delete-script", body)
	}

	// The share's own folder is never in a script, whatever the style.
	r := post(map[string]any{"dir_ids": []int64{rootID}})
	expect(t, r, 400, "")
	if r.Body["field"] != "ids" || !strings.Contains(r.Raw, "root 1") {
		t.Errorf("the share itself must be refused with the reason: %s", r.Raw)
	}
	// With something else next to it, the rest is written and the share is named as left out.
	r = post(map[string]any{"dir_ids": []int64{rootID, dirs["/a"]}, "ids": idList(files, "/b/two.bin")})
	expect(t, r, 200, "")
	if r.Header.Get("X-Skipped") != "1" || !strings.Contains(r.Raw, "this is the share itself, which is never deleted") {
		t.Errorf("the share is not reported as left out:\n%s", r.Raw)
	}
	if strings.Count(r.Raw, "\nrm -") != 2 {
		t.Errorf("want two commands:\n%s", r.Raw)
	}
	// Ids of another share, and ids that do not exist, are files that are gone.
	r = post(map[string]any{"ids": []int64{files["/a/one.bin"], 987654}})
	expect(t, r, 200, "")
	if r.Header.Get("X-Skipped") != "1" || !strings.Contains(r.Raw, "id 987654: no longer in the index") {
		t.Errorf("an id that is gone is not reported:\n%s", r.Raw)
	}
	expect(t, post(map[string]any{}), 400, "")
	// Only an administrator can ask for a script, in either style.
	v, _ := e.newUser(admin, "viewer2", "viewer")
	expect(t, v.do("POST", s+"/delete-script", map[string]any{"ids": []int64{1}, "script": "sh", "client_root": root, "style": "simple"}), 403, "")
}

func TestDeleteScriptsPreference(t *testing.T) {
	e := newAuthEnv(t, nil)
	admin, _, _ := e.setupAdmin()
	prefs := func() map[string]any {
		return admin.do("GET", "/api/v1/auth/me", nil).Body["user"].(map[string]any)["prefs"].(map[string]any)
	}
	if v, set := prefs()["delete_scripts"]; set {
		t.Errorf("the preference is %v before anyone chose: the interface falls back to the advanced script", v)
	}
	for _, v := range []string{"simple", "advanced"} {
		expect(t, admin.do("PUT", "/api/v1/auth/prefs", map[string]string{"delete_scripts": v}), 200, "")
		if prefs()["delete_scripts"] != v {
			t.Errorf("delete_scripts = %v, want %s", prefs()["delete_scripts"], v)
		}
	}
	for _, bad := range []string{"", "Simple", "easy", "txt", "advanced ", "simple;"} {
		expect(t, admin.do("PUT", "/api/v1/auth/prefs", map[string]string{"delete_scripts": bad}), 400, "")
	}
	if prefs()["delete_scripts"] != "advanced" {
		t.Errorf("a refused value changed the stored preference: %v", prefs()["delete_scripts"])
	}
}
