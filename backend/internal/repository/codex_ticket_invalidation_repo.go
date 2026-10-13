package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/lib/pq"
)

// CurrentTicket returns the global shared ticket together with the accounts that
// invalidated its current generation and the row version that orders cache writes.
// A missing ticket yields an empty state at version 0.
func (r *codexTicketAttemptRepository) CurrentTicket(ctx context.Context) (*service.CodexSharedTicketState, error) {
	var raw json.RawMessage
	var invalidated pq.Int64Array
	var version int64
	err := r.db.QueryRowContext(ctx, `SELECT t.ticket,
		ARRAY(SELECT i.account_id FROM codex_ticket_invalidations i WHERE i.ticket_generation_id=t.generation_id ORDER BY i.account_id),
		t.version
		FROM codex_shared_tickets t WHERE t.id=1`).Scan(&raw, &invalidated, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return &service.CodexSharedTicketState{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &service.CodexSharedTicketState{Ticket: raw, InvalidatedAccountIDs: []int64(invalidated), Version: version}, nil
}

// StoreTicket replaces the global shared ticket; the latest successful harvest wins.
// Every replacement bumps the version so a cache can reject older reads.
func (r *codexTicketAttemptRepository) StoreTicket(ctx context.Context, generationID string, sourceAccountID int64, capturedAt time.Time, ticket json.RawMessage) error {
	if generationID == "" || len(ticket) == 0 {
		return errors.New("invalid shared ticket")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO codex_shared_tickets (id,generation_id,source_account_id,ticket,captured_at,updated_at)
		VALUES (1,$1,$2,$3,$4,NOW())
		ON CONFLICT (id) DO UPDATE SET generation_id=EXCLUDED.generation_id, source_account_id=EXCLUDED.source_account_id,
			ticket=EXCLUDED.ticket, captured_at=EXCLUDED.captured_at, updated_at=NOW(), version=codex_shared_tickets.version+1`,
		generationID, sourceAccountID, []byte(ticket), capturedAt)
	return err
}

// Invalidate records that event.AccountID can no longer use the generation. The
// shared ticket itself stays for every other account until a new one replaces it;
// a repeated report from the same account for the same generation is ignored.
func (r *codexTicketAttemptRepository) Invalidate(ctx context.Context, event *service.CodexTicketInvalidation) (bool, error) {
	if event == nil || event.TicketGenerationID == "" || event.Model == "" || event.ReturnedTicket == nil || *event.ReturnedTicket == "" {
		return false, errors.New("invalid ticket invalidation")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// Lock the ticket row so a concurrent StoreTicket cannot swap generations mid-check.
	// Compare as UUIDs so the textual case of the generation id never matters.
	var current bool
	err = tx.QueryRowContext(ctx, `SELECT generation_id = $1::uuid FROM codex_shared_tickets WHERE id=1 FOR UPDATE`, event.TicketGenerationID).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
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
	var originalTicket, originalCookie, returnedTicket, returnedCookie any
	if event.OriginalTicket != nil {
		originalTicket = *event.OriginalTicket
	}
	if event.ReturnedTicket != nil {
		returnedTicket = *event.ReturnedTicket
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
		ON CONFLICT (account_id, ticket_generation_id) DO NOTHING RETURNING id`,
		event.AccountID, event.Model, event.TicketGenerationID, event.OccurredAt, event.ResponseHTTPStatus, event.RequestKind, event.RequestRoute,
		current, originalTicket, originalCookie, returnedTicket, returnedCookie, cookies, event.ReasonCode).Scan(&insertedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	invalidated := current
	if invalidated {
		// The invalidated account set is part of the cached state; bump the version
		// so a cache write carrying the previous set is rejected.
		if _, err := tx.ExecContext(ctx, `UPDATE codex_shared_tickets SET version=version+1 WHERE id=1`); err != nil {
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
	var original, oldCookie, newTicket, newCookie sql.NullString
	var cookies []byte
	if err := row.Scan(&item.ID, &item.Model, &item.TicketGenerationID, &item.OccurredAt, &item.RecordedAt, &item.ReasonCode, &status, &item.RequestKind, &item.RequestRoute, &item.InvalidatedCurrent,
		&original, &oldCookie, &newTicket, &newCookie, &cookies); err != nil {
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
	if newTicket.Valid {
		item.ReturnedTicket = &newTicket.String
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
