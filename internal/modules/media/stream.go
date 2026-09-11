package media

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

const streamTTL = 5 * time.Minute

var (
	ErrInvalidInput                = errors.New("invalid media input")
	ErrInvalidCapabilities         = errors.New("invalid stream capabilities")
	ErrStreamRejected              = errors.New("stream start rejected")
	ErrStreamCapabilityUnavailable = errors.New("stream capability unavailable")
	ErrStreamNotFound              = errors.New("stream not found")
	ErrStreamLeaseUnsupported      = errors.New("stream lease renewal unsupported")
)

const (
	CapabilityContractVersion  = "webrtc.v1"
	ControlDataChannelProtocol = "explorex.control.v1"
)

// Provider-neutral aliases keep the narrow media port close to its consumer
// while sharing the canonical capability types with other adapters.
type ICEServer = provider.ICEServer
type RealtimeChannel = provider.RealtimeChannel
type RealtimePlayback = provider.RealtimePlayback
type PlaybackProvider = provider.PlaybackProvider

// KVS is retained as a source-compatible name for existing constructors while
// its method types are provider-neutral. New code should use PlaybackProvider.
type KVS = PlaybackProvider

type Commander interface {
	SendCommands(context.Context, string, string, string, []device.CommandInput, bool) (device.SendCommandsResult, error)
}

// DPCommander is the profile-contract-aware command entry point. Typed
// profile-aware DP services use it so their payloads cannot bypass DP profile
// validation, while workers that speak internal protocols keep Commander.
type DPCommander interface {
	SendDPCommands(context.Context, string, string, string, []device.CommandInput, bool) (device.SendCommandsResult, error)
}

type Stream struct {
	ID         string
	DeviceID   string
	DeviceSN   string
	BindingID  string
	ChannelARN string
	Status     string
	ExpiresAt  time.Time
}

type StreamRepository interface {
	Create(context.Context, Stream) error
	Find(context.Context, string, string, string) (bool, error)
	Close(context.Context, string, string, string) (bool, error)
}

type StreamLeaseRepository interface {
	Renew(context.Context, string, string, string, time.Time) (time.Time, error)
}

type StreamLease struct {
	ExpiresAt time.Time `json:"expires_at"`
}

type Signaling struct {
	WSSURL    string    `json:"wss_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// StreamCapabilities are always expressed from the Viewer/App perspective.
type StreamCapabilities struct {
	VideoReceive bool     `json:"video_receive"`
	AudioReceive bool     `json:"audio_receive"`
	AudioSend    bool     `json:"audio_send"`
	DataChannels []string `json:"data_channels"`
}

func (capabilities StreamCapabilities) empty() bool {
	return !capabilities.VideoReceive && !capabilities.AudioReceive && !capabilities.AudioSend && len(capabilities.DataChannels) == 0
}

type PlaybackControlCapability struct {
	Contract              string   `json:"contract"`
	Ops                   []string `json:"ops"`
	ProgressIntervalMS    int      `json:"progress_interval_ms"`
	MaxConcurrentSessions int      `json:"max_concurrent_sessions"`
	SupportsRate          bool     `json:"supports_rate"`
}

type StreamResult struct {
	StreamID                  string                     `json:"stream_id"`
	Provider                  string                     `json:"provider"`
	Protocol                  string                     `json:"protocol,omitempty"`
	ClientConfig              map[string]any             `json:"client_config,omitempty"`
	CapabilityContractVersion string                     `json:"capability_contract_version"`
	FirmwareVersion           string                     `json:"firmware_version"`
	SupportedCapabilities     StreamCapabilities         `json:"supported_capabilities"`
	GrantedCapabilities       StreamCapabilities         `json:"granted_capabilities"`
	PlaybackControl           *PlaybackControlCapability `json:"playback_control,omitempty"`
	Signaling                 Signaling                  `json:"signaling"`
	ICEServers                []ICEServer                `json:"ice_servers"`
	ExpiresAt                 time.Time                  `json:"expires_at"`
	SessionLease              *StreamLease               `json:"session_lease,omitempty"`
}

type StreamService struct {
	kvs              KVS
	realtime         provider.RealtimeProvider
	realtimeInstance string
	commander        DPCommander
	repository       StreamRepository
	channelPrefix    string
	newID            func() (string, error)
	now              func() time.Time
	logger           *log.Logger
}

func NewStreamService(kvs KVS, commander DPCommander, repository StreamRepository, channelPrefix string, newID func() (string, error)) *StreamService {
	if newID == nil {
		newID = randomUUID
	}
	return &StreamService{kvs: kvs, commander: commander, repository: repository, channelPrefix: channelPrefix, newID: newID, now: time.Now}
}

func NewRealtimeStreamService(realtime provider.RealtimeProvider, providerInstance string, commander DPCommander, repository StreamRepository, newID func() (string, error), logger *log.Logger) *StreamService {
	if newID == nil {
		newID = randomUUID
	}
	return &StreamService{realtime: realtime, realtimeInstance: strings.TrimSpace(providerInstance), commander: commander, repository: repository, newID: newID, now: time.Now, logger: logger}
}

func (service *StreamService) Create(
	ctx context.Context,
	internalAppID, deviceID, bindingID, deviceSN string,
	requested StreamCapabilities,
) (StreamResult, error) {
	return service.create(ctx, internalAppID, deviceID, bindingID, deviceSN, requested, false)
}

// CreateExact is for legacy viewers that cannot consume a granted-capability
// subset. A partial grant is revoked before it can be returned as a usable stream.
func (service *StreamService) CreateExact(
	ctx context.Context,
	internalAppID, deviceID, bindingID, deviceSN string,
	requested StreamCapabilities,
) (StreamResult, error) {
	return service.create(ctx, internalAppID, deviceID, bindingID, deviceSN, requested, true)
}

func (service *StreamService) create(
	ctx context.Context,
	internalAppID, deviceID, bindingID, deviceSN string,
	requested StreamCapabilities,
	requireExactGrant bool,
) (StreamResult, error) {
	if service == nil || (service.kvs == nil && service.realtime == nil) || service.commander == nil || service.repository == nil || service.newID == nil ||
		strings.TrimSpace(internalAppID) == "" || strings.TrimSpace(deviceID) == "" || strings.TrimSpace(bindingID) == "" || strings.TrimSpace(deviceSN) == "" {
		return StreamResult{}, ErrInvalidInput
	}
	if err := validateRequestedCapabilities(requested); err != nil {
		return StreamResult{}, err
	}
	requestID, err := service.newID()
	if err != nil || strings.TrimSpace(requestID) == "" {
		return StreamResult{}, fmt.Errorf("generate realtime viewer id: %w", err)
	}
	viewerID := requestID
	var providerName, protocol, signalingURL, channelRef string
	var iceServers []ICEServer
	var expiresAt time.Time
	var realtimeSessionID string
	var realtimeSession provider.RealtimeSessionDescriptor
	if service.realtime != nil {
		expiresAt = service.now().UTC().Add(streamTTL)
		capabilities, err := capabilitiesMap(requested)
		if err != nil {
			return StreamResult{}, err
		}
		realtimeSession, err = service.realtime.PrepareSession(ctx, provider.RealtimeSessionRequest{
			DeviceID: deviceID, BindingID: bindingID, RequestID: requestID,
			RequestedProtocol: "explorex-signal-v1", Capabilities: capabilities, ExpiresAt: expiresAt,
		})
		if err != nil {
			return StreamResult{}, err
		}
		realtimeSessionID = realtimeSession.SessionID
		providerName, protocol = realtimeSession.ProviderInstance, realtimeSession.Protocol
		signalingURL = stringValue(realtimeSession.ClientConfig["wss_url"])
		if signalingURL == "" || realtimeSessionID == "" || protocol == "" || providerName == "" {
			_ = service.realtime.CloseSession(ctx, provider.RealtimeSessionRef{ID: realtimeSessionID, ProviderInstance: providerName})
			return StreamResult{}, ErrStreamRejected
		}
		// The provider session is the durable stream identity. The request ID
		// remains only the idempotency key used during PrepareSession.
		viewerID = realtimeSessionID
		iceServers, err = parseICEServers(realtimeSession.ClientConfig["ice_servers"])
		if err != nil {
			_ = service.realtime.CloseSession(ctx, provider.RealtimeSessionRef{ID: realtimeSessionID, ProviderInstance: providerName})
			return StreamResult{}, ErrStreamRejected
		}
		channelRef = realtimeSessionID
	} else {
		channel, err := service.kvs.EnsureChannel(ctx, service.channelPrefix+deviceSN)
		if err != nil {
			return StreamResult{}, err
		}
		playback, err := service.kvs.Playback(ctx, channel, viewerID, streamTTL)
		if err != nil {
			return StreamResult{}, err
		}
		providerName, signalingURL, iceServers, expiresAt, channelRef = playback.Provider, playback.WSSURL, playback.ICEServers, playback.ExpiresAt, channel.ExternalRef
	}
	valuePayload := map[string]any{
		"type": "webrtc", "start": 1, "viewer_client_id": viewerID,
		"requested_capabilities": requested,
	}
	if service.realtime != nil {
		deviceConfig, ok := realtimeSession.ClientConfig["device_config"].(map[string]any)
		if !ok || strings.TrimSpace(stringValue(deviceConfig["wss_url"])) == "" {
			_ = service.realtime.CloseSession(ctx, provider.RealtimeSessionRef{ID: realtimeSessionID, ProviderInstance: providerName})
			return StreamResult{}, ErrStreamRejected
		}
		valuePayload["realtime"] = map[string]any{
			"session_id": realtimeSessionID, "provider_instance": providerName,
			"protocol": protocol, "device_config": deviceConfig,
		}
	}
	value, _ := json.Marshal(valuePayload)
	commandResult, err := service.commander.SendDPCommands(ctx, internalAppID, deviceID, deviceSN,
		[]device.CommandInput{{Code: "stream", Value: value}}, true)
	if err != nil {
		if service.realtime != nil {
			_ = service.realtime.CloseSession(ctx, provider.RealtimeSessionRef{ID: realtimeSessionID, ProviderInstance: providerName})
		}
		return StreamResult{}, err
	}
	capabilityFacts, err := validateCapabilityACK(commandResult, viewerID, requested)
	if err != nil {
		if service.realtime != nil {
			_ = service.realtime.CloseSession(ctx, provider.RealtimeSessionRef{ID: realtimeSessionID, ProviderInstance: providerName})
		}
		return StreamResult{}, err
	}
	if requireExactGrant && !capabilitiesEqual(capabilityFacts.accepted, requested) {
		stopValue, _ := json.Marshal(map[string]any{"type": "webrtc", "start": 0, "viewer_client_id": viewerID})
		_, _ = service.commander.SendDPCommands(ctx, internalAppID, deviceID, deviceSN,
			[]device.CommandInput{{Code: "stream", Value: stopValue}}, false)
		if service.realtime != nil {
			_ = service.realtime.CloseSession(ctx, provider.RealtimeSessionRef{ID: realtimeSessionID, ProviderInstance: providerName})
		}
		return StreamResult{}, ErrStreamCapabilityUnavailable
	}
	stream := Stream{
		ID: viewerID, DeviceID: deviceID, DeviceSN: deviceSN, BindingID: bindingID,
		ChannelARN: channelRef, Status: "ready", ExpiresAt: expiresAt,
	}
	if err := service.repository.Create(ctx, stream); err != nil {
		if service.realtime != nil {
			_ = service.realtime.CloseSession(ctx, provider.RealtimeSessionRef{ID: realtimeSessionID, ProviderInstance: providerName})
		}
		return StreamResult{}, err
	}
	var lease *StreamLease
	if _, ok := service.realtime.(provider.RealtimeSessionRenewer); ok {
		if _, ok := service.repository.(StreamLeaseRepository); ok {
			lease = &StreamLease{ExpiresAt: realtimeSession.ExpiresAt}
		}
	}
	return StreamResult{
		SessionLease: lease,
		StreamID:     viewerID, Provider: providerName, Protocol: protocol,
		ClientConfig:              viewerClientConfig(realtimeSession.ClientConfig),
		CapabilityContractVersion: CapabilityContractVersion,
		FirmwareVersion:           capabilityFacts.firmwareVersion,
		SupportedCapabilities:     capabilityFacts.supported,
		GrantedCapabilities:       capabilityFacts.accepted,
		PlaybackControl:           capabilityFacts.playbackControl,
		Signaling:                 Signaling{WSSURL: signalingURL, ExpiresAt: expiresAt},
		ICEServers:                iceServers,
		ExpiresAt:                 expiresAt,
	}, nil
}

// Renew is reached only after the HTTP boundary re-authorizes the device token.
// Ownership is checked again before recording the provider's bounded deadline.
func (service *StreamService) Renew(ctx context.Context, bindingID, deviceSN, streamID string) (StreamLease, error) {
	if service == nil || strings.TrimSpace(streamID) == "" || strings.TrimSpace(bindingID) == "" || strings.TrimSpace(deviceSN) == "" {
		return StreamLease{}, ErrInvalidInput
	}
	renewer, ok := service.realtime.(provider.RealtimeSessionRenewer)
	repository, repositoryOK := service.repository.(StreamLeaseRepository)
	if !ok || !repositoryOK || service.realtimeInstance == "" {
		return StreamLease{}, ErrStreamLeaseUnsupported
	}
	found, err := service.repository.Find(ctx, bindingID, deviceSN, streamID)
	if err != nil {
		service.logf("renew_failed stream=%s binding=%s device=%s reason=%v provider_session=kept", streamID, bindingID, deviceSN, err)
		return StreamLease{}, err
	}
	if !found {
		service.logf("renew_failed stream=%s binding=%s device=%s reason=%v provider_session=kept", streamID, bindingID, deviceSN, ErrStreamNotFound)
		return StreamLease{}, ErrStreamNotFound
	}
	ref := provider.RealtimeSessionRef{ID: streamID, ProviderInstance: service.realtimeInstance}
	deadline := service.now().UTC().Add(streamTTL)
	expiresAt, err := renewer.RenewSession(ctx, ref, deadline)
	if err != nil {
		// The provider lease is left intact for an upstream renewal failure: the
		// session may still be alive and a retry could succeed before its expiry.
		service.logf("renew_failed stream=%s binding=%s device=%s reason=%v provider_session=kept", streamID, bindingID, deviceSN, err)
		return StreamLease{}, err
	}
	if !expiresAt.After(service.now().UTC()) || expiresAt.After(deadline) {
		err = ErrStreamRejected
	} else {
		expiresAt, err = repository.Renew(ctx, bindingID, deviceSN, streamID, expiresAt)
	}
	if err != nil {
		// Bound cleanup separately: a cancelled HTTP request must not prevent it.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = service.realtime.CloseSession(cleanup, ref)
		service.logf("renew_failed stream=%s binding=%s device=%s reason=%v provider_session=closed", streamID, bindingID, deviceSN, err)
		return StreamLease{}, err
	}
	service.logf("renew_ok stream=%s binding=%s device=%s expires_at=%s", streamID, bindingID, deviceSN, expiresAt.UTC().Format(time.RFC3339Nano))
	return StreamLease{ExpiresAt: expiresAt}, nil
}

func (service *StreamService) logf(format string, args ...any) {
	if service == nil || service.logger == nil {
		return
	}
	service.logger.Printf(format, args...)
}

func viewerClientConfig(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	result := make(map[string]any, len(config))
	for key, value := range config {
		switch key {
		case "device_config", "device_wss_url", "ticket_expires_at":
			continue
		default:
			result[key] = value
		}
	}
	return result
}

func capabilitiesMap(requested StreamCapabilities) (map[string]any, error) {
	encoded, err := json.Marshal(requested)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func parseICEServers(value any) ([]ICEServer, error) {
	if value == nil {
		// A provider may legitimately have no relay/STUN entries on an
		// isolated LAN. Keep the wire shape JSON [] (rather than null) so
		// consumers can distinguish an empty candidate list from a malformed
		// response and choose their own provider-neutral fallback policy.
		return []ICEServer{}, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var servers []ICEServer
	if err := json.Unmarshal(encoded, &servers); err != nil {
		return nil, err
	}
	return servers, nil
}

func validateRequestedCapabilities(capabilities StreamCapabilities) error {
	if capabilities.empty() {
		return ErrInvalidCapabilities
	}
	seen := make(map[string]struct{}, len(capabilities.DataChannels))
	for _, protocol := range capabilities.DataChannels {
		if protocol != ControlDataChannelProtocol {
			return ErrInvalidCapabilities
		}
		if _, duplicate := seen[protocol]; duplicate {
			return ErrInvalidCapabilities
		}
		seen[protocol] = struct{}{}
	}
	return nil
}

type capabilityFacts struct {
	firmwareVersion string
	supported       StreamCapabilities
	accepted        StreamCapabilities
	playbackControl *PlaybackControlCapability
}

func validateCapabilityACK(result device.SendCommandsResult, viewerID string, requested StreamCapabilities) (capabilityFacts, error) {
	if len(result.Results) != 1 || result.Results[0].Status != "success" {
		return capabilityFacts{}, ErrStreamRejected
	}
	ack := result.Results[0].Data
	if ack == nil || strings.TrimSpace(stringValue(ack["viewer_client_id"])) != viewerID ||
		stringValue(ack["capability_contract_version"]) != CapabilityContractVersion ||
		stringValue(ack["action"]) != "stream" || stringValue(ack["type"]) != "webrtc" ||
		!numericValueEquals(ack["code"], 0) || !numericValueEquals(ack["start"], 1) {
		return capabilityFacts{}, ErrStreamRejected
	}
	firmwareVersion := strings.TrimSpace(stringValue(ack["firmware_version"]))
	if firmwareVersion == "" {
		return capabilityFacts{}, ErrStreamRejected
	}
	supported, err := parseCapabilities(ack["supported"])
	if err != nil {
		return capabilityFacts{}, ErrStreamRejected
	}
	accepted, err := parseCapabilities(ack["accepted"])
	if err != nil || !capabilitiesSubset(accepted, requested) || !capabilitiesSubset(accepted, supported) {
		return capabilityFacts{}, ErrStreamRejected
	}
	status := strings.TrimSpace(stringValue(ack["status"]))
	if accepted.empty() {
		if status == "unavailable" {
			return capabilityFacts{}, ErrStreamCapabilityUnavailable
		}
		return capabilityFacts{}, ErrStreamRejected
	}
	if status != "ready" {
		return capabilityFacts{}, ErrStreamRejected
	}
	playbackControl, err := parsePlaybackControl(ack["playback_control"], accepted)
	if err != nil {
		return capabilityFacts{}, ErrStreamRejected
	}
	return capabilityFacts{
		firmwareVersion: firmwareVersion, supported: supported, accepted: accepted,
		playbackControl: playbackControl,
	}, nil
}

func parsePlaybackControl(value any, accepted StreamCapabilities) (*PlaybackControlCapability, error) {
	if value == nil {
		return nil, nil
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: playback_control must be an object", ErrStreamRejected)
	}
	if len(fields) != 5 {
		return nil, fmt.Errorf("%w: playback_control must carry exactly 5 fields (got %d)",
			ErrStreamRejected, len(fields))
	}
	if !accepted.VideoReceive {
		return nil, fmt.Errorf("%w: playback_control requires accepted video_receive",
			ErrStreamRejected)
	}
	if !containsString(accepted.DataChannels, ControlDataChannelProtocol) {
		return nil, fmt.Errorf("%w: playback_control requires the %s data_channel",
			ErrStreamRejected, ControlDataChannelProtocol)
	}
	contract, contractOK := fields["contract"].(string)
	if !contractOK || contract != "sd-playback.v1" {
		return nil, fmt.Errorf("%w: playback_control contract must be %q", ErrStreamRejected,
			"sd-playback.v1")
	}
	rawOps, opsOK := fields["ops"].([]any)
	if !opsOK {
		return nil, fmt.Errorf("%w: playback_control ops must be a list", ErrStreamRejected)
	}
	progress, progressOK := exactInt(fields["progress_interval_ms"])
	if !progressOK || progress != 500 {
		return nil, fmt.Errorf("%w: playback_control progress_interval_ms must be 500",
			ErrStreamRejected)
	}
	maximum, maximumOK := exactInt(fields["max_concurrent_sessions"])
	if !maximumOK || maximum != 1 {
		return nil, fmt.Errorf("%w: playback_control max_concurrent_sessions must be 1",
			ErrStreamRejected)
	}
	supportsRate, rateOK := fields["supports_rate"].(bool)
	if !rateOK || supportsRate {
		return nil, fmt.Errorf("%w: playback_control supports_rate must be false", ErrStreamRejected)
	}
	expected := []string{"load", "play", "pause", "resume", "seek", "skip", "stop", "get_state"}
	if len(rawOps) != len(expected) {
		return nil, fmt.Errorf("%w: playback_control ops must list %d operations (got %d)",
			ErrStreamRejected, len(expected), len(rawOps))
	}
	ops := make([]string, len(expected))
	for index := range expected {
		operation, ok := rawOps[index].(string)
		if !ok || operation != expected[index] {
			return nil, fmt.Errorf("%w: playback_control ops[%d] must be %q", ErrStreamRejected,
				index, expected[index])
		}
		ops[index] = operation
	}
	return &PlaybackControlCapability{
		Contract: contract, Ops: ops, ProgressIntervalMS: progress,
		MaxConcurrentSessions: maximum, SupportsRate: supportsRate,
	}, nil
}

func exactInt(value any) (int, bool) {
	switch number := value.(type) {
	case float64:
		integer := int(number)
		return integer, float64(integer) == number
	case int:
		return number, true
	case int64:
		return int(number), int64(int(number)) == number
	case json.Number:
		parsed, err := number.Int64()
		return int(parsed), err == nil && int64(int(parsed)) == parsed
	default:
		return 0, false
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func parseCapabilities(value any) (StreamCapabilities, error) {
	fields, ok := value.(map[string]any)
	if !ok || len(fields) != 4 {
		return StreamCapabilities{}, ErrStreamRejected
	}
	videoReceive, videoOK := fields["video_receive"].(bool)
	audioReceive, audioReceiveOK := fields["audio_receive"].(bool)
	audioSend, audioSendOK := fields["audio_send"].(bool)
	if !videoOK || !audioReceiveOK || !audioSendOK {
		return StreamCapabilities{}, ErrStreamRejected
	}
	dataChannels, err := parseDataChannels(fields["data_channels"])
	if err != nil {
		return StreamCapabilities{}, err
	}
	return StreamCapabilities{
		VideoReceive: videoReceive, AudioReceive: audioReceive, AudioSend: audioSend, DataChannels: dataChannels,
	}, nil
}

func parseDataChannels(value any) ([]string, error) {
	var items []any
	switch channels := value.(type) {
	case []any:
		items = channels
	case []string:
		items = make([]any, len(channels))
		for index := range channels {
			items[index] = channels[index]
		}
	default:
		return nil, ErrStreamRejected
	}
	result := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		protocol, ok := item.(string)
		if !ok || protocol != ControlDataChannelProtocol {
			return nil, ErrStreamRejected
		}
		if _, duplicate := seen[protocol]; duplicate {
			return nil, ErrStreamRejected
		}
		seen[protocol] = struct{}{}
		result = append(result, protocol)
	}
	return result, nil
}

func capabilitiesSubset(subset, superset StreamCapabilities) bool {
	if subset.VideoReceive && !superset.VideoReceive || subset.AudioReceive && !superset.AudioReceive || subset.AudioSend && !superset.AudioSend {
		return false
	}
	available := make(map[string]struct{}, len(superset.DataChannels))
	for _, protocol := range superset.DataChannels {
		available[protocol] = struct{}{}
	}
	for _, protocol := range subset.DataChannels {
		if _, found := available[protocol]; !found {
			return false
		}
	}
	return true
}

func capabilitiesEqual(left, right StreamCapabilities) bool {
	return capabilitiesSubset(left, right) && capabilitiesSubset(right, left)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func numericValueEquals(value any, expected int64) bool {
	switch number := value.(type) {
	case float64:
		return int64(number) == expected
	case int:
		return int64(number) == expected
	case int64:
		return number == expected
	case json.Number:
		parsed, err := number.Int64()
		return err == nil && parsed == expected
	default:
		return false
	}
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

func (service *StreamService) Delete(ctx context.Context, internalAppID, deviceID, bindingID, deviceSN, streamID string) error {
	if service == nil || strings.TrimSpace(streamID) == "" {
		return ErrInvalidInput
	}
	found, err := service.repository.Find(ctx, bindingID, deviceSN, streamID)
	if err != nil {
		return err
	}
	if !found {
		return ErrStreamNotFound
	}
	if service.realtime != nil {
		if service.realtimeInstance == "" {
			return ErrInvalidInput
		}
		if err := service.realtime.CloseSession(ctx, provider.RealtimeSessionRef{ID: streamID, ProviderInstance: service.realtimeInstance}); err != nil {
			return err
		}
	}
	value, _ := json.Marshal(map[string]any{"type": "webrtc", "start": 0, "viewer_client_id": streamID})
	if _, err := service.commander.SendDPCommands(ctx, internalAppID, deviceID, deviceSN,
		[]device.CommandInput{{Code: "stream", Value: value}}, false); err != nil {
		return err
	}
	closed, err := service.repository.Close(ctx, bindingID, deviceSN, streamID)
	if err != nil {
		return err
	}
	if !closed {
		return ErrStreamNotFound
	}
	return nil
}
