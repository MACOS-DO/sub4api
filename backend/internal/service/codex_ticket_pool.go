package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

const codexTicketPoolSettingKey = "openai_codex_ticket_proxy_pool"

type CodexTicketPool struct {
	Mode     string  `json:"mode"`
	ProxyIDs []int64 `json:"proxy_ids"`
}

func (s *SettingService) GetCodexTicketPool(ctx context.Context) (CodexTicketPool, error) {
	pool := CodexTicketPool{Mode: "all", ProxyIDs: []int64{}}
	if s == nil || s.settingRepo == nil {
		return pool, errors.New("ticket proxy pool settings unavailable")
	}
	raw, err := s.settingRepo.GetValue(ctx, codexTicketPoolSettingKey)
	if errors.Is(err, ErrSettingNotFound) {
		return pool, nil
	}
	if err != nil {
		return pool, err
	}
	if err := json.Unmarshal([]byte(raw), &pool); err != nil {
		return pool, fmt.Errorf("decode ticket proxy pool: %w", err)
	}
	if pool.Mode != "all" && pool.Mode != "custom" {
		return pool, errors.New("invalid ticket proxy pool mode")
	}
	if pool.ProxyIDs == nil {
		pool.ProxyIDs = []int64{}
	}
	return pool, nil
}

func (s *SettingService) SetCodexTicketPool(ctx context.Context, pool CodexTicketPool) error {
	if s == nil || s.settingRepo == nil || s.proxyRepo == nil {
		return errors.New("ticket proxy pool unavailable")
	}
	if pool.Mode != "all" && pool.Mode != "custom" {
		return errors.New("mode must be all or custom")
	}
	if pool.Mode == "all" {
		pool.ProxyIDs = []int64{}
	}
	if pool.Mode == "custom" {
		if len(pool.ProxyIDs) == 0 {
			return errors.New("select at least one proxy")
		}
		seen := make(map[int64]bool, len(pool.ProxyIDs))
		for _, id := range pool.ProxyIDs {
			if id <= 0 || seen[id] {
				return errors.New("invalid or duplicate proxy ID")
			}
			seen[id] = true
		}
		found, err := s.proxyRepo.ListByIDs(ctx, pool.ProxyIDs)
		if err != nil {
			return err
		}
		if len(found) != len(pool.ProxyIDs) {
			return errors.New("proxy not found")
		}
		sort.Slice(pool.ProxyIDs, func(i, j int) bool { return pool.ProxyIDs[i] < pool.ProxyIDs[j] })
	}
	data, err := json.Marshal(pool)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, codexTicketPoolSettingKey, string(data))
}

func (s *SettingService) AvailableCodexTicketProxies(ctx context.Context) ([]Proxy, error) {
	if s == nil || s.proxyRepo == nil {
		return nil, errors.New("ticket proxy repository unavailable")
	}
	pool, err := s.GetCodexTicketPool(ctx)
	if err != nil {
		return nil, err
	}
	proxies, err := s.proxyRepo.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	selected := make(map[int64]bool, len(pool.ProxyIDs))
	for _, id := range pool.ProxyIDs {
		selected[id] = true
	}
	available := make([]Proxy, 0, len(proxies))
	now := time.Now()
	for _, proxy := range proxies {
		if !proxy.IsExpired(now) && (pool.Mode == "all" || selected[proxy.ID]) {
			available = append(available, proxy)
		}
	}
	sort.Slice(available, func(i, j int) bool { return available[i].ID < available[j].ID })
	return available, nil
}

type CodexTicketPoolProxyRef struct {
	ID     int64  `json:"id"`
	Name   string `json:"name,omitempty"`
	Reason string `json:"reason,omitempty"` // deleted | disabled | expired
}

// CodexTicketPoolView 是代理池的已保存配置加上「此刻实际生效」的结果，供后台核对：
// Effective 是打票会轮换的代理；Unavailable 是已勾选但当前不会被使用的代理。
type CodexTicketPoolView struct {
	CodexTicketPool
	Source      string                    `json:"source"`
	Effective   []CodexTicketPoolProxyRef `json:"effective"`
	Unavailable []CodexTicketPoolProxyRef `json:"unavailable"`
}

func (s *SettingService) DescribeCodexTicketPool(ctx context.Context) (CodexTicketPoolView, error) {
	pool, err := s.GetCodexTicketPool(ctx)
	if err != nil {
		return CodexTicketPoolView{}, err
	}
	view := CodexTicketPoolView{CodexTicketPool: pool, Source: s.GetCodexTicketProxySource(ctx),
		Effective: []CodexTicketPoolProxyRef{}, Unavailable: []CodexTicketPoolProxyRef{}}
	available, err := s.AvailableCodexTicketProxies(ctx)
	if err != nil {
		return view, err
	}
	effective := make(map[int64]bool, len(available))
	for _, proxy := range available {
		effective[proxy.ID] = true
		view.Effective = append(view.Effective, CodexTicketPoolProxyRef{ID: proxy.ID, Name: proxy.Name})
	}
	if pool.Mode != "custom" {
		return view, nil
	}
	found, err := s.proxyRepo.ListByIDs(ctx, pool.ProxyIDs)
	if err != nil {
		return view, err
	}
	byID := make(map[int64]Proxy, len(found))
	for _, proxy := range found {
		byID[proxy.ID] = proxy
	}
	now := time.Now()
	for _, id := range pool.ProxyIDs {
		if effective[id] {
			continue
		}
		ref := CodexTicketPoolProxyRef{ID: id, Reason: "deleted"}
		if proxy, ok := byID[id]; ok {
			ref.Name = proxy.Name
			switch {
			case !proxy.IsActive():
				ref.Reason = "disabled"
			case proxy.IsExpired(now):
				ref.Reason = "expired"
			}
		}
		view.Unavailable = append(view.Unavailable, ref)
	}
	return view, nil
}
