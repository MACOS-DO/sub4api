package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexHypothesisRawLogAndComparisons(t *testing.T) {
	state := strings.Repeat("actual-state-", 20)
	var output bytes.Buffer
	printExchange(&output, "gpt-6-astra", service.CodexHypothesisExchange{
		RequestState: state, ResponseState: state,
		RequestCookie:   "session=one; other=two",
		ResponseCookies: []string{"session=one; Path=/; Secure", "new=three; Path=/"},
		StatusCode:      http.StatusOK,
	}, modelTraceResult{Prediction: "gpt-6-astra", Probability: .9}, nil, nil, time.Second)
	text := output.String()
	require.Contains(t, strings.ReplaceAll(text, "│     ", ""), state[:96])
	require.Contains(t, text, state[len(state)-20:])
	require.Equal(t, 2, strings.Count(text, "Set-Cookie #"))
	require.Contains(t, text, "session=one; other=two")
	require.Contains(t, text, "session=one; Path=/; Secure")
	require.Contains(t, text, "两个 turn-state：匹配")
	require.Contains(t, text, "两个 cookie：匹配（1 个同名 cookie 的值相同）")
	require.Equal(t, "不匹配", compareStrings("old", "new"))
	require.Contains(t, compareCookies("session=one", []string{"session=two; Path=/"}), "不匹配")
	require.Contains(t, compareCookies("session=one", []string{"other=two; Path=/"}), "无法比较")
	require.Contains(t, compareCookies("session=one", nil), "无法比较")
}

type concurrentOutput struct {
	mu         sync.Mutex
	buffer     bytes.Buffer
	pinned     chan struct{}
	finished   chan struct{}
	pinOnce    sync.Once
	finishOnce sync.Once
}

func (out *concurrentOutput) Write(data []byte) (int, error) {
	out.mu.Lock()
	defer out.mu.Unlock()
	length, err := out.buffer.Write(data)
	if out.pinned != nil && bytes.Contains(data, []byte("首张匹配票据已固定")) {
		out.pinOnce.Do(func() { close(out.pinned) })
	}
	if out.finished != nil && bytes.Contains(data, []byte("当前匹配率：2/3")) {
		out.finishOnce.Do(func() { close(out.finished) })
	}
	return length, err
}
func (out *concurrentOutput) String() string {
	out.mu.Lock()
	defer out.mu.Unlock()
	return out.buffer.String()
}

func TestCodexHypothesisConcurrentPinsFirstFinishedTicket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out := &concurrentOutput{pinned: make(chan struct{}), finished: make(chan struct{})}
	var sequence, inFlight, maximum atomic.Int32
	entered := make(chan int, 3)
	verified := make(chan [2]string, 3)
	var releases [4]chan struct{}
	for index := 1; index <= 3; index++ {
		releases[index] = make(chan struct{})
	}
	var waitMu sync.Mutex
	var waits []time.Duration
	deps := watchDependencies{
		challenge: func(context.Context) (service.CodexHypothesisChallenge, error) {
			return service.CodexHypothesisChallenge{Prompt: strconv.Itoa(int(sequence.Add(1))), ExpectedCount: 292}, nil
		},
		probe: func(ctx context.Context, challenge service.CodexHypothesisChallenge, state, cookie string) (service.CodexHypothesisExchange, error) {
			count := inFlight.Add(1)
			for previous := maximum.Load(); count > previous; previous = maximum.Load() {
				if maximum.CompareAndSwap(previous, count) {
					break
				}
			}
			defer inFlight.Add(-1)
			index, _ := strconv.Atoi(challenge.Prompt)
			if state == "" {
				entered <- index
				select {
				case <-releases[index]:
				case <-ctx.Done():
					return service.CodexHypothesisExchange{RequestPrepared: true}, ctx.Err()
				}
				prediction := "gpt-5.6-luna"
				if index == 2 || index == 3 {
					prediction = "gpt-6-astra"
				}
				return service.CodexHypothesisExchange{Output: service.CodexHypothesisOutput{Text: prediction}, TicketState: "state-" + challenge.Prompt, TicketCookie: "cookie=" + challenge.Prompt, StatusCode: 200, RequestPrepared: true}, nil
			}
			verified <- [2]string{state, cookie}
			prediction := "gpt-6-astra"
			if index == 6 {
				prediction = "gpt-5.6-luna"
			}
			return service.CodexHypothesisExchange{Output: service.CodexHypothesisOutput{Text: prediction}, RequestState: state, RequestCookie: cookie, StatusCode: 200, RequestPrepared: true}, nil
		},
		score: func(_ context.Context, result service.CodexHypothesisOutput) (modelTraceResult, error) {
			return modelTraceResult{Prediction: result.Text, UsedOutputs: 1}, nil
		},
		wait: func(ctx context.Context, duration time.Duration, _ io.Writer) error {
			waitMu.Lock()
			waits = append(waits, duration)
			waitMu.Unlock()
			return waitWithHeartbeat(ctx, duration, io.Discard)
		},
	}
	ended := make(chan error, 1)
	go func() { ended <- watch(ctx, "gpt-6-astra", out, deps) }()
	for index := 0; index < 3; index++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("baseline did not reach three in-flight requests")
		}
	}
	close(releases[2])
	select {
	case <-out.pinned:
	case <-ctx.Done():
		t.Fatal("matching ticket not selected")
	}
	close(releases[1])
	close(releases[3])
	for index := 0; index < 3; index++ {
		select {
		case state := <-verified:
			require.Equal(t, [2]string{"state-2", "cookie=2"}, state)
		case <-ctx.Done():
			t.Fatal("three validations did not start")
		}
	}
	select {
	case <-out.finished:
	case <-ctx.Done():
		t.Fatal("validations did not finish")
	}
	cancel()
	require.NoError(t, <-ended)
	require.EqualValues(t, 3, maximum.Load())
	require.Contains(t, out.String(), "票据已固定，此结果不替换它")
	require.Contains(t, out.String(), "匹配率：2/3 = 66.67%")
	waitMu.Lock()
	defer waitMu.Unlock()
	require.NotEmpty(t, waits)
	for _, duration := range waits {
		require.Equal(t, hypothesisPause, duration)
	}
	for _, block := range strings.Split(out.String(), "╭─ 验证 #")[1:] {
		require.Contains(t, strings.SplitN(block, "╰─", 2)[0], "两个 cookie：")
	}
}

func TestCodexHypothesisConcurrentRetriesErrorsAt100ms(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	out := &concurrentOutput{}
	var requests, waits atomic.Int32
	deps := watchDependencies{
		challenge: func(context.Context) (service.CodexHypothesisChallenge, error) {
			return service.CodexHypothesisChallenge{Prompt: "challenge", ExpectedCount: 292}, nil
		},
		probe: func(_ context.Context, _ service.CodexHypothesisChallenge, _, _ string) (service.CodexHypothesisExchange, error) {
			attempt := requests.Add(1)
			if attempt == 5 {
				return service.CodexHypothesisExchange{RequestPrepared: true}, errors.New("network unavailable")
			}
			status := []int{401, 403, 429, 500}[(attempt-1)%4]
			return service.CodexHypothesisExchange{StatusCode: status, RetryAfter: "3600", RequestPrepared: true}, errors.New("upstream error")
		},
		wait: func(ctx context.Context, duration time.Duration, _ io.Writer) error {
			if duration != hypothesisPause {
				t.Errorf("wait=%s", duration)
			}
			waits.Add(1)
			return waitWithHeartbeat(ctx, duration, io.Discard)
		},
	}
	require.NoError(t, watch(ctx, "gpt-6-astra", out, deps))
	require.GreaterOrEqual(t, requests.Load(), int32(5))
	require.GreaterOrEqual(t, waits.Load(), int32(3))
	for _, status := range []string{"401", "403", "429", "500"} {
		require.Contains(t, out.String(), "HTTP："+status)
	}
	require.Contains(t, out.String(), "Retry-After：3600（本实验明确忽略")
	require.Contains(t, out.String(), "network unavailable")
}

func TestCodexHypothesisProxyConfigurationStopsAllWorkers(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		proxyURL string
		probeErr error
		message  string
	}{
		{
			name: "SOCKS protocol mismatch", proxyURL: "socks5://user:secret@proxy.example:3010",
			probeErr: errors.New("socks connect tcp user:secret@proxy.example:3010->chatgpt.com:443: unexpected protocol version 72"),
			message:  "代理配置错误：socks5://proxy.example:3010",
		},
		{
			name: "HTTP CONNECT forbidden", proxyURL: "http://user:secret@proxy.example:3010",
			probeErr: fmt.Errorf("upstream request failed: %w", &url.Error{Op: "Post", URL: "https://chatgpt.com/backend-api/codex/responses", Err: errors.New("403 Forbidden")}),
			message:  "代理拒绝 CONNECT：http://proxy.example:3010 返回 403 Forbidden",
		},
		{
			name: "HTTP proxy auth required", proxyURL: "http://user:secret@proxy.example:3010",
			probeErr: &url.Error{Op: "Post", URL: "https://chatgpt.com/backend-api/codex/responses", Err: errors.New("407 Proxy Authentication Required")},
			message:  "代理拒绝 CONNECT：http://proxy.example:3010 返回 407 Proxy Authentication Required",
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var requests, waits atomic.Int32
			out := &concurrentOutput{}
			deps := watchDependencies{
				proxyURL: scenario.proxyURL,
				challenge: func(context.Context) (service.CodexHypothesisChallenge, error) {
					return service.CodexHypothesisChallenge{Prompt: "challenge", ExpectedCount: 292}, nil
				},
				probe: func(context.Context, service.CodexHypothesisChallenge, string, string) (service.CodexHypothesisExchange, error) {
					requests.Add(1)
					return service.CodexHypothesisExchange{RequestPrepared: true}, scenario.probeErr
				},
				wait: func(context.Context, time.Duration, io.Writer) error {
					waits.Add(1)
					return nil
				},
			}
			err := watch(ctx, "gpt-6-astra", out, deps)
			require.ErrorContains(t, err, scenario.message)
			require.LessOrEqual(t, requests.Load(), int32(hypothesisWorkers))
			require.Zero(t, waits.Load())
			require.Contains(t, out.String(), scenario.message)
			require.NotContains(t, out.String(), "user:secret")
		})
	}
	require.Nil(t, proxyProtocolError("http://user:secret@proxy.example:3010", errors.New("upstream HTTP 403: inconclusive")))
	require.Nil(t, proxyProtocolError("socks5://user:secret@proxy.example:3010", errors.New("network unavailable")))
}

func TestCodexHypothesisConcurrentDeadlineAndLocalError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	out := &concurrentOutput{}
	var started atomic.Int32
	deps := watchDependencies{
		challenge: func(context.Context) (service.CodexHypothesisChallenge, error) {
			return service.CodexHypothesisChallenge{Prompt: "challenge", ExpectedCount: 292}, nil
		},
		probe: func(ctx context.Context, _ service.CodexHypothesisChallenge, _, _ string) (service.CodexHypothesisExchange, error) {
			started.Add(1)
			<-ctx.Done()
			return service.CodexHypothesisExchange{RequestPrepared: true}, ctx.Err()
		},
		wait: waitWithHeartbeat,
	}
	require.NoError(t, watch(ctx, "gpt-6-astra", out, deps))
	require.EqualValues(t, 3, started.Load())
	require.Contains(t, out.String(), "已达到运行上限")
	localCtx, localCancel := context.WithTimeout(context.Background(), time.Second)
	defer localCancel()
	deps.challenge = func(context.Context) (service.CodexHypothesisChallenge, error) {
		return service.CodexHypothesisChallenge{}, errors.New("Node unavailable")
	}
	err := runStage(localCtx, "找票", "gpt-6-astra", &watchStats{}, &watchLog{output: out}, deps)
	require.ErrorContains(t, err, "Node unavailable")
}
