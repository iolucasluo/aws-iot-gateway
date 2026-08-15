package binding

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
)

type recommissionCommanderStub struct {
	commands []device.CommandInput
	waitACK  bool
}

func (commander *recommissionCommanderStub) SendCommands(
	_ context.Context, _, _, _ string, commands []device.CommandInput, waitACK bool,
) (device.SendCommandsResult, error) {
	commander.commands = append([]device.CommandInput(nil), commands...)
	commander.waitACK = waitACK
	return device.SendCommandsResult{}, nil
}

func TestDeviceRecommissionerForwardsSupportedResetScopeAndWaitsForACK(t *testing.T) {
	t.Parallel()

	for _, resetScope := range []string{"runtime", "all"} {
		resetScope := resetScope
		t.Run(resetScope, func(t *testing.T) {
			t.Parallel()
			commander := &recommissionCommanderStub{}
			recommissioner, err := NewDeviceRecommissioner(commander)
			if err != nil {
				t.Fatalf("NewDeviceRecommissioner() error = %v", err)
			}
			if err := recommissioner.Reset(context.Background(), testBinding(), resetScope); err != nil {
				t.Fatalf("Reset(%s) error = %v", resetScope, err)
			}
			if len(commander.commands) != 1 || commander.commands[0].Code != "recommission" || !commander.waitACK {
				t.Fatalf("commands = %#v, waitACK = %t", commander.commands, commander.waitACK)
			}
			var value struct {
				ResetScope string `json:"reset_scope"`
			}
			if err := json.Unmarshal(commander.commands[0].Value, &value); err != nil || value.ResetScope != resetScope {
				t.Fatalf("command value = %s, error = %v", commander.commands[0].Value, err)
			}
		})
	}
}

func TestDeviceRecommissionerRejectsUnknownResetScope(t *testing.T) {
	t.Parallel()

	commander := &recommissionCommanderStub{}
	recommissioner, err := NewDeviceRecommissioner(commander)
	if err != nil {
		t.Fatalf("NewDeviceRecommissioner() error = %v", err)
	}
	if err := recommissioner.Reset(context.Background(), testBinding(), "factory"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Reset(factory) error = %v", err)
	}
	if len(commander.commands) != 0 {
		t.Fatalf("unknown scope published commands: %#v", commander.commands)
	}
}
