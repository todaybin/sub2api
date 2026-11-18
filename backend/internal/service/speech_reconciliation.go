// Copyright (c) 2026 WorkMesh. 保留所有权利。
package service

import "context"

type SpeechReconciliation struct {
	Decision string  `json:"decision"`
	Quantity float64 `json:"quantity"`
	Evidence string  `json:"evidence"`
	ActorID  int64   `json:"-"`
}

// 核对仅复用现有管理员授权；审计不包含录音、转写或密钥。
type SpeechReconciliationStore interface {
	ListSpeechReconciliation(context.Context, string) ([]map[string]any, error)
	ReconcileSpeech(context.Context, string, SpeechReconciliation) error
}
