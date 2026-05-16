package azure

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
)

// getRequestBodyForAnthropicResponses serializes a BifrostResponsesRequest into the Anthropic wire format for Azure.
// It delegates to BuildAnthropicResponsesRequestBody with the Azure provider and the target deployment name.
func getRequestBodyForAnthropicResponses(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, deployment string, isStreaming bool, shouldSendBackRawRequest bool, shouldSendBackRawResponse bool) ([]byte, *schemas.BifrostError) {
	return anthropic.BuildAnthropicResponsesRequestBody(ctx, request, anthropic.AnthropicRequestBuildConfig{
		Provider:                  schemas.Azure,
		Deployment:                deployment,
		IsStreaming:               isStreaming,
		ValidateTools:             true,
		ShouldSendBackRawRequest:  shouldSendBackRawRequest,
		ShouldSendBackRawResponse: shouldSendBackRawResponse,
	})
}

// getAzureScopes returns the configured scopes or the default scope if none are valid.
// It filters out empty/whitespace-only strings.
func getAzureScopes(configuredScopes []string) []string {
	scopes := []string{DefaultAzureScope}
	if len(configuredScopes) > 0 {
		cleaned := make([]string, 0, len(configuredScopes))
		for _, s := range configuredScopes {
			if strings.TrimSpace(s) != "" {
				cleaned = append(cleaned, strings.TrimSpace(s))
			}
		}
		if len(cleaned) > 0 {
			scopes = cleaned
		}
	}
	return scopes
}
