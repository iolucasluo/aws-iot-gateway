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
	ErrInvalidInput   = errors.New("invalid media input")
	ErrStreamNotFound = errors.New("stream not found")
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

type StreamResult struct {
	StreamID   string             `json:"stream_id"`
	Provider   string             `json:"provider"`
	Signaling  Signaling          `json:"signaling"`
	ICEServers []awskvs.ICEServer `json:"ice_servers"`
	ExpiresAt  time.Time          `json:"expires_at"`
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

func (service *StreamService) Create(ctx context.Context, internalAppID, deviceID, bindingID, deviceSN string) (StreamResult, error) {
	if service == nil || service.kvs == nil || service.commander == nil || service.repository == nil || service.newID == nil ||
		strings.TrimSpace(internalAppID) == "" || strings.TrimSpace(deviceID) == "" || strings.TrimSpace(bindingID) == "" || strings.TrimSpace(deviceSN) == "" {
		return StreamResult{}, ErrInvalidInput
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
		"requested_capabilities": map[string]any{
			"video_receive": true, "audio_receive": false, "audio_send": false, "data_channels": []string{},
		},
	})
	if _, err := service.commander.SendCommands(ctx, internalAppID, deviceID, deviceSN,
		[]device.CommandInput{{Code: "stream", Value: value}}, true); err != nil {
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
		Signaling:  Signaling{WSSURL: playback.WSSURL, ExpiresAt: playback.ExpiresAt},
		ICEServers: playback.ICEServers, ExpiresAt: playback.ExpiresAt,
	}, nil
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
