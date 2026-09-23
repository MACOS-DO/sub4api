package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func replayFixture(t *testing.T, root string, installation bool) (string, string) {
	t.Helper()
	directory := filepath.Join(root, ".codex", "sessions", "2026", "09", "23")
	require.NoError(t, os.MkdirAll(directory, 0700))
	if installation {
		require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "installation_id"), []byte("88b082f2-973a-4af1-a14c-88392661f6b3\n"), 0600))
	}
	path := filepath.Join(directory, "record.jsonl")
	items := []any{
		map[string]any{"type": "session_meta", "payload": map[string]any{"originator": "Codex Desktop", "base_instructions": map[string]any{"text": "fixed system"}}},
		map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "developer", "content": []any{
			map[string]string{"type": "input_text", "text": "<app-context>fixed application</app-context>"},
			map[string]string{"type": "input_text", "text": "## Memory\nlarge memory"},
			map[string]string{"type": "input_text", "text": "<skills_instructions>large skills</skills_instructions>"},
			map[string]string{"type": "input_text", "text": "<permissions instructions>large permissions</permissions instructions>"},
			map[string]string{"type": "input_text", "text": "<collaboration_mode>large collaboration</collaboration_mode>"},
		}}},
		map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "developer", "content": []any{map[string]string{"type": "input_text", "text": "<multi_agent_role>large role</multi_agent_role>"}}}},
		map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "developer", "content": []any{map[string]string{"type": "input_text", "text": "<multi_agent_mode>large mode</multi_agent_mode>"}}}},
		map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": "<environment_context>timezone America/Los_Angeles</environment_context>"}}}},
		map[string]any{"type": "turn_context", "payload": map[string]string{"timezone": "America/Los_Angeles", "model": "gpt-6-astra"}},
		map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": "逐项选择 324 个整数。"}}}},
		map[string]any{"type": "response_item", "payload": map[string]any{"type": "reasoning", "summary": "never replay reasoning"}},
		map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": "never replay answer"}}}},
	}
	var lines strings.Builder
	for _, item := range items {
		encoded, err := json.Marshal(item)
		require.NoError(t, err)
		lines.Write(encoded)
		lines.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(path, []byte(lines.String()), 0600))
	return path, lines.String()
}

func TestCodexHypothesisReplayPreservesInitialRolesOnly(t *testing.T) {
	root := t.TempDir()
	path, source := replayFixture(t, root, true)
	replay, err := loadSessionReplay(path, filepath.Join(root, "experiment.env"))
	require.NoError(t, err)
	require.Equal(t, "fixed system", replay.Instructions)
	require.Equal(t, "gpt-6-astra", replay.RecordedModel)
	require.Equal(t, "88b082f2-973a-4af1-a14c-88392661f6b3", replay.InstallationID)
	require.Len(t, replay.Messages, 5)
	for index, role := range []string{"developer", "developer", "developer", "user", "user"} {
		require.Equal(t, role, replay.Messages[index].Role)
	}
	require.Len(t, replay.Messages[0].Parts, 5)
	require.Equal(t, "<app-context>fixed application</app-context>", replay.Messages[0].Parts[0])
	for _, part := range replay.Messages[0].Parts[1:] {
		require.Equal(t, 1, strings.Count(part, "。"))
		require.NotContains(t, part, "large")
	}
	require.Contains(t, replay.Messages[3].Parts[0], "Asia/Singapore")
	require.Equal(t, service.CodexHypothesisPromptPlaceholder, replay.Messages[4].Parts[0])
	encoded, err := json.Marshal(replay.Messages)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "never replay")
	require.NotContains(t, string(encoded), "逐项选择 324 个")
	unchanged, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, source, string(unchanged))
}

func TestCodexHypothesisReplayRejectsMissingOrDuplicatePrompt(t *testing.T) {
	root := t.TempDir()
	path, original := replayFixture(t, root, true)
	envPath := filepath.Join(root, "experiment.env")
	for _, change := range []struct{ name, text string }{
		{"missing", strings.Replace(original, "逐项选择 324 个", "无关内容", 1)},
		{"duplicate", strings.Replace(original, "逐项选择 324 个", "逐项选择 324 个，逐项选择 324 个", 1)},
		{"wrong timezone", strings.Replace(original, "America/Los_Angeles", "Europe/London", 1)},
	} {
		t.Run(change.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(path, []byte(change.text), 0600))
			_, err := loadSessionReplay(path, envPath)
			require.Error(t, err)
		})
	}
}

func TestCodexHypothesisReplayPersistsFallbackInstallationID(t *testing.T) {
	root := t.TempDir()
	path, _ := replayFixture(t, root, false)
	envPath := filepath.Join(root, "experiment.env")
	first, err := loadSessionReplay(path, envPath)
	require.NoError(t, err)
	second, err := loadSessionReplay(path, envPath)
	require.NoError(t, err)
	require.Equal(t, first.InstallationID, second.InstallationID)
	identifier, err := uuid.Parse(first.InstallationID)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(4), identifier.Version())
	info, err := os.Stat(filepath.Join(root, ".codex-installation.env"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}
