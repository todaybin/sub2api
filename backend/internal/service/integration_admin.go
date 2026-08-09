package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// IntegrationAdminCredentials are deliberately separate from the global admin
// API key. The signing secret is only returned when it is generated.
type IntegrationAdminCredentials struct {
	IntegrationID string `json:"integration_id"`
	SigningSecret string `json:"signing_secret"`
	Enabled       bool   `json:"enabled"`
}

func (s *SettingService) GenerateIntegrationAdminCredentials(ctx context.Context) (*IntegrationAdminCredentials, error) {
	id, err := randomIntegrationCredential("int", 16)
	if err != nil {
		return nil, err
	}
	secret, err := randomIntegrationCredential("sec", 32)
	if err != nil {
		return nil, err
	}
	credentials := &IntegrationAdminCredentials{IntegrationID: id, SigningSecret: secret, Enabled: true}
	encoded, err := json.Marshal(credentials)
	if err != nil {
		return nil, fmt.Errorf("encode integration credentials: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyIntegrationAdminCredentials, string(encoded)); err != nil {
		return nil, fmt.Errorf("save integration credentials: %w", err)
	}
	return credentials, nil
}

func (s *SettingService) GetIntegrationAdminCredentials(ctx context.Context) (*IntegrationAdminCredentials, error) {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyIntegrationAdminCredentials)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var credentials IntegrationAdminCredentials
	if err := json.Unmarshal([]byte(raw), &credentials); err != nil {
		return nil, fmt.Errorf("decode integration credentials: %w", err)
	}
	if credentials.IntegrationID == "" || credentials.SigningSecret == "" {
		return nil, fmt.Errorf("integration credentials are incomplete")
	}
	return &credentials, nil
}

func (s *SettingService) GetIntegrationAdminCredentialsStatus(ctx context.Context) (string, bool, error) {
	credentials, err := s.GetIntegrationAdminCredentials(ctx)
	if err != nil || credentials == nil || !credentials.Enabled {
		return "", false, err
	}
	return maskIntegrationID(credentials.IntegrationID), true, nil
}

func (s *SettingService) DeleteIntegrationAdminCredentials(ctx context.Context) error {
	return s.settingRepo.Delete(ctx, SettingKeyIntegrationAdminCredentials)
}

func randomIntegrationCredential(prefix string, bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate integration credential: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(buf), nil
}

func maskIntegrationID(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:8] + "..." + value[len(value)-4:]
}
