package integrations

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// findListModelsRoute locates the /v1/models RouteConfig from CreateOpenAIListModelsRouteConfigs.
func findListModelsRoute(t *testing.T) *RouteConfig {
	t.Helper()
	routes := CreateOpenAIListModelsRouteConfigs("/openai", &mockHandlerStore{allowDirectKeys: true})
	for i := range routes {
		if routes[i].Path == "/openai/v1/models" {
			return &routes[i]
		}
	}
	t.Fatal("could not find /openai/v1/models route")
	return nil
}

// makeListModelsReq returns a BifrostListModelsRequest with an optional provider preset.
func makeListModelsReq(provider schemas.ModelProvider) *schemas.BifrostListModelsRequest {
	r := &schemas.BifrostListModelsRequest{}
	r.Provider = provider
	return r
}

// applyPreCallback runs the route's PreCallback (if any) against a synthetic fasthttp.RequestCtx
// built with the given User-Agent, then returns the resulting BifrostContext.
func applyPreCallback(t *testing.T, route *RouteConfig, userAgent string) (*fasthttp.RequestCtx, *schemas.BifrostContext) {
	t.Helper()
	httpCtx := &fasthttp.RequestCtx{}
	if userAgent != "" {
		httpCtx.Request.Header.Set("User-Agent", userAgent)
	}
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if route.PreCallback != nil {
		req := route.GetRequestTypeInstance(context.Background())
		err := route.PreCallback(httpCtx, bifrostCtx, req)
		require.NoError(t, err)
	}
	return httpCtx, bifrostCtx
}

// TestListModelsRequestConverter_DefaultsToOpenAI verifies that, for a non-Azure caller,
// the RequestConverter sets Provider to OpenAI when no provider was pre-set.
func TestListModelsRequestConverter_DefaultsToOpenAI(t *testing.T) {
	route := findListModelsRoute(t)
	require.NotNil(t, route.RequestConverter, "RequestConverter must be set")

	_, bifrostCtx := applyPreCallback(t, route, "")

	req := makeListModelsReq("")
	bifrostReq, err := route.RequestConverter(bifrostCtx, req)

	require.NoError(t, err)
	require.NotNil(t, bifrostReq)
	require.NotNil(t, bifrostReq.ListModelsRequest)
	assert.Equal(t, schemas.OpenAI, bifrostReq.ListModelsRequest.Provider,
		"non-Azure caller must default to OpenAI provider")
}

// TestListModelsRequestConverter_DefaultsToAzureForAzureSDK verifies that when the
// Azure OpenAI SDK User-Agent is present, Provider defaults to Azure.
func TestListModelsRequestConverter_DefaultsToAzureForAzureSDK(t *testing.T) {
	route := findListModelsRoute(t)
	require.NotNil(t, route.PreCallback, "PreCallback must be set for Azure SDK detection")
	require.NotNil(t, route.RequestConverter, "RequestConverter must be set")

	_, bifrostCtx := applyPreCallback(t, route, "python-httpx/0.27.0 AzureOpenAI/1.35.0")

	req := makeListModelsReq("")
	bifrostReq, err := route.RequestConverter(bifrostCtx, req)

	require.NoError(t, err)
	require.NotNil(t, bifrostReq)
	require.NotNil(t, bifrostReq.ListModelsRequest)
	assert.Equal(t, schemas.Azure, bifrostReq.ListModelsRequest.Provider,
		"Azure SDK User-Agent must yield Azure provider")
}

// TestListModelsRequestConverter_RespectsExplicitProvider verifies that an explicitly set
// provider on the request is preserved and not overwritten.
func TestListModelsRequestConverter_RespectsExplicitProvider(t *testing.T) {
	route := findListModelsRoute(t)

	_, bifrostCtx := applyPreCallback(t, route, "")

	req := makeListModelsReq(schemas.Anthropic)
	bifrostReq, err := route.RequestConverter(bifrostCtx, req)

	require.NoError(t, err)
	require.NotNil(t, bifrostReq)
	assert.Equal(t, schemas.Anthropic, bifrostReq.ListModelsRequest.Provider,
		"an explicitly set provider must not be overwritten")
}

// TestListModelsRequestConverter_InvalidReqReturnsError verifies that passing a non-list-models
// request type returns an error.
func TestListModelsRequestConverter_InvalidReqReturnsError(t *testing.T) {
	route := findListModelsRoute(t)

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, err := route.RequestConverter(bifrostCtx, "not-a-list-models-request")

	assert.Error(t, err)
}

// TestListModelsPreCallback_NonAzureUADoesNotSetAzureFlag ensures the PreCallback
// does not mark non-Azure requests as Azure.
func TestListModelsPreCallback_NonAzureUADoesNotSetAzureFlag(t *testing.T) {
	route := findListModelsRoute(t)
	require.NotNil(t, route.PreCallback)

	_, bifrostCtx := applyPreCallback(t, route, "python-httpx/0.27.0 openai/1.35.0")

	isAzure, _ := bifrostCtx.Value(schemas.BifrostContextKeyIsAzureUserAgent).(bool)
	assert.False(t, isAzure, "non-Azure User-Agent must not set the Azure flag")
}

// TestListModelsPreCallback_AzureSetsFlag confirms the PreCallback sets the Azure flag
// for an Azure SDK User-Agent.
func TestListModelsPreCallback_AzureSetsFlag(t *testing.T) {
	route := findListModelsRoute(t)
	require.NotNil(t, route.PreCallback)

	_, bifrostCtx := applyPreCallback(t, route, "AzureOpenAI/1.35.0")

	isAzure, ok := bifrostCtx.Value(schemas.BifrostContextKeyIsAzureUserAgent).(bool)
	assert.True(t, ok && isAzure, "Azure SDK User-Agent must set the Azure flag in BifrostContext")
}

// TestListModelsRouteConfig_AllPathsRegistered verifies the three expected path variants exist.
func TestListModelsRouteConfig_AllPathsRegistered(t *testing.T) {
	routes := CreateOpenAIListModelsRouteConfigs("/openai", &mockHandlerStore{allowDirectKeys: true})

	paths := make(map[string]bool)
	for _, r := range routes {
		paths[r.Path] = true
	}

	for _, expected := range []string{"/openai/v1/models", "/openai/models", "/openai/openai/models"} {
		assert.True(t, paths[expected], "expected path %s to be registered", expected)
	}
}

// TestListModelsRequestConverter_BifrostRequestIsPopulated is an integration-style test
// that verifies the BifrostRequest returned from the converter carries a populated
// ListModelsRequest (i.e., the Provider field is set and not empty).
func TestListModelsRequestConverter_BifrostRequestIsPopulated(t *testing.T) {
	route := findListModelsRoute(t)

	_, bifrostCtx := applyPreCallback(t, route, "")
	req := makeListModelsReq("")
	bifrostReq, err := route.RequestConverter(bifrostCtx, req)

	require.NoError(t, err)
	require.NotNil(t, bifrostReq, "BifrostRequest must not be nil")
	require.NotNil(t, bifrostReq.ListModelsRequest, "ListModelsRequest must be set on BifrostRequest")
	assert.NotEmpty(t, bifrostReq.ListModelsRequest.Provider,
		"Provider must be populated after conversion (no fan-out)")
}
