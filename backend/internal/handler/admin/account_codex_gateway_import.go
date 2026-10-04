package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
	"github.com/MACOS-DO/sub4api/internal/pkg/response"
	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const codexGatewayImportTimeout = 15 * time.Minute

type CodexGatewayImportRequest struct {
	Content         string               `json:"gateway_import_content"`
	Method          string               `json:"method"`
	Account         CreateAccountRequest `json:"account"`
	TargetAccountID int64                `json:"target_account_id,omitempty"`
	UpdateExisting  *bool                `json:"update_existing,omitempty"`
}
type CodexGatewayImportResult struct {
	Total   int                      `json:"total"`
	Created int                      `json:"created"`
	Updated int                      `json:"updated"`
	Pending int                      `json:"pending"`
	Skipped int                      `json:"skipped"`
	Failed  int                      `json:"failed"`
	Items   []CodexSessionImportItem `json:"items"`
}

// Normalize format only. Credential validation, exchange and persistence remain
// inside Gateway; this function must never call the legacy OAuth provider.
func normalizeGatewayImport(entry codexImportEntry, method string) (*codexgateway.CredentialInput, *codexImportAccount, error) {
	if method == "refresh_token" || method == "mobile_refresh_token" || method == "personal_access_token" || method == "setup_token" {
		text, ok := entry.Value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, nil, errors.New("expected one credential per line")
		}
		kind := method
		if kind == "mobile_refresh_token" {
			kind = "refresh_token"
		}
		input := &codexgateway.CredentialInput{Type: kind}
		if kind == "refresh_token" {
			input.RefreshToken = strings.TrimSpace(text)
		} else {
			input.AccessToken = strings.TrimSpace(text)
		}
		return input, &codexImportAccount{}, nil
	}
	// Canonical Gateway input is accepted in the same shared JSON input area.
	if value, ok := entry.Value.(map[string]any); ok && value["type"] != nil {
		raw, _ := json.Marshal(value)
		input, err := codexgateway.ParseCredentials(raw)
		if err != nil || input.Type == "oauth_code" {
			return nil, nil, errors.New("invalid import credential")
		}
		item := &codexImportAccount{AccountID: input.ChatGPTAccountID, AccessToken: input.AccessToken, RefreshToken: input.RefreshToken}
		if input.Type == "tokens" {
			if parsed, e := normalizeCodexImportEntry(codexImportEntry{Index: entry.Index, Value: value}); e == nil {
				item = parsed
			}
		}
		if method == "agent_identity" && input.Type != "agent_identity" {
			return nil, nil, errors.New("expected Agent Identity")
		}
		return input, item, nil
	}
	item, err := normalizeCodexImportEntry(entry)
	if err != nil {
		return nil, nil, errors.New("invalid credential or missing account identity")
	}
	if method == "agent_identity" && !item.IsAgentIdentity {
		return nil, nil, errors.New("expected Agent Identity")
	}
	if item.IsAgentIdentity {
		return &codexgateway.CredentialInput{Type: "agent_identity", AgentRuntimeID: item.AgentRuntimeID, AgentPrivateKey: item.AgentPrivateKey, TaskID: item.AgentTaskID, ChatGPTAccountID: item.AccountID}, item, nil
	}
	if item.AccountID == "" {
		return nil, nil, errors.New("ChatGPT account ID is required")
	}
	return &codexgateway.CredentialInput{Type: "tokens", AccessToken: item.AccessToken, RefreshToken: item.RefreshToken, ChatGPTAccountID: item.AccountID, ChatGPTPlanType: item.PlanType}, item, nil
}

func gatewayImportMatch(accounts []service.Account, item *codexImportAccount) (*service.Account, error) {
	var match *service.Account
	for i := range accounts {
		a := &accounts[i]
		if !a.IsOpenAICodex() || a.IsShadow() || a.Gateway == nil || a.Gateway.Snapshot == nil {
			continue
		}
		snapshot := a.Gateway.Snapshot
		matched := false
		if item.AccessToken != "" && item.RefreshToken == "" {
			matched = a.Extra["access_token_sha256"] == codexTokenFingerprint(item.AccessToken)
		} else if item.AccountID != "" && snapshot.ChatGPTAccountID != nil && *snapshot.ChatGPTAccountID == item.AccountID {
			matched = item.UserID != "" && snapshot.Metadata.ChatGPTUserID != nil && *snapshot.Metadata.ChatGPTUserID == item.UserID
			if item.IsAgentIdentity {
				matched = true
			}
		}
		if matched {
			if match != nil {
				return nil, errors.New("multiple matching accounts; reauthorize the selected account")
			}
			match = a
		}
	}
	return match, nil
}

func (h *AccountHandler) ImportCodexGatewayCredentials(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var req CodexGatewayImportRequest
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid import request")
		return
	}
	key := c.GetHeader("Idempotency-Key")
	if _, err := uuid.Parse(key); err != nil {
		response.BadRequest(c, "A UUID Idempotency-Key is required")
		return
	}
	switch req.Method {
	case "codex_session", "agent_identity", "refresh_token", "mobile_refresh_token", "personal_access_token", "setup_token":
	default:
		response.BadRequest(c, "Unsupported import method")
		return
	}
	if req.Account.Platform != "openai_codex" || req.Account.Type != "gateway" {
		response.BadRequest(c, "OpenAI Codex account configuration is required")
		return
	}
	entries, err := parseCodexSessionImportEntries(CodexSessionImportRequest{Content: req.Content})
	if err != nil || len(entries) == 0 || len(entries) > 200 {
		response.BadRequest(c, "Provide 1 to 200 valid import entries")
		return
	}
	if req.TargetAccountID > 0 && len(entries) != 1 {
		response.BadRequest(c, "Reauthorization accepts exactly one entry")
		return
	}
	accounts, err := h.listAccountsFiltered(c.Request.Context(), service.PlatformOpenAICodex, service.AccountTypeGateway, "", "", 0, "", "created_at", "desc")
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	executeAdminIdempotentJSONWithTimeout(c, "admin.openai-codex.import", req, service.DefaultWriteIdempotencyTTL(), codexGatewayImportTimeout, func(ctx context.Context) (any, error) {
		ctx = service.WithCodexGatewayActor(ctx, adminActorScope(c))
		result := CodexGatewayImportResult{Total: len(entries), Items: []CodexSessionImportItem{}}
		seen := map[string]bool{}
		for _, entry := range entries {
			row := CodexSessionImportItem{Index: entry.Index, Action: "failed"}
			fail := func(message string) { row.Message = message; result.Failed++; result.Items = append(result.Items, row) }
			credential, item, err := normalizeGatewayImport(entry, req.Method)
			if err != nil {
				fail(err.Error())
				continue
			}
			raw, _ := json.Marshal(credential)
			identity := string(raw)
			if seen[identity] {
				row.Action = "skipped"
				result.Skipped++
				result.Items = append(result.Items, row)
				continue
			}
			seen[identity] = true
			var existing *service.Account
			if req.TargetAccountID > 0 {
				for i := range accounts {
					if accounts[i].ID == req.TargetAccountID {
						existing = &accounts[i]
						break
					}
				}
				if existing == nil || existing.IsShadow() {
					fail("Reauthorize an existing OpenAI Codex parent account")
					continue
				}
			} else if req.UpdateExisting == nil || *req.UpdateExisting {
				existing, err = gatewayImportMatch(accounts, item)
				if err != nil {
					fail(err.Error())
					continue
				}
			}
			base := req.Account
			extra := maps.Clone(base.Extra)
			if extra == nil {
				extra = map[string]any{}
			}
			if item.AccessToken != "" {
				extra["access_token_sha256"] = codexTokenFingerprint(item.AccessToken)
			}
			client := base.GatewayOAuthClientID
			if req.Method == "mobile_refresh_token" {
				client = "app_LlGpXReQgckcGGUo2JrYvtJK"
			}
			operation := fmt.Sprintf("%s:%s:%d", adminActorScope(c), key, entry.Index)
			lookup := adminActorScope(c) + ":" + key
			var account *service.Account
			if existing != nil {
				preserve := credential.Type == "tokens" && credential.RefreshToken == "" && existing.Gateway != nil && existing.Gateway.Authentication == "at_rt"
				if preserve && existing.Gateway.Snapshot != nil && existing.Gateway.Snapshot.OAuthClientID != nil {
					client = *existing.Gateway.Snapshot.OAuthClientID
				}
				mergedExtra := maps.Clone(existing.Extra)
				if mergedExtra == nil {
					mergedExtra = map[string]any{}
				}
				maps.Copy(mergedExtra, extra)
				business := maps.Clone(existing.Credentials)
				if business == nil {
					business = map[string]any{}
				}
				maps.Copy(business, base.Credentials)
				update := &service.UpdateAccountInput{GatewayCredentials: raw, GatewayOperationKey: operation, GatewayLookupKey: lookup, GatewayItemIndex: entry.Index, GatewayOAuthClientID: client, GatewayPreserveRefreshToken: preserve, Credentials: business, Extra: mergedExtra}
				if req.TargetAccountID == 0 {
					update.ProxyID = base.ProxyID
					update.Concurrency = &base.Concurrency
					update.Priority = &base.Priority
					update.LoadFactor = base.LoadFactor
					update.RateMultiplier = base.RateMultiplier
					if len(base.GroupIDs) > 0 {
						update.GroupIDs = &base.GroupIDs
					}
				}
				account, err = h.adminService.UpdateAccount(ctx, existing.ID, update)
			} else {
				name := buildCodexCreateAccountName(base.Name, item, entry.Index, len(entries))
				if strings.TrimSpace(name) == "" {
					name = fmt.Sprintf("Codex %d", entry.Index)
				}
				account, err = h.adminService.CreateAccount(ctx, &service.CreateAccountInput{GatewayCredentials: raw, GatewayOperationKey: operation, GatewayLookupKey: lookup, GatewayItemIndex: entry.Index, GatewayOAuthClientID: client, Name: name, Notes: base.Notes, Platform: service.PlatformOpenAICodex, Type: service.AccountTypeGateway, Credentials: base.Credentials, Extra: extra, ProxyID: base.ProxyID, Concurrency: base.Concurrency, Priority: base.Priority, LoadFactor: base.LoadFactor, RateMultiplier: base.RateMultiplier, GroupIDs: base.GroupIDs, ExpiresAt: base.ExpiresAt, AutoPauseOnExpired: base.AutoPauseOnExpired})
			}
			if err != nil {
				fail(err.Error())
				continue
			}
			row.AccountID = account.ID
			row.Name = account.Name
			if account.Gateway == nil || account.Gateway.SyncState == service.CodexSyncPending || account.Gateway.SyncState == service.CodexSyncUnknown {
				row.Action = "pending"
				result.Pending++
			} else if account.Gateway.SyncState != service.CodexSyncReady {
				if account.Gateway.Error != nil {
					fail(account.Gateway.Error.Message)
				} else {
					fail("Authorization failed")
				}
				continue
			} else if existing != nil {
				row.Action = "updated"
				result.Updated++
			} else {
				row.Action = "created"
				result.Created++
			}
			result.Items = append(result.Items, row)
			if existing == nil {
				accounts = append(accounts, *account)
			} else {
				*existing = *account
			}
		}
		return result, nil
	})
}
