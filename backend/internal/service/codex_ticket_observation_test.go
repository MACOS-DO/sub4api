package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexInvalidationCapture struct {
	CodexTicketLifecycleRepository
	current      *openAICodexTicket
	events       []*CodexTicketInvalidation
	onInvalidate func()
	err          error
}

func (capture *codexInvalidationCapture) CurrentTicket(_ context.Context, _ int64, _ string) (json.RawMessage, error) {
	if capture.current == nil {
		return nil, nil
	}
	return json.Marshal(capture.current)
}

func (capture *codexInvalidationCapture) Invalidate(_ context.Context, event *CodexTicketInvalidation) (bool, error) {
	if capture.err != nil {
		return false, capture.err
	}
	if len(capture.events) > 0 {
		return false, nil
	}
	event.InvalidatedCurrent = capture.current != nil && capture.current.GenerationID == event.TicketGenerationID
	capture.events = append(capture.events, event)
	if capture.onInvalidate != nil {
		capture.onInvalidate()
	}
	return true, nil
}

func ticketObservationRequest(t *testing.T, ticket *openAICodexTicket) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	req.Header.Set(openAICodexTurnStateHeader, ticket.State)
	req.Header.Set("Cookie", ticket.Cookie)
	return req.WithContext(context.WithValue(req.Context(), codexTicketRequestContextKey{}, ticket))
}

func changedTicketResponse() *http.Response {
	headers := http.Header{}
	headers.Set(openAICodexTurnStateHeader, "new-state")
	headers.Add("Set-Cookie", "__oailb=new; Path=/; Secure; HttpOnly")
	headers.Add("Set-Cookie", "other=two; Secure")
	return &http.Response{StatusCode: http.StatusOK, Header: headers, Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n"))}
}

func TestCodexTicketObservationChangeMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, sentState, sentCookie, returnedState string
		setCookies                                 []string
		managed, want                              bool
	}{
		{"both changed", "old", "__oailb=old", "new", []string{"__oailb=new; Path=/; Secure"}, true, true},
		{"state only", "old", "__oailb=old", "new", []string{"__oailb=old"}, true, false},
		{"cookie only", "old", "__oailb=old", "old", []string{"__oailb=new"}, true, false},
		{"identical", "old", "__oailb=old", "old", []string{"__oailb=old"}, true, false},
		{"other cookie", "old", "__oailb=old; other=old", "new", []string{"other=new; Path=/", "__oailb=old"}, true, false},
		{"attributes order quotes", "old", "other=one; __oailb=old", "new", []string{`__oailb="old"; Secure; Path=/; Expires=Wed, 09 Jun 2027 10:18:14 GMT`, "other=two"}, true, false},
		{"missing state", "old", "__oailb=old", "", []string{"__oailb=new"}, true, false},
		{"missing returned cookie", "old", "__oailb=old", "new", nil, true, false},
		{"missing sent cookie", "old", "", "new", []string{"__oailb=new"}, true, false},
		{"missing sent state", "", "__oailb=old", "new", []string{"__oailb=new"}, true, false},
		{"invalid response cookie", "old", "__oailb=old", "new", []string{`__oailb="unfinished`}, true, false},
		{"invalid request cookie", "old", `__oailb="unfinished`, "new", []string{"__oailb=new"}, true, false},
		{"ambiguous response", "old", "__oailb=old", "new", []string{"__oailb=new", "__oailb=old; Path=/other"}, true, false},
		{"ambiguous request", "old", "__oailb=old; __oailb=another", "new", []string{"__oailb=new"}, true, false},
		{"explicit deletion", "old", "__oailb=old", "new", []string{"__oailb=; Max-Age=0; Path=/"}, true, true},
		{"no managed ticket", "old", "__oailb=old", "new", []string{"__oailb=new"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := ticketTestAccount(41)
			ticket := verifiedTicket(account, "gpt-6-astra", tc.sentState, tc.sentCookie)
			capture := &codexInvalidationCapture{current: ticket}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
			svc.openaiCodexTicketLifecycle = capture
			req := ticketObservationRequest(t, ticket)
			if !tc.managed {
				req = req.WithContext(context.Background())
			}
			header := http.Header{}
			header.Set(openAICodexTurnStateHeader, tc.returnedState)
			for _, cookie := range tc.setCookies {
				header.Add("Set-Cookie", cookie)
			}
			svc.observeCodexTicketResponse(req, &http.Response{StatusCode: 200, Header: header}, account)
			require.Equal(t, tc.want, len(capture.events) == 1)
		})
	}
}

func TestCodexTicketObservationDispatchSnapshotAndDiagnostics(t *testing.T) {
	for _, route := range []string{"/v1/responses", "/v1/messages"} {
		t.Run(route, func(t *testing.T) {
			account := ticketTestAccount(41)
			ticket := verifiedTicket(account, "gpt-6-astra", "old-state", "__oailb=old")
			generation := ticket.GenerationID
			capture := &codexInvalidationCapture{current: ticket}
			upstream := &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
				// Simulate a header mutation after dispatch; the snapshot must survive.
				req.Header.Set(openAICodexTurnStateHeader, "new-state")
				req.Header.Set("Cookie", "__oailb=new")
				return changedTicketResponse(), nil
			}}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, upstream)
			svc.openaiCodexTicketLifecycle = capture
			req := ticketObservationRequest(t, ticket)
			req.URL.Path = route
			req = req.WithContext(WithCodexTicketDiagnostic(req.Context(), account.ID))
			resp, err := svc.doOpenAIUpstream(req, "", account)
			require.NoError(t, err)
			resp.Body.Close()
			require.Len(t, capture.events, 1)
			event := capture.events[0]
			require.Equal(t, generation, event.TicketGenerationID)
			require.Equal(t, "old-state", *event.OriginalTicket)
			require.Equal(t, "__oailb=old", *event.OriginalCookie)
			require.Equal(t, CodexTicketInvalidationCredentialsChanged, event.ReasonCode)
			require.Equal(t, "diagnostic", event.RequestKind)
			require.Equal(t, route, event.RequestRoute)
			require.Len(t, event.ReturnedSetCookies, 2)
			require.True(t, event.Summary().OriginalCookiePresent)
		})
	}
}

func TestCodexTicketObservationGenerationAndCleanup(t *testing.T) {
	for _, tc := range []string{"current", "late", "new cache during transaction", "new retry during transaction", "persist failure", "without cache"} {
		t.Run(tc, func(t *testing.T) {
			account := ticketTestAccount(41)
			old := verifiedTicket(account, "gpt-6-astra", "old-state", "__oailb=old")
			fresh := verifiedTicket(account, old.Model, "fresh-state", "__oailb=fresh")
			other := verifiedTicket(account, "gpt-5.5", "other", "__oailb=other")
			capture := &codexInvalidationCapture{current: old}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
			svc.openaiCodexTicketLifecycle = capture
			svc.openaiCodexTicketWake = make(chan struct{}, 1)
			key := openAICodexTicketKey(account.ID, old.Model)
			otherKey := openAICodexTicketKey(account.ID, other.Model)
			retry := time.Now().Add(time.Hour)
			if tc != "without cache" {
				svc.openaiCodexTickets.Store(key, old)
			}
			svc.openaiCodexTickets.Store(otherKey, other)
			svc.openaiCodexTicketNextAttempt.Store(key, retry)
			svc.openaiCodexTicketNextAttempt.Store(otherKey, retry)
			switch tc {
			case "late":
				capture.current = fresh
				svc.openaiCodexTickets.Store(key, fresh)
			case "new cache during transaction":
				capture.onInvalidate = func() { svc.openaiCodexTickets.Store(key, fresh) }
			case "new retry during transaction":
				capture.onInvalidate = func() { svc.openaiCodexTicketNextAttempt.Store(key, retry.Add(time.Hour)) }
			case "persist failure":
				capture.err = errors.New("transaction failed")
			}
			req := ticketObservationRequest(t, old)
			svc.observeCodexTicketResponse(req, changedTicketResponse(), account)
			svc.observeCodexTicketResponse(req, changedTicketResponse(), account)
			if tc == "persist failure" {
				require.Empty(t, capture.events)
			} else {
				require.Len(t, capture.events, 1)
			}
			cached, cachedOK := svc.openaiCodexTickets.Load(key)
			_, retryOK := svc.openaiCodexTicketNextAttempt.Load(key)
			require.Equal(t, tc != "current" && tc != "without cache" && tc != "new retry during transaction", cachedOK)
			require.Equal(t, tc != "current" && tc != "without cache", retryOK)
			if tc == "late" || tc == "new cache during transaction" {
				require.Same(t, fresh, cached)
			}
			require.Equal(t, tc != "late" && tc != "persist failure", len(svc.openaiCodexTicketWake) == 1)
			_, ok := svc.openaiCodexTickets.Load(otherKey)
			require.True(t, ok)
			_, ok = svc.openaiCodexTicketNextAttempt.Load(otherKey)
			require.True(t, ok)
		})
	}
}

func TestCodexTicketObservationRequiresMatchingGenerationCredentials(t *testing.T) {
	for _, test := range []struct {
		name, savedState, savedCookie, sentState, sentCookie string
		invalidate                                           bool
	}{
		{"matching", "old", "__oailb=old", "old", "__oailb=old", true},
		{"historical state", "old", "__oailb=old", "connection-state", "__oailb=old", false},
		{"replaced cookie", "old", "__oailb=old", "old", "__oailb=connection-cookie", false},
		{"both replaced", "old", "__oailb=old", "connection-state", "__oailb=connection-cookie", false},
		{"cookie order and other values", "old", "__oailb=old; other=saved", "old", `other=sent; __oailb="old"`, true},
		{"duplicate same cookie", "old", "__oailb=old", "old", "__oailb=old; __oailb=old", true},
		{"conflicting sent cookie", "old", "__oailb=old", "old", "__oailb=old; __oailb=conflict", false},
		{"conflicting saved cookie", "old", "__oailb=old; __oailb=conflict", "old", "__oailb=old", false},
		{"missing sent state", "old", "__oailb=old", "", "__oailb=old", false},
		{"missing saved state", "", "__oailb=old", "old", "__oailb=old", false},
		{"missing sent cookie", "old", "__oailb=old", "old", "other=value", false},
		{"missing saved cookie", "old", "other=value", "old", "__oailb=old", false},
		{"invalid sent cookie", "old", "__oailb=old", "old", `__oailb="unfinished`, false},
		{"invalid saved cookie", "old", `__oailb="unfinished`, "old", "__oailb=old", false},
	} {
		for _, transport := range []string{"http", "websocket"} {
			t.Run(test.name+"/"+transport, func(t *testing.T) {
				account := ticketTestAccount(41)
				ticket := verifiedTicket(account, "gpt-6-astra", test.savedState, test.savedCookie)
				capture := &codexInvalidationCapture{current: ticket}
				svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
				svc.openaiCodexTicketLifecycle = capture
				svc.openaiCodexTicketWake = make(chan struct{}, 1)
				key := openAICodexTicketKey(account.ID, ticket.Model)
				retry := time.Now().Add(time.Hour)
				svc.openaiCodexTickets.Store(key, ticket)
				svc.openaiCodexTicketNextAttempt.Store(key, retry)
				req := ticketObservationRequest(t, ticket)
				req.Header.Set(openAICodexTurnStateHeader, test.sentState)
				req.Header.Set("Cookie", test.sentCookie)
				response := changedTicketResponse()
				defer response.Body.Close()
				if transport == "http" {
					svc.observeCodexTicketResponse(req, response, account)
				} else {
					observe := svc.codexTicketHandshakeObserver(context.Background(), ticket)
					observe(req.Header, response.StatusCode, response.Header)
				}
				require.Equal(t, test.invalidate, len(capture.events) == 1)
				require.Equal(t, test.invalidate, len(svc.openaiCodexTicketWake) == 1)
				cached, cachedOK := svc.openaiCodexTickets.Load(key)
				remainingRetry, retryOK := svc.openaiCodexTicketNextAttempt.Load(key)
				require.Equal(t, !test.invalidate, cachedOK)
				require.Equal(t, !test.invalidate, retryOK)
				if !test.invalidate {
					require.Same(t, ticket, cached)
					require.Equal(t, retry, remainingRetry)
				}
			})
		}
	}
}
