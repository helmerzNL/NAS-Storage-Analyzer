// Package mounts mounts NFS and SMB shares read-only inside the worker
// container for the duration of a scan. This route needs CAP_SYS_ADMIN; the
// safer alternative is to mount on the host and bind-mount the path (see the
// README), in which case this package only checks the path.
package mounts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
)

// Runner executes a command; tests replace it.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (output string, err error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// Manager mounts shares below Dir.
type Manager struct {
	Dir string // mount points are Dir/<share id>
	// CredDir holds the temporary SMB credential files (a tmpfs in the container).
	CredDir string
	Run     Runner
	Timeout time.Duration
}

// New returns a Manager with production defaults.
func New(dir string) *Manager {
	return &Manager{Dir: dir, CredDir: os.TempDir(), Run: execRunner{}, Timeout: 90 * time.Second}
}

// ErrUnavailable: this container cannot mount (no CAP_SYS_ADMIN or no mount helpers).
var ErrUnavailable = errors.New("mounting is not available in this container: mount the share on the host and bind-mount it " +
	"under the shares directory, or run the worker with the mount override (see the README)")

// Command returns the mount command for a share. credFile is the SMB
// credentials file; it is ignored for NFS.
func Command(f *shares.Full, mountpoint, credFile string) (name string, args []string, err error) {
	in := f.Input
	opts, err := shares.MountOptions(in.Kind, in.Version, in.MountOptions)
	if err != nil {
		return "", nil, err
	}
	switch in.Kind {
	case shares.KindNFS:
		return "mount", []string{"-t", "nfs", "-o", opts, in.Server + ":" + in.Export, mountpoint}, nil
	case shares.KindSMB:
		if in.Username == "" {
			opts += ",guest"
		} else {
			opts += ",credentials=" + credFile
		}
		return "mount", []string{"-t", "cifs", "-o", opts, "//" + in.Server + "/" + in.Share, mountpoint}, nil
	}
	return "", nil, fmt.Errorf("share kind %q is not mounted", in.Kind)
}

// credentials renders a cifs credentials file. Newlines are impossible in the
// validated user name and domain; the password may contain anything but a
// newline, which would end the line early.
func credentials(in shares.Input, password string) (string, error) {
	if strings.ContainsAny(password, "\r\n\x00") {
		return "", errors.New("the SMB password must not contain line breaks")
	}
	s := "username=" + in.Username + "\npassword=" + password + "\n"
	if in.Domain != "" {
		s += "domain=" + in.Domain + "\n"
	}
	return s, nil
}

// Prepare makes the share's files reachable and returns the directory to scan
// plus a cleanup function. For a bind share that is just the path.
func (m *Manager) Prepare(ctx context.Context, f *shares.Full) (path string, cleanup func(), err error) {
	noop := func() {}
	in := f.Input
	if in.Kind == shares.KindBind {
		fi, err := os.Stat(in.Path)
		if err != nil {
			return "", noop, fmt.Errorf("share path %s: %w (is the share mounted into the container?)", in.Path, err)
		}
		if !fi.IsDir() {
			return "", noop, fmt.Errorf("share path %s is not a directory", in.Path)
		}
		return in.Path, noop, nil
	}

	mp := filepath.Join(m.Dir, strconv.FormatInt(f.ID, 10))
	if err := os.MkdirAll(mp, 0o755); err != nil {
		return "", noop, fmt.Errorf("%w (%v)", ErrUnavailable, err)
	}
	ctx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()
	// A mount left by a crashed scan would hide new data; clear it first.
	_, _ = m.Run.Run(ctx, "umount", "-l", mp)

	cred := ""
	if in.Kind == shares.KindSMB && in.Username != "" {
		body, err := credentials(in, f.Password)
		if err != nil {
			return "", noop, err
		}
		file, err := os.CreateTemp(m.CredDir, "nasa-cred-*")
		if err != nil {
			return "", noop, err
		}
		cred = file.Name()
		defer os.Remove(cred) // cifs reads the file while mounting
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			return "", noop, err
		}
		_, werr := file.WriteString(body)
		if cerr := file.Close(); werr != nil || cerr != nil {
			return "", noop, errors.New("cannot write the credentials file")
		}
	}
	name, args, err := Command(f, mp, cred)
	if err != nil {
		return "", noop, err
	}
	if out, err := m.Run.Run(ctx, name, args...); err != nil {
		low := strings.ToLower(out + err.Error())
		if strings.Contains(low, "operation not permitted") || strings.Contains(low, "executable file not found") ||
			strings.Contains(low, "must be superuser") || strings.Contains(low, "unknown filesystem type") {
			return "", noop, fmt.Errorf("%w: %s", ErrUnavailable, firstLine(out, err))
		}
		return "", noop, fmt.Errorf("mount failed: %s", firstLine(out, err))
	}
	return mp, func() {
		uctx, ucancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ucancel()
		if _, err := m.Run.Run(uctx, "umount", mp); err != nil {
			_, _ = m.Run.Run(uctx, "umount", "-l", mp)
		}
	}, nil
}

func firstLine(out string, err error) string {
	if out == "" {
		return err.Error()
	}
	if i := strings.IndexByte(out, '\n'); i > 0 {
		out = out[:i]
	}
	return out
}
