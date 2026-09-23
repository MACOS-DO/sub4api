package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/google/uuid"
)

const maximumReplaySize = 8 << 20

var replayDeveloperParts = map[string]string{
	"## Memory":                  "历史记忆仅供参考，本次请求以当前输入为准。",
	"<skills_instructions>":      "可按需使用既有技能。",
	"<permissions instructions>": "在当前工作区权限范围内完成请求。",
	"<collaboration_mode>":       "按默认协作方式完成当前任务。",
	"<multi_agent_role>":         "你是当前对话的主执行者。",
	"<multi_agent_mode>":         "本次请求不启动子代理。",
}

type replayRecord struct {
	Type    string `json:"type"`
	Payload struct {
		Type             string `json:"type"`
		Role             string `json:"role"`
		Originator       string `json:"originator"`
		Timezone         string `json:"timezone"`
		Model            string `json:"model"`
		BaseInstructions struct {
			Text string `json:"text"`
		} `json:"base_instructions"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"payload"`
}

func loadSessionReplay(sessionPath, envPath string) (*service.CodexHypothesisReplay, error) {
	file, err := os.Open(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("open replay session: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > maximumReplaySize {
		return nil, errors.New("replay session is unreadable or too large")
	}
	replay := &service.CodexHypothesisReplay{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maximumReplaySize)
	seenParts := make(map[string]bool)
	var environmentCount, promptCount, timezoneCount int
	for scanner.Scan() {
		var record replayRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("decode replay session: %w", err)
		}
		switch record.Type {
		case "session_meta":
			if replay.Instructions != "" || record.Payload.Originator != "Codex Desktop" || strings.TrimSpace(record.Payload.BaseInstructions.Text) == "" {
				return nil, errors.New("replay requires one Codex Desktop base instruction")
			}
			replay.Instructions = record.Payload.BaseInstructions.Text
		case "turn_context":
			if record.Payload.Timezone == "America/Los_Angeles" {
				timezoneCount++
			}
			if replay.RecordedModel == "" {
				replay.RecordedModel = record.Payload.Model
			}
		case "response_item":
			if record.Payload.Type != "message" || (record.Payload.Role != "developer" && record.Payload.Role != "user") {
				continue
			}
			message := service.CodexHypothesisReplayMessage{Role: record.Payload.Role}
			for _, part := range record.Payload.Content {
				if part.Type != "input_text" {
					return nil, errors.New("unexpected replay message content")
				}
				text := part.Text
				if record.Payload.Role == "developer" {
					switch {
					case strings.HasPrefix(text, "<app-context>"):
						if seenParts["app"] {
							return nil, errors.New("duplicate replay app context")
						}
						seenParts["app"] = true
					default:
						matched := false
						for prefix, replacement := range replayDeveloperParts {
							if strings.HasPrefix(text, prefix) {
								if seenParts[prefix] {
									return nil, errors.New("duplicate replay developer template")
								}
								seenParts[prefix] = true
								text = replacement
								matched = true
								break
							}
						}
						if !matched {
							return nil, errors.New("unknown replay developer template")
						}
					}
				} else if strings.HasPrefix(text, "<environment_context>") {
					environmentCount++
					if !strings.Contains(text, "America/Los_Angeles") {
						return nil, errors.New("replay environment timezone not found")
					}
					text = strings.ReplaceAll(text, "America/Los_Angeles", "Asia/Singapore")
				} else if strings.Contains(text, "逐项选择 324 个") {
					if strings.Count(text, "逐项选择 324 个") != 1 {
						return nil, errors.New("replay challenge prompt appears more than once")
					}
					promptCount++
					text = service.CodexHypothesisPromptPlaceholder
				} else {
					return nil, errors.New("unexpected replay user message")
				}
				message.Parts = append(message.Parts, text)
			}
			if len(message.Parts) == 0 {
				return nil, errors.New("empty replay message")
			}
			replay.Messages = append(replay.Messages, message)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read replay session: %w", err)
	}
	if !seenParts["app"] || len(seenParts) != len(replayDeveloperParts)+1 || replay.Instructions == "" || environmentCount != 1 || promptCount != 1 || timezoneCount != 1 || replay.RecordedModel == "" {
		return nil, errors.New("incomplete Codex Desktop initial context")
	}
	if last := replay.Messages[len(replay.Messages)-1]; last.Role != "user" || len(last.Parts) != 1 || last.Parts[0] != service.CodexHypothesisPromptPlaceholder {
		return nil, errors.New("replay challenge must be the final user message")
	}
	installationID, err := loadReplayInstallationID(sessionPath, envPath)
	if err != nil {
		return nil, err
	}
	replay.InstallationID = installationID
	return replay, nil
}

func loadReplayInstallationID(sessionPath, envPath string) (string, error) {
	for directory := filepath.Dir(sessionPath); directory != filepath.Dir(directory); directory = filepath.Dir(directory) {
		if filepath.Base(directory) != ".codex" {
			continue
		}
		value, err := os.ReadFile(filepath.Join(directory, "installation_id"))
		if err == nil {
			return parseReplayInstallationID(value)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read Codex installation ID: %w", err)
		}
		break
	}
	path := filepath.Join(filepath.Dir(envPath), ".codex-installation.env")
	value, err := os.ReadFile(path)
	if err == nil {
		return parseReplayInstallationID(value)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read replay installation ID: %w", err)
	}
	generated, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate replay installation ID: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		value, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return parseReplayInstallationID(value)
	}
	if err != nil {
		return "", fmt.Errorf("create replay installation ID: %w", err)
	}
	_, writeErr := fmt.Fprintln(file, generated.String())
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return "", fmt.Errorf("save replay installation ID: %w", err)
	}
	return generated.String(), nil
}

func parseReplayInstallationID(value []byte) (string, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(string(value)))
	if err != nil || parsed.Version() != 4 {
		return "", errors.New("Codex installation ID must be UUIDv4")
	}
	return parsed.String(), nil
}
