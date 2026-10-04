package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
	"github.com/stretchr/testify/require"
)

type codexRecoveryRepository struct {
	AccountRepository
	account  *Account
	binding  *CodexAccountBinding
	failSave bool
	mu       sync.Mutex
	locks    map[string]*sync.Mutex
}

func copyCodexBinding(b *CodexAccountBinding) *CodexAccountBinding {
	if b == nil {
		return nil
	}
	raw, _ := json.Marshal(b)
	var result CodexAccountBinding
	_ = json.Unmarshal(raw, &result)
	return &result
}
func (r *codexRecoveryRepository) CreateWithAccountGroups(_ context.Context, a *Account, _ []AccountGroup) error {
	a.ID = 19
	a.GatewayBinding.AccountID = a.ID
	r.binding = copyCodexBinding(a.GatewayBinding)
	copy := *a
	r.account = &copy
	return nil
}
func (r *codexRecoveryRepository) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.account == nil || r.account.ID != id {
		return nil, ErrAccountNotFound
	}
	a := *r.account
	a.GatewayBinding = copyCodexBinding(r.binding)
	a.Gateway = a.GatewayBinding.Projection()
	return &a, nil
}
func (r *codexRecoveryRepository) GetCodexBinding(context.Context, int64) (*CodexAccountBinding, error) {
	return copyCodexBinding(r.binding), nil
}
func (r *codexRecoveryRepository) FindCodexBindingByCreationKey(_ context.Context, key string) (*CodexAccountBinding, error) {
	if r.binding != nil && r.binding.CreationKey == key {
		return copyCodexBinding(r.binding), nil
	}
	return nil, nil
}
func (r *codexRecoveryRepository) SaveCodexBinding(_ context.Context, b *CodexAccountBinding, version int64, operation string) error {
	if r.failSave {
		r.failSave = false
		return errors.New("injected local commit failure")
	}
	if r.binding.ConfigVersion != version || r.binding.OperationID != operation {
		return errors.New("stale binding")
	}
	r.binding = copyCodexBinding(b)
	return nil
}
func (r *codexRecoveryRepository) ListCodexBindingAccountIDs(context.Context) ([]int64, error) {
	if r.account == nil {
		return nil, nil
	}
	return []int64{r.account.ID}, nil
}
func (r *codexRecoveryRepository) ListShadowsByParent(context.Context, int64) ([]*Account, error) {
	return nil, nil
}
func (r *codexRecoveryRepository) Delete(context.Context, int64) error { r.account = nil; return nil }
func (r *codexRecoveryRepository) WithCodexBindingLock(ctx context.Context, key string, fn func(context.Context) error) error {
	r.mu.Lock()
	if r.locks == nil {
		r.locks = map[string]*sync.Mutex{}
	}
	lock := r.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		r.locks[key] = lock
	}
	r.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	return fn(ctx)
}

func codexRecoveryFixture(t *testing.T, outcome string) (*adminServiceImpl, *codexRecoveryRepository, *atomic.Int32) {
	t.Helper()
	var puts atomic.Int32
	var remoteMu sync.Mutex
	var operation codexgateway.Operation
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteMu.Lock()
		defer remoteMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/internal/capabilities" {
			_ = json.NewEncoder(w).Encode(codexgateway.Capabilities{APIVersion: 4, StorageMode: "postgres", SharedPGLayoutSupported: true, PersistentAccountOperationsSupported: true, MaxRequestBytes: 1024})
			return
		}
		if r.Method == "PUT" {
			puts.Add(1)
			revision := uint64(1)
			operation = codexgateway.Operation{OperationID: r.Header.Get("Idempotency-Key"), AccountID: strings.TrimPrefix(r.URL.Path, "/internal/accounts/"), Kind: "put", State: outcome, ResultRevision: &revision}
			if outcome == "indeterminate" {
				w.WriteHeader(409)
			}
			_ = json.NewEncoder(w).Encode(operation)
			return
		}
		if r.Method == http.MethodDelete {
			revision, _ := strconv.ParseUint(r.URL.Query().Get("expected_revision"), 10, 64)
			operation = codexgateway.Operation{OperationID: r.Header.Get("Idempotency-Key"), AccountID: strings.TrimPrefix(r.URL.Path, "/internal/accounts/"), Kind: "delete", State: "succeeded", ExpectedRevision: revision}
			_ = json.NewEncoder(w).Encode(operation)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/internal/account-operations/") {
			_ = json.NewEncoder(w).Encode(operation)
			return
		}
		_ = json.NewEncoder(w).Encode(codexgateway.AccountStatus{ID: operation.AccountID, Revision: 1, Status: "ready", CanAcceptRequests: true, Credential: codexgateway.CredentialStatus{Mode: "at_rt"}})
	}))
	t.Cleanup(server.Close)
	key := filepath.Join(t.TempDir(), "service-key")
	require.NoError(t, os.WriteFile(key, []byte("local-recovery-service-key"), 0600))
	cfg := &config.Config{}
	cfg.Gateway.MaxBodySize = 1024
	cfg.Gateway.Codex4Server = config.Codex4ServerConfig{Enabled: true, BaseURL: server.URL, ServiceKeyFile: key}
	repo := &codexRecoveryRepository{}
	managed := NewCodexGatewayService(cfg, repo)
	admin := &adminServiceImpl{cfg: cfg, accountRepo: repo, accountDuplicateRepo: repo, codexGateway: managed}
	return admin, repo, &puts
}

func TestCodexGatewayRemoteSuccessLocalFailureRecoversWithoutCredentials(t *testing.T) {
	admin, repo, puts := codexRecoveryFixture(t, "succeeded")
	repo.failSave = true
	input := &CreateAccountInput{Name: "test", Platform: PlatformOpenAICodex, Type: AccountTypeGateway, Concurrency: 3, GatewayOperationKey: "admin:1:create-1", GatewayCredentials: json.RawMessage(`{"type":"refresh_token","refresh_token":"secret-that-must-never-be-stored"}`)}
	a, err := admin.CreateAccount(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, CodexSyncPending, a.Gateway.SyncState)
	require.False(t, a.IsSchedulable())
	require.EqualValues(t, 1, puts.Load())
	stored, _ := json.Marshal(struct {
		Account *Account
		Binding *CodexAccountBinding
	}{repo.account, repo.binding})
	require.NotContains(t, string(stored), "secret-that-must-never-be-stored")
	// A retry of the original create must recover the same local row, not PUT again.
	retry, err := admin.CreateAccount(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, a.ID, retry.ID)
	require.EqualValues(t, 1, puts.Load())
	repo.account.Status = "inactive"
	repo.account.Schedulable = false
	require.NoError(t, admin.codexGateway.Synchronize(context.Background(), a.ID))
	updated, err := admin.GetAccount(context.Background(), a.ID)
	require.NoError(t, err)
	require.Equal(t, CodexSyncReady, updated.Gateway.SyncState)
	require.Equal(t, "inactive", updated.Status)
	require.False(t, updated.Schedulable)
	require.EqualValues(t, 1, puts.Load())
}

func TestCodexGatewayIndeterminateImportIsNeverReplayed(t *testing.T) {
	admin, _, puts := codexRecoveryFixture(t, "indeterminate")
	input := &CreateAccountInput{Name: "test", Platform: PlatformOpenAICodex, Type: AccountTypeGateway, Concurrency: 3, GatewayOperationKey: "admin:1:create-2", GatewayCredentials: json.RawMessage(`{"type":"refresh_token","refresh_token":"rotating-secret"}`)}
	a, err := admin.CreateAccount(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, CodexSyncUnknown, a.Gateway.SyncState)
	require.NoError(t, admin.codexGateway.Synchronize(context.Background(), a.ID))
	require.NoError(t, admin.codexGateway.Synchronize(context.Background(), a.ID))
	retry, err := admin.CreateAccount(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, a.ID, retry.ID)
	require.EqualValues(t, 1, puts.Load())
	require.False(t, retry.IsSchedulable())
}

func TestCodexGatewayCreateKeyCannotBeReusedWithOtherCredentials(t *testing.T) {
	admin, _, puts := codexRecoveryFixture(t, "succeeded")
	input := &CreateAccountInput{Name: "same", Platform: PlatformOpenAICodex, Type: AccountTypeGateway, Concurrency: 1, GatewayOperationKey: "same-key", GatewayCredentials: json.RawMessage(`{"type":"refresh_token","refresh_token":"one"}`)}
	first, err := admin.CreateAccount(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, first)
	input.GatewayCredentials = json.RawMessage(`{"type":"refresh_token","refresh_token":"two"}`)
	_, err = admin.CreateAccount(context.Background(), input)
	require.ErrorIs(t, err, ErrIdempotencyKeyConflict)
	require.EqualValues(t, 1, puts.Load())
}

func TestCodexGatewayUnrelatedReceiptCannotReleaseScheduling(t *testing.T) {
	admin, repo, _ := codexRecoveryFixture(t, "pending")
	input := &CreateAccountInput{Name: "pending", Platform: PlatformOpenAICodex, Type: AccountTypeGateway, Concurrency: 1, GatewayOperationKey: "pending-key", GatewayCredentials: json.RawMessage(`{"type":"refresh_token","refresh_token":"one"}`)}
	a, err := admin.CreateAccount(context.Background(), input)
	require.NoError(t, err)
	b := copyCodexBinding(repo.binding)
	revision := uint64(100)
	receipt := &codexgateway.Operation{OperationID: b.OperationID, AccountID: "another-account", Kind: "put", State: "succeeded", ResultRevision: &revision}
	require.Error(t, admin.codexGateway.observeOperation(context.Background(), a, b, receipt))
	require.Equal(t, CodexSyncPending, repo.binding.SyncState)
	require.False(t, repo.binding.Projection().Usable())
}

func TestCodexGatewayStaleSnapshotCannotReplaceNewerState(t *testing.T) {
	admin, repo, _ := codexRecoveryFixture(t, "succeeded")
	a, err := admin.CreateAccount(context.Background(), &CreateAccountInput{Name: "snapshot", Platform: PlatformOpenAICodex, Type: AccountTypeGateway, Concurrency: 1, GatewayOperationKey: "snapshot-key", GatewayCredentials: json.RawMessage(`{"type":"refresh_token","refresh_token":"one"}`)})
	require.NoError(t, err)
	require.True(t, a.Gateway.Usable())
	older := copyCodexBinding(repo.binding)
	newer := copyCodexBinding(repo.binding)
	version, operation := newer.ConfigVersion, newer.OperationID
	newer.Snapshot.CanAcceptRequests = false
	require.NoError(t, admin.codexGateway.save(context.Background(), newer, version, operation))
	require.Error(t, admin.codexGateway.save(context.Background(), older, version, operation))
	require.False(t, repo.binding.Projection().Usable())
}

func TestCodexGatewayDeletionNeverReopensScheduling(t *testing.T) {
	for _, outcome := range []string{"succeeded", "failed", "indeterminate", "pending"} {
		t.Run(outcome, func(t *testing.T) {
			admin, repo, puts := codexRecoveryFixture(t, outcome)
			// Reproduce a remote success whose local acknowledgement was lost.
			repo.failSave = outcome == "succeeded"
			a, err := admin.CreateAccount(context.Background(), &CreateAccountInput{Name: "delete-pending", Platform: PlatformOpenAICodex, Type: AccountTypeGateway, Concurrency: 1, GatewayOperationKey: "delete-" + outcome, GatewayCredentials: json.RawMessage(`{"type":"refresh_token","refresh_token":"synthetic-delete-input"}`)})
			require.NoError(t, err)
			require.NoError(t, admin.codexGateway.Delete(context.Background(), a.ID))
			require.True(t, repo.binding.DeleteRequested)
			require.Equal(t, CodexSyncDeleting, repo.binding.SyncState)
			require.False(t, repo.binding.Projection().Usable())
			loaded, err := admin.GetAccount(context.Background(), a.ID)
			require.NoError(t, err)
			require.False(t, loaded.IsSchedulable())

			restarted := NewCodexGatewayService(admin.cfg, repo)
			require.NoError(t, restarted.Synchronize(context.Background(), a.ID))
			if outcome == "pending" {
				require.NotNil(t, repo.account, "an unfinished receipt must keep the local deletion pending")
				require.False(t, repo.binding.Projection().Usable())
			} else {
				require.Nil(t, repo.account, "delete only after the remote delete receipt succeeds")
			}
			require.EqualValues(t, 1, puts.Load(), "recovery must not replay credentials")
		})
	}
}

func TestCodexGatewayDeletionWinsOverEveryEarlierMutationReceipt(t *testing.T) {
	for _, kind := range []string{"put", "patch", "refresh"} {
		for _, state := range []string{"pending", "succeeded", "failed", "indeterminate"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				admin, repo, _ := codexRecoveryFixture(t, "pending")
				a, err := admin.CreateAccount(context.Background(), &CreateAccountInput{Name: "earlier-operation", Platform: PlatformOpenAICodex, Type: AccountTypeGateway, GatewayOperationKey: "earlier", GatewayCredentials: json.RawMessage(`{"type":"refresh_token","refresh_token":"synthetic"}`)})
				require.NoError(t, err)
				repo.binding.OperationKind = kind
				repo.binding.DeleteRequested = true
				repo.binding.SyncState = CodexSyncDeleting
				b := copyCodexBinding(repo.binding)
				revision := uint64(1)
				op := &codexgateway.Operation{OperationID: b.OperationID, AccountID: b.GatewayID, ExpectedRevision: b.OperationRevision, Kind: kind, State: state, ResultRevision: &revision}
				require.NoError(t, admin.codexGateway.observeOperation(context.Background(), a, b, op))
				require.Equal(t, CodexSyncDeleting, repo.binding.SyncState)
				require.False(t, repo.binding.Projection().Usable())
			})
		}
	}
}

func TestCodexGatewayLegacyDeletionProjectionRemainsClosed(t *testing.T) {
	b := &CodexAccountBinding{GatewayID: "old-deletion", DeleteRequested: true, SyncState: CodexSyncReady, Snapshot: &codexgateway.AccountStatus{ID: "old-deletion", CanAcceptRequests: true}}
	require.Equal(t, CodexSyncDeleting, b.Projection().SyncState)
	require.False(t, b.Projection().Usable())
	require.Equal(t, CodexSyncReady, b.SyncState, "projection must not mutate the persisted input")
	admin, repo, puts := codexRecoveryFixture(t, "pending")
	a, err := admin.CreateAccount(context.Background(), &CreateAccountInput{Name: "legacy-pending", Platform: PlatformOpenAICodex, Type: AccountTypeGateway, GatewayOperationKey: "legacy-pending", GatewayCredentials: json.RawMessage(`{"type":"refresh_token","refresh_token":"synthetic"}`)})
	require.NoError(t, err)
	repo.binding.DeleteRequested = true
	repo.binding.SyncState = CodexSyncReady
	version := repo.binding.ConfigVersion
	require.NoError(t, admin.codexGateway.Synchronize(context.Background(), a.ID))
	require.Equal(t, CodexSyncDeleting, repo.binding.SyncState)
	require.Greater(t, repo.binding.ConfigVersion, version)
	require.EqualValues(t, 1, puts.Load())
}
