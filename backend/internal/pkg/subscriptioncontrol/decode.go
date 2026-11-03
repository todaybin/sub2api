// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：严格读取固定原子命令，防止缺字段、重复字段和路径/幂等标识歧义。
package subscriptioncontrol

import (
	"bytes"
	"encoding/json"
	"io"
)

// MaxCommandBytes 限制不包含业务正文的固定命令输入大小。
const MaxCommandBytes = 4096

// DecodeCommand 要求十个字段全部出现且不重复；不接受未知字段、null或第二个JSON值。
// 路径标识和幂等键必须与规范小写命令UUID完全一致，不做修剪或隐式转换。
func DecodeCommand(reader io.Reader, pathID, idempotencyKey string) (SubscriptionControlCommand, error) {
	var command SubscriptionControlCommand
	if reader == nil {
		return command, ErrSubscriptionControlInput
	}
	body, err := io.ReadAll(io.LimitReader(reader, MaxCommandBytes+1))
	if err != nil || len(body) == 0 || len(body) > MaxCommandBytes {
		return command, ErrSubscriptionControlInput
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return command, ErrSubscriptionControlInput
	}
	seen := make(map[string]bool, 10)
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return command, ErrSubscriptionControlInput
		}
		switch name {
		case "protocol", "command_id", "actor_uid", "user_id", "group_id", "subscription_id", "expected_revision", "action", "days", "reset_period":
		default:
			return command, ErrSubscriptionControlInput
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return command, ErrSubscriptionControlInput
		}
		seen[name] = true
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != 10 {
		return command, ErrSubscriptionControlInput
	}
	if _, err = decoder.Token(); err != io.EOF {
		return command, ErrSubscriptionControlInput
	}
	if err = json.Unmarshal(body, &command); err != nil || command.Validate() != nil || pathID != command.CommandID || idempotencyKey != command.CommandID {
		return SubscriptionControlCommand{}, ErrSubscriptionControlInput
	}
	return command, nil
}
