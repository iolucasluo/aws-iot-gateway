package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
)

const (
	integrationProjectOwnerID = "10000000-0000-0000-0000-0000000000d1"
	integrationProjectID      = "50000000-0000-0000-0000-0000000000d1"
	integrationCredentialID   = "60000000-0000-0000-0000-0000000000d1"
)

func newProjectIntegrationRepository(t *testing.T) (*ProjectRepository, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	repository, err := NewProjectRepository(pool)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	return repository, pool
}

func seedProjectOwner(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	accounts, err := NewPortalAccountRepository(pool)
	if err != nil {
		t.Fatalf("NewPortalAccountRepository() error = %v", err)
	}
	if err := accounts.CreateAccount(ctx, portalaccount.Account{
		ID: integrationProjectOwnerID, Kind: portalaccount.KindDeveloper, Role: portalaccount.RoleDeveloper,
		Email: "dev-integration@example.com", Status: portalaccount.StatusActive,
		CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed project owner: %v", err)
	}
}

func TestProjectRepositoryRoundTripAndScopes(t *testing.T) {
	ctx := context.Background()
	repository, pool := newProjectIntegrationRepository(t)
	seedIntegrationManufacturer(t, ctx, pool)
	seedProjectOwner(t, ctx, pool)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	value := project.Project{
		ID: integrationProjectID, OwnerAccountID: integrationProjectOwnerID, Type: project.TypeOEM,
		Name: "OEM Line", Description: "factory line", ManufacturerID: integrationManufacturerID,
		Status: project.StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateProject(ctx, value); err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	loaded, err := repository.FindProjectByID(ctx, integrationProjectID)
	if err != nil {
		t.Fatalf("FindProjectByID() error = %v", err)
	}
	if loaded.Type != project.TypeOEM || loaded.ManufacturerID != integrationManufacturerID || loaded.Description != "factory line" {
		t.Fatalf("loaded project = %+v", loaded)
	}
	if _, err := repository.FindProjectByID(ctx, "50000000-0000-0000-0000-0000000000ff"); !errors.Is(err, project.ErrProjectNotFound) {
		t.Fatalf("FindProjectByID(unknown) error = %v, want ErrProjectNotFound", err)
	}

	// cloud 项目不得带厂商：由 CHECK 兜底。
	if err := repository.CreateProject(ctx, project.Project{
		ID: "50000000-0000-0000-0000-0000000000d9", OwnerAccountID: integrationProjectOwnerID, Type: project.TypeCloud,
		Name: "Cloud", ManufacturerID: integrationManufacturerID, Status: project.StatusActive, CreatedAt: now, UpdatedAt: now,
	}); err == nil {
		t.Fatal("CreateProject(cloud with manufacturer) was accepted despite the check constraint")
	}
	// oem 项目必须带厂商。
	if err := repository.CreateProject(ctx, project.Project{
		ID: "50000000-0000-0000-0000-0000000000da", OwnerAccountID: integrationProjectOwnerID, Type: project.TypeOEM,
		Name: "OEM without factory", Status: project.StatusActive, CreatedAt: now, UpdatedAt: now,
	}); err == nil {
		t.Fatal("CreateProject(oem without manufacturer) was accepted despite the check constraint")
	}

	cloud := project.Project{
		ID: "50000000-0000-0000-0000-0000000000d2", OwnerAccountID: integrationProjectOwnerID, Type: project.TypeCloud,
		Name: "Cloud", Status: project.StatusActive, CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute),
	}
	if err := repository.CreateProject(ctx, cloud); err != nil {
		t.Fatalf("CreateProject(cloud) error = %v", err)
	}
	byOwner, err := repository.ListProjects(ctx, project.ProjectFilter{OwnerAccountID: integrationProjectOwnerID})
	if err != nil || len(byOwner) != 2 {
		t.Fatalf("ListProjects(owner) = %d, err = %v; want 2", len(byOwner), err)
	}
	byManufacturer, err := repository.ListProjects(ctx, project.ProjectFilter{ManufacturerID: integrationManufacturerID})
	if err != nil || len(byManufacturer) != 1 || byManufacturer[0].ID != integrationProjectID {
		t.Fatalf("ListProjects(manufacturer) = %+v, err = %v", byManufacturer, err)
	}
	if cloudOnly, err := repository.ListProjects(ctx, project.ProjectFilter{Type: project.TypeCloud}); err != nil || len(cloudOnly) != 1 {
		t.Fatalf("ListProjects(cloud) = %d, err = %v; want 1", len(cloudOnly), err)
	}

	value.Name = "Renamed"
	value.Status = project.StatusDisabled
	value.UpdatedAt = now.Add(2 * time.Hour)
	if err := repository.UpdateProject(ctx, value); err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}
	updated, err := repository.FindProjectByID(ctx, integrationProjectID)
	if err != nil || updated.Name != "Renamed" || updated.Status != project.StatusDisabled {
		t.Fatalf("updated project = %+v, err = %v", updated, err)
	}
	if err := repository.UpdateProject(ctx, project.Project{ID: "50000000-0000-0000-0000-0000000000ff"}); !errors.Is(err, project.ErrProjectNotFound) {
		t.Fatalf("UpdateProject(unknown) error = %v, want ErrProjectNotFound", err)
	}
}

func TestProjectRepositoryProductWhiteList(t *testing.T) {
	ctx := context.Background()
	repository, pool := newProjectIntegrationRepository(t)
	seedProjectOwner(t, ctx, pool)
	applyProductSeeds(t, ctx, pool)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	if err := repository.CreateProject(ctx, project.Project{
		ID: integrationProjectID, OwnerAccountID: integrationProjectOwnerID, Type: project.TypeCloud,
		Name: "Cloud", Status: project.StatusActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	// 产品目录由 seed 提供：feeder-x1。
	var productID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM products WHERE pid='feeder-x1'`).Scan(&productID); err != nil {
		t.Fatalf("read seeded product: %v", err)
	}

	if err := repository.AddProjectProduct(ctx, "70000000-0000-0000-0000-0000000000d1", integrationProjectID, productID, now); err != nil {
		t.Fatalf("AddProjectProduct() error = %v", err)
	}
	if err := repository.AddProjectProduct(ctx, "70000000-0000-0000-0000-0000000000d2", integrationProjectID, productID, now); !errors.Is(err, project.ErrDuplicateProduct) {
		t.Fatalf("AddProjectProduct(duplicate) error = %v, want ErrDuplicateProduct", err)
	}
	if err := repository.AddProjectProduct(ctx, "70000000-0000-0000-0000-0000000000d3", integrationProjectID,
		"30000000-0000-0000-0000-0000000000ff", now); !errors.Is(err, project.ErrProductNotFound) {
		t.Fatalf("AddProjectProduct(unknown product) error = %v, want ErrProductNotFound", err)
	}
	if err := repository.AddProjectProduct(ctx, "70000000-0000-0000-0000-0000000000d4",
		"50000000-0000-0000-0000-0000000000ff", productID, now); !errors.Is(err, project.ErrProjectNotFound) {
		t.Fatalf("AddProjectProduct(unknown project) error = %v, want ErrProjectNotFound", err)
	}

	productIDs, err := repository.ListProjectProductIDs(ctx, integrationProjectID)
	if err != nil || len(productIDs) != 1 || productIDs[0] != productID {
		t.Fatalf("ListProjectProductIDs() = %v, err = %v", productIDs, err)
	}
	pids, err := repository.ListProjectPIDs(ctx, integrationProjectID)
	if err != nil || len(pids) != 1 || pids[0] != "feeder-x1" {
		t.Fatalf("ListProjectPIDs() = %v, err = %v", pids, err)
	}
	if err := repository.RemoveProjectProduct(ctx, integrationProjectID, productID); err != nil {
		t.Fatalf("RemoveProjectProduct() error = %v", err)
	}
	if err := repository.RemoveProjectProduct(ctx, integrationProjectID, productID); !errors.Is(err, project.ErrProductNotInProject) {
		t.Fatalf("RemoveProjectProduct(again) error = %v, want ErrProductNotInProject", err)
	}
}

func TestCredentialRepositoryRoundTripAndConstraints(t *testing.T) {
	ctx := context.Background()
	repository, pool := newProjectIntegrationRepository(t)
	seedProjectOwner(t, ctx, pool)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	if err := repository.CreateProject(ctx, project.Project{
		ID: integrationProjectID, OwnerAccountID: integrationProjectOwnerID, Type: project.TypeCloud,
		Name: "Cloud", Status: project.StatusActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	value := project.Credential{
		ID: integrationCredentialID, ProjectID: integrationProjectID, Name: "primary",
		APIKey: "ak_integration_primary", SecretCiphertext: []byte("0123456789abcdef0123456789abcdef"),
		KEKID: "kek-v1", Permissions: []string{"devices:read", "ota:deploy"},
		Status: project.StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateCredential(ctx, value); err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}
	// api_key 唯一。
	if err := repository.CreateCredential(ctx, project.Credential{
		ID: "60000000-0000-0000-0000-0000000000d2", ProjectID: integrationProjectID, Name: "duplicate",
		APIKey: "ak_integration_primary", SecretCiphertext: []byte("0123456789abcdef0123456789abcdef"),
		KEKID: "kek-v1", Status: project.StatusActive, CreatedAt: now, UpdatedAt: now,
	}); !errors.Is(err, project.ErrInvalidInput) {
		t.Fatalf("CreateCredential(duplicate key) error = %v, want ErrInvalidInput", err)
	}
	// 权限白名单由 CHECK 兜底。
	if err := repository.CreateCredential(ctx, project.Credential{
		ID: "60000000-0000-0000-0000-0000000000d3", ProjectID: integrationProjectID, Name: "bad permission",
		APIKey: "ak_integration_bad", SecretCiphertext: []byte("0123456789abcdef0123456789abcdef"),
		KEKID: "kek-v1", Permissions: []string{"devices:write"}, Status: project.StatusActive,
		CreatedAt: now, UpdatedAt: now,
	}); err == nil {
		t.Fatal("CreateCredential(unknown permission) was accepted despite the check constraint")
	}
	// 项目必须存在。
	if err := repository.CreateCredential(ctx, project.Credential{
		ID: "60000000-0000-0000-0000-0000000000d4", ProjectID: "50000000-0000-0000-0000-0000000000ff", Name: "orphan",
		APIKey: "ak_integration_orphan", SecretCiphertext: []byte("0123456789abcdef0123456789abcdef"),
		KEKID: "kek-v1", Status: project.StatusActive, CreatedAt: now, UpdatedAt: now,
	}); err == nil {
		t.Fatal("CreateCredential(unknown project) was accepted despite the foreign key")
	}

	byKey, err := repository.FindCredentialByAPIKey(ctx, "ak_integration_primary")
	if err != nil {
		t.Fatalf("FindCredentialByAPIKey() error = %v", err)
	}
	if byKey.ID != integrationCredentialID || byKey.KEKID != "kek-v1" || len(byKey.Permissions) != 2 {
		t.Fatalf("credential = %+v", byKey)
	}
	// 停用后的凭证仍然按 api_key 查得到：调用方需要区分"不存在"与"已停用"。
	byKey.Status = project.StatusDisabled
	byKey.UpdatedAt = now.Add(time.Hour)
	if err := repository.UpdateCredential(ctx, byKey); err != nil {
		t.Fatalf("UpdateCredential() error = %v", err)
	}
	disabled, err := repository.FindCredentialByAPIKey(ctx, "ak_integration_primary")
	if err != nil || disabled.Status != project.StatusDisabled {
		t.Fatalf("disabled credential = %+v, err = %v", disabled.Status, err)
	}
	if _, err := repository.FindCredentialByAPIKey(ctx, "ak_unknown"); !errors.Is(err, project.ErrCredentialNotFound) {
		t.Fatalf("FindCredentialByAPIKey(unknown) error = %v, want ErrCredentialNotFound", err)
	}
	if _, err := repository.FindCredentialByID(ctx, "60000000-0000-0000-0000-0000000000ff"); !errors.Is(err, project.ErrCredentialNotFound) {
		t.Fatalf("FindCredentialByID(unknown) error = %v, want ErrCredentialNotFound", err)
	}

	if err := repository.TouchCredentialUsed(ctx, integrationCredentialID, now.Add(2*time.Hour)); err != nil {
		t.Fatalf("TouchCredentialUsed() error = %v", err)
	}
	touched, err := repository.FindCredentialByID(ctx, integrationCredentialID)
	if err != nil || touched.LastUsedAt == nil {
		t.Fatalf("last_used_at was not persisted: %+v, err = %v", touched.LastUsedAt, err)
	}
	listed, err := repository.ListCredentials(ctx, integrationProjectID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListCredentials() = %d, err = %v; want 1", len(listed), err)
	}
	if err := repository.UpdateCredential(ctx, project.Credential{ID: "60000000-0000-0000-0000-0000000000ff"}); !errors.Is(err, project.ErrCredentialNotFound) {
		t.Fatalf("UpdateCredential(unknown) error = %v, want ErrCredentialNotFound", err)
	}
}

func TestProductRepositoryRoundTripAndProfileAdmission(t *testing.T) {
	ctx := context.Background()
	repository, pool := newProjectIntegrationRepository(t)
	seedProjectOwner(t, ctx, pool)
	applyProductSeeds(t, ctx, pool)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	// seed 已发布 feeder-x1 使用的 profile；用它证明 active 判定来自真实 dp_profiles。
	profiles, err := repository.ListActiveDPProfiles(ctx)
	if err != nil || len(profiles) == 0 {
		t.Fatalf("ListActiveDPProfiles() = %d, err = %v; want the seeded contract profiles", len(profiles), err)
	}
	active, err := repository.ActiveDPProfileExists(ctx, profiles[0].Code)
	if err != nil || !active {
		t.Fatalf("ActiveDPProfileExists(%s) = %v, %v; want true", profiles[0].Code, active, err)
	}
	if unknown, err := repository.ActiveDPProfileExists(ctx, "ghost-v1"); err != nil || unknown {
		t.Fatalf("ActiveDPProfileExists(ghost-v1) = %v, %v; want false", unknown, err)
	}

	account, err := repository.FindPortalAccount(ctx, integrationProjectOwnerID)
	if err != nil || account.Kind != portalaccount.KindDeveloper {
		t.Fatalf("FindPortalAccount() = %+v, err = %v", account.Kind, err)
	}
	if _, err := repository.FindPortalAccount(ctx, "10000000-0000-0000-0000-0000000000ff"); !errors.Is(err, project.ErrOwnerNotFound) {
		t.Fatalf("FindPortalAccount(unknown) error = %v, want ErrOwnerNotFound", err)
	}

	product := project.Product{
		ID: "30000000-0000-0000-0000-0000000000d1", PID: "feeder-x2", DPProfileCode: profiles[0].Code,
		Name: "Feeder X2", Status: project.StatusActive, Capabilities: []string{"webrtc.v1", "ota.app.v1"},
		OwnerAccountID: integrationProjectOwnerID, Category: "feeder", CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateProduct(ctx, product); err != nil {
		t.Fatalf("CreateProduct() error = %v", err)
	}
	// pid 唯一。
	if err := repository.CreateProduct(ctx, project.Product{
		ID: "30000000-0000-0000-0000-0000000000d2", PID: "feeder-x2", DPProfileCode: profiles[0].Code,
		Name: "Duplicate", Status: project.StatusActive, CreatedAt: now, UpdatedAt: now,
	}); !errors.Is(err, project.ErrInvalidInput) {
		t.Fatalf("CreateProduct(duplicate pid) error = %v, want ErrInvalidInput", err)
	}

	loaded, err := repository.FindProductByPID(ctx, "feeder-x2")
	if err != nil {
		t.Fatalf("FindProductByPID() error = %v", err)
	}
	if loaded.OwnerAccountID != integrationProjectOwnerID || loaded.Category != "feeder" ||
		len(loaded.Capabilities) != 2 || loaded.Capabilities[0] != "webrtc.v1" {
		t.Fatalf("loaded product = %+v", loaded)
	}
	if _, err := repository.FindProductByPID(ctx, "ghost-x1"); !errors.Is(err, project.ErrProductNotFound) {
		t.Fatalf("FindProductByPID(unknown) error = %v, want ErrProductNotFound", err)
	}

	owned, err := repository.ListProducts(ctx, project.ProductFilter{OwnerAccountID: integrationProjectOwnerID})
	if err != nil || len(owned) != 1 || owned[0].PID != "feeder-x2" {
		t.Fatalf("ListProducts(owner) = %+v, err = %v", owned, err)
	}
	// seed 的公共目录产品没有 owner，因此不会出现在按归属过滤的结果里。
	all, err := repository.ListProducts(ctx, project.ProductFilter{})
	if err != nil || len(all) != 4 {
		t.Fatalf("ListProducts(all) = %d, err = %v; want 4 (3 seeded + 1 created)", len(all), err)
	}

	loaded.Name = "Feeder X2 Pro"
	loaded.Capabilities = []string{"webrtc.v1"}
	loaded.Status = project.StatusDisabled
	loaded.OwnerAccountID = ""
	loaded.UpdatedAt = now.Add(time.Hour)
	if err := repository.UpdateProduct(ctx, loaded); err != nil {
		t.Fatalf("UpdateProduct() error = %v", err)
	}
	updated, err := repository.FindProductByPID(ctx, "feeder-x2")
	if err != nil || updated.Name != "Feeder X2 Pro" || updated.Status != project.StatusDisabled ||
		updated.OwnerAccountID != "" || len(updated.Capabilities) != 1 {
		t.Fatalf("updated product = %+v, err = %v", updated, err)
	}
	if err := repository.UpdateProduct(ctx, project.Product{ID: "30000000-0000-0000-0000-0000000000ff"}); !errors.Is(err, project.ErrProductNotFound) {
		t.Fatalf("UpdateProduct(unknown) error = %v, want ErrProductNotFound", err)
	}
}
