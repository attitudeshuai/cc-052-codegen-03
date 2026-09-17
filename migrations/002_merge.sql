BEGIN;

-- ============================================================================
-- 合并功能（采集端 activity × 手工台账 ledger_record）
--
-- 对账恒等式（任意时刻成立）：
--   device_in = matched_pairs + device_only     （采集端一条不多一条不少）
--   ledger_in = matched_pairs + ledger_only     （台账同理）
--   events    = resolved + unresolved           （配对算一个事件，单边算一个）
--   apply 后： output_count = resolved - skipped（核销不进最终台账）
-- 对不上时，apply 整体回滚，错误指明哪一侧计数出问题，再按 merge_item 逐行定位。
-- ============================================================================

-- 1) activity 标注来源 / 合并状态，使「合并后的一份」可直接区分来源
ALTER TABLE activity ADD COLUMN IF NOT EXISTS source VARCHAR(24) NOT NULL DEFAULT 'device';
ALTER TABLE activity ADD COLUMN merge_run_id BIGINT;
ALTER TABLE activity ADD COLUMN merge_state VARCHAR(16); -- kept|overwritten|skipped
ALTER TABLE activity ADD COLUMN note TEXT;
CREATE INDEX IF NOT EXISTS idx_activity_merge_run ON activity(merge_run_id);

-- 2) 手工台账（staging，不直接写 activity）
CREATE TABLE IF NOT EXISTS ledger_record (
    id            BIGSERIAL PRIMARY KEY,
    ledger_batch  VARCHAR(64)  NOT NULL,          -- 一次导入批号
    source_ref    VARCHAR(128) NOT NULL,          -- 台账第几行/表单编号（人工溯源用）
    plot_name     VARCHAR(255) NOT NULL,          -- 手工台账通常写地块名而非 id
    plot_id       BIGINT REFERENCES plot(id),     -- 解析到的地块（解析不了留空进 pending）
    crop_id       VARCHAR(64),                    -- 作物
    kind          VARCHAR(16) NOT NULL CHECK (kind IN ('fertilize','pesticide','irrigation','weed')),
    happened_on   DATE NOT NULL,                  -- 台账按「日」记账
    operator_norm VARCHAR(128) NOT NULL,          -- 归一化操作人
    operator_raw  VARCHAR(128),                   -- 原始操作人写法
    input_name    VARCHAR(255),                   -- 台账可能只写投入品名称
    input_id      BIGINT REFERENCES input_material(id),
    dose          DECIMAL(12,4),
    dose_unit     VARCHAR(16),
    has_evidence  BOOLEAN NOT NULL DEFAULT FALSE, -- 台账行是否附凭据（签字/照片/报告编号）
    evidence_ref  VARCHAR(255),
    raw           JSONB NOT NULL DEFAULT '{}',    -- 原始行，留档
    status        VARCHAR(16) NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','merged','skipped','conflict')),
    merged_batch_id     BIGINT,                   -- 确认后对应的批次
    merged_activity_id  BIGINT,                   -- 确认后对应的 activity（matched 时为另一条）
    merge_run_id        BIGINT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_ledger_dedup
    ON ledger_record(ledger_batch, source_ref);
CREATE INDEX IF NOT EXISTS idx_ledger_status ON ledger_record(status);
CREATE INDEX IF NOT EXISTS idx_ledger_pending_key
    ON ledger_record(plot_id, crop_id, happened_on, operator_norm)
    WHERE status = 'pending';

-- 3) 合并批次：一次「并成一份」的完整工作单及其逐项计数（对账用）
CREATE TABLE IF NOT EXISTS merge_run (
    id            BIGSERIAL PRIMARY KEY,
    farm_id       BIGINT REFERENCES farm(id),
    status        VARCHAR(16) NOT NULL DEFAULT 'open'
                    CHECK (status IN ('open','resolved','applied','cancelled')),
    device_in     INT NOT NULL DEFAULT 0,         -- 参与对账的采集端条数
    ledger_in     INT NOT NULL DEFAULT 0,         -- 参与对账的手工台账条数
    device_only   INT NOT NULL DEFAULT 0,         -- 仅采集端有
    ledger_only   INT NOT NULL DEFAULT 0,         -- 仅台账有
    matched_pairs INT NOT NULL DEFAULT 0,         -- 认出是同一件事的对数
    consistent    INT NOT NULL DEFAULT 0,         -- 配对且字段一致
    conflicts     INT NOT NULL DEFAULT 0,         -- 配对但字段打架（含已裁决与未裁决）
    resolved      INT NOT NULL DEFAULT 0,         -- 已处理（一致自动通过 + 人工确认/核销）
    unresolved    INT NOT NULL DEFAULT 0,         -- 还挂着等人处理
    skipped       INT NOT NULL DEFAULT 0,         -- 核销/作废，不进最终台账
    output_count  INT NOT NULL DEFAULT 0,         -- apply 后的「一份」记录数
    note          TEXT,
    created_by    VARCHAR(128),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    applied_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_merge_run_status ON merge_run(status);

-- 4) 合并明细：每一个“事件”一行（配对算一个事件，单边算一个事件）
CREATE TABLE IF NOT EXISTS merge_item (
    id               BIGSERIAL PRIMARY KEY,
    run_id           BIGINT NOT NULL REFERENCES merge_run(id) ON DELETE CASCADE,
    match_type       VARCHAR(16) NOT NULL CHECK (match_type IN ('matched','device_only','ledger_only')),
    status           VARCHAR(16) NOT NULL
                       CHECK (status IN ('consistent','conflict','pending','skipped')),
    -- 配对身份（同一件事的判定结果）
    plot_id          BIGINT,
    plot_name        VARCHAR(255),
    crop_id          VARCHAR(64),
    happened_on      DATE,
    operator_norm    VARCHAR(128),
    -- 两边记录
    activity_id      BIGINT REFERENCES activity(id),
    ledger_id        BIGINT REFERENCES ledger_record(id),
    -- 裁决
    winner_side      VARCHAR(16) CHECK (winner_side IN ('device','ledger',NULL)),
    chosen_activity_id BIGINT REFERENCES activity(id), -- 保留在「一份」里的那条
    new_input_id     BIGINT REFERENCES input_material(id), -- 人工改写（可选）
    resolution_note  TEXT,
    resolved_by      VARCHAR(128),
    resolved_at      TIMESTAMPTZ,
    -- apply 结果
    applied_activity_id BIGINT REFERENCES activity(id),
    -- 字段级差异（diff 留档，JSON 数组）
    differences      JSONB NOT NULL DEFAULT '[]',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_merge_item_run ON merge_item(run_id);
CREATE INDEX IF NOT EXISTS idx_merge_item_status ON merge_item(run_id, status);

-- 5) 人工操作留档（谁在什么时候改了哪一项的取舍）
CREATE TABLE IF NOT EXISTS merge_audit_log (
    id         BIGSERIAL PRIMARY KEY,
    run_id     BIGINT NOT NULL REFERENCES merge_run(id) ON DELETE CASCADE,
    item_id    BIGINT REFERENCES merge_item(id) ON DELETE SET NULL,
    action     VARCHAR(32) NOT NULL,             -- create/resolve/skip/apply/cancel
    actor      VARCHAR(128) NOT NULL,
    detail     JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_merge_audit_run ON merge_audit_log(run_id, created_at);

COMMIT;
