// Package crypto encrypts secrets Findo persists to SQLite (SMB volume
// passwords) at rest, using AES-256-GCM under a single server-wide master
// key supplied via FINDO_MASTER_KEY. The key never touches the database;
// only ciphertext does, so a copy of the DB file alone doesn't expose
// credentials.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

// KeySize is the required master key length: 32 bytes for AES-256.
const KeySize = 32

// ParseKey decodes a base64-encoded master key and validates its length.
func ParseKey(b64 string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("must decode to %d bytes, got %d", KeySize, len(key))
	}
	return key, nil
}

// Encrypt seals plaintext under key, returning nonce||ciphertext. The nonce
// travels alongside the ciphertext since GCM nonces aren't secret, just
// required to be unique per encryption under the same key.
func Encrypt(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt reverses Encrypt, authenticating the ciphertext before returning
// the plaintext.
func Decrypt(key, data []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, fmt.Errorf("ciphertext shorter than nonce")
	}
	nonce, ciphertext := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
