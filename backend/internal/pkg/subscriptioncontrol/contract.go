// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：落实Gateway公开订阅原子协议的对端类型和固定摘要；不跨仓导入内部实现。
package subscriptioncontrol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// SubscriptionControlProtocol 要求对端明确声明原子命令及持久化结果语义。
const SubscriptionControlProtocol = "workmesh.subscription-control.v1"

var (
	// ErrSubscriptionControlInput 表示命令未提供完整、明确且有界的操作依据。
	ErrSubscriptionControlInput = errors.New("MODEL_SUBSCRIPTION_COMMAND_INVALID")
	// ErrSubscriptionControlReceipt 表示回执不能证明同一命令的权威结果。
	ErrSubscriptionControlReceipt = errors.New("MODEL_SUBSCRIPTION_RECEIPT_INVALID")
)

// SubscriptionControlCommand 由本地持久化协调器固定；命令标识不能在重试时重新生成。
type SubscriptionControlCommand struct {
	Protocol         string `json:"protocol"`
	CommandID        string `json:"command_id"`
	ActorUID         int64  `json:"actor_uid"`
	UserID           int64  `json:"user_id"`
	GroupID          int64  `json:"group_id"`
	SubscriptionID   int64  `json:"subscription_id"`
	ExpectedRevision string `json:"expected_revision"`
	Action           string `json:"action"`
	Days             int    `json:"days"`
	ResetPeriod      string `json:"reset_period"`
}

// Validate 拒绝跨动作参数混用、未知动作和缺失版本；零版本只用于空槽首次分配。
func (c SubscriptionControlCommand) Validate() error {
	id, e := uuid.Parse(c.CommandID)
	if e != nil || id == uuid.Nil || id.String() != c.CommandID || c.Protocol != SubscriptionControlProtocol ||
		c.ActorUID < 1 || c.UserID < 1 || c.GroupID < 1 {
		return ErrSubscriptionControlInput
	}
	revision, ok := subscriptionRevision(c.ExpectedRevision)
	if !ok || revision == math.MaxInt64 {
		return ErrSubscriptionControlInput
	}
	switch c.Action {
	case "assign":
		if c.SubscriptionID != 0 || c.Days < 1 || c.Days > 36500 || c.ResetPeriod != "" {
			return ErrSubscriptionControlInput
		}
	case "extend":
		if c.SubscriptionID < 1 || revision == 0 || c.Days == 0 || c.Days < -36500 || c.Days > 36500 || c.ResetPeriod != "" {
			return ErrSubscriptionControlInput
		}
	case "revoke", "restore":
		if c.SubscriptionID < 1 || revision == 0 || c.Days != 0 || c.ResetPeriod != "" {
			return ErrSubscriptionControlInput
		}
	case "reset_quota":
		if c.SubscriptionID < 1 || revision == 0 || c.Days != 0 ||
			c.ResetPeriod != "daily" && c.ResetPeriod != "weekly" && c.ResetPeriod != "monthly" && c.ResetPeriod != "all" {
			return ErrSubscriptionControlInput
		}
	default:
		return ErrSubscriptionControlInput
	}
	return nil
}

// Digest 固定结构生成摘要；操作者、归属、旧版本及全部动作参数均参与核验。
func (c SubscriptionControlCommand) Digest() (string, error) {
	if e := c.Validate(); e != nil {
		return "", e
	}
	body, e := json.Marshal(c)
	if e != nil {
		return "", ErrSubscriptionControlInput
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func subscriptionRevision(value string) (int64, bool) {
	n, e := strconv.ParseInt(value, 10, 64)
	return n, e == nil && n >= 0 && strconv.FormatInt(n, 10) == value
}

// ControlledSubscription 只允许有界状态与时间字段，不保存远端用户、备注、凭据或正文。
type ControlledSubscription struct {
	ID        int64      `json:"id"`
	Status    string     `json:"status"`
	StartsAt  time.Time  `json:"starts_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

// SubscriptionControlState 的版本属于用户与分组槽，撤销或删除订阅后仍须永久保留。
type SubscriptionControlState struct {
	Protocol     string                  `json:"protocol"`
	UserID       int64                   `json:"user_id"`
	GroupID      int64                   `json:"group_id"`
	Revision     string                  `json:"revision"`
	SlotStatus   string                  `json:"slot_status"`
	Subscription *ControlledSubscription `json:"subscription"`
}

// ValidateOwner 核对完整归属和显式版本，不以旧列表的更新时间推导控制版本。
func (s SubscriptionControlState) ValidateOwner(user, group int64) error {
	revision, ok := subscriptionRevision(s.Revision)
	if s.Protocol != SubscriptionControlProtocol || user < 1 || group < 1 ||
		s.UserID != user || s.GroupID != group || !ok {
		return ErrSubscriptionControlReceipt
	}
	if s.SlotStatus != "empty" && s.SlotStatus != "occupied" || (s.SlotStatus == "empty") != (s.Subscription == nil) {
		return ErrSubscriptionControlReceipt
	}
	if s.Subscription == nil {
		// 空槽仍可保留非零历史版本，避免删除再建造成版本ABA。
		return nil
	}
	v := s.Subscription
	if revision < 1 || v.ID < 1 || v.StartsAt.IsZero() || !v.ExpiresAt.After(v.StartsAt) ||
		v.UpdatedAt.IsZero() {
		return ErrSubscriptionControlReceipt
	}
	switch v.Status {
	case "active", "expired", "suspended":
		if v.RevokedAt != nil {
			return ErrSubscriptionControlReceipt
		}
	case "revoked":
		if v.RevokedAt == nil || v.RevokedAt.IsZero() {
			return ErrSubscriptionControlReceipt
		}
	default:
		return ErrSubscriptionControlReceipt
	}
	return nil
}

// SubscriptionControlReceipt 明确区分处理中、权威成功、权威拒绝及未查到命令。
type SubscriptionControlReceipt struct {
	Protocol      string                    `json:"protocol"`
	CommandID     string                    `json:"command_id"`
	RequestDigest string                    `json:"request_digest"`
	UserID        int64                     `json:"user_id"`
	GroupID       int64                     `json:"group_id"`
	Status        string                    `json:"status"`
	ErrorCode     string                    `json:"error_code"`
	State         *SubscriptionControlState `json:"state"`
	FinishedAt    *time.Time                `json:"finished_at"`
}

// ValidateFor 只接受同一命令的持久化回执；HTTP成功本身不是业务成功。
func (r SubscriptionControlReceipt) ValidateFor(c SubscriptionControlCommand, allowNotFound bool) error {
	digest, e := c.Digest()
	if e != nil {
		return e
	}
	if r.Protocol != SubscriptionControlProtocol || r.CommandID != c.CommandID || r.RequestDigest != digest ||
		r.UserID != c.UserID || r.GroupID != c.GroupID {
		return ErrSubscriptionControlReceipt
	}
	switch r.Status {
	case "pending", "not_found":
		if r.Status == "not_found" && !allowNotFound || r.State != nil || r.FinishedAt != nil || r.ErrorCode != "" {
			return ErrSubscriptionControlReceipt
		}
	case "rejected":
		if r.State != nil || r.FinishedAt == nil || r.FinishedAt.IsZero() {
			return ErrSubscriptionControlReceipt
		}
		switch r.ErrorCode {
		case "VERSION_CONFLICT", "OWNER_MISMATCH", "SUBSCRIPTION_STATE_INVALID", "GROUP_UNAVAILABLE", "SUBSCRIPTION_NOT_FOUND":
		default:
			return ErrSubscriptionControlReceipt
		}
	case "succeeded":
		if r.ErrorCode != "" || r.State == nil || r.FinishedAt == nil || r.FinishedAt.IsZero() ||
			r.State.ValidateOwner(c.UserID, c.GroupID) != nil || r.State.Subscription == nil {
			return ErrSubscriptionControlReceipt
		}
		expected, _ := subscriptionRevision(c.ExpectedRevision)
		actual, _ := subscriptionRevision(r.State.Revision)
		v := r.State.Subscription
		if actual != expected+1 || c.Action != "assign" && v.ID != c.SubscriptionID ||
			r.FinishedAt.Before(v.UpdatedAt) {
			return ErrSubscriptionControlReceipt
		}
		switch c.Action {
		case "assign":
			if v.Status != "active" {
				return ErrSubscriptionControlReceipt
			}
		case "revoke":
			if v.Status != "revoked" {
				return ErrSubscriptionControlReceipt
			}
		case "extend":
			if v.Status != "active" && v.Status != "suspended" {
				return ErrSubscriptionControlReceipt
			}
		case "restore", "reset_quota":
			if v.Status == "revoked" {
				return ErrSubscriptionControlReceipt
			}
		}
	default:
		return ErrSubscriptionControlReceipt
	}
	return nil
}
