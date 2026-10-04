package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
	infraerrors "github.com/MACOS-DO/sub4api/internal/pkg/errors"
)

func (s *CodexGatewayService) Auxiliary(ctx context.Context, id int64, method, path string, input, result any) error {
	if s == nil || s.client == nil {
		return codexgateway.Unavailable()
	}
	if !s.Ready() {
		if err := s.Check(ctx); err != nil {
			return err
		}
	}
	account, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if !account.IsOpenAICodex() || account.Gateway == nil {
		return infraerrors.BadRequest("CODEX_ACCOUNT_REQUIRED", "OpenAI Codex binding required")
	}
	if account.IsShadow() && method != http.MethodGet {
		return infraerrors.BadRequest("CODEX_PARENT_REQUIRED", "perform this operation on the parent account")
	}
	var body []byte
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return infraerrors.BadRequest("CODEX_AUXILIARY_INPUT", "invalid account operation")
		}
	}
	response, err := s.client.Auxiliary(ctx, account.Gateway.BindingID, method, path, bytes.NewReader(body), http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return codexgateway.Unavailable()
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return codexgateway.ManagementResponseError(response, raw)
	}

	if result != nil && len(raw) > 0 {
		if err = json.Unmarshal(raw, result); err != nil {
			return infraerrors.New(502, "CODEX_AUXILIARY_RESPONSE", "invalid Gateway account response")
		}
	}
	return nil
}

func (s *OpenAIQuotaService) queryCodexGatewayQuota(ctx context.Context, id int64) (*OpenAIQuotaUsage, error) {
	if s == nil || s.codexGateway == nil {
		return nil, codexgateway.Unavailable()
	}
	var usage OpenAIQuotaUsage
	if err := s.codexGateway.Auxiliary(ctx, id, http.MethodGet, "usage", nil, &usage); err != nil {
		return nil, err
	}
	usage.FetchedAt = time.Now().Unix()
	var raw json.RawMessage
	if err := s.codexGateway.Auxiliary(ctx, id, http.MethodGet, "reset-credits", nil, &raw); err == nil {
		if details, err := parseOpenAIRateLimitResetCreditDetails(raw); err == nil || details.AvailableCount != nil {
			if details.AvailableCount == nil && !details.CreditListPresent {
				return &usage, nil
			}
			if usage.RateLimitResetCredits == nil {
				usage.RateLimitResetCredits = &OpenAIRateLimitResetCredits{}
			}
			if details.CreditListPresent {
				usage.RateLimitResetCredits.Credits = details.Credits
			}
			if details.AvailableCount != nil {
				usage.RateLimitResetCredits.AvailableCount = *details.AvailableCount
			} else if details.CreditListPresent {
				usage.RateLimitResetCredits.AvailableCount = details.AvailableCreditCount
			}
		}
	}
	return &usage, nil
}

func (s *OpenAIQuotaService) resetCodexGatewayQuota(ctx context.Context, id int64, creditID, requestID string) (*OpenAIQuotaResetResult, error) {
	if s == nil || s.codexGateway == nil {
		return nil, codexgateway.Unavailable()
	}
	input := map[string]string{"redeem_request_id": requestID}
	if creditID != "" {
		input["credit_id"] = creditID
	}
	var result OpenAIQuotaResetResult
	if err := s.codexGateway.Auxiliary(ctx, id, http.MethodPost, "reset-credits/consume", input, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *OpenAIQuotaService) queryCodexGatewayReferral(ctx context.Context, id int64, program string) (*OpenAIReferralEligibility, error) {
	if s == nil || s.codexGateway == nil {
		return nil, codexgateway.Unavailable()
	}
	var result OpenAIReferralEligibility
	if err := s.codexGateway.Auxiliary(ctx, id, http.MethodGet, "referrals/eligibility?program_id="+url.QueryEscape(program), nil, &result); err != nil {
		return nil, err
	}
	result.ProgramID = program
	result.AvailableInvites = referralCapacity(&result)
	result.FetchedAt = time.Now().Unix()
	return &result, nil
}

func (s *adminServiceImpl) SetCodexGatewayPrivacy(ctx context.Context, id int64) (*Account, error) {
	if s == nil || s.codexGateway == nil {
		return nil, codexgateway.Unavailable()
	}
	var result json.RawMessage
	if err := s.codexGateway.Auxiliary(ctx, id, http.MethodPatch, "privacy", map[string]bool{"training_allowed": false}, &result); err != nil {
		return nil, err
	}
	if err := s.accountRepo.UpdateExtra(ctx, id, map[string]any{"privacy_mode": PrivacyModeTrainingOff}); err != nil {
		return nil, err
	}
	return s.GetAccount(ctx, id)
}

func (s *adminServiceImpl) RefreshCodexGatewayProfile(ctx context.Context, id int64) (*Account, error) {
	if s == nil || s.codexGateway == nil {
		return nil, codexgateway.Unavailable()
	}
	var profile codexgateway.Metadata
	if err := s.codexGateway.Auxiliary(ctx, id, http.MethodGet, "profile", nil, &profile); err != nil {
		return nil, err
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	owner := id
	if account.ParentAccountID != nil {
		owner = *account.ParentAccountID
	}
	err = s.codexGateway.WithAccountLock(ctx, owner, func(ctx context.Context) error {
		binding, err := s.codexGateway.bindings.GetCodexBinding(ctx, owner)
		if err != nil {
			return err
		}
		if binding == nil {
			return ErrAccountNotFound
		}
		binding.Profile = &profile
		return s.codexGateway.save(ctx, binding, binding.ConfigVersion, binding.OperationID)
	})
	if err != nil {
		return nil, err
	}
	return s.GetAccount(ctx, id)
}

func (s *AccountUsageService) probeCodexGatewayUsageSnapshot(ctx context.Context, account *Account) (map[string]any, error) {
	if s.openAIQuotaService == nil {
		return nil, codexgateway.Unavailable()
	}
	usage, err := s.openAIQuotaService.QueryUsage(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	if usage == nil || usage.RateLimit == nil {
		return nil, nil
	}
	now := time.Now()
	if account.IsShadow() {
		return buildCodexSparkWindowExtraUpdates(usage, now), nil
	}
	snapshot := &OpenAICodexUsageSnapshot{UpdatedAt: now.UTC().Format(time.RFC3339)}
	window := func(w *OpenAIRateLimitWindow) (*float64, *int, *int) {
		if w == nil {
			return nil, nil, nil
		}
		used := w.UsedPercent
		reset := int(w.ResetAfterSeconds)
		minutes := int(w.LimitWindowSeconds / 60)
		if reset <= 0 && w.ResetAt > 0 {
			reset = max(0, int(w.ResetAt-now.Unix()))
		}
		return &used, &reset, &minutes
	}
	snapshot.PrimaryUsedPercent, snapshot.PrimaryResetAfterSeconds, snapshot.PrimaryWindowMinutes = window(usage.RateLimit.PrimaryWindow)
	snapshot.SecondaryUsedPercent, snapshot.SecondaryResetAfterSeconds, snapshot.SecondaryWindowMinutes = window(usage.RateLimit.SecondaryWindow)
	updates := buildCodexUsageExtraUpdates(snapshot, now)
	s.persistOpenAICodexProbeSnapshot(account.ID, updates)
	return updates, nil
}
