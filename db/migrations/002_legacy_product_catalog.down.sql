-- This compatibility migration preserves legacy activation and registry rows
-- while adding required product references. Automatic rollback would require
-- dropping live columns/constraints and is intentionally refused.
DO $$
BEGIN
    RAISE EXCEPTION '002_legacy_product_catalog cannot be rolled back automatically; restore a reviewed database backup instead';
END;
$$;
