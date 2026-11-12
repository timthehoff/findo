package crypto

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func testKey() []byte {
	return bytes.Repeat([]byte{0x42}, KeySize)
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := testKey()
	plaintext := []byte("hunter2")

	ciphertext, err := Encrypt(key, plaintext, nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Contains(ciphertext, plaintext) {
		t.Fatalf("ciphertext should not contain the plaintext in the clear")
	}

	got, err := Decrypt(key, ciphertext, nil)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round trip mismatch: got %q, want %q", got, plaintext)
	}
}

func TestEncryptProducesDistinctCiphertextsForSamePlaintext(t *testing.T) {
	key := testKey()
	a, err := Encrypt(key, []byte("same"), nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	b, err := Encrypt(key, []byte("same"), nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatalf("expected distinct ciphertexts (distinct nonces) for repeated Encrypt calls")
	}
}

func TestDecryptFailsWithWrongKey(t *testing.T) {
	ciphertext, err := Encrypt(testKey(), []byte("secret"), nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	wrongKey := bytes.Repeat([]byte{0x99}, KeySize)
	if _, err := Decrypt(wrongKey, ciphertext, nil); err == nil {
		t.Fatalf("expected Decrypt to fail with the wrong key")
	}
}

func TestDecryptFailsOnTamperedCiphertext(t *testing.T) {
	key := testKey()
	ciphertext, err := Encrypt(key, []byte("secret"), nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	tampered := append([]byte(nil), ciphertext...)
	tampered[len(tampered)-1] ^= 0xFF

	if _, err := Decrypt(key, tampered, nil); err == nil {
		t.Fatalf("expected Decrypt to reject a tampered ciphertext")
	}
}

func TestParseKeyValidatesLength(t *testing.T) {
	if _, err := ParseKey(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16))); err == nil {
		t.Fatalf("expected ParseKey to reject a 16-byte key")
	}
	good := base64.StdEncoding.EncodeToString(testKey())
	key, err := ParseKey(good)
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	if len(key) != KeySize {
		t.Fatalf("expected %d-byte key, got %d", KeySize, len(key))
	}
}

func TestParseKeyRejectsInvalidBase64(t *testing.T) {
	if _, err := ParseKey("not-valid-base64!!!"); err == nil {
		t.Fatalf("expected ParseKey to reject invalid base64")
	}
}

func TestAdditionalDataBindsTheCiphertextToItsContext(t *testing.T) {
	key := testKey()
	aad := []byte("volume=1;owner=2")
	ciphertext, err := Encrypt(key, []byte("secret"), aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if got, err := Decrypt(key, ciphertext, aad); err != nil || string(got) != "secret" {
		t.Fatalf("Decrypt with the same data = %q, %v", got, err)
	}
	for name, other := range map[string][]byte{
		"different data": []byte("volume=1;owner=3"),
		"no data":        nil,
	} {
		if _, err := Decrypt(key, ciphertext, other); err == nil {
			t.Errorf("Decrypt with %s must fail: a ciphertext moved to another row must not open", name)
		}
	}
}
