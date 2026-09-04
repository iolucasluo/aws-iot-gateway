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
	"strings"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/devicebootstrap"
)

func TestRunLocalWritesDevelopmentContractFromOwnerOnlyPrivateKey(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	privateKeyPath := filepath.Join(directory, "time-bootstrap-private.pem")
	writePrivateKey(t, privateKeyPath)
	output := filepath.Join(directory, "contract")
	err := run(context.Background(), []string{
		"local",
		"--out", output,
		"--service-url", "http://192.168.1.20:14080",
		"--time-bootstrap-url", "http://192.168.1.20:14080/api/v1/device-time/bootstrap",
		"--key-id", "time-local-test-g1",
		"--private-key", privateKeyPath,
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	contract, err := devicebootstrap.Load(output)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if contract.Environment != "development" ||
		contract.TimeBootstrap.Keys[0].Kid != "time-local-test-g1" {
		t.Fatalf("contract = %#v", contract)
	}
}

func TestRunLocalCanPublishCurrentAndNextTimeBootstrapKeys(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	privateKeyPath := filepath.Join(directory, "time-bootstrap-private.pem")
	writePrivateKey(t, privateKeyPath)
	nextPublicKeyPath := filepath.Join(directory, "time-bootstrap-next.der")
	writeP256PublicKey(t, nextPublicKeyPath)
	output := filepath.Join(directory, "contract")
	err := run(context.Background(), []string{
		"local",
		"--out", output,
		"--service-url", "http://192.168.1.20:14080",
		"--time-bootstrap-url", "http://192.168.1.20:14080/api/v1/device-time/bootstrap",
		"--key-id", "time-local-test-g1",
		"--private-key", privateKeyPath,
		"--next-key-id", "time-local-test-g2",
		"--next-public-key", nextPublicKeyPath,
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	contract, err := devicebootstrap.Load(output)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(contract.TimeBootstrap.Keys) != 2 || contract.TimeBootstrap.Keys[1].Kid != "time-local-test-g2" {
		t.Fatalf("contract keys = %#v", contract.TimeBootstrap.Keys)
	}
}

func TestRunLocalWritesDevelopmentContractFromPublicKey(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	publicKeyPath := filepath.Join(directory, "time-bootstrap-current.der")
	writeP256PublicKey(t, publicKeyPath)
	output := filepath.Join(directory, "contract")
	err := run(context.Background(), []string{
		"local",
		"--out", output,
		"--service-url", "http://api-staging.example.test",
		"--time-bootstrap-url", "http://api-staging.example.test/api/v1/device-time/bootstrap",
		"--key-id", "time-cn-cloud-staging-g1",
		"--public-key", publicKeyPath,
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	contract, err := devicebootstrap.Load(output)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if contract.Environment != "development" ||
		contract.TimeBootstrap.Keys[0].Kid != "time-cn-cloud-staging-g1" {
		t.Fatalf("contract = %#v", contract)
	}
}

func TestRunLocalRejectsAmbiguousCurrentKeySource(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	privateKeyPath := filepath.Join(directory, "time-bootstrap-private.pem")
	publicKeyPath := filepath.Join(directory, "time-bootstrap-current.der")
	writePrivateKey(t, privateKeyPath)
	writeP256PublicKey(t, publicKeyPath)
	err := run(context.Background(), []string{
		"local",
		"--out", filepath.Join(directory, "contract"),
		"--service-url", "http://api-staging.example.test",
		"--time-bootstrap-url", "http://api-staging.example.test/api/v1/device-time/bootstrap",
		"--key-id", "time-cn-cloud-staging-g1",
		"--private-key", privateKeyPath,
		"--public-key", publicKeyPath,
	})
	if err == nil || !strings.Contains(err.Error(), "exactly one of --private-key or --public-key") {
		t.Fatalf("run() error = %v, want ambiguous key source rejection", err)
	}
}

func TestRunAWSReleaseRequiresACompleteRotationKeyPairBeforeAWSAccess(t *testing.T) {
	t.Parallel()

	err := run(context.Background(), []string{
		"aws-release",
		"--out", filepath.Join(t.TempDir(), "contract"),
		"--environment", "staging",
		"--stack", "aws-iot-gateway-staging",
		"--region", "ap-east-1",
		"--service-ca", filepath.Join(t.TempDir(), "service-ca.pem"),
		"--next-key-id", "time-staging-g2",
	})
	if err == nil || !strings.Contains(err.Error(), "both --next-key-id and --next-kms-key-id") {
		t.Fatalf("run() error = %v, want incomplete rotation rejection", err)
	}
}

func writePrivateKey(t *testing.T, path string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func writeP256PublicKey(t *testing.T, path string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey() error = %v", err)
	}
	if err := os.WriteFile(path, der, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
