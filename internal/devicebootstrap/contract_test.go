package devicebootstrap

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWritePublishesProviderNeutralServiceContract(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "device-service-bootstrap-contract")
	if _, err := Write(directory, Input{
		Environment:      "development",
		ServiceBaseURL:   "http://192.168.1.20:14080",
		TimeBootstrapURL: "http://192.168.1.20:14080/api/v1/device-time/bootstrap",
		SignerBackend:    "local",
		Keys:             []KeyInput{{Kid: "time-local-test-g1", SPKIDER: testP256SPKI(t)}},
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	var contract map[string]any
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(directory, "contract.json")), &contract); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if contract["kind"] != "device-service-bootstrap" {
		t.Fatalf("kind = %v, want device-service-bootstrap", contract["kind"])
	}
	if _, ok := contract["service"]; !ok {
		t.Fatal("provider-neutral service field is missing")
	}
	if _, ok := contract["gateway"]; ok {
		t.Fatal("provider-specific gateway field must not be published")
	}
}

func TestWriteAndLoadDevelopmentContractWithLocalP256Key(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "device-bootstrap-contract")
	spki := testP256SPKI(t)
	written, err := Write(directory, Input{
		Environment:      "development",
		ServiceBaseURL:   "http://192.168.1.20:14080",
		TimeBootstrapURL: "http://192.168.1.20:14080/api/v1/device-time/bootstrap",
		SignerBackend:    "local",
		Keys:             []KeyInput{{Kid: "time-local-test-g1", SPKIDER: spki}},
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if written.Service.CAPEMFile != "" || len(written.TimeBootstrap.Keys) != 1 {
		t.Fatalf("Write() contract = %#v", written)
	}
	if _, err := os.Stat(filepath.Join(directory, "contract.json")); err != nil {
		t.Fatalf("contract.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "time-bootstrap", "time-local-test-g1.der")); err != nil {
		t.Fatalf("SPKI file missing: %v", err)
	}
	loaded, err := Load(directory)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Environment != "development" || loaded.TimeBootstrap.Keys[0].SHA256 != written.TimeBootstrap.Keys[0].SHA256 {
		t.Fatalf("Load() contract = %#v", loaded)
	}
}

func TestWriteRejectsReleaseHTTPAndMismatchedP256Input(t *testing.T) {
	t.Parallel()

	spki := testP256SPKI(t)
	for name, input := range map[string]Input{
		"release HTTP": {
			Environment:      "staging",
			ServiceBaseURL:   "http://gateway.staging.example.test",
			TimeBootstrapURL: "http://time.staging.example.test/api/v1/device-time/bootstrap",
			SignerBackend:    "aws-kms",
			ServiceCAPEM:     []byte("-----BEGIN CERTIFICATE-----\nZmFrZQ==\n-----END CERTIFICATE-----\n"),
			Keys:             []KeyInput{{Kid: "time-staging-g1", SPKIDER: spki}},
		},
		"bad SPKI": {
			Environment:      "development",
			ServiceBaseURL:   "http://192.168.1.20:14080",
			TimeBootstrapURL: "http://192.168.1.20:14080/api/v1/device-time/bootstrap",
			SignerBackend:    "local",
			Keys:             []KeyInput{{Kid: "time-local-test-g1", SPKIDER: []byte("not a public key")}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Write(filepath.Join(t.TempDir(), "contract"), input); err == nil {
				t.Fatal("Write() error = nil, want invalid contract rejection")
			}
		})
	}
}

func TestLoadRejectsTrailingContractJSON(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "device-bootstrap-contract")
	if _, err := Write(directory, Input{
		Environment:      "development",
		ServiceBaseURL:   "http://192.168.1.20:14080",
		TimeBootstrapURL: "http://192.168.1.20:14080/api/v1/device-time/bootstrap",
		SignerBackend:    "local",
		Keys:             []KeyInput{{Kid: "time-local-test-g1", SPKIDER: testP256SPKI(t)}},
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	path := filepath.Join(directory, "contract.json")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if err := os.WriteFile(path, append(mustReadFile(t, path), []byte("\ntrailing-junk\n")...), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := Load(directory); err == nil {
		t.Fatal("Load() error = nil, want trailing JSON rejection")
	}
}

func TestLoadRejectsSymlinkedContractDirectoryComponent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated Windows privileges")
	}

	directory := filepath.Join(t.TempDir(), "device-bootstrap-contract")
	spki := testP256SPKI(t)
	if _, err := Write(directory, Input{
		Environment:      "development",
		ServiceBaseURL:   "http://192.168.1.20:14080",
		TimeBootstrapURL: "http://192.168.1.20:14080/api/v1/device-time/bootstrap",
		SignerBackend:    "local",
		Keys:             []KeyInput{{Kid: "time-local-test-g1", SPKIDER: spki}},
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	external := filepath.Join(t.TempDir(), "external-time-bootstrap")
	if err := os.Mkdir(external, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(external, "time-local-test-g1.der"), spki, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	keyDirectory := filepath.Join(directory, "time-bootstrap")
	if err := os.RemoveAll(keyDirectory); err != nil {
		t.Fatalf("RemoveAll() error = %v", err)
	}
	if err := os.Symlink(external, keyDirectory); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := Load(directory); err == nil {
		t.Fatal("Load() error = nil, want symlinked directory rejection")
	}
}

func TestInputFromAWSReleaseBindsStackOutputsToTheKMSPublicKey(t *testing.T) {
	t.Parallel()

	input, err := InputFromAWSRelease(ReleaseSource{
		Environment:  "staging",
		ServiceCAPEM: testCertificatePEM(t),
		Outputs: map[string]string{
			"APIBaseURL":          "https://gateway.staging.example.test",
			"TimeBootstrapURL":    "http://time.staging.example.test/api/v1/device-time/bootstrap",
			"TimeBootstrapKeyID":  "time-staging-g1",
			"TimeBootstrapKeyArn": "arn:aws:kms:ap-east-1:000000000000:key/00000000-0000-0000-0000-000000000000",
		},
		ActiveSPKIDER: testP256SPKI(t),
	})
	if err != nil {
		t.Fatalf("InputFromAWSRelease() error = %v", err)
	}
	if input.SignerBackend != "aws-kms" || input.Keys[0].Kid != "time-staging-g1" {
		t.Fatalf("InputFromAWSRelease() = %#v", input)
	}
}

func TestInputFromAWSReleaseRejectsMissingKeyProvenance(t *testing.T) {
	t.Parallel()

	_, err := InputFromAWSRelease(ReleaseSource{
		Environment:  "production",
		ServiceCAPEM: testCertificatePEM(t),
		Outputs: map[string]string{
			"APIBaseURL":         "https://gateway.production.example.test",
			"TimeBootstrapURL":   "http://time.production.example.test/api/v1/device-time/bootstrap",
			"TimeBootstrapKeyID": "time-production-g1",
		},
		ActiveSPKIDER: testP256SPKI(t),
	})
	if err == nil {
		t.Fatal("InputFromAWSRelease() error = nil, want missing KMS key provenance")
	}
}

func testP256SPKI(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey() error = %v", err)
	}
	return spki
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return contents
}

func testCertificatePEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate() error = %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
