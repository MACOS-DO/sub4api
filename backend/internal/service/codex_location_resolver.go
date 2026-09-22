package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	// 嵌入 IANA tzdata：镜像未安装系统 zoneinfo 时仍能加载任意出口时区。
	_ "time/tzdata"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/MACOS-DO/sub4api/internal/pkg/httpclient"
	"github.com/MACOS-DO/sub4api/internal/pkg/logger"
	"golang.org/x/sync/singleflight"
)

const (
	codexLocationExtraKey          = "codex_location"
	codexLocationAlignEnabledKey   = "codex_location_align_enabled"
	codexLocationIPLookupMaxBytes  = int64(64 * 1024)
	codexLocationIPLookupUserAgent = "sub4api-codex-location/1.0"
	defaultCodexLocationProbeTime  = 5 * time.Second
	defaultCodexLocationCacheTTL   = 24 * time.Hour
	defaultCodexLocationFailureTTL = 60 * time.Second

	// codexLocationResolveMaxWait 是冷缓存时调用方同步等待探测的上限：超时即用
	// 兜底返回，探测在后台继续完成并写缓存，避免请求被出口探测拖慢。
	codexLocationResolveMaxWait = 2 * time.Second
)

// defaultCodexLocationConfig 在未加载配置（测试/工具）时提供与 setDefaults 一致的默认值。
func defaultCodexLocationConfig() config.GatewayCodexLocationConfig {
	return config.GatewayCodexLocationConfig{
		Enabled:                true,
		FallbackTimezone:       codexLocationFallbackTimezone,
		FallbackCountry:        "JP",
		FallbackRegion:         "Tokyo",
		FallbackCity:           "Tokyo",
		ProbeTimeoutSeconds:    5,
		CacheTTLHours:          24,
		FailureCacheTTLSeconds: 60,
		DirectProbeEnabled:     true,
		MarkerAbsentHeuristic:  true,
		DeniedCountries:        []string{"CN"},
		DeniedTimezones:        []string{"Asia/Shanghai", "Asia/Urumqi", "Asia/Chongqing", "Asia/Harbin", "Asia/Kashgar", "PRC"},
		IPTimezoneLookupURL:    "http://ip-api.com/json/{ip}?fields=status,message,country,countryCode,regionName,city,timezone",
	}
}

type codexLocationResolver struct {
	cfg     *config.Config
	prober  ProxyExitInfoProber
	cache   CodexLocationCache
	flights singleflight.Group
	now     func() time.Time
}

// NewCodexLocationResolver 构建出口地理解析器。prober/cache 允许为 nil（测试或
// 降级场景），此时探测或缓存不可用，直接走兜底。
func NewCodexLocationResolver(cfg *config.Config, prober ProxyExitInfoProber, cache CodexLocationCache) CodexLocationResolver {
	return &codexLocationResolver{
		cfg:    cfg,
		prober: prober,
		cache:  cache,
		now:    time.Now,
	}
}

func (r *codexLocationResolver) effectiveConfig() config.GatewayCodexLocationConfig {
	if r == nil || r.cfg == nil {
		return defaultCodexLocationConfig()
	}
	cfg := r.cfg.Gateway.CodexLocation
	defaults := defaultCodexLocationConfig()
	if strings.TrimSpace(cfg.FallbackTimezone) == "" {
		cfg.FallbackTimezone = defaults.FallbackTimezone
	}
	if strings.TrimSpace(cfg.FallbackCountry) == "" {
		cfg.FallbackCountry = defaults.FallbackCountry
	}
	if strings.TrimSpace(cfg.FallbackRegion) == "" {
		cfg.FallbackRegion = defaults.FallbackRegion
	}
	if strings.TrimSpace(cfg.FallbackCity) == "" {
		cfg.FallbackCity = defaults.FallbackCity
	}
	if cfg.ProbeTimeoutSeconds <= 0 {
		cfg.ProbeTimeoutSeconds = defaults.ProbeTimeoutSeconds
	}
	if cfg.CacheTTLHours <= 0 {
		cfg.CacheTTLHours = defaults.CacheTTLHours
	}
	if cfg.FailureCacheTTLSeconds <= 0 {
		cfg.FailureCacheTTLSeconds = defaults.FailureCacheTTLSeconds
	}
	if len(cfg.DeniedCountries) == 0 {
		cfg.DeniedCountries = defaults.DeniedCountries
	}
	if len(cfg.DeniedTimezones) == 0 {
		cfg.DeniedTimezones = defaults.DeniedTimezones
	}
	// 集中清洗兜底地理：账号级 override 未提供的字段会继承这里的兜底值，
	// 若兜底本身命中拒绝名单（例如被误配成中国），会绕过 fallbackLocation 的保护。
	// 分别只传国家/时区列表，避免交叉误判。
	if !codexLocationTimezoneUsable(cfg.FallbackTimezone) ||
		codexLocationDenied("", cfg.FallbackTimezone, nil, cfg.DeniedTimezones) {
		cfg.FallbackTimezone = codexLocationFallbackTimezone
	}
	if codexLocationDenied(cfg.FallbackCountry, "", cfg.DeniedCountries, nil) {
		cfg.FallbackCountry = "JP"
	}
	return cfg
}

func (r *codexLocationResolver) probeTimeout(cfg config.GatewayCodexLocationConfig) time.Duration {
	return time.Duration(cfg.ProbeTimeoutSeconds) * time.Second
}

func (r *codexLocationResolver) cacheTTL(cfg config.GatewayCodexLocationConfig) time.Duration {
	return time.Duration(cfg.CacheTTLHours) * time.Hour
}

func (r *codexLocationResolver) failureTTL(cfg config.GatewayCodexLocationConfig) time.Duration {
	return time.Duration(cfg.FailureCacheTTLSeconds) * time.Second
}

func (r *codexLocationResolver) nowFunc() time.Time {
	if r != nil && r.now != nil {
		return r.now()
	}
	return time.Now()
}

// fallbackLocation 返回兜底地理。若配置的兜底时区不可加载或命中拒绝名单（例如
// 被误配成中国时区），强制回到硬编码的东京。
func (r *codexLocationResolver) fallbackLocation(cfg config.GatewayCodexLocationConfig) CodexLocation {
	timezone := strings.TrimSpace(cfg.FallbackTimezone)
	if !codexLocationTimezoneUsable(timezone) ||
		codexLocationDenied(cfg.FallbackCountry, timezone, cfg.DeniedCountries, cfg.DeniedTimezones) {
		timezone = codexLocationFallbackTimezone
	}
	country := strings.TrimSpace(cfg.FallbackCountry)
	if codexLocationDenied(country, timezone, cfg.DeniedCountries, cfg.DeniedTimezones) {
		country = "JP"
	}
	if country == "" {
		country = "JP"
	}
	region := strings.TrimSpace(cfg.FallbackRegion)
	if region == "" {
		region = "Tokyo"
	}
	city := strings.TrimSpace(cfg.FallbackCity)
	if city == "" {
		city = "Tokyo"
	}
	return CodexLocation{
		Timezone: timezone,
		Country:  country,
		Region:   region,
		City:     city,
		Source:   codexLocationSourceFallback,
	}
}

// Resolve 解析账号当前出口地理。任何失败都返回兜底值；冷缓存时最多同步等待
// codexLocationResolveMaxWait，超时后用兜底返回，探测在后台继续完成并写缓存。
func (r *codexLocationResolver) Resolve(ctx context.Context, account *Account) CodexLocation {
	if r == nil {
		return defaultCodexLocationResolverFallback()
	}
	cfg := r.effectiveConfig()
	if !cfg.Enabled {
		return r.fallbackLocation(cfg)
	}
	if account == nil {
		return r.fallbackLocation(cfg)
	}
	if override, ok := account.CodexLocationOverride(); ok {
		if loc, ok := r.normalizeLocation(override, codexLocationSourceOverride, cfg); ok {
			return loc
		}
	}
	proxyID, proxyHash := codexLocationProxyFingerprint(account)
	if rec := r.readCachedLocation(ctx, account.ID, proxyID, proxyHash); rec != nil {
		return locationFromRecord(rec)
	}

	// 探测在 singleflight 的后台 goroutine 中执行（键含代理指纹），调用方只等
	// 有界时间；超时后探测不取消，仍会完成并写入缓存供后续请求命中。
	ch := r.flights.DoChan(codexLocationFlightKey(account.ID, proxyID, proxyHash), func() (any, error) {
		flightCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.probeTimeout(cfg)+time.Second)
		defer cancel()
		if rec := r.readCachedLocation(flightCtx, account.ID, proxyID, proxyHash); rec != nil {
			return locationFromRecord(rec), nil
		}
		return r.probeLocation(flightCtx, account, cfg, proxyID, proxyHash), nil
	})
	timer := time.NewTimer(r.resolveWait(cfg))
	defer timer.Stop()
	select {
	case result := <-ch:
		if loc, ok := result.Val.(CodexLocation); ok {
			return loc
		}
	case <-timer.C:
		// 探测仍在后台进行，本次请求用兜底。
	case <-ctx.Done():
	}
	return r.fallbackLocation(cfg)
}

// codexLocationFlightKey 以账号+代理绑定指纹作为 singleflight 键：账号刚改绑代理
// 时的并发请求必须各自探测，不能共享另一条出口链路的解析结果。
func codexLocationFlightKey(accountID, proxyID int64, proxyHash string) string {
	return fmt.Sprintf("%d|%d|%s", accountID, proxyID, proxyHash)
}

// resolveWait 返回冷缓存时调用方同步等待探测的上限。
func (r *codexLocationResolver) resolveWait(cfg config.GatewayCodexLocationConfig) time.Duration {
	wait := r.probeTimeout(cfg)
	if wait > codexLocationResolveMaxWait {
		return codexLocationResolveMaxWait
	}
	return wait
}

// readCachedLocation 读取缓存并做绑定指纹自校验；指纹不一致视为未命中。
func (r *codexLocationResolver) readCachedLocation(ctx context.Context, accountID, proxyID int64, proxyHash string) *CodexLocationCacheRecord {
	if r.cache == nil {
		return nil
	}
	rec, err := r.cache.GetAccountLocation(ctx, accountID)
	if err != nil || rec == nil {
		return nil
	}
	if !codexLocationFingerprintMatches(rec, proxyID, proxyHash) {
		return nil
	}
	if rec.Success && !codexLocationTimezoneUsable(rec.Timezone) {
		return nil
	}
	return rec
}

// probeLocation 执行一次出口地理探测并写入缓存。失败返回兜底（负缓存）。
func (r *codexLocationResolver) probeLocation(ctx context.Context, account *Account, cfg config.GatewayCodexLocationConfig, proxyID int64, proxyHash string) CodexLocation {
	fallback := r.fallbackLocation(cfg)
	if r.prober == nil {
		return r.storeLocation(ctx, account, cfg, fallback, false, proxyID, proxyHash)
	}

	proxyURL := ""
	source := codexLocationSourceDirectProbe
	if proxyID != 0 {
		if account.Proxy == nil {
			// 调度元数据快照没有代理详情：不直连探测（会误用服务器出口），走兜底。
			return r.storeLocation(ctx, account, cfg, fallback, false, proxyID, proxyHash)
		}
		proxyURL = account.Proxy.URL()
		source = codexLocationSourceProxyProbe
	} else if !cfg.DirectProbeEnabled {
		return r.storeLocation(ctx, account, cfg, fallback, false, proxyID, proxyHash)
	}

	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.probeTimeout(cfg))
	defer cancel()

	exitInfo, _, err := r.prober.ProbeProxy(probeCtx, proxyURL)
	if err != nil || exitInfo == nil {
		if err != nil {
			logger.LegacyPrintf("service.codex_location",
				"[CodexLocation] probe failed account=%d proxy_id=%d err=%v", account.ID, proxyID, err)
		}
		return r.storeLocation(ctx, account, cfg, fallback, false, proxyID, proxyHash)
	}

	timezone := strings.TrimSpace(exitInfo.Timezone)
	country := strings.TrimSpace(exitInfo.CountryCode)
	region := strings.TrimSpace(exitInfo.Region)
	city := strings.TrimSpace(exitInfo.City)
	if timezone == "" && strings.TrimSpace(exitInfo.IP) != "" {
		if lookedUp, lookupErr := r.lookupTimezoneByIP(probeCtx, exitInfo.IP, cfg); lookupErr == nil && lookedUp != nil {
			timezone = strings.TrimSpace(lookedUp.Timezone)
			if country == "" {
				country = strings.TrimSpace(lookedUp.CountryCode)
			}
			if region == "" {
				region = strings.TrimSpace(lookedUp.Region)
			}
			if city == "" {
				city = strings.TrimSpace(lookedUp.City)
			}
			source = codexLocationSourceIPLookup
		}
	}

	if !codexLocationTimezoneUsable(timezone) ||
		codexLocationDenied(country, timezone, cfg.DeniedCountries, cfg.DeniedTimezones) {
		logger.LegacyPrintf("service.codex_location",
			"[CodexLocation] rejected detected location account=%d proxy_id=%d country=%s timezone=%s; using fallback",
			account.ID, proxyID, country, timezone)
		return r.storeLocation(ctx, account, cfg, fallback, true, proxyID, proxyHash)
	}

	loc := CodexLocation{
		Timezone: timezone,
		Country:  country,
		Region:   region,
		City:     city,
		IP:       strings.TrimSpace(exitInfo.IP),
		Source:   source,
	}
	return r.storeLocation(ctx, account, cfg, loc, true, proxyID, proxyHash)
}

// storeLocation 写缓存并返回地理值。success=false 使用较短的负缓存 TTL。
func (r *codexLocationResolver) storeLocation(ctx context.Context, account *Account, cfg config.GatewayCodexLocationConfig, loc CodexLocation, success bool, proxyID int64, proxyHash string) CodexLocation {
	if r.cache == nil || account == nil {
		return loc
	}
	ttl := r.cacheTTL(cfg)
	if !success {
		ttl = r.failureTTL(cfg)
	}
	rec := &CodexLocationCacheRecord{
		Timezone:  loc.Timezone,
		Country:   loc.Country,
		Region:    loc.Region,
		City:      loc.City,
		IP:        loc.IP,
		Source:    loc.Source,
		ProxyID:   proxyID,
		ProxyHash: proxyHash,
		Success:   success,
		ProbedAt:  r.nowFunc().Unix(),
	}
	if err := r.cache.SetAccountLocation(context.WithoutCancel(ctx), account.ID, rec, ttl); err != nil {
		logger.LegacyPrintf("service.codex_location",
			"[CodexLocation] cache write failed account=%d err=%v", account.ID, err)
	}
	return loc
}

// normalizeLocation 校验手动覆盖值；无效时返回 false 让调用方回退探测。
// override 未提供的 country/region/city 会继承 effectiveConfig 已清洗的兜底地理：
// 这是刻意行为，避免未覆盖字段透传客户端可能携带的被拒地区值。
func (r *codexLocationResolver) normalizeLocation(loc CodexLocation, source string, cfg config.GatewayCodexLocationConfig) (CodexLocation, bool) {
	timezone := strings.TrimSpace(loc.Timezone)
	if !codexLocationTimezoneUsable(timezone) {
		return CodexLocation{}, false
	}
	country := strings.TrimSpace(loc.Country)
	if codexLocationDenied(country, timezone, cfg.DeniedCountries, cfg.DeniedTimezones) {
		return CodexLocation{}, false
	}
	if country == "" {
		country = strings.TrimSpace(cfg.FallbackCountry)
	}
	region := strings.TrimSpace(loc.Region)
	if region == "" {
		region = strings.TrimSpace(cfg.FallbackRegion)
	}
	city := strings.TrimSpace(loc.City)
	if city == "" {
		city = strings.TrimSpace(cfg.FallbackCity)
	}
	return CodexLocation{
		Timezone: timezone,
		Country:  country,
		Region:   region,
		City:     city,
		Source:   source,
	}, true
}

type codexLocationIPLookupResult struct {
	Status      string `json:"status"`
	Message     string `json:"message"`
	Query       string `json:"query"`
	Country     string `json:"country"`
	CountryCode string `json:"countryCode"`
	Region      string `json:"regionName"`
	City        string `json:"city"`
	Timezone    string `json:"timezone"`
}

// lookupTimezoneByIP 在探测端点不返回时区时，按出口 IP 直连补查（不再走代理，
// 避免 AI-only 代理封禁探测端点）。
func (r *codexLocationResolver) lookupTimezoneByIP(ctx context.Context, ip string, cfg config.GatewayCodexLocationConfig) (*codexLocationIPLookupResult, error) {
	template := strings.TrimSpace(cfg.IPTimezoneLookupURL)
	if template == "" {
		return nil, fmt.Errorf("ip timezone lookup disabled")
	}
	trimmedIP := strings.TrimSpace(ip)
	if net.ParseIP(trimmedIP) == nil {
		return nil, fmt.Errorf("invalid exit ip %q", ip)
	}
	target := strings.ReplaceAll(template, "{ip}", url.QueryEscape(trimmedIP))
	client, err := httpclient.GetClient(httpclient.Options{
		Timeout: r.probeTimeout(cfg),
	})
	if err != nil {
		return nil, err
	}
	//nolint:gosec // G704: URL 来自运维配置的固定模板，{ip} 已校验为合法 IP 并做转义
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", codexLocationIPLookupUserAgent)
	resp, err := client.Do(req) //nolint:gosec // G704: 同上，目标为运维配置的查询端点
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ip timezone lookup status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, codexLocationIPLookupMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > codexLocationIPLookupMaxBytes {
		return nil, fmt.Errorf("ip timezone lookup response exceeds limit")
	}
	var result codexLocationIPLookupResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(result.Status), "success") {
		return nil, fmt.Errorf("ip timezone lookup failed: %s", strings.TrimSpace(result.Message))
	}
	return &result, nil
}

func locationFromRecord(rec *CodexLocationCacheRecord) CodexLocation {
	return CodexLocation{
		Timezone: rec.Timezone,
		Country:  rec.Country,
		Region:   rec.Region,
		City:     rec.City,
		IP:       rec.IP,
		Source:   rec.Source,
	}
}

func defaultCodexLocationResolverFallback() CodexLocation {
	return CodexLocation{
		Timezone: codexLocationFallbackTimezone,
		Country:  "JP",
		Region:   "Tokyo",
		City:     "Tokyo",
		Source:   codexLocationSourceFallback,
	}
}

// CodexLocationOverride 读取账号级手动覆盖（extra.codex_location 或
// extra.openai.codex_location）。未配置时返回 false。
func (a *Account) CodexLocationOverride() (CodexLocation, bool) {
	if a == nil || a.Platform != PlatformOpenAI || a.Extra == nil {
		return CodexLocation{}, false
	}
	if loc, ok := codexLocationOverrideFromMap(a.Extra); ok {
		return loc, true
	}
	openaiConfig, _ := a.Extra[PlatformOpenAI].(map[string]any)
	return codexLocationOverrideFromMap(openaiConfig)
}

func codexLocationOverrideFromMap(values map[string]any) (CodexLocation, bool) {
	if values == nil {
		return CodexLocation{}, false
	}
	raw, ok := values[codexLocationExtraKey].(map[string]any)
	if !ok || len(raw) == 0 {
		return CodexLocation{}, false
	}
	loc := CodexLocation{
		Timezone: codexLocationStringFromMap(raw, "timezone"),
		Country:  codexLocationStringFromMap(raw, "country"),
		Region:   codexLocationStringFromMap(raw, "region"),
		City:     codexLocationStringFromMap(raw, "city"),
	}
	if strings.TrimSpace(loc.Timezone) == "" {
		return CodexLocation{}, false
	}
	return loc, true
}

func codexLocationStringFromMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// CodexLocationAlignOverride 读取账号级时区对齐开关覆盖。nil 表示跟随全局配置。
func (a *Account) CodexLocationAlignOverride() *bool {
	if a == nil || a.Platform != PlatformOpenAI || a.Extra == nil {
		return nil
	}
	if override := boolOverrideFromMap(a.Extra, codexLocationAlignEnabledKey); override != nil {
		return override
	}
	openaiConfig, _ := a.Extra[PlatformOpenAI].(map[string]any)
	return boolOverrideFromMap(openaiConfig, codexLocationAlignEnabledKey)
}
