package merge

import (
	"testing"
	"time"
)

func day(s string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02", s, time.Local)
	return t
}

func pfloat(x float64) *float64 { return &x }

func TestMatchSameIdentityAndConsistent(t *testing.T) {
	devices := []SideRecord{{
		Side: SideDevice, ID: 1, BatchID: 10, PlotID: 7, PlotName: "3号地",
		CropID: "rice", Kind: "fertilize", HappenedOn: day("2026-05-01"),
		HappenedAt: day("2026-05-01").Add(8 * time.Hour), CreatedAt: day("2026-05-01").Add(9 * time.Hour),
		Operator: "张三", InputID: 4, InputName: "尿素", Dose: pfloat(10), DoseUnit: "kg",
	}}
	ledgers := []SideRecord{{
		Side: SideLedger, ID: 100, PlotID: 7, PlotName: "3号地（东）",
		CropID: "RICE", Kind: "fertilize", HappenedOn: day("2026-05-01"),
		CreatedAt: day("2026-05-02"), Operator: "张 三", InputID: 4, InputName: "尿素",
		Dose: pfloat(10), DoseUnit: "KG",
	}}
	rep := Build(devices, ledgers)
	if err := rep.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.Counts.MatchedPairs != 1 || rep.Counts.Consistent != 1 {
		t.Fatalf("want 1 matched consistent, got %+v", rep.Counts)
	}
	// 留早：设备记录产生更早
	if it := rep.Items[0]; it.SuggestedWinner != SideDevice || it.EarliestSide != SideDevice {
		t.Fatalf("want device winner/earliest, got %s/%s", it.SuggestedWinner, it.EarliestSide)
	}
}

func TestConflictEvidenceWins(t *testing.T) {
	devices := []SideRecord{{
		Side: SideDevice, ID: 1, BatchID: 10, PlotID: 7, PlotName: "3号地",
		CropID: "rice", Kind: "pesticide", HappenedOn: day("2026-05-01"),
		CreatedAt: day("2026-05-01"), Operator: "张三",
		InputName: "吡虫啉", Dose: pfloat(10), DoseUnit: "kg",
	}}
	ledgers := []SideRecord{{
		Side: SideLedger, ID: 100, PlotID: 7, PlotName: "3号地",
		CropID: "rice", Kind: "pesticide", HappenedOn: day("2026-05-01"),
		CreatedAt: day("2026-05-03"), Operator: "张三",
		InputName: "吡虫啉", Dose: pfloat(12), DoseUnit: "kg",
		HasEvidence: true, EvidenceRef: "签字单#8",
	}}
	rep := Build(devices, ledgers)
	if err := rep.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.Counts.Conflicts != 1 {
		t.Fatalf("want 1 conflict, got %+v", rep.Counts)
	}
	it := rep.Items[0]
	if it.SuggestedWinner != SideLedger {
		t.Fatalf("evidence side (ledger) should win, got %s (%s)", it.SuggestedWinner, it.SuggestReason)
	}
	if len(it.Differences) != 1 || it.Differences[0].Field != "dose" {
		t.Fatalf("want single dose diff, got %+v", it.Differences)
	}
}

func TestConflictNoEvidenceKeepsEarlier(t *testing.T) {
	devices := []SideRecord{{
		Side: SideDevice, ID: 1, BatchID: 10, PlotID: 7, PlotName: "p",
		CropID: "c", Kind: "weed", HappenedOn: day("2026-05-01"),
		CreatedAt: day("2026-05-05"), Operator: "o", DoseUnit: "亩",
	}}
	ledgers := []SideRecord{{
		Side: SideLedger, ID: 100, PlotID: 7, PlotName: "p",
		CropID: "c", Kind: "weed", HappenedOn: day("2026-05-01"),
		CreatedAt: day("2026-05-02"), Operator: "o", DoseUnit: "斤",
	}}
	rep := Build(devices, ledgers)
	it := rep.Items[0]
	if it.Status != StatusConflict || it.SuggestedWinner != SideLedger {
		t.Fatalf("want conflict with earlier ledger as winner, got %s %s", it.Status, it.SuggestedWinner)
	}
}

func TestBothEvidenceForcesManualChoice(t *testing.T) {
	devices := []SideRecord{{
		Side: SideDevice, ID: 1, PlotID: 1, PlotName: "p", CropID: "c", Kind: "irrigation",
		HappenedOn: day("2026-05-01"), CreatedAt: day("2026-05-01"),
		Operator: "o", DoseUnit: "桶", HasEvidence: true,
	}}
	ledgers := []SideRecord{{
		Side: SideLedger, ID: 2, PlotID: 1, PlotName: "p", CropID: "c", Kind: "irrigation",
		HappenedOn: day("2026-05-01"), CreatedAt: day("2026-05-02"),
		Operator: "o", DoseUnit: "方", HasEvidence: true,
	}}
	rep := Build(devices, ledgers)
	if it := rep.Items[0]; it.SuggestedWinner != "" {
		t.Fatalf("both having evidence must force manual choice, got %s", it.SuggestedWinner)
	}
}

func TestSinglesAndCountsIdentity(t *testing.T) {
	devices := []SideRecord{
		{Side: SideDevice, ID: 1, PlotID: 1, PlotName: "a", CropID: "c", Kind: "weed",
			HappenedOn: day("2026-05-01"), Operator: "o"},
		{Side: SideDevice, ID: 2, PlotID: 2, PlotName: "b", CropID: "c", Kind: "weed",
			HappenedOn: day("2026-05-02"), Operator: "o"},
	}
	ledgers := []SideRecord{
		{Side: SideLedger, ID: 10, PlotID: 1, PlotName: "a", CropID: "c", Kind: "weed",
			HappenedOn: day("2026-05-01"), Operator: "o"},
		{Side: SideLedger, ID: 11, PlotID: 3, PlotName: "x", CropID: "c", Kind: "weed",
			HappenedOn: day("2026-05-03"), Operator: "o"},
	}
	rep := Build(devices, ledgers)
	if err := rep.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	c := rep.Counts
	if c.MatchedPairs != 1 || c.DeviceOnly != 1 || c.LedgerOnly != 1 || c.Pending != 2 || c.Consistent != 1 {
		t.Fatalf("unexpected counts: %+v", c)
	}
	// ledger-only 已认出 plot
	var foundUnresolvedPlot bool
	for _, it := range rep.Items {
		if it.MatchType == MatchLedgerOnly && !it.PlotResolved {
			foundUnresolvedPlot = true
		}
	}
	if foundUnresolvedPlot {
		t.Fatal("ledger-only with plot id should be plot-resolved")
	}
}

func TestSplitSameGroupByKind(t *testing.T) {
	// 同地块/同日/同人做了两件事，两边都在，不能被算成 2 对也不能丢。
	mk := func(id int64, kind string) SideRecord {
		return SideRecord{Side: SideDevice, ID: id, PlotID: 1, PlotName: "p", CropID: "c",
			Kind: kind, HappenedOn: day("2026-05-01"), Operator: "o"}
	}
	devices := []SideRecord{mk(1, "fertilize"), mk(2, "pesticide")}
	ledgers := []SideRecord{
		{Side: SideLedger, ID: 10, PlotID: 1, PlotName: "p", CropID: "c",
			Kind: "pesticide", HappenedOn: day("2026-05-01"), Operator: "o"},
		{Side: SideLedger, ID: 11, PlotID: 1, PlotName: "p", CropID: "c",
			Kind: "fertilize", HappenedOn: day("2026-05-01"), Operator: "o"},
	}
	rep := Build(devices, ledgers)
	if err := rep.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.Counts.MatchedPairs != 2 || rep.Counts.Conflicts != 0 {
		t.Fatalf("want 2 matched consistent pairs, got %+v", rep.Counts)
	}
}

func TestDifferentDayOperatorDoNotMatch(t *testing.T) {
	devices := []SideRecord{{
		Side: SideDevice, ID: 1, PlotID: 1, PlotName: "p", CropID: "c", Kind: "weed",
		HappenedOn: day("2026-05-01"), Operator: "张三",
	}}
	for _, l := range []SideRecord{
		{Side: SideLedger, ID: 10, PlotID: 1, PlotName: "p", CropID: "c", Kind: "weed",
			HappenedOn: day("2026-05-02"), Operator: "张三"},
		{Side: SideLedger, ID: 10, PlotID: 1, PlotName: "p", CropID: "c", Kind: "weed",
			HappenedOn: day("2026-05-01"), Operator: "李四"},
		{Side: SideLedger, ID: 10, PlotID: 2, PlotName: "q", CropID: "c", Kind: "weed",
			HappenedOn: day("2026-05-01"), Operator: "张三"},
	} {
		rep := Build(devices, []SideRecord{l})
		if rep.Counts.MatchedPairs != 0 || rep.Counts.LedgerOnly != 1 {
			t.Fatalf("expected no match for %+v, got %+v", l, rep.Counts)
		}
	}
}

func TestLedgerPlotNameFallbackMatch(t *testing.T) {
	// 台账 plot_id 没认出来，但归一化地名一致 → 仍可配对。
	devices := []SideRecord{{
		Side: SideDevice, ID: 1, PlotID: 0, PlotName: "3号地(东)", CropID: "c", Kind: "weed",
		HappenedOn: day("2026-05-01"), Operator: "o",
	}}
	ledgers := []SideRecord{{
		Side: SideLedger, ID: 10, PlotID: 0, PlotName: "3 号地（东）", CropID: "c", Kind: "weed",
		HappenedOn: day("2026-05-01"), Operator: "o",
	}}
	rep := Build(devices, ledgers)
	if rep.Counts.MatchedPairs != 1 {
		t.Fatalf("expected name-fallback match, got %+v", rep.Counts)
	}
}

func TestDoseMissingVersusValueIsConflict(t *testing.T) {
	devices := []SideRecord{{
		Side: SideDevice, ID: 1, PlotID: 1, PlotName: "p", CropID: "c", Kind: "weed",
		HappenedOn: day("2026-05-01"), Operator: "o",
	}}
	ledgers := []SideRecord{{
		Side: SideLedger, ID: 10, PlotID: 1, PlotName: "p", CropID: "c", Kind: "weed",
		HappenedOn: day("2026-05-01"), Operator: "o", Dose: pfloat(5), DoseUnit: "kg",
	}}
	rep := Build(devices, ledgers)
	fields := map[string]bool{}
	for _, d := range rep.Items[0].Differences {
		fields[d.Field] = true
	}
	if !fields["dose"] {
		t.Fatalf("missing-vs-value dose must be a conflict, got %+v", rep.Items[0].Differences)
	}
}
