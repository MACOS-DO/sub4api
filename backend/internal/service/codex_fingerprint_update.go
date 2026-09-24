package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"
)

const modelTraceBankSettingKey = "codex_modeltrace_bank_v1"
const modelTraceCommitEndpoint = "https://api.github.com/repos/xqy2006/ModelTrace/commits/main"
const modelTraceRawEndpoint = "https://raw.githubusercontent.com/xqy2006/ModelTrace/"

var modelTraceCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var modelTraceUpdateMu sync.Mutex

type modelTraceSavedBank struct {
	Commit string          `json:"commit"`
	Data   json.RawMessage `json:"data"`
}

func (s *SettingService) LoadModelTraceBank(ctx context.Context) error {
	if s == nil || s.settingRepo == nil {
		return ErrSettingNotFound
	}
	raw, err := s.settingRepo.GetValue(ctx, modelTraceBankSettingKey)
	if err != nil {
		return err
	}
	var saved modelTraceSavedBank
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		return err
	}
	if !modelTraceCommitPattern.MatchString(saved.Commit) {
		return errors.New("invalid saved fingerprint commit")
	}
	bank, err := parseModelTraceBank(saved.Data)
	if err != nil {
		return err
	}
	modelTraceActive.Store(&modelTraceVersion{bank: bank, commit: saved.Commit})
	return nil
}

func (s *SettingService) RefreshModelTraceBank(ctx context.Context) (string, error) {
	if s == nil || s.settingRepo == nil {
		return "", ErrSettingNotFound
	}
	modelTraceUpdateMu.Lock()
	defer modelTraceUpdateMu.Unlock()
	client := &http.Client{Timeout: 20 * time.Second}
	fetch := func(url string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		response, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fingerprint source HTTP %d", response.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		if err != nil {
			return nil, err
		}
		if len(data) >= 4<<20 {
			return nil, errors.New("fingerprint source exceeds limit")
		}
		return data, nil
	}
	metadata, err := fetch(modelTraceCommitEndpoint)
	if err != nil {
		return "", err
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(metadata, &commit); err != nil {
		return "", err
	}
	if !modelTraceCommitPattern.MatchString(commit.SHA) {
		return "", errors.New("invalid fingerprint source commit")
	}
	current, err := currentModelTraceBank()
	if err == nil && current.commit == commit.SHA {
		return current.commit, nil
	}
	data, err := fetch(modelTraceRawEndpoint + commit.SHA + "/data/unified_bank.json")
	if err != nil {
		return "", err
	}
	bank, err := parseModelTraceBank(data)
	if err != nil {
		return "", err
	}
	saved, err := json.Marshal(modelTraceSavedBank{Commit: commit.SHA, Data: data})
	if err != nil {
		return "", err
	}
	if err := s.settingRepo.Set(ctx, modelTraceBankSettingKey, string(saved)); err != nil {
		return "", err
	}
	modelTraceActive.Store(&modelTraceVersion{bank: bank, commit: commit.SHA})
	return commit.SHA, nil
}
