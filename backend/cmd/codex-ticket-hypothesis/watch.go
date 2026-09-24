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
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"
)

const hypothesisWorkers = 3
const hypothesisPause = 100 * time.Millisecond

type watchDependencies struct {
	started   time.Time
	proxyURL  string
	challenge func(context.Context) (service.CodexHypothesisChallenge, error)
	probe     func(context.Context, service.CodexHypothesisChallenge, string, string) (service.CodexHypothesisExchange, error)
	score     func(context.Context, service.CodexHypothesisOutput) (modelTraceResult, error)
	wait      func(context.Context, time.Duration, io.Writer) error
}

type watchStats struct {
	started       time.Time
	ticketAt      time.Time
	probes        int
	verifications int
	valid         int
	matched       int
	pinnedState   string
	pinnedCookie  string
}

type watchLog struct {
	mu     sync.Mutex
	output io.Writer
}

func (log *watchLog) Write(text []byte) (int, error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.output.Write(text)
}

type challengeResult struct {
	workerID int
	index    int
	started  time.Time
	elapsed  time.Duration
	exchange service.CodexHypothesisExchange
	score    modelTraceResult
	probeErr error
	scoreErr error
	fatalErr error
	ack      chan bool
}

func watch(ctx context.Context, model string, output io.Writer, deps watchDependencies) error {
	started := deps.started
	if started.IsZero() {
		started = time.Now()
	}
	stats := watchStats{started: started}
	log := &watchLog{output: output}
	defer func() { printSummary(log, stats) }()
	if err := runStage(ctx, "找票", model, &stats, log, deps); err != nil {
		return err
	}
	if stats.pinnedState != "" && ctx.Err() == nil {
		fmt.Fprintln(log, "\n★ 找票请求已全部结束，开始 3 工位并发验证同一份冻结票据")
		if err := runStage(ctx, "验证", model, &stats, log, deps); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		fmt.Fprintf(log, "⏱ 已达到运行上限，停止发送挑战（%v）\n", ctx.Err())
	}
	return nil
}

func runStage(ctx context.Context, phase, model string, stats *watchStats, log *watchLog, deps watchDependencies) error {
	stageCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopDispatch := make(chan struct{})
	var dispatchMu sync.Mutex
	var stopOnce sync.Once
	stopNew := func() { dispatchMu.Lock(); stopOnce.Do(func() { close(stopDispatch) }); dispatchMu.Unlock() }
	pinnedState, pinnedCookie := stats.pinnedState, stats.pinnedCookie
	results := make(chan challengeResult, hypothesisWorkers)
	var sequence atomic.Int64
	var workers sync.WaitGroup
	for workerID := 1; workerID <= hypothesisWorkers; workerID++ {
		workers.Add(1)
		go func(workerID int) {
			defer workers.Done()
			if phase == "验证" {
				fmt.Fprintf(log, "工位 %d | 验证开始前固定等待 %s\n", workerID, hypothesisPause)
				if err := deps.wait(stageCtx, hypothesisPause, io.Discard); err != nil {
					return
				}
			}
			for stageCtx.Err() == nil {
				select {
				case <-stopDispatch:
					return
				default:
				}
				challenge, err := deps.challenge(stageCtx)
				if err != nil {
					if stageCtx.Err() == nil {
						results <- challengeResult{workerID: workerID, fatalErr: fmt.Errorf("generate challenge: %w", err)}
					}
					return
				}
				dispatchMu.Lock()
				select {
				case <-stopDispatch:
					dispatchMu.Unlock()
					return
				case <-stageCtx.Done():
					dispatchMu.Unlock()
					return
				default:
				}
				index := int(sequence.Add(1))
				started := time.Now()
				fmt.Fprintf(log, "\n▶ %s #%d | 工位 %d | %s | 挑战 %d 个数字 | 剩余 %s\n", phase, index, workerID, started.Format("15:04:05"), challenge.ExpectedCount, remaining(stageCtx))
				dispatchMu.Unlock()
				stopHeartbeat := make(chan struct{})
				heartbeatStopped := make(chan struct{})
				go func() {
					defer close(heartbeatStopped)
					ticker := time.NewTicker(15 * time.Second)
					defer ticker.Stop()
					for {
						select {
						case <-stopHeartbeat:
							return
						case <-ticker.C:
							fmt.Fprintf(log, "⏳ %s #%d | 工位 %d | 已耗时 %s | 剩余 %s\n", phase, index, workerID, time.Since(started).Round(time.Second), remaining(stageCtx))
						}
					}
				}()
				exchange, probeErr := deps.probe(stageCtx, challenge, pinnedState, pinnedCookie)
				close(stopHeartbeat)
				<-heartbeatStopped
				proxyErr := proxyProtocolError(deps.proxyURL, probeErr)
				if proxyErr != nil {
					probeErr = proxyErr
				}
				var score modelTraceResult
				var scoreErr error
				if probeErr == nil && stageCtx.Err() == nil {
					score, scoreErr = deps.score(stageCtx, exchange.Output)
				}
				result := challengeResult{workerID: workerID, index: index, started: started, elapsed: time.Since(started), exchange: exchange, score: score, probeErr: probeErr, scoreErr: scoreErr, ack: make(chan bool, 1)}
				if proxyErr != nil && exchange.StatusCode == 0 && stageCtx.Err() == nil {
					result.fatalErr = proxyErr
				}
				if probeErr != nil && exchange.StatusCode == 0 && !exchange.RequestPrepared && stageCtx.Err() == nil {
					result.fatalErr = fmt.Errorf("local request construction failed: %w", probeErr)
				}
				results <- result
				var proceed bool
				select {
				case proceed = <-result.ack:
				case <-stageCtx.Done():
					return
				}
				if !proceed || stageCtx.Err() != nil {
					return
				}
				fmt.Fprintf(log, "⏳ %s #%d | 工位 %d | 固定等待 %s 后重试\n", phase, index, workerID, hypothesisPause)
				if err := deps.wait(stageCtx, hypothesisPause, io.Discard); err != nil {
					return
				}
			}
		}(workerID)
	}
	go func() { workers.Wait(); close(results) }()
	var fatal error
	for result := range results {
		if result.index != 0 {
			var block bytes.Buffer
			fmt.Fprintf(&block, "\n╭─ %s #%d | 工位 %d | %s ────────────────────\n", phase, result.index, result.workerID, result.started.Format("15:04:05"))
			printExchange(&block, model, result.exchange, result.score, result.probeErr, result.scoreErr, result.elapsed)
			if phase == "找票" {
				stats.probes++
			} else {
				stats.verifications++
				if result.probeErr == nil && result.scoreErr == nil && result.score.Prediction != "" {
					stats.valid++
					if result.score.Prediction == model {
						stats.matched++
					}
					fmt.Fprintf(&block, "│ 当前匹配率：%d/%d\n", stats.matched, stats.valid)
				}
			}
			if phase == "找票" && stats.pinnedState == "" && result.probeErr == nil && result.scoreErr == nil && result.score.Prediction == model && result.exchange.TicketState != "" && result.exchange.TicketCookie != "" {
				stats.pinnedState, stats.pinnedCookie = result.exchange.TicketState, result.exchange.TicketCookie
				stats.ticketAt = time.Now()
				stopNew()
				fmt.Fprintf(&block, "│ ★ 首张匹配票据已固定（耗时 %s）；等待其余在途找票请求结束\n", stats.ticketAt.Sub(stats.started).Round(time.Second))
			} else if phase == "找票" && stats.pinnedState != "" {
				fmt.Fprintln(&block, "│ 找票阶段结果：票据已固定，此结果不替换它")
			}
			if result.exchange.StatusCode == http.StatusTooManyRequests && result.exchange.RetryAfter != "" {
				fmt.Fprintf(&block, "│ Retry-After：%s（本实验明确忽略，下一次仍固定等待 100ms）\n", result.exchange.RetryAfter)
			}
			fmt.Fprintln(&block, "╰───────────────────────────────────────────────")
			_, _ = log.Write(block.Bytes())
		}
		if result.fatalErr != nil && fatal == nil {
			fatal = result.fatalErr
			stopNew()
			cancel()
			fmt.Fprintf(log, "工位 %d | 本地错误，停止实验：%v\n", result.workerID, fatal)
		}
		if result.ack != nil {
			result.ack <- fatal == nil && stageCtx.Err() == nil && (phase == "验证" || stats.pinnedState == "")
		}
	}
	return fatal
}

func proxyProtocolError(proxyURL string, err error) error {
	if err == nil {
		return nil
	}
	parsed, parseErr := url.Parse(proxyURL)
	if parseErr != nil || parsed.Hostname() == "" {
		return nil
	}
	if (parsed.Scheme == "socks5" || parsed.Scheme == "socks5h") && strings.Contains(err.Error(), "unexpected protocol version ") {
		return fmt.Errorf("代理配置错误：%s://%s 返回了非 SOCKS5 握手响应；请核对代理协议和端口（HTTP 代理应使用 http://）", parsed.Scheme, parsed.Host)
	}
	if parsed.Scheme == "http" || parsed.Scheme == "https" {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			switch requestError.Err.Error() {
			case "403 Forbidden", "407 Proxy Authentication Required":
				return fmt.Errorf("代理拒绝 CONNECT：%s://%s 返回 %s；请检查代理账号、来源 IP 授权和目标地址限制", parsed.Scheme, parsed.Host, requestError.Err.Error())
			}
		}
	}
	return nil
}

func remaining(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	left := time.Until(deadline)
	if left < 0 {
		return 0
	}
	return left.Round(time.Second)
}

func waitWithHeartbeat(ctx context.Context, duration time.Duration, output io.Writer) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case <-ticker.C:
			fmt.Fprintf(output, "  ⏳ 等待下一次挑战，剩余运行时间 %s\n", remaining(ctx))
		}
	}
}

func printSummary(output io.Writer, stats watchStats) {
	fmt.Fprintln(output, "\n══════════════════ 一小时实验汇总 ══════════════════")
	fmt.Fprintf(output, "找票请求：%d | 验证请求：%d | 运行耗时：%s\n", stats.probes, stats.verifications, time.Since(stats.started).Round(time.Second))
	if stats.pinnedState == "" {
		fmt.Fprintln(output, "票据：未找到同时满足模型匹配、有效响应、turn-state 和 cookie 的票据")
	} else {
		fmt.Fprintf(output, "票据：已固定（首次命中耗时 %s）\n", stats.ticketAt.Sub(stats.started).Round(time.Second))
	}
	if stats.valid == 0 {
		fmt.Fprintln(output, "匹配率：无法计算（验证阶段没有有效评分）")
	} else {
		fmt.Fprintf(output, "匹配率：%d/%d = %.2f%%（只统计验证阶段有效评分）\n", stats.matched, stats.valid, float64(stats.matched)*100/float64(stats.valid))
	}
	fmt.Fprintln(output, "══════════════════════════════════════════════════")
}

func printExchange(output io.Writer, expected string, exchange service.CodexHypothesisExchange, score modelTraceResult, probeErr, scoreErr error, elapsed time.Duration) {
	status := "未收到"
	if exchange.StatusCode != 0 {
		status = strconv.Itoa(exchange.StatusCode)
	}
	fmt.Fprintf(output, "│ HTTP：%s | 用时：%s | 响应：", status, elapsed.Round(time.Millisecond))
	if probeErr != nil {
		fmt.Fprintf(output, "未完成（%v）\n", probeErr)
	} else {
		fmt.Fprintln(output, "完整")
	}
	fmt.Fprintln(output, "│ 【请求携带】")
	writeRaw(output, "期望模型", expected, "未配置")
	writeRaw(output, "x-codex-turn-state", exchange.RequestState, "未携带")
	writeRaw(output, "Cookie", exchange.RequestCookie, "未携带")
	fmt.Fprintln(output, "│ 【上游返回】")
	if scoreErr != nil {
		fmt.Fprintf(output, "│   ModelTrace：无法评分（%v）\n", scoreErr)
	}
	actual := score.Prediction
	if actual == "" {
		actual = "无法判定"
	}
	writeRaw(output, "实际模型（ModelTrace）", actual, "无法判定")
	if score.Prediction != "" {
		fmt.Fprintf(output, "│   归因概率：%.2f%%（单样本闭集归因）\n", score.Probability*100)
	}
	writeRaw(output, "x-codex-turn-state", exchange.ResponseState, "未返回")
	if len(exchange.ResponseCookies) == 0 {
		fmt.Fprintln(output, "│   Set-Cookie：未返回")
	}
	for index, cookie := range exchange.ResponseCookies {
		writeRaw(output, fmt.Sprintf("Set-Cookie #%d", index+1), cookie, "未返回")
	}
	fmt.Fprintln(output, "│ 【比较结果】")
	if score.Prediction == "" {
		fmt.Fprintln(output, "│   期望/实际模型：无法比较")
	} else {
		fmt.Fprintf(output, "│   期望/实际模型：%t\n", score.Prediction == expected)
	}
	fmt.Fprintf(output, "│   两个 turn-state：%s\n", compareStrings(exchange.RequestState, exchange.ResponseState))
	fmt.Fprintf(output, "│   两个 cookie：%s\n", compareCookies(exchange.RequestCookie, exchange.ResponseCookies))
}

func writeRaw(output io.Writer, label, value, absent string) {
	if value == "" {
		fmt.Fprintf(output, "│   %s：%s\n", label, absent)
		return
	}
	fmt.Fprintf(output, "│   %s：\n", label)
	for _, line := range strings.Split(value, "\n") {
		text := []rune(line)
		for len(text) > 96 {
			fmt.Fprintf(output, "│     %s\n", string(text[:96]))
			text = text[96:]
		}
		fmt.Fprintf(output, "│     %s\n", string(text))
	}
}

func compareStrings(request, response string) string {
	if request == "" || response == "" {
		return "无法比较（有一方未携带/未返回）"
	}
	if request == response {
		return "匹配"
	}
	return "不匹配"
}

func compareCookies(request string, responses []string) string {
	if request == "" || len(responses) == 0 {
		return "无法比较（有一方未携带/未返回）"
	}
	header := http.Header{}
	header.Set("Cookie", request)
	values := make(map[string]string)
	for _, cookie := range (&http.Request{Header: header}).Cookies() {
		values[cookie.Name] = cookie.Value
	}
	header = http.Header{}
	for _, item := range responses {
		header.Add("Set-Cookie", item)
	}
	compared := 0
	for _, cookie := range (&http.Response{Header: header}).Cookies() {
		value, ok := values[cookie.Name]
		if !ok {
			continue
		}
		compared++
		if value != cookie.Value {
			return "不匹配（同名 cookie 值不同）"
		}
	}
	if compared == 0 {
		return "无法比较（没有同名 cookie；Set-Cookie 不是 Cookie 的完整快照）"
	}
	return fmt.Sprintf("匹配（%d 个同名 cookie 的值相同）", compared)
}
