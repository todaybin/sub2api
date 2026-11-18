// Copyright (c) 2026 WorkMesh. 保留所有权利。
package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func (h *OpenAIGatewayHandler) SpeechUserHistory(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		speechError(c, 401, "SPEECH_FORBIDDEN")
		return
	}
	cursor := c.Query("cursor")
	if len(cursor) > 36 {
		speechError(c, 400, "SPEECH_INPUT_INVALID")
		return
	}
	store, err := h.gatewayService.SpeechStore()
	if err != nil {
		speechError(c, 503, "SPEECH_UNAVAILABLE")
		return
	}
	rows, err := store.ListUserSpeech(c.Request.Context(), subject.UserID, cursor)
	if err != nil {
		speechError(c, 503, "SPEECH_UNAVAILABLE")
		return
	}
	next := ""
	if len(rows) > 50 {
		next = rows[49]["id"].(string)
		rows = rows[:50]
	}
	c.JSON(200, gin.H{"items": rows, "nextCursor": next})
}
