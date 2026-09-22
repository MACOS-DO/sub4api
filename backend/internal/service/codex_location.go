package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Codex 请求时区/地点对齐的出口地理来源。
const (
	codexLocationSourceOverride    = "override"
	codexLocationSourceProxyProbe  = "proxy_probe"
	codexLocationSourceDirectProbe = "direct_probe"
	codexLocationSourceIPLookup    = "ip_lookup"
	codexLocationSourceFallback    = "fallback"
)

// codexLocationFallbackTimezone 是配置缺失时使用的硬兜底时区（禁止使用中国时区）。
const codexLocationFallbackTimezone = "Asia/Tokyo"

// CodexLocation 表示一次请求最终采用的出口地理/时区。
type CodexLocation struct {
	Timezone string
	Country  string
	Region   string
	City     string
	IP       string
	Source   string
}

// Date 按该时区把绝对时间格式化为 YYYY-MM-DD，供 environment_context 的
// <current_date> 使用。时区不可加载时按东京（UTC+9）兜底。
func (l CodexLocation) Date(now time.Time) string {
	location, err := time.LoadLocation(strings.TrimSpace(l.Timezone))
	if err != nil || location == nil {
		location = time.FixedZone("JST", 9*60*60)
	}
	return now.In(location).Format("2006-01-02")
}

// CodexLocationCacheRecord 是账号维度的出口地理缓存记录。记录中携带账号当时的
// 代理绑定指纹（ProxyID + ProxyHash），读取时与当前绑定比对，指纹不一致即视为
// 未命中，避免任何变更路径漏挂失效钩子时读到过期出口地理。
type CodexLocationCacheRecord struct {
	Timezone  string `json:"timezone"`
	Country   string `json:"country"`
	Region    string `json:"region"`
	City      string `json:"city"`
	IP        string `json:"ip"`
	Source    string `json:"source"`
	ProxyID   int64  `json:"proxy_id"`
	ProxyHash string `json:"proxy_hash"`
	Success   bool   `json:"success"`
	ProbedAt  int64  `json:"probed_at"`
}

// CodexLocationCache 是账号维度出口地理缓存。键必须只由账号 ID 决定，禁止跨账号
// 共享（同一代理下不同账号可能有不同出口）。
type CodexLocationCache interface {
	GetAccountLocation(ctx context.Context, accountID int64) (*CodexLocationCacheRecord, error)
	SetAccountLocation(ctx context.Context, accountID int64, rec *CodexLocationCacheRecord, ttl time.Duration) error
	DeleteAccountLocation(ctx context.Context, accountID int64) error
	DeleteAccountLocations(ctx context.Context, accountIDs []int64) error
}

// CodexLocationResolver 解析账号当前的出口地理/时区。
type CodexLocationResolver interface {
	Resolve(ctx context.Context, account *Account) CodexLocation
}

// codexLocationProxyFingerprint 返回账号当前代理绑定的指纹。代理未水合（Proxy 为
// nil）但有 ProxyID 时返回 (ProxyID, "")，与真实代理记录永不匹配，从而强制重探。
func codexLocationProxyFingerprint(account *Account) (int64, string) {
	if account == nil || account.ProxyID == nil {
		return 0, ""
	}
	if account.Proxy == nil {
		return *account.ProxyID, ""
	}
	proxy := account.Proxy
	raw := strings.Join([]string{
		proxy.Protocol,
		proxy.Host,
		strconv.Itoa(proxy.Port),
		proxy.Username,
		proxy.Password,
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return proxy.ID, hex.EncodeToString(sum[:8])
}

func codexLocationFingerprintMatches(rec *CodexLocationCacheRecord, proxyID int64, proxyHash string) bool {
	if rec == nil {
		return false
	}
	return rec.ProxyID == proxyID && rec.ProxyHash == proxyHash
}

// codexLocationTimezoneUsable 校验 IANA 时区可加载。中国时区由调用方按拒绝名单处理。
func codexLocationTimezoneUsable(timezone string) bool {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		return false
	}
	location, err := time.LoadLocation(timezone)
	return err == nil && location != nil
}

// codexLocationDenied 判断探测结果是否命中配置的拒绝国家/时区名单（默认中国大陆）。
func codexLocationDenied(countryCode, timezone string, deniedCountries, deniedTimezones []string) bool {
	countryCode = strings.ToUpper(strings.TrimSpace(countryCode))
	for _, denied := range deniedCountries {
		if countryCode != "" && countryCode == strings.ToUpper(strings.TrimSpace(denied)) {
			return true
		}
	}
	timezone = strings.TrimSpace(timezone)
	for _, denied := range deniedTimezones {
		if timezone != "" && strings.EqualFold(timezone, strings.TrimSpace(denied)) {
			return true
		}
	}
	return false
}
