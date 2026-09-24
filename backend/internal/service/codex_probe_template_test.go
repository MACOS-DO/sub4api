package service

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexProbeDefaultStructureAndPrivacy(t *testing.T) {
	raw := DefaultCodexProbeTemplate()
	template, err := ParseCodexProbeTemplate(raw)
	require.NoError(t, err)
	require.Len(t, strings.Split(strings.TrimSpace(raw), "\n"), 7)
	for _, char := range raw {
		require.Less(t, char, rune(128), "default fixed instructions must be English")
	}
	require.NotRegexp(t, regexp.MustCompile(`[A-Za-z]:[\\/]|/(?:mnt|home|Users)/|[\w.+-]+@[\w.-]+\.[A-Za-z]{2,}|[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}`), raw)
	require.Len(t, template.replay.Messages, 5)
	for i, role := range []string{"developer", "developer", "developer", "user", "user"} {
		require.Equal(t, role, template.replay.Messages[i].Role)
		require.Len(t, template.replay.Messages[i].Parts, []int{5, 1, 1, 1, 1}[i])
	}
	decoder := xml.NewDecoder(strings.NewReader(template.replay.Messages[3].Parts[0]))
	counts := map[string]int{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if start, ok := token.(xml.StartElement); ok {
			counts[start.Name.Local]++
		}
	}
	require.Equal(t, 3, counts["root"])
	require.Equal(t, 18, counts["entry"])
	require.Contains(t, template.replay.Messages[0].Parts[3], "<permissions instructions>")
}

func TestCodexProbeTemplateRejectsInvalidOrPrivateRecords(t *testing.T) {
	good := DefaultCodexProbeTemplate()
	for name, raw := range map[string]string{
		"malformed":             "{private-secret",
		"too large":             strings.Repeat(" ", CodexProbeTemplateMaxBytes+1),
		"recording metadata":    strings.Replace(good, `"originator":`, `"cwd":"/home/private-user","originator":`, 1),
		"assistant":             strings.Replace(good, `"role":"developer"`, `"role":"assistant"`, 1),
		"output text":           strings.Replace(good, `"input_text"`, `"output_text"`, 1),
		"unknown placeholder":   strings.Replace(good, "anonymous workspace", "{{PRIVATE_VALUE}}", 1),
		"malformed placeholder": strings.Replace(good, "anonymous workspace", "{{PRIVATE_VALUE", 1),
		"duplicate prompt":      strings.Replace(good, "anonymous workspace", CodexHypothesisPromptPlaceholder, 1),
		"missing prompt":        strings.ReplaceAll(good, CodexHypothesisPromptPlaceholder, "private-secret"),
		"fixed timezone":        strings.ReplaceAll(good, "{{TIMEZONE}}", "Europe/London"),
		"missing tag":           strings.Replace(good, "</skills_instructions>", "", 1),
		"changed tree":          strings.Replace(good, "<root>/workspace</root>", "", 1),
		"extra event":           good + `{"type":"response_item","payload":{"type":"reasoning","encrypted_content":"private-secret"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseCodexProbeTemplate(raw)
			require.ErrorIs(t, err, ErrCodexProbeTemplateInvalid)
			require.Contains(t, err.Error(), "line ")
			require.NotContains(t, err.Error(), "private-secret")
			require.NotContains(t, err.Error(), "private-user")
		})
	}
}

func TestCodexProbeRenderingTimezoneAndEscaping(t *testing.T) {
	template, err := ParseCodexProbeTemplate(strings.Replace(DefaultCodexProbeTemplate(), "You are Codex, an AI assistant.", "Target: {{MODEL}}.", 1))
	require.NoError(t, err)
	identity, err := newCodexReplayIdentity(uuid.NewString(), "", "", time.Date(2026, 9, 24, 0, 30, 0, 0, time.UTC))
	require.NoError(t, err)
	prompt := "quoted \"value\"\n中文 \\ {{TIMEZONE}}"
	for zone, date := range map[string]string{"America/Los_Angeles": "2026-09-23", "Asia/Singapore": "2026-09-24"} {
		body, headers, err := template.render("gpt-6-astra", prompt, zone, identity)
		require.NoError(t, err)
		require.True(t, json.Valid(body))
		require.Equal(t, prompt, gjson.GetBytes(body, "input.4.content.0.text").String(), "replacement values must not be expanded recursively")
		env := gjson.GetBytes(body, "input.3.content.0.text").String()
		require.Contains(t, env, "<timezone>"+zone+"</timezone>")
		require.Contains(t, env, "<current_date>"+date+"</current_date>")
		require.Contains(t, gjson.GetBytes(body, "instructions").String(), "Target: gpt-6-astra.")
		assertCodexProbeIdentity(t, body, headers)
	}
	require.Contains(t, template.replay.Messages[3].Parts[0], "{{TIMEZONE}}", "cached template must remain immutable")
}

func assertCodexProbeIdentity(t *testing.T, body []byte, headers http.Header) {
	t.Helper()
	session := headers.Get("session_id")
	for _, name := range []string{"session-id", "thread-id", "x-client-request-id"} {
		require.Equal(t, session, headers.Get(name))
	}
	for _, path := range []string{"prompt_cache_key", "client_metadata.session_id", "client_metadata.thread_id"} {
		require.Equal(t, session, gjson.GetBytes(body, path).String())
	}
	metadata := headers.Get("x-codex-turn-metadata")
	require.Equal(t, metadata, gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String())
	require.Equal(t, session, gjson.Get(metadata, "session_id").String())
	require.Equal(t, session, gjson.Get(metadata, "thread_id").String())
	for _, pair := range [][2]string{{"installation_id", "x-codex-installation-id"}, {"window_id", "x-codex-window-id"}} {
		require.Equal(t, headers.Get(pair[1]), gjson.Get(metadata, pair[0]).String())
		require.Equal(t, headers.Get(pair[1]), gjson.GetBytes(body, "client_metadata."+pair[1]).String())
	}
	require.Equal(t, gjson.Get(metadata, "turn_id").String(), gjson.GetBytes(body, "client_metadata.turn_id").String())
	require.Positive(t, gjson.Get(metadata, "turn_started_at_unix_ms").Int())
	require.Equal(t, "turn", gjson.Get(metadata, "request_kind").String())
	for _, value := range []string{session, headers.Get("x-codex-window-id"), gjson.Get(metadata, "turn_id").String()} {
		id, err := uuid.Parse(value)
		require.NoError(t, err)
		require.Equal(t, uuid.Version(7), id.Version())
	}
	id, err := uuid.Parse(headers.Get("x-codex-installation-id"))
	require.NoError(t, err)
	require.Equal(t, uuid.Version(4), id.Version())
}

func TestCodexProbeAccountIdentityAndDefaultTimezone(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := ticketTestAccount(41)
	challenge := ModelTraceChallenge{Prompt: "test prompt", ExpectedCount: 292}
	body, first, err := svc.buildCodexProbeRequest(context.Background(), account, "gpt-6-astra", challenge)
	require.NoError(t, err)
	assertCodexProbeIdentity(t, body, first)
	require.Contains(t, gjson.GetBytes(body, "input.3.content.0.text").String(), "<timezone>Asia/Singapore</timezone>")
	_, second, err := svc.buildCodexProbeRequest(context.Background(), account, "gpt-5.6-sol", challenge)
	require.NoError(t, err)
	require.Equal(t, first.Get("x-codex-installation-id"), second.Get("x-codex-installation-id"))
	require.NotEqual(t, first.Get("session_id"), second.Get("session_id"))
	require.NotEqual(t, first.Get("x-codex-window-id"), second.Get("x-codex-window-id"))
	account.Credentials["chatgpt_account_id"] = "another-test-account"
	_, third, err := svc.buildCodexProbeRequest(context.Background(), account, "gpt-6-astra", challenge)
	require.NoError(t, err)
	require.NotEqual(t, first.Get("x-codex-installation-id"), third.Get("x-codex-installation-id"))
	account.Credentials = nil
	_, _, err = svc.buildCodexProbeRequest(context.Background(), account, "gpt-6-astra", challenge)
	require.ErrorIs(t, err, ErrCodexProbeIdentity)
}

func TestCodexProbeSnapshotAndCacheRefresh(t *testing.T) {
	repo := &codexPolicyMigrationRepoStub{values: map[string]string{}}
	settings := &SettingService{settingRepo: repo}
	svc := &OpenAIGatewayService{settingService: settings}
	pinned, err := svc.PrepareCodexProbeContext(context.Background())
	require.NoError(t, err)
	custom := strings.Replace(DefaultCodexProbeTemplate(), "anonymous workspace", "custom workspace", 1)
	repo.values[SettingKeyOpenAICodexTicketPromptTemplate] = custom
	// A second process's write becomes visible after the local five-second TTL.
	settings.codexProbeTemplateCache.expiresAt = time.Now().Add(-time.Second)
	fresh, err := svc.PrepareCodexProbeContext(context.Background())
	require.NoError(t, err)
	for _, test := range []struct {
		ctx  context.Context
		want string
	}{{pinned, "anonymous workspace"}, {fresh, "custom workspace"}} {
		body, _, err := svc.buildCodexProbeRequest(test.ctx, ticketTestAccount(41), "gpt-6-astra", ModelTraceChallenge{Prompt: "fresh challenge"})
		require.NoError(t, err)
		require.Contains(t, string(body), test.want)
	}
	repo.values[SettingKeyOpenAICodexTicketPromptTemplate] = "invalid"
	settings.InvalidateCodexProbeTemplateCache()
	_, err = svc.PrepareCodexProbeContext(context.Background())
	require.ErrorIs(t, err, ErrCodexProbeTemplateInvalid)
	// The earlier model still uses its valid snapshot even after a configuration change.
	_, _, err = svc.buildCodexProbeRequest(pinned, ticketTestAccount(41), "gpt-6-astra", ModelTraceChallenge{Prompt: "next request"})
	require.NoError(t, err)
	repo.values[SettingKeyOpenAICodexTicketPromptTemplate] = ""
	settings.InvalidateCodexProbeTemplateCache()
	_, err = svc.PrepareCodexProbeContext(context.Background())
	require.NoError(t, err)
}

func TestCodexProbeInvalidTemplateDoesNotHarvestOrBlockOrdinaryRequests(t *testing.T) {
	svc, account, history := challengeHarvestService(t, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid template must not reach upstream")
		return nil, nil
	}})
	svc.settingService.settingRepo = &codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAICodexTicketPromptTemplate: "invalid"}}
	result, err := svc.runCodexTicketAttempt(context.Background(), account, "gpt-6-astra", "automatic")
	require.NoError(t, err)
	require.Equal(t, "error", result.Outcome)
	require.Equal(t, "template_invalid", result.ReasonCode)
	require.Nil(t, result.HTTPStatus)
	require.Len(t, history.attempts, 1)
	require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", http.Header{}))
}

func TestCodexTicketAllowWithoutTicketPrecedenceAndInjection(t *testing.T) {
	for _, test := range []struct {
		name       string
		failClosed bool
		global     string
		account    *bool
		allow      bool
	}{
		{"default allow", false, "", nil, true},
		{"explicit config deny", true, "", nil, false},
		{"saved global deny", false, "false", nil, false},
		{"saved global allow", true, "true", nil, true},
		{"account deny", false, "true", new(false), false},
		{"account allow", true, "false", new(true), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAICodexTicketAllowWithoutTicket: test.global}}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: test.failClosed}, nil)
			svc.settingService = &SettingService{settingRepo: repo}
			account := ticketTestAccount(41)
			account.Extra = map[string]any{}
			if test.account != nil {
				account.Extra["codex_allow_without_ticket"] = *test.account
			}
			require.Equal(t, !test.allow, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
			err := svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", http.Header{})
			require.Equal(t, !test.allow, errors.Is(err, ErrOpenAICodexTicketUnavailable))
			account.Extra[openAICodexTicketExtraKey("gpt-6-astra")] = verifiedTicket(account, "gpt-6-astra", "test-ticket", "session=test")
			headers := http.Header{}
			require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", headers))
			require.Equal(t, "test-ticket", headers.Get(openAICodexTurnStateHeader))
			require.Equal(t, "session=test", headers.Get("Cookie"))
			require.Equal(t, test.global, repo.values[SettingKeyOpenAICodexTicketAllowWithoutTicket])
		})
	}
}
