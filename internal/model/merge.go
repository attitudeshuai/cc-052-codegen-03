package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

// RecordSource 记录来源：collector=采集端，ledger=手工台账
type RecordSource string

const (
	SourceCollector RecordSource = "collector"
	SourceLedger    RecordSource = "ledger"
)

// MergeGroupStatus 合并分组状态
const (
	GroupStatusAuto     = "auto"     // 无冲突，自动保留最早一条
	GroupStatusEvidence = "evidence" // 有冲突，自动以有凭据的一条为准
	GroupStatusPending  = "pending"  // 有冲突，待人工挑拣
	GroupStatusResolved = "resolved" // 人工已挑拣
)

// MergeDecisionRule 抉择依据
const (
	RuleEarliest = "earliest" // 保留最早一条
	RuleEvidence = "evidence" // 以有凭据的一条为准
	RuleManual   = "manual"   // 人工挑拣
)

// FieldRecord 暂存区原始记录：采集端与手工台账统一落这张表
type FieldRecord struct {
	ID          int64        `db:"id" json:"id"`
	Source      RecordSource `db:"source" json:"source"`
	ClientUUID  *string      `db:"client_uuid" json:"client_uuid,omitempty"`
	UploadBatch *string      `db:"upload_batch" json:"upload_batch,omitempty"`
	PlotID      int64        `db:"plot_id" json:"plot_id"`
	CropID      string       `db:"crop_id" json:"crop_id"`
	Kind        ActivityKind `db:"kind" json:"kind"`
	HappenedAt  time.Time    `db:"happened_at" json:"happened_at"`
	InputID     *int64       `db:"input_id" json:"input_id,omitempty"`
	Dose        *float64     `db:"dose" json:"dose,omitempty"`
	DoseUnit    *string      `db:"dose_unit" json:"dose_unit,omitempty"`
	Operator    string       `db:"operator" json:"operator"`
	Photos      StringMap    `db:"photos" json:"photos,omitempty"`
	Geo         *string      `db:"geo" json:"geo,omitempty"`
	Note        *string      `db:"note" json:"note,omitempty"`
	MergedRunID *int64       `db:"merged_run_id" json:"merged_run_id,omitempty"`
	CreatedAt   time.Time    `db:"created_at" json:"created_at"`
}

type SubmitRecordRequest struct {
	Source      RecordSource `json:"source" binding:"required"`
	ClientUUID  string       `json:"client_uuid"`
	UploadBatch string       `json:"upload_batch"`
	PlotID      int64        `json:"plot_id" binding:"required"`
	CropID      string       `json:"crop_id" binding:"required"`
	Kind        ActivityKind `json:"kind" binding:"required"`
	HappenedAt  string       `json:"happened_at" binding:"required"`
	InputID     *int64       `json:"input_id"`
	Dose        *float64     `json:"dose"`
	DoseUnit    *string      `json:"dose_unit"`
	Operator    string       `json:"operator" binding:"required"`
	Photos      StringMap    `json:"photos"`
	Geo         *string      `json:"geo"`
	Note        *string      `json:"note"`
}

type BatchSubmitRecordsRequest struct {
	Records []SubmitRecordRequest `json:"records" binding:"required,min=1,dive"`
}

// RejectedRecord 提交时被拒的记录及原因（保证交上来的每条都有交代）
type RejectedRecord struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

type SubmitRecordsResult struct {
	Total      int              `json:"total"`
	Inserted   int              `json:"inserted"`
	Duplicates int              `json:"duplicates"`
	Rejected   []RejectedRecord `json:"rejected"`
}

// MergeRun 一次合并运行的汇总（合并前后总数快照，供对账）
type MergeRun struct {
	ID               int64     `db:"id" json:"id"`
	Status           string    `db:"status" json:"status"`
	TotalIn          int       `db:"total_in" json:"total_in"`
	GroupsFormed     int       `db:"groups_formed" json:"groups_formed"`
	Kept             int       `db:"kept" json:"kept"`
	Superseded       int       `db:"superseded" json:"superseded"`
	ConsistentGroups int       `db:"consistent_groups" json:"consistent_groups"`
	EvidenceResolved int       `db:"evidence_resolved" json:"evidence_resolved"`
	PendingConflicts int       `db:"pending_conflicts" json:"pending_conflicts"`
	CreatedAt        time.Time `db:"created_at" json:"created_at"`
}

// MergeGroup 按 地块+作物+日期+操作人 认出的一组“同一件事”
type MergeGroup struct {
	ID           int64     `db:"id" json:"id"`
	RunID        int64     `db:"run_id" json:"run_id"`
	PlotID       int64     `db:"plot_id" json:"plot_id"`
	CropID       string    `db:"crop_id" json:"crop_id"`
	ActivityDate time.Time `db:"activity_date" json:"activity_date"`
	Operator     string    `db:"operator" json:"operator"`
	Status       string    `db:"status" json:"status"`
	KeptRecordID *int64    `db:"kept_record_id" json:"kept_record_id,omitempty"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
}

// DiffValue 某个字段在某条记录上的取值
type DiffValue struct {
	RecordID int64       `json:"record_id"`
	Value    interface{} `json:"value"`
}

// FieldDiff 组内某字段的不一致明细
type FieldDiff struct {
	Field  string      `json:"field"`
	Values []DiffValue `json:"values"`
}

// FieldDiffList 供 jsonb 存取
type FieldDiffList []FieldDiff

func (l FieldDiffList) Value() (driver.Value, error) {
	if l == nil {
		l = FieldDiffList{}
	}
	return json.Marshal(l)
}

func (l *FieldDiffList) Scan(value interface{}) error {
	if value == nil {
		*l = nil
		return nil
	}
	b, ok := value.([]byte)
	if !ok {
		return errors.New("type assertion to []byte failed")
	}
	return json.Unmarshal(b, l)
}

// MergeGroupMember 组成员：每条原始记录的去向
type MergeGroupMember struct {
	ID        int64         `db:"id" json:"id"`
	GroupID   int64         `db:"group_id" json:"group_id"`
	RecordID  int64         `db:"record_id" json:"record_id"`
	IsKept    bool          `db:"is_kept" json:"is_kept"`
	Diffs     FieldDiffList `db:"diffs" json:"diffs,omitempty"`
	CreatedAt time.Time     `db:"created_at" json:"created_at"`
}

// Int64List 供 jsonb 存取
type Int64List []int64

func (l Int64List) Value() (driver.Value, error) {
	if l == nil {
		l = Int64List{}
	}
	return json.Marshal(l)
}

func (l *Int64List) Scan(value interface{}) error {
	if value == nil {
		*l = nil
		return nil
	}
	b, ok := value.([]byte)
	if !ok {
		return errors.New("type assertion to []byte failed")
	}
	return json.Unmarshal(b, l)
}

// MergedRecord 合并产出：并成的一份台账
type MergedRecord struct {
	ID              int64        `db:"id" json:"id"`
	GroupID         int64        `db:"group_id" json:"group_id"`
	RunID           int64        `db:"run_id" json:"run_id"`
	PlotID          int64        `db:"plot_id" json:"plot_id"`
	CropID          string       `db:"crop_id" json:"crop_id"`
	Kind            ActivityKind `db:"kind" json:"kind"`
	HappenedAt      time.Time    `db:"happened_at" json:"happened_at"`
	InputID         *int64       `db:"input_id" json:"input_id,omitempty"`
	Dose            *float64     `db:"dose" json:"dose,omitempty"`
	DoseUnit        *string      `db:"dose_unit" json:"dose_unit,omitempty"`
	Operator        string       `db:"operator" json:"operator"`
	Photos          StringMap    `db:"photos" json:"photos,omitempty"`
	Geo             *string      `db:"geo" json:"geo,omitempty"`
	Note            *string      `db:"note" json:"note,omitempty"`
	SourceRecordIDs Int64List    `db:"source_record_ids" json:"source_record_ids"`
	CreatedAt       time.Time    `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time    `db:"updated_at" json:"updated_at"`
}

// FieldPickMap 字段级挑选：{字段名: 取值来源 record_id}
type FieldPickMap map[string]int64

func (m FieldPickMap) Value() (driver.Value, error) {
	if m == nil {
		m = FieldPickMap{}
	}
	return json.Marshal(m)
}

func (m *FieldPickMap) Scan(value interface{}) error {
	if value == nil {
		*m = nil
		return nil
	}
	b, ok := value.([]byte)
	if !ok {
		return errors.New("type assertion to []byte failed")
	}
	return json.Unmarshal(b, m)
}

// MergeDecision 抉择留档（自动与人工的每一次定夺）
type MergeDecision struct {
	ID           int64        `db:"id" json:"id"`
	GroupID      int64        `db:"group_id" json:"group_id"`
	RunID        int64        `db:"run_id" json:"run_id"`
	Rule         string       `db:"rule" json:"rule"`
	DecidedBy    string       `db:"decided_by" json:"decided_by"`
	KeptRecordID int64        `db:"kept_record_id" json:"kept_record_id"`
	FieldPicks   FieldPickMap `db:"field_picks" json:"field_picks,omitempty"`
	Note         *string      `db:"note" json:"note,omitempty"`
	CreatedAt    time.Time    `db:"created_at" json:"created_at"`
}

// ResolveGroupRequest 人工挑拣请求
type ResolveGroupRequest struct {
	DecidedBy    string           `json:"decided_by" binding:"required"`
	KeptRecordID *int64           `json:"kept_record_id"`
	FieldPicks   map[string]int64 `json:"field_picks"`
	Note         string           `json:"note"`
}

// ReconcileResult 对账结果：合并前后总数核对，不符时给出具体记录
type ReconcileResult struct {
	RunID               int64    `json:"run_id"`
	OK                  bool     `json:"ok"`
	TotalIn             int      `json:"total_in"`              // 合并前总数
	Accounted           int      `json:"accounted"`             // 已入账（组成员）条数
	Kept                int      `json:"kept"`                  // 合并后保留条数
	Superseded          int      `json:"superseded"`            // 被并掉条数
	MissingRecordIDs    []int64  `json:"missing_record_ids"`    // 收了但没进任何组的记录
	UnexpectedRecordIDs []int64  `json:"unexpected_record_ids"` // 进了组但不属于本次运行的记录
	GroupsWithoutKept   []int64  `json:"groups_without_kept"`   // 没有保留记录的组
	GroupsWithoutOutput []int64  `json:"groups_without_output"` // 没有合并产出的组
	Problems            []string `json:"problems"`
}
