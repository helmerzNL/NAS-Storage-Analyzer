package delscript

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// These tests do not look at the text of a script: they run it, in the real shell, against real files with the
// names that break shell scripts, and check which files are gone afterwards. Each shell has its own runner
// (runners_unix_test.go, runners_windows_test.go); the scenarios are the same for all of them.

// runner executes one script file in one shell.
type runner struct {
	name   string
	target Target
	// absent says why the shell cannot run here; the subtest is skipped. A shell named in NASA_REQUIRE_SHELLS that
	// is absent fails the test instead, so a CI image that lost a shell does not turn the test into a green no-op.
	absent string
	// run feeds stdin to the script and returns its combined output and exit code.
	run func(t *testing.T, script, stdin string) (string, int)
	// eol is the line ending of what is typed at the confirmation.
	eol string
}

// present keeps the runners whose shell is here and says in the log which ones did not run.
func present(t *testing.T, all []runner) []runner {
	t.Helper()
	var out []runner
	for _, r := range all {
		if r.absent != "" {
			t.Logf("%s: %s, so its scenarios did not run here", r.name, r.absent)
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		t.Fatal("no shell to run the scripts in")
	}
	return out
}

// shellRequired reports whether NASA_REQUIRE_SHELLS names this shell.
func shellRequired(name string) bool {
	for _, n := range strings.Split(os.Getenv("NASA_REQUIRE_SHELLS"), ",") {
		if strings.TrimSpace(n) == name {
			return true
		}
	}
	return false
}

// hostileNames are the names that break scripts. The ones Windows cannot hold are filtered out per target by the
// same classifier the generator uses, so a Windows run only sees names it can address.
var hostileNames = []string{
	"plain.txt", "with space.txt", "two  spaces  here.txt", "it's.txt", "dollar $HOME $(id) ${x}.txt", "back`tick`.txt",
	"100% pure %PATH% %%.txt", "bang! ^caret & ampersand (paren) {brace} [bracket] ;semi ,comma =eq #hash @at ~tilde +plus.txt",
	"-dash.txt", "-n", "uni-é-ü-日本語.txt", "emoji-\U0001F600.txt", "é-decomposed.txt", "é-composed.txt",
	"long-" + strings.Repeat("y", 150) + ".txt", " leading space.txt", "’quotes’ ‘x’.txt",
	// Names only a Linux file system can hold.
	`dq"uote.txt`, "star*.txt", "q?mark.txt", `back\slash.txt`, "pipe|bar.txt", "ends|", "|starts", "colon:name.txt",
	"lt<gt>.txt", "trailing.dot.", "trailing space ", "CON", "NUL.txt",
}

var hostileFolders = []string{"dir one", "it's a dir", "dir $HOME", "100% dir", "日本語 dir", "[x] (y) & z"}

// tree is a share on disk with a selection from it, and files that must survive.
type tree struct {
	t      *testing.T
	root   string
	files  []File   // the selection
	dirs   []Dir    // the selected folders, each with everything in it
	paths  []string // where each selected file is on disk
	decoys []string // must still exist after the script ran
}

var epoch = time.Date(2025, 10, 31, 12, 0, 0, 0, time.UTC)

// shortTempDir is a temporary folder with a short path. t.TempDir() puts the whole name of the test in it, and on
// Windows that alone takes most of the 259 characters a path may have: the generator then rightly leaves the
// files with long names out of the script, which is not what these tests are about.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ds")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func newTree(t *testing.T, target Target) *tree {
	t.Helper()
	tr := &tree{t: t, root: shortTempDir(t)}
	write := func(path string, size int, mtime time.Time) { writeFile(t, path, size, mtime) }
	i := 0
	for _, name := range hostileNames {
		folder := hostileFolders[i%len(hostileFolders)]
		rel := "/" + folder + "/" + name
		if ClassifyPath(target, rel) != "" {
			continue // this target cannot address it; the generator leaves it out, which another test covers
		}
		size := len(name) + i + 1
		mtime := epoch.Add(time.Duration(i) * time.Minute)
		abs := filepath.Join(tr.root, filepath.FromSlash(rel))
		write(abs, size, mtime)
		tr.files = append(tr.files, File{Path: rel, Size: int64(size), Mtime: mtime.Unix(), Nlink: 1})
		tr.paths = append(tr.paths, abs)
		// Files a careless script would take along: a sibling that starts with the same name, and the same name
		// in another folder.
		tr.decoys = append(tr.decoys, abs+".bak", filepath.Join(tr.root, "other", name))
		write(abs+".bak", 3, mtime)
		write(filepath.Join(tr.root, "other", name), 3, mtime)
		i++
	}
	write(filepath.Join(tr.root, "keep.txt"), 3, epoch)
	tr.decoys = append(tr.decoys, filepath.Join(tr.root, "keep.txt"))
	if runtime.GOOS == "linux" { // a case-sensitive file system: a twin that differs only in case must survive
		twin := filepath.Join(tr.root, "dir one", "PLAIN.TXT")
		write(twin, 3, epoch)
		tr.decoys = append(tr.decoys, twin)
	}
	if len(tr.files) < 8 {
		t.Fatalf("only %d usable files in the fixture", len(tr.files))
	}
	return tr
}

func writeFile(t *testing.T, path string, size int, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatalf("create %q: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// addFile puts one more file in the selection, in a folder of its own, and returns its index.
func (tr *tree) addFile(rel string) int {
	abs := filepath.Join(tr.root, filepath.FromSlash(rel))
	writeFile(tr.t, abs, 9, epoch)
	tr.files = append(tr.files, File{Path: rel, Size: 9, Mtime: epoch.Unix(), Nlink: 1})
	tr.paths = append(tr.paths, abs)
	return len(tr.paths) - 1
}

func (tr *tree) generate(target Target, mut func(*Params)) *Result {
	tr.t.Helper()
	p := paramsFor(target, LangEN)
	p.Root = tr.root
	p.Dirs = tr.dirs
	if mut != nil {
		mut(&p)
	}
	return mustGenerate(tr.t, tr.files, p)
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

func (tr *tree) remaining() (present, gone []int) {
	for i, p := range tr.paths {
		if exists(p) {
			present = append(present, i)
		} else {
			gone = append(gone, i)
		}
	}
	return
}

func (tr *tree) checkDecoys() {
	tr.t.Helper()
	for _, d := range tr.decoys {
		if !exists(d) {
			tr.t.Errorf("a file that was never selected is gone: %s", d)
		}
	}
}

// scenario runs a script in a fresh tree.
func scenario(t *testing.T, r runner, name string, f func(t *testing.T, tr *tree)) {
	t.Run(r.name+"/"+name, func(t *testing.T) {
		if r.absent != "" {
			t.Skip(r.absent)
		}
		f(t, newTree(t, r.target))
	})
}

func writeScript(t *testing.T, target Target, script []byte) string {
	t.Helper()
	path := filepath.Join(shortTempDir(t), "delete"+target.Extension())
	if err := os.WriteFile(path, script, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func normalize(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

// runScenarios is the same set for every shell.
func runScenarios(t *testing.T, runners []runner) {
	for _, r := range runners {
		r := r
		confirm := func(n int) string { return fmt.Sprintf("DELETE %d%s", n, r.eol) }
		runWith := func(t *testing.T, tr *tree, res *Result, stdin string) (string, int) {
			t.Helper()
			out, code := r.run(t, writeScript(t, r.target, res.Script), stdin)
			return normalize(out), code
		}

		scenario(t, r, "deletes exactly the selection", func(t *testing.T, tr *tree) {
			res := tr.generate(r.target, nil)
			if res.Files != len(tr.files) {
				t.Fatalf("the generator left files out: %s", names(res.Skipped))
			}
			out, code := runWith(t, tr, res, confirm(res.Files))
			if code != 0 {
				t.Fatalf("exit code %d\n%s", code, out)
			}
			present, _ := tr.remaining()
			if len(present) != 0 {
				t.Errorf("still there: %v\n%s", tr.paths[present[0]], out)
			}
			tr.checkDecoys()
			// Not anchored to the start of the line: with piped input the first line follows the prompt directly.
			if n := strings.Count(out, "deleted: "); n != res.Files {
				t.Errorf("%d deleted lines for %d files\n%s", n, res.Files, out)
			}
			if !strings.Contains(out, fmt.Sprintf("Done: %d deleted, 0 kept, 0 already gone, 0 failed.", res.Files)) {
				t.Errorf("the final line is wrong:\n%s", out)
			}
			if !strings.Contains(out, "Base path:") || !strings.Contains(out, "Files to delete:") {
				t.Errorf("the summary was not shown:\n%s", out)
			}
		})

		scenario(t, r, "a wrong confirmation deletes nothing", func(t *testing.T, tr *tree) {
			res := tr.generate(r.target, nil)
			for _, in := range []string{"", r.eol, "yes" + r.eol, "delete " + fmt.Sprint(res.Files) + r.eol,
				fmt.Sprintf("DELETE %d%s", res.Files+1, r.eol), fmt.Sprintf("DELETE%s", r.eol), fmt.Sprintf(" DELETE %d%s", res.Files, r.eol)} {
				out, code := runWith(t, tr, res, in)
				if code == 0 {
					t.Errorf("input %q: exit code 0\n%s", in, out)
				}
				if present, _ := tr.remaining(); len(present) != len(tr.paths) {
					t.Fatalf("input %q deleted files\n%s", in, out)
				}
			}
			tr.checkDecoys()
		})

		scenario(t, r, "an incomplete script deletes nothing", func(t *testing.T, tr *tree) {
			res := tr.generate(r.target, nil)
			s := string(res.Script)
			eol := "\n"
			if strings.Contains(s, "\r\n") {
				eol = "\r\n"
			}
			lines := strings.Split(s, eol)
			// Drop the last three file records wherever the data sits in the script.
			var kept []string
			dropped := 0
			for i := len(lines) - 1; i >= 0; i-- {
				if dropped < 3 && strings.HasPrefix(lines[i], "F|") {
					dropped++
					continue
				}
				kept = append([]string{lines[i]}, kept...)
			}
			// The data is not what the script was made for: it must refuse and delete nothing.
			strict := map[string]string{
				"records missing": strings.Join(kept, eol),
				"count raised":    strings.Replace(s, fmt.Sprintf("N|%d", res.Files), fmt.Sprintf("N|%d", res.Files+1), 1),
				"count lowered":   strings.Replace(s, fmt.Sprintf("N|%d", res.Files), fmt.Sprintf("N|%d", res.Files-1), 1),
			}
			for what, text := range strict {
				if text == s {
					t.Fatalf("%s: the edit changed nothing", what)
				}
				out, code := r.run(t, writeScript(t, r.target, []byte(text)), confirm(res.Files))
				if code == 0 {
					t.Errorf("%s: exit code 0\n%s", what, normalize(out))
				}
				if present, _ := tr.remaining(); len(present) != len(tr.paths) {
					t.Fatalf("%s: files were deleted\n%s", what, normalize(out))
				}
			}
			// The code is cut off. The records are all there, so a cut that leaves the deletion loop whole (only the
			// closing lines are gone) deletes everything, exactly as the full script would; a cut inside the loop is a
			// syntax error and deletes nothing. What must never happen is that only some of the files are deleted.
			// cmd runs what it has read so far and has its data after its code, so a cut inside its code is not defined.
			if r.target == TargetCmd {
				return
			}
			for _, pct := range []int{20, 40, 60, 80, 90, 95, 99} {
				at := strings.LastIndex(s[:len(s)*pct/100], eol)
				text := s[:at+len(eol)]
				out, _ := r.run(t, writeScript(t, r.target, []byte(text)), confirm(res.Files))
				present, _ := tr.remaining()
				if len(present) != 0 && len(present) != len(tr.paths) {
					t.Fatalf("cut at %d%%: %d of %d files are left, a partial deletion\n%s", pct, len(present), len(tr.paths), normalize(out))
				}
				if len(present) == 0 {
					return // everything went, as the full script would have done; nothing more to cut
				}
			}
		})

		scenario(t, r, "a file that changed since the scan is kept", func(t *testing.T, tr *tree) {
			res := tr.generate(r.target, nil)
			if err := os.WriteFile(tr.paths[0], []byte(strings.Repeat("y", int(tr.files[0].Size)+5)), 0o644); err != nil {
				t.Fatal(err)
			}
			out, code := runWith(t, tr, res, confirm(res.Files))
			if code != 0 {
				t.Fatalf("exit code %d\n%s", code, out)
			}
			present, _ := tr.remaining()
			if len(present) != 1 || present[0] != 0 {
				t.Errorf("only the changed file must remain, got %v\n%s", present, out)
			}
			if !strings.Contains(out, "changed since the scan, kept") {
				t.Errorf("the kept file is not reported:\n%s", out)
			}
			tr.checkDecoys()
		})

		scenario(t, r, "a file with a different modification time is kept, or deleted where the time is not checked", func(t *testing.T, tr *tree) {
			res := tr.generate(r.target, nil)
			later := epoch.Add(48 * time.Hour)
			if err := os.Chtimes(tr.paths[0], later, later); err != nil {
				t.Fatal(err)
			}
			out, code := runWith(t, tr, res, confirm(res.Files))
			if code != 0 {
				t.Fatalf("exit code %d\n%s", code, out)
			}
			present, _ := tr.remaining()
			if r.target == TargetCmd { // cmd can only compare the size: pinned so a change is noticed
				if len(present) != 0 {
					t.Errorf("cmd must not have kept anything: %v", present)
				}
				return
			}
			if len(present) != 1 || present[0] != 0 {
				t.Errorf("only the file with the other time must remain, got %v\n%s", present, out)
			}
		})

		scenario(t, r, "a time within two seconds is accepted", func(t *testing.T, tr *tree) {
			res := tr.generate(r.target, nil)
			near := epoch.Add(time.Second)
			if err := os.Chtimes(tr.paths[0], near, near); err != nil {
				t.Fatal(err)
			}
			out, code := runWith(t, tr, res, confirm(res.Files))
			if present, _ := tr.remaining(); code != 0 || len(present) != 0 {
				t.Errorf("exit %d, still there: %v\n%s", code, present, out)
			}
		})

		scenario(t, r, "a folder in place of a file is kept", func(t *testing.T, tr *tree) {
			res := tr.generate(r.target, nil)
			if err := os.Remove(tr.paths[0]); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(tr.paths[0], 0o755); err != nil {
				t.Fatal(err)
			}
			inside := filepath.Join(tr.paths[0], "inside.txt")
			if err := os.WriteFile(inside, []byte("precious"), 0o644); err != nil {
				t.Fatal(err)
			}
			out, code := runWith(t, tr, res, confirm(res.Files))
			if code != 0 {
				t.Fatalf("exit code %d\n%s", code, out)
			}
			if !exists(inside) {
				t.Errorf("the contents of a folder were deleted\n%s", out)
			}
			tr.checkDecoys()
		})

		scenario(t, r, "a link in place of a file is kept, and so is its target", func(t *testing.T, tr *tree) {
			res := tr.generate(r.target, nil)
			target := filepath.Join(tr.root, "link-target.txt")
			if err := os.WriteFile(target, []byte(strings.Repeat("z", int(tr.files[0].Size))), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(tr.paths[0]); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, tr.paths[0]); err != nil {
				t.Skipf("cannot create a link here: %v", err)
			}
			out, code := runWith(t, tr, res, confirm(res.Files))
			if code != 0 {
				t.Fatalf("exit code %d\n%s", code, out)
			}
			if !exists(target) {
				t.Errorf("the target of a link was deleted\n%s", out)
			}
		})

		scenario(t, r, "a file that is already gone is reported", func(t *testing.T, tr *tree) {
			res := tr.generate(r.target, nil)
			if err := os.Remove(tr.paths[1]); err != nil {
				t.Fatal(err)
			}
			out, code := runWith(t, tr, res, confirm(res.Files))
			if code != 0 {
				t.Fatalf("exit code %d\n%s", code, out)
			}
			if !strings.Contains(out, fmt.Sprintf("Done: %d deleted, 0 kept, 1 already gone, 0 failed.", res.Files-1)) {
				t.Errorf("the count is wrong:\n%s", out)
			}
			tr.checkDecoys()
		})

		scenario(t, r, "a wrong base path stops before the question", func(t *testing.T, tr *tree) {
			elsewhere := t.TempDir()
			res := tr.generate(r.target, func(p *Params) { p.Root = elsewhere })
			out, code := runWith(t, tr, res, confirm(res.Files))
			if code == 0 || !strings.Contains(out, "Found under the base path: 0 of") {
				t.Errorf("exit %d\n%s", code, out)
			}
			if strings.Contains(out, "Type DELETE") {
				t.Errorf("it asked for a confirmation although nothing was found:\n%s", out)
			}
			if present, _ := tr.remaining(); len(present) != len(tr.paths) {
				t.Errorf("files were deleted")
			}
		})

		scenario(t, r, "a base path that does not exist is refused", func(t *testing.T, tr *tree) {
			res := tr.generate(r.target, func(p *Params) { p.Root = filepath.Join(tr.root, "no", "such", "place") })
			out, code := runWith(t, tr, res, confirm(res.Files))
			if code == 0 {
				t.Errorf("exit code 0\n%s", out)
			}
			if present, _ := tr.remaining(); len(present) != len(tr.paths) {
				t.Errorf("files were deleted\n%s", out)
			}
		})

		scenario(t, r, "a file that cannot be deleted is reported as failed", func(t *testing.T, tr *tree) {
			idx := tr.addFile("/locked folder/stuck.txt")
			res := tr.generate(r.target, nil)
			restore := makeUndeletable(t, tr.paths[idx])
			defer restore()
			out, code := runWith(t, tr, res, confirm(res.Files))
			if code != 1 {
				t.Errorf("exit code %d, want 1\n%s", code, out)
			}
			if !strings.Contains(out, "FAILED: ") || !strings.Contains(out, " 1 failed.") {
				t.Errorf("the failure is not reported:\n%s", out)
			}
			restore()
			present, _ := tr.remaining()
			if len(present) != 1 || present[0] != idx {
				t.Errorf("only the undeletable file must remain, got %v", present)
			}
		})
	}
}

func TestScriptsDeleteExactlyTheSelection(t *testing.T) {
	runScenarios(t, runners(t))
}
