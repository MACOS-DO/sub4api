package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIBPSCredentialStateExpiry(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name    string
		expires any
		status  string
	}{
		{"RFC3339 expired", now.Add(-time.Hour).Format(time.RFC3339), "expired"},
		{"Unix seconds", now.Add(-time.Hour).Unix(), "expired"},
		{"Unix string", "0", "expired"},
		{"JSON number", json.Number("0"), "expired"},
		{"Unix milliseconds", now.Add(-time.Hour).UnixMilli(), "expired"},
		{"boundary", now.Unix(), "expired"},
		{"future", now.Add(time.Hour).Format(time.RFC3339), "not_expired"},
		{"unknown", nil, "unknown"}, {"invalid", "invalid", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, a := bpsFixture()
			a.Credentials["expires_at"] = tc.expires
			state := a.OpenAIBPSCredentialState(now)
			require.Equal(t, tc.status, state.Status)
			require.Equal(t, StatusActive, a.Status)
			require.True(t, a.Schedulable)
			if tc.status == "expired" {
				require.False(t, a.IsSchedulable())
				require.NotNil(t, state.ExpiresAt)
				require.Equal(t, "bps_token_expired", state.ErrorCode)
			}
		})
	}
	a := &Account{Platform: PlatformOpenAI}
	require.Nil(t, a.OpenAIBPSCredentialState(now))
}

func TestOpenAIBPSCredentialStateRevokedBeforeExpiry(t *testing.T) {
	_, a := bpsFixture()
	now := time.Now().UTC()
	a.Credentials["expires_at"] = now.Add(time.Hour).Format(time.RFC3339)
	a.Extra = map[string]any{OpenAIBPSCredentialStateExtraKey: map[string]any{"status": "revoked", "error_code": "token_revoked", "observed_at": now.Format(time.RFC3339Nano), "managed_status_error": true}}
	state := a.OpenAIBPSCredentialState(now)
	require.Equal(t, "revoked", state.Status)
	require.Equal(t, "token_revoked", state.ErrorCode)
	require.NotNil(t, state.ObservedAt)
	require.False(t, a.IsSchedulable())
	// Revocation remains more specific than a later natural deadline.
	require.Equal(t, "revoked", a.OpenAIBPSCredentialState(now.Add(2*time.Hour)).Status)
}

func TestOpenAIBPSCredentialStateReplacementAndBlankSave(t *testing.T) {
	for _, tc := range []struct {
		name, status  string
		schedulable   bool
		token         string
		wantStatus    string
		keepDiagnosis bool
	}{
		{"recover automatic error", StatusError, true, "new-token", StatusActive, false},
		{"keep manual pause", StatusError, false, "new-token", StatusActive, false},
		{"keep disabled", StatusDisabled, true, "new-token", StatusDisabled, false},
		{"keep inactive", "inactive", false, "new-token", "inactive", false},
		{"blank preserves error", StatusError, true, "", StatusError, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, a := bpsFixture()
			a.Status = tc.status
			a.Schedulable = tc.schedulable
			a.ErrorMessage = bpsCredentialAuthFailedMessage
			a.Extra = map[string]any{OpenAIBPSCredentialStateExtraKey: map[string]any{"status": "revoked", "error_code": "token_revoked", "managed_status_error": true}}
			repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{a.ID: a}}
			admin := &adminServiceImpl{accountRepo: repo}
			updated, err := admin.UpdateAccount(context.Background(), a.ID, &UpdateAccountInput{Credentials: map[string]any{"access_token": tc.token}, Extra: map[string]any{OpenAIBPSCredentialStateExtraKey: map[string]any{"status": "unknown"}, "note": "keep"}})
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, updated.Status)
			require.Equal(t, tc.schedulable, updated.Schedulable)
			_, exists := updated.Extra[OpenAIBPSCredentialStateExtraKey]
			require.Equal(t, tc.keepDiagnosis, exists)
			if tc.keepDiagnosis {
				require.Equal(t, "revoked", updated.OpenAIBPSCredentialState(time.Now()).Status)
			}
			if updated.Status == StatusActive && !updated.Schedulable {
				require.True(t, updated.OpenAIBPSCredentialState(time.Now()).RequiresManualResume)
			}
		})
	}
}

func TestOpenAIBPSCredentialStateLockedMerge(t *testing.T) {
	_, a := bpsFixture()
	diagnosis := map[string]any{"status": "revoked", "error_code": "token_revoked", "managed_status_error": true, "credential_identity": OpenAIBPSCredentialIdentity(OpenAIBPSCredentialSnapshotFromAccount(a))}
	current := map[string]any{OpenAIBPSCredentialStateExtraKey: diagnosis}
	// A stale form changing a model mapping cannot erase a concurrent 401.
	result := MergeOpenAIBPSCredentialStateExtra(a, map[string]any{"note": "updated"}, current, false)
	require.Equal(t, diagnosis, result[OpenAIBPSCredentialStateExtraKey])
	require.Equal(t, StatusError, a.Status)
	require.True(t, a.Schedulable)
	a.Credentials["access_token"] = "replacement"
	a.Status = StatusActive
	result = MergeOpenAIBPSCredentialStateExtra(a, map[string]any{OpenAIBPSCredentialStateExtraKey: diagnosis}, current, false)
	require.NotContains(t, result, OpenAIBPSCredentialStateExtraKey)
	require.Equal(t, StatusActive, a.Status)
}

type bpsCredentialMutationRepo struct {
	AccountRepository
	current  *Account
	observed OpenAIBPSCredentialSnapshot
	writes   int
}

func (r *bpsCredentialMutationRepo) SetOpenAIBPSCredentialErrorIfMatch(_ context.Context, _ int64, snapshot OpenAIBPSCredentialSnapshot, state OpenAIBPSCredentialState) (bool, error) {
	r.observed = snapshot
	if snapshot != OpenAIBPSCredentialSnapshotFromAccount(r.current) {
		return false, nil
	}
	r.writes++
	r.current.Extra = map[string]any{OpenAIBPSCredentialStateExtraKey: map[string]any{"status": state.Status, "error_code": state.ErrorCode}}
	if r.current.Status == StatusActive {
		r.current.Status = StatusError
	}
	return true, nil
}

type bpsCredentialChangingHTTP struct {
	HTTPUpstream
	account *Account
}

func (u *bpsCredentialChangingHTTP) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	old := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	u.account.Credentials = map[string]any{"access_token": "new-token", "chatgpt_account_id": "new-workspace"}
	return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bpsJSON(map[string]any{"error": map[string]any{"code": "token_revoked", "message": "Invalid " + old}})))}, nil
}
func TestOpenAIBPSCredentialStateRejectsLateResponse(t *testing.T) {
	s, a := bpsFixture()
	repo := &bpsCredentialMutationRepo{current: a}
	s.accountRepo = repo
	s.httpUpstream = &bpsCredentialChangingHTTP{account: a}
	c, rec := bpsContext(1, "/responses")
	_, err := s.Forward(c.Request.Context(), c, a, bpsRequestBody("hi", false))
	require.Error(t, err)
	require.Equal(t, "test-secret", repo.observed.AccessToken)
	require.Equal(t, "workspace-1", repo.observed.AccountID)
	require.Zero(t, repo.writes)
	require.Equal(t, StatusActive, a.Status)
	require.True(t, a.Schedulable)
	require.NotContains(t, rec.Body.String(), "test-secret")
}

func TestOpenAIBPSAccountTestDiagnostics(t *testing.T) {
	for _, status := range []int{200, 401, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s, a := bpsFixture()
			repo := &bpsCredentialMutationRepo{current: a}
			s.accountRepo = repo
			body := bpsJSON(bpsResponse(bpsText("OK")))
			code := ""
			if status == 401 {
				code = "token_revoked"
			}
			if status == 403 {
				code = "basispoints_model_access_changed"
			}
			if status != 200 {
				body = bpsJSON(map[string]any{"error": map[string]any{"code": code, "message": "upstream rejected"}})
			}
			// A model denial uses the existing model-scoped repository fixture.
			if status == 403 {
				s.accountRepo = &bpsFailureRepo{}
			}
			s.httpUpstream = &bpsHTTPStub{status: status, contentType: "application/json", body: body}
			service := &AccountTestService{openaiGatewayService: s}
			c, rec := bpsContext(1, "/admin/test")
			err := service.testOpenAIBPSAccountConnection(c, a, "gpt-6-astra", "hi", "default")
			var events []gjson.Result
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if strings.HasPrefix(line, "data: ") {
					events = append(events, gjson.Parse(strings.TrimPrefix(line, "data: ")))
				}
			}
			require.Equal(t, "test_start", events[0].Get("type").String())
			require.Equal(t, "upstream_response", events[1].Get("type").String())
			require.Equal(t, int64(status), events[1].Get("upstream_status").Int())
			require.Equal(t, "gpt-6-astra", events[1].Get("upstream_model").String())
			last := events[len(events)-1]
			if status == 200 {
				require.NoError(t, err)
				require.Equal(t, "test_complete", last.Get("type").String())
				require.True(t, last.Get("success").Bool())
			} else {
				require.Error(t, err)
				require.Equal(t, "error", last.Get("type").String())
				require.Equal(t, code, last.Get("upstream_error_code").String())
				require.Equal(t, int64(status), last.Get("upstream_status").Int())
			}
			if status == 401 {
				require.Equal(t, 1, repo.writes)
				require.True(t, a.Schedulable)
				require.Equal(t, StatusError, a.Status)
			}
		})
	}
}
