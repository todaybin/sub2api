// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：在订阅业务事务内复核当前签名身份，不读取缓存或按外部标识推定归属。
package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

	control "github.com/Wei-Shaw/sub2api/internal/pkg/subscriptioncontrol"
	"github.com/google/uuid"
)

// SubscriptionControlActiveAppVerifier 仅供本地管理员维护授权时核对现有集成应用。
// 签名请求必须使用SubscriptionControlSignedAppVerifier，不能用此方法代替请求认证。
func SubscriptionControlActiveAppVerifier(ctx context.Context, tx *sql.Tx, app string) error {
	_, err := subscriptionControlCredentials(ctx, tx, app)
	return err
}

// SubscriptionControlSignedAppVerifier 固定原请求的密钥摘要和签名，供每个实际事务重新核验。
// canonical沿用既有签名网关的六行协议，由控制器从实际方法、路径及原始正文构造。
func SubscriptionControlSignedAppVerifier(apiKey, signature, canonical string) (control.AppVerifier, error) {
	signature = strings.ToLower(strings.TrimSpace(signature))
	signed, err := hex.DecodeString(signature)
	parts := strings.Split(canonical, "\n")
	if len(apiKey) < 1 || len(apiKey) > 1024 || err != nil || len(signed) != sha256.Size || len(canonical) > 2048 || len(parts) != 6 ||
		(parts[0] != "GET" && parts[0] != "PUT") || !validSubscriptionControlSignedPath(parts[0], parts[1]) ||
		!isIntegrationCredential(parts[2], 16) || len(parts[4]) < 1 || len(parts[4]) > 128 || !isIntegrationCredential(parts[5], 32) {
		return nil, control.ErrSubscriptionControlInput
	}
	for _, r := range parts[4] {
		if unicode.IsControl(r) {
			return nil, control.ErrSubscriptionControlInput
		}
	}
	timestamp, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || strconv.FormatInt(timestamp, 10) != parts[3] {
		return nil, control.ErrSubscriptionControlInput
	}
	keyDigest := sha256.Sum256([]byte(apiKey))
	return func(ctx context.Context, tx *sql.Tx, app string) error {
		if parts[2] != app || time.Since(time.Unix(timestamp, 0)).Abs() > 5*time.Minute {
			return control.ErrAccessDenied
		}
		credentials, err := subscriptionControlCredentials(ctx, tx, app)
		if err != nil {
			return err
		}
		currentKey, err := subscriptionControlSetting(ctx, tx, SettingKeyAdminAPIKey, 1024)
		if err != nil {
			return err
		}
		actual := sha256.Sum256([]byte(currentKey))
		if currentKey == "" || subtle.ConstantTimeCompare(actual[:], keyDigest[:]) != 1 {
			return control.ErrAccessDenied
		}
		mac := hmac.New(sha256.New, []byte(credentials.Secret))
		_, _ = mac.Write([]byte(canonical))
		if !hmac.Equal(mac.Sum(nil), signed) {
			return control.ErrAccessDenied
		}
		return nil
	}, nil
}

func subscriptionControlSetting(ctx context.Context, tx *sql.Tx, key string, max int) (string, error) {
	if tx == nil {
		return "", control.ErrStoreUnavailable
	}
	var raw sql.NullString
	// 无界或损坏配置不进入内存、错误或响应；共享锁保证本事务结束前不被轮换。
	err := tx.QueryRowContext(ctx, "SELECT CASE WHEN octet_length(value)<=$2 THEN value ELSE NULL END FROM settings WHERE key=$1 FOR SHARE", key, max).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", control.ErrAccessDenied
	}
	if err != nil || !raw.Valid {
		return "", control.ErrStoreUnavailable
	}
	return raw.String, nil
}

func subscriptionControlCredentials(ctx context.Context, tx *sql.Tx, app string) (*IntegrationAdminCredentials, error) {
	raw, err := subscriptionControlSetting(ctx, tx, SettingKeyIntegrationAdminCredentials, 4096)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(strings.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, control.ErrStoreUnavailable
	}
	seen := map[string]bool{}
	var c IntegrationAdminCredentials
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return nil, control.ErrStoreUnavailable
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil || string(value) == "null" {
			return nil, control.ErrStoreUnavailable
		}
		switch key {
		case "appid":
			err = json.Unmarshal(value, &c.AppID)
		case "secret":
			err = json.Unmarshal(value, &c.Secret)
		case "enabled":
			err = json.Unmarshal(value, &c.Enabled)
		default:
			return nil, control.ErrStoreUnavailable
		}
		if err != nil {
			return nil, control.ErrStoreUnavailable
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || len(seen) != 3 {
		return nil, control.ErrStoreUnavailable
	}
	var extra json.RawMessage
	if d.Decode(&extra) != io.EOF || !isIntegrationCredential(c.AppID, 16) || !isIntegrationCredential(c.Secret, 32) {
		return nil, control.ErrStoreUnavailable
	}
	if !c.Enabled || c.AppID != app {
		return nil, control.ErrAccessDenied
	}
	return &c, nil
}

func validSubscriptionControlSignedPath(method, path string) bool {
	const prefix = "/api/v1/admin/subscriptions/control-v1"
	if method == "GET" && path == prefix+"/state" {
		return true
	}
	const commands = prefix + "/commands/"
	if !strings.HasPrefix(path, commands) {
		return false
	}
	id := strings.TrimPrefix(path, commands)
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}
