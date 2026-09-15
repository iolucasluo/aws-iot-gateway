package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/operations"
)

// OperationsRepository 是平台面只读运维视图的 PostgreSQL 实现。它只做 SELECT。
type OperationsRepository struct{ pool *pgxpool.Pool }

func NewOperationsRepository(pool *pgxpool.Pool) (*OperationsRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL operations repository requires a pool")
	}
	return &OperationsRepository{pool: pool}, nil
}

// keyset 返回 (created_at, id) 倒序翻页条件；cursor 为空时不过滤。
func keyset(before *operations.Cursor) (any, any) {
	if before == nil {
		return nil, nil
	}
	return before.CreatedAt, before.ID
}

func (repository *OperationsRepository) ListDevices(ctx context.Context, filter operations.DeviceFilter) ([]operations.Device, error) {
	beforeAt, beforeID := keyset(filter.Page.Before)
	rows, err := repository.pool.Query(ctx, `
		SELECT d.id::text, d.device_sn, d.pid, d.status, d.presence_online, d.last_seen_at,
		       COALESCE(d.certificate_id, ''), d.created_at,
		       COALESCE(b.internal_app_id, ''), COALESCE(b.binding_external_user_id, ''), b.activated_at
		FROM device_registry d
		LEFT JOIN device_bindings b ON b.device_registry_id = d.id AND b.status = 'active'
		WHERE ($1 = '' OR d.pid = $1)
		  AND ($2 = '' OR d.status = $2)
		  AND ($3::boolean IS NULL OR d.presence_online = $3)
		  AND ($4 = '' OR d.device_sn LIKE $4 || '%')
		  AND ($5::timestamptz IS NULL OR (d.created_at, d.id::text) < ($5::timestamptz, $6))
		ORDER BY d.created_at DESC, d.id DESC
		LIMIT $7
	`, filter.PID, filter.Status, filter.Online, filter.Query, beforeAt, beforeID, filter.Page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()
	devices := make([]operations.Device, 0, filter.Page.Limit)
	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	return devices, nil
}

func (repository *OperationsRepository) FindDevice(ctx context.Context, deviceSN string) (operations.Device, error) {
	return scanDevice(repository.pool.QueryRow(ctx, `
		SELECT d.id::text, d.device_sn, d.pid, d.status, d.presence_online, d.last_seen_at,
		       COALESCE(d.certificate_id, ''), d.created_at,
		       COALESCE(b.internal_app_id, ''), COALESCE(b.binding_external_user_id, ''), b.activated_at
		FROM device_registry d
		LEFT JOIN device_bindings b ON b.device_registry_id = d.id AND b.status = 'active'
		WHERE d.device_sn = $1
	`, deviceSN))
}

func (repository *OperationsRepository) ListBindings(ctx context.Context, filter operations.BindingFilter) ([]operations.Binding, error) {
	beforeAt, beforeID := keyset(filter.Page.Before)
	rows, err := repository.pool.Query(ctx, `
		SELECT b.id::text, d.device_sn, d.pid, b.internal_app_id, b.binding_external_user_id,
		       b.status, b.activated_at, b.released_at, COALESCE(b.release_reason, ''), b.created_at
		FROM device_bindings b
		JOIN device_registry d ON d.id = b.device_registry_id
		WHERE ($1 = '' OR d.device_sn = $1)
		  AND ($2 = '' OR b.internal_app_id = $2)
		  AND ($3 = '' OR b.status = $3)
		  AND ($4::timestamptz IS NULL OR (b.created_at, b.id::text) < ($4::timestamptz, $5))
		ORDER BY b.created_at DESC, b.id DESC
		LIMIT $6
	`, filter.DeviceSN, filter.ProjectID, filter.Status, beforeAt, beforeID, filter.Page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list bindings: %w", err)
	}
	defer rows.Close()
	bindings := make([]operations.Binding, 0, filter.Page.Limit)
	for rows.Next() {
		var binding operations.Binding
		if err := rows.Scan(&binding.ID, &binding.DeviceSN, &binding.PID, &binding.SubjectID,
			&binding.BindingExternalUserID, &binding.Status, &binding.ActivatedAt, &binding.ReleasedAt,
			&binding.ReleaseReason, &binding.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan binding: %w", err)
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bindings: %w", err)
	}
	return bindings, nil
}

func (repository *OperationsRepository) ListActivations(ctx context.Context, filter operations.ActivationFilter) ([]operations.Activation, error) {
	beforeAt, beforeID := keyset(filter.Page.Before)
	rows, err := repository.pool.Query(ctx, `
		SELECT a.id::text, a.pid, COALESCE(d.device_sn, COALESCE(a.requested_device_sn, '')),
		       a.internal_app_id, a.binding_external_user_id, a.method, a.status, a.created_at, a.updated_at
		FROM device_activations a
		LEFT JOIN device_registry d ON d.id = a.device_registry_id
		WHERE ($1 = '' OR a.pid = $1)
		  AND ($2 = '' OR a.status = $2)
		  AND ($3::timestamptz IS NULL OR (a.created_at, a.id::text) < ($3::timestamptz, $4))
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT $5
	`, filter.PID, filter.Status, beforeAt, beforeID, filter.Page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list activations: %w", err)
	}
	defer rows.Close()
	activations := make([]operations.Activation, 0, filter.Page.Limit)
	for rows.Next() {
		var activation operations.Activation
		if err := rows.Scan(&activation.ID, &activation.PID, &activation.DeviceSN, &activation.SubjectID,
			&activation.BindingExternalUserID, &activation.Method, &activation.Status,
			&activation.CreatedAt, &activation.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan activation: %w", err)
		}
		activations = append(activations, activation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list activations: %w", err)
	}
	return activations, nil
}

func (repository *OperationsRepository) ListAuditLogs(ctx context.Context, filter operations.AuditFilter) ([]operations.AuditEntry, error) {
	beforeAt, beforeID := keyset(filter.Page.Before)
	rows, err := repository.pool.Query(ctx, `
		SELECT id::text, COALESCE(actor_kind, ''), COALESCE(actor_account_id::text, ''), COALESCE(actor_label, ''),
		       COALESCE(internal_app_id, ''), operation, COALESCE(resource_type, ''), COALESCE(resource_id, ''),
		       COALESCE(request_id, ''), outcome, COALESCE(error_code, ''), metadata, created_at
		FROM audit_logs
		WHERE ($1 = '' OR actor_kind = $1)
		  AND ($2 = '' OR actor_account_id = $2::uuid)
		  AND ($3 = '' OR operation LIKE $3 || '%')
		  AND ($4 = '' OR outcome = $4)
		  AND ($5::timestamptz IS NULL OR created_at >= $5::timestamptz)
		  AND ($6::timestamptz IS NULL OR created_at <= $6::timestamptz)
		  AND ($7::timestamptz IS NULL OR (created_at, id::text) < ($7::timestamptz, $8))
		ORDER BY created_at DESC, id DESC
		LIMIT $9
	`, filter.ActorKind, filter.ActorAccountID, filter.Operation, filter.Outcome,
		filter.From, filter.To, beforeAt, beforeID, filter.Page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()
	entries := make([]operations.AuditEntry, 0, filter.Page.Limit)
	for rows.Next() {
		var entry operations.AuditEntry
		var metadata []byte
		if err := rows.Scan(&entry.ID, &entry.ActorKind, &entry.ActorAccountID, &entry.ActorLabel,
			&entry.InternalAppID, &entry.Operation, &entry.ResourceType, &entry.ResourceID,
			&entry.RequestID, &entry.Outcome, &entry.ErrorCode, &metadata, &entry.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit log: %w", err)
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &entry.Metadata); err != nil {
				return nil, fmt.Errorf("decode audit metadata: %w", err)
			}
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	return entries, nil
}

func scanDevice(row pgx.Row) (operations.Device, error) {
	var device operations.Device
	err := row.Scan(&device.ID, &device.DeviceSN, &device.PID, &device.Status, &device.Online, &device.LastSeenAt,
		&device.CertificateID, &device.CreatedAt, &device.BindingProjectID,
		&device.BindingExternalUserID, &device.BindingActivatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return operations.Device{}, fmt.Errorf("%w: device not found", operations.ErrInvalidQuery)
	}
	if err != nil {
		return operations.Device{}, fmt.Errorf("scan device: %w", err)
	}
	return device, nil
}
