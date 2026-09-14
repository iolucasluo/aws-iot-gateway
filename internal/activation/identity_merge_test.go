package activation

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	testIssuedDeviceSN = "EXF1-PACSNBZY-5"
	testChipUIDHex     = "b5eb3f00adc1"
)

func createInputWithIdentity(t *testing.T, method Method, deviceSN, chipUID string) CreateInput {
	t.Helper()
	input := testCreateInput(method)
	input.DeviceSN = deviceSN
	input.ChipUID = chipUID
	return input
}

func newPrepareInput(created Activation, method Method) PrepareInput {
	return PrepareInput{
		ActivationID:   created.ID,
		Code:           created.Code,
		CSRPEM:         "-----BEGIN CERTIFICATE REQUEST-----\nrequest\n-----END CERTIFICATE REQUEST-----",
		ExpectedMethod: method,
	}
}

func mustCreate(t *testing.T, service *Service, input CreateInput) Activation {
	t.Helper()
	created, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return created
}

func mustPrepare(t *testing.T, service *Service, input PrepareInput) Installation {
	t.Helper()
	installation, err := service.Prepare(context.Background(), input)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	return installation
}

// 三级来源 1：调用方（HMAC 内部 App）在 Create 提供 SN + chip UID。
func TestPrepareUsesCallerSuppliedDeviceIdentity(t *testing.T) {
	t.Parallel()

	provisioner := &deviceSNProvisioner{}
	service := NewServiceWith(provisioner)
	created := mustCreate(t, service, createInputWithIdentity(t, MethodBLE, testIssuedDeviceSN, testChipUIDHex))

	installation := mustPrepare(t, service, newPrepareInput(created, MethodBLE))
	if installation.DeviceID != testIssuedDeviceSN {
		t.Fatalf("installation device id = %q, want caller supplied %q", installation.DeviceID, testIssuedDeviceSN)
	}
	if provisioner.deviceSN != testIssuedDeviceSN {
		t.Fatalf("provisioned device sn = %q, want %q", provisioner.deviceSN, testIssuedDeviceSN)
	}
}

// 三级来源 2：调用方没给，设备在 prepare 上报。
func TestPrepareUsesDeviceReportedIdentityWhenCallerOmittedIt(t *testing.T) {
	t.Parallel()

	provisioner := &deviceSNProvisioner{}
	service := NewServiceWith(provisioner)
	created := mustCreate(t, service, testCreateInput(MethodBLE))

	input := newPrepareInput(created, MethodBLE)
	input.DeviceSN = testIssuedDeviceSN
	input.ChipUID = testChipUIDHex
	installation := mustPrepare(t, service, input)
	if installation.DeviceID != testIssuedDeviceSN {
		t.Fatalf("installation device id = %q, want device reported %q", installation.DeviceID, testIssuedDeviceSN)
	}
}

// 三级来源 3：两者都没有时保留原有兜底生成行为（兼容窗口）。
func TestPrepareFallsBackToGeneratedDeviceSN(t *testing.T) {
	t.Parallel()

	service := NewServiceWith(&deviceSNProvisioner{})
	created := mustCreate(t, service, testCreateInput(MethodAP))
	installation := mustPrepare(t, service, newPrepareInput(created, MethodAP))
	if !strings.HasPrefix(installation.DeviceID, "fdr-") {
		t.Fatalf("installation device id = %q, want generated fdr- prefix", installation.DeviceID)
	}
}

func TestCreateRejectsInvalidDeviceIdentity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		deviceSN string
		chipUID  string
		want     error
	}{
		{name: "bad sn checksum", deviceSN: "EXF1-12345678-0", want: ErrInvalidDeviceSN},
		{name: "bad sn prefix", deviceSN: "ABCD-12345678-0", want: ErrInvalidDeviceSN},
		{name: "bad chip uid", chipUID: "zzzz", want: ErrInvalidChipUID},
		{name: "odd chip uid", chipUID: "abc", want: ErrInvalidChipUID},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			service := NewService()
			_, err := service.Create(context.Background(),
				createInputWithIdentity(t, MethodBLE, testCase.deviceSN, testCase.chipUID))
			if !errors.Is(err, testCase.want) {
				t.Fatalf("Create() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestPrepareRejectsInvalidDeviceIdentity(t *testing.T) {
	t.Parallel()

	service := NewServiceWith(&deviceSNProvisioner{})
	created := mustCreate(t, service, testCreateInput(MethodQR))

	input := newPrepareInput(created, MethodQR)
	input.DeviceSN = "EXF1-12345678-0"
	if _, err := service.Prepare(context.Background(), input); !errors.Is(err, ErrInvalidDeviceSN) {
		t.Fatalf("Prepare() error = %v, want ErrInvalidDeviceSN", err)
	}
}

// 归并：同一 chip UID 的第二次激活必须复用同一 registry 行与同一 device_sn。
func TestPrepareReusesRegistryRowForSameChipUID(t *testing.T) {
	t.Parallel()

	service := NewServiceWith(&deviceSNProvisioner{})
	first := mustCreate(t, service, createInputWithIdentity(t, MethodBLE, "", testChipUIDHex))
	firstInstallation := mustPrepare(t, service, newPrepareInput(first, MethodBLE))

	secondInput := createInputWithIdentity(t, MethodBLE, "", testChipUIDHex)
	secondInput.IdempotencyKey = "repeat-activation-same-chip-uid"
	second := mustCreate(t, service, secondInput)
	secondInstallation := mustPrepare(t, service, newPrepareInput(second, MethodBLE))

	if firstInstallation.DeviceID != secondInstallation.DeviceID {
		t.Fatalf("device sn changed across activation: %q -> %q", firstInstallation.DeviceID, secondInstallation.DeviceID)
	}
	repository, ok := service.repository.(*MemoryRepository)
	if !ok {
		t.Fatal("service repository is not the memory repository")
	}
	firstRecord, err := repository.FindByID(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("FindByID(first) error = %v", err)
	}
	secondRecord, err := repository.FindByID(context.Background(), second.ID)
	if err != nil {
		t.Fatalf("FindByID(second) error = %v", err)
	}
	if firstRecord.DeviceInternalID != secondRecord.DeviceInternalID {
		t.Fatalf("registry row changed: %q -> %q", firstRecord.DeviceInternalID, secondRecord.DeviceInternalID)
	}
}

// 归并冲突：同一 chip UID 出现在不同产品上必须拒绝，而不是静默复用。
func TestPrepareRejectsChipUIDBoundToAnotherProduct(t *testing.T) {
	t.Parallel()

	service := NewServiceWith(&deviceSNProvisioner{})
	first := mustCreate(t, service, createInputWithIdentity(t, MethodBLE, "", testChipUIDHex))
	mustPrepare(t, service, newPrepareInput(first, MethodBLE))

	secondInput := createInputWithIdentity(t, MethodBLE, "", testChipUIDHex)
	secondInput.PID = "lamp-x1"
	secondInput.IdempotencyKey = "lamp-chip-uid-conflict"
	second := mustCreate(t, service, secondInput)

	if _, err := service.Prepare(context.Background(), newPrepareInput(second, MethodBLE)); !errors.Is(err, ErrDeviceIdentityConflict) {
		t.Fatalf("Prepare() error = %v, want ErrDeviceIdentityConflict", err)
	}
}

// 归并冲突：同一 device_sn 出现在不同 chip UID 上必须拒绝。
func TestPrepareRejectsDeviceSNBoundToAnotherChipUID(t *testing.T) {
	t.Parallel()

	service := NewServiceWith(&deviceSNProvisioner{})
	first := mustCreate(t, service, createInputWithIdentity(t, MethodBLE, testIssuedDeviceSN, "b5eb3f00adc1"))
	mustPrepare(t, service, newPrepareInput(first, MethodBLE))

	secondInput := createInputWithIdentity(t, MethodBLE, testIssuedDeviceSN, "b5eb3f00adc2")
	secondInput.IdempotencyKey = "second-chip-uid"
	second := mustCreate(t, service, secondInput)

	if _, err := service.Prepare(context.Background(), newPrepareInput(second, MethodBLE)); !errors.Is(err, ErrDeviceIdentityConflict) {
		t.Fatalf("Prepare() error = %v, want ErrDeviceIdentityConflict", err)
	}
}
