// Package databricks implements the Databricks provider. It covers the two Databricks
// inference surfaces that serve foundation models, both of which speak the OpenAI wire
// format under a workspace host:
//
//   - Model Serving / Foundation Model APIs, at /serving-endpoints. The model name is a
//     serving endpoint name (e.g. "databricks-claude-sonnet-4-5"), covering pay-per-token
//     endpoints and provisioned-throughput endpoints alike. This surface also serves the
//     OpenAI Responses API at /serving-endpoints/responses.
//   - Unity AI Gateway model APIs (model services), at /ai-gateway/mlflow/v1. The model name
//     is a Unity Catalog name (e.g. "system.ai.claude-sonnet-4-5", or a user-created
//     "<catalog>.<schema>.<service>").
//
// Because both surfaces are OpenAI-compatible, every request is delegated to the shared
// openai.HandleOpenAI* helpers; this package only resolves the workspace host, selects the
// base path, and produces the Authorization header.
//
// Authentication is either a Databricks personal access token (set as the key value) or
// OAuth machine-to-machine using a service principal (set client_id/client_secret and leave
// the key value empty). M2M tokens are minted and refreshed by a cached oauth2.TokenSource.
package databricks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	// modelServingBasePath is the OpenAI-compatible base path for Model Serving / Foundation
	// Model API endpoints. Databricks documents it as an OpenAI client base_url.
	modelServingBasePath = "/serving-endpoints"
	// aiGatewayBasePath is the OpenAI-compatible base path for Unity AI Gateway model APIs.
	aiGatewayBasePath = "/ai-gateway/mlflow/v1"

	// oauthTokenPath is the workspace-level OAuth token endpoint for M2M service principals.
	oauthTokenPath = "/oidc/v1/token"
	// oauthScope is the scope Databricks requires for M2M client-credentials grants.
	oauthScope = "all-apis"

	// gatewayRequestTagsHeader carries JSON string-to-string labels that Databricks records
	// against the request for usage tracking and cost attribution.
	gatewayRequestTagsHeader = "Databricks-Ai-Gateway-Request-Tags"

	// modelServingListPath lists Model Serving endpoints.
	modelServingListPath = "/api/2.0/serving-endpoints"
	// aiGatewayListPath lists Unity Catalog model services.
	aiGatewayListPath = "/api/2.1/unity-catalog/model-services"
)

// DatabricksProvider implements the Provider interface for Databricks.
type DatabricksProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for unary API requests (ReadTimeout bounds overall response)
	streamingClient     *fasthttp.Client      // HTTP client for streaming API requests (no ReadTimeout; idle governed by NewIdleTimeoutReader)
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewDatabricksProvider creates a new Databricks provider instance.
// There is no default BaseURL: every request targets a workspace host resolved from the key's
// DatabricksKeyConfig, falling back to a provider-level BaseURL when one is configured.
func NewDatabricksProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*DatabricksProvider, error) {
	config.CheckAndSetDefaults()

	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)
	client := &fasthttp.Client{
		ReadTimeout:         requestTimeout,
		WriteTimeout:        requestTimeout,
		MaxConnsPerHost:     config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConnDuration: time.Second * time.Duration(config.NetworkConfig.KeepAliveTimeoutInSeconds),
		MaxConnWaitTimeout:  requestTimeout,
		MaxConnDuration:     time.Second * time.Duration(schemas.DefaultMaxConnDurationInSeconds),
		ConnPoolStrategy:    fasthttp.FIFO,
	}
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client, config.NetworkConfig.AllowPrivateNetwork)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)
	streamingClient := providerUtils.BuildStreamingClient(client)

	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &DatabricksProvider{
		logger:              logger,
		client:              client,
		streamingClient:     streamingClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Databricks.
func (provider *DatabricksProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Databricks
}

// resolveWorkspaceHost returns the bare Databricks workspace host for a key, tolerating a
// value pasted with a scheme or a trailing path. It falls back to the provider-level BaseURL
// so a single-workspace deployment can configure the host once instead of per key.
func (provider *DatabricksProvider) resolveWorkspaceHost(key schemas.Key) (string, *schemas.BifrostError) {
	if key.DatabricksKeyConfig != nil {
		if host := schemas.NormalizeEndpointHost(&key.DatabricksKeyConfig.WorkspaceURL); host != "" {
			return host, nil
		}
	}
	if provider.networkConfig.BaseURL != "" {
		if host := normalizeHost(provider.networkConfig.BaseURL); host != "" {
			return host, nil
		}
	}
	return "", providerUtils.NewConfigurationError("databricks workspace url is not set: configure databricks_key_config.workspace_url on the key, or a provider base_url")
}

// normalizeHost strips a scheme and any trailing path from a raw workspace URL string.
func normalizeHost(raw string) string {
	host := strings.TrimSpace(raw)
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	return host
}

// resolveAPIFormat decides which Databricks surface a request targets. An explicit
// api_format on the key wins; otherwise the model name decides. A dotted name is a Unity
// Catalog model service (system.ai.*, or <catalog>.<schema>.<service>) and goes to the Unity
// AI Gateway; anything else is a Model Serving endpoint name.
//
// Model aliases need no special handling here: alias resolution rewrites the model to its
// upstream model_id before the provider sees it, so auto-detection reads the wire name.
func resolveAPIFormat(key schemas.Key, model string) schemas.DatabricksAPIFormat {
	if key.DatabricksKeyConfig != nil {
		switch key.DatabricksKeyConfig.APIFormat {
		case schemas.DatabricksAPIFormatModelServing, schemas.DatabricksAPIFormatAIGateway:
			return key.DatabricksKeyConfig.APIFormat
		}
	}
	if strings.Contains(model, ".") {
		return schemas.DatabricksAPIFormatAIGateway
	}
	return schemas.DatabricksAPIFormatModelServing
}

// basePathFor returns the OpenAI-compatible base path under the workspace host for a surface.
func basePathFor(format schemas.DatabricksAPIFormat) string {
	if format == schemas.DatabricksAPIFormatAIGateway {
		return aiGatewayBasePath
	}
	return modelServingBasePath
}

// buildURL composes the full upstream URL for a request. suffix is an OpenAI path such as
// "/chat/completions". A BifrostContextKeyURLPath override replaces the suffix, which lets a
// caller reach a route this provider does not model (an absolute override replaces the whole URL).
func (provider *DatabricksProvider) buildURL(ctx *schemas.BifrostContext, key schemas.Key, model, suffix string) (string, *schemas.BifrostError) {
	path := providerUtils.GetPathFromContext(ctx, suffix)
	if u, err := url.Parse(path); err == nil && u.IsAbs() && u.Host != "" {
		return path, nil
	}
	host, bErr := provider.resolveWorkspaceHost(key)
	if bErr != nil {
		return "", bErr
	}
	return "https://" + host + basePathFor(resolveAPIFormat(key, model)) + path, nil
}

// workspaceAPIURL composes a URL for a Databricks control-plane REST API (not an inference
// surface), which lives directly under the workspace host rather than a base path.
func (provider *DatabricksProvider) workspaceAPIURL(key schemas.Key, path string) (string, *schemas.BifrostError) {
	host, bErr := provider.resolveWorkspaceHost(key)
	if bErr != nil {
		return "", bErr
	}
	return "https://" + host + path, nil
}

// tokenSourcePool caches oauth2.TokenSource instances keyed by a hash of the workspace host
// and service principal credentials. The clientcredentials TokenSource refreshes on expiry
// and serialises concurrent refreshes internally, so caching the source is all that is needed
// to avoid a token mint per request.
var tokenSourcePool sync.Map

// tokenSourceCacheKey hashes the credential tuple so no secret is used as a map key in a form
// that could be logged.
func tokenSourceCacheKey(host, clientID, clientSecret string) string {
	sum := sha256.Sum256([]byte(host + "|" + clientID + "|" + clientSecret))
	return hex.EncodeToString(sum[:])
}

// getTokenSource returns a cached OAuth M2M token source for the given workspace and service
// principal, creating one on first use.
func getTokenSource(host, clientID, clientSecret string) oauth2.TokenSource {
	cacheKey := tokenSourceCacheKey(host, clientID, clientSecret)
	if cached, ok := tokenSourcePool.Load(cacheKey); ok {
		return cached.(oauth2.TokenSource)
	}
	conf := &clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     "https://" + host + oauthTokenPath,
		Scopes:       []string{oauthScope},
		// Databricks expects the service principal credentials as HTTP Basic auth.
		AuthStyle: oauth2.AuthStyleInHeader,
	}
	// oauth2.ReuseTokenSource caches the token until shortly before expiry and refreshes
	// under a mutex, so a burst of concurrent requests mints exactly one token.
	ts := oauth2.ReuseTokenSource(nil, conf.TokenSource(oauthContext()))
	actual, _ := tokenSourcePool.LoadOrStore(cacheKey, ts)
	return actual.(oauth2.TokenSource)
}

// oauthHTTPClient overrides the HTTP client the token exchange uses. It is nil in production,
// where the oauth2 package uses http.DefaultClient; tests set it to reach a stub token endpoint.
var oauthHTTPClient *http.Client

// oauthContext returns the context the client-credentials token exchange runs under.
func oauthContext() context.Context {
	if oauthHTTPClient != nil {
		return context.WithValue(context.Background(), oauth2.HTTPClient, oauthHTTPClient)
	}
	return context.Background()
}

// removeTokenSource evicts a cached token source so the next request re-mints. Called when
// credentials are rejected, mirroring the eviction the Vertex provider performs on 401/403.
func removeTokenSource(host, clientID, clientSecret string) {
	tokenSourcePool.Delete(tokenSourceCacheKey(host, clientID, clientSecret))
}

// authHeader builds the Authorization header for a request: a personal access token when the
// key carries a value, otherwise an OAuth M2M bearer token minted from the service principal.
//
// Unlike SigV4-style providers this needs no BodySigner, because the credential does not
// depend on the request body and can be resolved before the body is built.
func (provider *DatabricksProvider) authHeader(key schemas.Key) (map[string]string, *schemas.BifrostError) {
	if token := key.Value.GetValue(); token != "" {
		return map[string]string{"Authorization": "Bearer " + token}, nil
	}

	cfg := key.DatabricksKeyConfig
	if cfg == nil || cfg.ClientID == nil || cfg.ClientSecret == nil {
		return nil, providerUtils.NewConfigurationError("databricks key has no credentials: set the key value to a personal access token, or set databricks_key_config.client_id and client_secret for OAuth M2M")
	}
	clientID, clientSecret := cfg.ClientID.GetValue(), cfg.ClientSecret.GetValue()
	if clientID == "" || clientSecret == "" {
		return nil, providerUtils.NewConfigurationError("databricks oauth m2m requires both databricks_key_config.client_id and databricks_key_config.client_secret")
	}
	host, bErr := provider.resolveWorkspaceHost(key)
	if bErr != nil {
		return nil, bErr
	}

	token, err := getTokenSource(host, clientID, clientSecret).Token()
	if err != nil {
		// The credentials may have been rotated or revoked; drop the cached source so the
		// next attempt re-mints rather than replaying a source pinned to a failing refresh.
		removeTokenSource(host, clientID, clientSecret)
		// err can embed the token endpoint response; never surface or log the secret itself.
		return nil, providerUtils.NewBifrostOperationError("failed to acquire databricks oauth token", err)
	}
	return map[string]string{"Authorization": "Bearer " + token.AccessToken}, nil
}

// applyGatewayTags forwards Bifrost governance labels to Databricks as request tags, so
// Databricks-side usage tracking can attribute spend to the same virtual key, team and
// customer that Bifrost bills against. Opt-in per key.
//
// Only display names are forwarded, never user identifiers. The context extra-headers value
// is a whole map, so it is read, merged and written back rather than overwritten — otherwise
// this would clobber headers the caller supplied via x-bf-eh-*.
func (provider *DatabricksProvider) applyGatewayTags(ctx *schemas.BifrostContext, key schemas.Key) {
	if key.DatabricksKeyConfig == nil || !key.DatabricksKeyConfig.ForwardGatewayTags || ctx == nil {
		return
	}

	tags := map[string]string{}
	for label, ctxKey := range map[string]schemas.BifrostContextKey{
		"virtual_key": schemas.BifrostContextKeyGovernanceVirtualKeyName,
		"team":        schemas.BifrostContextKeyGovernanceTeamName,
		"customer":    schemas.BifrostContextKeyGovernanceCustomerName,
	} {
		if v, ok := ctx.Value(ctxKey).(string); ok && v != "" {
			tags[label] = v
		}
	}
	if len(tags) == 0 {
		return
	}
	encoded, err := providerUtils.MarshalSorted(tags)
	if err != nil {
		provider.logger.Warn("[databricks] failed to encode ai gateway request tags: %v", err)
		return
	}

	extraHeaders := map[string][]string{}
	if existing, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok && existing != nil {
		extraHeaders = existing
	}
	// A caller-supplied tag header wins: it is more specific than the governance defaults.
	if _, exists := extraHeaders[gatewayRequestTagsHeader]; !exists {
		extraHeaders[gatewayRequestTagsHeader] = []string{string(encoded)}
		ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, extraHeaders)
	}
}

// prepareRequest resolves the upstream URL and auth header for an inference request, and
// applies the per-request context flags every Databricks call needs.
func (provider *DatabricksProvider) prepareRequest(ctx *schemas.BifrostContext, key schemas.Key, model, suffix string) (string, map[string]string, *schemas.BifrostError) {
	url, bErr := provider.buildURL(ctx, key, model, suffix)
	if bErr != nil {
		return "", nil, bErr
	}
	auth, bErr := provider.authHeader(key)
	if bErr != nil {
		return "", nil, bErr
	}
	// Databricks accepts fields Bifrost does not model canonically — service_tier for
	// priority pay-per-token, reasoning_effort, and other model-specific extensions. Enable
	// extra-param passthrough so they survive to the wire instead of being dropped.
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	provider.applyGatewayTags(ctx, key)
	return url, auth, nil
}
