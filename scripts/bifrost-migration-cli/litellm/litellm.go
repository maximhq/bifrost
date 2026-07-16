package litellm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// DefaultModelProvider is the provider assumed for a deployment whose model has
// no provider prefix and no custom_llm_provider. LiteLLM itself defaults such
// bare names to OpenAI.
const DefaultModelProvider = "openai"

// doGet is a helper that executes an authenticated HTTP GET request. It appends
// the provided path to the client's BaseURL, attaches the Bearer token, and
// unmarshals the resulting JSON response body into the target destination.
func doGet[T any](ctx context.Context, c *LiteLLMClient, path string, target *T) error {
	url := strings.TrimRight(c.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error while reading response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode error: %w", err)
	}
	return nil
}

// LiteLLMClient reads entities from the LiteLLM management API.
type LiteLLMClient struct {
	BaseURL string
	APIKey  string // LiteLLM admin key
}

// LiteLLMConfig mirrors the parts of LiteLLM's config.yaml the migration consumes.
type LiteLLMConfig struct {
	ModelList      []LiteLLMModel            `yaml:"model_list"`
	RouterSettings LiteLLMRouterSettings     `yaml:"router_settings"`
	CredentialList []LiteLLMConfigCredential `yaml:"credential_list"`
}

// LiteLLMConfigCredential is one credential_list entry. Config-declared
// credential values are plaintext (env refs like "os.environ/FOO" or literals),
// unlike the encrypted values in LiteLLM_CredentialsTable.
type LiteLLMConfigCredential struct {
	CredentialName   string            `yaml:"credential_name"`
	CredentialValues map[string]string `yaml:"credential_values"`
	CredentialInfo   map[string]string `yaml:"credential_info"`
}

// LiteLLMModel is one model_list entry.
type LiteLLMModel struct {
	ModelName     string        `yaml:"model_name"`
	LiteLLMParams LiteLLMParams `yaml:"litellm_params"`
}

// LiteLLMParams mirrors litellm_params fields consumed by the migration.
type LiteLLMParams struct {
	Model             string   `yaml:"model"`               // litellm model name - "openai/gpt-4o", "bedrock/*", "gpt-4.1"
	APIKey            string   `yaml:"api_key"`             // env ref ("os.environ/FOO") or literal
	APIBase           string   `yaml:"api_base"`            // per-deployment base URL
	CustomLLMProvider string   `yaml:"custom_llm_provider"` // explicit provider override
	MaxBudget         *float64 `yaml:"max_budget"`          // per-deployment spend cap
	BudgetDuration    *string  `yaml:"budget_duration"`     // budget reset window, e.g. "30d", "1mo"
	RPM               *int64   `yaml:"rpm"`                 // per-deployment request per minute override
	TPM               *int64   `yaml:"tpm"`                 // per-deployment token per minute override
	// Azure-specific
	AzureClientID     string `yaml:"azure_client_id"`
	AzureClientSecret string `yaml:"azure_client_secret"`
	AzureTenantID     string `yaml:"azure_tenant_id"`
	AzureADToken      string `yaml:"azure_ad_token"`
	// Bedrock-specific (AWS credentials)
	AWSAccessKeyID     string `yaml:"aws_access_key_id"`
	AWSSecretAccessKey string `yaml:"aws_secret_access_key"`
	AWSRegionName      string `yaml:"aws_region_name"`
	AWSRoleName        string `yaml:"aws_role_name"`    // IAM role ARN for STS AssumeRole
	AWSSessionName     string `yaml:"aws_session_name"` // STS session name
	AWSSessionToken    string `yaml:"aws_session_token"` // temporary session token
	// Vertex-specific (GCP credentials)
	VertexProject     string `yaml:"vertex_project"`
	VertexLocation    string `yaml:"vertex_location"`
	VertexCredentials string `yaml:"vertex_credentials"` // service account JSON or ADC path; empty = ADC
}

// Normalize trims LiteLLM params and validates that a target model is present.
func (p *LiteLLMParams) Normalize() error {
	p.Model = strings.TrimSpace(p.Model)
	if p.Model == "" {
		return fmt.Errorf("model is empty")
	}

	p.APIKey = strings.TrimSpace(p.APIKey)
	p.APIBase = strings.TrimSpace(p.APIBase)
	p.CustomLLMProvider = strings.TrimSpace(p.CustomLLMProvider)
	return nil
}

// LiteLLMRouterSettings mirrors router_settings.
type LiteLLMRouterSettings struct {
	ModelGroupAlias map[string]string `yaml:"model_group_alias"`
}

// ReadLiteLLMConfig parses a LiteLLM proxy config YAML from disk. The model
// migration reads secrets here rather than from the management API, which
// redacts them.
func ReadLiteLLMConfig(path string) (*LiteLLMConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read litellm config %q: %w", path, err)
	}
	var cfg LiteLLMConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse litellm config %q: %w", path, err)
	}
	return &cfg, nil
}
