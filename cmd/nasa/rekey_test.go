package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/secrets"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

func keyOf(fill byte) (string, *secrets.Box) {
	raw := bytes.Repeat([]byte{fill}, secrets.KeySize)
	box, _ := secrets.New(raw)
	return base64.StdEncoding.EncodeToString(raw), box
}

// The command, from the environment to the database and back: the current key and the new key by their variables, a share whose SMB
// password moves from the one to the other, and what the command says.
func TestRekeyCommand(t *testing.T) {
	ctx := context.Background()
	st := ts.NewStore(t)
	dsn := st.Pool.Config().ConnString()
	oldKey, oldBox := keyOf(1)
	newKey, newBox := keyOf(2)
	pw := "smb-password-of-the-test"
	in := shares.Defaults()
	in.Name, in.Kind, in.Path, in.Server, in.Share, in.Username, in.Password = "media", shares.KindSMB, "", "nas.lan", "media", "me", &pw
	id, err := shares.NewService(st.Pool, oldBox, shares.Options{}, time.UTC).Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runRekey(append([]string{"-db", dsn}, args...), &out)
		return out.String(), err
	}
	for _, v := range []string{"NASSA_ENCRYPTION_KEY", "NASSA_ENCRYPTION_KEY_FILE", "NASSA_NEW_ENCRYPTION_KEY", "NASSA_NEW_ENCRYPTION_KEY_FILE"} {
		t.Setenv(v, "")
	}

	// Without the current key, without a new key, and with the new key equal to the current one: refused, each with its reason.
	if _, err := run(); err == nil || !strings.Contains(err.Error(), "NASSA_ENCRYPTION_KEY") {
		t.Fatalf("no current key: %v", err)
	}
	t.Setenv("NASSA_ENCRYPTION_KEY", oldKey)
	if _, err := run(); err == nil || !strings.Contains(err.Error(), "NASSA_NEW_ENCRYPTION_KEY") || !strings.Contains(err.Error(), "genkey") {
		t.Fatalf("no new key: %v", err)
	}
	t.Setenv("NASSA_NEW_ENCRYPTION_KEY", oldKey)
	if _, err := run(); err == nil || !strings.Contains(err.Error(), "the new key is the current key") {
		t.Fatalf("the same key: %v", err)
	}
	t.Setenv("NASSA_NEW_ENCRYPTION_KEY", "not base64 !")
	if _, err := run(); err == nil || !strings.Contains(err.Error(), "NASSA_NEW_ENCRYPTION_KEY is not valid base64") {
		t.Fatalf("a new key that is no key: %v", err)
	}

	// -check needs no new key and changes nothing.
	t.Setenv("NASSA_NEW_ENCRYPTION_KEY", "")
	out, err := run("-check")
	if err != nil || !strings.Contains(out, "All stored secrets open with NASSA_ENCRYPTION_KEY (1 SMB password(s) of shares, 0 authenticator secret(s) of users)") {
		t.Fatalf("-check with the right key: %q, %v", out, err)
	}

	// The new key from a file (a Docker secret), with the newline an editor leaves.
	file := filepath.Join(t.TempDir(), "new_key")
	if err := os.WriteFile(file, []byte(newKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NASSA_NEW_ENCRYPTION_KEY_FILE", file)
	out, err = run()
	if err != nil || !strings.Contains(out, "Re-encrypted with the new key: 1 SMB password(s) of shares") || !strings.Contains(out, "set NASSA_ENCRYPTION_KEY to the new key") ||
		!strings.Contains(out, "backup file") {
		t.Fatalf("rekey: %q, %v", out, err)
	}
	if strings.Contains(out, oldKey) || strings.Contains(out, newKey) || strings.Contains(out, pw) {
		t.Fatalf("the command printed a key or a secret: %q", out)
	}
	if full, err := shares.NewService(st.Pool, newBox, shares.Options{}, time.UTC).Load(ctx, id); err != nil || full.Password != pw {
		t.Fatalf("the password under the new key: %q, %v", full.Password, err)
	}

	// Started with the old key by mistake: -check says which secret does not open, and running rekey again does no harm.
	out, err = run("-check")
	if err == nil || !strings.Contains(err.Error(), `1 of 1 stored secrets do not open with NASSA_ENCRYPTION_KEY`) || !strings.Contains(err.Error(), `share "media"`) {
		t.Fatalf("-check with the old key after the rekey: %q, %v", out, err)
	}
	if out, err = run(); err != nil || !strings.Contains(out, "already under the new key") {
		t.Fatalf("a second run: %q, %v", out, err)
	}
	// With the new key as the current one everything opens.
	t.Setenv("NASSA_ENCRYPTION_KEY", newKey)
	if out, err = run("-check"); err != nil || !strings.Contains(out, "All stored secrets open") {
		t.Fatalf("-check with the new key: %q, %v", out, err)
	}
}
