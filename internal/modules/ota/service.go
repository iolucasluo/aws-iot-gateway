package ota

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	FirmwareStatusUploading = "uploading"
	FirmwareStatusAvailable = "available"

	DeploymentStatePending    = "pending"
	DeploymentStateAccepted   = "accepted"
	DeploymentStateFailed     = "failed"
	DeploymentStateSucceeded  = "succeeded"
	DeploymentStateRolledBack = "rolled_back"

	UpgradeTypeApp         = "app"
	UpgradeTypeEnvironment = "environment"

	maxFirmwareBytes = int64(3 * 1024 * 1024)
)

var (
	ErrInvalidInput        = errors.New("invalid OTA input")
	ErrSourceURLForbidden  = errors.New("OTA source URL is forbidden")
	ErrDispatchFailed      = errors.New("OTA dispatch failed")
	ErrNotFound            = errors.New("OTA record not found")
	ErrConflict            = errors.New("OTA state conflict")
	ErrDeviceOffline       = errors.New("OTA device is offline")
	sha256Pattern          = regexp.MustCompile(`^[0-9a-f]{64}$`)
	environmentCodePattern = regexp.MustCompile(`^(0[1-9]|[1-9][0-9])$`)
	tokenPattern           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	uuidPattern            = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

type FirmwareVersion struct {
	ID                    string     `json:"id"`
	CreatedByAppID        string     `json:"-"`
	IdempotencyKey        string     `json:"-"`
	PID                   string     `json:"pid"`
	UpgradeType           string     `json:"upgrade_type,omitempty"`
	TargetEnvironmentID   string     `json:"target_environment_id,omitempty"`
	TargetEnvironmentCode string     `json:"target_environment_code,omitempty"`
	Version               string     `json:"version"`
	FirmwareBuild         int64      `json:"firmware_build"`
	SHA256                string     `json:"sha256"`
	ManifestSHA256        string     `json:"manifest_sha256"`
	FileSize              int64      `json:"file_size"`
	ReleaseStatement      string     `json:"-"`
	Signature             string     `json:"-"`
	ArtifactKey           string     `json:"-"`
	SourceURL             string     `json:"-"`
	Status                string     `json:"status"`
	CreatedAt             time.Time  `json:"created_at"`
	AvailableAt           *time.Time `json:"available_at,omitempty"`
}

type Deployment struct {
	ID                string     `json:"id"`
	InternalAppID     string     `json:"-"`
	DeviceID          string     `json:"-"`
	DeviceSN          string     `json:"device_sn"`
	FirmwareVersionID string     `json:"firmware_version_id"`
	TargetVersion     string     `json:"target_version"`
	TargetBuild       int64      `json:"target_build"`
	IdempotencyKey    string     `json:"-"`
	State             string     `json:"state"`
	FailureReason     string     `json:"failure_reason,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
}

type CreateFirmwareVersionInput struct {
	IdempotencyKey        string `json:"idempotency_key"`
	PID                   string `json:"pid"`
	UpgradeType           string `json:"upgrade_type,omitempty"`
	TargetEnvironmentID   string `json:"target_environment_id,omitempty"`
	TargetEnvironmentCode string `json:"target_environment_code,omitempty"`
	Version               string `json:"version"`
	FirmwareBuild         int64  `json:"firmware_build"`
	SHA256                string `json:"sha256"`
	ManifestSHA256        string `json:"manifest_sha256"`
	FileSize              int64  `json:"file_size"`
	ReleaseStatement      string `json:"release_statement"`
	Signature             string `json:"signature"`
	SourceURL             string `json:"source_url,omitempty"`
}

type CreateDeploymentInput struct {
	FirmwareVersionID string `json:"firmware_version_id"`
	IdempotencyKey    string `json:"idempotency_key"`
}

type UploadTarget struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

type CreateFirmwareVersionResult struct {
	FirmwareVersion FirmwareVersion `json:"firmware_version"`
	Upload          *UploadTarget   `json:"upload,omitempty"`
}

type Command struct {
	DeploymentID     string
	DeviceID         string
	DeviceSN         string
	PID              string
	UpgradeType      string
	Version          string
	FirmwareBuild    int64
	DownloadURL      string
	SHA256           string
	FileSize         int64
	ReleaseStatement string
	Signature        string
}

type Repository interface {
	CreateFirmwareVersion(context.Context, FirmwareVersion) (FirmwareVersion, error)
	GetFirmwareVersionForPublisher(context.Context, string, string) (FirmwareVersion, error)
	CompleteFirmwareVersion(context.Context, string, string) (FirmwareVersion, error)
	ListAvailableFirmwareVersions(context.Context, string) ([]FirmwareVersion, error)
	CreateDeployment(context.Context, Deployment) (Deployment, FirmwareVersion, error)
	UpdateDeployment(context.Context, string, string, string) error
	GetDeployment(context.Context, string, string, string) (Deployment, error)
}

type ArtifactStore interface {
	PrepareUpload(context.Context, string, int64, string, time.Duration) (UploadTarget, error)
	Verify(context.Context, string, int64, string) error
	DownloadURL(context.Context, string, time.Duration) (string, error)
}

type CommandSender interface {
	SendOTACommand(context.Context, Command) error
}

type IDGenerator func() (string, error)

type Service struct {
	repository     Repository
	artifacts      ArtifactStore
	commands       CommandSender
	environment    string
	allowedOrigins map[string]struct{}
	downloadProxy  *DownloadProxy
	newID          IDGenerator
	now            func() time.Time
}

func NewService(repository Repository, artifacts ArtifactStore, commands CommandSender, environment string, allowedOrigins []string, newID IDGenerator, now func() time.Time, options ...ServiceOption) *Service {
	origins := make(map[string]struct{}, len(allowedOrigins))
	for _, value := range allowedOrigins {
		origins[strings.TrimSpace(value)] = struct{}{}
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = randomUUID
	}
	service := &Service{repository: repository, artifacts: artifacts, commands: commands, environment: environment, allowedOrigins: origins, newID: newID, now: now}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func randomUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	hexValue := fmt.Sprintf("%x", value)
	return hexValue[:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:], nil
}

func (service *Service) CreateFirmwareVersion(ctx context.Context, internalAppID string, input CreateFirmwareVersionInput) (CreateFirmwareVersionResult, error) {
	if service == nil || service.repository == nil || service.newID == nil || strings.TrimSpace(internalAppID) == "" || validateFirmwareInput(input) != nil {
		return CreateFirmwareVersionResult{}, ErrInvalidInput
	}
	id, err := service.newID()
	if err != nil {
		return CreateFirmwareVersionResult{}, fmt.Errorf("generate OTA firmware version id: %w", err)
	}
	if !uuidPattern.MatchString(id) {
		return CreateFirmwareVersionResult{}, fmt.Errorf("generate OTA firmware version id: invalid UUID")
	}
	now := service.now().UTC()
	version := FirmwareVersion{
		ID: id, CreatedByAppID: internalAppID, IdempotencyKey: input.IdempotencyKey,
		PID: input.PID, UpgradeType: normalizedUpgradeType(input), TargetEnvironmentID: input.TargetEnvironmentID, TargetEnvironmentCode: input.TargetEnvironmentCode,
		Version: input.Version, FirmwareBuild: input.FirmwareBuild,
		SHA256: input.SHA256, ManifestSHA256: input.ManifestSHA256, FileSize: input.FileSize,
		ReleaseStatement: input.ReleaseStatement, Signature: input.Signature, CreatedAt: now,
	}
	if strings.TrimSpace(input.SourceURL) != "" {
		if service.environment != "development" || !service.sourceURLAllowed(input.SourceURL) {
			return CreateFirmwareVersionResult{}, ErrSourceURLForbidden
		}
		version.SourceURL = input.SourceURL
		version.Status = FirmwareStatusAvailable
		version.AvailableAt = &now
	} else {
		if service.artifacts == nil {
			return CreateFirmwareVersionResult{}, ErrInvalidInput
		}
		version.ArtifactKey = fmt.Sprintf("ota/%s/%d-%s.tar.gz", input.PID, input.FirmwareBuild, input.SHA256)
		version.Status = FirmwareStatusUploading
	}
	created, err := service.repository.CreateFirmwareVersion(ctx, version)
	if err != nil {
		return CreateFirmwareVersionResult{}, err
	}
	created.UpgradeType = version.UpgradeType
	var upload *UploadTarget
	if created.Status == FirmwareStatusUploading {
		target, err := service.artifacts.PrepareUpload(ctx, created.ArtifactKey, created.FileSize, created.SHA256, 15*time.Minute)
		if err != nil {
			return CreateFirmwareVersionResult{}, fmt.Errorf("prepare OTA upload: %w", err)
		}
		upload = &target
	}
	return CreateFirmwareVersionResult{FirmwareVersion: created, Upload: upload}, nil
}

func (service *Service) CompleteFirmwareVersion(ctx context.Context, internalAppID, versionID string) (FirmwareVersion, error) {
	if service == nil || service.repository == nil || service.artifacts == nil || strings.TrimSpace(internalAppID) == "" || !uuidPattern.MatchString(versionID) {
		return FirmwareVersion{}, ErrInvalidInput
	}
	version, err := service.repository.GetFirmwareVersionForPublisher(ctx, internalAppID, versionID)
	if err != nil {
		return FirmwareVersion{}, err
	}
	if version.Status == FirmwareStatusAvailable {
		return version, nil
	}
	if version.Status != FirmwareStatusUploading || version.ArtifactKey == "" {
		return FirmwareVersion{}, ErrConflict
	}
	if err := service.artifacts.Verify(ctx, version.ArtifactKey, version.FileSize, version.SHA256); err != nil {
		return FirmwareVersion{}, fmt.Errorf("verify OTA upload: %w", err)
	}
	return service.repository.CompleteFirmwareVersion(ctx, internalAppID, versionID)
}

func (service *Service) ListAvailableFirmwareVersions(ctx context.Context, pid string) ([]FirmwareVersion, error) {
	if service == nil || service.repository == nil || !tokenPattern.MatchString(pid) {
		return nil, ErrInvalidInput
	}
	versions, err := service.repository.ListAvailableFirmwareVersions(ctx, pid)
	if err != nil {
		return nil, err
	}
	for index := range versions {
		versions[index].UpgradeType = firmwareUpgradeType(versions[index])
	}
	return versions, nil
}

func (service *Service) CreateDeployment(ctx context.Context, internalAppID, deviceID, deviceSN string, input CreateDeploymentInput) (Deployment, error) {
	if service == nil || service.repository == nil || service.commands == nil || service.newID == nil || strings.TrimSpace(internalAppID) == "" || strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceSN) == "" || !uuidPattern.MatchString(input.FirmwareVersionID) || !tokenPattern.MatchString(input.IdempotencyKey) {
		return Deployment{}, ErrInvalidInput
	}
	id, err := service.newID()
	if err != nil {
		return Deployment{}, fmt.Errorf("generate OTA deployment id: %w", err)
	}
	if !uuidPattern.MatchString(id) {
		return Deployment{}, fmt.Errorf("generate OTA deployment id: invalid UUID")
	}
	now := service.now().UTC()
	deployment, version, err := service.repository.CreateDeployment(ctx, Deployment{
		ID: id, InternalAppID: internalAppID, DeviceID: deviceID, DeviceSN: deviceSN,
		FirmwareVersionID: input.FirmwareVersionID, IdempotencyKey: input.IdempotencyKey,
		State: DeploymentStatePending, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return Deployment{}, err
	}
	if deployment.ID != id {
		return deployment, nil
	}
	downloadURL := version.SourceURL
	if downloadURL == "" && service.downloadProxy != nil {
		downloadURL, err = service.downloadProxy.Issue(ctx, DownloadGrant{
			DeviceSN: deviceSN, DeploymentID: deployment.ID,
			ArtifactKey: version.ArtifactKey, SHA256: version.SHA256, FileSize: version.FileSize,
		})
		if err != nil {
			_ = service.repository.UpdateDeployment(ctx, deployment.ID, DeploymentStateFailed, "artifact_unavailable")
			return Deployment{}, fmt.Errorf("%w: %v", ErrDispatchFailed, err)
		}
	}
	if downloadURL == "" {
		if service.artifacts == nil {
			_ = service.repository.UpdateDeployment(ctx, deployment.ID, DeploymentStateFailed, "artifact_unavailable")
			return Deployment{}, ErrDispatchFailed
		}
		downloadURL, err = service.artifacts.DownloadURL(ctx, version.ArtifactKey, 15*time.Minute)
		if err != nil {
			_ = service.repository.UpdateDeployment(ctx, deployment.ID, DeploymentStateFailed, "artifact_unavailable")
			return Deployment{}, fmt.Errorf("%w: %v", ErrDispatchFailed, err)
		}
	}
	err = service.commands.SendOTACommand(ctx, Command{
		DeploymentID: deployment.ID, DeviceID: deviceID, DeviceSN: deviceSN, PID: version.PID,
		UpgradeType: firmwareUpgradeType(version),
		Version:     version.Version, FirmwareBuild: version.FirmwareBuild, DownloadURL: downloadURL,
		SHA256: version.SHA256, FileSize: version.FileSize,
		ReleaseStatement: version.ReleaseStatement, Signature: version.Signature,
	})
	if err != nil {
		_ = service.repository.UpdateDeployment(ctx, deployment.ID, DeploymentStateFailed, "dispatch_failed")
		return Deployment{}, fmt.Errorf("%w: %v", ErrDispatchFailed, err)
	}
	if err := service.repository.UpdateDeployment(ctx, deployment.ID, DeploymentStateAccepted, ""); err != nil {
		return Deployment{}, err
	}
	deployment.State = DeploymentStateAccepted
	deployment.TargetVersion = version.Version
	deployment.TargetBuild = version.FirmwareBuild
	deployment.UpdatedAt = service.now().UTC()
	return deployment, nil
}

func (service *Service) GetDeployment(ctx context.Context, internalAppID, deviceID, deploymentID string) (Deployment, error) {
	if service == nil || service.repository == nil || strings.TrimSpace(internalAppID) == "" || strings.TrimSpace(deviceID) == "" || !uuidPattern.MatchString(deploymentID) {
		return Deployment{}, ErrInvalidInput
	}
	return service.repository.GetDeployment(ctx, internalAppID, deviceID, deploymentID)
}

func normalizedUpgradeType(input CreateFirmwareVersionInput) string {
	if input.UpgradeType != "" {
		return input.UpgradeType
	}
	if input.TargetEnvironmentID != "" {
		return UpgradeTypeEnvironment
	}
	return UpgradeTypeApp
}

func firmwareUpgradeType(version FirmwareVersion) string {
	if version.UpgradeType != "" {
		return version.UpgradeType
	}
	if version.TargetEnvironmentID != "" {
		return UpgradeTypeEnvironment
	}
	return UpgradeTypeApp
}

func validateFirmwareInput(input CreateFirmwareVersionInput) error {
	upgradeType := normalizedUpgradeType(input)
	if !tokenPattern.MatchString(input.IdempotencyKey) || !tokenPattern.MatchString(input.PID) ||
		!tokenPattern.MatchString(input.Version) || input.FirmwareBuild <= 0 || input.FirmwareBuild > 2147483647 || input.FileSize <= 0 ||
		(upgradeType != UpgradeTypeApp && upgradeType != UpgradeTypeEnvironment) ||
		(input.TargetEnvironmentID != "" && !tokenPattern.MatchString(input.TargetEnvironmentID)) ||
		(input.TargetEnvironmentCode != "" && !environmentCodePattern.MatchString(input.TargetEnvironmentCode)) ||
		((input.TargetEnvironmentID == "") != (input.TargetEnvironmentCode == "")) ||
		(upgradeType == UpgradeTypeEnvironment && input.TargetEnvironmentID == "") ||
		(upgradeType == UpgradeTypeApp && input.TargetEnvironmentID != "") ||
		input.FileSize > maxFirmwareBytes || !sha256Pattern.MatchString(input.SHA256) ||
		!sha256Pattern.MatchString(input.ManifestSHA256) {
		return ErrInvalidInput
	}
	expected := fmt.Sprintf("schema=explorex-ota-v2|pid=%s|version=%s|firmware_build=%d|upgrade_type=app|manifest_sha256=%s|sha256=%s|file_size=%d", input.PID, input.Version, input.FirmwareBuild, input.ManifestSHA256, input.SHA256, input.FileSize)
	if upgradeType == UpgradeTypeEnvironment {
		expected = fmt.Sprintf("schema=explorex-ota-v3|pid=%s|version=%s|firmware_build=%d|upgrade_type=environment|target_environment_id=%s|target_environment_code=%s|manifest_sha256=%s|sha256=%s|file_size=%d", input.PID, input.Version, input.FirmwareBuild, input.TargetEnvironmentID, input.TargetEnvironmentCode, input.ManifestSHA256, input.SHA256, input.FileSize)
	}
	if input.ReleaseStatement != expected {
		return ErrInvalidInput
	}
	signature, err := base64.StdEncoding.DecodeString(input.Signature)
	if err != nil || len(input.Signature) > 511 || len(signature) == 0 ||
		base64.StdEncoding.EncodeToString(signature) != input.Signature {
		return ErrInvalidInput
	}
	return nil
}

func (service *Service) sourceURLAllowed(value string) bool {
	if len(value) > 1023 {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	origin := parsed.Scheme + "://" + parsed.Host
	_, ok := service.allowedOrigins[origin]
	return ok
}
