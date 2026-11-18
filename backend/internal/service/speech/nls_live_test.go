// Copyright (c) 2026 WorkMesh. 保留所有权利。
package speech

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestLiveAliyunNLS(t *testing.T) {
	path := os.Getenv("WORKMESH_SPEECH_LIVE_KEYS")
	if path == "" {
		t.Skip("未启用供应商付费测试")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal("凭据文件不可读")
	}
	text := strings.Split(string(raw), "阿里百炼")[0]
	get := func(name string) string {
		v := regexp.MustCompile("(?im)" + name + "[[:space:]]*[:：=][[:space:]]*([^[:space:]]+)").FindStringSubmatch(text)
		if len(v) == 2 {
			return v[1]
		}
		return ""
	}
	c := Credentials{AccessKeyID: get("AccessKeyID"), AccessKeySecret: get("AccessKeySecret"), AppKey: get("appkey")}
	if c.AccessKeyID == "" || c.AccessKeySecret == "" || c.AppKey == "" {
		t.Fatal("NLS 字段不完整")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	stream, e := (Aliyun{}).StartASR(ctx, Model{Provider: "aliyun", Region: "cn-shanghai", Languages: []string{"zh-CN"}}, c, "zh-CN", func(Segment) error { return nil })
	if e != nil {
		t.Fatal(safeLiveError(e))
	}
	defer func() { _ = stream.Close() }()
	for i := 0; i < 4; i++ {
		if e = stream.Send(ctx, make([]byte, 6400)); e != nil {
			t.Fatal(safeLiveError(e))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if e = stream.Finish(ctx); e != nil {
		t.Fatal(safeLiveError(e))
	}
	t.Log("NLS 鉴权、800ms PCM 发送、结束回调成功")
}
