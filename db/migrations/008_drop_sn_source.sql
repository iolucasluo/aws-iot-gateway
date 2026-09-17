-- 008_drop_sn_source：移除 device_sn 的并行来源语义。
--
-- device_sn 是设备唯一身份标识；sn_source 与 device_sn 表达同一事实，属冗余派生态。
-- 已部署库登记过 001/002 且带有该列，这里以顺序增量移除；fresh 库经 001/002 后已不含该列，
-- 因此本迁移必须幂等（IF EXISTS），重复执行是 no-op。

ALTER TABLE device_registry DROP CONSTRAINT IF EXISTS device_registry_sn_source_check;
ALTER TABLE device_registry DROP COLUMN IF EXISTS sn_source;
