package repository

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// newAliyunCaptchaTestTarget 起一个假的阿里云端点，让真实 SDK 走完整的签名/序列化链路。
func newAliyunCaptchaTestTarget(t *testing.T, handler http.HandlerFunc) (*aliyunCaptchaVerifier, service.AliyunCaptchaCredentials) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	endpoint := strings.TrimPrefix(server.URL, "http://")
	// 阿里云 SDK 对 NO_PROXY 精确匹配含端口的 Host，测试端点必须直连。
	t.Setenv("NO_PROXY", endpoint)

	verifier := &aliyunCaptchaVerifier{protocol: "HTTP", timeoutMillis: 2_000}
	cred := service.AliyunCaptchaCredentials{
		AccessKeyID:     "test-ak-id",
		AccessKeySecret: "test-ak-secret",
		SceneID:         "scene-1",
		Endpoint:        endpoint,
	}
	return verifier, cred
}

func TestAliyunCaptchaVerifier_VerifySuccess(t *testing.T) {
	var capturedParam, capturedSceneID string
	verifier, cred := newAliyunCaptchaTestTarget(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		capturedParam = r.Form.Get("CaptchaVerifyParam")
		capturedSceneID = r.Form.Get("SceneId")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Code":"Success","Message":"success","RequestId":"req-1","Success":true,"Result":{"VerifyResult":true,"VerifyCode":"T001"}}`))
	})

	result, err := verifier.VerifyCaptcha(context.Background(), cred, "the-verify-param")
	require.NoError(t, err)
	require.True(t, result.VerifyResult)
	require.Equal(t, "T001", result.VerifyCode)
	require.Equal(t, "the-verify-param", capturedParam)
	require.Equal(t, "scene-1", capturedSceneID)
}

func TestAliyunCaptchaVerifier_VerifyResultFalse(t *testing.T) {
	verifier, cred := newAliyunCaptchaTestTarget(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Code":"Success","RequestId":"req-2","Success":true,"Result":{"VerifyResult":false,"VerifyCode":"F002"}}`))
	})

	result, err := verifier.VerifyCaptcha(context.Background(), cred, "bad-param")
	require.NoError(t, err)
	require.False(t, result.VerifyResult)
	require.Equal(t, "F002", result.VerifyCode)
}

func TestAliyunCaptchaVerifier_APIErrorNormalized(t *testing.T) {
	verifier, cred := newAliyunCaptchaTestTarget(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"Code":"SignatureDoesNotMatch","Message":"Specified signature is not matched with our calculation.","RequestId":"req-3"}`))
	})

	_, err := verifier.VerifyCaptcha(context.Background(), cred, "param")
	require.Error(t, err)
	var apiErr *service.AliyunCaptchaAPIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "SignatureDoesNotMatch", apiErr.Code)
}

func TestAliyunCaptchaVerifier_TransportError(t *testing.T) {
	// 保持监听端口占用，只关闭连接，避免端口被并行测试重用后返回业务响应。
	verifier, cred := newAliyunCaptchaTestTarget(t, func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("验证码测试响应不支持连接接管")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("制造验证码传输失败: %v", err)
			return
		}
		_ = conn.Close()
	})

	_, err := verifier.VerifyCaptcha(context.Background(), cred, "param")
	require.Error(t, err)
	var apiErr *service.AliyunCaptchaAPIError
	require.False(t, errors.As(err, &apiErr), "transport errors must not be normalized to API errors: %T %v", err, err)
}
