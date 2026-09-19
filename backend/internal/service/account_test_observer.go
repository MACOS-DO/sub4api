package service

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// AccountTestUpstreamResponse preserves every value received from the upstream.
// Path intentionally excludes query strings, which can contain credentials.
type AccountTestUpstreamResponse struct {
	Sequence   int         `json:"sequence"`
	Method     string      `json:"method"`
	Stage      string      `json:"stage"`
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers"`
}

type accountTestObserverKey struct{}
type accountTestObserver struct {
	mu       sync.Mutex
	sequence int
	emit     func(AccountTestUpstreamResponse)
}

func withAccountTestObserver(ctx context.Context, emit func(AccountTestUpstreamResponse)) context.Context {
	ctx = context.WithValue(ctx, accountTestObserverKey{}, &accountTestObserver{emit: emit})
	return WithHTTPUpstreamResponseObserver(ctx, func(req *http.Request, resp *http.Response) {
		observeAccountTestResponse(ctx, req.Method, req.URL.Path, resp.StatusCode, resp.Header)
	})
}

func observeAccountTestResponse(ctx context.Context, method, stage string, status int, headers http.Header) {
	observer, _ := ctx.Value(accountTestObserverKey{}).(*accountTestObserver)
	if observer == nil || status == 0 {
		return
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.sequence++
	snapshot := headers.Clone()
	if snapshot == nil {
		snapshot = http.Header{}
	}
	observer.emit(AccountTestUpstreamResponse{Sequence: observer.sequence, Method: method, Stage: stage, StatusCode: status, Headers: snapshot})
}

func accountTestPrompt(fallback string, prompts ...string) string {
	if len(prompts) > 0 && strings.TrimSpace(prompts[0]) != "" {
		return strings.TrimSpace(prompts[0])
	}
	return fallback
}
