package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"time"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
)

// The cache stores only non-sensitive identity translations. Raw client IDs
// are hashed and scoped to the credential owner and caller, even in full mode.
// A missing parent mapping fails closed instead of inventing a different turn.
type CodexGatewayIdentityCache interface {
	GetCodexIdentity(context.Context, string) (string, error)
	PutCodexIdentities(context.Context, map[string]string, time.Duration) error
}

func (s *OpenAIGatewayService) resolveGatewayIdentity(ctx context.Context, a *Account, caller int64, headers http.Header, body []byte) (codexgateway.Identity, error) {
	result, err := resolveCodexGatewayIdentity(a, caller, headers, body)
	if err != nil {
		return result, err
	}
	mode := codexFingerprintModeFromExtra(a.Extra)
	if mode != codexFingerprintSession && mode != codexFingerprintFull {
		return result, nil
	}
	raw := codexGatewayIdentityInput(headers, body)
	cache, _ := s.cache.(CodexGatewayIdentityCache)
	key := func(kind, value string) string {
		return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%s:%s:%s", caller, a.Gateway.BindingID, mode, kind, value))))
	}
	resolve := func(kind, original string, target *string) error {
		if original == "" {
			return nil
		}
		if kind == "turn" && original == raw["turn_id"] {
			*target = result.TurnID
			return nil
		}
		if kind == "thread" && original == raw["thread_id"] {
			*target = result.ThreadID
			return nil
		}
		if kind == "thread" && mode == codexFingerprintFull {
			*target = result.ThreadID
			return nil
		}
		if cache == nil {
			return codexgateway.Unavailable()
		}
		mapped, err := cache.GetCodexIdentity(ctx, key(kind, original))
		if err != nil {
			return codexgateway.Unavailable()
		}
		if mapped == "" {
			return &codexgateway.Error{Status: 400, Origin: "account", Code: "codex_identity_lineage_missing", Message: "Converged parent identity is unavailable; start a new conversation"}
		}
		*target = mapped
		return nil
	}
	for _, ref := range []struct {
		kind, raw string
		target    *string
	}{
		{"turn", raw["parent_turn_id"], &result.ParentTurnID}, {"turn", raw["root_turn_id"], &result.RootTurnID},
		{"thread", raw["parent_thread_id"], &result.ParentThreadID}, {"thread", raw["forked_from_thread_id"], &result.ForkedFromThreadID},
	} {
		if err := resolve(ref.kind, ref.raw, ref.target); err != nil {
			return result, err
		}
	}
	if raw["context_window_id"] != "" {
		result.ContextWindowID = result.WindowID
	}
	mappings := map[string]string{}
	if raw["turn_id"] != "" {
		mappings[key("turn", raw["turn_id"])] = result.TurnID
	}
	if raw["thread_id"] != "" {
		mappings[key("thread", raw["thread_id"])] = result.ThreadID
	}
	if len(mappings) > 0 && cache != nil {
		if err := cache.PutCodexIdentities(ctx, mappings, 30*24*time.Hour); err != nil {
			return result, codexgateway.Unavailable()
		}
	}
	return result, nil
}
