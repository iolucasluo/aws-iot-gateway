-- Add the small, read-only capability list consumed by first-party apps when a
-- database already applied 001/002 before product capabilities were introduced.

ALTER TABLE products
    ADD COLUMN IF NOT EXISTS capabilities jsonb NOT NULL DEFAULT '[]'::jsonb;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'products'::regclass
          AND conname = 'products_capabilities_check'
    ) THEN
        ALTER TABLE products
            ADD CONSTRAINT products_capabilities_check
            CHECK (jsonb_typeof(capabilities) = 'array');
    END IF;
END;
$$;
