package vertex

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func getRequestBodyForAnthropicResponses(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, deployment string, providerName schemas.ModelProvider, isStreaming bool) ([]byte, *schemas.BifrostError) {
	var jsonBody []byte
	var err error

	// Check if raw request body should be used
	if useRawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && useRawBody {
		jsonBody = request.GetRawRequestBody()
		// Unmarshal and check if model and region are present
		var requestBody map[string]interface{}
		if err := sonic.Unmarshal(jsonBody, &requestBody); err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, fmt.Errorf("failed to unmarshal request body: %w", err), providerName)
		}
		// Add max_tokens if not present
		if _, exists := requestBody["max_tokens"]; !exists {
			requestBody["max_tokens"] = anthropic.AnthropicDefaultMaxTokens
		}
		delete(requestBody, "model")
		delete(requestBody, "region")
		delete(requestBody, "fallbacks")
		// Add anthropic_version if not present
		if _, exists := requestBody["anthropic_version"]; !exists {
			requestBody["anthropic_version"] = DefaultVertexAnthropicVersion
		}
		// Add stream if not present
		if isStreaming {
			requestBody["stream"] = true
		}
		jsonBody, err = sonic.Marshal(requestBody)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
		}
	} else {
		// Convert request to Anthropic format
		request.Model = deployment
		reqBody, err := anthropic.ToAnthropicResponsesRequest(ctx, request)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err, providerName)
		}
		if reqBody == nil {
			return nil, providerUtils.NewBifrostOperationError("request body is not provided", nil, providerName)
		}

		if isStreaming {
			reqBody.Stream = schemas.Ptr(true)
		}

		// Convert struct to map for Vertex API
		reqBytes, err := sonic.Marshal(reqBody)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, fmt.Errorf("failed to marshal request body: %w", err), providerName)
		}

		var requestBody map[string]interface{}
		if err := sonic.Unmarshal(reqBytes, &requestBody); err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, fmt.Errorf("failed to unmarshal request body: %w", err), providerName)
		}

		// Add anthropic_version if not present
		if _, exists := requestBody["anthropic_version"]; !exists {
			requestBody["anthropic_version"] = DefaultVertexAnthropicVersion
		}

		// Remove fields not needed by Vertex API
		delete(requestBody, "model")
		delete(requestBody, "region")

		jsonBody, err = sonic.Marshal(requestBody)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
		}
	}

	return jsonBody, nil
}

// getCompleteURLForGeminiEndpoint constructs the complete URL for the Gemini endpoint, for both streaming and non-streaming requests
// for custom/fine-tuned models, it uses the projectNumber
// for gemini models, it uses the projectID
func getCompleteURLForGeminiEndpoint(deployment string, region string, projectID string, projectNumber string, method string) string {
	var url string
	if schemas.IsAllDigitsASCII(deployment) {
		// Custom/fine-tuned models use projectNumber
		if region == "global" {
			url = fmt.Sprintf("https://aiplatform.googleapis.com/v1beta1/projects/%s/locations/global/endpoints/%s%s", projectNumber, deployment, method)
		} else {
			url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/endpoints/%s%s", region, projectNumber, region, deployment, method)
		}
	} else {
		// Gemini models use projectID
		if region == "global" {
			url = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/google/models/%s%s", projectID, deployment, method)
		} else {
			url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s%s", region, projectID, region, deployment, method)
		}
	}
	return url
}
