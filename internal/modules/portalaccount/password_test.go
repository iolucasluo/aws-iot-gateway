package portalaccount

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestHashPasswordRoundTripAndRejectsWrongPassword(t *testing.T) {
	t.Parallel()
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("VerifyPassword() = false for the correct password")
	}
	if VerifyPassword(hash, "correct horse battery stapl") {
		t.Fatal("VerifyPassword() = true for a wrong password")
	}
	if VerifyPassword(hash, "") {
		t.Fatal("VerifyPassword() = true for an empty password")
	}
}

func TestHashPasswordUsesFreshSaltAndDeclaredParameters(t *testing.T) {
	t.Parallel()
	first, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	second, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if first == second {
		t.Fatal("HashPassword() reused a salt: identical inputs produced identical hashes")
	}
	const prefix = "$argon2id$v=19$m=65536,t=3,p=2$"
	for _, hash := range []string{first, second} {
		if !strings.HasPrefix(hash, prefix) {
			t.Fatalf("HashPassword() = %q, want prefix %q", hash, prefix)
		}
		if strings.Contains(hash, "=") && strings.Count(hash, "$") != 5 {
			t.Fatalf("HashPassword() = %q, want 5 field separators", hash)
		}
	}
}

func TestHashPasswordRejectsBlankPassword(t *testing.T) {
	t.Parallel()
	for _, password := range []string{"", "   ", "\t\n"} {
		if _, err := HashPassword(password); err != ErrInvalidInput {
			t.Fatalf("HashPassword(%q) error = %v, want ErrInvalidInput", password, err)
		}
	}
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	t.Parallel()
	salt := base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef"))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	for name, encoded := range map[string]string{
		"empty":            "",
		"plaintext":        "not-a-hash",
		"missing-fields":   "$argon2id$v=19$m=65536,t=3,p=2$" + salt,
		"wrong-function":   "$argon2i$v=19$m=65536,t=3,p=2$" + salt + "$" + key,
		"wrong-version":    "$argon2id$v=13$m=65536,t=3,p=2$" + salt + "$" + key,
		"zero-memory":      "$argon2id$v=19$m=0,t=3,p=2$" + salt + "$" + key,
		"missing-param":    "$argon2id$v=19$m=65536,t=3$" + salt + "$" + key,
		"unknown-param":    "$argon2id$v=19$m=65536,t=3,p=2,x=1$" + salt + "$" + key,
		"short-salt":       "$argon2id$v=19$m=65536,t=3,p=2$" + base64.RawStdEncoding.EncodeToString([]byte("short")) + "$" + key,
		"short-key":        "$argon2id$v=19$m=65536,t=3,p=2$" + salt + "$" + base64.RawStdEncoding.EncodeToString([]byte("short")),
		"not-base64-salt":  "$argon2id$v=19$m=65536,t=3,p=2$!!!!$" + key,
		"not-base64-key":   "$argon2id$v=19$m=65536,t=3,p=2$" + salt + "$!!!!",
		"zero-parallelism": "$argon2id$v=19$m=65536,t=3,p=0$" + salt + "$" + key,
	} {
		if VerifyPassword(encoded, "any-password") {
			t.Errorf("VerifyPassword(%s) = true, want false", name)
		}
	}
}
