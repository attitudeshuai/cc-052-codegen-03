package service

import (
	"cc-052/internal/model"
	"cc-052/internal/repository"
	"cc-052/pkg/mergenorm"
	"encoding/json"
	"fmt"
	"time"
)

type LedgerService struct {
	repo      *repository.LedgerRepo
	plotRepo  *repository.PlotRepo
	inputRepo *repository.InputMaterialRepo
}

func NewLedgerService(repo *repository.LedgerRepo, plotRepo *repository.PlotRepo, inputRepo *repository.InputMaterialRepo) *LedgerService {
	return &LedgerService{repo: repo, plotRepo: plotRepo, inputRepo: inputRepo}
}

// Import 解析并暂存一批手工台账。幂等键为 (ledger_batch, source_ref)。
// 解析不了的内容不静默丢弃：日期/类型错误按行失败返回；地块认不出来的行
// 照常入库（plot_id 留空），留到合并阶段作为待处理项摆给人看。
func (s *LedgerService) Import(req *model.ImportLedgerRequest) (*model.ImportLedgerResult, error) {
	batch := req.LedgerBatch
	if batch == "" {
		batch = fmt.Sprintf("L%d", time.Now().UnixNano())
	}

	plotIdx, err := s.plotRepo.NameIndex()
	if err != nil {
		return nil, err
	}
	inputMap := map[string]int64{}
	if inputs, err := s.inputRepo.List(); err == nil {
		for _, m := range inputs {
			inputMap[mergenorm.Crop(m.Name)] = m.ID
		}
	}

	res := &model.ImportLedgerResult{LedgerBatch: batch}
	var records []model.LedgerRecord

	for i, row := range req.Rows {
		res.Received++

		kind := model.ActivityKind(row.Kind)
		if !validKind(kind) {
			res.Failed++
			res.RowErrors = append(res.RowErrors, model.LedgerRowError{
				SourceRef: row.SourceRef, Reason: "unknown kind: " + row.Kind})
			continue
		}
		on, err := time.ParseInLocation("2006-01-02", row.HappenedOn, time.Local)
		if err != nil {
			res.Failed++
			res.RowErrors = append(res.RowErrors, model.LedgerRowError{
				SourceRef: row.SourceRef, Reason: "bad happened_on, want YYYY-MM-DD"})
			continue
		}
		opRaw := row.Operator
		if mergenorm.Operator(opRaw) == "" {
			res.Failed++
			res.RowErrors = append(res.RowErrors, model.LedgerRowError{
				SourceRef: row.SourceRef, Reason: "empty operator"})
			continue
		}

		rec := model.LedgerRecord{
			LedgerBatch:  batch,
			SourceRef:    row.SourceRef,
			PlotName:     row.PlotName,
			PlotID:       row.PlotID,
			Kind:         kind,
			HappenedOn:   on,
			OperatorNorm: mergenorm.Operator(opRaw),
			InputName:    strPtrOrNil(row.InputName),
			InputID:      row.InputID,
			Dose:         row.Dose,
			DoseUnit:     strPtrOrNil(row.DoseUnit),
			HasEvidence:  row.HasEvidence,
			EvidenceRef:  strPtrOrNil(row.EvidenceRef),
			Status:       model.LedgerPending,
		}
		op := opRaw
		rec.OperatorRaw = &op
		if row.CropID != "" {
			c := mergenorm.Crop(row.CropID)
			rec.CropID = &c
		}
		if raw, err := json.Marshal(row); err == nil {
			rec.Raw = model.StringMap{"row": json.RawMessage(raw), "line": i + 1}
		}

		// 显式给了 plot_id 就信调用方；否则按地块名在（指定农场的）地块里认。
		if rec.PlotID == nil {
			if pid, ok := resolvePlot(plotIdx, req.FarmID, row.PlotName); ok {
				rec.PlotID = &pid
			}
		}
		// 没给 input_id 时，按投入品名称在字典里认。
		if rec.InputID == nil && row.InputName != "" {
			if id, ok := inputMap[mergenorm.Crop(row.InputName)]; ok {
				rec.InputID = &id
			}
		}

		records = append(records, rec)
	}

	imported, err := s.repo.InsertMany(records)
	if err != nil {
		return nil, err
	}
	res.Imported = imported
	res.Duplicated = len(records) - imported
	return res, nil
}

// ListPending 透传给 handler。
func (s *LedgerService) ListPending(farmID *int64) ([]model.LedgerRecord, error) {
	return s.repo.ListPending(farmID)
}

func validKind(k model.ActivityKind) bool {
	switch k {
	case model.ActivityFertilize, model.ActivityPesticide, model.ActivityIrrigation, model.ActivityWeed:
		return true
	}
	return false
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// resolvePlot 先在指定农场里按名认；没指定农场则要求全社唯一，重名时不认
// （歧义必须由人在合并时处理，不能乱点鸳鸯谱）。
func resolvePlot(idx map[int64]map[string]model.Plot, farmID *int64, name string) (int64, bool) {
	key := mergenorm.PlotName(name)
	if farmID != nil {
		if m, ok := idx[*farmID]; ok {
			if p, ok := m[key]; ok {
				return p.ID, true
			}
		}
		return 0, false
	}
	var found model.Plot
	hits := 0
	for _, m := range idx {
		if p, ok := m[key]; ok {
			found = p
			hits++
		}
	}
	if hits == 1 {
		return found.ID, true
	}
	return 0, false
}
