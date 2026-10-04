package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
	infraerrors "github.com/MACOS-DO/sub4api/internal/pkg/errors"
	"github.com/google/uuid"
)

// CodexGatewayService owns reconciliation, not upstream credentials. A missing
// service/key never prevents the surrounding Sub4API process from starting.
type CodexGatewayService struct {
	client   *codexgateway.Client
	repo     AccountRepository
	bindings CodexBindingRepository
	interval time.Duration
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	start    sync.Once
	sessions CodexOAuthSessionStore
}

func NewCodexGatewayService(cfg *config.Config, repo AccountRepository) *CodexGatewayService {
	s := &CodexGatewayService{repo: repo, interval: 30 * time.Second}
	s.bindings, _ = repo.(CodexBindingRepository)
	if cfg == nil || !cfg.Gateway.Codex4Server.Enabled {
		return s
	}
	c := cfg.Gateway.Codex4Server
	var err error
	s.client, err = codexgateway.New(codexgateway.Config{Enabled: true, BaseURL: c.BaseURL, ServiceKeyFile: c.ServiceKeyFile, AutoGenerateServiceKey: c.AutoGenerateServiceKey, AdminTimeout: c.AdminTimeout, MaxRequestBytes: cfg.Gateway.MaxBodySize})
	if err != nil {
		// Client initialization errors contain fixed summaries, never key contents.
		slog.Warn("codex_gateway initialization failed; OpenAI Codex unavailable", "reason", err.Error())
	}
	if c.SyncInterval > 0 {
		s.interval = c.SyncInterval
	}
	return s
}

func (s *CodexGatewayService) Ready() bool {
	return s != nil && s.client != nil && s.bindings != nil && s.client.Ready()
}

func (s *CodexGatewayService) Check(ctx context.Context) error {
	if s == nil || s.client == nil || s.bindings == nil {
		return codexgateway.Unavailable()
	}
	return s.client.Check(ctx)
}

func (s *CodexGatewayService) Start() {
	if s == nil || s.client == nil || s.bindings == nil {
		return
	}
	s.start.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(s.interval)
			defer ticker.Stop()
			for {
				s.sync(ctx)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	})
}

func (s *CodexGatewayService) Stop() {
	if s != nil && s.cancel != nil {
		s.cancel()
		s.wg.Wait()
	}
}

func (s *CodexGatewayService) sync(ctx context.Context) {
	if s.Check(ctx) != nil {
		return
	}
	ids, err := s.bindings.ListCodexBindingAccountIDs(ctx)
	if err != nil {
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		// Lock conflicts mean an interactive operation owns the account; the
		// next tick retries. Errors are fixed summaries and never contain keys.
		if err := s.Synchronize(ctx, id); err != nil && ctx.Err() == nil && infraerrors.Reason(err) != "CODEX_OPERATION_PENDING" {
			slog.Warn("codex_gateway_sync_failed", "account_id", id, "reason", err.Error())
		}
	}
}

func (s *CodexGatewayService) WithAccountLock(ctx context.Context, id int64, fn func(context.Context) error) error {
	if s == nil || s.bindings == nil {
		return codexgateway.Unavailable()
	}
	return s.bindings.WithCodexBindingLock(ctx, "account:"+strconv.FormatInt(id, 10), fn)
}

func codexDesiredProxy(account *Account) codexgateway.Proxy {
	if account.Proxy != nil {
		return codexgateway.Proxy{Mode: "proxy", URL: account.Proxy.URL()}
	}
	return codexgateway.Proxy{Mode: "direct"}
}

func codexDesiredConfigHash(account *Account) string {
	raw, _ := json.Marshal(struct {
		Name  string
		Proxy codexgateway.Proxy
	}{account.Name, codexDesiredProxy(account)})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func (s *CodexGatewayService) save(ctx context.Context, b *CodexAccountBinding, version int64, operation string) error {
	// A receipt for an earlier mutation must never reopen a requested deletion.
	if b.DeleteRequested {
		b.SyncState = CodexSyncDeleting
	}
	// Advance on every snapshot write as well as on configuration changes.
	// If a lock session is lost, a stale reader cannot publish over a newer one.
	if b.ConfigVersion <= version {
		b.ConfigVersion = version + 1
	}
	return s.bindings.SaveCodexBinding(ctx, b, version, operation)
}

// stage must run under the account lock. It closes the scheduling gate before
// any remote mutation, and records enough information for query-only recovery.
func (s *CodexGatewayService) stage(ctx context.Context, a *Account, b *CodexAccountBinding, kind string) error {
	oldVersion, oldOperation := b.ConfigVersion, b.OperationID
	b.ConfigVersion++
	b.OperationID = uuid.NewString()
	b.OperationKind = kind
	b.OperationRevision = b.Revision
	b.OperationConfigHash = codexDesiredConfigHash(a)
	b.SyncState = CodexSyncPending
	b.Error = nil
	if kind == "delete" {
		b.DeleteRequested = true
		b.SyncState = CodexSyncDeleting
	}
	return s.save(ctx, b, oldVersion, oldOperation)
}

func (s *CodexGatewayService) execute(ctx context.Context, a *Account, b *CodexAccountBinding, credentials *codexgateway.CredentialInput, options ...codexgateway.PutOptions) error {
	if s == nil || s.client == nil {
		return codexgateway.Unavailable()
	}
	if !s.Ready() {
		if err := s.Check(ctx); err != nil {
			return err
		}
	}
	var input any
	switch b.OperationKind {
	case "put":
		if credentials == nil {
			return errors.New("credentials must be supplied for an explicit Gateway import")
		}
		put := codexgateway.PutAccount{DisplayName: a.Name, ExpectedRevision: b.OperationRevision, Credentials: credentials, OutboundProxy: codexDesiredProxy(a)}
		if len(options) > 0 {
			put.OAuthClientID = options[0].OAuthClientID
			put.PreserveRefreshToken = options[0].PreserveRefreshToken
		}
		input = put
	case "patch":
		input = codexgateway.PatchAccount{ExpectedRevision: b.OperationRevision, DisplayName: a.Name, OutboundProxy: codexDesiredProxy(a)}
	case "refresh":
		input = struct {
			ExpectedRevision uint64 `json:"expected_revision"`
			Mode             string `json:"mode"`
		}{b.OperationRevision, "force"}
	case "delete":
	default:
		return errors.New("invalid pending Gateway operation")
	}
	operation, err := s.client.Mutate(ctx, b.GatewayID, b.OperationKind, b.OperationID, b.OperationRevision, input)
	if err != nil {
		// Leave the durable operation marker intact. Even a 404 receipt read after
		// a caller timeout cannot prove that an in-flight request was never accepted.
		return err
	}
	return s.observeOperation(ctx, a, b, operation)
}

func (s *CodexGatewayService) observeOperation(ctx context.Context, a *Account, b *CodexAccountBinding, op *codexgateway.Operation) error {
	if op == nil || op.OperationID != b.OperationID || op.AccountID != b.GatewayID || op.Kind != b.OperationKind || op.ExpectedRevision != b.OperationRevision {
		return codexgateway.Unavailable()
	}
	version, operation := b.ConfigVersion, b.OperationID
	for key, ref := range b.Operations {
		if ref.OperationID == op.OperationID {
			ref.State = op.State
			ref.Error = op.Error
			b.Operations[key] = ref
		}
	}
	if b.DeleteRequested && b.OperationKind != "delete" && (op.State == "failed" || op.State == "indeterminate") {
		b.OperationID = ""
		b.OperationKind = ""
		b.SyncState = CodexSyncDeleting
		return s.save(ctx, b, version, operation)
	}
	switch op.State {
	case "pending":
		return nil
	case "indeterminate":
		b.SyncState = CodexSyncUnknown
		b.Error = &codexgateway.ErrorSummary{Code: "operation_outcome_unknown", Message: "操作结果不确定，请查询状态或重新授权"}
		return s.save(ctx, b, version, operation)
	case "failed":
		if b.OperationKind == "refresh" {
			remote, err := s.client.GetAccount(ctx, b.GatewayID)
			if err != nil {
				return err
			}
			b.Revision = remote.Revision
			b.Snapshot = remote
			b.SyncState = CodexSyncReady
			b.Error = op.Error
			b.OperationID = ""
			b.OperationKind = ""
			return s.save(ctx, b, version, operation)
		}
		b.Error = op.Error
		if b.OperationKind == "patch" || b.OperationKind == "delete" {
			b.OperationID = ""
			b.OperationKind = ""
			b.SyncState = CodexSyncPending
			if b.DeleteRequested {
				b.SyncState = CodexSyncDeleting
			}
		} else {
			b.SyncState = CodexSyncReauthorize
		}
		return s.save(ctx, b, version, operation)
	case "succeeded":
		if b.OperationKind == "delete" {
			return s.finishLocalDelete(ctx, a.ID)
		}
		remote, err := s.client.GetAccount(ctx, b.GatewayID)
		if err != nil {
			return err
		}
		if op.ResultRevision == nil || remote.Revision < *op.ResultRevision {
			return codexgateway.Unavailable()
		}
		if b.OperationKind == "put" || b.OperationKind == "patch" {
			b.AppliedConfigHash = b.OperationConfigHash
		}
		b.Revision = remote.Revision
		b.Snapshot = remote
		b.SyncState = CodexSyncReady
		b.Error = nil
		if b.AppliedConfigHash != codexDesiredConfigHash(a) {
			b.SyncState = CodexSyncPending
		}
		b.OperationID = ""
		b.OperationKind = ""
		now := time.Now().UTC()
		b.SyncedAt = &now
		return s.save(ctx, b, version, operation)
	default:
		return codexgateway.Unavailable()
	}
}

func (s *CodexGatewayService) Synchronize(ctx context.Context, id int64) error {
	if !s.Ready() {
		if err := s.Check(ctx); err != nil {
			return err
		}
	}
	return s.WithAccountLock(ctx, id, func(ctx context.Context) error { return s.synchronizeLocked(ctx, id) })
}

func (s *CodexGatewayService) synchronizeLocked(ctx context.Context, id int64) error {
	a, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if !a.IsOpenAICodex() {
		return errors.New("account is not managed by Gateway")
	}
	if a.ParentAccountID != nil {
		return errors.New("synchronize the parent account")
	}
	b, err := s.bindings.GetCodexBinding(ctx, id)
	if err != nil {
		return err
	}
	if b == nil {
		return errors.New("Gateway binding is missing")
	}
	if b.DeleteRequested && b.SyncState != CodexSyncDeleting {
		// Repair older inconsistent records and retire their cached projections
		// even while the preceding operation is still pending remotely.
		if err := s.save(ctx, b, b.ConfigVersion, b.OperationID); err != nil {
			return err
		}
	}
	if b.OperationID != "" {
		if b.SyncState == CodexSyncReauthorize && !b.DeleteRequested {
			return nil
		}
		op, err := s.client.GetOperation(ctx, b.OperationID)
		if err != nil {
			var failure *codexgateway.Error
			if errors.As(err, &failure) && failure.Code == "operation_not_found" {
				if b.OperationKind == "put" || b.OperationKind == "refresh" {
					b.SyncState = CodexSyncUnknown
					b.Error = &codexgateway.ErrorSummary{Code: "operation_outcome_unknown", Message: "尚未确认凭据操作，不能自动重新提交"}
					return s.save(ctx, b, b.ConfigVersion, b.OperationID)
				}
				// Config and deletion are safe to resume with the original key and body.
				return s.execute(ctx, a, b, nil)
			}
			return err
		}
		return s.observeOperation(ctx, a, b, op)
	}
	remote, err := s.client.GetAccount(ctx, b.GatewayID)
	if err != nil {
		var failure *codexgateway.Error
		if errors.As(err, &failure) && failure.Code == "account_not_found" {
			if b.DeleteRequested {
				return s.finishLocalDelete(ctx, id)
			}
			b.SyncState = CodexSyncMissing
			b.Error = &codexgateway.ErrorSummary{Code: "account_not_found", Message: "远端账号不存在，请重新导入凭据"}
			return s.save(ctx, b, b.ConfigVersion, b.OperationID)
		}
		return err
	}
	b.Revision = remote.Revision
	b.Snapshot = remote
	if b.DeleteRequested {
		if err = s.stage(ctx, a, b, "delete"); err != nil {
			return err
		}
		return s.execute(ctx, a, b, nil)
	}
	if b.SyncState == CodexSyncReauthorize || b.SyncState == CodexSyncUnknown {
		return nil
	}
	if b.AppliedConfigHash != codexDesiredConfigHash(a) {
		if err = s.stage(ctx, a, b, "patch"); err != nil {
			return err
		}
		return s.execute(ctx, a, b, nil)
	}
	b.SyncState = CodexSyncReady
	b.Error = nil
	now := time.Now().UTC()
	b.SyncedAt = &now
	return s.save(ctx, b, b.ConfigVersion, b.OperationID)
}

func (s *CodexGatewayService) SubmitCredentialsLocked(ctx context.Context, a *Account, credentials *codexgateway.CredentialInput) error {
	b, err := s.bindings.GetCodexBinding(ctx, a.ID)
	if err != nil {
		return err
	}
	if b == nil {
		return errors.New("Gateway binding is missing")
	}
	if b.DeleteRequested {
		return infraerrors.Conflict("CODEX_ACCOUNT_DELETING", "account deletion is pending")
	}
	if b.OperationID != "" && b.SyncState == CodexSyncPending {
		return infraerrors.Conflict("CODEX_OPERATION_PENDING", "query the previous operation before replacing credentials")
	}
	if err = s.Check(ctx); err != nil {
		return err
	}
	remote, err := s.client.GetAccount(ctx, b.GatewayID)
	if err == nil {
		b.Revision = remote.Revision
	} else {
		var failure *codexgateway.Error
		if !errors.As(err, &failure) || failure.Code != "account_not_found" {
			return err
		}
		b.Revision = 0
	}
	if err = s.stage(ctx, a, b, "put"); err != nil {
		return err
	}
	return s.execute(ctx, a, b, credentials)
}

type codexOperationKeyContext struct{}

func WithCodexGatewayOperationKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, codexOperationKeyContext{}, key)
}

func (s *CodexGatewayService) Refresh(ctx context.Context, id int64) error {
	requestKey, _ := ctx.Value(codexOperationKeyContext{}).(string)
	if strings.TrimSpace(requestKey) == "" {
		return ErrIdempotencyKeyRequired
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte("refresh:"+requestKey)))

	if err := s.Check(ctx); err != nil {
		return err
	}
	return s.WithAccountLock(ctx, id, func(ctx context.Context) error {
		a, err := s.repo.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if a.IsShadow() {
			return errors.New("refresh the parent account")
		}
		b, err := s.bindings.GetCodexBinding(ctx, id)
		if err != nil {
			return err
		}
		if b == nil {
			return errors.New("Gateway binding is missing")
		}
		if _, exists := b.MutationHistory[key]; exists {
			return nil
		}
		if b.OperationID != "" || b.DeleteRequested {
			return infraerrors.Conflict("CODEX_OPERATION_PENDING", "query the previous operation first")
		}
		remote, err := s.client.GetAccount(ctx, b.GatewayID)
		if err != nil {
			return err
		}
		b.Revision = remote.Revision
		if b.MutationHistory == nil {
			b.MutationHistory = map[string]string{}
		}
		b.MutationHistory[key] = "refresh"
		if err = s.stage(ctx, a, b, "refresh"); err != nil {
			return err
		}
		return s.execute(ctx, a, b, nil)
	})
}

func (s *CodexGatewayService) Delete(ctx context.Context, id int64) error {
	return s.WithAccountLock(ctx, id, func(ctx context.Context) error {
		a, err := s.repo.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if a.IsShadow() {
			return s.repo.Delete(ctx, id)
		}
		b, err := s.bindings.GetCodexBinding(ctx, id)
		if err != nil {
			return err
		}
		if b == nil {
			return errors.New("Gateway binding is missing")
		}
		version, operation := b.ConfigVersion, b.OperationID
		b.DeleteRequested = true
		b.SyncState = CodexSyncDeleting
		if err = s.save(ctx, b, version, operation); err != nil {
			return err
		}
		// Service outages leave a durable, unschedulable deletion request.
		if s.Check(ctx) != nil {
			return nil
		}
		_ = s.synchronizeLocked(ctx, id)
		return nil
	})
}

func (s *CodexGatewayService) finishLocalDelete(ctx context.Context, id int64) error {
	shadows, err := s.repo.ListShadowsByParent(ctx, id)
	if err != nil {
		return err
	}
	for _, shadow := range shadows {
		if err = s.repo.Delete(ctx, shadow.ID); err != nil {
			return err
		}
	}
	if err = s.repo.Delete(ctx, id); err != nil {
		return err
	}
	// Accounts are soft-deleted, so the foreign-key cascade never runs. Release
	// the Gateway ID and creation key once the remote account is gone.
	return s.bindings.DeleteCodexBinding(ctx, id)
}
