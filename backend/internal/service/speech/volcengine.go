// Copyright (c) 2026 WorkMesh. 保留所有权利。
package speech

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	ws "github.com/coder/websocket"
	"io"
	"net/http"
	"strings"
	"sync"
)

type Volcengine struct{ Client *http.Client }

func gzipData(value []byte) []byte {
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	_, _ = z.Write(value)
	_ = z.Close()
	return out.Bytes()
}
func volcPacket(kind, flags, serialization byte, payload []byte) []byte {
	payload = gzipData(payload)
	body := []byte{0x11, kind<<4 | flags, serialization<<4 | 1, 0}
	body = binary.BigEndian.AppendUint32(body, uint32(len(payload)))
	return append(body, payload...)
}
func decodeVolcPacket(packet []byte) ([]byte, error) {
	if len(packet) < 8 || packet[0]>>4 != 1 {
		return nil, fmt.Errorf("VOLC_ASR_FRAME_INVALID")
	}
	offset := int(packet[0]&15) * 4
	if offset < 4 || offset > len(packet)-4 {
		return nil, fmt.Errorf("VOLC_ASR_FRAME_INVALID")
	}
	kind, flags := packet[1]>>4, packet[1]&15
	if kind == 15 {
		return nil, fmt.Errorf("VOLC_ASR_UPSTREAM_FAILED")
	}
	if kind != 9 {
		return nil, fmt.Errorf("VOLC_ASR_FRAME_INVALID")
	}
	if flags&1 != 0 {
		offset += 4
	}
	if offset > len(packet)-4 {
		return nil, fmt.Errorf("VOLC_ASR_FRAME_INVALID")
	}
	length := int(binary.BigEndian.Uint32(packet[offset : offset+4]))
	offset += 4
	if length > 256000 || length != len(packet)-offset {
		return nil, fmt.Errorf("VOLC_ASR_FRAME_INVALID")
	}
	raw := packet[offset:]
	switch packet[2] & 15 {
	case 0:
		return raw, nil
	case 1:
		z, e := gzip.NewReader(bytes.NewReader(raw))
		if e != nil {
			return nil, e
		}
		defer func() { _ = z.Close() }()
		data, e := io.ReadAll(io.LimitReader(z, 256001))
		if e != nil || len(data) > 256000 {
			return nil, fmt.Errorf("VOLC_ASR_FRAME_TOO_LARGE")
		}
		return data, nil
	default:
		return nil, fmt.Errorf("VOLC_ASR_COMPRESSION_UNSUPPORTED")
	}
}

type volcASR struct {
	conn *ws.Conn
	done chan error
	mu   sync.Mutex
	err  error
}

func (v Volcengine) StartASR(ctx context.Context, m Model, c Credentials, language string, emit func(Segment) error) (ASR, error) {
	if _, err := m.ASRLanguage(language); err != nil {
		return nil, err
	}
	if c.APIKey == "" || m.ResourceID == "" {
		return nil, ErrUnavailable
	}
	headers := http.Header{"X-Api-Key": {c.APIKey}, "X-Api-Resource-Id": {m.ResourceID}, "X-Api-Request-Id": {providerRequestID(ctx)}}
	conn, _, err := ws.Dial(ctx, "wss://openspeech.bytedance.com/api/v3/sauc/bigmodel", &ws.DialOptions{HTTPHeader: headers})
	if err != nil {
		return nil, fmt.Errorf("VOLC_ASR_CONNECTION_FAILED")
	}
	conn.SetReadLimit(256000)
	stream := &volcASR{conn: conn, done: make(chan error, 1)}
	body, _ := json.Marshal(map[string]any{"user": map[string]string{"uid": "workmesh"}, "audio": map[string]any{"format": "pcm", "codec": "raw", "rate": 16000, "bits": 16, "channel": 1}, "request": map[string]any{"model_name": "bigmodel", "enable_itn": true, "enable_punc": true, "show_utterances": true}})
	if err = conn.Write(ctx, ws.MessageBinary, volcPacket(1, 0, 1, body)); err != nil {
		_ = conn.CloseNow()
		return nil, err
	}
	go func() {
		var failure error
		defer func() { stream.mu.Lock(); stream.err = failure; stream.mu.Unlock(); stream.done <- failure }()
		finalized := map[string]bool{}
		for {
			_, packet, e := conn.Read(ctx)
			if e != nil {
				failure = fmt.Errorf("VOLC_ASR_DISCONNECTED")
				return
			}
			raw, e := decodeVolcPacket(packet)
			if e != nil {
				failure = e
				return
			}
			var response struct {
				Result struct {
					Text       string
					Utterances []struct {
						Text      string
						StartTime int64 `json:"start_time"`
						EndTime   int64 `json:"end_time"`
						Definite  bool
					}
				}
			}
			if json.Unmarshal(raw, &response) != nil {
				failure = fmt.Errorf("VOLC_ASR_RESPONSE_INVALID")
				return
			}
			for _, u := range response.Result.Utterances {
				id := fmt.Sprintf("%012d", u.StartTime)
				if finalized[id] {
					continue
				}
				if e = emit(Segment{ID: id, StartMS: u.StartTime, EndMS: u.EndTime, Text: u.Text, Final: u.Definite}); e != nil {
					failure = e
					return
				}
				if u.Definite {
					finalized[id] = true
				}
			}
			if packet[1]&2 != 0 {
				return
			}
		}
	}()
	return stream, nil
}
func (s *volcASR) Send(ctx context.Context, pcm []byte) error {
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, ws.MessageBinary, volcPacket(2, 0, 0, pcm))
}
func (s *volcASR) Finish(ctx context.Context) error {
	if err := s.conn.Write(ctx, ws.MessageBinary, volcPacket(2, 2, 0, nil)); err != nil {
		return err
	}
	select {
	case err := <-s.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *volcASR) Close() error { return s.conn.CloseNow() }
func (v Volcengine) Synthesize(ctx context.Context, m Model, c Credentials, text, voice string, emit func([]byte) error) (int64, error) {
	if c.APIKey == "" || m.ResourceID == "" || len(text) == 0 || len(text) > 64000 {
		return 0, ErrUnavailable
	}
	body, _ := json.Marshal(map[string]any{"user": map[string]string{"uid": "workmesh"}, "req_params": map[string]any{"text": text, "speaker": voice, "audio_params": map[string]any{"format": "pcm", "sample_rate": 16000}}})
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://openspeech.bytedance.com/api/v3/tts/unidirectional", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("X-Api-Resource-Id", m.ResourceID)
	req.Header.Set("X-Api-Request-Id", providerRequestID(ctx))
	req.Header.Set("X-Control-Require-Usage-Tokens-Return", "*")
	client := v.Client
	if client == nil {
		client = newHTTP()
	}
	response, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("VOLC_TTS_CONNECTION_FAILED")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		return 0, fmt.Errorf("VOLC_TTS_FAILED:%d", response.StatusCode)
	}
	scanner := bufio.NewScanner(io.LimitReader(response.Body, 64<<20))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	var units int64
	var total int
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if line == "" {
			continue
		}
		var event struct {
			Code  int
			Data  string
			Usage struct {
				TextWords int64 `json:"text_words"`
			}
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			return 0, fmt.Errorf("VOLC_TTS_RESPONSE_INVALID")
		}
		if event.Usage.TextWords > 0 {
			units = event.Usage.TextWords
		}
		if event.Code != 0 && event.Code != 20000000 {
			return 0, fmt.Errorf("VOLC_TTS_UPSTREAM_FAILED:%d", event.Code)
		}
		if event.Data != "" {
			audio, e := base64.StdEncoding.DecodeString(event.Data)
			if e != nil {
				return 0, e
			}
			total += len(audio)
			if total > 64<<20 {
				return 0, fmt.Errorf("TTS_OUTPUT_TOO_LARGE")
			}
			if e = emit(audio); e != nil {
				return 0, e
			}
		}
		if event.Code == 20000000 {
			if units <= 0 {
				return 0, fmt.Errorf("VOLC_TTS_USAGE_MISSING")
			}
			return units, nil
		}
	}
	if scanner.Err() != nil {
		return 0, fmt.Errorf("VOLC_TTS_READ_FAILED")
	}
	return 0, fmt.Errorf("VOLC_TTS_INCOMPLETE")
}
