package localsigner

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReturnsP256SignerForOwnerOnlyPEM(t *testing.T) {
	t.Parallel()

	path := writePrivateKey(t, elliptic.P256(), 0o600)
	signer, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	digest := sha256.Sum256([]byte("local signed time bootstrap"))
	signature, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	publicKey, ok := signer.Public().(*ecdsa.PublicKey)
	if !ok || publicKey.Curve != elliptic.P256() || !ecdsa.VerifyASN1(publicKey, digest[:], signature) {
		t.Fatal("Load() did not return a usable P-256 signer")
	}
}

func TestLoadRejectsUnsafeOrNonP256PrivateKey(t *testing.T) {
	t.Parallel()

	for name, path := range map[string]string{
		"group readable": writePrivateKey(t, elliptic.P256(), 0o640),
		"P-384":          writePrivateKey(t, elliptic.P384(), 0o600),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(path); err == nil {
				t.Fatal("Load() error = nil, want rejected local signer")
			}
		})
	}
}

func writePrivateKey(t *testing.T, curve elliptic.Curve, mode os.FileMode) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "time-bootstrap-private.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), mode); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
