package governance

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// governedToolsFixture is a key holding one rate limit and a grant on DeepSeek, the provider
// that serves Search and Fetch.
func governedToolsFixture(t *testing.T) (*GovernancePlugin, GovernanceStore) {
	t.Helper()
	logger := NewMockLogger()

	rl := buildRateLimit("rl-vk", 1_000_000, 1_000_000)
	vk := buildVirtualKeyWithRateLimit("vk-tools", "sk-bf-tools", "Tools VK", rl)
	vk.ProviderConfigs = append(vk.ProviderConfigs, buildProviderConfig("deepseek", []string{"*"}))

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rl},
	}, nil, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)},
		logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin, store
}

func toolsUsage(cost float64) *schemas.BifrostLLMUsage {
	return &schemas.BifrostLLMUsage{Cost: &schemas.BifrostCost{TotalCost: cost}}
}

// Search and Fetch carry no model, so the "a model was named" admission never applies to them.
// Without their own rule a priced call reached the log and never the limits it was checked
// against: the key reported nothing spent while its traffic ran without bound. A call is
// charged when the provider reported usage on it, and an empty Search, which the provider
// does not bill, is not.
func TestAccounting_ModellessToolsCallsAreChargedWhenPriced(t *testing.T) {
	for _, tc := range []struct {
		name        string
		request     *schemas.BifrostRequest
		response    *schemas.BifrostResponse
		wantCharged bool
	}{
		{
			name: "search with results",
			request: &schemas.BifrostRequest{RequestType: schemas.SearchRequest,
				SearchRequest: &schemas.BifrostSearchRequest{Provider: schemas.DeepSeek, Query: "q"}},
			response: &schemas.BifrostResponse{SearchResponse: &schemas.BifrostSearchResponse{
				Results:     []schemas.BifrostSearchResult{{URL: "https://example.com"}},
				Usage:       toolsUsage(0.00075),
				ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.SearchRequest, Provider: schemas.DeepSeek},
			}},
			wantCharged: true,
		},
		{
			name: "fetch",
			request: &schemas.BifrostRequest{RequestType: schemas.FetchRequest,
				FetchRequest: &schemas.BifrostFetchRequest{Provider: schemas.DeepSeek, URL: "https://example.com"}},
			response: &schemas.BifrostResponse{FetchResponse: &schemas.BifrostFetchResponse{
				Content:     "# page",
				Usage:       toolsUsage(0.00075),
				ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.FetchRequest, Provider: schemas.DeepSeek},
			}},
			wantCharged: true,
		},
		{
			name: "search with no results",
			request: &schemas.BifrostRequest{RequestType: schemas.SearchRequest,
				SearchRequest: &schemas.BifrostSearchRequest{Provider: schemas.DeepSeek, Query: "q"}},
			response: &schemas.BifrostResponse{SearchResponse: &schemas.BifrostSearchResponse{
				Results:     []schemas.BifrostSearchResult{},
				ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.SearchRequest, Provider: schemas.DeepSeek},
			}},
			wantCharged: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plugin, store := governedToolsFixture(t)

			ctx := presentCtx("sk-bf-tools")
			_, shortCircuit, err := plugin.PreLLMHook(ctx, tc.request)
			require.NoError(t, err)
			require.Nil(t, shortCircuit, "the key permits this provider and can afford the request")

			_, _, err = plugin.PostLLMHook(ctx, tc.response, nil)
			require.NoError(t, err)
			settleAccounting(t, plugin)

			rateLimit := store.GetGovernanceData(context.Background()).RateLimits["rl-vk"]
			rateLimitIDs, _ := ctx.Value(schemas.BifrostContextKeyGovernanceRateLimitIDs).([]string)
			if tc.wantCharged {
				assert.Equal(t, int64(1), rateLimit.RequestCurrentUsage, "a priced call counts against the key that made it")
				assert.Contains(t, rateLimitIDs, "rl-vk", "the limits charged are recorded on the request for the log row")
			} else {
				assert.Equal(t, int64(0), rateLimit.RequestCurrentUsage, "a call the provider did not bill is not charged")
				assert.Empty(t, rateLimitIDs)
			}
		})
	}
}
