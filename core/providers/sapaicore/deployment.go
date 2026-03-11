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
	"golang.org/x/sync/singleflight"
)

// DeploymentCache manages deployment ID resolution and caching
type DeploymentCache struct {
	mu          sync.RWMutex
	deployments map[string]*cachedDeployments // keyed by resource group + base URL
	client      *fasthttp.Client
	tokenCache  *TokenCache
	ttl         time.Duration
	group       singleflight.Group
}

// cachedDeployments holds cached deployment data for a resource group
type cachedDeployments struct {
	modelToDeployment map[string]SAPAICoreCachedDeployment // model name -> deployment info
	fetchedAt         time.Time
}

// deploymentFetchResult carries the fetch result for singleflight return.
// Since BifrostError doesn't implement the error interface, we pass it through the result.
type deploymentFetchResult struct {
	deployments map[string]SAPAICoreCachedDeployment
	bifError    *schemas.BifrostError
}

// NewDeploymentCache creates a new deployment cache with default TTL
func NewDeploymentCache(client *fasthttp.Client, tokenCache *TokenCache) *DeploymentCache {
	return NewDeploymentCacheWithTTL(client, tokenCache, defaultDeploymentCacheTTL)
}

// NewDeploymentCacheWithTTL creates a new deployment cache with a custom TTL.
// TTL values less than MinDeploymentCacheTTL will be clamped to the minimum.
func NewDeploymentCacheWithTTL(client *fasthttp.Client, tokenCache *TokenCache, ttl time.Duration) *DeploymentCache {
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
// Uses singleflight to coalesce concurrent refresh requests for the same cache key.
func (dc *DeploymentCache) resolveDeployment(
	modelName, clientID, clientSecret, authURL, baseURL, resourceGroup string,
) (string, SAPAICoreBackendType, *schemas.BifrostError) {
	cacheKey := deploymentCacheKey(baseURL, resourceGroup)

	// Try cache first (read lock)
	dc.mu.RLock()
	if cached, ok := dc.deployments[cacheKey]; ok {
		if time.Since(cached.fetchedAt) < dc.ttl {
			if deployment, ok := cached.modelToDeployment[modelName]; ok {
				dc.mu.RUnlock()
				return deployment.DeploymentID, deployment.Backend, nil
			}
			// Model not found in fresh cache — no need to refetch
			dc.mu.RUnlock()
			return "", "", providerUtils.NewBifrostOperationError(
				fmt.Sprintf("no running deployment found for model: %s", modelName),
				fmt.Errorf("model not deployed"),
				schemas.SAPAICore,
			)
		}
	}
	dc.mu.RUnlock()

	// Opportunistic cleanup: prune expired entries during cache misses
	dc.pruneExpired()

	// Use singleflight to coalesce concurrent fetches for the same cache key.
	result, _, _ := dc.group.Do(cacheKey, func() (interface{}, error) {
		// Double-check cache (another goroutine may have just refreshed)
		dc.mu.RLock()
		if cached, ok := dc.deployments[cacheKey]; ok {
			if time.Since(cached.fetchedAt) < dc.ttl {
				dc.mu.RUnlock()
				return &deploymentFetchResult{deployments: cached.modelToDeployment}, nil
			}
		}
		dc.mu.RUnlock()

		// Fetch deployments from API
		deployments, fetchErr := dc.fetchDeployments(clientID, clientSecret, authURL, baseURL, resourceGroup)
		if fetchErr != nil {
			return &deploymentFetchResult{bifError: fetchErr}, nil
		}

		// Cache the results
		dc.mu.Lock()
		dc.deployments[cacheKey] = &cachedDeployments{
			modelToDeployment: deployments,
			fetchedAt:         time.Now(),
		}
		dc.mu.Unlock()

		return &deploymentFetchResult{deployments: deployments}, nil
	})

	fr := result.(*deploymentFetchResult)
	if fr.bifError != nil {
		return "", "", fr.bifError
	}

	// Look up the requested model
	if deployment, ok := fr.deployments[modelName]; ok {
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

	if err := dc.client.DoTimeout(req, resp, 30*time.Second); err != nil {
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

	// Build model -> deployment mapping
	result := make(map[string]SAPAICoreCachedDeployment)
	for _, res := range deploymentsResp.Resources {
		if res.Status != SAPAICoreDeploymentStatusRunning {
			continue
		}
		modelName := res.Details.Resources.SAPAICoreBackendDetails.Model.Name
		if modelName == "" {
			continue
		}
		result[modelName] = SAPAICoreCachedDeployment{
			DeploymentID: res.ID,
			ModelName:    modelName,
			Backend:      determineBackend(modelName),
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

	if err := dc.client.DoTimeout(req, resp, 30*time.Second); err != nil {
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
			config := GetSAPAICoreModelConfig(modelName)
			modelSet[modelName] = SAPAICoreModel{
				ID:              modelName,
				Name:            modelName,
				DeploymentID:    res.ID,
				ContextLength:   config.ContextWindow,
				MaxOutputTokens: config.MaxTokens,
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

// ClearCache clears the deployment cache for a specific resource group.
// If both baseURL and resourceGroup are empty, clears the entire cache.
func (dc *DeploymentCache) ClearCache(baseURL, resourceGroup string) {
	dc.mu.Lock()
	defer dc.mu.Unlock()

	if baseURL == "" && resourceGroup == "" {
		// Clear all cache entries
		dc.deployments = make(map[string]*cachedDeployments)
		return
	}

	cacheKey := deploymentCacheKey(baseURL, resourceGroup)
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

// pruneExpired is a non-blocking opportunistic cleanup that removes expired entries
// if the write lock can be acquired without contention.
func (dc *DeploymentCache) pruneExpired() {
	if !dc.mu.TryLock() {
		return
	}
	defer dc.mu.Unlock()

	now := time.Now()
	for key, cached := range dc.deployments {
		if now.Sub(cached.fetchedAt) >= dc.ttl {
			delete(dc.deployments, key)
		}
	}
}
