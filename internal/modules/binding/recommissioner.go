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

func (recommissioner *DeviceRecommissioner) Reset(ctx context.Context, binding Binding, resetScope string) error {
	if resetScope != "runtime" && resetScope != "all" {
		return ErrInvalidInput
	}
	value, err := json.Marshal(map[string]any{"reset_scope": resetScope})
	if err != nil {
		return fmt.Errorf("encode recommission command: %w", err)
	}
	_, err = recommissioner.commander.SendCommands(
		ctx, binding.InternalAppID, binding.DeviceID, binding.DeviceSN,
		[]device.CommandInput{{Code: "recommission", Value: value}}, true,
	)
	return err
}
