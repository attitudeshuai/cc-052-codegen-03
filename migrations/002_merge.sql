BEGIN;

-- ---------------------------------------------------------------------------
-- 采集端 / 手工台账 合并功能
--
-- 数据流：field_record(暂存) -> merge_run 分组识别 -> merge_group(+member)
--         -> merged_record(合并产出) ；merge_decision 留存每一次人工/自动抉择
-- ---------------------------------------------------------------------------

-- 暂存区：采集端与手工台账交上来的原始记录，两边统一落这张表
CREATE TABLE IF NOT EXISTS field_record (
    id BIGSERIAL PRIMARY KEY,
    source VARCHAR(16) NOT NULL CHECK (source IN ('collector','ledger')),
    client_uuid VARCHAR(64),              -- 采集端幂等键；手工台账可空
    upload_batch VARCHAR(64),             -- 第几次/哪一批上交（可选）
    plot_id BIGINT NOT NULL REFERENCES plot(id),
    crop_id VARCHAR(64) NOT NULL,
    kind VARCHAR(16) NOT NULL CHECK (kind IN ('fertilize','pesticide','irrigation','weed')),
    happened_at TIMESTAMPTZ NOT NULL,
    input_id BIGINT REFERENCES input_material(id),
    dose DECIMAL(12,4),
    dose_unit VARCHAR(16),
    operator VARCHAR(128) NOT NULL,
    photos JSONB DEFAULT '{}',
    geo VARCHAR(64),
    note TEXT,
    merged_run_id BIGINT,                 -- 被哪一次合并收编；NULL=未合并
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 采集端幂等：同一 client_uuid 重传不产生重复记录（台账无 uuid 不受影响）
CREATE UNIQUE INDEX IF NOT EXISTS idx_field_record_client_uuid
    ON field_record(client_uuid) WHERE client_uuid IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_field_record_match
    ON field_record(plot_id, crop_id, operator);
CREATE INDEX IF NOT EXISTS idx_field_record_merged_run
    ON field_record(merged_run_id);

-- 一次合并运行：记录合并前后总数，供对账
CREATE TABLE IF NOT EXISTS merge_run (
    id BIGSERIAL PRIMARY KEY,
    status VARCHAR(16) NOT NULL DEFAULT 'running' CHECK (status IN ('running','completed','failed')),
    total_in INT NOT NULL DEFAULT 0,          -- 参与合并的原始记录数（合并前总数）
    groups_formed INT NOT NULL DEFAULT 0,     -- 认出多少件“同一件事”
    kept INT NOT NULL DEFAULT 0,              -- 合并后保留条数（合并后总数）
    superseded INT NOT NULL DEFAULT 0,        -- 被并掉的条数
    consistent_groups INT NOT NULL DEFAULT 0, -- 完全一致直接合并的组数
    evidence_resolved INT NOT NULL DEFAULT 0, -- 有冲突但以凭据方为准自动定的组数
    pending_conflicts INT NOT NULL DEFAULT 0, -- 待人工挑拣的冲突组数
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 合并分组：按 地块+作物+日期+操作人 认出的一组“同一件事”
CREATE TABLE IF NOT EXISTS merge_group (
    id BIGSERIAL PRIMARY KEY,
    run_id BIGINT NOT NULL REFERENCES merge_run(id),
    plot_id BIGINT NOT NULL,
    crop_id VARCHAR(64) NOT NULL,
    activity_date DATE NOT NULL,
    operator VARCHAR(128) NOT NULL,
    status VARCHAR(16) NOT NULL CHECK (status IN ('auto','evidence','pending','resolved')),
    kept_record_id BIGINT,                  -- 当前保留的原始记录（人工可改）
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_merge_group_run ON merge_group(run_id);
CREATE INDEX IF NOT EXISTS idx_merge_group_status ON merge_group(status);

-- 组成员：每条原始记录的去向（被保留 / 被并掉），record_id 全局唯一
CREATE TABLE IF NOT EXISTS merge_group_member (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES merge_group(id),
    record_id BIGINT NOT NULL REFERENCES field_record(id),
    is_kept BOOLEAN NOT NULL DEFAULT FALSE,
    diffs JSONB DEFAULT '[]',               -- 组内字段级不一致明细
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_merge_member_record ON merge_group_member(record_id);
CREATE INDEX IF NOT EXISTS idx_merge_member_group ON merge_group_member(group_id);

-- 合并产出：并成的一份台账
CREATE TABLE IF NOT EXISTS merged_record (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES merge_group(id),
    run_id BIGINT NOT NULL REFERENCES merge_run(id),
    plot_id BIGINT NOT NULL,
    crop_id VARCHAR(64) NOT NULL,
    kind VARCHAR(16) NOT NULL,
    happened_at TIMESTAMPTZ NOT NULL,
    input_id BIGINT,
    dose DECIMAL(12,4),
    dose_unit VARCHAR(16),
    operator VARCHAR(128) NOT NULL,
    photos JSONB DEFAULT '{}',
    geo VARCHAR(64),
    note TEXT,
    source_record_ids JSONB NOT NULL,       -- 由哪些原始记录并成，可回溯
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_merged_record_group ON merged_record(group_id);
CREATE INDEX IF NOT EXISTS idx_merged_record_run ON merged_record(run_id);

-- 抉择留档：自动（earliest/evidence）与人工（manual）的每一次定夺都留痕
CREATE TABLE IF NOT EXISTS merge_decision (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES merge_group(id),
    run_id BIGINT NOT NULL REFERENCES merge_run(id),
    rule VARCHAR(16) NOT NULL CHECK (rule IN ('earliest','evidence','manual')),
    decided_by VARCHAR(128) NOT NULL,       -- 'system' 或人工操作人
    kept_record_id BIGINT NOT NULL,
    field_picks JSONB DEFAULT '{}',         -- 字段级挑选：{字段名: 取值来源 record_id}
    note TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_merge_decision_group ON merge_decision(group_id);

COMMIT;
