package portalaccount

import (
	"context"
	"errors"
	"testing"
	"time"
)

type testClock struct{ moment time.Time }

func (clock *testClock) now() time.Time          { return clock.moment }
func (clock *testClock) advance(d time.Duration) { clock.moment = clock.moment.Add(d) }

func newTestService(t *testing.T) (*Service, *MemoryRepository, *testClock) {
	t.Helper()
	clock := &testClock{moment: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	repository := NewMemoryRepository()
	service, err := NewService(repository, clock.now)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, repository, clock
}

func seedAccount(t *testing.T, repository *MemoryRepository, kind AccountKind, role, email, password, status string) Account {
	t.Helper()
	hash := ""
	if password != "" {
		encoded, err := HashPassword(password)
		if err != nil {
			t.Fatalf("HashPassword() error = %v", err)
		}
		hash = encoded
	}
	id, err := randomUUID()
	if err != nil {
		t.Fatalf("randomUUID() error = %v", err)
	}
	account := Account{
		ID: id, Kind: kind, Role: role, Email: email, PasswordHash: hash, Status: status,
		CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
	if kind == KindManufacturer {
		account.ManufacturerID = "20000000-0000-0000-0000-000000000001"
	}
	if err := repository.CreateAccount(context.Background(), account); err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	return account
}

func TestLoginIssuesSessionAndNeverStoresPlaintextToken(t *testing.T) {
	t.Parallel()
	service, repository, clock := newTestService(t)
	seedAccount(t, repository, KindStaff, RoleSuperAdmin, "ops@example.com", "correct horse battery", StatusActive)

	result, err := service.Login(context.Background(), AudienceAdmin, "Ops@Example.com ", "correct horse battery")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if result.Token == "" {
		t.Fatal("Login() returned an empty token")
	}
	wantExpiry := clock.now().Add(SessionTTL)
	if !result.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("Login() expires_at = %v, want %v", result.ExpiresAt, wantExpiry)
	}
	if result.Account.Email != "ops@example.com" {
		t.Fatalf("Login() account email = %q, want the normalised address", result.Account.Email)
	}
	if result.Account.LastLoginAt == nil {
		t.Fatal("Login() did not record last_login_at")
	}
	session, err := repository.FindSessionByTokenHash(context.Background(), HashToken(result.Token))
	if err != nil {
		t.Fatalf("stored session lookup error = %v", err)
	}
	if session.Audience != AudienceAdmin {
		t.Fatalf("session audience = %q, want %q", session.Audience, AudienceAdmin)
	}
}

func TestLoginRejectsUnknownWrongDisabledAndCrossPortalCredentials(t *testing.T) {
	t.Parallel()
	service, repository, _ := newTestService(t)
	seedAccount(t, repository, KindStaff, RoleSuperAdmin, "ops@example.com", "correct horse battery", StatusActive)
	seedAccount(t, repository, KindDeveloper, RoleDeveloper, "dev@example.com", "correct horse battery", StatusActive)
	seedAccount(t, repository, KindStaff, RoleSuperAdmin, "off@example.com", "correct horse battery", StatusDisabled)
	seedAccount(t, repository, KindManufacturer, RoleManufacturerAdmin, "invited@example.com", "", StatusActive)

	for name, input := range map[string]struct {
		audience Audience
		email    string
		password string
	}{
		"unknown-email":    {AudienceAdmin, "nobody@example.com", "correct horse battery"},
		"wrong-password":   {AudienceAdmin, "ops@example.com", "wrong horse battery"},
		"disabled-account": {AudienceAdmin, "off@example.com", "correct horse battery"},
		"no-password-yet":  {AudienceManufacturer, "invited@example.com", "correct horse battery"},
		"cross-portal":     {AudienceAdmin, "dev@example.com", "correct horse battery"},
		"empty-password":   {AudienceAdmin, "ops@example.com", ""},
		"empty-audience":   {"", "ops@example.com", "correct horse battery"},
	} {
		if _, err := service.Login(context.Background(), input.audience, input.email, input.password); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("Login(%s) error = %v, want ErrInvalidCredentials", name, err)
		}
	}
}

func TestAuthenticateBindsSessionToAudienceAndAccountState(t *testing.T) {
	t.Parallel()
	service, repository, clock := newTestService(t)
	account := seedAccount(t, repository, KindDeveloper, RoleDeveloper, "dev@example.com", "correct horse battery", StatusActive)
	result, err := service.Login(context.Background(), AudienceDeveloper, "dev@example.com", "correct horse battery")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	got, session, err := service.Authenticate(context.Background(), AudienceDeveloper, result.Token)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if got.ID != account.ID || session.AccountID != account.ID {
		t.Fatalf("Authenticate() account = %q / session account = %q, want %q", got.ID, session.AccountID, account.ID)
	}
	if _, _, err := service.Authenticate(context.Background(), AudienceAdmin, result.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Authenticate() cross-audience error = %v, want ErrSessionNotFound", err)
	}
	if _, _, err := service.Authenticate(context.Background(), AudienceDeveloper, "not-a-token"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Authenticate() unknown token error = %v, want ErrSessionNotFound", err)
	}

	// 账号被禁用后，已签发的会话立即失效。
	account.Status = StatusDisabled
	if err := repository.UpdateAccount(context.Background(), account); err != nil {
		t.Fatalf("UpdateAccount() error = %v", err)
	}
	if _, _, err := service.Authenticate(context.Background(), AudienceDeveloper, result.Token); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("Authenticate() disabled account error = %v, want ErrAccountDisabled", err)
	}
	clock.advance(time.Minute)
}

func TestAuthenticateRejectsExpiredAndRevokedSessions(t *testing.T) {
	t.Parallel()
	service, repository, clock := newTestService(t)
	seedAccount(t, repository, KindStaff, RoleSuperAdmin, "ops@example.com", "correct horse battery", StatusActive)

	expiring, err := service.Login(context.Background(), AudienceAdmin, "ops@example.com", "correct horse battery")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	clock.advance(SessionTTL)
	if _, _, err := service.Authenticate(context.Background(), AudienceAdmin, expiring.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Authenticate() expired session error = %v, want ErrSessionNotFound", err)
	}

	fresh, err := service.Login(context.Background(), AudienceAdmin, "ops@example.com", "correct horse battery")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	_, session, err := service.Authenticate(context.Background(), AudienceAdmin, fresh.Token)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if err := service.Logout(context.Background(), session); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if err := service.Logout(context.Background(), session); err != nil {
		t.Fatalf("Logout() second call error = %v, want idempotent nil", err)
	}
	if _, _, err := service.Authenticate(context.Background(), AudienceAdmin, fresh.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Authenticate() revoked session error = %v, want ErrSessionNotFound", err)
	}
}

func TestAuthenticateSamplesLastSeenWrites(t *testing.T) {
	t.Parallel()
	service, repository, clock := newTestService(t)
	seedAccount(t, repository, KindStaff, RoleSuperAdmin, "ops@example.com", "correct horse battery", StatusActive)
	result, err := service.Login(context.Background(), AudienceAdmin, "ops@example.com", "correct horse battery")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	_, initial, err := service.Authenticate(context.Background(), AudienceAdmin, result.Token)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}

	clock.advance(time.Minute)
	if _, session, err := service.Authenticate(context.Background(), AudienceAdmin, result.Token); err != nil || !session.LastSeenAt.Equal(initial.LastSeenAt) {
		t.Fatalf("Authenticate() inside the sampling window updated last_seen (err=%v)", err)
	}

	clock.advance(sessionTouchEvery)
	_, session, err := service.Authenticate(context.Background(), AudienceAdmin, result.Token)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if !session.LastSeenAt.After(initial.LastSeenAt) {
		t.Fatalf("Authenticate() outside the sampling window did not update last_seen: %v", session.LastSeenAt)
	}
}

func TestChangePasswordRevokesEverySessionAndEnforcesPolicy(t *testing.T) {
	t.Parallel()
	service, repository, _ := newTestService(t)
	account := seedAccount(t, repository, KindStaff, RoleSuperAdmin, "ops@example.com", "correct horse battery", StatusActive)
	first, err := service.Login(context.Background(), AudienceAdmin, "ops@example.com", "correct horse battery")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	second, err := service.Login(context.Background(), AudienceAdmin, "ops@example.com", "correct horse battery")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	if err := service.ChangePassword(context.Background(), account.ID, "wrong current password", "a brand new passphrase"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("ChangePassword() wrong current error = %v, want ErrInvalidCredentials", err)
	}
	for name, candidate := range map[string]string{
		"too-short":       "short",
		"blank":           "            ",
		"same-as-current": "correct horse battery",
	} {
		if err := service.ChangePassword(context.Background(), account.ID, "correct horse battery", candidate); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("ChangePassword(%s) error = %v, want ErrInvalidInput", name, err)
		}
	}

	if err := service.ChangePassword(context.Background(), account.ID, "correct horse battery", "a brand new passphrase"); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if active := repository.countActiveSessions(account.ID); active != 0 {
		t.Fatalf("active sessions after password change = %d, want 0", active)
	}
	for name, token := range map[string]string{"first": first.Token, "second": second.Token} {
		if _, _, err := service.Authenticate(context.Background(), AudienceAdmin, token); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("Authenticate(%s) after password change error = %v, want ErrSessionNotFound", name, err)
		}
	}
	if _, err := service.Login(context.Background(), AudienceAdmin, "ops@example.com", "correct horse battery"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() with the retired password error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := service.Login(context.Background(), AudienceAdmin, "ops@example.com", "a brand new passphrase"); err != nil {
		t.Fatalf("Login() with the new password error = %v", err)
	}
}

func TestNewServiceRejectsMissingRepository(t *testing.T) {
	t.Parallel()
	if _, err := NewService(nil, nil); err == nil {
		t.Fatal("NewService(nil) error = nil, want a failure")
	}
}
