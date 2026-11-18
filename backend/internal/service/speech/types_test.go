// Copyright (c) 2026 WorkMesh. 保留所有权利。
package speech

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestMeteringRejectsMissingPriceAndInvalidPCM(t *testing.T) {
	rate := 0.0
	m := Model{ID: "asr", Label: "识别", Kind: "asr", Provider: "volcengine", Languages: []string{"auto"}, ResourceID: "resource", AccountID: 1, Groups: []int64{2}, Unit: "seconds", Version: "v1", Enabled: true}
	if m.Allows(2) {
		t.Fatal("缺价放行")
	}
	m.USDPerUnit = &rate
	if !m.Allows(2) || m.Allows(3) {
		t.Fatal("显式零价或归属错误")
	}
	for _, n := range []int{0, 1, 16002} {
		if _, e := PCMSeconds(make([]byte, n)); e == nil {
			t.Fatalf("无效帧放行: %d", n)
		}
	}
	if n, e := PCMSeconds(make([]byte, 6400)); e != nil || n != .2 {
		t.Fatal(n, e)
	}
	if n, e := AliyunCharacters("你好A😀"); e != nil || n != 6 {
		t.Fatal(n, e)
	}
}
func TestVolcPacketBoundsSequenceAndCompression(t *testing.T) {
	payload := []byte(`{"result":{}}`)
	frame := volcPacket(9, 0, 1, payload)
	got, e := decodeVolcPacket(frame)
	if e != nil || !bytes.Equal(got, payload) {
		t.Fatal(e)
	}
	sequence := append([]byte{}, frame[:4]...)
	sequence = append(sequence, 0, 0, 0, 1)
	sequence = append(sequence, frame[4:]...)
	sequence[1] |= 1
	if _, e = decodeVolcPacket(sequence); e != nil {
		t.Fatal(e)
	}
	broken := append([]byte{}, frame...)
	binary.BigEndian.PutUint32(broken[4:8], 999)
	for _, v := range [][]byte{nil, frame[:5], broken, volcPacket(15, 0, 1, nil), volcPacket(9, 0, 1, bytes.Repeat([]byte("x"), 256001))} {
		if _, e = decodeVolcPacket(v); e == nil {
			t.Fatal("畸形或解压超限数据未拒绝")
		}
	}
}

func TestConfiguredSpeechLanguages(t *testing.T) {
	m := Model{Languages: []string{"zh-CN", "en-US"}}
	for _, v := range []string{"", "zh-CN", "en-US"} {
		if !m.AcceptsLanguage(v) {
			t.Fatal("配置的语言被拒绝")
		}
	}
	for _, v := range []string{"unconfigured", string(bytes.Repeat([]byte("a"), 33))} {
		if m.AcceptsLanguage(v) {
			t.Fatal("未配置或超限语言被接受")
		}
	}
}

func TestSpeechASRFixedLanguageRejectsBeforeProviderCall(t *testing.T) {
	for _, provider := range []string{"aliyun", "volcengine"} {
		language := "zh-CN"
		if provider == "volcengine" {
			language = "auto"
		}
		m := Model{Provider: provider, Languages: []string{language}}
		if _, e := m.ASRLanguage(language); e != nil {
			t.Fatal(e)
		}
		if _, e := m.ASRLanguage("unsupported"); e == nil {
			t.Fatal("错误语言没有拒绝")
		}
		m.Languages = []string{"zh-CN", "en-US"}
		if _, e := m.ASRLanguage(""); e == nil {
			t.Fatal("固定模型声明了多种语言")
		}
	}
}
