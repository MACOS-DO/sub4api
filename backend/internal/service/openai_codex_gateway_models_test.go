package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type codexGatewayModelsRepository struct {
	AccountRepository
	accounts         []Account
	selectedPlatform string
}

func (r *codexGatewayModelsRepository) ListByGroup(context.Context, int64) ([]Account, error) {
	return r.accounts, nil
}

func (r *codexGatewayModelsRepository) ListSchedulableByGroupIDAndPlatform(_ context.Context, _ int64, platform string) ([]Account, error) {
	r.selectedPlatform = platform
	var accounts []Account
	for _, a := range r.accounts {
		if a.Platform == platform && a.IsSchedulable() {
			accounts = append(accounts, a)
		}
	}
	return accounts, nil
}

func TestCodexGatewayPinnedModelsRespectPlatformMembershipAndReadiness(t *testing.T) {
	ready := *codexIdentityTestAccount("off")
	legacy := ready
	legacy.ID, legacy.Platform, legacy.Type = 20, PlatformOpenAI, AccountTypeOAuth
	blocked := *codexIdentityTestAccount("off")
	blocked.ID = 21
	blocked.Gateway.SyncState = CodexSyncDeleting
	repo := &codexGatewayModelsRepository{accounts: []Account{ready, legacy, blocked}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	group := &Group{ID: 7, Platform: PlatformOpenAICodex, CodexModelsManifestConfig: GroupCodexModelsManifestConfig{Enabled: true, AccountIDs: []int64{20, 21, 999, ready.ID}}}
	var called []int64
	results, err := svc.fetchPinnedOpenAIModels(context.Background(), group, func(_ context.Context, a *Account) (*OpenAIModelsResponse, error) {
		called = append(called, a.ID)
		return &OpenAIModelsResponse{Body: []byte(`{"models":[]}`)}, nil
	})
	require.NoError(t, err)
	require.Equal(t, []int64{ready.ID}, called)
	require.Len(t, results, 1)
	admin := &adminServiceImpl{accountRepo: repo}
	group.CodexModelsManifestConfig.AccountIDs = []int64{ready.ID}
	require.NoError(t, admin.validateCodexModelsManifestConfig(context.Background(), group, group.CodexModelsManifestConfig))
	group.CodexModelsManifestConfig.AccountIDs = []int64{legacy.ID}
	require.Error(t, admin.validateCodexModelsManifestConfig(context.Background(), group, group.CodexModelsManifestConfig))
	group.Platform = PlatformOpenAI
	require.NoError(t, admin.validateCodexModelsManifestConfig(context.Background(), group, group.CodexModelsManifestConfig))
}

func TestCodexGatewayPinnedModelsMergePartialSuccessInConfiguredOrder(t *testing.T) {
	repo := &codexGatewayModelsRepository{}
	for _, id := range []int64{1, 2, 3} {
		a := *codexIdentityTestAccount("off")
		a.ID = id
		repo.accounts = append(repo.accounts, a)
	}
	svc := &OpenAIGatewayService{accountRepo: repo}
	group := &Group{ID: 7, Platform: PlatformOpenAICodex, CodexModelsManifestConfig: GroupCodexModelsManifestConfig{Enabled: true, AccountIDs: []int64{3, 2, 1}}}
	var mu sync.Mutex
	var calls []int64
	results, err := svc.fetchPinnedOpenAIModels(context.Background(), group, func(_ context.Context, a *Account) (*OpenAIModelsResponse, error) {
		mu.Lock()
		calls = append(calls, a.ID)
		mu.Unlock()
		if a.ID == 2 {
			return nil, errors.New("synthetic failure")
		}
		return &OpenAIModelsResponse{Body: []byte(`{"models":[{"slug":"model-` + strconv.FormatInt(a.ID, 10) + `"}]}`)}, nil
	})
	require.NoError(t, err)
	require.Len(t, calls, 3)
	require.Len(t, results, 2)
	require.EqualValues(t, 3, results[0].account.ID)
	require.EqualValues(t, 1, results[1].account.ID)
	body, err := mergeCodexModelsManifestBodies([][]byte{results[0].response.Body, results[1].response.Body})
	require.NoError(t, err)
	require.JSONEq(t, `{"models":[{"slug":"model-3"},{"slug":"model-1"}]}`, string(body))
}

func TestCodexGatewayModelsFallbackAndETag(t *testing.T) {
	svc, account := codexGatewayDataFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.5","display_name":"Test model"}]}`))
	})
	repo := &codexGatewayModelsRepository{accounts: []Account{*account}}
	svc.accountRepo = repo
	group := &Group{ID: 7, Platform: PlatformOpenAICodex, CodexModelsManifestConfig: GroupCodexModelsManifestConfig{Enabled: true, AccountIDs: []int64{999}}}
	_, _, err := svc.FetchPinnedOpenAIModelsList(context.Background(), group, 1, "")
	require.ErrorIs(t, err, ErrNoPinnedCodexModelsAccounts)
	require.Empty(t, repo.selectedPlatform)
	group.CodexModelsManifestConfig.FallbackToScheduler = true
	response, selected, err := svc.FetchPinnedOpenAIModelsList(context.Background(), group, 1, "")
	require.NoError(t, err)
	require.Equal(t, PlatformOpenAICodex, repo.selectedPlatform)
	require.Equal(t, account.ID, selected.ID)
	require.NotEmpty(t, response.ETag)
	again, _, err := svc.FetchPinnedOpenAIModelsList(context.Background(), group, 1, response.ETag)
	require.NoError(t, err)
	require.True(t, again.NotModified)
}

func TestCodexGatewayTestPickerUsesCatalogAndPreservesEmptyResults(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(strconv.FormatBool(empty), func(t *testing.T) {
			svc, a := codexGatewayDataFixture(t, func(w http.ResponseWriter, r *http.Request) {
				models := []map[string]string{}
				if !empty {
					models = append(models, map[string]string{"slug": "gpt-5.5", "display_name": "Test model"})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
			})
			a.Credentials = map[string]any{"model_mapping": map[string]any{"public-alias": "gpt-5.5"}}
			tests := &AccountTestService{openaiGatewayService: svc}
			models, err := tests.FetchOpenAIAccountModels(context.Background(), a)
			require.NoError(t, err)
			if empty {
				require.Empty(t, models)
			} else {
				require.Len(t, models, 1)
				require.Equal(t, "public-alias", models[0].ID)
			}
		})
	}
}
