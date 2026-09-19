package repository

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alibabacloud-go/tea/dara"
	"github.com/alibabacloud-go/tea/tea"
	"github.com/stretchr/testify/require"

	"github.com/MACOS-DO/sub4api/internal/service"
)

// newAliyunCaptchaTestTarget 起一个假的阿里云端点，让真实 SDK 走完整的签名/序列化链路。
func newAliyunCaptchaTestTarget(t *testing.T, handler http.HandlerFunc) (*aliyunCaptchaVerifier, service.AliyunCaptchaCredentials) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	verifier := &aliyunCaptchaVerifier{protocol: "HTTP", timeoutMillis: 2_000}
	cred := service.AliyunCaptchaCredentials{
		AccessKeyID:     "test-ak-id",
		AccessKeySecret: "test-ak-secret",
		SceneID:         "scene-1",
		Endpoint:        strings.TrimPrefix(server.URL, "http://"),
	}
	// The SDK matches NO_PROXY against the complete endpoint, including its port.
	t.Setenv("NO_PROXY", cred.Endpoint)
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
	server := httptest.NewServer(http.NotFoundHandler())
	endpoint := strings.TrimPrefix(server.URL, "http://")
	t.Setenv("NO_PROXY", endpoint)
	server.Close() // 立即关闭，制造连接失败

	verifier := &aliyunCaptchaVerifier{protocol: "HTTP", timeoutMillis: 2_000}
	cred := service.AliyunCaptchaCredentials{
		AccessKeyID:     "test-ak-id",
		AccessKeySecret: "test-ak-secret",
		SceneID:         "scene-1",
		Endpoint:        endpoint,
	}

	_, err := verifier.VerifyCaptcha(context.Background(), cred, "param")
	require.Error(t, err)
	var apiErr *service.AliyunCaptchaAPIError
	require.False(t, errors.As(err, &apiErr), "transport errors must not be normalized to API errors")
}

func TestNormalizeAliyunCaptchaError(t *testing.T) {
	for _, sdk := range []struct {
		name     string
		newError func(*int) error
	}{
		{
			name: "tea",
			newError: func(status *int) error {
				return &tea.SDKError{StatusCode: status, Code: tea.String("test-code"), Message: tea.String("test-message")}
			},
		},
		{
			name: "dara",
			newError: func(status *int) error {
				return &dara.SDKError{StatusCode: status, Code: dara.String("test-code"), Message: dara.String("test-message")}
			},
		},
	} {
		t.Run(sdk.name, func(t *testing.T) {
			for _, tc := range []struct {
				name         string
				status       *int
				wantAPIError bool
			}{
				{name: "missing status"},
				{name: "transport error", status: tea.Int(0)},
				{name: "negative status", status: tea.Int(-1)},
				{name: "invalid status", status: tea.Int(600)},
				{name: "forbidden", status: tea.Int(http.StatusForbidden), wantAPIError: true},
				{name: "server error", status: tea.Int(http.StatusInternalServerError), wantAPIError: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					err := fmt.Errorf("verify captcha: %w", sdk.newError(tc.status))
					got := normalizeAliyunCaptchaError(err)
					if !tc.wantAPIError {
						require.Same(t, err, got, "preserve errors without an HTTP response, including their wrapping")
						return
					}
					var apiErr *service.AliyunCaptchaAPIError
					require.ErrorAs(t, got, &apiErr)
					require.Equal(t, "test-code", apiErr.Code)
					require.Equal(t, "test-message", apiErr.Message)
				})
			}
		})
	}
	require.Nil(t, normalizeAliyunCaptchaError(nil))
	err := errors.New("connection failed")
	require.Same(t, err, normalizeAliyunCaptchaError(err))
}
