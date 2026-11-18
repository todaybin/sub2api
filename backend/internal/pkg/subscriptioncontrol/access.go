// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：持久维护显式集成订阅范围，重放、状态和结果均在同一事务内重新授权。
package subscriptioncontrol

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ErrAccessDenied 表示缺少当前应用、管理员、用户分组或显式业务授权。
var ErrAccessDenied = errors.New("SUBSCRIPTION_CONTROL_ACCESS_DENIED")

// ErrGrantVersionConflict 表示授权版本已变更，不能覆盖当前范围。
var ErrGrantVersionConflict = errors.New("SUBSCRIPTION_CONTROL_GRANT_VERSION_CONFLICT")

// AppVerifier 在传入事务中核验当前集成配置；签名入口还必须复核原请求身份。
type AppVerifier func(context.Context, *sql.Tx, string) error

// GrantCommand 仅允许由已认证的上游本地管理员提交；外部集成不能自授。
type GrantCommand struct {
	CommandID        string `json:"command_id"`
	AppID            string `json:"app_id"`
	UserID           int64  `json:"user_id"`
	GroupID          int64  `json:"group_id"`
	OperatorID       int64  `json:"operator_id"`
	ExpectedRevision string `json:"expected_revision"`
	Enabled          bool   `json:"enabled"`
	Reason           string `json:"reason"`
}

// Grant 是授权维护当时的固定结果，版本按字符串保留完整整数精度。
type Grant struct {
	AppID     string    `json:"app_id"`
	UserID    int64     `json:"user_id"`
	GroupID   int64     `json:"group_id"`
	Revision  string    `json:"revision"`
	Enabled   bool      `json:"enabled"`
	UpdatedBy int64     `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate 拒绝不完整、非规范版本、控制字符和无界理由。
func (c GrantCommand) Validate() error {
	id, e := uuid.Parse(c.CommandID)
	n, ok := subscriptionRevision(c.ExpectedRevision)
	if e != nil || id == uuid.Nil || id.String() != c.CommandID || !applicationID.MatchString(c.AppID) ||
		c.UserID < 1 || c.GroupID < 1 || c.OperatorID < 1 || !ok || n == math.MaxInt64 ||
		!utf8.ValidString(c.Reason) || c.Reason != strings.TrimSpace(c.Reason) || len(c.Reason) < 1 || len(c.Reason) > 2000 || utf8.RuneCountInString(c.Reason) > 500 {
		return ErrSubscriptionControlInput
	}
	for _, r := range c.Reason {
		if unicode.IsControl(r) {
			return ErrSubscriptionControlInput
		}
	}
	return nil
}

// SaveGrant 原子提交范围、版本、理由审计和永久幂等结果；不按外部标识自动授予。
// verifyApp 对启用操作核验当前应用；停用失效资源仍可进行，不影响旧业务接口。
func (s *Store) SaveGrant(ctx context.Context, c GrantCommand, verifyApp AppVerifier) (Grant, error) {
	if s == nil || s.db == nil || verifyApp == nil || c.Validate() != nil {
		return Grant{}, ErrSubscriptionControlInput
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Grant{}, fmt.Errorf("开启订阅范围授权事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "subscription-control-grant-command:"+c.CommandID); err != nil {
		return Grant{}, err
	}
	// 先核验管理员，历史幂等结果也不能被已停用管理员取回。
	if err = authorizeLocalAdmin(ctx, tx, c.OperatorID); err != nil {
		return Grant{}, err
	}
	raw, _ := json.Marshal(c)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	var oldDigest string
	var snapshot []byte
	err = tx.QueryRowContext(ctx, "SELECT request_digest,snapshot FROM subscription_control_grant_events WHERE command_id=$1", c.CommandID).Scan(&oldDigest, &snapshot)
	if err == nil {
		if oldDigest != digest {
			return Grant{}, ErrCommandConflict
		}
		var result Grant
		if json.Unmarshal(snapshot, &result) != nil || !validGrantSnapshot(result, c) {
			return Grant{}, ErrStoreUnavailable
		}
		if err = tx.Commit(); err != nil {
			return Grant{}, fmt.Errorf("提交历史范围读取失败: %w", err)
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Grant{}, fmt.Errorf("读取原授权结果失败: %w", err)
	}
	if c.Enabled {
		if err = verifyApp(ctx, tx, c.AppID); err != nil {
			return Grant{}, err
		}
		if err = authorizeTarget(ctx, tx, c.UserID, c.GroupID, true); err != nil {
			return Grant{}, err
		}
	}
	// 同一授权不存在时也串行化，避免并发首次写入绕过旧版本校验。
	ownerKey := fmt.Sprintf("subscription-control-grant:%s:%d:%d", c.AppID, c.UserID, c.GroupID)
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", ownerKey); err != nil {
		return Grant{}, err
	}
	var revision string
	err = tx.QueryRowContext(ctx, "SELECT revision::text FROM subscription_control_grants WHERE app_id=$1 AND user_id=$2 AND group_id=$3 FOR UPDATE", c.AppID, c.UserID, c.GroupID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		if c.ExpectedRevision != "0" {
			return Grant{}, ErrGrantVersionConflict
		}
		if !c.Enabled {
			return Grant{}, ErrAccessDenied
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO subscription_control_grants(app_id,user_id,group_id,enabled,revision,updated_by) VALUES($1,$2,$3,true,1,$4)", c.AppID, c.UserID, c.GroupID, c.OperatorID)
	} else if err == nil {
		if revision != c.ExpectedRevision {
			return Grant{}, ErrGrantVersionConflict
		}
		_, err = tx.ExecContext(ctx, "UPDATE subscription_control_grants SET enabled=$4,revision=revision+1,updated_by=$5,updated_at=clock_timestamp() WHERE app_id=$1 AND user_id=$2 AND group_id=$3", c.AppID, c.UserID, c.GroupID, c.Enabled, c.OperatorID)
	}
	if err != nil {
		return Grant{}, fmt.Errorf("保存订阅范围授权失败: %w", err)
	}
	result := Grant{AppID: c.AppID, UserID: c.UserID, GroupID: c.GroupID}
	err = tx.QueryRowContext(ctx, "SELECT revision::text,enabled,updated_by,updated_at FROM subscription_control_grants WHERE app_id=$1 AND user_id=$2 AND group_id=$3", c.AppID, c.UserID, c.GroupID).Scan(&result.Revision, &result.Enabled, &result.UpdatedBy, &result.UpdatedAt)
	if err != nil {
		return Grant{}, err
	}
	snapshot, err = json.Marshal(result)
	if err != nil {
		return Grant{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO subscription_control_grant_events
 (command_id,app_id,user_id,group_id,revision,operator_id,enabled,reason,request_digest,snapshot)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, c.CommandID, c.AppID, c.UserID, c.GroupID, result.Revision, c.OperatorID, c.Enabled, c.Reason, digest, string(snapshot))
	if err != nil {
		return Grant{}, fmt.Errorf("保存订阅范围授权审计失败: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return Grant{}, fmt.Errorf("提交订阅范围授权失败，须原键核查: %w", err)
	}
	return result, nil
}

// GrantAuthorizer 返回事务内范围核验；调用方必须提供原签名请求的当前身份核验器。
// 所有范围依据持有共享锁至提交，范围撤销或组权限移除后新请求即时拒绝。
func (s *Store) GrantAuthorizer(app string, adminID int64, verifyApp AppVerifier) Authorize {
	return func(ctx context.Context, tx *sql.Tx, user, group int64) error {
		if s == nil || s.db == nil || tx == nil || verifyApp == nil || !applicationID.MatchString(app) || adminID < 1 || user < 1 || group < 1 {
			return ErrAccessDenied
		}
		if err := verifyApp(ctx, tx, app); err != nil {
			return err
		}
		if err := authorizeLocalAdmin(ctx, tx, adminID); err != nil {
			return err
		}
		if err := authorizeTarget(ctx, tx, user, group, false); err != nil {
			return err
		}
		var enabled bool
		err := tx.QueryRowContext(ctx, "SELECT enabled FROM subscription_control_grants WHERE app_id=$1 AND user_id=$2 AND group_id=$3 FOR SHARE", app, user, group).Scan(&enabled)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !enabled {
			return ErrAccessDenied
		}
		if err != nil {
			return fmt.Errorf("核验订阅范围授权失败: %w", err)
		}
		return nil
	}
}

func authorizeLocalAdmin(ctx context.Context, tx *sql.Tx, id int64) error {
	var actual int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM users WHERE id=$1 AND role='admin' AND status='active' AND deleted_at IS NULL FOR SHARE", id).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAccessDenied
	}
	if err != nil {
		return fmt.Errorf("核验本地范围管理员失败: %w", err)
	}
	return nil
}

func authorizeTarget(ctx context.Context, tx *sql.Tx, user, group int64, active bool) error {
	var id int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM users WHERE id=$1 AND role='user' AND deleted_at IS NULL AND (NOT $2::boolean OR status='active') FOR SHARE", user, active).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAccessDenied
	}
	if err != nil {
		return fmt.Errorf("核验计费用户范围失败: %w", err)
	}
	err = tx.QueryRowContext(ctx, "SELECT id FROM groups WHERE id=$1 AND is_exclusive=true AND subscription_type='subscription' AND deleted_at IS NULL AND (NOT $2::boolean OR status='active') FOR SHARE", group, active).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAccessDenied
	}
	if err != nil {
		return fmt.Errorf("核验订阅分组范围失败: %w", err)
	}
	err = tx.QueryRowContext(ctx, "SELECT user_id FROM user_allowed_groups WHERE user_id=$1 AND group_id=$2 FOR SHARE", user, group).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAccessDenied
	}
	if err != nil {
		return fmt.Errorf("核验用户分组授权失败: %w", err)
	}
	return nil
}

func validGrantSnapshot(g Grant, c GrantCommand) bool {
	expected, ok := subscriptionRevision(c.ExpectedRevision)
	actual, valid := subscriptionRevision(g.Revision)
	return ok && valid && actual == expected+1 && g.AppID == c.AppID && g.UserID == c.UserID &&
		g.GroupID == c.GroupID && g.Enabled == c.Enabled && g.UpdatedBy == c.OperatorID && !g.UpdatedAt.IsZero()
}
