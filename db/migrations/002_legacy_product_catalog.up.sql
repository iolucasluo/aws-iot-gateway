-- Reconcile databases that applied the pre-production 001_initial.sql before
-- the product catalog and explicit activation PID were added to that squashed
-- migration. Fresh schemas already have the final shape and intentionally no-op.

DO $$
DECLARE
    legacy_activation_schema boolean;
BEGIN
    SELECT to_regclass('device_registry') IS NOT NULL
       AND to_regclass('device_activations') IS NOT NULL
       AND NOT EXISTS (
            SELECT 1
            FROM information_schema.columns
            WHERE table_schema = current_schema()
              AND table_name = 'device_activations'
              AND column_name = 'pid'
       )
    INTO legacy_activation_schema;

    IF NOT legacy_activation_schema THEN
        RETURN;
    END IF;

    CREATE TABLE IF NOT EXISTS products (
        id uuid CONSTRAINT products_pkey PRIMARY KEY,
        pid varchar(64) NOT NULL CONSTRAINT products_pid_key UNIQUE,
        name varchar(128) NOT NULL,
        status varchar(16) NOT NULL DEFAULT 'active'
            CONSTRAINT products_status_check CHECK (status IN ('active', 'disabled')),
        created_at timestamptz NOT NULL DEFAULT now(),
        updated_at timestamptz NOT NULL DEFAULT now(),
        CONSTRAINT products_pid_check CHECK (btrim(pid) <> ''),
        CONSTRAINT products_name_check CHECK (btrim(name) <> '')
    );

    -- Old 001_initial.sql constrained every registry row to feeder-x1. This is
    -- compatibility backfill for existing references, not general product seed.
    INSERT INTO products (id, pid, name, status) VALUES
        ('10000000-0000-0000-0000-000000000001', 'feeder-x1', 'Feeder X1', 'active')
    ON CONFLICT (pid) DO NOTHING;

    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
        WHERE tgrelid = 'products'::regclass
          AND tgname = 'trg_products_updated_at'
          AND NOT tgisinternal
    ) THEN
        CREATE TRIGGER trg_products_updated_at
            BEFORE UPDATE ON products
            FOR EACH ROW EXECUTE FUNCTION set_updated_at();
    END IF;

    ALTER TABLE device_registry
        DROP CONSTRAINT IF EXISTS device_registry_pid_check;
    ALTER TABLE device_registry
        ALTER COLUMN pid DROP DEFAULT;
    ALTER TABLE device_registry
        ADD CONSTRAINT device_registry_pid_check CHECK (btrim(pid) <> '');
    ALTER TABLE device_registry
        ADD CONSTRAINT device_registry_pid_fkey
        FOREIGN KEY (pid) REFERENCES products(pid) ON DELETE RESTRICT;

    ALTER TABLE device_activations
        ADD COLUMN pid varchar(64);
    UPDATE device_activations AS activation
    SET pid = COALESCE(
        (SELECT registry.pid
         FROM device_registry AS registry
         WHERE registry.id = activation.device_registry_id),
        'feeder-x1'
    )
    WHERE activation.pid IS NULL;
    ALTER TABLE device_activations
        ALTER COLUMN pid SET NOT NULL;
    ALTER TABLE device_activations
        ADD CONSTRAINT device_activations_pid_check CHECK (btrim(pid) <> '');
    ALTER TABLE device_activations
        ADD CONSTRAINT device_activations_pid_fkey
        FOREIGN KEY (pid) REFERENCES products(pid) ON DELETE RESTRICT;
END;
$$;
