package service

import (
	"context"
	"time"
)

type CodexTicketRecentEvent struct {
	Model      string    `json:"model"`
	Kind       string    `json:"kind"`
	OccurredAt time.Time `json:"occurred_at"`
}

type CodexTicketRecentRepository interface {
	LatestEvents(context.Context, []int64) (map[int64]CodexTicketRecentEvent, error)
}

func (s *OpenAIGatewayService) CodexTicketLatestEvents(ctx context.Context, accountIDs []int64) (map[int64]CodexTicketRecentEvent, error) {
	repo, ok := s.openaiCodexTicketHistory.(CodexTicketRecentRepository)
	if !ok {
		return map[int64]CodexTicketRecentEvent{}, nil
	}
	return repo.LatestEvents(ctx, accountIDs)
}
