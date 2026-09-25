package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// CompleteCompositeBPSModelCatalog adds only explicitly routed BPS models to
// the shared HTTP/Codex catalog. A non-nil empty slice disables static fallback
// when this group has configured BPS accounts or routes but no usable models.
func (s *GatewayService) CompleteCompositeBPSModelCatalog(ctx context.Context, groupID *int64, existing []string) ([]string, error) {
	if groupID == nil || s == nil || s.accountRepo == nil {
		if len(existing) == 0 {
			return nil, nil
		}
		return existing, nil
	}
	accounts, err := s.accountRepo.ListByGroup(ctx, *groupID)
	if err != nil {
		return nil, fmt.Errorf("load composite catalog accounts: %w", err)
	}
	configured := false
	candidates := map[string]bool{}
	addCandidate := func(model string, alreadyListed bool) {
		model = strings.TrimSpace(model)
		if model != "" {
			candidates[model] = candidates[model] || alreadyListed
		}
	}
	for _, model := range existing {
		addCandidate(model, true)
	}
	for _, account := range accounts {
		if !account.IsOpenAIBPS() {
			continue
		}
		configured = true
		mapping := account.GetModelMapping()
		if len(mapping) == 0 {
			for _, model := range OpenAIBPSDefaultModels() {
				addCandidate(model, false)
			}
		}
		for model := range mapping {
			if strings.ContainsAny(model, "*?") {
				continue
			}
			addCandidate(model, false)
		}
	}
	var routes []CompositeModelRoute
	if s.compositeResolver != nil && s.compositeResolver.repo != nil {
		all, err := s.compositeResolver.repo.ListByGroup(ctx, *groupID, true)
		if err != nil {
			return nil, fmt.Errorf("load composite catalog routes: %w", err)
		}
		for _, route := range all {
			if route.TargetPlatform == PlatformOpenAIBPS {
				configured = true
			}
			if !route.Enabled {
				continue
			}
			routes = append(routes, route)
			endpoint := normalizeCompositeRouteEndpoint(route.Endpoint)
			if route.TargetPlatform == PlatformOpenAIBPS && normalizeCompositeRouteMatchType(route.MatchType) == CompositeRouteMatchExact && (endpoint == CompositeRouteEndpointAny || endpoint == CompositeRouteEndpointResponses) {
				addCandidate(route.PublicModel, false)
			}
		}
	}
	if !configured {
		if len(existing) == 0 {
			return nil, nil
		}
		return existing, nil
	}
	models := make([]string, 0, len(candidates))
	for model, wasListed := range candidates {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		route, matched := matchCompositeRoute(routes, model, CompositeRouteEndpointResponses)
		if !matched || route.TargetPlatform != PlatformOpenAIBPS {
			if wasListed {
				models = append(models, model)
			}
			continue
		}
		target := strings.TrimSpace(route.UpstreamModel)
		if target == "" {
			target = model
		}
		for i := range accounts {
			a := &accounts[i]
			if a.Type == AccountTypeOAuth && isOpenAICompatibleAccountEligibleForRequestBeforeProfit(ctx, a, PlatformOpenAIBPS, target, false, OpenAIEndpointCapabilityResponses) {
				models = append(models, model)
				break
			}
		}
	}
	sort.Strings(models)
	return models, nil
}
