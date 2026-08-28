package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

var (
	ErrDPUnknown      = errors.New("device DP is not registered")
	ErrDPDisabled     = errors.New("device DP is disabled")
	ErrDPReadOnly     = errors.New("device DP is read-only")
	ErrDPControlled   = errors.New("device DP requires a controlled service path")
	ErrDPInvalidValue = errors.New("device DP value is invalid")
	ErrDPUnavailable  = errors.New("device DP contract is unavailable")
)

// ProductDPDefinition is the product-scoped command contract loaded from the
// Gateway-owned product_dps catalog.
type ProductDPDefinition struct {
	Code      string
	Type      string
	Direction string
	Spec      json.RawMessage
	Status    string
}

// DPDefinitionLoader resolves the active product contract for a registered device.
type DPDefinitionLoader interface {
	LoadProductDPDefinitions(context.Context, string) ([]ProductDPDefinition, error)
}

// ValidateCommands verifies command existence, lifecycle state, direction and
// the JSON shape described by the product DP catalog. It intentionally does not
// validate device ACKs; those remain part of the existing command state machine.
func ValidateCommands(definitions []ProductDPDefinition, commands []CommandInput) error {
	byCode := make(map[string]ProductDPDefinition, len(definitions))
	for _, definition := range definitions {
		code := strings.TrimSpace(definition.Code)
		if code != "" {
			byCode[code] = definition
		}
	}
	for _, command := range commands {
		code := strings.TrimSpace(command.Code)
		if isControlledDP(code) {
			return fmt.Errorf("%w: %w (%s)", ErrInvalidInput, ErrDPControlled, code)
		}
		definition, ok := byCode[code]
		if !ok {
			return fmt.Errorf("%w: %w (%s)", ErrInvalidInput, ErrDPUnknown, code)
		}
		if strings.EqualFold(strings.TrimSpace(definition.Status), "disabled") {
			return fmt.Errorf("%w: %w (%s)", ErrInvalidInput, ErrDPDisabled, code)
		}
		switch strings.ToLower(strings.TrimSpace(definition.Direction)) {
		case "wo", "rw", "downlink_request_ack":
		default:
			return fmt.Errorf("%w: %w (%s)", ErrInvalidInput, ErrDPReadOnly, code)
		}
		if err := validateDPValue(definition, command.Value); err != nil {
			return fmt.Errorf("%w: %w (%s): %v", ErrInvalidInput, ErrDPInvalidValue, code, err)
		}
	}
	return nil
}

func isControlledDP(code string) bool {
	switch code {
	case "factory_reset", "recommission", "reset_wifi":
		return true
	default:
		return false
	}
}

// SendDPCommands is the product-contract-aware command entry point used by
// generic HTTP commands. The legacy constructor intentionally keeps a nil
// loader as a compatibility mode for isolated unit tests and typed internal
// services; production wiring always supplies a loader.
func (service *Service) SendDPCommands(
	ctx context.Context,
	internalAppID, deviceID, deviceSN string,
	commands []CommandInput,
	waitACK bool,
) (SendCommandsResult, error) {
	if service.dpLoader == nil {
		return service.SendCommands(ctx, internalAppID, deviceID, deviceSN, commands, waitACK)
	}
	definitions, err := service.dpLoader.LoadProductDPDefinitions(ctx, deviceSN)
	if err != nil {
		return SendCommandsResult{}, fmt.Errorf("load product DP contract: %w", err)
	}
	if err := ValidateCommands(definitions, commands); err != nil {
		return SendCommandsResult{}, err
	}
	return service.SendCommands(ctx, internalAppID, deviceID, deviceSN, commands, waitACK)
}

func validateDPValue(definition ProductDPDefinition, raw json.RawMessage) error {
	typeName := strings.ToLower(strings.TrimSpace(definition.Type))
	if len(raw) == 0 {
		if typeName == "object" {
			return validateObjectSpec(map[string]any{}, definition.Spec)
		}
		if typeName == "raw" {
			return nil
		}
		return errors.New("value is required")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return errors.New("value is not valid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("value contains trailing JSON")
	}
	switch typeName {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return errors.New("value must be a JSON object")
		}
		return validateObjectSpec(object, definition.Spec)
	case "bool", "boolean":
		if _, ok := value.(bool); !ok {
			return errors.New("value must be boolean")
		}
	case "int", "integer":
		number, ok := value.(json.Number)
		if !ok {
			return errors.New("value must be an integer")
		}
		if _, err := number.Int64(); err != nil {
			return errors.New("value must be an integer")
		}
	case "float", "number":
		number, ok := value.(json.Number)
		if !ok {
			return errors.New("value must be a number")
		}
		parsed, err := number.Float64()
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return errors.New("value must be a finite number")
		}
	case "string":
		if _, ok := value.(string); !ok {
			return errors.New("value must be a string")
		}
	case "array":
		if _, ok := value.([]any); !ok {
			return errors.New("value must be a JSON array")
		}
	case "enum":
		if err := validateEnum(value, definition.Spec); err != nil {
			return err
		}
	case "raw", "":
		return nil
	default:
		return fmt.Errorf("unsupported DP type %q", definition.Type)
	}
	return nil
}

func validateObjectSpec(object map[string]any, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var spec struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
		AllowExtra *bool                     `json:"allow_extra"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return errors.New("DP spec is invalid")
	}
	for _, field := range spec.Required {
		if _, ok := object[field]; !ok {
			return fmt.Errorf("missing required field %q", field)
		}
	}
	for field, property := range spec.Properties {
		value, present := object[field]
		if !present {
			continue
		}
		if err := validateProperty(value, property); err != nil {
			return fmt.Errorf("field %q: %w", field, err)
		}
	}
	if spec.AllowExtra != nil && !*spec.AllowExtra {
		for field := range object {
			if _, ok := spec.Properties[field]; !ok {
				return fmt.Errorf("unexpected field %q", field)
			}
		}
	}
	return nil
}

func validateProperty(value any, property map[string]any) error {
	typeName, _ := property["type"].(string)
	definition := ProductDPDefinition{Type: typeName}
	if enum, ok := property["enum"]; ok {
		definition.Type = "enum"
		spec, _ := json.Marshal(map[string]any{"values": enum})
		definition.Spec = spec
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return errors.New("value cannot be encoded")
	}
	if err := validateDPValue(definition, raw); err != nil {
		return err
	}
	if err := validateNumericBounds(raw, typeName, property); err != nil {
		return err
	}
	return nil
}

func validateNumericBounds(raw json.RawMessage, typeName string, property map[string]any) error {
	if typeName != "integer" && typeName != "int" && typeName != "number" && typeName != "float" {
		return nil
	}
	number, err := json.Number(string(raw)).Float64()
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return errors.New("value must be a finite number")
	}
	if minimum, ok := propertyNumber(property["minimum"]); ok && number < minimum {
		return fmt.Errorf("value must be >= %v", minimum)
	}
	if maximum, ok := propertyNumber(property["maximum"]); ok && number > maximum {
		return fmt.Errorf("value must be <= %v", maximum)
	}
	return nil
}

func propertyNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func validateEnum(value any, raw json.RawMessage) error {
	var spec struct {
		Values []any `json:"values"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil || len(spec.Values) == 0 {
		return errors.New("enum spec is invalid")
	}
	for _, candidate := range spec.Values {
		if fmt.Sprint(candidate) == fmt.Sprint(value) {
			return nil
		}
	}
	return errors.New("value is outside enum")
}
