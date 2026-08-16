package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/binding"
)

type BindingRepository struct{ pool *pgxpool.Pool }

func NewBindingRepository(pool *pgxpool.Pool) (*BindingRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL binding repository requires a pool")
	}
	return &BindingRepository{pool: pool}, nil
}

func (repository *BindingRepository) FindActiveBinding(ctx context.Context, internalAppID, deviceSN string) (binding.Binding, error) {
	return scanBinding(repository.pool.QueryRow(ctx, `
		SELECT b.id::text, d.id::text, d.device_sn, b.internal_app_id,
		       b.binding_external_user_id, COALESCE(d.certificate_id, ''), b.status='active'
		FROM device_bindings b
		JOIN device_registry d ON d.id=b.device_registry_id
		WHERE b.internal_app_id=$1 AND d.device_sn=$2 AND b.status='active'
	`, internalAppID, deviceSN))
}

func (repository *BindingRepository) StoreToken(ctx context.Context, token binding.DeviceToken) error {
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO device_tokens (id, device_binding_id, token_hash, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5)
	`, token.ID, token.BindingID, token.TokenHash[:], token.ExpiresAt, token.CreatedAt); err != nil {
		return fmt.Errorf("store device token: %w", err)
	}
	return nil
}

func (repository *BindingRepository) ValidateToken(
	ctx context.Context,
	internalAppID string,
	tokenHash [32]byte,
	deviceSN string,
) (binding.Binding, error) {
	return scanBinding(repository.pool.QueryRow(ctx, `
		SELECT b.id::text, d.id::text, d.device_sn, b.internal_app_id,
		       b.binding_external_user_id, COALESCE(d.certificate_id, ''), b.status='active'
		FROM device_tokens t
		JOIN device_bindings b ON b.id=t.device_binding_id
		JOIN device_registry d ON d.id=b.device_registry_id
		WHERE t.token_hash=$1 AND t.revoked_at IS NULL AND t.expires_at>now()
		  AND b.status='active' AND b.internal_app_id=$2 AND d.device_sn=$3
	`, tokenHash[:], internalAppID, deviceSN))
}

func (repository *BindingRepository) FindBindingByToken(ctx context.Context, tokenHash [32]byte) (binding.Binding, error) {
	return scanBinding(repository.pool.QueryRow(ctx, `
		SELECT b.id::text, d.id::text, d.device_sn, b.internal_app_id,
		       b.binding_external_user_id, COALESCE(d.certificate_id, ''), b.status='active'
		FROM device_tokens t
		JOIN device_bindings b ON b.id=t.device_binding_id
		JOIN device_registry d ON d.id=b.device_registry_id
		WHERE t.token_hash=$1 AND t.revoked_at IS NULL AND t.expires_at>now() AND b.status='active'
	`, tokenHash[:]))
}

func (repository *BindingRepository) ReleaseBinding(ctx context.Context, bindingID, reason string) (binding.Binding, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return binding.Binding{}, fmt.Errorf("begin binding release: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	released, err := scanBinding(transaction.QueryRow(ctx, `
		SELECT b.id::text, d.id::text, d.device_sn, b.internal_app_id,
		       b.binding_external_user_id, COALESCE(d.certificate_id, ''), b.status='active'
		FROM device_bindings b JOIN device_registry d ON d.id=b.device_registry_id
		WHERE b.id=$1 AND b.status='active'
		FOR UPDATE OF b, d
	`, bindingID))
	if err != nil {
		return binding.Binding{}, err
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE device_tokens SET revoked_at=now()
		WHERE device_binding_id=$1 AND revoked_at IS NULL
	`, bindingID); err != nil {
		return binding.Binding{}, fmt.Errorf("revoke binding tokens: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE device_bindings
		SET status='inactive', released_at=now(), release_reason=$2, updated_at=now()
		WHERE id=$1
	`, bindingID, reason); err != nil {
		return binding.Binding{}, fmt.Errorf("release binding: %w", err)
	}
	if _, err := transaction.Exec(ctx, `UPDATE device_registry SET status='revoked', updated_at=now() WHERE id=$1`, released.DeviceID); err != nil {
		return binding.Binding{}, fmt.Errorf("revoke device runtime: %w", err)
	}
	if released.CertificateID != "" {
		outboxID, err := postgresUUID()
		if err != nil {
			return binding.Binding{}, fmt.Errorf("generate lifecycle outbox id: %w", err)
		}
		if _, err := transaction.Exec(ctx, `
			INSERT INTO device_lifecycle_outbox (id, device_registry_id, operation, payload, status)
			VALUES ($1,$2,'revoke_certificate',jsonb_build_object('certificate_id',$3::text),'pending')
		`, outboxID, released.DeviceID, released.CertificateID); err != nil {
			return binding.Binding{}, fmt.Errorf("queue certificate revocation: %w", err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return binding.Binding{}, fmt.Errorf("commit binding release: %w", err)
	}
	released.Active = false
	return released, nil
}

type bindingRow interface{ Scan(...any) error }

func scanBinding(row bindingRow) (binding.Binding, error) {
	var result binding.Binding
	if err := row.Scan(
		&result.ID, &result.DeviceID, &result.DeviceSN, &result.InternalAppID,
		&result.BindingExternalUserID, &result.CertificateID, &result.Active,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return binding.Binding{}, binding.ErrBindingNotFound
		}
		return binding.Binding{}, fmt.Errorf("load binding: %w", err)
	}
	return result, nil
}

func postgresUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	hexValue := fmt.Sprintf("%x", value)
	return hexValue[:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:], nil
}
