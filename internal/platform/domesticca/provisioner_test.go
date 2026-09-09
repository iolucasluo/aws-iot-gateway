package domesticca

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProvisionerIssuesClientCertificateFromCSR(t *testing.T) {
	caCertPath, caKeyPath := writeTestCA(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:   pkix.Name{CommonName: "device-csr"},
		PublicKey: &key.PublicKey,
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	csr := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	p, err := New(Config{
		Endpoint:   "mqtts://192.0.2.10:8883",
		CACertPath: caCertPath,
		CAKeyPath:  caKeyPath,
		Now:        func() time.Time { return time.Date(2026, 9, 2, 1, 2, 3, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := p.Provision(context.Background(), "fdr-test-device", string(csr))
	if err != nil {
		t.Fatal(err)
	}
	if identity.DeviceID != "fdr-test-device" || identity.Endpoint != "mqtts://192.0.2.10:8883" || identity.CertificateID == "" {
		t.Fatalf("identity = %#v", identity)
	}
	block, _ := pem.Decode([]byte(identity.CertificatePEM))
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Subject.CommonName != "fdr-test-device" || !containsExtKeyUsage(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		t.Fatalf("leaf subject/usages = %q/%v", leaf.Subject.CommonName, leaf.ExtKeyUsage)
	}
	if err := leaf.CheckSignatureFrom(loadTestCACert(t, caCertPath)); err != nil {
		t.Fatalf("leaf is not signed by configured CA: %v", err)
	}
}

func containsExtKeyUsage(usages []x509.ExtKeyUsage, want x509.ExtKeyUsage) bool {
	for _, usage := range usages {
		if usage == want {
			return true
		}
	}
	return false
}

func TestNewRejectsInsecureCAKeyPathAndAWSEndpoint(t *testing.T) {
	caCertPath, caKeyPath := writeTestCA(t)
	if _, err := New(Config{Endpoint: "mqtts://x:8883", CACertPath: caCertPath, CAKeyPath: caKeyPath}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if _, err := New(Config{Endpoint: "mqtts://x.amazonaws.com:8883", CACertPath: caCertPath, CAKeyPath: caKeyPath}); err == nil {
		t.Fatal("AWS endpoint accepted by domestic CA provisioner")
	}
	if err := os.Chmod(caKeyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Endpoint: "mqtts://x:8883", CACertPath: caCertPath, CAKeyPath: caKeyPath}); err == nil {
		t.Fatal("group-readable CA key accepted")
	}
}

func TestProvisionRejectsInvalidCSR(t *testing.T) {
	caCertPath, caKeyPath := writeTestCA(t)
	p, err := New(Config{Endpoint: "mqtts://x:8883", CACertPath: caCertPath, CAKeyPath: caKeyPath})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Provision(context.Background(), "fdr-test-device", "not a csr"); err == nil {
		t.Fatal("invalid CSR accepted")
	}
}

func writeTestCA(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "dell-dev-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "ca-key.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func loadTestCACert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(contents)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(cert.Subject.CommonName) == "" {
		t.Fatal("test CA has no subject")
	}
	return cert
}
