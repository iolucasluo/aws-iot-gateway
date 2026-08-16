package binding

import (
	"context"
	"encoding/json"
	"fmt"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
)

type DeviceCommander interface {
	SendCommands(context.Context, string, string, string, []device.CommandInput, bool) (device.SendCommandsResult, error)
}

type DeviceRecommissioner struct{ commander DeviceCommander }

func NewDeviceRecommissioner(commander DeviceCommander) (*DeviceRecommissioner, error) {
	if commander == nil {
		return nil, fmt.Errorf("device recommissioner requires a commander")
	}
	return &DeviceRecommissioner{commander: commander}, nil
}

func (recommissioner *DeviceRecommissioner) Reset(ctx context.Context, binding Binding, resetScope string) (ResetReceipt, error) {
	if resetScope != "runtime" && resetScope != "all" {
		return ResetReceipt{}, ErrInvalidInput
	}
	value, err := json.Marshal(map[string]any{"reset_scope": resetScope})
	if err != nil {
		return ResetReceipt{}, fmt.Errorf("encode recommission command: %w", err)
	}
	result, err := recommissioner.commander.SendCommands(
		ctx, binding.InternalAppID, binding.DeviceID, binding.DeviceSN,
		[]device.CommandInput{{Code: "recommission", Value: value}}, true,
	)
	if err != nil {
		return ResetReceipt{}, err
	}
	if len(result.Results) != 1 || result.Results[0].Code != "recommission" || result.Results[0].CommandID == "" {
		return ResetReceipt{}, fmt.Errorf("device rejected recommission command")
	}
	commandResult := result.Results[0]
	code, hasCode := ackCode(commandResult.Data)
	if !hasCode || (commandResult.Status == "success" && code != 0) ||
		(commandResult.Status == "error" && code == 0) ||
		(commandResult.Status != "success" && commandResult.Status != "error") {
		return ResetReceipt{}, fmt.Errorf("device returned an invalid recommission ACK")
	}
	return ResetReceipt{
		CommandID: commandResult.CommandID, Status: commandResult.Status, Data: commandResult.Data, Reason: commandResult.Reason,
	}, nil
}

func successfulACK(data map[string]any) bool {
	code, ok := ackCode(data)
	return ok && code == 0
}

func ackCode(data map[string]any) (int64, bool) {
	switch code := data["code"].(type) {
	case float64:
		return int64(code), code == float64(int64(code))
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
