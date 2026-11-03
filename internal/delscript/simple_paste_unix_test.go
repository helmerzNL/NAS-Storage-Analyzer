//go:build !windows

package delscript

import (
	"os/exec"
	"strings"
	"testing"
)

// What the simple script is for is pasting it into a terminal, and a terminal is a shell that reads its commands from the keyboard. These tests do that
// as far as a test can: the whole text, comment lines and blank line included, goes to the shell's standard input, which an interactive shell treats
// like the keyboard. An interactive bash also does history expansion, which is why a file name with an exclamation mark is in the tree.
func TestSimpleScriptCanBePastedIntoAShell(t *testing.T) {
	shells := []struct {
		name string
		bin  string
		args []string
	}{
		{"sh", "sh", nil}, {"dash", "dash", nil}, {"bash", "bash", nil},
		{"bash-interactive", "bash", []string{"--norc", "--noprofile", "-i"}},
	}
	for _, s := range shells {
		path, err := exec.LookPath(s.bin)
		if err != nil {
			if shellRequired(s.name) || shellRequired(s.bin) {
				t.Fatalf("%s is required but is not installed", s.bin)
			}
			t.Logf("%s is not installed, so it did not run here", s.bin)
			continue
		}
		s := s
		t.Run(s.name, func(t *testing.T) {
			tr := newTree(t, TargetSh)
			rel := simpleFolder(TargetSh)
			tr.addDir(rel)
			res := tr.generate(TargetSh, func(p *Params) { p.Style = StyleSimple })
			if len(res.Skipped) != 0 {
				t.Fatalf("left out: %s", names(res.Skipped))
			}
			out, code := execute(t, path, s.args, string(res.Script))
			if code != 0 {
				t.Fatalf("exit code %d\n%s", code, out)
			}
			if present, _ := tr.remaining(); len(present) != 0 {
				t.Errorf("still there: %s\n%s", tr.paths[present[0]], out)
			}
			if exists(tr.abs(rel)) {
				t.Errorf("the folder is still there\n%s", out)
			}
			tr.checkDecoys()
			// An interactive shell may complain that it has no terminal; it must not complain about the script.
			for _, bad := range []string{"command not found", "syntax error", "event not found", "No such file", "unexpected"} {
				if strings.Contains(out, bad) {
					t.Errorf("the shell says %q:\n%s", bad, out)
				}
			}
		})
	}
}
