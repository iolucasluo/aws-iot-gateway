-- 002_device_identity：运行身份硬件锚与 device_sn 来源审计。
--
-- 背景：001_initial.sql 在设备身份归并落地时被原地扩展，但 runner 按文件名登记迁移
-- （internal/platform/postgres/migrate.go），已部署库既然登记过 001_initial.sql 就永远
-- 不会重跑，chip_uid / sn_source / requested_* 因此缺失，activation 会在
-- "column chip_uid does not exist" 处失败。这里以顺序增量补齐，不改写 001 的历史。
--
-- 语义：纯 additive（只加列与索引），可重复执行；既有行 sn_source 取默认值
-- 'generated' 是正确的历史事实——它们的 device_sn 全部由 Gateway 兜底铸号产生。

ALTER TABLE device_registry ADD COLUMN IF NOT EXISTS chip_uid bytea;

ALTER TABLE device_registry
    ADD COLUMN IF NOT EXISTS sn_source varchar(16) NOT NULL DEFAULT 'generated';

-- 列与命名约束分开添加：ADD COLUMN IF NOT EXISTS 在列已存在时会连同内联约束一起跳过，
-- 单独补约束才能保证最终 schema 与 001 基线一致，同时保持可重复执行。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'device_registry_sn_source_check'
          AND conrelid = 'device_registry'::regclass
    ) THEN
        ALTER TABLE device_registry ADD CONSTRAINT device_registry_sn_source_check
            CHECK (sn_source IN ('caller', 'device', 'generated'));
    END IF;
END $$;

-- partial unique index：同一 eFuse UID 只能对应一行运行身份，是"一芯多号"的最终防线。
CREATE UNIQUE INDEX IF NOT EXISTS uniq_device_registry_chip_uid
    ON device_registry (chip_uid) WHERE chip_uid IS NOT NULL;

COMMENT ON COLUMN device_registry.chip_uid IS
    'eFuse 芯片 UID（hex 解码后的字节）。可空：存量行与未上报 UID 的设备保持 NULL，首次上报时由归并逻辑回填。';
COMMENT ON COLUMN device_registry.sn_source IS
    'device_sn 来源：caller（内部 App 创建 activation 时提供）、device（设备 prepare 上报）、generated（Gateway 兜底铸号）。';

ALTER TABLE device_activations ADD COLUMN IF NOT EXISTS requested_device_sn varchar(64);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'device_activations_requested_device_sn_check'
          AND conrelid = 'device_activations'::regclass
    ) THEN
        ALTER TABLE device_activations ADD CONSTRAINT device_activations_requested_device_sn_check
            CHECK (requested_device_sn IS NULL OR btrim(requested_device_sn) <> '');
    END IF;
END $$;

ALTER TABLE device_activations ADD COLUMN IF NOT EXISTS requested_chip_uid bytea;

COMMENT ON COLUMN device_activations.requested_device_sn IS
    '创建 activation 时调用方声明的 device_sn，在 prepare 阶段作为最高优先级身份来源参与归并。';
COMMENT ON COLUMN device_activations.requested_chip_uid IS
    '创建 activation 时调用方声明的芯片 UID（字节），优先于设备 prepare 上报值参与归并。';
