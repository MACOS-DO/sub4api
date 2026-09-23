package service

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"hash/fnv"
	"maps"
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
)

const codexTicketAccountEnabledKey = "codex_ticket_harvest_enabled"
const codexTicketModelsEnabledKey = "codex_ticket_harvest_models"

const modelTraceChallengeCount = 292

func CodexTicketHarvestEnabled(account *Account, model string) bool {
	if account == nil || !isOpenAICodexTicketAccount(account) {
		return false
	}
	if account.Extra[codexTicketAccountEnabledKey] == false {
		return false
	}
	switch models := account.Extra[codexTicketModelsEnabledKey].(type) {
	case map[string]any:
		return models[model] != false
	case map[string]bool:
		participating, exists := models[model]
		return !exists || participating
	default:
		return true
	}
}

func (s *OpenAIGatewayService) codexTicketSupportedModel(model string) bool {
	for _, configured := range s.openAICodexTicketConfig().Models {
		if model == configured {
			return true
		}
	}
	return false
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

func (s *OpenAIGatewayService) scheduleCodexTicketAfterSuccess(ticket *openAICodexTicket) {
	if ticket == nil {
		return
	}
	key := openAICodexTicketKey(ticket.AccountID, ticket.Model)
	seconds := s.codexTicketCadence(context.Background()).RefreshSeconds
	if seconds <= 0 {
		s.openaiCodexTicketNextAttempt.Delete(key)
		return
	}
	s.openaiCodexTicketNextAttempt.Store(key, ticket.CapturedAt.Add(time.Duration(seconds)*time.Second))
}

func (s *OpenAIGatewayService) codexTicketAutomaticDue(account *Account, model string, now time.Time) bool {
	key := openAICodexTicketKey(account.ID, model)
	var previous *openAICodexTicket
	if raw, ok := s.openaiCodexTickets.Load(key); ok {
		previous, _ = raw.(*openAICodexTicket)
	}
	ticket := s.lookupOpenAICodexTicket(account, model)
	if previous != nil && (ticket == nil || previous.GenerationID != ticket.GenerationID) {
		s.openaiCodexTicketNextAttempt.Delete(key)
	}
	if value, ok := s.openaiCodexTicketNextAttempt.Load(key); ok {
		if next, valid := value.(time.Time); valid && now.Before(next) {
			return false
		}
		return true
	}
	if ticket.usable(now, 0, false, 0) {
		s.scheduleCodexTicketAfterSuccess(ticket)
		refreshSeconds := s.codexTicketCadence(context.Background()).RefreshSeconds
		return refreshSeconds > 0 && !now.Before(ticket.CapturedAt.Add(time.Duration(refreshSeconds)*time.Second))
	}
	return true
}

func (s *OpenAIGatewayService) runCodexTicketAttempt(ctx context.Context, account *Account, model, trigger string) (result CodexTicketHarvestResult, err error) {
	key := openAICodexTicketKey(account.ID, model)
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
	cfg := s.openAICodexTicketConfig()
	start := time.Now()
	a := &result.CodexTicketAttempt
	a.AccountID, a.Model, a.Trigger = account.ID, model, trigger
	a.VerificationMethod, a.FingerprintCommit = "modeltrace_v1", ModelTraceBankCommit()
	a.ChallengeExpectedCount = new(modelTraceChallengeCount)
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
	output, state, cookie, status, probeErr := s.fireOpenAICodexTicketProbe(ctx, account, token, model, proxyURL,
		modelTraceChallengeCount, time.Duration(cfg.HarvestAttemptTimeoutSeconds)*time.Second)
	if status != 0 {
		a.HTTPStatus = &status
		length := len(state)
		a.TicketLength = &length
		turnPresent, cookiePresent := strings.TrimSpace(state) != "", strings.TrimSpace(cookie) != ""
		a.TurnStatePresent, a.CookiePresent = &turnPresent, &cookiePresent
	}
	if probeErr != nil {
		a.Outcome, a.ReasonCode = "error", "request"
		if errors.Is(probeErr, context.DeadlineExceeded) {
			a.ReasonCode = "timeout"
		}
		return result, nil
	}
	if status != http.StatusOK {
		a.Outcome, a.ReasonCode = "miss", "http_status"
		return result, nil
	}
	prediction, commit, predictErr := ModelTracePredictCommitted(output, modelTraceChallengeCount)
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
	if strings.TrimSpace(state) == "" && strings.TrimSpace(cookie) == "" {
		a.Outcome, a.ReasonCode = "miss", "missing_credentials"
		return result, nil
	}
	now := time.Now()
	generation := uuid.NewString()
	ticket := &openAICodexTicket{AccountID: account.ID, Model: model, State: state, Length: len(state),
		GenerationID: generation, VerificationMethod: "modeltrace_v1", FingerprintCommit: commit,
		CapturedAt: now, Attempts: 1, HTTPStatus: status, Cookie: cookie}
	if err := s.storeOpenAICodexTicket(ctx, account, ticket); err != nil {
		a.Outcome, a.ReasonCode = "error", "persistence"
		return result, nil
	}
	s.scheduleCodexTicketAfterSuccess(ticket)
	account.Extra = maps.Clone(account.Extra)
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	account.Extra[openAICodexTicketExtraKey(model)] = ticket
	a.Outcome, a.TicketGenerationID = "success", &generation
	return result, nil

}

func (s *OpenAIGatewayService) ManualCodexTicketHarvest(ctx context.Context, accountID int64, model string) (CodexTicketHarvestResult, error) {
	var empty CodexTicketHarvestResult
	if !s.codexTicketSupportedModel(model) {
		return empty, ErrCodexTicketModel
	}
	if !s.openAICodexTicketEnabledContext(ctx) {
		return empty, ErrCodexTicketUnavailable
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return empty, err
	}
	if account.Status != StatusActive || !CodexTicketHarvestEnabled(account, model) {
		return empty, ErrCodexTicketUnavailable
	}
	result, err := s.runCodexTicketAttempt(ctx, account, model, "manual")
	if err != nil {
		return result, err
	}
	cfg := s.openAICodexTicketConfig()
	cfg.Enabled = s.openAICodexTicketEnabledContext(ctx)
	status := OpenAICodexTicketStatuses(account, cfg, time.Now())
	for _, item := range status {
		if item.Model == model {
			result.TicketStatus = item
			break
		}
	}
	return result, nil
}

func (s *OpenAIGatewayService) CodexTicketHistory(ctx context.Context, accountID int64, model string, successOnly bool, page, size int) ([]CodexTicketAttempt, int64, OpenAICodexTicketStatus, error) {
	var empty OpenAICodexTicketStatus
	if model != "" && !s.codexTicketSupportedModel(model) {
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
	cfg := s.openAICodexTicketConfig()
	cfg.Enabled = s.openAICodexTicketEnabledContext(ctx)
	for _, status := range OpenAICodexTicketStatuses(account, cfg, time.Now()) {
		if status.Model == model {
			empty = status
			break
		}
	}
	return rows, total, empty, nil
}

func (s *OpenAIGatewayService) SetCodexTicketParticipation(ctx context.Context, accountID int64, enabled bool, models map[string]bool) error {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return err
	}
	if !isOpenAICodexTicketAccount(account) {
		return ErrCodexTicketUnavailable
	}
	for model := range models {
		if !s.codexTicketSupportedModel(model) {
			return ErrCodexTicketModel
		}
	}
	values := make(map[string]any, len(models))
	for model, participating := range models {
		values[model] = participating
	}
	return s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{
		codexTicketAccountEnabledKey: enabled,
		codexTicketModelsEnabledKey:  values,
	})
}

func (s *OpenAIGatewayService) ListCodexTicketInvalidations(ctx context.Context, accountID int64, model string, start, end time.Time, page, size int) ([]CodexTicketInvalidationSummary, int64, error) {
	if s.openaiCodexTicketLifecycle == nil {
		return nil, 0, ErrCodexTicketUnavailable
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, 0, err
	}
	if !isOpenAICodexTicketAccount(account) || model != "" && !s.codexTicketSupportedModel(model) {
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
