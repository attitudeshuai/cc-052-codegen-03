// Package merge 是「采集端 × 手工台账」的纯内存对账引擎：
// 按 地块+作物+日期+操作人 认出两边说的是同一件事，逐字段摆出差异，
// 给出默认取舍（先有凭据者，凭据相当时留早的那条），并核对条数恒等式。
//
// 它不碰数据库：输入是两侧的归一化记录，输出是待裁决明细与对账报告，
// 由 service 层持久化与落地。
package merge

import (
	"cc-052/pkg/mergenorm"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type Side string

const (
	SideDevice Side = "device"
	SideLedger Side = "ledger"
)

const (
	MatchMatched    = "matched"
	MatchDeviceOnly = "device_only"
	MatchLedgerOnly = "ledger_only"

	StatusConsistent = "consistent" // 配对且一致，自动通过
	StatusConflict   = "conflict"   // 配对但打架，等人挑（可能已预选凭据方）
	StatusPending    = "pending"    // 单边记录，等确认/补目标批次
	StatusSkipped    = "skipped"    // 人工核销
)

// SideRecord 一侧的一条记录，字段已经过 service 层归一化与解析。
type SideRecord struct {
	Side        Side
	ID          int64  // activity.id 或 ledger_record.id
	BatchID     int64  // 采集端批次；台账侧为 0（裁决后再指定）
	SourceRef   string // 台账行号，便于人工溯源
	PlotID      int64  // 0 = 认不出地块
	PlotName    string // 归一化地块名（认不出 id 时的兜底）
	CropID      string // 归一化
	Kind        string
	HappenedOn  time.Time // 统一到当地日期
	HappenedAt  time.Time // 采集端的精确时刻（留早用）
	CreatedAt   time.Time // 记录产生/上报时间（留早的第二依据）
	Operator    string    // 归一化
	InputID     int64     // 0 = 未解析
	InputName   string    // 归一化名称
	Dose        *float64
	DoseUnit    string
	HasEvidence bool
	EvidenceRef string
}

// Identity 是「同一件事」的四要素。地块认不出 id 时用地名兜底。
type Identity struct {
	PlotID   int64
	PlotName string
	CropID   string
	Day      string // YYYY-MM-DD（当地日）
	Operator string
}

// FieldValue 一侧某个字段的展示值。
type FieldValue struct {
	Side    Side   `json:"side"`
	Value   string `json:"value"`
	Missing bool   `json:"missing,omitempty"`
}

// FieldDiff 一个打架字段的两边取值与预选结果。
type FieldDiff struct {
	Field         string     `json:"field"`
	Label         string     `json:"label"`
	Device        FieldValue `json:"device"`
	Ledger        FieldValue `json:"ledger"`
	SuggestedSide Side       `json:"suggested_side,omitempty"`
	Reason        string     `json:"reason,omitempty"`
}

// ItemDraft 一个「事件」的对账明细：配对算一个，单边算一个。
type ItemDraft struct {
	MatchType string
	Status    string
	Identity  Identity

	Device *SideRecord
	Ledger *SideRecord

	Differences []FieldDiff

	// SuggestedWinner 预选胜方：配对冲突按凭据，凭据相当时留早；
	// 最终仍以人工确认为准（留档）。
	SuggestedWinner Side
	SuggestReason   string

	// EarliestSide 更早产生的那条（“认出来之后保留早的”）。
	EarliestSide Side

	// PlotResolved 台账侧是否已认出地块（ledger-only 落地时必须为真）。
	PlotResolved bool
}

// Counts 逐项计数，任意时刻都必须满足对账恒等式。
type Counts struct {
	DeviceIn     int `json:"device_in"`
	LedgerIn     int `json:"ledger_in"`
	MatchedPairs int `json:"matched_pairs"`
	DeviceOnly   int `json:"device_only"`
	LedgerOnly   int `json:"ledger_only"`
	Consistent   int `json:"consistent"`
	Conflicts    int `json:"conflicts"`
	Pending      int `json:"pending"`
	Skipped      int `json:"skipped"`
}

// Resolved/Unresolved 是派生数：resolved = 已有着落（一致 + 核销），
// 冲突与单边在人工确认前一律算未决。
func (c Counts) Resolved() int   { return c.Consistent + c.Skipped }
func (c Counts) Unresolved() int { return c.Conflicts + c.Pending }

// Report 一次对账的完整结果。
type Report struct {
	Items  []*ItemDraft
	Counts Counts
}

type idKey struct {
	plotID   int64
	plotName string
	crop     string
	day      string
	operator string
}

// Build 执行配对与比对。要求两侧记录已归一化（同 service 的归一规则）。
func Build(devices, ledgers []SideRecord) *Report {
	rep := &Report{
		Counts: Counts{DeviceIn: len(devices), LedgerIn: len(ledgers)},
	}

	// 采集端两个索引：能认地块时按 id 归组，认不全时按归一地名兜底。
	byID := map[idKey][]*SideRecord{}
	byName := map[idKey][]*SideRecord{}
	for i := range devices {
		d := &devices[i]
		idn := identityOf(d)
		k := idKey{plotID: idn.PlotID, crop: idn.CropID, day: idn.Day, operator: idn.Operator}
		byID[k] = append(byID[k], d)
		kn := idKey{plotName: idn.PlotName, crop: idn.CropID, day: idn.Day, operator: idn.Operator}
		byName[kn] = append(byName[kn], d)
	}

	used := map[*SideRecord]bool{}

	for i := range ledgers {
		l := &ledgers[i]
		idn := identityOf(l)
		var group []*SideRecord
		if l.PlotID != 0 {
			group = byID[idKey{plotID: l.PlotID, crop: idn.CropID, day: idn.Day, operator: idn.Operator}]
		}
		if len(group) == 0 && idn.PlotName != "" {
			group = byName[idKey{plotName: idn.PlotName, crop: idn.CropID, day: idn.Day, operator: idn.Operator}]
		}

		d := pickUnused(group, used, l.Kind)
		if d == nil {
			rep.addSingle(l)
			continue
		}
		used[d] = true
		rep.addPair(d, l)
	}
	for i := range devices {
		d := &devices[i]
		if !used[d] {
			rep.addSingle(d)
		}
	}

	// 稳定的展示顺序：日期、地块、操作人。
	sort.SliceStable(rep.Items, func(a, b int) bool {
		ia, ib := rep.Items[a].Identity, rep.Items[b].Identity
		if ia.Day != ib.Day {
			return ia.Day < ib.Day
		}
		pa, pb := ia.PlotName, ib.PlotName
		if pa != pb {
			return pa < pb
		}
		return ia.Operator < ib.Operator
	})

	rep.recompute()
	return rep
}

// Verify 校验对账恒等式，不成立时返回能定位到明细的错误。
//
//	device_in = matched_pairs + device_only
//	ledger_in = matched_pairs + ledger_only
//	事件总数    = consistent + conflicts + pending + skipped
func (r *Report) Verify() error {
	c := r.Counts
	if c.DeviceIn != c.MatchedPairs+c.DeviceOnly {
		return fmt.Errorf("采集端条数对不上: in=%d 但配对(%d)+单边(%d)=%d",
			c.DeviceIn, c.MatchedPairs, c.DeviceOnly, c.MatchedPairs+c.DeviceOnly)
	}
	if c.LedgerIn != c.MatchedPairs+c.LedgerOnly {
		return fmt.Errorf("台账条数对不上: in=%d 但配对(%d)+单边(%d)=%d",
			c.LedgerIn, c.MatchedPairs, c.LedgerOnly, c.MatchedPairs+c.LedgerOnly)
	}
	events := c.MatchedPairs + c.DeviceOnly + c.LedgerOnly
	if events != c.Consistent+c.Conflicts+c.Pending+c.Skipped {
		return fmt.Errorf("事件总数对不上: 配对%d+单边%d=%d 但 一致%d+冲突%d+待决%d+核销%d=%d",
			c.MatchedPairs, c.DeviceOnly+c.LedgerOnly, events,
			c.Consistent, c.Conflicts, c.Pending, c.Skipped,
			c.Consistent+c.Conflicts+c.Pending+c.Skipped)
	}
	if events != len(r.Items) {
		return fmt.Errorf("明细行数对不上: 计数=%d 实际=%d，逐条核对 merge_item", events, len(r.Items))
	}

	// 逐条溯源：每个 item 的两侧引用数之和必须等于两侧入参总数。
	dRefs, lRefs := 0, 0
	for idx, it := range r.Items {
		if it.Device != nil {
			dRefs++
		}
		if it.Ledger != nil {
			lRefs++
		}
		switch it.MatchType {
		case MatchMatched:
			if it.Device == nil || it.Ledger == nil {
				return fmt.Errorf("第 %d 条明细标记 matched 但引用不全（activity=%v ledger=%v）",
					idx+1, it.Device != nil, it.Ledger != nil)
			}
		case MatchDeviceOnly:
			if it.Device == nil || it.Ledger != nil {
				return fmt.Errorf("第 %d 条明细标记 device_only 但引用异常", idx+1)
			}
		case MatchLedgerOnly:
			if it.Ledger == nil || it.Device != nil {
				return fmt.Errorf("第 %d 条明细标记 ledger_only 但引用异常", idx+1)
			}
		}
	}
	if dRefs != c.DeviceIn {
		return fmt.Errorf("采集端引用丢失: %d 条入参但只有 %d 条挂在明细上", c.DeviceIn, dRefs)
	}
	if lRefs != c.LedgerIn {
		return fmt.Errorf("台账引用丢失: %d 条入参但只有 %d 条挂在明细上", c.LedgerIn, lRefs)
	}
	return nil
}

func (r *Report) addPair(d, l *SideRecord) {
	r.Counts.MatchedPairs++
	it := &ItemDraft{
		MatchType:    MatchMatched,
		Identity:     identityOf(d),
		Device:       d,
		Ledger:       l,
		EarliestSide: earlierOf(d, l),
		PlotResolved: true,
	}
	it.Differences = diffFields(d, l)
	if len(it.Differences) == 0 {
		it.Status = StatusConsistent
		// 一致也按“留早”决定保留哪条，便于 apply 统一处理。
		it.SuggestedWinner = it.EarliestSide
	} else {
		it.Status = StatusConflict
		it.SuggestedWinner, it.SuggestReason = suggestWinner(d, l, it.EarliestSide)
		for i := range it.Differences {
			it.Differences[i].SuggestedSide = it.SuggestedWinner
			if it.SuggestedWinner != "" {
				it.Differences[i].Reason = it.SuggestReason
			}
		}
	}
	r.Items = append(r.Items, it)
}

func (r *Report) addSingle(rec *SideRecord) {
	it := &ItemDraft{
		Identity:     identityOf(rec),
		EarliestSide: rec.Side,
	}
	switch rec.Side {
	case SideDevice:
		r.Counts.DeviceOnly++
		it.MatchType = MatchDeviceOnly
		it.Device = rec
		it.SuggestedWinner = SideDevice
		it.SuggestReason = "仅采集端有此记录，默认保留"
	default:
		r.Counts.LedgerOnly++
		it.MatchType = MatchLedgerOnly
		it.Ledger = rec
		it.PlotResolved = rec.PlotID != 0
		it.SuggestedWinner = SideLedger
		it.SuggestReason = "仅手工台账有此记录，确认后补入"
	}
	it.Status = StatusPending
	r.Items = append(r.Items, it)
}

func (r *Report) recompute() {
	c := Counts{DeviceIn: r.Counts.DeviceIn, LedgerIn: r.Counts.LedgerIn}
	for _, it := range r.Items {
		switch it.MatchType {
		case MatchMatched:
			c.MatchedPairs++
		case MatchDeviceOnly:
			c.DeviceOnly++
		case MatchLedgerOnly:
			c.LedgerOnly++
		}
		switch it.Status {
		case StatusConsistent:
			c.Consistent++
		case StatusConflict:
			c.Conflicts++
		case StatusPending:
			c.Pending++
		case StatusSkipped:
			c.Skipped++
		}
	}
	r.Counts = c
}

// pickUnused 在候选组里挑一条还没用过的采集端记录：优先同操作类型，
// 避免把同地块同日同操作人的「施肥」和「打药」错配（配错了也会以
// kind 冲突暴露，但优先同类型更稳）。
func pickUnused(group []*SideRecord, used map[*SideRecord]bool, kind string) *SideRecord {
	var fallback *SideRecord
	for _, d := range group {
		if used[d] {
			continue
		}
		if d.Kind == kind {
			return d
		}
		if fallback == nil {
			fallback = d
		}
	}
	return fallback
}

func identityOf(r *SideRecord) Identity {
	return Identity{
		PlotID:   r.PlotID,
		PlotName: mergenorm.PlotName(r.PlotName),
		CropID:   mergenorm.Crop(r.CropID),
		Day:      r.HappenedOn.Format("2006-01-02"),
		Operator: mergenorm.Operator(r.Operator),
	}
}

// earlierOf 留早规则：先比农事发生的「日」（台账只记到日，不与设备的
// 当天时刻直接比）；同一天则比记录产生时间（采集端拍照上报 / 台账录入）。
func earlierOf(d, l *SideRecord) Side {
	if !d.HappenedOn.IsZero() && !l.HappenedOn.IsZero() {
		ds, ls := dayStart(d.HappenedOn), dayStart(l.HappenedOn)
		if ds.Before(ls) {
			return SideDevice
		}
		if ls.Before(ds) {
			return SideLedger
		}
	}
	if !d.CreatedAt.IsZero() && !l.CreatedAt.IsZero() {
		if d.CreatedAt.Before(l.CreatedAt) {
			return SideDevice
		}
		if l.CreatedAt.Before(d.CreatedAt) {
			return SideLedger
		}
	}
	return SideDevice
}

// suggestWinner 打架时的预选：只有一边有凭据 → 凭据方；
// 两边都有/都没有 → 留早的那条；仍然分不出就空着强制人工挑。
func suggestWinner(d, l *SideRecord, earliest Side) (Side, string) {
	switch {
	case d.HasEvidence && !l.HasEvidence:
		return SideDevice, "采集端记录有凭据而台账无凭据，以采集端为准"
	case l.HasEvidence && !d.HasEvidence:
		return SideLedger, "台账行有凭据（签字/报告）而采集端无凭据，以台账为准"
	case d.HasEvidence && l.HasEvidence:
		return "", "两边都有凭据，需人工核对后指定"
	default:
		if earliest != "" {
			return earliest, "两边均无凭据，默认保留更早登记的那条，可人工改判"
		}
		return "", "两边均无凭据且先后难辨，需人工指定"
	}
}

func diffFields(d, l *SideRecord) []FieldDiff {
	var diffs []FieldDiff

	if d.Kind != l.Kind {
		diffs = append(diffs, FieldDiff{Field: "kind", Label: "操作类型",
			Device: fv(SideDevice, d.Kind, false), Ledger: fv(SideLedger, l.Kind, false)})
	}

	// 投入品：id 都解析出来按 id 比；否则按归一化名称比；名称对不上 id 的
	// 情况（一边字典 id、一边手写名）以名称为准比一次。
	if !inputEqual(d, l) {
		dn, ln := d.InputName, l.InputName
		diffs = append(diffs, FieldDiff{Field: "input", Label: "投入品",
			Device: fv(SideDevice, dn, dn == ""), Ledger: fv(SideLedger, ln, ln == "")})
	}

	if !doseEqual(d.Dose, l.Dose) {
		diffs = append(diffs, FieldDiff{Field: "dose", Label: "用量",
			Device: doseFV(SideDevice, d.Dose), Ledger: doseFV(SideLedger, l.Dose)})
	}

	du, lu := strings.ToLower(strings.TrimSpace(d.DoseUnit)), strings.ToLower(strings.TrimSpace(l.DoseUnit))
	if du != lu {
		diffs = append(diffs, FieldDiff{Field: "dose_unit", Label: "用量单位",
			Device: fv(SideDevice, d.DoseUnit, d.DoseUnit == ""),
			Ledger: fv(SideLedger, l.DoseUnit, l.DoseUnit == "")})
	}

	return diffs
}

func inputEqual(d, l *SideRecord) bool {
	if d.InputID != 0 && l.InputID != 0 {
		return d.InputID == l.InputID
	}
	dn, ln := mergenorm.Crop(d.InputName), mergenorm.Crop(l.InputName)
	if dn == "" && ln == "" {
		return true // 两边都没记投入品，视为一致
	}
	return dn != "" && ln != "" && dn == ln
}

func doseEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	// DECIMAL(12,4) 精度
	return math.Abs(round4(*a)-round4(*b)) < 0.00005
}

func round4(x float64) float64 { return math.Round(x*10000) / 10000 }

func fv(side Side, v string, missing bool) FieldValue {
	return FieldValue{Side: side, Value: v, Missing: missing}
}

func doseFV(side Side, v *float64) FieldValue {
	if v == nil {
		return FieldValue{Side: side, Missing: true}
	}
	return FieldValue{Side: side, Value: fmt.Sprintf("%g", round4(*v))}
}

func dayStart(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
