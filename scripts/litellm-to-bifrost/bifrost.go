package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Bifrost sink types: the subset of the management-API request bodies the
// migration writes. Kept as local DTOs so the migration depends only on the
// wire (JSON) contract, not on the transports package.

// BifrostClient writes entities to the Bifrost management API.
type BifrostClient struct {
	BaseURL string // e.g. http://localhost:8080
	APIKey  string // optional bearer token
}

// BifrostCreateCustomerRequest is the body for POST /api/governance/customers.
type BifrostCreateCustomerRequest struct {
	Name      string                         `json:"name"`
	Budgets   []BifrostCreateBudgetRequest   `json:"budgets,omitempty"`
	RateLimit *BifrostCreateRateLimitRequest `json:"rate_limit,omitempty"`
}

// BifrostCreateTeamRequest is the body for POST /api/governance/teams.
// CustomerID links the team to a migrated customer (LiteLLM organization); it is
// omitted for a standalone team or one whose customer could not be resolved.
type BifrostCreateTeamRequest struct {
	Name       string                         `json:"name"`
	CustomerID *string                        `json:"customer_id,omitempty"`
	Budgets    []BifrostCreateBudgetRequest   `json:"budgets,omitempty"`
	RateLimit  *BifrostCreateRateLimitRequest `json:"rate_limit,omitempty"`
}

// BifrostCreateUserRequest is the body for POST /api/users. role_id is
// intentionally omitted: LiteLLM's string user_role has no Bifrost numeric
// role_id mapping, and user governance is driven by access profiles.
type BifrostCreateUserRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// BifrostCreateVirtualKeyRequest is the body for
// POST /api/governance/virtual-keys. TeamID and CustomerID are mutually
// exclusive (VK ownership is team XOR customer). The key value is
// server-generated; the LiteLLM token is not carried.
type BifrostCreateVirtualKeyRequest struct {
	Name            string                           `json:"name"`
	ProviderConfigs []BifrostVKProviderConfigRequest `json:"provider_configs,omitempty"`
	TeamID          *string                          `json:"team_id,omitempty"`
	CustomerID      *string                          `json:"customer_id,omitempty"`
	Budgets         []BifrostCreateBudgetRequest     `json:"budgets,omitempty"`
	RateLimit       *BifrostCreateRateLimitRequest   `json:"rate_limit,omitempty"`
	IsActive        *bool                            `json:"is_active,omitempty"`
}

// BifrostCreateModelConfigRequest is the body for
// POST /api/governance/model-configs. Scope is global for LiteLLM model-level
// budgets and rate limits.
type BifrostCreateModelConfigRequest struct {
	ModelName string                         `json:"model_name"`
	Provider  *string                        `json:"provider,omitempty"`
	Scope     string                         `json:"scope,omitempty"`
	Budgets   []BifrostCreateBudgetRequest   `json:"budgets,omitempty"`
	RateLimit *BifrostCreateRateLimitRequest `json:"rate_limit,omitempty"`
}

// bifrostModelConfigScopeGlobal is the Bifrost scope for global model configs.
const bifrostModelConfigScopeGlobal = "global"

// BifrostVKProviderConfigRequest selects which keys of a provider a VK may use.
// key_ids ["*"] grants all keys; specific UUIDs limit to those keys; empty denies all.
// allowed_models ["*"] allows all models on the matched keys.
type BifrostVKProviderConfigRequest struct {
	Provider      string   `json:"provider"`
	KeyIDs        []string `json:"key_ids,omitempty"`
	AllowedModels []string `json:"allowed_models,omitempty"`
}

// BifrostCreateBudgetRequest is a single Bifrost budget.
type BifrostCreateBudgetRequest struct {
	MaxLimit      float64 `json:"max_limit"`
	ResetDuration string  `json:"reset_duration"` // e.g. "30s", "5m", "1h", "1d", "1w", "1M", "1Y"
}

// BifrostCreateRateLimitRequest is a Bifrost rate limit. Token and request
// dimensions are independent; each is omitted when LiteLLM has no positive
// limit for it.
type BifrostCreateRateLimitRequest struct {
	TokenMaxLimit        *int64  `json:"token_max_limit,omitempty"`
	TokenResetDuration   *string `json:"token_reset_duration,omitempty"`
	RequestMaxLimit      *int64  `json:"request_max_limit,omitempty"`
	RequestResetDuration *string `json:"request_reset_duration,omitempty"`
}

// bifrostDefaultConcurrency / bifrostDefaultBufferSize mirror
// schemas.DefaultConcurrency / DefaultBufferSize. The provider PUT/POST require
// both to be > 0.
const (
	bifrostDefaultConcurrency = 1000
	bifrostDefaultBufferSize  = 5000
)

// BifrostConcurrencyAndBufferSize mirrors schemas.ConcurrencyAndBufferSize.
type BifrostConcurrencyAndBufferSize struct {
	Concurrency int `json:"concurrency"`
	BufferSize  int `json:"buffer_size"`
}

// BifrostNetworkConfig is the subset of schemas.NetworkConfig the migration
// sets. BaseURL is provider-level in Bifrost (LiteLLM api_base is
// per-deployment).
type BifrostNetworkConfig struct {
	BaseURL string `json:"base_url,omitempty"`
}

// BifrostCustomProviderConfig mirrors schemas.CustomProviderConfig for a
// synthesized provider that wraps a base provider at a distinct base URL.
type BifrostCustomProviderConfig struct {
	IsKeyLess        bool   `json:"is_key_less"`
	BaseProviderType string `json:"base_provider_type"`
}

// BifrostProviderUpdatePayload is the body for PUT /api/providers/{provider}
// for standard providers; keys are managed separately via the /keys endpoint.
type BifrostProviderUpdatePayload struct {
	NetworkConfig            *BifrostNetworkConfig           `json:"network_config,omitempty"`
	ConcurrencyAndBufferSize BifrostConcurrencyAndBufferSize `json:"concurrency_and_buffer_size"`
}

// BifrostProviderCreatePayload is the body for POST /api/providers.
type BifrostProviderCreatePayload struct {
	Provider                 string                          `json:"provider"`
	CustomProviderConfig     *BifrostCustomProviderConfig    `json:"custom_provider_config,omitempty"`
	NetworkConfig            *BifrostNetworkConfig           `json:"network_config,omitempty"`
	ConcurrencyAndBufferSize BifrostConcurrencyAndBufferSize `json:"concurrency_and_buffer_size"`
}

// BifrostProviderKey is the body for POST /api/providers/{provider}/keys. It
// decodes into schemas.Key; Value is a plain string ("env.FOO" => from
// environment, else a literal value). The *_key_config fields carry provider-
// specific credentials (Azure endpoint, AWS credentials, GCP credentials) or
// the per-key server URL for the keyless self-hosted providers (vllm/ollama).
type BifrostProviderKey struct {
	Name             string                   `json:"name"`
	Value            string                   `json:"value"`
	Models           []string                 `json:"models"`
	Weight           float64                  `json:"weight"`
	VLLMKeyConfig    *BifrostVLLMKeyConfig    `json:"vllm_key_config,omitempty"`
	OllamaKeyConfig  *BifrostKeyURLConfig     `json:"ollama_key_config,omitempty"`
	AzureKeyConfig   *BifrostAzureKeyConfig   `json:"azure_key_config,omitempty"`
	BedrockKeyConfig *BifrostBedrockKeyConfig `json:"bedrock_key_config,omitempty"`
	VertexKeyConfig  *BifrostVertexKeyConfig  `json:"vertex_key_config,omitempty"`
}

// BifrostVLLMKeyConfig mirrors schemas.VLLMKeyConfig: a per-key vLLM server URL
// plus the exact served model used to select the key.
type BifrostVLLMKeyConfig struct {
	URL       string `json:"url"`
	ModelName string `json:"model_name,omitempty"`
}

// BifrostKeyURLConfig mirrors schemas.OllamaKeyConfig: a per-key server URL with
// no model selector.
type BifrostKeyURLConfig struct {
	URL string `json:"url"`
}

// BifrostAzureKeyConfig mirrors schemas.AzureKeyConfig. Endpoint is the Azure
// OpenAI service URL (e.g. https://myazure.openai.azure.com/). For Entra ID
// (service principal) auth, set ClientID + ClientSecret + TenantID; for API-key
// auth, leave those nil and set the key Value instead.
type BifrostAzureKeyConfig struct {
	Endpoint     string  `json:"endpoint"`
	ClientID     *string `json:"client_id,omitempty"`
	ClientSecret *string `json:"client_secret,omitempty"`
	TenantID     *string `json:"tenant_id,omitempty"`
}

// BifrostBedrockKeyConfig mirrors schemas.BedrockKeyConfig. For static IAM
// credentials set AccessKey + SecretKey (+ optional SessionToken). For role
// assumption set RoleARN (+ optional RoleSessionName). For EC2 instance-profile
// or ECS task-role auth leave AccessKey and SecretKey empty; Bifrost uses the
// SDK default credential chain. Region is optional but recommended.
type BifrostBedrockKeyConfig struct {
	AccessKey       string  `json:"access_key,omitempty"`
	SecretKey       string  `json:"secret_key,omitempty"`
	SessionToken    *string `json:"session_token,omitempty"`
	Region          *string `json:"region,omitempty"`
	RoleARN         *string `json:"role_arn,omitempty"`
	RoleSessionName *string `json:"session_name,omitempty"`
}

// BifrostVertexKeyConfig mirrors schemas.VertexKeyConfig. ProjectID and Region
// are required. AuthCredentials is the service-account JSON (or a path/env ref
// to it); leave empty to use Application Default Credentials (ADC).
type BifrostVertexKeyConfig struct {
	ProjectID       string `json:"project_id"`
	ProjectNumber   string `json:"project_number,omitempty"`
	Region          string `json:"region"`
	AuthCredentials string `json:"auth_credentials"`
}

// CreateCustomer posts a single customer to POST /api/governance/customers.
func (c *BifrostClient) CreateCustomer(ctx context.Context, in *BifrostCreateCustomerRequest) error {
	return c.sendJSON(ctx, http.MethodPost, "/api/governance/customers", in, "customer "+in.Name)
}

// CreateTeam posts a single team to POST /api/governance/teams.
func (c *BifrostClient) CreateTeam(ctx context.Context, in *BifrostCreateTeamRequest) error {
	return c.sendJSON(ctx, http.MethodPost, "/api/governance/teams", in, "team "+in.Name)
}

// FindCustomerByName resolves a Bifrost customer id by exact name via
// GET /api/governance/customers?search=. It returns ok=false (no error) when no
// customer matches, so the caller can create the team unlinked and warn.
func (c *BifrostClient) FindCustomerByName(ctx context.Context, name string) (id string, ok bool, err error) {
	body, err := c.getJSON(ctx, "/api/governance/customers?search="+url.QueryEscape(name), fmt.Sprintf("find customer %q", name))
	if err != nil {
		return "", false, err
	}

	var out struct {
		Customers []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"customers"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", false, fmt.Errorf("decode customers: %w", err)
	}
	// search may match by substring; require an exact name match.
	for _, cust := range out.Customers {
		if cust.Name == name {
			return cust.ID, true, nil
		}
	}
	return "", false, nil
}

// CreateUser creates a Bifrost user via POST /api/users and returns its id. An
// existing user (409 on duplicate email) is resolved to the existing id via
// FindUserByEmail, so the caller can still link team memberships.
func (c *BifrostClient) CreateUser(ctx context.Context, in *BifrostCreateUserRequest) (string, error) {
	body, status, err := c.doRequest(ctx, http.MethodPost, "/api/users", in)
	if err != nil {
		return "", fmt.Errorf("create user %q: %w", maskEmail(in.Email), err)
	}

	if status == http.StatusConflict {
		id, ok, ferr := c.FindUserByEmail(ctx, in.Email)
		if ferr != nil {
			return "", fmt.Errorf("create user %q: already exists but lookup failed: %w", maskEmail(in.Email), ferr)
		}
		if !ok {
			return "", fmt.Errorf("create user %q: conflict but no matching user found", maskEmail(in.Email))
		}
		return id, nil
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("create user %q: status %d: %s", maskEmail(in.Email), status, strings.TrimSpace(string(body)))
	}

	var out struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode created user: %w", err)
	}
	if out.User.ID == "" {
		return "", fmt.Errorf("create user %q: empty id in response", maskEmail(in.Email))
	}
	return out.User.ID, nil
}

// ListProviders returns the names of providers configured in Bifrost via
// GET /api/providers. The VK migration uses this to drop allow-list entries for
// providers that were not migrated (a VK create rejects unknown providers).
func (c *BifrostClient) ListProviders(ctx context.Context) (map[string]bool, error) {
	body, err := c.getJSON(ctx, "/api/providers", "list providers")
	if err != nil {
		return nil, err
	}
	var out struct {
		Providers []struct {
			Name string `json:"name"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode providers: %w", err)
	}
	set := make(map[string]bool, len(out.Providers))
	for _, p := range out.Providers {
		set[p.Name] = true
	}
	return set, nil
}

// BifrostKeyMeta is the key identity returned by GET /api/keys. The value is
// redacted; only the UUID (KeyID) and name are needed for VK key attachment.
type BifrostKeyMeta struct {
	KeyID    string `json:"key_id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

// ListAllKeys returns every provider key registered in Bifrost via GET /api/keys.
// The VK migration uses the KeyID UUIDs to attach specific keys to virtual keys.
func (c *BifrostClient) ListAllKeys(ctx context.Context) ([]BifrostKeyMeta, error) {
	body, err := c.getJSON(ctx, "/api/keys", "list all keys")
	if err != nil {
		return nil, err
	}
	var keys []BifrostKeyMeta
	if err := json.Unmarshal(body, &keys); err != nil {
		return nil, fmt.Errorf("decode keys: %w", err)
	}
	return keys, nil
}

// CreateVirtualKey creates a Bifrost virtual key via
// POST /api/governance/virtual-keys. The key value is server-generated.
func (c *BifrostClient) CreateVirtualKey(ctx context.Context, in *BifrostCreateVirtualKeyRequest) error {
	return c.sendProvider(ctx, http.MethodPost, "/api/governance/virtual-keys", in, "virtual key "+in.Name)
}

// CreateModelConfig creates a global Bifrost model config via
// POST /api/governance/model-configs. An existing config is treated as success.
func (c *BifrostClient) CreateModelConfig(ctx context.Context, in ModelConfigPlan) error {
	req := BifrostCreateModelConfigRequest{
		ModelName: in.ModelName,
		Provider:  in.Provider,
		Scope:     bifrostModelConfigScopeGlobal,
		Budgets:   in.Budgets,
		RateLimit: in.RateLimit,
	}
	return c.sendProvider(ctx, http.MethodPost, "/api/governance/model-configs", req, "model config "+modelConfigSignature(in))
}

// FindUserByEmail resolves a Bifrost user id by exact email via
// GET /api/users?search=. Returns ok=false (no error) when none matches.
func (c *BifrostClient) FindUserByEmail(ctx context.Context, email string) (id string, ok bool, err error) {
	body, err := c.getJSON(ctx, "/api/users?search="+url.QueryEscape(email), fmt.Sprintf("find user %q", maskEmail(email)))
	if err != nil {
		return "", false, err
	}
	var out struct {
		Users []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"users"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", false, fmt.Errorf("decode users: %w", err)
	}
	for _, u := range out.Users {
		if strings.EqualFold(u.Email, email) {
			return u.ID, true, nil
		}
	}
	return "", false, nil
}

// FindTeamByName resolves a Bifrost team id by exact name via
// GET /api/governance/teams?search=. Returns ok=false (no error) when none
// matches, so the caller can warn and skip the membership link.
func (c *BifrostClient) FindTeamByName(ctx context.Context, name string) (id string, ok bool, err error) {
	body, err := c.getJSON(ctx, "/api/governance/teams?search="+url.QueryEscape(name), fmt.Sprintf("find team %q", name))
	if err != nil {
		return "", false, err
	}
	var out struct {
		Teams []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"teams"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", false, fmt.Errorf("decode teams: %w", err)
	}
	for _, tm := range out.Teams {
		if tm.Name == name {
			return tm.ID, true, nil
		}
	}
	return "", false, nil
}

// AddTeamMember links a user to a team via POST /api/teams/{id}/members. An
// existing membership (409) is treated as success.
func (c *BifrostClient) AddTeamMember(ctx context.Context, teamID, userID string) error {
	return c.sendProvider(ctx, http.MethodPost, "/api/teams/"+teamID+"/members", map[string]string{"user_id": userID}, "team "+teamID+" member "+userID)
}

// EnsureProvider creates or upserts a Bifrost provider's config (no keys). A
// custom provider (distinct base URL) is created via POST /api/providers; a
// standard provider is upserted via PUT /api/providers/{provider}. An existing
// provider (409 / already-present) is treated as success.
func (c *BifrostClient) EnsureProvider(ctx context.Context, p ProviderPlan) error {
	concurrency := BifrostConcurrencyAndBufferSize{Concurrency: bifrostDefaultConcurrency, BufferSize: bifrostDefaultBufferSize}

	var network *BifrostNetworkConfig
	if p.BaseURL != "" {
		network = &BifrostNetworkConfig{BaseURL: p.BaseURL}
	}

	if p.IsCustom {
		payload := BifrostProviderCreatePayload{
			Provider:                 p.Name,
			CustomProviderConfig:     &BifrostCustomProviderConfig{BaseProviderType: p.BaseProvider},
			NetworkConfig:            network,
			ConcurrencyAndBufferSize: concurrency,
		}
		return c.sendProvider(ctx, http.MethodPost, "/api/providers", payload, p.Name)
	}

	payload := BifrostProviderUpdatePayload{
		NetworkConfig:            network,
		ConcurrencyAndBufferSize: concurrency,
	}
	return c.sendProvider(ctx, http.MethodPut, "/api/providers/"+p.Name, payload, p.Name)
}

// CreateProviderKey adds a single key to an existing provider via
// POST /api/providers/{provider}/keys. An already-present key (409) is treated
// as success.
func (c *BifrostClient) CreateProviderKey(ctx context.Context, provider string, k KeyPlan) error {
	key := BifrostProviderKey{
		Name:             k.Name,
		Value:            k.Value,
		Models:           k.Models,
		Weight:           1.0,
		AzureKeyConfig:   k.AzureKeyConfig,
		BedrockKeyConfig: k.BedrockKeyConfig,
		VertexKeyConfig:  k.VertexKeyConfig,
	}
	// Self-hosted providers carry the server URL on the key itself.
	if k.URL != "" {
		switch provider {
		case "vllm":
			key.VLLMKeyConfig = &BifrostVLLMKeyConfig{URL: k.URL, ModelName: k.VLLMModelName}
		case "ollama":
			key.OllamaKeyConfig = &BifrostKeyURLConfig{URL: k.URL}
		}
	}
	return c.sendProvider(ctx, http.MethodPost, "/api/providers/"+provider+"/keys", key, provider+"/"+k.Name)
}

func (c *BifrostClient) endpoint(path string) string {
	return strings.TrimRight(c.BaseURL, "/") + path
}

func (c *BifrostClient) doRequest(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response body: %w", err)
	}
	return out, resp.StatusCode, nil
}

// getJSON performs a GET with optional bearer and returns the body, mapping
// non-2xx to an error.
func (c *BifrostClient) getJSON(ctx context.Context, path, what string) ([]byte, error) {
	body, status, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s: status %d: %s", what, status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// sendProvider marshals body and performs the request, mapping 2xx and 409
// (already exists) to success.
func (c *BifrostClient) sendProvider(ctx context.Context, method, path string, body any, what string) error {
	return c.sendJSON(ctx, method, path, body, what)
}

func (c *BifrostClient) sendJSON(ctx context.Context, method, path string, body any, what string) error {
	out, status, err := c.doRequest(ctx, method, path, body)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, what, err)
	}
	if status == http.StatusConflict {
		return nil // already exists
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("%s %s: status %d: %s", method, what, status, strings.TrimSpace(string(out)))
	}
	return nil
}
