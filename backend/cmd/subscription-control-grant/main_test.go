// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：验证本地授权命令在缺少显式依据时不读取配置、不打开数据库。
package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestGrantCommandRejectsMissingAuthorityBeforeDatabase(t *testing.T) {
	base := []string{"--allow-write", "--config", "fixture.yaml", "--app", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--operator", "501", "--user", "101", "--group", "4", "--version", "0", "--request-id", "e59ba9e9-b1d7-424d-91a0-fd909ab40845", "--enabled=true", "--reason", "隔离测试"}
	cases := map[string][]string{
		"no-write-flag":          {},
		"missing-explicit-state": {"--allow-write", "--config", "fixture.yaml"},
		"invalid-uuid":           append(append([]string{}, base...), "--request-id", "not-a-uuid"),
		"missing-version":        append(append([]string{}, base...), "--version", ""),
		"invalid-operator":       append(append([]string{}, base...), "--operator", "0"),
		"invalid-reason":         append(append([]string{}, base...), "--reason", "\n"),
		"ambiguous-state":        append(append([]string{}, base...), "--enabled", "1"),
		"unexpected-argument":    append(append([]string{}, base...), "extra"),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			opened := false
			var out bytes.Buffer
			err := run(t.Context(), args, &out, func(context.Context, string) (*sql.DB, error) { opened = true; return nil, errors.New("不应连接") })
			if err == nil || opened || out.Len() != 0 {
				t.Fatal("缺少明确授权仍访问数据库或输出结果", opened, out.String(), err)
			}
		})
	}
}
func TestGrantCommandRequiresSelectedConfiguration(t *testing.T) {
	var out bytes.Buffer
	err := run(t.Context(), []string{"--allow-write", "--app", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--operator", "501", "--user", "101", "--group", "4", "--version", "0", "--request-id", "e59ba9e9-b1d7-424d-91a0-fd909ab40845", "--enabled=false", "--reason", "隔离测试"}, &out, func(context.Context, string) (*sql.DB, error) {
		t.Fatal("没有明确配置文件仍打开数据库")
		return nil, nil
	})
	if err == nil {
		t.Fatal("缺少配置文件仍执行")
	}
}
