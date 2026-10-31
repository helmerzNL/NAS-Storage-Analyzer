package shares_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	"github.com/helmerznl/nas-storage-analyzer/internal/secrets"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

var ctx = context.Background()

func valid() shares.Input {
	in := shares.Defaults()
	in.Name, in.Path = "media", "/mnt/shares/media"
	return in
}

func field(err error) string {
	var v *shares.ValidationError
	if errors.As(err, &v) {
		return v.Field
	}
	return ""
}

func TestValidateBind(t *testing.T) {
	opt := shares.Options{SharesRoot: "/mnt/shares"}
	in := valid()
	if err := shares.Validate(&in, opt); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{"": "path", "relative/dir": "path", "/etc": "path", "/mnt/shares/../etc": "path", "/mnt/sharesX/a": "path"}
	for p, wantField := range cases {
		in := valid()
		in.Path = p
		if err := shares.Validate(&in, opt); field(err) != wantField {
			t.Errorf("path %q: %v", p, err)
		}
	}
	in = valid()
	in.Path = "/mnt/shares//media/./sub/"
	if err := shares.Validate(&in, opt); err != nil || in.Path != "/mnt/shares/media/sub" {
		t.Errorf("path not normalised: %q %v", in.Path, err)
	}
	in = valid()
	in.Path = "/mnt/shares"
	if err := shares.Validate(&in, opt); err != nil {
		t.Errorf("the root itself must be allowed: %v", err)
	}
	in = valid()
	in.Server = "nas"
	if err := shares.Validate(&in, opt); field(err) != "kind" {
		t.Errorf("bind share with a server: %v", err)
	}
}

// The shares root is NASA_SHARES_MOUNT, so it need not be /mnt/shares: the check and the example in
// the error message must follow it.
func TestValidateBindFollowsConfiguredRoot(t *testing.T) {
	opt := shares.Options{SharesRoot: "/mnt/user"}
	in := valid()
	in.Path = "/mnt/user/media"
	if err := shares.Validate(&in, opt); err != nil {
		t.Fatalf("a path below the configured root: %v", err)
	}
	in = valid() // /mnt/shares/media is outside this root
	if err := shares.Validate(&in, opt); field(err) != "path" || !strings.Contains(err.Error(), "/mnt/user") {
		t.Errorf("a path outside the configured root: %v", err)
	}
	in = valid()
	in.Path = "media"
	err := shares.Validate(&in, opt)
	if field(err) != "path" || !strings.Contains(err.Error(), "/mnt/user/media") {
		t.Errorf("a relative path must show an example below the configured root: %v", err)
	}
}

func TestValidateFields(t *testing.T) {
	opt := shares.Options{}
	cases := []struct {
		name  string
		mut   func(*shares.Input)
		field string
	}{
		{"empty name", func(i *shares.Input) { i.Name = "  " }, "name"},
		{"name with slash", func(i *shares.Input) { i.Name = "a/b" }, "name"},
		{"long name", func(i *shares.Input) { i.Name = strings.Repeat("x", 65) }, "name"},
		{"bad kind", func(i *shares.Input) { i.Kind = "ftp" }, "kind"},
		{"bad pattern", func(i *shares.Input) { i.Excludes = []string{"[unclosed"} }, "excludes"},
		{"too many patterns", func(i *shares.Input) {
			i.Excludes = make([]string, 101)
			for k := range i.Excludes {
				i.Excludes[k] = "x"
			}
		}, "excludes"},
		{"workers 0", func(i *shares.Input) { i.Workers = 0 }, "workers"},
		{"workers 1000", func(i *shares.Input) { i.Workers = 1000 }, "workers"},
		{"negative ops", func(i *shares.Input) { i.MaxOpsPerSec = -1 }, "max_ops_per_sec"},
		{"hour", func(i *shares.Input) { i.DayStartHour = 24 }, "day_start_hour"},
		{"depth", func(i *shares.Input) { i.SnapshotDepth = 13 }, "snapshot_depth"},
		{"basis", func(i *shares.Input) { i.LastUsedBasis = "ctime" }, "last_used_basis"},
		{"cron", func(i *shares.Input) { i.Cron = "every night" }, "cron"},
		{"cron six fields", func(i *shares.Input) { i.Cron = "0 0 3 * * *" }, "cron"},
	}
	for _, c := range cases {
		in := valid()
		c.mut(&in)
		if err := shares.Validate(&in, opt); field(err) != c.field {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	in := valid()
	in.Excludes = []string{" .snapshot ", "", "@eaDir"}
	in.Cron = "0 3 * * *"
	if err := shares.Validate(&in, opt); err != nil || len(in.Excludes) != 2 || in.Excludes[0] != ".snapshot" {
		t.Errorf("good input rejected or not trimmed: %v %v", err, in.Excludes)
	}
}

// The base paths of the delete scripts are checked as hard as the generator checks them, because what is stored here
// ends up in a script.
func TestValidateClientRoots(t *testing.T) {
	in := valid()
	in.ClientWindows, in.ClientPosix = `  //NAS/Media//Films/ `, "/mnt/user//media/"
	if err := shares.Validate(&in, shares.Options{}); err != nil {
		t.Fatal(err)
	}
	if in.ClientWindows != `\\NAS\Media\Films` || in.ClientPosix != "/mnt/user/media" {
		t.Errorf("not normalised: %q %q", in.ClientWindows, in.ClientPosix)
	}
	in = valid()
	in.ClientWindows, in.ClientPosix = "z:", "   "
	if err := shares.Validate(&in, shares.Options{}); err != nil || in.ClientWindows != `Z:\` || in.ClientPosix != "" {
		t.Errorf("drive letter and blank: %q %q %v", in.ClientWindows, in.ClientPosix, err)
	}
	for name, c := range map[string]struct{ windows, posix, field string }{
		"posix path for Windows":    {"/mnt/user/media", "", "client_windows"},
		"percent breaks cmd":        {`\\nas\100%`, "", "client_windows"},
		"long-path prefix":          {`\\?\C:\x`, "", "client_windows"},
		"control character":         {"Z:\\a\nb", "", "client_windows"},
		"Windows path for Linux":    {"", `\\nas\media`, "client_posix"},
		"relative path":             {"", "media/films", "client_posix"},
		"dot-dot":                   {"", "/mnt/../etc", "client_posix"},
		"newline in a Linux path":   {"", "/mnt/a\nrm -rf ~", "client_posix"},
		"quote in a Linux path":     {"", `/mnt/a"b`, "client_posix"},
		"bar in a Linux path":       {"", "/mnt/a|b", "client_posix"},
		"replacement character too": {"", "/mnt/a\ufffdb", "client_posix"},
	} {
		in := valid()
		in.ClientWindows, in.ClientPosix = c.windows, c.posix
		if err := shares.Validate(&in, shares.Options{}); field(err) != c.field {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestValidateMountedShares(t *testing.T) {
	nfs := valid()
	nfs.Kind, nfs.Path, nfs.Server, nfs.Export = shares.KindNFS, "", "nas.lan", "/volume1/media"
	if err := shares.Validate(&nfs, shares.Options{}); err != nil || nfs.Version != "3" {
		t.Errorf("nfs: %v version=%s", err, nfs.Version)
	}
	smb := valid()
	smb.Kind, smb.Path, smb.Server, smb.Share, smb.Username = shares.KindSMB, "", "192.168.1.5", "media", "me"
	if err := shares.Validate(&smb, shares.Options{}); err != nil || smb.Version != "3.0" {
		t.Errorf("smb: %v version=%s", err, smb.Version)
	}
	bad := []struct {
		mut   func(*shares.Input)
		field string
	}{
		{func(i *shares.Input) { i.Server = "nas;reboot" }, "server"},
		{func(i *shares.Input) { i.Server = "-oProxyCommand=x" }, "server"},
		{func(i *shares.Input) { i.Export = "relative" }, "export"},
		{func(i *shares.Input) { i.Export = "/a/../etc" }, "export"},
		{func(i *shares.Input) { i.Export = "/a,rw" }, "export"},
		{func(i *shares.Input) { i.Version = "5" }, "version"},
		{func(i *shares.Input) { i.MountOptions = "rw" }, "mount_options"},
		{func(i *shares.Input) { i.MountOptions = "soft,vers=4.2" }, "mount_options"},
		{func(i *shares.Input) { i.MountOptions = "nconnect=99" }, "mount_options"},
		{func(i *shares.Input) { i.MountOptions = "soft; rm -rf /" }, "mount_options"},
		{func(i *shares.Input) { i.Path = "/mnt/shares/x" }, "kind"},
	}
	for i, b := range bad {
		in := nfs
		b.mut(&in)
		if err := shares.Validate(&in, shares.Options{}); field(err) != b.field {
			t.Errorf("nfs case %d: %v", i, err)
		}
	}
	for _, b := range []func(*shares.Input){
		func(i *shares.Input) { i.Share = "a/b" }, func(i *shares.Input) { i.Share = "" },
		func(i *shares.Input) { i.Username = "me\nrw" }, func(i *shares.Input) { i.Domain = "x y;" },
		func(i *shares.Input) { i.MountOptions = "uid=0" }, func(i *shares.Input) { i.Version = "1.0" },
	} {
		in := smb
		b(&in)
		if err := shares.Validate(&in, shares.Options{}); err == nil {
			t.Errorf("smb accepted %+v", in)
		}
	}
}

func TestMountOptionsAlwaysReadOnly(t *testing.T) {
	got, err := shares.MountOptions(shares.KindNFS, "4.1", "soft, timeo=30,nconnect=4,soft")
	if err != nil || got != "ro,vers=4.1,soft,timeo=30,nconnect=4" {
		t.Errorf("nfs options = %q %v", got, err)
	}
	got, err = shares.MountOptions(shares.KindSMB, "3.1.1", "cache=none,iocharset=utf8")
	if err != nil || got != "ro,vers=3.1.1,cache=none,iocharset=utf8" {
		t.Errorf("smb options = %q %v", got, err)
	}
	for _, evil := range []string{"rw", "ro=0", "remount", "bind", "nosuid,exec", "context=x", "fsc", "sync"} {
		if _, err := shares.MountOptions(shares.KindNFS, "3", evil); err == nil {
			t.Errorf("nfs accepted %q", evil)
		}
		if _, err := shares.MountOptions(shares.KindSMB, "3.0", evil); err == nil {
			t.Errorf("smb accepted %q", evil)
		}
	}
}

func newService(t *testing.T) (*shares.Service, *ts.DBHandle) {
	t.Helper()
	st := ts.NewStore(t)
	box, _ := secrets.New(make([]byte, secrets.KeySize))
	return shares.NewService(st.Pool, box, shares.Options{SharesRoot: "/mnt/shares"}, time.UTC), &ts.DBHandle{Store: st}
}

func TestCreateLoadUpdateDelete(t *testing.T) {
	svc, db := newService(t)
	in := valid()
	in.Cron = "0 3 * * *"
	id, err := svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, in); !errors.Is(err, shares.ErrConflict) {
		t.Errorf("duplicate name: %v", err)
	}
	// Partitions exist, so the share can be scanned.
	var n int
	db.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relname IN ('files_s`+itoa(id)+`', 'dirs_s`+itoa(id)+`')`).Scan(&n)
	if n != 2 {
		t.Errorf("partitions = %d", n)
	}
	f, err := svc.Load(ctx, id)
	if err != nil || f.Input.Path != "/mnt/shares/media" || f.Input.Cron != "0 3 * * *" || f.Input.Workers != 16 {
		t.Fatalf("load: %+v %v", f, err)
	}
	var next *time.Time
	db.Store.Pool.QueryRow(ctx, `SELECT next_run_at FROM shares WHERE id = $1`, id).Scan(&next)
	if next == nil || !next.After(time.Now()) {
		t.Errorf("next_run_at = %v", next)
	}

	// Partial update keeps what is not mentioned and recomputes the schedule.
	if err := svc.Update(ctx, id, []byte(`{"workers": 4, "cron": ""}`)); err != nil {
		t.Fatal(err)
	}
	f, _ = svc.Load(ctx, id)
	if f.Input.Workers != 4 || f.Input.Path != "/mnt/shares/media" || f.Input.Cron != "" || len(f.Input.Excludes) != 4 {
		t.Errorf("after update: %+v", f.Input)
	}
	db.Store.Pool.QueryRow(ctx, `SELECT next_run_at FROM shares WHERE id = $1`, id).Scan(&next)
	if next != nil {
		t.Errorf("schedule not cleared: %v", next)
	}
	if err := svc.Update(ctx, id, []byte(`{"workers": 0}`)); field(err) != "workers" {
		t.Errorf("invalid update: %v", err)
	}
	if err := svc.Update(ctx, id, []byte(`not json`)); err == nil {
		t.Error("garbage accepted")
	}
	if err := svc.Update(ctx, 9999, []byte(`{}`)); !errors.Is(err, shares.ErrNotFound) {
		t.Errorf("update unknown: %v", err)
	}
	other := valid()
	other.Name = "other"
	otherID, _ := svc.Create(ctx, other)
	if err := svc.Update(ctx, otherID, []byte(`{"name": "media"}`)); !errors.Is(err, shares.ErrConflict) {
		t.Errorf("rename onto an existing name: %v", err)
	}

	// A share with a waiting job cannot be deleted.
	jobs.NewQueue(db.Store.Pool).Enqueue(ctx, jobs.KindScan, id, "auto", "manual", "")
	if err := svc.Delete(ctx, id); !errors.Is(err, shares.ErrBusy) {
		t.Errorf("delete while queued: %v", err)
	}
	db.Store.Pool.Exec(ctx, `DELETE FROM jobs`)
	if err := svc.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	db.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relname IN ('files_s`+itoa(id)+`', 'dirs_s`+itoa(id)+`')`).Scan(&n)
	if n != 0 {
		t.Errorf("partitions of a deleted share remain: %d", n)
	}
	if err := svc.Delete(ctx, id); !errors.Is(err, shares.ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
}

// A base path is stored in the config column next to the settings of the kind, so everything that rewrites that
// column has to carry it along: a partial update of something else, and the scan command's EnsureShare.
func TestClientRootsSurviveOtherChanges(t *testing.T) {
	svc, db := newService(t)
	in := valid()
	in.ClientWindows, in.ClientPosix = `\\NAS\Media`, "/mnt/user/media"
	id, err := svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := svc.Load(ctx, id)
	if f.Input.ClientWindows != `\\NAS\Media` || f.Input.ClientPosix != "/mnt/user/media" {
		t.Fatalf("after create: %+v", f.Input)
	}

	if err := svc.Update(ctx, id, []byte(`{"cron": "0 3 * * *", "workers": 2}`)); err != nil {
		t.Fatal(err)
	}
	f, _ = svc.Load(ctx, id)
	if f.Input.ClientWindows != `\\NAS\Media` || f.Input.ClientPosix != "/mnt/user/media" {
		t.Errorf("a partial update lost the base paths: %+v", f.Input)
	}

	if _, err := db.Store.EnsureShare(ctx, "media", "/mnt/shares/elsewhere", nil, 3, 0); err != nil {
		t.Fatal(err)
	}
	f, _ = svc.Load(ctx, id)
	if f.Input.Path != "/mnt/shares/elsewhere" || f.Input.ClientWindows != `\\NAS\Media` || f.Input.ClientPosix != "/mnt/user/media" {
		t.Errorf("EnsureShare must change the path and keep the base paths: %+v", f.Input)
	}

	// One of them is changed, the other is cleared with an empty string, and an invalid one changes nothing.
	if err := svc.Update(ctx, id, []byte(`{"client_windows": "", "client_posix": "/volume1/media"}`)); err != nil {
		t.Fatal(err)
	}
	f, _ = svc.Load(ctx, id)
	if f.Input.ClientWindows != "" || f.Input.ClientPosix != "/volume1/media" {
		t.Errorf("after change and clear: %+v", f.Input)
	}
	if err := svc.Update(ctx, id, []byte(`{"client_posix": "relative"}`)); field(err) != "client_posix" {
		t.Errorf("invalid base path: %v", err)
	}
	f, _ = svc.Load(ctx, id)
	if f.Input.ClientPosix != "/volume1/media" {
		t.Errorf("an invalid update changed the stored path: %q", f.Input.ClientPosix)
	}
}

func TestSMBPasswordIsEncryptedAndNeverReturned(t *testing.T) {
	svc, db := newService(t)
	in := valid()
	in.Kind, in.Path, in.Server, in.Share, in.Username = shares.KindSMB, "", "nas.lan", "media", "me"
	pw := "s3cret-Pa$$word"
	in.Password = &pw
	id, err := svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var cfg string
	db.Store.Pool.QueryRow(ctx, `SELECT secret_enc, config::text FROM shares WHERE id = $1`, id).Scan(&raw, &cfg)
	if len(raw) == 0 || strings.Contains(string(raw), "s3cret") || strings.Contains(cfg, "s3cret") {
		t.Errorf("password stored in clear: %q %s", raw, cfg)
	}
	f, _ := svc.Load(ctx, id)
	if f.Password != pw {
		t.Errorf("decrypted password = %q", f.Password)
	}
	// An update without a password keeps it; an empty string removes it.
	svc.Update(ctx, id, []byte(`{"workers": 2}`))
	if f, _ = svc.Load(ctx, id); f.Password != pw {
		t.Error("password lost by an unrelated update")
	}
	svc.Update(ctx, id, []byte(`{"password": ""}`))
	if f, _ = svc.Load(ctx, id); f.Password != "" {
		t.Error("password not removed")
	}
	svc.Update(ctx, id, []byte(`{"password": "new-one"}`))
	if f, _ = svc.Load(ctx, id); f.Password != "new-one" {
		t.Error("password not replaced")
	}
	// Changing to a bind share drops the secret.
	svc.Update(ctx, id, []byte(`{"kind":"bind","path":"/mnt/shares/x","server":"","share":"","username":""}`))
	db.Store.Pool.QueryRow(ctx, `SELECT secret_enc FROM shares WHERE id = $1`, id).Scan(&raw)
	if len(raw) != 0 {
		t.Error("secret kept after the share became a bind share")
	}
	b, _ := json.Marshal(valid())
	if strings.Contains(string(b), "password") {
		t.Errorf("Input JSON exposes a password field when unset: %s", b)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
