package ota

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

type downloadGrantStoreStub struct {
	token     string
	grant     DownloadGrant
	attempts  int
	ttl       time.Duration
	redeemErr error
}

func (store *downloadGrantStoreStub) Store(_ context.Context, token string, grant DownloadGrant, attempts int, ttl time.Duration) error {
	store.token, store.grant, store.attempts, store.ttl = token, grant, attempts, ttl
	return nil
}

func (store *downloadGrantStoreStub) Redeem(_ context.Context, token, deviceSN string) (DownloadGrant, error) {
	if store.redeemErr != nil || token != store.token || deviceSN != store.grant.DeviceSN {
		return DownloadGrant{}, ErrDownloadNotFound
	}
	return store.grant, nil
}

type artifactReaderStub struct {
	key  string
	body string
	size int64
}

func (reader *artifactReaderStub) Open(_ context.Context, key string) (io.ReadCloser, int64, error) {
	reader.key = key
	return io.NopCloser(strings.NewReader(reader.body)), reader.size, nil
}

func TestDownloadProxyIssuesShortDeviceBoundURLAndOpensPrivateArtifact(t *testing.T) {
	t.Parallel()
	store := &downloadGrantStoreStub{}
	reader := &artifactReaderStub{body: "signed-ota", size: 10}
	proxy, err := NewDownloadProxy(
		"https://gateway.example",
		store,
		reader,
		func() (string, error) { return "0123456789abcdef0123456789abcdef", nil },
	)
	if err != nil {
		t.Fatalf("NewDownloadProxy() error = %v", err)
	}
	grant := DownloadGrant{
		DeviceSN: "fdr-device-1", DeploymentID: testDeploymentID,
		ArtifactKey: "ota/feeder-x1/private.tar.gz", SHA256: validFirmwareInput().SHA256,
		FileSize: 10,
	}
	url, err := proxy.Issue(context.Background(), grant)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if len(url) >= 256 || !strings.HasPrefix(url, "https://gateway.example/api/v1/device-ota/download/fdr-device-1/") ||
		strings.Contains(url, grant.ArtifactKey) {
		t.Fatalf("issued URL is not a short opaque capability: %q", url)
	}
	if store.attempts != 4 || store.ttl != 15*time.Minute || store.grant != grant {
		t.Fatalf("stored grant = %+v attempts=%d ttl=%v", store.grant, store.attempts, store.ttl)
	}
	artifact, err := proxy.Open(context.Background(), grant.DeviceSN, store.token)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer artifact.Body.Close()
	payload, _ := io.ReadAll(artifact.Body)
	if string(payload) != reader.body || artifact.FileSize != grant.FileSize || artifact.SHA256 != grant.SHA256 || reader.key != grant.ArtifactKey {
		t.Fatalf("artifact = %+v payload=%q key=%q", artifact, payload, reader.key)
	}
	if _, err := proxy.Open(context.Background(), "fdr-wrong-device", store.token); err == nil {
		t.Fatal("Open() accepted a capability for the wrong device path")
	}
}

func TestDeploymentUsesShortProxyURLInsteadOfSessionCredentialPresign(t *testing.T) {
	t.Parallel()
	repository := &repositoryStub{createdVersion: FirmwareVersion{
		ID: testVersionID, PID: "feeder-x1", Version: "v1.2.3", FirmwareBuild: 42,
		SHA256: validFirmwareInput().SHA256, ManifestSHA256: validFirmwareInput().ManifestSHA256,
		FileSize: 1234, ReleaseStatement: validFirmwareInput().ReleaseStatement,
		Signature: validFirmwareInput().Signature, ArtifactKey: "ota/feeder-x1/v1.tar.gz",
	}}
	store := &downloadGrantStoreStub{}
	proxy, err := NewDownloadProxy("https://gateway.example", store, &artifactReaderStub{}, func() (string, error) {
		return "0123456789abcdef0123456789abcdef", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sender := &commandSenderStub{}
	service := NewService(repository, artifactStoreStub{url: strings.Repeat("x", 1199)}, sender, "staging", nil,
		func() (string, error) { return testDeploymentID, nil }, time.Now, WithDownloadProxy(proxy))
	if _, err := service.CreateDeployment(context.Background(), "demo-app", "device-id", "fdr-device-1",
		CreateDeploymentInput{FirmwareVersionID: testVersionID, IdempotencyKey: "short-proxy"}); err != nil {
		t.Fatalf("CreateDeployment() error = %v", err)
	}
	if len(sender.input.DownloadURL) >= 256 || !strings.Contains(sender.input.DownloadURL, "/api/v1/device-ota/download/") {
		t.Fatalf("download URL = %q", sender.input.DownloadURL)
	}
}
