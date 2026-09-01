// Package localsigner loads the development-only P-256 time bootstrap signer.
package localsigner

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

// Load returns a P-256 signer from an owner-only PEM private key.
func Load(path string) (crypto.Signer, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat local time signer: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("local time signer must be an owner-only regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read local time signer: %w", err)
	}
	block, remainder := pem.Decode(contents)
	if block == nil || len(remainder) != 0 || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("local time signer must be a single PKCS#8 PRIVATE KEY PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse local time signer: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("local time signer must be an ECDSA P-256 private key")
	}
	return key, nil
}
