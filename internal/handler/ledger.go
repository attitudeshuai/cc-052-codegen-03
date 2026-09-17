package handler

import (
	"cc-052/internal/model"
	"cc-052/internal/service"
	"cc-052/pkg/response"
	"strconv"

	"github.com/gin-gonic/gin"
)

type LedgerHandler struct {
	svc *service.LedgerService
}

func NewLedgerHandler(svc *service.LedgerService) *LedgerHandler {
	return &LedgerHandler{svc: svc}
}

// Import POST /api/v1/ledger/import
func (h *LedgerHandler) Import(c *gin.Context) {
	var req model.ImportLedgerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	res, err := h.svc.Import(&req)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Created(c, res)
}

// ListPending GET /api/v1/ledger?farm_id=
func (h *LedgerHandler) ListPending(c *gin.Context) {
	var farmID *int64
	if v := c.Query("farm_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			response.BadRequest(c, "invalid farm_id")
			return
		}
		farmID = &id
	}
	rows, err := h.svc.ListPending(farmID)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, rows)
}
