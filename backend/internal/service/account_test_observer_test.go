package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccountTestObserverPreservesEveryResponse(t *testing.T) {
	var events []AccountTestUpstreamResponse
	ctx := withAccountTestObserver(context.Background(), func(event AccountTestUpstreamResponse) { events = append(events, event) })
	req, err := http.NewRequestWithContext(ctx, "POST", "https://example.com/v1/responses?key=private", nil)
	require.NoError(t, err)
	headers := http.Header{"Set-Cookie": {"a=1", "b=2"}, "X-Long": {strings.Repeat("x", 16000)}, "X-Empty": {""}}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: 429, Header: headers},
		{StatusCode: 200, Header: http.Header{"X-Request-Id": {"retry"}}},
	}}
	_, err = upstream.Do(req, "", 1, 1)
	require.NoError(t, err)
	_, err = upstream.DoWithTLS(req, "", 1, 1, nil)
	require.NoError(t, err)
	observeAccountTestResponse(ctx, "GET", "websocket", 101, http.Header{"Upgrade": {"websocket"}})
	require.Len(t, events, 3)
	for i, event := range events {
		require.Equal(t, i+1, event.Sequence)
	}
	require.Equal(t, 429, events[0].StatusCode)
	require.Equal(t, headers, events[0].Headers)
	require.Equal(t, "/v1/responses", events[0].Stage)
	headers.Set("Set-Cookie", "changed")
	require.Equal(t, []string{"a=1", "b=2"}, events[0].Headers["Set-Cookie"])
	require.Equal(t, 101, events[2].StatusCode)
	failed := &httpUpstreamRecorder{err: errors.New("connection failed")}
	_, err = failed.Do(req, "", 1, 1)
	require.Error(t, err)
	require.Len(t, events, 3, "network errors without a response must not invent headers")
}

func TestAccountTestPayloadCustomAndDefaultPrompts(t *testing.T) {
	for _, prompt := range []string{"", " \n", "请解释这个函数\n第二行"} {
		expected := accountTestPrompt("hi", prompt)
		claude, err := createTestPayload("claude", prompt)
		require.NoError(t, err)
		body, err := json.Marshal(claude)
		require.NoError(t, err)
		require.Equal(t, expected, gjson.GetBytes(body, "messages.0.content.0.text").String())
		body, err = json.Marshal(createOpenAITestPayload("gpt", true, prompt))
		require.NoError(t, err)
		require.Equal(t, expected, gjson.GetBytes(body, "input.0.content.0.text").String())
		body = createGeminiTestPayload("gemini-text", prompt)
		require.Equal(t, expected, gjson.GetBytes(body, "contents.0.parts.0.text").String())
		body, err = buildGrokQuotaProbeBody("grok", prompt)
		require.NoError(t, err)
		require.Equal(t, accountTestPrompt(grokQuotaProbeInput, prompt), gjson.GetBytes(body, "input").String())
		body, err = json.Marshal(createOpenAICompactProbePayload("gpt", true, prompt))
		require.NoError(t, err)
		require.Equal(t, accountTestPrompt("Respond with OK.", prompt), gjson.GetBytes(body, "input.0.content").String())
		svc := &AntigravityGatewayService{}
		body, err = svc.buildGeminiTestRequest("project", "gemini", prompt)
		require.NoError(t, err)
		require.Equal(t, accountTestPrompt(".", prompt), gjson.GetBytes(body, "request.contents.0.parts.0.text").String())
	}
}
