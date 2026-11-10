// Package secrets encrypts small secrets (SMB passwords, TOTP seeds) at rest
// with AES-256-GCM. The key comes from the environment or a Docker secret file.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

// formatV1 prefixes every ciphertext so the format can change later (key
// rotation, another cipher) without guessing.
const formatV1 byte = 1

// Box seals and opens secrets with one key.
type Box struct {
	aead cipher.AEAD
}

// New builds a Box from a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("encryption key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// GenerateKey returns a new random key encoded for NASSA_ENCRYPTION_KEY.
func GenerateKey() string {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

// FromEnv loads the key from NASSA_ENCRYPTION_KEY (base64) or, as a Docker
// secret, from the file named by NASSA_ENCRYPTION_KEY_FILE.
func FromEnv() (*Box, error) {
	return fromEnv("NASSA_ENCRYPTION_KEY", "no encryption key: set NASSA_ENCRYPTION_KEY or NASSA_ENCRYPTION_KEY_FILE (generate one with: nasa genkey)")
}

// NewKeyFromEnv loads the key that `nasa rekey` changes to, from NASSA_NEW_ENCRYPTION_KEY or the file named by
// NASSA_NEW_ENCRYPTION_KEY_FILE. A key is never taken from the command line: arguments are visible to every process on the machine.
func NewKeyFromEnv() (*Box, error) {
	return fromEnv("NASSA_NEW_ENCRYPTION_KEY", "no new key: set NASSA_NEW_ENCRYPTION_KEY or NASSA_NEW_ENCRYPTION_KEY_FILE (generate one with: nasa genkey)")
}

func fromEnv(name, missing string) (*Box, error) {
	v := os.Getenv(name)
	if f := os.Getenv(name + "_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read %s_FILE: %w", name, err)
		}
		v = string(b)
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, errors.New(missing)
	}
	key, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid base64", name)
	}
	return New(key)
}

// SameKey reports whether the other box has the same key: what one seals, the other opens.
func (b *Box) SameKey(o *Box) bool {
	_, err := o.Open(b.Seal([]byte("same key?")))
	return err == nil
}

// Seal encrypts plaintext. The output is format byte + nonce + ciphertext.
func (b *Box) Seal(plaintext []byte) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	out := append([]byte{formatV1}, nonce...)
	return b.aead.Seal(out, nonce, plaintext, nil)
}

// ErrCannotOpen is what Open returns for a value that this key does not open: sealed with another key, or damaged.
var ErrCannotOpen = errors.New("cannot decrypt: wrong encryption key or corrupted value")

// Open decrypts a value produced by Seal.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(sealed) < 1+ns+b.aead.Overhead() || sealed[0] != formatV1 {
		return nil, errors.New("sealed value has an unknown format")
	}
	pt, err := b.aead.Open(nil, sealed[1:1+ns], sealed[1+ns:], nil)
	if err != nil {
		return nil, ErrCannotOpen
	}
	return pt, nil
}
