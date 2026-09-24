package service

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexObservationDialer struct {
	calls atomic.Int32
	fail  bool
	conn  openAIWSClientConn
}

func (d *codexObservationDialer) Dial(_ context.Context, _ string, headers http.Header, _ string) (openAIWSClientConn, int, http.Header, error) {
	d.calls.Add(1)
	// A transport rewriting the map cannot change the pre-dial snapshot.
	headers.Set("Cookie", "__oailb=new")
	response := changedTicketResponse()
	if d.fail {
		return nil, http.StatusUnauthorized, response.Header, errors.New("handshake failed")
	}
	if d.conn != nil {
		return d.conn, http.StatusSwitchingProtocols, response.Header, nil
	}
	return &openAIWSFakeConn{}, http.StatusSwitchingProtocols, response.Header, nil
}

func TestCodexTicketWSObservationRealDialsOnly(t *testing.T) {
	account := ticketTestAccount(71)
	ticket := verifiedTicket(account, "gpt-6-astra", "old-state", "__oailb=old")
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	capture := &codexInvalidationCapture{current: ticket}
	svc.openaiCodexTicketLifecycle = capture
	pool := newOpenAIWSConnPool(&config.Config{})
	defer pool.Close()
	dialer := &codexObservationDialer{}
	pool.setClientDialerForTest(dialer)
	observer := svc.codexTicketHandshakeObserver(context.Background(), ticket)
	observations := 0
	req := openAIWSAcquireRequest{Account: account, WSURL: "wss://upstream.example/responses", Headers: ticketObservationRequest(t, ticket).Header,
		ObserveHandshake: func(sent http.Header, status int, returned http.Header) {
			observations++
			observer(sent, status, returned)
		},
	}
	lease, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	id := lease.ConnID()
	lease.Release()
	require.Len(t, capture.events, 1)
	require.Equal(t, "__oailb=old", *capture.events[0].OriginalCookie)
	req.PreferredConnID = id
	req.ForcePreferredConn = true
	lease, err = pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, id, lease.ConnID())
	require.Equal(t, 1, observations, "a reused connection has no new handshake")
	lease.MarkBroken()
	lease.Release()
	req.PreferredConnID, req.ForcePreferredConn = "", false
	lease, err = pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	lease.Release()
	require.Equal(t, int32(2), dialer.calls.Load())
	require.Equal(t, 2, observations, "reconnect observes a fresh handshake")
	require.Len(t, capture.events, 1, "one invalidation per generation")
}

func TestCodexTicketWSObservationFailedHandshake(t *testing.T) {
	account := ticketTestAccount(71)
	ticket := verifiedTicket(account, "gpt-6-astra", "old-state", "__oailb=old")
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	capture := &codexInvalidationCapture{current: ticket}
	svc.openaiCodexTicketLifecycle = capture
	pool := newOpenAIWSConnPool(&config.Config{})
	defer pool.Close()
	pool.setClientDialerForTest(&codexObservationDialer{fail: true})
	req := openAIWSAcquireRequest{Account: account, WSURL: "wss://upstream.example/responses", Headers: ticketObservationRequest(t, ticket).Header,
		ObserveHandshake: svc.codexTicketHandshakeObserver(WithCodexTicketDiagnostic(context.Background(), account.ID), ticket),
	}
	_, err := pool.Acquire(context.Background(), req)
	require.Error(t, err)
	require.Len(t, capture.events, 1)
	require.Equal(t, http.StatusUnauthorized, *capture.events[0].ResponseHTTPStatus)
	require.Equal(t, "diagnostic", capture.events[0].RequestKind)
	require.Nil(t, svc.codexTicketHandshakeObserver(context.Background(), nil))
}

func TestCodexTicketWSHeadersPreserveInjectedGeneration(t *testing.T) {
	account := ticketTestAccount(71)
	ticket := verifiedTicket(account, "gpt-6-astra", "old-state", "__oailb=old")
	account.Extra = map[string]any{openAICodexTicketExtraKey(ticket.Model): ticket}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	headers, _, injected, err := svc.buildOpenAIWSHeadersWithTicket(context.Background(), nil, account, "token", OpenAIWSProtocolDecision{}, true, "client-state", "", "session", ticket.Model, "")
	require.NoError(t, err)
	require.NotNil(t, injected)
	require.Equal(t, ticket.GenerationID, injected.GenerationID)
	require.Equal(t, ticket.State, headers.Get(openAICodexTurnStateHeader))
	require.Equal(t, ticket.Cookie, headers.Get("Cookie"))
}

func TestCodexTicketWSPassthroughObservesSuccessAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			account := ticketTestAccount(901)
			account.Concurrency = 1
			account.Extra = map[string]any{"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough}
			ticket := verifiedTicket(account, "gpt-6-astra", "old-state", "__oailb=old")
			capture := &codexInvalidationCapture{current: ticket}
			cfg := passthroughLifecycleConfig()
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAICodexTicket.Enabled = true
			upstream := newStagedPassthroughConn()
			svc := newPassthroughLifecycleService(cfg, upstream)
			svc.openaiCodexTicketLifecycle = capture
			svc.openaiWSPassthroughDialer = &codexObservationDialer{fail: fail, conn: upstream}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			server, done := startPassthroughLifecycleServer(t, ctx, svc, account)
			defer server.Close()
			client := dialPassthroughLifecycleClientWithPayload(t, server, `{"type":"response.create","model":"gpt-6-astra","stream":false}`)
			defer client.CloseNow()
			if !fail {
				requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
				client.CloseNow()
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("passthrough did not exit")
			}
			require.Len(t, capture.events, 1)
			require.Equal(t, ticket.GenerationID, capture.events[0].TicketGenerationID)
			require.Equal(t, "__oailb=old", *capture.events[0].OriginalCookie)
		})
	}
}

func TestCodexTicketWSPrewarmChecksSentCredentials(t *testing.T) {
	for _, fail := range []bool{false, true} {
		for _, rewrite := range []string{"none", "turn-state", "cookie"} {
			t.Run(map[bool]string{false: "success", true: "failed"}[fail]+"/"+rewrite, func(t *testing.T) {
				account := ticketTestAccount(71)
				ticket := verifiedTicket(account, "gpt-6-astra", "old-state", "__oailb=old")
				capture := &codexInvalidationCapture{current: ticket}
				svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
				svc.openaiCodexTicketLifecycle = capture
				svc.openaiCodexTicketWake = make(chan struct{}, 1)
				pool := newOpenAIWSConnPool(&config.Config{})
				defer pool.Close()
				dialer := &codexObservationDialer{fail: fail}
				pool.setClientDialerForTest(dialer)
				req := openAIWSAcquireRequest{
					Account: account, WSURL: "wss://upstream.example/responses",
					Headers:          ticketObservationRequest(t, ticket).Header,
					ObserveHandshake: svc.codexTicketHandshakeObserver(context.Background(), ticket),
					HeadersFactory: func(_ context.Context, headers http.Header) (http.Header, error) {
						switch rewrite {
						case "turn-state":
							headers.Set(openAICodexTurnStateHeader, "connection-state")
						case "cookie":
							headers.Set("Cookie", "__oailb=connection-cookie")
						}
						return headers, nil
					},
				}
				ap := pool.getOrCreateAccountPool(account.ID)
				ap.mu.Lock()
				ap.lastAcquire, ap.creating = &req, 1
				ap.mu.Unlock()
				pool.prewarmConns(account.ID, req, 1)
				require.Equal(t, int32(1), dialer.calls.Load())
				require.Equal(t, rewrite == "none", len(capture.events) == 1)
				require.Equal(t, rewrite == "none", len(svc.openaiCodexTicketWake) == 1)
			})
		}
	}
}
