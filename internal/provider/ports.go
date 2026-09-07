package provider

import (
	"context"
	"time"
)

// DeviceMessage is the provider-neutral product protocol envelope. Provider
// adapters own topic namespaces and transport-specific headers.
type DeviceMessage struct {
	Topic          string
	Payload        []byte
	RequestID      string
	IdempotencyKey string
	ExpiresAt      time.Time
}

type DeviceSubscription struct {
	TopicFilter string
	Handler     func(context.Context, DeviceMessage) error
}

type SubscriptionHandle interface {
	Close() error
}

type DeviceMessaging interface {
	Publish(context.Context, DeviceMessage) error
	Subscribe(context.Context, DeviceSubscription) (SubscriptionHandle, error)
	Health(context.Context) error
}

type IdentityRequest struct {
	DeviceID string
	PID      string
	CSRPEM   string
}

type IdentityRef struct {
	ProviderInstance string
	ExternalID       string
	CertificateID    string
	CertificatePEM   string
	Endpoint         string
}

type DeviceIdentity interface {
	Prepare(context.Context, IdentityRequest) (IdentityRef, error)
	Activate(context.Context, IdentityRef) error
	Rotate(context.Context, IdentityRef) (IdentityRef, error)
	Revoke(context.Context, IdentityRef, string) error
	Health(context.Context) error
}

type ICEServer struct {
	URLs       any    `json:"urls"`
	Username   string `json:"username,omitempty"`
	Credential string `json:"credential,omitempty"`
}

// RealtimeChannel and RealtimePlayback keep provider-owned resource identity
// opaque to the media domain. The AWS adapter may put an ARN in ExternalRef;
// a domestic adapter can use a channel ID or session resource instead.
type RealtimeChannel struct {
	Name        string
	ExternalRef string
}

type RealtimePlayback struct {
	Provider    string
	ChannelName string
	ExternalRef string
	Region      string
	ClientID    string
	WSSURL      string
	ICEServers  []ICEServer
	ExpiresAt   time.Time
}

type PlaybackProvider interface {
	EnsureChannel(context.Context, string) (RealtimeChannel, error)
	Playback(context.Context, RealtimeChannel, string, time.Duration) (RealtimePlayback, error)
}

type RealtimeSessionRequest struct {
	DeviceID          string
	BindingID         string
	RequestID         string
	RequestedProtocol string
	Capabilities      map[string]any
	ExpiresAt         time.Time
}

type RealtimeSessionRef struct {
	ID               string
	ProviderInstance string
}

type RealtimeSessionDescriptor struct {
	SessionID        string         `json:"session_id" yaml:"session_id"`
	ProviderInstance string         `json:"provider_instance" yaml:"provider_instance"`
	Protocol         string         `json:"protocol" yaml:"protocol"`
	ClientConfig     map[string]any `json:"client_config" yaml:"client_config"`
	Capabilities     map[string]any `json:"capabilities" yaml:"capabilities"`
	ExpiresAt        time.Time      `json:"expires_at" yaml:"expires_at"`
}

type RealtimeCapabilities struct {
	Protocols    []string `json:"protocols"`
	DataChannel  bool     `json:"data_channel"`
	AudioSend    bool     `json:"audio_send"`
	AudioReceive bool     `json:"audio_receive"`
}

type RealtimeProvider interface {
	PrepareSession(context.Context, RealtimeSessionRequest) (RealtimeSessionDescriptor, error)
	CloseSession(context.Context, RealtimeSessionRef) error
	Capabilities() RealtimeCapabilities
	Health(context.Context) error
}

// RealtimeSessionRenewer is optional: providers without renewable sessions
// (including the independent KVS playback adapter) keep their own lifecycle.
type RealtimeSessionRenewer interface {
	RenewSession(context.Context, RealtimeSessionRef, time.Time) (time.Time, error)
}

// ObjectStore is the provider-neutral media object boundary. Credentials,
// bucket addressing and presigned URL formats remain owned by the selected
// adapter; media services only use opaque object keys and short-lived URLs.
type ObjectStore interface {
	PutURL(context.Context, string, string, time.Duration) (string, error)
	GetURL(context.Context, string, time.Duration) (string, error)
	Upload(context.Context, string, string, []byte) error
	Download(context.Context, string, int64) ([]byte, error)
	Exists(context.Context, string) (bool, error)
}

type QueueMessage struct {
	ID         string
	Body       []byte
	LeaseToken string
}

type EventQueue interface {
	Receive(context.Context) ([]QueueMessage, error)
	Ack(context.Context, QueueMessage) error
	Health(context.Context) error
}

type Signer interface {
	Sign(context.Context, []byte) ([]byte, error)
	Health(context.Context) error
}
