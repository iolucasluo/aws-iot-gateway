package media

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
)

const (
	LocalMediaContractVersion = "local-media.v1"
	localMediaMinLeaseSeconds = 30
	localMediaMaxLeaseSeconds = 900
)

var (
	ErrLocalMediaUnsupported     = errors.New("local media is unsupported for product")
	ErrLocalMediaRejected        = errors.New("device rejected local media")
	ErrInvalidLocalMediaResponse = errors.New("invalid local media device response")
	localMediaTokenPattern       = regexp.MustCompile(`^[A-Za-z0-9_-]{8,63}$`)
	localMediaFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type LocalMediaCommander interface {
	SendLocalMediaCommand(context.Context, string, string, string, json.RawMessage) (device.CommandResult, error)
}

type LocalMediaRevisionStore interface {
	Next(context.Context, string, int64) (int64, error)
}

type LocalMediaProductPolicy interface {
	ProductSupports(context.Context, string, string) (bool, error)
}

type LocalMediaCredentials struct {
	SessionID  string `json:"-"`
	Generation string `json:"generation"`
	Username   string `json:"username"`
	Password   string `json:"password"`
}

type LocalMediaSession struct {
	ContractVersion   string                `json:"contract_version"`
	Protocol          string                `json:"protocol"`
	SessionID         string                `json:"session_id"`
	State             string                `json:"state"`
	Revision          int64                 `json:"revision"`
	LeaseSeconds      int                   `json:"lease_seconds"`
	ExpiresAtEpochSec int64                 `json:"expires_at_epoch_sec"`
	Credentials       LocalMediaCredentials `json:"credentials"`
	ONVIFEndpoint     string                `json:"onvif_endpoint"`
	RTSPBaseURI       string                `json:"rtsp_base_uri"`
	Profiles          []string              `json:"profiles"`
}

type LocalMediaCloseResult struct {
	ContractVersion string `json:"contract_version"`
	Protocol        string `json:"protocol"`
	State           string `json:"state"`
	Revision        int64  `json:"revision"`
}

type LocalMediaCredentialGenerator func() (LocalMediaCredentials, error)

type LocalMediaService struct {
	commander      LocalMediaCommander
	revisions      LocalMediaRevisionStore
	products       LocalMediaProductPolicy
	now            func() time.Time
	newCredentials LocalMediaCredentialGenerator
	newSessionID   func() (string, error)
}

func NewLocalMediaService(
	commander LocalMediaCommander,
	revisions LocalMediaRevisionStore,
	products LocalMediaProductPolicy,
	now func() time.Time,
	newCredentials LocalMediaCredentialGenerator,
) *LocalMediaService {
	if now == nil {
		now = time.Now
	}
	if newCredentials == nil {
		newCredentials = generateLocalMediaCredentials
	}
	return &LocalMediaService{
		commander: commander, revisions: revisions, products: products, now: now,
		newCredentials: newCredentials, newSessionID: generateLocalMediaSessionID,
	}
}

func (service *LocalMediaService) Create(
	ctx context.Context, internalAppID, pid, deviceSN string, leaseSeconds int,
) (LocalMediaSession, error) {
	if service == nil || service.commander == nil || service.revisions == nil || service.products == nil ||
		strings.TrimSpace(internalAppID) == "" || strings.TrimSpace(pid) == "" ||
		strings.TrimSpace(deviceSN) == "" || leaseSeconds < localMediaMinLeaseSeconds ||
		leaseSeconds > localMediaMaxLeaseSeconds {
		return LocalMediaSession{}, ErrInvalidInput
	}
	supported, err := service.products.ProductSupports(ctx, pid, LocalMediaContractVersion)
	if err != nil {
		return LocalMediaSession{}, err
	}
	if !supported {
		return LocalMediaSession{}, ErrLocalMediaUnsupported
	}
	credentials, err := service.newCredentials()
	if err != nil {
		return LocalMediaSession{}, fmt.Errorf("generate local media credentials: %w", err)
	}
	if !validLocalMediaCredentials(credentials) {
		return LocalMediaSession{}, fmt.Errorf("generate local media credentials: invalid result")
	}
	now := service.now().UTC()
	revision, err := service.revisions.Next(ctx, deviceSN, now.UnixMilli())
	if err != nil {
		return LocalMediaSession{}, fmt.Errorf("allocate local media revision: %w", err)
	}
	if revision < now.UnixMilli() {
		return LocalMediaSession{}, fmt.Errorf("allocate local media revision: invalid result")
	}
	expiresAt := now.Unix() + int64(leaseSeconds)
	value, err := json.Marshal(map[string]any{
		"contract_version": LocalMediaContractVersion,
		"op":               "open", "protocol": "onvif", "revision": revision,
		"lease_seconds": leaseSeconds, "expires_at_epoch_sec": expiresAt,
		"credential_generation": credentials.Generation,
		"username":              credentials.Username, "password": credentials.Password,
	})
	if err != nil {
		return LocalMediaSession{}, err
	}
	result, err := service.commander.SendLocalMediaCommand(
		ctx, internalAppID, deviceSN, credentials.SessionID, value,
	)
	if err != nil {
		return LocalMediaSession{}, err
	}
	if result.Status != "success" {
		return LocalMediaSession{}, ErrLocalMediaRejected
	}
	onvifEndpoint, rtspBaseURI, err := validateLocalMediaReadyACK(
		result, credentials, revision, expiresAt,
	)
	if err != nil {
		return LocalMediaSession{}, err
	}
	return LocalMediaSession{
		ContractVersion: LocalMediaContractVersion, Protocol: "onvif",
		SessionID: credentials.SessionID, State: "ready", Revision: revision,
		LeaseSeconds: leaseSeconds, ExpiresAtEpochSec: expiresAt, Credentials: credentials,
		ONVIFEndpoint: onvifEndpoint, RTSPBaseURI: rtspBaseURI,
		Profiles: []string{"profile_main", "profile_sub"},
	}, nil
}

func (service *LocalMediaService) Close(
	ctx context.Context, internalAppID, pid, deviceSN, targetCredentialGeneration string,
) (LocalMediaCloseResult, error) {
	if service == nil || service.commander == nil || service.revisions == nil || service.products == nil ||
		strings.TrimSpace(internalAppID) == "" || strings.TrimSpace(pid) == "" ||
		strings.TrimSpace(deviceSN) == "" || len(targetCredentialGeneration) < 16 ||
		!localMediaTokenPattern.MatchString(targetCredentialGeneration) {
		return LocalMediaCloseResult{}, ErrInvalidInput
	}
	supported, err := service.products.ProductSupports(ctx, pid, LocalMediaContractVersion)
	if err != nil {
		return LocalMediaCloseResult{}, err
	}
	if !supported {
		return LocalMediaCloseResult{}, ErrLocalMediaUnsupported
	}
	sessionID, err := service.newSessionID()
	if err != nil {
		return LocalMediaCloseResult{}, fmt.Errorf("generate local media session id: %w", err)
	}
	if !localMediaTokenPattern.MatchString(sessionID) || len(sessionID) < 16 || len(sessionID) > 63 {
		return LocalMediaCloseResult{}, fmt.Errorf("generate local media session id: invalid result")
	}
	now := service.now().UTC()
	revision, err := service.revisions.Next(ctx, deviceSN, now.UnixMilli())
	if err != nil {
		return LocalMediaCloseResult{}, fmt.Errorf("allocate local media revision: %w", err)
	}
	if revision < now.UnixMilli() {
		return LocalMediaCloseResult{}, fmt.Errorf("allocate local media revision: invalid result")
	}
	value, err := json.Marshal(map[string]any{
		"contract_version": LocalMediaContractVersion,
		"op":               "close", "protocol": "onvif", "revision": revision,
		"target_credential_generation": targetCredentialGeneration,
	})
	if err != nil {
		return LocalMediaCloseResult{}, err
	}
	result, err := service.commander.SendLocalMediaCommand(ctx, internalAppID, deviceSN, sessionID, value)
	if err != nil {
		return LocalMediaCloseResult{}, err
	}
	if result.Status != "success" {
		return LocalMediaCloseResult{}, ErrLocalMediaRejected
	}
	if len(result.Data) != 8 || result.CommandID != sessionID ||
		result.Data["action"] != "local_media" ||
		result.Data["session_id"] != sessionID || result.Data["contract_version"] != LocalMediaContractVersion ||
		result.Data["protocol"] != "onvif" || !validLocalMediaApplyResult(result.Data["result"]) ||
		result.Data["state"] != "disabled" || !exactLocalMediaInteger(result.Data["code"], 0) ||
		!exactLocalMediaInteger(result.Data["revision"], revision) {
		return LocalMediaCloseResult{}, ErrInvalidLocalMediaResponse
	}
	return LocalMediaCloseResult{
		ContractVersion: LocalMediaContractVersion, Protocol: "onvif", State: "disabled", Revision: revision,
	}, nil
}

func validateLocalMediaReadyACK(
	result device.CommandResult, credentials LocalMediaCredentials, revision, expiresAt int64,
) (string, string, error) {
	data := result.Data
	if len(data) != 13 || result.CommandID != credentials.SessionID || data["action"] != "local_media" ||
		data["session_id"] != credentials.SessionID || data["contract_version"] != LocalMediaContractVersion ||
		data["protocol"] != "onvif" || !validLocalMediaApplyResult(data["result"]) || data["state"] != "ready" ||
		!exactLocalMediaInteger(data["code"], 0) ||
		data["credential_generation"] != credentials.Generation ||
		!exactLocalMediaInteger(data["revision"], revision) ||
		!exactLocalMediaInteger(data["expires_at_epoch_sec"], expiresAt) {
		return "", "", ErrInvalidLocalMediaResponse
	}
	fingerprint, ok := data["credential_fingerprint"].(string)
	onvifEndpoint, onvifOK := data["onvif_endpoint"].(string)
	rtspBaseURI, rtspOK := data["rtsp_base_uri"].(string)
	if !ok || !localMediaFingerprintPattern.MatchString(fingerprint) ||
		fingerprint != localMediaCredentialFingerprint(credentials) || !onvifOK || !rtspOK ||
		!validLocalMediaLocators(onvifEndpoint, rtspBaseURI) {
		return "", "", ErrInvalidLocalMediaResponse
	}
	return onvifEndpoint, rtspBaseURI, nil
}

func validLocalMediaApplyResult(value any) bool {
	result, ok := value.(string)
	return ok && (result == "applied" || result == "duplicate")
}

func localMediaCredentialFingerprint(credentials LocalMediaCredentials) string {
	digest := sha256.New()
	for _, field := range []string{
		"ExploreX-Local-Media-Credential-v1",
		credentials.Generation,
		credentials.Username,
		credentials.Password,
	} {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(field)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(field))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func validLocalMediaLocators(onvifEndpoint, rtspBaseURI string) bool {
	onvifURL, err := url.Parse(onvifEndpoint)
	if err != nil || onvifURL.Scheme != "http" || onvifURL.User != nil || onvifURL.Port() != "8081" ||
		onvifURL.Path != "/onvif/device_service" || onvifURL.RawQuery != "" || onvifURL.Fragment != "" {
		return false
	}
	onvifIP := net.ParseIP(onvifURL.Hostname())
	if onvifIP == nil || onvifIP.To4() == nil || !onvifIP.IsPrivate() ||
		onvifIP.IsLoopback() || onvifIP.IsUnspecified() {
		return false
	}
	rtspURL, err := url.Parse(rtspBaseURI)
	if err != nil || rtspURL.Scheme != "rtsp" || rtspURL.User != nil || rtspURL.Port() != "554" ||
		rtspURL.Path != "" || rtspURL.RawQuery != "" || rtspURL.Fragment != "" {
		return false
	}
	rtspIP := net.ParseIP(rtspURL.Hostname())
	return rtspIP != nil && rtspIP.To4() != nil && rtspIP.Equal(onvifIP)
}

func validLocalMediaCredentials(credentials LocalMediaCredentials) bool {
	return len(credentials.SessionID) >= 16 && len(credentials.SessionID) <= 63 &&
		localMediaTokenPattern.MatchString(credentials.SessionID) &&
		len(credentials.Generation) >= 16 && localMediaTokenPattern.MatchString(credentials.Generation) &&
		localMediaTokenPattern.MatchString(credentials.Username) && len(credentials.Password) >= 32 &&
		localMediaTokenPattern.MatchString(credentials.Password)
}

func exactLocalMediaInteger(value any, expected int64) bool {
	var integer int64
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed ||
			typed < math.MinInt64 || typed >= 9223372036854775808.0 {
			return false
		}
		integer = int64(typed)
	case int64:
		integer = typed
	case int:
		integer = int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return false
		}
		integer = parsed
	default:
		return false
	}
	return integer == expected
}

func generateLocalMediaCredentials() (LocalMediaCredentials, error) {
	sessionID, err := randomRestrictedToken("lm_", 16)
	if err != nil {
		return LocalMediaCredentials{}, err
	}
	generation, err := randomRestrictedToken("generation_", 12)
	if err != nil {
		return LocalMediaCredentials{}, err
	}
	username, err := randomRestrictedToken("onvif_", 8)
	if err != nil {
		return LocalMediaCredentials{}, err
	}
	password, err := randomRestrictedToken("", 24)
	if err != nil {
		return LocalMediaCredentials{}, err
	}
	return LocalMediaCredentials{
		SessionID: sessionID, Generation: generation, Username: username, Password: password,
	}, nil
}

func generateLocalMediaSessionID() (string, error) { return randomRestrictedToken("lm_", 16) }

func randomRestrictedToken(prefix string, randomBytes int) (string, error) {
	value := make([]byte, randomBytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value), nil
}
