package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
	infraerrors "github.com/MACOS-DO/sub4api/internal/pkg/errors"
	"github.com/google/uuid"
)

type codexEditInProgressKey struct{}
type codexBindingUpdateKey struct{}

type CodexBindingUpdate struct {
	Binding           *CodexAccountBinding
	ExpectedVersion   int64
	ExpectedOperation string
}

func CodexBindingUpdateFromContext(ctx context.Context) *CodexBindingUpdate {
	update, _ := ctx.Value(codexBindingUpdateKey{}).(*CodexBindingUpdate)
	return update
}

func (s *adminServiceImpl) validateCodexGroupBindings(ctx context.Context, ids []int64) error {
	for _, id := range ids {
		g, err := s.groupRepo.GetByIDLite(ctx, id)
		if err != nil {
			return err
		}
		if g.Platform == PlatformOpenAICodex {
			continue
		}
		if g.Platform == PlatformComposite && s.compositeRouteRepo != nil {
			routes, err := s.compositeRouteRepo.ListByGroup(ctx, id, false)
			if err != nil {
				return err
			}
			found := false
			for _, route := range routes {
				if route.Enabled && route.TargetPlatform == PlatformOpenAICodex {
					found = true
					break
				}
			}
			if found {
				continue
			}
		}
		return infraerrors.BadRequest("CODEX_GROUP_MISMATCH", "OpenAI Codex requires its own group or an explicit Composite route")
	}
	return s.ValidateAccountGroupBindings(ctx, ids)
}

type codexShadowCreationKey struct{}

func (s *adminServiceImpl) createCodexGatewayAccount(ctx context.Context, input *CreateAccountInput) (*Account, error) {
	if input.Type != AccountTypeGateway {
		return nil, infraerrors.BadRequest("CODEX_ACCOUNT_TYPE", "OpenAI Codex requires type gateway")
	}
	if s.codexGateway == nil || s.codexGateway.bindings == nil || s.cfg == nil || !s.cfg.Gateway.Codex4Server.Enabled {
		return nil, codexgateway.Unavailable()
	}
	if strings.TrimSpace(input.GatewayOperationKey) == "" {
		return nil, ErrIdempotencyKeyRequired
	}
	var credentials *codexgateway.CredentialInput
	options := codexgateway.PutOptions{OAuthClientID: input.GatewayOAuthClientID, PreserveRefreshToken: input.GatewayPreserveRefreshToken}
	var err error
	if input.GatewayImportID == "" {
		credentials, err = codexgateway.ParseCredentials(input.GatewayCredentials)
		if err != nil {
			return nil, infraerrors.BadRequest("CODEX_CREDENTIALS_INVALID", err.Error())
		}
	}
	if err = ValidateCodexBusinessCredentials(input.Credentials); err != nil {
		return nil, infraerrors.BadRequest("CODEX_BUSINESS_CONFIG_INVALID", err.Error())
	}
	if err = ValidateOpenAIRequestTimezoneExtra(input.Platform, input.Extra); err != nil {
		return nil, err
	}
	if err = s.validateCodexGroupBindings(ctx, input.GroupIDs); err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(input.GatewayOperationKey)))
	fingerprint, err := BuildIdempotencyFingerprint("POST", "admin.accounts.codex", input.GatewayOperationKey, input)
	if err != nil {
		return nil, err
	}
	var result *Account
	err = s.codexGateway.bindings.WithCodexBindingLock(ctx, "create:"+key, func(ctx context.Context) error {
		previous, err := s.codexGateway.bindings.FindCodexBindingByCreationKey(ctx, key)
		if err != nil {
			return err
		}
		if previous != nil {
			if previous.CreationFingerprint != fingerprint {
				return ErrIdempotencyKeyConflict
			}
			result, err = s.GetAccount(ctx, previous.AccountID)
			return err
		}
		extra := maps.Clone(input.Extra)
		if extra == nil {
			extra = map[string]any{}
		}
		delete(extra, "access_token")
		delete(extra, "refresh_token")
		delete(extra, "id_token")
		delete(extra, "agent_private_key")
		extra[codexFingerprintSeedExtraKey] = newCodexFingerprintSeed()
		account, err := buildAccountForCreate(input, extra)
		if err != nil {
			return err
		}
		if account.ProxyID != nil {
			account.Proxy, err = s.proxyRepo.GetByID(ctx, *account.ProxyID)
			if err != nil {
				return err
			}
		}
		if err := s.codexGateway.validateCredentialWrite(ctx, credentials, &options, account, 0); err != nil {
			return err
		}
		binding := &CodexAccountBinding{GatewayID: "sub4-" + uuid.NewString(), CreationKey: key, CreationFingerprint: fingerprint, ConfigVersion: 1, SyncState: CodexSyncPending, OperationID: uuid.NewString(), OperationKind: "put", OperationConfigHash: codexDesiredConfigHash(account)}
		binding.rememberOperation(input.GatewayOperationKey, input.GatewayLookupKey, input.GatewayItemIndex)
		if input.GatewayImportID != "" {
			binding.GatewayID = input.GatewayImportID
			binding.OperationID = ""
			binding.OperationKind = ""
			binding.SyncState = CodexSyncMissing
		}
		account.GatewayBinding = binding
		account.Gateway = binding.Projection()
		groupIDs := input.GroupIDs
		if len(groupIDs) == 0 && !input.SkipDefaultGroupBind && s.groupRepo != nil {
			if candidates, err := s.groupRepo.ListActiveByPlatform(ctx, PlatformOpenAICodex); err == nil {
				for _, group := range candidates {
					if group.Name == PlatformOpenAICodex+"-default" {
						groupIDs = []int64{group.ID}
						break
					}
				}
			}
		}
		groups := make([]AccountGroup, 0, len(groupIDs))
		for _, id := range groupIDs {
			groups = append(groups, AccountGroup{GroupID: id, Priority: account.Priority})
		}
		creator := s.accountDuplicateRepo
		if creator == nil {
			creator, _ = s.accountRepo.(AccountDuplicateRepository)
		}
		if creator == nil {
			return errors.New("transactional account creation unavailable")
		}
		if err = creator.CreateWithAccountGroups(ctx, account, groups); err != nil {
			return err
		}
		// Local commit is the durable boundary. Remote failures return this same
		// pending account; no raw input is queued for background replay.
		if input.GatewayImportID != "" {
			_ = s.codexGateway.Synchronize(ctx, account.ID)
		} else {
			_ = s.codexGateway.WithAccountLock(ctx, account.ID, func(ctx context.Context) error {
				return s.codexGateway.execute(ctx, account, binding, credentials, options)
			})
		}
		result, err = s.GetAccount(ctx, account.ID)
		return err
	})
	return result, err
}

func (s *adminServiceImpl) updateCodexGatewayAccount(ctx context.Context, account *Account, input *UpdateAccountInput) (*Account, error) {
	options := codexgateway.PutOptions{OAuthClientID: input.GatewayOAuthClientID, PreserveRefreshToken: input.GatewayPreserveRefreshToken}
	if len(input.GatewayCredentials) == 0 && (options.OAuthClientID != "" || options.PreserveRefreshToken) {
		return nil, infraerrors.BadRequest("CODEX_CREDENTIALS_REQUIRED", "Credential options require credentials")
	}
	if s.codexGateway == nil || s.codexGateway.bindings == nil {
		return nil, codexgateway.Unavailable()
	}
	if input.Type != "" && input.Type != AccountTypeGateway {
		return nil, infraerrors.BadRequest("CODEX_ACCOUNT_TYPE", "Gateway account type cannot change")
	}
	if err := ValidateCodexBusinessCredentials(input.Credentials); err != nil {
		return nil, infraerrors.BadRequest("CODEX_BUSINESS_CONFIG_INVALID", err.Error())
	}
	if input.GroupIDs != nil {
		if err := s.validateCodexGroupBindings(ctx, *input.GroupIDs); err != nil {
			return nil, err
		}
	}
	var credentials *codexgateway.CredentialInput
	var err error
	if len(input.GatewayCredentials) > 0 {
		if account.IsShadow() {
			return nil, infraerrors.BadRequest("CODEX_SHADOW_CREDENTIALS", "reauthorize the parent account")
		}
		if strings.TrimSpace(input.GatewayOperationKey) == "" {
			return nil, ErrIdempotencyKeyRequired
		}
		credentials, err = codexgateway.ParseCredentials(input.GatewayCredentials)
		if err != nil {
			return nil, infraerrors.BadRequest("CODEX_CREDENTIALS_INVALID", err.Error())
		}
	}
	var result *Account
	err = s.codexGateway.WithAccountLock(ctx, account.ID, func(ctx context.Context) error {
		lockedCtx := context.WithValue(ctx, codexEditInProgressKey{}, true)
		current, err := s.accountRepo.GetByID(ctx, account.ID)
		if err != nil {
			return err
		}
		if !current.IsShadow() {
			b, err := s.codexGateway.bindings.GetCodexBinding(ctx, current.ID)
			if err != nil {
				return err
			}
			if b == nil {
				return errors.New("Gateway binding is missing")
			}
			if credentials != nil {
				key := fmt.Sprintf("%x", sha256.Sum256([]byte("put:"+input.GatewayOperationKey)))
				fingerprint, err := BuildIdempotencyFingerprint("PUT", "codex.account", input.GatewayOperationKey, input)
				if err != nil {
					return err
				}
				if previous, ok := b.MutationHistory[key]; ok {
					if previous != fingerprint {
						return ErrIdempotencyKeyConflict
					}
					result, err = s.GetAccount(ctx, current.ID)
					return err
				}
				if b.MutationHistory == nil {
					b.MutationHistory = map[string]string{}
				}
				b.MutationHistory[key] = fingerprint
			}
			if b.DeleteRequested {
				return infraerrors.Conflict("CODEX_ACCOUNT_DELETING", "account deletion is pending")
			}
			if credentials != nil && b.OperationID != "" && b.SyncState == CodexSyncPending {
				return infraerrors.Conflict("CODEX_OPERATION_PENDING", "query the previous operation before replacing credentials")
			}
			if credentials != nil {
				// Gateway may have refreshed since the last 30-second snapshot.
				// This is a new, explicit authorization input, never a replay of
				// the previous credential operation. Only read its current revision.
				if err := s.codexGateway.Check(ctx); err != nil {
					return err
				}
				remote, readErr := s.codexGateway.client.GetAccount(ctx, b.GatewayID)
				if readErr != nil {
					var failure *codexgateway.Error
					if !errors.As(readErr, &failure) || failure.Code != "account_not_found" {
						return readErr
					}
					b.Revision = 0
				} else {
					b.Revision = remote.Revision
				}
			}
			changed := credentials != nil || (input.Name != "" && input.Name != current.Name) || input.ProxyID != nil
			if changed {
				update := &CodexBindingUpdate{Binding: b, ExpectedVersion: b.ConfigVersion, ExpectedOperation: b.OperationID}
				b.ConfigVersion++
				if b.OperationID == "" {
					b.SyncState = CodexSyncPending
				}
				if credentials != nil {
					b.OperationID = uuid.NewString()
					b.OperationKind = "put"
					b.OperationRevision = b.Revision
					b.SyncState = CodexSyncPending
					b.Error = nil
					desired := *current
					if input.Name != "" {
						desired.Name = input.Name
					}
					if input.ProxyID != nil {
						desired.Proxy = nil
						if *input.ProxyID > 0 {
							desired.Proxy, err = s.proxyRepo.GetByID(ctx, *input.ProxyID)
							if err != nil {
								return err
							}
						}
					}
					b.OperationConfigHash = codexDesiredConfigHash(&desired)
					if err := s.codexGateway.validateCredentialWrite(ctx, credentials, &options, &desired, current.ID); err != nil {
						return err
					}
					b.rememberOperation(input.GatewayOperationKey, input.GatewayLookupKey, input.GatewayItemIndex)
				}
				lockedCtx = context.WithValue(lockedCtx, codexBindingUpdateKey{}, update)
			}
		}
		result, err = s.UpdateAccount(lockedCtx, current.ID, input)
		if err != nil {
			return err
		}
		if !current.IsShadow() {
			if credentials != nil {
				b, err := s.codexGateway.bindings.GetCodexBinding(ctx, current.ID)
				if err != nil {
					return err
				}
				_ = s.codexGateway.execute(ctx, result, b, credentials, options)
			} else if s.codexGateway.Ready() {
				_ = s.codexGateway.synchronizeLocked(ctx, current.ID)
			}
		}
		result, err = s.GetAccount(ctx, current.ID)
		return err
	})
	return result, err
}

func (s *adminServiceImpl) SyncCodexGatewayAccount(ctx context.Context, id int64) (*Account, error) {
	if s.codexGateway == nil {
		return nil, codexgateway.Unavailable()
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !account.IsOpenAICodex() {
		return nil, infraerrors.BadRequest("CODEX_ACCOUNT_REQUIRED", "OpenAI Codex account required")
	}
	owner := id
	if account.ParentAccountID != nil {
		owner = *account.ParentAccountID
	}
	if err = s.codexGateway.Synchronize(ctx, owner); err != nil {
		return nil, err
	}
	return s.GetAccount(ctx, id)
}

func (s *adminServiceImpl) annotateCodexGateway(account *Account) {
	if account != nil && account.Gateway != nil {
		available := s.codexGateway.Ready()
		account.Gateway.ServiceAvailable = &available
	}
}
