package handler

import (
	"cc-052/internal/model"
	"cc-052/internal/service"
	"cc-052/pkg/response"
	"database/sql"
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
)

type MergeHandler struct {
	svc *service.MergeService
}

func NewMergeHandler(svc *service.MergeService) *MergeHandler {
	return &MergeHandler{svc: svc}
}

// Create POST /api/v1/merges — 跑一遍对账，生成工作单（不落地）。
func (h *MergeHandler) Create(c *gin.Context) {
	var req model.CreateMergeRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// 允许空 body：全量合并。
		req = model.CreateMergeRunRequest{}
	}
	detail, err := h.svc.CreateRun(&req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Created(c, detail)
}

// List GET /api/v1/merges
func (h *MergeHandler) List(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	runs, err := h.svc.ListRuns(limit)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, runs)
}

// Get GET /api/v1/merges/:id — 计数、恒等式核对、逐条明细（含两边取值与 diff）。
func (h *MergeHandler) Get(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid merge run id")
		return
	}
	detail, err := h.svc.GetRun(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.NotFound(c, "merge run not found")
			return
		}
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, detail)
}

// Resolve POST /api/v1/merges/:id/items/:itemId/resolve — 人工挑一条（留档）。
func (h *MergeHandler) Resolve(c *gin.Context) {
	runID, itemID, ok := parseRunItem(c)
	if !ok {
		return
	}
	var req model.ResolveItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	it, err := h.svc.Resolve(runID, itemID, &req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, it)
}

// Skip POST /api/v1/merges/:id/items/:itemId/skip — 核销（重复抄录/作废）。
func (h *MergeHandler) Skip(c *gin.Context) {
	runID, itemID, ok := parseRunItem(c)
	if !ok {
		return
	}
	var req model.SkipItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := h.svc.Skip(runID, itemID, &req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"skipped": true})
}

// Unresolve POST /api/v1/merges/:id/items/:itemId/unresolve — 撤回去重挑。
func (h *MergeHandler) Unresolve(c *gin.Context) {
	runID, itemID, ok := parseRunItem(c)
	if !ok {
		return
	}
	var body struct {
		Actor string `json:"resolved_by"`
	}
	_ = c.ShouldBindJSON(&body)
	if body.Actor == "" {
		body.Actor = c.Query("actor")
	}
	if err := h.svc.Unresolve(runID, itemID, body.Actor); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"unresolved": true})
}

// Apply POST /api/v1/merges/:id/apply — 全部挑完后落地成「一份」。
func (h *MergeHandler) Apply(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid merge run id")
		return
	}
	var req model.ApplyMergeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	output, check, err := h.svc.Apply(id, &req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"output_count": output, "check": check})
}

// Cancel POST /api/v1/merges/:id/cancel — 取消并释放两边记录。
func (h *MergeHandler) Cancel(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid merge run id")
		return
	}
	var body struct {
		Actor string `json:"actor"`
	}
	_ = c.ShouldBindJSON(&body)
	if err := h.svc.Cancel(id, body.Actor); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"cancelled": true})
}

// Audit GET /api/v1/merges/:id/audit — 人工取舍留档。
func (h *MergeHandler) Audit(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid merge run id")
		return
	}
	logs, err := h.svc.AuditTrail(id)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, logs)
}

func parseRunItem(c *gin.Context) (int64, int64, bool) {
	runID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid merge run id")
		return 0, 0, false
	}
	itemID, err := strconv.ParseInt(c.Param("itemId"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid item id")
		return 0, 0, false
	}
	return runID, itemID, true
}
