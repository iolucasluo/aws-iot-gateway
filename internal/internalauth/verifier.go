package internalauth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	SignatureTimeWindow = 5 * time.Minute
	NonceTTL            = 2 * SignatureTimeWindow
)

var (
	ErrUnknownAPIKey               = errors.New("unknown api key")
	ErrCredentialRevoked           = errors.New("credential is disabled or expired")
	ErrInvalidSignature            = errors.New("invalid signature")
	ErrTimestampOutsideWindow      = errors.New("timestamp outside signature window")
	ErrNonceReplayed               = errors.New("nonce already used")
	ErrReplayProtectionUnavailable = errors.New("replay protection unavailable")
)

type Credential struct {
	InternalAppID string
	APIKey        string
	APISecret     string
	Permissions   []string
	// ProjectID 只在 DB 来源（平台面 api_credentials）非空；
	// 静态 INTERNAL_APPS_JSON 来源为空，表示该主体没有项目作用域。
	ProjectID string
}

type Principal struct {
	InternalAppID string
	APIKey        string
	Permissions   []string
	ProjectID     string
}

func (principal Principal) HasPermission(permission string) bool {
	for _, candidate := range principal.Permissions {
		if candidate == permission {
			return true
		}
	}
	return false
}

type NonceStore interface {
	Consume(context.Context, string, time.Duration) (bool, error)
}

// CredentialStore 是 DB 凭证来源（平台面 projects/api_credentials）。
//
// 约定：
//   - 该 api_key 在 DB 中不存在：返回 ErrUnknownAPIKey，允许回退静态来源；
//   - 存在但已禁用/过期：返回 ErrCredentialRevoked，**不允许回退**；
//   - 其它错误：来源不可用，验签 fail closed。
//
// 这样"DB 里禁用了一个仍然存在于 INTERNAL_APPS_JSON 的 key"不会被静态配置复活。
type CredentialStore interface {
	FindCredentialByAPIKey(ctx context.Context, apiKey string) (Credential, error)
}

type Verifier struct {
	credentials map[string]Credential
	stores      []CredentialStore
	nonces      NonceStore
	now         func() time.Time
}

// NewVerifier 组装验签器。credentials 是部署期静态来源（本地开发与应急引导），
// stores 是 DB 来源；两者至少要有一个，且 DB 来源优先解析。
func NewVerifier(credentials []Credential, nonces NonceStore, now func() time.Time, stores ...CredentialStore) (*Verifier, error) {
	if nonces == nil {
		return nil, fmt.Errorf("nonce store is required")
	}
	if now == nil {
		now = time.Now
	}
	for _, store := range stores {
		if store == nil {
			return nil, fmt.Errorf("credential store must not be nil")
		}
	}
	byKey := make(map[string]Credential, len(credentials))
	seenApps := make(map[string]struct{}, len(credentials))
	for _, credential := range credentials {
		if strings.TrimSpace(credential.InternalAppID) == "" ||
			strings.TrimSpace(credential.APIKey) == "" || credential.APIKey != strings.TrimSpace(credential.APIKey) ||
			credential.APISecret == "" {
			return nil, fmt.Errorf("invalid internal app credential")
		}
		if _, duplicate := byKey[credential.APIKey]; duplicate {
			return nil, fmt.Errorf("duplicate api key")
		}
		if _, duplicate := seenApps[credential.InternalAppID]; duplicate {
			return nil, fmt.Errorf("duplicate internal app id")
		}
		credential.Permissions = append([]string(nil), credential.Permissions...)
		byKey[credential.APIKey] = credential
		seenApps[credential.InternalAppID] = struct{}{}
	}
	if len(byKey) == 0 && len(stores) == 0 {
		return nil, fmt.Errorf("at least one internal app credential or credential store is required")
	}
	return &Verifier{credentials: byKey, stores: stores, nonces: nonces, now: now}, nil
}

// resolveCredential 先查 DB 来源，全部未命中才回退静态来源。
// DB 来源报错时直接返回错误，不做任何回退。
func (verifier *Verifier) resolveCredential(ctx context.Context, apiKey string) (Credential, error) {
	for _, store := range verifier.stores {
		credential, err := store.FindCredentialByAPIKey(ctx, apiKey)
		if err == nil {
			if strings.TrimSpace(credential.InternalAppID) == "" || credential.APISecret == "" {
				return Credential{}, ErrUnknownAPIKey
			}
			return credential, nil
		}
		if errors.Is(err, ErrCredentialRevoked) {
			return Credential{}, ErrCredentialRevoked
		}
		if !errors.Is(err, ErrUnknownAPIKey) {
			return Credential{}, err
		}
	}
	if credential, found := verifier.credentials[apiKey]; found {
		return credential, nil
	}
	return Credential{}, ErrUnknownAPIKey
}

func (verifier *Verifier) Authenticate(ctx context.Context, request SignatureRequest) (Principal, error) {
	credential, err := verifier.resolveCredential(ctx, request.APIKey)
	if err != nil {
		if errors.Is(err, ErrUnknownAPIKey) || errors.Is(err, ErrCredentialRevoked) {
			return Principal{}, ErrUnknownAPIKey
		}
		return Principal{}, err
	}
	requestTime, err := timestampTime(request.Timestamp)
	if err != nil {
		return Principal{}, ErrInvalidSignature
	}
	skew := verifier.now().Sub(requestTime)
	if skew < -SignatureTimeWindow || skew > SignatureTimeWindow {
		return Principal{}, ErrTimestampOutsideWindow
	}
	if !Verify(credential.APISecret, request) {
		return Principal{}, ErrInvalidSignature
	}
	consumed, err := verifier.nonces.Consume(ctx, credential.InternalAppID+":"+request.Nonce, NonceTTL)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrReplayProtectionUnavailable, err)
	}
	if !consumed {
		return Principal{}, ErrNonceReplayed
	}
	return Principal{InternalAppID: credential.InternalAppID, APIKey: credential.APIKey,
		Permissions: append([]string(nil), credential.Permissions...), ProjectID: credential.ProjectID}, nil
}

func timestampTime(value string) (time.Time, error) {
	if !validTimestamp(value) {
		return time.Time{}, ErrInvalidCanonicalRequest
	}
	milliseconds, err := strconv.ParseInt(value[7:], 10, 64)
	if err != nil {
		return time.Time{}, ErrInvalidCanonicalRequest
	}
	return time.UnixMilli(milliseconds), nil
}
