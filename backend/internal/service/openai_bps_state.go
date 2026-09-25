package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const OpenAIBPSStateTTL = 7 * 24 * time.Hour

// Implemented by the Redis gateway cache. No in-process fallback: tool history
// must survive an instance change and must never be shared between API keys.
type OpenAIBPSStateStore interface {
	BPSGet(context.Context, string) ([]byte, error)
	BPSPut(context.Context, string, []byte) error
	BPSBind(context.Context, string, []byte) ([]byte, error)
}

type bpsSessionBinding struct {
	AccountID int64  `json:"account_id"`
	Identity  string `json:"identity"`
}
type bpsRoutingContext struct {
	Scope   string
	Binding *bpsSessionBinding
}
type bpsRoutingContextKey struct{}

type bpsError struct {
	Status        int
	Code, Message string
}

func (e *bpsError) Error() string     { return e.Message }
func bpsInvalid(message string) error { return &bpsError{400, "invalid_request_error", message} }
func bpsExpired() error {
	return &bpsError{409, "bps_context_expired", "BPS conversation state has expired; start a new conversation"}
}
func bpsUnavailable() error {
	return &bpsError{503, "bps_state_unavailable", "BPS conversation state is temporarily unavailable"}
}
func bpsDigest(s string) string { d := sha256.Sum256([]byte(s)); return hex.EncodeToString(d[:]) }
func (s *OpenAIGatewayService) bpsStateStore() (OpenAIBPSStateStore, error) {
	store, ok := s.cache.(OpenAIBPSStateStore)
	if !ok {
		return nil, bpsUnavailable()
	}
	return store, nil
}
func (s *OpenAIGatewayService) bpsExplicitSession(c *gin.Context, body []byte) string {
	session := s.ExtractSessionID(c, body)
	if session == "" {
		for _, key := range []string{"client_metadata.session_id", "client_metadata.sessionId", "session_id", "sessionId", "promptCacheKey"} {
			if value := gjson.GetBytes(body, key).String(); value != "" {
				session = value
				break
			}
		}
	}
	return strings.TrimSpace(session)
}

func (s *OpenAIGatewayService) bpsScope(c *gin.Context, body []byte) string {
	session := s.bpsExplicitSession(c, body)
	if session != "" {
		session = "explicit:" + session
	} else {
		input := gjson.GetBytes(body, "input")
		if input.Type == gjson.String {
			session = "root:" + input.String()
		} else {
			for _, item := range input.Array() {
				if item.Get("role").String() != "user" {
					continue
				}
				content := item.Get("content")
				if content.Type == gjson.String {
					session = "root:" + content.String()
				} else {
					var texts []string
					for _, part := range content.Array() {
						if part.Get("type").String() == "input_text" {
							texts = append(texts, part.Get("text").String())
						}
					}
					session = "root:" + strings.Join(texts, "\n")
				}
				break
			}
		}
	}
	return "openai_bps:" + bpsDigest(fmt.Sprintf("%d:%s", getAPIKeyIDFromContext(c), session))
}

type bpsCompactReference struct {
	Scope string `json:"scope"`
}

func bpsCompactReferenceKey(apiKeyID int64, ref string) string {
	return "openai_bps:ref:" + bpsDigest(fmt.Sprintf("%d:%s", apiKeyID, ref))
}

// PrepareOpenAIBPSRouting runs before account selection. An established binding
// is a hard constraint, not a sticky preference that can silently fail over.
func (s *OpenAIGatewayService) PrepareOpenAIBPSRouting(c *gin.Context, body []byte) error {
	store, err := s.bpsStateStore()
	if err != nil {
		return err
	}
	scope := s.bpsScope(c, body)
	// A compacted history no longer contains the original user message. Recover
	// its root from a tenant-scoped opaque reference when no session ID exists.
	if s.bpsExplicitSession(c, body) == "" {
		for _, item := range gjson.GetBytes(body, "input").Array() {
			if item.Get("type").String() != "compaction" && item.Get("type").String() != "compaction_summary" {
				continue
			}
			ref := item.Get("encrypted_content").String()
			if !strings.HasPrefix(ref, bpsCompactPrefix) {
				continue
			}
			var reference bpsCompactReference
			if err := s.bpsGet(c.Request.Context(), bpsCompactReferenceKey(getAPIKeyIDFromContext(c), ref), &reference); err != nil {
				return err
			}
			if reference.Scope == "" {
				return bpsExpired()
			}
			scope = reference.Scope
			break
		}
	}
	data, err := store.BPSGet(c.Request.Context(), scope+":session")
	if err != nil {
		return bpsUnavailable()
	}
	routing := bpsRoutingContext{Scope: scope}
	if len(data) > 0 {
		var binding bpsSessionBinding
		if json.Unmarshal(data, &binding) != nil {
			return bpsExpired()
		}
		routing.Binding = &binding
	} else {
		for _, item := range gjson.GetBytes(body, "input").Array() {
			// A text-only continuation still belongs to its established account.
			if item.Get("role").String() == "assistant" {
				return bpsExpired()
			}
			switch item.Get("type").String() {
			case "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output", "reasoning", "compaction", "compaction_summary":
				if item.Get("encrypted_content").String() != "" || item.Get("call_id").String() != "" {
					return bpsExpired()
				}
			}
		}
	}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), bpsRoutingContextKey{}, routing))
	return nil
}

func bpsBoundAccountAllowed(ctx context.Context, account *Account) bool {
	routing, ok := ctx.Value(bpsRoutingContextKey{}).(bpsRoutingContext)
	return !ok || routing.Binding == nil || (account.ID == routing.Binding.AccountID && account.GetCredential("chatgpt_account_id") == routing.Binding.Identity)
}
func bpsHasBinding(ctx context.Context) bool {
	r, ok := ctx.Value(bpsRoutingContextKey{}).(bpsRoutingContext)
	return ok && r.Binding != nil
}

func (s *OpenAIGatewayService) bpsBind(ctx context.Context, scope string, account *Account) error {
	store, err := s.bpsStateStore()
	if err != nil {
		return err
	}
	binding := bpsSessionBinding{account.ID, account.GetCredential("chatgpt_account_id")}
	data, _ := json.Marshal(binding)
	actual, err := store.BPSBind(ctx, scope+":session", data)
	if err != nil {
		return bpsUnavailable()
	}
	var saved bpsSessionBinding
	if json.Unmarshal(actual, &saved) != nil || saved != binding {
		return &bpsError{409, "bps_account_conflict", "This BPS conversation is bound to another account"}
	}
	return nil
}
func bpsStateKey(scope string, account *Account, kind, id string) string {
	return fmt.Sprintf("%s:%d:%s:%s", scope, account.ID, kind, bpsDigest(id))
}
func (s *OpenAIGatewayService) bpsGet(ctx context.Context, key string, v any) error {
	store, e := s.bpsStateStore()
	if e != nil {
		return e
	}
	data, e := store.BPSGet(ctx, key)
	if e != nil {
		return bpsUnavailable()
	}
	if len(data) == 0 || json.Unmarshal(data, v) != nil {
		return bpsExpired()
	}
	return nil
}
func (s *OpenAIGatewayService) bpsPut(ctx context.Context, key string, v any) error {
	store, e := s.bpsStateStore()
	if e != nil {
		return e
	}
	data, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if e = store.BPSPut(ctx, key, data); e != nil {
		return bpsUnavailable()
	}
	return nil
}

func OpenAIBPSHasBinding(ctx context.Context) bool { return bpsHasBinding(ctx) }
func WriteOpenAIBPSAccountUnavailable(c *gin.Context) {
	WriteOpenAIBPSError(c, &bpsError{409, "bps_account_unavailable", "The account bound to this BPS conversation is unavailable; restore that account or start a new conversation"})
}
