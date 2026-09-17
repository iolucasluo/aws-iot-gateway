-- 007_platform_manufacturing：产线工位、生产批次与工位质量证据。
--
-- 背景：代工厂门户需要提交产线测试结果。这里刻意不引入"产线身份"：
-- 工位只是代工厂内部的编号，报告是 append-only 事实，不参与设备身份或激活门禁。
-- 产线证据允许引用尚不存在于 device_registry 的 device_sn（产线先于运行身份），
-- 因此 unit_results 是结构化数组而不是外键。
--
-- 语义：纯 additive，只新建平台面表。

CREATE TABLE IF NOT EXISTS production_stations (
    id uuid CONSTRAINT production_stations_pkey PRIMARY KEY,
    manufacturer_id uuid NOT NULL
        CONSTRAINT production_stations_manufacturer_id_fkey REFERENCES manufacturers(id) ON DELETE RESTRICT,
    code varchar(64) NOT NULL,
    name varchar(128) NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'active'
        CONSTRAINT production_stations_status_check CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uniq_production_stations_code UNIQUE (manufacturer_id, code),
    CONSTRAINT production_stations_code_check CHECK (btrim(code) <> ''),
    CONSTRAINT production_stations_name_check CHECK (btrim(name) <> '')
);

COMMENT ON TABLE production_stations IS
    '代工厂内部工位编号。它不是"产线身份"，不参与设备激活或身份校验。';

CREATE TABLE IF NOT EXISTS production_runs (
    id uuid CONSTRAINT production_runs_pkey PRIMARY KEY,
    project_id uuid NOT NULL
        CONSTRAINT production_runs_project_id_fkey REFERENCES projects(id) ON DELETE RESTRICT,
    product_pid varchar(64) NOT NULL
        CONSTRAINT production_runs_product_pid_fkey REFERENCES products(pid) ON DELETE RESTRICT,
    manufacturer_id uuid NOT NULL
        CONSTRAINT production_runs_manufacturer_id_fkey REFERENCES manufacturers(id) ON DELETE RESTRICT,
    batch_code varchar(96) NOT NULL,
    planned_quantity integer
        CONSTRAINT production_runs_planned_quantity_check CHECK (planned_quantity IS NULL OR planned_quantity > 0),
    status varchar(16) NOT NULL DEFAULT 'open'
        CONSTRAINT production_runs_status_check CHECK (status IN ('open', 'closed')),
    opened_by_account_id uuid NOT NULL
        CONSTRAINT production_runs_opened_by_account_id_fkey REFERENCES portal_accounts(id) ON DELETE RESTRICT,
    opened_at timestamptz NOT NULL DEFAULT now(),
    closed_by_account_id uuid
        CONSTRAINT production_runs_closed_by_account_id_fkey REFERENCES portal_accounts(id) ON DELETE RESTRICT,
    closed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uniq_production_runs_batch UNIQUE (project_id, batch_code),
    CONSTRAINT production_runs_batch_code_check CHECK (btrim(batch_code) <> ''),
    CONSTRAINT production_runs_close_state_check CHECK (
        (status = 'open' AND closed_at IS NULL AND closed_by_account_id IS NULL) OR
        (status = 'closed' AND closed_at IS NOT NULL)
    )
);

COMMENT ON TABLE production_runs IS
    '产线批次/测试 Run：绑定项目、产品与代工厂，是工位报告的归属锚点。';

CREATE TABLE IF NOT EXISTS production_station_reports (
    id uuid CONSTRAINT production_station_reports_pkey PRIMARY KEY,
    run_id uuid NOT NULL
        CONSTRAINT production_station_reports_run_id_fkey REFERENCES production_runs(id) ON DELETE RESTRICT,
    station_id uuid NOT NULL
        CONSTRAINT production_station_reports_station_id_fkey REFERENCES production_stations(id) ON DELETE RESTRICT,
    submitted_by_account_id uuid NOT NULL
        CONSTRAINT production_station_reports_submitted_by_account_id_fkey REFERENCES portal_accounts(id) ON DELETE RESTRICT,
    sequence_no integer NOT NULL CONSTRAINT production_station_reports_sequence_check CHECK (sequence_no > 0),
    units_total integer NOT NULL CONSTRAINT production_station_reports_total_check CHECK (units_total >= 0),
    units_passed integer NOT NULL CONSTRAINT production_station_reports_passed_check CHECK (units_passed >= 0),
    units_failed integer NOT NULL CONSTRAINT production_station_reports_failed_check CHECK (units_failed >= 0),
    overall_result varchar(16) NOT NULL
        CONSTRAINT production_station_reports_result_check CHECK (overall_result IN ('pass', 'fail')),
    unit_results jsonb NOT NULL DEFAULT '[]'::jsonb
        CONSTRAINT production_station_reports_units_check CHECK (jsonb_typeof(unit_results) = 'array'),
    evidence_sha256 char(64)
        CONSTRAINT production_station_reports_evidence_check
        CHECK (evidence_sha256 IS NULL OR evidence_sha256 ~ '^[0-9a-f]{64}$'),
    note text,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uniq_production_station_reports_sequence UNIQUE (run_id, station_id, sequence_no),
    CONSTRAINT production_station_reports_counts_check CHECK (units_passed + units_failed = units_total)
);

COMMENT ON TABLE production_station_reports IS
    'append-only 工位质量证据：只插入、不更新。unit_results 允许引用尚未拥有运行身份的 device_sn。';

CREATE INDEX IF NOT EXISTS idx_production_runs_project ON production_runs (project_id, status);
CREATE INDEX IF NOT EXISTS idx_production_runs_manufacturer ON production_runs (manufacturer_id, status);
CREATE INDEX IF NOT EXISTS idx_production_station_reports_run ON production_station_reports (run_id, created_at DESC);

DROP TRIGGER IF EXISTS trg_production_stations_updated_at ON production_stations;
CREATE TRIGGER trg_production_stations_updated_at
    BEFORE UPDATE ON production_stations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS trg_production_runs_updated_at ON production_runs;
CREATE TRIGGER trg_production_runs_updated_at
    BEFORE UPDATE ON production_runs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
