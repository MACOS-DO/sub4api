package service

import (
	"time"
)

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
