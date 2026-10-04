//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexGatewayBindingAtomicityAndSchedulingProjection(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	makeAccount := func(gatewayID string) *service.Account {
		return &service.Account{Name: "codex-" + uuid.NewString(), Platform: service.PlatformOpenAICodex, Type: service.AccountTypeGateway, Status: service.StatusActive, Schedulable: true, Concurrency: 3, Priority: 50, Credentials: map[string]any{}, Extra: map[string]any{}, GatewayBinding: &service.CodexAccountBinding{GatewayID: gatewayID, CreationKey: uuid.NewString(), ConfigVersion: 1, SyncState: service.CodexSyncPending, OperationID: uuid.NewString(), OperationKind: "put"}}
	}
	account := makeAccount("gw-" + uuid.NewString())
	require.NoError(t, repo.CreateWithAccountGroups(ctx, account, nil))
	t.Cleanup(func() {
		_, _ = integrationDB.Exec("DELETE FROM scheduler_outbox WHERE account_id=$1", account.ID)
		_, _ = integrationDB.Exec("DELETE FROM accounts WHERE id=$1", account.ID)
	})
	loaded, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.False(t, loaded.IsSchedulable())
	require.NotNil(t, loaded.Gateway)
	duplicate := makeAccount(account.GatewayBinding.GatewayID)
	require.Error(t, repo.CreateWithAccountGroups(ctx, duplicate, nil))
	var count int
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM accounts WHERE name=$1", duplicate.Name).Scan(&count))
	require.Zero(t, count, "binding conflict must roll back the business account")
	binding, err := repo.GetCodexBinding(ctx, account.ID)
	require.NoError(t, err)
	oldOperation := binding.OperationID
	binding.SyncState = service.CodexSyncReady
	binding.OperationID = ""
	binding.OperationKind = ""
	binding.Revision = 1
	binding.Snapshot = &codexgateway.AccountStatus{ID: binding.GatewayID, Revision: 1, CanAcceptRequests: true, Credential: codexgateway.CredentialStatus{Mode: "at_rt"}}
	require.NoError(t, repo.SaveCodexBinding(ctx, binding, 1, oldOperation))
	loaded, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.True(t, loaded.IsSchedulable())
	batch, err := repo.GetByIDs(ctx, []int64{account.ID})
	require.NoError(t, err)
	require.Len(t, batch, 1)
	require.True(t, batch[0].IsSchedulable())
	require.Error(t, repo.SaveCodexBinding(ctx, binding, 1, oldOperation), "stale operation must not replace the confirmed state")
	_, err = integrationDB.Exec("UPDATE accounts SET status='inactive',schedulable=false WHERE id=$1", account.ID)
	require.NoError(t, err)
	require.NoError(t, repo.SaveCodexBinding(ctx, binding, 1, ""))
	loaded, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, "inactive", loaded.Status)
	require.False(t, loaded.Schedulable)
	data, err := json.Marshal(loaded.Gateway)
	require.NoError(t, err)
	require.NotContains(t, string(data), "creation_key")
	require.NotContains(t, string(data), "mutation_history")
}

func TestCodexGatewayAccountLockCoordinatesInstances(t *testing.T) {
	ctx := context.Background()
	one := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	two := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	key := "test:" + uuid.NewString()
	require.NoError(t, one.WithCodexBindingLock(ctx, key, func(ctx context.Context) error {
		require.Error(t, two.WithCodexBindingLock(ctx, key, func(context.Context) error { t.Fatal("second instance acquired the account lock"); return nil }))
		return nil
	}))
	require.NoError(t, two.WithCodexBindingLock(ctx, key, func(context.Context) error { return nil }))
}

func TestCodexGatewayPendingDeletionClosesParentAndShadowRedisProjections(t *testing.T) {
	ctx := context.Background()
	cache := NewSchedulerCache(integrationRedis)
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, cache)
	binding := &service.CodexAccountBinding{GatewayID: "gw-" + uuid.NewString(), CreationKey: uuid.NewString(), ConfigVersion: 1, SyncState: service.CodexSyncReady}
	binding.Snapshot = &codexgateway.AccountStatus{ID: binding.GatewayID, Revision: 1, CanAcceptRequests: true}
	parent := &service.Account{Name: "deleting-parent-" + uuid.NewString(), Platform: service.PlatformOpenAICodex, Type: service.AccountTypeGateway, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{}, Extra: map[string]any{}, GatewayBinding: binding}
	require.NoError(t, repo.CreateWithAccountGroups(ctx, parent, nil))
	shadow := &service.Account{Name: "deleting-shadow-" + uuid.NewString(), Platform: service.PlatformOpenAICodex, Type: service.AccountTypeGateway, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{}, Extra: map[string]any{}, ParentAccountID: &parent.ID, QuotaDimension: service.QuotaDimensionSpark}
	t.Cleanup(func() {
		for _, id := range []int64{shadow.ID, parent.ID} {
			_ = cache.DeleteAccount(ctx, id)
			_, _ = integrationDB.Exec("DELETE FROM scheduler_outbox WHERE account_id=$1", id)
			_, _ = integrationDB.Exec("DELETE FROM accounts WHERE id=$1", id)
		}
	})
	require.NoError(t, repo.CreateWithAccountGroups(ctx, shadow, nil))
	for _, id := range []int64{parent.ID, shadow.ID} {
		loaded, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		require.True(t, loaded.IsSchedulable())
	}
	// Emulate an old persisted inconsistent state. Projection must close it
	// even before the reconciliation worker repairs the stored sync_state.
	binding.DeleteRequested = true
	binding.SyncState = service.CodexSyncReady
	binding.ConfigVersion++
	require.NoError(t, repo.SaveCodexBinding(ctx, binding, 1, ""))
	for _, id := range []int64{parent.ID, shadow.ID} {
		loaded, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, service.CodexSyncDeleting, loaded.Gateway.SyncState)
		require.False(t, loaded.IsSchedulable())
		cached, err := cache.GetAccount(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, cached)
		require.Equal(t, service.CodexSyncDeleting, cached.Gateway.SyncState)
		require.False(t, cached.IsSchedulable())
		var events int
		require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM scheduler_outbox WHERE account_id=$1", id).Scan(&events))
		require.Positive(t, events)
	}
}

func TestCodexGatewayDeletedAccountReleasesBinding(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	gatewayID, creationKey := "gw-"+uuid.NewString(), uuid.NewString()
	makeAccount := func() *service.Account {
		return &service.Account{Name: "codex-" + uuid.NewString(), Platform: service.PlatformOpenAICodex, Type: service.AccountTypeGateway, Status: service.StatusActive, Credentials: map[string]any{}, Extra: map[string]any{}, GatewayBinding: &service.CodexAccountBinding{GatewayID: gatewayID, CreationKey: creationKey, ConfigVersion: 1, SyncState: service.CodexSyncMissing}}
	}
	first := makeAccount()
	require.NoError(t, repo.CreateWithAccountGroups(ctx, first, nil))
	second := makeAccount()
	t.Cleanup(func() {
		for _, id := range []int64{first.ID, second.ID} {
			_, _ = integrationDB.Exec("DELETE FROM scheduler_outbox WHERE account_id=$1", id)
			_, _ = integrationDB.Exec("DELETE FROM accounts WHERE id=$1", id)
		}
	})
	require.NoError(t, repo.Delete(ctx, first.ID))
	previous, err := repo.FindCodexBindingByCreationKey(ctx, creationKey)
	require.NoError(t, err)
	require.Nil(t, previous, "a soft-deleted account must not satisfy creation idempotency")
	require.NoError(t, repo.DeleteCodexBinding(ctx, first.ID))
	require.NoError(t, repo.CreateWithAccountGroups(ctx, second, nil), "the Gateway ID must be reusable after local deletion")
}
