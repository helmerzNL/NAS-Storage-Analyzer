package backup_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
	"github.com/helmerznl/nas-storage-analyzer/internal/backup"
	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	"github.com/helmerznl/nas-storage-analyzer/internal/secrets"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

var fixed = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type env struct {
	t   *testing.T
	st  *store.Store
	box *secrets.Box
	sh  *shares.Service
	au  *auth.Service
	q   *jobs.Queue
	bk  *backup.Service
}

// newEnv is a migrated database with the services on it. key picks the encryption key: two environments with different keys cannot read each
// other's sealed passwords. Restores run inline, so a test can look at the result as soon as Start returns.
func newEnv(t *testing.T, key byte) *env {
	t.Helper()
	st := ts.NewStore(t)
	k := bytes.Repeat([]byte{key}, secrets.KeySize)
	box, err := secrets.New(k)
	if err != nil {
		t.Fatal(err)
	}
	sh := shares.NewService(st.Pool, box, shares.Options{SharesRoot: "/mnt/shares"}, time.UTC)
	au := auth.NewService(st.Pool, box, auth.DefaultConfig())
	q := jobs.NewQueue(st.Pool)
	bk := backup.New(st.Pool, sh, au, q, nil, "9.9.9")
	bk.Now = func() time.Time { return fixed }
	bk.Go = func(f func()) { f() }
	return &env{t: t, st: st, box: box, sh: sh, au: au, q: q, bk: bk}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func bind(name, path string) shares.Input {
	in := shares.Defaults()
	in.Name, in.Path = name, path
	return in
}

func (e *env) addShare(in shares.Input) int64 {
	e.t.Helper()
	id, err := e.sh.Create(context.Background(), in)
	if err != nil {
		e.t.Fatalf("create share %q: %v", in.Name, err)
	}
	return id
}

const smbPassword = "plain-smb-password-12345"

// seed fills an installation the way an owner would have: three kinds of share (one with an SMB password), an edited file-type map, and two
// people with preferences.
func (e *env) seed() {
	e.t.Helper()
	media := bind("media", "/mnt/shares/media")
	media.Excludes = []string{"@eaDir", "*.tmp"}
	media.Cron = "0 3 * * *"
	media.ClientWindows = `\\nas\media`
	media.ClientPosix = "/mnt/user/media"
	media.MaxOpsPerSec, media.MaxOpsDay, media.RetentionDays, media.FullScanEvery, media.LastUsedBasis, media.SnapshotDepth = 500, 100, 365, 3, "max", 6
	e.addShare(media)
	nfs := shares.Defaults()
	nfs.Name, nfs.Kind, nfs.Server, nfs.Export, nfs.Version, nfs.Workers = "nas", shares.KindNFS, "nas.local", "/volume1/data", "4.1", 8
	nfs.MountOptions = "nolock,noatime"
	e.addShare(nfs)
	smb := shares.Defaults()
	pw := smbPassword
	smb.Name, smb.Kind, smb.Server, smb.Share, smb.Username, smb.Password, smb.Version = "docs", shares.KindSMB, "nas.local", "docs", "anna", &pw, "3.0"
	smb.Domain = "WORKGROUP"
	e.addShare(smb)
	if _, err := e.sh.SetExtension(context.Background(), "xyz", 1); err != nil { // video
		e.t.Fatal(err)
	}
	if err := e.sh.DeleteExtension(context.Background(), "mp3"); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO users (username, role, password_hash, prefs) VALUES
		('helmer', 'admin', '$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$aGFzaGhhc2hoYXNo', '{"language":"nl","theme":"dark","accent":"#0c9aa6"}'),
		('Anna', 'viewer', '$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$b3RoZXJoYXNo', '{"units":"decimal"}'),
		('quiet', 'viewer', NULL, '{}')`)
}

func (e *env) export() []byte {
	e.t.Helper()
	b, err := e.bk.Export(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

// restore stages a file and starts the restore as the given person; Start runs it inline, so the job it returns... is read again here.
func (e *env) restore(file []byte, by string) *backup.Job {
	e.t.Helper()
	st, err := e.bk.Stage(context.Background(), by, bytes.NewReader(file))
	if err != nil {
		e.t.Fatalf("stage: %v", err)
	}
	return e.start(by, st.Token)
}

func (e *env) start(by, token string) *backup.Job {
	e.t.Helper()
	j, err := e.bk.Start(context.Background(), auth.Actor{Username: by}, token)
	if err != nil {
		e.t.Fatalf("start: %v", err)
	}
	return e.job(j.ID)
}

func (e *env) job(id int64) *backup.Job {
	e.t.Helper()
	j, err := e.bk.Latest(context.Background())
	if err != nil || j == nil || j.ID != id {
		e.t.Fatalf("latest job: %v %v", j, err)
	}
	return j
}

func names(l []shares.Portable) []string {
	out := []string{}
	for _, s := range l {
		out = append(out, s.Name)
	}
	return out
}

// ---- round trip --------------------------------------------------------------------------------------

func TestRoundTripRestoresWhatWasBackedUp(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	a := e.export()

	// Change everything the file covers, in every way a restore has to undo.
	ctx := context.Background()
	list, _ := e.sh.Export(ctx)
	var mediaID, nasID int64
	for _, s := range list {
		var id int64
		e.st.Pool.QueryRow(ctx, `SELECT id FROM shares WHERE name = $1`, s.Name).Scan(&id)
		switch s.Name {
		case "media":
			mediaID = id
		case "nas":
			nasID = id
		}
	}
	e.exec(`DELETE FROM shares WHERE id = $1`, mediaID)
	e.exec(`SELECT drop_share_partitions($1)`, mediaID)
	if err := e.sh.Update(ctx, nasID, []byte(`{"workers": 99, "cron": "5 5 * * *"}`)); err != nil {
		t.Fatal(err)
	}
	e.addShare(bind("extra", "/mnt/shares/extra"))
	e.sh.SetExtension(ctx, "mp3", 3)
	e.sh.DeleteExtension(ctx, "xyz")
	e.exec(`UPDATE users SET prefs = '{"language":"en"}' WHERE username = 'helmer'`)
	if string(e.export()) == string(a) {
		t.Fatal("the test did not change anything")
	}

	j := e.restore(a, "helmer")
	if j.Status != "done" {
		t.Fatalf("restore: %+v", j)
	}
	if b := e.export(); string(b) != string(a) {
		t.Fatalf("restored is not equal to backed up:\n--- backed up\n%s\n--- restored\n%s", a, b)
	}
	c := j.Summary
	// "docs" was not touched, so it is not said to be replaced; "nas" was; "media" was deleted and comes back; "extra" goes.
	if strings.Join(c.SharesAdded, ",") != "media" || strings.Join(c.SharesUpdated, ",") != "nas" || strings.Join(c.SharesRemoved, ",") != "extra" {
		t.Errorf("summary of the shares: %+v", c)
	}
	if c.FileTypesChanged != 2 || c.PreferencesApplied != 1 { // helmer's changed back; Anna's was never touched
		t.Errorf("summary: %+v", c)
	}
	// The password came back as a password: the sealed blob is the one that was in the file, and it opens with the key of this installation.
	var docs int64
	e.st.Pool.QueryRow(ctx, `SELECT id FROM shares WHERE name = 'docs'`).Scan(&docs)
	full, err := e.sh.Load(ctx, docs)
	if err != nil || full.Password != smbPassword {
		t.Errorf("the SMB password after a restore: %q, %v", full.Password, err)
	}
	// A schedule that came back is scheduled: next_run_at is worked out again, not copied.
	if n := e.count(`SELECT count(*) FROM shares WHERE cron <> '' AND next_run_at IS NULL`); n != 0 {
		t.Errorf("%d restored shares have a schedule but no next run", n)
	}
}

func TestRestoreKeepsTheDataOfSharesThatStayAndDropsTheRest(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	ctx := context.Background()
	var keep, gone int64
	e.st.Pool.QueryRow(ctx, `SELECT id FROM shares WHERE name = 'media'`).Scan(&keep)
	e.st.Pool.QueryRow(ctx, `SELECT id FROM shares WHERE name = 'nas'`).Scan(&gone)
	for _, id := range []int64{keep, gone} {
		e.exec(`INSERT INTO dirs (share_id, id, parent_id, name, depth, path, mtime, entry_count, last_seen_scan) VALUES ($1, 1, NULL, '', 0, '', now(), 0, 1)`, id)
	}
	var f backup.File
	if err := json.Unmarshal(e.export(), &f); err != nil {
		t.Fatal(err)
	}
	// The file has "media" and "docs" and a share that does not exist yet; it does not have "nas".
	kept := f.Shares[:0]
	for _, s := range f.Shares {
		if s.Name != "nas" {
			kept = append(kept, s)
		}
	}
	f.Shares = append(kept, shares.Portable{Input: bind("fresh", "/mnt/shares/fresh")})
	file, _ := json.Marshal(f)

	if j := e.restore(file, "helmer"); j.Status != "done" {
		t.Fatalf("%+v", j)
	}
	if n := e.count(`SELECT count(*) FROM dirs WHERE share_id = $1`, keep); n != 1 {
		t.Errorf("a share that stays lost its scan data (%d rows)", n)
	}
	if n := e.count(`SELECT count(*) FROM shares WHERE id = $1`, gone); n != 0 {
		t.Error("a share the file does not have is still there")
	}
	if n := e.count(`SELECT count(*) FROM pg_tables WHERE tablename = $1`, fmt.Sprintf("dirs_s%d", gone)); n != 0 {
		t.Error("the partitions of a removed share are still there")
	}
	var fresh int64
	e.st.Pool.QueryRow(ctx, `SELECT id FROM shares WHERE name = 'fresh'`).Scan(&fresh)
	if n := e.count(`SELECT count(*) FROM pg_tables WHERE tablename = $1`, fmt.Sprintf("dirs_s%d", fresh)); n != 1 {
		t.Error("a new share has no partitions to scan into")
	}
}

// ---- rule 1: the file never unlocks the app ----------------------------------------------------------------

func TestTheFileHoldsNothingThatUnlocksTheApplication(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	// Every kind of thing that unlocks the application, with a value that can be looked for.
	e.exec(`UPDATE users SET totp_secret_enc = '\x7071727374757677', totp_enabled = true WHERE username = 'helmer'`)
	var uid int64
	e.st.Pool.QueryRow(context.Background(), `SELECT id FROM users WHERE username = 'helmer'`).Scan(&uid)
	e.exec(`INSERT INTO recovery_codes (user_id, code_hash) VALUES ($1, '\x52454331524543315245433152454331')`, uid)
	e.exec(`INSERT INTO webauthn_credentials (credential_id, user_id, name, public_key) VALUES ('\x43524544494431434544', $1, 'laptop', '\x5055424b4559313233')`, uid)
	e.exec(`INSERT INTO sessions (token_hash, user_id, stage, csrf_token, idle_expires_at, absolute_expires_at) VALUES ('\x5345535349304e', $1, 'active', 'csrf-value-xyz', now() + interval '1 day', now() + interval '1 day')`, uid)
	e.exec(`INSERT INTO invitations (token_hash, kind, username, role, expires_at) VALUES ('\x494e564954453132', 'new', 'guest', 'admin', now() + interval '1 day')`)
	e.exec(`INSERT INTO login_failures (scope, key) VALUES ('ip', '203.0.113.9')`)
	e.exec(`INSERT INTO audit_log (username, action, ip) VALUES ('helmer', 'auth.login', '198.51.100.7')`)

	file := e.export()
	text := string(file)
	for _, what := range []string{
		"$argon2id", "aGFzaGhhc2hoYXNo", // the password hash
		smbPassword,                                   // the SMB password in plain text
		"pqrstuvw", "7071727374757677", "cHFyc3R1dnc", // the authenticator secret as text, hex and base64
		"REC1REC1", "CREDID1CED", "PUBKEY123", "SESSI0N", "INVITE12", // the other secrets as text
		"csrf-value-xyz", "203.0.113.9", "198.51.100.7",
		"password_hash", "totp", "recovery", "webauthn", "credential", "public_key", "csrf", "token_hash", "session", "invitation", "login_failures",
		`"role"`, `"admin"`, `"viewer"`, "audit",
	} {
		if strings.Contains(text, what) {
			t.Errorf("the backup contains %q", what)
		}
	}
	for _, raw := range [][]byte{[]byte("pqrstuvw"), []byte("REC1REC1REC1REC1"), []byte("CREDID1CED"), []byte("PUBKEY123"), []byte("SESSI0N"), []byte("INVITE12")} {
		for _, enc := range []string{base64.StdEncoding.EncodeToString(raw), base64.RawStdEncoding.EncodeToString(raw), base64.URLEncoding.EncodeToString(raw), hex.EncodeToString(raw)} {
			if strings.Contains(text, enc) {
				t.Errorf("the backup contains %q in encoded form", enc)
			}
		}
	}

	// The one allowed exception: the SMB password, as the ciphertext the database already holds, which opens only with the key in the environment.
	var f backup.File
	if err := json.Unmarshal(file, &f); err != nil {
		t.Fatal(err)
	}
	sealed := 0
	for _, s := range f.Shares {
		if len(s.Secret) == 0 {
			continue
		}
		sealed++
		if s.Kind != shares.KindSMB || bytes.Contains(s.Secret, []byte(smbPassword)) {
			t.Errorf("share %q carries a secret that is not a sealed SMB password", s.Name)
		}
		pw, err := e.box.Open(s.Secret)
		if err != nil || string(pw) != smbPassword {
			t.Errorf("the secret of %q does not open with the key: %v", s.Name, err)
		}
		other, _ := secrets.New(bytes.Repeat([]byte{2}, secrets.KeySize))
		if _, err := other.Open(s.Secret); err == nil {
			t.Error("the secret opens with another key: it is not sealed")
		}
	}
	if sealed != 1 {
		t.Errorf("%d shares carry a secret, want 1", sealed)
	}
	// Preferences name people and nothing else about them.
	for _, p := range f.Preferences {
		if p.Username == "" || len(p.Prefs) == 0 {
			t.Errorf("a preference entry says nothing: %+v", p)
		}
	}
}

func TestAPasswordInPlainTextIsRefused(t *testing.T) {
	e := newEnv(t, 1)
	file := `{"format":"nas-storage-analyzer-backup","format_version":1,"shares":[{"name":"s","kind":"smb","server":"nas","share":"d","version":"3.0",
		"password":"hunter2","excludes":[],"workers":4,"day_start_hour":8,"day_end_hour":22,"last_used_basis":"atime","full_scan_every":7,"retention_scans":10,"snapshot_depth":4}]}`
	_, err := e.bk.Stage(context.Background(), "helmer", strings.NewReader(file))
	var inv *backup.InvalidError
	if !errors.As(err, &inv) || inv.Field != "shares[0].password" {
		t.Fatalf("a plain password must be refused, got %v", err)
	}
}

func TestASecretThatThisKeyCannotOpenIsLeftOutAndSaid(t *testing.T) {
	a := newEnv(t, 1)
	a.seed()
	file := a.export()
	b := newEnv(t, 2) // another installation: another key
	st, err := b.bk.Stage(context.Background(), "helmer", bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Summary.Changes.PasswordsUnreadable; len(got) != 1 || got[0] != "docs" {
		t.Fatalf("the confirmation must say which password cannot come back: %v", got)
	}
	j := b.start("helmer", st.Token)
	if j.Status != "done" || len(j.Summary.PasswordsUnreadable) != 1 {
		t.Fatalf("%+v", j)
	}
	var docs int64
	b.st.Pool.QueryRow(context.Background(), `SELECT id FROM shares WHERE name = 'docs'`).Scan(&docs)
	var sealed []byte
	b.st.Pool.QueryRow(context.Background(), `SELECT secret_enc FROM shares WHERE id = $1`, docs).Scan(&sealed)
	if len(sealed) != 0 {
		t.Error("something unreadable was stored as if it were a password")
	}
	if full, err := b.sh.Load(context.Background(), docs); err != nil || full.Password != "" {
		t.Errorf("load: %q %v", full.Password, err)
	}
}

// ---- rule 2: all or nothing, and the file is not trusted --------------------------------------------------

func TestARefusedFileChangesNothingAndIsNotKept(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	good := string(e.export())
	before := good

	smb := func(extra string) string {
		return `{"name":"s","kind":"smb","server":"nas","share":"d","version":"3.0"` + extra + `,"excludes":[],"workers":4,"day_start_hour":8,"day_end_hour":22,"last_used_basis":"atime","full_scan_every":7,"retention_scans":10,"snapshot_depth":4}`
	}
	hdr := `"format":"nas-storage-analyzer-backup","format_version":1`
	pad := strings.Repeat(" ", backup.MaxFileBytes)
	var many []string
	for i := 0; i <= backup.MaxShares; i++ {
		many = append(many, `{"name":"n`+fmt.Sprint(i)+`"}`)
	}
	cases := []struct{ name, file, key, field string }{
		{"not JSON", `{"format": `, "not_backup", ""},
		{"another kind of file", `{"format":"something-else","format_version":1}`, "not_backup", ""},
		{"no version", `{"format":"nas-storage-analyzer-backup"}`, "not_backup", "format_version"},
		{"a newer version", `{"format":"nas-storage-analyzer-backup","format_version":2,"app_version":"7.0.0","brand_new":true}`, "newer", "format_version"},
		{"an unknown field", `{` + hdr + `,"surprise":1}`, "invalid", ""},
		{"an unknown field in a share", `{` + hdr + `,"shares":[` + smb(`,"role":"admin"`) + `]}`, "invalid", ""},
		{"files claimed", `{` + hdr + `,"includes_files":true}`, "invalid", "includes_files"},
		{"more after the end", `{` + hdr + `} {}`, "not_backup", ""},
		{"a share with a bad name", `{` + hdr + `,"shares":[` + strings.Replace(smb(""), `"name":"s"`, `"name":"/etc"`, 1) + `]}`, "invalid", "shares[0].name"},
		{"a share with a bad schedule", `{` + hdr + `,"shares":[` + smb(`,"cron":"every day"`) + `]}`, "invalid", "shares[0].cron"},
		{"a bind share outside the shares folder", `{` + hdr + `,"shares":[{"name":"b","kind":"bind","path":"/etc","excludes":[],"workers":4,"day_start_hour":8,"day_end_hour":22,"last_used_basis":"atime","full_scan_every":7,"retention_scans":10,"snapshot_depth":4}]}`, "invalid", "shares[0].path"},
		{"a share name twice", `{` + hdr + `,"shares":[` + smb("") + `,` + smb("") + `]}`, "invalid", "shares[1].name"},
		{"a secret on a share that has none", `{` + hdr + `,"shares":[{"name":"b","kind":"bind","path":"/mnt/shares/b","secret":"AAAA","excludes":[],"workers":4,"day_start_hour":8,"day_end_hour":22,"last_used_basis":"atime","full_scan_every":7,"retention_scans":10,"snapshot_depth":4}]}`, "invalid", "shares[0].secret"},
		{"a secret that is not base64", `{` + hdr + `,"shares":[` + smb(`,"secret":"not base64!"`) + `]}`, "invalid", ""},
		{"a file type with a dot", `{` + hdr + `,"file_types":[{"ext":".mkv","category":"video"}]}`, "invalid", "file_types[0].ext"},
		{"a file type in a category that does not exist", `{` + hdr + `,"file_types":[{"ext":"mkv","category":"fruit"}]}`, "invalid", "file_types[0].category"},
		{"a file type twice", `{` + hdr + `,"file_types":[{"ext":"mkv","category":"video"},{"ext":"mkv","category":"audio"}]}`, "invalid", "file_types[1].ext"},
		{"a preference that does not exist", `{` + hdr + `,"preferences":[{"username":"helmer","prefs":{"favourite":"blue"}}]}`, "invalid", "preferences[0].prefs"},
		{"a preference value that does not exist", `{` + hdr + `,"preferences":[{"username":"helmer","prefs":{"theme":"neon"}}]}`, "invalid", "preferences[0].prefs"},
		{"an accent that is not a colour", `{` + hdr + `,"preferences":[{"username":"helmer","prefs":{"accent":"red; background:url(x)"}}]}`, "invalid", "preferences[0].prefs"},
		{"a person twice", `{` + hdr + `,"preferences":[{"username":"helmer","prefs":{}},{"username":"HELMER","prefs":{}}]}`, "invalid", "preferences[1].username"},
		{"a role in the preferences", `{` + hdr + `,"preferences":[{"username":"helmer","prefs":{},"role":"admin"}]}`, "invalid", ""},
		{"too many shares", `{` + hdr + `,"shares":[` + strings.Join(many, ",") + `]}`, "too_many", "shares"},
		{"too big", `{` + hdr + `}` + pad, "too_big", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.bk.Stage(context.Background(), "helmer", strings.NewReader(c.file))
			var inv *backup.InvalidError
			if !errors.As(err, &inv) {
				t.Fatalf("want a refused file, got %v", err)
			}
			if inv.Key != c.key || (c.field != "" && inv.Field != c.field) {
				t.Errorf("refused as %q at %q (%s), want %q at %q", inv.Key, inv.Field, inv.Detail, c.key, c.field)
			}
			if inv.Detail == "" {
				t.Error("a refusal with no reason")
			}
			if n := e.count(`SELECT count(*) FROM backup_stages`); n != 0 {
				t.Errorf("a refused file was kept (%d staged)", n)
			}
			if after := string(e.export()); after != before {
				t.Error("a refused file changed something")
			}
		})
	}
	// The newer version is reported as that, with the version that wrote it, and not as a file with fields this version does not know.
	_, err := e.bk.Stage(context.Background(), "helmer", strings.NewReader(cases[3].file))
	var inv *backup.InvalidError
	if !errors.As(err, &inv) || inv.Params["app_version"] != "7.0.0" {
		t.Errorf("a newer file must name the version that wrote it: %v", err)
	}
	if string(e.export()) != good {
		t.Error("the installation changed")
	}
}

func TestAFailureHalfwayLeavesEverythingAsItWas(t *testing.T) {
	for _, after := range []string{"shares", "file_types", "preferences"} {
		t.Run("after "+after, func(t *testing.T) {
			e := newEnv(t, 1)
			e.seed()
			file := e.export()
			// Something to undo in every section the swap writes.
			ctx := context.Background()
			e.addShare(bind("extra", "/mnt/shares/extra"))
			e.sh.SetExtension(ctx, "mp3", 3)
			e.exec(`UPDATE users SET prefs = '{"language":"en"}' WHERE username = 'helmer'`)
			before := e.export()
			e.bk.SetHookForTest(func(step string) error {
				if step == after {
					return errors.New("the database went away")
				}
				return nil
			})
			j := e.restore(file, "helmer")
			if j.Status != "failed" || j.ErrorKey != "failed" {
				t.Fatalf("%+v", j)
			}
			if string(e.export()) != string(before) {
				t.Error("a failed restore changed something")
			}
			if strings.Contains(fmt.Sprint(j.ErrorParams), "database went away") {
				t.Error("the text of an unexpected error is shown to the person")
			}
			if n := e.count(`SELECT count(*) FROM backup_stages`); n != 0 {
				t.Error("the staged file was not removed")
			}
		})
	}
}

func TestTheJobChecksTheFileAgain(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	st, err := e.bk.Stage(context.Background(), "helmer", bytes.NewReader(e.export()))
	if err != nil {
		t.Fatal(err)
	}
	before := e.export()
	// Whatever is on the server is not trusted to be what was checked.
	e.exec(`UPDATE backup_stages SET data = convert_to('{"format":"nas-storage-analyzer-backup","format_version":1,"shares":[{"name":"/etc"}]}', 'UTF8') WHERE token = $1`, st.Token)
	j := e.start("helmer", st.Token)
	if j.Status != "failed" || j.ErrorKey != "invalid" || j.ErrorParams["field"] != "shares[0].name" {
		t.Fatalf("%+v", j)
	}
	if string(e.export()) != string(before) {
		t.Error("a file that fails the second check changed something")
	}
}

func TestARestoreWaitsForScans(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	ctx := context.Background()
	file := e.export()
	var id int64
	e.st.Pool.QueryRow(ctx, `SELECT id FROM shares WHERE name = 'media'`).Scan(&id)
	if _, err := e.q.Enqueue(ctx, jobs.KindScan, id, "auto", "manual", "helmer"); err != nil {
		t.Fatal(err)
	}
	e.addShare(bind("extra", "/mnt/shares/extra"))
	before := e.export()
	st, err := e.bk.Stage(ctx, "helmer", bytes.NewReader(file))
	if err != nil || !st.Summary.ScansActive {
		t.Fatalf("the confirmation must say that scans are active: %+v %v", st, err)
	}

	// Refused before anything is taken: the staged file is still there, so the person presses the button again when the scan is done.
	if _, err := e.bk.Start(ctx, auth.Actor{Username: "helmer"}, st.Token); !errors.Is(err, backup.ErrBusy) {
		t.Fatalf("a restore started while a scan was queued: %v", err)
	}
	if e.count(`SELECT count(*) FROM backup_stages WHERE token = $1`, st.Token) != 1 || e.count(`SELECT count(*) FROM backup_jobs`) != 0 {
		t.Error("a refused start used up the staged file or left a job")
	}
	if string(e.export()) != string(before) {
		t.Error("a restore that waited for scans changed something")
	}
	e.exec(`UPDATE jobs SET state = 'done', finished_at = now()`)
	if j := e.start("helmer", st.Token); j.Status != "done" {
		t.Fatalf("the same file, once the scan was done: %+v", j)
	}
}

// A scan queued in the instant between the early answer and the swap is caught by the check inside the transaction, which nothing can race:
// the job fails as "busy" and changes nothing.
func TestAScanQueuedAtTheLastMomentStopsTheSwap(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	ctx := context.Background()
	file := e.export()
	var id int64
	e.st.Pool.QueryRow(ctx, `SELECT id FROM shares WHERE name = 'media'`).Scan(&id)
	e.addShare(bind("extra", "/mnt/shares/extra"))
	before := e.export()
	e.bk.Go = func(f func()) {
		if _, err := e.q.Enqueue(ctx, jobs.KindScan, id, "auto", "manual", "helmer"); err != nil {
			t.Error(err)
		}
		f()
	}
	j := e.restore(file, "helmer")
	if j.Status != "failed" || j.ErrorKey != "busy" {
		t.Fatalf("%+v", j)
	}
	if string(e.export()) != string(before) {
		t.Error("a swap that found a scan changed something")
	}
}

// ---- what the confirmation says ------------------------------------------------------------------------------

func TestTheConfirmationSaysWhatWillHappen(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	file := e.export()
	e.addShare(bind("extra", "/mnt/shares/extra"))
	e.exec(`DELETE FROM users WHERE username = 'Anna'`) // the file has preferences for someone who is no longer here
	st, err := e.bk.Stage(context.Background(), "helmer", bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	s := st.Summary
	if s.Counts.Shares != 3 || s.Counts.FileTypes == 0 || s.Counts.Preferences != 2 || s.AppVersion != "9.9.9" || s.FormatVersion != 1 || !s.CreatedAt.Equal(fixed) {
		t.Errorf("counts and header: %+v", s)
	}
	if strings.Join(s.Changes.SharesRemoved, ",") != "extra" || len(s.Changes.SharesAdded) != 0 || len(s.Changes.SharesUpdated) != 0 {
		t.Errorf("shares: %+v", s.Changes)
	}
	if got := s.Changes.PreferencesSkipped; len(got) != 1 || got[0] != "Anna" || s.Changes.PreferencesApplied != 0 { // helmer's are what the file says
		t.Errorf("preferences: %+v", s.Changes)
	}
	if len(s.Missing) != 0 {
		t.Errorf("missing sections: %v", s.Missing)
	}
	// Choosing the file is not restoring it: nothing changed.
	if n := e.count(`SELECT count(*) FROM shares`); n != 4 {
		t.Errorf("staging changed the shares (%d)", n)
	}
}

func TestASectionThatIsNotInTheFileIsLeftAsItIsAndAnEmptyOneIsNot(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	prefs := e.count(`SELECT count(*) FROM users WHERE prefs <> '{}'::jsonb`)
	types := e.count(`SELECT count(*) FROM extension_map`)

	only := `{"format":"nas-storage-analyzer-backup","format_version":1,"shares":[]}`
	st, err := e.bk.Stage(context.Background(), "helmer", strings.NewReader(only))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(st.Summary.Missing, ",") != "file_types,preferences" {
		t.Errorf("missing: %v", st.Summary.Missing)
	}
	if len(st.Summary.Changes.SharesRemoved) != 3 {
		t.Errorf("an empty list of shares has to say that every share goes: %+v", st.Summary.Changes)
	}
	if j := e.start("helmer", st.Token); j.Status != "done" {
		t.Fatalf("%+v", j)
	}
	if n := e.count(`SELECT count(*) FROM shares`); n != 0 {
		t.Errorf("an explicit empty list left %d shares", n)
	}
	if e.count(`SELECT count(*) FROM users WHERE prefs <> '{}'::jsonb`) != prefs || e.count(`SELECT count(*) FROM extension_map`) != types {
		t.Error("a section that is not in the file was changed")
	}
}

func TestChangedFileTypesAreAppliedToTheScansInTheBackground(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	ctx := context.Background()
	file := e.export()
	var withData, without int64
	e.st.Pool.QueryRow(ctx, `SELECT id FROM shares WHERE name = 'media'`).Scan(&withData)
	e.st.Pool.QueryRow(ctx, `SELECT id FROM shares WHERE name = 'nas'`).Scan(&without)
	e.exec(`INSERT INTO scans (id, share_id, status, mode) VALUES (500, $1, 'succeeded', 'full')`, withData)
	e.exec(`UPDATE shares SET last_scan_id = 500 WHERE id = $1`, withData)

	// Nothing to apply when the map is the same.
	j := e.restore(file, "helmer")
	if j.Status != "done" || j.Summary.FileTypesChanged != 0 || e.count(`SELECT count(*) FROM jobs WHERE kind = 'recategorize'`) != 0 {
		t.Fatalf("an unchanged map must queue nothing: %+v", j.Summary)
	}
	e.sh.SetExtension(ctx, "mp3", 3)
	j = e.restore(file, "helmer")
	if j.Status != "done" || j.Summary.FileTypesChanged != 1 || j.Summary.RecategorizeShares != 1 {
		t.Fatalf("%+v", j.Summary)
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE kind = 'recategorize' AND state = 'queued' AND share_id = $1`, withData); n != 1 {
		t.Errorf("the share with data was not queued for re-categorisation (%d)", n)
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE share_id = $1`, without); n != 0 {
		t.Error("a share without data was queued")
	}
}

func TestRestoringWhatIsAlreadyThereChangesNothingAndLeavesTheSchedulesAlone(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	ctx := context.Background()
	file := e.export()
	var next1 *time.Time
	e.st.Pool.QueryRow(ctx, `SELECT next_run_at FROM shares WHERE name = 'media'`).Scan(&next1)
	if next1 == nil {
		t.Fatal("the seed has no schedule")
	}
	// Time passes before the restore: working out the schedule again would give another moment, so it must not be worked out again.
	e.exec(`UPDATE shares SET next_run_at = next_run_at + interval '1 hour'`)
	var moved *time.Time
	e.st.Pool.QueryRow(ctx, `SELECT next_run_at FROM shares WHERE name = 'media'`).Scan(&moved)

	st, err := e.bk.Stage(ctx, "helmer", bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	c := st.Summary.Changes
	if len(c.SharesAdded)+len(c.SharesUpdated)+len(c.SharesRemoved) != 0 || c.FileTypesChanged != 0 || c.PreferencesApplied != 0 || len(c.PreferencesSkipped) != 0 {
		t.Fatalf("the file is what is here, so nothing changes: %+v", c)
	}
	j := e.start("helmer", st.Token)
	// What the job reports it did is what the confirmation said it would do: nothing.
	if jc := j.Summary; j.Status != "done" || jc.RecategorizeShares != 0 || jc.PreferencesApplied != 0 || len(jc.SharesAdded)+len(jc.SharesUpdated)+len(jc.SharesRemoved) != 0 || jc.FileTypesChanged != 0 {
		t.Fatalf("%+v", j)
	}
	var after *time.Time
	e.st.Pool.QueryRow(ctx, `SELECT next_run_at FROM shares WHERE name = 'media'`).Scan(&after)
	if after == nil || !after.Equal(*moved) {
		t.Errorf("a restore that changed nothing moved the schedule: %v -> %v", moved, after)
	}
}

func TestPreferencesOnlyReachPeopleWhoExistHere(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	file := `{"format":"nas-storage-analyzer-backup","format_version":1,"preferences":[
		{"username":"HELMER","prefs":{"language":"en","accent":"#AABBCC"}},{"username":"ghost","prefs":{"theme":"dark"}}]}`
	j := e.restore([]byte(file), "helmer")
	if j.Status != "done" || j.Summary.PreferencesApplied != 1 || strings.Join(j.Summary.PreferencesSkipped, ",") != "ghost" {
		t.Fatalf("%+v", j)
	}
	var prefs map[string]string
	e.st.Pool.QueryRow(context.Background(), `SELECT prefs FROM users WHERE username = 'helmer'`).Scan(&prefs)
	if len(prefs) != 2 || prefs["language"] != "en" || prefs["accent"] != "#aabbcc" {
		t.Errorf("the preferences replace what was there and are stored as a person's own would be: %v", prefs)
	}
	if n := e.count(`SELECT count(*) FROM users`); n != 3 {
		t.Errorf("a restore made or removed accounts (%d)", n)
	}
	// Nothing about who may do what, or how to get in, is touched.
	if n := e.count(`SELECT count(*) FROM users WHERE username = 'helmer' AND role = 'admin' AND password_hash LIKE '$argon2id%'`); n != 1 {
		t.Error("a restore changed an account")
	}
}

// ---- staging and tokens -------------------------------------------------------------------------------------

func TestAStagedFileBelongsToOnePersonAndWorksOnce(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	ctx := context.Background()
	file := e.export()
	stage := func(by string) string {
		st, err := e.bk.Stage(ctx, by, bytes.NewReader(file))
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Token) != 32 {
			t.Fatalf("a token is 32 characters, got %q", st.Token)
		}
		return st.Token
	}
	start := func(by, token string) error {
		_, err := e.bk.Start(ctx, auth.Actor{Username: by}, token)
		return err
	}

	tok := stage("helmer")
	for _, bad := range []string{"", "x", strings.Repeat("g", 32), "../../etc/passwd", strings.Repeat("A", 32), tok + "0"} {
		if err := start("helmer", bad); !errors.Is(err, backup.ErrNoStage) {
			t.Errorf("token %q: %v", bad, err)
		}
	}
	if err := start("mallory", tok); !errors.Is(err, backup.ErrNoStage) {
		t.Errorf("another person started somebody else's file: %v", err)
	}
	if err := start("helmer", tok); err != nil {
		t.Fatal(err)
	}
	if err := start("helmer", tok); !errors.Is(err, backup.ErrNoStage) {
		t.Errorf("a token worked twice: %v", err)
	}

	// Choosing another file replaces the first; changing one's mind removes it; somebody else cannot remove it.
	first := stage("helmer")
	second := stage("helmer")
	if err := start("helmer", first); !errors.Is(err, backup.ErrNoStage) {
		t.Errorf("an older file still works after another was chosen: %v", err)
	}
	if err := e.bk.Unstage(ctx, "mallory", second); err != nil || e.count(`SELECT count(*) FROM backup_stages`) != 1 {
		t.Error("somebody else removed a staged file")
	}
	if err := e.bk.Unstage(ctx, "helmer", second); err != nil || e.count(`SELECT count(*) FROM backup_stages`) != 0 {
		t.Error("the file was not removed")
	}
	if err := e.bk.Unstage(ctx, "helmer", "not a token"); err != nil {
		t.Error("an unknown token is not an error")
	}

	// A file that waited more than an hour is gone, and is cleared the next time one is staged.
	old := stage("helmer")
	e.exec(`UPDATE backup_stages SET created_at = now() - interval '61 minutes' WHERE token = $1`, old)
	if err := start("helmer", old); !errors.Is(err, backup.ErrNoStage) {
		t.Errorf("an expired file was restored: %v", err)
	}
	e.exec(`UPDATE backup_stages SET created_by = 'someone-else'`)
	e.exec(`UPDATE backup_stages SET created_at = now() - interval '2 hours'`)
	stage("helmer")
	if n := e.count(`SELECT count(*) FROM backup_stages`); n != 1 {
		t.Errorf("an old staged file was not cleared (%d)", n)
	}
}

// ---- jobs -----------------------------------------------------------------------------------------------------

func TestAFinishedJobReportsStepsTimingsAndWhatWasRestored(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	j := e.restore(e.export(), "helmer")
	if j.Status != "done" || j.Phase != "replacing" || j.Steps == 0 || j.Step != j.Steps || j.StartedBy != "helmer" || j.FinishedAt == nil {
		t.Errorf("%+v", j)
	}
	if j.CheckingMS == nil || j.ReplacingMS == nil || j.Summary == nil || j.ErrorKey != "" {
		t.Errorf("timings and summary: %+v", j)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'backup.restore' AND username = 'helmer'`); n != 1 {
		t.Errorf("the restore is not in the audit log (%d)", n)
	}
	var detail string
	e.st.Pool.QueryRow(context.Background(), `SELECT detail::text FROM audit_log WHERE action = 'backup.restore'`).Scan(&detail)
	for _, leak := range []string{"media", "docs", "nas", "helmer", smbPassword} {
		if strings.Contains(detail, leak) {
			t.Errorf("the audit entry names %q: counts only", leak)
		}
	}
}

func TestAFailedJobSaysWhyAndIsAudited(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	st, _ := e.bk.Stage(context.Background(), "helmer", bytes.NewReader(e.export()))
	e.exec(`UPDATE backup_stages SET data = convert_to('{"format":"nas-storage-analyzer-backup","format_version":1,"file_types":[{"ext":"x","category":"fruit"}]}', 'UTF8')`)
	j := e.start("helmer", st.Token)
	if j.Status != "failed" || j.ErrorKey != "invalid" || j.FinishedAt == nil || j.Summary != nil {
		t.Fatalf("%+v", j)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'backup.restore.failed'`); n != 1 {
		t.Errorf("the failure is not in the audit log (%d)", n)
	}
}

func TestOnlyOneRestoreRunsAtATime(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	ctx := context.Background()
	var pending func()
	e.bk.Go = func(f func()) { pending = f } // the first restore has started and has not finished
	file := e.export()
	first, _ := e.bk.Stage(ctx, "helmer", bytes.NewReader(file))
	j, err := e.bk.Start(ctx, auth.Actor{Username: "helmer"}, first.Token)
	if err != nil || j.Status != "running" {
		t.Fatalf("%+v %v", j, err)
	}
	second, _ := e.bk.Stage(ctx, "helmer", bytes.NewReader(file))
	if _, err := e.bk.Start(ctx, auth.Actor{Username: "helmer"}, second.Token); !errors.Is(err, backup.ErrRunning) {
		t.Fatalf("a second restore started while one was running: %v", err)
	}
	if e.count(`SELECT count(*) FROM backup_stages WHERE token = $1`, second.Token) != 1 {
		t.Error("the refused start used up the staged file")
	}
	if err := e.bk.Dismiss(ctx, j.ID); !errors.Is(err, backup.ErrRunning) {
		t.Errorf("a running job was dismissed: %v", err)
	}
	pending()
	if got := e.job(j.ID); got.Status != "done" {
		t.Fatalf("%+v", got)
	}
	if _, err := e.bk.Start(ctx, auth.Actor{Username: "helmer"}, second.Token); err != nil {
		t.Errorf("a restore after the first finished: %v", err)
	}
}

func TestAJobThatStoppedMovingIsReportedInterrupted(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	ctx := context.Background()
	e.exec(`INSERT INTO backup_jobs (status, started_by, started_at, updated_at) VALUES ('running', 'helmer', now() - interval '1 hour', now() - interval '16 minutes')`)
	j, err := e.bk.Latest(ctx)
	if err != nil || j == nil || j.Status != "failed" || j.ErrorKey != "interrupted" {
		t.Fatalf("%+v %v", j, err)
	}
	// A job that moved a minute ago is still running.
	e.exec(`UPDATE backup_jobs SET status = 'running', error_key = '', updated_at = now() - interval '1 minute'`)
	if j, _ := e.bk.Latest(ctx); j.Status != "running" {
		t.Errorf("%+v", j)
	}
	// And a restore can start after the interrupted one is given up.
	e.exec(`UPDATE backup_jobs SET updated_at = now() - interval '16 minutes'`)
	if j := e.restore(e.export(), "helmer"); j.Status != "done" {
		t.Errorf("%+v", j)
	}
}

func TestAFinishedJobIsShownThenDismissed(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	ctx := context.Background()
	if j, err := e.bk.Latest(ctx); j != nil || err != nil {
		t.Fatalf("no restore yet: %+v %v", j, err)
	}
	j := e.restore(e.export(), "helmer")
	if got, _ := e.bk.Latest(ctx); got == nil || got.ID != j.ID {
		t.Fatal("a finished job that has not been dismissed must still be shown")
	}
	if err := e.bk.Dismiss(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.bk.Latest(ctx); got != nil {
		t.Errorf("a dismissed job is shown again: %+v", got)
	}
	if err := e.bk.Dismiss(ctx, 9999); !errors.Is(err, backup.ErrNoJob) {
		t.Errorf("%v", err)
	}
}

// One test uses the real thread, as the server does.
func TestARestoreRunsOnTheServerOnItsOwnThread(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	e.bk.Go = nil
	ctx := context.Background()
	st, err := e.bk.Stage(ctx, "helmer", bytes.NewReader(e.export()))
	if err != nil {
		t.Fatal(err)
	}
	j, err := e.bk.Start(ctx, auth.Actor{Username: "helmer"}, st.Token)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		got, err := e.bk.Latest(ctx)
		if err != nil || got == nil || got.ID != j.ID {
			t.Fatalf("%+v %v", got, err)
		}
		if got.Status == "done" {
			return
		}
		if got.Status == "failed" || time.Now().After(deadline) {
			t.Fatalf("%+v", got)
		}
	}
}

// ---- the guards that keep the file complete --------------------------------------------------------------------------

func TestEveryTableIsClassifiedAsInTheFileOrNot(t *testing.T) {
	e := newEnv(t, 1)
	rows, err := e.st.Pool.Query(context.Background(), `
		SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		seen[n] = true
		if c, ok := backup.Tables[n]; !ok {
			t.Errorf("table %q is not classified in backup.Tables: does it hold something the owner entered? Then it belongs in the backup and in its round-trip test, in the same change", n)
		} else if c.Why == "" {
			t.Errorf("table %q is classified without a reason", n)
		}
	}
	for n := range backup.Tables {
		if !seen[n] {
			t.Errorf("backup.Tables lists %q, which is not a table of the schema", n)
		}
	}
}

func TestEveryColumnOfSharesIsClassified(t *testing.T) {
	e := newEnv(t, 1)
	rows, err := e.st.Pool.Query(context.Background(), `SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'shares'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		seen[n] = true
		if _, ok := backup.ShareColumns[n]; !ok {
			t.Errorf("column shares.%s is not classified in backup.ShareColumns: a setting the owner can change belongs in the backup, in the same change", n)
		}
	}
	for n := range backup.ShareColumns {
		if !seen[n] {
			t.Errorf("backup.ShareColumns lists %q, which is not a column of shares", n)
		}
	}
}

// The round trip proves a field comes back only if the seeded shares use it. This fails when a field is added to a share (Input) and the
// seed does not set it: then the round trip would pass while the field is lost, which is the failure this whole guard exists to prevent.
func TestTheSeedSetsEveryFieldOfAShare(t *testing.T) {
	e := newEnv(t, 1)
	e.seed()
	list, err := e.sh.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	typ := reflect.TypeOf(shares.Input{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Name == "Password" { // never in the file: the SMB password travels only as the sealed blob
			continue
		}
		used := false
		for _, s := range list {
			if !reflect.ValueOf(s.Input).Field(i).IsZero() {
				used = true
			}
		}
		if !used {
			t.Errorf("no seeded share sets %s: the round trip would not notice if a restore lost it", f.Name)
		}
	}
}
