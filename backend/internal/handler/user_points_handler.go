package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func (h *UserHandler) PointsSummary(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	summary, err := h.userService.PointsSummary(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, summary)
}

func (h *UserHandler) PointsHistory(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	page, size := response.ParsePagination(c)
	entries, total, err := h.userService.PointsHistory(c.Request.Context(), subject.UserID, page, size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, entries, total, page, size)
}
