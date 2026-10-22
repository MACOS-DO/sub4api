package service

import (
	"context"
	"errors"
	"strings"
)

const codexTicketProxySourceSettingKey = "openai_codex_ticket_proxy_source"

const (
	// CodexTicketProxySourcePool 打票出口从 Codex 打票代理池轮换（默认）。
	CodexTicketProxySourcePool = "pool"
	// CodexTicketProxySourceAccount 打票出口与账号业务请求一致：用账号绑定的代理，未绑定则直连。
	CodexTicketProxySourceAccount = "account"
)

type CodexTicketProxySource struct {
	Source string `json:"source"`
}

func normalizeCodexTicketProxySource(raw string) (string, error) {
	switch source := strings.TrimSpace(raw); source {
	case "", CodexTicketProxySourcePool:
		return CodexTicketProxySourcePool, nil
	case CodexTicketProxySourceAccount:
		return source, nil
	default:
		return "", errors.New("source must be pool or account")
	}
}

// GetCodexTicketProxySource 返回打票出口来源；未配置或读取失败时回退到代理池。
func (s *SettingService) GetCodexTicketProxySource(ctx context.Context) string {
	if s == nil || s.settingRepo == nil {
		return CodexTicketProxySourcePool
	}
	raw, err := s.settingRepo.GetValue(ctx, codexTicketProxySourceSettingKey)
	if err != nil {
		return CodexTicketProxySourcePool
	}
	source, err := normalizeCodexTicketProxySource(raw)
	if err != nil {
		return CodexTicketProxySourcePool
	}
	return source
}

func (s *SettingService) SetCodexTicketProxySource(ctx context.Context, raw string) (string, error) {
	if s == nil || s.settingRepo == nil {
		return "", ErrSettingNotFound
	}
	source, err := normalizeCodexTicketProxySource(raw)
	if err != nil {
		return "", err
	}
	if err := s.settingRepo.Set(ctx, codexTicketProxySourceSettingKey, source); err != nil {
		return "", err
	}
	return source, nil
}

// CodexTicketManualProxyAvailable 判断手动打票是否有可用出口：账号模式总有出口（代理或直连），
// 代理池模式需要池内至少一个可用代理。
func (s *SettingService) CodexTicketManualProxyAvailable(ctx context.Context) bool {
	if s.GetCodexTicketProxySource(ctx) == CodexTicketProxySourceAccount {
		return true
	}
	proxies, err := s.AvailableCodexTicketProxies(ctx)
	return err == nil && len(proxies) > 0
}
