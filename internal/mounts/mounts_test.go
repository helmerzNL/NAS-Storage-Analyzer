package mounts_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/helmerznl/nas-storage-analyzer/internal/mounts"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
)

type call struct {
	name string
	args []string
	cred string // contents of the credentials file at call time
}

type fakeRunner struct {
	calls  []call
	failOn string
	out    string
	err    error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	c := call{name: name, args: args}
	for _, a := range args {
		if i := strings.Index(a, "credentials="); i >= 0 {
			b, _ := os.ReadFile(a[i+len("credentials="):])
			c.cred = string(b)
		}
	}
	f.calls = append(f.calls, c)
	if f.failOn != "" && name == "mount" {
		return f.out, f.err
	}
	return "", nil
}

func nfs() *shares.Full {
	in := shares.Defaults()
	in.Name, in.Kind, in.Server, in.Export, in.Version = "nas", shares.KindNFS, "nas.lan", "/volume1/media", "4.1"
	in.MountOptions = "soft,timeo=30"
	return &shares.Full{ID: 7, Input: in}
}

func smb(user string) *shares.Full {
	in := shares.Defaults()
	in.Name, in.Kind, in.Server, in.Share, in.Username, in.Domain, in.Version = "nas", shares.KindSMB, "nas.lan", "media", user, "HOME", "3.1.1"
	return &shares.Full{ID: 8, Input: in, Password: "p@ss word=1"}
}

func newManager(t *testing.T, r mounts.Runner) *mounts.Manager {
	m := mounts.New(filepath.Join(t.TempDir(), "managed"))
	m.CredDir = t.TempDir()
	m.Run = r
	return m
}

func TestNFSMountCommand(t *testing.T) {
	r := &fakeRunner{}
	m := newManager(t, r)
	path, cleanup, err := m.Prepare(context.Background(), nfs())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "/managed/7") {
		t.Errorf("mount point = %s", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("mount point not created: %v", err)
	}
	// First a lazy umount of any leftover, then the mount.
	if len(r.calls) != 2 || r.calls[0].name != "umount" || r.calls[0].args[0] != "-l" {
		t.Fatalf("calls = %+v", r.calls)
	}
	got := strings.Join(r.calls[1].args, " ")
	want := "-t nfs -o ro,vers=4.1,soft,timeo=30 nas.lan:/volume1/media " + path
	if r.calls[1].name != "mount" || got != want {
		t.Errorf("mount %s\nwant  %s", got, want)
	}
	cleanup()
	if last := r.calls[len(r.calls)-1]; last.name != "umount" || last.args[0] != path {
		t.Errorf("cleanup did not unmount: %+v", last)
	}
}

func TestSMBUsesACredentialsFileNotTheCommandLine(t *testing.T) {
	r := &fakeRunner{}
	m := newManager(t, r)
	path, cleanup, err := m.Prepare(context.Background(), smb("me"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	mount := r.calls[1]
	joined := strings.Join(mount.args, " ")
	if strings.Contains(joined, "p@ss") {
		t.Errorf("password on the command line: %s", joined)
	}
	if !strings.Contains(joined, "-t cifs") || !strings.Contains(joined, "//nas.lan/media") || !strings.Contains(joined, "ro,vers=3.1.1,credentials=") ||
		!strings.HasSuffix(joined, path) {
		t.Errorf("command = %s", joined)
	}
	if mount.cred != "username=me\npassword=p@ss word=1\ndomain=HOME\n" {
		t.Errorf("credentials file = %q", mount.cred)
	}
	// The file is gone once the mount is done.
	entries, _ := os.ReadDir(m.CredDir)
	if len(entries) != 0 {
		t.Errorf("credentials file left behind: %v", entries)
	}
}

func TestSMBGuest(t *testing.T) {
	r := &fakeRunner{}
	m := newManager(t, r)
	if _, _, err := m.Prepare(context.Background(), smb("")); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(r.calls[1].args, " "); !strings.Contains(joined, ",guest ") || strings.Contains(joined, "credentials") {
		t.Errorf("guest command = %s", joined)
	}
}

func TestPasswordWithNewlineIsRefused(t *testing.T) {
	f := smb("me")
	f.Password = "line1\nusername=root"
	if _, _, err := newManager(t, &fakeRunner{}).Prepare(context.Background(), f); err == nil {
		t.Error("a newline in the password could inject credentials-file keys")
	}
}

func TestMountFailureMessages(t *testing.T) {
	r := &fakeRunner{failOn: "mount", out: "mount.nfs: Operation not permitted", err: errors.New("exit status 32")}
	_, _, err := newManager(t, r).Prepare(context.Background(), nfs())
	if !errors.Is(err, mounts.ErrUnavailable) {
		t.Errorf("a permission failure must explain the capability problem: %v", err)
	}
	r = &fakeRunner{failOn: "mount", out: "mount.nfs: Connection timed out\nmore detail", err: errors.New("exit status 32")}
	_, _, err = newManager(t, r).Prepare(context.Background(), nfs())
	if err == nil || errors.Is(err, mounts.ErrUnavailable) || !strings.Contains(err.Error(), "Connection timed out") || strings.Contains(err.Error(), "more detail") {
		t.Errorf("other failures keep the first line of the message: %v", err)
	}
}

func TestBindShareChecksThePath(t *testing.T) {
	dir := t.TempDir()
	in := shares.Defaults()
	in.Path = dir
	m := newManager(t, &fakeRunner{})
	path, cleanup, err := m.Prepare(context.Background(), &shares.Full{ID: 1, Input: in})
	if err != nil || path != dir {
		t.Fatalf("bind: %q %v", path, err)
	}
	cleanup()
	in.Path = filepath.Join(dir, "missing")
	if _, _, err := m.Prepare(context.Background(), &shares.Full{ID: 1, Input: in}); err == nil || !strings.Contains(err.Error(), "mounted into the container") {
		t.Errorf("missing path: %v", err)
	}
	file := filepath.Join(dir, "f")
	os.WriteFile(file, nil, 0o644)
	in.Path = file
	if _, _, err := m.Prepare(context.Background(), &shares.Full{ID: 1, Input: in}); err == nil {
		t.Error("a file is not a share")
	}
}
