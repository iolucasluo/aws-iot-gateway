package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	devicemodule "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
)

// DPProfileRepository loads the contract selected by a registered device.
// Profile publication is performed through reviewed contract seeds; no runtime CRUD is exposed.
type DPProfileRepository struct{ pool *pgxpool.Pool }

func NewDPProfileRepository(pool *pgxpool.Pool) (*DPProfileRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL DP profile repository requires a pool")
	}
	return &DPProfileRepository{pool: pool}, nil
}

func (repository *DPProfileRepository) LoadDPDefinitions(ctx context.Context, deviceSN string) ([]devicemodule.DPDefinition, error) {
	var profileID string
	if err := repository.pool.QueryRow(ctx, `
		SELECT profile.id::text
		FROM device_registry device
		JOIN dp_profiles profile ON profile.id=device.dp_profile_id
		WHERE device.device_sn=$1 AND device.status='active'
		  AND profile.status IN ('active', 'retired')
	`, deviceSN).Scan(&profileID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: device %s", devicemodule.ErrDPUnavailable, deviceSN)
		}
		return nil, fmt.Errorf("find device DP profile: %w", err)
	}

	rows, err := repository.pool.Query(ctx, `
		SELECT definition.code, definition.type, definition.direction, definition.spec, member.status
		FROM dp_profile_members member
		JOIN dp_definitions definition ON definition.id=member.dp_definition_id
		WHERE member.dp_profile_id=$1
		ORDER BY definition.code
	`, profileID)
	if err != nil {
		return nil, fmt.Errorf("list DP profile definitions: %w", err)
	}
	defer rows.Close()
	definitions := make([]devicemodule.DPDefinition, 0)
	for rows.Next() {
		var definition devicemodule.DPDefinition
		var spec []byte
		if err := rows.Scan(&definition.Code, &definition.Type, &definition.Direction, &spec, &definition.Status); err != nil {
			return nil, fmt.Errorf("scan DP profile definition: %w", err)
		}
		if len(spec) > 0 && !json.Valid(spec) {
			return nil, fmt.Errorf("decode DP profile definition %s: invalid spec", definition.Code)
		}
		definition.Spec = append(json.RawMessage(nil), spec...)
		definitions = append(definitions, definition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate DP profile definitions: %w", err)
	}
	return definitions, nil
}
