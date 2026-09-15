package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/audit"
)

// AuditRepository 只实现 INSERT：审计是 append-only 事实。
type AuditRepository struct{ pool *pgxpool.Pool }

func NewAuditRepository(pool *pgxpool.Pool) (*AuditRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL audit repository requires a pool")
	}
	return &AuditRepository{pool: pool}, nil
}

func (repository *AuditRepository) InsertAuditLog(ctx context.Context, entry audit.Entry) error {
	metadata, err := json.Marshal(entry.Metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO audit_logs (
			id, internal_app_id, actor_kind, actor_account_id, actor_label,
			operation, resource_type, resource_id, request_id, outcome, error_code, metadata, created_at
		) VALUES ($1,NULLIF($2,''),NULLIF($3,''),NULLIF($4,'')::uuid,NULLIF($5,''),
		          $6,NULLIF($7,''),NULLIF($8,''),NULLIF($9,''),$10,NULLIF($11,''),$12,$13)
	`, entry.ID, entry.InternalAppID, entry.ActorKind, entry.ActorAccountID, entry.ActorLabel,
		entry.Operation, entry.ResourceType, entry.ResourceID, entry.RequestID, entry.Outcome,
		entry.ErrorCode, metadata, entry.CreatedAt); err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}
