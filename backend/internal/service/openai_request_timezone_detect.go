package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/MACOS-DO/sub4api/internal/pkg/errors"
)

const (
	openAIRequestTimezoneModeExtraKey     = "openai_request_timezone_mode"
	openAIRequestTimezoneDetectedExtraKey = "openai_request_timezone_detected"

	// OpenAIRequestTimezoneModeFixed 使用账号上手选的固定时区（缺省值，存量账号均为此模式）。
	OpenAIRequestTimezoneModeFixed = "fixed"
	// OpenAIRequestTimezoneModeFollowProxy 时区跟随账号出口 IP：修改代理后检测出口所在地时区并写回账号。
	OpenAIRequestTimezoneModeFollowProxy = "follow_proxy"

	openAIRequestTimezoneDetectTimeout = 30 * time.Second
)

// OpenAIRequestTimezoneDetection 是最近一次出口时区检测的结果，存于 extra.openai_request_timezone_detected。
// 检测失败时 Error 非空，Timezone 为空，账号继续沿用上一次成功写入的 openai_request_timezone。
type OpenAIRequestTimezoneDetection struct {
	IP         string `json:"ip,omitempty"`
	Country    string `json:"country,omitempty"`
	Region     string `json:"region,omitempty"`
	City       string `json:"city,omitempty"`
	Timezone   string `json:"timezone,omitempty"`
	ProxyID    *int64 `json:"proxy_id,omitempty"`
	DetectedAt string `json:"detected_at"`
	Error      string `json:"error,omitempty"`
}

var openAIRequestTimezoneDetecting sync.Map

func (account *Account) OpenAIRequestTimezoneMode() string {
	if account != nil && account.getExtraString(openAIRequestTimezoneModeExtraKey) == OpenAIRequestTimezoneModeFollowProxy {
		return OpenAIRequestTimezoneModeFollowProxy
	}
	return OpenAIRequestTimezoneModeFixed
}

func (account *Account) followsProxyRequestTimezone() bool {
	return account != nil && account.IsOpenAI() && !account.IsShadow() &&
		account.OpenAIRequestTimezoneMode() == OpenAIRequestTimezoneModeFollowProxy
}

func validateOpenAIRequestTimezoneModeExtra(platform string, extra map[string]any) error {
	raw, exists := extra[openAIRequestTimezoneModeExtraKey]
	if !exists || raw == nil {
		return nil
	}
	mode, ok := raw.(string)
	if !ok || (mode != "" && mode != OpenAIRequestTimezoneModeFixed && mode != OpenAIRequestTimezoneModeFollowProxy) ||
		(mode == OpenAIRequestTimezoneModeFollowProxy && platform != PlatformOpenAI) {
		return infraerrors.New(http.StatusBadRequest, "INVALID_OPENAI_REQUEST_TIMEZONE_MODE", "openai_request_timezone_mode must be fixed or follow_proxy for an OpenAI account")
	}
	return nil
}

// preserveOpenAIRequestTimezoneDetection 让检测结果只由后端写入：编辑表单回传的旧检测记录被丢弃；
// 跟随模式下表单回传的时区也被丢弃，保留库里最新检测到的时区，避免把刚写入的结果覆盖回旧值。
func preserveOpenAIRequestTimezoneDetection(current, next map[string]any) {
	if next == nil {
		return
	}
	delete(next, openAIRequestTimezoneDetectedExtraKey)
	if value, ok := current[openAIRequestTimezoneDetectedExtraKey]; ok {
		next[openAIRequestTimezoneDetectedExtraKey] = value
	}
	if mode, _ := next[openAIRequestTimezoneModeExtraKey].(string); mode == OpenAIRequestTimezoneModeFollowProxy {
		delete(next, openAIRequestTimezoneExtraKey)
		if value, ok := current[openAIRequestTimezoneExtraKey]; ok {
			next[openAIRequestTimezoneExtraKey] = value
		}
	}
}

func openAIRequestTimezoneProxyKey(proxyID *int64) int64 {
	if proxyID == nil {
		return 0
	}
	return *proxyID
}

func sameProxyID(a, b *int64) bool {
	return openAIRequestTimezoneProxyKey(a) == openAIRequestTimezoneProxyKey(b)
}

func (s *adminServiceImpl) probeOpenAIRequestTimezone(ctx context.Context, account *Account) OpenAIRequestTimezoneDetection {
	result := OpenAIRequestTimezoneDetection{DetectedAt: time.Now().UTC().Format(time.RFC3339)}
	if account.ProxyID != nil {
		id := *account.ProxyID
		result.ProxyID = &id
	}
	if s.proxyProber == nil {
		result.Error = "proxy prober unavailable"
		return result
	}
	proxyURL := ""
	if account.ProxyID != nil {
		proxy := account.Proxy
		if proxy == nil || proxy.ID != *account.ProxyID {
			if s.proxyRepo == nil {
				result.Error = "proxy repository unavailable"
				return result
			}
			loaded, err := s.proxyRepo.GetByID(ctx, *account.ProxyID)
			if err != nil || loaded == nil {
				result.Error = "proxy not found"
				return result
			}
			proxy = loaded
		}
		proxyURL = proxy.URL()
	}
	info, _, err := s.proxyProber.ProbeProxy(ctx, proxyURL)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if info == nil {
		result.Error = "empty probe result"
		return result
	}
	result.IP, result.Country, result.Region, result.City = info.IP, info.Country, info.Region, info.City
	timezone := strings.TrimSpace(info.Timezone)
	switch {
	case timezone == "":
		result.Error = "exit IP lookup returned no timezone"
	case !isAllowedOpenAIRequestTimezone(timezone):
		result.Error = "unsupported timezone: " + timezone
	default:
		result.Timezone = timezone
	}
	return result
}

// applyOpenAIRequestTimezoneDetection 写回检测结果。写回前重新读取账号：若期间已切回固定时区或又换了代理，
// 本次结果已过期，直接丢弃。
func (s *adminServiceImpl) applyOpenAIRequestTimezoneDetection(ctx context.Context, accountID int64, result OpenAIRequestTimezoneDetection) error {
	current, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return err
	}
	if !current.followsProxyRequestTimezone() || !sameProxyID(current.ProxyID, result.ProxyID) {
		return nil
	}
	updates := map[string]any{openAIRequestTimezoneDetectedExtraKey: result}
	if result.Timezone != "" {
		updates[openAIRequestTimezoneExtraKey] = result.Timezone
	}
	return s.accountRepo.UpdateExtra(ctx, accountID, updates)
}

// scheduleOpenAIRequestTimezoneDetection 在后台为跟随出口 IP 的账号检测时区，不阻塞保存。
// 同一批里出口相同（同一代理或都直连）的账号只探测一次。
func (s *adminServiceImpl) scheduleOpenAIRequestTimezoneDetection(accounts ...*Account) {
	byProxy := make(map[int64][]*Account)
	for _, account := range accounts {
		if !account.followsProxyRequestTimezone() {
			continue
		}
		if _, running := openAIRequestTimezoneDetecting.LoadOrStore(account.ID, struct{}{}); running {
			continue
		}
		key := openAIRequestTimezoneProxyKey(account.ProxyID)
		byProxy[key] = append(byProxy[key], account)
	}
	if len(byProxy) == 0 {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("openai_request_timezone_detect_panic", "recover", r)
			}
		}()
		for _, group := range byProxy {
			s.detectOpenAIRequestTimezoneGroup(group)
		}
	}()
}

func (s *adminServiceImpl) detectOpenAIRequestTimezoneGroup(group []*Account) {
	defer func() {
		for _, account := range group {
			openAIRequestTimezoneDetecting.Delete(account.ID)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), openAIRequestTimezoneDetectTimeout)
	defer cancel()
	result := s.probeOpenAIRequestTimezone(ctx, group[0])
	for _, account := range group {
		if err := s.applyOpenAIRequestTimezoneDetection(ctx, account.ID, result); err != nil {
			slog.Warn("openai_request_timezone_detect_persist_failed", "account_id", account.ID, "error", err)
			continue
		}
		if result.Error != "" {
			slog.Info("openai_request_timezone_detect_failed", "account_id", account.ID, "error", result.Error)
		}
	}
}

func (s *adminServiceImpl) scheduleOpenAIRequestTimezoneDetectionByIDs(ctx context.Context, ids []int64) {
	accounts := make([]*Account, 0, len(ids))
	for _, id := range ids {
		account, err := s.accountRepo.GetByID(ctx, id)
		if err == nil && account != nil {
			accounts = append(accounts, account)
		}
	}
	s.scheduleOpenAIRequestTimezoneDetection(accounts...)
}

// DetectOpenAIRequestTimezone 立即检测账号出口 IP 的时区并写回（后台「重新检测」）。
func (s *adminServiceImpl) DetectOpenAIRequestTimezone(ctx context.Context, id int64) (*Account, error) {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !account.followsProxyRequestTimezone() {
		return nil, infraerrors.BadRequest("OPENAI_REQUEST_TIMEZONE_NOT_FOLLOWING_PROXY", "account request timezone does not follow the exit IP")
	}
	if _, running := openAIRequestTimezoneDetecting.LoadOrStore(account.ID, struct{}{}); running {
		return nil, infraerrors.New(http.StatusConflict, "OPENAI_REQUEST_TIMEZONE_DETECTING", "timezone detection is already running for this account")
	}
	defer openAIRequestTimezoneDetecting.Delete(account.ID)
	probeCtx, cancel := context.WithTimeout(ctx, openAIRequestTimezoneDetectTimeout)
	defer cancel()
	result := s.probeOpenAIRequestTimezone(probeCtx, account)
	if err := s.applyOpenAIRequestTimezoneDetection(ctx, account.ID, result); err != nil {
		return nil, err
	}
	return s.accountRepo.GetByID(ctx, id)
}
