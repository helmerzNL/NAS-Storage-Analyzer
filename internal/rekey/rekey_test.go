package rekey_test

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
	"github.com/helmerznl/nas-storage-analyzer/internal/rekey"
	"github.com/helmerznl/nas-storage-analyzer/internal/secrets"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

var ctx = context.Background()

const smbPassword = "p@ss word=1"

func box(t *testing.T, fill byte) *secrets.Box {
	t.Helper()
	b, err := secrets.New(bytes.Repeat([]byte{fill}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// seed stores one SMB password and one authenticator secret under the key, next to a share and a user that have none.
func seed(t *testing.T, key *secrets.Box) (st *store.Store, shareID int64, totpSecret string) {
	t.Helper()
	auth.SetFastHashingForTests()
	st = ts.NewStore(t)
	svc := shares.NewService(st.Pool, key, shares.Options{}, time.UTC)
	in := shares.Defaults()
	pw := smbPassword
	in.Name, in.Kind, in.Path, in.Server, in.Share, in.Username, in.Password = "media", shares.KindSMB, "", "nas.lan", "media", "me", &pw
	shareID, err := svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	plain := shares.Defaults()
	plain.Name, plain.Path = "local", t.TempDir()
	if _, err := svc.Create(ctx, plain); err != nil {
		t.Fatal(err)
	}
	a := auth.NewService(st.Pool, key, auth.DefaultConfig())
	alice, err := a.CreateFirstAdmin(ctx, "alice", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := a.CreateInvitation(ctx, alice, "bob", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.AcceptInvitationPassword(ctx, token, "bob", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if totpSecret, _, err = a.BeginTOTP(ctx, alice); err != nil {
		t.Fatal(err)
	}
	return st, shareID, totpSecret
}

func sealed(t *testing.T, st *store.Store) (share, user []byte) {
	t.Helper()
	if err := st.Pool.QueryRow(ctx, `SELECT secret_enc FROM shares WHERE name = 'media'`).Scan(&share); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool.QueryRow(ctx, `SELECT totp_secret_enc FROM users WHERE username = 'alice'`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	return share, user
}

func audits(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'encryption.rekey'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRekeyMovesEverySecretToTheNewKey(t *testing.T) {
	oldKey, newKey := box(t, 1), box(t, 2)
	st, shareID, totpSecret := seed(t, oldKey)

	res, already, err := rekey.Run(ctx, st.Pool, oldKey, newKey, "nasa rekey")
	if err != nil || already {
		t.Fatalf("rekey: %v (already %v)", err, already)
	}
	if len(res.Counts) != 2 || res.Counts[0] != 1 || res.Counts[1] != 1 || res.Total() != 2 {
		t.Fatalf("one SMB password and one authenticator secret: %+v", res)
	}

	// The application, started with the new key, reads what it stored; with the old key it reads nothing.
	full, err := shares.NewService(st.Pool, newKey, shares.Options{}, time.UTC).Load(ctx, shareID)
	if err != nil || full.Password != smbPassword {
		t.Fatalf("the SMB password under the new key: %q, %v", full.Password, err)
	}
	if _, err := shares.NewService(st.Pool, oldKey, shares.Options{}, time.UTC).Load(ctx, shareID); err == nil {
		t.Error("the old key still opens the SMB password")
	}
	_, userEnc := sealed(t, st)
	if got, err := newKey.Open(userEnc); err != nil || string(got) != totpSecret {
		t.Fatalf("the authenticator secret under the new key: %q, %v", got, err)
	}
	if _, err := oldKey.Open(userEnc); err == nil {
		t.Error("the old key still opens the authenticator secret")
	}
	// The share and the user without a secret are left alone.
	var empty int
	st.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM shares WHERE secret_enc IS NULL) + (SELECT count(*) FROM users WHERE totp_secret_enc IS NULL)`).Scan(&empty)
	if empty != 2 {
		t.Errorf("rows without a secret got one: %d left empty", empty)
	}
	// It is in the audit log, with how many and without any value.
	var who, detail string
	if err := st.Pool.QueryRow(ctx, `SELECT username, detail::text FROM audit_log WHERE action = 'encryption.rekey'`).Scan(&who, &detail); err != nil {
		t.Fatal(err)
	}
	if who != "nasa rekey" || !strings.Contains(detail, `"secrets": 2`) || strings.Contains(detail, smbPassword) || strings.Contains(detail, totpSecret) {
		t.Errorf("audit entry: by %q, %s", who, detail)
	}

	// A second run with the same keys finds everything under the new key and changes nothing.
	shareBefore, userBefore := sealed(t, st)
	if _, already, err = rekey.Run(ctx, st.Pool, oldKey, newKey, "nasa rekey"); err != nil || !already {
		t.Fatalf("a second run: %v (already %v)", err, already)
	}
	shareAfter, userAfter := sealed(t, st)
	if !bytes.Equal(shareBefore, shareAfter) || !bytes.Equal(userBefore, userAfter) || audits(t, st) != 1 {
		t.Error("a second run changed something")
	}
}

func TestRekeyChangesNothingWhenAValueOpensWithNeitherKey(t *testing.T) {
	oldKey, newKey, other := box(t, 1), box(t, 2), box(t, 3)
	st, _, _ := seed(t, oldKey)
	// The authenticator secret was sealed with a key nobody has any more.
	if _, err := st.Pool.Exec(ctx, `UPDATE users SET totp_secret_enc = $1 WHERE username = 'alice'`, other.Seal([]byte("LOSTSECRET"))); err != nil {
		t.Fatal(err)
	}
	shareBefore, userBefore := sealed(t, st)

	_, _, err := rekey.Run(ctx, st.Pool, oldKey, newKey, "nasa rekey")
	if !errors.Is(err, rekey.ErrUnreadable) {
		t.Fatalf("a value that opens with neither key must stop the rekey: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, `"alice"`) || !strings.Contains(msg, "Nothing was changed") || strings.Contains(msg, "LOSTSECRET") || strings.Contains(msg, "media") {
		t.Errorf("the message names the user, not the share that is fine, and no value: %s", msg)
	}
	// Nothing changed: the SMB password, which was fine, is still under the old key.
	shareAfter, userAfter := sealed(t, st)
	if !bytes.Equal(shareBefore, shareAfter) || !bytes.Equal(userBefore, userAfter) {
		t.Error("a refused rekey changed a value")
	}
	if _, err := oldKey.Open(shareAfter); err != nil {
		t.Errorf("the SMB password no longer opens with the old key: %v", err)
	}
	if audits(t, st) != 0 {
		t.Error("a refused rekey is in the audit log as done")
	}
}

func TestCheckNamesWhatDoesNotOpen(t *testing.T) {
	key := box(t, 1)
	st, _, _ := seed(t, key)
	shareBefore, userBefore := sealed(t, st)

	res, problems, err := rekey.Check(ctx, st.Pool, key)
	if err != nil || len(problems) != 0 || res.Total() != 2 {
		t.Fatalf("the right key: %+v %v %v", res, problems, err)
	}
	res, problems, err = rekey.Check(ctx, st.Pool, box(t, 9))
	if err != nil || res.Total() != 2 || len(problems) != 2 {
		t.Fatalf("a wrong key: %+v %v %v", res, problems, err)
	}
	all := strings.Join(problems, " | ")
	if !strings.Contains(all, `share "media"`) || !strings.Contains(all, `user "alice"`) || strings.Contains(all, smbPassword) {
		t.Errorf("the problems name the share and the user, never a value: %s", all)
	}
	shareAfter, userAfter := sealed(t, st)
	if !bytes.Equal(shareBefore, shareAfter) || !bytes.Equal(userBefore, userAfter) {
		t.Error("checking changed a value")
	}
}

// A column that holds a sealed value and is not in rekey.Columns would be left behind under the old key, and lost with it. Sealed
// columns are bytea and end in _enc; this compares the schema with the list.
func TestEverySealedColumnIsKnown(t *testing.T) {
	st := ts.NewStore(t)
	rows, err := st.Pool.Query(ctx, `SELECT table_name || '.' || column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND data_type = 'bytea' AND column_name LIKE '%\_enc' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var inSchema []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		inSchema = append(inSchema, c)
	}
	var known []string
	for _, c := range rekey.Columns {
		known = append(known, c.Table+"."+c.Column)
	}
	sort.Strings(known)
	if strings.Join(inSchema, ",") != strings.Join(known, ",") {
		t.Fatalf("sealed columns in the schema: %v; known to rekey: %v", inSchema, known)
	}
}
