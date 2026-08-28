package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
)

type LocalReconciliationWorker struct {
	repository media.LocalReconciliationRepository
	commander  media.Commander
}

func NewLocalReconciliationWorker(
	repository media.LocalReconciliationRepository,
	commander media.Commander,
) (*LocalReconciliationWorker, error) {
	if repository == nil || commander == nil {
		return nil, fmt.Errorf("local reconciliation worker requires repository and commander")
	}
	return &LocalReconciliationWorker{repository: repository, commander: commander}, nil
}

func (worker *LocalReconciliationWorker) PollOnce(ctx context.Context) error {
	if worker == nil {
		return nil
	}
	task, err := worker.repository.ClaimLocalReconciliation(ctx)
	if errors.Is(err, media.ErrLocalReconciliationTaskNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	value := map[string]any{"op": "list", "limit": "10"}
	if task.Cursor != "" {
		value["cursor"] = task.Cursor
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return worker.retry(ctx, task, fmt.Errorf("encode local reconciliation command: %w", err))
	}
	result, err := worker.commander.SendCommands(
		ctx, task.InternalAppID, task.DeviceID, task.DeviceSN,
		[]device.CommandInput{{Code: "sd_recordings_v1", Value: payload}}, true,
	)
	if err != nil {
		return worker.retry(ctx, task, fmt.Errorf("request device recording catalog: %w", err))
	}
	if len(result.Results) != 1 || result.Results[0].Status != "success" {
		return worker.retry(ctx, task, fmt.Errorf("device rejected recording catalog request"))
	}
	page, err := media.ParseDeviceCatalogACK(
		result.Results[0].Data, result.Results[0].CommandID,
	)
	if err != nil {
		return worker.retry(ctx, task, err)
	}
	if err := worker.repository.ApplyLocalReconciliationPage(ctx, task, page); err != nil {
		return fmt.Errorf("apply local reconciliation page: %w", err)
	}
	return nil
}

func (worker *LocalReconciliationWorker) retry(
	ctx context.Context, task media.LocalReconciliationTask, cause error,
) error {
	if err := worker.repository.RetryLocalReconciliation(ctx, task.DeviceID, cause.Error()); err != nil {
		return fmt.Errorf("%v; persist reconciliation retry: %w", cause, err)
	}
	return cause
}
