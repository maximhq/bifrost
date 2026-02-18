package sapaicore

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// DefaultDeploymentCacheTTL is the default TTL for deployment cache entries
const DefaultDeploymentCacheTTL = 1 * time.Hour

// MinDeploymentCacheTTL is the minimum allowed TTL for deployment cache entries
const MinDeploymentCacheTTL = 1 * time.Minute

// DeploymentCache manages deployment ID resolution and caching
type DeploymentCache struct {
	mu          sync.RWMutex
	deployments map[string]*cachedDeployments // keyed by resource group + base URL
	client      *fasthttp.Client
	tokenCache  *TokenCache
	ttl         time.Duration
}

// cachedDeployments holds cached deployment data for a resource group
type cachedDeployments struct {
	modelToDeployment map[string]CachedDeployment // model name -> deployment info
	fetchedAt         time.Time
}

// NewDeploymentCache creates a new deployment cache with default TTL
func NewDeploymentCache(client *fasthttp.Client, tokenCache *TokenCache) *DeploymentCache {
	return NewDeploymentCacheWithTTL(client, tokenCache, DefaultDeploymentCacheTTL)
}

// NewDeploymentCacheWithTTL creates a new deployment cache with a custom TTL.
// TTL values less than MinDeploymentCacheTTL will be clamped to the minimum.
func NewDeploymentCacheWithTTL(client *fasthttp.Client, tokenCache *TokenCache, ttl time.Duration) *DeploymentCache {
	if ttl <= 0 {
		ttl = DefaultDeploymentCacheTTL
	} else if ttl < MinDeploymentCacheTTL {
		ttl = MinDeploymentCacheTTL
	}
	return &DeploymentCache{
		deployments: make(map[string]*cachedDeployments),
		client:      client,
		tokenCache:  tokenCache,
		ttl:         ttl,
	}
}

// deploymentCacheKey generates a unique key for deployment cache
func deploymentCacheKey(baseURL, resourceGroup string) string {
	return baseURL + ":" + resourceGroup
}

// GetDeploymentID resolves a model name to a deployment ID
// First checks static deployments map from config, then falls back to auto-resolution
func (dc *DeploymentCache) GetDeploymentID(
	modelName string,
	staticDeployments map[string]string,
	clientID, clientSecret, authURL, baseURL, resourceGroup string,
) (string, BackendType, *schemas.BifrostError) {
	// Check static deployments first
	if staticDeployments != nil {
		if deploymentID, ok := staticDeployments[modelName]; ok {
			backend := DetermineBackend(modelName)
			return deploymentID, backend, nil
		}
	}

	// Auto-resolve from deployments API
	return dc.resolveDeployment(modelName, clientID, clientSecret, authURL, baseURL, resourceGroup)
}

// resolveDeployment fetches and caches deployments, then returns the deployment ID for the model
func (dc *DeploymentCache) resolveDeployment(
	modelName, clientID, clientSecret, authURL, baseURL, resourceGroup string,
) (string, BackendType, *schemas.BifrostError) {
	cacheKey := deploymentCacheKey(baseURL, resourceGroup)

	// Try cache first (read lock)
	dc.mu.RLock()
	if cached, ok := dc.deployments[cacheKey]; ok {
		if time.Since(cached.fetchedAt) < dc.ttl {
			if deployment, ok := cached.modelToDeployment[modelName]; ok {
				dc.mu.RUnlock()
				return deployment.DeploymentID, deployment.Backend, nil
			}
		}
	}
	dc.mu.RUnlock()

	// Need to refresh cache (write lock)
	dc.mu.Lock()
	defer dc.mu.Unlock()

	// Double-check after acquiring write lock
	if cached, ok := dc.deployments[cacheKey]; ok {
		if time.Since(cached.fetchedAt) < dc.ttl {
			if deployment, ok := cached.modelToDeployment[modelName]; ok {
				return deployment.DeploymentID, deployment.Backend, nil
			}
			// Model not found in fresh cache
			return "", "", providerUtils.NewBifrostOperationError(
				fmt.Sprintf("no running deployment found for model: %s", modelName),
				fmt.Errorf("model not deployed"),
				schemas.SAPAICore,
			)
		}
	}

	// Fetch deployments from API
	deployments, err := dc.fetchDeployments(clientID, clientSecret, authURL, baseURL, resourceGroup)
	if err != nil {
		return "", "", err
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
) (map[string]CachedDeployment, *schemas.BifrostError) {
	// Get auth token
	token, tokenErr := dc.tokenCache.GetToken(clientID, clientSecret, authURL)
	if tokenErr != nil {
		return nil, tokenErr
	}

	// Ensure baseURL has /v2 suffix
	normalizedURL := normalizeBaseURL(baseURL)

	// Build request URL
	deploymentsURL := fmt.Sprintf("%s/lm/deployments?status=RUNNING&resourceGroup=%s", normalizedURL, resourceGroup)

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
			fmt.Sprintf("deployments request failed with status %d: %s", resp.StatusCode(), string(resp.Body())),
			fmt.Errorf("HTTP %d", resp.StatusCode()),
			schemas.SAPAICore,
		)
	}

	var deploymentsResp DeploymentsResponse
	if err := sonic.Unmarshal(resp.Body(), &deploymentsResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(
			"failed to parse deployments response",
			err,
			schemas.SAPAICore,
		)
	}

	// Build model -> deployment mapping
	result := make(map[string]CachedDeployment)
	for _, res := range deploymentsResp.Resources {
		if res.Status != DeploymentStatusRunning {
			continue
		}
		modelName := res.Details.Resources.BackendDetails.Model.Name
		if modelName == "" {
			continue
		}
		result[modelName] = CachedDeployment{
			DeploymentID: res.ID,
			ModelName:    modelName,
			Backend:      DetermineBackend(modelName),
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
	deploymentsURL := fmt.Sprintf("%s/lm/deployments?status=RUNNING&resourceGroup=%s", normalizedURL, resourceGroup)

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
			fmt.Sprintf("model listing request failed with status %d: %s", resp.StatusCode(), string(resp.Body())),
			fmt.Errorf("HTTP %d", resp.StatusCode()),
			schemas.SAPAICore,
		)
	}

	var deploymentsResp DeploymentsResponse
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
		if res.Status != DeploymentStatusRunning {
			continue
		}
		modelName := res.Details.Resources.BackendDetails.Model.Name
		if modelName == "" {
			continue
		}
		if _, exists := modelSet[modelName]; !exists {
			config := GetModelConfig(modelName)
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

// DetermineBackend determines the backend type based on model name prefix
func DetermineBackend(modelName string) BackendType {
	if strings.HasPrefix(modelName, "anthropic--") || strings.HasPrefix(modelName, "amazon--") {
		return BackendBedrock
	}
	if strings.HasPrefix(modelName, "gemini-") {
		return BackendVertex
	}
	return BackendOpenAI
}

// normalizeBaseURL ensures the base URL has the /v2 suffix
func normalizeBaseURL(baseURL string) string {
	if strings.HasSuffix(baseURL, "/v2") {
		return baseURL
	}
	if strings.HasSuffix(baseURL, "/") {
		return baseURL + "v2"
	}
	return baseURL + "/v2"
}
