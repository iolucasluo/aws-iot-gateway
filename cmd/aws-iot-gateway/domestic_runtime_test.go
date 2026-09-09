package main

import (
	"context"
	"crypto/sha256"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/binding"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
)

func TestNewDomesticBindingServiceWiresRecommissioner(t *testing.T) {
	repository := &domesticBindingRepositoryStub{binding: binding.Binding{
		ID: "binding-1", DeviceID: "device-1", DeviceSN: "fdr-1",
		InternalAppID: "demo-local", Active: true,
	}}
	commander := &domesticCommanderStub{}
	service, err := newDomesticBindingService(repository, commander)
	if err != nil {
		t.Fatalf("newDomesticBindingService: %v", err)
	}

	result, err := service.Recommission(context.Background(), "demo-local", "token-1", "all")
	if err != nil {
		t.Fatalf("Recommission: %v", err)
	}
	if !repository.released || result.Binding.Active || commander.calls != 1 {
		t.Fatalf("released=%t active=%t commander_calls=%d", repository.released, result.Binding.Active, commander.calls)
	}
}

type domesticCommanderStub struct{ calls int }

func (stub *domesticCommanderStub) SendCommands(
	context.Context, string, string, string, []device.CommandInput, bool,
) (device.SendCommandsResult, error) {
	stub.calls++
	return device.SendCommandsResult{Results: []device.CommandResult{{
		Code: "recommission", Status: "success", CommandID: "session-1", Data: map[string]any{"code": float64(0)},
	}}}, nil
}

type domesticBindingRepositoryStub struct {
	binding  binding.Binding
	released bool
}

func (stub *domesticBindingRepositoryStub) FindActiveBinding(context.Context, string, string) (binding.Binding, error) {
	return stub.binding, nil
}

func (stub *domesticBindingRepositoryStub) StoreToken(context.Context, binding.DeviceToken) error {
	return nil
}

func (stub *domesticBindingRepositoryStub) ValidateToken(context.Context, string, [sha256.Size]byte, string) (binding.Binding, error) {
	return stub.binding, nil
}

func (stub *domesticBindingRepositoryStub) FindBindingByToken(context.Context, [sha256.Size]byte) (binding.Binding, error) {
	return stub.binding, nil
}

func (stub *domesticBindingRepositoryStub) ReleaseBinding(context.Context, string, string) (binding.Binding, error) {
	stub.released = true
	stub.binding.Active = false
	return stub.binding, nil
}

var _ binding.Repository = (*domesticBindingRepositoryStub)(nil)
var _ binding.DeviceCommander = (*domesticCommanderStub)(nil)
