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

// runners returns the shells that can run on this machine: every POSIX shell found, and PowerShell where installed.
// A shell named in NASA_REQUIRE_SHELLS has to be there.
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
	pwsh := runner{name: "pwsh", target: TargetPowerShell, eol: "\n"}
	if path, err := exec.LookPath("pwsh"); err == nil {
		pwsh.run = func(t *testing.T, script, stdin string) (string, int) {
			return execute(t, path, []string{"-NoProfile", "-File", script}, stdin)
		}
	} else if shellRequired("pwsh") {
		t.Fatalf("pwsh is required by NASA_REQUIRE_SHELLS but is not installed")
	} else {
		pwsh.absent = "pwsh is not installed"
	}
	return present(t, append(out, pwsh))
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
