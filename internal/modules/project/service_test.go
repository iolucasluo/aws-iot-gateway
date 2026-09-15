package project

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/internalauth"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/credentialcipher"
)

const (
	testManufacturerID = "20000000-0000-0000-0000-0000000000f1"
	testProductID      = "30000000-0000-0000-0000-0000000000f1"
	testProductPID     = "feeder-x1"
)

type testClock struct{ moment time.Time }

func (clock *testClock) now() time.Time          { return clock.moment }
func (clock *testClock) advance(d time.Duration) { clock.moment = clock.moment.Add(d) }

func newTestService(t *testing.T) (*Service, *MemoryRepository, *testClock) {
	t.Helper()
	clock := &testClock{moment: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	key := make([]byte, credentialcipher.KeyBytes)
	cipher, err := credentialcipher.New(key, "kek-v1")
	if err != nil {
		t.Fatalf("credentialcipher.New() error = %v", err)
	}
	repository := NewMemoryRepository()
	repository.AddManufacturer(testManufacturerID)
	repository.AddProduct(testProductID, testProductPID)
	service, err := NewService(repository, cipher, clock.now)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, repository, clock
}

func staffActor() Actor {
	return Actor{AccountID: "10000000-0000-0000-0000-0000000000f1", Kind: portalaccount.KindStaff, Role: portalaccount.RoleSuperAdmin}
}

func developerActor(id string) Actor {
	return Actor{AccountID: id, Kind: portalaccount.KindDeveloper, Role: portalaccount.RoleDeveloper}
}

func manufacturerActor() Actor {
	return Actor{AccountID: "10000000-0000-0000-0000-0000000000f2", Kind: portalaccount.KindManufacturer,
		Role: portalaccount.RoleManufacturerAdmin, ManufacturerID: testManufacturerID}
}

func TestCreateProjectValidatesTypeAndManufacturer(t *testing.T) {
	t.Parallel()
	service, _, _ := newTestService(t)
	ctx := context.Background()
	owner := developerActor("10000000-0000-0000-0000-0000000000a1")

	cloud, err := service.CreateProject(ctx, owner, CreateProjectInput{Type: TypeCloud, Name: " Cloud Project "})
	if err != nil {
		t.Fatalf("CreateProject(cloud) error = %v", err)
	}
	if cloud.Name != "Cloud Project" || cloud.OwnerAccountID != owner.AccountID || cloud.Status != StatusActive {
		t.Fatalf("CreateProject(cloud) = %+v", cloud)
	}
	oem, err := service.CreateProject(ctx, owner, CreateProjectInput{
		Type: TypeOEM, Name: "OEM Project", ManufacturerID: testManufacturerID,
	})
	if err != nil {
		t.Fatalf("CreateProject(oem) error = %v", err)
	}
	if oem.ManufacturerID != testManufacturerID {
		t.Fatalf("CreateProject(oem) manufacturer = %q", oem.ManufacturerID)
	}

	for name, input := range map[string]CreateProjectInput{
		"missing-name":        {Type: TypeCloud},
		"unknown-type":        {Type: "reseller", Name: "x"},
		"oem-without-factory": {Type: TypeOEM, Name: "x"},
		"cloud-with-factory":  {Type: TypeCloud, Name: "x", ManufacturerID: testManufacturerID},
	} {
		if _, err := service.CreateProject(ctx, owner, input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("CreateProject(%s) error = %v, want ErrInvalidInput", name, err)
		}
	}
	if _, err := service.CreateProject(ctx, owner, CreateProjectInput{
		Type: TypeOEM, Name: "x", ManufacturerID: "20000000-0000-0000-0000-0000000000ff",
	}); !errors.Is(err, ErrManufacturerNotFound) {
		t.Fatalf("CreateProject(unknown manufacturer) error = %v, want ErrManufacturerNotFound", err)
	}
	// 开发者不能替别人建项目；厂商不能建项目。
	if _, err := service.CreateProject(ctx, owner, CreateProjectInput{
		Type: TypeCloud, Name: "x", OwnerAccountID: "10000000-0000-0000-0000-0000000000b2",
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("CreateProject(other owner) error = %v, want ErrForbidden", err)
	}
	if _, err := service.CreateProject(ctx, manufacturerActor(), CreateProjectInput{Type: TypeCloud, Name: "x"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("CreateProject(manufacturer) error = %v, want ErrForbidden", err)
	}
}

func TestProjectScopeConfinesReadsAndWrites(t *testing.T) {
	t.Parallel()
	service, _, _ := newTestService(t)
	ctx := context.Background()
	first := developerActor("10000000-0000-0000-0000-0000000000a1")
	second := developerActor("10000000-0000-0000-0000-0000000000a2")

	mine, err := service.CreateProject(ctx, first, CreateProjectInput{Type: TypeCloud, Name: "Mine"})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	oem, err := service.CreateProject(ctx, first, CreateProjectInput{Type: TypeOEM, Name: "OEM", ManufacturerID: testManufacturerID})
	if err != nil {
		t.Fatalf("CreateProject(oem) error = %v", err)
	}
	if _, err := service.CreateProject(ctx, second, CreateProjectInput{Type: TypeCloud, Name: "Theirs"}); err != nil {
		t.Fatalf("CreateProject(other) error = %v", err)
	}

	visible, err := service.ListProjects(ctx, first, ProjectFilter{})
	if err != nil || len(visible) != 2 {
		t.Fatalf("ListProjects(developer) = %d, err = %v; want 2", len(visible), err)
	}
	for _, item := range visible {
		if item.OwnerAccountID != first.AccountID {
			t.Fatalf("ListProjects(developer) leaked %+v", item)
		}
	}
	if all, err := service.ListProjects(ctx, staffActor(), ProjectFilter{}); err != nil || len(all) != 3 {
		t.Fatalf("ListProjects(staff) = %d, err = %v; want 3", len(all), err)
	}
	manufacturerView, err := service.ListProjects(ctx, manufacturerActor(), ProjectFilter{})
	if err != nil || len(manufacturerView) != 1 || manufacturerView[0].ID != oem.ID {
		t.Fatalf("ListProjects(manufacturer) = %+v, err = %v", manufacturerView, err)
	}

	if _, err := service.GetProject(ctx, second, mine.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("GetProject(other developer) error = %v, want ErrProjectNotFound", err)
	}
	if _, err := service.UpdateProject(ctx, second, mine.ID, ProjectPatch{Status: stringPointer(StatusDisabled)}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("UpdateProject(other developer) error = %v, want ErrForbidden", err)
	}
	if _, err := service.UpdateProject(ctx, manufacturerActor(), oem.ID, ProjectPatch{Status: stringPointer(StatusDisabled)}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("UpdateProject(manufacturer) error = %v, want ErrForbidden", err)
	}
	if _, err := service.UpdateProject(ctx, first, mine.ID, ProjectPatch{Status: stringPointer("archived")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("UpdateProject(unknown status) error = %v, want ErrInvalidInput", err)
	}
	disabled, err := service.UpdateProject(ctx, first, mine.ID, ProjectPatch{Status: stringPointer(StatusDisabled)})
	if err != nil || disabled.Status != StatusDisabled {
		t.Fatalf("UpdateProject(disable) = %+v, err = %v", disabled, err)
	}
}

func TestProjectProductWhiteList(t *testing.T) {
	t.Parallel()
	service, _, _ := newTestService(t)
	ctx := context.Background()
	owner := developerActor("10000000-0000-0000-0000-0000000000a1")
	other := developerActor("10000000-0000-0000-0000-0000000000a2")
	created, err := service.CreateProject(ctx, owner, CreateProjectInput{Type: TypeCloud, Name: "Mine"})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	if _, err := service.AttachProduct(ctx, other, created.ID, testProductID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("AttachProduct(other developer) error = %v, want ErrForbidden", err)
	}
	if _, err := service.AttachProduct(ctx, owner, created.ID, "30000000-0000-0000-0000-0000000000ff"); !errors.Is(err, ErrProductNotFound) {
		t.Fatalf("AttachProduct(unknown product) error = %v, want ErrProductNotFound", err)
	}
	if _, err := service.AttachProduct(ctx, owner, created.ID, testProductID); err != nil {
		t.Fatalf("AttachProduct() error = %v", err)
	}
	if _, err := service.AttachProduct(ctx, owner, created.ID, testProductID); !errors.Is(err, ErrDuplicateProduct) {
		t.Fatalf("AttachProduct(duplicate) error = %v, want ErrDuplicateProduct", err)
	}

	productIDs, pids, err := service.ListProjectProducts(ctx, owner, created.ID)
	if err != nil || len(productIDs) != 1 || len(pids) != 1 || pids[0] != testProductPID {
		t.Fatalf("ListProjectProducts() = %v / %v, err = %v", productIDs, pids, err)
	}
	scope, err := service.ScopeFor(ctx, owner, created.ID)
	if err != nil || len(scope.AllowedPIDs) != 1 || scope.AllowedPIDs[0] != testProductPID {
		t.Fatalf("ScopeFor() = %+v, err = %v", scope, err)
	}
	if _, err := service.DetachProduct(ctx, owner, created.ID, testProductID); err != nil {
		t.Fatalf("DetachProduct() error = %v", err)
	}
	if _, err := service.DetachProduct(ctx, owner, created.ID, testProductID); !errors.Is(err, ErrProductNotInProject) {
		t.Fatalf("DetachProduct(again) error = %v, want ErrProductNotInProject", err)
	}
}

func TestCredentialLifecycleKeepsOwnershipOnTheProject(t *testing.T) {
	t.Parallel()
	service, repository, clock := newTestService(t)
	ctx := context.Background()
	owner := developerActor("10000000-0000-0000-0000-0000000000a1")
	other := developerActor("10000000-0000-0000-0000-0000000000a2")
	created, err := service.CreateProject(ctx, owner, CreateProjectInput{Type: TypeCloud, Name: "Mine"})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	issued, err := service.CreateCredential(ctx, owner, created.ID, CreateCredentialInput{
		Name: " primary ", Permissions: []string{"devices:read", "devices:read", " ota:deploy "},
	})
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}
	if issued.APISecret == "" || issued.Credential.APIKey == "" {
		t.Fatal("CreateCredential() returned empty key material")
	}
	if issued.Credential.Name != "primary" {
		t.Fatalf("credential name = %q, want trimmed", issued.Credential.Name)
	}
	if len(issued.Credential.Permissions) != 2 {
		t.Fatalf("permissions = %v, want deduplicated", issued.Credential.Permissions)
	}
	if string(issued.Credential.SecretCiphertext) == issued.APISecret {
		t.Fatal("stored ciphertext equals the plaintext secret")
	}

	if _, err := service.CreateCredential(ctx, owner, created.ID, CreateCredentialInput{Name: "x", Permissions: []string{"devices:write"}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateCredential(unknown permission) error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.CreateCredential(ctx, other, created.ID, CreateCredentialInput{Name: "x"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("CreateCredential(other developer) error = %v, want ErrForbidden", err)
	}
	if _, err := service.CreateCredential(ctx, owner, created.ID, CreateCredentialInput{Name: " "}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateCredential(blank name) error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.ListCredentials(ctx, manufacturerActor(), created.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ListCredentials(manufacturer) error = %v, want ErrForbidden", err)
	}

	// 验签主体必须是 project id，而不是凭证行 id。
	principal, err := service.AuthenticateCredential(ctx, issued.Credential.APIKey)
	if err != nil {
		t.Fatalf("AuthenticateCredential() error = %v", err)
	}
	if principal.InternalAppID != created.ID {
		t.Fatalf("principal id = %q, want the project id %q", principal.InternalAppID, created.ID)
	}
	if principal.APISecret != issued.APISecret {
		t.Fatal("AuthenticateCredential() returned a different secret")
	}
	if len(principal.Permissions) != 2 || principal.Permissions[0] != "devices:read" || principal.Permissions[1] != "ota:deploy" {
		t.Fatalf("principal permissions = %v, want [devices:read ota:deploy]", principal.Permissions)
	}

	// 轮换：旧 key 立即失效，新 key 主体仍是同一个 project。
	clock.advance(time.Minute)
	rotated, err := service.RotateCredential(ctx, owner, issued.Credential.ID)
	if err != nil {
		t.Fatalf("RotateCredential() error = %v", err)
	}
	if rotated.Credential.APIKey == issued.Credential.APIKey || rotated.APISecret == issued.APISecret {
		t.Fatal("RotateCredential() reused key material")
	}
	if _, err := service.AuthenticateCredential(ctx, issued.Credential.APIKey); !errors.Is(err, internalauth.ErrCredentialRevoked) {
		t.Fatalf("AuthenticateCredential(old key) error = %v, want ErrCredentialRevoked", err)
	}
	rotatedPrincipal, err := service.AuthenticateCredential(ctx, rotated.Credential.APIKey)
	if err != nil || rotatedPrincipal.InternalAppID != created.ID {
		t.Fatalf("AuthenticateCredential(rotated) = %+v, err = %v", rotatedPrincipal.InternalAppID, err)
	}

	// 停用项目会让它的全部凭证失效。
	if _, err := service.UpdateProject(ctx, owner, created.ID, ProjectPatch{Status: stringPointer(StatusDisabled)}); err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}
	if _, err := service.AuthenticateCredential(ctx, rotated.Credential.APIKey); !errors.Is(err, internalauth.ErrCredentialRevoked) {
		t.Fatalf("AuthenticateCredential(disabled project) error = %v, want ErrCredentialRevoked", err)
	}
	_ = repository
}

func TestAuthenticateCredentialFailureModes(t *testing.T) {
	t.Parallel()
	service, repository, _ := newTestService(t)
	ctx := context.Background()
	owner := developerActor("10000000-0000-0000-0000-0000000000a1")
	created, err := service.CreateProject(ctx, owner, CreateProjectInput{Type: TypeCloud, Name: "Mine"})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	issued, err := service.CreateCredential(ctx, owner, created.ID, CreateCredentialInput{Name: "primary", Permissions: []string{"devices:read"}})
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}

	if _, err := service.AuthenticateCredential(ctx, ""); !errors.Is(err, internalauth.ErrUnknownAPIKey) {
		t.Fatalf("AuthenticateCredential(empty) error = %v, want ErrUnknownAPIKey", err)
	}
	if _, err := service.AuthenticateCredential(ctx, "ak_unknown"); !errors.Is(err, internalauth.ErrUnknownAPIKey) {
		t.Fatalf("AuthenticateCredential(unknown) error = %v, want ErrUnknownAPIKey", err)
	}

	// 密文被破坏时 fail closed：返回错误而不是回退。
	broken := issued.Credential
	broken.SecretCiphertext = []byte("not-a-ciphertext")
	if err := repository.UpdateCredential(ctx, broken); err != nil {
		t.Fatalf("UpdateCredential() error = %v", err)
	}
	if _, err := service.AuthenticateCredential(ctx, issued.Credential.APIKey); err == nil {
		t.Fatal("AuthenticateCredential(broken ciphertext) succeeded, want a failure")
	}

	if _, err := service.DisableCredential(ctx, owner, issued.Credential.ID); err != nil {
		t.Fatalf("DisableCredential() error = %v", err)
	}
	if _, err := service.AuthenticateCredential(ctx, issued.Credential.APIKey); !errors.Is(err, internalauth.ErrCredentialRevoked) {
		t.Fatalf("AuthenticateCredential(disabled) error = %v, want ErrCredentialRevoked", err)
	}
	if _, err := service.DisableCredential(ctx, owner, "40000000-0000-0000-0000-0000000000ff"); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("DisableCredential(unknown) error = %v, want ErrCredentialNotFound", err)
	}
}

func stringPointer(value string) *string { return &value }
