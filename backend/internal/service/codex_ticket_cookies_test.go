package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketStoredRoutingCookieValidation(t *testing.T) {
	for name, test := range map[string]struct {
		cookie string
		valid  bool
	}{
		"complete":          {"__oailb=lb; __cflb=cf", true},
		"legacy oailb only": {"__oailb=lb", false},
		"cflb only":         {"__cflb=cf", false},
	} {
		t.Run(name, func(t *testing.T) {
			account := ticketTestAccount(41)
			ticket := verifiedTicket(account, openAICodexTicketHarvestModel, "state", test.cookie)
			ticket.CapturedAt = time.Now()
			require.Equal(t, test.valid, ticket.structurallyValid(0))
			raw, err := json.Marshal(ticket)
			require.NoError(t, err)
			for _, redisHit := range []bool{false, true} {
				svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, HarvestRefreshSeconds: 1800}, nil)
				svc.openaiCodexTicketLifecycle = &codexTicketLookupStub{raw: raw}
				if redisHit {
					cache := newMemoryCodexTicketCache()
					require.NoError(t, cache.SetSharedTicket(context.Background(), &CodexSharedTicketState{Ticket: raw}, time.Minute))
					svc.SetCodexSharedTicketCache(cache)
				}
				headers := http.Header{}
				err := svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", headers)
				if test.valid {
					require.NoError(t, err)
					require.Equal(t, test.cookie, headers.Get("Cookie"))
				} else {
					require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
					require.Empty(t, headers.Get("Cookie"))
				}
				status := svc.codexTicketStatusFor(context.Background(), account)
				require.Equal(t, test.valid, status.Ready)
				require.Equal(t, !test.valid, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
				require.Equal(t, !test.valid, svc.codexTicketAutomaticDue(account, time.Now()), "legacy incomplete tickets must trigger replacement")
				if !test.valid {
					account.Extra = map[string]any{"codex_allow_without_ticket": true}
					headers := http.Header{}
					require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", headers))
					require.Empty(t, headers.Get("Cookie"))
					account.Extra = nil
				}
			}
		})
	}
}
