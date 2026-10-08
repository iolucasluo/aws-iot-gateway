package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	ackRetention       = 30 * time.Second
	maxJSONSafeInteger = int64(9007199254740991)
)

var (
	namespacePattern             = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	deviceSNPattern              = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,64}$`)
	uuidPattern                  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	localMediaTokenPattern       = regexp.MustCompile(`^[A-Za-z0-9_-]{8,63}$`)
	localMediaFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Event struct {
	ID            string         `json:"id"`
	DeviceSN      string         `json:"device_sn"`
	EventType     string         `json:"event_type"`
	BootID        string         `json:"boot_id"`
	EventSeq      int64          `json:"event_seq"`
	MonotonicMS   *int64         `json:"monotonic_ms,omitempty"`
	TimeQuality   string         `json:"time_quality,omitempty"`
	TimeSource    string         `json:"time_source,omitempty"`
	OccurredAt    *time.Time     `json:"occurred_at,omitempty"`
	ReceivedAt    time.Time      `json:"received_at"`
	Data          map[string]any `json:"data"`
	InternalAppID string         `json:"-"`
}

type ACKSink interface {
	Store(context.Context, string, string, map[string]any, time.Duration) error
}

type EventRepository interface {
	Store(context.Context, Event) error
}

type PresenceRepository interface {
	UpdatePresence(context.Context, string, bool, time.Time) error
}

type IngestRepository interface {
	EventRepository
	PresenceRepository
}

// EventCursor 是事件列表的 keyset 分页位置；零值表示从头开始。
type EventCursor struct {
	ReceivedAt time.Time
	ID         string
}

func (cursor EventCursor) Zero() bool {
	return cursor.ReceivedAt.IsZero() || cursor.ID == ""
}

// EncodeEventCursor 生成不透明分页游标（接收时间 unix nano + 事件 id）。
func EncodeEventCursor(event Event) string {
	return strconv.FormatInt(event.ReceivedAt.UnixNano(), 10) + "_" + event.ID
}

// ErrInvalidCursor 表示分页游标格式非法，属于客户端输入错误。
var ErrInvalidCursor = fmt.Errorf("invalid event cursor")

// ParseEventCursor 解析并校验分页游标；空串表示从头开始。
func ParseEventCursor(raw string) (EventCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return EventCursor{}, nil
	}
	stamp, id, found := strings.Cut(raw, "_")
	nanos, err := strconv.ParseInt(stamp, 10, 64)
	if !found || err != nil || id == "" || nanos <= 0 {
		return EventCursor{}, ErrInvalidCursor
	}
	return EventCursor{ReceivedAt: time.Unix(0, nanos).UTC(), ID: id}, nil
}

type EventReader interface {
	List(context.Context, string, string, int, EventCursor) ([]Event, error)
}

type QueryService struct{ reader EventReader }

func NewQueryService(reader EventReader) *QueryService { return &QueryService{reader: reader} }

// List 返回一页事件与下一页游标；游标为空串表示没有更多数据。
func (service *QueryService) List(ctx context.Context, deviceID, eventType string, limit int, rawCursor string) ([]Event, string, error) {
	if service == nil || service.reader == nil || strings.TrimSpace(deviceID) == "" || limit < 0 {
		return nil, "", fmt.Errorf("invalid event query")
	}
	if limit == 0 || limit > 100 {
		limit = 50
	}
	cursor, err := ParseEventCursor(rawCursor)
	if err != nil {
		return nil, "", err
	}
	items, err := service.reader.List(ctx, deviceID, strings.TrimSpace(eventType), limit+1, cursor)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = EncodeEventCursor(items[len(items)-1])
	}
	return items, next, nil
}

// PetPhotoWorkflow owns the private capture transaction. Internal ready messages
// must never be persisted as user events; final events are checked against the
// verified, device-bound image before the ordinary event repository sees them.
type PetPhotoWorkflow interface {
	CaptureReady(context.Context, Event) error
	VerifyFinal(context.Context, Event) error
}

type IngestService struct {
	namespace  string
	ackSink    ACKSink
	repository IngestRepository
	petPhotos  PetPhotoWorkflow
}

// SetPetPhotoWorkflow is called at process startup before ingest workers run.
func (service *IngestService) SetPetPhotoWorkflow(workflow PetPhotoWorkflow) {
	service.petPhotos = workflow
}

func NewIngestService(namespace string, ackSink ACKSink, repository IngestRepository) (*IngestService, error) {
	if !namespacePattern.MatchString(namespace) || ackSink == nil || repository == nil {
		return nil, fmt.Errorf("event ingest requires a namespace, ACK sink, and repository")
	}
	return &IngestService{namespace: namespace, ackSink: ackSink, repository: repository}, nil
}

func (service *IngestService) Handle(ctx context.Context, payload []byte) error {
	var message map[string]any
	if err := rejectDuplicateJSONKeys(payload); err != nil {
		return fmt.Errorf("decode IoT ingest message: %w", err)
	}
	if err := json.Unmarshal(payload, &message); err != nil {
		return fmt.Errorf("decode IoT ingest message: %w", err)
	}
	return service.handleMessage(ctx, message)
}

func (service *IngestService) HandleMQTT(ctx context.Context, topic string, payload []byte) error {
	var message map[string]any
	if err := rejectDuplicateJSONKeys(payload); err != nil {
		return fmt.Errorf("decode MQTT message: %w", err)
	}
	if err := json.Unmarshal(payload, &message); err != nil {
		return fmt.Errorf("decode MQTT message: %w", err)
	}
	message["topic"] = topic
	parts := strings.Split(topic, "/")
	if len(parts) == 4 && parts[0] == "dt" && parts[1] == service.namespace &&
		(parts[3] == "status" || parts[3] == "lwt") {
		// Direct MQTT payloads do not pass through the IoT Rule that adds
		// timestamp(). Use the authenticated Gateway receipt time instead of a
		// device-controlled field so presence ordering cannot be forged.
		message["received_at_ms"] = float64(time.Now().UTC().UnixMilli())
	}
	return service.handleMessage(ctx, message)
}

func rejectDuplicateJSONKeys(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON contains trailing value")
		}
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		keys := map[string]struct{}{}
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return keyErr
			}
			key, keyOK := keyToken.(string)
			if !keyOK {
				return fmt.Errorf("JSON object key is invalid")
			}
			if _, duplicate := keys[key]; duplicate {
				return fmt.Errorf("JSON object contains duplicate key %q", key)
			}
			keys[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim('}') {
			return fmt.Errorf("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim(']') {
			return fmt.Errorf("JSON array is not closed")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func (service *IngestService) handleMessage(ctx context.Context, message map[string]any) error {
	topic, ok := message["topic"].(string)
	if !ok || topic == "" {
		return fmt.Errorf("IoT ingest message has no topic")
	}
	parts := strings.Split(topic, "/")
	if len(parts) != 4 || parts[1] != service.namespace || !deviceSNPattern.MatchString(parts[2]) {
		return fmt.Errorf("IoT ingest topic is outside the configured namespace")
	}
	// Canonical topics contain four segments: cmd/{prefix}/{sn}/res or
	// dt/{prefix}/{sn}/event. Keep this branch separate to make namespace
	// validation explicit and reject wildcard-shaped messages.
	deviceSN := parts[2]
	if parts[0] == "cmd" && parts[3] == "res" {
		sessionID, ok := message["session_id"].(string)
		if !ok || strings.TrimSpace(sessionID) == "" || len(sessionID) > 128 {
			return fmt.Errorf("command ACK has an invalid session_id")
		}
		delete(message, "topic")
		if (message["action"] == "local_media" || strings.HasPrefix(sessionID, "lm_")) &&
			!safeLocalMediaACKForStorage(message, sessionID) {
			return fmt.Errorf("local-media command ACK failed pre-storage validation")
		}
		return service.ackSink.Store(ctx, deviceSN, sessionID, message, ackRetention)
	}
	if parts[0] == "dt" && (parts[3] == "status" || parts[3] == "lwt") {
		online, onlineOK := message["online"].(bool)
		receivedAtMS, timestampOK := integer(message["received_at_ms"])
		expectedOnline := parts[3] == "status"
		if !onlineOK || online != expectedOnline || !timestampOK || receivedAtMS <= 0 {
			return fmt.Errorf("device presence message is invalid")
		}
		return service.repository.UpdatePresence(ctx, deviceSN, online, time.UnixMilli(receivedAtMS).UTC())
	}
	if parts[0] != "dt" || parts[3] != "event" {
		return fmt.Errorf("unsupported IoT ingest topic")
	}
	event, err := parseEvent(deviceSN, message)
	if err != nil {
		return err
	}
	if event.EventType == "pet_photo_ready_v1" {
		if !validPetPhotoReady(event.Data) || service.petPhotos == nil {
			return fmt.Errorf("pet photo capture is not authorized")
		}
		return service.petPhotos.CaptureReady(ctx, event)
	}
	if event.EventType == "pet_detected" &&
		(event.Data["schema"] != nil || event.Data["media_id"] != nil ||
			event.Data["image_url"] != nil || event.Data["model_id"] == "0x0A00002C") {
		if !validPetPhotoFinal(event.DeviceSN, event.Data) || service.petPhotos == nil {
			return fmt.Errorf("pet photo final event is not authorized")
		}
		if err := service.petPhotos.VerifyFinal(ctx, event); err != nil {
			return fmt.Errorf("verify pet photo before event storage: %w", err)
		}
	}
	return service.repository.Store(ctx, event)
}

func validPetPhotoReady(data map[string]any) bool {
	triggerID, _ := data["trigger_id"].(string)
	hash, _ := data["sha256"].(string)
	size, ok := integer(data["size_bytes"])
	if !uuidPattern.MatchString(triggerID) || data["model_id"] != "0x0A00002C" ||
		!ok || size <= 0 || size > 256*1024 || len(hash) != 64 {
		return false
	}
	for _, digit := range hash {
		if (digit < '0' || digit > '9') && (digit < 'a' || digit > 'f') {
			return false
		}
	}
	return true
}

func validPetPhotoFinal(deviceSN string, data map[string]any) bool {
	triggerID, _ := data["trigger_id"].(string)
	mediaID, _ := data["media_id"].(string)
	modelID, _ := data["model_id"].(string)
	rawURL, _ := data["image_url"].(string)
	if data["schema"] != "pet-detection.v1" ||
		!uuidPattern.MatchString(triggerID) || !uuidPattern.MatchString(mediaID) ||
		modelID != "0x0A00002C" || len(rawURL) > 1024 {
		return false
	}
	parsed, err := url.Parse(rawURL)
	expectedPath := "/api/v1/openapi/devices/" + deviceSN + "/pet-photos/" + mediaID
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" &&
		parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" &&
		parsed.EscapedPath() == expectedPath && parsed.Opaque == ""
}

func safeLocalMediaACKForStorage(message map[string]any, sessionID string) bool {
	if message["action"] != "local_media" || message["session_id"] != sessionID ||
		!localMediaTokenPattern.MatchString(sessionID) {
		return false
	}
	code, codeOK := integer(message["code"])
	if !codeOK {
		return false
	}
	if code != 0 {
		return len(message) == 3
	}
	if message["contract_version"] != "local-media.v1" || message["protocol"] != "onvif" ||
		(message["result"] != "applied" && message["result"] != "duplicate") {
		return false
	}
	revision, revisionOK := integer(message["revision"])
	if !revisionOK || revision <= 0 {
		return false
	}
	switch message["state"] {
	case "disabled":
		return len(message) == 8
	case "ready":
		if len(message) != 13 {
			return false
		}
		expiresAt, expiresOK := integer(message["expires_at_epoch_sec"])
		generation, generationOK := message["credential_generation"].(string)
		fingerprint, fingerprintOK := message["credential_fingerprint"].(string)
		onvifEndpoint, onvifOK := message["onvif_endpoint"].(string)
		rtspBaseURI, rtspOK := message["rtsp_base_uri"].(string)
		return expiresOK && expiresAt > 0 && generationOK &&
			localMediaTokenPattern.MatchString(generation) && fingerprintOK &&
			localMediaFingerprintPattern.MatchString(fingerprint) && onvifOK && rtspOK &&
			safeLocalMediaLocatorsForStorage(onvifEndpoint, rtspBaseURI)
	default:
		return false
	}
}

func safeLocalMediaLocatorsForStorage(onvifEndpoint, rtspBaseURI string) bool {
	onvifURL, err := url.Parse(onvifEndpoint)
	if err != nil || onvifURL.Scheme != "http" || onvifURL.User != nil ||
		onvifURL.Port() != "8081" || onvifURL.Path != "/onvif/device_service" ||
		onvifURL.RawQuery != "" || onvifURL.Fragment != "" {
		return false
	}
	onvifIP := net.ParseIP(onvifURL.Hostname())
	if onvifIP == nil || onvifIP.To4() == nil || !onvifIP.IsPrivate() ||
		onvifIP.IsLoopback() || onvifIP.IsUnspecified() {
		return false
	}
	rtspURL, err := url.Parse(rtspBaseURI)
	if err != nil || rtspURL.Scheme != "rtsp" || rtspURL.User != nil ||
		rtspURL.Port() != "554" || rtspURL.Path != "" || rtspURL.RawQuery != "" ||
		rtspURL.Fragment != "" {
		return false
	}
	rtspIP := net.ParseIP(rtspURL.Hostname())
	return rtspIP != nil && rtspIP.To4() != nil && rtspIP.Equal(onvifIP)
}

func parseEvent(deviceSN string, message map[string]any) (Event, error) {
	eventID, _ := message["event_id"].(string)
	eventType, _ := message["event_type"].(string)
	bootID, _ := message["boot_id"].(string)
	eventSeq, ok := integer(message["event_seq"])
	data, dataOK := message["data"].(map[string]any)
	if !uuidPattern.MatchString(eventID) || strings.TrimSpace(eventType) == "" || len(eventType) > 64 ||
		!uuidPattern.MatchString(bootID) || !ok || eventSeq < 0 || !dataOK {
		return Event{}, fmt.Errorf("device event identity or data is invalid")
	}
	event := Event{
		ID: eventID, DeviceSN: deviceSN, EventType: eventType, BootID: bootID,
		EventSeq: eventSeq, Data: data, ReceivedAt: time.Now().UTC(),
	}
	if value, found := message["monotonic_ms"]; found && value != nil {
		monotonic, ok := integer(value)
		if !ok || monotonic < 0 {
			return Event{}, fmt.Errorf("device event monotonic_ms is invalid")
		}
		event.MonotonicMS = &monotonic
	}
	event.TimeQuality, _ = message["time_quality"].(string)
	event.TimeSource, _ = message["time_source"].(string)
	if value, found := message["occurred_at"]; found && value != nil {
		var occurredAt time.Time
		switch typed := value.(type) {
		case string:
			parsed, err := time.Parse(time.RFC3339Nano, typed)
			if err != nil {
				return Event{}, fmt.Errorf("device event occurred_at is invalid")
			}
			occurredAt = parsed.UTC()
		default:
			unixMilliseconds, ok := integer(value)
			if !ok || unixMilliseconds <= 0 {
				return Event{}, fmt.Errorf("device event occurred_at is invalid")
			}
			occurredAt = time.UnixMilli(unixMilliseconds).UTC()
		}
		event.OccurredAt = &occurredAt
	}
	return event, nil
}

func integer(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) || number > float64(maxJSONSafeInteger) ||
		number < -float64(maxJSONSafeInteger) {
		return 0, false
	}
	return int64(number), true
}
