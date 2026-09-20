package service

import (
	"context"
	"errors"
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

const (
	codexTicketValidFirstRetryMin = 5 * time.Minute
	codexTicketValidFirstRetryMax = 8 * time.Minute
	codexTicketValidRetryMin      = 20 * time.Second
	codexTicketValidRetryMax      = 40 * time.Second
	codexTicketMissingRetry       = 10 * time.Second
)

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
		// Legacy tests construct the gateway without settings. Production always
		// has settings and exclusively uses the managed proxy pool.
		if s.cfg != nil && s.cfg.Gateway.OpenAICodexTicket.HarvestProxyURL != "" {
			return strings.TrimSpace(s.cfg.Gateway.OpenAICodexTicket.HarvestProxyURL), nil, nil
		}
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
	next := ticket.CapturedAt.Add(codexTicketJitter(key, ticket.CapturedAt, codexTicketValidFirstRetryMin, codexTicketValidFirstRetryMax))
	s.openaiCodexTicketNextAttempt.Store(key, next)
}

func (s *OpenAIGatewayService) codexTicketAutomaticDue(account *Account, model string, now time.Time) bool {
	key := openAICodexTicketKey(account.ID, model)
	if value, ok := s.openaiCodexTicketNextAttempt.Load(key); ok {
		if next, valid := value.(time.Time); valid && now.Before(next) {
			return false
		}
		return true
	}
	ticket := s.lookupOpenAICodexTicket(account, model)
	if ticket.valid(now, openAICodexTicketTargetLength(account, s.openAICodexTicketConfig().TargetLength)) {
		next := ticket.CapturedAt.Add(codexTicketJitter(key, ticket.CapturedAt, codexTicketValidFirstRetryMin, codexTicketValidFirstRetryMax))
		s.openaiCodexTicketNextAttempt.Store(key, next)
		return !now.Before(next)
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
	proxyURL, proxy, err := s.chooseCodexTicketProxy(ctx, account.ID, model)
	if err != nil {
		return result, err
	}
	if s.httpUpstream == nil {
		return result, ErrCodexTicketUnavailable
	}
	start := time.Now()
	hadValidTicket := s.lookupOpenAICodexTicket(account, model).valid(start, openAICodexTicketTargetLength(account, s.openAICodexTicketConfig().TargetLength))
	a := &result.CodexTicketAttempt
	a.AccountID, a.Model, a.Trigger = account.ID, model, trigger
	if proxy != nil {
		a.ProxyID, a.ProxyName = &proxy.ID, proxy.Name
	}
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
		if trigger == "automatic" && a.Outcome != "success" {
			delay := codexTicketMissingRetry
			if hadValidTicket {
				delay = codexTicketJitter(key, a.OccurredAt, codexTicketValidRetryMin, codexTicketValidRetryMax)
			}
			s.openaiCodexTicketNextAttempt.Store(key, a.OccurredAt.Add(delay))
		}
	}()
	token, _, tokenErr := s.GetAccessToken(ctx, account)
	if tokenErr != nil || strings.TrimSpace(token) == "" {
		a.Outcome, a.ReasonCode = "error", "credentials"
		return result, nil
	}
	state, status, probeErr := s.fireOpenAICodexTicketProbe(ctx, account, token, model, proxyURL,
		time.Duration(s.openAICodexTicketConfig().HarvestAttemptTimeoutSeconds)*time.Second)
	if status != 0 {
		a.HTTPStatus = &status
	}
	length := len(state)
	if status != 0 {
		a.TicketLength = &length
	}
	if probeErr != nil {
		a.Outcome, a.ReasonCode = "error", "request"
		if errors.Is(probeErr, context.DeadlineExceeded) {
			a.ReasonCode = "timeout"
		}
		return result, nil
	}
	if !openAICodexTicketResponseValid(account, s.openAICodexTicketConfig().TargetLength, status, state) {
		a.Outcome, a.ReasonCode = "miss", "invalid_ticket"
		if status != http.StatusOK && status != http.StatusTooManyRequests {
			a.ReasonCode = "http_status"
		}
		return result, nil
	}
	now := time.Now()
	ticket := &openAICodexTicket{AccountID: account.ID, Model: model, State: state, Length: len(state),
		CapturedAt: now, ExpiresAt: now.Add(time.Duration(s.openAICodexTicketConfig().TTLSeconds) * time.Second),
		Attempts: 1, HTTPStatus: status}
	s.storeOpenAICodexTicket(ctx, account, ticket)
	s.scheduleCodexTicketAfterSuccess(ticket)
	account.Extra = maps.Clone(account.Extra)
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	account.Extra[openAICodexTicketExtraKey(model)] = ticket
	a.Outcome, a.ExpiresAt = "success", &ticket.ExpiresAt
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
	if !s.codexTicketSupportedModel(model) {
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
