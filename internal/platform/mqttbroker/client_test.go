package mqttbroker

import (
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

func TestNewClientRequiresMQTTSAndMutualTLSMaterial(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	caPath := filepath.Join(directory, "ca.pem")
	certPath := filepath.Join(directory, "client.pem")
	keyPath := filepath.Join(directory, "client.key")
	for _, path := range []string{caPath, certPath, keyPath} {
		if err := os.WriteFile(path, []byte("not-a-certificate"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := Config{
		Endpoint:       "mqtts://127.0.0.1:8883",
		ClientID:       "gateway-cn-staging",
		TopicPrefix:    "cn-staging",
		CAFile:         caPath,
		ClientCertFile: certPath,
		ClientKeyFile:  keyPath,
	}
	if _, err := NewClient(base); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("NewClient() error = %v, want TLS material failure", err)
	}

	base.Endpoint = "mqtt://127.0.0.1:1883"
	if _, err := NewClient(base); err == nil || !strings.Contains(err.Error(), "mqtts") {
		t.Fatalf("NewClient() error = %v, want mqtts-only failure", err)
	}
}

func TestNewClientRejectsUnsafeNamespaceAndIdentity(t *testing.T) {
	t.Parallel()

	cases := map[string]Config{
		"empty endpoint":      {ClientID: "gateway-cn-staging", TopicPrefix: "cn-staging"},
		"unsafe client id":    {Endpoint: "mqtts://127.0.0.1:8883", ClientID: "gateway/../../aws", TopicPrefix: "cn-staging"},
		"unsafe topic prefix": {Endpoint: "mqtts://127.0.0.1:8883", ClientID: "gateway-cn-staging", TopicPrefix: "cn staging"},
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewClient(config); err == nil {
				t.Fatal("NewClient() error = nil, want validation failure")
			}
		})
	}
}

func TestTopicNamespaceIsFailClosed(t *testing.T) {
	t.Parallel()

	for _, topic := range []string{
		"cmd/cn-staging/device/req",
		"dt/cn-staging/device/status",
		"cmd/cn-staging/+/res",
		"dt/cn-staging/#",
	} {
		if !topicInNamespace(topic, "cn-staging") {
			t.Fatalf("topicInNamespace(%q) = false", topic)
		}
	}
	for _, topic := range []string{
		"cmd/cn-staging-prod/device/req",
		"cmd/cn-staging/device/../other",
		"cmd/aws-iot-gateway-staging/device/req",
	} {
		if topicInNamespace(topic, "cn-staging") {
			t.Fatalf("topicInNamespace(%q) = true", topic)
		}
	}
}

func TestNewClientEnablesSubscriptionResumeForBrokerReconnects(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	caPath, certPath, keyPath := writeTestTLSMaterial(t, directory)
	client, err := NewClient(Config{
		Endpoint:       "mqtts://127.0.0.1:8883",
		ClientID:       "gateway-cn-staging",
		TopicPrefix:    "cn-staging",
		CAFile:         caPath,
		ClientCertFile: certPath,
		ClientKeyFile:  keyPath,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	options := client.broker.OptionsReader()
	if !options.ResumeSubs() {
		t.Fatal("NewClient() disabled subscription resume; broker reconnect would drop device ingest subscriptions")
	}
}

func TestActiveSubscriptionFiltersAreRestoredAfterReconnect(t *testing.T) {
	t.Parallel()

	client := &Client{subscriptions: map[string]struct{}{
		"cmd/cn-staging/+/res":  {},
		"dt/cn-staging/+/event": {},
	}}
	var restored []string
	client.restoreSubscriptions(func(filter string) error {
		restored = append(restored, filter)
		return nil
	})
	if strings.Join(restored, ",") != "cmd/cn-staging/+/res,dt/cn-staging/+/event" {
		t.Fatalf("restored filters = %v", restored)
	}
}

func writeTestTLSMaterial(t *testing.T, directory string) (string, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mqtt-client-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateKeyDER})
	caPath := filepath.Join(directory, "ca.pem")
	certPath := filepath.Join(directory, "client.pem")
	keyPath := filepath.Join(directory, "client.key")
	for _, file := range []struct {
		path string
		data []byte
	}{
		{path: caPath, data: certificatePEM},
		{path: certPath, data: certificatePEM},
		{path: keyPath, data: privateKeyPEM},
	} {
		if err := os.WriteFile(file.path, file.data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return caPath, certPath, keyPath
}
