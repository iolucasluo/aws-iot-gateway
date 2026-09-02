// Package domesticca provides the development-only local CA adapter used by
// the DELL domestic staging composition. It is deliberately file-backed and
// must be replaced by an HSM/KMS-backed implementation before production.
package domesticca

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
)

type Config struct {
	Endpoint   string
	CACertPath string
	CAKeyPath  string
	Now        func() time.Time
}

type Provisioner struct {
	endpoint string
	ca       *x509.Certificate
	key      crypto.Signer
	now      func() time.Time
}

var _ activation.Provisioner = (*Provisioner)(nil)

func New(config Config) (*Provisioner, error) {
	endpoint := strings.TrimSpace(config.Endpoint)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "mqtts" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		strings.ContainsAny(endpoint, "\r\n") || strings.Contains(strings.ToLower(parsed.Hostname()), "amazonaws.com") {
		return nil, errors.New("domestic CA endpoint must be a non-AWS mqtts URL")
	}
	ca, err := loadCertificate(config.CACertPath)
	if err != nil {
		return nil, fmt.Errorf("load domestic CA certificate: %w", err)
	}
	if !ca.IsCA || !ca.BasicConstraintsValid || ca.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("domestic CA certificate is not permitted to sign certificates")
	}
	key, err := loadPrivateKey(config.CAKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load domestic CA private key: %w", err)
	}
	if !publicKeysEqual(ca.PublicKey, key.Public()) {
		return nil, errors.New("domestic CA private key does not match certificate")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Provisioner{endpoint: endpoint, ca: ca, key: key, now: config.Now}, nil
}

func (p *Provisioner) Provision(_ context.Context, deviceSN, csrPEM string) (activation.PreparedIdentity, error) {
	deviceSN = strings.TrimSpace(deviceSN)
	if p == nil || p.ca == nil || p.key == nil || deviceSN == "" || len(deviceSN) > 128 {
		return activation.PreparedIdentity{}, errors.New("domestic CA provision request is invalid")
	}
	block, rest := pem.Decode([]byte(csrPEM))
	if block == nil || len(rest) != 0 || block.Type != "CERTIFICATE REQUEST" {
		return activation.PreparedIdentity{}, errors.New("domestic CA requires a single certificate request PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return activation.PreparedIdentity{}, fmt.Errorf("parse certificate request: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return activation.PreparedIdentity{}, fmt.Errorf("verify certificate request signature: %w", err)
	}
	if csr.PublicKey == nil {
		return activation.PreparedIdentity{}, errors.New("certificate request public key is missing")
	}
	now := p.now().UTC().Truncate(time.Second)
	serial, err := randomSerial()
	if err != nil {
		return activation.PreparedIdentity{}, fmt.Errorf("generate certificate serial: %w", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: deviceSN},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	der, err := x509.CreateCertificate(rand.Reader, leafTemplate, p.ca, csr.PublicKey, p.key)
	if err != nil {
		return activation.PreparedIdentity{}, fmt.Errorf("issue domestic device certificate: %w", err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	digest := sha256.Sum256(der)
	return activation.PreparedIdentity{
		DeviceID:       deviceSN,
		CertificateID:  "domestic-cert-" + hex.EncodeToString(digest[:]),
		CertificatePEM: string(certificatePEM),
		Endpoint:       p.endpoint,
	}, nil
}

func (p *Provisioner) Activate(_ context.Context, certificateID string) error {
	if strings.TrimSpace(certificateID) == "" {
		return errors.New("domestic certificate id is empty")
	}
	return nil
}

func loadCertificate(path string) (*x509.Certificate, error) {
	contents, err := readRegularFile(path, false)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(contents)
	if block == nil || len(rest) != 0 || block.Type != "CERTIFICATE" {
		return nil, errors.New("CA certificate must be a single CERTIFICATE PEM block")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	return certificate, nil
}

func loadPrivateKey(path string) (crypto.Signer, error) {
	contents, err := readRegularFile(path, true)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(contents)
	if block == nil || len(rest) != 0 || block.Type != "PRIVATE KEY" {
		return nil, errors.New("CA key must be a single PKCS#8 PRIVATE KEY PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA private key: %w", err)
	}
	key, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, errors.New("CA private key does not implement crypto.Signer")
	}
	return key, nil
}

func readRegularFile(path string, ownerOnly bool) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsAny(path, "\r\n") {
		return nil, errors.New("file path is required and invalid")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || (ownerOnly && info.Mode().Perm()&0o077 != 0) {
		return nil, errors.New("file must be a regular owner-only file")
	}
	return os.ReadFile(path)
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	if serial.Sign() == 0 {
		return big.NewInt(1), nil
	}
	return serial, nil
}

func publicKeysEqual(left, right crypto.PublicKey) bool {
	leftDER, leftErr := x509.MarshalPKIXPublicKey(left)
	rightDER, rightErr := x509.MarshalPKIXPublicKey(right)
	return leftErr == nil && rightErr == nil && string(leftDER) == string(rightDER)
}
