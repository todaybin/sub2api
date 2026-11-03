// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：使用Gateway协议固定向量检验跨仓摘要及有界严格解析，不连接数据库或启动进程。
package subscriptioncontrol_test

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	control "github.com/Wei-Shaw/sub2api/internal/pkg/subscriptioncontrol"
)

const vector = `{"protocol":"workmesh.subscription-control.v1","command_id":"bbcb0f18-b6da-4a38-809a-123456789012","actor_uid":9,"user_id":101,"group_id":4,"subscription_id":31,"expected_revision":"9007199254740993","action":"extend","days":30,"reset_period":""}`
const commandID = "bbcb0f18-b6da-4a38-809a-123456789012"

func TestGatewayCommandVector(t *testing.T) {
	command, err := control.DecodeCommand(strings.NewReader(vector), commandID, commandID)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(command)
	if err != nil || string(canonical) != vector {
		t.Fatalf("与Gateway固定字段顺序不一致：%s %v", canonical, err)
	}
	digest, err := command.Digest()
	if err != nil || digest != "64625b3a03864e26b9edaa3cd9e9fbc86bfbd657526f3597d374d131af74fbb7" {
		t.Fatalf("固定SHA256向量不匹配：%s %v", digest, err)
	}
	if command.ExpectedRevision != "9007199254740993" {
		t.Fatal("精确版本丢失")
	}
	// 参数顺序和空白不影响固定语义摘要，原始输入字节仍由既有签名中间件验证。
	changed := strings.Replace(vector, `"action":"extend","days":30`, `"days":30, "action":"extend"`, 1)
	reordered, err := control.DecodeCommand(strings.NewReader(changed), commandID, commandID)
	actual, digestErr := reordered.Digest()
	if err != nil || digestErr != nil || actual != digest {
		t.Fatal("等价字段顺序改变了命令摘要", err, digestErr)
	}
}

func TestCommandRejectsAmbiguousInput(t *testing.T) {
	cases := map[string]string{
		"缺零值字段":   strings.Replace(vector, `,"reset_period":""`, "", 1),
		"重复同值":    strings.Replace(vector, `"days":30`, `"days":30,"days":30`, 1),
		"重复不同值":   strings.Replace(vector, `"days":30`, `"days":30,"days":-1`, 1),
		"转义重复字段":  strings.Replace(vector, `"days":30`, `"days":30,"\u0064ays":-1`, 1),
		"未知字段":    strings.Replace(vector, `"days":30`, `"days":30,"site_id":7`, 1),
		"大小写别名":   strings.Replace(vector, `"days":30`, `"Days":30`, 1),
		"空值":      strings.Replace(vector, `"reset_period":""`, `"reset_period":null`, 1),
		"数字版本":    strings.Replace(vector, `"9007199254740993"`, `9007199254740993`, 1),
		"小数天数":    strings.Replace(vector, `"days":30`, `"days":30.0`, 1),
		"指数天数":    strings.Replace(vector, `"days":30`, `"days":3e1`, 1),
		"整数溢出":    strings.Replace(vector, `"actor_uid":9`, `"actor_uid":9223372036854775808`, 1),
		"非规范版本":   strings.Replace(vector, `"9007199254740993"`, `"09007199254740993"`, 1),
		"耗尽版本":    strings.Replace(vector, `"9007199254740993"`, `"9223372036854775807"`, 1),
		"零天数":     strings.Replace(vector, `"days":30`, `"days":0`, 1),
		"跨动作参数":   strings.Replace(vector, `"reset_period":""`, `"reset_period":"daily"`, 1),
		"非规范UUID": strings.ReplaceAll(vector, "bbcb0f18", "BBCB0F18"),
		"第二个值":    vector + "{}",
		"尾随垃圾":    vector + "x",
		"数组":      "[" + vector + "]",
		"空对象":     "{}",
		"空输入":     "",
		"超限输入":    vector + strings.Repeat(" ", control.MaxCommandBytes),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := control.DecodeCommand(strings.NewReader(body), commandID, commandID); !errors.Is(err, control.ErrSubscriptionControlInput) {
				t.Fatal("歧义输入未被拒绝", err)
			}
		})
	}
	for _, identities := range [][2]string{{commandID, ""}, {"", commandID}, {commandID, commandID + " "}, {"00000000-0000-0000-0000-000000000000", commandID}} {
		if _, err := control.DecodeCommand(strings.NewReader(vector), identities[0], identities[1]); err == nil {
			t.Fatal("路径与幂等键不一致未被拒绝")
		}
	}
	if _, err := control.DecodeCommand(nil, commandID, commandID); err == nil {
		t.Fatal("空reader未被拒绝")
	}
	if _, err := control.DecodeCommand(failingReader{}, commandID, commandID); err == nil {
		t.Fatal("读取错误未被拒绝")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestEveryCommandActionMatchesContract(t *testing.T) {
	base, err := control.DecodeCommand(strings.NewReader(vector), commandID, commandID)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"assign", "extend", "revoke", "restore", "reset_quota"} {
		command := base
		command.Action, command.Days = action, 0
		switch action {
		case "assign":
			command.SubscriptionID, command.ExpectedRevision, command.Days = 0, "0", 36500
		case "extend":
			command.Days = -36500
		case "reset_quota":
			command.ResetPeriod = "all"
		}
		body, err := json.Marshal(command)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = control.DecodeCommand(strings.NewReader(string(body)), commandID, commandID); err != nil {
			t.Fatal("有效动作被拒绝", action, err)
		}
	}
}
