package router

import (
	"cc-052/internal/handler"
	"cc-052/internal/middleware"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

func Setup(
	farmH *handler.FarmHandler,
	plotH *handler.PlotHandler,
	batchH *handler.BatchHandler,
	activityH *handler.ActivityHandler,
	inspectionH *handler.InspectionHandler,
	traceCodeH *handler.TraceCodeHandler,
	ledgerH *handler.LedgerHandler,
	mergeH *handler.MergeHandler,
	healthH *handler.HealthHandler,
	rdb *redis.Client,
) *gin.Engine {
	r := gin.Default()

	// Health check
	r.GET("/healthz", healthH.Health)

	// API v1
	v1 := r.Group("/api/v1")
	{
		// Farms
		v1.POST("/farms", farmH.Create)
		v1.GET("/farms", farmH.List)
		v1.GET("/farms/:id", farmH.GetByID)

		// Plots
		v1.POST("/plots", plotH.Create)
		v1.GET("/plots/:id", plotH.GetByID)
		v1.GET("/plots", plotH.ListByFarm)

		// Batches
		v1.POST("/batches", batchH.Create)
		v1.GET("/batches/:id", batchH.GetByID)

		// Activities
		v1.POST("/batches/:id/activities", activityH.Create)
		v1.POST("/batches/:id/activities/batch", activityH.BatchCreate)
		v1.GET("/batches/:id/activities", activityH.ListByBatch)

		// Inspections
		v1.POST("/batches/:id/inspection", inspectionH.Create)

		// Trace codes
		v1.POST("/batches/:id/codes", traceCodeH.Generate)

		// 手工台账导入（staging，不直接写 activity）
		v1.POST("/ledger/import", ledgerH.Import)
		v1.GET("/ledger", ledgerH.ListPending)

		// 采集端 × 手工台账 对账合并
		v1.POST("/merges", mergeH.Create)
		v1.GET("/merges", mergeH.List)
		v1.GET("/merges/:id", mergeH.Get)
		v1.POST("/merges/:id/apply", mergeH.Apply)
		v1.POST("/merges/:id/cancel", mergeH.Cancel)
		v1.GET("/merges/:id/audit", mergeH.Audit)
		v1.POST("/merges/:id/items/:itemId/resolve", mergeH.Resolve)
		v1.POST("/merges/:id/items/:itemId/skip", mergeH.Skip)
		v1.POST("/merges/:id/items/:itemId/unresolve", mergeH.Unresolve)
	}

	// Public trace endpoints with rate limiting
	traceGroup := r.Group("/api/v1/trace")
	traceGroup.Use(middleware.RateLimit(rdb, 30, time.Minute))
	{
		traceGroup.GET("/:code", traceCodeH.Trace)
		traceGroup.GET("/:code/validate", traceCodeH.Validate)
	}

	return r
}
