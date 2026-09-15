package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
)

const (
	integrationManufacturerID = "20000000-0000-0000-0000-0000000000c1"
	integrationStaffID        = "10000000-0000-0000-0000-0000000000c1"
	integrationDeveloperID    = "10000000-0000-0000-0000-0000000000c2"
)

func newPortalAccountIntegrationRepository(t *testing.T) (*PortalAccountRepository, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	repository, err := NewPortalAccountRepository(pool)
	if err != nil {
		t.Fatalf("NewPortalAccountRepository() error = %v", err)
	}
	return repository, pool
}

func seedIntegrationManufacturer(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO manufacturers (id, code, name, status) VALUES ($1, 'factory-a', 'Factory A', 'active')
	`, integrationManufacturerID); err != nil {
		t.Fatalf("seed manufacturer: %v", err)
	}
}

func TestPortalAccountRepositoryRoundTripAndConstraints(t *testing.T) {
	ctx := context.Background()
	repository, pool := newPortalAccountIntegrationRepository(t)
	seedIntegrationManufacturer(t, ctx, pool)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	staff := portalaccount.Account{
		ID: integrationStaffID, Kind: portalaccount.KindStaff, Role: portalaccount.RoleSuperAdmin,
		Email: "ops@example.com", PasswordHash: "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
		DisplayName: "Ops", Status: portalaccount.StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateAccount(ctx, staff); err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if err := repository.CreateAccount(ctx, staff); !errors.Is(err, portalaccount.ErrEmailTaken) {
		t.Fatalf("CreateAccount(duplicate) error = %v, want ErrEmailTaken", err)
	}

	loaded, err := repository.FindAccountByEmail(ctx, "ops@example.com")
	if err != nil {
		t.Fatalf("FindAccountByEmail() error = %v", err)
	}
	if loaded.ID != staff.ID || loaded.Role != portalaccount.RoleSuperAdmin || loaded.DisplayName != "Ops" {
		t.Fatalf("loaded account = %+v", loaded)
	}
	if loaded.ManufacturerID != "" {
		t.Fatalf("staff manufacturer = %q, want empty", loaded.ManufacturerID)
	}
	if _, err := repository.FindAccountByID(ctx, "10000000-0000-0000-0000-0000000000ff"); !errors.Is(err, portalaccount.ErrAccountNotFound) {
		t.Fatalf("FindAccountByID(unknown) error = %v, want ErrAccountNotFound", err)
	}

	// kind/role/manufacturer 组合由数据库 CHECK 兜底。
	if err := repository.CreateAccount(ctx, portalaccount.Account{
		ID: integrationDeveloperID, Kind: portalaccount.KindDeveloper, Role: portalaccount.RoleSuperAdmin,
		Email: "broken@example.com", Status: portalaccount.StatusActive, CreatedAt: now, UpdatedAt: now,
	}); err == nil {
		t.Fatal("CreateAccount(kind/role mismatch) was accepted by the database check")
	}
	// 厂商账号必须指向存在的厂商。
	if err := repository.CreateAccount(ctx, portalaccount.Account{
		ID: "10000000-0000-0000-0000-0000000000c3", Kind: portalaccount.KindManufacturer, Role: portalaccount.RoleManufacturerOperator,
		Email: "ghost@example.com", ManufacturerID: "20000000-0000-0000-0000-0000000000ff",
		Status: portalaccount.StatusActive, CreatedAt: now, UpdatedAt: now,
	}); err == nil {
		t.Fatal("CreateAccount(unknown manufacturer) was accepted despite the foreign key")
	}

	operator := portalaccount.Account{
		ID: "10000000-0000-0000-0000-0000000000c4", Kind: portalaccount.KindManufacturer, Role: portalaccount.RoleManufacturerOperator,
		Email: "line@example.com", ManufacturerID: integrationManufacturerID,
		Status: portalaccount.StatusActive, CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute),
	}
	if err := repository.CreateAccount(ctx, operator); err != nil {
		t.Fatalf("CreateAccount(operator) error = %v", err)
	}
	exists, err := repository.ManufacturerExists(ctx, integrationManufacturerID)
	if err != nil || !exists {
		t.Fatalf("ManufacturerExists() = %v, %v", exists, err)
	}
	if exists, err := repository.ManufacturerExists(ctx, "20000000-0000-0000-0000-0000000000ff"); err != nil || exists {
		t.Fatalf("ManufacturerExists(unknown) = %v, %v", exists, err)
	}

	// 只有 staff 与 operator 建成功：developer 被 kind/role CHECK 拒绝、ghost 被厂商 FK 拒绝。
	all, err := repository.ListAccounts(ctx, portalaccount.AccountFilter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("ListAccounts() = %d accounts, err = %v; want 2", len(all), err)
	}
	scoped, err := repository.ListAccounts(ctx, portalaccount.AccountFilter{
		Kind: portalaccount.KindManufacturer, ManufacturerID: integrationManufacturerID,
	})
	if err != nil || len(scoped) != 1 || scoped[0].ID != operator.ID {
		t.Fatalf("ListAccounts(manufacturer scope) = %+v, err = %v", scoped, err)
	}
	disabled, err := repository.ListAccounts(ctx, portalaccount.AccountFilter{Status: portalaccount.StatusDisabled})
	if err != nil || len(disabled) != 0 {
		t.Fatalf("ListAccounts(disabled) = %d, err = %v; want 0", len(disabled), err)
	}

	staff.Status = portalaccount.StatusDisabled
	staff.DisplayName = "Ops Retired"
	staff.UpdatedAt = now.Add(2 * time.Hour)
	if err := repository.UpdateAccount(ctx, staff); err != nil {
		t.Fatalf("UpdateAccount() error = %v", err)
	}
	reloaded, err := repository.FindAccountByID(ctx, staff.ID)
	if err != nil || reloaded.Status != portalaccount.StatusDisabled || reloaded.DisplayName != "Ops Retired" {
		t.Fatalf("reloaded account = %+v, err = %v", reloaded, err)
	}
	if err := repository.MarkAccountLogin(ctx, staff.ID, now.Add(3*time.Hour)); err != nil {
		t.Fatalf("MarkAccountLogin() error = %v", err)
	}
	loggedIn, err := repository.FindAccountByID(ctx, staff.ID)
	if err != nil || loggedIn.LastLoginAt == nil {
		t.Fatalf("last_login_at was not persisted: %+v, err = %v", loggedIn.LastLoginAt, err)
	}

	if err := repository.UpdateAccount(ctx, portalaccount.Account{ID: "10000000-0000-0000-0000-0000000000ff"}); !errors.Is(err, portalaccount.ErrAccountNotFound) {
		t.Fatalf("UpdateAccount(unknown) error = %v, want ErrAccountNotFound", err)
	}
}

func TestPortalSessionAndSetupTokenRepositorySemantics(t *testing.T) {
	ctx := context.Background()
	repository, _ := newPortalAccountIntegrationRepository(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	account := portalaccount.Account{
		ID: integrationStaffID, Kind: portalaccount.KindStaff, Role: portalaccount.RoleSuperAdmin,
		Email: "ops@example.com", Status: portalaccount.StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateAccount(ctx, account); err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}

	tokenHash := portalaccount.HashToken("session-token-value")
	session := portalaccount.Session{
		ID: "30000000-0000-0000-0000-0000000000d1", AccountID: account.ID, TokenHash: tokenHash,
		Audience: portalaccount.AudienceAdmin, ExpiresAt: now.Add(time.Hour), LastSeenAt: now, CreatedAt: now,
	}
	if err := repository.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	loaded, err := repository.FindSessionByTokenHash(ctx, tokenHash)
	if err != nil || loaded.ID != session.ID || loaded.Audience != portalaccount.AudienceAdmin {
		t.Fatalf("FindSessionByTokenHash() = %+v, err = %v", loaded, err)
	}
	if _, err := repository.FindSessionByTokenHash(ctx, portalaccount.HashToken("unknown")); !errors.Is(err, portalaccount.ErrSessionNotFound) {
		t.Fatalf("FindSessionByTokenHash(unknown) error = %v, want ErrSessionNotFound", err)
	}
	if err := repository.TouchSession(ctx, session.ID, now.Add(5*time.Minute)); err != nil {
		t.Fatalf("TouchSession() error = %v", err)
	}
	if err := repository.RevokeSession(ctx, session.ID, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	revoked, err := repository.FindSessionByTokenHash(ctx, tokenHash)
	if err != nil || revoked.RevokedAt == nil {
		t.Fatalf("session was not revoked: %+v, err = %v", revoked.RevokedAt, err)
	}
	// 重复吊销是幂等的，不应改写首次吊销时间。
	if err := repository.RevokeSession(ctx, session.ID, now.Add(time.Hour)); err != nil {
		t.Fatalf("RevokeSession() second call error = %v", err)
	}
	again, err := repository.FindSessionByTokenHash(ctx, tokenHash)
	if err != nil || !again.RevokedAt.Equal(*revoked.RevokedAt) {
		t.Fatalf("repeat revoke changed revoked_at: %v -> %v", revoked.RevokedAt, again.RevokedAt)
	}

	second := session
	second.ID = "30000000-0000-0000-0000-0000000000d2"
	second.TokenHash = portalaccount.HashToken("second-session-token")
	if err := repository.CreateSession(ctx, second); err != nil {
		t.Fatalf("CreateSession(second) error = %v", err)
	}
	if err := repository.RevokeAccountSessions(ctx, account.ID, now.Add(2*time.Hour)); err != nil {
		t.Fatalf("RevokeAccountSessions() error = %v", err)
	}
	remaining, err := repository.FindSessionByTokenHash(ctx, second.TokenHash)
	if err != nil || remaining.RevokedAt == nil {
		t.Fatalf("account-wide revoke missed a session: %+v, err = %v", remaining.RevokedAt, err)
	}

	setupHash := portalaccount.HashToken("setup-token-value")
	setup := portalaccount.SetupToken{
		ID: "40000000-0000-0000-0000-0000000000e1", AccountID: account.ID, TokenHash: setupHash,
		Purpose: portalaccount.PurposeInvite, ExpiresAt: now.Add(48 * time.Hour), CreatedAt: now,
	}
	if err := repository.CreateSetupToken(ctx, setup); err != nil {
		t.Fatalf("CreateSetupToken() error = %v", err)
	}
	loadedSetup, err := repository.FindSetupTokenByHash(ctx, setupHash)
	if err != nil || loadedSetup.Purpose != portalaccount.PurposeInvite || loadedSetup.UsedAt != nil {
		t.Fatalf("FindSetupTokenByHash() = %+v, err = %v", loadedSetup, err)
	}
	if err := repository.ConsumeSetupToken(ctx, setup.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("ConsumeSetupToken() error = %v", err)
	}
	// 条件更新保证并发重放只有一次成功。
	if err := repository.ConsumeSetupToken(ctx, setup.ID, now.Add(2*time.Minute)); !errors.Is(err, portalaccount.ErrSetupTokenUsed) {
		t.Fatalf("ConsumeSetupToken(replay) error = %v, want ErrSetupTokenUsed", err)
	}
	if _, err := repository.FindSetupTokenByHash(ctx, portalaccount.HashToken("unknown")); !errors.Is(err, portalaccount.ErrSetupTokenNotFound) {
		t.Fatalf("FindSetupTokenByHash(unknown) error = %v, want ErrSetupTokenNotFound", err)
	}
}
