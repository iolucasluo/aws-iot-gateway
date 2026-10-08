-- 宠物识别照片的窄域、私有媒体事务；内部 ready 不进入 device_events。
-- 资源 ID 使用设备生成的 trigger UUID；重复通知仅可重试同一设备/同一内容。
CREATE TABLE IF NOT EXISTS device_pet_photos (
    id uuid CONSTRAINT device_pet_photos_pkey PRIMARY KEY,
    device_registry_id uuid NOT NULL
        CONSTRAINT device_pet_photos_device_registry_id_fkey
        REFERENCES device_registry(id) ON DELETE RESTRICT,
    device_binding_id uuid NOT NULL
        CONSTRAINT device_pet_photos_device_binding_id_fkey
        REFERENCES device_bindings(id) ON DELETE RESTRICT,
    size_bytes integer NOT NULL
        CONSTRAINT device_pet_photos_size_bytes_check
        CHECK (size_bytes > 0 AND size_bytes <= 262144),
    sha256 char(64) NOT NULL
        CONSTRAINT device_pet_photos_sha256_check CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    object_key text,
    image_url text,
    status text NOT NULL DEFAULT 'pending'
        CONSTRAINT device_pet_photos_status_check
        CHECK (status IN ('pending','uploading','verified','finalized','cleared','expiring','expired','dead')),
    CONSTRAINT device_pet_photos_verified_resource_check
        CHECK (status NOT IN ('verified','finalized','cleared') OR (object_key IS NOT NULL AND image_url IS NOT NULL)),
    attempts integer NOT NULL DEFAULT 0
        CONSTRAINT device_pet_photos_attempts_check CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '24 hours'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_device_pet_photos_due
    ON device_pet_photos (next_attempt_at, id)
    WHERE status IN ('pending','uploading','verified','finalized','cleared','expiring');
CREATE INDEX IF NOT EXISTS idx_device_pet_photos_device_binding
    ON device_pet_photos (device_registry_id, device_binding_id, created_at);
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='trg_device_pet_photos_updated_at') THEN
        CREATE TRIGGER trg_device_pet_photos_updated_at
            BEFORE UPDATE ON device_pet_photos
            FOR EACH ROW EXECUTE FUNCTION set_updated_at();
    END IF;
END $$;
