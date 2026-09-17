//go:build dbtest

// 端到端集成测试，需要一个本机 PostgreSQL：
//
//	export DB_TEST_DSN_HOST=/tmp DB_TEST_PORT=54329
//	go test -tags dbtest ./internal/service/ -run TestMergeEndToEnd -v
package service

import (
	"cc-052/internal/model"
	"cc-052/internal/repository"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

const testDBName = "farm_merge_test"

func testBaseDSN(dbname string) string {
	host := os.Getenv("DB_TEST_DSN_HOST")
	if host == "" {
		host = "localhost"
	}
	port := os.Getenv("DB_TEST_PORT")
	if port == "" {
		port = "5432"
	}
	return fmt.Sprintf("host=%s port=%s user=farm dbname=%s sslmode=disable", host, port, dbname)
}

func setupTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	admin, err := sqlx.Connect("postgres", testBaseDSN("postgres"))
	if err != nil {
		t.Skipf("no test postgres: %v", err)
	}
	admin.Exec("DROP DATABASE IF EXISTS " + testDBName)
	if _, err := admin.Exec("CREATE DATABASE " + testDBName); err != nil {
		t.Fatalf("create db: %v", err)
	}
	admin.Close()

	db, err := sqlx.Connect("postgres", testBaseDSN(testDBName))
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	if err := repository.RunMigrations(db, "../../migrations"); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if err := repository.RunSeed(db, "../../seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMergeEndToEnd(t *testing.T) {
	db := setupTestDB(t)

	// ---- 基础资料 ----
	farmRepo := repository.NewFarmRepo(db)
	plotRepo := repository.NewPlotRepo(db)
	batchRepo := repository.NewBatchRepo(db)
	actRepo := repository.NewActivityRepo(db)
	ledgerRepo := repository.NewLedgerRepo(db)
	inputRepo := repository.NewInputMaterialRepo(db)
	mergeRepo := repository.NewMergeRepo(db)

	farm := &model.Farm{Name: "测试合作社", RegionCode: "330100"}
	if err := farmRepo.Create(farm); err != nil {
		t.Fatal(err)
	}
	plot := &model.Plot{FarmID: farm.ID, Name: "3号地", AreaMu: 12.5}
	if err := plotRepo.Create(plot); err != nil {
		t.Fatal(err)
	}
	var ureaID, imiID int64
	inputs, err := inputRepo.List()
	if err != nil {
		t.Fatalf("list inputs: %v", err)
	}
	for i := range inputs {
		switch inputs[i].Name {
		case "尿素":
			ureaID = inputs[i].ID
		case "吡虫啉":
			imiID = inputs[i].ID
		}
	}
	if ureaID == 0 || imiID == 0 {
		t.Fatalf("seed inputs not found in %+v", inputs)
	}

	sow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.Local)
	batch := &model.CropBatch{PlotID: plot.ID, CropID: "rice", SowingDate: sow,
		ExpectedYieldKg: 1000, Status: model.BatchStatusGrowing}
	if err := batchRepo.Create(batch); err != nil {
		t.Fatal(err)
	}

	// ---- 采集端 5 条 ----
	d10, d12 := 10.0, 12.0
	kg := "kg"
	mkAct := func(uuid string, kind model.ActivityKind, day time.Time, op string,
		inputID *int64, dose *float64, photos model.StringMap) model.Activity {
		a := model.Activity{
			BatchID: batch.ID, ClientUUID: uuid, Kind: kind,
			HappenedAt: day.Add(9 * time.Hour), InputID: inputID, Dose: dose,
			Operator: op, Photos: photos,
		}
		if dose != nil {
			a.DoseUnit = &kg
		}
		return a
	}
	a1 := mkAct("u-consistent", model.ActivityFertilize, time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local), "张三", &ureaID, &d10, nil)
	a2 := mkAct("u-conflict", model.ActivityPesticide, time.Date(2026, 5, 2, 0, 0, 0, 0, time.Local), "张三", &imiID, &d10, nil)
	a3 := mkAct("u-device-only-1", model.ActivityWeed, time.Date(2026, 5, 3, 0, 0, 0, 0, time.Local), "李四", nil, nil, nil)
	a4 := mkAct("u-will-skip", model.ActivityIrrigation, time.Date(2026, 5, 4, 0, 0, 0, 0, time.Local), "王五", nil, nil, nil)
	a5 := mkAct("u-device-only-2", model.ActivityFertilize, time.Date(2026, 5, 5, 0, 0, 0, 0, time.Local), "赵六", &ureaID, &d10,
		model.StringMap{"photo": "x.jpg"}) // 设备有照片凭据但台账无此行 → device_only
	for _, a := range []model.Activity{a1, a2, a3, a4, a5} {
		if err := actRepo.Create(&a); err != nil {
			t.Fatalf("create activity: %v", err)
		}
	}

	// ---- 手工台账 4 行 ----
	ledgerSvc := NewLedgerService(ledgerRepo, plotRepo, inputRepo)
	imp, err := ledgerSvc.Import(&model.ImportLedgerRequest{
		LedgerBatch: "L-E2E", FarmID: &farm.ID,
		Rows: []model.ImportLedgerRow{
			{SourceRef: "r1", PlotName: "3号地", CropID: "rice", Kind: "fertilize",
				HappenedOn: "2026-05-01", Operator: "张 三", InputName: "尿素", Dose: &d10, DoseUnit: "kg"},
			{SourceRef: "r2", PlotName: "3号地", CropID: "rice", Kind: "pesticide",
				HappenedOn: "2026-05-02", Operator: "张三", InputName: "吡虫啉", Dose: &d12, DoseUnit: "kg",
				HasEvidence: true, EvidenceRef: "签字单#8"},
			{SourceRef: "r3", PlotName: "3号地", CropID: "rice", Kind: "irrigation",
				HappenedOn: "2026-05-04", Operator: "王五"},
			{SourceRef: "r4", PlotName: "3号地", CropID: "rice", Kind: "weed",
				HappenedOn: "2026-05-06", Operator: "李四"},
		},
	})
	if err != nil {
		t.Fatalf("import ledger: %v", err)
	}
	if imp.Imported != 4 || imp.Failed != 0 {
		t.Fatalf("ledger import result wrong: %+v", imp)
	}
	// 重复导入同批号应全部去重
	imp2, err := ledgerSvc.Import(&model.ImportLedgerRequest{
		LedgerBatch: "L-E2E", FarmID: &farm.ID,
		Rows: []model.ImportLedgerRow{
			{SourceRef: "r1", PlotName: "3号地", Kind: "fertilize", HappenedOn: "2026-05-01", Operator: "张三"},
		},
	})
	if err != nil || imp2.Imported != 0 || imp2.Duplicated != 1 {
		t.Fatalf("ledger re-import not idempotent: %+v err=%v", imp2, err)
	}

	// ---- 建合并工作单 ----
	svc := NewMergeService(mergeRepo, actRepo, ledgerRepo, batchRepo, inputRepo)
	detail, err := svc.CreateRun(&model.CreateMergeRunRequest{FarmID: &farm.ID, CreatedBy: "tester"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if !detail.Check.Balanced {
		t.Fatalf("initial check unbalanced: %+v", detail.Check)
	}
	c := detail.Counts
	// 5 设备 + 4 台账；配对 3（r1/a1 一致、r2/a2 冲突、r3/a4 一致）；设备单边 2；台账单边 1
	if c.DeviceIn != 5 || c.LedgerIn != 4 || c.MatchedPairs != 3 ||
		c.DeviceOnly != 2 || c.LedgerOnly != 1 || c.Conflicts != 1 {
		t.Fatalf("unexpected counts: %+v", c)
	}

	var conflictItem, irrigItem, ledgerOnlyItem *model.MergeItem
	for _, it := range detail.Items {
		switch {
		case it.MatchType == "matched" && it.Status == "conflict":
			conflictItem = it
		case it.MatchType == "matched" && it.Status == "consistent" &&
			it.Activity != nil && it.Activity.ClientUUID == "u-will-skip":
			irrigItem = it
		case it.MatchType == "ledger_only":
			ledgerOnlyItem = it
		}
	}
	if conflictItem == nil || irrigItem == nil || ledgerOnlyItem == nil {
		t.Fatalf("missing items: conflict=%v irrig=%v ledgerOnly=%v", conflictItem, irrigItem, ledgerOnlyItem)
	}
	if got := conflictItem.Differences[0].Field; got != "dose" {
		t.Fatalf("want dose diff, got %s", got)
	}
	if w := *conflictItem.WinnerSide; w != "ledger" {
		t.Fatalf("evidence ledger should be suggested winner, got %s", w)
	}

	// 未处理完就 apply 必须被拒绝
	if _, _, err := svc.Apply(detail.Run.ID, &model.ApplyMergeRequest{Actor: "张三"}); err == nil {
		t.Fatal("apply with unresolved items must be rejected")
	}

	// 仅台账记录没给目标批次就 resolve 必须被拒绝
	if _, err := svc.Resolve(detail.Run.ID, ledgerOnlyItem.ID, &model.ResolveItemRequest{
		Winner: "ledger", ResolvedBy: "张三"}); err == nil {
		t.Fatal("ledger-only resolve without target batch must be rejected")
	}

	// ---- 人工处理 ----
	if _, err := svc.Resolve(detail.Run.ID, conflictItem.ID, &model.ResolveItemRequest{
		Winner: "ledger", ResolvedBy: "张三", Note: "以签字单用量为准"}); err != nil {
		t.Fatalf("resolve conflict: %v", err)
	}
	// 撤销裁决：冲突回到未决，计数里 unresolved 应 +1；之后可重新挑（改判留档）
	if err := svc.Unresolve(detail.Run.ID, conflictItem.ID, "张三"); err != nil {
		t.Fatalf("unresolve: %v", err)
	}
	afterUndo, err := svc.GetRun(detail.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterUndo.Run.Unresolved < 1 {
		t.Fatalf("after undo, conflict must be unresolved again, got %d", afterUndo.Run.Unresolved)
	}
	if _, _, err := svc.Apply(detail.Run.ID, &model.ApplyMergeRequest{Actor: "张三"}); err == nil {
		t.Fatal("apply must still be rejected after undo")
	}
	if _, err := svc.Resolve(detail.Run.ID, conflictItem.ID, &model.ResolveItemRequest{
		Winner: "ledger", ResolvedBy: "张三", Note: "复核后仍以签字单为准"}); err != nil {
		t.Fatalf("re-resolve conflict: %v", err)
	}
	if err := svc.Skip(detail.Run.ID, irrigItem.ID, &model.SkipItemRequest{
		Reason: "台账重复抄录", ResolvedBy: "张三"}); err != nil {
		t.Fatalf("skip: %v", err)
	}
	bid := batch.ID
	if _, err := svc.Resolve(detail.Run.ID, ledgerOnlyItem.ID, &model.ResolveItemRequest{
		Winner: "ledger", TargetBatchID: &bid, ResolvedBy: "张三"}); err != nil {
		t.Fatalf("resolve ledger-only: %v", err)
	}

	mid, err := svc.GetRun(detail.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !mid.Check.Balanced || mid.Run.Unresolved != 0 {
		t.Fatalf("pre-apply not balanced: %+v unresolved=%d", mid.Check, mid.Run.Unresolved)
	}

	// ---- 落地成「一份」 ----
	output, check, err := svc.Apply(detail.Run.ID, &model.ApplyMergeRequest{Actor: "张三"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !check.Balanced {
		t.Fatalf("post-apply unbalanced: %+v", check)
	}
	// 事件 6 - 核销 1 = 5
	if output != 5 {
		t.Fatalf("want 5 final records, got %d", output)
	}

	// ---- 落库事实核对 ----
	final, err := actRepo.ListByBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(final, func(i, j int) bool { return final[i].ID < final[j].ID })
	var kept, ledgerSourced, skippedAct int
	for _, a := range final {
		state := ""
		if a.MergeState != nil {
			state = *a.MergeState
		}
		switch {
		case a.Source == "ledger" && state == "kept":
			ledgerSourced++
		case state == "skipped":
			skippedAct++
		case state == "kept":
			kept++
		}
	}
	// 5 设备（其中 1 核销，4 保留/覆写）+ 1 台账补入
	if len(final) != 6 {
		t.Fatalf("expect 6 activity rows, got %d", len(final))
	}
	if ledgerSourced != 1 || skippedAct != 1 || kept != 4 {
		t.Fatalf("states wrong: kept=%d ledger=%d skipped=%d", kept, ledgerSourced, skippedAct)
	}

	// 冲突项覆写后用量 12、note 带台账凭据
	var dose float64
	var note sql.NullString
	if err := db.QueryRowx(`SELECT dose, note FROM activity WHERE client_uuid='u-conflict'`).
		Scan(&dose, &note); err != nil {
		t.Fatal(err)
	}
	if dose != d12 {
		t.Fatalf("overwrite dose want 12, got %v", dose)
	}

	// 台账最终状态：merged 3、skipped 1
	stCounts := map[string]int{}
	rows, _ := db.Queryx(`SELECT status, count(*) FROM ledger_record GROUP BY status`)
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			t.Fatal(err)
		}
		stCounts[s] = n
	}
	if stCounts["merged"] != 3 || stCounts["skipped"] != 1 {
		t.Fatalf("ledger final statuses wrong: %+v", stCounts)
	}

	// 重复 apply 必须报错
	if _, _, err := svc.Apply(detail.Run.ID, &model.ApplyMergeRequest{Actor: "张三"}); err == nil {
		t.Fatal("re-apply should be rejected")
	}

	// 审计留档
	logs, err := svc.AuditTrail(detail.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]int{}
	for _, l := range logs {
		actions[l.Action]++
	}
	for _, want := range []string{"create", "resolve", "skip", "apply"} {
		if actions[want] == 0 {
			t.Fatalf("audit missing action %s, got %+v", want, actions)
		}
	}

	// 全部记录已被认领，再建单应报“没有待合并记录”
	if _, err := svc.CreateRun(&model.CreateMergeRunRequest{FarmID: &farm.ID, CreatedBy: "tester"}); err == nil {
		t.Fatal("creating a new run with no pending records should fail")
	}
}

func TestMergeCancelReleasesRecords(t *testing.T) {
	db := setupTestDB(t)
	farmRepo := repository.NewFarmRepo(db)
	plotRepo := repository.NewPlotRepo(db)
	batchRepo := repository.NewBatchRepo(db)
	actRepo := repository.NewActivityRepo(db)
	ledgerRepo := repository.NewLedgerRepo(db)
	inputRepo := repository.NewInputMaterialRepo(db)
	mergeRepo := repository.NewMergeRepo(db)

	farm := &model.Farm{Name: "取消测试社", RegionCode: "330100"}
	if err := farmRepo.Create(farm); err != nil {
		t.Fatal(err)
	}
	plot := &model.Plot{FarmID: farm.ID, Name: "1号地", AreaMu: 5}
	if err := plotRepo.Create(plot); err != nil {
		t.Fatal(err)
	}
	batch := &model.CropBatch{PlotID: plot.ID, CropID: "rice",
		SowingDate:      time.Date(2026, 3, 1, 0, 0, 0, 0, time.Local),
		ExpectedYieldKg: 100, Status: model.BatchStatusGrowing}
	if err := batchRepo.Create(batch); err != nil {
		t.Fatal(err)
	}
	a := model.Activity{BatchID: batch.ID, ClientUUID: "u-c", Kind: model.ActivityWeed,
		HappenedAt: time.Date(2026, 5, 7, 9, 0, 0, 0, time.Local), Operator: "张三"}
	if err := actRepo.Create(&a); err != nil {
		t.Fatal(err)
	}
	ledgerSvc := NewLedgerService(ledgerRepo, plotRepo, inputRepo)
	if _, err := ledgerSvc.Import(&model.ImportLedgerRequest{
		LedgerBatch: "L-C", FarmID: &farm.ID,
		Rows: []model.ImportLedgerRow{{
			SourceRef: "x1", PlotName: "1号地", CropID: "rice", Kind: "weed",
			HappenedOn: "2026-05-07", Operator: "张三"}},
	}); err != nil {
		t.Fatal(err)
	}

	svc := NewMergeService(mergeRepo, actRepo, ledgerRepo, batchRepo, inputRepo)
	d1, err := svc.CreateRun(&model.CreateMergeRunRequest{FarmID: &farm.ID, CreatedBy: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if d1.Counts.MatchedPairs != 1 {
		t.Fatalf("want 1 matched pair, got %+v", d1.Counts)
	}
	if err := svc.Cancel(d1.Run.ID, "t"); err != nil {
		t.Fatal(err)
	}
	// 认领释放后应能再建一张单，且数字一致
	d2, err := svc.CreateRun(&model.CreateMergeRunRequest{FarmID: &farm.ID, CreatedBy: "t"})
	if err != nil {
		t.Fatalf("records should be releasable after cancel: %v", err)
	}
	if d2.Counts.DeviceIn != 1 || d2.Counts.LedgerIn != 1 || d2.Counts.MatchedPairs != 1 {
		t.Fatalf("second run counts wrong: %+v", d2.Counts)
	}
	logs, err := svc.AuditTrail(d1.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawCancel bool
	for _, l := range logs {
		if l.Action == "cancel" {
			sawCancel = true
		}
	}
	if !sawCancel {
		t.Fatal("cancel must be recorded in audit log")
	}
}

func TestMergeKindOverwriteFromLedger(t *testing.T) {
	db := setupTestDB(t)
	farmRepo := repository.NewFarmRepo(db)
	plotRepo := repository.NewPlotRepo(db)
	batchRepo := repository.NewBatchRepo(db)
	actRepo := repository.NewActivityRepo(db)
	ledgerRepo := repository.NewLedgerRepo(db)
	inputRepo := repository.NewInputMaterialRepo(db)
	mergeRepo := repository.NewMergeRepo(db)

	farm := &model.Farm{Name: "类型覆写社", RegionCode: "330100"}
	if err := farmRepo.Create(farm); err != nil {
		t.Fatal(err)
	}
	plot := &model.Plot{FarmID: farm.ID, Name: "7号地", AreaMu: 3}
	if err := plotRepo.Create(plot); err != nil {
		t.Fatal(err)
	}
	batch := &model.CropBatch{PlotID: plot.ID, CropID: "rice",
		SowingDate:      time.Date(2026, 3, 1, 0, 0, 0, 0, time.Local),
		ExpectedYieldKg: 50, Status: model.BatchStatusGrowing}
	if err := batchRepo.Create(batch); err != nil {
		t.Fatal(err)
	}
	// 设备记成 fertilize，台账记成 weed 且有凭据
	a := model.Activity{BatchID: batch.ID, ClientUUID: "u-kind", Kind: model.ActivityFertilize,
		HappenedAt: time.Date(2026, 5, 8, 9, 0, 0, 0, time.Local), Operator: "张三", DoseUnit: nil}
	if err := actRepo.Create(&a); err != nil {
		t.Fatal(err)
	}
	ledgerSvc := NewLedgerService(ledgerRepo, plotRepo, inputRepo)
	if _, err := ledgerSvc.Import(&model.ImportLedgerRequest{
		LedgerBatch: "L-K", FarmID: &farm.ID,
		Rows: []model.ImportLedgerRow{{
			SourceRef: "k1", PlotName: "7号地", CropID: "rice", Kind: "weed",
			HappenedOn: "2026-05-08", Operator: "张三",
			HasEvidence: true, EvidenceRef: "照片对照单#1"}},
	}); err != nil {
		t.Fatal(err)
	}

	svc := NewMergeService(mergeRepo, actRepo, ledgerRepo, batchRepo, inputRepo)
	d, err := svc.CreateRun(&model.CreateMergeRunRequest{FarmID: &farm.ID, CreatedBy: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Counts.Conflicts != 1 {
		t.Fatalf("want 1 conflict, got %+v", d.Counts)
	}
	var conflictID int64
	for _, it := range d.Items {
		if it.Status == "conflict" {
			conflictID = it.ID
			var foundKind bool
			for _, df := range it.Differences {
				if df.Field == "kind" {
					foundKind = true
				}
			}
			if !foundKind {
				t.Fatal("kind diff missing")
			}
		}
	}
	if _, err := svc.Resolve(d.Run.ID, conflictID, &model.ResolveItemRequest{
		Winner: "ledger", ResolvedBy: "张三"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Apply(d.Run.ID, &model.ApplyMergeRequest{Actor: "张三"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var kind string
	if err := db.Get(&kind, `SELECT kind FROM activity WHERE client_uuid='u-kind'`); err != nil {
		t.Fatal(err)
	}
	if kind != "weed" {
		t.Fatalf("kind should be overwritten to weed, got %s", kind)
	}
}
