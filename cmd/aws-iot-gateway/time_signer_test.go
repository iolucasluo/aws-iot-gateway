package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
)

func TestNewTimeBootstrapSignerUsesLocalPrivateKeyForDevelopment(t *testing.T) {
	t.Parallel()

	path := writeDevelopmentTimePrivateKey(t)
	signer, err := newTimeBootstrapSigner(context.Background(), runtime.Config{
		Environment:                      "development",
		TimeBootstrapSigner:              "local",
		TimeBootstrapLocalPrivateKeyPath: path,
		TimeBootstrapKeyID:               "time-local-test-g1",
	}, nil)
	if err != nil {
		t.Fatalf("newTimeBootstrapSigner() error = %v", err)
	}
	publicKey, ok := signer.Public().(*ecdsa.PublicKey)
	if !ok || publicKey.Curve != elliptic.P256() {
		t.Fatal("newTimeBootstrapSigner() did not load the local P-256 signer")
	}
}

func writeDevelopmentTimePrivateKey(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "time-bootstrap-private.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
