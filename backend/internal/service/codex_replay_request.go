package service

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type codexReplayIdentity struct {
	installationID, sessionID, windowID, turnID string
	startedAt                                   time.Time
}

func newCodexReplayIdentity(installationID, sessionID, windowID string, now time.Time) (codexReplayIdentity, error) {
	identity := codexReplayIdentity{installationID: installationID, sessionID: sessionID, windowID: windowID, startedAt: now}
	for _, value := range []*string{&identity.sessionID, &identity.windowID, &identity.turnID} {
		if *value == "" {
			id, err := uuid.NewV7()
			if err != nil {
				return identity, err
			}
			*value = id.String()
		}
	}
	return identity, nil
}

// buildCodexReplayRequest is shared with the hypothesis runner. It has no
// filesystem, account, network or global client-identity side effects.
func buildCodexReplayRequest(replay *CodexHypothesisReplay, model, system string, identity codexReplayIdentity, render func(string) string) (map[string]any, http.Header) {
	metadata, _ := json.Marshal(map[string]any{
		"installation_id": identity.installationID, "session_id": identity.sessionID,
		"thread_id": identity.sessionID, "turn_id": identity.turnID, "window_id": identity.windowID,
		"turn_started_at_unix_ms": identity.startedAt.UnixMilli(), "request_kind": "turn",
	})
	input := make([]any, 0, len(replay.Messages)+1)
	for _, message := range replay.Messages {
		parts := make([]any, 0, len(message.Parts))
		for _, part := range message.Parts {
			parts = append(parts, map[string]string{"type": "input_text", "text": render(part)})
		}
		input = append(input, map[string]any{"role": message.Role, "content": parts})
	}
	if system != "" {
		last := input[len(input)-1]
		input[len(input)-1] = map[string]any{"role": "developer", "content": []any{map[string]string{"type": "input_text", "text": system}}}
		input = append(input, last)
	}
	body := map[string]any{
		"model": model, "store": false, "stream": true, "instructions": render(replay.Instructions), "input": input,
		"prompt_cache_key": identity.sessionID,
		"client_metadata": map[string]string{
			"session_id": identity.sessionID, "thread_id": identity.sessionID, "turn_id": identity.turnID,
			"x-codex-installation-id": identity.installationID, "x-codex-window-id": identity.windowID,
			"x-codex-turn-metadata": string(metadata),
		},
	}
	headers := http.Header{}
	for _, key := range []string{"session_id", "session-id", "thread-id", "x-client-request-id"} {
		headers.Set(key, identity.sessionID)
	}
	headers.Set("x-codex-installation-id", identity.installationID)
	headers.Set("x-codex-window-id", identity.windowID)
	headers.Set("x-codex-turn-metadata", string(metadata))
	return body, headers
}
