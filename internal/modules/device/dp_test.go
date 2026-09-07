package device

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateCommandsAgainstDPDefinitions(t *testing.T) {
	t.Parallel()
	definitions := []DPDefinition{
		{Code: "video_res", Type: "object", Direction: "wo", Status: "active"},
		{Code: "privacy_mode", Type: "object", Direction: "wo", Status: "disabled"},
		{Code: "device_info", Type: "object", Direction: "ro", Status: "active"},
	}

	if err := ValidateCommands(definitions, []CommandInput{{Code: "video_res", Value: json.RawMessage(`{"resolution":"1080p"}`)}}); err != nil {
		t.Fatalf("ValidateCommands() valid command error = %v", err)
	}

	tests := []struct {
		name string
		code string
		want error
	}{
		{name: "unknown", code: "not_registered", want: ErrDPUnknown},
		{name: "disabled", code: "privacy_mode", want: ErrDPDisabled},
		{name: "read only", code: "device_info", want: ErrDPReadOnly},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidateCommands(definitions, []CommandInput{{Code: testCase.code, Value: json.RawMessage(`{}`)}})
			if !errors.Is(err, ErrInvalidInput) || !errors.Is(err, testCase.want) {
				t.Fatalf("ValidateCommands() error = %v, want ErrInvalidInput and %v", err, testCase.want)
			}
		})
	}
}

func TestValidateCommandsChecksObjectPayloadShape(t *testing.T) {
	t.Parallel()
	definitions := []DPDefinition{{Code: "privacy_mode", Type: "object", Direction: "wo", Status: "active"}}
	err := ValidateCommands(definitions, []CommandInput{{Code: "privacy_mode", Value: json.RawMessage(`true`)}})
	if !errors.Is(err, ErrDPInvalidValue) {
		t.Fatalf("ValidateCommands() error = %v, want ErrDPInvalidValue", err)
	}
}

func TestValidateCommandsChecksRequiredObjectFieldsWhenValueIsOmitted(t *testing.T) {
	t.Parallel()
	definitions := []DPDefinition{{
		Code: "feed", Type: "object", Direction: "wo", Status: "active",
		Spec: json.RawMessage(`{"required":["portions"]}`),
	}}
	err := ValidateCommands(definitions, []CommandInput{{Code: "feed"}})
	if !errors.Is(err, ErrDPInvalidValue) {
		t.Fatalf("ValidateCommands() error = %v, want ErrDPInvalidValue", err)
	}
}

func TestValidateCommandsChecksNumericPropertyBounds(t *testing.T) {
	t.Parallel()
	definitions := []DPDefinition{{
		Code: "speaker_vol", Type: "object", Direction: "wo", Status: "active",
		Spec: json.RawMessage(`{"required":["volume"],"properties":{"volume":{"type":"integer","minimum":0,"maximum":100}}}`),
	}}

	if err := ValidateCommands(definitions, []CommandInput{{
		Code: "speaker_vol", Value: json.RawMessage(`{"volume":60}`),
	}}); err != nil {
		t.Fatalf("ValidateCommands() valid bounded value error = %v", err)
	}
	for _, value := range []string{`{"volume":-1}`, `{"volume":101}`} {
		err := ValidateCommands(definitions, []CommandInput{{
			Code: "speaker_vol", Value: json.RawMessage(value),
		}})
		if !errors.Is(err, ErrDPInvalidValue) {
			t.Fatalf("ValidateCommands(%s) error = %v, want ErrDPInvalidValue", value, err)
		}
	}
}

func TestValidateCommandsRejectsLifecycleDPsFromGenericPath(t *testing.T) {
	t.Parallel()
	definitions := []DPDefinition{
		{Code: "factory_reset", Type: "object", Direction: "downlink_request_ack", Status: "active"},
		{Code: "recommission", Type: "object", Direction: "downlink_request_ack", Status: "active"},
		{Code: "reset_wifi", Type: "object", Direction: "downlink_request_ack", Status: "active"},
	}
	for _, code := range []string{"factory_reset", "recommission", "reset_wifi"} {
		err := ValidateCommands(definitions, []CommandInput{{Code: code, Value: json.RawMessage(`{}`)}})
		if !errors.Is(err, ErrInvalidInput) || !errors.Is(err, ErrDPControlled) {
			t.Fatalf("ValidateCommands(%q) error = %v, want controlled-path rejection", code, err)
		}
	}
}

func TestValidateCommandsEnforcesConditionalVariants(t *testing.T) {
	t.Parallel()
	definitions := []DPDefinition{{
		Code: "ptz", Type: "object", Direction: "downlink_request_ack", Status: "active",
		Spec: json.RawMessage(`{
			"required":["op"],
			"properties":{
				"op":{"type":"string","enum":["move","move_to","stop"]},
				"direction":{"type":"string"},
				"steps":{"type":"integer","minimum":1,"maximum":2048},
				"pan_normalized":{"type":"number","minimum":-1,"maximum":1},
				"tilt_normalized":{"type":"number","minimum":-1,"maximum":1}
			},
			"allow_extra":false,
			"variants":{
				"field":"op",
				"cases":{
					"move":{"required":["direction"],"properties":{"direction":{"type":"string","enum":["up","down","left","right"]}}},
					"move_to":{"required":["pan_normalized","tilt_normalized"]},
					"stop":{}
				}
			}
		}`),
	}}

	for _, testCase := range []struct {
		name  string
		value string
		valid bool
	}{
		{name: "move requires direction", value: `{"op":"move","steps":10}`, valid: false},
		{name: "move rejects unknown direction", value: `{"op":"move","direction":"diagonal"}`, valid: false},
		{name: "move_to requires both axes", value: `{"op":"move_to","pan_normalized":0.2}`, valid: false},
		{name: "stop accepts no movement fields", value: `{"op":"stop"}`, valid: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidateCommands(definitions, []CommandInput{{Code: "ptz", Value: json.RawMessage(testCase.value)}})
			if (err == nil) != testCase.valid {
				t.Fatalf("ValidateCommands(%s) error = %v, valid=%t", testCase.value, err, testCase.valid)
			}
		})
	}
}

func TestValidateCommandsEnforcesNestedAndCrossFieldConstraints(t *testing.T) {
	t.Parallel()
	definitions := []DPDefinition{{
		Code: "stream", Type: "object", Direction: "downlink_request_ack", Status: "active",
		Spec: json.RawMessage(`{
			"required":["start"],
			"properties":{
				"start":{"type":"integer","enum":[0,1]},
				"viewer_client_id":{"type":"string"},
				"requested_capabilities":{
					"type":"object","required":["video_receive","audio_receive","audio_send","data_channels"],
					"properties":{
						"video_receive":{"type":"boolean"},
						"audio_receive":{"type":"boolean"},
						"audio_send":{"type":"boolean"},
						"data_channels":{"type":"array","items":{"type":"string","enum":["explorex.control.v1"]}}
					},"allow_extra":false
				}
			},
			"allow_extra":false,
			"variants":{
				"field":"start",
				"cases":{
					"1":{"required":["viewer_client_id","requested_capabilities"]},
					"0":{"required":["viewer_client_id"]}
				}
			}
		}`),
	}}
	valid := `{"start":1,"viewer_client_id":"viewer-1","requested_capabilities":{"video_receive":true,"audio_receive":false,"audio_send":false,"data_channels":["explorex.control.v1"]}}`
	if err := ValidateCommands(definitions, []CommandInput{{Code: "stream", Value: json.RawMessage(valid)}}); err != nil {
		t.Fatalf("ValidateCommands() valid stream error = %v", err)
	}
	for _, value := range []string{
		`{"start":1,"viewer_client_id":"viewer-1"}`,
		`{"start":1,"viewer_client_id":"viewer-1","requested_capabilities":{"video_receive":true,"audio_receive":false,"audio_send":false,"data_channels":["unknown.v1"]}}`,
		`{"start":0,"viewer_client_id":"viewer-1","requested_capabilities":{}}`,
	} {
		if err := ValidateCommands(definitions, []CommandInput{{Code: "stream", Value: json.RawMessage(value)}}); !errors.Is(err, ErrDPInvalidValue) {
			t.Fatalf("ValidateCommands(%s) error = %v, want ErrDPInvalidValue", value, err)
		}
	}
}

func TestValidateCommandsEnforcesContinuousRecordingPresets(t *testing.T) {
	t.Parallel()
	definitions := []DPDefinition{{
		Code: "record_mode", Type: "object", Direction: "downlink_request_ack", Status: "active",
		Spec: json.RawMessage(`{
			"properties":{
				"mode":{"type":"integer","enum":[0,1,2]},
				"segment_s":{"type":"integer","enum":[60,180,300]}
			},"allow_extra":false
		}`),
	}}
	for _, testCase := range []struct {
		value string
		valid bool
	}{
		{value: `{"mode":1}`, valid: true},
		{value: `{"mode":1,"segment_s":60}`, valid: true},
		{value: `{"mode":1,"segment_s":180}`, valid: true},
		{value: `{"mode":1,"segment_s":300}`, valid: true},
		{value: `{"mode":1,"segment_s":120}`, valid: false},
		{value: `{"mode":1,"segment_s":60,"unexpected":true}`, valid: false},
	} {
		err := ValidateCommands(definitions, []CommandInput{{Code: "record_mode", Value: json.RawMessage(testCase.value)}})
		if (err == nil) != testCase.valid {
			t.Fatalf("ValidateCommands(%s) error = %v, valid=%t", testCase.value, err, testCase.valid)
		}
	}
}

func TestValidateCommandsEnforcesRecordStartVariant(t *testing.T) {
	t.Parallel()
	definitions := []DPDefinition{{
		Code: "record_start", Type: "object", Direction: "downlink_request_ack", Status: "active",
		Spec: json.RawMessage(`{
			"properties":{
				"mode":{"type":"integer","enum":[0,1]},
				"duration_s":{"type":"integer","minimum":0,"maximum":60}
			},"allow_extra":false,
			"variants":{"field":"mode","cases":{"0":{},"1":{"required":["duration_s"]}}}
		}`),
	}}
	for _, testCase := range []struct {
		value string
		valid bool
	}{
		{value: `{"mode":0}`, valid: true},
		{value: `{"mode":1,"duration_s":30}`, valid: true},
		{value: `{"mode":1}`, valid: false},
	} {
		err := ValidateCommands(definitions, []CommandInput{{Code: "record_start", Value: json.RawMessage(testCase.value)}})
		if (err == nil) != testCase.valid {
			t.Fatalf("ValidateCommands(%s) error = %v, valid=%t", testCase.value, err, testCase.valid)
		}
	}
}

func TestValidateCommandsEnforcesNestedRectangleBounds(t *testing.T) {
	t.Parallel()
	definitions := []DPDefinition{{
		Code: "alarm_config", Type: "object", Direction: "downlink_request_ack", Status: "active",
		Spec: json.RawMessage(`{
			"properties":{"roi":{
				"type":"object","required":["x","y","width","height"],
				"properties":{
					"x":{"type":"integer","minimum":0,"maximum":10000},
					"y":{"type":"integer","minimum":0,"maximum":10000},
					"width":{"type":"integer","minimum":1,"maximum":10000},
					"height":{"type":"integer","minimum":1,"maximum":10000}
				},"allow_extra":false,
				"constraints":{"positive":["width","height"],"max_sums":{"x+width":10000,"y+height":10000}}
			}},"allow_extra":false
		}`),
	}}
	valid := `{"roi":{"x":100,"y":200,"width":300,"height":400}}`
	if err := ValidateCommands(definitions, []CommandInput{{Code: "alarm_config", Value: json.RawMessage(valid)}}); err != nil {
		t.Fatalf("ValidateCommands() valid ROI error = %v", err)
	}
	for _, value := range []string{
		`{"roi":{"x":9900,"y":0,"width":101,"height":1}}`,
		`{"roi":{"x":0,"y":0,"width":0,"height":1}}`,
	} {
		if err := ValidateCommands(definitions, []CommandInput{{Code: "alarm_config", Value: json.RawMessage(value)}}); !errors.Is(err, ErrDPInvalidValue) {
			t.Fatalf("ValidateCommands(%s) error = %v, want ErrDPInvalidValue", value, err)
		}
	}
}

func TestValidateCommandsEnforcesEnumPropertyType(t *testing.T) {
	t.Parallel()
	definitions := []DPDefinition{{
		Code: "video_res", Type: "object", Direction: "downlink_request_ack", Status: "active",
		Spec: json.RawMessage(`{
			"required":["resolution"],
			"properties":{"resolution":{"type":"string","enum":["360p","480p","720p"]}},
			"allow_extra":false
		}`),
	}}
	if err := ValidateCommands(definitions, []CommandInput{{
		Code: "video_res", Value: json.RawMessage(`{"resolution":"720p"}`),
	}}); err != nil {
		t.Fatalf("ValidateCommands() valid enum value error = %v", err)
	}
	for _, value := range []string{`{"resolution":720}`, `{"resolution":true}`} {
		if err := ValidateCommands(definitions, []CommandInput{{Code: "video_res", Value: json.RawMessage(value)}}); !errors.Is(err, ErrDPInvalidValue) {
			t.Fatalf("ValidateCommands(%s) error = %v, want ErrDPInvalidValue", value, err)
		}
	}
}
