package delscript

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The simple script is run in the real shells as well, against the same trees and the same names that break scripts. It has no confirmation and no check, so
// what these tests prove is the one thing it has to get right: a line deletes the file or folder it names, whatever the name, and nothing else.

// simpleFolder is a folder name the target can take in a simple script. A cmd script cannot take the percent sign of folderName.
func simpleFolder(target Target) string {
	name := folderName
	if classifyDirSimple(target, "/"+name) != "" {
		name = strings.ReplaceAll(name, "%", "")
	}
	return "/" + name
}

var commandLine = regexp.MustCompile(`^(rm -|Remove-Item |del |rd )`)

func TestSimpleScriptsDeleteTheirLinesInRealShells(t *testing.T) {
	for _, r := range runners(t) {
		r := r
		gen := func(tr *tree) *Result {
			return tr.generate(r.target, func(p *Params) { p.Style = StyleSimple })
		}
		run := func(t *testing.T, script []byte) (string, int) {
			t.Helper()
			out, code := r.run(t, writeScript(t, r.target, script), "")
			return normalize(out), code
		}
		// Names the target leaves out are reported, and stay where they are.
		leftOut := func(res *Result) map[string]bool {
			m := map[string]bool{}
			for _, s := range res.Skipped {
				m[s.Path] = true
			}
			return m
		}

		scenario(t, r, "simple: deletes exactly the files it names", func(t *testing.T, tr *tree) {
			// Characters that mean something to a command line and that the shared names do not have next to each other without an exclamation mark or a
			// percent sign, which cmd leaves out: the caret and the ampersand of cmd, brackets and braces, a comma, a semicolon and an equals sign.
			for _, name := range []string{"caret^name & (x) ,;=.txt", "[b] {c} #d @e ~f +g.txt"} {
				if rel := "/extras/" + name; ClassifyPath(r.target, rel) == "" {
					tr.addFile(rel)
				}
			}
			res := gen(tr)
			skipped := leftOut(res)
			for _, s := range res.Skipped {
				if r.target != TargetCmd || s.Reason != ReasonCmdChars {
					t.Fatalf("the generator left %s out: %s", s.Path, s.Reason)
				}
			}
			if res.Files+len(skipped) != len(tr.files) || res.Files == 0 {
				t.Fatalf("%d files in the script and %d left out for %d selected", res.Files, len(skipped), len(tr.files))
			}
			out, code := run(t, res.Script)
			if code != 0 || strings.TrimSpace(out) != "" {
				t.Fatalf("exit code %d, and a simple script says nothing when it works:\n%s", code, out)
			}
			for i, f := range tr.files {
				if skipped[f.Path] != exists(tr.paths[i]) {
					t.Errorf("%s: left out %v, still there %v", f.Path, skipped[f.Path], exists(tr.paths[i]))
				}
			}
			tr.checkDecoys()
		})

		scenario(t, r, "simple: a folder goes with everything in it, and nothing next to it", func(t *testing.T, tr *tree) {
			rel := simpleFolder(r.target)
			d := tr.addDir(rel)
			res := gen(tr)
			if res.Folders != 1 || res.FolderFiles != d.Files || res.FolderDirs != d.Dirs {
				t.Fatalf("the generator counted %d folders with %d files and %d folders; the disk has %+v", res.Folders, res.FolderFiles, res.FolderDirs, d)
			}
			out, code := run(t, res.Script)
			if code != 0 || strings.TrimSpace(out) != "" {
				t.Fatalf("exit code %d\n%s", code, out)
			}
			if exists(tr.abs(rel)) {
				t.Errorf("the folder is still there")
			}
			tr.checkDecoys() // /name2/keep.txt, /other/name/keep.txt and /name.txt
		})

		scenario(t, r, "simple: a line that is left out leaves its file alone", func(t *testing.T, tr *tree) {
			res := gen(tr)
			eol := eolOf(r.target)
			lines := strings.Split(string(res.Script), eol)
			at := -1
			for i, l := range lines {
				if commandLine.MatchString(strings.TrimPrefix(l, psBOM)) {
					at = i
					break
				}
			}
			if at < 0 {
				t.Fatal("no command line in the script")
			}
			cut := append(append([]string(nil), lines[:at]...), lines[at+1:]...)
			before := len(tr.files)
			if out, code := run(t, []byte(strings.Join(cut, eol))); code != 0 {
				t.Fatalf("exit code %d\n%s", code, out)
			}
			present, _ := tr.remaining()
			left := leftOut(res)
			wantLeft := 1 + len(left) // the file whose line was cut, and the ones that were never in the script
			if len(present) != wantLeft {
				t.Errorf("%d of %d files are left, want %d (the one whose line was cut and %d the script never had)", len(present), before, wantLeft, len(left))
			}
			tr.checkDecoys()
		})

		scenario(t, r, "simple: a file that is already gone does not stop the lines after it", func(t *testing.T, tr *tree) {
			res := gen(tr)
			if err := os.Remove(tr.paths[0]); err != nil {
				t.Fatal(err)
			}
			run(t, res.Script) // a missing file is an error message for some shells and silence for others; the other lines run either way
			left := leftOut(res)
			for i, f := range tr.files {
				if !left[f.Path] && exists(tr.paths[i]) {
					t.Errorf("%s is still there: a line that failed stopped the script", f.Path)
				}
			}
			tr.checkDecoys()
		})

		if r.target == TargetSh {
			// rm does not follow a link: what it points at is not in the folder. The advanced script goes further and keeps a folder that holds a
			// link; the simple one does not look, but this much it gets from the command.
			scenario(t, r, "simple: a link inside a folder is removed, and what it points at is not", func(t *testing.T, tr *tree) {
				rel := simpleFolder(r.target)
				tr.addDir(rel)
				outside := tr.abs("outside target")
				writeFile(t, filepath.Join(outside, "precious.txt"), 8, epoch)
				tr.decoys = append(tr.decoys, filepath.Join(outside, "precious.txt"))
				res := gen(tr)
				makeDirLink(t, outside, tr.abs(rel+"/link to outside"))
				if out, code := run(t, res.Script); code != 0 {
					t.Fatalf("exit code %d\n%s", code, out)
				}
				if exists(tr.abs(rel)) {
					t.Errorf("the folder is still there")
				}
				tr.checkDecoys()
			})
		}
	}
}
