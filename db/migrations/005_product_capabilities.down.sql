ALTER TABLE products
    DROP CONSTRAINT IF EXISTS products_capabilities_check,
    DROP COLUMN IF EXISTS capabilities;
