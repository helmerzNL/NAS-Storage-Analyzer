//go:build !windows

package delscript

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runners returns the POSIX shells that can run on this machine. A shell named in NASA_REQUIRE_SHELLS has to be there.
func runners(t *testing.T) []runner {
	var out []runner
	posix := []struct {
		name string
		bin  string
		args []string
	}{
		{"sh", "sh", nil}, {"dash", "dash", nil}, {"bash", "bash", nil}, {"bash-posix", "bash", []string{"--posix"}},
	}
	for _, s := range posix {
		path, err := exec.LookPath(s.bin)
		r := runner{name: s.name, target: TargetSh, eol: "\n"}
		switch {
		case err != nil && shellRequired(s.name):
			t.Fatalf("%s is required by NASA_REQUIRE_SHELLS but is not installed", s.name)
		case err != nil:
			r.absent = s.bin + " is not installed"
		default:
			args := s.args
			r.run = func(t *testing.T, script, stdin string) (string, int) {
				return execute(t, path, append(append([]string(nil), args...), script), stdin)
			}
		}
		out = append(out, r)
	}
	// PowerShell is not run here: the script is a Windows script (\\server\share and Z:\ paths, backslashes), and sh covers
	// Linux and macOS. runners_windows_test.go runs it in Windows PowerShell 5.1 and in PowerShell 7.
	return present(t, out)
}

func execute(t *testing.T, bin string, args []string, stdin string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return buf.String(), ee.ExitCode()
	}
	if err != nil {
		t.Fatalf("%s: %v", bin, err)
	}
	return buf.String(), 0
}

// makeUndeletable makes path impossible to remove without touching the file itself: its folder loses write permission.
func makeUndeletable(t *testing.T, path string) func() {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions; run as another user to cover this")
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	return func() { os.Chmod(dir, 0o755) }
}

// makeDirLink makes link a link to the folder target: a symbolic link here, a junction on Windows.
func makeDirLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("cannot make a symbolic link: %v", err) // not a skip: CI fails on a skipped test, and a link is what this proves
	}
}
