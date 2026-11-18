// Copyright (c) 2026 WorkMesh. 保留所有权利。
package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/service/speech"
	ws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http"
	"regexp"
	"strings"
	"time"
)

func (h *OpenAIGatewayHandler) speechKeyAllowed(c *gin.Context, key *service.APIKey) bool {
	if key == nil || !key.IsActive() || key.User == nil || !key.User.IsActive() || h.apiKeyService.CheckAPIKeyQuotaAndExpiry(key) != nil {
		return false
	}
	trust := false
	if h.cfg != nil {
		trust = h.cfg.TrustForwardedIPForAPIKeyACL()
	}
	allowed, _ := ip.CheckIPRestriction(ip.GetSecurityClientIP(c, trust), key.IPWhitelist, key.IPBlacklist)
	return allowed
}
func (h *OpenAIGatewayHandler) speechSlots(c *gin.Context, key *service.APIKey, account int64, limit int) (func(), error) {
	if h.concurrencyHelper == nil {
		return nil, fmt.Errorf("SPEECH_UNAVAILABLE")
	}
	release, ok, e := h.concurrencyHelper.TryAcquireUserSlotForAPIKey(c.Request.Context(), key.User.ID, key.User.Concurrency, key.ID)
	if e != nil || !ok {
		return nil, fmt.Errorf("SPEECH_CONCURRENCY_LIMIT")
	}
	accountRelease, ok, e := h.concurrencyHelper.TryAcquireAccountSlot(c.Request.Context(), account, limit)
	if e != nil || !ok {
		release()
		return nil, fmt.Errorf("SPEECH_CONCURRENCY_LIMIT")
	}
	return func() { accountRelease(); release() }, nil
}
func speechError(c *gin.Context, status int, code string) {
	c.JSON(status, gin.H{"code": code, "message": code})
}
func (h *OpenAIGatewayHandler) SpeechCatalog(c *gin.Context) {
	key, ok := middleware.GetAPIKeyFromContext(c)
	if !ok {
		speechError(c, 401, "SPEECH_FORBIDDEN")
		return
	}
	models, err := h.gatewayService.SpeechModels(c.Request.Context(), key)
	if err != nil {
		speechError(c, 503, "SPEECH_UNAVAILABLE")
		return
	}
	items := []gin.H{}
	for _, m := range models {
		items = append(items, gin.H{"id": m.ID, "label": m.Label, "kind": m.Kind, "provider": m.Provider, "languages": m.Languages, "voices": m.Voices, "unit": m.Unit, "usdPerUnit": m.USDPerUnit, "priceVersion": m.Version, "priceLabel": fmt.Sprintf("%.8f USD/%s", *m.USDPerUnit, m.Unit)})
	}
	c.JSON(200, gin.H{"items": items})
}
func (h *OpenAIGatewayHandler) SpeechCreate(c *gin.Context) {
	key, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || key.Group == nil || !key.Group.AllowPointsBilling {
		speechError(c, 403, "SPEECH_POINTS_REQUIRED")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var in struct {
		Model     string `json:"model"`
		Language  string `json:"language"`
		RequestID string `json:"requestId"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || !regexp.MustCompile("^[A-Za-z0-9_-]{1,128}$").MatchString(in.RequestID) {
		speechError(c, 400, "SPEECH_INPUT_INVALID")
		return
	}
	models, err := h.gatewayService.SpeechModels(c.Request.Context(), key)
	var chosen *speech.Model
	for _, m := range models {
		if m.ID == in.Model {
			value := m
			chosen = &value
			break
		}
	}
	if err != nil || chosen == nil {
		speechError(c, 403, "SPEECH_MODEL_FORBIDDEN")
		return
	}
	if !chosen.AcceptsLanguage(in.Language) {
		speechError(c, 400, "SPEECH_LANGUAGE_INVALID")
		return
	}
	store, err := h.gatewayService.SpeechStore()
	if err != nil {
		speechError(c, 503, "SPEECH_STORAGE_UNAVAILABLE")
		return
	}
	if _, err = h.billingCacheService.GetPointsAccount(c.Request.Context(), key.User.ID); err != nil {
		speechError(c, 503, "SPEECH_BILLING_UNAVAILABLE")
		return
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		speechError(c, 503, "SPEECH_UNAVAILABLE")
		return
	}
	ticket := base64.RawURLEncoding.EncodeToString(token)
	hash := sha256.Sum256([]byte(ticket))
	session := &service.SpeechSessionRecord{ID: uuid.NewString(), APIKeyID: key.ID, UserID: key.User.ID, GroupID: key.Group.ID, RequestID: in.RequestID, Language: in.Language, TicketHash: hex.EncodeToString(hash[:]), Model: *chosen, ExpiresAt: time.Now().Add(time.Minute)}
	if err = store.CreateSpeechSession(c.Request.Context(), session); err != nil {
		speechError(c, 409, "SPEECH_SESSION_CONFLICT")
		return
	}
	c.JSON(200, gin.H{"sessionId": session.ID, "ticket": ticket, "streamPath": "/v1/speech/sessions/" + session.ID + "/stream", "expiresIn": 60})
}
func (h *OpenAIGatewayHandler) SpeechUsage(c *gin.Context) {
	key, ok := middleware.GetAPIKeyFromContext(c)
	if !ok {
		speechError(c, 401, "SPEECH_FORBIDDEN")
		return
	}
	store, err := h.gatewayService.SpeechStore()
	if err != nil {
		speechError(c, 503, "SPEECH_STORAGE_UNAVAILABLE")
		return
	}
	session, err := store.GetSpeechSession(c.Request.Context(), c.Param("id"), key.ID)
	if err != nil {
		speechError(c, 404, "SPEECH_SESSION_NOT_FOUND")
		return
	}
	meters, err := store.ListSpeechMeters(c.Request.Context(), session.ID)
	if err != nil {
		speechError(c, 503, "SPEECH_STORAGE_UNAVAILABLE")
		return
	}
	c.JSON(200, gin.H{"sessionId": session.ID, "model": session.Model.ID, "feature": session.Model.Kind, "priceVersion": session.Model.Version, "state": session.State, "items": meters})
}
func (h *OpenAIGatewayHandler) SpeechAdminModels(c *gin.Context) {
	if c.Request.Method == "GET" {
		models, err := h.gatewayService.AllSpeechModels(c.Request.Context())
		if err != nil {
			speechError(c, 503, "SPEECH_CONFIG_UNAVAILABLE")
			return
		}
		c.JSON(200, gin.H{"items": models, "version": service.SpeechConfigVersion(models)})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256000)
	var in struct {
		Items           []speech.Model `json:"items"`
		ExpectedVersion string         `json:"expectedVersion"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		speechError(c, 400, "SPEECH_CONFIG_INVALID")
		return
	}
	if err := h.gatewayService.SetSpeechModels(c.Request.Context(), in.Items, in.ExpectedVersion); err != nil {
		code := err.Error()
		switch code {
		case "SPEECH_CONFIG_CONFLICT", "SPEECH_PRICE_VERSION_REQUIRED", "SPEECH_ACCOUNT_INVALID", "SPEECH_GROUP_INVALID":
		default:
			code = "SPEECH_CONFIG_INVALID"
		}
		speechError(c, 409, code)
		return
	}
	c.JSON(200, gin.H{"saved": true, "version": service.SpeechConfigVersion(in.Items)})
}

// SpeechStream 仅接受一次性票据，不借用管理员身份或页面的长期 API Key。
func (h *OpenAIGatewayHandler) SpeechStream(c *gin.Context) {
	token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	if len(token) != 43 {
		speechError(c, 401, "SPEECH_TICKET_INVALID")
		return
	}
	hash := sha256.Sum256([]byte(token))
	store, err := h.gatewayService.SpeechStore()
	if err != nil {
		speechError(c, 503, "SPEECH_UNAVAILABLE")
		return
	}
	session, err := store.RedeemSpeechSession(c.Request.Context(), hex.EncodeToString(hash[:]))
	if err != nil || session.ID != c.Param("id") {
		speechError(c, 403, "SPEECH_TICKET_INVALID")
		return
	}
	key, err := h.apiKeyService.GetByID(c.Request.Context(), session.APIKeyID)
	if err != nil || key == nil || key.Status != "active" || key.User == nil || key.User.Status != "active" || key.Group == nil || key.User.ID != session.UserID || key.Group.ID != session.GroupID || !key.Group.AllowPointsBilling || !session.Model.Allows(key.Group.ID) {
		speechError(c, 403, "SPEECH_FORBIDDEN")
		return
	}
	if !h.speechKeyAllowed(c, key) {
		speechError(c, 403, "SPEECH_FORBIDDEN")
		return
	}
	if session.Model.Kind != "asr" {
		speechError(c, 400, "SPEECH_ASR_MODEL_REQUIRED")
		return
	}
	adapter, credentials, err := h.gatewayService.SpeechAdapter(c.Request.Context(), session.Model)
	if err != nil {
		speechError(c, 503, "SPEECH_PROVIDER_UNAVAILABLE")
		return
	}
	releaseSlot, e := h.speechSlots(c, key, session.Model.AccountID, credentials.MaxConcurrency)
	if e != nil {
		speechError(c, 429, "SPEECH_CONCURRENCY_LIMIT")
		return
	}
	defer releaseSlot()
	conn, err := ws.Accept(c.Writer, c.Request, &ws.AcceptOptions{})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(24000)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Hour)
	defer cancel()
	state := "interrupted"
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = store.EndSpeechSession(cleanup, session.ID, state)
	}()
	emit := func(value any) error {
		raw, _ := json.Marshal(value)
		writeCtx, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		return conn.Write(writeCtx, ws.MessageText, raw)
	}
	var meter *service.SpeechMeterRecord
	var payment *service.PointsPayment
	var release func()
	var window, sequence int
	var frozenRate float64
	settle := func() error {
		if meter == nil {
			return nil
		}
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if meter.SupplierState == "inflight" || meter.SupplierState == "unknown" {
			meter.SupplierState = "unknown"
			err = store.PutSpeechMeter(cleanup, meter)
			if err == nil {
				err = fmt.Errorf("SPEECH_USAGE_UNCERTAIN")
			}
		} else if meter.Quantity == 0 {
			meter.State = "released"
			err = store.PutSpeechMeter(cleanup, meter)
		} else if *session.Model.USDPerUnit == 0 {
			meter.State = "settled"
			err = store.PutSpeechMeter(cleanup, meter)
		} else {
			cost, _ := session.Model.Cost(meter.Quantity)
			meter.Points = service.PointsCost(cost, payment.Rate)
			cmd := &service.UsageBillingCommand{RequestID: meter.ID, APIKeyID: key.ID, UserID: key.User.ID, AccountID: session.Model.AccountID, Model: session.Model.ID, BillingType: service.BillingTypePoints, MediaType: "audio", PointsReservationID: payment.ID, PointsGroupID: key.Group.ID, PointsPerUSD: payment.Rate, PointsCostUSD: cost, APIKeyQuotaCost: cost, APIKeyRateLimitCost: cost}
			err = h.gatewayService.SpeechSettle(cleanup, meter, cmd)
		}
		// 失败保留 pending 预扣及用量，由现有恢复循环对账；停止续租后才会被恢复。
		if release != nil {
			if err == nil || (meter.Quantity == 0 && meter.SupplierState != "unknown" && meter.SupplierState != "inflight") {
				release()
			} else {
				payment.StopRenewal()
			}
			release = nil
		}
		meter = nil
		payment = nil
		return err
	}
	defer func() { _ = settle() }()
	reserve := func() error {
		if err = settle(); err != nil {
			return err
		}
		window++
		fresh, e := h.apiKeyService.GetByID(ctx, key.ID)
		if e != nil || !h.speechKeyAllowed(c, fresh) || fresh.Status != "active" || fresh.User == nil || fresh.User.Status != "active" || fresh.Group == nil || fresh.Group.ID != session.GroupID || !fresh.Group.AllowPointsBilling {
			return fmt.Errorf("SPEECH_FORBIDDEN")
		}
		available, e := h.gatewayService.SpeechModels(ctx, fresh)
		if e != nil {
			return e
		}
		allowed := false
		for _, m := range available {
			if m.ID == session.Model.ID {
				allowed = true
			}
		}
		if !allowed {
			return fmt.Errorf("SPEECH_MODEL_FORBIDDEN")
		}
		cost, _ := session.Model.Cost(60)
		reservation := "free"
		if cost > 0 {
			if e = h.billingCacheService.CheckBillingEligibility(ctx, fresh.User, fresh, fresh.Group, nil, ""); e != nil {
				return e
			}
		}
		if cost > 0 {
			paymentCtx, done, selected, e := h.billingCacheService.ReservePointsPayment(ctx, key, cost)
			if e != nil {
				return e
			}
			if !selected {
				return fmt.Errorf("SPEECH_POINTS_REQUIRED")
			}
			release = done
			payment = service.PointsPaymentFromContext(paymentCtx)
			if frozenRate != 0 && payment.Rate != frozenRate {
				release()
				release = nil
				return fmt.Errorf("SPEECH_RATE_CHANGED")
			}
			frozenRate = payment.Rate
			reservation = payment.ID
		}
		meter = &service.SpeechMeterRecord{ID: fmt.Sprintf("%s_%04d", session.ID, window), SessionID: session.ID, ReservationID: reservation, Unit: "seconds", State: "pending", ProviderRequestID: speech.ProviderRequestID(session.ID)}
		return store.PutSpeechMeter(ctx, meter)
	}
	if err = reserve(); err != nil {
		_ = emit(gin.H{"type": "error", "message": "积分不足或语音服务暂不可用"})
		return
	}
	upstream, err := adapter.StartASR(speech.WithProviderRequestID(ctx, speech.ProviderRequestID(session.ID)), session.Model, credentials, session.Language, func(segment speech.Segment) error { return emit(gin.H{"type": "transcript", "segment": segment}) })
	if err != nil {
		_ = emit(gin.H{"type": "error", "message": "云端识别启动失败"})
		return
	}
	defer upstream.Close()
	for {
		readCtx, done := context.WithTimeout(ctx, 30*time.Second)
		kind, raw, e := conn.Read(readCtx)
		done()
		if e != nil {
			return
		}
		if kind != ws.MessageText {
			return
		}
		var frame struct {
			Type     string `json:"type"`
			Sequence int    `json:"sequence"`
			PCM      string `json:"pcm"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&frame) != nil {
			return
		}
		if frame.Type == "finish" {
			finishCtx, done := context.WithTimeout(ctx, 15*time.Second)
			err = upstream.Finish(finishCtx)
			done()
			if err == nil && settle() == nil {
				state = "completed"
				_ = emit(gin.H{"type": "completed"})
			}
			return
		}
		if frame.Type != "audio" || frame.Sequence != sequence {
			return
		}
		pcm, e := base64.StdEncoding.DecodeString(frame.PCM)
		seconds, e2 := speech.PCMSeconds(pcm)
		if e != nil || e2 != nil {
			return
		}
		if meter.Quantity+seconds > 60 {
			if err = reserve(); err != nil {
				_ = emit(gin.H{"type": "error", "message": "积分不足，云端识别已停止"})
				return
			}
		}
		// 发送前持久化风险标记，避免发送成功后进程退出导致零消费回收。
		meter.SupplierState = "inflight"
		if e = store.PutSpeechMeter(ctx, meter); e != nil {
			return
		}
		if e = upstream.Send(ctx, pcm); e != nil {
			return
		}
		meter.SupplierState = "confirmed"
		sequence++
		meter.Quantity += seconds
		if e = store.PutSpeechMeter(ctx, meter); e != nil {
			return
		}
		_ = emit(gin.H{"type": "usage", "state": "estimated", "quantity": meter.Quantity, "unit": "seconds", "priceVersion": session.Model.Version})
	}
}
