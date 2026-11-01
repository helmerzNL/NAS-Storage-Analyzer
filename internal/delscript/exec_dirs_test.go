package delscript

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scenarios for folders: a script that deletes a folder with everything in it is run in the real shell against real
// folders, with the things that must survive next to them and the things that must make it stop.

// folderName is a folder name that breaks scripts when it is not handled as data; it is not one the fixture of the files uses,
// so what is next to it can be checked.
const folderName = "proj $(id) `x` [y] & 100% 日本語"

// addDir puts a folder with a tree in it on disk and selects it. What the generator is told is counted from the disk, the way
// the index would have counted it at a scan: files and folders at any depth, and their size.
func (tr *tree) addDir(rel string) Dir {
	t := tr.t
	abs := filepath.Join(tr.root, filepath.FromSlash(rel))
	put := func(sub string, size int) { writeFile(t, filepath.Join(abs, filepath.FromSlash(sub)), size, epoch) }
	put("a.txt", 5)
	put("sub one/b.txt", 7)
	put("sub one/deeper/c.txt", 9)
	put(".hidden", 3)
	put("with space/it's.txt", 4)
	put("with space/$HOME.txt", 4)
	if err := os.MkdirAll(filepath.Join(abs, "empty dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A read-only file: deleting it takes more than a plain delete on Windows.
	if err := os.Chmod(filepath.Join(abs, "a.txt"), 0o444); err != nil {
		t.Fatal(err)
	}
	d := Dir{Path: rel}
	err := filepath.WalkDir(abs, func(p string, e fs.DirEntry, err error) error {
		if err != nil || p == abs {
			return err
		}
		if e.IsDir() {
			d.Dirs++
			return nil
		}
		info, err := e.Info()
		d.Files++
		d.Bytes += info.Size()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	tr.dirs = append(tr.dirs, d)
	// What a careless script would take along: a sibling with the same start of the name, the same name in another folder, and
	// a file that is named like the folder with something added.
	for _, decoy := range []string{abs + "2/keep.txt", filepath.Join(tr.root, "other", filepath.Base(abs), "keep.txt"), abs + ".txt"} {
		writeFile(t, decoy, 3, epoch)
		tr.decoys = append(tr.decoys, decoy)
	}
	return d
}

func (tr *tree) abs(rel string) string { return filepath.Join(tr.root, filepath.FromSlash(rel)) }

func TestScriptsDeleteFoldersWithEverythingInThem(t *testing.T) {
	for _, r := range runners(t) {
		r := r
		if r.target == TargetCmd {
			continue // cmd takes no folders: TestCmdLeavesFoldersOut
		}
		confirm := func(n int) string { return fmt.Sprintf("DELETE %d%s", n, r.eol) }
		run := func(t *testing.T, res *Result, stdin string) (string, int) {
			t.Helper()
			out, code := r.run(t, writeScript(t, r.target, res.Script), stdin)
			return normalize(out), code
		}
		usable := func(t *testing.T, tr *tree) string {
			t.Helper()
			if ClassifyDir(r.target, "/"+folderName) != "" {
				t.Skipf("this target cannot address %q", folderName)
			}
			return "/" + folderName
		}

		scenario(t, r, "a folder goes with everything in it, and nothing next to it", func(t *testing.T, tr *tree) {
			rel := usable(t, tr)
			d := tr.addDir(rel)
			res := tr.generate(r.target, nil)
			if res.Folders != 1 || res.FolderFiles != d.Files || res.FolderDirs != d.Dirs {
				t.Fatalf("the generator counted %d folders with %d files and %d folders; the disk has %+v", res.Folders, res.FolderFiles, res.FolderDirs, d)
			}
			out, code := run(t, res, confirm(res.Items))
			if code != 0 {
				t.Fatalf("exit code %d\n%s", code, out)
			}
			if exists(tr.abs(rel)) {
				t.Errorf("the folder is still there\n%s", out)
			}
			if present, _ := tr.remaining(); len(present) != 0 {
				t.Errorf("a selected file is still there: %s\n%s", tr.paths[present[0]], out)
			}
			tr.checkDecoys()
			// Every outcome counts what the scan knew of it, so the numbers add up to the number that was typed.
			if want := fmt.Sprintf("Done: %d deleted, 0 kept, 0 already gone, 0 failed.", res.Items); !strings.Contains(out, want) {
				t.Errorf("want %q in\n%s", want, out)
			}
			if !strings.Contains(out, "Items to delete:") || !strings.Contains(out, "EVERYTHING") {
				t.Errorf("the summary does not say that a folder goes with everything in it:\n%s", out)
			}
		})

		scenario(t, r, "the number to type is the items, not the files that were ticked", func(t *testing.T, tr *tree) {
			tr.addDir(usable(t, tr))
			res := tr.generate(r.target, nil)
			if res.Items <= res.Files+res.Folders {
				t.Fatalf("a folder with things in it must add to the number: items %d, files %d, folders %d", res.Items, res.Files, res.Folders)
			}
			for _, in := range []string{confirm(res.Files), confirm(res.Files + res.Folders), confirm(res.Items + 1), confirm(res.Items - 1)} {
				out, code := run(t, res, in)
				if code == 0 {
					t.Errorf("input %q: exit code 0\n%s", in, out)
				}
				if !exists(tr.abs(usable(t, tr))) {
					t.Fatalf("input %q deleted the folder\n%s", in, out)
				}
			}
			tr.checkDecoys()
		})

		scenario(t, r, "a folder that holds more than at the scan is kept, whole", func(t *testing.T, tr *tree) {
			rel := usable(t, tr)
			tr.addDir(rel)
			res := tr.generate(r.target, nil)
			writeFile(t, tr.abs(rel+"/new since the scan.txt"), 11, epoch) // what the person never saw
			out, code := run(t, res, confirm(res.Items))
			if code != 0 {
				t.Fatalf("exit code %d\n%s", code, out)
			}
			for _, p := range []string{"/new since the scan.txt", "/a.txt", "/sub one/deeper/c.txt", "/with space/$HOME.txt"} {
				if !exists(tr.abs(rel + p)) {
					t.Errorf("%s is gone: a folder that grew must be kept whole\n%s", p, out)
				}
			}
			if !strings.Contains(out, "holds more than at the scan, kept") {
				t.Errorf("the folder is not reported as kept:\n%s", out)
			}
			// What was selected one by one is still deleted: one folder that stops does not stop the rest.
			if present, _ := tr.remaining(); len(present) != 0 {
				t.Errorf("the selected files were not deleted: %s\n%s", tr.paths[present[0]], out)
			}
			if !strings.Contains(out, fmt.Sprintf("%d kept", res.FolderFiles+res.FolderDirs+1)) {
				t.Errorf("the kept folder counts what the scan knew of it:\n%s", out)
			}
		})

		scenario(t, r, "a new subfolder, even an empty one, also counts as more", func(t *testing.T, tr *tree) {
			rel := usable(t, tr)
			tr.addDir(rel)
			res := tr.generate(r.target, nil)
			if err := os.MkdirAll(tr.abs(rel+"/sub one/new empty folder"), 0o755); err != nil {
				t.Fatal(err)
			}
			out, _ := run(t, res, confirm(res.Items))
			if !exists(tr.abs(rel+"/a.txt")) || !strings.Contains(out, "holds more than at the scan, kept") {
				t.Errorf("a folder with a new subfolder must be kept:\n%s", out)
			}
		})

		scenario(t, r, "a folder with fewer files than at the scan is still deleted", func(t *testing.T, tr *tree) {
			rel := usable(t, tr)
			tr.addDir(rel)
			res := tr.generate(r.target, nil)
			if err := os.Remove(tr.abs(rel + "/sub one/b.txt")); err != nil { // somebody already removed part of it
				t.Fatal(err)
			}
			out, code := run(t, res, confirm(res.Items))
			if code != 0 || exists(tr.abs(rel)) {
				t.Errorf("exit %d, folder there: %v\n%s", code, exists(tr.abs(rel)), out)
			}
		})

		scenario(t, r, "a link inside a folder keeps the folder, and what the link points at is not touched", func(t *testing.T, tr *tree) {
			rel := usable(t, tr)
			tr.addDir(rel)
			outside := tr.abs("outside target")
			writeFile(t, filepath.Join(outside, "precious.txt"), 8, epoch)
			tr.decoys = append(tr.decoys, filepath.Join(outside, "precious.txt"))
			res := tr.generate(r.target, nil)
			makeDirLink(t, outside, tr.abs(rel+"/link to outside")) // made after the scan, as a link can be
			out, code := run(t, res, confirm(res.Items))
			if code != 0 {
				t.Fatalf("exit code %d\n%s", code, out)
			}
			if !exists(tr.abs(rel + "/a.txt")) {
				t.Errorf("a folder with a link in it must be kept whole\n%s", out)
			}
			if !strings.Contains(out, "holds links or special files, kept") && !strings.Contains(out, "holds more than at the scan, kept") {
				t.Errorf("the folder is not reported as kept:\n%s", out)
			}
			tr.checkDecoys()
		})

		scenario(t, r, "a link where the folder was is kept, and what it points at is not touched", func(t *testing.T, tr *tree) {
			rel := usable(t, tr)
			tr.addDir(rel)
			elsewhere := tr.abs("elsewhere")
			writeFile(t, filepath.Join(elsewhere, "a.txt"), 5, epoch)
			writeFile(t, filepath.Join(elsewhere, "sub one", "b.txt"), 7, epoch)
			tr.decoys = append(tr.decoys, filepath.Join(elsewhere, "a.txt"), filepath.Join(elsewhere, "sub one", "b.txt"))
			res := tr.generate(r.target, nil)
			if err := os.Chmod(tr.abs(rel+"/a.txt"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(tr.abs(rel)); err != nil {
				t.Fatal(err)
			}
			makeDirLink(t, elsewhere, tr.abs(rel))
			out, code := run(t, res, confirm(res.Items))
			if code != 0 {
				t.Fatalf("exit code %d\n%s", code, out)
			}
			if !strings.Contains(out, "not a plain folder, kept") {
				t.Errorf("a link in the place of the folder is not reported as kept:\n%s", out)
			}
			tr.checkDecoys()
		})

		scenario(t, r, "a file where the folder was is kept", func(t *testing.T, tr *tree) {
			rel := usable(t, tr)
			tr.addDir(rel)
			res := tr.generate(r.target, nil)
			if err := os.Chmod(tr.abs(rel+"/a.txt"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(tr.abs(rel)); err != nil {
				t.Fatal(err)
			}
			writeFile(t, tr.abs(rel), 5, epoch)
			out, _ := run(t, res, confirm(res.Items))
			if !exists(tr.abs(rel)) || !strings.Contains(out, "not a plain folder, kept") {
				t.Errorf("a file in the place of the folder must be kept and reported:\n%s", out)
			}
		})

		scenario(t, r, "a folder that is already gone is reported, and the rest still goes", func(t *testing.T, tr *tree) {
			rel := usable(t, tr)
			tr.addDir(rel)
			res := tr.generate(r.target, nil)
			if err := os.Chmod(tr.abs(rel+"/a.txt"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(tr.abs(rel)); err != nil {
				t.Fatal(err)
			}
			out, code := run(t, res, confirm(res.Items))
			if code != 0 || !strings.Contains(out, "already gone") {
				t.Errorf("exit %d\n%s", code, out)
			}
			if present, _ := tr.remaining(); len(present) != 0 {
				t.Errorf("the selected files were not deleted\n%s", out)
			}
		})

		scenario(t, r, "a folder inside a selected folder, and files inside it, go with it once", func(t *testing.T, tr *tree) {
			rel := usable(t, tr)
			tr.addDir(rel)
			tr.dirs = append(tr.dirs, Dir{Path: rel + "/sub one", Files: 2, Dirs: 1, Bytes: 16}) // inside the selected folder
			tr.files = append(tr.files, File{Path: rel + "/a.txt", Size: 5, Mtime: epoch.Unix(), Nlink: 1})
			tr.paths = append(tr.paths, tr.abs(rel+"/a.txt"))
			res := tr.generate(r.target, nil)
			if res.Folders != 1 || res.Covered != 2 {
				t.Fatalf("folders %d, covered %d: the inner folder and the file are covered by the outer one", res.Folders, res.Covered)
			}
			// Counted by line: a CRLF script holds "\r\nD|" and "\nD|" is a part of it, so counting both would count each record twice.
			if n := len(record(res.Script, "D")); n != 1 {
				t.Errorf("the script holds %d folder records, want 1", n)
			}
			out, code := run(t, res, confirm(res.Items))
			if code != 0 || exists(tr.abs(rel)) {
				t.Errorf("exit %d, folder there: %v\n%s", code, exists(tr.abs(rel)), out)
			}
		})
	}
}

// cmd cannot check what is in a folder, so a cmd script takes none, says why, and still deletes the files.
func TestCmdLeavesFoldersOut(t *testing.T) {
	root := shortTempDir(t)
	res, err := Generate([]File{{Path: "/keep/a.txt", Size: 5, Mtime: epoch.Unix(), Nlink: 1}}, Params{
		Share: "Media", Root: `\\ZBNAS\Media`, Target: TargetCmd, Lang: LangEN, ScanID: 1, ScanTime: scanTime, Generated: generated,
		Dirs: []Dir{{Path: "/Old", Files: 3, Dirs: 1, Bytes: 30}},
	})
	_ = root
	if err != nil {
		t.Fatal(err)
	}
	if res.Folders != 0 || strings.Contains(string(res.Script), "\nD|") || strings.Contains(string(res.Script), "\r\nD|") {
		t.Errorf("a cmd script must hold no folder record: %+v", res)
	}
	if got := names(res.Skipped); got != "/Old=folder-cmd" {
		t.Errorf("skipped = %s", got)
	}
}
