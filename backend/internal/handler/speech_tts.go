// Copyright (c) 2026 WorkMesh. 保留所有权利。
package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/service/speech"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

func (h *OpenAIGatewayHandler) SpeechSynthesis(c *gin.Context) {
	token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if len(token) != 43 {
		speechError(c, 401, "SPEECH_TICKET_INVALID")
		return
	}
	store, err := h.gatewayService.SpeechStore()
	if err != nil {
		speechError(c, 503, "SPEECH_UNAVAILABLE")
		return
	}
	hash := sha256.Sum256([]byte(token))
	session, err := store.RedeemSpeechSession(c.Request.Context(), hex.EncodeToString(hash[:]))
	if err != nil || session.ID != c.Param("id") || session.Model.Kind != "tts" {
		speechError(c, 403, "SPEECH_TICKET_INVALID")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 96000)
	var input struct {
		Text  string `json:"text"`
		Voice string `json:"voice"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || !utf8.ValidString(input.Text) || len(input.Text) == 0 || len(input.Text) > 64000 {
		speechError(c, 400, "TTS_TEXT_INVALID")
		return
	}
	voiceAllowed := false
	for _, v := range session.Model.Voices {
		if v == input.Voice {
			voiceAllowed = true
		}
	}
	if !voiceAllowed {
		speechError(c, 400, "TTS_VOICE_INVALID")
		return
	}
	key, err := h.apiKeyService.GetByID(c.Request.Context(), session.APIKeyID)
	if err != nil || key == nil || key.User == nil || key.Group == nil || key.Status != "active" || key.User.Status != "active" || key.User.ID != session.UserID || key.Group.ID != session.GroupID || !session.Model.Allows(key.Group.ID) || !key.Group.AllowPointsBilling {
		speechError(c, 403, "SPEECH_FORBIDDEN")
		return
	}
	if !h.speechKeyAllowed(c, key) {
		speechError(c, 403, "SPEECH_FORBIDDEN")
		return
	}
	adapter, credentials, err := h.gatewayService.SpeechAdapter(c.Request.Context(), session.Model)
	if err != nil {
		speechError(c, 503, "SPEECH_PROVIDER_UNAVAILABLE")
		return
	}
	slots, e := h.speechSlots(c, key, session.Model.AccountID, credentials.MaxConcurrency)
	if e != nil {
		speechError(c, 429, "SPEECH_CONCURRENCY_LIMIT")
		return
	}
	defer slots()
	units, _ := speech.AliyunCharacters(input.Text)
	estimate, _ := session.Model.Cost(float64(units))
	var payment *service.PointsPayment
	reservation := "free"
	var releasePayment func()
	if estimate > 0 {
		if err = h.billingCacheService.CheckBillingEligibility(c.Request.Context(), key.User, key, key.Group, nil, ""); err != nil {
			speechError(c, 402, "SPEECH_BILLING_LIMIT")
			return
		}
		paymentCtx, release, selected, e := h.billingCacheService.ReservePointsPayment(c.Request.Context(), key, estimate)
		if e != nil || !selected {
			speechError(c, 402, "INSUFFICIENT_POINTS")
			return
		}
		releasePayment = release
		payment = service.PointsPaymentFromContext(paymentCtx)
		reservation = payment.ID
	}
	meter := &service.SpeechMeterRecord{ID: session.ID + "_tts", SessionID: session.ID, ReservationID: reservation, Unit: "characters", State: "pending", SupplierState: "inflight", ProviderRequestID: speech.ProviderRequestID(session.ID + "_tts")}
	defer func() {
		if releasePayment != nil {
			if meter.SupplierState == "inflight" || meter.SupplierState == "unknown" || (meter.Quantity > 0 && err != nil) {
				payment.StopRenewal()
			} else {
				releasePayment()
			}
		}
	}()
	if err = store.PutSpeechMeter(c.Request.Context(), meter); err != nil {
		// 写入结果未知时同样保留预扣，由数据库恢复任务核对。
		speechError(c, 503, "SPEECH_STORAGE_UNAVAILABLE")
		return
	}
	c.Header("Content-Type", "audio/pcm")
	c.Header("Cache-Control", "no-store")
	c.Header("Trailer", "X-Speech-State")
	c.Header("X-Speech-Sample-Rate", "16000")
	ctx, cancel := context.WithTimeout(speech.WithProviderRequestID(context.WithoutCancel(c.Request.Context()), meter.ProviderRequestID), 110*time.Second)
	defer cancel()
	quantity, err := adapter.Synthesize(ctx, session.Model, credentials, input.Text, input.Voice, speechSynthesisSink(c.Request.Context(), func(data []byte) error {
		if _, e := c.Writer.Write(data); e != nil {
			return e
		}
		c.Writer.Flush()
		return nil
	}))
	cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	state := "interrupted"
	if err != nil {
		meter.SupplierState = "unknown"
		_ = store.PutSpeechMeter(cleanup, meter)
	}
	if err == nil {
		meter.SupplierState = "confirmed"
		meter.Quantity = float64(quantity)
		// 先保存供应商确认用量，结算暂不可用时恢复器仍能继续。
		if err = store.PutSpeechMeter(cleanup, meter); err != nil {
			meter.SupplierState = "unknown"
			_ = store.EndSpeechSession(cleanup, session.ID, state)
			c.Header("X-Speech-State", state)
			return
		}
		cost, _ := session.Model.Cost(meter.Quantity)
		if payment == nil {
			meter.State = "settled"
			err = store.PutSpeechMeter(cleanup, meter)
		} else {
			meter.Points = service.PointsCost(cost, payment.Rate)
			cmd := &service.UsageBillingCommand{RequestID: meter.ID, APIKeyID: key.ID, UserID: key.User.ID, AccountID: session.Model.AccountID, Model: session.Model.ID, BillingType: service.BillingTypePoints, MediaType: "audio", PointsReservationID: payment.ID, PointsGroupID: key.Group.ID, PointsPerUSD: payment.Rate, PointsCostUSD: cost, APIKeyQuotaCost: cost, APIKeyRateLimitCost: cost}
			err = h.gatewayService.SpeechSettle(cleanup, meter, cmd)
		}
		if err == nil {
			state = "completed"
		}
	}
	_ = store.EndSpeechSession(cleanup, session.ID, state)
	c.Header("X-Speech-State", state)
	if err != nil && !c.Writer.Written() {
		speechError(c, 502, "SPEECH_SYNTHESIS_FAILED")
	}
}

// 已准入的合成请求继续读取最终用量；播放端断开不能让已消费的云端请求绕过计费。
// 丢弃后续音频但不保存正文或音频，供应商调用仍受原来的 110 秒超时约束。
func speechSynthesisSink(ctx context.Context, send func([]byte) error) func([]byte) error {
	closed := false
	return func(data []byte) error {
		if closed || ctx.Err() != nil {
			return nil
		}
		if send(data) != nil {
			closed = true
		}
		return nil
	}
}
