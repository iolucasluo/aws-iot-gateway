package ota

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	testVersionID    = "11111111-1111-4111-8111-111111111111"
	testDeploymentID = "22222222-2222-4222-8222-222222222222"
)

type repositoryStub struct {
	versions       []FirmwareVersion
	created        Deployment
	createdVersion FirmwareVersion
	updates        []string
	order          *[]string
}

func (repository *repositoryStub) CreateFirmwareVersion(_ context.Context, version FirmwareVersion) (FirmwareVersion, error) {
	repository.versions = append(repository.versions, version)
	return version, nil
}

func (repository *repositoryStub) GetFirmwareVersionForPublisher(_ context.Context, _, _ string) (FirmwareVersion, error) {
	return repository.versions[0], nil
}

func (repository *repositoryStub) CompleteFirmwareVersion(_ context.Context, _ string, _ string) (FirmwareVersion, error) {
	version := repository.versions[0]
	version.Status = FirmwareStatusAvailable
	return version, nil
}

func (repository *repositoryStub) ListAvailableFirmwareVersions(_ context.Context, pid string) ([]FirmwareVersion, error) {
	return repository.versions, nil
}

func (repository *repositoryStub) CreateDeployment(_ context.Context, deployment Deployment) (Deployment, FirmwareVersion, error) {
	if repository.order != nil {
		*repository.order = append(*repository.order, "create")
	}
	repository.created = deployment
	return deployment, repository.createdVersion, nil
}

func (repository *repositoryStub) UpdateDeployment(_ context.Context, deploymentID, state, reason string) error {
	if repository.order != nil {
		*repository.order = append(*repository.order, "update:"+state)
	}
	repository.updates = append(repository.updates, state+":"+reason)
	return nil
}

func (repository *repositoryStub) GetDeployment(_ context.Context, _, _, deploymentID string) (Deployment, error) {
	result := repository.created
	result.ID = deploymentID
	return result, nil
}

type artifactStoreStub struct {
	upload UploadTarget
	url    string
}

func (store artifactStoreStub) PrepareUpload(context.Context, string, int64, string, time.Duration) (UploadTarget, error) {
	return store.upload, nil
}
func (store artifactStoreStub) Verify(context.Context, string, int64, string) error { return nil }
func (store artifactStoreStub) DownloadURL(context.Context, string, time.Duration) (string, error) {
	return store.url, nil
}

type commandSenderStub struct {
	err   error
	order *[]string
	input Command
}

func (sender *commandSenderStub) SendOTACommand(_ context.Context, command Command) error {
	if sender.order != nil {
		*sender.order = append(*sender.order, "publish")
	}
	sender.input = command
	return sender.err
}

func validFirmwareInput() CreateFirmwareVersionInput {
	return CreateFirmwareVersionInput{
		IdempotencyKey: "publish-v1", PID: "feeder-x1", Version: "v1.2.3", FirmwareBuild: 42,
		SHA256:           "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ManifestSHA256:   "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		FileSize:         1234,
		ReleaseStatement: "schema=explorex-ota-v2|pid=feeder-x1|version=v1.2.3|firmware_build=42|upgrade_type=app|manifest_sha256=abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789|sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef|file_size=1234",
		Signature:        "c2lnbmF0dXJl",
	}
}

func TestCreateFirmwareVersionReturnsChecksumBoundUploadAndKeepsSigningKeyOutOfGateway(t *testing.T) {
	t.Parallel()
	repository := &repositoryStub{}
	service := NewService(repository, artifactStoreStub{upload: UploadTarget{URL: "https://upload.example/firmware", Headers: map[string]string{"x-amz-checksum-sha256": "bound-checksum"}}}, nil, "staging", nil, func() (string, error) { return testVersionID, nil }, time.Now)

	created, err := service.CreateFirmwareVersion(context.Background(), "publisher", validFirmwareInput())
	if err != nil {
		t.Fatalf("CreateFirmwareVersion() error = %v", err)
	}
	if created.FirmwareVersion.Status != FirmwareStatusUploading || created.Upload == nil {
		t.Fatalf("created = %+v", created)
	}
	if created.Upload.Headers["x-amz-checksum-sha256"] != "bound-checksum" {
		t.Fatalf("upload headers = %#v", created.Upload.Headers)
	}
}

func TestFirmwareEnvironmentMetadataIsValidatedAndReturned(t *testing.T) {
	t.Parallel()
	input := validFirmwareInput()
	input.TargetEnvironmentID = "local-131"
	input.TargetEnvironmentCode = "02"
	input.ReleaseStatement = "schema=explorex-ota-v3|pid=feeder-x1|version=v1.2.3|firmware_build=42|upgrade_type=environment|target_environment_id=local-131|target_environment_code=02|manifest_sha256=" + input.ManifestSHA256 + "|sha256=" + input.SHA256 + "|file_size=1234"
	service := NewService(&repositoryStub{}, artifactStoreStub{upload: UploadTarget{URL: "https://upload.example/firmware"}}, nil, "staging", nil, func() (string, error) { return testVersionID, nil }, time.Now)
	created, err := service.CreateFirmwareVersion(context.Background(), "publisher", input)
	if err != nil {
		t.Fatalf("CreateFirmwareVersion() error = %v", err)
	}
	if created.FirmwareVersion.TargetEnvironmentID != "local-131" || created.FirmwareVersion.TargetEnvironmentCode != "02" {
		t.Fatalf("environment metadata = %+v", created.FirmwareVersion)
	}
	for _, invalid := range []struct{ id, code string }{
		{"", "02"}, {"local-131", "2"}, {"local/131", "02"},
	} {
		bad := validFirmwareInput()
		bad.TargetEnvironmentID, bad.TargetEnvironmentCode = invalid.id, invalid.code
		if _, err := service.CreateFirmwareVersion(context.Background(), "publisher", bad); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid metadata %+v error = %v, want ErrInvalidInput", invalid, err)
		}
	}
}

func TestDevelopmentDirectURLMustMatchGatewayAllowlist(t *testing.T) {
	t.Parallel()
	input := validFirmwareInput()
	input.SourceURL = "http://192.168.1.20:18080/ota.tar.gz"
	service := NewService(&repositoryStub{}, nil, nil, "development", []string{"http://192.168.1.10:18080"}, func() (string, error) { return testVersionID, nil }, time.Now)

	if _, err := service.CreateFirmwareVersion(context.Background(), "publisher", input); !errors.Is(err, ErrSourceURLForbidden) {
		t.Fatalf("error = %v, want ErrSourceURLForbidden", err)
	}
}

func TestDeploymentIsPersistedBeforePublishAndACKOnlyMarksAccepted(t *testing.T) {
	t.Parallel()
	order := []string{}
	repository := &repositoryStub{order: &order, createdVersion: FirmwareVersion{
		ID: testVersionID, PID: "feeder-x1", Version: "v1.2.3", FirmwareBuild: 42,
		SHA256: validFirmwareInput().SHA256, ManifestSHA256: validFirmwareInput().ManifestSHA256,
		FileSize: 1234, ReleaseStatement: validFirmwareInput().ReleaseStatement,
		Signature: validFirmwareInput().Signature, ArtifactKey: "ota/feeder-x1/v1.tar.gz",
	}}
	sender := &commandSenderStub{order: &order}
	service := NewService(repository, artifactStoreStub{url: "https://download.example/firmware"}, sender, "staging", nil, func() (string, error) { return testDeploymentID, nil }, time.Now)

	created, err := service.CreateDeployment(context.Background(), "demo-app", "device-id", "SN-1", CreateDeploymentInput{FirmwareVersionID: testVersionID, IdempotencyKey: "one-device-one-click"})
	if err != nil {
		t.Fatalf("CreateDeployment() error = %v", err)
	}
	if got := order; len(got) != 3 || got[0] != "create" || got[1] != "publish" || got[2] != "update:accepted" {
		t.Fatalf("operation order = %v", got)
	}
	if created.State != DeploymentStateAccepted || sender.input.DeploymentID != testDeploymentID {
		t.Fatalf("deployment = %+v command = %+v", created, sender.input)
	}
}

func TestPublishFailureLeavesFailedDeploymentRecord(t *testing.T) {
	t.Parallel()
	repository := &repositoryStub{createdVersion: FirmwareVersion{
		ID: testVersionID, PID: "feeder-x1", Version: "v1.2.3", FirmwareBuild: 42,
		SHA256: validFirmwareInput().SHA256, ManifestSHA256: validFirmwareInput().ManifestSHA256,
		FileSize: 1234, ReleaseStatement: validFirmwareInput().ReleaseStatement,
		Signature: validFirmwareInput().Signature, SourceURL: "http://192.168.1.10:18080/ota.tar.gz",
	}}
	service := NewService(repository, nil, &commandSenderStub{err: errors.New("offline")}, "development", []string{"http://192.168.1.10:18080"}, func() (string, error) { return testDeploymentID, nil }, time.Now)

	if _, err := service.CreateDeployment(context.Background(), "demo-app", "device-id", "SN-1", CreateDeploymentInput{FirmwareVersionID: testVersionID, IdempotencyKey: "one-device-one-click"}); !errors.Is(err, ErrDispatchFailed) {
		t.Fatalf("error = %v, want ErrDispatchFailed", err)
	}
	if len(repository.updates) != 1 || repository.updates[0] != "failed:dispatch_failed" {
		t.Fatalf("updates = %v", repository.updates)
	}
}

func TestRecordIDsAreValidatedBeforeRepositoryAccess(t *testing.T) {
	t.Parallel()
	validID := testVersionID
	repository := &repositoryStub{
		versions: []FirmwareVersion{{ID: validID, Status: FirmwareStatusAvailable}},
		createdVersion: FirmwareVersion{
			ID: validID, PID: "feeder-x1", Version: "v1.2.3", FirmwareBuild: 42,
			SHA256: validFirmwareInput().SHA256, FileSize: 1234,
			ReleaseStatement: validFirmwareInput().ReleaseStatement,
			Signature:        validFirmwareInput().Signature, SourceURL: "https://download.example/firmware",
		},
	}
	service := NewService(repository, artifactStoreStub{}, &commandSenderStub{}, "staging", nil,
		func() (string, error) { return validID, nil }, time.Now)

	if _, err := service.CreateDeployment(context.Background(), "demo-app", "device-id", "SN-1",
		CreateDeploymentInput{FirmwareVersionID: "not-a-uuid", IdempotencyKey: "one-click"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateDeployment() error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.GetDeployment(context.Background(), "demo-app", "device-id", "not-a-uuid"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("GetDeployment() error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.CompleteFirmwareVersion(context.Background(), "publisher", "not-a-uuid"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CompleteFirmwareVersion() error = %v, want ErrInvalidInput", err)
	}
}

func TestPublishedMetadataFitsTheDeviceCommandEnvelope(t *testing.T) {
	t.Parallel()
	input := validFirmwareInput()
	// RSA-3072 signatures are 384 bytes -> 512 base64 chars; the production OTA key
	// must fit the device command envelope.
	input.Signature = base64.StdEncoding.EncodeToString(make([]byte, 384))
	if err := validateFirmwareInput(input); err != nil {
		t.Fatalf("validateFirmwareInput() error = %v, want nil for RSA-3072 signature", err)
	}

	// 768 bytes -> 1024 base64 chars exceeds the device command envelope bound.
	input = validFirmwareInput()
	input.Signature = base64.StdEncoding.EncodeToString(make([]byte, 768))
	if err := validateFirmwareInput(input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("validateFirmwareInput() error = %v, want ErrInvalidInput for oversized signature", err)
	}

	input = validFirmwareInput()
	input.SourceURL = "http://192.168.1.10:18080/" + strings.Repeat("a", 1024)
	service := NewService(&repositoryStub{}, nil, nil, "development",
		[]string{"http://192.168.1.10:18080"}, func() (string, error) { return testVersionID, nil }, time.Now)
	if _, err := service.CreateFirmwareVersion(context.Background(), "publisher", input); !errors.Is(err, ErrSourceURLForbidden) {
		t.Fatalf("CreateFirmwareVersion() error = %v, want ErrSourceURLForbidden", err)
	}
}

func TestEnvironmentMigrationDoesNotRequireMonotonicBuild(t *testing.T) {
	t.Parallel()
	input := validFirmwareInput()
	input.UpgradeType = UpgradeTypeEnvironment
	input.TargetEnvironmentID = "demo-local"
	input.TargetEnvironmentCode = "02"
	input.FirmwareBuild = 1
	input.ReleaseStatement = "schema=explorex-ota-v3|pid=feeder-x1|version=v1.2.3|firmware_build=1|upgrade_type=environment|target_environment_id=demo-local|target_environment_code=02|manifest_sha256=" + input.ManifestSHA256 + "|sha256=" + input.SHA256 + "|file_size=1234"
	service := NewService(&repositoryStub{}, artifactStoreStub{upload: UploadTarget{URL: "https://upload.example/firmware"}}, nil, "staging", nil, func() (string, error) { return testVersionID, nil }, time.Now)
	created, err := service.CreateFirmwareVersion(context.Background(), "publisher", input)
	if err != nil {
		t.Fatalf("CreateFirmwareVersion() error = %v", err)
	}
	if created.FirmwareVersion.UpgradeType != UpgradeTypeEnvironment {
		t.Fatalf("upgrade type = %q", created.FirmwareVersion.UpgradeType)
	}
}
