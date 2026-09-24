package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/MACOS-DO/sub4api/internal/server/middleware"
	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/stretchr/testify/require"
)

func TestCodexDiagnosticGatewayErrorRedactsMessages(t *testing.T) {
	for _, test := range []struct{ body, code string }{
		{`{"code":403,"message":"secret-key","reason":"MODEL_NOT_ALLOWED"}`, "MODEL_NOT_ALLOWED"},
		{`{"error":{"code":"invalid_api_key","message":"secret-key"}}`, "invalid_api_key"},
		{`{"error":{"code":"secret key with spaces"}}`, ""},
		{`{"message":"secret-key"}`, ""},
	} {
		require.Equal(t, test.code, codexDiagnosticGatewayError([]byte(test.body)))
	}
}

func TestCodexDiagnosticOutputRequiresCompletedResponse(t *testing.T) {
	for _, test := range []struct {
		name     string
		payload  string
		text     string
		complete bool
	}{
		{"completed stream", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"1, 2\"}\n\ndata: {\"type\":\"response.completed\"}\n\n", "1, 2", true},
		{"partial stream", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"1, 2\"}\n\n", "1, 2", false},
		{"failed stream", "data: {\"type\":\"response.failed\"}\n\n", "", false},
		{"completed JSON", `{"status":"completed","output_text":"3, 4"}`, "3, 4", true},
		{"partial JSON", `{"status":"incomplete","output_text":"3, 4"}`, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			text, complete := codexDiagnosticOutput([]byte(test.payload))
			require.Equal(t, test.complete, complete)
			require.Equal(t, test.text, text)
		})
	}
}

type codexDiagnosticChallengeAccountRepo struct {
	service.AccountRepository
	account *service.Account
}

func (r *codexDiagnosticChallengeAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	if r.account != nil {
		return r.account, nil
	}
	return &service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Credentials: map[string]any{"chatgpt_account_id": "diagnostic-test-account"}}, nil
}

type codexDiagnosticChallengeKeyRepo struct{ service.APIKeyRepository }

func (r *codexDiagnosticChallengeKeyRepo) GetByID(_ context.Context, id int64) (*service.APIKey, error) {
	return &service.APIKey{ID: id, UserID: 17, Key: "test-key", Status: service.StatusActive}, nil
}

func newCodexChallengeDiagnosticHandler(t *testing.T, models []string, router http.Handler, settings ...*service.SettingService) (*AccountHandler, *gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	cfg := &config.Config{}
	var settingService *service.SettingService
	if len(settings) > 0 {
		settingService = settings[0]
	}
	return newCodexDiagnosticHandlerForAccount(t, models, router, cfg, nil, settingService)
}

func newCodexDiagnosticHandlerForAccount(t *testing.T, models []string, router http.Handler, cfg *config.Config, account *service.Account, settingService *service.SettingService) (*AccountHandler, *gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gateway := service.NewOpenAIGatewayService(
		&codexDiagnosticChallengeAccountRepo{account: account}, nil, nil, nil, nil, nil, nil, cfg,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, settingService, nil,
	)
	handler := &AccountHandler{
		codexTicketGateway: gateway,
		codexTicketRouter:  router,
		codexTicketAPIKeys: service.NewAPIKeyService(&codexDiagnosticChallengeKeyRepo{}, nil, nil, nil, nil, nil, cfg),
	}
	payload, err := json.Marshal(map[string]any{"api_key_id": 9, "models": models})
	require.NoError(t, err)
	writer := httptest.NewRecorder()
	client, _ := gin.CreateTestContext(writer)
	client.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/41/codex-diagnostic", strings.NewReader(string(payload)))
	client.Request.Header.Set("Content-Type", "application/json")
	client.Params = gin.Params{{Key: "id", Value: "41"}}
	client.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 17})
	return handler, client, writer
}

func TestCodexDiagnosticChallengeRequestAndValidationUseSameDraw(t *testing.T) {
	challenges := []service.ModelTraceChallenge{
		{Prompt: "first challenge: 292 个 1 到 355 的整数", ExpectedCount: 292},
		{Prompt: "second challenge: 332 个 1 到 355 的整数", ExpectedCount: 332},
	}
	calls := 0
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Less(t, calls, len(challenges))
		require.Equal(t, challenges[calls].Prompt, gjson.GetBytes(body, "input.4.content.0.text").String())
		require.Equal(t, "/v1/responses", r.URL.Path)
		calls++
		// 170 parsed values distinguish the old fixed 292 threshold from 332.
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"status": "completed", "output_text": strings.Repeat("1 ", 170)}))
	})
	handler, client, writer := newCodexChallengeDiagnosticHandler(t, []string{"gpt-5.4", "gpt-5.5"}, router)
	generated := 0
	handler.diagnoseCodexModels(client, func() (service.ModelTraceChallenge, error) {
		require.Less(t, generated, len(challenges))
		challenge := challenges[generated]
		generated++
		return challenge, nil
	})
	require.Equal(t, http.StatusOK, writer.Code)
	require.Equal(t, 2, generated)
	require.Equal(t, 2, calls)
	items := gjson.GetBytes(writer.Body.Bytes(), "data.items").Array()
	require.Len(t, items, 2)
	require.NotEqual(t, "insufficient_numbers", items[0].Get("reason").String())
	require.NotEmpty(t, items[0].Get("predicted_model").String())
	require.Equal(t, "insufficient_numbers", items[1].Get("reason").String())
	require.Equal(t, "failed", items[1].Get("status").String())
	for _, item := range items {
		require.EqualValues(t, 170, item.Get("parsed_number_count").Int())
	}
}

func TestCodexDiagnosticChallengeGenerationFailureDoesNotSendRequest(t *testing.T) {
	handler, client, writer := newCodexChallengeDiagnosticHandler(t, []string{"gpt-5.4"}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("failed challenge must not enter the gateway")
	}))
	generated := 0
	handler.diagnoseCodexModels(client, func() (service.ModelTraceChallenge, error) {
		generated++
		return service.ModelTraceChallenge{}, errors.New("entropy unavailable")
	})
	require.Equal(t, http.StatusOK, writer.Code)
	require.Equal(t, 1, generated)
	item := gjson.GetBytes(writer.Body.Bytes(), "data.items.0")
	require.Equal(t, "failed", item.Get("status").String())
	require.Equal(t, "challenge_generation_failed", item.Get("reason").String())
	require.False(t, item.Get("http_status").Exists())
}

func TestCodexDiagnosticDefaultUsesModelTraceChallenge(t *testing.T) {
	calls := 0
	handler, client, writer := newCodexChallengeDiagnosticHandler(t, []string{"gpt-5.4"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		prompt := gjson.GetBytes(body, "input.4.content.0.text").String()
		match := regexp.MustCompile(` ([0-9]+) 个 1 到 355`).FindStringSubmatch(prompt)
		require.Len(t, match, 2)
		count, err := strconv.Atoi(match[1])
		require.NoError(t, err)
		require.GreaterOrEqual(t, count, 292)
		require.LessOrEqual(t, count, 332)
		_, err = fmt.Fprint(w, `{"status":"completed","output_text":"1"}`)
		require.NoError(t, err)
	}))
	handler.DiagnoseCodexModels(client)
	require.Equal(t, http.StatusOK, writer.Code)
	require.Equal(t, 1, calls)
	require.Equal(t, "insufficient_numbers", gjson.GetBytes(writer.Body.Bytes(), "data.items.0.reason").String())
}
