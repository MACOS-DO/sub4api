package codexgateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	key := filepath.Join(t.TempDir(), "service-key")
	if err := os.WriteFile(key, []byte("test-gateway-service-key-123456"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{Enabled: true, BaseURL: server.URL, ServiceKeyFile: key, AdminTimeout: time.Second, MaxRequestBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestGatewayNativeResponseAndTrustedIdentity(t *testing.T) {
	const native = `{"id":"resp_test","future":{"opaque":[1,2,3]},"usage":{"input_tokens":7}}`
	identity := Identity{Version: 1, InstallationID: "installation", SessionID: "session", ThreadID: "thread", TurnID: "turn"}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-gateway-service-key-123456" {
			t.Error("service authentication was not used")
		}
		if r.URL.Path == "/internal/capabilities" {
			_, _ = io.WriteString(w, `{"api_version":4,"storage_mode":"postgres","shared_pg_layout_supported":true,"persistent_account_operations_supported":true,"max_request_bytes":1024}`)
			return
		}
		if r.Header.Get("X-Codex4Server-Account-Id") != "bound-account" || r.Header.Get("X-Codex4Server-Timezone") != "Asia/Singapore" {
			t.Error("trusted account/timezone missing")
		}
		if r.Header.Get("Cookie") != "" {
			t.Error("caller cookie leaked onto internal request")
		}
		raw, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Codex4Server-Identity"))
		if err != nil {
			t.Error(err)
		}
		var received Identity
		if json.Unmarshal(raw, &received) != nil || received != identity {
			t.Errorf("identity mismatch: %+v", received)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"future_request":{"raw":true}}` {
			t.Error("request body was rewritten")
		}
		w.Header().Set("X-Codex4Server-Upstream-Request-Id", "official-request")
		w.Header().Set("X-Codex4Server-Upstream-Set-Cookie", base64.RawURLEncoding.EncodeToString([]byte(`["ticket=example; Path=/"]`)))
		_, _ = io.WriteString(w, native)
	})
	if err := client.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	headers := http.Header{"Authorization": {"Bearer caller"}, "Cookie": {"caller=cookie"}, "X-Codex4server-Account-Id": {"forged"}, "X-Codex4server-Identity": {"forged"}}
	response, err := client.Forward(context.Background(), "bound-account", "POST", "/v1/responses", strings.NewReader(`{"future_request":{"raw":true}}`), headers, identity, "Asia/Singapore", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if err = DecodeResponse(response); err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("X-Request-Id") != "official-request" || len(response.Header.Values("Set-Cookie")) != 1 {
		t.Fatal("issuer metadata lost")
	}
	actual, _ := io.ReadAll(response.Body)
	if string(actual) != native {
		t.Fatal("native response was changed")
	}
	StripControlHeaders(response.Header)
	for name := range response.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-codex4server-") {
			t.Fatal("internal header leaked")
		}
	}
}

func TestGatewayMutationTimeoutDoesNotReplayRefreshToken(t *testing.T) {
	var calls atomic.Int32
	received, release := make(chan struct{}), make(chan struct{})
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		close(received)
		<-release
	})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := client.Mutate(ctx, "a", "put", "11111111-1111-4111-8111-111111111111", 0, PutAccount{ExpectedRevision: 0, Credentials: &CredentialInput{Type: "refresh_token", RefreshToken: "test-only-secret"}})
		result <- err
	}()
	select {
	case <-received:
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach server")
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not stop client")
	}
	if calls.Load() != 1 {
		t.Fatalf("mutation was replayed: %d", calls.Load())
	}
}

func TestGatewayMutationsReturnReceiptOnNonSuccessStatus(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		_ = json.NewEncoder(w).Encode(Operation{OperationID: id, AccountID: "a", Kind: "put", ExpectedRevision: 0, State: "indeterminate", Error: &ErrorSummary{Code: "operation_outcome_unknown", Message: "Unknown"}})
	})
	op, err := client.Mutate(context.Background(), "a", "put", id, 0, PutAccount{Credentials: &CredentialInput{Type: "refresh_token", RefreshToken: "test"}})
	if err != nil || op == nil || op.State != "indeterminate" {
		t.Fatalf("receipt was lost: %v", err)
	}
}

func TestGatewayRejectsUnsupportedStorageAndMissingCredentials(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"api_version":4,"storage_mode":"file","shared_pg_layout_supported":true,"persistent_account_operations_supported":false,"max_request_bytes":1024}`)
	})
	if client.Check(context.Background()) == nil || client.Ready() {
		t.Fatal("file storage must not be accepted")
	}
	for _, input := range []string{`{"type":"refresh_token","refresh_token":"test","access_token":"wrong-mode"}`, `{"type":"tokens","access_token":"test"}`, `{"type":"agent_identity","agent_runtime_id":"test","agent_private_key":null}`, `{"type":"auth_json","auth_json":[]}`} {
		if _, err := ParseCredentials([]byte(input)); err == nil {
			t.Errorf("invalid credential input was accepted")
		}
	}
	for _, input := range []string{`{"type":"auth_json","auth_json":{"tokens":{}}}`, `{"type":"tokens","access_token":"test","chatgpt_account_id":"a"}`, `{"type":"refresh_token","refresh_token":"test"}`, `{"type":"personal_access_token","access_token":"test"}`, `{"type":"setup_token","access_token":"test"}`, `{"type":"agent_identity","agent_runtime_id":"test","agent_private_key":"test"}`} {
		if _, err := ParseCredentials([]byte(input)); err != nil {
			t.Errorf("supported credential input rejected: %v", err)
		}
	}
}

func TestGatewayManagementAuthenticationDoesNotInvalidateAdminLogin(t *testing.T) {
	for _, test := range []struct {
		origin string
		want   int
	}{{"gateway", 503}, {"account", 502}, {"upstream", 502}} {
		response := &http.Response{StatusCode: 401, Header: make(http.Header)}
		response.Header.Set("X-Codex4Server-Error-Origin", test.origin)
		err := ManagementResponseError(response, []byte(`{"error":{"code":"authentication_failed","message":"private upstream context"}}`))
		if got := err.(*Error); got.Status != test.want || got.Origin != test.origin {
			t.Fatalf("unexpected classification: %+v", got)
		}
		if strings.Contains(err.Error(), "private upstream context") {
			t.Fatal("upstream context leaked")
		}
	}
}

const testCapabilities = `{"api_version":4,"storage_mode":"postgres","shared_pg_layout_supported":true,"persistent_account_operations_supported":true,"max_request_bytes":1024}`

func TestGatewayForwardStripsCallerCredentialsAndTransportHeaders(t *testing.T) {
	removed := []string{"Authorization", "Proxy-Authorization", "Cookie", "X-Api-Key", "X-Goog-Api-Key", "Accept-Encoding", "Content-Encoding", "Te", "Keep-Alive", "Forwarded", "X-Forwarded-For", "X-Forwarded-Proto", "X-Real-Ip", "Cf-Connecting-Ip", "True-Client-Ip", "X-Client-Ip"}
	kept := map[string]string{"Originator": "codex_cli_rs", "Session_id": "session", "X-Oai-Attestation": "attestation", "Openai-Beta": "responses=experimental", "Content-Type": "application/json"}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/capabilities" {
			_, _ = io.WriteString(w, testCapabilities)
			return
		}
		for _, name := range removed {
			if r.Header.Get(name) == "caller-secret" {
				t.Errorf("%s leaked onto internal request", name)
			}
		}
		for name, value := range kept {
			if r.Header.Get(name) != value {
				t.Errorf("%s was not forwarded", name)
			}
		}
		_, _ = io.WriteString(w, `{}`)
	})
	if err := client.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	for _, name := range removed {
		headers.Set(name, "caller-secret")
	}
	headers["Session_id"] = []string{"session"}
	for name, value := range kept {
		headers[name] = []string{value}
	}
	identity := Identity{Version: 1, InstallationID: "i", SessionID: "s", ThreadID: "t"}
	response, err := client.Forward(context.Background(), "account", "POST", "/v1/responses", strings.NewReader(`{}`), headers, identity, "Asia/Singapore", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

func TestGatewayDialCancellationKeepsReadiness(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, testCapabilities)
	})
	if err := client.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	identity := Identity{Version: 1, InstallationID: "i", SessionID: "s", ThreadID: "t"}
	if _, _, err := client.Dial(ctx, "account", "/v1/responses", nil, identity, "Asia/Singapore", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if !client.Ready() {
		t.Fatal("caller cancellation disabled Gateway")
	}
}

func TestGatewayCancellationAndBadRequestDoNotDisableOtherClients(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/capabilities" {
			_, _ = io.WriteString(w, `{"api_version":4,"storage_mode":"postgres","shared_pg_layout_supported":true,"persistent_account_operations_supported":true,"max_request_bytes":1024}`)
			return
		}
		w.Header().Set("X-Codex4Server-Error-Origin", "gateway")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_identity"}}`)
	})
	if err := client.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	identity := Identity{Version: 1, InstallationID: "i", SessionID: "s", ThreadID: "t"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if client.Check(ctx) == nil || !client.Ready() {
		t.Fatal("canceled capability check disabled Gateway")
	}
	req, err := client.NewRequest(ctx, "account", "POST", "/v1/responses", strings.NewReader(`{}`), nil, identity, "Asia/Singapore", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Do(req); err == nil {
		t.Fatal("canceled request succeeded")
	}
	if !client.Ready() {
		t.Fatal("caller cancellation disabled Gateway")
	}
	req, err = client.NewRequest(context.Background(), "account", "POST", "/v1/responses", strings.NewReader(`{}`), nil, identity, "Asia/Singapore", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 400 || !client.Ready() {
		t.Fatal("bad request disabled Gateway")
	}
}
