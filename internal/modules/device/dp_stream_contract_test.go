package device

import (
	"encoding/json"
	"strings"
	"testing"
)

// feeder-v1 线上 stream DP 契约（dp_definitions.spec），只声明调用方可写的字段。
const liveStreamSpec = `{"required": ["type", "start"], "variants": {"cases": {"0": {"required": ["viewer_client_id"]}, "1": {"required": ["viewer_client_id", "requested_capabilities"]}}, "field": "start"}, "properties": {"type": {"enum": ["webrtc"], "type": "string"}, "start": {"enum": [0, 1], "type": "integer"}, "viewer_client_id": {"type": "string"}, "requested_capabilities": {"type": "object", "required": ["video_receive", "audio_receive", "audio_send", "data_channels"], "properties": {"audio_send": {"type": "boolean"}, "audio_receive": {"type": "boolean"}, "data_channels": {"type": "array", "items": {"enum": ["explorex.control.v1"], "type": "string"}}, "video_receive": {"type": "boolean"}}, "allow_extra": false}}, "allow_extra": false}`

const streamCallerValue = `{"type":"webrtc","start":1,"viewer_client_id":"viewer-1","requested_capabilities":{"video_receive":true,"audio_receive":false,"audio_send":false,"data_channels":[]}`

// 国内 realtime 路径下 media 服务在调用方字段之上追加的网关签发字段。
const streamInjectedFields = `,"ice_servers":[{"urls":["stun:1.2.3.4:3478"]}],"realtime":{"session_id":"s-1","device_config":{"wss_url":"wss://host/ws"}}`

func streamValue(extra string) json.RawMessage {
	return json.RawMessage(streamCallerValue + extra + "}")
}

func TestStreamContractTreatsGatewayInjectedFieldsAsTrusted(t *testing.T) {
	definitions := []DPDefinition{{
		Code: "stream", Type: "object", Direction: "downlink_request_ack",
		Status: "active", Spec: json.RawMessage(liveStreamSpec),
	}}

	if err := ValidateCommands(definitions, []CommandInput{{Code: "stream", Value: streamValue("")}}); err != nil {
		t.Fatalf("caller-shaped stream command rejected: %v", err)
	}
	// 未登记为 trusted 就是越界，allow_extra:false 必须继续拦住。
	if err := ValidateCommands(definitions, []CommandInput{{Code: "stream", Value: streamValue(streamInjectedFields)}}); err == nil ||
		!strings.Contains(err.Error(), `unexpected field "ice_servers"`) {
		t.Fatalf("unregistered extra field must stay rejected, got %v", err)
	}
	// 网关自己签发的字段不属于调用方合同，推流必须能过。
	if err := ValidateCommands(definitions, []CommandInput{{
		Code: "stream", Value: streamValue(streamInjectedFields),
		TrustedFields: []string{"ice_servers", "realtime"},
	}}); err != nil {
		t.Fatalf("gateway-injected stream fields rejected: %v", err)
	}
	// trusted 字段不能替调用方漏写的必写字段兜底。
	if err := ValidateCommands(definitions, []CommandInput{{
		Code: "stream", Value: json.RawMessage(`{"type":"webrtc","start":1` + streamInjectedFields + "}"),
		TrustedFields: []string{"ice_servers", "realtime"},
	}}); err == nil || !strings.Contains(err.Error(), `missing required field "viewer_client_id"`) {
		t.Fatalf("trusted fields must not satisfy required fields, got %v", err)
	}
}
