package model

import (
	"cc-052/internal/merge"
	"database/sql/driver"
	"encoding/json"
	"time"
)

type MergeRunStatus string

const (
	MergeOpen      MergeRunStatus = "open"
	MergeResolved  MergeRunStatus = "resolved"
	MergeApplied   MergeRunStatus = "applied"
	MergeCancelled MergeRunStatus = "cancelled"
)

// MergeRun 一次「并成一份」的工作单与逐项计数（合并前后总数核对的依据）。
type MergeRun struct {
	ID           int64          `db:"id" json:"id"`
	FarmID       *int64         `db:"farm_id" json:"farm_id,omitempty"`
	Status       MergeRunStatus `db:"status" json:"status"`
	DeviceIn     int            `db:"device_in" json:"device_in"`
	LedgerIn     int            `db:"ledger_in" json:"ledger_in"`
	DeviceOnly   int            `db:"device_only" json:"device_only"`
	LedgerOnly   int            `db:"ledger_only" json:"ledger_only"`
	MatchedPairs int            `db:"matched_pairs" json:"matched_pairs"`
	Consistent   int            `db:"consistent" json:"consistent"`
	Conflicts    int            `db:"conflicts" json:"conflicts"`
	Resolved     int            `db:"resolved" json:"resolved"`
	Unresolved   int            `db:"unresolved" json:"unresolved"`
	Skipped      int            `db:"skipped" json:"skipped"`
	OutputCount  int            `db:"output_count" json:"output_count"`
	Note         *string        `db:"note" json:"note,omitempty"`
	CreatedBy    *string        `db:"created_by" json:"created_by,omitempty"`
	CreatedAt    time.Time      `db:"created_at" json:"created_at"`
	AppliedAt    *time.Time     `db:"applied_at" json:"applied_at,omitempty"`
}

// DiffList 让 []merge.FieldDiff 可以直接存 jsonb。
type DiffList []merge.FieldDiff

func (d DiffList) Value() (driver.Value, error) {
	if d == nil {
		return "[]", nil
	}
	return json.Marshal(d)
}
func (d *DiffList) Scan(value interface{}) error {
	if value == nil {
		*d = nil
		return nil
	}
	b, ok := value.([]byte)
	if !ok {
		return json.Unmarshal([]byte(value.(string)), d)
	}
	return json.Unmarshal(b, d)
}

// MergeItem 一个事件一行，含两边取值、字段差异、裁决结果。
type MergeItem struct {
	ID                int64      `db:"id" json:"id"`
	RunID             int64      `db:"run_id" json:"run_id"`
	MatchType         string     `db:"match_type" json:"match_type"`
	Status            string     `db:"status" json:"status"`
	PlotID            *int64     `db:"plot_id" json:"plot_id,omitempty"`
	PlotName          *string    `db:"plot_name" json:"plot_name,omitempty"`
	CropID            *string    `db:"crop_id" json:"crop_id,omitempty"`
	HappenedOn        *time.Time `db:"happened_on" json:"happened_on,omitempty"`
	OperatorNorm      *string    `db:"operator_norm" json:"operator_norm,omitempty"`
	ActivityID        *int64     `db:"activity_id" json:"activity_id,omitempty"`
	LedgerID          *int64     `db:"ledger_id" json:"ledger_id,omitempty"`
	WinnerSide        *string    `db:"winner_side" json:"winner_side,omitempty"`
	ChosenActivityID  *int64     `db:"chosen_activity_id" json:"chosen_activity_id,omitempty"`
	NewInputID        *int64     `db:"new_input_id" json:"new_input_id,omitempty"`
	ResolutionNote    *string    `db:"resolution_note" json:"resolution_note,omitempty"`
	ResolvedBy        *string    `db:"resolved_by" json:"resolved_by,omitempty"`
	ResolvedAt        *time.Time `db:"resolved_at" json:"resolved_at,omitempty"`
	AppliedActivityID *int64     `db:"applied_activity_id" json:"applied_activity_id,omitempty"`
	Differences       DiffList   `db:"differences" json:"differences"`
	CreatedAt         time.Time  `db:"created_at" json:"created_at"`

	// 两边记录快照（列表/详情接口带出，方便人直接挑）
	Activity *Activity     `db:"-" json:"activity,omitempty"`
	Ledger   *LedgerRecord `db:"-" json:"ledger,omitempty"`
}

// CreateMergeRunRequest 创建合并工作单。
type CreateMergeRunRequest struct {
	FarmID *int64 `json:"farm_id"`
	// OnlyBatchIDs 只合并指定采集端批次；为空取全部未合并的采集端记录。
	OnlyBatchIDs []int64 `json:"only_batch_ids"`
	CreatedBy    string  `json:"created_by"`
	Note         string  `json:"note"`
}

// MergeRunDetail 工作单 + 对账摘要 + 明细。
type MergeRunDetail struct {
	Run    *MergeRun    `json:"run"`
	Counts merge.Counts `json:"counts"`
	Check  MergeCheck   `json:"check"`
	Items  []*MergeItem `json:"items"`
}

// MergeCheck 合并前后总数核对结果。balanced=false 时 problems 指明哪一项对不上。
type MergeCheck struct {
	Balanced       bool     `json:"balanced"`
	DeviceIdentity string   `json:"device_identity"` // 如 "5 = 3 + 2"
	LedgerIdentity string   `json:"ledger_identity"`
	EventIdentity  string   `json:"event_identity"`
	Problems       []string `json:"problems,omitempty"`
}

// ResolveItemRequest 人工对一个事件做取舍（留档）。
type ResolveItemRequest struct {
	// Winner 取哪一边：device / ledger。
	Winner string `json:"winner" binding:"required"`
	// TargetBatchID 台账侧补入时必须指定落到哪个种植批次。
	TargetBatchID *int64 `json:"target_batch_id"`
	// NewInputID 可选：人工把投入品改成字典里的某一项。
	NewInputID *int64 `json:"new_input_id"`
	Note       string `json:"note"`
	ResolvedBy string `json:"resolved_by" binding:"required"`
}

// SkipItemRequest 核销某事件（重复抄录/作废），不进最终台账。
type SkipItemRequest struct {
	Reason     string `json:"reason"`
	ResolvedBy string `json:"resolved_by" binding:"required"`
}

// ApplyMergeRequest 确认全部处理完，落地成「一份」。
type ApplyMergeRequest struct {
	Actor string `json:"actor" binding:"required"`
}

// MergeAuditLog 人工操作留档。
type MergeAuditLog struct {
	ID        int64     `db:"id" json:"id"`
	RunID     int64     `db:"run_id" json:"run_id"`
	ItemID    *int64    `db:"item_id" json:"item_id,omitempty"`
	Action    string    `db:"action" json:"action"`
	Actor     string    `db:"actor" json:"actor"`
	Detail    StringMap `db:"detail" json:"detail,omitempty"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}
