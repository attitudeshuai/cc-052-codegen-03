package service

import (
	"testing"
	"time"

	"cc-052/internal/model"
)

func rec(id int64, plotID int64, crop, date, operator string, createdAt time.Time) model.FieldRecord {
	t, _ := time.Parse("2006-01-02", date)
	return model.FieldRecord{
		ID:         id,
		Source:     model.SourceCollector,
		PlotID:     plotID,
		CropID:     crop,
		Kind:       model.ActivityFertilize,
		HappenedAt: t,
		Operator:   operator,
		CreatedAt:  createdAt,
	}
}

func withPhotos(r model.FieldRecord) model.FieldRecord {
	r.Photos = model.StringMap{"0": "photo/a.jpg"}
	return r
}

func withDose(r model.FieldRecord, dose float64, unit string) model.FieldRecord {
	r.Dose = &dose
	r.DoseUnit = &unit
	return r
}

var base = time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)

func TestGroupRecordsSameEvent(t *testing.T) {
	// 同一地块/作物/日期/操作人：采集端与台账各一条，应认成同一件事
	a := rec(1, 10, "rice", "2026-09-01", "张三", base)
	b := rec(2, 10, "rice", "2026-09-01", "张三", base.Add(time.Hour))
	// 不同操作人 / 不同日期 / 不同地块 / 不同作物：各成一组
	c := rec(3, 10, "rice", "2026-09-01", "李四", base)
	d := rec(4, 10, "rice", "2026-09-02", "张三", base)
	e := rec(5, 11, "rice", "2026-09-01", "张三", base)
	f := rec(6, 10, "wheat", "2026-09-01", "张三", base)

	groups := groupRecords([]model.FieldRecord{a, b, c, d, e, f})
	if len(groups) != 5 {
		t.Fatalf("expected 5 groups, got %d", len(groups))
	}
	// 找到含两条记录的组，确认是最早的在前
	found := false
	for _, g := range groups {
		if len(g) == 2 {
			found = true
			if g[0].ID != 1 || g[1].ID != 2 {
				t.Fatalf("group not ordered by created_at: %v, %v", g[0].ID, g[1].ID)
			}
		}
	}
	if !found {
		t.Fatal("expected a group of 2 records")
	}
}

func TestGroupRecordsSameDayDifferentTime(t *testing.T) {
	// 同一天不同时刻仍是一件事
	morning, _ := time.Parse(time.RFC3339, "2026-09-01T06:00:00Z")
	evening, _ := time.Parse(time.RFC3339, "2026-09-01T22:30:00Z")
	a := rec(1, 10, "rice", "2026-09-01", "张三", base)
	a.HappenedAt = morning
	b := rec(2, 10, "rice", "2026-09-01", "张三", base)
	b.HappenedAt = evening

	groups := groupRecords([]model.FieldRecord{a, b})
	if len(groups) != 1 {
		t.Fatalf("same-day records should group together, got %d groups", len(groups))
	}
}

func TestDecideGroupConsistentKeepsEarliest(t *testing.T) {
	a := rec(1, 10, "rice", "2026-09-01", "张三", base)
	b := rec(2, 10, "rice", "2026-09-01", "张三", base.Add(time.Hour))

	status, rule, kept := decideGroup([]model.FieldRecord{a, b})
	if status != model.GroupStatusAuto || rule != model.RuleEarliest {
		t.Fatalf("expected auto/earliest, got %s/%s", status, rule)
	}
	if kept.ID != 1 {
		t.Fatalf("expected earliest record 1 kept, got %d", kept.ID)
	}
}

func TestDecideGroupEvidenceWins(t *testing.T) {
	// 两边打架：台账剂量 50，采集端剂量 20 且有照片凭据 -> 以有凭据的为准
	a := withDose(rec(1, 10, "rice", "2026-09-01", "张三", base), 50, "kg")
	a.Source = model.SourceLedger
	b := withPhotos(withDose(rec(2, 10, "rice", "2026-09-01", "张三", base.Add(time.Hour)), 20, "kg"))

	status, rule, kept := decideGroup([]model.FieldRecord{a, b})
	if status != model.GroupStatusEvidence || rule != model.RuleEvidence {
		t.Fatalf("expected evidence/evidence, got %s/%s", status, rule)
	}
	if kept.ID != 2 {
		t.Fatalf("expected evidenced record 2 kept, got %d", kept.ID)
	}
}

func TestDecideGroupPendingWhenNoEvidence(t *testing.T) {
	// 两边都没凭据又打架 -> 挂起人工，暂留最早
	a := withDose(rec(1, 10, "rice", "2026-09-01", "张三", base), 50, "kg")
	b := withDose(rec(2, 10, "rice", "2026-09-01", "张三", base.Add(time.Hour)), 20, "kg")

	status, _, kept := decideGroup([]model.FieldRecord{a, b})
	if status != model.GroupStatusPending {
		t.Fatalf("expected pending, got %s", status)
	}
	if kept.ID != 1 {
		t.Fatalf("pending should provisionally keep earliest, got %d", kept.ID)
	}
}

func TestDecideGroupPendingWhenBothHaveEvidence(t *testing.T) {
	// 两边都有凭据还打架 -> 也只能人工挑
	a := withPhotos(withDose(rec(1, 10, "rice", "2026-09-01", "张三", base), 50, "kg"))
	b := withPhotos(withDose(rec(2, 10, "rice", "2026-09-01", "张三", base.Add(time.Hour)), 20, "kg"))

	status, _, _ := decideGroup([]model.FieldRecord{a, b})
	if status != model.GroupStatusPending {
		t.Fatalf("expected pending, got %s", status)
	}
}

func TestDiffGroupListsOnlyInconsistentFields(t *testing.T) {
	a := withDose(rec(1, 10, "rice", "2026-09-01", "张三", base), 50, "kg")
	b := withDose(rec(2, 10, "rice", "2026-09-01", "张三", base.Add(time.Hour)), 20, "kg")

	diffs := diffGroup([]model.FieldRecord{a, b})
	fields := map[string]bool{}
	for _, d := range diffs {
		fields[d.Field] = true
		if len(d.Values) != 2 {
			t.Fatalf("diff %s should carry both records' values", d.Field)
		}
	}
	if !fields["dose"] {
		t.Fatal("expected dose diff")
	}
	if fields["operator"] || fields["kind"] {
		t.Fatal("consistent fields should not appear in diff")
	}
}

func TestBuildMergedRecordWithFieldPicks(t *testing.T) {
	// 保留最早的台账记录，但剂量挑采集端的
	a := withDose(rec(1, 10, "rice", "2026-09-01", "张三", base), 50, "kg")
	b := withDose(rec(2, 10, "rice", "2026-09-01", "张三", base.Add(time.Hour)), 20, "kg")

	m, err := buildMergedRecord([]model.FieldRecord{a, b}, 7, 100, 1, map[string]int64{"dose": 2})
	if err != nil {
		t.Fatal(err)
	}
	if m.Dose == nil || *m.Dose != 20 {
		t.Fatalf("expected dose picked from record 2, got %v", m.Dose)
	}
	if len(m.SourceRecordIDs) != 2 || m.SourceRecordIDs[0] != 1 || m.SourceRecordIDs[1] != 2 {
		t.Fatalf("source record ids wrong: %v", m.SourceRecordIDs)
	}
}

func TestBuildMergedRecordRejectsBadPicks(t *testing.T) {
	a := rec(1, 10, "rice", "2026-09-01", "张三", base)
	b := rec(2, 10, "rice", "2026-09-01", "张三", base.Add(time.Hour))

	// 保留记录不在组内
	if _, err := buildMergedRecord([]model.FieldRecord{a, b}, 7, 100, 99, nil); err == nil {
		t.Fatal("expected error for kept record outside group")
	}
	// 非法字段
	if _, err := buildMergedRecord([]model.FieldRecord{a, b}, 7, 100, 1, map[string]int64{"operator": 2}); err == nil {
		t.Fatal("expected error for invalid pick field")
	}
	// 取值来源不在组内
	if _, err := buildMergedRecord([]model.FieldRecord{a, b}, 7, 100, 1, map[string]int64{"dose": 99}); err == nil {
		t.Fatal("expected error for pick source outside group")
	}
}

func TestReconcileArithmetic(t *testing.T) {
	// 合并前后总数恒等式：total_in == kept + superseded
	records := []model.FieldRecord{
		rec(1, 10, "rice", "2026-09-01", "张三", base),
		rec(2, 10, "rice", "2026-09-01", "张三", base.Add(time.Hour)),
		rec(3, 10, "rice", "2026-09-01", "张三", base.Add(2*time.Hour)),
		rec(4, 10, "rice", "2026-09-01", "李四", base),
	}
	groups := groupRecords(records)
	kept, superseded := 0, 0
	for _, g := range groups {
		kept++
		superseded += len(g) - 1
	}
	if kept+superseded != len(records) {
		t.Fatalf("reconcile broken: %d+%d != %d", kept, superseded, len(records))
	}
	if kept != 2 {
		t.Fatalf("expected 2 groups, got %d", kept)
	}
}
