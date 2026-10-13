package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/MACOS-DO/sub4api/internal/pkg/logger"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// CodexSharedTicketState 是全局共享票的持久化视图：当前这一代票据，以及已对这一代失效的账号。
// 全站只有一张票，所有参与账号在所有门控模型上共用。失效按账号生效：账号 A 失效后只有 A
// 不再使用该代票，其他账号照常使用，直到新票全局替换。
// Version 随每次换票与失效递增，缓存只接受不低于已有版本的写入。
type CodexSharedTicketState struct {
	Ticket                json.RawMessage `json:"ticket,omitempty"`
	InvalidatedAccountIDs []int64         `json:"invalidated_account_ids,omitempty"`
	Version               int64           `json:"version"`
}

// CodexSharedTicketCache 是全局共享票的 Redis 缓存，同时提供按名称的分布式锁。
type CodexSharedTicketCache interface {
	// GetSharedTicket 的 bool 表示是否命中；无票也会以空 Ticket 的形式缓存，防止穿透。
	GetSharedTicket(ctx context.Context) (*CodexSharedTicketState, bool, error)
	// SetSharedTicket 在已缓存版本高于 state.Version 时不写入。
	SetSharedTicket(ctx context.Context, state *CodexSharedTicketState, ttl time.Duration) error
	DeleteSharedTicket(ctx context.Context) error
	TryLock(ctx context.Context, name, owner string, ttl time.Duration) (bool, error)
	// ExtendLock 仅在 owner 仍持有锁时续期。
	ExtendLock(ctx context.Context, name, owner string, ttl time.Duration) (bool, error)
	Unlock(ctx context.Context, name, owner string) error
}

const (
	// 进程内缓存服务调度、出站注入与管理端；过期后先返回旧值再后台刷新。
	codexSharedTicketLocalTTL    = time.Second
	codexSharedTicketRedisTTL    = 10 * time.Minute
	codexSharedTicketLockTTL     = 3 * time.Second
	codexSharedTicketLockWait    = time.Second
	codexSharedTicketLockPoll    = 50 * time.Millisecond
	codexSharedTicketLoadTimeout = 2 * time.Second

	// codexSharedTicketLoadLock 是回源锁名：Redis 未命中时只有持锁方查库并回写。
	codexSharedTicketLoadLock = "lock"
	// codexSharedTicketFlightKey 合并同一实例内的并发回源。
	codexSharedTicketFlightKey = "shared"
)

// codexSharedTicketView 是解析后的共享票状态。
type codexSharedTicketView struct {
	ticket      *openAICodexTicket
	invalidated map[int64]struct{}
	version     int64
}

type openAICodexSharedTicketEntry struct {
	view     *codexSharedTicketView
	loadedAt time.Time
}

func (v *codexSharedTicketView) current() *openAICodexTicket {
	if v == nil {
		return nil
	}
	return v.ticket
}

func (v *codexSharedTicketView) invalidatedFor(accountID int64) bool {
	if v == nil {
		return false
	}
	_, ok := v.invalidated[accountID]
	return ok
}

func (v *codexSharedTicketView) hasInvalidations() bool {
	return v != nil && len(v.invalidated) > 0
}

// usableFor 返回该账号可用的共享票：票据结构有效且该账号未对这一代失效。
func (v *codexSharedTicketView) usableFor(accountID int64) *openAICodexTicket {
	ticket := v.current()
	if !ticket.usable(time.Now(), 0, false, 0) || v.invalidatedFor(accountID) {
		return nil
	}
	return ticket
}

func parseCodexSharedTicketState(state *CodexSharedTicketState) (*codexSharedTicketView, error) {
	view := &codexSharedTicketView{}
	if state == nil {
		return view, nil
	}
	view.version = state.Version
	if len(state.Ticket) == 0 || string(state.Ticket) == "null" {
		return view, nil
	}
	var current openAICodexTicket
	if err := json.Unmarshal(state.Ticket, &current); err != nil {
		return nil, err
	}
	view.ticket = parseOpenAICodexTicketFromAny(current.AccountID, current.Model, current)
	if view.ticket != nil && len(state.InvalidatedAccountIDs) > 0 {
		view.invalidated = make(map[int64]struct{}, len(state.InvalidatedAccountIDs))
		for _, id := range state.InvalidatedAccountIDs {
			view.invalidated[id] = struct{}{}
		}
	}
	return view, nil
}

func (s *OpenAIGatewayService) SetCodexSharedTicketCache(cache CodexSharedTicketCache) {
	s.openaiCodexTicketCache = cache
}

// cacheSharedCodexTicketView 写入进程内缓存并返回生效的视图：已缓存的版本更高时保留它，
// 避免写库前发起的慢读取覆盖写后的新状态。
func (s *OpenAIGatewayService) cacheSharedCodexTicketView(view *codexSharedTicketView) *codexSharedTicketView {
	next := &openAICodexSharedTicketEntry{view: view, loadedAt: time.Now()}
	for {
		current := s.openaiCodexTicket.Load()
		if current != nil && current.view != nil && view != nil && current.view.version > view.version {
			return current.view
		}
		if s.openaiCodexTicket.CompareAndSwap(current, next) {
			return view
		}
	}
}

// sharedCodexTicketView 走三层读取（进程内 1 秒 → Redis → 数据库），供调度、打票判定与管理端使用。
// 本地值过期时先返回旧值、后台刷新，热路径只在进程内还没有值时才同步回源；回源失败按无票处理。
func (s *OpenAIGatewayService) sharedCodexTicketView() *codexSharedTicketView {
	view, err := s.sharedCodexTicketViewWithError()
	if err != nil {
		logger.L().Warn("openai_codex_ticket load shared ticket failed", zap.Error(err))
		return nil
	}
	return view
}

// sharedCodexTicketViewWithError 与 sharedCodexTicketView 相同，但进程内没有值且回源失败时返回错误，
// 供出站注入区分「查不到」与「没有票」。
func (s *OpenAIGatewayService) sharedCodexTicketViewWithError() (*codexSharedTicketView, error) {
	if s == nil {
		return nil, nil
	}
	if entry := s.openaiCodexTicket.Load(); entry != nil {
		if s.openaiCodexTicketLifecycle != nil && time.Since(entry.loadedAt) >= codexSharedTicketLocalTTL {
			s.refreshSharedCodexTicketAsync()
		}
		return entry.view, nil
	}
	if s.openaiCodexTicketLifecycle == nil {
		return nil, nil
	}
	return s.loadSharedCodexTicketView()
}

// refreshSharedCodexTicketAsync 在后台刷新本地缓存，同时只有一个刷新。
// 刷新失败时把旧值的读取时间顺延一个周期，避免故障期间每次读取都重新触发。
func (s *OpenAIGatewayService) refreshSharedCodexTicketAsync() {
	if !s.openaiCodexTicketRefreshing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.openaiCodexTicketRefreshing.Store(false)
		before := s.openaiCodexTicket.Load()
		_, err := s.reloadSharedCodexTicketView()
		if err == nil {
			return
		}
		logger.L().Warn("openai_codex_ticket background refresh failed; keeping last known value", zap.Error(err))
		if before != nil {
			s.openaiCodexTicket.CompareAndSwap(before, &openAICodexSharedTicketEntry{view: before.view, loadedAt: time.Now()})
		}
	}()
}

// loadSharedCodexTicketView 跳过进程内缓存，从 Redis（未命中再回源数据库）读取并刷新本地缓存。
// 读取失败时返回本地最后一次成功的值。
func (s *OpenAIGatewayService) loadSharedCodexTicketView() (*codexSharedTicketView, error) {
	view, err := s.reloadSharedCodexTicketView()
	if err != nil {
		if entry := s.openaiCodexTicket.Load(); entry != nil {
			logger.L().Warn("openai_codex_ticket load shared ticket failed; using last known value", zap.Error(err))
			return entry.view, nil
		}
		return nil, err
	}
	return view, nil
}

// reloadSharedCodexTicketView 从 Redis（未命中再回源数据库）读取并刷新本地缓存，失败时原样返回错误。
// 同一实例内的并发读取由 singleflight 合并。
func (s *OpenAIGatewayService) reloadSharedCodexTicketView() (*codexSharedTicketView, error) {
	value, err, _ := s.openaiCodexTicketFlight.Do(codexSharedTicketFlightKey, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), codexSharedTicketLoadTimeout)
		defer cancel()
		state, err := s.fetchSharedCodexTicketState(ctx)
		if err != nil {
			return nil, err
		}
		view, err := parseCodexSharedTicketState(state)
		if err != nil {
			return nil, err
		}
		return s.cacheSharedCodexTicketView(view), nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*codexSharedTicketView), nil
}

// fetchSharedCodexTicketState: Redis 命中直接返回；未命中时只有抢到锁的请求查库并回写，
// 其余请求轮询 Redis 等待回写，等待超时后自行查库兜底。Redis 不可用时直接查库。
func (s *OpenAIGatewayService) fetchSharedCodexTicketState(ctx context.Context) (*CodexSharedTicketState, error) {
	cache := s.openaiCodexTicketCache
	if cache == nil {
		return s.openaiCodexTicketLifecycle.CurrentTicket(ctx)
	}
	state, hit, err := cache.GetSharedTicket(ctx)
	if err != nil {
		logger.L().Warn("openai_codex_ticket redis read failed; reading database", zap.Error(err))
		return s.openaiCodexTicketLifecycle.CurrentTicket(ctx)
	}
	if hit {
		return state, nil
	}
	owner := uuid.NewString()
	locked, err := cache.TryLock(ctx, codexSharedTicketLoadLock, owner, codexSharedTicketLockTTL)
	if err != nil {
		logger.L().Warn("openai_codex_ticket redis lock failed; reading database", zap.Error(err))
		return s.openaiCodexTicketLifecycle.CurrentTicket(ctx)
	}
	if locked {
		defer func() {
			unlockCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = cache.Unlock(unlockCtx, codexSharedTicketLoadLock, owner)
		}()
		if state, hit, err := cache.GetSharedTicket(ctx); err == nil && hit {
			return state, nil
		}
		state, err := s.openaiCodexTicketLifecycle.CurrentTicket(ctx)
		if err != nil {
			return nil, err
		}
		if err := cache.SetSharedTicket(ctx, state, codexSharedTicketRedisTTL); err != nil {
			logger.L().Warn("openai_codex_ticket redis write failed", zap.Error(err))
		}
		return state, nil
	}
	deadline := time.Now().Add(codexSharedTicketLockWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(codexSharedTicketLockPoll):
		}
		if state, hit, err := cache.GetSharedTicket(ctx); err == nil && hit {
			return state, nil
		}
	}
	logger.L().Warn("openai_codex_ticket redis lock wait timed out; reading database")
	return s.openaiCodexTicketLifecycle.CurrentTicket(ctx)
}

// evictSharedCodexTicket 删除进程内与 Redis 缓存。
func (s *OpenAIGatewayService) evictSharedCodexTicket(ctx context.Context) {
	s.openaiCodexTicket.Store(nil)
	s.openaiCodexTicketFlight.Forget(codexSharedTicketFlightKey)
	if s.openaiCodexTicketCache == nil {
		return
	}
	if err := s.openaiCodexTicketCache.DeleteSharedTicket(ctx); err != nil {
		logger.L().Warn("openai_codex_ticket redis delete failed", zap.Error(err))
	}
}

// writeSharedCodexTicket 写库后立即重新读库，把新状态按版本写入进程内与 Redis 缓存。
// 版本比较保证写库前发起的回源即使更晚回写也会被拒绝；重新读取或写 Redis 失败时退回删除缓存。
// write 返回 changed=false 表示共享状态没有变化（如重复失效、旧一代失效），此时不重新发布。
func (s *OpenAIGatewayService) writeSharedCodexTicket(ctx context.Context, write func(context.Context) (bool, error)) error {
	changed, err := write(ctx)
	if err != nil {
		// 写入结果未知（可能已提交），清缓存让下一次读取回源。
		s.evictSharedCodexTicket(ctx)
		return err
	}
	if !changed {
		return nil
	}
	s.publishSharedCodexTicket(ctx)
	return nil
}

func (s *OpenAIGatewayService) publishSharedCodexTicket(ctx context.Context) {
	state, err := s.openaiCodexTicketLifecycle.CurrentTicket(ctx)
	if err != nil {
		logger.L().Warn("openai_codex_ticket reload after write failed; evicting cache", zap.Error(err))
		s.evictSharedCodexTicket(ctx)
		return
	}
	view, err := parseCodexSharedTicketState(state)
	if err != nil {
		s.evictSharedCodexTicket(ctx)
		return
	}
	s.cacheSharedCodexTicketView(view)
	if s.openaiCodexTicketCache == nil {
		return
	}
	if err := s.openaiCodexTicketCache.SetSharedTicket(ctx, state, codexSharedTicketRedisTTL); err != nil {
		logger.L().Warn("openai_codex_ticket redis write failed; evicting", zap.Error(err))
		if err := s.openaiCodexTicketCache.DeleteSharedTicket(ctx); err != nil {
			logger.L().Warn("openai_codex_ticket redis delete failed", zap.Error(err))
		}
	}
}
