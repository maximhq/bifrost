package handlers

import (
	"encoding/json"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PUT /api/providers/{provider} is a partial update, not a full replace. A client that
// predates a nested config block omits it, and erasing saved settings on an unrelated
// update is data loss the caller never asked for. Omission means "leave this alone";
// an explicit null is the request that clears a block, and stays supported so a block
// added by mistake can still be removed through the API.
func decodeUpdate(t *testing.T, body string) (*providerUpdatePayload, map[string]json.RawMessage) {
	t.Helper()
	var payload providerUpdatePayload
	require.NoError(t, sonic.Unmarshal([]byte(body), &payload))
	var fields map[string]json.RawMessage
	require.NoError(t, sonic.Unmarshal([]byte(body), &fields))
	return &payload, fields
}

func savedConfig() configstore.ProviderConfig {
	return configstore.ProviderConfig{
		ProxyConfig:          &schemas.ProxyConfig{Type: schemas.HTTPProxy},
		CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.Anthropic},
		OpenAIConfig:         &schemas.OpenAIConfig{DisableStore: true},
		PromptCache:          &schemas.PromptCacheConfig{AutoInject: true, TTL: schemas.Ptr("1h")},
		InjectedTools: &schemas.InjectedToolsConfig{
			WebSearch: &schemas.InjectedToolRef{MCPClientName: "tavily", ToolName: "search"},
		},
	}
}

func TestApplyProviderConfigUpdates_OmittedBlocksArePreserved(t *testing.T) {
	config := savedConfig()
	// The shape an older client sends: it knows nothing of these blocks.
	payload, fields := decodeUpdate(t, `{"network_config":{},"concurrency_and_buffer_size":{"concurrency":1,"buffer_size":2}}`)

	applyProviderConfigUpdates(&config, payload, fields)

	assert.NotNil(t, config.ProxyConfig, "an omitted proxy_config must survive an unrelated update")
	assert.NotNil(t, config.CustomProviderConfig, "an omitted custom_provider_config must survive")
	assert.NotNil(t, config.OpenAIConfig, "an omitted openai_config must survive")
	require.NotNil(t, config.PromptCache, "an omitted prompt_cache must survive")
	assert.True(t, config.PromptCache.AutoInject)
	require.NotNil(t, config.PromptCache.TTL)
	assert.Equal(t, "1h", *config.PromptCache.TTL)
	require.NotNil(t, config.InjectedTools, "an omitted injected_tools must survive")
	assert.Equal(t, "tavily", config.InjectedTools.WebSearch.MCPClientName)
}

func TestApplyProviderConfigUpdates_ExplicitNullClears(t *testing.T) {
	config := savedConfig()
	payload, fields := decodeUpdate(t, `{"proxy_config":null,"custom_provider_config":null,"openai_config":null,"prompt_cache":null,"injected_tools":null}`)

	applyProviderConfigUpdates(&config, payload, fields)

	assert.Nil(t, config.ProxyConfig, "an explicit null is how a block is cleared")
	assert.Nil(t, config.CustomProviderConfig)
	assert.Nil(t, config.OpenAIConfig)
	assert.Nil(t, config.PromptCache)
	assert.Nil(t, config.InjectedTools)
}

func TestApplyProviderConfigUpdates_PresentBlocksAreReplaced(t *testing.T) {
	config := savedConfig()
	payload, fields := decodeUpdate(t, `{"prompt_cache":{"auto_inject":false}}`)

	applyProviderConfigUpdates(&config, payload, fields)

	require.NotNil(t, config.PromptCache)
	assert.False(t, config.PromptCache.AutoInject, "a supplied block replaces the saved one wholesale")
	assert.Nil(t, config.PromptCache.TTL, "replacement is not a field-level merge")
	assert.NotNil(t, config.ProxyConfig, "the blocks this request did not mention are untouched")
}

func TestApplyProviderConfigUpdates_InjectedToolsReplaced(t *testing.T) {
	config := savedConfig()
	payload, fields := decodeUpdate(t, `{"injected_tools":{"web_search":{"mcp_client_name":"exa","tool_name":"web_search_exa"}}}`)

	applyProviderConfigUpdates(&config, payload, fields)

	require.NotNil(t, config.InjectedTools)
	require.NotNil(t, config.InjectedTools.WebSearch)
	assert.Equal(t, schemas.InjectedToolRef{MCPClientName: "exa", ToolName: "web_search_exa"}, *config.InjectedTools.WebSearch)
	assert.NotNil(t, config.PromptCache, "the blocks this request did not mention are untouched")
}
