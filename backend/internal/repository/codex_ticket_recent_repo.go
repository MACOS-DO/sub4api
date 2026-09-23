package repository

import (
	"context"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/lib/pq"
)

func (r *codexTicketAttemptRepository) LatestEvents(ctx context.Context, accountIDs []int64) (map[int64]service.CodexTicketRecentEvent, error) {
	result := make(map[int64]service.CodexTicketRecentEvent)
	if len(accountIDs) == 0 {
		return result, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT ON (account_id) account_id,model,kind,occurred_at FROM (
		SELECT account_id,model,outcome AS kind,occurred_at,id FROM codex_ticket_attempts
		WHERE account_id=ANY($1) AND occurred_at >= NOW()-INTERVAL '90 days'
		UNION ALL
		SELECT account_id,model,'invalidation' AS kind,occurred_at,id FROM codex_ticket_invalidations
		WHERE account_id=ANY($1) AND occurred_at >= NOW()-INTERVAL '90 days'
	) events ORDER BY account_id,occurred_at DESC,id DESC,kind`, pq.Array(accountIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var event service.CodexTicketRecentEvent
		var accountID int64
		if err := rows.Scan(&accountID, &event.Model, &event.Kind, &event.OccurredAt); err != nil {
			return nil, err
		}
		result[accountID] = event
	}
	return result, rows.Err()
}

var _ service.CodexTicketRecentRepository = (*codexTicketAttemptRepository)(nil)
