package worker

import (
	"context"
	"errors"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
)

type reconciliationRepositoryStub struct {
	task       media.LocalReconciliationTask
	page       media.DeviceCatalogPage
	retryError string
}

func (repository *reconciliationRepositoryStub) ClaimLocalReconciliation(context.Context) (media.LocalReconciliationTask, error) {
	return repository.task, nil
}
func (repository *reconciliationRepositoryStub) ApplyLocalReconciliationPage(_ context.Context, _ media.LocalReconciliationTask, page media.DeviceCatalogPage) error {
	repository.page = page
	return nil
}
func (repository *reconciliationRepositoryStub) RetryLocalReconciliation(_ context.Context, _ string, message string) error {
	repository.retryError = message
	return nil
}

type reconciliationCommanderStub struct {
	input device.CommandInput
	data  map[string]any
}

func (commander *reconciliationCommanderStub) SendCommands(
	_ context.Context, _, _, _ string, inputs []device.CommandInput, wait bool,
) (device.SendCommandsResult, error) {
	if !wait || len(inputs) != 1 {
		return device.SendCommandsResult{}, errors.New("invalid command")
	}
	commander.input = inputs[0]
	return device.SendCommandsResult{Results: []device.CommandResult{{
		Code: "sd_recordings_v1", Status: "success",
		CommandID: "local-reconciliation-session", Data: commander.data,
	}}}, nil
}

func TestLocalReconciliationWorkerAppliesStrictCatalogPage(t *testing.T) {
	t.Parallel()
	task := media.LocalReconciliationTask{
		DeviceID: "device-id", DeviceSN: "fdr-0001", InternalAppID: "livpet-staging",
		Generation: "018f0000-0000-7000-8000-000000000090",
	}
	data := map[string]any{
		"action":     "sd_recordings_v1",
		"session_id": "local-reconciliation-session",
		"code":       float64(0),
		"contract":   "sd-recording-catalog.v1",
		"items": []any{map[string]any{
			"recording_id": "rec_0123456789abcdef0123456789abcdef",
			"revision":     "rev_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"start_at":     "2026-08-26T08:32:00Z", "end_at": "2026-08-26T08:33:00Z",
			"duration_ms": float64(60000), "size_bytes": float64(1024), "type": "event",
			"codec": "h264", "width": float64(1920), "height": float64(1080),
			"fps_num": float64(25), "fps_den": float64(1), "playable": true, "seekable": true,
			"thumbnail": map[string]any{
				"status":     "pending",
				"etag":       "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				"size_bytes": float64(1234),
			},
		}},
		"next_cursor": "rec_0123456789abcdef0123456789abcdef",
	}
	repository := &reconciliationRepositoryStub{task: task}
	commander := &reconciliationCommanderStub{data: data}
	worker, _ := NewLocalReconciliationWorker(repository, commander)
	if err := worker.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if commander.input.Code != "sd_recordings_v1" || len(repository.page.Items) != 1 ||
		repository.page.Items[0].RecordingID == "" || repository.retryError != "" {
		t.Fatalf("command=%#v page=%#v retry=%q", commander.input, repository.page, repository.retryError)
	}
}

func TestLocalReconciliationWorkerRejectsMismatchedACKSession(t *testing.T) {
	t.Parallel()
	task := media.LocalReconciliationTask{
		DeviceID: "device-id", DeviceSN: "fdr-0001", InternalAppID: "livpet-staging",
		Generation: "018f0000-0000-7000-8000-000000000090",
	}
	repository := &reconciliationRepositoryStub{task: task}
	commander := &reconciliationCommanderStub{data: map[string]any{
		"action": "sd_recordings_v1", "session_id": "wrong-session", "code": float64(0),
		"contract": "sd-recording-catalog.v1", "items": []any{}, "next_cursor": nil,
	}}
	worker, _ := NewLocalReconciliationWorker(repository, commander)
	if err := worker.PollOnce(context.Background()); err == nil || repository.retryError == "" {
		t.Fatalf("PollOnce() error = %v retry=%q", err, repository.retryError)
	}
}
