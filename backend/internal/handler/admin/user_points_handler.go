package admin

import (
	"context"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (h *UserHandler) UpdatePoints(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid user ID")
		return
	}
	var req struct {
		Points    float64 `json:"points" binding:"gte=0,lte=999999999999"`
		Operation string  `json:"operation" binding:"required,oneof=set add subtract"`
		Notes     string  `json:"notes"`
	}
	if err = c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request")
		return
	}
	payload := struct {
		ID   int64
		Body any
	}{id, req}
	scope := "admin.users.points.update"
	if isIntegrationRequest(c) {
		scope += ":integration:" + c.GetString(middleware.IntegrationAppIDContextKey)
	}
	executeAdminIdempotentJSON(c, scope, payload, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		operationID, err := service.NormalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
		if err != nil {
			return nil, err
		}
		if operationID == "" {
			operationID = uuid.NewString()
		}
		prefix := "admin:"
		// Match the coordinator's app scope in the durable ledger as well, so
		// independent integrations cannot suppress each other's top-ups.
		if isIntegrationRequest(c) {
			prefix = "integration:" + c.GetString(middleware.IntegrationAppIDContextKey) + ":"
		}
		operationID = prefix + strconv.FormatInt(id, 10) + ":" + operationID
		user, err := h.userService.AdjustPoints(ctx, id, req.Points, req.Operation, operationID, req.Notes)
		if err != nil {
			return nil, err
		}
		return dto.UserFromServiceAdmin(user), nil
	})
}

func (h *UserHandler) PointsHistory(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid user ID")
		return
	}
	page, size := response.ParsePagination(c)
	entries, total, err := h.userService.PointsHistory(c.Request.Context(), id, page, size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, entries, total, page, size)
}
