package repository

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type diagnosticsAccountRepo struct {
	service.AccountRepository
	account *service.Account
}

func (r *diagnosticsAccountRepo) GetByID(context.Context, int64) (*service.Account, error) {
	return r.account, nil
}

func (r *diagnosticsAccountRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	return nil
}

func TestAccountTestDiagnosticsThroughRealHTTPUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "compressed_headers", true: "internal_fallback_responses"}[fallback], func(t *testing.T) {
			account := &service.Account{
				ID: 4421, Platform: service.PlatformGrok, Type: service.AccountTypeOAuth,
				Status: service.StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{
					"access_token": "test-access", "refresh_token": "test-refresh",
					"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
				},
			}
			upstream := NewHTTPUpstream(nil).(*httpUpstreamService)
			isolation := upstream.getIsolationMode()
			profile := service.HTTPUpstreamProfileDefault
			protocolMode := upstream.resolveProtocolMode(profile, directProxyKey, nil)
			settings := upstream.applyProfilePoolSettings(upstream.resolvePoolSettings(isolation, 1), profile)
			cacheKey := buildCacheKey(isolation, directProxyKey, account.ID, protocolMode)
			calls := 0
			var compressed bytes.Buffer
			zw := gzip.NewWriter(&compressed)
			_, err := io.WriteString(zw, "data: {\"type\":\"response.completed\"}\n\n")
			require.NoError(t, err)
			require.NoError(t, zw.Close())
			upstream.clients[cacheKey] = &upstreamClientEntry{
				proxyKey: directProxyKey, poolKey: buildPoolKey(settings, protocolMode), protocolMode: protocolMode,
				client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if fallback && calls == 1 {
						return &http.Response{StatusCode: 403, Request: req,
							Header: http.Header{"X-Request-Id": {"first-denied"}},
							Body:   io.NopCloser(strings.NewReader(`{"error":"Access denied"}`))}, nil
					}
					return &http.Response{StatusCode: 200, Request: req,
						Header: http.Header{"Content-Type": {"text/event-stream"}, "Content-Encoding": {"gzip"}, "Content-Length": {strconv.Itoa(compressed.Len())}, "X-Request-Id": {"final-ok"}, "Set-Cookie": {"a=1", "b=2"}},
						Body:   io.NopCloser(bytes.NewReader(compressed.Bytes()))}, nil
				})},
			}
			repo := &diagnosticsAccountRepo{account: account}
			svc := service.NewAccountTestService(repo, nil, nil, service.NewGrokTokenProvider(repo, nil), nil, upstream, nil, nil)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/4421/test", nil)
			require.NoError(t, svc.TestAccountConnection(c, account.ID, "grok-4.3", "custom prompt", "text"))
			var events []service.AccountTestUpstreamResponse
			for _, line := range strings.Split(w.Body.String(), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var event struct {
					Type string                              `json:"type"`
					Data service.AccountTestUpstreamResponse `json:"data"`
				}
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
				if event.Type == "upstream_response" {
					events = append(events, event.Data)
				}
			}
			require.NotEmpty(t, events)
			for i, event := range events {
				require.Equal(t, i+1, event.Sequence)
				require.Equal(t, http.MethodPost, event.Method)
				require.Equal(t, "/v1/responses", event.Stage)
			}
			final := events[len(events)-1]
			require.Equal(t, 200, final.StatusCode)
			require.Equal(t, "gzip", final.Headers.Get("Content-Encoding"))
			require.Equal(t, strconv.Itoa(compressed.Len()), final.Headers.Get("Content-Length"))
			require.Equal(t, []string{"a=1", "b=2"}, final.Headers.Values("Set-Cookie"))
			require.Contains(t, w.Body.String(), `"type":"test_complete"`)
			if fallback {
				require.Equal(t, 2, calls)
				require.Len(t, events, 2, "both the 403 and fallback 200 must be retained")
				require.Equal(t, 403, events[0].StatusCode)
				require.Equal(t, "first-denied", events[0].Headers.Get("X-Request-Id"))
			} else {
				require.Len(t, events, 1)
				require.Equal(t, "gzip", events[0].Headers.Get("Content-Encoding"), "original response headers must survive body decoding")
			}
		})
	}
}
