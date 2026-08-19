package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
)

type ActivationRepository struct{ pool *pgxpool.Pool }

func NewActivationRepository(pool *pgxpool.Pool) (*ActivationRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL activation repository requires a pool")
	}
	return &ActivationRepository{pool: pool}, nil
}

func (repository *ActivationRepository) FindActiveProduct(ctx context.Context, pid string) (activation.Product, error) {
	var product activation.Product
	if err := repository.pool.QueryRow(ctx, `
		SELECT pid, name FROM products WHERE pid=$1 AND status='active'
	`, pid).Scan(&product.PID, &product.Name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return activation.Product{}, activation.ErrProductUnavailable
		}
		return activation.Product{}, fmt.Errorf("find active product: %w", err)
	}
	return product, nil
}

func (repository *ActivationRepository) ListActiveProducts(ctx context.Context) ([]activation.Product, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT pid, name FROM products WHERE status='active' ORDER BY pid
	`)
	if err != nil {
		return nil, fmt.Errorf("list active products: %w", err)
	}
	defer rows.Close()
	products := make([]activation.Product, 0)
	for rows.Next() {
		var product activation.Product
		if err := rows.Scan(&product.PID, &product.Name); err != nil {
			return nil, fmt.Errorf("scan active product: %w", err)
		}
		products = append(products, product)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active products: %w", err)
	}
	return products, nil
}

func (repository *ActivationRepository) Create(ctx context.Context, record activation.Record) error {
	_, err := repository.pool.Exec(ctx, `
		INSERT INTO device_activations (
			id, internal_app_id, binding_external_user_id, method, pid, status,
			code_hash, idempotency_key, request_hash, expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, record.Activation.ID, record.Activation.InternalAppID, record.Activation.BindingExternalUserID,
		string(record.Activation.Method), record.Activation.PID, string(record.Activation.Status), record.CodeHash[:],
		record.Activation.IdempotencyKey, record.Activation.RequestHash[:], record.Activation.ExpiresAt)
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "23505" {
		return fmt.Errorf("insert activation: %w", err)
	}
	existing, findErr := repository.FindByIdempotency(ctx, record.Activation.InternalAppID, record.Activation.IdempotencyKey)
	if findErr != nil {
		return fmt.Errorf("resolve activation uniqueness conflict: %w", findErr)
	}
	if existing.Activation.RequestHash != record.Activation.RequestHash {
		return activation.ErrIdempotencyConflict
	}
	return activation.ErrActivationInProgress
}

func (repository *ActivationRepository) FindByID(ctx context.Context, id string) (activation.Record, error) {
	return loadActivationRecord(ctx, repository.pool, id, false)
}

func (repository *ActivationRepository) FindByIdempotency(ctx context.Context, internalAppID, key string) (activation.Record, error) {
	var id string
	if err := repository.pool.QueryRow(ctx, `
		SELECT id::text FROM device_activations WHERE internal_app_id=$1 AND idempotency_key=$2
	`, internalAppID, key).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return activation.Record{}, activation.ErrActivationNotFound
		}
		return activation.Record{}, fmt.Errorf("find activation by idempotency key: %w", err)
	}
	return repository.FindByID(ctx, id)
}

func (repository *ActivationRepository) ReservePreparation(
	ctx context.Context,
	activationID, deviceInternalID, deviceSN, csrPEM string,
) (activation.Record, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return activation.Record{}, fmt.Errorf("begin preparation reservation: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var status, method, pid, existingCSR string
	var productActive bool
	var expiresAt time.Time
	if err := transaction.QueryRow(ctx, `
		SELECT a.status, a.method, a.pid, a.expires_at, COALESCE(s.csr_pem, ''), p.status='active'
		FROM device_activations a
		JOIN products p ON p.pid=a.pid
		LEFT JOIN device_activation_sessions s ON s.device_activation_id=a.id
		WHERE a.id=$1
		FOR UPDATE OF a, p
	`, activationID).Scan(&status, &method, &pid, &expiresAt, &existingCSR, &productActive); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return activation.Record{}, activation.ErrActivationNotFound
		}
		return activation.Record{}, fmt.Errorf("lock activation preparation: %w", err)
	}
	switch activation.Status(status) {
	case activation.StatusPending:
		if !productActive {
			return activation.Record{}, activation.ErrProductUnavailable
		}
		if _, err := transaction.Exec(ctx, `
			INSERT INTO device_registry (id, device_sn, thing_name, pid, status)
			VALUES ($1,$2,$2,$3,'provisioning')
		`, deviceInternalID, deviceSN, pid); err != nil {
			return activation.Record{}, fmt.Errorf("reserve device identity: %w", err)
		}
		csrHash := sha256.Sum256([]byte(csrPEM))
		if _, err := transaction.Exec(ctx, `
			INSERT INTO device_activation_sessions (
				id, device_activation_id, session_external_id, method, status, csr_pem, csr_sha256, expires_at
			) VALUES ($1::uuid,$1::uuid,$1::text,$2,'pending',$3,$4,$5)
		`, activationID, method, csrPEM, csrHash[:], expiresAt); err != nil {
			return activation.Record{}, fmt.Errorf("reserve activation session: %w", err)
		}
		if _, err := transaction.Exec(ctx, `
			UPDATE device_activations SET status='activating', device_registry_id=$2, updated_at=now() WHERE id=$1
		`, activationID, deviceInternalID); err != nil {
			return activation.Record{}, fmt.Errorf("mark activation provisioning: %w", err)
		}
	case activation.StatusActivating:
		if existingCSR == csrPEM {
			return activation.Record{}, activation.ErrPreparationInProgress
		}
		return activation.Record{}, activation.ErrActivationAlreadyPrepared
	case activation.StatusInstalling, activation.StatusActivated:
		if existingCSR != csrPEM {
			return activation.Record{}, activation.ErrActivationAlreadyPrepared
		}
		if err := transaction.Commit(ctx); err != nil {
			return activation.Record{}, fmt.Errorf("finish preparation lookup: %w", err)
		}
		return repository.FindByID(ctx, activationID)
	default:
		return activation.Record{}, activation.ErrActivationAlreadyPrepared
	}
	if err := transaction.Commit(ctx); err != nil {
		return activation.Record{}, fmt.Errorf("commit preparation reservation: %w", err)
	}
	return repository.FindByID(ctx, activationID)
}

func (repository *ActivationRepository) CompletePreparation(
	ctx context.Context,
	activationID string,
	installation activation.Installation,
) (activation.Record, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return activation.Record{}, fmt.Errorf("begin preparation completion: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	commandTag, err := transaction.Exec(ctx, `
		UPDATE device_activations SET status='installing', updated_at=now()
		WHERE id=$1 AND status='activating'
	`, activationID)
	if err != nil {
		return activation.Record{}, fmt.Errorf("complete activation preparation: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return activation.Record{}, activation.ErrActivationAlreadyPrepared
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE device_registry SET certificate_id=$2, aws_iot_endpoint=$3, updated_at=now()
		WHERE id=(SELECT device_registry_id FROM device_activations WHERE id=$1)
	`, activationID, installation.CertificateID, installation.Endpoint); err != nil {
		return activation.Record{}, fmt.Errorf("store device certificate reference: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE device_activation_sessions
		SET status='prepared', certificate_pem=$2, installation_nonce=$3, updated_at=now()
		WHERE device_activation_id=$1
	`, activationID, installation.Certificate, installation.ProofNonce); err != nil {
		return activation.Record{}, fmt.Errorf("store installation challenge: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return activation.Record{}, fmt.Errorf("commit preparation completion: %w", err)
	}
	return repository.FindByID(ctx, activationID)
}

func (repository *ActivationRepository) FailPreparation(ctx context.Context, activationID, failureCode string) error {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin preparation failure: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, err := transaction.Exec(ctx, `
		UPDATE device_registry SET status='revoked', updated_at=now()
		WHERE id=(SELECT device_registry_id FROM device_activations WHERE id=$1)
	`, activationID); err != nil {
		return fmt.Errorf("revoke failed preparation device: %w", err)
	}
	if _, err := transaction.Exec(ctx, `UPDATE device_activation_sessions SET status='failed', updated_at=now() WHERE device_activation_id=$1`, activationID); err != nil {
		return fmt.Errorf("fail activation session: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE device_activations SET status='failed', failure_code=$2, updated_at=now() WHERE id=$1
	`, activationID, failureCode); err != nil {
		return fmt.Errorf("fail activation: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit preparation failure: %w", err)
	}
	return nil
}

func (repository *ActivationRepository) CompleteActivation(ctx context.Context, activationID, bindingID string) (activation.Record, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return activation.Record{}, fmt.Errorf("begin activation completion: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var deviceID, internalAppID, externalUserID string
	if err := transaction.QueryRow(ctx, `
		UPDATE device_activations SET status='activated', updated_at=now()
		WHERE id=$1 AND status='installing'
		RETURNING device_registry_id::text, internal_app_id, binding_external_user_id
	`, activationID).Scan(&deviceID, &internalAppID, &externalUserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return activation.Record{}, activation.ErrActivationAlreadyPrepared
		}
		return activation.Record{}, fmt.Errorf("complete activation: %w", err)
	}
	if _, err := transaction.Exec(ctx, `UPDATE device_registry SET status='active', updated_at=now() WHERE id=$1`, deviceID); err != nil {
		return activation.Record{}, fmt.Errorf("activate device record: %w", err)
	}
	if _, err := transaction.Exec(ctx, `UPDATE device_activation_sessions SET status='confirmed', updated_at=now() WHERE device_activation_id=$1`, activationID); err != nil {
		return activation.Record{}, fmt.Errorf("confirm activation session: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		INSERT INTO device_bindings (id, device_registry_id, internal_app_id, binding_external_user_id, status)
		VALUES ($1,$2,$3,$4,'active')
	`, bindingID, deviceID, internalAppID, externalUserID); err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "23505" {
			return activation.Record{}, activation.ErrDeviceBindingConflict
		}
		return activation.Record{}, fmt.Errorf("create device binding: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return activation.Record{}, fmt.Errorf("commit activation completion: %w", err)
	}
	return repository.FindByID(ctx, activationID)
}

func (repository *ActivationRepository) ListDevices(ctx context.Context, internalAppID, externalUserID string) ([]activation.Device, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT d.device_sn, a.id::text, d.pid, d.aws_iot_endpoint, b.binding_external_user_id, 'provisioned',
		       d.presence_online, d.last_seen_at
		FROM device_bindings b
		JOIN device_registry d ON d.id=b.device_registry_id
		JOIN device_activations a ON a.device_registry_id=d.id AND a.status='activated'
		WHERE b.internal_app_id=$1 AND ($2='' OR b.binding_external_user_id=$2) AND b.status='active'
		ORDER BY d.device_sn
	`, internalAppID, externalUserID)
	if err != nil {
		return nil, fmt.Errorf("list bound devices: %w", err)
	}
	defer rows.Close()
	devices := make([]activation.Device, 0)
	for rows.Next() {
		var device activation.Device
		if err := rows.Scan(
			&device.ID, &device.ActivationID, &device.PID, &device.Endpoint, &device.BindingExternalUserID,
			&device.Status, &device.Online, &device.LastSeenAt,
		); err != nil {
			return nil, fmt.Errorf("scan bound device: %w", err)
		}
		devices = append(devices, device)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bound devices: %w", err)
	}
	return devices, nil
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadActivationRecord(ctx context.Context, querier rowQuerier, id string, forUpdate bool) (activation.Record, error) {
	query := `
		SELECT a.id::text, a.internal_app_id, a.binding_external_user_id, a.method, a.pid, a.status,
		       a.code_hash, a.idempotency_key, a.request_hash, a.expires_at,
		       COALESCE(a.device_registry_id::text, ''), COALESCE(d.device_sn, ''),
		       COALESCE(s.csr_pem, ''), COALESCE(d.certificate_id, ''),
		       COALESCE(s.certificate_pem, ''), COALESCE(d.aws_iot_endpoint, ''),
		       COALESCE(s.installation_nonce, ''), COALESCE(a.failure_code, '')
		FROM device_activations a
		LEFT JOIN device_registry d ON d.id=a.device_registry_id
		LEFT JOIN device_activation_sessions s ON s.device_activation_id=a.id
		WHERE a.id=$1
		ORDER BY s.created_at DESC
		LIMIT 1`
	if forUpdate {
		query += ` FOR UPDATE OF a`
	}
	var record activation.Record
	var method, status string
	var codeHash, requestHash []byte
	var certificateID, certificatePEM, endpoint, nonce string
	if err := querier.QueryRow(ctx, query, id).Scan(
		&record.Activation.ID, &record.Activation.InternalAppID, &record.Activation.BindingExternalUserID,
		&method, &record.Activation.PID, &status, &codeHash, &record.Activation.IdempotencyKey, &requestHash,
		&record.Activation.ExpiresAt, &record.DeviceInternalID, &record.Activation.DeviceSN,
		&record.CSRPEM, &certificateID, &certificatePEM, &endpoint, &nonce, &record.FailureCode,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return activation.Record{}, activation.ErrActivationNotFound
		}
		return activation.Record{}, fmt.Errorf("load activation: %w", err)
	}
	if len(codeHash) != sha256.Size || len(requestHash) != sha256.Size {
		return activation.Record{}, fmt.Errorf("load activation: invalid hash length")
	}
	copy(record.CodeHash[:], codeHash)
	copy(record.Activation.RequestHash[:], requestHash)
	record.Activation.Method = activation.Method(method)
	record.Activation.Status = activation.Status(status)
	if certificateID != "" && certificatePEM != "" && endpoint != "" && nonce != "" {
		record.Installation = activation.Installation{
			ActivationID: record.Activation.ID, DeviceID: record.Activation.DeviceSN,
			PID: record.Activation.PID, CertificateID: certificateID, Certificate: certificatePEM, Endpoint: endpoint, ProofNonce: nonce,
		}
	}
	return record, nil
}
