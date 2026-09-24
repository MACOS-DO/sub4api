package service

import (
	"net/http"
	"strings"
	"time"
	"unicode"
)

// Use the same account-specific rule for harvesting, hydration, and forwarding.
func openAICodexTicketTargetLength(account *Account, configured int) int {
	if account != nil && account.IsOpenAIOAuthLike() {
		plan := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) || r == '_' || r == '-' {
				return -1
			}
			return unicode.ToLower(r)
		}, account.GetCredential("plan_type"))
		// Business Standard and Business Premium both issue 332-byte tickets.
		if plan == "team" || plan == "selfservebusinessprolite" {
			return 332
		}
	}
	if configured > 0 {
		return configured
	}
	return 292
}

func openAICodexTicketResponseValid(account *Account, configured, status int, state string) bool {
	// A quota-limited request can still issue a usable ticket. Other HTTP errors
	// remain failures, even if they happen to carry a ticket-shaped header.
	return (status == http.StatusOK || status == http.StatusTooManyRequests) &&
		len(state) == openAICodexTicketTargetLength(account, configured) &&
		strings.HasPrefix(state, openAICodexTicketStatePrefix)
}

type openAICodexTicketQuotaState struct {
	paused   bool
	resetAt  *time.Time
	resumeAt *time.Time
}

// The account's existing rate-limit state is the sole authority for harvesting.
// The ticket probe never observes or writes account quota state.
func openAICodexTicketQuota(account *Account, now time.Time) openAICodexTicketQuotaState {
	state := openAICodexTicketQuotaState{}
	if account == nil || account.RateLimitResetAt == nil || !account.RateLimitResetAt.After(now) {
		return state
	}
	resetAt := *account.RateLimitResetAt
	state.resetAt = &resetAt
	state.resumeAt = &resetAt
	state.paused = now.Before(resetAt)
	return state
}

func openAICodexTicketHarvestLimited(account *Account, model string, now time.Time) bool {
	if openAICodexTicketQuota(account, now).paused {
		return true
	}
	if account == nil {
		return false
	}
	resetAt := account.modelRateLimitResetAt(model)
	if resetAt == nil || !resetAt.After(now) {
		return false
	}
	if account.Extra["allow_overages"] == true {
		creditsReset := account.modelRateLimitResetAt("AICredits")
		return creditsReset != nil && creditsReset.After(now)
	}
	return true
}
