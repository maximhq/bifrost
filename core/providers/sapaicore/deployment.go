package sapaicore

import (
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// DeploymentCache manages deployment ID resolution and caching
type DeploymentCache struct {
	mu          sync.Mutex
	deployments map[string]*cachedDeployments // keyed by resource group + base URL
	client      *fasthttp.Client
	tokenCache  *TokenCache
	ttl         time.Duration
	timeout     time.Duration
}

// cachedDeployments holds cached deployment data for a resource group
type cachedDeployments struct {
	modelToDeployment map[string]SAPAICoreCachedDeployment // model name -> deployment info
	fetchedAt         time.Time
}

// newDeploymentCache creates a new deployment cache with default TTL
func newDeploymentCache(client *fasthttp.Client, tokenCache *TokenCache) *DeploymentCache {
	return newDeploymentCacheWithTTL(client, tokenCache, defaultDeploymentCacheTTL)
}

// newDeploymentCacheWithTTL creates a new deployment cache with a custom TTL.
// TTL values less than MinDeploymentCacheTTL will be clamped to the minimum.
func newDeploymentCacheWithTTL(client *fasthttp.Client, tokenCache *TokenCache, ttl time.Duration) *DeploymentCache {
	if ttl <= 0 {
		ttl = defaultDeploymentCacheTTL
	} else if ttl < minDeploymentCacheTTL {
		ttl = minDeploymentCacheTTL
	}
	return &DeploymentCache{
		deployments: make(map[string]*cachedDeployments),
		client:      client,
		tokenCache:  tokenCache,
		ttl:         ttl,
		timeout:     tokenCache.timeout,
	}
}

// GetDeploymentID resolves a model name to a deployment ID
// First checks static deployments map from config, then falls back to auto-resolution
func (dc *DeploymentCache) GetDeploymentID(
	modelName string,
	staticDeployments map[string]string,
	clientID, clientSecret, authURL, baseURL, resourceGroup string,
) (string, SAPAICoreBackendType, *schemas.BifrostError) {
	// Check static deployments first
	if staticDeployments != nil {
		if deploymentID, ok := staticDeployments[modelName]; ok {
			backend := determineBackend(modelName)
			return deploymentID, backend, nil
		}
	}

	// Auto-resolve from deployments API
	return dc.resolveDeployment(modelName, clientID, clientSecret, authURL, baseURL, resourceGroup)
}

// resolveDeployment fetches and caches deployments, then returns the deployment ID for the model.
// Concurrent callers are serialized by the mutex; the first one to find
// a cache miss fetches deployments and subsequent callers see the cached result.
func (dc *DeploymentCache) resolveDeployment(
	modelName, clientID, clientSecret, authURL, baseURL, resourceGroup string,
) (string, SAPAICoreBackendType, *schemas.BifrostError) {
	cacheKey := deploymentCacheKey(clientID, authURL, baseURL, resourceGroup)

	dc.mu.Lock()
	defer dc.mu.Unlock()

	// Check cache
	if cached, ok := dc.deployments[cacheKey]; ok {
		if time.Since(cached.fetchedAt) < dc.ttl {
			if deployment, ok := cached.modelToDeployment[modelName]; ok {
				return deployment.DeploymentID, deployment.Backend, nil
			}
			// Model not found in fresh cache — invalidate to force a refetch
			// so newly created deployments are discoverable before TTL expires.
			delete(dc.deployments, cacheKey)
		}
	}

	// Opportunistic cleanup: prune expired entries during cache misses
	dc.pruneExpiredLocked()

	// Fetch deployments from API
	deployments, fetchErr := dc.fetchDeployments(clientID, clientSecret, authURL, baseURL, resourceGroup)
	if fetchErr != nil {
		return "", "", fetchErr
	}

	// Cache the results
	dc.deployments[cacheKey] = &cachedDeployments{
		modelToDeployment: deployments,
		fetchedAt:         time.Now(),
	}

	// Look up the requested model
	if deployment, ok := deployments[modelName]; ok {
		return deployment.DeploymentID, deployment.Backend, nil
	}

	return "", "", providerUtils.NewBifrostOperationError(
		fmt.Sprintf("no running deployment found for model: %s", modelName),
		fmt.Errorf("model not deployed"),
		schemas.SAPAICore,
	)
}

// fetchDeployments retrieves all running deployments from SAP AI Core
func (dc *DeploymentCache) fetchDeployments(
	clientID, clientSecret, authURL, baseURL, resourceGroup string,
) (map[string]SAPAICoreCachedDeployment, *schemas.BifrostError) {
	// Get auth token
	token, tokenErr := dc.tokenCache.GetToken(clientID, clientSecret, authURL)
	if tokenErr != nil {
		return nil, tokenErr
	}

	// Ensure baseURL has /v2 suffix
	normalizedURL := normalizeBaseURL(baseURL)

	// Build request URL
	deploymentsURL := fmt.Sprintf("%s/lm/deployments?status=RUNNING&resourceGroup=%s", normalizedURL, url.QueryEscape(resourceGroup))

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(deploymentsURL)
	req.Header.SetMethod(fasthttp.MethodGet)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("AI-Resource-Group", resourceGroup)

	if err := dc.client.DoTimeout(req, resp, dc.timeout); err != nil {
		return nil, providerUtils.NewBifrostOperationError(
			"failed to fetch deployments from SAP AI Core",
			err,
			schemas.SAPAICore,
		)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.NewBifrostOperationError(
			fmt.Sprintf("deployments request failed with status %d", resp.StatusCode()),
			fmt.Errorf("http %d", resp.StatusCode()),
			schemas.SAPAICore,
		)
	}

	var deploymentsResp SAPAICoreDeploymentsResponse
	if err := sonic.Unmarshal(resp.Body(), &deploymentsResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(
			"failed to parse deployments response",
			err,
			schemas.SAPAICore,
		)
	}

	// Build model -> deployment mapping (keep first seen for deterministic selection)
	result := make(map[string]SAPAICoreCachedDeployment)
	for _, res := range deploymentsResp.Resources {
		if res.Status != SAPAICoreDeploymentStatusRunning {
			continue
		}
		modelName := res.Details.Resources.SAPAICoreBackendDetails.Model.Name
		if modelName == "" {
			continue
		}
		if _, exists := result[modelName]; !exists {
			result[modelName] = SAPAICoreCachedDeployment{
				DeploymentID: res.ID,
				ModelName:    modelName,
				Backend:      determineBackend(modelName),
			}
		}
	}

	return result, nil
}

// ListModels retrieves all available models from running deployments
func (dc *DeploymentCache) ListModels(
	clientID, clientSecret, authURL, baseURL, resourceGroup string,
) ([]SAPAICoreModel, *schemas.BifrostError) {
	// Get auth token
	token, tokenErr := dc.tokenCache.GetToken(clientID, clientSecret, authURL)
	if tokenErr != nil {
		return nil, tokenErr
	}

	// Ensure baseURL has /v2 suffix
	normalizedURL := normalizeBaseURL(baseURL)

	// Build request URL
	deploymentsURL := fmt.Sprintf("%s/lm/deployments?status=RUNNING&resourceGroup=%s", normalizedURL, url.QueryEscape(resourceGroup))

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(deploymentsURL)
	req.Header.SetMethod(fasthttp.MethodGet)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("AI-Resource-Group", resourceGroup)

	if err := dc.client.DoTimeout(req, resp, dc.timeout); err != nil {
		return nil, providerUtils.NewBifrostOperationError(
			"failed to fetch deployments for model listing",
			err,
			schemas.SAPAICore,
		)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.NewBifrostOperationError(
			fmt.Sprintf("model listing request failed with status %d", resp.StatusCode()),
			fmt.Errorf("http %d", resp.StatusCode()),
			schemas.SAPAICore,
		)
	}

	var deploymentsResp SAPAICoreDeploymentsResponse
	if err := sonic.Unmarshal(resp.Body(), &deploymentsResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(
			"failed to parse deployments response for model listing",
			err,
			schemas.SAPAICore,
		)
	}

	// Build unique models list
	modelSet := make(map[string]SAPAICoreModel)
	for _, res := range deploymentsResp.Resources {
		if res.Status != SAPAICoreDeploymentStatusRunning {
			continue
		}
		modelName := res.Details.Resources.SAPAICoreBackendDetails.Model.Name
		if modelName == "" {
			continue
		}
		if _, exists := modelSet[modelName]; !exists {
			modelSet[modelName] = SAPAICoreModel{
				ID:           modelName,
				Name:         modelName,
				DeploymentID: res.ID,
			}
		}
	}

	// Convert to slice
	models := make([]SAPAICoreModel, 0, len(modelSet))
	for _, model := range modelSet {
		models = append(models, model)
	}

	return models, nil
}

// ClearCache clears the deployment cache for a specific credential + resource group scope.
// If all parameters are empty, clears the entire cache.
func (dc *DeploymentCache) ClearCache(clientID, authURL, baseURL, resourceGroup string) {
	dc.mu.Lock()
	defer dc.mu.Unlock()

	if clientID == "" && authURL == "" && baseURL == "" && resourceGroup == "" {
		// Clear all cache entries
		dc.deployments = make(map[string]*cachedDeployments)
		return
	}

	cacheKey := deploymentCacheKey(clientID, authURL, baseURL, resourceGroup)
	delete(dc.deployments, cacheKey)
}

// Cleanup removes all expired entries from the deployment cache.
func (dc *DeploymentCache) Cleanup() {
	dc.mu.Lock()
	defer dc.mu.Unlock()

	now := time.Now()
	for key, cached := range dc.deployments {
		if now.Sub(cached.fetchedAt) >= dc.ttl {
			delete(dc.deployments, key)
		}
	}
}

// pruneExpiredLocked removes expired entries from the deployment cache.
// Must be called while dc.mu is held.
func (dc *DeploymentCache) pruneExpiredLocked() {
	now := time.Now()
	for key, cached := range dc.deployments {
		if now.Sub(cached.fetchedAt) >= dc.ttl {
			delete(dc.deployments, key)
		}
	}
}
