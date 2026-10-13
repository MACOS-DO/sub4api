package service

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"hash/fnv"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MACOS-DO/sub4api/internal/pkg/logger"
	"go.uber.org/zap"
)

var (
	ErrCodexTicketUnavailable = errors.New("codex ticket harvesting unavailable")
	ErrCodexTicketBusy        = errors.New("codex ticket attempt already running")
	ErrCodexTicketNoProxy     = errors.New("no available ticket proxy")
	ErrCodexTicketModel       = errors.New("unsupported ticket model")
	ErrCodexTicketRateLimited = errors.New("account or model is rate limited")
)

const codexTicketAccountEnabledKey = "codex_ticket_harvest_enabled"

// codexTicketRetiredModelsKey 是已作废的按模型参与开关。票据全站共享后参与只看账号级开关，
// 该键不再读取，写入时剥离，避免旧的「某模型不参与」设置继续生效又无法在界面上改回。
const codexTicketRetiredModelsKey = "codex_ticket_harvest_models"

// CodexTicketHarvestEnabled 表示该账号参与共享票：用它打票，并在门控模型请求上使用共享票。
func CodexTicketHarvestEnabled(account *Account) bool {
	return isOpenAICodexTicketAccount(account) && codexTicketParticipationEnabled(account)
}

// codexTicketParticipationEnabled reads only the saved account-level choice, not runtime availability.
func codexTicketParticipationEnabled(account *Account) bool {
	return account != nil && account.Extra[codexTicketAccountEnabledKey] != false
}

func (s *OpenAIGatewayService) chooseCodexTicketProxy(ctx context.Context, accountID int64, model string) (string, *Proxy, error) {
	if s.settingService == nil {
		return "", nil, ErrCodexTicketNoProxy
	}
	proxies, err := s.settingService.AvailableCodexTicketProxies(ctx)
	if err != nil {
		return "", nil, err
	}
	if len(proxies) == 0 {
		return "", nil, ErrCodexTicketNoProxy
	}
	key := openAICodexTicketKey(accountID, model)
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	seed := uint64(h.Sum32())
	value, _ := s.openaiCodexTicketProxyTurns.LoadOrStore(key, &atomic.Uint64{})
	turn := value.(*atomic.Uint64).Add(1) - 1
	selected := proxies[(seed+turn)%uint64(len(proxies))]
	return selected.URL(), &selected, nil
}

type CodexTicketHarvestResult struct {
	CodexTicketAttempt
	TicketStatus    OpenAICodexTicketStatus `json:"ticket_status"`
	HistoryRecorded bool                    `json:"history_recorded"`
}

func codexTicketJitter(key string, at time.Time, min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	_, _ = h.Write([]byte(at.UTC().Format(time.RFC3339Nano)))
	return min + time.Duration(h.Sum64()%uint64(max-min+1))
}

// CodexTicketReasonSuperseded 标记同一轮已有其他账号的票被采用：本次打票成功但没有写入共享票。
const CodexTicketReasonSuperseded = "superseded"

// scheduleCodexTicketAfterSuccess 在共享票写入后清掉所有账号的失败退避：
// 票已对全员生效，下一轮打票只由刷新间隔或失效触发。
func (s *OpenAIGatewayService) scheduleCodexTicketAfterSuccess(ticket *openAICodexTicket) {
	if ticket == nil {
		return
	}
	s.clearCodexTicketRetries()
	generation := ticket.GenerationID
	s.openaiCodexTicketLastGeneration.Store(&generation)
}

// clearCodexTicketRetries 删除所有账号的退避记录。
func (s *OpenAIGatewayService) clearCodexTicketRetries() {
	s.openaiCodexTicketNextAttempt.Clear()
}

// codexTicketRetryKey 是账号级打票状态（退避、并发去重、代理轮换）的键。
func codexTicketRetryKey(accountID int64) string {
	return openAICodexTicketKey(accountID, openAICodexTicketHarvestModel)
}

// codexTicketAutomaticDue 判断该账号本轮是否要打票：共享票有效、无账号失效且未到刷新间隔时
// 所有账号都不打；没有票、到了刷新间隔或当前这一代已有账号失效时，各账号在自己的失败退避到期后参与分批打票。
func (s *OpenAIGatewayService) codexTicketAutomaticDue(account *Account, now time.Time) bool {
	view := s.sharedCodexTicketView()
	ticket := view.current()
	generation := ""
	if ticket != nil {
		generation = ticket.GenerationID
	}
	// 共享票换代（被其他账号/实例打出或失效）时，旧的失败退避不再有意义。
	if previous := s.openaiCodexTicketLastGeneration.Swap(&generation); previous != nil && *previous != generation {
		s.clearCodexTicketRetries()
	}
	if ticket.usable(now, 0, false, 0) && !view.hasInvalidations() {
		refreshSeconds := s.codexTicketCadence(context.Background()).RefreshSeconds
		if refreshSeconds <= 0 || now.Before(ticket.CapturedAt.Add(time.Duration(refreshSeconds)*time.Second)) {
			return false
		}
	}
	if value, ok := s.openaiCodexTicketNextAttempt.Load(codexTicketRetryKey(account.ID)); ok {
		if next, valid := value.(time.Time); valid && now.Before(next) {
			return false
		}
	}
	return true
}

// runCodexTicketAttempt 用 gpt-6-sol 为全局共享票打一次票。
func (s *OpenAIGatewayService) runCodexTicketAttempt(ctx context.Context, account *Account, trigger string) (CodexTicketHarvestResult, error) {
	return s.runCodexTicketAttemptWithChallengeGenerator(ctx, account, trigger, NewModelTraceChallenge)
}

func (s *OpenAIGatewayService) runCodexTicketAttemptWithChallengeGenerator(ctx context.Context, account *Account, trigger string, generateChallenge func() (ModelTraceChallenge, error)) (result CodexTicketHarvestResult, err error) {
	model := openAICodexTicketHarvestModel
	key := codexTicketRetryKey(account.ID)
	if _, loaded := s.openaiCodexTicketInFlight.LoadOrStore(key, true); loaded {
		return result, ErrCodexTicketBusy
	}
	defer s.openaiCodexTicketInFlight.Delete(key)
	if s.openaiCodexTicketHistory != nil {
		unlock, acquired, err := s.openaiCodexTicketHistory.TryLock(ctx, account.ID, model)
		if err != nil {
			return result, err
		}
		if !acquired {
			return result, ErrCodexTicketBusy
		}
		defer unlock()
	}
	current, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil {
		return result, err
	}
	if current == nil || current.Status != StatusActive || !CodexTicketHarvestEnabled(current) {
		return result, ErrCodexTicketUnavailable
	}
	if openAICodexTicketHarvestLimited(current, model, time.Now()) {
		return result, ErrCodexTicketRateLimited
	}
	account = current
	cfg := s.openAICodexTicketConfig()
	start := time.Now()
	a := &result.CodexTicketAttempt
	a.AccountID, a.Model, a.Trigger = account.ID, model, trigger
	a.VerificationMethod, a.FingerprintCommit = "modeltrace_v1", ModelTraceBankCommit()
	defer func() {
		a.OccurredAt = time.Now()
		a.DurationMS = int(a.OccurredAt.Sub(start).Milliseconds())
		if s.openaiCodexTicketHistory != nil {
			writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.openaiCodexTicketHistory.Insert(writeCtx, a); err != nil {
				logger.L().Error("openai_codex_ticket history persist failed", zap.Int64("account_id", account.ID), zap.String("model", model), zap.Error(err))
			} else {
				result.HistoryRecorded = true
			}
		}
		if a.Outcome != "success" {
			cadence := s.codexTicketCadence(context.Background())
			min := time.Duration(cadence.RetryMinSeconds) * time.Second
			max := time.Duration(cadence.RetryMaxSeconds) * time.Second
			s.openaiCodexTicketNextAttempt.Store(key, a.OccurredAt.Add(codexTicketJitter(key, a.OccurredAt, min, max)))
		}

	}()
	ctx, err = s.PrepareCodexProbeContext(ctx)
	if err != nil {
		a.Outcome, a.ReasonCode = "error", "template_unavailable"
		if errors.Is(err, ErrCodexProbeTemplateInvalid) {
			a.ReasonCode = "template_invalid"
		}
		return result, nil
	}
	proxyURL, proxy, proxyErr := s.chooseCodexTicketProxy(ctx, account.ID, model)
	if proxyErr != nil {
		a.Outcome, a.ReasonCode = "error", "proxy_unavailable"
		return result, proxyErr
	}
	a.ProxyID, a.ProxyName = &proxy.ID, proxy.Name
	if s.httpUpstream == nil {
		a.Outcome, a.ReasonCode = "error", "upstream_unavailable"
		return result, ErrCodexTicketUnavailable
	}
	token, _, tokenErr := s.GetAccessToken(ctx, account)
	if tokenErr != nil || strings.TrimSpace(token) == "" {
		a.Outcome, a.ReasonCode = "error", "credentials"
		return result, nil
	}
	challenge, challengeErr := generateChallenge()
	if challengeErr != nil {
		a.Outcome, a.ReasonCode = "error", "challenge_generation_failed"
		return result, nil
	}
	a.ChallengeExpectedCount = new(challenge.ExpectedCount)
	output, state, cookie, status, probeErr := s.fireOpenAICodexTicketProbe(ctx, account, token, model, proxyURL,
		challenge, time.Duration(cfg.HarvestAttemptTimeoutSeconds)*time.Second)
	if status != 0 {
		a.HTTPStatus = &status
		length := len(state)
		a.TicketLength = &length
		turnPresent, cookiePresent := strings.TrimSpace(state) != "", strings.TrimSpace(cookie) != ""
		a.TurnStatePresent, a.CookiePresent = &turnPresent, &cookiePresent
	}
	if probeErr != nil {
		a.Outcome, a.ReasonCode = "error", "request"
		if errors.Is(probeErr, ErrCodexProbeTemplateInvalid) {
			a.ReasonCode = "template_invalid"
		}
		if errors.Is(probeErr, ErrCodexProbeIdentity) {
			a.ReasonCode = "identity_resolution_failed"
		}
		if errors.Is(probeErr, context.DeadlineExceeded) {
			a.ReasonCode = "timeout"
		}
		return result, nil
	}
	if status != http.StatusOK {
		a.Outcome, a.ReasonCode = "miss", "http_status"
		return result, nil
	}
	prediction, commit, predictErr := ModelTracePredictCommitted(output, challenge.ExpectedCount)
	a.FingerprintCommit = commit
	count := prediction.ParsedCount
	a.ParsedNumberCount = &count
	if predictErr != nil {
		a.Outcome, a.ReasonCode = "miss", "insufficient_numbers"
		return result, nil
	}
	a.FingerprintPredictedModel = prediction.Model
	a.FingerprintProbability = &prediction.Probability
	matched := prediction.Model == model
	a.FingerprintMatched = &matched
	if !matched {
		a.Outcome, a.ReasonCode = "miss", "fingerprint_mismatch"
		return result, nil
	}
	// 同一次打票必须得到 turn-state、__oailb 与 __cflb，缺少任一项都不共享。
	if strings.TrimSpace(state) == "" || !codexCookieHasRoutingCookies(cookie) {
		a.Outcome, a.ReasonCode = "miss", "missing_credentials"
		return result, nil
	}
	now := time.Now()
	generation := uuid.NewString()
	ticket := &openAICodexTicket{AccountID: account.ID, Model: model, State: state, Length: len(state),
		GenerationID: generation, VerificationMethod: "modeltrace_v1", FingerprintCommit: commit,
		CapturedAt: now, Attempts: 1, HTTPStatus: status, Cookie: cookie}
	store := func() error { return s.storeOpenAICodexTicket(ctx, account, ticket) }
	superseded, storeErr := false, error(nil)
	if round := codexTicketRoundFrom(ctx); round != nil {
		// 同一轮已有票写入：本次照样算打票成功，但不覆盖共享票，也不产生新的 generation。
		superseded, storeErr = round.adopt(store)
	} else {
		storeErr = store()
	}
	if storeErr != nil {
		a.Outcome, a.ReasonCode = "error", "persistence"
		return result, nil
	}
	if superseded {
		a.Outcome, a.ReasonCode = "success", CodexTicketReasonSuperseded
		return result, nil
	}
	s.scheduleCodexTicketAfterSuccess(ticket)
	a.Outcome, a.TicketGenerationID = "success", &generation
	return result, nil

}

// ManualCodexTicketHarvest 用 gpt-6-sol 为全局共享票手动打一次票。
func (s *OpenAIGatewayService) ManualCodexTicketHarvest(ctx context.Context, accountID int64) (CodexTicketHarvestResult, error) {
	var empty CodexTicketHarvestResult
	if !s.openAICodexTicketEnabledContext(ctx) {
		return empty, ErrCodexTicketUnavailable
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return empty, err
	}
	if account.Status != StatusActive || !CodexTicketHarvestEnabled(account) {
		return empty, ErrCodexTicketUnavailable
	}
	if openAICodexTicketHarvestLimited(account, openAICodexTicketHarvestModel, time.Now()) {
		return empty, ErrCodexTicketRateLimited
	}
	result, err := s.runCodexTicketAttempt(ctx, account, "manual")
	if err != nil {
		return result, err
	}
	if status := s.codexTicketStatusFor(ctx, account); status != nil {
		result.TicketStatus = *status
	}
	return result, nil
}

func (s *OpenAIGatewayService) codexTicketStatusFor(ctx context.Context, account *Account) *OpenAICodexTicketStatus {
	cfg := s.openAICodexTicketConfig()
	cfg.Enabled = s.openAICodexTicketEnabledContext(ctx)
	cfg.FailClosed = !s.openAICodexAllowsWithoutTicket(ctx, nil)
	return OpenAICodexTicketStatusOf(account, cfg, s.CodexSharedTicketSnapshot(ctx), time.Now())
}

func (s *OpenAIGatewayService) CodexTicketHistory(ctx context.Context, accountID int64, model string, successOnly bool, page, size int) ([]CodexTicketAttempt, int64, OpenAICodexTicketStatus, error) {
	var empty OpenAICodexTicketStatus
	if model != "" && !modelTraceKnownGPTModel(model) {
		return nil, 0, empty, ErrCodexTicketModel
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, 0, empty, err
	}
	if !isOpenAICodexTicketAccount(account) {
		return nil, 0, empty, ErrCodexTicketUnavailable
	}
	if s.openaiCodexTicketHistory == nil {
		return nil, 0, empty, ErrCodexTicketUnavailable
	}
	rows, total, err := s.openaiCodexTicketHistory.List(ctx, accountID, model, successOnly, page, size)
	if err != nil {
		return nil, 0, empty, err
	}
	if status := s.codexTicketStatusFor(ctx, account); status != nil {
		empty = *status
	}
	return rows, total, empty, nil
}

// SetCodexTicketParticipation 保存账号级的共享票参与开关。
func (s *OpenAIGatewayService) SetCodexTicketParticipation(ctx context.Context, accountID int64, enabled bool) error {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return err
	}
	if !isOpenAICodexTicketAccount(account) {
		return ErrCodexTicketUnavailable
	}
	return s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{codexTicketAccountEnabledKey: enabled})
}

func (s *OpenAIGatewayService) ListCodexTicketInvalidations(ctx context.Context, accountID int64, model string, start, end time.Time, page, size int) ([]CodexTicketInvalidationSummary, int64, error) {
	if s.openaiCodexTicketLifecycle == nil {
		return nil, 0, ErrCodexTicketUnavailable
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, 0, err
	}
	if !isOpenAICodexTicketAccount(account) || model != "" && !modelTraceKnownGPTModel(model) {
		return nil, 0, ErrCodexTicketUnavailable
	}
	return s.openaiCodexTicketLifecycle.ListInvalidations(ctx, accountID, model, start, end, page, size)
}

func (s *OpenAIGatewayService) GetCodexTicketInvalidation(ctx context.Context, accountID, eventID int64) (*CodexTicketInvalidation, error) {
	if s.openaiCodexTicketLifecycle == nil {
		return nil, ErrCodexTicketUnavailable
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !isOpenAICodexTicketAccount(account) {
		return nil, ErrCodexTicketUnavailable
	}
	return s.openaiCodexTicketLifecycle.GetInvalidation(ctx, accountID, eventID)
}
