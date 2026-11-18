// Copyright (c) 2026 WorkMesh. 保留所有权利。
package speech

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	ws "github.com/coder/websocket"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Aliyun struct{ Client *http.Client }

func newHTTP() *http.Client {
	return &http.Client{Timeout: 110 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (a Aliyun) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return newHTTP()
}
func escape(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
func (a Aliyun) nlsToken(ctx context.Context, c Credentials) (string, error) {
	if c.AccessKeyID == "" || c.AccessKeySecret == "" || c.AppKey == "" {
		return "", ErrUnavailable
	}
	p := url.Values{"AccessKeyId": {c.AccessKeyID}, "Action": {"CreateToken"}, "Format": {"JSON"}, "RegionId": {"cn-shanghai"}, "SignatureMethod": {"HMAC-SHA1"}, "SignatureNonce": {uuid.NewString()}, "SignatureVersion": {"1.0"}, "Timestamp": {time.Now().UTC().Format("2006-01-02T15:04:05Z")}, "Version": {"2019-02-28"}}
	canonical := strings.ReplaceAll(p.Encode(), "+", "%20")
	mac := hmac.New(sha1.New, []byte(c.AccessKeySecret+"&"))
	_, _ = mac.Write([]byte("GET&%2F&" + escape(canonical)))
	p.Set("Signature", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://nls-meta.cn-shanghai.aliyuncs.com/?"+p.Encode(), nil)
	response, err := a.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("ALIYUN_TOKEN_CONNECTION_FAILED")
	}
	defer func() { _ = response.Body.Close() }()
	var body struct{ Token struct{ ID string } }
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 64000)).Decode(&body) != nil || body.Token.ID == "" {
		return "", fmt.Errorf("ALIYUN_TOKEN_FAILED")
	}
	return body.Token.ID, nil
}

type aliyunASR struct {
	conn      *ws.Conn
	ctx       context.Context
	task, app string
	done      chan error
	mu        sync.Mutex
	err       error
}

func (a Aliyun) StartASR(ctx context.Context, m Model, c Credentials, language string, emit func(Segment) error) (ASR, error) {
	if _, err := m.ASRLanguage(language); err != nil {
		return nil, err
	}
	token, err := a.nlsToken(ctx, c)
	if err != nil {
		return nil, err
	}
	conn, _, err := ws.Dial(ctx, "wss://nls-gateway-cn-shanghai.aliyuncs.com/ws/v1?token="+url.QueryEscape(token), nil)
	if err != nil {
		return nil, fmt.Errorf("ALIYUN_ASR_CONNECTION_FAILED")
	}
	conn.SetReadLimit(256000)
	stream := &aliyunASR{conn: conn, ctx: ctx, task: strings.ReplaceAll(providerRequestID(ctx), "-", ""), app: c.AppKey, done: make(chan error, 1)}
	ready := make(chan error, 1)
	go func() {
		started := false
		var failure error
		defer func() {
			stream.mu.Lock()
			stream.err = failure
			stream.mu.Unlock()
			if !started {
				ready <- failure
			}
			stream.done <- failure
		}()
		for {
			_, raw, e := conn.Read(ctx)
			if e != nil {
				failure = fmt.Errorf("ALIYUN_ASR_DISCONNECTED")
				return
			}
			var event struct {
				Header struct {
					Name   string
					Status int
				}
				Payload struct {
					Index     int
					Time      int64
					BeginTime int64 `json:"begin_time"`
					Result    string
				}
			}
			if json.Unmarshal(raw, &event) != nil {
				failure = fmt.Errorf("ALIYUN_ASR_RESPONSE_INVALID")
				return
			}
			if event.Header.Status != 0 && event.Header.Status != 20000000 {
				failure = fmt.Errorf("ALIYUN_ASR_FAILED:%d", event.Header.Status)
				return
			}
			switch event.Header.Name {
			case "TranscriptionStarted":
				started = true
				ready <- nil
			case "TranscriptionResultChanged", "SentenceEnd":
				if e = emit(Segment{ID: fmt.Sprintf("%010d", event.Payload.Index), StartMS: event.Payload.BeginTime, EndMS: event.Payload.Time, Text: event.Payload.Result, Final: event.Header.Name == "SentenceEnd"}); e != nil {
					failure = e
					return
				}
			case "TranscriptionCompleted":
				return
			}
		}
	}()
	err = stream.command(ctx, "StartTranscription", map[string]any{"format": "pcm", "sample_rate": 16000, "enable_intermediate_result": true, "enable_punctuation_prediction": true, "enable_inverse_text_normalization": true})
	if err == nil {
		select {
		case err = <-ready:
		case <-time.After(12 * time.Second):
			err = fmt.Errorf("ALIYUN_ASR_START_TIMEOUT")
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if err != nil {
		_ = conn.CloseNow()
		return nil, err
	}
	return stream, nil
}
func (s *aliyunASR) command(ctx context.Context, name string, payload any) error {
	raw, _ := json.Marshal(map[string]any{"header": map[string]any{"message_id": strings.ReplaceAll(uuid.NewString(), "-", ""), "task_id": s.task, "namespace": "SpeechTranscriber", "name": name, "appkey": s.app}, "payload": payload})
	return s.conn.Write(ctx, ws.MessageText, raw)
}
func (s *aliyunASR) Send(ctx context.Context, pcm []byte) error {
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, ws.MessageBinary, pcm)
}
func (s *aliyunASR) Finish(ctx context.Context) error {
	if err := s.command(ctx, "StopTranscription", map[string]any{}); err != nil {
		return err
	}
	select {
	case err := <-s.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *aliyunASR) Close() error { return s.conn.CloseNow() }
func (a Aliyun) Synthesize(ctx context.Context, m Model, c Credentials, text, voice string, emit func([]byte) error) (int64, error) {
	units, err := AliyunCharacters(text)
	if err != nil || units == 0 {
		return 0, fmt.Errorf("TTS_TEXT_INVALID")
	}
	if c.APIKey == "" {
		return 0, ErrUnavailable
	}
	endpoint := "wss://dashscope.aliyuncs.com/api-ws/v1/inference/"
	headers := http.Header{"Authorization": {"Bearer " + c.APIKey}}
	if c.WorkspaceID != "" {
		if !regexp.MustCompile("^[a-zA-Z0-9-]{1,80}$").MatchString(c.WorkspaceID) {
			return 0, ErrUnavailable
		}
		headers.Set("X-DashScope-WorkSpace", c.WorkspaceID)
	}
	conn, response, err := ws.Dial(ctx, endpoint, &ws.DialOptions{HTTPHeader: headers})
	if err != nil {
		if response != nil {
			return 0, fmt.Errorf("ALIYUN_TTS_CONNECTION_FAILED:%d", response.StatusCode)
		}
		return 0, fmt.Errorf("ALIYUN_TTS_CONNECTION_FAILED")
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(1024 * 1024)
	task := providerRequestID(ctx)
	request := map[string]any{"header": map[string]any{"action": "run-task", "task_id": task, "streaming": "duplex"}, "payload": map[string]any{"task_group": "audio", "task": "tts", "function": "SpeechSynthesizer", "model": m.UpstreamModel, "parameters": map[string]any{"text_type": "PlainText", "voice": voice, "format": "pcm", "sample_rate": 16000}, "input": map[string]any{}}}
	raw, _ := json.Marshal(request)
	if err = conn.Write(ctx, ws.MessageText, raw); err != nil {
		return 0, err
	}
	var size int
	for {
		kind, data, e := conn.Read(ctx)
		if e != nil {
			return 0, fmt.Errorf("ALIYUN_TTS_DISCONNECTED")
		}
		if kind == ws.MessageBinary {
			size += len(data)
			if size > 64<<20 {
				return 0, fmt.Errorf("TTS_OUTPUT_TOO_LARGE")
			}
			if e = emit(data); e != nil {
				return 0, e
			}
			continue
		}
		var event struct{ Header struct{ Event string } }
		if json.Unmarshal(data, &event) != nil {
			return 0, fmt.Errorf("ALIYUN_TTS_RESPONSE_INVALID")
		}
		switch event.Header.Event {
		case "task-started":
			for _, action := range []string{"continue-task", "finish-task"} {
				input := map[string]any{}
				if action == "continue-task" {
					input["text"] = text
				}
				body, _ := json.Marshal(map[string]any{"header": map[string]any{"action": action, "task_id": task, "streaming": "duplex"}, "payload": map[string]any{"input": input}})
				if e = conn.Write(ctx, ws.MessageText, body); e != nil {
					return 0, e
				}
			}
		case "task-finished":
			return units, nil
		case "task-failed":
			return 0, fmt.Errorf("ALIYUN_TTS_FAILED")
		}
	}
}
