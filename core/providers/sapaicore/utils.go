package sapaicore

import (
	"fmt"
	"strings"
	"time"
)

// defaultDeploymentCacheTTL is the default TTL for deployment cache entries
const defaultDeploymentCacheTTL = 1 * time.Hour

// minDeploymentCacheTTL is the minimum allowed TTL for deployment cache entries
const minDeploymentCacheTTL = 1 * time.Minute

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
