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
}

type Principal struct {
	InternalAppID string
	APIKey        string
	Permissions   []string
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

type Verifier struct {
	credentials map[string]Credential
	nonces      NonceStore
	now         func() time.Time
}

func NewVerifier(credentials []Credential, nonces NonceStore, now func() time.Time) (*Verifier, error) {
	if nonces == nil {
		return nil, fmt.Errorf("nonce store is required")
	}
	if now == nil {
		now = time.Now
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
	if len(byKey) == 0 {
		return nil, fmt.Errorf("at least one internal app credential is required")
	}
	return &Verifier{credentials: byKey, nonces: nonces, now: now}, nil
}

func (verifier *Verifier) Authenticate(ctx context.Context, request SignatureRequest) (Principal, error) {
	credential, found := verifier.credentials[request.APIKey]
	if !found {
		return Principal{}, ErrUnknownAPIKey
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
		Permissions: append([]string(nil), credential.Permissions...)}, nil
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
