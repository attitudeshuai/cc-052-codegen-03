package service

import (
	"cc-052/internal/merge"
	"cc-052/internal/model"
	"cc-052/internal/repository"
	"cc-052/pkg/mergenorm"
	"errors"
	"fmt"
	"time"
)

type MergeService struct {
	mergeRepo    *repository.MergeRepo
	activityRepo *repository.ActivityRepo
	ledgerRepo   *repository.LedgerRepo
	batchRepo    *repository.BatchRepo
	inputRepo    *repository.InputMaterialRepo
}

func NewMergeService(
	mergeRepo *repository.MergeRepo,
	activityRepo *repository.ActivityRepo,
	ledgerRepo *repository.LedgerRepo,
	batchRepo *repository.BatchRepo,
	inputRepo *repository.InputMaterialRepo,
) *MergeService {
	return &MergeService{
		mergeRepo: mergeRepo, activityRepo: activityRepo, ledgerRepo: ledgerRepo,
		batchRepo: batchRepo, inputRepo: inputRepo,
	}
}

// CreateRun 把当前未合并的采集端记录与手工台账跑一遍对账引擎，落成工作单。
// 落库前先过引擎的恒等式自检；对不上直接失败，不会产生半张单。
func (s *MergeService) CreateRun(req *model.CreateMergeRunRequest) (*model.MergeRunDetail, error) {
	devices, err := s.activityRepo.ListUnmerged(req.OnlyBatchIDs, req.FarmID)
	if err != nil {
		return nil, err
	}
	ledgers, err := s.ledgerRepo.ListPending(req.FarmID)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 && len(ledgers) == 0 {
		return nil, errors.New("没有待合并的采集端记录或台账记录")
	}

	inputNames, err := s.inputNameMap()
	if err != nil {
		return nil, err
	}

	dRecs := make([]merge.SideRecord, 0, len(devices))
	for _, a := range devices {
		r := merge.SideRecord{
			Side: merge.SideDevice, ID: a.ID, BatchID: a.BatchID,
			PlotID: a.PlotID, PlotName: a.PlotName, CropID: a.CropID,
			Kind: string(a.Kind), HappenedOn: a.HappenedAt, HappenedAt: a.HappenedAt,
			CreatedAt: a.CreatedAt, Operator: mergenorm.Operator(a.Operator),
			Dose: a.Dose, DoseUnit: derefStr(a.DoseUnit),
		}
		if a.InputID != nil {
			r.InputID = *a.InputID
			r.InputName = inputNames[*a.InputID]
		}
		// 有照片即视为有凭据（采集端的凭据就是现场照片）。
		r.HasEvidence = len(a.Photos) > 0
		dRecs = append(dRecs, r)
	}

	lRecs := make([]merge.SideRecord, 0, len(ledgers))
	for _, l := range ledgers {
		r := merge.SideRecord{
			Side: merge.SideLedger, ID: l.ID,
			PlotName: l.PlotName, Kind: string(l.Kind),
			HappenedOn: l.HappenedOn, CreatedAt: l.CreatedAt,
			Operator: l.OperatorNorm,
			Dose:     l.Dose, DoseUnit: derefStr(l.DoseUnit),
			HasEvidence: l.HasEvidence, EvidenceRef: derefStr(l.EvidenceRef),
			InputName: derefStr(l.InputName),
		}
		if l.PlotID != nil {
			r.PlotID = *l.PlotID
		}
		if l.CropID != nil {
			r.CropID = *l.CropID
		}
		if l.InputID != nil {
			r.InputID = *l.InputID
		}
		lRecs = append(lRecs, r)
	}

	report := merge.Build(dRecs, lRecs)
	if err := report.Verify(); err != nil {
		return nil, fmt.Errorf("对账自检失败，拒绝建单: %w", err)
	}

	run := &model.MergeRun{
		FarmID:       req.FarmID,
		DeviceIn:     report.Counts.DeviceIn,
		LedgerIn:     report.Counts.LedgerIn,
		DeviceOnly:   report.Counts.DeviceOnly,
		LedgerOnly:   report.Counts.LedgerOnly,
		MatchedPairs: report.Counts.MatchedPairs,
		Consistent:   report.Counts.Consistent,
		Conflicts:    report.Counts.Conflicts,
		// 建单即自动确认「仅采集端有」的单边项，故初始已决 = 一致对 + 设备单边；
		// 未决 = 待裁决的冲突对 + 待指定批次的台账单边。
		Resolved:   report.Counts.Consistent + report.Counts.DeviceOnly,
		Unresolved: report.Counts.Conflicts + report.Counts.LedgerOnly,
	}
	if req.Note != "" {
		run.Note = &req.Note
	}
	if req.CreatedBy != "" {
		run.CreatedBy = &req.CreatedBy
	}

	items := make([]*model.MergeItem, 0, len(report.Items))
	for _, d := range report.Items {
		it := &model.MergeItem{
			MatchType:   d.MatchType,
			Status:      d.Status,
			Differences: model.DiffList(d.Differences),
		}
		putIdentity(it, d.Identity)
		if d.Device != nil {
			id := d.Device.ID
			it.ActivityID = &id
		}
		if d.Ledger != nil {
			id := d.Ledger.ID
			it.LedgerID = &id
		}
		if d.SuggestedWinner != "" {
			w := string(d.SuggestedWinner)
			it.WinnerSide = &w
		}
		// 仅采集端有的记录：系统默认保留（可随时改判/核销），
		// 建单时就记成「已自动确认」，避免每条都要人点一遍。
		if d.MatchType == merge.MatchDeviceOnly {
			now := time.Now()
			w := string(merge.SideDevice)
			sys := "system"
			note := "仅采集端有此记录，系统默认保留"
			it.WinnerSide = &w
			it.ResolvedBy = &sys
			it.ResolutionNote = &note
			it.ResolvedAt = &now
		}
		items = append(items, it)
	}

	runID, err := s.mergeRepo.CreateRun(run, items)
	if err != nil {
		return nil, err
	}
	return s.GetRun(runID)
}

// GetRun 工作单详情：计数、恒等式核对结果、逐条明细。
func (s *MergeService) GetRun(runID int64) (*model.MergeRunDetail, error) {
	run, err := s.mergeRepo.GetRun(runID)
	if err != nil {
		return nil, err
	}
	items, err := s.mergeRepo.ListItems(runID)
	if err != nil {
		return nil, err
	}
	detail := &model.MergeRunDetail{Run: run, Items: items}

	c := merge.Counts{
		DeviceIn: run.DeviceIn, LedgerIn: run.LedgerIn,
		MatchedPairs: run.MatchedPairs, DeviceOnly: run.DeviceOnly, LedgerOnly: run.LedgerOnly,
		Consistent: run.Consistent, Conflicts: run.Conflicts,
		Pending: run.Unresolved, Skipped: run.Skipped,
	}
	detail.Counts = c
	detail.Check = checkIdentities(run)
	return detail, nil
}

func (s *MergeService) ListRuns(limit int) ([]model.MergeRun, error) {
	return s.mergeRepo.ListRuns(limit)
}

// Resolve 记录一次人工取舍。台账侧补入必须指明目标批次，批次要真实存在。
func (s *MergeService) Resolve(runID, itemID int64, req *model.ResolveItemRequest) (*model.MergeItem, error) {
	if req.Winner != "device" && req.Winner != "ledger" {
		return nil, errors.New("winner 只能是 device 或 ledger")
	}
	if req.TargetBatchID != nil && *req.TargetBatchID != 0 {
		if _, err := s.batchRepo.GetByID(*req.TargetBatchID); err != nil {
			return nil, fmt.Errorf("target_batch_id 不存在: %w", err)
		}
	}
	return s.mergeRepo.ResolveItem(runID, itemID, req.Winner, req.TargetBatchID,
		req.NewInputID, req.Note, req.ResolvedBy)
}

func (s *MergeService) Skip(runID, itemID int64, req *model.SkipItemRequest) error {
	return s.mergeRepo.SkipItem(runID, itemID, req.Reason, req.ResolvedBy)
}

func (s *MergeService) Unresolve(runID, itemID int64, actor string) error {
	return s.mergeRepo.Unresolve(runID, itemID, actor)
}

// Apply 所有事件处理完后，把取舍落成「一份」activity。动作由当前明细与
// 裁决结果逐条推导，整个落地在一个事务里，提交前复核对账恒等式。
func (s *MergeService) Apply(runID int64, req *model.ApplyMergeRequest) (int, *model.MergeCheck, error) {
	items, err := s.mergeRepo.ListItems(runID)
	if err != nil {
		return 0, nil, err
	}
	if len(items) == 0 {
		return 0, nil, errors.New("合并工作单不存在或没有明细")
	}

	actions := make([]repository.ApplyAction, 0, len(items))
	for _, it := range items {
		if it.Status == "skipped" {
			actions = append(actions, repository.ApplyAction{ItemID: it.ID, Kind: "skip"})
			continue
		}
		winner := ""
		if it.WinnerSide != nil {
			winner = *it.WinnerSide
		}
		// 一致项建单时按「留早」预选了胜方；没有胜方的未决项一律拦住。
		if it.ResolvedAt == nil && it.Status != "consistent" {
			return 0, nil, fmt.Errorf("第 %d 条明细尚未人工处理（match=%s）", it.ID, it.MatchType)
		}

		switch it.MatchType {
		case "matched":
			if winner == "" {
				return 0, nil, fmt.Errorf("第 %d 条配对缺少取舍", it.ID)
			}
			if winner == "device" {
				actions = append(actions, repository.ApplyAction{
					ItemID: it.ID, Kind: "keep",
					ActivityID: derefInt64(it.ActivityID), LedgerID: derefInt64(it.LedgerID),
					InputID: it.NewInputID,
				})
			} else {
				l := it.Ledger
				if l == nil {
					return 0, nil, fmt.Errorf("第 %d 条缺少台账数据", it.ID)
				}
				inputID := l.InputID
				if it.NewInputID != nil {
					inputID = it.NewInputID
				}
				actions = append(actions, repository.ApplyAction{
					ItemID: it.ID, Kind: "overwrite",
					ActivityID: derefInt64(it.ActivityID), LedgerID: l.ID,
					InputID: inputID, Dose: l.Dose, DoseUnit: l.DoseUnit,
					EvidenceNote: ledgerEvidenceNote(l),
				})
			}
		case "device_only":
			actions = append(actions, repository.ApplyAction{
				ItemID: it.ID, Kind: "keep", ActivityID: derefInt64(it.ActivityID),
				InputID: it.NewInputID,
			})
		case "ledger_only":
			l := it.Ledger
			if l == nil || l.MergedBatchID == nil || *l.MergedBatchID == 0 {
				return 0, nil, fmt.Errorf("第 %d 条台账记录尚未指定目标批次", it.ID)
			}
			inputID := l.InputID
			if it.NewInputID != nil {
				inputID = it.NewInputID
			}
			op := l.OperatorNorm
			if l.OperatorRaw != nil && *l.OperatorRaw != "" {
				op = *l.OperatorRaw
			}
			actions = append(actions, repository.ApplyAction{
				ItemID: it.ID, Kind: "insert_ledger", LedgerID: l.ID,
				TargetBatchID: *l.MergedBatchID,
				ClientUUID:    fmt.Sprintf("ledger-%d-%d", runID, l.ID),
				Operator:      op, InputID: inputID, Dose: l.Dose, DoseUnit: l.DoseUnit,
				HappenedAt:   l.HappenedOn.Add(12 * time.Hour), // 台账只记到日，落正午
				EvidenceNote: ledgerEvidenceNote(l),
			})
		default:
			return 0, nil, fmt.Errorf("未知明细类型: %s", it.MatchType)
		}
	}

	output, err := s.mergeRepo.Apply(runID, actions, req.Actor)
	if err != nil {
		return 0, nil, err
	}
	run, err := s.mergeRepo.GetRun(runID)
	if err != nil {
		return output, nil, nil
	}
	check := checkIdentities(run)
	return output, &check, nil
}

func (s *MergeService) Cancel(runID int64, actor string) error {
	return s.mergeRepo.Cancel(runID, actor)
}

func (s *MergeService) AuditTrail(runID int64) ([]model.MergeAuditLog, error) {
	return s.mergeRepo.AuditTrail(runID)
}

func (s *MergeService) inputNameMap() (map[int64]string, error) {
	ms, err := s.inputRepo.List()
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(ms))
	for _, m := range ms {
		out[m.ID] = m.Name
	}
	return out, nil
}

// checkIdentities 由工作单计数生成「总数对不对」的核对结论。
func checkIdentities(run *model.MergeRun) model.MergeCheck {
	ch := model.MergeCheck{
		DeviceIdentity: fmt.Sprintf("%d = %d + %d", run.DeviceIn, run.MatchedPairs, run.DeviceOnly),
		LedgerIdentity: fmt.Sprintf("%d = %d + %d", run.LedgerIn, run.MatchedPairs, run.LedgerOnly),
		EventIdentity: fmt.Sprintf("%d = %d + %d",
			run.MatchedPairs+run.DeviceOnly+run.LedgerOnly, run.Resolved, run.Unresolved),
	}
	events := run.MatchedPairs + run.DeviceOnly + run.LedgerOnly
	ok := true
	if run.DeviceIn != run.MatchedPairs+run.DeviceOnly {
		ch.Problems = append(ch.Problems,
			fmt.Sprintf("采集端总数对不上：%s（差 %d）", ch.DeviceIdentity, run.DeviceIn-run.MatchedPairs-run.DeviceOnly))
		ok = false
	}
	if run.LedgerIn != run.MatchedPairs+run.LedgerOnly {
		ch.Problems = append(ch.Problems,
			fmt.Sprintf("台账总数对不上：%s（差 %d）", ch.LedgerIdentity, run.LedgerIn-run.MatchedPairs-run.LedgerOnly))
		ok = false
	}
	if events != run.Resolved+run.Unresolved {
		ch.Problems = append(ch.Problems, "事件总数与已决/未决对不上："+ch.EventIdentity)
		ok = false
	}
	if run.Status == model.MergeApplied {
		expect := run.Resolved - run.Skipped
		if run.OutputCount != expect {
			ch.Problems = append(ch.Problems,
				fmt.Sprintf("落地后「一份」条数 %d ≠ 已决 %d - 核销 %d，逐行核对 merge_item.applied_activity_id",
					run.OutputCount, run.Resolved, run.Skipped))
			ok = false
		}
	}
	ch.Balanced = ok
	return ch
}

func putIdentity(it *model.MergeItem, idn merge.Identity) {
	if idn.PlotID != 0 {
		p := idn.PlotID
		it.PlotID = &p
	}
	if idn.PlotName != "" {
		n := idn.PlotName
		it.PlotName = &n
	}
	if idn.CropID != "" {
		c := idn.CropID
		it.CropID = &c
	}
	if idn.Day != "" {
		t, _ := time.ParseInLocation("2006-01-02", idn.Day, time.Local)
		it.HappenedOn = &t
	}
	if idn.Operator != "" {
		o := idn.Operator
		it.OperatorNorm = &o
	}
}

func ledgerEvidenceNote(l *model.LedgerRecord) string {
	if l.EvidenceRef != nil && *l.EvidenceRef != "" {
		return "台账凭据: " + *l.EvidenceRef
	}
	return ""
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
