package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type bpsCatalogRouteRepo struct {
	service.CompositeModelRouteRepository
	routes []service.CompositeModelRoute
	err    error
}

func (r bpsCatalogRouteRepo) ListByGroup(_ context.Context, _ int64, all bool) ([]service.CompositeModelRoute, error) {
	if r.err != nil {
		return nil, r.err
	}
	var result []service.CompositeModelRoute
	for _, route := range r.routes {
		if all || route.Enabled {
			result = append(result, route)
		}
	}
	return result, nil
}
func bpsCatalogAccount() service.Account {
	return service.Account{ID: 1, Platform: service.PlatformOpenAIBPS, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "workspace", "model_mapping": map[string]any{"gpt-6-astra": "gpt-6-astra", "gpt-5.6-sol": "gpt-5.6-sol", "bps-prefix": "gpt-6-astra"}}}
}
func bpsCatalogHandler(accounts []service.Account, routes bpsCatalogRouteRepo) *GatewayHandler {
	repo := &gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{91: accounts}}
	resolver := service.NewCompositeRouteResolver(routes)
	return &GatewayHandler{gatewayService: service.NewGatewayService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, resolver, nil, nil)}
}
func bpsCatalogContext(group *service.Group, path string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	c.Set("api_key", &service.APIKey{Group: group})
	return c, rec
}
func bpsCatalogRoute() service.CompositeModelRoute {
	return service.CompositeModelRoute{ID: 1, GroupID: 91, PublicModel: "bps-astra", MatchType: service.CompositeRouteMatchExact, TargetPlatform: service.PlatformOpenAIBPS, UpstreamModel: "gpt-6-astra", Endpoint: service.CompositeRouteEndpointResponses, Enabled: true}
}
func TestOpenAIBPSModelsExplicitCompositeAlias(t *testing.T) {
	account := bpsCatalogAccount()
	route := bpsCatalogRoute()
	h := bpsCatalogHandler([]service.Account{account}, bpsCatalogRouteRepo{routes: []service.CompositeModelRoute{route}})
	group := &service.Group{ID: 91, Platform: service.PlatformComposite}
	ids, err := h.codexModelIDsForGroup(context.Background(), group, "")
	require.NoError(t, err)
	require.Equal(t, []string{"bps-astra"}, ids)
	c, rec := bpsCatalogContext(group, "/v1/models")
	h.Models(c)
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "bps-astra", gjson.Get(rec.Body.String(), "data.0.id").String())
	require.Len(t, gjson.Get(rec.Body.String(), "data").Array(), 1)
	c, rec = bpsCatalogContext(group, "/models?client_version=0.147.0")
	h.CodexModels(c)
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "bps-astra", gjson.Get(rec.Body.String(), "models.0.slug").String())
	require.False(t, gjson.Get(rec.Body.String(), "models.0.supports_parallel_tool_calls").Bool())
	require.False(t, gjson.Get(rec.Body.String(), "models.0.prefer_websockets").Bool())
	require.Len(t, gjson.Get(rec.Body.String(), "models.0.supported_reasoning_levels").Array(), 4)
	group.ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{"missing"}}
	c, rec = bpsCatalogContext(group, "/v1/models")
	h.Models(c)
	require.Empty(t, gjson.Get(rec.Body.String(), "data").Array())
}

func TestOpenAIBPSModelsEmptyCompositeStaysEmpty(t *testing.T) {
	for _, mode := range []string{"disabled account", "expired token", "disabled route", "no route", "wrong endpoint", "model denied", "no account"} {
		t.Run(mode, func(t *testing.T) {
			a := bpsCatalogAccount()
			route := bpsCatalogRoute()
			accounts := []service.Account{a}
			routes := []service.CompositeModelRoute{route}
			switch mode {
			case "disabled account":
				accounts[0].Status = "disabled"
			case "expired token":
				accounts[0].Credentials["expires_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
			case "disabled route":
				routes[0].Enabled = false
			case "no route":
				routes = nil
			case "wrong endpoint":
				routes[0].Endpoint = service.CompositeRouteEndpointChatCompletions
			case "model denied":
				routes[0].UpstreamModel = "unavailable-model"
			case "no account":
				accounts = nil
			}
			h := bpsCatalogHandler(accounts, bpsCatalogRouteRepo{routes: routes})
			group := &service.Group{ID: 91, Platform: service.PlatformComposite}
			for _, allowlist := range []bool{false, true} {
				group.ModelAllowlist = service.GroupModelAllowlist{Enabled: allowlist, Models: []string{"gpt-*", "claude-*", "bps-*"}}
				c, rec := bpsCatalogContext(group, "/v1/models")
				h.Models(c)
				require.Equal(t, 200, rec.Code)
				require.Empty(t, gjson.Get(rec.Body.String(), "data").Array())
				c, rec = bpsCatalogContext(group, "/models?client_version=0.147.0")
				h.CodexModels(c)
				require.Equal(t, 200, rec.Code)
				require.Empty(t, gjson.Get(rec.Body.String(), "models").Array())
			}
		})
	}
}

func TestOpenAIBPSModelsPrefixPrecedenceAndCatalogErrors(t *testing.T) {
	route := bpsCatalogRoute()
	route.MatchType = service.CompositeRouteMatchPrefix
	route.PublicModel = "bps-"
	route.UpstreamModel = ""
	exact := bpsCatalogRoute()
	exact.PublicModel = "bps-prefix"
	exact.UpstreamModel = "not-supported"
	h := bpsCatalogHandler([]service.Account{bpsCatalogAccount()}, bpsCatalogRouteRepo{routes: []service.CompositeModelRoute{route}})
	group := &service.Group{ID: 91, Platform: service.PlatformComposite}
	ids, err := h.codexModelIDsForGroup(context.Background(), group, "")
	require.NoError(t, err)
	require.Equal(t, []string{"bps-prefix"}, ids)
	h = bpsCatalogHandler([]service.Account{bpsCatalogAccount()}, bpsCatalogRouteRepo{routes: []service.CompositeModelRoute{route, exact}})
	ids, err = h.codexModelIDsForGroup(context.Background(), group, "")
	require.NoError(t, err)
	require.Empty(t, ids)
	h = bpsCatalogHandler([]service.Account{bpsCatalogAccount()}, bpsCatalogRouteRepo{err: errors.New("database unavailable")})
	c, rec := bpsCatalogContext(group, "/v1/models")
	h.Models(c)
	require.Equal(t, 500, rec.Code)
	require.NotContains(t, rec.Body.String(), "claude-")
}

func TestOpenAIBPSModelsOpenAIShape(t *testing.T) {
	for _, mode := range []string{"default", "mapped", "allowlist", "retrieve"} {
		t.Run(mode, func(t *testing.T) {
			a := bpsCatalogAccount()
			a.Credentials["model_mapping"] = map[string]any{"my-astra": "gpt-6-astra"}
			accounts := []service.Account{a}
			if mode == "default" {
				accounts = nil
			}
			h := bpsCatalogHandler(accounts, bpsCatalogRouteRepo{})
			group := &service.Group{ID: 91, Platform: service.PlatformOpenAIBPS}
			if mode == "allowlist" {
				group.ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{"my-astra"}}
			}
			c, rec := bpsCatalogContext(group, "/v1/models")
			if mode == "retrieve" {
				c.Params = gin.Params{{Key: "model", Value: "my-astra"}}
			}
			h.Models(c)
			require.Equal(t, 200, rec.Code)
			var data map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &data))
			var item map[string]any
			if mode == "retrieve" {
				item = data
			} else {
				item = data["data"].([]any)[0].(map[string]any)
			}
			require.Equal(t, "model", item["object"])
			require.NotEmpty(t, item["owned_by"])
			require.NotZero(t, item["created"])
			require.NotContains(t, item, "created_at")
		})
	}
}

func TestOpenAIBPSModelsDeduplicateNormalizedAliases(t *testing.T) {
	first := bpsCatalogRoute()
	second := first
	second.ID = 2
	second.PublicModel = " bps-astra "
	h := bpsCatalogHandler([]service.Account{bpsCatalogAccount()}, bpsCatalogRouteRepo{routes: []service.CompositeModelRoute{first, second}})
	group := &service.Group{ID: 91, Platform: service.PlatformComposite}
	ids, err := h.codexModelIDsForGroup(context.Background(), group, "")
	require.NoError(t, err)
	require.Equal(t, []string{"bps-astra"}, ids)
}
