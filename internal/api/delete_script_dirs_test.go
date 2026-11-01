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

// scriptDirs lists the folders of a scanned share through the API, as the interface does, and returns their ids by path
// ("/movies/old") and the id of the share's own folder.
func scriptDirs(t *testing.T, admin *client, url string) (ids map[string]int64, rootID int64) {
	t.Helper()
	root := admin.do("GET", url+"/dirs/root", nil)
	expect(t, root, 200, "")
	rootID = int64(root.Body["id"].(float64))
	ids = map[string]int64{}
	var walk func(dir, prefix string)
	walk = func(dir, prefix string) {
		r := admin.do("GET", url+"/dirs/"+dir+"/children?limit=200", nil)
		expect(t, r, 200, "")
		for _, it := range r.Body["items"].([]any) {
			m := it.(map[string]any)
			p := prefix + "/" + m["name"].(string)
			ids[p] = int64(m["id"].(float64))
			walk(fmt.Sprint(int64(m["id"].(float64))), p)
		}
	}
	walk("root", "")
	return ids, rootID
}

// The whole way, for a folder: a share is scanned, a folder is picked by id, the endpoint writes a script, and running that script
// removes the folder with everything in it and nothing next to it. The application itself deletes nothing.
func TestDeleteScriptDeletesAFolderWithEverythingInItWhenRun(t *testing.T) {
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
	for p, size := range map[string]int{
		"movies/old/a.mkv": 100, "movies/old/b.mkv": 200, "movies/old/deep/er/c.mkv": 300, "movies/old/.hidden": 10, "movies/old/it's $(id).txt": 20,
		"movies/old2/keep.mkv": 400, "movies/keep.mkv": 500, "movies/old.txt": 600, "docs/other.txt": 60, "loose.txt": 70,
	} {
		ts.Write(t, filepath.Join(root, p), size)
	}
	if err := os.MkdirAll(filepath.Join(root, "movies/old/empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, files := scriptShare(t, e, admin, "media", root)
	dirs, _ := scriptDirs(t, admin, s)
	if dirs["/movies/old"] == 0 || dirs["/movies/old2"] == 0 {
		t.Fatalf("folders = %v", dirs)
	}

	// A folder, and a loose file; one more file of the folder is ticked as well, and goes with the folder.
	r := admin.do("POST", s+"/delete-script", map[string]any{
		"dir_ids": []int64{dirs["/movies/old"]}, "ids": idList(files, "/loose.txt", "/movies/old/a.mkv"), "script": "sh", "client_root": root,
	})
	expect(t, r, 200, "")
	// Inside the folder: 5 files and 3 folders (deep, er, empty); the folder itself is the ninth item, and /loose.txt the tenth.
	if got := []string{r.Header.Get("X-Files"), r.Header.Get("X-Folders"), r.Header.Get("X-Items"), r.Header.Get("X-Skipped")}; strings.Join(got, ",") != "1,1,10,0" {
		t.Errorf("X-Files, X-Folders, X-Items, X-Skipped = %v, want 1,1,10,0", got)
	}
	for p := range files { // nothing is deleted by asking
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Fatalf("%s is gone after only asking for a script: %v", p, err)
		}
	}

	file := filepath.Join(t.TempDir(), "delete.sh")
	if err := os.WriteFile(file, []byte(r.Raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, file)
	cmd.Stdin = strings.NewReader("DELETE 10\n")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Done: 10 deleted, 0 kept, 0 already gone, 0 failed.") {
		t.Fatalf("running the script: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "movies/old")); !os.IsNotExist(err) {
		t.Errorf("the folder must be gone with everything in it: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "loose.txt")); !os.IsNotExist(err) {
		t.Errorf("the loose file must be gone: %v", err)
	}
	for _, keep := range []string{"movies/old2/keep.mkv", "movies/keep.mkv", "movies/old.txt", "docs/other.txt", "movies"} {
		if _, err := os.Stat(filepath.Join(root, keep)); err != nil {
			t.Errorf("%s must survive: %v", keep, err)
		}
	}

	// The log says what was handed out, in numbers and a hash, and never a name.
	a := admin.do("GET", "/api/v1/audit?action=script.", nil)
	expect(t, a, 200, "")
	for _, want := range []string{`"folders":1`, `"folder_files":5`, `"items":10`, `"script":"sh"`} {
		if !strings.Contains(a.Raw, want) {
			t.Errorf("audit lacks %s: %s", want, a.Raw)
		}
	}
	for _, leak := range []string{"old2", "movies", "deep", "loose", "hidden", "a.mkv", "$(id)"} { // not "old": it is in "folders"
		if strings.Contains(a.Raw, leak) {
			t.Errorf("audit holds part of a name (%q): %s", leak, a.Raw)
		}
	}
}

func TestDeleteScriptNeverTakesTheShareItselfOrAnotherSharesFolder(t *testing.T) {
	e := newAuthEnv(t, nil)
	e.startWorker()
	admin, _, _ := e.setupAdmin()
	root, other := t.TempDir(), t.TempDir()
	ts.Write(t, root+"/a/one.bin", 10)
	ts.Write(t, root+"/b/two.bin", 20)
	ts.Write(t, other+"/secret-folder/x.bin", 5)
	s, _ := scriptShare(t, e, admin, "media", root)
	dirs, rootID := scriptDirs(t, admin, s)
	os2, _ := scriptShare(t, e, admin, "other", other)
	foreign, _ := scriptDirs(t, admin, os2)
	post := func(body map[string]any) reply {
		body["script"], body["client_root"] = "sh", root
		return admin.do("POST", s+"/delete-script", body)
	}

	// The share's own folder: refused, whatever else is asked.
	r := post(map[string]any{"dir_ids": []int64{rootID}})
	expect(t, r, 400, "")
	if !strings.Contains(r.Raw, "none of the selected") || !strings.Contains(r.Raw, "root 1") {
		t.Errorf("the share itself must be refused with the reason: %s", r.Raw)
	}
	r = post(map[string]any{"dir_ids": []int64{rootID, dirs["/a"]}})
	expect(t, r, 200, "")
	if r.Header.Get("X-Folders") != "1" || r.Header.Get("X-Skipped") != "1" || strings.Contains(r.Raw, "D|1|0|/b") {
		t.Errorf("only /a goes, and the root is reported: folders %s, skipped %s", r.Header.Get("X-Folders"), r.Header.Get("X-Skipped"))
	}
	if n := strings.Count(r.Raw, "\nD|"); n != 1 {
		t.Errorf("the script holds %d folder records, want 1: %s", n, r.Raw)
	}

	// A folder of another share is told apart from nothing, and its name is not in the answer.
	r = post(map[string]any{"dir_ids": []int64{foreign["/secret-folder"]}})
	expect(t, r, 400, "")
	if strings.Contains(r.Raw, "secret") || !strings.Contains(r.Raw, "gone 1") {
		t.Errorf("another share's folder must look like one that does not exist: %s", r.Raw)
	}
	r = post(map[string]any{"dir_ids": []int64{987654321}})
	expect(t, r, 400, "")
	// A folder id that is not a positive number is refused as it is for files.
	expect(t, post(map[string]any{"dir_ids": []int64{0}}), 400, "")
	expect(t, post(map[string]any{"dir_ids": []int64{-3}}), 400, "")
}

func TestDeleteScriptFoldersNeedAShellThatCanCheckThem(t *testing.T) {
	e := newAuthEnv(t, nil)
	e.startWorker()
	admin, _, _ := e.setupAdmin()
	root := t.TempDir()
	ts.Write(t, root+"/a/one.bin", 10)
	ts.Write(t, root+"/b/two.bin", 20)
	s, files := scriptShare(t, e, admin, "media", root)
	dirs, _ := scriptDirs(t, admin, s)
	body := func(script string, ids ...int64) map[string]any {
		b := map[string]any{"dir_ids": []int64{dirs["/a"]}, "script": script, "client_root": `\\ZBNAS\Media`}
		if len(ids) > 0 {
			b["ids"] = ids
		}
		return b
	}
	// cmd cannot check what is in a folder: a folder alone is refused, with the reason, and with a file the file still goes.
	r := admin.do("POST", s+"/delete-script", body("cmd"))
	expect(t, r, 400, "")
	if !strings.Contains(r.Raw, "folder-cmd 1") {
		t.Errorf("the reason must be named: %s", r.Raw)
	}
	r = admin.do("POST", s+"/delete-script", body("cmd", files["/b/two.bin"]))
	expect(t, r, 200, "")
	if r.Header.Get("X-Folders") != "0" || r.Header.Get("X-Files") != "1" || r.Header.Get("X-Skipped") != "1" || strings.Contains(r.Raw, "\r\nD|") {
		t.Errorf("a cmd script holds no folder: %v", r.Header)
	}
	// PowerShell takes the folder, in the form Windows addresses it.
	r = admin.do("POST", s+"/delete-script", body("powershell"))
	expect(t, r, 200, "")
	if r.Header.Get("X-Folders") != "1" || !strings.Contains(r.Raw, "\r\nD|0|0|\\a\r\n") && !strings.Contains(r.Raw, "|\\a\r\n") {
		t.Errorf("a PowerShell script holds the folder with backslashes: folders %s", r.Header.Get("X-Folders"))
	}
	// Files and folders share the limit of a script.
	many := make([]int64, 1000)
	for i := range many {
		many[i] = int64(i + 1)
	}
	expect(t, admin.do("POST", s+"/delete-script", map[string]any{"ids": many, "dir_ids": append(many, 5, 6), "script": "sh", "client_root": root}), 400, "")
}
