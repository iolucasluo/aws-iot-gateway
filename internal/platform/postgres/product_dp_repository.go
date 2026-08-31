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

// ProductDPRepository reads the active product contract for a registered device.
// Product and DP mutation is intentionally kept in reviewed migrations/seeds for now.
type ProductDPRepository struct{ pool *pgxpool.Pool }

func NewProductDPRepository(pool *pgxpool.Pool) (*ProductDPRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL product DP repository requires a pool")
	}
	return &ProductDPRepository{pool: pool}, nil
}

func (repository *ProductDPRepository) LoadProductDPDefinitions(ctx context.Context, deviceSN string) ([]devicemodule.ProductDPDefinition, error) {
	var pid string
	if err := repository.pool.QueryRow(ctx, `
		SELECT dr.pid
		FROM device_registry dr
		JOIN products p ON p.pid = dr.pid AND p.status = 'active'
		WHERE dr.device_sn = $1 AND dr.status = 'active'
	`, deviceSN).Scan(&pid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: device %s", devicemodule.ErrDPUnavailable, deviceSN)
		}
		return nil, fmt.Errorf("find device product for DP contract: %w", err)
	}

	rows, err := repository.pool.Query(ctx, `
		SELECT dp.code, dp.type, dp.direction, dp.spec, dp.status
		FROM product_dps dp
		JOIN products p ON p.id = dp.product_id
		WHERE p.pid = $1 AND p.status = 'active'
		ORDER BY dp.code
	`, pid)
	if err != nil {
		return nil, fmt.Errorf("list product DP definitions: %w", err)
	}
	defer rows.Close()
	definitions := make([]devicemodule.ProductDPDefinition, 0)
	for rows.Next() {
		var definition devicemodule.ProductDPDefinition
		var spec []byte
		if err := rows.Scan(&definition.Code, &definition.Type, &definition.Direction, &spec, &definition.Status); err != nil {
			return nil, fmt.Errorf("scan product DP definition: %w", err)
		}
		if len(spec) > 0 && !json.Valid(spec) {
			return nil, fmt.Errorf("decode product DP definition %s: invalid spec", definition.Code)
		}
		definition.Spec = append(json.RawMessage(nil), spec...)
		definitions = append(definitions, definition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate product DP definitions: %w", err)
	}
	return definitions, nil
}
