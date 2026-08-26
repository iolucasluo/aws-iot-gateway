package activation

import (
	"context"
	"sort"
	"sync"
)

type MemoryRepository struct {
	mu       sync.Mutex
	records  map[string]Record
	products map[string]Product
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		records: make(map[string]Record),
		products: map[string]Product{
			"feeder-x1": {
				PID: "feeder-x1", Name: "Feeder X1",
				Capabilities: []string{"webrtc.v1", "local-media.v1", "ptz.v1"},
			},
			"lamp-x1": {
				PID: "lamp-x1", Name: "Lamp X1",
				Capabilities: []string{},
			},
			"minion-cam-x1": {
				PID: "minion-cam-x1", Name: "Minion Cam X1",
				Capabilities: []string{"webrtc.v1"},
			},
		},
	}
}

func (repository *MemoryRepository) FindActiveProduct(_ context.Context, pid string) (Product, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	product, found := repository.products[pid]
	if !found {
		return Product{}, ErrProductUnavailable
	}
	return product, nil
}

func (repository *MemoryRepository) ListActiveProducts(_ context.Context) ([]Product, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	products := make([]Product, 0, len(repository.products))
	for _, product := range repository.products {
		products = append(products, product)
	}
	sort.Slice(products, func(left, right int) bool { return products[left].PID < products[right].PID })
	return products, nil
}

func (repository *MemoryRepository) Create(_ context.Context, record Record) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for _, existing := range repository.records {
		if existing.Activation.InternalAppID == record.Activation.InternalAppID &&
			existing.Activation.IdempotencyKey == record.Activation.IdempotencyKey {
			if existing.Activation.RequestHash != record.Activation.RequestHash {
				return ErrIdempotencyConflict
			}
			return ErrActivationInProgress
		}
	}
	repository.records[record.Activation.ID] = record
	return nil
}

func (repository *MemoryRepository) FindByID(_ context.Context, id string) (Record, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	record, found := repository.records[id]
	if !found {
		return Record{}, ErrActivationNotFound
	}
	return record, nil
}

func (repository *MemoryRepository) FindByIdempotency(_ context.Context, internalAppID, key string) (Record, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for _, record := range repository.records {
		if record.Activation.InternalAppID == internalAppID && record.Activation.IdempotencyKey == key {
			return record, nil
		}
	}
	return Record{}, ErrActivationNotFound
}

func (repository *MemoryRepository) ReservePreparation(_ context.Context, activationID, deviceInternalID, deviceSN, csrPEM string) (Record, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	record, found := repository.records[activationID]
	if !found {
		return Record{}, ErrActivationNotFound
	}
	switch record.Activation.Status {
	case StatusPending:
		record.Activation.Status = StatusActivating
		record.Activation.DeviceSN = deviceSN
		record.DeviceInternalID = deviceInternalID
		record.CSRPEM = csrPEM
		repository.records[activationID] = record
		return record, nil
	case StatusActivating:
		if record.CSRPEM == csrPEM {
			return Record{}, ErrPreparationInProgress
		}
	case StatusInstalling, StatusActivated:
		if record.CSRPEM == csrPEM && record.Installation.ActivationID != "" {
			return record, nil
		}
	}
	return Record{}, ErrActivationAlreadyPrepared
}

func (repository *MemoryRepository) CompletePreparation(_ context.Context, activationID string, installation Installation) (Record, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	record, found := repository.records[activationID]
	if !found {
		return Record{}, ErrActivationNotFound
	}
	if record.Activation.Status != StatusActivating || record.Activation.DeviceSN != installation.DeviceID {
		return Record{}, ErrActivationAlreadyPrepared
	}
	record.Installation = installation
	record.Activation.Status = StatusInstalling
	repository.records[activationID] = record
	return record, nil
}

func (repository *MemoryRepository) FailPreparation(_ context.Context, activationID, failureCode string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	record, found := repository.records[activationID]
	if !found {
		return ErrActivationNotFound
	}
	record.Activation.Status = StatusFailed
	record.FailureCode = failureCode
	repository.records[activationID] = record
	return nil
}

func (repository *MemoryRepository) CompleteActivation(_ context.Context, activationID, _ string) (Record, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	record, found := repository.records[activationID]
	if !found {
		return Record{}, ErrActivationNotFound
	}
	if record.Activation.Status != StatusInstalling {
		return Record{}, ErrActivationAlreadyPrepared
	}
	record.Activation.Status = StatusActivated
	repository.records[activationID] = record
	return record, nil
}

func (repository *MemoryRepository) ListDevices(_ context.Context, internalAppID, externalUserID string) ([]Device, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	devices := make([]Device, 0)
	for _, record := range repository.records {
		if record.Activation.InternalAppID != internalAppID ||
			(externalUserID != "" && record.Activation.BindingExternalUserID != externalUserID) ||
			record.Activation.Status != StatusActivated || record.Installation.DeviceID == "" {
			continue
		}
		devices = append(devices, Device{
			ID: record.Installation.DeviceID, ActivationID: record.Activation.ID, PID: record.Activation.PID, Endpoint: record.Installation.Endpoint,
			BindingExternalUserID: record.Activation.BindingExternalUserID, Status: "provisioned",
		})
	}
	sort.Slice(devices, func(left, right int) bool { return devices[left].ID < devices[right].ID })
	return devices, nil
}
