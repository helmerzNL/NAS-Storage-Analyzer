// Package auth implements local accounts: Argon2id passwords, TOTP, recovery
// codes, passkeys (WebAuthn), sessions, lockout and the audit log.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (RFC 9106 second recommended option: 64 MiB, t=3, p=2).
// Tests lower them through SetFastHashingForTests.
var argonParams = struct {
	memory  uint32
	time    uint32
	threads uint8
}{64 * 1024, 3, 2}

const (
	saltLen = 16
	keyLen  = 32

	// MinPasswordLen is the shortest accepted password. Length matters more
	// than composition rules.
	MinPasswordLen = 12
	maxPasswordLen = 256
)

// SetFastHashingForTests makes hashing cheap; never call it outside tests.
func SetFastHashingForTests() {
	argonParams.memory, argonParams.time, argonParams.threads = 8, 1, 1
}

// HashPassword returns a PHC-formatted Argon2id hash.
func HashPassword(password string) string {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	key := argon2.IDKey([]byte(password), salt, argonParams.time, argonParams.memory, argonParams.threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonParams.memory, argonParams.time,
		argonParams.threads, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// VerifyPassword checks password against a PHC hash in constant time.
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash lets a login attempt for an unknown user cost the same as one for
// a known user, so response time does not reveal which usernames exist.
var dummyHash = func() string { return HashPassword("not-a-real-password") }()

// ErrWeakPassword describes why a password is rejected.
var ErrWeakPassword = errors.New("password too weak")

// CheckPasswordPolicy enforces the minimum length and rejects passwords that
// are just the username.
func CheckPasswordPolicy(password, username string) error {
	switch {
	case len([]rune(password)) < MinPasswordLen:
		return fmt.Errorf("%w: use at least %d characters", ErrWeakPassword, MinPasswordLen)
	case len(password) > maxPasswordLen:
		return fmt.Errorf("%w: at most %d bytes", ErrWeakPassword, maxPasswordLen)
	case strings.EqualFold(password, username):
		return fmt.Errorf("%w: must differ from the username", ErrWeakPassword)
	}
	return nil
}
