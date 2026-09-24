package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"
)

func (r *codexTicketAttemptRepository) ListEvents(ctx context.Context, accountID int64, model, filter string, start, end time.Time, page, size int) ([]service.CodexTicketEvent, int64, error) {
	if filter != "all" {
		return r.listFilteredEvents(ctx, accountID, model, filter, start, end, page, size)
	}
	const query = `WITH events AS (
		SELECT a.id, a.occurred_at, a.model, a.outcome AS kind,
			to_jsonb(a) - 'source' || jsonb_build_object('trigger', a.source, 'kind', a.outcome) AS payload
		FROM codex_ticket_attempts a WHERE a.account_id=$1 AND a.occurred_at >= $2 AND a.occurred_at < $3
			AND ($4='' OR a.model=$4)
		UNION ALL
		SELECT i.id, i.occurred_at, i.model, 'invalidation' AS kind,
			jsonb_build_object('id',i.id,'account_id',i.account_id,'model',i.model,'kind','invalidation',
				'occurred_at',i.occurred_at,'recorded_at',i.recorded_at,'ticket_generation_id',i.ticket_generation_id,
				'reason_code',i.reason_code,'response_http_status',i.response_http_status,'request_kind',i.request_kind,
				'request_route',i.request_route,'invalidated_current',i.invalidated_current,
				'original_ticket_present',i.original_ticket IS NOT NULL,'original_cookie_present',i.original_cookie IS NOT NULL,
				'returned_cookie_present',i.returned_cookie IS NOT NULL,'returned_set_cookie_count',COALESCE(jsonb_array_length(i.returned_set_cookies),0)) AS payload
		FROM codex_ticket_invalidations i WHERE i.account_id=$1 AND i.occurred_at >= $2 AND i.occurred_at < $3
			AND ($4='' OR i.model=$4)
	)
	SELECT payload FROM events ORDER BY occurred_at DESC, id DESC, kind LIMIT $5 OFFSET $6`
	const countQuery = `SELECT
		(SELECT COUNT(*) FROM codex_ticket_attempts WHERE account_id=$1 AND occurred_at >= $2 AND occurred_at < $3 AND ($4='' OR model=$4)) +
		(SELECT COUNT(*) FROM codex_ticket_invalidations WHERE account_id=$1 AND occurred_at >= $2 AND occurred_at < $3 AND ($4='' OR model=$4))`
	var total int64
	if err := r.db.QueryRowContext(ctx, countQuery, accountID, start, end, model).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, query, accountID, start, end, model, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result, err := scanCodexTicketEvents(rows, size)
	return result, total, err
}

func (r *codexTicketAttemptRepository) listFilteredEvents(ctx context.Context, accountID int64, model, filter string, start, end time.Time, page, size int) ([]service.CodexTicketEvent, int64, error) {
	table := "codex_ticket_attempts"
	if filter == "invalidation" {
		table = "codex_ticket_invalidations"
	} else if filter != "attempts" && filter != "success" && filter != "failure" {
		return nil, 0, fmt.Errorf("invalid codex ticket event filter")
	}
	condition := "account_id=$1 AND occurred_at >= $2 AND occurred_at < $3"
	args := []any{accountID, start, end}
	if model != "" {
		args = append(args, model)
		condition += " AND model=$4"
	}
	if filter == "success" {
		condition += " AND outcome='success'"
	} else if filter == "failure" {
		condition += " AND outcome IN ('miss','error')"
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE "+condition, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "occurred_at DESC,id DESC"
	payload := `jsonb_build_object('id',i.id,'account_id',i.account_id,'model',i.model,'kind','invalidation',
		'occurred_at',i.occurred_at,'recorded_at',i.recorded_at,'ticket_generation_id',i.ticket_generation_id,
		'reason_code',i.reason_code,'response_http_status',i.response_http_status,'request_kind',i.request_kind,
		'request_route',i.request_route,'invalidated_current',i.invalidated_current,
		'original_ticket_present',i.original_ticket IS NOT NULL,'original_cookie_present',i.original_cookie IS NOT NULL,
		'returned_cookie_present',i.returned_cookie IS NOT NULL,'returned_set_cookie_count',COALESCE(jsonb_array_length(i.returned_set_cookies),0))`
	alias := "i"
	if filter != "invalidation" {
		order += ",outcome"
		alias = "a"
		payload = "to_jsonb(a) - 'source' || jsonb_build_object('trigger', a.source, 'kind', a.outcome)"
	}
	query := fmt.Sprintf(`SELECT %s AS payload FROM (
		SELECT * FROM %s WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d
	) %s ORDER BY %s`, payload, table, condition, order, len(args)+1, len(args)+2, alias, order)
	rows, err := r.db.QueryContext(ctx, query, append(args, size, (page-1)*size)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result, err := scanCodexTicketEvents(rows, size)
	return result, total, err
}

func scanCodexTicketEvents(rows *sql.Rows, size int) ([]service.CodexTicketEvent, error) {
	result := make([]service.CodexTicketEvent, 0, size)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event service.CodexTicketEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

var _ service.CodexTicketEventRepository = (*codexTicketAttemptRepository)(nil)
