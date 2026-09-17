package repository

import (
	"cc-052/internal/model"

	"github.com/jmoiron/sqlx"
)

type LedgerRepo struct {
	db *sqlx.DB
}

func NewLedgerRepo(db *sqlx.DB) *LedgerRepo {
	return &LedgerRepo{db: db}
}

// InsertMany 批量落台账，(ledger_batch, source_ref) 重复的直接跳过，返回实际新增条数。
func (r *LedgerRepo) InsertMany(rows []model.LedgerRecord) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := r.db.Beginx()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Preparex(`INSERT INTO ledger_record
		(ledger_batch, source_ref, plot_name, plot_id, crop_id, kind, happened_on,
		 operator_norm, operator_raw, input_name, input_id, dose, dose_unit,
		 has_evidence, evidence_ref, raw, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,'pending')
		ON CONFLICT (ledger_batch, source_ref) DO NOTHING`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for _, x := range rows {
		res, err := stmt.Exec(x.LedgerBatch, x.SourceRef, x.PlotName, x.PlotID, x.CropID,
			x.Kind, x.HappenedOn, x.OperatorNorm, x.OperatorRaw, x.InputName, x.InputID,
			x.Dose, x.DoseUnit, x.HasEvidence, x.EvidenceRef, x.Raw)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

// ListPending 取出还没进入任何合并批次的台账行。farmID 为空时取全部。
func (r *LedgerRepo) ListPending(farmID *int64) ([]model.LedgerRecord, error) {
	var rows []model.LedgerRecord
	query := `SELECT l.id, l.ledger_batch, l.source_ref, l.plot_name, l.plot_id, l.crop_id,
		l.kind, l.happened_on, l.operator_norm, l.operator_raw, l.input_name, l.input_id,
		l.dose, l.dose_unit, l.has_evidence, l.evidence_ref, l.raw, l.status,
		l.merged_batch_id, l.merged_activity_id, l.merge_run_id, l.created_at
		FROM ledger_record l
		LEFT JOIN plot p ON l.plot_id = p.id
		WHERE l.status = 'pending' AND l.merge_run_id IS NULL
		  AND ($1::bigint IS NULL OR p.farm_id = $1)
		ORDER BY l.happened_on, l.id`
	if err := r.db.Select(&rows, query, farmID); err != nil {
		return nil, err
	}
	return rows, nil
}

// GetByID 取单条（人工处理时核对）。
func (r *LedgerRepo) GetByID(id int64) (*model.LedgerRecord, error) {
	var x model.LedgerRecord
	query := `SELECT id, ledger_batch, source_ref, plot_name, plot_id, crop_id,
		kind, happened_on, operator_norm, operator_raw, input_name, input_id,
		dose, dose_unit, has_evidence, evidence_ref, raw, status,
		merged_batch_id, merged_activity_id, merge_run_id, created_at
		FROM ledger_record WHERE id = $1`
	if err := r.db.Get(&x, query, id); err != nil {
		return nil, err
	}
	return &x, nil
}

// MarkMerged 台账行已进入「一份」。
func (r *LedgerRepo) MarkMerged(tx sqlx.Ext, id, runID, batchID, activityID int64) error {
	_, err := tx.Exec(`UPDATE ledger_record
		SET status='merged', merge_run_id=$2, merged_batch_id=$3, merged_activity_id=$4
		WHERE id=$1`, id, runID, batchID, activityID)
	return err
}

// MarkConflict 配对打架、等待人工挑。
func (r *LedgerRepo) MarkConflict(tx sqlx.Ext, id, runID int64) error {
	_, err := tx.Exec(`UPDATE ledger_record SET status='conflict', merge_run_id=$2 WHERE id=$1`, id, runID)
	return err
}

// MarkSkipped 人工核销。
func (r *LedgerRepo) MarkSkipped(tx sqlx.Ext, id, runID int64) error {
	_, err := tx.Exec(`UPDATE ledger_record SET status='skipped', merge_run_id=$2 WHERE id=$1`, id, runID)
	return err
}
