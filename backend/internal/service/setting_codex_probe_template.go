package service

import (
	"context"
	"errors"
	"time"
)

type cachedCodexProbeTemplate struct {
	template  *CodexProbeTemplate
	err       error
	expiresAt time.Time
}

func (s *SettingService) GetCodexProbeTemplate(ctx context.Context) (*CodexProbeTemplate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.settingRepo == nil {
		return parsedDefaultCodexProbeTemplate()
	}
	// Serialize refresh and invalidation so an old in-flight read cannot restore
	// a stale template after a successful settings save.
	s.codexProbeTemplateMu.Lock()
	defer s.codexProbeTemplateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cache := s.codexProbeTemplateCache; cache != nil && time.Now().Before(cache.expiresAt) {
		return cache.template, cache.err
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := s.settingRepo.GetValue(readCtx, SettingKeyOpenAICodexTicketPromptTemplate)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return nil, errors.New("Codex probe template settings unavailable")
	}
	template, err := ParseCodexProbeTemplate(raw)
	s.codexProbeTemplateCache = &cachedCodexProbeTemplate{template: template, err: err, expiresAt: time.Now().Add(5 * time.Second)}
	return template, err
}

func (s *SettingService) InvalidateCodexProbeTemplateCache() {
	if s == nil {
		return
	}
	s.codexProbeTemplateMu.Lock()
	defer s.codexProbeTemplateMu.Unlock()
	s.codexProbeTemplateCache = nil
}
