package repository

import (
	"database/sql"

	"cc-052/internal/model"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

type FieldRecordRepo struct {
	db *sqlx.DB
}

func NewFieldRecordRepo(db *sqlx.DB) *FieldRecordRepo {
	return &FieldRecordRepo{db: db}
}

const fieldRecordCols = `id, source, client_uuid, upload_batch, plot_id, crop_id, kind, happened_at,
	input_id, dose, dose_unit, operator, photos, geo, note, merged_run_id, created_at`

// Create 插入一条原始记录；client_uuid 冲突（采集端重传）时返回 inserted=false
func (r *FieldRecordRepo) Create(rec *model.FieldRecord) (inserted bool, err error) {
	query := `INSERT INTO field_record (source, client_uuid, upload_batch, plot_id, crop_id, kind, happened_at,
	            input_id, dose, dose_unit, operator, photos, geo, note)
	          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	          ON CONFLICT (client_uuid) WHERE client_uuid IS NOT NULL DO NOTHING
	          RETURNING id, created_at`
	err = r.db.QueryRow(query, rec.Source, rec.ClientUUID, rec.UploadBatch, rec.PlotID, rec.CropID,
		rec.Kind, rec.HappenedAt, rec.InputID, rec.Dose, rec.DoseUnit, rec.Operator,
		rec.Photos, rec.Geo, rec.Note).Scan(&rec.ID, &rec.CreatedAt)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// BatchCreate 批量插入，返回插入数与重复数（client_uuid 幂等）
func (r *FieldRecordRepo) BatchCreate(records []model.FieldRecord) (inserted int, duplicates int, err error) {
	tx, err := r.db.Beginx()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Preparex(`INSERT INTO field_record (source, client_uuid, upload_batch, plot_id, crop_id, kind,
		happened_at, input_id, dose, dose_unit, operator, photos, geo, note)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (client_uuid) WHERE client_uuid IS NOT NULL DO NOTHING`)
	if err != nil {
		return 0, 0, err
	}
	defer stmt.Close()

	for i := range records {
		rec := &records[i]
		result, err := stmt.Exec(rec.Source, rec.ClientUUID, rec.UploadBatch, rec.PlotID, rec.CropID,
			rec.Kind, rec.HappenedAt, rec.InputID, rec.Dose, rec.DoseUnit, rec.Operator,
			rec.Photos, rec.Geo, rec.Note)
		if err != nil {
			return 0, 0, err
		}
		if n, _ := result.RowsAffected(); n > 0 {
			inserted++
		} else {
			duplicates++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return inserted, duplicates, nil
}

// ListUnmerged 尚未被任何合并运行收编的记录，按提交先后排序
func (r *FieldRecordRepo) ListUnmerged() ([]model.FieldRecord, error) {
	var records []model.FieldRecord
	query := `SELECT ` + fieldRecordCols + ` FROM field_record WHERE merged_run_id IS NULL ORDER BY created_at, id`
	if err := r.db.Select(&records, query); err != nil {
		return nil, err
	}
	return records, nil
}

func (r *FieldRecordRepo) ListAll() ([]model.FieldRecord, error) {
	var records []model.FieldRecord
	query := `SELECT ` + fieldRecordCols + ` FROM field_record ORDER BY created_at, id`
	if err := r.db.Select(&records, query); err != nil {
		return nil, err
	}
	return records, nil
}

// ListByRun 被某次合并运行收编的记录（对账用）
func (r *FieldRecordRepo) ListByRun(runID int64) ([]model.FieldRecord, error) {
	var records []model.FieldRecord
	query := `SELECT ` + fieldRecordCols + ` FROM field_record WHERE merged_run_id = $1 ORDER BY id`
	if err := r.db.Select(&records, query, runID); err != nil {
		return nil, err
	}
	return records, nil
}

func (r *FieldRecordRepo) GetByIDs(ids []int64) ([]model.FieldRecord, error) {
	var records []model.FieldRecord
	if len(ids) == 0 {
		return records, nil
	}
	query := `SELECT ` + fieldRecordCols + ` FROM field_record WHERE id = ANY($1) ORDER BY id`
	if err := r.db.Select(&records, query, pq.Array(ids)); err != nil {
		return nil, err
	}
	return records, nil
}
