package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"
)

func (r *codexTicketAttemptRepository) CurrentTicket(ctx context.Context, accountID int64, model string) (json.RawMessage, error) {
	var raw json.RawMessage
	// A missing JSONB key is SQL NULL, which cannot be scanned into RawMessage.
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(extra->$2, 'null'::jsonb) FROM accounts WHERE id=$1 AND deleted_at IS NULL`, accountID, "codex_turn_ticket:"+model).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}

func (r *codexTicketAttemptRepository) Invalidate(ctx context.Context, event *service.CodexTicketInvalidation) (bool, error) {
	if event == nil || event.TicketGenerationID == "" || event.ReturnedTicket == "" {
		return false, errors.New("invalid ticket invalidation")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	key := "codex_turn_ticket:" + event.Model
	var current sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT extra->$2->>'generation_id' FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, event.AccountID, key).Scan(&current)
	if err != nil {
		return false, err
	}
	rawCookies, err := json.Marshal(event.ReturnedSetCookies)
	if err != nil {
		return false, err
	}
	var cookies any
	if len(event.ReturnedSetCookies) > 0 {
		cookies = string(rawCookies)
	}
	var originalTicket, originalCookie, returnedCookie any
	if event.OriginalTicket != nil {
		originalTicket = *event.OriginalTicket
	}
	if event.OriginalCookie != nil {
		originalCookie = *event.OriginalCookie
	}
	if event.ReturnedCookie != nil {
		returnedCookie = *event.ReturnedCookie
	}
	var insertedID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO codex_ticket_invalidations
		(account_id,model,ticket_generation_id,occurred_at,reason_code,response_http_status,request_kind,request_route,invalidated_current,
		 original_ticket,original_cookie,returned_ticket,returned_cookie,returned_set_cookies)
		VALUES ($1,$2,$3,$4,$14,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (ticket_generation_id) DO NOTHING RETURNING id`,
		event.AccountID, event.Model, event.TicketGenerationID, event.OccurredAt, event.ResponseHTTPStatus, event.RequestKind, event.RequestRoute,
		current.Valid && current.String == event.TicketGenerationID, originalTicket, originalCookie, event.ReturnedTicket, returnedCookie, cookies, event.ReasonCode).Scan(&insertedID)
	if errors.Is(err, sql.ErrNoRows) {
		_, updateErr := tx.ExecContext(ctx, `UPDATE codex_ticket_invalidations SET
			occurred_at=$2,response_http_status=$3,request_kind=$4,request_route=$5,
			original_ticket=$6,original_cookie=$7,returned_ticket=$8,returned_cookie=$9,returned_set_cookies=$10
			WHERE ticket_generation_id=$1 AND occurred_at>$2`,
			event.TicketGenerationID, event.OccurredAt, event.ResponseHTTPStatus, event.RequestKind, event.RequestRoute,
			originalTicket, originalCookie, event.ReturnedTicket, returnedCookie, cookies)
		if updateErr != nil {
			return false, updateErr
		}
		return false, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	invalidated := current.Valid && current.String == event.TicketGenerationID
	if invalidated {
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET extra=COALESCE(extra,'{}'::jsonb)-$2, updated_at=NOW() WHERE id=$1`, event.AccountID, key); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	event.ID, event.InvalidatedCurrent = insertedID, invalidated
	return true, nil
}

func (r *codexTicketAttemptRepository) ListInvalidations(ctx context.Context, accountID int64, model string, start, end time.Time, page, size int) ([]service.CodexTicketInvalidationSummary, int64, error) {
	var total int64
	condition := `account_id=$1 AND ($2='' OR model=$2) AND occurred_at >= GREATEST($3,NOW()-INTERVAL '90 days') AND occurred_at < $4`
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_ticket_invalidations WHERE `+condition, accountID, model, start, end).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,model,ticket_generation_id,occurred_at,recorded_at,reason_code,response_http_status,request_kind,request_route,invalidated_current,
		(original_ticket IS NOT NULL),(original_cookie IS NOT NULL),(returned_cookie IS NOT NULL),COALESCE(jsonb_array_length(returned_set_cookies),0)
		FROM codex_ticket_invalidations WHERE `+condition+` ORDER BY occurred_at DESC,id DESC LIMIT $5 OFFSET $6`, accountID, model, start, end, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := make([]service.CodexTicketInvalidationSummary, 0, size)
	for rows.Next() {
		var item service.CodexTicketInvalidationSummary
		var status sql.NullInt64
		item.AccountID = accountID
		if err := rows.Scan(&item.ID, &item.Model, &item.TicketGenerationID, &item.OccurredAt, &item.RecordedAt, &item.ReasonCode, &status, &item.RequestKind, &item.RequestRoute, &item.InvalidatedCurrent,
			&item.OriginalTicketPresent, &item.OriginalCookiePresent, &item.ReturnedCookiePresent, &item.ReturnedSetCookieCount); err != nil {
			return nil, 0, err
		}
		if status.Valid {
			value := int(status.Int64)
			item.ResponseHTTPStatus = &value
		}
		result = append(result, item)
	}
	return result, total, rows.Err()
}

func (r *codexTicketAttemptRepository) GetInvalidation(ctx context.Context, accountID, id int64) (*service.CodexTicketInvalidation, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id,model,ticket_generation_id,occurred_at,recorded_at,reason_code,response_http_status,request_kind,request_route,invalidated_current,
		original_ticket,original_cookie,returned_ticket,returned_cookie,returned_set_cookies
		FROM codex_ticket_invalidations WHERE account_id=$1 AND id=$2 AND occurred_at >= NOW()-INTERVAL '90 days'`, accountID, id)
	item := &service.CodexTicketInvalidation{AccountID: accountID}
	var status sql.NullInt64
	var original, oldCookie, newCookie sql.NullString
	var cookies []byte
	if err := row.Scan(&item.ID, &item.Model, &item.TicketGenerationID, &item.OccurredAt, &item.RecordedAt, &item.ReasonCode, &status, &item.RequestKind, &item.RequestRoute, &item.InvalidatedCurrent,
		&original, &oldCookie, &item.ReturnedTicket, &newCookie, &cookies); err != nil {
		return nil, err
	}
	if status.Valid {
		value := int(status.Int64)
		item.ResponseHTTPStatus = &value
	}
	if original.Valid {
		item.OriginalTicket = &original.String
	}
	if oldCookie.Valid {
		item.OriginalCookie = &oldCookie.String
	}
	if newCookie.Valid {
		item.ReturnedCookie = &newCookie.String
	}
	if len(cookies) > 0 {
		if err := json.Unmarshal(cookies, &item.ReturnedSetCookies); err != nil {
			return nil, fmt.Errorf("parse invalidation cookies: %w", err)
		}
	}
	return item, nil
}
