package repository

import (
	"cc-052/internal/model"

	"github.com/jmoiron/sqlx"
)

type ActivityRepo struct {
	db *sqlx.DB
}

func NewActivityRepo(db *sqlx.DB) *ActivityRepo {
	return &ActivityRepo{db: db}
}

func (r *ActivityRepo) Create(a *model.Activity) error {
	query := `INSERT INTO activity (batch_id, client_uuid, kind, happened_at, input_id, dose, dose_unit, operator, photos, geo)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) 
	          ON CONFLICT (client_uuid) DO NOTHING
	          RETURNING id, created_at`
	return r.db.QueryRow(query, a.BatchID, a.ClientUUID, a.Kind, a.HappenedAt,
		a.InputID, a.Dose, a.DoseUnit, a.Operator, a.Photos, a.Geo).
		Scan(&a.ID, &a.CreatedAt)
}

func (r *ActivityRepo) BatchCreate(activities []model.Activity) (int, error) {
	insertedCount := 0
	tx, err := r.db.Beginx()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Preparex(`INSERT INTO activity (batch_id, client_uuid, kind, happened_at, input_id, dose, dose_unit, operator, photos, geo)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT (client_uuid) DO NOTHING`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	for _, a := range activities {
		result, err := stmt.Exec(a.BatchID, a.ClientUUID, a.Kind, a.HappenedAt,
			a.InputID, a.Dose, a.DoseUnit, a.Operator, a.Photos, a.Geo)
		if err != nil {
			return 0, err
		}
		if n, _ := result.RowsAffected(); n > 0 {
			insertedCount++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return insertedCount, nil
}

func (r *ActivityRepo) ListByBatch(batchID int64) ([]model.Activity, error) {
	var activities []model.Activity
	query := `SELECT id, batch_id, client_uuid, kind, happened_at, input_id, dose, dose_unit,
	          operator, photos, geo, created_at, source, merge_run_id, merge_state, note
	          FROM activity WHERE batch_id = $1 ORDER BY happened_at ASC`
	if err := r.db.Select(&activities, query, batchID); err != nil {
		return nil, err
	}
	return activities, nil
}

// ActivityIdentity 采集端记录 + 它所属批次的地块/作物（配对四要素用）。
type ActivityIdentity struct {
	model.Activity
	PlotID   int64  `db:"plot_id"`
	PlotName string `db:"plot_name"`
	CropID   string `db:"crop_id"`
}

// ListUnmerged 取还没进入任何合并工作单的采集端记录，可按批次或农场收窄。
func (r *ActivityRepo) ListUnmerged(batchIDs []int64, farmID *int64) ([]ActivityIdentity, error) {
	var rows []ActivityIdentity
	query := `SELECT a.id, a.batch_id, a.client_uuid, a.kind, a.happened_at, a.input_id,
		a.dose, a.dose_unit, a.operator, a.photos, a.geo, a.created_at,
		a.source, a.merge_run_id, a.merge_state, a.note,
		b.plot_id AS plot_id, p.name AS plot_name, b.crop_id AS crop_id
		FROM activity a
		JOIN crop_batch b ON a.batch_id = b.id
		JOIN plot p ON b.plot_id = p.id
		WHERE a.merge_run_id IS NULL`
	args := []interface{}{}
	if len(batchIDs) > 0 {
		query += " AND a.batch_id IN (?)"
		args = append(args, batchIDs)
	}
	if farmID != nil {
		query += " AND p.farm_id = ?"
		args = append(args, *farmID)
	}
	query += " ORDER BY a.happened_at, a.id"
	q, qargs, err := sqlx.In(query, args...)
	if err != nil {
		return nil, err
	}
	q = r.db.Rebind(q)
	if err := r.db.Select(&rows, q, qargs...); err != nil {
		return nil, err
	}
	return rows, nil
}
