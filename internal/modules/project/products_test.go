package project

import (
	"context"
	"errors"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
)

const testDPProfileCode = "feeder-v3"

func newProductFixture(t *testing.T) (*Service, *MemoryRepository) {
	t.Helper()
	service, repository, _ := newTestService(t)
	repository.AddDPProfile(testDPProfileCode, "Feeder v3")
	for _, id := range []string{"10000000-0000-0000-0000-0000000000a1", "10000000-0000-0000-0000-0000000000a2"} {
		repository.AddPortalAccount(portalaccount.Account{
			ID: id, Kind: portalaccount.KindDeveloper,
			Role: portalaccount.RoleDeveloper, Email: id + "@example.com", Status: portalaccount.StatusActive,
		})
	}
	return service, repository
}

func TestCreateProductRequiresActiveDPProfileAndValidOwner(t *testing.T) {
	t.Parallel()
	service, repository := newProductFixture(t)
	ctx := context.Background()
	owner := developerActor("10000000-0000-0000-0000-0000000000a1")

	created, err := service.CreateProduct(ctx, owner, CreateProductInput{
		PID: "feeder-x2", Name: " Feeder X2 ", DPProfileCode: testDPProfileCode,
		Capabilities: []string{"webrtc.v1", "webrtc.v1", "ota.app.v1"}, Category: "feeder",
	})
	if err != nil {
		t.Fatalf("CreateProduct() error = %v", err)
	}
	if created.OwnerAccountID != owner.AccountID || created.Status != StatusActive {
		t.Fatalf("created product = %+v", created)
	}
	if len(created.Capabilities) != 2 {
		t.Fatalf("capabilities = %v, want deduplicated", created.Capabilities)
	}
	if created.Name != "Feeder X2" {
		t.Fatalf("name = %q, want trimmed", created.Name)
	}

	for name, input := range map[string]CreateProductInput{
		"bad-pid":         {PID: "Feeder X2", Name: "x", DPProfileCode: testDPProfileCode},
		"missing-name":    {PID: "feeder-x3", DPProfileCode: testDPProfileCode},
		"missing-profile": {PID: "feeder-x3", Name: "x"},
		"unknown-profile": {PID: "feeder-x3", Name: "x", DPProfileCode: "ghost-v1"},
		"bad-capability":  {PID: "feeder-x3", Name: "x", DPProfileCode: testDPProfileCode, Capabilities: []string{"Bad Cap"}},
		"bad-category":    {PID: "feeder-x3", Name: "x", DPProfileCode: testDPProfileCode, Category: "Feeder"},
		"unknown-status":  {PID: "feeder-x3", Name: "x", DPProfileCode: testDPProfileCode, Status: "retired"},
	} {
		if _, err := service.CreateProduct(ctx, owner, input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("CreateProduct(%s) error = %v, want ErrInvalidInput", name, err)
		}
	}
	// 开发者替别人建产品属于越权；staff 指定不存在的归属才落到 ErrOwnerNotFound。
	if _, err := service.CreateProduct(ctx, owner, CreateProductInput{
		PID: "feeder-x3", Name: "x", DPProfileCode: testDPProfileCode, OwnerAccountID: "10000000-0000-0000-0000-0000000000ff",
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("CreateProduct(developer assigning another owner) error = %v, want ErrForbidden", err)
	}
	if _, err := service.CreateProduct(ctx, staffActor(), CreateProductInput{
		PID: "feeder-x3", Name: "x", DPProfileCode: testDPProfileCode, OwnerAccountID: "10000000-0000-0000-0000-0000000000ff",
	}); !errors.Is(err, ErrOwnerNotFound) {
		t.Fatalf("CreateProduct(unknown owner) error = %v, want ErrOwnerNotFound", err)
	}
	// 归属必须是 active developer。
	repository.AddPortalAccount(portalaccount.Account{
		ID: "10000000-0000-0000-0000-0000000000b1", Kind: portalaccount.KindStaff,
		Role: portalaccount.RoleSuperAdmin, Email: "staff@example.com", Status: portalaccount.StatusActive,
	})
	if _, err := service.CreateProduct(ctx, staffActor(), CreateProductInput{
		PID: "feeder-x3", Name: "x", DPProfileCode: testDPProfileCode, OwnerAccountID: "10000000-0000-0000-0000-0000000000b1",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateProduct(staff owner) error = %v, want ErrInvalidInput", err)
	}

	// 重复 pid 由仓库拒绝。
	if _, err := service.CreateProduct(ctx, owner, CreateProductInput{
		PID: "feeder-x2", Name: "duplicate", DPProfileCode: testDPProfileCode,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateProduct(duplicate pid) error = %v, want ErrInvalidInput", err)
	}
	// 厂商不能建产品。
	if _, err := service.CreateProduct(ctx, manufacturerActor(), CreateProductInput{
		PID: "feeder-x4", Name: "x", DPProfileCode: testDPProfileCode,
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("CreateProduct(manufacturer) error = %v, want ErrForbidden", err)
	}
}

func TestProductScopeAndOwnershipRules(t *testing.T) {
	t.Parallel()
	service, _ := newProductFixture(t)
	ctx := context.Background()
	owner := developerActor("10000000-0000-0000-0000-0000000000a1")
	other := developerActor("10000000-0000-0000-0000-0000000000a2")

	if _, err := service.CreateProduct(ctx, owner, CreateProductInput{
		PID: "feeder-x2", Name: "Feeder X2", DPProfileCode: testDPProfileCode, Capabilities: []string{"webrtc.v1"},
	}); err != nil {
		t.Fatalf("CreateProduct() error = %v", err)
	}
	if _, err := service.CreateProduct(ctx, other, CreateProductInput{
		PID: "lamp-x2", Name: "Lamp X2", DPProfileCode: testDPProfileCode,
	}); err != nil {
		t.Fatalf("CreateProduct(other) error = %v", err)
	}
	// staff 建公共目录产品（无 owner）。
	catalog, err := service.CreateProduct(ctx, staffActor(), CreateProductInput{
		PID: "minion-cam-x2", Name: "Catalog Cam", DPProfileCode: testDPProfileCode,
	})
	if err != nil {
		t.Fatalf("CreateProduct(catalog) error = %v", err)
	}
	if catalog.OwnerAccountID != "" {
		t.Fatalf("catalog product owner = %q, want empty", catalog.OwnerAccountID)
	}

	mine, err := service.ListProducts(ctx, owner, ProductFilter{})
	if err != nil || len(mine) != 2 || mine[0].PID != "feeder-x2" || mine[1].PID != "minion-cam-x2" {
		t.Fatalf("ListProducts(developer) = %+v, err = %v; want own + catalog", mine, err)
	}
	all, err := service.ListProducts(ctx, staffActor(), ProductFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("ListProducts(staff) = %d, err = %v; want 3", len(all), err)
	}
	if _, err := service.ListProducts(ctx, manufacturerActor(), ProductFilter{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ListProducts(manufacturer) error = %v, want ErrForbidden", err)
	}

	if _, err := service.GetProduct(ctx, other, "feeder-x2"); !errors.Is(err, ErrProductNotFound) {
		t.Fatalf("GetProduct(other developer) error = %v, want ErrProductNotFound", err)
	}
	if _, err := service.UpdateProduct(ctx, other, "feeder-x2", ProductPatch{Status: stringPointer(StatusDisabled)}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("UpdateProduct(other developer) error = %v, want ErrForbidden", err)
	}
	// 开发者即使显式传 owner 也不能转移归属。
	if _, err := service.UpdateProduct(ctx, owner, "feeder-x2", ProductPatch{OwnerAccountID: stringPointer(other.AccountID)}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("UpdateProduct(developer reassign) error = %v, want ErrForbidden", err)
	}

	updated, err := service.UpdateProduct(ctx, owner, "feeder-x2", ProductPatch{
		Name: stringPointer("Feeder X2 Pro"), Capabilities: &[]string{"webrtc.v1", "local-media.v1"},
	})
	if err != nil {
		t.Fatalf("UpdateProduct() error = %v", err)
	}
	if updated.Name != "Feeder X2 Pro" || len(updated.Capabilities) != 2 || updated.PID != "feeder-x2" {
		t.Fatalf("updated product = %+v", updated)
	}
	if _, err := service.UpdateProduct(ctx, owner, "feeder-x2", ProductPatch{Name: stringPointer("  ")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("UpdateProduct(blank name) error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.UpdateProduct(ctx, owner, "feeder-x2", ProductPatch{
		Capabilities: &[]string{"not a capability"},
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("UpdateProduct(bad capability) error = %v, want ErrInvalidInput", err)
	}

	// staff 可以改任意产品并把目录产品归属给开发者。
	reassigned, err := service.UpdateProduct(ctx, staffActor(), "minion-cam-x2", ProductPatch{
		OwnerAccountID: stringPointer(owner.AccountID), Status: stringPointer(StatusDisabled),
	})
	if err != nil {
		t.Fatalf("UpdateProduct(staff reassign) error = %v", err)
	}
	if reassigned.OwnerAccountID != owner.AccountID || reassigned.Status != StatusDisabled {
		t.Fatalf("reassigned product = %+v", reassigned)
	}
	if _, err := service.UpdateProduct(ctx, staffActor(), "minion-cam-x2", ProductPatch{
		OwnerAccountID: stringPointer("10000000-0000-0000-0000-0000000000ff"),
	}); !errors.Is(err, ErrOwnerNotFound) {
		t.Fatalf("UpdateProduct(unknown owner) error = %v, want ErrOwnerNotFound", err)
	}
}

func TestListActiveDPProfilesIsSortedAndReadOnly(t *testing.T) {
	t.Parallel()
	service, repository := newProductFixture(t)
	repository.AddDPProfile("camera-v2", "Camera v2")
	profiles, err := service.ListActiveDPProfiles(context.Background())
	if err != nil || len(profiles) != 2 {
		t.Fatalf("ListActiveDPProfiles() = %+v, err = %v", profiles, err)
	}
	if profiles[0].Code != "camera-v2" || profiles[1].Code != testDPProfileCode {
		t.Fatalf("profiles = %+v, want sorted by code", profiles)
	}
}
