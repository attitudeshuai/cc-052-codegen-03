package model

import "time"

// LedgerStatus 手工台账行在合并流程中的状态。
type LedgerStatus string

const (
	LedgerPending  LedgerStatus = "pending"  // 待合并（staging）
	LedgerMerged   LedgerStatus = "merged"   // 已进入「一份」
	LedgerSkipped  LedgerStatus = "skipped"  // 核销/作废
	LedgerConflict LedgerStatus = "conflict" // 配对但打架，挂起等人挑
)

// LedgerRecord 手工抄录的台账行。台账按「日」记账、按地块名指认地块，
// 与采集端（batch_id + 精确时间戳）结构不同，因此单独建表做 staging，
// 绝不直接写进 activity。
type LedgerRecord struct {
	ID               int64        `db:"id" json:"id"`
	LedgerBatch      string       `db:"ledger_batch" json:"ledger_batch"`
	SourceRef        string       `db:"source_ref" json:"source_ref"`
	PlotName         string       `db:"plot_name" json:"plot_name"`
	PlotID           *int64       `db:"plot_id" json:"plot_id,omitempty"`
	CropID           *string      `db:"crop_id" json:"crop_id,omitempty"`
	Kind             ActivityKind `db:"kind" json:"kind"`
	HappenedOn       time.Time    `db:"happened_on" json:"happened_on"`
	OperatorNorm     string       `db:"operator_norm" json:"operator_norm"`
	OperatorRaw      *string      `db:"operator_raw" json:"operator_raw,omitempty"`
	InputName        *string      `db:"input_name" json:"input_name,omitempty"`
	InputID          *int64       `db:"input_id" json:"input_id,omitempty"`
	Dose             *float64     `db:"dose" json:"dose,omitempty"`
	DoseUnit         *string      `db:"dose_unit" json:"dose_unit,omitempty"`
	HasEvidence      bool         `db:"has_evidence" json:"has_evidence"`
	EvidenceRef      *string      `db:"evidence_ref" json:"evidence_ref,omitempty"`
	Raw              StringMap    `db:"raw" json:"raw,omitempty"`
	Status           LedgerStatus `db:"status" json:"status"`
	MergedBatchID    *int64       `db:"merged_batch_id" json:"merged_batch_id,omitempty"`
	MergedActivityID *int64       `db:"merged_activity_id" json:"merged_activity_id,omitempty"`
	MergeRunID       *int64       `db:"merge_run_id" json:"merge_run_id,omitempty"`
	CreatedAt        time.Time    `db:"created_at" json:"created_at"`
}

// ImportLedgerRequest 一次台账导入（十几个采集端之外的手工账，通常是一张表）。
type ImportLedgerRequest struct {
	// LedgerBatch 导入批号；为空则服务端生成。重复提交同批号 + source_ref 幂等。
	LedgerBatch string `json:"ledger_batch"`
	// FarmID 可选，用于按农场范围解析地块名。
	FarmID *int64            `json:"farm_id"`
	Rows   []ImportLedgerRow `json:"rows" binding:"required,min=1,dive"`
}

type ImportLedgerRow struct {
	SourceRef   string   `json:"source_ref" binding:"required"` // 行号/表单编号
	PlotName    string   `json:"plot_name" binding:"required"`
	PlotID      *int64   `json:"plot_id"` // 调用方已知地块 id 时直接给
	CropID      string   `json:"crop_id"`
	Kind        string   `json:"kind" binding:"required"`
	HappenedOn  string   `json:"happened_on" binding:"required"` // YYYY-MM-DD
	Operator    string   `json:"operator" binding:"required"`
	InputName   string   `json:"input_name"`
	InputID     *int64   `json:"input_id"`
	Dose        *float64 `json:"dose"`
	DoseUnit    string   `json:"dose_unit"`
	HasEvidence bool     `json:"has_evidence"`
	EvidenceRef string   `json:"evidence_ref"`
}

// ImportLedgerResult 汇报每行落地结果，导入阶段就把「查无此地」等问题摆出来。
type ImportLedgerResult struct {
	LedgerBatch string           `json:"ledger_batch"`
	Received    int              `json:"received"`
	Imported    int              `json:"imported"` // 新写入（去重后）
	Duplicated  int              `json:"duplicated"`
	Failed      int              `json:"failed"`
	RowErrors   []LedgerRowError `json:"row_errors,omitempty"`
}

type LedgerRowError struct {
	SourceRef string `json:"source_ref"`
	Reason    string `json:"reason"`
}
