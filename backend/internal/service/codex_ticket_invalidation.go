package service

import (
	"context"
	"encoding/json"
	"time"
)

type CodexTicketInvalidation struct {
	ID                 int64     `json:"id"`
	AccountID          int64     `json:"account_id"`
	Model              string    `json:"model"`
	TicketGenerationID string    `json:"ticket_generation_id"`
	OccurredAt         time.Time `json:"occurred_at"`
	RecordedAt         time.Time `json:"recorded_at"`
	ReasonCode         string    `json:"reason_code"`
	ResponseHTTPStatus *int      `json:"response_http_status"`
	RequestKind        string    `json:"request_kind"`
	RequestRoute       string    `json:"request_route"`
	InvalidatedCurrent bool      `json:"invalidated_current"`
	OriginalTicket     *string   `json:"original_ticket,omitempty"`
	OriginalCookie     *string   `json:"original_cookie,omitempty"`
	ReturnedTicket     string    `json:"returned_ticket,omitempty"`
	ReturnedCookie     *string   `json:"returned_cookie,omitempty"`
	ReturnedSetCookies []string  `json:"returned_set_cookies,omitempty"`
}

type CodexTicketInvalidationSummary struct {
	ID                     int64     `json:"id"`
	AccountID              int64     `json:"account_id"`
	Model                  string    `json:"model"`
	TicketGenerationID     string    `json:"ticket_generation_id"`
	OccurredAt             time.Time `json:"occurred_at"`
	RecordedAt             time.Time `json:"recorded_at"`
	ReasonCode             string    `json:"reason_code"`
	ResponseHTTPStatus     *int      `json:"response_http_status"`
	RequestKind            string    `json:"request_kind"`
	RequestRoute           string    `json:"request_route"`
	InvalidatedCurrent     bool      `json:"invalidated_current"`
	OriginalTicketPresent  bool      `json:"original_ticket_present"`
	OriginalCookiePresent  bool      `json:"original_cookie_present"`
	ReturnedCookiePresent  bool      `json:"returned_cookie_present"`
	ReturnedSetCookieCount int       `json:"returned_set_cookie_count"`
}

func (event CodexTicketInvalidation) Summary() CodexTicketInvalidationSummary {
	return CodexTicketInvalidationSummary{
		ID: event.ID, AccountID: event.AccountID, Model: event.Model,
		TicketGenerationID: event.TicketGenerationID, OccurredAt: event.OccurredAt,
		RecordedAt: event.RecordedAt, ReasonCode: event.ReasonCode,
		ResponseHTTPStatus: event.ResponseHTTPStatus, RequestKind: event.RequestKind,
		RequestRoute: event.RequestRoute, InvalidatedCurrent: event.InvalidatedCurrent,
		OriginalTicketPresent:  event.OriginalTicket != nil,
		OriginalCookiePresent:  event.OriginalCookie != nil,
		ReturnedCookiePresent:  event.ReturnedCookie != nil,
		ReturnedSetCookieCount: len(event.ReturnedSetCookies),
	}
}

type CodexTicketLifecycleRepository interface {
	CurrentTicket(context.Context, int64, string) (json.RawMessage, error)
	Invalidate(context.Context, *CodexTicketInvalidation) (bool, error)
	ListInvalidations(context.Context, int64, string, time.Time, time.Time, int, int) ([]CodexTicketInvalidationSummary, int64, error)
	GetInvalidation(context.Context, int64, int64) (*CodexTicketInvalidation, error)
}
