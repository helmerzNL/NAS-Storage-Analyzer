//go:build windows

package delscript

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// runners returns cmd.exe, Windows PowerShell 5.1 and PowerShell 7 (when installed). cmd.exe and powershell.exe
// ship with Windows, so they are always required; pwsh is required when NASA_REQUIRE_SHELLS names it.
func runners(t *testing.T) []runner {
	cmdRunner := runner{name: "cmd", target: TargetCmd, eol: "\r\n"}
	cmdRunner.run = func(t *testing.T, script, stdin string) (string, int) {
		t.Helper()
		// cmd /s /c "" "<path>" "" is the form that keeps the quotes around a path with spaces.
		c := exec.Command("cmd.exe")
		c.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c ""` + script + `""`}
		return runCommand(t, c, stdin)
	}
	out := []runner{cmdRunner}
	for _, p := range []struct{ name, bin string }{{"powershell", "powershell.exe"}, {"pwsh", "pwsh.exe"}} {
		r := runner{name: p.name, target: TargetPowerShell, eol: "\r\n"}
		path, err := exec.LookPath(p.bin)
		switch {
		case err != nil && (p.name == "powershell" || shellRequired(p.name)):
			t.Fatalf("%s is required but is not installed", p.bin)
		case err != nil:
			r.absent = p.bin + " is not installed"
		default:
			r.run = func(t *testing.T, script, stdin string) (string, int) {
				t.Helper()
				return runCommand(t, exec.Command(path, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script), stdin)
			}
		}
		out = append(out, r)
	}
	return present(t, out)
}

func runCommand(t *testing.T, c *exec.Cmd, stdin string) (string, int) {
	t.Helper()
	c.Stdin = strings.NewReader(stdin)
	var buf bytes.Buffer
	c.Stdout, c.Stderr = &buf, &buf
	err := c.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return buf.String(), ee.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.String(), 0
}

// makeUndeletable marks the file read-only. Neither cmd's del (without /f) nor FileInfo.Delete removes it.
func makeUndeletable(t *testing.T, path string) func() {
	t.Helper()
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	return func() { os.Chmod(path, 0o644) }
}

// A base path that is a network path: \\localhost\C$\... reaches the same folder through the administrative share,
// which is how a share on a NAS looks to the script.
func TestUNCBasePath(t *testing.T) {
	for _, r := range runners(t) {
		r := r
		t.Run(r.name, func(t *testing.T) {
			if r.absent != "" {
				t.Skip(r.absent)
			}
			tr := newTree(t, r.target)
			vol := filepath.VolumeName(tr.root)
			if len(vol) != 2 || vol[1] != ':' {
				t.Skipf("no drive letter in %q", tr.root)
			}
			unc := `\\localhost\` + vol[:1] + `$` + tr.root[len(vol):]
			if _, err := os.Stat(unc); err != nil {
				t.Skipf("the administrative share is not reachable here: %v", err)
			}
			res := tr.generate(r.target, func(p *Params) { p.Root = unc })
			out, code := r.run(t, writeScript(t, r.target, res.Script), fmt.Sprintf("DELETE %d%s", res.Files, r.eol))
			if present, _ := tr.remaining(); code != 0 || len(present) != 0 {
				t.Errorf("exit %d, still there: %d\n%s", code, len(present), normalize(out))
			}
			tr.checkDecoys()
		})
	}
}

// A file another program has open cannot be deleted: reported as failed, never as deleted.
func TestAnOpenFileIsReportedAsFailed(t *testing.T) {
	for _, r := range runners(t) {
		r := r
		t.Run(r.name, func(t *testing.T) {
			if r.absent != "" {
				t.Skip(r.absent)
			}
			tr := newTree(t, r.target)
			idx := tr.addFile("/in use/held.txt")
			res := tr.generate(r.target, nil)
			f, err := os.Open(tr.paths[idx])
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			out, code := r.run(t, writeScript(t, r.target, res.Script), fmt.Sprintf("DELETE %d%s", res.Files, r.eol))
			out = normalize(out)
			if code != 1 || !strings.Contains(out, "FAILED: ") {
				t.Errorf("exit %d\n%s", code, out)
			}
			f.Close()
			if present, _ := tr.remaining(); len(present) != 1 || present[0] != idx {
				t.Errorf("only the open file must remain, got %v", present)
			}
		})
	}
}
