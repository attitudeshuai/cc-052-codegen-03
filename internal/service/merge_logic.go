package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"cc-052/internal/model"
)

// mergeComparedFields 参与一致性比较的字段（地块/作物/日期/操作人是分组键，必然一致）
var mergeComparedFields = []string{
	"kind", "happened_at", "input_id", "dose", "dose_unit", "photos", "geo", "note",
}

// groupKey “同一件事”的识别键：地块 + 作物 + 日期 + 操作人
type groupKey struct {
	plotID   int64
	cropID   string
	date     string // happened_at 的 UTC 日期部分
	operator string
}

func keyOfRecord(r *model.FieldRecord) groupKey {
	return groupKey{
		plotID:   r.PlotID,
		cropID:   r.CropID,
		date:     r.HappenedAt.UTC().Format("2006-01-02"),
		operator: r.Operator,
	}
}

// groupRecords 按识别键分组；组内按提交先后（created_at, id）升序，最早的在前。
// 返回的组按组内最早记录排序，保证处理顺序确定。
func groupRecords(records []model.FieldRecord) [][]model.FieldRecord {
	buckets := map[groupKey][]model.FieldRecord{}
	for i := range records {
		k := keyOfRecord(&records[i])
		buckets[k] = append(buckets[k], records[i])
	}
	groups := make([][]model.FieldRecord, 0, len(buckets))
	for _, g := range buckets {
		sort.Slice(g, func(i, j int) bool {
			if !g[i].CreatedAt.Equal(g[j].CreatedAt) {
				return g[i].CreatedAt.Before(g[j].CreatedAt)
			}
			return g[i].ID < g[j].ID
		})
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if !groups[i][0].CreatedAt.Equal(groups[j][0].CreatedAt) {
			return groups[i][0].CreatedAt.Before(groups[j][0].CreatedAt)
		}
		return groups[i][0].ID < groups[j][0].ID
	})
	return groups
}

// hasEvidence 是否有凭据（照片非空即视为有凭据）
func hasEvidence(r *model.FieldRecord) bool {
	return len(r.Photos) > 0
}

// fieldValue 取记录某字段的规范化值（用于比较与展示）
func fieldValue(r *model.FieldRecord, field string) interface{} {
	switch field {
	case "kind":
		return string(r.Kind)
	case "happened_at":
		return r.HappenedAt.UTC().Format(time.RFC3339)
	case "input_id":
		if r.InputID == nil {
			return nil
		}
		return *r.InputID
	case "dose":
		if r.Dose == nil {
			return nil
		}
		return *r.Dose
	case "dose_unit":
		if r.DoseUnit == nil {
			return nil
		}
		return *r.DoseUnit
	case "photos":
		if len(r.Photos) == 0 {
			return nil
		}
		return r.Photos
	case "geo":
		if r.Geo == nil {
			return nil
		}
		return *r.Geo
	case "note":
		if r.Note == nil {
			return nil
		}
		return *r.Note
	}
	return nil
}

// canonical 值的规范 JSON 表示，用于判等
func canonical(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// diffGroup 找出组内各记录间的字段级不一致
func diffGroup(group []model.FieldRecord) model.FieldDiffList {
	var diffs model.FieldDiffList
	for _, f := range mergeComparedFields {
		first := canonical(fieldValue(&group[0], f))
		same := true
		for i := 1; i < len(group); i++ {
			if canonical(fieldValue(&group[i], f)) != first {
				same = false
				break
			}
		}
		if same {
			continue
		}
		d := model.FieldDiff{Field: f}
		for i := range group {
			d.Values = append(d.Values, model.DiffValue{
				RecordID: group[i].ID,
				Value:    fieldValue(&group[i], f),
			})
		}
		diffs = append(diffs, d)
	}
	return diffs
}

// decideGroup 决定一组记录的处理方式与保留记录：
//   - 无不一致      -> auto，保留最早一条
//   - 有不一致且恰有一条有凭据 -> evidence，以有凭据的为准
//   - 其余          -> pending，暂保留最早一条，待人工挑拣
func decideGroup(group []model.FieldRecord) (status string, rule string, kept *model.FieldRecord) {
	diffs := diffGroup(group)
	earliest := &group[0] // 组内已按提交先后排序
	if len(diffs) == 0 {
		return model.GroupStatusAuto, model.RuleEarliest, earliest
	}
	evidenced := []*model.FieldRecord{}
	for i := range group {
		if hasEvidence(&group[i]) {
			evidenced = append(evidenced, &group[i])
		}
	}
	if len(evidenced) == 1 {
		return model.GroupStatusEvidence, model.RuleEvidence, evidenced[0]
	}
	return model.GroupStatusPending, model.RuleEarliest, earliest
}

// applyGroup 对一组记录做出完整结论：状态、规则、保留记录、各成员差异
func applyGroup(group []model.FieldRecord) (status, rule string, keptID int64, diffs model.FieldDiffList) {
	status, rule, kept := decideGroup(group)
	return status, rule, kept.ID, diffGroup(group)
}

// validPickFields 允许人工挑选的字段
var validPickFields = map[string]bool{
	"kind": true, "happened_at": true, "input_id": true, "dose": true,
	"dose_unit": true, "photos": true, "geo": true, "note": true,
}

// validatePicks 校验人工挑选：字段合法、取值来源必须是组内记录
func validatePicks(group []model.FieldRecord, keptID int64, picks map[string]int64) error {
	inGroup := map[int64]bool{}
	for i := range group {
		inGroup[group[i].ID] = true
	}
	if !inGroup[keptID] {
		return fmt.Errorf("kept_record_id %d 不在本组记录中", keptID)
	}
	for field, srcID := range picks {
		if !validPickFields[field] {
			return fmt.Errorf("字段 %q 不允许挑选", field)
		}
		if !inGroup[srcID] {
			return fmt.Errorf("字段 %q 的取值来源记录 %d 不在本组", field, srcID)
		}
	}
	return nil
}

// buildMergedRecord 以保留记录为底，按人工挑选逐字段覆盖，生成合并产出
func buildMergedRecord(group []model.FieldRecord, runID, groupID, keptID int64, picks map[string]int64) (*model.MergedRecord, error) {
	if err := validatePicks(group, keptID, picks); err != nil {
		return nil, err
	}
	byID := map[int64]*model.FieldRecord{}
	ids := make([]int64, 0, len(group))
	for i := range group {
		byID[group[i].ID] = &group[i]
		ids = append(ids, group[i].ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	base := byID[keptID]
	m := &model.MergedRecord{
		GroupID:         groupID,
		RunID:           runID,
		PlotID:          base.PlotID,
		CropID:          base.CropID,
		Kind:            base.Kind,
		HappenedAt:      base.HappenedAt,
		InputID:         base.InputID,
		Dose:            base.Dose,
		DoseUnit:        base.DoseUnit,
		Operator:        base.Operator,
		Photos:          base.Photos,
		Geo:             base.Geo,
		Note:            base.Note,
		SourceRecordIDs: model.Int64List(ids),
	}
	// 应用字段级挑选
	for field, srcID := range picks {
		src := byID[srcID]
		switch field {
		case "kind":
			m.Kind = src.Kind
		case "happened_at":
			m.HappenedAt = src.HappenedAt
		case "input_id":
			m.InputID = src.InputID
		case "dose":
			m.Dose = src.Dose
		case "dose_unit":
			m.DoseUnit = src.DoseUnit
		case "photos":
			m.Photos = src.Photos
		case "geo":
			m.Geo = src.Geo
		case "note":
			m.Note = src.Note
		}
	}
	return m, nil
}
