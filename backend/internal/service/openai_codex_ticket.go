package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/MACOS-DO/sub4api/internal/pkg/logger"
	"github.com/MACOS-DO/sub4api/internal/pkg/openai"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	openAICodexTicketExtraKeyPrefix  = "codex_turn_ticket:"
	openAICodexAstraMinVersion       = "0.153.4"
	openAICodexTicketStatePrefix     = "gAAAAA"
	openAICodexTicketDefaultModel    = "gpt-6-astra"
	openAICodexTicketDefaultSolModel = "gpt-5.6-sol"

	// 票据有效期设置边界：最少 1 分钟，避免产生剧烈的打票循环。
	openAICodexTicketMinTTLSeconds     = 60
	openAICodexTicketMaxTTLSeconds     = 86400
	openAICodexTicketDefaultTTLSeconds = 200

	// 票据过期后最长复用时长：0 表示不限制，默认 600 秒。
	openAICodexTicketDefaultReuseWindowSeconds = 600
)

// normalizeOpenAICodexTicketTTLSeconds 归一化票据有效期：非正数回落默认值，
// 越界夹取到 [60, 86400]。
func normalizeOpenAICodexTicketTTLSeconds(seconds int) int {
	if seconds <= 0 {
		return openAICodexTicketDefaultTTLSeconds
	}
	if seconds < openAICodexTicketMinTTLSeconds {
		return openAICodexTicketMinTTLSeconds
	}
	if seconds > openAICodexTicketMaxTTLSeconds {
		return openAICodexTicketMaxTTLSeconds
	}
	return seconds
}

// normalizeOpenAICodexTicketReuseWindowSeconds 归一化过期后最长复用时长：
// 负数回退默认值，越界夹取到 [0, 86400]；0 表示不限制。
func normalizeOpenAICodexTicketReuseWindowSeconds(seconds int) int {
	if seconds < 0 {
		return openAICodexTicketDefaultReuseWindowSeconds
	}
	if seconds > openAICodexTicketMaxTTLSeconds {
		return openAICodexTicketMaxTTLSeconds
	}
	return seconds
}

// ErrOpenAICodexTicketUnavailable 表示该号该模型没有符合账号规则的有效门票，
// 且 fail_closed 禁止裸打业务请求。
var ErrOpenAICodexTicketUnavailable = errors.New("codex turn-state ticket unavailable")

type openAICodexTicket struct {
	AccountID          int64     `json:"account_id"`
	GenerationID       string    `json:"generation_id,omitempty"`
	VerificationMethod string    `json:"verification_method,omitempty"`
	FingerprintCommit  string    `json:"fingerprint_commit,omitempty"`
	Model              string    `json:"model"`
	State              string    `json:"state"`
	Length             int       `json:"length"`
	CapturedAt         time.Time `json:"captured_at"`
	ExpiresAt          time.Time `json:"expires_at"`
	Attempts           int       `json:"attempts"`
	HTTPStatus         int       `json:"http_status,omitempty"`
	// Cookie 是打票成功时上游 Set-Cookie 的 name=value 拼装结果，
	// 与 turn-state 同生命周期存取并在注入票据时一并复用。
	Cookie string `json:"cookie,omitempty"`
}

func openAICodexTicketKey(accountID int64, model string) string {
	return fmt.Sprintf("%d\x00%s", accountID, strings.TrimSpace(model))
}

func openAICodexTicketExtraKey(model string) string {
	return openAICodexTicketExtraKeyPrefix + strings.TrimSpace(model)
}

func normalizeOpenAICodexTicketModel(model string) string {
	return strings.TrimSpace(model)
}

func extractOpenAICodexTicketModel(body []byte) string {
	return normalizeOpenAICodexTicketModel(gjson.GetBytes(body, "model").String())
}

func (s *OpenAIGatewayService) openAICodexTicketConfig() config.OpenAICodexTicketConfig {
	cfg := config.OpenAICodexTicketConfig{}
	if s != nil && s.cfg != nil {
		cfg = s.cfg.Gateway.OpenAICodexTicket
	}
	if cfg.HarvestProbeIntervalSeconds <= 0 {
		cfg.HarvestProbeIntervalSeconds = 6
	}
	if cfg.HarvestAttemptTimeoutSeconds <= 0 {
		cfg.HarvestAttemptTimeoutSeconds = 90
	}
	if cfg.HarvestRetryMinSeconds <= 0 {
		cfg.HarvestRetryMinSeconds = 10
	}
	if cfg.HarvestRetryMaxSeconds <= 0 {
		cfg.HarvestRetryMaxSeconds = 30
	}
	if cfg.HarvestRetryMaxSeconds < cfg.HarvestRetryMinSeconds {
		cfg.HarvestRetryMaxSeconds = cfg.HarvestRetryMinSeconds
	}
	cfg.Models, _ = ModelTraceGPTModels()
	return cfg
}

func (s *OpenAIGatewayService) openAICodexTicketGatedModel(model string) bool {
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" || !s.openAICodexTicketEnabled() {
		return false
	}
	for _, item := range s.openAICodexTicketConfig().Models {
		if normalizeOpenAICodexTicketModel(item) == model {
			return true
		}
	}
	return false
}

// OpenAICodexTicketStatus 是给管理端看的门票摘要，不含 state blob。
type OpenAICodexTicketStatus struct {
	Model                string     `json:"model"`
	Length               int        `json:"length"`
	CapturedAt           *time.Time `json:"captured_at,omitempty"`
	TurnStatePresent     bool       `json:"turn_state_present"`
	CookiePresent        bool       `json:"cookie_present"`
	FingerprintCommit    string     `json:"fingerprint_commit,omitempty"`
	Ready                bool       `json:"ready"`
	RemainingSeconds     int64      `json:"remaining_seconds"`
	Blocked              bool       `json:"blocked"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	ExpectedTicketLength int        `json:"expected_ticket_length"`
	HarvestPaused        bool       `json:"harvest_paused"`
	HarvestEnabled       bool       `json:"harvest_enabled"`
	QuotaResetAt         *time.Time `json:"quota_reset_at,omitempty"`
	HarvestResumeAt      *time.Time `json:"harvest_resume_at,omitempty"`
	// ReusingExpired 为 true 表示当前请求正沿用已过期的上次票据（有效期已过但策略允许兜底）。
	ReusingExpired bool `json:"reusing_expired,omitempty"`
}

func OpenAICodexTicketStatuses(account *Account, cfg config.OpenAICodexTicketConfig, now time.Time) []OpenAICodexTicketStatus {
	if !isOpenAICodexTicketAccount(account) {
		return nil
	}
	models, err := ModelTraceGPTModels()
	if err != nil {
		return nil
	}
	quota := openAICodexTicketQuota(account, now)
	out := make([]OpenAICodexTicketStatus, 0, len(models))
	for _, model := range models {
		status := OpenAICodexTicketStatus{Model: model, HarvestPaused: quota.paused, QuotaResetAt: quota.resetAt, HarvestResumeAt: quota.resumeAt, HarvestEnabled: cfg.Enabled && account.Status == StatusActive && CodexTicketHarvestEnabled(account, model)}
		var ticket *openAICodexTicket
		if account.Extra != nil {
			ticket = parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
		}
		if ticket.structurallyValid(0) {
			status.Ready = true
			status.Length = ticket.Length
			status.CapturedAt = &ticket.CapturedAt
			status.TurnStatePresent = ticket.State != ""
			status.CookiePresent = ticket.Cookie != ""
			status.FingerprintCommit = ticket.FingerprintCommit
		}
		status.Blocked = !OpenAICodexAllowsWithoutTicket(account, !cfg.FailClosed) && !status.Ready
		out = append(out, status)
	}
	return out
}

func (s *OpenAIGatewayService) openAICodexTicketEnabled() bool {
	return s.openAICodexTicketEnabledContext(context.Background())
}

func (s *OpenAIGatewayService) openAICodexTicketEnabledContext(ctx context.Context) bool {
	if s == nil {
		return false
	}
	fallback := s.cfg != nil && s.cfg.Gateway.OpenAICodexTicket.Enabled
	if s.settingService != nil {
		return s.settingService.GetOpenAICodexTicketEnabled(ctx, fallback)
	}
	return fallback
}

// structurallyValid 只校验票据形状（长度规则与 gAAAAA 前缀），不看有效期。
func (t *openAICodexTicket) structurallyValid(_ int) bool {
	return t != nil && t.GenerationID != "" && t.VerificationMethod == "modeltrace_v1" && t.FingerprintCommit != "" && (strings.TrimSpace(t.State) != "" || strings.TrimSpace(t.Cookie) != "")
}

func (t *openAICodexTicket) usable(_ time.Time, _ int, _ bool, _ time.Duration) bool {
	return t.structurallyValid(0)
}

func (t *openAICodexTicket) valid(_ time.Time, _ int) bool {
	return t.structurallyValid(0)
}

func (s *OpenAIGatewayService) lookupOpenAICodexTicket(account *Account, model string) *openAICodexTicket {
	if s == nil || account == nil || account.ID <= 0 {
		return nil
	}
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" {
		return nil
	}
	key := openAICodexTicketKey(account.ID, model)
	var ticket *openAICodexTicket
	if account.Extra != nil {
		ticket = parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
	}
	if ticket == nil {
		s.openaiCodexTickets.Delete(key)
		return nil
	}
	s.openaiCodexTickets.Store(key, ticket)
	return ticket
}

func parseOpenAICodexTicketFromAny(accountID int64, model string, raw any) *openAICodexTicket {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var ticket openAICodexTicket
	if err := json.Unmarshal(b, &ticket); err != nil {
		return nil
	}
	ticket.AccountID = accountID
	if strings.TrimSpace(model) != "" {
		ticket.Model = model
	}
	ticket.State = strings.TrimSpace(ticket.State)
	if ticket.Length == 0 {
		ticket.Length = len(ticket.State)
	}
	if !ticket.structurallyValid(0) {
		return nil
	}
	return &ticket
}

func (s *OpenAIGatewayService) storeOpenAICodexTicket(ctx context.Context, account *Account, ticket *openAICodexTicket) error {
	if s == nil || account == nil || ticket == nil || account.ID <= 0 || s.accountRepo == nil {
		return ErrCodexTicketUnavailable
	}
	model := normalizeOpenAICodexTicketModel(ticket.Model)
	ticket.Model = model
	ticket.AccountID = account.ID
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
		openAICodexTicketExtraKey(model): ticket,
	}); err != nil {
		return err
	}
	s.openaiCodexTickets.Store(openAICodexTicketKey(account.ID, model), ticket)
	return nil
}

// applyOpenAICodexTicket 在出站请求上覆盖 x-codex-turn-state。
// 请求路径只注入已捕获的有效门票，不现场打票；无票则返回
// ErrOpenAICodexTicketUnavailable。打票由后台 harvester 完成。
func (s *OpenAIGatewayService) applyOpenAICodexTicket(ctx context.Context, account *Account, model string, h http.Header) error {
	_, err := s.applyOpenAICodexTicketWithGeneration(ctx, account, model, h)
	return err
}

func (s *OpenAIGatewayService) applyOpenAICodexTicketWithGeneration(ctx context.Context, account *Account, model string, h http.Header) (*openAICodexTicket, error) {
	if s == nil || h == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabledContext(ctx) {
		return nil, nil
	}
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" || !s.openAICodexTicketGatedModel(model) {
		return nil, nil
	}
	var ticket *openAICodexTicket
	if s.openaiCodexTicketLifecycle != nil {
		raw, err := s.openaiCodexTicketLifecycle.CurrentTicket(ctx, account.ID, model)
		if err != nil {
			return nil, err
		}
		if len(raw) > 0 && string(raw) != "null" {
			var current openAICodexTicket
			if err := json.Unmarshal(raw, &current); err != nil {
				return nil, err
			}
			ticket = parseOpenAICodexTicketFromAny(account.ID, model, current)
		}
	} else {
		ticket = s.lookupOpenAICodexTicket(account, model)
	}
	if ticket.usable(time.Now(), 0, false, 0) {
		if ticket.State != "" {
			h.Set(openAICodexTurnStateHeader, ticket.State)
		} else {
			h.Del(openAICodexTurnStateHeader)
		}
		applyOpenAICodexTicketCookie(h, ticket)
		return ticket, nil
	}
	if s.openAICodexAllowsWithoutTicket(ctx, account) {
		return nil, nil
	}
	return nil, ErrOpenAICodexTicketUnavailable
}

// applyOpenAICodexTicketCookie 把打票成功时捕获的 Cookie 与票据一并复用。
// 已有 Cookie（当前出站路径默认不透传客户端 Cookie）按追加处理，避免覆盖其他来源。
func applyOpenAICodexTicketCookie(h http.Header, ticket *openAICodexTicket) {
	if h == nil || ticket == nil {
		return
	}
	cookie := strings.TrimSpace(ticket.Cookie)
	if cookie == "" {
		return
	}
	if existing := strings.TrimSpace(h.Get("Cookie")); existing != "" {
		h.Set("Cookie", existing+"; "+cookie)
		return
	}
	h.Set("Cookie", cookie)
}

// openAICodexTicketOutboundModel 预测本请求真正出站的模型名，也就是
// applyOpenAICodexTicket 注入时读到的 body.model。
//
// 调度门控与注入必须按同一个模型名判定门票。普通请求下二者同源：Forward 的
// upstreamModel 与本函数都走 resolveOpenAIAccountUpstreamModelForRequest，且
// Forward 会把 body.model 改写成该值后才注入。但 /responses/compact 例外——
// Forward 会把出站模型进一步改写为 compact 映射或 gateway.openai_compact_model
// （默认非空），此时若门控仍按客户端原始模型判定，就会把「实际出站是非门控
// 模型、根本不需要票」的 compact 请求整片误拦成不可调度。
func (s *OpenAIGatewayService) openAICodexTicketOutboundModel(account *Account, requestedModel string, requireCompact bool) string {
	model := strings.TrimSpace(requestedModel)
	if account == nil || model == "" {
		return model
	}
	if !account.IsOpenAI() {
		return canonicalOpenAIAccountSchedulingModel(account, model)
	}
	_, upstreamModel := resolveOpenAIForwardMappedModels(account, model, requireCompact)
	if requireCompact {
		// 与 Forward 同序：compact 兜底模型优先于普通/compact 映射结果。
		if compactModel := strings.TrimSpace(s.resolveOpenAICompactFallbackModel(account, model)); compactModel != "" {
			upstreamModel = compactModel
		}
	}
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
		return upstreamModel
	}
	return model
}

// outboundModel 必须是真正会发给上游的模型名（openAICodexTicketOutboundModel），
// 不是客户端原始模型：注入侧读的是出站 body.model，两侧口径必须一致。
func (s *OpenAIGatewayService) openAICodexTicketBlocksAccount(account *Account, outboundModel string) bool {
	if s == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabled() {
		return false
	}
	if s.openAICodexAllowsWithoutTicket(context.Background(), account) {
		return false
	}
	model := normalizeOpenAICodexTicketModel(outboundModel)
	if !s.openAICodexTicketGatedModel(model) {
		return false
	}
	ticket := s.lookupOpenAICodexTicket(account, model)
	return !ticket.usable(time.Now(), 0, false, 0)
}

func (s *OpenAIGatewayService) fireOpenAICodexTicketProbe(ctx context.Context, account *Account, token, model, proxyURL string, expectedCount int, attemptTimeout time.Duration) (output string, state string, cookie string, status int, err error) {
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	prompt := ModelTraceChallengePrompt(expectedCount)
	body, err := json.Marshal(map[string]any{"model": model, "store": false, "stream": true, "input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": prompt}}}}})
	if err != nil {
		return "", "", "", 0, err
	}
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, chatgptCodexURL, bytes.NewReader(body))
	if err != nil {
		return "", "", "", 0, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAIHarvest))
	req.Close = true
	req.Host = "chatgpt.com"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("session_id", uuid.NewString())
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(attemptCtx, s.accountRepo, req.Header, account); err != nil {
		return "", "", "", 0, err
	}
	applyOpenAICodexTicketHarvestIdentity(req.Header, model)
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return "", "", "", 0, err
	}
	if resp == nil {
		return "", "", "", 0, errors.New("nil upstream response")
	}
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	state, cookie, status = extractOpenAICodexTurnState(resp.Header), extractOpenAICodexResponseCookies(resp.Header), resp.StatusCode
	if resp.Body == nil {
		return "", state, cookie, status, nil
	}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 2<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var deltas strings.Builder
	var completed string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		eventType := gjson.Get(payload, "type").String()
		if eventType == "response.output_text.delta" {
			deltas.WriteString(gjson.Get(payload, "delta").String())
		}
		if eventType == "response.completed" {
			gjson.Get(payload, "response.output").ForEach(func(_, message gjson.Result) bool {
				message.Get("content").ForEach(func(_, part gjson.Result) bool {
					if part.Get("type").String() == "output_text" {
						completed += part.Get("text").String()
					}
					return true
				})
				return true
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return "", state, cookie, status, err
	}
	if deltas.Len() > 0 {
		return deltas.String(), state, cookie, status, nil
	}
	return completed, state, cookie, status, nil
}

func extractOpenAICodexResponseCookies(h http.Header) string {
	if h == nil || len(h.Values("Set-Cookie")) == 0 {
		return ""
	}
	parsed := (&http.Response{Header: h}).Cookies()
	if len(parsed) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(parsed))
	seen := make(map[string]bool, len(parsed))
	for _, item := range parsed {
		if item == nil || item.Name == "" || seen[item.Name] {
			continue
		}
		seen[item.Name] = true
		pairs = append(pairs, item.Name+"="+item.Value)
	}
	return strings.Join(pairs, "; ")
}

func jsonString(v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(b)
}

func applyOpenAICodexTicketHarvestIdentity(h http.Header, model string) {
	ensureCodexIdentityHeaders(h)
	enforceCodexIdentityHeaders(h)
	version := strings.TrimSpace(h.Get("version"))
	if needsOpenAICodexAstraVersion(model) && (version == "" || CompareVersions(version, openAICodexAstraMinVersion) < 0) {
		h.Set("version", openAICodexAstraMinVersion)
		h.Set("user-agent", buildCodexCLIUserAgent(openAICodexAstraMinVersion))
		h.Set("originator", openai.CodexDefaultOriginator)
	}
}

func needsOpenAICodexAstraVersion(model string) bool {
	m := strings.ToLower(normalizeOpenAICodexTicketModel(model))
	return strings.Contains(m, "gpt-6") || strings.Contains(m, "astra")
}

func (s *OpenAIGatewayService) StartOpenAICodexTicketHarvester() {
	if s == nil {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	defer s.openaiCodexTicketLifecycleMu.Unlock()
	if s.openaiCodexTicketStopped || s.openaiCodexTicketDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.openaiCodexTicketCancel = cancel
	s.openaiCodexTicketDone = done
	go func() {
		defer close(done)
		s.openAICodexTicketHarvestLoop(ctx)
	}()
	logger.L().Info("openai_codex_ticket harvester started",
		zap.Int("target_length", s.openAICodexTicketConfig().TargetLength),
		zap.Strings("models", s.openAICodexTicketConfig().Models),
	)
}

func (s *OpenAIGatewayService) StopOpenAICodexTicketHarvester() {
	if s == nil {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	s.openaiCodexTicketStopped = true
	cancel, done := s.openaiCodexTicketCancel, s.openaiCodexTicketDone
	s.openaiCodexTicketLifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	lastCleanup := time.Time{}
	lastBankCheck := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if s.settingService != nil && time.Since(lastBankCheck) >= time.Hour {
				lastBankCheck = time.Now()
				updateCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if _, err := s.settingService.RefreshModelTraceBank(updateCtx); err != nil && ctx.Err() == nil {
					logger.L().Warn("ModelTrace fingerprint update failed; retaining last good version", zap.Error(err))
				}
				cancel()
			}
			if s.openaiCodexTicketHistory != nil && time.Since(lastCleanup) >= time.Hour {
				lastCleanup = time.Now()
				if err := s.openaiCodexTicketHistory.Cleanup(ctx); err != nil && ctx.Err() == nil {
					logger.L().Warn("openai_codex_ticket history cleanup failed", zap.Error(err))
				}
			}
			s.refreshOpenAICodexTickets(ctx)
			timer.Reset(s.openAICodexTicketNextScanDelay(time.Now()))
		}
	}
}

// Keep the configured scan cadence for account/config changes, but wake exactly
// when a scheduled retry falls inside that interval. This prevents the scanner
// cadence from stretching a requested 20-40 second retry beyond its upper bound.
func (s *OpenAIGatewayService) openAICodexTicketNextScanDelay(now time.Time) time.Duration {
	delay := time.Duration(s.openAICodexTicketConfig().HarvestProbeIntervalSeconds) * time.Second
	s.openaiCodexTicketNextAttempt.Range(func(_, value any) bool {
		next, ok := value.(time.Time)
		if !ok {
			return true
		}
		until := next.Sub(now)
		if until > 0 && until < delay {
			delay = until
		}
		return true
	})
	if delay <= 0 {
		return time.Second
	}
	return delay
}

// refreshOpenAICodexTickets probes each account/model with a missing or soon-to-expire
// ticket once. The loop waits for all probes, then waits the configured interval
// before starting the next cycle.
func (s *OpenAIGatewayService) refreshOpenAICodexTickets(ctx context.Context) {
	if s == nil || s.accountRepo == nil || ctx.Err() != nil || !s.openAICodexTicketEnabledContext(ctx) {
		return
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		logger.L().Warn("openai_codex_ticket list accounts failed", zap.Error(err))
		return
	}
	cfg := s.openAICodexTicketConfig()
	now := time.Now()
	var wg sync.WaitGroup
	probed := 0
	for i := range accounts {
		account := accounts[i]
		if !isOpenAICodexTicketAccount(&account) || account.Status != StatusActive || account.Extra[codexTicketAccountEnabledKey] == false {
			continue
		}
		if openAICodexTicketQuota(&account, now).paused {
			continue
		}
		for _, model := range cfg.Models {
			model := normalizeOpenAICodexTicketModel(model)
			if model == "" {
				continue
			}
			if !CodexTicketHarvestEnabled(&account, model) {
				continue
			}
			if !s.codexTicketAutomaticDue(&account, model, now) {
				continue
			}
			acc := account
			// Token/header helpers may update account metadata; each model owns its maps.
			acc.Extra = maps.Clone(account.Extra)
			acc.Credentials = maps.Clone(account.Credentials)
			probed++
			wg.Add(1)
			go func(acc Account, model string) {
				defer wg.Done()
				s.probeOnceOpenAICodexTicket(ctx, &acc, model)
			}(acc, model)
		}
	}
	wg.Wait()
	if probed > 0 {
		logger.L().Info("openai_codex_ticket probe cycle", zap.Int("probed", probed))
	}
}

// probeOnceOpenAICodexTicket 走打票代理打一发。HTTP 200/429 携带符合账号长度规则、
// gAAAAA 前缀的票据即落库；否则记 Info miss，交给下个周期重试。同一 key 并发去重，避免上一发还没
// 回来又叠一发。
func (s *OpenAIGatewayService) probeOnceOpenAICodexTicket(ctx context.Context, account *Account, model string) {
	if s == nil || !CodexTicketHarvestEnabled(account, model) || ctx.Err() != nil || !s.openAICodexTicketEnabledContext(ctx) {
		return
	}
	if openAICodexTicketQuota(account, time.Now()).paused {
		return
	}
	result, err := s.runCodexTicketAttempt(ctx, account, model, "automatic")
	if err != nil {
		if !errors.Is(err, ErrCodexTicketBusy) && !errors.Is(err, ErrCodexTicketNoProxy) {
			logger.L().Warn("openai_codex_ticket probe unavailable", zap.Int64("account_id", account.ID), zap.String("model", model), zap.Error(err))
		}
		return
	}
	if result.Outcome == "success" {
		logger.L().Info("openai_codex_ticket harvested", zap.Int64("account_id", account.ID), zap.String("model", model), zap.Int("http", *result.HTTPStatus), zap.String("mode", "continuous"))
	} else {
		logger.L().Info("openai_codex_ticket probe miss", zap.Int64("account_id", account.ID), zap.String("model", model), zap.String("reason", result.ReasonCode))
	}
}

// IsOpenAICodexTicketExtraKey identifies server-managed ticket material.
func IsOpenAICodexTicketExtraKey(key string) bool {
	return strings.HasPrefix(key, openAICodexTicketExtraKeyPrefix)
}

// MergeOpenAICodexTicketExtra preserves only persisted tickets, never summaries or
// blobs supplied by an account edit. The repository repeats this under the row
// lock so a concurrent harvest cannot be overwritten by a stale admin snapshot.
func MergeOpenAICodexTicketExtra(extra, current map[string]any) map[string]any {
	result := maps.Clone(extra)
	for key := range result {
		if IsOpenAICodexTicketExtraKey(key) {
			delete(result, key)
		}
	}
	for key, value := range current {
		if IsOpenAICodexTicketExtraKey(key) {
			if result == nil {
				result = make(map[string]any)
			}
			result[key] = value
		}
	}
	return result
}

// ValidateOpenAICodexTicketHarvestProxyURL validates only syntax, without making
// a network request or including credentials in validation errors.
func ValidateOpenAICodexTicketHarvestProxyURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("harvest proxy must be an HTTP(S) or SOCKS5(h) URL with a host and no path, query or fragment")
	}
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return errors.New("harvest proxy scheme must be http, https, socks5 or socks5h")
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("harvest proxy port must be between 1 and 65535")
		}
	}
	return nil
}

// MaskProxyURL never returns a stored proxy password, even for invalid legacy data.
func MaskProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || ValidateOpenAICodexTicketHarvestProxyURL(raw) != nil {
		return ""
	}
	parsed, _ := url.Parse(raw)
	if parsed.User != nil {
		if _, ok := parsed.User.Password(); ok {
			parsed.User = url.UserPassword(parsed.User.Username(), "***")
		}
	}
	return parsed.String()
}

// IsMaskedProxyURL recognizes the exact password placeholder emitted by the API.
func IsMaskedProxyURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return false
	}
	password, ok := parsed.User.Password()
	return ok && password == "***"
}

// Credential shadows do not own tickets. Keep their existing forwarding policy
// instead of imposing a gate for a key the harvester never populates.
func isOpenAICodexTicketAccount(account *Account) bool {
	return account != nil && account.IsOpenAIOAuthLike() && !account.IsShadow()
}

// IsOpenAICodexTicketPrivateExtraKey also covers the retired account-level proxy
// override, whose credentials may remain in older account records.
func IsOpenAICodexTicketPrivateExtraKey(key string) bool {
	return IsOpenAICodexTicketExtraKey(key) || key == "codex_harvest_proxy_url"
}

// RedactOpenAICodexTicketExtra strips ephemeral ticket material from exports
// without changing the source account or unrelated backup fields.
func RedactOpenAICodexTicketExtra(extra map[string]any) map[string]any {
	redacted := maps.Clone(extra)
	for key := range redacted {
		if IsOpenAICodexTicketPrivateExtraKey(key) {
			delete(redacted, key)
		}
	}
	return redacted
}

// OpenAICodexAllowsWithoutTicket applies an explicit account override to the global default.
func OpenAICodexAllowsWithoutTicket(account *Account, globalDefault bool) bool {
	if account != nil {
		if allow, ok := account.Extra["codex_allow_without_ticket"].(bool); ok {
			return allow
		}
	}
	return globalDefault
}

func (s *OpenAIGatewayService) openAICodexAllowsWithoutTicket(ctx context.Context, account *Account) bool {
	allow := !s.openAICodexTicketConfig().FailClosed
	if s.settingService != nil {
		allow = s.settingService.GetOpenAICodexTicketAllowWithoutTicket(ctx, allow)
	}
	return OpenAICodexAllowsWithoutTicket(account, allow)
}
