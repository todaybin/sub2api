// Copyright (c) 2026 WorkMesh. 保留所有权利。
package handler

import (
	"context"
	"errors"
	"testing"
)

func TestSpeechSynthesisDisconnectStillCollectsProviderUsage(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		sends := 0
		sink := speechSynthesisSink(ctx, func([]byte) error { sends++; return errors.New("client disconnected") })
		if cancelFirst {
			cancel()
		}
		for i := 0; i < 3; i++ {
			if err := sink([]byte{1, 2}); err != nil {
				t.Fatal("客户端取消不应中断上游计量", err)
			}
		}
		cancel()
		want := 1
		if cancelFirst {
			want = 0
		}
		if sends != want {
			t.Fatalf("断连后重复写音频: %d", sends)
		}
	}
}
