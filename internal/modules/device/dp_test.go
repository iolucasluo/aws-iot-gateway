package device

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateCommandsAgainstProductDPDefinitions(t *testing.T) {
	t.Parallel()
	definitions := []ProductDPDefinition{
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
	definitions := []ProductDPDefinition{{Code: "privacy_mode", Type: "object", Direction: "wo", Status: "active"}}
	err := ValidateCommands(definitions, []CommandInput{{Code: "privacy_mode", Value: json.RawMessage(`true`)}})
	if !errors.Is(err, ErrDPInvalidValue) {
		t.Fatalf("ValidateCommands() error = %v, want ErrDPInvalidValue", err)
	}
}

func TestValidateCommandsChecksRequiredObjectFieldsWhenValueIsOmitted(t *testing.T) {
	t.Parallel()
	definitions := []ProductDPDefinition{{
		Code: "feed", Type: "object", Direction: "wo", Status: "active",
		Spec: json.RawMessage(`{"required":["portions"]}`),
	}}
	err := ValidateCommands(definitions, []CommandInput{{Code: "feed"}})
	if !errors.Is(err, ErrDPInvalidValue) {
		t.Fatalf("ValidateCommands() error = %v, want ErrDPInvalidValue", err)
	}
}

func TestValidateCommandsRejectsLifecycleDPsFromGenericPath(t *testing.T) {
	t.Parallel()
	definitions := []ProductDPDefinition{
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
