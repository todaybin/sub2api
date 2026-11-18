// Copyright (c) 2026 WorkMesh. 保留所有权利。
package handler

import (
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *OpenAIGatewayHandler) SpeechAdminReconciliation(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		speechError(c, 401, "SPEECH_FORBIDDEN")
		return
	}
	base, e := h.gatewayService.SpeechStore()
	if e != nil {
		speechError(c, 503, "SPEECH_UNAVAILABLE")
		return
	}
	store, ok := base.(service.SpeechReconciliationStore)
	if !ok {
		speechError(c, 503, "SPEECH_UNAVAILABLE")
		return
	}
	if c.Request.Method == "GET" {
		cursor := c.Query("cursor")
		if len(cursor) > 128 {
			speechError(c, 400, "SPEECH_INPUT_INVALID")
			return
		}
		rows, e := store.ListSpeechReconciliation(c.Request.Context(), cursor)
		if e != nil {
			speechError(c, 503, "SPEECH_UNAVAILABLE")
			return
		}
		next := ""
		if len(rows) > 50 {
			next, ok = rows[49]["id"].(string)
			if !ok || next == "" {
				speechError(c, 503, "SPEECH_UNAVAILABLE")
				return
			}
			rows = rows[:50]
		}
		c.JSON(200, gin.H{"items": rows, "nextCursor": next})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2048)
	var in service.SpeechReconciliation
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || len(c.Param("id")) > 128 {
		speechError(c, 400, "SPEECH_INPUT_INVALID")
		return
	}
	in.ActorID = subject.UserID
	if e = store.ReconcileSpeech(c.Request.Context(), c.Param("id"), in); e != nil {
		speechError(c, 409, "SPEECH_RECONCILIATION_INVALID_OR_CONFLICT")
		return
	}
	c.JSON(200, gin.H{"saved": true})
}
