package service

import (
	"fmt"
	"time"

	"cc-052/internal/model"
	"cc-052/internal/repository"
)

type MergeService struct {
	recordRepo *repository.FieldRecordRepo
	mergeRepo  *repository.MergeRepo
	plotRepo   *repository.PlotRepo
}

func NewMergeService(recordRepo *repository.FieldRecordRepo, mergeRepo *repository.MergeRepo, plotRepo *repository.PlotRepo) *MergeService {
	return &MergeService{recordRepo: recordRepo, mergeRepo: mergeRepo, plotRepo: plotRepo}
}

// MergeGroupDetail 分组详情：分组、组内原始记录、成员去向、当前合并产出
type MergeGroupDetail struct {
	Group   *model.MergeGroup        `json:"group"`
	Records []model.FieldRecord      `json:"records"`
	Members []model.MergeGroupMember `json:"members"`
	Merged  *model.MergedRecord      `json:"merged,omitempty"`
}

func validKind(k model.ActivityKind) bool {
	switch k {
	case model.ActivityFertilize, model.ActivityPesticide, model.ActivityIrrigation, model.ActivityWeed:
		return true
	}
	return false
}

func parseHappenedAt(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}

// SubmitRecords 接收采集端/手工台账的原始记录；每条都有交代：入库、重复或被拒（含原因）
func (s *MergeService) SubmitRecords(reqs []model.SubmitRecordRequest) (*model.SubmitRecordsResult, error) {
	result := &model.SubmitRecordsResult{Total: len(reqs), Rejected: []model.RejectedRecord{}}

	plotCache := map[int64]bool{}
	var valid []model.FieldRecord
	for i, req := range reqs {
		rec, reason := s.toRecord(req, plotCache)
		if reason != "" {
			result.Rejected = append(result.Rejected, model.RejectedRecord{Index: i, Reason: reason})
			continue
		}
		valid = append(valid, *rec)
	}

	if len(valid) > 0 {
		inserted, duplicates, err := s.recordRepo.BatchCreate(valid)
		if err != nil {
			return nil, err
		}
		result.Inserted = inserted
		result.Duplicates = duplicates
	}
	return result, nil
}

func (s *MergeService) toRecord(req model.SubmitRecordRequest, plotCache map[int64]bool) (*model.FieldRecord, string) {
	if req.Source != model.SourceCollector && req.Source != model.SourceLedger {
		return nil, "source 必须是 collector 或 ledger"
	}
	if req.Source == model.SourceCollector && req.ClientUUID == "" {
		return nil, "采集端记录必须带 client_uuid（幂等键）"
	}
	if !validKind(req.Kind) {
		return nil, fmt.Sprintf("未知农事类型 %q", req.Kind)
	}
	happenedAt, err := parseHappenedAt(req.HappenedAt)
	if err != nil {
		return nil, fmt.Sprintf("happened_at 无法解析: %v", err)
	}
	exists, checked := plotCache[req.PlotID]
	if !checked {
		_, err := s.plotRepo.GetByID(req.PlotID)
		exists = err == nil
		plotCache[req.PlotID] = exists
	}
	if !exists {
		return nil, fmt.Sprintf("地块 %d 不存在", req.PlotID)
	}

	rec := &model.FieldRecord{
		Source:     req.Source,
		PlotID:     req.PlotID,
		CropID:     req.CropID,
		Kind:       req.Kind,
		HappenedAt: happenedAt,
		InputID:    req.InputID,
		Dose:       req.Dose,
		DoseUnit:   req.DoseUnit,
		Operator:   req.Operator,
		Photos:     req.Photos,
		Geo:        req.Geo,
		Note:       req.Note,
	}
	if req.ClientUUID != "" {
		rec.ClientUUID = &req.ClientUUID
	}
	if req.UploadBatch != "" {
		rec.UploadBatch = &req.UploadBatch
	}
	return rec, ""
}

// RunMerge 把所有未合并的原始记录并成一份：
// 分组认出同一件事 -> 保留早的/凭据优先 -> 产出合并记录 -> 记数供对账
func (s *MergeService) RunMerge() (*model.MergeRun, error) {
	records, err := s.recordRepo.ListUnmerged()
	if err != nil {
		return nil, err
	}

	groups := groupRecords(records)
	run := &model.MergeRun{
		TotalIn:      len(records),
		GroupsFormed: len(groups),
	}

	bundles := make([]repository.GroupBundle, 0, len(groups))
	recordIDs := make([]int64, 0, len(records))

	for _, group := range groups {
		status, rule, keptID, diffs := applyGroup(group)

		g := &model.MergeGroup{
			PlotID:       group[0].PlotID,
			CropID:       group[0].CropID,
			ActivityDate: group[0].HappenedAt.UTC().Truncate(24 * time.Hour),
			Operator:     group[0].Operator,
			Status:       status,
			KeptRecordID: &keptID,
		}

		members := make([]model.MergeGroupMember, 0, len(group))
		for i := range group {
			recordIDs = append(recordIDs, group[i].ID)
			members = append(members, model.MergeGroupMember{
				RecordID: group[i].ID,
				IsKept:   group[i].ID == keptID,
				Diffs:    diffs,
			})
		}

		merged, err := buildMergedRecord(group, 0, 0, keptID, nil)
		if err != nil {
			return nil, err
		}

		note := "无字段不一致，保留最早一条"
		if status == model.GroupStatusEvidence {
			note = "存在不一致，以有凭据的记录为准"
		} else if status == model.GroupStatusPending {
			note = "存在不一致且无法凭凭据判定，暂保留最早一条，待人工挑拣"
		}
		decision := &model.MergeDecision{
			Rule:         rule,
			DecidedBy:    "system",
			KeptRecordID: keptID,
			Note:         &note,
		}

		bundles = append(bundles, repository.GroupBundle{
			Group:    g,
			Members:  members,
			Merged:   merged,
			Decision: decision,
		})

		run.Kept++
		run.Superseded += len(group) - 1
		switch status {
		case model.GroupStatusAuto:
			run.ConsistentGroups++
		case model.GroupStatusEvidence:
			run.EvidenceResolved++
		case model.GroupStatusPending:
			run.PendingConflicts++
		}
	}

	if err := s.mergeRepo.SaveRun(run, bundles, recordIDs); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *MergeService) ListRuns() ([]model.MergeRun, error) {
	return s.mergeRepo.ListRuns()
}

func (s *MergeService) GetRun(id int64) (*model.MergeRun, error) {
	return s.mergeRepo.GetRun(id)
}

// ListConflicts 待人工挑拣的冲突组（含各记录取值与字段级差异）
func (s *MergeService) ListConflicts(runID int64) ([]MergeGroupDetail, error) {
	groups, err := s.mergeRepo.ListGroups(runID, model.GroupStatusPending)
	if err != nil {
		return nil, err
	}
	details := make([]MergeGroupDetail, 0, len(groups))
	for i := range groups {
		detail, err := s.GetGroupDetail(groups[i].ID)
		if err != nil {
			return nil, err
		}
		details = append(details, *detail)
	}
	return details, nil
}

func (s *MergeService) GetGroupDetail(groupID int64) (*MergeGroupDetail, error) {
	group, err := s.mergeRepo.GetGroup(groupID)
	if err != nil {
		return nil, err
	}
	records, err := s.mergeRepo.RecordsByGroup(groupID)
	if err != nil {
		return nil, err
	}
	members, err := s.mergeRepo.MembersByGroup(groupID)
	if err != nil {
		return nil, err
	}
	merged, err := s.mergeRepo.GetMergedByGroup(groupID)
	if err != nil {
		merged = nil // 产出缺失不阻断详情查看（对账会报告）
	}
	return &MergeGroupDetail{Group: group, Records: records, Members: members, Merged: merged}, nil
}

// ResolveGroup 人工挑拣：选定保留记录、逐字段取值来源，留档备查
func (s *MergeService) ResolveGroup(groupID int64, req *model.ResolveGroupRequest) (*model.MergeDecision, error) {
	group, err := s.mergeRepo.GetGroup(groupID)
	if err != nil {
		return nil, err
	}
	records, err := s.mergeRepo.RecordsByGroup(groupID)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("分组 %d 下没有记录", groupID)
	}

	keptID := int64(0)
	if req.KeptRecordID != nil {
		keptID = *req.KeptRecordID
	} else if group.KeptRecordID != nil {
		keptID = *group.KeptRecordID
	} else {
		return nil, fmt.Errorf("未指定保留记录")
	}

	merged, err := buildMergedRecord(records, group.RunID, groupID, keptID, req.FieldPicks)
	if err != nil {
		return nil, err
	}

	decision := &model.MergeDecision{
		GroupID:      groupID,
		RunID:        group.RunID,
		Rule:         model.RuleManual,
		DecidedBy:    req.DecidedBy,
		KeptRecordID: keptID,
		FieldPicks:   model.FieldPickMap(req.FieldPicks),
	}
	if req.Note != "" {
		decision.Note = &req.Note
	}

	if err := s.mergeRepo.ResolveGroup(groupID, keptID, merged, decision); err != nil {
		return nil, err
	}
	return decision, nil
}

func (s *MergeService) ListDecisions(groupID int64) ([]model.MergeDecision, error) {
	return s.mergeRepo.ListDecisionsByGroup(groupID)
}

func (s *MergeService) ListMerged(runID int64) ([]model.MergedRecord, error) {
	return s.mergeRepo.ListMerged(runID)
}

func (s *MergeService) ListRecords(unmergedOnly bool) ([]model.FieldRecord, error) {
	if unmergedOnly {
		return s.recordRepo.ListUnmerged()
	}
	return s.recordRepo.ListAll()
}

// Reconcile 对账：合并前后总数必须对得上；对不上时列出具体是哪条记录、哪个组出了问题
func (s *MergeService) Reconcile(runID int64) (*model.ReconcileResult, error) {
	run, err := s.mergeRepo.GetRun(runID)
	if err != nil {
		return nil, err
	}
	records, err := s.recordRepo.ListByRun(runID)
	if err != nil {
		return nil, err
	}
	members, err := s.mergeRepo.MembersByRun(runID)
	if err != nil {
		return nil, err
	}
	groups, err := s.mergeRepo.ListGroups(runID, "")
	if err != nil {
		return nil, err
	}
	mergedList, err := s.mergeRepo.ListMerged(runID)
	if err != nil {
		return nil, err
	}

	res := &model.ReconcileResult{
		RunID:               runID,
		OK:                  true,
		TotalIn:             run.TotalIn,
		Kept:                run.Kept,
		Superseded:          run.Superseded,
		MissingRecordIDs:    []int64{},
		UnexpectedRecordIDs: []int64{},
		GroupsWithoutKept:   []int64{},
		GroupsWithoutOutput: []int64{},
		Problems:            []string{},
	}

	memberRecordIDs := map[int64]bool{}
	keptByGroup := map[int64]int{}
	for _, m := range members {
		memberRecordIDs[m.RecordID] = true
		if m.IsKept {
			keptByGroup[m.GroupID]++
		}
	}
	res.Accounted = len(members)

	// 收编了但没进任何组的记录
	inRun := map[int64]bool{}
	for _, r := range records {
		inRun[r.ID] = true
		if !memberRecordIDs[r.ID] {
			res.MissingRecordIDs = append(res.MissingRecordIDs, r.ID)
		}
	}
	// 进了组但不属于本次运行的记录
	for _, m := range members {
		if !inRun[m.RecordID] {
			res.UnexpectedRecordIDs = append(res.UnexpectedRecordIDs, m.RecordID)
		}
	}
	// 每组恰有一条保留记录、且都有合并产出
	hasOutput := map[int64]bool{}
	for _, m := range mergedList {
		hasOutput[m.GroupID] = true
	}
	for _, g := range groups {
		if keptByGroup[g.ID] != 1 {
			res.GroupsWithoutKept = append(res.GroupsWithoutKept, g.ID)
		}
		if !hasOutput[g.ID] {
			res.GroupsWithoutOutput = append(res.GroupsWithoutOutput, g.ID)
		}
	}

	// 总数核对
	if len(records) != run.TotalIn {
		res.Problems = append(res.Problems,
			fmt.Sprintf("运行汇总 total_in=%d，实际收编记录 %d 条", run.TotalIn, len(records)))
	}
	if res.Accounted != run.TotalIn {
		res.Problems = append(res.Problems,
			fmt.Sprintf("运行汇总 total_in=%d，组成员共 %d 条", run.TotalIn, res.Accounted))
	}
	if run.Kept+run.Superseded != run.TotalIn {
		res.Problems = append(res.Problems,
			fmt.Sprintf("kept(%d)+superseded(%d) != total_in(%d)", run.Kept, run.Superseded, run.TotalIn))
	}
	if len(res.MissingRecordIDs) > 0 {
		res.Problems = append(res.Problems,
			fmt.Sprintf("%d 条记录被收编但未进任何分组: %v", len(res.MissingRecordIDs), res.MissingRecordIDs))
	}
	if len(res.UnexpectedRecordIDs) > 0 {
		res.Problems = append(res.Problems,
			fmt.Sprintf("%d 条记录进了分组但未标记为本次运行收编: %v", len(res.UnexpectedRecordIDs), res.UnexpectedRecordIDs))
	}
	if len(res.GroupsWithoutKept) > 0 {
		res.Problems = append(res.Problems,
			fmt.Sprintf("%d 个分组保留记录数不为 1: %v", len(res.GroupsWithoutKept), res.GroupsWithoutKept))
	}
	if len(res.GroupsWithoutOutput) > 0 {
		res.Problems = append(res.Problems,
			fmt.Sprintf("%d 个分组缺少合并产出: %v", len(res.GroupsWithoutOutput), res.GroupsWithoutOutput))
	}

	res.OK = len(res.Problems) == 0
	return res, nil
}
