// Package auth holds the pieces of passkey sign-in that don't need the HTTP
// layer: random tokens, the in-memory WebAuthn challenge store, the sign-in
// attempt limiter, and the adapters between Findo's records and go-webauthn.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// NewToken returns a random 256-bit token (base64url, for a cookie or URL
// fragment) and its SHA-256. Only the hash is ever stored: the token is a
// bearer secret and already high-entropy, so a plain hash is enough.
func NewToken() (token string, hash []byte) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("auth: no randomness available: " + err.Error()) // can't continue safely
	}
	token = base64.RawURLEncoding.EncodeToString(b[:])
	return token, HashToken(token)
}

// HashToken is the stored form of a token.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
