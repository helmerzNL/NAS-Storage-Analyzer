package secrets

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	key, _ := base64.StdEncoding.DecodeString(GenerateKey())
	b, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"", "hunter2", "ünïcødé ☃", string(make([]byte, 10000))} {
		sealed := b.Seal([]byte(msg))
		if bytes.Contains(sealed, []byte(msg)) && msg != "" {
			t.Errorf("plaintext visible in sealed value")
		}
		got, err := b.Open(sealed)
		if err != nil || string(got) != msg {
			t.Errorf("round trip of %q failed: %q %v", msg, got, err)
		}
	}
	if bytes.Equal(b.Seal([]byte("x")), b.Seal([]byte("x"))) {
		t.Error("two seals of the same value are identical: nonce reused")
	}
}

func TestOpenRejectsTamperingAndWrongKey(t *testing.T) {
	k1, k2 := bytes.Repeat([]byte{1}, KeySize), bytes.Repeat([]byte{2}, KeySize)
	b1, _ := New(k1)
	b2, _ := New(k2)
	sealed := b1.Seal([]byte("secret"))
	if _, err := b2.Open(sealed); err == nil {
		t.Error("opened with the wrong key")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := b1.Open(sealed); err == nil {
		t.Error("opened a tampered value")
	}
	if _, err := b1.Open([]byte{9, 1, 2}); err == nil {
		t.Error("opened garbage")
	}
	if _, err := New([]byte("short")); err == nil {
		t.Error("accepted a short key")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("NASSA_ENCRYPTION_KEY", "")
	t.Setenv("NASSA_ENCRYPTION_KEY_FILE", "")
	if _, err := FromEnv(); err == nil {
		t.Error("missing key accepted")
	}
	key := GenerateKey()
	t.Setenv("NASSA_ENCRYPTION_KEY", key)
	if _, err := FromEnv(); err != nil {
		t.Errorf("env key: %v", err)
	}
	file := filepath.Join(t.TempDir(), "key")
	os.WriteFile(file, []byte(key+"\n"), 0o600)
	t.Setenv("NASSA_ENCRYPTION_KEY", "")
	t.Setenv("NASSA_ENCRYPTION_KEY_FILE", file)
	if _, err := FromEnv(); err != nil {
		t.Errorf("key file: %v", err)
	}
	t.Setenv("NASSA_ENCRYPTION_KEY_FILE", "")
	t.Setenv("NASSA_ENCRYPTION_KEY", "not base64!!")
	if _, err := FromEnv(); err == nil {
		t.Error("invalid base64 accepted")
	}
}

func TestNewKeyFromEnvAndSameKey(t *testing.T) {
	a, b := GenerateKey(), GenerateKey()
	t.Setenv("NASSA_ENCRYPTION_KEY", a)
	t.Setenv("NASSA_ENCRYPTION_KEY_FILE", "")
	t.Setenv("NASSA_NEW_ENCRYPTION_KEY_FILE", "")
	t.Setenv("NASSA_NEW_ENCRYPTION_KEY", "")
	if _, err := NewKeyFromEnv(); err == nil || !strings.Contains(err.Error(), "NASSA_NEW_ENCRYPTION_KEY") {
		t.Fatalf("no new key set: %v", err)
	}
	t.Setenv("NASSA_NEW_ENCRYPTION_KEY", b)
	cur, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	next, err := NewKeyFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cur.SameKey(next) || next.SameKey(cur) || !cur.SameKey(cur) {
		t.Error("SameKey must tell two keys apart and know a key from itself")
	}
	// The new key is its own variable: the current one is not read in its place.
	if _, err := next.Open(cur.Seal([]byte("x"))); err == nil {
		t.Error("the new key opened what the current key sealed")
	}
}
