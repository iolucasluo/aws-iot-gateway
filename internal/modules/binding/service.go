package binding

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	tokenTTL                    = 24 * time.Hour
	recommissionFinalizeTimeout = 20 * time.Second
)

var (
	ErrInvalidInput            = errors.New("invalid binding input")
	ErrBindingNotFound         = errors.New("binding not found")
	ErrTokenNotFound           = errors.New("token not found")
	ErrForbidden               = errors.New("device operation forbidden")
	ErrRecommissionUnavailable = errors.New("device recommission unavailable")
	ErrRecommissionRejected    = errors.New("device recommission rejected")
)

type Binding struct {
	ID                    string
	DeviceID              string
	DeviceSN              string
	PID                   string
	InternalAppID         string
	BindingExternalUserID string
	CertificateID         string
	Active                bool
}

type DeviceToken struct {
	ID        string
	BindingID string
	TokenHash [sha256.Size]byte
	ExpiresAt time.Time
	CreatedAt time.Time
}

type IssueTokenResult struct {
	DeviceToken string    `json:"device_token"`
	TokenType   string    `json:"token_type"`
	ExpiresIn   int64     `json:"expires_in"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type ResetReceipt struct {
	CommandID string
	Status    string
	Data      map[string]any
	Reason    string
}

type RecommissionResult struct {
	Binding Binding
	Receipt ResetReceipt
}

type Repository interface {
	FindActiveBinding(context.Context, string, string) (Binding, error)
	StoreToken(context.Context, DeviceToken) error
	ValidateToken(context.Context, string, [sha256.Size]byte, string) (Binding, error)
	FindBindingByToken(context.Context, [sha256.Size]byte) (Binding, error)
	ReleaseBinding(context.Context, string, string) (Binding, error)
}

type Recommissioner interface {
	Reset(context.Context, Binding, string) (ResetReceipt, error)
}

type TokenGenerator func() (string, error)

type Service struct {
	repository     Repository
	newToken       TokenGenerator
	now            func() time.Time
	recommissioner Recommissioner
}

func NewService(repository Repository, newToken TokenGenerator, now func() time.Time, recommissioner Recommissioner) *Service {
	if newToken == nil {
		newToken = randomToken
	}
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, newToken: newToken, now: now, recommissioner: recommissioner}
}

func (service *Service) IssueToken(ctx context.Context, internalAppID, deviceSN string) (IssueTokenResult, error) {
	internalAppID = strings.TrimSpace(internalAppID)
	deviceSN = strings.TrimSpace(deviceSN)
	if service.repository == nil || internalAppID == "" || deviceSN == "" {
		return IssueTokenResult{}, ErrInvalidInput
	}
	binding, err := service.repository.FindActiveBinding(ctx, internalAppID, deviceSN)
	if err != nil || !binding.Active || binding.InternalAppID != internalAppID || binding.DeviceSN != deviceSN {
		return IssueTokenResult{}, ErrForbidden
	}
	plaintext, err := service.newToken()
	if err != nil {
		return IssueTokenResult{}, fmt.Errorf("generate device token: %w", err)
	}
	if strings.TrimSpace(plaintext) == "" {
		return IssueTokenResult{}, fmt.Errorf("generate device token: empty token")
	}
	tokenID, err := randomUUID()
	if err != nil {
		return IssueTokenResult{}, fmt.Errorf("generate device token id: %w", err)
	}
	now := service.now().UTC()
	expiresAt := now.Add(tokenTTL)
	if err := service.repository.StoreToken(ctx, DeviceToken{
		ID: tokenID, BindingID: binding.ID, TokenHash: HashToken(plaintext), ExpiresAt: expiresAt, CreatedAt: now,
	}); err != nil {
		return IssueTokenResult{}, err
	}
	return IssueTokenResult{
		DeviceToken: plaintext, TokenType: "DeviceToken", ExpiresIn: int64(tokenTTL.Seconds()), ExpiresAt: expiresAt,
	}, nil
}

func (service *Service) AuthorizeDevice(ctx context.Context, internalAppID, token, deviceSN string) (Binding, error) {
	if service.repository == nil || strings.TrimSpace(internalAppID) == "" || strings.TrimSpace(token) == "" || strings.TrimSpace(deviceSN) == "" {
		return Binding{}, ErrForbidden
	}
	binding, err := service.repository.ValidateToken(ctx, internalAppID, HashToken(token), deviceSN)
	if err != nil || !binding.Active || binding.InternalAppID != internalAppID || binding.DeviceSN != deviceSN {
		return Binding{}, ErrForbidden
	}
	return binding, nil
}

func (service *Service) Unbind(ctx context.Context, internalAppID, token string) (Binding, error) {
	binding, err := service.bindingForToken(ctx, internalAppID, token)
	if err != nil {
		return Binding{}, err
	}
	return service.repository.ReleaseBinding(ctx, binding.ID, "unbind")
}

func (service *Service) Recommission(ctx context.Context, internalAppID, token, resetScope string) (RecommissionResult, error) {
	if resetScope != "runtime" && resetScope != "all" {
		return RecommissionResult{}, ErrInvalidInput
	}
	binding, err := service.bindingForToken(ctx, internalAppID, token)
	if err != nil {
		return RecommissionResult{}, err
	}
	if service.recommissioner == nil {
		return RecommissionResult{}, ErrRecommissionUnavailable
	}
	receipt, err := service.recommissioner.Reset(ctx, binding, resetScope)
	if err != nil {
		return RecommissionResult{}, fmt.Errorf("%w: %w", ErrRecommissionUnavailable, err)
	}
	if strings.TrimSpace(receipt.CommandID) == "" {
		return RecommissionResult{}, fmt.Errorf("%w: device ACK receipt is incomplete", ErrRecommissionUnavailable)
	}
	if receipt.Status != "success" || !successfulACK(receipt.Data) {
		return RecommissionResult{Binding: binding, Receipt: receipt}, ErrRecommissionRejected
	}
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), recommissionFinalizeTimeout)
	defer cancel()
	released, err := service.repository.ReleaseBinding(finalizeContext, binding.ID, "recommission")
	if err != nil {
		return RecommissionResult{}, err
	}
	return RecommissionResult{Binding: released, Receipt: receipt}, nil
}

func (service *Service) bindingForToken(ctx context.Context, internalAppID, token string) (Binding, error) {
	internalAppID = strings.TrimSpace(internalAppID)
	token = strings.TrimSpace(token)
	if service.repository == nil || internalAppID == "" || token == "" {
		return Binding{}, ErrForbidden
	}
	binding, err := service.repository.FindBindingByToken(ctx, HashToken(token))
	if err != nil {
		return Binding{}, err
	}
	if !binding.Active || binding.InternalAppID != internalAppID {
		return Binding{}, ErrForbidden
	}
	return binding, nil
}

func HashToken(token string) [sha256.Size]byte { return sha256.Sum256([]byte(token)) }

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
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
