package activation

import (
	"context"
	"encoding/hex"
	"sort"
	"sync"
)

type MemoryRepository struct {
	mu       sync.Mutex
	records  map[string]Record
	devices  map[string]*memoryDeviceRow
	products map[string]Product
	profiles map[string]DPProfile
}

// memoryDeviceRow 对应 device_registry 的一行，按 chip UID 归并。
type memoryDeviceRow struct {
	ID       string
	DeviceSN string
	PID      string
	ChipUID  string // hex；空表示未知
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		records: make(map[string]Record),
		devices: make(map[string]*memoryDeviceRow),
		profiles: map[string]DPProfile{
			"feeder-v1": {Code: "feeder-v1", Name: "Anyka Feeder X1 v1"},
			"camera-v1": {Code: "camera-v1", Name: "Taixin Minion Cam X1 v1"},
			"lamp-v1":   {Code: "lamp-v1", Name: "Anyka Lamp X1 v1"},
		},
		products: map[string]Product{
			"feeder-x1": {
				PID: "feeder-x1", DPProfile: "feeder-v1", Name: "Feeder X1",
				Capabilities: []string{"webrtc.v1", "local-media.v1", "ptz.v1"},
			},
			"lamp-x1": {
				PID: "lamp-x1", DPProfile: "lamp-v1", Name: "Lamp X1",
				Capabilities: []string{},
			},
			"minion-cam-x1": {
				PID: "minion-cam-x1", DPProfile: "camera-v1", Name: "Minion Cam X1",
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
	product, ok := repository.products[record.Activation.PID]
	if !ok {
		return ErrProductUnavailable
	}
	if _, ok := repository.profiles[record.Activation.DPProfile]; !ok || product.DPProfile != record.Activation.DPProfile {
		return ErrDPProfileUnavailable
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

func (repository *MemoryRepository) ReservePreparation(_ context.Context, reservation Reservation) (Record, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	record, found := repository.records[reservation.ActivationID]
	if !found {
		return Record{}, ErrActivationNotFound
	}
	switch record.Activation.Status {
	case StatusPending:
		row, err := repository.reserveDeviceRow(reservation, record.Activation.PID)
		if err != nil {
			return Record{}, err
		}
		record.Activation.Status = StatusActivating
		record.Activation.DeviceSN = row.DeviceSN
		record.DeviceInternalID = row.ID
		record.CSRPEM = reservation.CSRPEM
		repository.records[reservation.ActivationID] = record
		return record, nil
	case StatusActivating:
		if record.CSRPEM == reservation.CSRPEM {
			return Record{}, ErrPreparationInProgress
		}
	case StatusInstalling, StatusActivated:
		if record.CSRPEM == reservation.CSRPEM && record.Installation.ActivationID != "" {
			return record, nil
		}
	}
	return Record{}, ErrActivationAlreadyPrepared
}

// reserveDeviceRow 实现与 PostgreSQL 实现一致的 chip UID 归并语义：
// 同 UID 复用行与 device_sn，跨产品或不一致的外部 SN 一律拒绝。
func (repository *MemoryRepository) reserveDeviceRow(reservation Reservation, pid string) (*memoryDeviceRow, error) {
	chipUID := ""
	if len(reservation.ChipUID) > 0 {
		chipUID = hex.EncodeToString(reservation.ChipUID)
	}
	if chipUID != "" {
		for _, row := range repository.devices {
			if row.ChipUID != chipUID {
				continue
			}
			if row.PID != pid {
				return nil, ErrDeviceIdentityConflict
			}
			if !reservation.DeviceSNGenerated && row.DeviceSN != reservation.DeviceSN {
				return nil, ErrDeviceIdentityConflict
			}
			return row, nil
		}
	}
	if reservation.DeviceSN != "" {
		for _, row := range repository.devices {
			if row.DeviceSN != reservation.DeviceSN {
				continue
			}
			if row.PID != pid {
				return nil, ErrDeviceIdentityConflict
			}
			if chipUID != "" && row.ChipUID != "" && row.ChipUID != chipUID {
				return nil, ErrDeviceIdentityConflict
			}
			if chipUID != "" {
				row.ChipUID = chipUID
			}
			return row, nil
		}
	}
	row := &memoryDeviceRow{
		ID: reservation.DeviceInternalID, DeviceSN: reservation.DeviceSN,
		PID: pid, ChipUID: chipUID,
	}
	repository.devices[row.ID] = row
	return row, nil
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

func (repository *MemoryRepository) FindActiveDPProfile(_ context.Context, code string) (DPProfile, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	profile, ok := repository.profiles[code]
	if !ok {
		return DPProfile{}, ErrDPProfileUnavailable
	}
	return profile, nil
}

func (repository *MemoryRepository) ListActiveDPProfiles(_ context.Context) ([]DPProfile, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	profiles := make([]DPProfile, 0, len(repository.profiles))
	for _, profile := range repository.profiles {
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Code < profiles[j].Code })
	return profiles, nil
}
