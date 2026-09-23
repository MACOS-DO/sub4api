package service

import (
	"context"
	"time"
)

type CodexTicketEvent map[string]any

type CodexTicketEventRepository interface {
	ListEvents(context.Context, int64, string, string, time.Time, time.Time, int, int) ([]CodexTicketEvent, int64, error)
}

func (s *OpenAIGatewayService) CodexTicketEvents(ctx context.Context, accountID int64, model, filter string, start, end time.Time, page, size int) ([]CodexTicketEvent, int64, error) {
	if model != "" && !modelTraceKnownGPTModel(model) {
		return nil, 0, ErrCodexTicketModel
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, 0, err
	}
	if !isOpenAICodexTicketAccount(account) {
		return nil, 0, ErrCodexTicketUnavailable
	}
	repo, ok := s.openaiCodexTicketHistory.(CodexTicketEventRepository)
	if !ok {
		return nil, 0, ErrCodexTicketUnavailable
	}
	return repo.ListEvents(ctx, accountID, model, filter, start, end, page, size)
}
