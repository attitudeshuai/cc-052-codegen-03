package repository

import (
	"cc-052/internal/model"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

type MergeRepo struct {
	db *sqlx.DB
}

func NewMergeRepo(db *sqlx.DB) *MergeRepo {
	return &MergeRepo{db: db}
}

// GroupBundle 一个合并分组及其全部产出，随运行一起落库
type GroupBundle struct {
	Group    *model.MergeGroup
	Members  []model.MergeGroupMember
	Merged   *model.MergedRecord
	Decision *model.MergeDecision
}

const mergeRunCols = `id, status, total_in, groups_formed, kept, superseded,
	consistent_groups, evidence_resolved, pending_conflicts, created_at`

const mergeGroupCols = `id, run_id, plot_id, crop_id, activity_date, operator, status, kept_record_id, created_at`

// SaveRun 把一次合并运行的全部结果放在一个事务里落库：
// 运行记录 -> 各分组/成员/合并产出/自动抉择 -> 标记原始记录已收编 -> 更新运行汇总
func (r *MergeRepo) SaveRun(run *model.MergeRun, bundles []GroupBundle, recordIDs []int64) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	err = tx.QueryRow(`INSERT INTO merge_run (status) VALUES ('running') RETURNING id, created_at`).
		Scan(&run.ID, &run.CreatedAt)
	if err != nil {
		return err
	}

	for _, b := range bundles {
		g := b.Group
		g.RunID = run.ID
		err = tx.QueryRow(`INSERT INTO merge_group (run_id, plot_id, crop_id, activity_date, operator, status, kept_record_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id, created_at`,
			g.RunID, g.PlotID, g.CropID, g.ActivityDate, g.Operator, g.Status, g.KeptRecordID).
			Scan(&g.ID, &g.CreatedAt)
		if err != nil {
			return err
		}

		for i := range b.Members {
			m := &b.Members[i]
			m.GroupID = g.ID
			if _, err = tx.Exec(`INSERT INTO merge_group_member (group_id, record_id, is_kept, diffs)
				VALUES ($1,$2,$3,$4)`, m.GroupID, m.RecordID, m.IsKept, m.Diffs); err != nil {
				return err
			}
		}

		if b.Merged != nil {
			b.Merged.RunID = run.ID
			b.Merged.GroupID = g.ID
			if _, err = tx.Exec(`INSERT INTO merged_record
				(group_id, run_id, plot_id, crop_id, kind, happened_at, input_id, dose, dose_unit,
				 operator, photos, geo, note, source_record_ids)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
				b.Merged.GroupID, b.Merged.RunID, b.Merged.PlotID, b.Merged.CropID, b.Merged.Kind,
				b.Merged.HappenedAt, b.Merged.InputID, b.Merged.Dose, b.Merged.DoseUnit,
				b.Merged.Operator, b.Merged.Photos, b.Merged.Geo, b.Merged.Note,
				b.Merged.SourceRecordIDs); err != nil {
				return err
			}
		}

		if b.Decision != nil {
			b.Decision.RunID = run.ID
			b.Decision.GroupID = g.ID
			if _, err = tx.Exec(`INSERT INTO merge_decision
				(group_id, run_id, rule, decided_by, kept_record_id, field_picks, note)
				VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				b.Decision.GroupID, b.Decision.RunID, b.Decision.Rule, b.Decision.DecidedBy,
				b.Decision.KeptRecordID, b.Decision.FieldPicks, b.Decision.Note); err != nil {
				return err
			}
		}
	}

	// 收编原始记录
	if len(recordIDs) > 0 {
		if _, err = tx.Exec(`UPDATE field_record SET merged_run_id = $1 WHERE id = ANY($2)`,
			run.ID, pq.Array(recordIDs)); err != nil {
			return err
		}
	}

	// 运行汇总
	run.Status = "completed"
	if _, err = tx.Exec(`UPDATE merge_run SET status=$2, total_in=$3, groups_formed=$4, kept=$5,
		superseded=$6, consistent_groups=$7, evidence_resolved=$8, pending_conflicts=$9 WHERE id=$1`,
		run.ID, run.Status, run.TotalIn, run.GroupsFormed, run.Kept, run.Superseded,
		run.ConsistentGroups, run.EvidenceResolved, run.PendingConflicts); err != nil {
		return err
	}

	return tx.Commit()
}

func (r *MergeRepo) GetRun(id int64) (*model.MergeRun, error) {
	var run model.MergeRun
	if err := r.db.Get(&run, `SELECT `+mergeRunCols+` FROM merge_run WHERE id=$1`, id); err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *MergeRepo) ListRuns() ([]model.MergeRun, error) {
	var runs []model.MergeRun
	if err := r.db.Select(&runs, `SELECT `+mergeRunCols+` FROM merge_run ORDER BY id DESC`); err != nil {
		return nil, err
	}
	return runs, nil
}

func (r *MergeRepo) GetGroup(id int64) (*model.MergeGroup, error) {
	var g model.MergeGroup
	if err := r.db.Get(&g, `SELECT `+mergeGroupCols+` FROM merge_group WHERE id=$1`, id); err != nil {
		return nil, err
	}
	return &g, nil
}

// ListGroups 按运行与状态（空串=全部）查分组
func (r *MergeRepo) ListGroups(runID int64, status string) ([]model.MergeGroup, error) {
	var groups []model.MergeGroup
	query := `SELECT ` + mergeGroupCols + ` FROM merge_group WHERE run_id=$1`
	args := []interface{}{runID}
	if status != "" {
		query += ` AND status=$2`
		args = append(args, status)
	}
	query += ` ORDER BY id`
	if err := r.db.Select(&groups, query, args...); err != nil {
		return nil, err
	}
	return groups, nil
}

func (r *MergeRepo) MembersByGroup(groupID int64) ([]model.MergeGroupMember, error) {
	var members []model.MergeGroupMember
	query := `SELECT id, group_id, record_id, is_kept, diffs, created_at
	          FROM merge_group_member WHERE group_id=$1 ORDER BY id`
	if err := r.db.Select(&members, query, groupID); err != nil {
		return nil, err
	}
	return members, nil
}

// MembersByRun 一次运行全部组成员（对账用）
func (r *MergeRepo) MembersByRun(runID int64) ([]model.MergeGroupMember, error) {
	var members []model.MergeGroupMember
	query := `SELECT m.id, m.group_id, m.record_id, m.is_kept, m.diffs, m.created_at
	          FROM merge_group_member m
	          JOIN merge_group g ON g.id = m.group_id
	          WHERE g.run_id=$1 ORDER BY m.id`
	if err := r.db.Select(&members, query, runID); err != nil {
		return nil, err
	}
	return members, nil
}

// RecordsByGroup 组内全部原始记录（按提交先后）
func (r *MergeRepo) RecordsByGroup(groupID int64) ([]model.FieldRecord, error) {
	var records []model.FieldRecord
	query := `SELECT r.id, r.source, r.client_uuid, r.upload_batch, r.plot_id, r.crop_id, r.kind,
		r.happened_at, r.input_id, r.dose, r.dose_unit, r.operator, r.photos, r.geo, r.note,
		r.merged_run_id, r.created_at
	          FROM field_record r
	          JOIN merge_group_member m ON m.record_id = r.id
	          WHERE m.group_id=$1 ORDER BY r.created_at, r.id`
	if err := r.db.Select(&records, query, groupID); err != nil {
		return nil, err
	}
	return records, nil
}

func (r *MergeRepo) GetMergedByGroup(groupID int64) (*model.MergedRecord, error) {
	var m model.MergedRecord
	query := `SELECT id, group_id, run_id, plot_id, crop_id, kind, happened_at, input_id, dose,
		dose_unit, operator, photos, geo, note, source_record_ids, created_at, updated_at
	          FROM merged_record WHERE group_id=$1`
	if err := r.db.Get(&m, query, groupID); err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *MergeRepo) ListMerged(runID int64) ([]model.MergedRecord, error) {
	var out []model.MergedRecord
	query := `SELECT id, group_id, run_id, plot_id, crop_id, kind, happened_at, input_id, dose,
		dose_unit, operator, photos, geo, note, source_record_ids, created_at, updated_at
	          FROM merged_record`
	args := []interface{}{}
	if runID > 0 {
		query += ` WHERE run_id=$1`
		args = append(args, runID)
	}
	query += ` ORDER BY id`
	if err := r.db.Select(&out, query, args...); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *MergeRepo) ListDecisionsByGroup(groupID int64) ([]model.MergeDecision, error) {
	var decisions []model.MergeDecision
	query := `SELECT id, group_id, run_id, rule, decided_by, kept_record_id, field_picks, note, created_at
	          FROM merge_decision WHERE group_id=$1 ORDER BY id`
	if err := r.db.Select(&decisions, query, groupID); err != nil {
		return nil, err
	}
	return decisions, nil
}

// ResolveGroup 人工挑拣落库（一个事务）：更新分组状态与保留记录、重写合并产出、抉择留档
func (r *MergeRepo) ResolveGroup(groupID int64, keptRecordID int64, merged *model.MergedRecord, decision *model.MergeDecision) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err = tx.Exec(`UPDATE merge_group SET status=$2, kept_record_id=$3 WHERE id=$1`,
		groupID, model.GroupStatusResolved, keptRecordID); err != nil {
		return err
	}

	// 成员保留标记同步
	if _, err = tx.Exec(`UPDATE merge_group_member SET is_kept = (record_id=$2) WHERE group_id=$1`,
		groupID, keptRecordID); err != nil {
		return err
	}

	if _, err = tx.Exec(`UPDATE merged_record SET kind=$2, happened_at=$3, input_id=$4, dose=$5,
		dose_unit=$6, operator=$7, photos=$8, geo=$9, note=$10, source_record_ids=$11, updated_at=NOW()
		WHERE group_id=$1`,
		groupID, merged.Kind, merged.HappenedAt, merged.InputID, merged.Dose, merged.DoseUnit,
		merged.Operator, merged.Photos, merged.Geo, merged.Note, merged.SourceRecordIDs); err != nil {
		return err
	}

	if _, err = tx.Exec(`INSERT INTO merge_decision
		(group_id, run_id, rule, decided_by, kept_record_id, field_picks, note)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		decision.GroupID, decision.RunID, decision.Rule, decision.DecidedBy,
		decision.KeptRecordID, decision.FieldPicks, decision.Note); err != nil {
		return err
	}

	return tx.Commit()
}
