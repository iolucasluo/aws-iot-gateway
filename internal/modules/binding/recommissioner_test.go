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
	result   device.SendCommandsResult
	err      error
}

func (commander *recommissionCommanderStub) SendCommands(
	_ context.Context, _, _, _ string, commands []device.CommandInput, waitACK bool,
) (device.SendCommandsResult, error) {
	commander.commands = append([]device.CommandInput(nil), commands...)
	commander.waitACK = waitACK
	return commander.result, commander.err
}

func TestDeviceRecommissionerForwardsSupportedResetScopeAndWaitsForACK(t *testing.T) {
	t.Parallel()

	for _, resetScope := range []string{"runtime", "all"} {
		resetScope := resetScope
		t.Run(resetScope, func(t *testing.T) {
			t.Parallel()
			commander := &recommissionCommanderStub{result: successfulRecommissionResult()}
			recommissioner, err := NewDeviceRecommissioner(commander)
			if err != nil {
				t.Fatalf("NewDeviceRecommissioner() error = %v", err)
			}
			receipt, err := recommissioner.Reset(context.Background(), testBinding(), resetScope)
			if err != nil {
				t.Fatalf("Reset(%s) error = %v", resetScope, err)
			}
			if receipt.CommandID != "session-recommission" || receipt.Data["code"] != float64(0) {
				t.Fatalf("Reset(%s) receipt = %#v", resetScope, receipt)
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

func TestDeviceRecommissionerReturnsNonSuccessACKReceipt(t *testing.T) {
	t.Parallel()

	commander := &recommissionCommanderStub{result: device.SendCommandsResult{Results: []device.CommandResult{{
		Code: "recommission", Status: "error", CommandID: "session-recommission",
		Data: map[string]any{"code": float64(-1)},
	}}}}
	recommissioner, err := NewDeviceRecommissioner(commander)
	if err != nil {
		t.Fatalf("NewDeviceRecommissioner() error = %v", err)
	}
	receipt, err := recommissioner.Reset(context.Background(), testBinding(), "all")
	if err != nil {
		t.Fatalf("Reset(all) error = %v", err)
	}
	if receipt.Status != "error" || receipt.Data["code"] != float64(-1) {
		t.Fatalf("Reset(all) receipt = %#v", receipt)
	}
}

func TestDeviceRecommissionerRejectsSuccessResultWithoutZeroACKCode(t *testing.T) {
	t.Parallel()

	for _, data := range []map[string]any{
		{},
		{"code": float64(7)},
	} {
		commander := &recommissionCommanderStub{result: device.SendCommandsResult{Results: []device.CommandResult{{
			Code: "recommission", Status: "success", CommandID: "session-recommission", Data: data,
		}}}}
		recommissioner, err := NewDeviceRecommissioner(commander)
		if err != nil {
			t.Fatalf("NewDeviceRecommissioner() error = %v", err)
		}
		if _, err := recommissioner.Reset(context.Background(), testBinding(), "all"); err == nil {
			t.Fatalf("Reset(all) accepted invalid ACK data %#v", data)
		}
	}
}

func TestDeviceRecommissionerRejectsUnknownResetScope(t *testing.T) {
	t.Parallel()

	commander := &recommissionCommanderStub{}
	recommissioner, err := NewDeviceRecommissioner(commander)
	if err != nil {
		t.Fatalf("NewDeviceRecommissioner() error = %v", err)
	}
	if _, err := recommissioner.Reset(context.Background(), testBinding(), "factory"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Reset(factory) error = %v", err)
	}
	if len(commander.commands) != 0 {
		t.Fatalf("unknown scope published commands: %#v", commander.commands)
	}
}

func successfulRecommissionResult() device.SendCommandsResult {
	return device.SendCommandsResult{Results: []device.CommandResult{{
		Code: "recommission", Status: "success", CommandID: "session-recommission",
		Data: map[string]any{"code": float64(0), "reset_scope": "all", "will_reboot": true},
	}}}
}
