package handler

import (
	"strconv"

	"cc-052/internal/model"
	"cc-052/internal/service"
	"cc-052/pkg/response"

	"github.com/gin-gonic/gin"
)

type MergeHandler struct {
	svc *service.MergeService
}

func NewMergeHandler(svc *service.MergeService) *MergeHandler {
	return &MergeHandler{svc: svc}
}

// SubmitRecords POST /api/v1/records  采集端/手工台账批量上交原始记录
func (h *MergeHandler) SubmitRecords(c *gin.Context) {
	var req model.BatchSubmitRecordsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	result, err := h.svc.SubmitRecords(req.Records)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Created(c, result)
}

// ListRecords GET /api/v1/records?unmerged=1
func (h *MergeHandler) ListRecords(c *gin.Context) {
	unmerged := c.Query("unmerged") == "1" || c.Query("unmerged") == "true"
	records, err := h.svc.ListRecords(unmerged)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, records)
}

// RunMerge POST /api/v1/merge/runs  执行一次合并
func (h *MergeHandler) RunMerge(c *gin.Context) {
	run, err := h.svc.RunMerge()
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Created(c, run)
}

// ListRuns GET /api/v1/merge/runs
func (h *MergeHandler) ListRuns(c *gin.Context) {
	runs, err := h.svc.ListRuns()
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, runs)
}

// GetRun GET /api/v1/merge/runs/:id
func (h *MergeHandler) GetRun(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid run id")
		return
	}
	run, err := h.svc.GetRun(id)
	if err != nil {
		response.NotFound(c, "merge run not found")
		return
	}
	response.Success(c, run)
}

// Reconcile GET /api/v1/merge/runs/:id/reconcile  对账：合并前后总数核对
func (h *MergeHandler) Reconcile(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid run id")
		return
	}
	result, err := h.svc.Reconcile(id)
	if err != nil {
		response.NotFound(c, "merge run not found")
		return
	}
	response.Success(c, result)
}

// ListConflicts GET /api/v1/merge/runs/:id/conflicts  待人工挑拣的冲突组
func (h *MergeHandler) ListConflicts(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid run id")
		return
	}
	details, err := h.svc.ListConflicts(id)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, details)
}

// GetGroup GET /api/v1/merge/groups/:id  分组详情（原始记录 + 差异 + 当前产出）
func (h *MergeHandler) GetGroup(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid group id")
		return
	}
	detail, err := h.svc.GetGroupDetail(id)
	if err != nil {
		response.NotFound(c, "merge group not found")
		return
	}
	response.Success(c, detail)
}

// ResolveGroup POST /api/v1/merge/groups/:id/resolve  人工挑拣（留档）
func (h *MergeHandler) ResolveGroup(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid group id")
		return
	}
	var req model.ResolveGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	decision, err := h.svc.ResolveGroup(id, &req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, decision)
}

// ListDecisions GET /api/v1/merge/groups/:id/decisions  某组的抉择档案
func (h *MergeHandler) ListDecisions(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid group id")
		return
	}
	decisions, err := h.svc.ListDecisions(id)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, decisions)
}

// ListMerged GET /api/v1/merged-records?run_id=  合并后的台账
func (h *MergeHandler) ListMerged(c *gin.Context) {
	var runID int64
	if s := c.Query("run_id"); s != "" {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			response.BadRequest(c, "invalid run_id")
			return
		}
		runID = id
	}
	records, err := h.svc.ListMerged(runID)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, records)
}
