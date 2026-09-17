package credentialcipher

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func testKey() []byte {
	key := make([]byte, KeyBytes)
	for index := range key {
		key[index] = byte(index)
	}
	return key
}

func newTestCipher(t *testing.T) *Cipher {
	t.Helper()
	cipher, err := New(testKey(), "kek-v1")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return cipher
}

func TestSealOpenRoundTripUsesFreshNonce(t *testing.T) {
	t.Parallel()
	cipher := newTestCipher(t)
	first, err := cipher.Seal("super-secret-value")
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	second, err := cipher.Seal("super-secret-value")
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("Seal() reused a nonce: identical secrets produced identical ciphertext")
	}
	if bytes.Contains(first, []byte("super-secret-value")) {
		t.Fatal("Seal() output contains the plaintext secret")
	}
	for name, sealed := range map[string][]byte{"first": first, "second": second} {
		opened, err := cipher.Open(sealed)
		if err != nil {
			t.Fatalf("Open(%s) error = %v", name, err)
		}
		if opened != "super-secret-value" {
			t.Fatalf("Open(%s) = %q", name, opened)
		}
	}
	if cipher.KEKID() != "kek-v1" {
		t.Fatalf("KEKID() = %q", cipher.KEKID())
	}
}

func TestOpenFailsClosedForWrongKeyAndTamperedCiphertext(t *testing.T) {
	t.Parallel()
	cipher := newTestCipher(t)
	sealed, err := cipher.Seal("super-secret-value")
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}

	otherKey := make([]byte, KeyBytes)
	otherCipher, err := New(otherKey, "kek-v2")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := otherCipher.Open(sealed); !errors.Is(err, ErrCiphertextBroken) {
		t.Fatalf("Open(wrong key) error = %v, want ErrCiphertextBroken", err)
	}

	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := cipher.Open(tampered); !errors.Is(err, ErrCiphertextBroken) {
		t.Fatalf("Open(tampered) error = %v, want ErrCiphertextBroken", err)
	}
	flippedNonce := append([]byte(nil), sealed...)
	flippedNonce[0] ^= 0xff
	if _, err := cipher.Open(flippedNonce); !errors.Is(err, ErrCiphertextBroken) {
		t.Fatalf("Open(flipped nonce) error = %v, want ErrCiphertextBroken", err)
	}
	for name, broken := range map[string][]byte{
		"nil":        nil,
		"empty":      {},
		"nonce-only": make([]byte, nonceBytes),
		"truncated":  sealed[:nonceBytes+1],
	} {
		if _, err := cipher.Open(broken); !errors.Is(err, ErrCiphertextBroken) {
			t.Errorf("Open(%s) error = %v, want ErrCiphertextBroken", name, err)
		}
	}
}

func TestNewValidatesKeyAndKEKID(t *testing.T) {
	t.Parallel()
	for name, key := range map[string][]byte{
		"empty": nil,
		"short": make([]byte, KeyBytes-1),
		"long":  make([]byte, KeyBytes+1),
	} {
		if _, err := New(key, "kek-v1"); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("New(%s key) error = %v, want ErrInvalidKey", name, err)
		}
	}
	for name, kekID := range map[string]string{
		"empty":      "",
		"blank":      "   ",
		"separator":  "kek/v1",
		"whitespace": "kek v1",
		"too-long":   strings.Repeat("k", maxKEKIDLength+1),
	} {
		if _, err := New(testKey(), kekID); !errors.Is(err, ErrInvalidKEKID) {
			t.Errorf("New(kekID=%s) error = %v, want ErrInvalidKEKID", name, err)
		}
	}
	if _, err := newTestCipher(t).Seal(""); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("Seal(empty) error = %v, want ErrInvalidSecret", err)
	}
}

func TestNewFromBase64AcceptsStandardAndURLEncodings(t *testing.T) {
	t.Parallel()
	key := testKey()
	for name, encoded := range map[string]string{
		"standard": base64.StdEncoding.EncodeToString(key),
		"url":      base64.RawURLEncoding.EncodeToString(key),
	} {
		cipher, err := NewFromBase64(encoded, "kek-v1")
		if err != nil {
			t.Fatalf("NewFromBase64(%s) error = %v", name, err)
		}
		if cipher.KEKID() != "kek-v1" {
			t.Fatalf("NewFromBase64(%s) kekID = %q", name, cipher.KEKID())
		}
	}
	for _, encoded := range []string{"", "   ", "not base64!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := NewFromBase64(encoded, "kek-v1"); err == nil {
			t.Errorf("NewFromBase64(%q) error = nil, want a failure", encoded)
		}
	}
}
