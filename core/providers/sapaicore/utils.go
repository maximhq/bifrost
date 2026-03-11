package sapaicore

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// SAPAICoreAuthorizationTokenKey is the context key for passing a pre-fetched SAP AI Core token.
const SAPAICoreAuthorizationTokenKey schemas.BifrostContextKey = "sapaicore-authorization-token"

// defaultCleanupInterval is the interval at which the background goroutine
// prunes expired entries from the token and deployment caches.
const defaultCleanupInterval = 5 * time.Minute

// defaultDeploymentCacheTTL is the default TTL for deployment cache entries
const defaultDeploymentCacheTTL = 1 * time.Hour

// minDeploymentCacheTTL is the minimum allowed TTL for deployment cache entries
const minDeploymentCacheTTL = 1 * time.Minute

// openaiReasoningAndGpt5Models is the list of OpenAI models that require special parameter handling.
// These models don't support max_tokens and temperature parameters when accessed via SAP AI Core.
var openaiReasoningAndGpt5Models = []string{
	"o1",
	"o3-mini",
	"o3",
	"o4-mini",
	"gpt-5",
}

// isOpenaiReasoningOrGpt5Model checks if the model requires special parameter handling.
// These models don't support max_tokens and temperature parameters when accessed via SAP AI Core.
func isOpenaiReasoningOrGpt5Model(model string) bool {
	modelLower := strings.ToLower(model)
	for _, rm := range openaiReasoningAndGpt5Models {
		if strings.Contains(modelLower, rm) {
			return true
		}
	}
	return false
}

// releaseStreamingResponseNoDrain releases a streaming response without draining the body stream.
// Use this for binary EventStream protocols (like AWS EventStream) where:
// 1. The stream has been fully consumed up to io.EOF
// 2. Draining would block because the protocol doesn't send additional data after the final event
// This skips the drain step that can cause the connection to hang on certain streaming protocols.
func releaseStreamingResponseNoDrain(resp *fasthttp.Response, logger schemas.Logger) {
	if bodyStream := resp.BodyStream(); bodyStream != nil {
		if closer, ok := bodyStream.(io.Closer); ok {
			if err := closer.Close(); err != nil {
				logger.Warn("failed to close streaming response body: %v", err)
			}
		}
	}
	fasthttp.ReleaseResponse(resp)
}

// deploymentCacheKey generates a unique key for deployment cache.
// Includes clientID and authURL so that different credential sets sharing the
// same baseURL and resourceGroup are isolated in the cache and singleflight.
// Uses length-prefixed format to avoid collisions when values contain ":"
// The baseURL is normalized before use so that "https://host", "https://host/",
// and "https://host/v2" all map to the same cache entry.
func deploymentCacheKey(clientID, authURL, baseURL, resourceGroup string) string {
	normalizedBase := normalizeBaseURL(baseURL)
	normalizedAuth := normalizeAuthURL(authURL)
	return fmt.Sprintf("%d:%s:%d:%s:%d:%s:%s",
		len(clientID), clientID,
		len(normalizedAuth), normalizedAuth,
		len(normalizedBase), normalizedBase,
		resourceGroup)
}

// determineBackend determines the backend type based on model name prefix
func determineBackend(modelName string) SAPAICoreBackendType {
	if strings.HasPrefix(modelName, "anthropic--") || strings.HasPrefix(modelName, "amazon--") {
		return SAPAICoreBackendBedrock
	}
	if strings.HasPrefix(modelName, "gemini-") {
		return SAPAICoreBackendVertex
	}
	return SAPAICoreBackendOpenAI
}

// normalizeBaseURL ensures the base URL has the /v2 suffix
func normalizeBaseURL(baseURL string) string {
	trimmed := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(trimmed, "/v2") {
		return trimmed
	}
	return trimmed + "/v2"
}
