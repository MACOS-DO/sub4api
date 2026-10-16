//go:build unit

package service

import (
	"context"
	"errors"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type timezoneDetectAccountRepo struct {
	mockAccountRepoForGemini
	mu       sync.Mutex
	accounts map[int64]*Account
}

func (r *timezoneDetectAccountRepo) clone(id int64) *Account {
	r.mu.Lock()
	defer r.mu.Unlock()
	account, ok := r.accounts[id]
	if !ok {
		return nil
	}
	copied := *account
	copied.Extra = maps.Clone(account.Extra)
	return &copied
}

func (r *timezoneDetectAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if account := r.clone(id); account != nil {
		return account, nil
	}
	return nil, ErrAccountNotFound
}

func (r *timezoneDetectAccountRepo) Create(_ context.Context, account *Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if account.ID == 0 {
		account.ID = int64(len(r.accounts) + 100)
	}
	copied := *account
	copied.Extra = maps.Clone(account.Extra)
	r.accounts[account.ID] = &copied
	return nil
}

func (r *timezoneDetectAccountRepo) Update(_ context.Context, account *Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := *account
	copied.Extra = maps.Clone(account.Extra)
	r.accounts[account.ID] = &copied
	return nil
}

func (r *timezoneDetectAccountRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	account := r.accounts[id]
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	maps.Copy(account.Extra, updates)
	return nil
}

func (r *timezoneDetectAccountRepo) BulkUpdate(_ context.Context, ids []int64, updates AccountBulkUpdate) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if updates.ProxyID != nil {
			proxyID := *updates.ProxyID
			r.accounts[id].ProxyID = &proxyID
		}
	}
	return int64(len(ids)), nil
}

type timezoneDetectProxyRepo struct{ ProxyRepository }

func (timezoneDetectProxyRepo) GetByID(_ context.Context, id int64) (*Proxy, error) {
	return &Proxy{ID: id, Protocol: "http", Host: "proxy.example.com", Port: int(8000 + id), Status: StatusActive}, nil
}

type timezoneDetectProber struct {
	mu    sync.Mutex
	calls []string
	info  *ProxyExitInfo
	err   error
}

func (p *timezoneDetectProber) ProbeProxy(_ context.Context, proxyURL string) (*ProxyExitInfo, int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, proxyURL)
	return p.info, 1, p.err
}

func (p *timezoneDetectProber) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func newTimezoneDetectService(prober *timezoneDetectProber, accounts ...*Account) (*adminServiceImpl, *timezoneDetectAccountRepo) {
	repo := &timezoneDetectAccountRepo{accounts: map[int64]*Account{}}
	for _, account := range accounts {
		repo.accounts[account.ID] = account
	}
	return &adminServiceImpl{accountRepo: repo, proxyRepo: timezoneDetectProxyRepo{}, proxyProber: prober}, repo
}

func followProxyAccount(id int64, proxyID *int64, extra map[string]any) *Account {
	merged := map[string]any{openAIRequestTimezoneModeExtraKey: OpenAIRequestTimezoneModeFollowProxy}
	maps.Copy(merged, extra)
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, ProxyID: proxyID, Extra: merged}
}

func waitTimezoneDetection(t *testing.T, repo *timezoneDetectAccountRepo, id int64) map[string]any {
	t.Helper()
	var extra map[string]any
	require.Eventually(t, func() bool {
		extra = repo.clone(id).Extra
		_, ok := extra[openAIRequestTimezoneDetectedExtraKey]
		_, running := openAIRequestTimezoneDetecting.Load(id)
		return ok && !running
	}, 2*time.Second, 5*time.Millisecond)
	return extra
}

func TestOpenAIRequestTimezoneModeValidation(t *testing.T) {
	require.NoError(t, ValidateOpenAIRequestTimezoneExtra(PlatformOpenAI, map[string]any{openAIRequestTimezoneModeExtraKey: OpenAIRequestTimezoneModeFollowProxy}))
	require.NoError(t, ValidateOpenAIRequestTimezoneExtra(PlatformOpenAI, map[string]any{openAIRequestTimezoneModeExtraKey: OpenAIRequestTimezoneModeFixed}))
	require.Error(t, ValidateOpenAIRequestTimezoneExtra(PlatformOpenAI, map[string]any{openAIRequestTimezoneModeExtraKey: "auto"}))
	require.Error(t, ValidateOpenAIRequestTimezoneExtra(PlatformAnthropic, map[string]any{openAIRequestTimezoneModeExtraKey: OpenAIRequestTimezoneModeFollowProxy}))
	require.Equal(t, OpenAIRequestTimezoneModeFixed, (&Account{Platform: PlatformOpenAI}).OpenAIRequestTimezoneMode(), "existing accounts without a mode stay fixed")
}

func TestCreateFollowProxyAccountDetectsExitTimezone(t *testing.T) {
	prober := &timezoneDetectProber{info: &ProxyExitInfo{IP: "203.0.113.7", Country: "日本", City: "东京", Timezone: "Asia/Tokyo"}}
	svc, repo := newTimezoneDetectService(prober)
	proxyID := int64(3)
	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name: "tz", Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &proxyID, SkipDefaultGroupBind: true,
		Credentials: map[string]any{"access_token": "tok"},
		Extra:       map[string]any{openAIRequestTimezoneModeExtraKey: OpenAIRequestTimezoneModeFollowProxy},
	})
	require.NoError(t, err)
	extra := waitTimezoneDetection(t, repo, account.ID)
	require.Equal(t, "Asia/Tokyo", extra[openAIRequestTimezoneExtraKey])
	require.Equal(t, "Asia/Tokyo", repo.clone(account.ID).OpenAIRequestTimezone())
	require.Equal(t, []string{"http://proxy.example.com:8003"}, prober.calls)
}

func TestTimezoneDetectionFailureKeepsPreviousTimezone(t *testing.T) {
	proxyID := int64(1)
	cases := []struct {
		name   string
		prober *timezoneDetectProber
		extra  map[string]any
		want   string
	}{
		{"probe error keeps last result", &timezoneDetectProber{err: errors.New("dial timeout")}, map[string]any{openAIRequestTimezoneExtraKey: "Europe/Berlin"}, "Europe/Berlin"},
		{"first failure falls back to default", &timezoneDetectProber{info: &ProxyExitInfo{IP: "198.51.100.1"}}, nil, DefaultOpenAIRequestTimezone},
		{"unsupported timezone is a failure", &timezoneDetectProber{info: &ProxyExitInfo{IP: "198.51.100.1", Timezone: "Mars/Olympus"}}, map[string]any{openAIRequestTimezoneExtraKey: "Europe/Berlin"}, "Europe/Berlin"},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := int64(10 + index)
			svc, repo := newTimezoneDetectService(tc.prober, followProxyAccount(id, &proxyID, tc.extra))
			updated, err := svc.DetectOpenAIRequestTimezone(context.Background(), id)
			require.NoError(t, err)
			require.Equal(t, tc.want, updated.OpenAIRequestTimezone())
			detection, ok := repo.clone(id).Extra[openAIRequestTimezoneDetectedExtraKey].(OpenAIRequestTimezoneDetection)
			require.True(t, ok)
			require.NotEmpty(t, detection.Error)
		})
	}
}

func TestDetectRejectsFixedModeAccount(t *testing.T) {
	account := &Account{ID: 5, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{openAIRequestTimezoneExtraKey: "Asia/Singapore"}}
	prober := &timezoneDetectProber{info: &ProxyExitInfo{Timezone: "Asia/Tokyo"}}
	svc, _ := newTimezoneDetectService(prober, account)
	_, err := svc.DetectOpenAIRequestTimezone(context.Background(), 5)
	require.Error(t, err)
	require.Zero(t, prober.callCount())
}

func TestUpdateAccountTriggersDetectionOnlyWhenProxyOrModeChanges(t *testing.T) {
	oldProxy, newProxy := int64(1), int64(2)
	detected := map[string]any{openAIRequestTimezoneExtraKey: "Asia/Tokyo", openAIRequestTimezoneDetectedExtraKey: map[string]any{"timezone": "Asia/Tokyo"}}
	prober := &timezoneDetectProber{info: &ProxyExitInfo{IP: "192.0.2.1", Timezone: "America/New_York"}}
	svc, repo := newTimezoneDetectService(prober, followProxyAccount(21, &oldProxy, detected))

	// Unchanged proxy: the form echoes a stale timezone, which must not overwrite the detected value.
	_, err := svc.UpdateAccount(context.Background(), 21, &UpdateAccountInput{Extra: map[string]any{
		openAIRequestTimezoneModeExtraKey: OpenAIRequestTimezoneModeFollowProxy,
		openAIRequestTimezoneExtraKey:     "Asia/Singapore",
	}})
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, prober.callCount())
	require.Equal(t, "Asia/Tokyo", repo.clone(21).Extra[openAIRequestTimezoneExtraKey])

	// Proxy change re-detects.
	delete(repo.accounts[21].Extra, openAIRequestTimezoneDetectedExtraKey)
	_, err = svc.UpdateAccount(context.Background(), 21, &UpdateAccountInput{ProxyID: &newProxy})
	require.NoError(t, err)
	extra := waitTimezoneDetection(t, repo, 21)
	require.Equal(t, "America/New_York", extra[openAIRequestTimezoneExtraKey])
	require.Equal(t, 1, prober.callCount())
}

func TestUpdateFixedAccountNeverDetects(t *testing.T) {
	oldProxy, newProxy := int64(1), int64(2)
	account := &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &oldProxy, Extra: map[string]any{openAIRequestTimezoneExtraKey: "Asia/Singapore"}}
	prober := &timezoneDetectProber{info: &ProxyExitInfo{Timezone: "Asia/Tokyo"}}
	svc, repo := newTimezoneDetectService(prober, account)
	_, err := svc.UpdateAccount(context.Background(), 31, &UpdateAccountInput{ProxyID: &newProxy})
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, prober.callCount())
	require.Equal(t, "Asia/Singapore", repo.clone(31).OpenAIRequestTimezone())
}

func TestBulkProxyChangeProbesEachExitOnce(t *testing.T) {
	proxyID := int64(4)
	prober := &timezoneDetectProber{info: &ProxyExitInfo{IP: "192.0.2.9", Timezone: "Europe/London"}}
	fixed := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{}}
	svc, repo := newTimezoneDetectService(prober, followProxyAccount(41, nil, nil), followProxyAccount(42, nil, nil), fixed)
	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{41, 42, 43}, ProxyID: &proxyID})
	require.NoError(t, err)
	require.Equal(t, "Europe/London", waitTimezoneDetection(t, repo, 41)[openAIRequestTimezoneExtraKey])
	require.Equal(t, "Europe/London", waitTimezoneDetection(t, repo, 42)[openAIRequestTimezoneExtraKey])
	require.Equal(t, 1, prober.callCount())
	require.NotContains(t, repo.clone(43).Extra, openAIRequestTimezoneDetectedExtraKey)
}

func TestStaleDetectionIsDiscardedAfterProxyChange(t *testing.T) {
	oldProxy, newProxy := int64(1), int64(2)
	svc, repo := newTimezoneDetectService(&timezoneDetectProber{}, followProxyAccount(51, &newProxy, nil))
	require.NoError(t, svc.applyOpenAIRequestTimezoneDetection(context.Background(), 51, OpenAIRequestTimezoneDetection{ProxyID: &oldProxy, Timezone: "Asia/Tokyo"}))
	require.NotContains(t, repo.clone(51).Extra, openAIRequestTimezoneExtraKey)
}
