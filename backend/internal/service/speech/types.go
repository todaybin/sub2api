// Copyright (c) 2026 WorkMesh. 保留所有权利。
package speech

import (
	"context"
	"errors"
	"fmt"
	"github.com/shopspring/decimal"
	"math"
	"unicode"
	"unicode/utf8"
)

var ErrUnavailable = errors.New("SPEECH_UNAVAILABLE")

type Model struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Kind          string   `json:"kind"`
	Provider      string   `json:"provider"`
	AccountID     int64    `json:"accountId"`
	UpstreamModel string   `json:"upstreamModel"`
	ResourceID    string   `json:"resourceId"`
	Region        string   `json:"region"`
	Groups        []int64  `json:"groups"`
	Languages     []string `json:"languages"`
	Voices        []string `json:"voices"`
	USDPerUnit    *float64 `json:"usdPerUnit"`
	Unit          string   `json:"unit"`
	Version       string   `json:"version"`
	Enabled       bool     `json:"enabled"`
}

func (m Model) Validate() error {
	if m.ID == "" || len(m.ID) > 100 || m.Label == "" || len(m.Groups) == 0 || len(m.Groups) > 100 || m.AccountID <= 0 || m.Version == "" || m.USDPerUnit == nil || math.IsNaN(*m.USDPerUnit) || math.IsInf(*m.USDPerUnit, 0) || *m.USDPerUnit < 0 {
		return fmt.Errorf("语音服务价格或归属无效")
	}
	if m.Provider != "aliyun" && m.Provider != "volcengine" {
		return fmt.Errorf("语音供应商无效")
	}
	if (m.Kind == "asr" && m.Unit != "seconds") || (m.Kind == "tts" && m.Unit != "characters") || (m.Kind != "asr" && m.Kind != "tts") {
		return fmt.Errorf("语音计价单位无效")
	}
	if m.Provider == "volcengine" && m.ResourceID == "" {
		return fmt.Errorf("火山语音必须配置资源 ID")
	}
	if m.Provider == "aliyun" && m.Kind == "asr" && m.Region != "cn-shanghai" {
		return fmt.Errorf("首版阿里 NLS 仅开放上海地域")
	}
	if m.Provider == "aliyun" && m.Kind == "tts" && (m.UpstreamModel == "" || m.Region != "cn-beijing") {
		return fmt.Errorf("CosyVoice 需配置北京地域和模型")
	}
	if len(m.Label) > 100 || len(m.Version) > 100 || len(m.UpstreamModel) > 100 || len(m.ResourceID) > 100 || len(m.Languages) > 100 || len(m.Voices) > 100 {
		return fmt.Errorf("语音配置范围无效")
	}
	for _, id := range m.Groups {
		if id <= 0 {
			return fmt.Errorf("语音授权分组无效")
		}
	}
	for _, v := range append(append([]string{}, m.Languages...), m.Voices...) {
		if len(v) == 0 || len(v) > 100 {
			return fmt.Errorf("语音语言或音色无效")
		}
	}
	if m.Kind == "asr" {
		if _, err := m.ASRLanguage(""); err != nil {
			return err
		}
	}
	if m.Kind == "tts" && len(m.Voices) == 0 {
		return fmt.Errorf("朗读必须配置允许音色")
	}
	return nil
}

// 阿里 NLS 语言由 AppKey 所属项目模型决定；一个目录项只声明一种经过验证的语言。
// 字节当前 bigmodel 接口使用自动识别，不能把语言选项伪装为强制切换。
func (m Model) ASRLanguage(language string) (string, error) {
	if len(m.Languages) != 1 {
		return "", fmt.Errorf("SPEECH_ASR_LANGUAGE_CONFIG_INVALID")
	}
	fixed := m.Languages[0]
	if m.Provider == "volcengine" && fixed != "auto" {
		return "", fmt.Errorf("SPEECH_ASR_LANGUAGE_CONFIG_INVALID")
	}
	if m.Provider == "aliyun" && fixed != "zh-CN" && fixed != "en-US" {
		return "", fmt.Errorf("SPEECH_ASR_LANGUAGE_CONFIG_INVALID")
	}
	if language != "" && language != fixed {
		return "", fmt.Errorf("SPEECH_LANGUAGE_UNSUPPORTED")
	}
	return fixed, nil
}

// AcceptsLanguage 仅开放目录声明的语言；适配器另校验固定模型约束。
func (m Model) AcceptsLanguage(language string) bool {
	if len(language) > 32 {
		return false
	}
	if language == "" {
		return true
	}
	for _, v := range m.Languages {
		if v == language {
			return true
		}
	}
	return false
}
func (m Model) Allows(group int64) bool {
	if !m.Enabled || m.Validate() != nil {
		return false
	}
	for _, id := range m.Groups {
		if id == group {
			return true
		}
	}
	return false
}
func (m Model) Cost(quantity float64) (float64, error) {
	if m.Validate() != nil || quantity < 0 || math.IsNaN(quantity) || math.IsInf(quantity, 0) {
		return 0, ErrUnavailable
	}
	value, _ := decimal.NewFromFloat(quantity).Mul(decimal.NewFromFloat(*m.USDPerUnit)).Round(8).Float64()
	return value, nil
}

type Credentials struct {
	APIKey, AppKey, AccessKeyID, AccessKeySecret, WorkspaceID string
	MaxConcurrency                                            int
}
type Segment struct {
	ID      string `json:"id"`
	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
	Text    string `json:"text"`
	Final   bool   `json:"final"`
}
type ASR interface {
	Send(context.Context, []byte) error
	Finish(context.Context) error
	Close() error
}
type Adapter interface {
	StartASR(context.Context, Model, Credentials, string, func(Segment) error) (ASR, error)
	Synthesize(context.Context, Model, Credentials, string, string, func([]byte) error) (int64, error)
}

/** 字符规则是价格版本的一部分；不将供应商计费字符等同 UTF-8 字节数。 */
func AliyunCharacters(text string) (int64, error) {
	if !utf8.ValidString(text) || len(text) > 64000 {
		return 0, fmt.Errorf("TTS_TEXT_INVALID")
	}
	var n int64
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			n += 2
		} else {
			n++
		}
	}
	return n, nil
}
func PCMSeconds(pcm []byte) (float64, error) {
	if len(pcm) == 0 || len(pcm)%2 != 0 || len(pcm) > 16000 {
		return 0, fmt.Errorf("PCM_INVALID")
	}
	return float64(len(pcm)) / 32000, nil
}
