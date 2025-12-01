package vertex

import (
	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func parseVertexError(providerName schemas.ModelProvider, resp *fasthttp.Response) *schemas.BifrostError {
	var openAIErr schemas.BifrostError
	var vertexErr []VertexError
	if err := sonic.Unmarshal(resp.Body(), &openAIErr); err != nil || openAIErr.Error == nil {
		// Try Vertex error format if OpenAI format fails or is incomplete
		if err := sonic.Unmarshal(resp.Body(), &vertexErr); err != nil {
			//try with single Vertex error format
			var vertexErr VertexError
			if err := sonic.Unmarshal(resp.Body(), &vertexErr); err != nil {
				// Try VertexValidationError format (validation errors from Mistral endpoint)
				var validationErr VertexValidationError
				if err := sonic.Unmarshal(resp.Body(), &validationErr); err != nil {
					return providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
				}
				if len(validationErr.Detail) > 0 {
					return providerUtils.NewProviderAPIError(validationErr.Detail[0].Msg, nil, resp.StatusCode(), providerName, nil, nil)
				}
				return providerUtils.NewProviderAPIError("Unknown error", nil, resp.StatusCode(), providerName, nil, nil)
			}
			return providerUtils.NewProviderAPIError(vertexErr.Error.Message, nil, resp.StatusCode(), providerName, nil, nil)
		}
		if len(vertexErr) > 0 {
			return providerUtils.NewProviderAPIError(vertexErr[0].Error.Message, nil, resp.StatusCode(), providerName, nil, nil)
		}
		return providerUtils.NewProviderAPIError("Unknown error", nil, resp.StatusCode(), providerName, nil, nil)
	}
	// OpenAI error format succeeded with valid Error field
	return providerUtils.NewProviderAPIError(openAIErr.Error.Message, nil, resp.StatusCode(), providerName, nil, nil)
}
