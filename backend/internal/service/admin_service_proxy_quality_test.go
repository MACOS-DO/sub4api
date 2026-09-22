package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recordingProxyLatencyCache struct {
	mu      sync.Mutex
	records map[int64]*ProxyLatencyInfo
}

func newRecordingProxyLatencyCache() *recordingProxyLatencyCache {
	return &recordingProxyLatencyCache{records: make(map[int64]*ProxyLatencyInfo)}
}

func (c *recordingProxyLatencyCache) GetProxyLatencies(_ context.Context, proxyIDs []int64) (map[int64]*ProxyLatencyInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[int64]*ProxyLatencyInfo, len(proxyIDs))
	for _, id := range proxyIDs {
		if rec := c.records[id]; rec != nil {
			copied := *rec
			out[id] = &copied
		}
	}
	return out, nil
}

func (c *recordingProxyLatencyCache) SetProxyLatency(_ context.Context, proxyID int64, info *ProxyLatencyInfo) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := *info
	c.records[proxyID] = &copied
	return nil
}

func (c *recordingProxyLatencyCache) record(proxyID int64) *ProxyLatencyInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec := c.records[proxyID]
	if rec == nil {
		return nil
	}
	copied := *rec
	return &copied
}

func TestSaveProxyQualitySnapshotKeepsTimezone(t *testing.T) {
	cache := newRecordingProxyLatencyCache()
	svc := &adminServiceImpl{proxyLatencyCache: cache}
	result := &ProxyQualityCheckResult{
		Score:       100,
		Grade:       "A",
		CheckedAt:   time.Now().Unix(),
		PassedCount: 1,
		Items:       []ProxyQualityCheckItem{{Target: "base_connectivity", Status: "pass"}},
	}

	svc.saveProxyQualitySnapshot(context.Background(), 7, result, &ProxyExitInfo{
		IP:          "1.2.3.4",
		Country:     "Japan",
		CountryCode: "JP",
		Region:      "Tokyo",
		City:        "Tokyo",
		Timezone:    "Asia/Tokyo",
	})

	rec := cache.record(7)
	require.NotNil(t, rec)
	require.Equal(t, "Asia/Tokyo", rec.Timezone)
}

func TestProbeProxyLatencyKeepsTimezone(t *testing.T) {
	cache := newRecordingProxyLatencyCache()
	prober := &fakeProxyExitInfoProber{results: []*ProxyExitInfo{{
		IP:          "1.2.3.4",
		CountryCode: "JP",
		City:        "Tokyo",
		Timezone:    "Asia/Tokyo",
	}}}
	svc := &adminServiceImpl{proxyProber: prober, proxyLatencyCache: cache}

	svc.probeProxyLatency(context.Background(), &Proxy{ID: 8, Protocol: "http", Host: "127.0.0.1", Port: 8080})

	rec := cache.record(8)
	require.NotNil(t, rec)
	require.True(t, rec.Success)
	require.Equal(t, "Asia/Tokyo", rec.Timezone)
}

func TestFinalizeProxyQualityResult_ScoreAndGrade(t *testing.T) {
	result := &ProxyQualityCheckResult{
		PassedCount:    2,
		WarnCount:      1,
		FailedCount:    1,
		ChallengeCount: 1,
	}

	finalizeProxyQualityResult(result)

	require.Equal(t, 38, result.Score)
	require.Equal(t, "F", result.Grade)
	require.Contains(t, result.Summary, "通过 2 项")
	require.Contains(t, result.Summary, "告警 1 项")
	require.Contains(t, result.Summary, "失败 1 项")
	require.Contains(t, result.Summary, "挑战 1 项")
}

func TestRunProxyQualityTarget_CloudflareChallenge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("cf-ray", "test-ray-123")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<!DOCTYPE html><title>Just a moment...</title><script>window._cf_chl_opt={};</script>"))
	}))
	defer server.Close()

	target := proxyQualityTarget{
		Target: "openai",
		URL:    server.URL,
		Method: http.MethodGet,
		AllowedStatuses: map[int]struct{}{
			http.StatusUnauthorized: {},
		},
	}

	item := runProxyQualityTarget(context.Background(), server.Client(), target)
	require.Equal(t, "challenge", item.Status)
	require.Equal(t, http.StatusForbidden, item.HTTPStatus)
	require.Equal(t, "test-ray-123", item.CFRay)
}

func TestRunProxyQualityTarget_AllowedStatusPass(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()

	target := proxyQualityTarget{
		Target: "gemini",
		URL:    server.URL,
		Method: http.MethodGet,
		AllowedStatuses: map[int]struct{}{
			http.StatusOK: {},
		},
	}

	item := runProxyQualityTarget(context.Background(), server.Client(), target)
	require.Equal(t, "pass", item.Status)
	require.Equal(t, http.StatusOK, item.HTTPStatus)
}

func TestRunProxyQualityTarget_AllowedStatusPassForUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	target := proxyQualityTarget{
		Target: "openai",
		URL:    server.URL,
		Method: http.MethodGet,
		AllowedStatuses: map[int]struct{}{
			http.StatusUnauthorized: {},
		},
	}

	item := runProxyQualityTarget(context.Background(), server.Client(), target)
	require.Equal(t, "pass", item.Status)
	require.Equal(t, http.StatusUnauthorized, item.HTTPStatus)
	require.Contains(t, item.Message, "目标可达")
}

func TestProxyQualityTargets_IncludesGrok(t *testing.T) {
	var grokTarget *proxyQualityTarget
	for i := range proxyQualityTargets {
		if proxyQualityTargets[i].Target == "grok" {
			grokTarget = &proxyQualityTargets[i]
			break
		}
	}

	require.NotNil(t, grokTarget)
	require.Equal(t, "https://api.x.ai/v1/models", grokTarget.URL)
	require.Equal(t, http.MethodGet, grokTarget.Method)
	require.Contains(t, grokTarget.AllowedStatuses, http.StatusUnauthorized)
}

func TestRunProxyQualityTarget_GrokUnauthorizedPasses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	target := proxyQualityTarget{
		Target: "grok",
		URL:    server.URL,
		Method: http.MethodGet,
		AllowedStatuses: map[int]struct{}{
			http.StatusUnauthorized: {},
		},
	}

	item := runProxyQualityTarget(context.Background(), server.Client(), target)
	require.Equal(t, "grok", item.Target)
	require.Equal(t, "pass", item.Status)
	require.Equal(t, http.StatusUnauthorized, item.HTTPStatus)
	require.Contains(t, item.Message, "目标可达")
}
