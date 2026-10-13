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

	// 每批并发打票的账号数默认值。
	openAICodexTicketDefaultBatchSize = 10

	// openAICodexTicketHarvestModel 是唯一的打票模型：打票请求用它发出，指纹也必须预测为它。
	// 打出的票全站共享，注入到所有门控模型的请求上。
	openAICodexTicketHarvestModel = "gpt-6-sol"
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

// openAICodexTicket 是全站唯一的共享票据：任一账号打出后，所有参与账号在所有门控模型上复用。
// 票据由 turn-state 与 Cookie 组成，AccountID 记录打出该票的来源账号，Model 记录打票模型。
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
	if cfg.HarvestBatchSize <= 0 {
		cfg.HarvestBatchSize = openAICodexTicketDefaultBatchSize
	}
	cfg.Models, _ = ModelTraceTicketModels()
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

// OpenAICodexTicketStatus 是给管理端看的共享票摘要，不含 state blob 与 Cookie 原文。
type OpenAICodexTicketStatus struct {
	Model                string     `json:"model"`
	Length               int        `json:"length"`
	CapturedAt           *time.Time `json:"captured_at,omitempty"`
	SourceAccountID      int64      `json:"source_account_id,omitempty"`
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
	// InvalidatedForAccount 表示共享票存在，但本账号已对这一代失效，正在等待新票。
	InvalidatedForAccount bool `json:"invalidated_for_account,omitempty"`
}

// OpenAICodexTicketStatusOf 以账号视角展示全局共享票。账号未参与打票、或本账号已对当前这一代失效时，
// Ready 为 false。
func OpenAICodexTicketStatusOf(account *Account, cfg config.OpenAICodexTicketConfig, shared CodexSharedTickets, now time.Time) *OpenAICodexTicketStatus {
	if !isOpenAICodexTicketAccount(account) {
		return nil
	}
	quota := openAICodexTicketQuota(account, now)
	participating := codexTicketParticipationEnabled(account)
	status := &OpenAICodexTicketStatus{
		Model:           openAICodexTicketHarvestModel,
		HarvestPaused:   openAICodexTicketHarvestLimited(account, openAICodexTicketHarvestModel, now),
		QuotaResetAt:    quota.resetAt,
		HarvestResumeAt: quota.resumeAt,
		HarvestEnabled:  cfg.Enabled && account.Status == StatusActive && CodexTicketHarvestEnabled(account),
	}
	view := shared.view
	status.InvalidatedForAccount = participating && view.current().structurallyValid(0) && view.invalidatedFor(account.ID)
	if ticket := view.usableFor(account.ID); ticket != nil && participating {
		capturedAt := ticket.CapturedAt
		status.Ready = true
		status.CapturedAt = &capturedAt
		status.SourceAccountID = ticket.AccountID
		status.Length = ticket.Length
		status.TurnStatePresent = ticket.State != ""
		status.CookiePresent = ticket.Cookie != ""
		status.FingerprintCommit = ticket.FingerprintCommit
	}
	status.Blocked = cfg.Enabled && !OpenAICodexAllowsWithoutTicket(account, !cfg.FailClosed) && !status.Ready
	return status
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

// structurallyValid 校验打票元数据、turn-state 及 __oailb、__cflb 两种路由 Cookie，不看有效期。
// 缺少任一项凭据的存量票与新票都不共享。
func (t *openAICodexTicket) structurallyValid(_ int) bool {
	return t != nil && t.GenerationID != "" && t.VerificationMethod == "modeltrace_v1" && t.FingerprintCommit != "" &&
		strings.TrimSpace(t.State) != "" && codexCookieHasRoutingCookies(t.Cookie)
}

func (t *openAICodexTicket) usable(_ time.Time, _ int, _ bool, _ time.Duration) bool {
	return t.structurallyValid(0)
}

func (t *openAICodexTicket) valid(_ time.Time, _ int) bool {
	return t.structurallyValid(0)
}

// CodexSharedTickets 是全局共享票状态的快照，供管理端在一次响应内复用。
type CodexSharedTickets struct {
	view *codexSharedTicketView
}

// CodexSharedTicketSnapshot 返回当前全局共享票状态（缓存读）。
func (s *OpenAIGatewayService) CodexSharedTicketSnapshot(_ context.Context) CodexSharedTickets {
	if s == nil {
		return CodexSharedTickets{}
	}
	return CodexSharedTickets{view: s.sharedCodexTicketView()}
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
	ticket.Cookie = strings.TrimSpace(ticket.Cookie)
	if !ticket.structurallyValid(0) {
		return nil
	}
	return &ticket
}

// storeOpenAICodexTicket 写入（覆盖）全局共享票：后打成功的票替换先前的票。
func (s *OpenAIGatewayService) storeOpenAICodexTicket(ctx context.Context, account *Account, ticket *openAICodexTicket) error {
	if s == nil || account == nil || ticket == nil || account.ID <= 0 {
		return ErrCodexTicketUnavailable
	}
	ticket.Model = normalizeOpenAICodexTicketModel(ticket.Model)
	ticket.AccountID = account.ID
	if s.openaiCodexTicketLifecycle == nil {
		s.cacheSharedCodexTicketView(&codexSharedTicketView{ticket: ticket})
		return nil
	}
	raw, err := json.Marshal(ticket)
	if err != nil {
		return err
	}
	// 新票全局替换：新的一代没有任何失效记录，此前失效的账号随之恢复。
	return s.writeSharedCodexTicket(ctx, func(ctx context.Context) (bool, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return true, s.openaiCodexTicketLifecycle.StoreTicket(ctx, ticket.GenerationID, account.ID, ticket.CapturedAt, raw)
	})
}

// applyOpenAICodexTicket 在门控模型的出站请求上用全局共享票覆盖 x-codex-turn-state 并追加票据 Cookie。
// 请求路径只注入已捕获的共享票，不现场打票；仅在无票策略禁止时返回
// ErrOpenAICodexTicketUnavailable。关闭全局或账号参与时不限制无票请求。
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
	if !codexTicketParticipationEnabled(account) {
		return nil, nil
	}
	// 本实例的换代与失效在写库后立即更新进程内缓存；其他实例的变化在 1 秒左右的刷新周期内可见。
	view, err := s.sharedCodexTicketViewWithError()
	if err != nil {
		return nil, err
	}
	if ticket := view.usableFor(account.ID); ticket != nil {
		h.Set(openAICodexTurnStateHeader, ticket.State)
		applyOpenAICodexTicketCookie(h, ticket)
		// 返回副本并记下实际请求模型，失效事件据此归属；不能改共享 view 里的票。
		used := *ticket
		used.Model = model
		return &used, nil
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
	if !s.openAICodexTicketGatedModel(outboundModel) {
		return false
	}
	return s.sharedCodexTicketView().usableFor(account.ID) == nil
}

func (s *OpenAIGatewayService) fireOpenAICodexTicketProbe(ctx context.Context, account *Account, token, model, proxyURL string, challenge ModelTraceChallenge, attemptTimeout time.Duration) (output string, state string, cookie string, status int, err error) {
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	body, replayHeaders, err := s.buildCodexProbeRequest(attemptCtx, account, model, challenge)
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
	for name, values := range replayHeaders {
		req.Header[name] = values
	}
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
	state, cookie, status = extractOpenAICodexTurnState(resp.Header), extractOpenAICodexHarvestCookies(resp.Header), resp.StatusCode
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
	s.openaiCodexTicketWake = make(chan struct{}, 1)
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

// Coalesce invalidations into the existing scheduler; the caller never waits
// for model requests and all participation, quota and concurrency gates remain.
func (s *OpenAIGatewayService) notifyOpenAICodexTicketHarvester() {
	s.openaiCodexTicketLifecycleMu.Lock()
	defer s.openaiCodexTicketLifecycleMu.Unlock()
	if s.openaiCodexTicketStopped || s.openaiCodexTicketWake == nil {
		return
	}
	select {
	case s.openaiCodexTicketWake <- struct{}{}:
	default:
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
		case <-s.openaiCodexTicketWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(0)
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

// refreshOpenAICodexTickets 用 gpt-6-sol 为全局共享票分批打票：候选账号按批大小切分，
// 一批并发打完后若已拿到新票就停止，否则同一轮接着打下一批。
func (s *OpenAIGatewayService) refreshOpenAICodexTickets(ctx context.Context) {
	if s == nil || s.accountRepo == nil || ctx.Err() != nil || !s.openAICodexTicketEnabledContext(ctx) {
		return
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		logger.L().Warn("openai_codex_ticket list accounts failed", zap.Error(err))
		return
	}
	now := time.Now()
	var candidates []Account
	for i := range accounts {
		account := accounts[i]
		if account.Status != StatusActive || !CodexTicketHarvestEnabled(&account) {
			continue
		}
		if openAICodexTicketHarvestLimited(&account, openAICodexTicketHarvestModel, now) {
			continue
		}
		if !s.codexTicketAutomaticDue(&account, now) {
			continue
		}
		// Token/header helpers may update account metadata; each probe owns its maps.
		account.Extra = maps.Clone(account.Extra)
		account.Credentials = maps.Clone(account.Credentials)
		candidates = append(candidates, account)
	}
	if len(candidates) == 0 {
		return
	}
	batchSize := s.codexTicketCadence(ctx).BatchSize
	if probed := s.harvestCodexTicketInBatches(ctx, candidates, batchSize); probed > 0 {
		logger.L().Info("openai_codex_ticket probe cycle", zap.Int("probed", probed), zap.Int("batch_size", batchSize))
	}
}

// codexTicketRound 是一轮打票共用的写入状态：同一轮里打出的票依次尝试写入共享票，
// 第一张写入成功的票被采用，之后完成的票记为被取代；写入失败时后续的票仍可接着写入。
type codexTicketRound struct {
	mu     sync.Mutex
	stored bool
}

// adopt 串行地尝试把一张新票写入共享票。已有票写入时返回 superseded=true 且不调用 store。
func (r *codexTicketRound) adopt(store func() error) (superseded bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stored {
		return true, nil
	}
	if err := store(); err != nil {
		return false, err
	}
	r.stored = true
	return false, nil
}

func (r *codexTicketRound) adopted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stored
}

type codexTicketRoundKey struct{}

func withCodexTicketRound(ctx context.Context, round *codexTicketRound) context.Context {
	return context.WithValue(ctx, codexTicketRoundKey{}, round)
}

func codexTicketRoundFrom(ctx context.Context) *codexTicketRound {
	round, _ := ctx.Value(codexTicketRoundKey{}).(*codexTicketRound)
	return round
}

// codexTicketHarvestLockName 让全站同一时间只有一个实例打票。
const codexTicketHarvestLockName = "harvest"

// codexTicketHarvestLockMargin 是打票锁在单次打票超时之外留的余量（测试可缩短）。
var codexTicketHarvestLockMargin = 30 * time.Second

// acquireCodexTicketHarvestLock 让同一时间只有一个实例打票。没有 Redis 或 Redis 出错时照常打票，
// 不因锁不可用而停止补票。持锁期间后台每 ttl/3 续期一次，直到 release；续期发现锁已丢失时取消返回的 ctx，
// 让本轮尽快停下，避免与新的持锁实例同时打票。
func (s *OpenAIGatewayService) acquireCodexTicketHarvestLock(ctx context.Context) (lockCtx context.Context, release func(), ok bool) {
	cache := s.openaiCodexTicketCache
	if cache == nil {
		return ctx, func() {}, true
	}
	owner := uuid.NewString()
	ttl := time.Duration(s.openAICodexTicketConfig().HarvestAttemptTimeoutSeconds)*time.Second + codexTicketHarvestLockMargin
	locked, err := cache.TryLock(ctx, codexTicketHarvestLockName, owner, ttl)
	if err != nil {
		logger.L().Warn("openai_codex_ticket harvest lock failed; harvesting without it", zap.Error(err))
		return ctx, func() {}, true
	}
	if !locked {
		return nil, nil, false
	}
	lockCtx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-lockCtx.Done():
				return
			case <-ticker.C:
				extended, err := cache.ExtendLock(lockCtx, codexTicketHarvestLockName, owner, ttl)
				if err != nil {
					logger.L().Warn("openai_codex_ticket harvest lock extend failed", zap.Error(err))
					continue
				}
				if !extended {
					logger.L().Warn("openai_codex_ticket harvest lock lost; stopping this round")
					cancel()
					return
				}
			}
		}
	}()
	release = func() {
		close(stopped)
		<-heartbeatDone
		cancel()
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), time.Second)
		defer unlockCancel()
		_ = cache.Unlock(unlockCtx, codexTicketHarvestLockName, owner)
	}
	return lockCtx, release, true
}

// harvestCodexTicketInBatches 依次按批并发打票，拿到新票（本实例写入或其他实例已换代）即停止，返回实际打票的账号数。
// 另一个实例正在打票时本轮跳过，返回 0。
func (s *OpenAIGatewayService) harvestCodexTicketInBatches(ctx context.Context, accounts []Account, batchSize int) int {
	if batchSize <= 0 {
		batchSize = openAICodexTicketDefaultBatchSize
	}
	lockCtx, releaseLock, ok := s.acquireCodexTicketHarvestLock(ctx)
	if !ok {
		return 0
	}
	defer releaseLock()
	startGeneration := ""
	if ticket := s.sharedCodexTicketView().current(); ticket != nil {
		startGeneration = ticket.GenerationID
	}
	round := &codexTicketRound{}
	roundCtx := withCodexTicketRound(lockCtx, round)
	probe := s.probeOnceOpenAICodexTicket
	if s.openaiCodexTicketProbe != nil {
		probe = s.openaiCodexTicketProbe
	}
	probed := 0
	for start := 0; start < len(accounts) && roundCtx.Err() == nil; start += batchSize {
		end := min(start+batchSize, len(accounts))
		var wg sync.WaitGroup
		for i := start; i < end; i++ {
			wg.Add(1)
			go func(acc Account) {
				defer wg.Done()
				probe(roundCtx, &acc)
			}(accounts[i])
		}
		wg.Wait()
		probed += end - start
		if round.adopted() {
			return probed
		}
		view := s.sharedCodexTicketView()
		if ticket := view.current(); ticket != nil && ticket.GenerationID != startGeneration && !view.hasInvalidations() {
			return probed
		}
	}
	return probed
}

// probeOnceOpenAICodexTicket 走打票代理用 gpt-6-sol 打一发，返回是否打出可用的票（本次写入或同轮已有票写入）。
// 同一账号并发去重，避免上一发还没回来又叠一发。
func (s *OpenAIGatewayService) probeOnceOpenAICodexTicket(ctx context.Context, account *Account) bool {
	if s == nil || !CodexTicketHarvestEnabled(account) || ctx.Err() != nil || !s.openAICodexTicketEnabledContext(ctx) {
		return false
	}
	if openAICodexTicketHarvestLimited(account, openAICodexTicketHarvestModel, time.Now()) {
		return false
	}
	result, err := s.runCodexTicketAttempt(ctx, account, "automatic")
	if err != nil {
		if !errors.Is(err, ErrCodexTicketBusy) && !errors.Is(err, ErrCodexTicketNoProxy) {
			logger.L().Warn("openai_codex_ticket probe unavailable", zap.Int64("account_id", account.ID), zap.Error(err))
		}
		return false
	}
	if result.Outcome == "success" {
		message := "openai_codex_ticket harvested"
		if result.ReasonCode == CodexTicketReasonSuperseded {
			message = "openai_codex_ticket harvested_superseded"
		}
		logger.L().Info(message, zap.Int64("account_id", account.ID), zap.String("model", openAICodexTicketHarvestModel), zap.Int("http", *result.HTTPStatus), zap.String("mode", "continuous"))
		return true
	}
	logger.L().Info("openai_codex_ticket probe miss", zap.Int64("account_id", account.ID), zap.String("model", openAICodexTicketHarvestModel), zap.String("reason", result.ReasonCode))
	return false
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

// IsOpenAICodexTicketExtraKey identifies retired per-account ticket material that
// older account records or backups may still carry.
func IsOpenAICodexTicketExtraKey(key string) bool {
	return strings.HasPrefix(key, openAICodexTicketExtraKeyPrefix)
}

// stripRetiredOpenAICodexTicketExtra drops retired ticket material and the retired
// per-model participation map from an account write: tickets live only in the
// global shared ticket and participation is an account-level choice.
func stripRetiredOpenAICodexTicketExtra(extra map[string]any) map[string]any {
	for key := range extra {
		if IsOpenAICodexTicketExtraKey(key) || key == codexTicketRetiredModelsKey {
			delete(extra, key)
		}
	}
	return extra
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

// OpenAICodexAllowsWithoutTicket bypasses the missing-ticket gate when the account
// does not participate; otherwise the account override wins over the global default.
func OpenAICodexAllowsWithoutTicket(account *Account, globalDefault bool) bool {
	if account != nil {
		if !codexTicketParticipationEnabled(account) {
			return true
		}
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
