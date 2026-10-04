package handler

import (
	"net/http"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOrdinaryPinnedModelsCodexGatewayCannotUseLegacyAccounts(t *testing.T) {
	upstream := &codexModelsPinnedHTTPUpstream{bodies: map[int64]string{1: `{"data":[{"id":"legacy-model"}]}`}}
	h := newPinnedCodexTestHandler([]service.Account{newPinnedCodexAccount(1, service.StatusActive, true, false)}, upstream, 3)
	group := &service.Group{ID: 91, Platform: service.PlatformOpenAICodex,
		CodexModelsManifestConfig: service.GroupCodexModelsManifestConfig{Enabled: true, AccountIDs: []int64{1}}}
	response := performOrdinaryPinnedModelsRequest(t, h, group, "/v1/models", "")
	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "No available OpenAI model discovery accounts")
	require.Empty(t, upstream.accountIDs())
}
