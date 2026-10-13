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

// codexInvalidationCapture mirrors the repository: invalidations are recorded per
// account and generation, and the shared row is only replaced by a new harvest.
type codexInvalidationCapture struct {
	CodexTicketLifecycleRepository
	current      *openAICodexTicket
	events       []*CodexTicketInvalidation
	stored       []*openAICodexTicket
	onInvalidate func()
	err          error
	reads        int
}

func (capture *codexInvalidationCapture) CurrentTicket(_ context.Context) (*CodexSharedTicketState, error) {
	capture.reads++
	if capture.current == nil {
		return &CodexSharedTicketState{}, nil
	}
	raw, err := json.Marshal(capture.current)
	if err != nil {
		return nil, err
	}
	state := &CodexSharedTicketState{Ticket: raw}
	for _, event := range capture.events {
		if event.TicketGenerationID == capture.current.GenerationID {
			state.InvalidatedAccountIDs = append(state.InvalidatedAccountIDs, event.AccountID)
		}
	}
	return state, nil
}

func (capture *codexInvalidationCapture) StoreTicket(_ context.Context, generationID string, sourceAccountID int64, _ time.Time, raw json.RawMessage) error {
	var ticket openAICodexTicket
	if err := json.Unmarshal(raw, &ticket); err != nil {
		return err
	}
	if ticket.GenerationID != generationID || ticket.AccountID != sourceAccountID {
		return errors.New("shared ticket metadata mismatch")
	}
	capture.current = &ticket
	capture.stored = append(capture.stored, &ticket)
	return nil
}

func (capture *codexInvalidationCapture) Invalidate(_ context.Context, event *CodexTicketInvalidation) (bool, error) {
	if capture.err != nil {
		return false, capture.err
	}
	for _, existing := range capture.events {
		if existing.AccountID == event.AccountID && existing.TicketGenerationID == event.TicketGenerationID {
			return false, nil
		}
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
		{"both changed", "old", "__oailb=old; __cflb=route", "new", []string{"__oailb=new; Path=/; Secure"}, true, true},
		{"state only", "old", "__oailb=old; __cflb=route", "new", []string{"__oailb=old"}, true, false},
		{"state only without cookies", "old", "__oailb=old; __cflb=route", "new", nil, true, false},
		{"oailb only", "old", "__oailb=old; __cflb=route", "", []string{"__oailb=new"}, true, false},
		{"oailb changed with same state", "old", "__oailb=old; __cflb=route", "old", []string{"__oailb=new"}, true, false},
		{"identical", "old", "__oailb=old; __cflb=route", "old", []string{"__oailb=old"}, true, false},
		{"other cookie", "old", "__oailb=old; other=old; __cflb=route", "new", []string{"other=new; Path=/", "__oailb=old"}, true, false},
		{"attributes order quotes", "old", "other=one; __oailb=old; __cflb=route", "new", []string{`__oailb="old"; Secure; Path=/; Expires=Wed, 09 Jun 2027 10:18:14 GMT`, "other=two"}, true, false},
		{"missing sent cookie", "old", "", "new", []string{"__oailb=new"}, true, false},
		{"missing sent state", "", "__oailb=old; __cflb=route", "new", []string{"__oailb=new"}, true, false},
		{"invalid response cookie", "old", "__oailb=old; __cflb=route", "new", []string{`__oailb="unfinished`}, true, false},
		{"invalid request cookie", "old", `__oailb="unfinished; __cflb=route`, "new", []string{"__oailb=new"}, true, false},
		{"ambiguous response", "old", "__oailb=old; __cflb=route", "new", []string{"__oailb=new", "__oailb=old; Path=/other"}, true, false},
		{"ambiguous request", "old", "__oailb=old; __oailb=another; __cflb=route", "new", []string{"__oailb=new"}, true, false},
		{"explicit deletion", "old", "__oailb=old; __cflb=route", "new", []string{"__oailb=; Max-Age=0; Path=/"}, true, true},
		{"no managed ticket", "old", "__oailb=old; __cflb=route", "new", []string{"__oailb=new"}, false, false},
		{"state and cflb", "old", "__oailb=old; __cflb=route", "new", []string{"__cflb=new; Secure; Path=/"}, true, true},
		{"state and cflb with unchanged oailb", "old", "__oailb=old; __cflb=route", "new", []string{"__oailb=old", "__cflb=new"}, true, true},
		{"all three changed", "old", "__oailb=old; __cflb=route", "new", []string{"__oailb=new", "__cflb=new"}, true, true},
		{"cflb without returned state", "old", "__oailb=old; __cflb=route", "", []string{"__cflb=new"}, true, false},
		{"cflb with unchanged state", "old", "__oailb=old; __cflb=route", "old", []string{"__cflb=new"}, true, false},
		{"cflb attributes only", "old", "__oailb=old; __cflb=route", "new", []string{`__cflb="route"; Secure; Path=/other`}, true, false},
		{"explicit cflb deletion", "old", "__oailb=old; __cflb=route", "new", []string{"__cflb=; Max-Age=0; Path=/"}, true, true},
		{"malformed cflb response", "old", "__oailb=old; __cflb=route", "new", []string{`__cflb="unfinished`}, true, false},
		{"ambiguous cflb response", "old", "__oailb=old; __cflb=route", "new", []string{"__cflb=new", "__cflb=route; Path=/other"}, true, false},
		{"identical cflb duplicates", "old", "__oailb=old; __cflb=route", "new", []string{"__cflb=new", "__cflb=new; Path=/other"}, true, true},
		{"changed cflb despite malformed oailb", "old", "__oailb=old; __cflb=route", "new", []string{`__oailb="unfinished`, "__cflb=new"}, true, true},
		{"changed oailb despite malformed cflb", "old", "__oailb=old; __cflb=route", "new", []string{"__oailb=new", `__cflb="unfinished`}, true, true},
		{"changed cflb despite ambiguous oailb", "old", "__oailb=old; __cflb=route", "new", []string{"__oailb=new", "__oailb=other", "__cflb=new"}, true, true},
		{"missing sent cflb", "old", "__oailb=old", "new", []string{"__oailb=new"}, true, false},
	} {
		transports := []string{"http"}
		if tc.name == "state and cflb" {
			transports = append(transports, "websocket", "diagnostic")
		}
		for _, transport := range transports {
			t.Run(tc.name+"/"+transport, func(t *testing.T) {
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
				if tc.returnedState != "" {
					header.Set(openAICodexTurnStateHeader, tc.returnedState)
				}
				for _, cookie := range tc.setCookies {
					header.Add("Set-Cookie", cookie)
				}
				if transport == "websocket" {
					var used *openAICodexTicket
					if tc.managed {
						used = ticket
					}
					if observe := svc.codexTicketHandshakeObserver(context.Background(), account.ID, used); observe != nil {
						observe(req.Header, 200, header)
					}
				} else {
					if transport == "diagnostic" {
						req = req.WithContext(WithCodexTicketDiagnostic(req.Context(), account.ID))
					}
					svc.observeCodexTicketResponse(req, &http.Response{StatusCode: 200, Header: header}, account)
				}
				require.Equal(t, tc.want, len(capture.events) == 1)
				if tc.want {
					event := capture.events[0]
					require.Equal(t, CodexTicketInvalidationRoutingCookieChanged, event.ReasonCode)
					require.Equal(t, tc.sentState, *event.OriginalTicket)
					require.Equal(t, tc.sentCookie, *event.OriginalCookie)
					require.Equal(t, tc.returnedState, *event.ReturnedTicket)
				}
			})
		}
	}
}

func TestCodexTicketObservationDispatchSnapshotAndDiagnostics(t *testing.T) {
	for _, route := range []string{"/v1/responses", "/v1/messages"} {
		t.Run(route, func(t *testing.T) {
			account := ticketTestAccount(41)
			ticket := verifiedTicket(account, "gpt-6-astra", "old-state", "__oailb=old; __cflb=route")
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
			require.Equal(t, account.ID, event.AccountID)
			require.Equal(t, generation, event.TicketGenerationID)
			require.Equal(t, "old-state", *event.OriginalTicket)
			require.Equal(t, "__oailb=old; __cflb=route", *event.OriginalCookie)
			require.Equal(t, "new-state", *event.ReturnedTicket)
			require.Equal(t, CodexTicketInvalidationRoutingCookieChanged, event.ReasonCode)
			require.Equal(t, "diagnostic", event.RequestKind)
			require.Equal(t, route, event.RequestRoute)
			require.Len(t, event.ReturnedSetCookies, 2)
			require.True(t, event.Summary().OriginalCookiePresent)
			require.True(t, event.Summary().OriginalTicketPresent)
		})
	}
}

func TestCodexTicketObservationGenerationAndCleanup(t *testing.T) {
	for _, tc := range []string{"current", "late", "persist failure"} {
		t.Run(tc, func(t *testing.T) {
			account := ticketTestAccount(41)
			peer := ticketTestAccount(42)
			old := verifiedTicket(account, openAICodexTicketHarvestModel, "old-state", "__oailb=old; __cflb=route")
			fresh := verifiedTicket(peer, old.Model, "fresh-state", "__oailb=fresh; __cflb=route")
			capture := &codexInvalidationCapture{current: old}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
			svc.openaiCodexTicketLifecycle = capture
			svc.openaiCodexTicketWake = make(chan struct{}, 1)
			key := codexTicketRetryKey(account.ID)
			peerKey := codexTicketRetryKey(peer.ID)
			retry := time.Now().Add(time.Hour)
			seedSharedCodexTicket(svc, old)
			svc.openaiCodexTicketNextAttempt.Store(key, retry)
			svc.openaiCodexTicketNextAttempt.Store(peerKey, retry)
			switch tc {
			case "late":
				capture.current = fresh
			case "persist failure":
				capture.err = errors.New("transaction failed")
			}
			req := ticketObservationRequest(t, old)
			svc.observeCodexTicketResponse(req, changedTicketResponse(), account)
			svc.observeCodexTicketResponse(req, changedTicketResponse(), account)
			if tc == "persist failure" {
				require.Empty(t, capture.events)
			} else {
				require.Len(t, capture.events, 1, "one account reports a generation once")
				require.Equal(t, account.ID, capture.events[0].AccountID)
				require.Equal(t, tc == "current", capture.events[0].InvalidatedCurrent)
			}
			if tc == "late" {
				require.Same(t, fresh, capture.current)
			} else {
				require.Same(t, old, capture.current, "invalidation never deletes the shared ticket")
			}
			// An invalidation of the current generation is published to the local cache at once;
			// a late one leaves the shared state unchanged and is not republished; a failed write evicts it.
			entry := svc.openaiCodexTicket.Load()
			switch tc {
			case "persist failure":
				require.Nil(t, entry)
			case "late":
				require.Equal(t, old.GenerationID, entry.view.current().GenerationID)
				require.False(t, entry.view.invalidatedFor(account.ID))
				require.Zero(t, capture.reads, "a stale-generation report does not reload the shared state")
			default:
				require.Equal(t, old.GenerationID, entry.view.current().GenerationID)
				require.True(t, entry.view.invalidatedFor(account.ID))
				require.Nil(t, svc.sharedCodexTicketView().usableFor(account.ID))
				require.NotNil(t, svc.sharedCodexTicketView().usableFor(peer.ID), "other accounts keep the generation")
			}
			// Only invalidating the current generation clears backoff and wakes the harvester.
			current := tc == "current"
			_, retryOK := svc.openaiCodexTicketNextAttempt.Load(key)
			require.Equal(t, !current, retryOK)
			_, retryOK = svc.openaiCodexTicketNextAttempt.Load(peerKey)
			require.Equal(t, !current, retryOK)
			require.Equal(t, current, len(svc.openaiCodexTicketWake) == 1)
		})
	}
}

func TestCodexTicketObservationRequiresMatchingGenerationCredentials(t *testing.T) {
	for _, test := range []struct {
		name, savedState, savedCookie, sentState, sentCookie string
		invalidate                                           bool
	}{
		{"matching", "old", "__oailb=old; __cflb=route", "old", "__oailb=old; __cflb=route", true},
		{"historical state", "old", "__oailb=old; __cflb=route", "connection-state", "__oailb=old; __cflb=route", false},
		{"replaced cookie", "old", "__oailb=old; __cflb=route", "old", "__oailb=connection-cookie; __cflb=route", false},
		{"both replaced", "old", "__oailb=old; __cflb=route", "connection-state", "__oailb=connection-cookie; __cflb=route", false},
		{"cookie order and other values", "old", "__oailb=old; other=saved; __cflb=route", "old", `other=sent; __oailb="old"; __cflb=route`, true},
		{"duplicate same cookie", "old", "__oailb=old; __cflb=route", "old", "__oailb=old; __oailb=old; __cflb=route", true},
		{"conflicting sent cookie", "old", "__oailb=old; __cflb=route", "old", "__oailb=old; __oailb=conflict; __cflb=route", false},
		{"conflicting saved cookie", "old", "__oailb=old; __oailb=conflict; __cflb=route", "old", "__oailb=old; __cflb=route", false},
		{"missing sent state", "old", "__oailb=old; __cflb=route", "", "__oailb=old; __cflb=route", false},
		{"missing saved state", "", "__oailb=old; __cflb=route", "old", "__oailb=old; __cflb=route", false},
		{"missing sent cookie", "old", "__oailb=old; __cflb=route", "old", "other=value", false},
		{"missing saved cookie", "old", "other=value", "old", "__oailb=old; __cflb=route", false},
		{"invalid sent cookie", "old", "__oailb=old; __cflb=route", "old", `__oailb="unfinished; __cflb=route`, false},
		{"invalid saved cookie", "old", `__oailb="unfinished; __cflb=route`, "old", "__oailb=old; __cflb=route", false},
		{"replaced cflb", "old", "__oailb=old; __cflb=route", "old", "__oailb=old; __cflb=other", false},
		{"missing sent cflb", "old", "__oailb=old; __cflb=route", "old", "__oailb=old", false},
		{"missing saved cflb", "old", "__oailb=old", "old", "__oailb=old; __cflb=route", false},
		{"empty sent cflb", "old", "__oailb=old; __cflb=route", "old", "__oailb=old; __cflb=", false},
		{"conflicting sent cflb", "old", "__oailb=old; __cflb=route", "old", "__oailb=old; __cflb=route; __cflb=other", false},
		{"conflicting saved cflb", "old", "__oailb=old; __cflb=route; __cflb=other", "old", "__oailb=old; __cflb=route", false},
		{"identical sent cflb duplicates", "old", "__oailb=old; __cflb=route", "old", "__cflb=route; __oailb=old; __cflb=route", true},
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
				seedSharedCodexTicket(svc, ticket)
				svc.openaiCodexTicketNextAttempt.Store(key, retry)
				req := ticketObservationRequest(t, ticket)
				req.Header.Set(openAICodexTurnStateHeader, test.sentState)
				req.Header.Set("Cookie", test.sentCookie)
				response := changedTicketResponse()
				defer response.Body.Close()
				if transport == "http" {
					svc.observeCodexTicketResponse(req, response, account)
				} else {
					observe := svc.codexTicketHandshakeObserver(context.Background(), account.ID, ticket)
					observe(req.Header, response.StatusCode, response.Header)
				}
				require.Equal(t, test.invalidate, len(capture.events) == 1)
				require.Equal(t, test.invalidate, len(svc.openaiCodexTicketWake) == 1)
				entry := svc.openaiCodexTicket.Load()
				require.NotNil(t, entry)
				remainingRetry, retryOK := svc.openaiCodexTicketNextAttempt.Load(key)
				require.Equal(t, test.invalidate, entry.view.invalidatedFor(account.ID), "the invalidation is published to the local cache")
				require.Equal(t, !test.invalidate, retryOK)
				if !test.invalidate {
					require.Same(t, ticket, entry.view.current())
					require.Equal(t, retry, remainingRetry)
				}
			})
		}
	}
}

func TestCodexTicketSingleCredentialChangeDoesNotInvalidate(t *testing.T) {
	for name, change := range map[string]struct {
		state  string
		oailb  string
		others []string
	}{
		"turn-state only":               {state: fakeCodexTicketState(292), oailb: "__oailb=old; Path=/", others: []string{"other=rotated; Path=/"}},
		"oailb only":                    {state: "old-state", oailb: "__oailb=rotated; Path=/"},
		"oailb only, no returned state": {oailb: "__oailb=rotated; Path=/"},
	} {
		t.Run(name, func(t *testing.T) {
			account := ticketTestAccount(41)
			ticket := verifiedTicket(account, "gpt-6-astra", "old-state", "__oailb=old; __cflb=route")
			capture := &codexInvalidationCapture{current: ticket}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
			svc.openaiCodexTicketLifecycle = capture
			svc.openaiCodexTicketWake = make(chan struct{}, 1)
			seedSharedCodexTicket(svc, ticket)
			header := http.Header{}
			if change.state != "" {
				header.Set(openAICodexTurnStateHeader, change.state)
			}
			header.Add("Set-Cookie", change.oailb)
			for _, cookie := range change.others {
				header.Add("Set-Cookie", cookie)
			}
			for _, transport := range []string{"http", "websocket"} {
				req := ticketObservationRequest(t, ticket)
				if transport == "http" {
					svc.observeCodexTicketResponse(req, &http.Response{StatusCode: http.StatusOK, Header: header}, account)
				} else {
					svc.codexTicketHandshakeObserver(context.Background(), account.ID, ticket)(req.Header, http.StatusSwitchingProtocols, header)
				}
			}
			require.Empty(t, capture.events)
			require.Empty(t, svc.openaiCodexTicketWake)
			headers := http.Header{}
			require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, ticket.Model, headers))
			require.Equal(t, ticket.State, headers.Get(openAICodexTurnStateHeader))
			require.Equal(t, ticket.Cookie, headers.Get("Cookie"))
		})
	}
}

func TestCodexTicketInvalidationIsPerAccountUntilNewTicket(t *testing.T) {
	for _, cookieName := range []string{codexOAILBCookie, codexCFLBCookie} {
		t.Run(cookieName, func(t *testing.T) {
			const model = "gpt-6-astra"
			source, borrower := ticketTestAccount(41), ticketTestAccount(42)
			capture := &codexInvalidationCapture{}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
			svc.openaiCodexTicketLifecycle = capture
			svc.openaiCodexTicketWake = make(chan struct{}, 1)

			// Account A harvests; the shared ticket is persisted with A as the source.
			harvested := verifiedTicket(source, model, "shared-state", "__oailb=shared; route=a; __cflb=route")
			require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), source, harvested))
			require.Len(t, capture.stored, 1)
			require.Equal(t, source.ID, capture.stored[0].AccountID)
			svc.openaiCodexTicketNextAttempt.Store(openAICodexTicketKey(source.ID, model), time.Now().Add(time.Hour))
			svc.openaiCodexTicketNextAttempt.Store(openAICodexTicketKey(borrower.ID, model), time.Now().Add(time.Hour))

			// Account B's request carries the shared ticket's turn-state and cookie.
			headers := http.Header{}
			headers.Set(openAICodexTurnStateHeader, "client-state")
			injected, err := svc.applyOpenAICodexTicketWithGeneration(context.Background(), borrower, model, headers)
			require.NoError(t, err)
			require.NotNil(t, injected)
			require.Equal(t, harvested.GenerationID, injected.GenerationID)
			require.Equal(t, source.ID, injected.AccountID)
			require.Equal(t, harvested.Cookie, headers.Get("Cookie"))
			require.Equal(t, "shared-state", headers.Get(openAICodexTurnStateHeader))

			req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
			require.NoError(t, err)
			req.Header = headers
			req = req.WithContext(context.WithValue(req.Context(), codexTicketRequestContextKey{}, injected))

			// A rotated routing cookie without a new turn-state is not enough.
			response := http.Header{}
			response.Add("Set-Cookie", cookieName+"=rotated; Path=/; Secure")
			svc.observeCodexTicketResponse(req, &http.Response{StatusCode: http.StatusOK, Header: response}, borrower)
			require.Empty(t, capture.events)

			// A new turn-state plus either changed routing cookie invalidates B only.
			response.Set(openAICodexTurnStateHeader, "rotated-state")
			svc.observeCodexTicketResponse(req, &http.Response{StatusCode: http.StatusOK, Header: response}, borrower)

			require.Len(t, capture.events, 1)
			event := capture.events[0]
			require.Equal(t, borrower.ID, event.AccountID, "the event belongs to the account that observed the rotation")
			require.Equal(t, model, event.Model)
			require.Equal(t, harvested.GenerationID, event.TicketGenerationID)
			require.Equal(t, CodexTicketInvalidationRoutingCookieChanged, event.ReasonCode)
			require.True(t, event.InvalidatedCurrent)
			require.Equal(t, "shared-state", *event.OriginalTicket)
			require.Equal(t, "rotated-state", *event.ReturnedTicket)
			require.Equal(t, cookieName+"=rotated", *event.ReturnedCookie)
			require.Equal(t, harvested.GenerationID, capture.current.GenerationID, "the shared ticket survives for other accounts")
			for _, account := range []*Account{source, borrower} {
				_, ok := svc.openaiCodexTicketNextAttempt.Load(openAICodexTicketKey(account.ID, model))
				require.False(t, ok)
			}
			require.Len(t, svc.openaiCodexTicketWake, 1, "an invalidation starts harvesting at once")

			// A keeps using the ticket; B is denied and blocked until a new generation arrives.
			headers = http.Header{}
			injected, err = svc.applyOpenAICodexTicketWithGeneration(context.Background(), source, model, headers)
			require.NoError(t, err)
			require.Equal(t, harvested.GenerationID, injected.GenerationID)
			require.Equal(t, "shared-state", headers.Get(openAICodexTurnStateHeader))
			require.False(t, svc.openAICodexTicketBlocksAccount(source, model))
			headers = http.Header{}
			injected, err = svc.applyOpenAICodexTicketWithGeneration(context.Background(), borrower, model, headers)
			require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
			require.Nil(t, injected)
			require.Empty(t, headers.Get("Cookie"))
			require.Empty(t, headers.Get(openAICodexTurnStateHeader))
			require.True(t, svc.openAICodexTicketBlocksAccount(borrower, model))
			for _, account := range []*Account{source, borrower} {
				require.True(t, svc.codexTicketAutomaticDue(account, time.Now()), "a partly invalidated generation is due for replacement")
			}
			status := OpenAICodexTicketStatusOf(borrower, svc.openAICodexTicketConfig(), svc.CodexSharedTicketSnapshot(context.Background()), time.Now())
			require.False(t, status.Ready)
			require.True(t, status.InvalidatedForAccount)
			require.True(t, status.Blocked)

			// A new harvest replaces the ticket globally and B recovers.
			replacement := verifiedTicket(source, model, "next-state", "__oailb=next; __cflb=route")
			require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), source, replacement))
			headers = http.Header{}
			injected, err = svc.applyOpenAICodexTicketWithGeneration(context.Background(), borrower, model, headers)
			require.NoError(t, err)
			require.Equal(t, replacement.GenerationID, injected.GenerationID)
			require.Equal(t, "next-state", headers.Get(openAICodexTurnStateHeader))
			require.Equal(t, "__oailb=next; __cflb=route", headers.Get("Cookie"))
			require.False(t, svc.openAICodexTicketBlocksAccount(borrower, model))
			require.False(t, svc.codexTicketAutomaticDue(borrower, time.Now()))

		})
	}
}

// The invalidation records the gated model the request used, not the harvest model,
// and a repeated report of the same generation does not republish the shared state.
func TestCodexTicketInvalidationRecordsRequestModelOnce(t *testing.T) {
	const requestModel = "gpt-6-astra"
	source, borrower := ticketTestAccount(41), ticketTestAccount(42)
	capture := &codexInvalidationCapture{}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	svc.openaiCodexTicketLifecycle = capture
	svc.openaiCodexTicketWake = make(chan struct{}, 1)

	harvested := verifiedTicket(source, openAICodexTicketHarvestModel, "shared-state", "__oailb=shared; __cflb=route")
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), source, harvested))

	headers := http.Header{}
	injected, err := svc.applyOpenAICodexTicketWithGeneration(context.Background(), borrower, requestModel, headers)
	require.NoError(t, err)
	require.Equal(t, requestModel, injected.Model)
	require.Equal(t, openAICodexTicketHarvestModel, svc.sharedCodexTicketView().current().Model, "the shared ticket is not mutated")

	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	req.Header = headers
	req = req.WithContext(context.WithValue(req.Context(), codexTicketRequestContextKey{}, injected))
	response := http.Header{}
	response.Set(openAICodexTurnStateHeader, "rotated-state")
	response.Add("Set-Cookie", "__oailb=rotated; Path=/; Secure")

	reads := capture.reads
	svc.observeCodexTicketResponse(req, &http.Response{StatusCode: http.StatusOK, Header: response}, borrower)
	require.Len(t, capture.events, 1)
	require.Equal(t, requestModel, capture.events[0].Model)
	require.Equal(t, reads+1, capture.reads, "a new invalidation republishes the shared state")

	svc.observeCodexTicketResponse(req, &http.Response{StatusCode: http.StatusOK, Header: response}, borrower)
	require.Len(t, capture.events, 1)
	require.Equal(t, reads+1, capture.reads, "a repeated report does not reload the shared state")
}
