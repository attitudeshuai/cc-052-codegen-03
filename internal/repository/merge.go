package repository

import (
	"cc-052/internal/model"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

type MergeRepo struct {
	db *sqlx.DB
}

func NewMergeRepo(db *sqlx.DB) *MergeRepo {
	return &MergeRepo{db: db}
}

// CreateRun 把引擎结果落成工作单 + 明细，并把两边记录「认领」到该单，
// 防止它们同时进入另一张单。整过程一个事务。
func (r *MergeRepo) CreateRun(run *model.MergeRun, items []*model.MergeItem) (int64, error) {
	tx, err := r.db.Beginx()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	err = tx.QueryRow(`INSERT INTO merge_run
		(farm_id, status, device_in, ledger_in, device_only, ledger_only, matched_pairs,
		 consistent, conflicts, resolved, unresolved, skipped, note, created_by)
		VALUES ($1,'open',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING id, created_at`,
		run.FarmID, run.DeviceIn, run.LedgerIn, run.DeviceOnly, run.LedgerOnly,
		run.MatchedPairs, run.Consistent, run.Conflicts, run.Resolved, run.Unresolved,
		run.Skipped, run.Note, run.CreatedBy).Scan(&run.ID, &run.CreatedAt)
	if err != nil {
		return 0, err
	}

	for _, it := range items {
		it.RunID = run.ID
		err = tx.QueryRow(`INSERT INTO merge_item
			(run_id, match_type, status, plot_id, plot_name, crop_id, happened_on, operator_norm,
			 activity_id, ledger_id, winner_side, new_input_id, resolution_note, resolved_by, resolved_at,
			 differences)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
			RETURNING id, created_at`,
			it.RunID, it.MatchType, it.Status, it.PlotID, it.PlotName, it.CropID,
			it.HappenedOn, it.OperatorNorm, it.ActivityID, it.LedgerID, it.WinnerSide,
			it.NewInputID, it.ResolutionNote, it.ResolvedBy, it.ResolvedAt,
			it.Differences).Scan(&it.ID, &it.CreatedAt)
		if err != nil {
			return 0, err
		}
		if it.ActivityID != nil {
			res, err := tx.Exec(`UPDATE activity SET merge_run_id=$1 WHERE id=$2 AND merge_run_id IS NULL`,
				run.ID, *it.ActivityID)
			if err != nil {
				return 0, err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return 0, fmt.Errorf("采集端记录 %d 已属于另一张合并工作单，请刷新后重试", *it.ActivityID)
			}
		}
		if it.LedgerID != nil {
			newStatus := "pending"
			if it.Status == "conflict" {
				newStatus = "conflict"
			}
			res, err := tx.Exec(`UPDATE ledger_record SET merge_run_id=$1, status=$2
				WHERE id=$3 AND merge_run_id IS NULL`, run.ID, newStatus, *it.LedgerID)
			if err != nil {
				return 0, err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return 0, fmt.Errorf("台账记录 %d 已属于另一张合并工作单，请刷新后重试", *it.LedgerID)
			}
		}
	}

	if err := r.writeAudit(tx, run.ID, nil, "create", orEmpty(run.CreatedBy), map[string]interface{}{
		"device_in": run.DeviceIn, "ledger_in": run.LedgerIn,
		"matched_pairs": run.MatchedPairs, "conflicts": run.Conflicts,
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return run.ID, nil
}

func (r *MergeRepo) GetRun(id int64) (*model.MergeRun, error) {
	var run model.MergeRun
	if err := r.db.Get(&run, `SELECT id, farm_id, status, device_in, ledger_in, device_only, ledger_only,
		matched_pairs, consistent, conflicts, resolved, unresolved, skipped, output_count,
		note, created_by, created_at, applied_at FROM merge_run WHERE id=$1`, id); err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *MergeRepo) ListRuns(limit int) ([]model.MergeRun, error) {
	var runs []model.MergeRun
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if err := r.db.Select(&runs, `SELECT id, farm_id, status, device_in, ledger_in, device_only, ledger_only,
		matched_pairs, consistent, conflicts, resolved, unresolved, skipped, output_count,
		note, created_by, created_at, applied_at FROM merge_run ORDER BY id DESC LIMIT $1`, limit); err != nil {
		return nil, err
	}
	return runs, nil
}

func (r *MergeRepo) ListItems(runID int64) ([]*model.MergeItem, error) {
	var items []*model.MergeItem
	if err := r.db.Select(&items, `SELECT id, run_id, match_type, status, plot_id, plot_name, crop_id,
		happened_on, operator_norm, activity_id, ledger_id, winner_side, chosen_activity_id,
		new_input_id, resolution_note, resolved_by, resolved_at, applied_activity_id,
		differences, created_at FROM merge_item WHERE run_id=$1 ORDER BY id`, runID); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return items, nil
	}

	// 批量带出两边快照，避免 N+1。
	actIDs, ledIDs := map[int64]bool{}, map[int64]bool{}
	for _, it := range items {
		if it.ActivityID != nil {
			actIDs[*it.ActivityID] = true
		}
		if it.LedgerID != nil {
			ledIDs[*it.LedgerID] = true
		}
	}
	acts := map[int64]*model.Activity{}
	if len(actIDs) > 0 {
		var rows []model.Activity
		q, args, err := inQuery(`SELECT id, batch_id, client_uuid, kind, happened_at, input_id, dose,
			dose_unit, operator, photos, geo, created_at, source, merge_run_id, merge_state, note
			FROM activity WHERE id IN (?)`, keysOf(actIDs))
		if err != nil {
			return nil, err
		}
		if err := r.db.Select(&rows, r.db.Rebind(q), args...); err != nil {
			return nil, err
		}
		for i := range rows {
			a := rows[i]
			acts[a.ID] = &a
		}
	}
	leds := map[int64]*model.LedgerRecord{}
	if len(ledIDs) > 0 {
		var rows []model.LedgerRecord
		q, args, err := inQuery(`SELECT id, ledger_batch, source_ref, plot_name, plot_id, crop_id,
			kind, happened_on, operator_norm, operator_raw, input_name, input_id, dose, dose_unit,
			has_evidence, evidence_ref, raw, status, merged_batch_id, merged_activity_id,
			merge_run_id, created_at FROM ledger_record WHERE id IN (?)`, keysOf(ledIDs))
		if err != nil {
			return nil, err
		}
		if err := r.db.Select(&rows, r.db.Rebind(q), args...); err != nil {
			return nil, err
		}
		for i := range rows {
			l := rows[i]
			leds[l.ID] = &l
		}
	}
	for _, it := range items {
		if it.ActivityID != nil {
			it.Activity = acts[*it.ActivityID]
		}
		if it.LedgerID != nil {
			it.Ledger = leds[*it.LedgerID]
		}
	}
	return items, nil
}

func (r *MergeRepo) GetItem(runID, itemID int64) (*model.MergeItem, error) {
	var it model.MergeItem
	err := r.db.Get(&it, `SELECT id, run_id, match_type, status, plot_id, plot_name, crop_id,
		happened_on, operator_norm, activity_id, ledger_id, winner_side, chosen_activity_id,
		new_input_id, resolution_note, resolved_by, resolved_at, applied_activity_id,
		differences, created_at FROM merge_item WHERE id=$1 AND run_id=$2`, itemID, runID)
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// ResolveItem 记录人工取舍（留档），并重算工作单计数。
func (r *MergeRepo) ResolveItem(runID, itemID int64, winner string, targetBatchID, newInputID *int64,
	note, actor string) (*model.MergeItem, error) {

	tx, err := r.db.Beginx()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var it model.MergeItem
	if err := tx.Get(&it, `SELECT id, run_id, match_type, status, activity_id, ledger_id, winner_side,
		resolved_at, differences FROM merge_item WHERE id=$1 AND run_id=$2 FOR UPDATE`, itemID, runID); err != nil {
		return nil, err
	}
	if err := assertOpen(tx, runID); err != nil {
		return nil, err
	}
	if winner != "device" && winner != "ledger" {
		return nil, fmt.Errorf("winner must be device or ledger")
	}
	if it.MatchType == "device_only" && winner == "ledger" {
		return nil, fmt.Errorf("该事件只有采集端记录，不能改取台账侧")
	}
	if it.MatchType == "ledger_only" {
		if winner == "device" {
			return nil, fmt.Errorf("该事件只有台账记录，不能改取采集端侧")
		}
		if targetBatchID == nil || *targetBatchID == 0 {
			return nil, fmt.Errorf("仅台账有的记录，必须指定 target_batch_id 才能确认")
		}
	}

	_, err = tx.Exec(`UPDATE merge_item SET
		winner_side=$1, new_input_id=$2, resolution_note=$3, resolved_by=$4,
		resolved_at=NOW() WHERE id=$5`,
		winner, newInputID, note, actor, itemID)
	if err != nil {
		return nil, err
	}
	// 目标批次先记在备注明细里（apply 时使用）：用 resolution_note 之外的列存会更清晰，
	// 这里通过 new_input_id 旁的临时表不合适——target_batch 直接落 audit + chosen_activity 暂存不了。
	detail := map[string]interface{}{
		"winner": winner, "target_batch_id": targetBatchID, "new_input_id": newInputID, "note": note,
	}
	if targetBatchID != nil {
		// 存进 resolution_note 会覆盖人工备注，因此目标批次写入 audit 详情并同步到台账 merged_batch_id。
		if _, err := tx.Exec(`UPDATE ledger_record SET merged_batch_id=$1 WHERE id=$2`,
			*targetBatchID, deref(it.LedgerID)); err != nil {
			return nil, err
		}
	}
	if err := r.writeAudit(tx, runID, &itemID, "resolve", actor, detail); err != nil {
		return nil, err
	}
	if err := r.recount(tx, runID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.GetItem(runID, itemID)
}

// SkipItem 人工核销：该事件不进最终台账。
func (r *MergeRepo) SkipItem(runID, itemID int64, reason, actor string) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var it model.MergeItem
	if err := tx.Get(&it, `SELECT id, activity_id, ledger_id FROM merge_item
		WHERE id=$1 AND run_id=$2 FOR UPDATE`, itemID, runID); err != nil {
		return err
	}
	if err := assertOpen(tx, runID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE merge_item SET status='skipped', resolution_note=$1,
		resolved_by=$2, resolved_at=NOW(), winner_side=NULL WHERE id=$3`, reason, actor, itemID); err != nil {
		return err
	}
	if it.ActivityID != nil {
		if _, err := tx.Exec(`UPDATE activity SET merge_state='skipped' WHERE id=$1`, *it.ActivityID); err != nil {
			return err
		}
	}
	if it.LedgerID != nil {
		if _, err := tx.Exec(`UPDATE ledger_record SET status='skipped' WHERE id=$1`, *it.LedgerID); err != nil {
			return err
		}
	}
	if err := r.writeAudit(tx, runID, &itemID, "skip", actor,
		map[string]interface{}{"reason": reason}); err != nil {
		return err
	}
	if err := r.recount(tx, runID); err != nil {
		return err
	}
	return tx.Commit()
}

// Unresolve 撤销一次裁决（改判用）：恢复明细状态、清掉核销标记。
func (r *MergeRepo) Unresolve(runID, itemID int64, actor string) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var it model.MergeItem
	if err := tx.Get(&it, `SELECT id, match_type, status, activity_id, ledger_id, differences
		FROM merge_item WHERE id=$1 AND run_id=$2 FOR UPDATE`, itemID, runID); err != nil {
		return err
	}
	if err := assertOpen(tx, runID); err != nil {
		return err
	}
	// 恢复成建单时的原始状态：一致对→consistent，打架对→conflict，单边→pending。
	original := "pending"
	if it.MatchType == "matched" {
		if len(it.Differences) == 0 {
			original = "consistent"
		} else {
			original = "conflict"
		}
	}
	if _, err := tx.Exec(`UPDATE merge_item SET status=$1, winner_side=NULL, new_input_id=NULL,
		resolution_note=NULL, resolved_by=NULL, resolved_at=NULL WHERE id=$2`, original, itemID); err != nil {
		return err
	}
	if it.ActivityID != nil {
		if _, err := tx.Exec(`UPDATE activity SET merge_state=NULL WHERE id=$1`, *it.ActivityID); err != nil {
			return err
		}
	}
	if it.LedgerID != nil {
		// 建单时只有「打架对」的台账行会被置为 conflict，其余是 pending。
		if _, err := tx.Exec(`UPDATE ledger_record SET status=CASE WHEN $1 THEN 'conflict' ELSE 'pending' END
			WHERE id=$2`, original == "conflict", *it.LedgerID); err != nil {
			return err
		}
	}
	if err := r.writeAudit(tx, runID, &itemID, "unresolve", actor, nil); err != nil {
		return err
	}
	if err := r.recount(tx, runID); err != nil {
		return err
	}
	return tx.Commit()
}

// ApplyAction 一条明细的落地动作（由 service 依据裁决结果生成）。
type ApplyAction struct {
	ItemID        int64
	Kind          string // keep | overwrite | insert_ledger | skip
	ActivityID    int64  // keep/overwrite 的现有 activity
	LedgerID      int64  // 关联台账行
	TargetBatchID int64  // insert_ledger 时的目标批次
	ClientUUID    string // insert_ledger 时的幂等码
	Operator      string
	InputID       *int64 // 最终投入品（含人工改写）
	Dose          *float64
	DoseUnit      *string
	HappenedAt    time.Time
	EvidenceNote  string
}

// Apply 把所有处理结果在一个事务里落成「一份」，返回产出条数。
// 提交前重算计数并复核恒等式，对不上直接回滚。
func (r *MergeRepo) Apply(runID int64, actions []ApplyAction, actor string) (int, error) {
	tx, err := r.db.Beginx()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var status string
	if err := tx.Get(&status, `SELECT status FROM merge_run WHERE id=$1 FOR UPDATE`, runID); err != nil {
		return 0, err
	}
	if status == "applied" {
		return 0, fmt.Errorf("run %d 已落地，不能重复执行", runID)
	}
	if status == "cancelled" {
		return 0, fmt.Errorf("run %d 已取消", runID)
	}

	var unresolved int
	if err := tx.Get(&unresolved, `SELECT count(*) FROM merge_item
		WHERE run_id=$1 AND status NOT IN ('skipped') AND resolved_at IS NULL
		  AND status <> 'consistent'`, runID); err != nil {
		return 0, err
	}
	if unresolved > 0 {
		return 0, fmt.Errorf("还有 %d 条没人工处理完，不能落地（逐项见 merge_item）", unresolved)
	}

	output := 0
	for _, a := range actions {
		switch a.Kind {
		case "skip":
			// 核销项不产出，状态已在 SkipItem 落好，这里只补 applied 标记。
			_, err = tx.Exec(`UPDATE merge_item SET applied_activity_id=NULL WHERE id=$1`, a.ItemID)
		case "keep":
			// 以采集端原记录为准；人工可能只改了投入品字典项。
			if a.InputID != nil {
				_, err = tx.Exec(`UPDATE activity SET merge_state='kept', input_id=$1 WHERE id=$2`,
					*a.InputID, a.ActivityID)
			} else {
				_, err = tx.Exec(`UPDATE activity SET merge_state='kept' WHERE id=$1`, a.ActivityID)
			}
			if err == nil {
				_, err = tx.Exec(`UPDATE merge_item SET chosen_activity_id=$1, applied_activity_id=$1 WHERE id=$2`,
					a.ActivityID, a.ItemID)
			}
			if err == nil && a.LedgerID != 0 {
				err = markLedgerMerged(tx, a.LedgerID, runID, batchOf(tx, a.ActivityID), a.ActivityID)
			}
			output++
		case "overwrite":
			// 以台账为准：把台账字段（含可能不同的操作类型）覆写到现有 activity
			// （保留同一 id、照片与上报凭据）。
			_, err = tx.Exec(`UPDATE activity a SET
				kind=l.kind, input_id=$1, dose=$2, dose_unit=$3, merge_state='kept', note=$4
				FROM ledger_record l
				WHERE a.id=$5 AND l.id=$6`,
				a.InputID, a.Dose, a.DoseUnit, a.EvidenceNote, a.ActivityID, a.LedgerID)
			if err == nil {
				_, err = tx.Exec(`UPDATE merge_item SET chosen_activity_id=$1, applied_activity_id=$1 WHERE id=$2`,
					a.ActivityID, a.ItemID)
			}
			if err == nil && a.LedgerID != 0 {
				var batchID int64
				if e := tx.Get(&batchID, `SELECT batch_id FROM activity WHERE id=$1`, a.ActivityID); e == nil {
					err = markLedgerMerged(tx, a.LedgerID, runID, batchID, a.ActivityID)
				}
			}
			output++
		case "insert_ledger":
			var newID int64
			err = tx.QueryRow(`INSERT INTO activity
				(batch_id, client_uuid, kind, happened_at, input_id, dose, dose_unit, operator,
				 photos, geo, source, merge_run_id, merge_state, note)
				SELECT $1,$2,kind,$3,$4,$5,$6,$7,
				       jsonb_build_object('ledger_id',id,'evidence_ref',evidence_ref),
				       NULL,'ledger',$8,'kept',$9
				FROM ledger_record WHERE id=$10
				RETURNING id`,
				a.TargetBatchID, a.ClientUUID, a.HappenedAt, a.InputID, a.Dose,
				a.DoseUnit, a.Operator, runID, a.EvidenceNote, a.LedgerID).Scan(&newID)
			if err == nil {
				_, err = tx.Exec(`UPDATE merge_item SET chosen_activity_id=$1, applied_activity_id=$1 WHERE id=$2`,
					newID, a.ItemID)
			}
			if err == nil {
				err = markLedgerMerged(tx, a.LedgerID, runID, a.TargetBatchID, newID)
			}
			output++
		default:
			err = fmt.Errorf("unknown apply action: %s", a.Kind)
		}
		if err != nil {
			return 0, fmt.Errorf("item %d apply (%s): %w", a.ItemID, a.Kind, err)
		}
	}

	if err := r.recount(tx, runID); err != nil {
		return 0, err
	}

	// 落地前对账恒等式（对不上回滚，错误里带具体差在哪一项）。
	var c struct {
		DeviceIn   int `db:"device_in"`
		LedgerIn   int `db:"ledger_in"`
		Matched    int `db:"matched_pairs"`
		DOnly      int `db:"device_only"`
		LOnly      int `db:"ledger_only"`
		Resolved   int `db:"resolved"`
		Unresolved int `db:"unresolved"`
		Skipped    int `db:"skipped"`
	}
	if err := tx.Get(&c, `SELECT device_in, ledger_in, matched_pairs, device_only, ledger_only,
		resolved, unresolved, skipped FROM merge_run WHERE id=$1`, runID); err != nil {
		return 0, err
	}
	if c.DeviceIn != c.Matched+c.DOnly {
		return 0, fmt.Errorf("对账失败: 采集端 %d != 配对 %d + 单边 %d", c.DeviceIn, c.Matched, c.DOnly)
	}
	if c.LedgerIn != c.Matched+c.LOnly {
		return 0, fmt.Errorf("对账失败: 台账 %d != 配对 %d + 单边 %d", c.LedgerIn, c.Matched, c.LOnly)
	}
	events := c.Matched + c.DOnly + c.LOnly
	if events != c.Resolved+c.Unresolved {
		return 0, fmt.Errorf("对账失败: 事件 %d != 已决 %d + 未决 %d", events, c.Resolved, c.Unresolved)
	}
	if c.Unresolved != 0 {
		return 0, fmt.Errorf("对账失败: 仍有 %d 条未决，不能落地（逐条核对 merge_item.resolved_at）", c.Unresolved)
	}
	if output != c.Resolved-c.Skipped {
		return 0, fmt.Errorf("对账失败: 产出 %d != 已决 %d - 核销 %d（逐条核对 merge_item.applied_activity_id）",
			output, c.Resolved, c.Skipped)
	}
	if _, err := tx.Exec(`UPDATE merge_run SET status='applied', output_count=$1, applied_at=NOW()
		WHERE id=$2`, output, runID); err != nil {
		return 0, err
	}
	if err := r.writeAudit(tx, runID, nil, "apply", actor,
		map[string]interface{}{"output_count": output}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return output, nil
}

// Cancel 取消工作单并释放两边记录的认领，使它们能重新参与合并。
func (r *MergeRepo) Cancel(runID int64, actor string) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status string
	if err := tx.Get(&status, `SELECT status FROM merge_run WHERE id=$1 FOR UPDATE`, runID); err != nil {
		return err
	}
	if status != "open" {
		return fmt.Errorf("只有 open 状态的工作单可取消，当前状态: %s", status)
	}
	if _, err := tx.Exec(`UPDATE activity SET merge_run_id=NULL, merge_state=NULL
		WHERE merge_run_id=$1`, runID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE ledger_record SET merge_run_id=NULL,
		merged_batch_id=NULL, merged_activity_id=NULL,
		status=CASE WHEN status IN ('skipped','conflict') THEN 'pending' ELSE status END
		WHERE merge_run_id=$1`, runID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE merge_run SET status='cancelled' WHERE id=$1`, runID); err != nil {
		return err
	}
	if err := r.writeAudit(tx, runID, nil, "cancel", actor, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// assertOpen 确保工作单仍可进行人工处理。
func assertOpen(tx *sqlx.Tx, runID int64) error {
	var status string
	if err := tx.Get(&status, `SELECT status FROM merge_run WHERE id=$1 FOR UPDATE`, runID); err != nil {
		return err
	}
	if status != "open" {
		return fmt.Errorf("工作单当前状态为 %s，不能再修改取舍", status)
	}
	return nil
}

func (r *MergeRepo) AuditTrail(runID int64) ([]model.MergeAuditLog, error) {
	var logs []model.MergeAuditLog
	if err := r.db.Select(&logs, `SELECT id, run_id, item_id, action, actor, detail, created_at
		FROM merge_audit_log WHERE run_id=$1 ORDER BY id`, runID); err != nil {
		return nil, err
	}
	return logs, nil
}

// recount 按明细重算 run 的各项计数（任何裁决/落地后调用，保证计数来自事实）。
func (r *MergeRepo) recount(tx *sqlx.Tx, runID int64) error {
	_, err := tx.Exec(`UPDATE merge_run r SET
		consistent = (SELECT count(*) FROM merge_item WHERE run_id=r.id AND status='consistent'),
		conflicts  = (SELECT count(*) FROM merge_item WHERE run_id=r.id AND match_type='matched'
		              AND status='conflict'),
		skipped    = (SELECT count(*) FROM merge_item WHERE run_id=r.id AND status='skipped'),
		unresolved = (SELECT count(*) FROM merge_item WHERE run_id=r.id
		              AND status NOT IN ('consistent','skipped') AND resolved_at IS NULL),
		resolved   = (SELECT count(*) FROM merge_item WHERE run_id=r.id
		              AND (status='consistent' OR status='skipped' OR resolved_at IS NOT NULL))
		WHERE r.id=$1`, runID)
	return err
}

func (r *MergeRepo) writeAudit(tx sqlx.Ext, runID int64, itemID *int64, action, actor string,
	detail map[string]interface{}) error {
	if detail == nil {
		detail = map[string]interface{}{}
	}
	b, _ := json.Marshal(detail)
	_, err := tx.Exec(`INSERT INTO merge_audit_log (run_id, item_id, action, actor, detail)
		VALUES ($1,$2,$3,$4,$5)`, runID, itemID, action, actor, string(b))
	return err
}

func markLedgerMerged(tx sqlx.Ext, ledgerID, runID, batchID, activityID int64) error {
	_, err := tx.Exec(`UPDATE ledger_record SET status='merged', merge_run_id=$1,
		merged_batch_id=$2, merged_activity_id=$3 WHERE id=$4`, runID, batchID, activityID, ledgerID)
	return err
}

func batchOf(tx *sqlx.Tx, activityID int64) int64 {
	var b int64
	_ = tx.Get(&b, `SELECT batch_id FROM activity WHERE id=$1`, activityID)
	return b
}

func orEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func keysOf(m map[int64]bool) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func inQuery(query string, args []int64) (string, []interface{}, error) {
	return sqlx.In(query, args)
}
