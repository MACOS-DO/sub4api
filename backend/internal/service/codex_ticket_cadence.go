package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const codexTicketCadenceSettingKey = "openai_codex_ticket_cadence"

type CodexTicketCadence struct {
	RetryMinSeconds int `json:"retry_min_seconds"`
	RetryMaxSeconds int `json:"retry_max_seconds"`
	RefreshSeconds  int `json:"refresh_seconds"`
}

type cachedCodexTicketCadence struct {
	value     CodexTicketCadence
	expiresAt time.Time
}

func validateCodexTicketCadence(value CodexTicketCadence) error {
	if value.RetryMinSeconds < 1 || value.RetryMinSeconds > 3600 || value.RetryMaxSeconds < value.RetryMinSeconds || value.RetryMaxSeconds > 3600 || value.RefreshSeconds < 0 || value.RefreshSeconds > 86400 {
		return errors.New("retry interval must be 1..3600 seconds with max >= min, refresh interval 0..86400 seconds")
	}
	return nil
}

func (s *SettingService) GetCodexTicketCadence(ctx context.Context, fallback CodexTicketCadence) (CodexTicketCadence, error) {
	if s == nil || s.settingRepo == nil {
		return fallback, nil
	}
	if cached, ok := s.codexTicketCadenceCache.Load().(*cachedCodexTicketCadence); ok && time.Now().Before(cached.expiresAt) {
		return cached.value, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, codexTicketCadenceSettingKey)
	if errors.Is(err, ErrSettingNotFound) {
		return fallback, nil
	}
	if err != nil {
		return fallback, err
	}
	var result CodexTicketCadence
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return fallback, err
	}
	if err := validateCodexTicketCadence(result); err != nil {
		return fallback, err
	}
	s.codexTicketCadenceCache.Store(&cachedCodexTicketCadence{value: result, expiresAt: time.Now().Add(10 * time.Second)})
	return result, nil
}

func (s *SettingService) SetCodexTicketCadence(ctx context.Context, value CodexTicketCadence) error {
	if s == nil || s.settingRepo == nil {
		return ErrSettingNotFound
	}
	if err := validateCodexTicketCadence(value); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := s.settingRepo.Set(ctx, codexTicketCadenceSettingKey, string(data)); err != nil {
		return err
	}
	s.codexTicketCadenceCache.Store(&cachedCodexTicketCadence{value: value, expiresAt: time.Now().Add(10 * time.Second)})
	return nil
}

func (s *OpenAIGatewayService) codexTicketCadence(ctx context.Context) CodexTicketCadence {
	cfg := s.openAICodexTicketConfig()
	fallback := CodexTicketCadence{RetryMinSeconds: cfg.HarvestRetryMinSeconds, RetryMaxSeconds: cfg.HarvestRetryMaxSeconds, RefreshSeconds: cfg.HarvestRefreshSeconds}
	if s.settingService == nil {
		return fallback
	}
	value, err := s.settingService.GetCodexTicketCadence(ctx, fallback)
	if err != nil {
		return fallback
	}
	return value
}

func (s *OpenAIGatewayService) CodexTicketCadence(ctx context.Context) CodexTicketCadence {
	return s.codexTicketCadence(ctx)
}
