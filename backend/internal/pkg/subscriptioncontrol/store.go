// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：在同一数据库事务内协调持久控制版本、业务修改、永久回执和缓存恢复依据。
package subscriptioncontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrStoreUnavailable 表示依赖缺失或持久事实不完整，不能推定业务成功。
	ErrStoreUnavailable = errors.New("SUBSCRIPTION_CONTROL_UNAVAILABLE")
	// ErrCommandConflict 表示同一集成应用的原命令标识对应另一组固定参数。
	ErrCommandConflict = errors.New("SUBSCRIPTION_CONTROL_COMMAND_CONFLICT")
)
var applicationID = regexp.MustCompile("^[A-Za-z0-9_-]{1,128}$")

// Authorize 必须在传入事务内按真实集成身份核验用户和分组；重放与查询也会重新授权。
type Authorize func(context.Context, *sql.Tx, int64, int64) error

// Transition 只允许使用传入事务修改原订阅，不能调用外部服务或提交独立事务。
// 返回固定业务拒绝码时会回滚整个业务savepoint；成功必须恰好推进一次控制版本。
type Transition func(context.Context, *sql.Tx, SubscriptionControlCommand) (string, error)

// Store 复用调用方现有连接池，不创建服务、连接池或后台进程。
type Store struct{ db *sql.DB }

// NewStore 将持久协调器绑定到既有数据库连接池。
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// State 在同一事务快照中授权并读取版本和当前订阅，不用更新时间替代持久版本。
func (s *Store) State(ctx context.Context, app string, user, group int64, authorize Authorize) (SubscriptionControlState, error) {
	if s == nil || s.db == nil || authorize == nil || !applicationID.MatchString(app) || user < 1 || group < 1 {
		return SubscriptionControlState{}, ErrSubscriptionControlInput
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return SubscriptionControlState{}, fmt.Errorf("读取订阅控制事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = authorize(ctx, tx, user, group); err != nil {
		return SubscriptionControlState{}, err
	}
	state, err := stateInTransaction(ctx, tx, user, group)
	if err != nil {
		return SubscriptionControlState{}, err
	}
	if err = tx.Commit(); err != nil {
		return SubscriptionControlState{}, fmt.Errorf("提交订阅控制读取失败: %w", err)
	}
	return state, nil
}

// Apply 按固定原键原子执行；只对数据库明确已回滚的死锁或串行化失败重试，最多三次。
func (s *Store) Apply(ctx context.Context, app string, command SubscriptionControlCommand, authorize Authorize, transition Transition) (SubscriptionControlReceipt, error) {
	if s == nil || s.db == nil || authorize == nil || transition == nil || !applicationID.MatchString(app) || command.Validate() != nil {
		return SubscriptionControlReceipt{}, ErrSubscriptionControlInput
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		result, err := s.applyOnce(ctx, app, command, authorize, transition)
		if err == nil || !rollbackRetryable(err) || attempt == 2 {
			return result, err
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return SubscriptionControlReceipt{}, ctx.Err()
		case <-timer.C:
		}
	}
	return SubscriptionControlReceipt{}, ErrStoreUnavailable
}

func (s *Store) applyOnce(ctx context.Context, app string, c SubscriptionControlCommand, authorize Authorize, transition Transition) (SubscriptionControlReceipt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SubscriptionControlReceipt{}, fmt.Errorf("开启订阅命令事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// 哈希仅用于互斥，同键数据仍由完整应用标识和UUID主键隔离。
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "subscription-control-command:"+app+":"+c.CommandID); err != nil {
		return SubscriptionControlReceipt{}, err
	}
	if err = authorize(ctx, tx, c.UserID, c.GroupID); err != nil {
		return SubscriptionControlReceipt{}, err
	}
	old, receipt, found, err := loadReceipt(ctx, tx, app, c.CommandID)
	if err != nil {
		return SubscriptionControlReceipt{}, err
	}
	digest, _ := c.Digest()
	if found {
		oldDigest, _ := old.Digest()
		if oldDigest != digest {
			return SubscriptionControlReceipt{}, ErrCommandConflict
		}
		if err = tx.Commit(); err != nil {
			return SubscriptionControlReceipt{}, fmt.Errorf("读取原订阅回执提交失败: %w", err)
		}
		return receipt, nil
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO subscription_control_slots(user_id,group_id) VALUES($1,$2) ON CONFLICT(user_id,group_id) DO NOTHING", c.UserID, c.GroupID); err != nil {
		return SubscriptionControlReceipt{}, err
	}
	var revision string
	if err = tx.QueryRowContext(ctx, "SELECT revision::text FROM subscription_control_slots WHERE user_id=$1 AND group_id=$2 FOR UPDATE", c.UserID, c.GroupID).Scan(&revision); err != nil {
		return SubscriptionControlReceipt{}, err
	}
	state, err := stateInTransaction(ctx, tx, c.UserID, c.GroupID)
	if err != nil {
		return SubscriptionControlReceipt{}, err
	}
	code := ""
	if revision != c.ExpectedRevision {
		code = "VERSION_CONFLICT"
	} else if c.Action == "assign" {
		if state.Subscription != nil {
			code = "SUBSCRIPTION_STATE_INVALID"
		}
	} else if state.Subscription == nil {
		code = "SUBSCRIPTION_NOT_FOUND"
	} else if state.Subscription.ID != c.SubscriptionID {
		code = "OWNER_MISMATCH"
	} else if (c.Action == "restore") != (state.Subscription.Status == "revoked") {
		code = "SUBSCRIPTION_STATE_INVALID"
	}
	if code == "" {
		if _, err = tx.ExecContext(ctx, "SAVEPOINT subscription_control_mutation"); err != nil {
			return SubscriptionControlReceipt{}, err
		}
		code, err = transition(ctx, tx, c)
		if err != nil {
			return SubscriptionControlReceipt{}, fmt.Errorf("处理订阅命令事务失败: %w", err)
		}
		if code != "" {
			if !rejectionCode(code) {
				return SubscriptionControlReceipt{}, ErrSubscriptionControlReceipt
			}
			if _, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT subscription_control_mutation"); err != nil {
				return SubscriptionControlReceipt{}, err
			}
		}
		if _, err = tx.ExecContext(ctx, "RELEASE SAVEPOINT subscription_control_mutation"); err != nil {
			return SubscriptionControlReceipt{}, err
		}
	}
	result := SubscriptionControlReceipt{Protocol: SubscriptionControlProtocol, CommandID: c.CommandID, RequestDigest: digest, UserID: c.UserID, GroupID: c.GroupID, Status: "rejected", ErrorCode: code}
	if code == "" {
		after, err := stateInTransaction(ctx, tx, c.UserID, c.GroupID)
		if err != nil {
			return SubscriptionControlReceipt{}, err
		}
		result.Status, result.State = "succeeded", &after
	}
	var finished time.Time
	if err = tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&finished); err != nil {
		return SubscriptionControlReceipt{}, err
	}
	result.FinishedAt = &finished
	if err = result.ValidateFor(c, false); err != nil {
		return SubscriptionControlReceipt{}, err
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > 8192 {
		return SubscriptionControlReceipt{}, ErrSubscriptionControlReceipt
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO subscription_control_commands
 (app_id,command_id,actor_uid,user_id,group_id,subscription_id,expected_revision,action,days,reset_period,request_digest,status,receipt,finished_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		app, c.CommandID, c.ActorUID, c.UserID, c.GroupID, c.SubscriptionID, c.ExpectedRevision, c.Action, c.Days, c.ResetPeriod, digest, result.Status, string(raw), finished)
	if err != nil {
		return SubscriptionControlReceipt{}, fmt.Errorf("保存订阅永久回执失败: %w", err)
	}
	if result.Status == "succeeded" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO subscription_control_cache_jobs(app_id,command_id) VALUES($1,$2)", app, c.CommandID); err != nil {
			return SubscriptionControlReceipt{}, fmt.Errorf("保存订阅缓存恢复依据失败: %w", err)
		}
	}
	// 提交错误可能代表回执已提交；不得在无法证明回滚时再执行业务。
	if err = tx.Commit(); err != nil {
		return SubscriptionControlReceipt{}, fmt.Errorf("提交订阅命令事务失败: %w", err)
	}
	return result, nil
}

// Result 只按原命令与摘要读取永久事实，不执行命令、不自动创建或重发。
func (s *Store) Result(ctx context.Context, app, id string, user, group int64, digest string, authorize Authorize) (SubscriptionControlReceipt, error) {
	parsed, parseErr := uuid.Parse(id)
	if s == nil || s.db == nil || authorize == nil || !applicationID.MatchString(app) || parseErr != nil || parsed == uuid.Nil || parsed.String() != id || user < 1 || group < 1 || !digestPattern.MatchString(digest) {
		return SubscriptionControlReceipt{}, ErrSubscriptionControlInput
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SubscriptionControlReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = authorize(ctx, tx, user, group); err != nil {
		return SubscriptionControlReceipt{}, err
	}
	old, result, found, err := loadReceipt(ctx, tx, app, id)
	if err != nil {
		return SubscriptionControlReceipt{}, err
	}
	if found {
		actual, _ := old.Digest()
		if old.UserID != user || old.GroupID != group || actual != digest {
			return SubscriptionControlReceipt{}, ErrCommandConflict
		}
	} else {
		result = SubscriptionControlReceipt{Protocol: SubscriptionControlProtocol, CommandID: id, RequestDigest: digest, UserID: user, GroupID: group, Status: "not_found"}
	}
	if err = tx.Commit(); err != nil {
		return SubscriptionControlReceipt{}, err
	}
	return result, nil
}

var digestPattern = regexp.MustCompile("^[0-9a-f]{64}$")

func loadReceipt(ctx context.Context, tx *sql.Tx, app, id string) (SubscriptionControlCommand, SubscriptionControlReceipt, bool, error) {
	command := SubscriptionControlCommand{Protocol: SubscriptionControlProtocol, CommandID: id}
	var raw []byte
	var storedDigest string
	err := tx.QueryRowContext(ctx, `SELECT actor_uid,user_id,group_id,subscription_id,expected_revision::text,action,days,reset_period,request_digest,receipt
 FROM subscription_control_commands WHERE app_id=$1 AND command_id=$2`, app, id).
		Scan(&command.ActorUID, &command.UserID, &command.GroupID, &command.SubscriptionID, &command.ExpectedRevision, &command.Action, &command.Days, &command.ResetPeriod, &storedDigest, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return command, SubscriptionControlReceipt{}, false, nil
	}
	if err != nil {
		return command, SubscriptionControlReceipt{}, false, err
	}
	digest, e := command.Digest()
	var result SubscriptionControlReceipt
	if e != nil || digest != storedDigest || len(raw) > 8192 || json.Unmarshal(raw, &result) != nil || result.ValidateFor(command, false) != nil {
		return command, SubscriptionControlReceipt{}, false, ErrSubscriptionControlReceipt
	}
	return command, result, true, nil
}

func stateInTransaction(ctx context.Context, tx *sql.Tx, user, group int64) (SubscriptionControlState, error) {
	state := SubscriptionControlState{Protocol: SubscriptionControlProtocol, UserID: user, GroupID: group, SlotStatus: "empty"}
	var current, id sql.NullInt64
	var status sql.NullString
	var start, end, updated, deleted sql.NullTime
	var now time.Time
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(s.revision,0)::text,s.subscription_id,u.id,u.status,u.starts_at,u.expires_at,u.updated_at,u.deleted_at,clock_timestamp()
 FROM (VALUES($1::bigint,$2::bigint)) AS owner(user_id,group_id)
 LEFT JOIN subscription_control_slots s ON s.user_id=owner.user_id AND s.group_id=owner.group_id
 LEFT JOIN user_subscriptions u ON u.id=s.subscription_id AND u.user_id=owner.user_id AND u.group_id=owner.group_id`, user, group).
		Scan(&state.Revision, &current, &id, &status, &start, &end, &updated, &deleted, &now)
	if err != nil {
		return state, fmt.Errorf("读取订阅控制快照失败: %w", err)
	}
	if current.Valid != id.Valid {
		return state, ErrStoreUnavailable
	}
	if id.Valid {
		if !start.Valid || !end.Valid || !updated.Valid || !status.Valid {
			return state, ErrStoreUnavailable
		}
		state.SlotStatus = "occupied"
		state.Subscription = &ControlledSubscription{ID: id.Int64, Status: status.String, StartsAt: start.Time, ExpiresAt: end.Time, UpdatedAt: updated.Time}
		if deleted.Valid {
			state.Subscription.Status = "revoked"
			state.Subscription.RevokedAt = &deleted.Time
		} else if status.String == "active" && !end.Time.After(now) {
			state.Subscription.Status = "expired"
		}
	}
	if err = state.ValidateOwner(user, group); err != nil {
		return SubscriptionControlState{}, err
	}
	return state, nil
}

func rejectionCode(code string) bool {
	switch code {
	case "VERSION_CONFLICT", "OWNER_MISMATCH", "SUBSCRIPTION_STATE_INVALID", "GROUP_UNAVAILABLE", "SUBSCRIPTION_NOT_FOUND":
		return true
	}
	return false
}

func rollbackRetryable(err error) bool {
	var state interface{ SQLState() string }
	if !errors.As(err, &state) {
		return false
	}
	return state.SQLState() == "40P01" || state.SQLState() == "40001"
}
