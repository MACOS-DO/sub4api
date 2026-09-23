package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type codexTicketDiagnosticTargetKey struct{}

func WithCodexTicketDiagnostic(ctx context.Context, accountID int64) context.Context {
	return context.WithValue(ctx, codexTicketDiagnosticTargetKey{}, accountID)
}

func isCodexTicketDiagnostic(ctx context.Context) bool {
	accountID, _ := ctx.Value(codexTicketDiagnosticTargetKey{}).(int64)
	return accountID > 0
}

func (s *OpenAIGatewayService) HasCodexTicket(ctx context.Context, accountID int64, model string) (string, error) {
	if s.openaiCodexTicketLifecycle == nil || !s.codexTicketSupportedModel(model) {
		return "", ErrCodexTicketUnavailable
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return "", err
	}
	if !isOpenAICodexTicketAccount(account) || account.Status != StatusActive {
		return "", ErrCodexTicketUnavailable
	}
	raw, err := s.openaiCodexTicketLifecycle.CurrentTicket(ctx, accountID, model)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var current openAICodexTicket
	if err := json.Unmarshal(raw, &current); err != nil {
		return "", err
	}
	ticket := parseOpenAICodexTicketFromAny(accountID, model, current)
	if !ticket.usable(time.Now(), 0, false, 0) {
		return "", nil
	}
	return ticket.GenerationID, nil
}

func (s *OpenAIGatewayService) DiagnosticCodexTicketHarvest(ctx context.Context, accountID int64, model string) (CodexTicketHarvestResult, error) {
	var empty CodexTicketHarvestResult
	if !s.codexTicketSupportedModel(model) || !s.openAICodexTicketEnabledContext(ctx) {
		return empty, ErrCodexTicketUnavailable
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return empty, err
	}
	if account.Status != StatusActive || !CodexTicketHarvestEnabled(account, model) {
		return empty, ErrCodexTicketUnavailable
	}
	return s.runCodexTicketAttempt(ctx, account, model, "diagnostic")
}

func ModelTraceChallengePrompt(count int) string {
	return fmt.Sprintf("For each of %d positions, make one separate first-instinct choice of an integer from 1 to 355 inclusive. Output exactly %d whole numbers separated by spaces, with no explanations. Choose each value independently; do not count upward, sort, use arithmetic progressions or repeating patterns. Do not call tools or external random generators.", count, count)
}
