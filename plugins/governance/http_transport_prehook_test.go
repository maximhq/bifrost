package governance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/stretchr/testify/require"
)

// TestHTTPTransportPreHook_VirtualKeyReplicateRefinesNestedModel verifies that
// virtual-key provider pinning rewrites the request model to Replicate's nested provider slug.
func TestHTTPTransportPreHook_VirtualKeyReplicateRefinesNestedModel(t *testing.T) {
	logger := NewMockLogger()
	mc := modelcatalog.NewTestCatalog(map[string]string{
		"openai/gpt-5-nano": "gpt-5-nano",
	})
	mc.UpsertModelDataForProvider(schemas.Replicate, &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{
			{ID: "replicate/openai/gpt-5-nano"},
		},
	}, nil, nil)

	virtualKey := buildVirtualKeyWithProviders(
		"vk1",
		"sk-bf-test",
		"replicate-only",
		[]configstoreTables.TableVirtualKeyProviderConfig{
			buildProviderConfig("replicate", nil),
		},
	)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*virtualKey},
	}, mc)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, mc, nil, nil)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = "POST"
	req.Path = "/v1/chat/completions"
	req.Headers["Authorization"] = "Bearer sk-bf-test"
	req.Headers["Content-Type"] = "application/json"
	req.Body = []byte(`{"model":"gpt-5-nano","messages":[{"role":"user","content":"Hello!"}]}`)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := plugin.HTTPTransportPreHook(bfCtx, req)
	require.NoError(t, err)
	require.Nil(t, resp)

	var payload struct {
		Model string `json:"model"`
	}
	require.NoError(t, json.Unmarshal(req.Body, &payload))
	require.Equal(t, "replicate/openai/gpt-5-nano", payload.Model)
}
