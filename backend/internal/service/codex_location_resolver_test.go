package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
)

func testCodexLocationConfig() *config.Config {
	return &config.Config{Gateway: config.GatewayConfig{
		CodexLocation: config.GatewayCodexLocationConfig{
			Enabled:                true,
			FallbackTimezone:       "Asia/Tokyo",
			FallbackCountry:        "JP",
			FallbackRegion:         "Tokyo",
			FallbackCity:           "Tokyo",
			ProbeTimeoutSeconds:    1,
			CacheTTLHours:          24,
			FailureCacheTTLSeconds: 60,
			DirectProbeEnabled:     true,
			MarkerAbsentHeuristic:  true,
			DeniedCountries:        []string{"CN"},
			DeniedTimezones:        []string{"Asia/Shanghai", "Asia/Urumqi"},
			IPTimezoneLookupURL:    "",
		},
	}}
}

type fakeProxyExitInfoProber struct {
	mu      sync.Mutex
	calls   int
	urls    []string
	results []*ProxyExitInfo
	err     error
	started chan struct{}
	release chan struct{}
}

func (f *fakeProxyExitInfoProber) ProbeProxy(ctx context.Context, proxyURL string) (*ProxyExitInfo, int64, error) {
	f.mu.Lock()
	index := f.calls
	f.calls++
	f.urls = append(f.urls, proxyURL)
	f.mu.Unlock()
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		<-f.release
	}
	if f.err != nil {
		return nil, 0, f.err
	}
	if len(f.results) == 0 {
		return nil, 0, errors.New("no probe result configured")
	}
	if index >= len(f.results) {
		index = len(f.results) - 1
	}
	return f.results[index], 1, nil
}

func (f *fakeProxyExitInfoProber) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeProxyExitInfoProber) callURLs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.urls))
	copy(out, f.urls)
	return out
}

type fakeCodexLocationCache struct {
	mu      sync.Mutex
	records map[int64]*CodexLocationCacheRecord
	ttls    map[int64]int64
	deleted []int64
}

func newFakeCodexLocationCache() *fakeCodexLocationCache {
	return &fakeCodexLocationCache{
		records: make(map[int64]*CodexLocationCacheRecord),
		ttls:    make(map[int64]int64),
	}
}

func (c *fakeCodexLocationCache) GetAccountLocation(ctx context.Context, accountID int64) (*CodexLocationCacheRecord, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record, ok := c.records[accountID]
	if !ok {
		return nil, nil
	}
	copied := *record
	return &copied, nil
}

func (c *fakeCodexLocationCache) SetAccountLocation(ctx context.Context, accountID int64, record *CodexLocationCacheRecord, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := *record
	c.records[accountID] = &copied
	c.ttls[accountID] = int64(ttl.Seconds())
	return nil
}

func (c *fakeCodexLocationCache) DeleteAccountLocation(ctx context.Context, accountID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.records, accountID)
	c.deleted = append(c.deleted, accountID)
	return nil
}

func (c *fakeCodexLocationCache) DeleteAccountLocations(ctx context.Context, accountIDs []int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, accountID := range accountIDs {
		delete(c.records, accountID)
		c.deleted = append(c.deleted, accountID)
	}
	return nil
}

func testProxy(id int64, host string) *Proxy {
	return &Proxy{ID: id, Protocol: "http", Host: host, Port: 8080, Username: "u", Password: "p"}
}

func testProxiedAccount(accountID, proxyID int64) *Account {
	proxy := testProxy(proxyID, "1.2.3.4")
	return &Account{ID: accountID, Platform: PlatformOpenAI, ProxyID: &proxyID, Proxy: proxy}
}

func TestCodexLocationResolverCachesPerAccountNotPerProxy(t *testing.T) {
	prober := &fakeProxyExitInfoProber{results: []*ProxyExitInfo{
		{IP: "1.1.1.1", City: "Piketon", Region: "Ohio", Country: "United States", CountryCode: "US", Timezone: "America/New_York"},
		{IP: "2.2.2.2", City: "Frankfurt", Region: "Hesse", Country: "Germany", CountryCode: "DE", Timezone: "Europe/Berlin"},
	}}
	cache := newFakeCodexLocationCache()
	resolver := NewCodexLocationResolver(testCodexLocationConfig(), prober, cache)

	// 两个账号绑定同一个代理，各自独立探测、各自缓存（不跨账号共享）。
	first := testProxiedAccount(1, 100)
	second := testProxiedAccount(2, 100)
	require.Equal(t, "America/New_York", resolver.Resolve(context.Background(), first).Timezone)
	require.Equal(t, "Europe/Berlin", resolver.Resolve(context.Background(), second).Timezone)
	require.Equal(t, 2, prober.callCount())
	require.NotNil(t, cache.records[1])
	require.NotNil(t, cache.records[2])
	require.Equal(t, "America/New_York", cache.records[1].Timezone)
	require.Equal(t, "Europe/Berlin", cache.records[2].Timezone)

	// 命中各自缓存，不再探测。
	require.Equal(t, "America/New_York", resolver.Resolve(context.Background(), first).Timezone)
	require.Equal(t, "Europe/Berlin", resolver.Resolve(context.Background(), second).Timezone)
	require.Equal(t, 2, prober.callCount())

	// 账号 1 改绑新代理：指纹变化 → 重新探测，账号 2 缓存不受影响。
	updatedProxy := testProxy(101, "5.6.7.8")
	proxyID := int64(101)
	first.ProxyID = &proxyID
	first.Proxy = updatedProxy
	require.Equal(t, "Europe/Berlin", resolver.Resolve(context.Background(), first).Timezone)
	require.Equal(t, 3, prober.callCount())
	require.Equal(t, "Europe/Berlin", cache.records[2].Timezone)
}

func TestCodexLocationResolverProxyIdentityChangeReprobes(t *testing.T) {
	prober := &fakeProxyExitInfoProber{results: []*ProxyExitInfo{
		{IP: "1.1.1.1", CountryCode: "US", Timezone: "America/New_York"},
		{IP: "2.2.2.2", CountryCode: "JP", Timezone: "Asia/Tokyo"},
	}}
	cache := newFakeCodexLocationCache()
	resolver := NewCodexLocationResolver(testCodexLocationConfig(), prober, cache)
	account := testProxiedAccount(1, 100)

	require.Equal(t, "America/New_York", resolver.Resolve(context.Background(), account).Timezone)
	// 代理身份变化（host 改变）但 ID 不变：指纹不一致 → 重新探测。
	account.Proxy = testProxy(100, "9.9.9.9")
	require.Equal(t, "Asia/Tokyo", resolver.Resolve(context.Background(), account).Timezone)
	require.Equal(t, 2, prober.callCount())
}

func TestCodexLocationResolverFallbackAndNegativeCache(t *testing.T) {
	prober := &fakeProxyExitInfoProber{err: errors.New("probe failed")}
	cache := newFakeCodexLocationCache()
	resolver := NewCodexLocationResolver(testCodexLocationConfig(), prober, cache)
	account := testProxiedAccount(1, 100)

	location := resolver.Resolve(context.Background(), account)
	require.Equal(t, "Asia/Tokyo", location.Timezone)
	require.Equal(t, "JP", location.Country)
	require.Equal(t, codexLocationSourceFallback, location.Source)
	require.Equal(t, 1, prober.callCount())

	// 负缓存命中：短时间内不重复探测。
	resolver.Resolve(context.Background(), account)
	require.Equal(t, 1, prober.callCount())
	require.NotNil(t, cache.records[1])
	require.False(t, cache.records[1].Success)
}

func TestCodexLocationResolverRejectsChinaEgress(t *testing.T) {
	prober := &fakeProxyExitInfoProber{results: []*ProxyExitInfo{
		{IP: "3.3.3.3", City: "Shanghai", Region: "Shanghai", Country: "China", CountryCode: "CN", Timezone: "Asia/Shanghai"},
	}}
	cache := newFakeCodexLocationCache()
	resolver := NewCodexLocationResolver(testCodexLocationConfig(), prober, cache)
	account := testProxiedAccount(1, 100)

	location := resolver.Resolve(context.Background(), account)
	require.Equal(t, "Asia/Tokyo", location.Timezone)
	require.Equal(t, "JP", location.Country)
	require.Equal(t, "Tokyo", location.City)
	require.Empty(t, location.IP)

	// 探测成功但结果被拒：按正常 TTL 缓存，避免每次请求都探测。
	require.NotNil(t, cache.records[1])
	require.True(t, cache.records[1].Success)
	require.Equal(t, "Asia/Tokyo", cache.records[1].Timezone)
}

func TestCodexLocationResolverAccountOverrideWins(t *testing.T) {
	prober := &fakeProxyExitInfoProber{results: []*ProxyExitInfo{{CountryCode: "US", Timezone: "America/New_York"}}}
	cache := newFakeCodexLocationCache()
	resolver := NewCodexLocationResolver(testCodexLocationConfig(), prober, cache)
	account := testProxiedAccount(1, 100)
	account.Extra = map[string]any{
		"codex_location": map[string]any{
			"timezone": "Europe/London",
			"country":  "GB",
			"region":   "England",
			"city":     "London",
		},
	}

	location := resolver.Resolve(context.Background(), account)
	require.Equal(t, "Europe/London", location.Timezone)
	require.Equal(t, "GB", location.Country)
	require.Equal(t, codexLocationSourceOverride, location.Source)
	require.Zero(t, prober.callCount())
	require.Empty(t, cache.records)
}

func TestCodexLocationResolverChinaOverrideFallsBack(t *testing.T) {
	prober := &fakeProxyExitInfoProber{results: []*ProxyExitInfo{{CountryCode: "US", Timezone: "America/New_York"}}}
	resolver := NewCodexLocationResolver(testCodexLocationConfig(), prober, newFakeCodexLocationCache())
	account := testProxiedAccount(1, 100)
	account.Extra = map[string]any{
		"codex_location": map[string]any{"timezone": "Asia/Shanghai", "country": "CN"},
	}

	// 覆盖值命中拒绝名单 → 不能作为覆盖生效，回退到正常探测。
	location := resolver.Resolve(context.Background(), account)
	require.Equal(t, "America/New_York", location.Timezone)
	require.Equal(t, 1, prober.callCount())
}

func TestCodexLocationResolverSanitizesFallbackGeo(t *testing.T) {
	cfg := testCodexLocationConfig()
	cfg.Gateway.CodexLocation.FallbackCountry = "CN"
	cfg.Gateway.CodexLocation.FallbackTimezone = "Asia/Shanghai"
	resolver, ok := NewCodexLocationResolver(cfg, nil, nil).(*codexLocationResolver)
	require.True(t, ok)

	effective := resolver.effectiveConfig()
	require.Equal(t, "Asia/Tokyo", effective.FallbackTimezone)
	require.Equal(t, "JP", effective.FallbackCountry)
}

func TestCodexLocationResolverPartialOverrideUsesSanitizedFallbackCountry(t *testing.T) {
	cfg := testCodexLocationConfig()
	// 误配兜底国家为中国：部分字段的 override 不得把它带进最终结果。
	cfg.Gateway.CodexLocation.FallbackCountry = "CN"
	prober := &fakeProxyExitInfoProber{results: []*ProxyExitInfo{{CountryCode: "US", Timezone: "America/New_York"}}}
	resolver := NewCodexLocationResolver(cfg, prober, newFakeCodexLocationCache())
	account := testProxiedAccount(1, 100)
	account.Extra = map[string]any{
		"codex_location": map[string]any{"timezone": "Asia/Tokyo"},
	}

	location := resolver.Resolve(context.Background(), account)
	require.Equal(t, "Asia/Tokyo", location.Timezone)
	require.Equal(t, "JP", location.Country)
	require.Equal(t, codexLocationSourceOverride, location.Source)
	require.Zero(t, prober.callCount())
}

func TestCodexLocationResolverDirectProbe(t *testing.T) {
	prober := &fakeProxyExitInfoProber{results: []*ProxyExitInfo{
		{IP: "8.8.8.8", City: "Tokyo", Region: "Tokyo", Country: "Japan", CountryCode: "JP", Timezone: "Asia/Tokyo"},
	}}
	cache := newFakeCodexLocationCache()
	resolver := NewCodexLocationResolver(testCodexLocationConfig(), prober, cache)
	account := &Account{ID: 7, Platform: PlatformOpenAI}

	location := resolver.Resolve(context.Background(), account)
	require.Equal(t, "Asia/Tokyo", location.Timezone)
	require.Equal(t, codexLocationSourceDirectProbe, location.Source)
	require.Equal(t, []string{""}, prober.callURLs())
	require.NotNil(t, cache.records[7])
	require.Equal(t, int64(0), cache.records[7].ProxyID)

	// 直连结果缓存命中，不重复探测。
	resolver.Resolve(context.Background(), account)
	require.Equal(t, 1, prober.callCount())
}

func TestCodexLocationResolverDirectProbeDisabled(t *testing.T) {
	cfg := testCodexLocationConfig()
	cfg.Gateway.CodexLocation.DirectProbeEnabled = false
	prober := &fakeProxyExitInfoProber{results: []*ProxyExitInfo{{CountryCode: "US", Timezone: "America/New_York"}}}
	resolver := NewCodexLocationResolver(cfg, prober, newFakeCodexLocationCache())
	account := &Account{ID: 7, Platform: PlatformOpenAI}

	location := resolver.Resolve(context.Background(), account)
	require.Equal(t, "Asia/Tokyo", location.Timezone)
	require.Zero(t, prober.callCount())
}

func TestCodexLocationResolverProxyNotHydratedFallsBack(t *testing.T) {
	prober := &fakeProxyExitInfoProber{results: []*ProxyExitInfo{{CountryCode: "US", Timezone: "America/New_York"}}}
	resolver := NewCodexLocationResolver(testCodexLocationConfig(), prober, newFakeCodexLocationCache())
	proxyID := int64(100)
	account := &Account{ID: 1, Platform: PlatformOpenAI, ProxyID: &proxyID}

	location := resolver.Resolve(context.Background(), account)
	require.Equal(t, "Asia/Tokyo", location.Timezone)
	require.Zero(t, prober.callCount())
}

func TestCodexLocationResolverIPTimezoneLookup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":      "success",
			"query":       "1.1.1.1",
			"country":     "United States",
			"countryCode": "US",
			"regionName":  "Ohio",
			"city":        "Piketon",
			"timezone":    "America/New_York",
		})
	}))
	t.Cleanup(server.Close)

	cfg := testCodexLocationConfig()
	cfg.Gateway.CodexLocation.IPTimezoneLookupURL = server.URL + "/json/{ip}"
	prober := &fakeProxyExitInfoProber{results: []*ProxyExitInfo{
		{IP: "1.1.1.1", Country: "United States", CountryCode: "US", City: "Piketon", Region: "Ohio"},
	}}
	resolver := NewCodexLocationResolver(cfg, prober, newFakeCodexLocationCache())
	account := testProxiedAccount(1, 100)

	location := resolver.Resolve(context.Background(), account)
	require.Equal(t, "America/New_York", location.Timezone)
	require.Equal(t, codexLocationSourceIPLookup, location.Source)
	require.Equal(t, "US", location.Country)
}

func TestCodexLocationResolverSingleflight(t *testing.T) {
	prober := &fakeProxyExitInfoProber{
		results: []*ProxyExitInfo{{CountryCode: "US", Timezone: "America/New_York"}},
		release: make(chan struct{}),
	}
	// 探测短暂阻塞后放行：调用方都在有界等待窗口内，避免依赖 2s 上限的时序。
	time.AfterFunc(20*time.Millisecond, func() { close(prober.release) })
	cache := newFakeCodexLocationCache()
	resolver := NewCodexLocationResolver(testCodexLocationConfig(), prober, cache)
	account := testProxiedAccount(1, 100)

	const goroutines = 4
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			require.Equal(t, "America/New_York", resolver.Resolve(context.Background(), account).Timezone)
		}()
	}
	wg.Wait()
	require.Equal(t, 1, prober.callCount())
}

func TestCodexLocationResolverSingleflightKeyIncludesProxyFingerprint(t *testing.T) {
	prober := &fakeProxyExitInfoProber{
		results: []*ProxyExitInfo{
			{CountryCode: "US", Timezone: "America/New_York"},
			{CountryCode: "JP", Timezone: "Asia/Tokyo"},
		},
		release: make(chan struct{}),
	}
	time.AfterFunc(50*time.Millisecond, func() { close(prober.release) })
	cache := newFakeCodexLocationCache()
	resolver := NewCodexLocationResolver(testCodexLocationConfig(), prober, cache)

	// 同一账号 ID、不同代理身份：必须各自探测，不能共享 singleflight 结果。
	accountA := testProxiedAccount(1, 100)
	accountB := testProxiedAccount(1, 100)
	accountB.Proxy = testProxy(100, "5.6.7.8")

	results := make([]string, 2)
	var wg sync.WaitGroup
	for i, account := range []*Account{accountA, accountB} {
		wg.Add(1)
		go func(i int, account *Account) {
			defer wg.Done()
			results[i] = resolver.Resolve(context.Background(), account).Timezone
		}(i, account)
	}
	require.Eventually(t, func() bool { return prober.callCount() == 2 }, 2*time.Second, 10*time.Millisecond)
	wg.Wait()
	require.ElementsMatch(t, []string{"America/New_York", "Asia/Tokyo"}, results)
	require.Equal(t, 2, prober.callCount())
}

func TestCodexLocationResolverBoundedWaitFallsBackWhileProbeContinues(t *testing.T) {
	prober := &fakeProxyExitInfoProber{
		results: []*ProxyExitInfo{{CountryCode: "US", Timezone: "America/New_York"}},
		release: make(chan struct{}),
	}
	cache := newFakeCodexLocationCache()
	resolver := NewCodexLocationResolver(testCodexLocationConfig(), prober, cache)
	account := testProxiedAccount(1, 100)

	// 若实现退化为“等探测完成”，兜底放行避免测试永久挂起，再由耗时断言报错。
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(prober.release) }) }
	safetyTimer := time.AfterFunc(5*time.Second, release)
	defer safetyTimer.Stop()
	defer release()

	start := time.Now()
	loc := resolver.Resolve(context.Background(), account)
	require.Equal(t, codexLocationFallbackTimezone, loc.Timezone)
	require.Less(t, time.Since(start), 3*time.Second, "resolve must not wait for the full probe")

	// 调用方放弃后探测仍在后台完成并写入缓存。
	release()
	require.Eventually(t, func() bool {
		rec, err := cache.GetAccountLocation(context.Background(), account.ID)
		return err == nil && rec != nil && rec.Timezone == "America/New_York"
	}, 3*time.Second, 10*time.Millisecond)

	require.Equal(t, "America/New_York", resolver.Resolve(context.Background(), account).Timezone)
	require.Equal(t, 1, prober.callCount())
}

func TestCodexLocationResolverResolveWaitCapsAtTwoSeconds(t *testing.T) {
	resolver, ok := NewCodexLocationResolver(testCodexLocationConfig(), nil, nil).(*codexLocationResolver)
	require.True(t, ok)

	cfg := testCodexLocationConfig().Gateway.CodexLocation
	cfg.ProbeTimeoutSeconds = 30
	require.Equal(t, codexLocationResolveMaxWait, resolver.resolveWait(cfg))

	cfg.ProbeTimeoutSeconds = 1
	require.Equal(t, time.Second, resolver.resolveWait(cfg))
}
