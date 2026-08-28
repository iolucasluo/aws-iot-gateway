package device

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/ota"
)

const commandACKTimeout = 10 * time.Second

var (
	ErrInvalidInput          = errors.New("invalid device operation input")
	ErrACKTimeout            = errors.New("device ack timeout")
	ErrDeviceOffline         = errors.New("device offline")
	ErrDeviceBusy            = errors.New("device busy")
	ErrDeviceJammed          = errors.New("device jammed")
	ErrPublishFailed         = errors.New("device command publish failed")
	ErrInvalidDeviceResponse = errors.New("invalid device response")
	topicPrefixRegexp        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
)

type CommandInput struct {
	Code         string          `json:"code"`
	Value        json.RawMessage `json:"value"`
	CommandID    string          `json:"command_id,omitempty"`
	RedactFields []string        `json:"-"`
}

type Command struct {
	ID            string
	DeviceID      string
	InternalAppID string
	SessionID     string
	RequestID     string
	Payload       map[string]any
	Status        string
	ACK           map[string]any
	ErrorCode     string
	CreatedAt     time.Time
}

type CommandResult struct {
	Index     int            `json:"index"`
	Code      string         `json:"code"`
	Status    string         `json:"status"`
	CommandID string         `json:"command_id"`
	Data      map[string]any `json:"data,omitempty"`
	Reason    string         `json:"reason,omitempty"`
}

type SendCommandsResult struct {
	Results []CommandResult `json:"results"`
}

type PTZStatus struct {
	Pan            int64   `json:"pan"`
	Tilt           int64   `json:"tilt"`
	LimitH         int64   `json:"limit_h"`
	LimitV         int64   `json:"limit_v"`
	PanNormalized  float64 `json:"pan_normalized"`
	TiltNormalized float64 `json:"tilt_normalized"`
	Moving         bool    `json:"moving"`
}

type Publisher interface {
	Publish(context.Context, string, []byte) error
}

type ShadowReader interface {
	GetShadow(context.Context, string) ([]byte, error)
}

type ACKWaiter interface {
	Wait(context.Context, string, string, time.Duration) (map[string]any, error)
}

type CommandRepository interface {
	Create(context.Context, Command) error
	Complete(context.Context, string, string, map[string]any, string) error
}

type IDGenerator func() (string, error)

type Service struct {
	publisher   Publisher
	ackWaiter   ACKWaiter
	repository  CommandRepository
	topicPrefix string
	newID       IDGenerator
}

func NewService(publisher Publisher, ackWaiter ACKWaiter, repository CommandRepository, topicPrefix string, newID IDGenerator) *Service {
	if newID == nil {
		newID = randomUUID
	}
	return &Service{publisher: publisher, ackWaiter: ackWaiter, repository: repository, topicPrefix: topicPrefix, newID: newID}
}

func (service *Service) GetShadow(ctx context.Context, deviceSN string) (any, error) {
	reader, ok := service.publisher.(ShadowReader)
	if !ok || strings.TrimSpace(deviceSN) == "" {
		return nil, fmt.Errorf("shadow reader is unavailable")
	}
	payload, err := reader.GetShadow(ctx, deviceSN)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		State struct {
			Reported map[string]any `json:"reported"`
		} `json:"state"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("decode AWS IoT shadow: %w", err)
	}
	sn, snOK := envelope.State.Reported["sn"].(string)
	pid, pidOK := envelope.State.Reported["pid"].(string)
	_, onlineOK := envelope.State.Reported["online"].(bool)
	if !snOK || strings.TrimSpace(sn) == "" || !pidOK || strings.TrimSpace(pid) == "" || !onlineOK {
		return nil, fmt.Errorf("decode AWS IoT shadow: reported projection is invalid")
	}
	return envelope.State.Reported, nil
}

func (service *Service) GetPTZStatus(ctx context.Context, internalAppID, deviceID, deviceSN string) (PTZStatus, error) {
	result, err := service.SendCommands(ctx, internalAppID, deviceID, deviceSN, []CommandInput{{
		Code: "ptz", Value: json.RawMessage(`{"op":"get_position"}`),
	}}, true)
	if err != nil {
		return PTZStatus{}, err
	}
	if len(result.Results) != 1 || result.Results[0].Status != "success" {
		return PTZStatus{}, ErrInvalidDeviceResponse
	}
	return parsePTZStatus(result.Results[0].Data, result.Results[0].CommandID)
}

func (service *Service) SendCommands(
	ctx context.Context,
	internalAppID, deviceID, deviceSN string,
	commands []CommandInput,
	waitACK bool,
) (SendCommandsResult, error) {
	if service.publisher == nil || service.repository == nil || strings.TrimSpace(internalAppID) == "" || strings.TrimSpace(deviceID) == "" ||
		strings.TrimSpace(deviceSN) == "" || !topicPrefixRegexp.MatchString(service.topicPrefix) ||
		len(commands) == 0 || len(commands) > 32 || (waitACK && service.ackWaiter == nil) {
		return SendCommandsResult{}, ErrInvalidInput
	}
	results := make([]CommandResult, 0, len(commands))
	for index, input := range commands {
		action := strings.TrimSpace(input.Code)
		if action == "" || action == "local_media" || action == "ota_install" || len(input.Code) > 64 {
			return SendCommandsResult{}, ErrInvalidInput
		}
		sessionID := strings.TrimSpace(input.CommandID)
		if sessionID == "" {
			var err error
			sessionID, err = service.newID()
			if err != nil || strings.TrimSpace(sessionID) == "" {
				return SendCommandsResult{}, fmt.Errorf("generate command session: %w", err)
			}
		}
		if len(sessionID) > 128 || strings.HasPrefix(sessionID, "lm_") {
			return SendCommandsResult{}, ErrInvalidInput
		}
		commandID, err := randomUUID()
		if err != nil {
			return SendCommandsResult{}, fmt.Errorf("generate command id: %w", err)
		}
		payload, err := commandPayload(input, sessionID)
		if err != nil {
			return SendCommandsResult{}, ErrInvalidInput
		}
		persistedPayload, err := redactCommandFields(payload, input.RedactFields)
		if err != nil {
			return SendCommandsResult{}, ErrInvalidInput
		}
		command := Command{
			ID: commandID, DeviceID: deviceID, InternalAppID: internalAppID, SessionID: sessionID, Payload: persistedPayload,
			Status: "pending", CreatedAt: time.Now().UTC(),
		}
		if err := service.repository.Create(ctx, command); err != nil {
			return SendCommandsResult{}, err
		}
		body, err := marshalWithoutHTMLEscape(payload)
		if err != nil {
			return SendCommandsResult{}, fmt.Errorf("marshal device command: %w", err)
		}
		topic := "cmd/" + service.topicPrefix + "/" + deviceSN + "/req"
		if err := service.publisher.Publish(ctx, topic, body); err != nil {
			_ = service.repository.Complete(ctx, commandID, "failed", nil, "publish_failed")
			return SendCommandsResult{}, fmt.Errorf("%w: %v", ErrPublishFailed, err)
		}
		if !waitACK {
			if err := service.repository.Complete(ctx, commandID, "published", nil, ""); err != nil {
				return SendCommandsResult{}, err
			}
			results = append(results, CommandResult{Index: index, Code: input.Code, Status: "accepted", CommandID: sessionID})
			continue
		}
		ack, err := service.ackWaiter.Wait(ctx, deviceSN, sessionID, commandACKTimeout)
		if err != nil {
			_ = service.repository.Complete(ctx, commandID, "timed_out", nil, "device_offline")
			if errors.Is(err, ErrACKTimeout) || errors.Is(err, context.DeadlineExceeded) {
				return SendCommandsResult{}, ErrDeviceOffline
			}
			return SendCommandsResult{}, err
		}
		code, ok := numericCode(ack["code"])
		persistedACK, redactErr := redactCommandFields(ack, input.RedactFields)
		if redactErr != nil {
			_ = service.repository.Complete(ctx, commandID, "failed", nil, "invalid_device_response")
			return SendCommandsResult{}, ErrInvalidDeviceResponse
		}
		if !ok {
			_ = service.repository.Complete(ctx, commandID, "failed", persistedACK, "invalid_device_response")
			return SendCommandsResult{}, ErrInvalidDeviceResponse
		}
		switch code {
		case -2:
			_ = service.repository.Complete(ctx, commandID, "failed", persistedACK, "device_busy")
			return SendCommandsResult{}, ErrDeviceBusy
		case -6:
			_ = service.repository.Complete(ctx, commandID, "failed", persistedACK, "device_jammed")
			return SendCommandsResult{}, ErrDeviceJammed
		case 0:
			if err := service.repository.Complete(ctx, commandID, "acked", persistedACK, ""); err != nil {
				return SendCommandsResult{}, err
			}
			results = append(results, CommandResult{Index: index, Code: input.Code, Status: "success", CommandID: sessionID, Data: ack})
		default:
			if err := service.repository.Complete(ctx, commandID, "failed", persistedACK, "device_rejected"); err != nil {
				return SendCommandsResult{}, err
			}
			results = append(results, CommandResult{
				Index: index, Code: input.Code, Status: "error", CommandID: sessionID,
				Data: ack, Reason: fmt.Sprintf("device rejected command with code %d", code),
			})
		}
	}
	return SendCommandsResult{Results: results}, nil
}

// SendOTACommand is the only path that may publish ota_install. The deployment table is the
// execution authority, so presigned URLs and signed release metadata never enter device_commands.
func (service *Service) SendOTACommand(ctx context.Context, command ota.Command) error {
	if service == nil || service.publisher == nil || service.ackWaiter == nil ||
		!topicPrefixRegexp.MatchString(service.topicPrefix) ||
		strings.TrimSpace(command.DeploymentID) == "" || len(command.DeploymentID) > 63 ||
		strings.TrimSpace(command.DeviceID) == "" || strings.TrimSpace(command.DeviceSN) == "" ||
		strings.TrimSpace(command.PID) == "" || strings.TrimSpace(command.Version) == "" ||
		command.FirmwareBuild <= 0 || command.FileSize <= 0 || command.FileSize > 3*1024*1024 ||
		len(command.DownloadURL) < 8 || len(command.DownloadURL) > 1023 ||
		len(command.SHA256) != 64 || len(command.ReleaseStatement) == 0 ||
		len(command.ReleaseStatement) > 1023 || len(command.Signature) == 0 || len(command.Signature) > 511 {
		return ErrInvalidInput
	}
	payload := map[string]any{
		"action": "ota_install", "session_id": command.DeploymentID,
		"schema": "explorex-device-ota-v1", "deployment_id": command.DeploymentID,
		"pid": command.PID, "version": command.Version, "firmware_build": command.FirmwareBuild,
		"download_url": command.DownloadURL, "sha256": command.SHA256,
		"file_size": command.FileSize, "release_statement": command.ReleaseStatement,
		"signature": command.Signature,
	}
	body, err := marshalWithoutHTMLEscape(payload)
	if err != nil {
		return fmt.Errorf("marshal OTA command: %w", err)
	}
	if len(body) > 2048 {
		return ErrInvalidInput
	}
	if err := service.publisher.Publish(ctx, "cmd/"+service.topicPrefix+"/"+command.DeviceSN+"/req", body); err != nil {
		return fmt.Errorf("%w: %v", ErrPublishFailed, err)
	}
	ack, err := service.ackWaiter.Wait(ctx, command.DeviceSN, command.DeploymentID, commandACKTimeout)
	if err != nil {
		if errors.Is(err, ErrACKTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return ErrDeviceOffline
		}
		return err
	}
	code, ok := numericCode(ack["code"])
	if !ok || ack["action"] != "ota_install" || ack["session_id"] != command.DeploymentID ||
		ack["deployment_id"] != command.DeploymentID {
		return ErrInvalidDeviceResponse
	}
	if code == -2 {
		return ErrDeviceBusy
	}
	if code != 0 || ack["status"] != "accepted" {
		return ErrInvalidDeviceResponse
	}
	return nil
}

// SendLocalMediaCommand publishes the one command whose payload contains temporary LAN media
// credentials. It deliberately bypasses CommandRepository: the plaintext password exists only in
// the request/response lifetime and the MQTT transport. The device ACK is expected to be redacted.
func (service *Service) SendLocalMediaCommand(
	ctx context.Context,
	internalAppID, deviceSN, sessionID string,
	value json.RawMessage,
) (CommandResult, error) {
	if service.publisher == nil || service.ackWaiter == nil || strings.TrimSpace(internalAppID) == "" ||
		strings.TrimSpace(deviceSN) == "" || !topicPrefixRegexp.MatchString(service.topicPrefix) ||
		len(sessionID) < 16 || len(sessionID) > 63 || len(value) == 0 || len(value) > 1024 {
		return CommandResult{}, ErrInvalidInput
	}
	for _, character := range sessionID {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' {
			return CommandResult{}, ErrInvalidInput
		}
	}
	payload, err := commandPayload(CommandInput{Code: "local_media", Value: value}, sessionID)
	if err != nil {
		return CommandResult{}, ErrInvalidInput
	}
	body, err := marshalWithoutHTMLEscape(payload)
	if err != nil {
		return CommandResult{}, fmt.Errorf("marshal local media command: %w", err)
	}
	if err := service.publisher.Publish(ctx, "cmd/"+service.topicPrefix+"/"+deviceSN+"/req", body); err != nil {
		return CommandResult{}, fmt.Errorf("%w: %v", ErrPublishFailed, err)
	}
	ack, err := service.ackWaiter.Wait(ctx, deviceSN, sessionID, commandACKTimeout)
	if err != nil {
		if errors.Is(err, ErrACKTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return CommandResult{}, ErrDeviceOffline
		}
		return CommandResult{}, err
	}
	code, ok := numericCode(ack["code"])
	if !ok || ack["action"] != "local_media" || ack["session_id"] != sessionID {
		return CommandResult{}, ErrInvalidDeviceResponse
	}
	switch code {
	case -2:
		return CommandResult{}, ErrDeviceBusy
	case 0:
		return CommandResult{
			Code: "local_media", Status: "success", CommandID: sessionID, Data: ack,
		}, nil
	default:
		return CommandResult{
			Code: "local_media", Status: "error", CommandID: sessionID, Data: ack,
			Reason: fmt.Sprintf("device rejected command with code %d", code),
		}, nil
	}
}

func parsePTZStatus(data map[string]any, expectedSessionID string) (PTZStatus, error) {
	if data == nil || data["action"] != "ptz" || data["op"] != "get_position" ||
		data["session_id"] != expectedSessionID {
		return PTZStatus{}, ErrInvalidDeviceResponse
	}
	code, ok := numericCode(data["code"])
	if !ok || code != 0 {
		return PTZStatus{}, ErrInvalidDeviceResponse
	}
	pan, panOK := exactInteger(data["pan"])
	tilt, tiltOK := exactInteger(data["tilt"])
	limitH, limitHOK := exactInteger(data["limit_h"])
	limitV, limitVOK := exactInteger(data["limit_v"])
	panNormalized, panNormalizedOK := numericFloat(data["pan_normalized"])
	tiltNormalized, tiltNormalizedOK := numericFloat(data["tilt_normalized"])
	moving, movingOK := data["moving"].(bool)
	if !panOK || !tiltOK || !limitHOK || !limitVOK || limitH <= 0 || limitV <= 0 ||
		!panNormalizedOK || !tiltNormalizedOK || panNormalized < -1 || panNormalized > 1 ||
		tiltNormalized < -1 || tiltNormalized > 1 || !movingOK {
		return PTZStatus{}, ErrInvalidDeviceResponse
	}
	return PTZStatus{
		Pan: pan, Tilt: tilt, LimitH: limitH, LimitV: limitV,
		PanNormalized: panNormalized, TiltNormalized: tiltNormalized, Moving: moving,
	}, nil
}

func exactInteger(value any) (int64, bool) {
	return numericCode(value)
}

func numericFloat(value any) (float64, bool) {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0)
}

func commandPayload(input CommandInput, sessionID string) (map[string]any, error) {
	payload := map[string]any{"action": input.Code, "session_id": sessionID}
	if len(input.Value) == 0 {
		return payload, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(input.Value))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if object, ok := value.(map[string]any); ok {
		for key, field := range object {
			if key == "action" || key == "session_id" {
				return nil, ErrInvalidInput
			}
			payload[key] = field
		}
	} else {
		payload["value"] = value
	}
	return payload, nil
}

func redactCommandFields(payload map[string]any, fields []string) (map[string]any, error) {
	result := make(map[string]any, len(payload))
	for key, value := range payload {
		result[key] = value
	}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" || field == "action" || field == "session_id" {
			return nil, ErrInvalidInput
		}
		delete(result, field)
	}
	return result, nil
}

func marshalWithoutHTMLEscape(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func numericCode(value any) (int64, bool) {
	switch code := value.(type) {
	case float64:
		const minInt64Float = -9223372036854775808.0
		const maxInt64ExclusiveFloat = 9223372036854775808.0
		if math.IsNaN(code) || math.IsInf(code, 0) || math.Trunc(code) != code ||
			code < minInt64Float || code >= maxInt64ExclusiveFloat {
			return 0, false
		}
		return int64(code), true
	case int:
		return int64(code), true
	case int64:
		return code, true
	case json.Number:
		parsed, err := code.Int64()
		return parsed, err == nil
	default:
		return 0, false
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
