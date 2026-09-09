package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestProviderCompositionBindingEnforcesOneActiveInstancePerCapability(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	applyProductSeeds(t, ctx, pool)

	deviceID := "018f0000-0000-7000-8000-000000000061"
	bindingID := "018f0000-0000-7000-8000-000000000062"
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_registry (dp_profile_id, id, device_sn, thing_name, pid, status)
		VALUES ('40000000-0000-0000-0000-000000000001', $1, 'fdr-00000000000000000000000000000061', 'fdr-00000000000000000000000000000061', 'feeder-x1', 'active')
	`, deviceID); err != nil {
		t.Fatalf("insert provider device fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_bindings (id, device_registry_id, internal_app_id, binding_external_user_id, status)
		VALUES ($2, $1, 'livpet-staging', 'provider-user', 'active')
	`, deviceID, bindingID); err != nil {
		t.Fatalf("insert provider binding fixture: %v", err)
	}

	insert := func(id, capability, instance, status, reason string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO device_component_bindings (
				id, device_registry_id, device_binding_id, capability, provider_instance,
				external_ref, contract_version, status, revoked_reason
			) VALUES ($1, $2, $3, $4, $5, $6, 'installation.v2', $7, NULLIF($8, ''))
		`, id, deviceID, bindingID, capability, instance, "opaque-ref", status, reason)
		return err
	}
	if err := insert("018f0000-0000-7000-8000-000000000063", "messaging", "messaging.cn", "active", ""); err != nil {
		t.Fatalf("insert active component binding: %v", err)
	}
	if err := insert("018f0000-0000-7000-8000-000000000064", "messaging", "messaging.other", "active", ""); err == nil {
		t.Fatal("second active instance for the same capability was accepted")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			t.Fatalf("second active component error = %v, want unique violation", err)
		}
	}
	if err := insert("018f0000-0000-7000-8000-000000000065", "messaging", "messaging.cn", "revoked", "rotation"); err != nil {
		t.Fatalf("insert revoked component binding: %v", err)
	}
	if err := insert("018f0000-0000-7000-8000-000000000067", "realtime", "realtime.cn", "revoked", ""); err == nil {
		t.Fatal("revoked component without reason was accepted")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("revoked component without reason error = %v, want check violation", err)
		}
	}
	if err := insert("018f0000-0000-7000-8000-000000000066", "realtime", "realtime.cn", "active", "revoked-by-mistake"); err == nil {
		t.Fatal("active component with revoked reason was accepted")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("active revoked reason error = %v, want check violation", err)
		}
	}
}
