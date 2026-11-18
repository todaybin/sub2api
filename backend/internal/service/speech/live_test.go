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

// TestLiveSuppliers 必须显式指定本机凭据文件；日志只输出供应商状态和用量。
func TestLiveSuppliers(t *testing.T) {
	path := os.Getenv("WORKMESH_SPEECH_LIVE_KEYS")
	if path == "" {
		t.Skip("未启用供应商付费测试")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal("凭据文件不可读")
	}
	text := string(raw)
	parts := strings.Split(text, "阿里百炼")
	if len(parts) != 2 {
		t.Fatal("凭据分区无效")
	}
	nls := parts[0]
	tail := strings.Split(parts[1], "字节火山")
	if len(tail) != 2 {
		t.Fatal("凭据分区无效")
	}
	capture := func(pattern, value string) string {
		r := regexp.MustCompile(pattern).FindStringSubmatch(value)
		if len(r) > 1 {
			return strings.TrimSpace(r[1])
		}
		return ""
	}
	ali := Credentials{
		AccessKeyID:     capture(`(?im)AccessKeyID[[:space:]]*[:：=][[:space:]]*([^[:space:]]+)`, nls),
		AccessKeySecret: capture(`(?im)AccessKeySecret[[:space:]]*[:：=][[:space:]]*([^[:space:]]+)`, nls),
		AppKey:          capture(`(?im)(?:appkey|AppKey)[[:space:]]*[:：=][[:space:]]*([^[:space:]]+)`, nls),
		APIKey:          capture(`(sk-[A-Za-z0-9_./+=-]+)`, tail[0]),
	}
	volc := Credentials{APIKey: capture(`([a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12})`, strings.Split(tail[1], "微信")[0])}
	for _, item := range []struct {
		name        string
		adapter     Adapter
		credentials Credentials
		model       Model
		voice       string
	}{
		{"aliyun", Aliyun{}, ali, Model{Provider: "aliyun", UpstreamModel: "cosyvoice-v3-flash", Region: "cn-beijing"}, "longanyang"},
		{"volcengine", Volcengine{}, volc, Model{Provider: "volcengine", ResourceID: "seed-tts-2.0"}, "zh_female_vv_uranus_bigtts"},
	} {
		t.Run(item.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			ctx = WithProviderRequestID(ctx, ProviderRequestID(item.name+"-live-tts-"+time.Now().UTC().Format(time.RFC3339Nano)))
			var audio []byte
			units, err := item.adapter.Synthesize(ctx, item.model, item.credentials, "你好，测试结束。", item.voice, func(b []byte) error { audio = append(audio, b...); return nil })
			if err != nil {
				t.Errorf("TTS 状态=%s", safeLiveError(err))
				return
			}
			t.Logf("TTS 成功，计费单位=%d，PCM字节=%d", units, len(audio))
			if len(audio) == 0 || len(audio) > 320000 {
				t.Fatal("TTS 长度超出短测试范围")
			}
			model := item.model
			model.Kind = "asr"
			model.Languages = []string{"zh-CN"}
			if item.name == "volcengine" {
				model.ResourceID = "volc.seedasr.sauc.duration"
				model.Languages = []string{"auto"}
			} else {
				model.Region = "cn-shanghai"
			}
			finals, characters := 0, 0
			stream, err := item.adapter.StartASR(WithProviderRequestID(ctx, ProviderRequestID(item.name+"-live-asr-"+time.Now().UTC().Format(time.RFC3339Nano))), model, item.credentials, model.Languages[0], func(s Segment) error {
				if s.Final {
					finals++
					characters += len([]rune(s.Text))
				}
				return nil
			})
			if err != nil {
				t.Errorf("ASR 状态=%s", safeLiveError(err))
				return
			}
			defer stream.Close()
			audio = append(audio, make([]byte, 16000)...)
			for offset := 0; offset < len(audio); offset += 6400 {
				end := offset + 6400
				if end > len(audio) {
					end = len(audio)
				}
				if err = stream.Send(ctx, audio[offset:end]); err != nil {
					t.Fatalf("ASR发送 状态=%s", safeLiveError(err))
				}
				select {
				case <-time.After(200 * time.Millisecond):
				case <-ctx.Done():
					t.Fatal("ASR超时")
				}
			}
			if err = stream.Finish(ctx); err != nil {
				t.Fatalf("ASR结束 状态=%s", safeLiveError(err))
			}
			if finals == 0 || characters == 0 {
				t.Fatal("ASR 未产生最终文本")
			}
			t.Logf("ASR 成功，最终段=%d，文本字符=%d", finals, characters)
		})
	}
}
func safeLiveError(e error) string {
	// 只放行适配器主动构造的无凭据业务码，底层 HTTP/WS 错误一律隐去。
	value := e.Error()
	if regexp.MustCompile(`^(ALIYUN|VOLC|TTS|SPEECH)_[A-Z_]+(?::[0-9]+)?$`).MatchString(value) {
		return value
	}
	return "SUPPLIER_TRANSPORT_FAILED"
}
