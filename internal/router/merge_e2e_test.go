package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"

	"cc-052/internal/handler"
	"cc-052/internal/repository"
	"cc-052/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

// 端到端：采集端 + 手工台账 -> 合并 -> 冲突 -> 人工挑拣 -> 对账 -> 留档
// 需要真实 PostgreSQL：MERGE_E2E_DSN="host=... user=... dbname=... sslmode=disable" go test ./internal/router/
func TestMergeEndToEnd(t *testing.T) {
	dsn := os.Getenv("MERGE_E2E_DSN")
	if dsn == "" {
		t.Skip("MERGE_E2E_DSN not set")
	}
	gin.SetMode(gin.TestMode)

	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// 干净环境
	if _, err := db.Exec(`DROP TABLE IF EXISTS merge_decision, merged_record, merge_group_member,
		merge_group, merge_run, field_record, trace_code, inspection, activity,
		crop_batch, input_material, plot, farm CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.RunMigrations(db, "../../migrations"); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if err := repository.RunSeed(db, "../../seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 与 main.go 相同的装配
	farmRepo := repository.NewFarmRepo(db)
	plotRepo := repository.NewPlotRepo(db)
	batchRepo := repository.NewBatchRepo(db)
	activityRepo := repository.NewActivityRepo(db)
	inspectionRepo := repository.NewInspectionRepo(db)
	codeRepo := repository.NewTraceCodeRepo(db)
	fieldRecordRepo := repository.NewFieldRecordRepo(db)
	mergeRepo := repository.NewMergeRepo(db)

	farmSvc := service.NewFarmService(farmRepo)
	plotSvc := service.NewPlotService(plotRepo)
	batchSvc := service.NewBatchService(batchRepo, plotRepo, farmRepo)
	activitySvc := service.NewActivityService(activityRepo, batchRepo)
	inspectionSvc := service.NewInspectionService(inspectionRepo, batchRepo)
	traceCodeSvc := service.NewTraceCodeService(codeRepo, batchRepo, inspectionRepo, activityRepo, plotRepo, farmRepo)
	mergeSvc := service.NewMergeService(fieldRecordRepo, mergeRepo, plotRepo)

	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}) // 本测试不经过限流路由
	r := Setup(
		handler.NewFarmHandler(farmSvc),
		handler.NewPlotHandler(plotSvc),
		handler.NewBatchHandler(batchSvc),
		handler.NewActivityHandler(activitySvc),
		handler.NewInspectionHandler(inspectionSvc),
		handler.NewTraceCodeHandler(traceCodeSvc),
		handler.NewMergeHandler(mergeSvc),
		handler.NewHealthHandler(db, rdb),
		rdb,
	)

	call := func(method, path string, body interface{}) json.RawMessage {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var env struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s %s: bad json %s: %v", method, path, w.Body.String(), err)
		}
		if env.Code >= 400 {
			t.Fatalf("%s %s -> %d: %s", method, path, w.Code, env.Message)
		}
		return env.Data
	}
	do := func(method, path string, body interface{}) map[string]interface{} {
		out := map[string]interface{}{}
		if raw := call(method, path, body); len(raw) > 0 {
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("%s %s: data not object: %s", method, path, string(raw))
			}
		}
		return out
	}
	doArray := func(method, path string, body interface{}) []map[string]interface{} {
		var out []map[string]interface{}
		if raw := call(method, path, body); len(raw) > 0 {
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("%s %s: data not array: %s", method, path, string(raw))
			}
		}
		return out
	}

	// 基础资料：农场 + 地块
	farm := do("POST", "/api/v1/farms", map[string]interface{}{"name": "丰收合作社", "region_code": "330000"})
	farmID := int64(farm["id"].(float64))
	plot := do("POST", "/api/v1/plots", map[string]interface{}{"farm_id": farmID, "name": "东一亩", "area_mu": 1.5})
	plotID := int64(plot["id"].(float64))

	// 采集端 + 手工台账一起交上来（c-001 重传一次，验证幂等）
	collectorA := map[string]interface{}{
		"source": "collector", "client_uuid": "c-001", "plot_id": plotID, "crop_id": "rice",
		"kind": "fertilize", "happened_at": "2026-09-01", "operator": "张三",
		"dose": 20, "dose_unit": "kg", "photos": map[string]string{"0": "photo/a.jpg"},
	}
	ledgerFertilize := map[string]interface{}{
		"source": "ledger", "plot_id": plotID, "crop_id": "rice",
		"kind": "fertilize", "happened_at": "2026-09-01", "operator": "张三",
		"dose": 50, "dose_unit": "kg",
	}
	ledgerPesticide := map[string]interface{}{
		"source": "ledger", "plot_id": plotID, "crop_id": "rice",
		"kind": "pesticide", "happened_at": "2026-09-03", "operator": "李四",
		"dose": 5, "dose_unit": "L",
	}
	collectorB := map[string]interface{}{
		"source": "collector", "client_uuid": "c-002", "plot_id": plotID, "crop_id": "rice",
		"kind": "pesticide", "happened_at": "2026-09-03T09:00:00Z", "operator": "李四",
		"dose": 6, "dose_unit": "L",
	}
	collectorC := map[string]interface{}{
		"source": "collector", "client_uuid": "c-003", "plot_id": plotID, "crop_id": "wheat",
		"kind": "irrigation", "happened_at": "2026-09-05", "operator": "王五",
	}

	submit := do("POST", "/api/v1/records", map[string]interface{}{
		"records": []interface{}{collectorA, collectorA, ledgerFertilize, ledgerPesticide, collectorB, collectorC},
	})
	if submit["inserted"].(float64) != 5 || submit["duplicates"].(float64) != 1 {
		t.Fatalf("submit: %+v", submit)
	}

	// 记录 id 映射
	records := doArray("GET", "/api/v1/records", nil)
	if len(records) != 5 {
		t.Fatalf("expected 5 field records, got %d", len(records))
	}
	idByUUID := map[string]int64{}
	var ledgerPestID int64
	for _, rec := range records {
		if u, ok := rec["client_uuid"].(string); ok {
			idByUUID[u] = int64(rec["id"].(float64))
		} else if rec["kind"] == "pesticide" {
			ledgerPestID = int64(rec["id"].(float64))
		}
	}

	// 执行合并
	run := do("POST", "/api/v1/merge/runs", nil)
	runID := int64(run["id"].(float64))
	if run["total_in"].(float64) != 5 || run["kept"].(float64) != 3 || run["superseded"].(float64) != 2 {
		t.Fatalf("run counts: %+v", run)
	}
	if run["evidence_resolved"].(float64) != 1 || run["pending_conflicts"].(float64) != 1 {
		t.Fatalf("run groups: %+v", run)
	}

	// 对账：合并前后总数必须对得上
	rec1 := do("GET", fmt.Sprintf("/api/v1/merge/runs/%d/reconcile", runID), nil)
	if rec1["ok"].(bool) != true || rec1["accounted"].(float64) != 5 {
		t.Fatalf("reconcile: %+v", rec1)
	}

	// 待人工冲突：李四打药那组（台账 5L vs 采集端 6L，都没凭据）
	conflicts := doArray("GET", fmt.Sprintf("/api/v1/merge/runs/%d/conflicts", runID), nil)
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict group, got %d", len(conflicts))
	}
	conflictGroup := conflicts[0]["group"].(map[string]interface{})
	groupID := int64(conflictGroup["id"].(float64))
	members := conflicts[0]["members"].([]interface{})
	if len(members) != 2 {
		t.Fatalf("conflict group should have 2 members: %+v", members)
	}

	// 人工挑拣：整组保留台账那条，但剂量挑采集端的 6L
	decision := do("POST", fmt.Sprintf("/api/v1/merge/groups/%d/resolve", groupID), map[string]interface{}{
		"decided_by":     "老王",
		"kept_record_id": ledgerPestID,
		"field_picks":    map[string]int64{"dose": idByUUID["c-002"]},
		"note":           "剂量以采集端为准",
	})
	if decision["rule"].(string) != "manual" || decision["decided_by"].(string) != "老王" {
		t.Fatalf("decision: %+v", decision)
	}

	// 留档：系统临时决定 + 人工定夺，两次都在
	decisions := doArray("GET", fmt.Sprintf("/api/v1/merge/groups/%d/decisions", groupID), nil)
	if len(decisions) != 2 {
		t.Fatalf("expected 2 archived decisions, got %d", len(decisions))
	}
	if decisions[1]["rule"].(string) != "manual" {
		t.Fatalf("second decision should be manual: %+v", decisions[1])
	}

	// 合并产出：3 条；冲突组剂量=6（人工挑的），凭据组剂量=20（有照片的赢）
	merged := doArray("GET", fmt.Sprintf("/api/v1/merged-records?run_id=%d", runID), nil)
	if len(merged) != 3 {
		t.Fatalf("expected 3 merged records, got %d", len(merged))
	}
	doseByKind := map[string]float64{}
	for _, m := range merged {
		if d, ok := m["dose"].(float64); ok {
			doseByKind[m["kind"].(string)] = d
		}
	}
	if doseByKind["pesticide"] != 6 {
		t.Fatalf("resolved group dose should be 6 (picked), got %v", doseByKind["pesticide"])
	}
	if doseByKind["fertilize"] != 20 {
		t.Fatalf("evidence group dose should be 20 (evidenced), got %v", doseByKind["fertilize"])
	}

	// 人工处理后对账仍然平
	rec2 := do("GET", fmt.Sprintf("/api/v1/merge/runs/%d/reconcile", runID), nil)
	if rec2["ok"].(bool) != true {
		t.Fatalf("reconcile after resolve: %+v", rec2)
	}

	// 再次重传 c-001 仍幂等；再跑一次合并应为空运行
	submit2 := do("POST", "/api/v1/records", map[string]interface{}{"records": []interface{}{collectorA}})
	if submit2["inserted"].(float64) != 0 || submit2["duplicates"].(float64) != 1 {
		t.Fatalf("resubmit: %+v", submit2)
	}
	run2 := do("POST", "/api/v1/merge/runs", nil)
	if run2["total_in"].(float64) != 0 {
		t.Fatalf("second run should be empty: %+v", run2)
	}
	rec3 := do("GET", fmt.Sprintf("/api/v1/merge/runs/%d/reconcile", int64(run2["id"].(float64))), nil)
	if rec3["ok"].(bool) != true {
		t.Fatalf("reconcile empty run: %+v", rec3)
	}

	// 对不上要能查出是哪一条引起的：人为删掉一条成员去向，对账必须点名这条记录
	victim := idByUUID["c-003"]
	if _, err := db.Exec(`DELETE FROM merge_group_member WHERE record_id=$1`, victim); err != nil {
		t.Fatal(err)
	}
	recBad := do("GET", fmt.Sprintf("/api/v1/merge/runs/%d/reconcile", runID), nil)
	if recBad["ok"].(bool) != false {
		t.Fatalf("reconcile should fail after member removal: %+v", recBad)
	}
	missing, _ := recBad["missing_record_ids"].([]interface{})
	if len(missing) != 1 || int64(missing[0].(float64)) != victim {
		t.Fatalf("missing_record_ids should name record %d: %+v", victim, recBad)
	}
}
