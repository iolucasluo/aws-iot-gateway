package ota

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	downloadGrantTTL      = 15 * time.Minute
	downloadGrantAttempts = 4
)

var (
	ErrDownloadNotFound    = errors.New("OTA download capability not found")
	ErrDownloadUnavailable = errors.New("OTA download unavailable")
	downloadTokenPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{32,128}$`)
)

type DownloadGrant struct {
	DeviceSN     string `json:"device_sn"`
	DeploymentID string `json:"deployment_id"`
	ArtifactKey  string `json:"artifact_key"`
	SHA256       string `json:"sha256"`
	FileSize     int64  `json:"file_size"`
}

type DownloadGrantStore interface {
	Store(context.Context, string, DownloadGrant, int, time.Duration) error
	Redeem(context.Context, string, string) (DownloadGrant, error)
}

type ArtifactReader interface {
	Open(context.Context, string) (io.ReadCloser, int64, error)
}

type DownloadArtifact struct {
	Body     io.ReadCloser
	FileSize int64
	SHA256   string
}

type DownloadTokenGenerator func() (string, error)

type DownloadProxy struct {
	baseURL   string
	grants    DownloadGrantStore
	artifacts ArtifactReader
	newToken  DownloadTokenGenerator
}

type ServiceOption func(*Service)

func WithDownloadProxy(proxy *DownloadProxy) ServiceOption {
	return func(service *Service) {
		service.downloadProxy = proxy
	}
}

func NewDownloadProxy(baseURL string, grants DownloadGrantStore, artifacts ArtifactReader, newToken DownloadTokenGenerator) (*DownloadProxy, error) {
	baseURL = strings.TrimSpace(baseURL)
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" ||
		grants == nil || artifacts == nil {
		return nil, fmt.Errorf("OTA download proxy requires an HTTPS origin, grant store, and artifact reader")
	}
	if newToken == nil {
		newToken = randomDownloadToken
	}
	return &DownloadProxy{
		baseURL: strings.TrimSuffix(baseURL, "/"), grants: grants, artifacts: artifacts, newToken: newToken,
	}, nil
}

func randomDownloadToken() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (proxy *DownloadProxy) Issue(ctx context.Context, grant DownloadGrant) (string, error) {
	if proxy == nil || proxy.grants == nil || proxy.newToken == nil || !validDownloadGrant(grant) {
		return "", ErrDownloadUnavailable
	}
	token, err := proxy.newToken()
	if err != nil || !downloadTokenPattern.MatchString(token) {
		return "", ErrDownloadUnavailable
	}
	if err := proxy.grants.Store(ctx, token, grant, downloadGrantAttempts, downloadGrantTTL); err != nil {
		return "", fmt.Errorf("%w: store capability", ErrDownloadUnavailable)
	}
	return proxy.baseURL + "/api/v1/device-ota/download/" + url.PathEscape(grant.DeviceSN) + "/" + token, nil
}

func (proxy *DownloadProxy) Open(ctx context.Context, deviceSN, token string) (DownloadArtifact, error) {
	if proxy == nil || proxy.grants == nil || proxy.artifacts == nil || !tokenPattern.MatchString(deviceSN) ||
		!downloadTokenPattern.MatchString(token) {
		return DownloadArtifact{}, ErrDownloadNotFound
	}
	grant, err := proxy.grants.Redeem(ctx, token, deviceSN)
	if err != nil || !validDownloadGrant(grant) || grant.DeviceSN != deviceSN {
		return DownloadArtifact{}, ErrDownloadNotFound
	}
	body, size, err := proxy.artifacts.Open(ctx, grant.ArtifactKey)
	if err != nil {
		return DownloadArtifact{}, fmt.Errorf("%w: open artifact", ErrDownloadUnavailable)
	}
	if size != grant.FileSize {
		_ = body.Close()
		return DownloadArtifact{}, fmt.Errorf("%w: artifact size mismatch", ErrDownloadUnavailable)
	}
	return DownloadArtifact{Body: body, FileSize: grant.FileSize, SHA256: grant.SHA256}, nil
}

func validDownloadGrant(grant DownloadGrant) bool {
	return tokenPattern.MatchString(grant.DeviceSN) && uuidPattern.MatchString(grant.DeploymentID) &&
		strings.TrimSpace(grant.ArtifactKey) != "" && len(grant.ArtifactKey) <= 512 &&
		sha256Pattern.MatchString(grant.SHA256) && grant.FileSize > 0 && grant.FileSize <= maxFirmwareBytes
}

func (service *Service) OpenDownload(ctx context.Context, deviceSN, token string) (DownloadArtifact, error) {
	if service == nil || service.downloadProxy == nil {
		return DownloadArtifact{}, ErrDownloadNotFound
	}
	return service.downloadProxy.Open(ctx, deviceSN, token)
}
