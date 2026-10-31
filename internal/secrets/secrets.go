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

// GenerateKey returns a new random key encoded for NASA_ENCRYPTION_KEY.
func GenerateKey() string {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

// FromEnv loads the key from NASA_ENCRYPTION_KEY (base64) or, as a Docker
// secret, from the file named by NASA_ENCRYPTION_KEY_FILE.
func FromEnv() (*Box, error) {
	v := os.Getenv("NASA_ENCRYPTION_KEY")
	if f := os.Getenv("NASA_ENCRYPTION_KEY_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read NASA_ENCRYPTION_KEY_FILE: %w", err)
		}
		v = string(b)
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, errors.New("no encryption key: set NASA_ENCRYPTION_KEY or NASA_ENCRYPTION_KEY_FILE (generate one with: nasa genkey)")
	}
	key, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, errors.New("NASA_ENCRYPTION_KEY is not valid base64")
	}
	return New(key)
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

// Open decrypts a value produced by Seal.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(sealed) < 1+ns+b.aead.Overhead() || sealed[0] != formatV1 {
		return nil, errors.New("sealed value has an unknown format")
	}
	pt, err := b.aead.Open(nil, sealed[1:1+ns], sealed[1+ns:], nil)
	if err != nil {
		return nil, errors.New("cannot decrypt: wrong encryption key or corrupted value")
	}
	return pt, nil
}
