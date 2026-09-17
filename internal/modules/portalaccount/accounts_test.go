package portalaccount

import (
	"context"
	"errors"
	"testing"
)

const testManufacturerID = "20000000-0000-0000-0000-000000000001"

func newAccountFixture(t *testing.T) (*Service, *MemoryRepository, *testClock, Actor) {
	t.Helper()
	service, repository, clock := newTestService(t)
	repository.AddManufacturer(testManufacturerID)
	staff := seedAccount(t, repository, KindStaff, RoleSuperAdmin, "ops@example.com", "correct horse battery", StatusActive)
	return service, repository, clock, ActorFromAccount(staff)
}

func TestCreateAccountIssuesInviteTokenAndRequiresValidScope(t *testing.T) {
	t.Parallel()
	service, repository, _, staff := newAccountFixture(t)
	ctx := context.Background()

	developer, err := service.CreateAccount(ctx, staff, CreateAccountInput{
		Kind: KindDeveloper, Role: RoleDeveloper, Email: "Dev@Example.com ", DisplayName: " Dev ",
	})
	if err != nil {
		t.Fatalf("CreateAccount(developer) error = %v", err)
	}
	if developer.Account.Email != "dev@example.com" || developer.Account.DisplayName != "Dev" {
		t.Fatalf("CreateAccount() account = %+v, want normalised email and trimmed display name", developer.Account)
	}
	if developer.Account.PasswordHash != "" {
		t.Fatal("CreateAccount() set a password hash before the invite was redeemed")
	}
	if developer.SetupToken == "" {
		t.Fatal("CreateAccount() returned an empty setup token")
	}
	if _, err := service.Login(ctx, AudienceDeveloper, "dev@example.com", "any password value"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() before redeeming the invite error = %v, want ErrInvalidCredentials", err)
	}

	manufacturer, err := service.CreateAccount(ctx, staff, CreateAccountInput{
		Kind: KindManufacturer, Role: RoleManufacturerAdmin, Email: "factory@example.com", ManufacturerID: testManufacturerID,
	})
	if err != nil {
		t.Fatalf("CreateAccount(manufacturer) error = %v", err)
	}
	if manufacturer.Account.ManufacturerID != testManufacturerID {
		t.Fatalf("CreateAccount(manufacturer) manufacturer = %q, want %q", manufacturer.Account.ManufacturerID, testManufacturerID)
	}

	for name, input := range map[string]CreateAccountInput{
		"staff-with-manufacturer": {Kind: KindStaff, Role: RoleSuperAdmin, Email: "a@example.com", ManufacturerID: testManufacturerID},
		"developer-as-staff":      {Kind: KindDeveloper, Role: RoleSuperAdmin, Email: "b@example.com"},
		"manufacturer-no-scope":   {Kind: KindManufacturer, Role: RoleManufacturerAdmin, Email: "c@example.com"},
		"unknown-kind":            {Kind: "reseller", Role: "reseller", Email: "d@example.com"},
		"unknown-role":            {Kind: KindManufacturer, Role: "supervisor", Email: "e@example.com", ManufacturerID: testManufacturerID},
		"invalid-email":           {Kind: KindDeveloper, Role: RoleDeveloper, Email: "not-an-email"},
		"empty-email":             {Kind: KindDeveloper, Role: RoleDeveloper, Email: "   "},
	} {
		if _, err := service.CreateAccount(ctx, staff, input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("CreateAccount(%s) error = %v, want ErrInvalidInput", name, err)
		}
	}

	if _, err := service.CreateAccount(ctx, staff, CreateAccountInput{
		Kind: KindManufacturer, Role: RoleManufacturerAdmin, Email: "ghost@example.com",
		ManufacturerID: "20000000-0000-0000-0000-0000000000ff",
	}); !errors.Is(err, ErrManufacturerNotFound) {
		t.Fatalf("CreateAccount(unknown manufacturer) error = %v, want ErrManufacturerNotFound", err)
	}
	if _, err := service.CreateAccount(ctx, staff, CreateAccountInput{
		Kind: KindDeveloper, Role: RoleDeveloper, Email: "dev@example.com",
	}); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("CreateAccount(duplicate email) error = %v, want ErrEmailTaken", err)
	}
	_ = repository
}

func TestManufacturerAdminIsConfinedToItsOwnOperators(t *testing.T) {
	t.Parallel()
	service, repository, _, staff := newAccountFixture(t)
	repository.AddManufacturer("20000000-0000-0000-0000-000000000002")
	ctx := context.Background()

	admin, err := service.CreateAccount(ctx, staff, CreateAccountInput{
		Kind: KindManufacturer, Role: RoleManufacturerAdmin, Email: "lead@example.com", ManufacturerID: testManufacturerID,
	})
	if err != nil {
		t.Fatalf("CreateAccount(admin) error = %v", err)
	}
	actor := ActorFromAccount(admin.Account)

	operator, err := service.CreateAccount(ctx, actor, CreateAccountInput{
		Kind: KindManufacturer, Role: RoleManufacturerOperator, Email: "line@example.com", ManufacturerID: testManufacturerID,
	})
	if err != nil {
		t.Fatalf("CreateAccount(operator) error = %v", err)
	}

	for name, input := range map[string]CreateAccountInput{
		"other-manufacturer": {Kind: KindManufacturer, Role: RoleManufacturerOperator, Email: "x@example.com", ManufacturerID: "20000000-0000-0000-0000-000000000002"},
		"second-admin":       {Kind: KindManufacturer, Role: RoleManufacturerAdmin, Email: "y@example.com", ManufacturerID: testManufacturerID},
		"developer":          {Kind: KindDeveloper, Role: RoleDeveloper, Email: "z@example.com"},
	} {
		if _, err := service.CreateAccount(ctx, actor, input); !errors.Is(err, ErrForbidden) {
			t.Errorf("CreateAccount(%s) by manufacturer admin error = %v, want ErrForbidden", name, err)
		}
	}

	listed, err := service.ListAccounts(ctx, actor, AccountFilter{})
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	for _, account := range listed {
		if account.ManufacturerID != testManufacturerID || account.Kind != KindManufacturer {
			t.Fatalf("ListAccounts() leaked %+v outside the manufacturer scope", account)
		}
	}
	if len(listed) != 2 {
		t.Fatalf("ListAccounts() returned %d accounts, want 2 (admin + operator)", len(listed))
	}

	if _, err := service.UpdateAccount(ctx, actor, operator.Account.ID, AccountPatch{Role: stringPointer(RoleManufacturerAdmin)}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("UpdateAccount(promote to admin) error = %v, want ErrForbidden", err)
	}
	other, err := service.CreateAccount(ctx, staff, CreateAccountInput{
		Kind: KindManufacturer, Role: RoleManufacturerAdmin, Email: "other@example.com", ManufacturerID: "20000000-0000-0000-0000-000000000002",
	})
	if err != nil {
		t.Fatalf("CreateAccount(other admin) error = %v", err)
	}
	if _, err := service.UpdateAccount(ctx, actor, other.Account.ID, AccountPatch{Status: stringPointer(StatusDisabled)}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("UpdateAccount(cross-manufacturer) error = %v, want ErrForbidden", err)
	}
}

func TestDeveloperCannotListOrManageAccounts(t *testing.T) {
	t.Parallel()
	service, repository, _, _ := newAccountFixture(t)
	developer := seedAccount(t, repository, KindDeveloper, RoleDeveloper, "dev@example.com", "correct horse battery", StatusActive)
	actor := ActorFromAccount(developer)

	if _, err := service.ListAccounts(context.Background(), actor, AccountFilter{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ListAccounts(developer) error = %v, want ErrForbidden", err)
	}
	if _, err := service.CreateAccount(context.Background(), actor, CreateAccountInput{
		Kind: KindDeveloper, Role: RoleDeveloper, Email: "peer@example.com",
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("CreateAccount(developer) error = %v, want ErrForbidden", err)
	}
}

func TestUpdateAccountEnforcesSelfProtectionAndScopeValidation(t *testing.T) {
	t.Parallel()
	service, repository, _, staff := newAccountFixture(t)
	ctx := context.Background()
	target := seedAccount(t, repository, KindDeveloper, RoleDeveloper, "dev@example.com", "correct horse battery", StatusActive)

	updated, err := service.UpdateAccount(ctx, staff, target.ID, AccountPatch{
		DisplayName: stringPointer(" Platform Dev "), Status: stringPointer(StatusDisabled),
	})
	if err != nil {
		t.Fatalf("UpdateAccount() error = %v", err)
	}
	if updated.DisplayName != "Platform Dev" || updated.Status != StatusDisabled {
		t.Fatalf("UpdateAccount() = %+v, want trimmed display name and disabled status", updated)
	}
	if _, err := service.UpdateAccount(ctx, staff, target.ID, AccountPatch{Role: stringPointer(RoleSuperAdmin)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("UpdateAccount(invalid role for kind) error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.UpdateAccount(ctx, staff, target.ID, AccountPatch{Status: stringPointer("suspended")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("UpdateAccount(unknown status) error = %v, want ErrInvalidInput", err)
	}

	if _, err := service.UpdateAccount(ctx, staff, staff.AccountID, AccountPatch{Status: stringPointer(StatusDisabled)}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("UpdateAccount(self disable) error = %v, want ErrForbidden", err)
	}
	if _, err := service.UpdateAccount(ctx, staff, staff.AccountID, AccountPatch{Role: stringPointer(RoleDeveloper)}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("UpdateAccount(self role change) error = %v, want ErrForbidden", err)
	}
	if _, err := service.UpdateAccount(ctx, staff, staff.AccountID, AccountPatch{DisplayName: stringPointer("Ops")}); err != nil {
		t.Fatalf("UpdateAccount(self display name) error = %v, want nil", err)
	}
}

func TestSetPasswordRedeemsInviteOnceAndRevokesSessions(t *testing.T) {
	t.Parallel()
	service, repository, clock, staff := newAccountFixture(t)
	ctx := context.Background()

	created, err := service.CreateAccount(ctx, staff, CreateAccountInput{
		Kind: KindDeveloper, Role: RoleDeveloper, Email: "dev@example.com",
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if _, err := service.SetPassword(ctx, created.SetupToken, "short"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("SetPassword(weak) error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.SetPassword(ctx, "unknown-token", "a brand new passphrase"); !errors.Is(err, ErrSetupTokenNotFound) {
		t.Fatalf("SetPassword(unknown token) error = %v, want ErrSetupTokenNotFound", err)
	}

	account, err := service.SetPassword(ctx, created.SetupToken, "a brand new passphrase")
	if err != nil {
		t.Fatalf("SetPassword() error = %v", err)
	}
	if account.ID != created.Account.ID {
		t.Fatalf("SetPassword() account = %q, want %q", account.ID, created.Account.ID)
	}
	if _, err := service.SetPassword(ctx, created.SetupToken, "another brand new passphrase"); !errors.Is(err, ErrSetupTokenUsed) {
		t.Fatalf("SetPassword(replay) error = %v, want ErrSetupTokenUsed", err)
	}
	if _, err := service.Login(ctx, AudienceDeveloper, "dev@example.com", "a brand new passphrase"); err != nil {
		t.Fatalf("Login() after set-password error = %v", err)
	}

	// 管理员重置会吊销现有会话，并签发可用的新 token。
	resetToken, err := service.ResetPassword(ctx, staff, created.Account.ID)
	if err != nil {
		t.Fatalf("ResetPassword() error = %v", err)
	}
	if active := repository.countActiveSessions(created.Account.ID); active != 0 {
		t.Fatalf("active sessions after reset = %d, want 0", active)
	}
	clock.advance(SetupTokenTTL)
	if _, err := service.SetPassword(ctx, resetToken, "yet another passphrase"); !errors.Is(err, ErrSetupTokenNotFound) {
		t.Fatalf("SetPassword(expired token) error = %v, want ErrSetupTokenNotFound", err)
	}
}

func TestSetPasswordRejectsDisabledAccountAndResetRequiresScope(t *testing.T) {
	t.Parallel()
	service, repository, _, staff := newAccountFixture(t)
	ctx := context.Background()
	created, err := service.CreateAccount(ctx, staff, CreateAccountInput{
		Kind: KindDeveloper, Role: RoleDeveloper, Email: "dev@example.com",
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if _, err := service.UpdateAccount(ctx, staff, created.Account.ID, AccountPatch{Status: stringPointer(StatusDisabled)}); err != nil {
		t.Fatalf("UpdateAccount() error = %v", err)
	}
	if _, err := service.SetPassword(ctx, created.SetupToken, "a brand new passphrase"); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("SetPassword(disabled account) error = %v, want ErrAccountDisabled", err)
	}

	developer := seedAccount(t, repository, KindDeveloper, RoleDeveloper, "peer@example.com", "correct horse battery", StatusActive)
	if _, err := service.ResetPassword(ctx, ActorFromAccount(developer), created.Account.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ResetPassword(developer actor) error = %v, want ErrForbidden", err)
	}
	if _, err := service.ResetPassword(ctx, staff, "10000000-0000-0000-0000-0000000000ff"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("ResetPassword(unknown account) error = %v, want ErrAccountNotFound", err)
	}
}

func stringPointer(value string) *string { return &value }
