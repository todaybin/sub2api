// Copyright (c) 2026 WorkMesh. 保留所有权利。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service/speech"
	"time"
)

const speechSettingsKey = "workmesh_speech_models_v1"

type SpeechSessionRecord struct {
	ID                           string `json:"id"`
	APIKeyID, UserID, GroupID    int64
	RequestID, TicketHash, State string
	Language                     string       `json:"language"`
	Model                        speech.Model `json:"model"`
	CreatedAt, ExpiresAt         time.Time
}
type SpeechMeterRecord struct {
	ID                string               `json:"id"`
	SessionID         string               `json:"sessionId"`
	ReservationID     string               `json:"-"`
	Quantity          float64              `json:"quantity"`
	Points            float64              `json:"points"`
	Unit              string               `json:"unit"`
	State             string               `json:"state"`
	SupplierState     string               `json:"supplierState"`
	ProviderRequestID string               `json:"providerRequestId"`
	Command           *UsageBillingCommand `json:"-"`
}
type SpeechStore interface {
	CreateSpeechSession(context.Context, *SpeechSessionRecord) error
	RedeemSpeechSession(context.Context, string) (*SpeechSessionRecord, error)
	GetSpeechSession(context.Context, string, int64) (*SpeechSessionRecord, error)
	EndSpeechSession(context.Context, string, string) error
	PutSpeechMeter(context.Context, *SpeechMeterRecord) error
	ListSpeechMeters(context.Context, string) ([]SpeechMeterRecord, error)
	RecoverSpeechBilling(context.Context) error
	CompareSpeechModels(context.Context, string, string) error
	ListUserSpeech(context.Context, int64, string) ([]map[string]any, error)
}

func (s *SettingService) SpeechModels(ctx context.Context) ([]speech.Model, error) {
	if s == nil || s.settingRepo == nil {
		return nil, speech.ErrUnavailable
	}
	raw, err := s.settingRepo.GetValue(ctx, speechSettingsKey)
	if errors.Is(err, ErrSettingNotFound) {
		return []speech.Model{}, nil
	}
	if err != nil {
		return nil, err
	}
	var models []speech.Model
	if json.Unmarshal([]byte(raw), &models) != nil || len(models) > 100 {
		return nil, fmt.Errorf("SPEECH_CONFIG_INVALID")
	}
	return models, nil
}
func (s *SettingService) SetSpeechModels(ctx context.Context, models []speech.Model) error {
	if len(models) > 100 {
		return fmt.Errorf("SPEECH_CONFIG_INVALID")
	}
	seen := map[string]bool{}
	for _, m := range models {
		if err := m.Validate(); err != nil {
			return err
		}
		if seen[m.ID] {
			return fmt.Errorf("语音服务重复")
		}
		seen[m.ID] = true
	}
	raw, err := json.Marshal(models)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, speechSettingsKey, string(raw))
}
func (s *OpenAIGatewayService) SpeechStore() (SpeechStore, error) {
	store, ok := s.usageBillingRepo.(SpeechStore)
	if !ok {
		return nil, speech.ErrUnavailable
	}
	return store, nil
}
func (s *OpenAIGatewayService) SpeechModels(ctx context.Context, key *APIKey) ([]speech.Model, error) {
	all, err := s.settingService.SpeechModels(ctx)
	if err != nil {
		return nil, err
	}
	result := []speech.Model{}
	if key == nil || key.Group == nil || !key.Group.AllowPointsBilling {
		return result, nil
	}
	for _, m := range all {
		if m.Allows(key.Group.ID) {
			result = append(result, m)
		}
	}
	return result, nil
}
func (s *OpenAIGatewayService) SpeechAdapter(ctx context.Context, m speech.Model) (speech.Adapter, speech.Credentials, error) {
	account, err := s.accountRepo.GetByID(ctx, m.AccountID)
	if err != nil || account == nil || account.Status != "active" {
		return nil, speech.Credentials{}, speech.ErrUnavailable
	}
	read := func(key string) string { v, _ := account.Credentials[key].(string); return v }
	creds := speech.Credentials{APIKey: read("api_key"), AppKey: read("app_key"), AccessKeyID: read("access_key_id"), AccessKeySecret: read("access_key_secret"), WorkspaceID: read("workspace_id"), MaxConcurrency: account.Concurrency}
	switch m.Provider {
	case "aliyun":
		return speech.Aliyun{}, creds, nil
	case "volcengine":
		return speech.Volcengine{}, creds, nil
	}
	return nil, creds, speech.ErrUnavailable
}
func SpeechConfigVersion(models []speech.Model) string {
	raw, _ := json.Marshal(models)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (s *OpenAIGatewayService) SetSpeechModels(ctx context.Context, m []speech.Model, expected string) error {
	old, e := s.settingService.SpeechModels(ctx)
	if e != nil {
		return e
	}
	if SpeechConfigVersion(old) != expected {
		return fmt.Errorf("SPEECH_CONFIG_CONFLICT")
	}
	seen := map[string]bool{}
	if len(m) > 100 {
		return fmt.Errorf("SPEECH_CONFIG_INVALID")
	}
	for _, v := range m {
		if e = v.Validate(); e != nil {
			return e
		}
		if seen[v.ID] {
			return fmt.Errorf("SPEECH_CONFIG_INVALID")
		}
		seen[v.ID] = true
		if _, e = s.accountRepo.GetByID(ctx, v.AccountID); e != nil {
			return fmt.Errorf("SPEECH_ACCOUNT_INVALID")
		}
		if s.settingService.defaultSubGroupReader == nil {
			return fmt.Errorf("SPEECH_GROUP_UNAVAILABLE")
		}
		for _, group := range v.Groups {
			g, e := s.settingService.defaultSubGroupReader.GetByID(ctx, group)
			if e != nil || g == nil || !g.AllowPointsBilling {
				return fmt.Errorf("SPEECH_GROUP_INVALID")
			}
		}
		for _, o := range old {
			if v.ID == o.ID && v.Version == o.Version {
				a, b := v, o
				a.Enabled = false
				b.Enabled = false
				a.Groups = nil
				b.Groups = nil
				a.Label = ""
				b.Label = ""
				x, _ := json.Marshal(a)
				y, _ := json.Marshal(b)
				if string(x) != string(y) {
					return fmt.Errorf("SPEECH_PRICE_VERSION_REQUIRED")
				}
			}
		}
	}
	before, e := s.settingService.settingRepo.GetValue(ctx, speechSettingsKey)
	if errors.Is(e, ErrSettingNotFound) {
		before = ""
	} else if e != nil {
		return e
	}
	// 校验实际 CAS 前像，防止校验期间另一管理员已修改配置。
	var current []speech.Model
	if before == "" {
		current = []speech.Model{}
	} else if json.Unmarshal([]byte(before), &current) != nil {
		return fmt.Errorf("SPEECH_CONFIG_INVALID")
	}
	if SpeechConfigVersion(current) != expected {
		return fmt.Errorf("SPEECH_CONFIG_CONFLICT")
	}
	body, _ := json.Marshal(m)
	store, e := s.SpeechStore()
	if e != nil {
		return e
	}
	return store.CompareSpeechModels(ctx, before, string(body))
}
func (s *OpenAIGatewayService) AllSpeechModels(ctx context.Context) ([]speech.Model, error) {
	return s.settingService.SpeechModels(ctx)
}

// SpeechSettle 同步复用原积分仓储；先保存可恢复命令，再进入已有幂等账务事务。
func (s *OpenAIGatewayService) SpeechSettle(ctx context.Context, meter *SpeechMeterRecord, cmd *UsageBillingCommand) error {
	store, err := s.SpeechStore()
	if err != nil {
		return err
	}
	meter.Command = cmd
	meter.State = "ready"
	if err = store.PutSpeechMeter(ctx, meter); err != nil {
		return err
	}
	_, err = s.usageBillingRepo.Apply(ctx, cmd)
	if err != nil {
		return err
	}
	meter.State = "settled"
	return store.PutSpeechMeter(ctx, meter)
}
