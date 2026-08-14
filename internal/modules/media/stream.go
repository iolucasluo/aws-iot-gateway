package media

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/awskvs"
)

const streamTTL = 5 * time.Minute

var (
	ErrInvalidInput                = errors.New("invalid media input")
	ErrInvalidCapabilities         = errors.New("invalid stream capabilities")
	ErrStreamRejected              = errors.New("stream start rejected")
	ErrStreamCapabilityUnavailable = errors.New("stream capability unavailable")
	ErrStreamNotFound              = errors.New("stream not found")
)

const (
	CapabilityContractVersion  = "webrtc.v1"
	ControlDataChannelProtocol = "explorex.control.v1"
)

type KVS interface {
	EnsureChannel(context.Context, string) (awskvs.Channel, error)
	Playback(context.Context, awskvs.Channel, string, time.Duration) (awskvs.PlaybackConfig, error)
}

type Commander interface {
	SendCommands(context.Context, string, string, string, []device.CommandInput, bool) (device.SendCommandsResult, error)
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

type StreamResult struct {
	StreamID                  string             `json:"stream_id"`
	Provider                  string             `json:"provider"`
	CapabilityContractVersion string             `json:"capability_contract_version"`
	FirmwareVersion           string             `json:"firmware_version"`
	SupportedCapabilities     StreamCapabilities `json:"supported_capabilities"`
	GrantedCapabilities       StreamCapabilities `json:"granted_capabilities"`
	Signaling                 Signaling          `json:"signaling"`
	ICEServers                []awskvs.ICEServer `json:"ice_servers"`
	ExpiresAt                 time.Time          `json:"expires_at"`
}

type StreamService struct {
	kvs           KVS
	commander     Commander
	repository    StreamRepository
	channelPrefix string
	newID         func() (string, error)
}

func NewStreamService(kvs KVS, commander Commander, repository StreamRepository, channelPrefix string, newID func() (string, error)) *StreamService {
	if newID == nil {
		newID = randomUUID
	}
	return &StreamService{kvs: kvs, commander: commander, repository: repository, channelPrefix: channelPrefix, newID: newID}
}

func (service *StreamService) Create(
	ctx context.Context,
	internalAppID, deviceID, bindingID, deviceSN string,
	requested StreamCapabilities,
) (StreamResult, error) {
	if service == nil || service.kvs == nil || service.commander == nil || service.repository == nil || service.newID == nil ||
		strings.TrimSpace(internalAppID) == "" || strings.TrimSpace(deviceID) == "" || strings.TrimSpace(bindingID) == "" || strings.TrimSpace(deviceSN) == "" {
		return StreamResult{}, ErrInvalidInput
	}
	if err := validateRequestedCapabilities(requested); err != nil {
		return StreamResult{}, err
	}
	viewerID, err := service.newID()
	if err != nil || strings.TrimSpace(viewerID) == "" {
		return StreamResult{}, fmt.Errorf("generate KVS viewer id: %w", err)
	}
	channel, err := service.kvs.EnsureChannel(ctx, service.channelPrefix+deviceSN)
	if err != nil {
		return StreamResult{}, err
	}
	playback, err := service.kvs.Playback(ctx, channel, viewerID, streamTTL)
	if err != nil {
		return StreamResult{}, err
	}
	value, _ := json.Marshal(map[string]any{
		"type": "webrtc", "start": 1, "viewer_client_id": viewerID,
		"requested_capabilities": requested,
	})
	commandResult, err := service.commander.SendCommands(ctx, internalAppID, deviceID, deviceSN,
		[]device.CommandInput{{Code: "stream", Value: value}}, true)
	if err != nil {
		return StreamResult{}, err
	}
	capabilityFacts, err := validateCapabilityACK(commandResult, viewerID, requested)
	if err != nil {
		return StreamResult{}, err
	}
	stream := Stream{
		ID: viewerID, DeviceID: deviceID, DeviceSN: deviceSN, BindingID: bindingID,
		ChannelARN: channel.ARN, Status: "ready", ExpiresAt: playback.ExpiresAt,
	}
	if err := service.repository.Create(ctx, stream); err != nil {
		return StreamResult{}, err
	}
	return StreamResult{
		StreamID: viewerID, Provider: playback.Provider,
		CapabilityContractVersion: CapabilityContractVersion,
		FirmwareVersion:           capabilityFacts.firmwareVersion,
		SupportedCapabilities:     capabilityFacts.supported,
		GrantedCapabilities:       capabilityFacts.accepted,
		Signaling:                 Signaling{WSSURL: playback.WSSURL, ExpiresAt: playback.ExpiresAt},
		ICEServers:                playback.ICEServers,
		ExpiresAt:                 playback.ExpiresAt,
	}, nil
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
	return capabilityFacts{firmwareVersion: firmwareVersion, supported: supported, accepted: accepted}, nil
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
	value, _ := json.Marshal(map[string]any{"type": "webrtc", "start": 0, "viewer_client_id": streamID})
	if _, err := service.commander.SendCommands(ctx, internalAppID, deviceID, deviceSN,
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
