package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

const idempotencyEncryptedResponsePrefix = "enc:v1:"

func newIdempotencyResponseCipher(secret string) cipher.AEAD {
	if strings.TrimSpace(secret) == "" {
		return nil
	}
	// Derive a separate key; JWT signing and response encryption never share key material.
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("sub2api/idempotency-response/v1"))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return nil
	}
	gcm, _ := cipher.NewGCM(block)
	return gcm
}

func (c *IdempotencyCoordinator) marshalReplayResponse(data any, sensitive bool, fingerprint string) (string, error) {
	if !sensitive {
		return c.marshalStoredResponse(data)
	}
	if c.responseCipher == nil {
		return "", fmt.Errorf("response encryption unavailable")
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, c.responseCipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := c.responseCipher.Seal(nonce, nonce, raw, []byte(fingerprint))
	stored := idempotencyEncryptedResponsePrefix + base64.RawStdEncoding.EncodeToString(sealed)
	if c.cfg.MaxStoredResponseLen > 0 && len(stored) > c.cfg.MaxStoredResponseLen {
		return "", fmt.Errorf("encrypted response exceeds storage limit")
	}
	return stored, nil
}

func (c *IdempotencyCoordinator) decodeReplayResponse(stored *string, sensitive bool, fingerprint string) (any, error) {
	if !sensitive {
		return c.decodeStoredResponse(stored)
	}
	if c.responseCipher == nil || stored == nil || !strings.HasPrefix(*stored, idempotencyEncryptedResponsePrefix) {
		return nil, fmt.Errorf("encrypted response unavailable")
	}
	sealed, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(*stored, idempotencyEncryptedResponsePrefix))
	if err != nil || len(sealed) < c.responseCipher.NonceSize()+c.responseCipher.Overhead() {
		return nil, fmt.Errorf("invalid encrypted response")
	}
	nonce := sealed[:c.responseCipher.NonceSize()]
	raw, err := c.responseCipher.Open(nil, nonce, sealed[c.responseCipher.NonceSize():], []byte(fingerprint))
	if err != nil {
		return nil, fmt.Errorf("response decryption failed")
	}
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("invalid decrypted response")
	}
	return data, nil
}
