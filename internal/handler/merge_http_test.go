//go:build dbtest

// 真实 HTTP 冒烟：farm → plot → batch → 批量 activities → 台账导入
// → 建合并单 → 人工裁决/核销 → 落地 → 审计，全走 Gin 路由与 JSON 绑定。
package handler_test

import (
	"bytes"
	"cc-052/internal/handler"
	"cc-052/internal/repository"
	"cc-052/internal/router"
	"cc-052/internal/service"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-redis/redis/v8"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

const httpTestDB = "farm_merge_http_test"

func httpDSN(dbname string) string {
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

func setupHTTPEngine(t *testing.T) http.Handler {
	t.Helper()
	admin, err := sqlx.Connect("postgres", httpDSN("postgres"))
	if err != nil {
		t.Skipf("no test postgres: %v", err)
	}
	admin.Exec("DROP DATABASE IF EXISTS " + httpTestDB)
	if _, err := admin.Exec("CREATE DATABASE " + httpTestDB); err != nil {
		t.Fatalf("create db: %v", err)
	}
	admin.Close()

	db, err := sqlx.Connect("postgres", httpDSN(httpTestDB))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RunMigrations(db, "../../migrations"); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if err := repository.RunSeed(db, "../../seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	farmRepo := repository.NewFarmRepo(db)
	plotRepo := repository.NewPlotRepo(db)
	batchRepo := repository.NewBatchRepo(db)
	actRepo := repository.NewActivityRepo(db)
	inspRepo := repository.NewInspectionRepo(db)
	codeRepo := repository.NewTraceCodeRepo(db)
	inputRepo := repository.NewInputMaterialRepo(db)
	ledgerRepo := repository.NewLedgerRepo(db)
	mergeRepo := repository.NewMergeRepo(db)

	return router.Setup(
		handler.NewFarmHandler(service.NewFarmService(farmRepo)),
		handler.NewPlotHandler(service.NewPlotService(plotRepo)),
		handler.NewBatchHandler(service.NewBatchService(batchRepo, plotRepo, farmRepo)),
		handler.NewActivityHandler(service.NewActivityService(actRepo, batchRepo)),
		handler.NewInspectionHandler(service.NewInspectionService(inspRepo, batchRepo)),
		handler.NewTraceCodeHandler(service.NewTraceCodeService(codeRepo, batchRepo, inspRepo, actRepo, plotRepo, farmRepo)),
		handler.NewLedgerHandler(service.NewLedgerService(ledgerRepo, plotRepo, inputRepo)),
		handler.NewMergeHandler(service.NewMergeService(mergeRepo, actRepo, ledgerRepo, batchRepo, inputRepo)),
		handler.NewHealthHandler(db, nil),
		redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"}), // 只绕开 /trace 与 /healthz
	)
}

type apiEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func doJSON(t *testing.T, h http.Handler, method, path string, body interface{}) (int, apiEnvelope) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var env apiEnvelope
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("bad json body (%d): %s", w.Code, w.Body.String())
		}
	}
	return w.Code, env
}

func dataMap(t *testing.T, env apiEnvelope) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(env.Data, &m); err != nil {
		t.Fatalf("data not object: %s", string(env.Data))
	}
	return m
}

func TestMergeHTTPFlow(t *testing.T) {
	h := setupHTTPEngine(t)

	// 投入品字典里拿两个 id
	_, envInputs := doJSON(t, h, "GET", "/api/v1/ledger", nil) // 仅探活：台账为空
	_ = envInputs

	st, env := doJSON(t, h, "POST", "/api/v1/farms", map[string]interface{}{
		"name": "HTTP合作社", "region_code": "330100"})
	if st != http.StatusCreated {
		t.Fatalf("create farm %d %s", st, env.Message)
	}
	farmID := int64(dataMap(t, env)["id"].(float64))

	st, env = doJSON(t, h, "POST", "/api/v1/plots", map[string]interface{}{
		"farm_id": farmID, "name": "3号地", "area_mu": 12.5})
	if st != http.StatusCreated {
		t.Fatalf("create plot %d %s", st, env.Message)
	}
	plotID := int64(dataMap(t, env)["id"].(float64))

	st, env = doJSON(t, h, "POST", "/api/v1/batches", map[string]interface{}{
		"plot_id": plotID, "crop_id": "rice", "sowing_date": "2026-03-01",
		"expected_yield_kg": 1000})
	if st != http.StatusCreated {
		t.Fatalf("create batch %d %s", st, env.Message)
	}
	batchID := int64(dataMap(t, env)["id"].(float64))

	// 采集端 3 条：一致 / 用量冲突（台账有凭据）/ 仅设备
	// 种子字典 id：吡虫啉=2、尿素=5
	acts := []map[string]interface{}{
		{"client_uuid": "h1", "kind": "fertilize", "happened_at": "2026-05-01T09:00:00+08:00",
			"input_id": 5, "operator": "张三", "dose": 10, "dose_unit": "kg"},
		{"client_uuid": "h2", "kind": "pesticide", "happened_at": "2026-05-02T09:00:00+08:00",
			"input_id": 2, "operator": "张三", "dose": 10, "dose_unit": "kg"},
		{"client_uuid": "h3", "kind": "weed", "happened_at": "2026-05-03T09:00:00+08:00",
			"operator": "李四"},
	}
	st, env = doJSON(t, h, "POST", fmt.Sprintf("/api/v1/batches/%d/activities/batch", batchID),
		map[string]interface{}{"activities": acts})
	if st != http.StatusCreated {
		t.Fatalf("batch activities %d %s", st, env.Message)
	}

	// 台账 2 行
	st, env = doJSON(t, h, "POST", "/api/v1/ledger/import", map[string]interface{}{
		"ledger_batch": "LH1", "farm_id": farmID,
		"rows": []map[string]interface{}{
			{"source_ref": "r1", "plot_name": "3号地", "crop_id": "rice", "kind": "fertilize",
				"happened_on": "2026-05-01", "operator": "张 三", "input_name": "尿素",
				"dose": 10, "dose_unit": "kg"},
			{"source_ref": "r2", "plot_name": "3号地", "crop_id": "rice", "kind": "pesticide",
				"happened_on": "2026-05-02", "operator": "张三", "input_name": "吡虫啉",
				"dose": 12, "dose_unit": "kg", "has_evidence": true, "evidence_ref": "单#8"},
		},
	})
	if st != http.StatusCreated {
		t.Fatalf("import ledger %d %s", st, env.Message)
	}
	if m := dataMap(t, env); m["imported"].(float64) != 2 {
		t.Fatalf("imported count: %v", m)
	}

	// 建合并单
	st, env = doJSON(t, h, "POST", "/api/v1/merges", map[string]interface{}{
		"farm_id": farmID, "created_by": "tester"})
	if st != http.StatusCreated {
		t.Fatalf("create merge %d %s", st, env.Message)
	}
	runData := dataMap(t, env)
	runID := int64(runData["run"].(map[string]interface{})["id"].(float64))
	chk := runData["check"].(map[string]interface{})
	if chk["balanced"] != true {
		t.Fatalf("initial check: %v", chk)
	}
	items := runData["items"].([]interface{})
	if len(items) != 3 {
		t.Fatalf("want 3 items, got %d", len(items))
	}
	var conflictID int64
	for _, raw := range items {
		it := raw.(map[string]interface{})
		if it["status"] == "conflict" {
			conflictID = int64(it["id"].(float64))
		}
	}
	if conflictID == 0 {
		t.Fatal("conflict item not found")
	}

	// 冲突未处理时 apply 被拒（400）
	st, _ = doJSON(t, h, "POST", fmt.Sprintf("/api/v1/merges/%d/apply", runID),
		map[string]interface{}{"actor": "张三"})
	if st != http.StatusBadRequest {
		t.Fatalf("apply before resolve should be 400, got %d", st)
	}

	// 人工采纳台账（凭据方）
	st, env = doJSON(t, h, "POST",
		fmt.Sprintf("/api/v1/merges/%d/items/%d/resolve", runID, conflictID),
		map[string]interface{}{"winner": "ledger", "resolved_by": "张三",
			"note": "以签字单为准"})
	if st != http.StatusOK {
		t.Fatalf("resolve %d %s", st, env.Message)
	}

	// 落地
	st, env = doJSON(t, h, "POST", fmt.Sprintf("/api/v1/merges/%d/apply", runID),
		map[string]interface{}{"actor": "张三"})
	if st != http.StatusOK {
		t.Fatalf("apply %d %s", st, env.Message)
	}
	applyData := dataMap(t, env)
	if applyData["output_count"].(float64) != 3 {
		t.Fatalf("output_count: %v", applyData)
	}
	if applyData["check"].(map[string]interface{})["balanced"] != true {
		t.Fatalf("apply check: %v", applyData["check"])
	}

	// 审计留档可查
	st, env = doJSON(t, h, "GET", fmt.Sprintf("/api/v1/merges/%d/audit", runID), nil)
	if st != http.StatusOK || len(dataList(t, env)) == 0 {
		t.Fatalf("audit trail empty / %d", st)
	}
}

func dataList(t *testing.T, env apiEnvelope) []interface{} {
	t.Helper()
	var l []interface{}
	if err := json.Unmarshal(env.Data, &l); err != nil {
		t.Fatalf("data not list: %v", err)
	}
	return l
}
