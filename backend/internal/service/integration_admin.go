package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// IntegrationAdminCredentials must be used with the global admin API key.
// The secret is only returned when it is generated.
type IntegrationAdminCredentials struct {
	AppID   string `json:"appid"`
	Secret  string `json:"secret"`
	Enabled bool   `json:"enabled"`
}

func (s *SettingService) GenerateIntegrationAdminCredentials(ctx context.Context) (*IntegrationAdminCredentials, error) {
	id, err := randomIntegrationCredential(16)
	if err != nil {
		return nil, err
	}
	secret, err := randomIntegrationCredential(32)
	if err != nil {
		return nil, err
	}
	credentials := &IntegrationAdminCredentials{AppID: id, Secret: secret, Enabled: true}
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
	// Legacy credentials require explicit rotation through the admin UI. Reading
	// settings must never silently strip prefixes or reactivate the old secret.
	var legacy struct {
		IntegrationID json.RawMessage `json:"integration_id"`
		SigningSecret json.RawMessage `json:"signing_secret"`
	}
	if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
		return nil, fmt.Errorf("decode integration credentials: %w", err)
	}
	if legacy.IntegrationID != nil || legacy.SigningSecret != nil {
		return nil, nil
	}
	var credentials IntegrationAdminCredentials
	if err := json.Unmarshal([]byte(raw), &credentials); err != nil {
		return nil, fmt.Errorf("decode integration credentials: %w", err)
	}
	if !isIntegrationCredential(credentials.AppID, 16) || !isIntegrationCredential(credentials.Secret, 32) {
		return nil, fmt.Errorf("integration credentials have an invalid format")
	}
	return &credentials, nil
}

func (s *SettingService) GetIntegrationAdminCredentialsStatus(ctx context.Context) (string, bool, error) {
	credentials, err := s.GetIntegrationAdminCredentials(ctx)
	if err != nil || credentials == nil || !credentials.Enabled {
		return "", false, err
	}
	return maskAppID(credentials.AppID), true, nil
}

func (s *SettingService) DeleteIntegrationAdminCredentials(ctx context.Context) error {
	return s.settingRepo.Delete(ctx, SettingKeyIntegrationAdminCredentials)
}

func randomIntegrationCredential(bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate integration credential: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func isIntegrationCredential(value string, bytesLen int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == bytesLen && hex.EncodeToString(decoded) == value
}

func maskAppID(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:8] + "..." + value[len(value)-4:]
}
