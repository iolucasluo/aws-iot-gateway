package operations

import (
	"context"
	"errors"
	"testing"
	"time"
)

const opsTestDeviceSN = "fdr-000000000000000000000000000000a1"

type stubRepository struct {
	deviceFilter      DeviceFilter
	bindingFilter     BindingFilter
	activationFilter  ActivationFilter
	auditFilter       AuditFilter
	tokenFilter       DeviceTokenFilter
	certificateFilter CertificateFilter
	err               error
}

func (stub *stubRepository) ListDevices(_ context.Context, filter DeviceFilter) ([]Device, error) {
	stub.deviceFilter = filter
	return nil, stub.err
}
func (stub *stubRepository) FindDevice(_ context.Context, deviceSN string) (Device, error) {
	if stub.err != nil {
		return Device{}, stub.err
	}
	return Device{DeviceSN: deviceSN}, nil
}
func (stub *stubRepository) ListBindings(_ context.Context, filter BindingFilter) ([]Binding, error) {
	stub.bindingFilter = filter
	return nil, stub.err
}
func (stub *stubRepository) ListActivations(_ context.Context, filter ActivationFilter) ([]Activation, error) {
	stub.activationFilter = filter
	return nil, stub.err
}
func (stub *stubRepository) ListAuditLogs(_ context.Context, filter AuditFilter) ([]AuditEntry, error) {
	stub.auditFilter = filter
	return nil, stub.err
}
func (stub *stubRepository) ListDeviceTokens(_ context.Context, filter DeviceTokenFilter) ([]DeviceToken, error) {
	stub.tokenFilter = filter
	return nil, stub.err
}
func (stub *stubRepository) ListCertificates(_ context.Context, filter CertificateFilter) ([]Certificate, error) {
	stub.certificateFilter = filter
	return nil, stub.err
}

func TestCursorRoundTripAndRejectsGarbage(t *testing.T) {
	t.Parallel()
	original := Cursor{CreatedAt: time.Date(2026, 9, 15, 12, 0, 0, 123456789, time.UTC), ID: "60000000-0000-0000-0000-000000000001"}
	decoded, ok := DecodeCursor(original.Encode())
	if !ok {
		t.Fatal("DecodeCursor() rejected an encoded cursor")
	}
	if !decoded.CreatedAt.Equal(original.CreatedAt) || decoded.ID != original.ID {
		t.Fatalf("decoded cursor = %+v, want %+v", decoded, original)
	}
	if empty, ok := DecodeCursor("  "); !ok || !empty.CreatedAt.IsZero() {
		t.Fatalf("DecodeCursor(empty) = %+v, %v; want zero cursor and ok", empty, ok)
	}
	for name, value := range map[string]string{
		"not-base64":   "!!!",
		"no-separator": "bm8tc2VwYXJhdG9y",
		"bad-time":     "bm90LWEtdGltZXxpZA",
		"missing-id":   "MjAyNi0wOS0xNVQxMjowMDowMFp8",
	} {
		if _, ok := DecodeCursor(value); ok {
			t.Errorf("DecodeCursor(%s) was accepted", name)
		}
	}
}

func TestQueryServiceNormalizesFiltersAndLimits(t *testing.T) {
	t.Parallel()
	repository := &stubRepository{}
	service, err := NewQueryService(repository)
	if err != nil {
		t.Fatalf("NewQueryService() error = %v", err)
	}
	ctx := context.Background()

	if _, err := service.ListDevices(ctx, DeviceFilter{PID: " feeder-x1 ", Query: " fdr- "}); err != nil {
		t.Fatalf("ListDevices() error = %v", err)
	}
	if repository.deviceFilter.PID != "feeder-x1" || repository.deviceFilter.Query != "fdr-" {
		t.Fatalf("device filter was not trimmed: %+v", repository.deviceFilter)
	}
	if repository.deviceFilter.Page.Limit != 50 {
		t.Fatalf("default limit = %d, want 50", repository.deviceFilter.Page.Limit)
	}
	if _, err := service.ListDevices(ctx, DeviceFilter{Page: Page{Limit: 5000}}); err != nil {
		t.Fatalf("ListDevices() error = %v", err)
	}
	if repository.deviceFilter.Page.Limit != 200 {
		t.Fatalf("capped limit = %d, want 200", repository.deviceFilter.Page.Limit)
	}
	if _, err := service.ListBindings(ctx, BindingFilter{Status: " active "}); err != nil {
		t.Fatalf("ListBindings() error = %v", err)
	}
	if repository.bindingFilter.Status != "active" {
		t.Fatalf("binding filter was not trimmed: %+v", repository.bindingFilter)
	}
	if _, err := service.ListActivations(ctx, ActivationFilter{PID: " feeder-x1 "}); err != nil {
		t.Fatalf("ListActivations() error = %v", err)
	}
	if repository.activationFilter.PID != "feeder-x1" {
		t.Fatalf("activation filter was not trimmed: %+v", repository.activationFilter)
	}
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if _, err := service.ListAuditLogs(ctx, AuditFilter{Operation: " POST auth.login ", From: &from}); err != nil {
		t.Fatalf("ListAuditLogs() error = %v", err)
	}
	if repository.auditFilter.Operation != "POST auth.login" || repository.auditFilter.From == nil {
		t.Fatalf("audit filter = %+v", repository.auditFilter)
	}

	if _, err := service.ListDeviceTokens(ctx, DeviceTokenFilter{DeviceSN: " " + opsTestDeviceSN + " "}); err != nil {
		t.Fatalf("ListDeviceTokens() error = %v", err)
	}
	if repository.tokenFilter.DeviceSN != opsTestDeviceSN || repository.tokenFilter.Page.Limit != 50 {
		t.Fatalf("token filter = %+v", repository.tokenFilter)
	}
	if _, err := service.ListCertificates(ctx, CertificateFilter{Status: " active "}); err != nil {
		t.Fatalf("ListCertificates() error = %v", err)
	}
	if repository.certificateFilter.Status != "active" || repository.certificateFilter.Page.Limit != 50 {
		t.Fatalf("certificate filter = %+v", repository.certificateFilter)
	}
	if _, err := service.ListDevices(ctx, DeviceFilter{SubjectID: " subject "}); err != nil {
		t.Fatalf("ListDevices() error = %v", err)
	}
	if repository.deviceFilter.SubjectID != "subject" {
		t.Fatalf("subject filter was not trimmed: %+v", repository.deviceFilter)
	}
	if _, err := service.FindDevice(ctx, "   "); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("FindDevice(blank) error = %v, want ErrInvalidQuery", err)
	}
	if _, err := NewQueryService(nil); err == nil {
		t.Fatal("NewQueryService(nil) was accepted")
	}
}
