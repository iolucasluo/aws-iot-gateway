package events

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const ackRetention = 30 * time.Second

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

type EventReader interface {
	List(context.Context, string, string, int) ([]Event, error)
}

type QueryService struct{ reader EventReader }

func NewQueryService(reader EventReader) *QueryService { return &QueryService{reader: reader} }

func (service *QueryService) List(ctx context.Context, deviceID, eventType string, limit int) ([]Event, error) {
	if service == nil || service.reader == nil || strings.TrimSpace(deviceID) == "" || limit < 0 {
		return nil, fmt.Errorf("invalid event query")
	}
	if limit == 0 || limit > 100 {
		limit = 50
	}
	return service.reader.List(ctx, deviceID, strings.TrimSpace(eventType), limit)
}

type IngestService struct {
	namespace  string
	ackSink    ACKSink
	repository IngestRepository
}

func NewIngestService(namespace string, ackSink ACKSink, repository IngestRepository) (*IngestService, error) {
	if !namespacePattern.MatchString(namespace) || ackSink == nil || repository == nil {
		return nil, fmt.Errorf("event ingest requires a namespace, ACK sink, and repository")
	}
	return &IngestService{namespace: namespace, ackSink: ackSink, repository: repository}, nil
}

func (service *IngestService) Handle(ctx context.Context, payload []byte) error {
	var message map[string]any
	if err := json.Unmarshal(payload, &message); err != nil {
		return fmt.Errorf("decode IoT ingest message: %w", err)
	}
	return service.handleMessage(ctx, message)
}

func (service *IngestService) HandleMQTT(ctx context.Context, topic string, payload []byte) error {
	var message map[string]any
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
	return service.repository.Store(ctx, event)
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
	if !ok || number != float64(int64(number)) {
		return 0, false
	}
	return int64(number), true
}
