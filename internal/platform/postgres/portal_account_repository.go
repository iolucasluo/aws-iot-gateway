package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
)

// PortalAccountRepository 是平台面账号域的 PostgreSQL 实现。
// 唯一性冲突按语义转成 ErrEmailTaken，不让调用方解析数据库错误。
type PortalAccountRepository struct{ pool *pgxpool.Pool }

func NewPortalAccountRepository(pool *pgxpool.Pool) (*PortalAccountRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL portal account repository requires a pool")
	}
	return &PortalAccountRepository{pool: pool}, nil
}

const portalAccountColumns = `
	id::text, kind, role, email, COALESCE(password_hash, ''), COALESCE(display_name, ''),
	COALESCE(manufacturer_id::text, ''), status, last_login_at, created_at, updated_at
`

func (repository *PortalAccountRepository) CreateAccount(ctx context.Context, account portalaccount.Account) error {
	_, err := repository.pool.Exec(ctx, `
		INSERT INTO portal_accounts (id, kind, role, email, password_hash, display_name, manufacturer_id, status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,'')::uuid,$8,$9,$10)
	`, account.ID, string(account.Kind), account.Role, account.Email, account.PasswordHash,
		account.DisplayName, account.ManufacturerID, account.Status, account.CreatedAt, account.UpdatedAt)
	if err != nil {
		if isPortalAccountUniqueViolation(err) {
			return portalaccount.ErrEmailTaken
		}
		return fmt.Errorf("create portal account: %w", err)
	}
	return nil
}

func (repository *PortalAccountRepository) FindAccountByEmail(ctx context.Context, email string) (portalaccount.Account, error) {
	return scanPortalAccount(repository.pool.QueryRow(ctx, `
		SELECT `+portalAccountColumns+` FROM portal_accounts WHERE email=$1
	`, email))
}

func (repository *PortalAccountRepository) FindAccountByID(ctx context.Context, accountID string) (portalaccount.Account, error) {
	return scanPortalAccount(repository.pool.QueryRow(ctx, `
		SELECT `+portalAccountColumns+` FROM portal_accounts WHERE id=$1
	`, accountID))
}

func (repository *PortalAccountRepository) ListAccounts(ctx context.Context, filter portalaccount.AccountFilter) ([]portalaccount.Account, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := repository.pool.Query(ctx, `
		SELECT `+portalAccountColumns+` FROM portal_accounts
		WHERE ($1='' OR kind=$1)
		  AND ($2='' OR status=$2)
		  AND ($3='' OR manufacturer_id=$3::uuid)
		ORDER BY created_at ASC, id ASC
		LIMIT $4
	`, string(filter.Kind), filter.Status, filter.ManufacturerID, limit)
	if err != nil {
		return nil, fmt.Errorf("list portal accounts: %w", err)
	}
	defer rows.Close()
	accounts := make([]portalaccount.Account, 0, limit)
	for rows.Next() {
		account, err := scanPortalAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list portal accounts: %w", err)
	}
	return accounts, nil
}

func (repository *PortalAccountRepository) UpdateAccount(ctx context.Context, account portalaccount.Account) error {
	tag, err := repository.pool.Exec(ctx, `
		UPDATE portal_accounts
		SET role=$2, password_hash=NULLIF($3,''), display_name=NULLIF($4,''), status=$5, updated_at=$6
		WHERE id=$1
	`, account.ID, account.Role, account.PasswordHash, account.DisplayName, account.Status, account.UpdatedAt)
	if err != nil {
		if isPortalAccountUniqueViolation(err) {
			return portalaccount.ErrEmailTaken
		}
		return fmt.Errorf("update portal account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return portalaccount.ErrAccountNotFound
	}
	return nil
}

func (repository *PortalAccountRepository) MarkAccountLogin(ctx context.Context, accountID string, at time.Time) error {
	tag, err := repository.pool.Exec(ctx, `
		UPDATE portal_accounts SET last_login_at=$2, updated_at=$2 WHERE id=$1
	`, accountID, at)
	if err != nil {
		return fmt.Errorf("mark portal account login: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return portalaccount.ErrAccountNotFound
	}
	return nil
}

func (repository *PortalAccountRepository) CreateSession(ctx context.Context, session portalaccount.Session) error {
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO portal_sessions (id, account_id, token_hash, audience, expires_at, last_seen_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$6,$6)
	`, session.ID, session.AccountID, session.TokenHash[:], string(session.Audience),
		session.ExpiresAt, session.CreatedAt); err != nil {
		return fmt.Errorf("create portal session: %w", err)
	}
	return nil
}

func (repository *PortalAccountRepository) FindSessionByTokenHash(ctx context.Context, tokenHash [32]byte) (portalaccount.Session, error) {
	var session portalaccount.Session
	var audience string
	var rawHash []byte
	err := repository.pool.QueryRow(ctx, `
		SELECT id::text, account_id::text, token_hash, audience, expires_at, revoked_at, last_seen_at, created_at
		FROM portal_sessions WHERE token_hash=$1
	`, tokenHash[:]).Scan(&session.ID, &session.AccountID, &rawHash, &audience,
		&session.ExpiresAt, &session.RevokedAt, &session.LastSeenAt, &session.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return portalaccount.Session{}, portalaccount.ErrSessionNotFound
	}
	if err != nil {
		return portalaccount.Session{}, fmt.Errorf("find portal session: %w", err)
	}
	// pgx 不能把 bytea 直接扫进定长数组：先取字节再按长度校验，长度不符视为不可用。
	if len(rawHash) != len(session.TokenHash) {
		return portalaccount.Session{}, portalaccount.ErrSessionNotFound
	}
	copy(session.TokenHash[:], rawHash)
	session.Audience = portalaccount.Audience(audience)
	return session, nil
}

func (repository *PortalAccountRepository) TouchSession(ctx context.Context, sessionID string, at time.Time) error {
	if _, err := repository.pool.Exec(ctx, `
		UPDATE portal_sessions SET last_seen_at=$2, updated_at=$2 WHERE id=$1
	`, sessionID, at); err != nil {
		return fmt.Errorf("touch portal session: %w", err)
	}
	return nil
}

func (repository *PortalAccountRepository) RevokeSession(ctx context.Context, sessionID string, at time.Time) error {
	if _, err := repository.pool.Exec(ctx, `
		UPDATE portal_sessions SET revoked_at=$2, updated_at=$2 WHERE id=$1 AND revoked_at IS NULL
	`, sessionID, at); err != nil {
		return fmt.Errorf("revoke portal session: %w", err)
	}
	return nil
}

func (repository *PortalAccountRepository) RevokeAccountSessions(ctx context.Context, accountID string, at time.Time) error {
	if _, err := repository.pool.Exec(ctx, `
		UPDATE portal_sessions SET revoked_at=$2, updated_at=$2 WHERE account_id=$1 AND revoked_at IS NULL
	`, accountID, at); err != nil {
		return fmt.Errorf("revoke portal account sessions: %w", err)
	}
	return nil
}

func (repository *PortalAccountRepository) CreateSetupToken(ctx context.Context, token portalaccount.SetupToken) error {
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO portal_account_setup_tokens (id, account_id, token_hash, purpose, expires_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$6)
	`, token.ID, token.AccountID, token.TokenHash[:], token.Purpose, token.ExpiresAt, token.CreatedAt); err != nil {
		return fmt.Errorf("create portal setup token: %w", err)
	}
	return nil
}

func (repository *PortalAccountRepository) FindSetupTokenByHash(ctx context.Context, tokenHash [32]byte) (portalaccount.SetupToken, error) {
	var token portalaccount.SetupToken
	var rawHash []byte
	err := repository.pool.QueryRow(ctx, `
		SELECT id::text, account_id::text, token_hash, purpose, expires_at, used_at, created_at
		FROM portal_account_setup_tokens WHERE token_hash=$1
	`, tokenHash[:]).Scan(&token.ID, &token.AccountID, &rawHash, &token.Purpose,
		&token.ExpiresAt, &token.UsedAt, &token.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return portalaccount.SetupToken{}, portalaccount.ErrSetupTokenNotFound
	}
	if err != nil {
		return portalaccount.SetupToken{}, fmt.Errorf("find portal setup token: %w", err)
	}
	if len(rawHash) != len(token.TokenHash) {
		return portalaccount.SetupToken{}, portalaccount.ErrSetupTokenNotFound
	}
	copy(token.TokenHash[:], rawHash)
	return token, nil
}

// ConsumeSetupToken 用条件更新实现单次消费：并发重放只有一次能把 used_at 由 NULL 改掉。
func (repository *PortalAccountRepository) ConsumeSetupToken(ctx context.Context, tokenID string, at time.Time) error {
	tag, err := repository.pool.Exec(ctx, `
		UPDATE portal_account_setup_tokens SET used_at=$2, updated_at=$2 WHERE id=$1 AND used_at IS NULL
	`, tokenID, at)
	if err != nil {
		return fmt.Errorf("consume portal setup token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return portalaccount.ErrSetupTokenUsed
	}
	return nil
}

func (repository *PortalAccountRepository) ManufacturerExists(ctx context.Context, manufacturerID string) (bool, error) {
	var exists bool
	if err := repository.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM manufacturers WHERE id=$1)
	`, manufacturerID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check manufacturer existence: %w", err)
	}
	return exists, nil
}

func scanPortalAccount(row pgx.Row) (portalaccount.Account, error) {
	var account portalaccount.Account
	var kind string
	err := row.Scan(&account.ID, &kind, &account.Role, &account.Email, &account.PasswordHash,
		&account.DisplayName, &account.ManufacturerID, &account.Status, &account.LastLoginAt,
		&account.CreatedAt, &account.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return portalaccount.Account{}, portalaccount.ErrAccountNotFound
	}
	if err != nil {
		return portalaccount.Account{}, fmt.Errorf("scan portal account: %w", err)
	}
	account.Kind = portalaccount.AccountKind(kind)
	return account, nil
}

func isPortalAccountUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}
