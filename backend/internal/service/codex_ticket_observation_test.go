package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexInvalidationCapture struct {
	CodexTicketLifecycleRepository
	current      *openAICodexTicket
	events       []*CodexTicketInvalidation
	onInvalidate func()
}

func (capture *codexInvalidationCapture) CurrentTicket(_ context.Context, _ int64, _ string) (json.RawMessage, error) {
	return nil, nil
}

func (capture *codexInvalidationCapture) Invalidate(_ context.Context, event *CodexTicketInvalidation) (bool, error) {
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

func TestCodexTicketObservationCapturesFirstChangedTurnState(t *testing.T) {
	account := ticketTestAccount(41)
	ticket := verifiedTicket(account, "gpt-6-astra", "old-state", "old=one")
	capture := &codexInvalidationCapture{current: ticket}
	upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		header := http.Header{}
		header.Set(openAICodexTurnStateHeader, "new-state")
		header.Add("Set-Cookie", "session=two; HttpOnly")
		header.Add("Set-Cookie", "route=three; Secure")
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader("data: [DONE]\\n\\n"))}, nil
	}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, upstream)
	svc.openaiCodexTicketLifecycle = capture
	request, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	request = request.WithContext(context.WithValue(request.Context(), codexTicketRequestContextKey{}, ticket))
	response, err := svc.doOpenAIUpstream(request, "", account)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	_, err = svc.doOpenAIUpstream(request, "", account)
	require.NoError(t, err)
	require.Len(t, capture.events, 1)
	event := capture.events[0]
	require.True(t, event.InvalidatedCurrent)
	require.Equal(t, ticket.GenerationID, event.TicketGenerationID)
	require.Equal(t, "old-state", *event.OriginalTicket)
	require.Equal(t, "old=one", *event.OriginalCookie)
	require.Equal(t, "new-state", event.ReturnedTicket)
	require.Equal(t, []string{"session=two; HttpOnly", "route=three; Secure"}, event.ReturnedSetCookies)
	require.Equal(t, "/v1/responses", event.RequestRoute)
	summary := event.Summary()
	require.True(t, summary.OriginalTicketPresent)
	require.True(t, summary.ReturnedCookiePresent)
}

func TestCodexTicketObservationCookieOnlyAndRefreshedGeneration(t *testing.T) {
	account := ticketTestAccount(41)
	old := verifiedTicket(account, "gpt-5.5", "", "old=one")
	newTicket := verifiedTicket(account, "gpt-5.5", "new-current", "")
	capture := &codexInvalidationCapture{current: newTicket}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	svc.openaiCodexTicketLifecycle = capture
	request, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	request = request.WithContext(context.WithValue(request.Context(), codexTicketRequestContextKey{}, old))
	header := http.Header{}
	header.Set(openAICodexTurnStateHeader, "returned")
	svc.observeCodexTicketResponse(request, &http.Response{StatusCode: 200, Header: header}, account)
	require.Len(t, capture.events, 1)
	require.False(t, capture.events[0].InvalidatedCurrent)
	require.Nil(t, capture.events[0].OriginalTicket)
	require.Equal(t, "old=one", *capture.events[0].OriginalCookie)
	require.Equal(t, newTicket.GenerationID, capture.current.GenerationID)
}

func TestCodexTicketObservationIgnoresUnchangedOrEmptyState(t *testing.T) {
	account := ticketTestAccount(41)
	ticket := verifiedTicket(account, "gpt-6-astra", "same", "")
	capture := &codexInvalidationCapture{current: ticket}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	svc.openaiCodexTicketLifecycle = capture
	request, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	request = request.WithContext(context.WithValue(request.Context(), codexTicketRequestContextKey{}, ticket))
	for _, state := range []string{"", "same"} {
		header := http.Header{}
		header.Set(openAICodexTurnStateHeader, state)
		svc.observeCodexTicketResponse(request, &http.Response{StatusCode: 200, Header: header}, account)
	}
	require.Empty(t, capture.events)
}

func TestCodexTicketObservationDoesNotDeleteNewCachedGeneration(t *testing.T) {
	account := ticketTestAccount(41)
	old := verifiedTicket(account, "gpt-6-astra", "old", "")
	fresh := verifiedTicket(account, "gpt-6-astra", "fresh", "")
	capture := &codexInvalidationCapture{current: old}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	svc.openaiCodexTicketLifecycle = capture
	key := openAICodexTicketKey(account.ID, old.Model)
	svc.openaiCodexTickets.Store(key, old)
	capture.onInvalidate = func() { svc.openaiCodexTickets.Store(key, fresh) }
	request, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	request = request.WithContext(context.WithValue(request.Context(), codexTicketRequestContextKey{}, old))
	header := http.Header{}
	header.Set(openAICodexTurnStateHeader, "returned")
	svc.observeCodexTicketResponse(request, &http.Response{StatusCode: 200, Header: header}, account)
	cached, ok := svc.openaiCodexTickets.Load(key)
	require.True(t, ok)
	require.Equal(t, fresh.GenerationID, cached.(*openAICodexTicket).GenerationID)
}
