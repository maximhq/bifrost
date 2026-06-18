package litellm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The LiteLLM management API masks secrets (/credentials shows "os****KE",
// /model/info omits api_key entirely), so the real key material is read from two
// places: the config.yaml credential_list/model_list (plaintext env refs and
// literals) and the Postgres tables LiteLLM_CredentialsTable and
// LiteLLM_ProxyModelTable (values encrypted with the salt key). CredentialStore
// loads both and hands back fully resolved credentials.

// OpenDB opens the LiteLLM Postgres database from its DATABASE_URL DSN.
func OpenDB(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("open litellm db: %w", err)
	}
	return db, nil
}

// credentialRow mirrors LiteLLM_CredentialsTable. credential_values holds each
// value encrypted; credential_info is plaintext JSON.
type credentialRow struct {
	CredentialName   string
	CredentialValues string // jsonb: {"api_key": "<encrypted>", "api_base": "<encrypted>"}
	CredentialInfo   string // jsonb: {"custom_llm_provider": "openai"}
}

func (credentialRow) TableName() string { return "LiteLLM_CredentialsTable" }

// proxyModelRow mirrors LiteLLM_ProxyModelTable. Every string value in
// litellm_params is encrypted; numeric values (rpm/tpm) are stored as-is.
type proxyModelRow struct {
	ModelID       string
	ModelName     string
	LitellmParams string // jsonb: {"model": "<enc>", "api_key": "<enc>", "api_base": "<enc>", ...}
}

func (proxyModelRow) TableName() string { return "LiteLLM_ProxyModelTable" }

// dbCredentialValues is the encrypted credential_values payload.
type dbCredentialValues struct {
	APIKey  *string `json:"api_key"`
	APIBase *string `json:"api_base"`
}

// dbCredentialInfo is the plaintext credential_info payload.
type dbCredentialInfo struct {
	CustomLLMProvider *string `json:"custom_llm_provider"`
}

// dbLitellmParams is the subset of an encrypted litellm_params blob the
// migration needs: inline credential fields plus per-deployment budget. String
// values are encrypted; numeric values (max_budget) are stored as-is.
type dbLitellmParams struct {
	APIKey            *string  `json:"api_key"`
	APIBase           *string  `json:"api_base"`
	CustomLLMProvider *string  `json:"custom_llm_provider"`
	MaxBudget         *float64 `json:"max_budget"`
	BudgetDuration    *string  `json:"budget_duration"`
	// Azure
	AzureClientID     *string `json:"azure_client_id"`
	AzureClientSecret *string `json:"azure_client_secret"`
	AzureTenantID     *string `json:"azure_tenant_id"`
	AzureADToken      *string `json:"azure_ad_token"`
	// Bedrock (AWS)
	AWSAccessKeyID     *string `json:"aws_access_key_id"`
	AWSSecretAccessKey *string `json:"aws_secret_access_key"`
	AWSRegionName      *string `json:"aws_region_name"`
	AWSRoleName        *string `json:"aws_role_name"`
	AWSSessionName     *string `json:"aws_session_name"`
	AWSSessionToken    *string `json:"aws_session_token"`
	// Vertex (GCP)
	VertexProject     *string `json:"vertex_project"`
	VertexLocation    *string `json:"vertex_location"`
	VertexCredentials *string `json:"vertex_credentials"`
}

// Deployment is the resolved (decrypted) per-deployment view the migration needs
// from litellm_params: the inline credential (when the deployment carries one
// directly) and its budget. Rate limits (rpm/tpm) are read separately from the
// /model/info response, where they are not encrypted.
type Deployment struct {
	APIKey            string
	APIBase           string
	CustomLLMProvider string
	MaxBudget         *float64
	BudgetDuration    *string
	// Azure
	AzureClientID     string
	AzureClientSecret string
	AzureTenantID     string
	AzureADToken      string
	// Bedrock (AWS)
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSRegionName      string
	AWSRoleName        string
	AWSSessionName     string
	AWSSessionToken    string
	// Vertex (GCP)
	VertexProject     string
	VertexLocation    string
	VertexCredentials string
}

// InlineCredential returns the deployment's inline credential, or nil when it
// carries neither an api_key nor an api_base (i.e. it uses a named credential).
func (d *Deployment) InlineCredential() *LiteLLMModelCredential {
	if d == nil || (d.APIKey == "" && d.APIBase == "") {
		return nil
	}
	return &LiteLLMModelCredential{
		CredentialValues: &LiteLLMModelCredentialValues{ApiKey: strPtr(d.APIKey), ApiBase: strPtr(d.APIBase)},
		CredentialInfo:   &LiteLLMModelCredentialInfo{CustomLLMProvider: d.CustomLLMProvider},
	}
}

// CredentialStore resolves LiteLLM credentials from config + database, decrypting
// the database values with the salt key.
type CredentialStore struct {
	db      *gorm.DB
	cfg     *LiteLLMConfig
	saltKey string
	byName  map[string]*LiteLLMModelCredential
}

// NewCredentialStore loads every named credential from the config credential_list
// (plaintext) and the LiteLLM_CredentialsTable (decrypted), with the database
// taking precedence on name collisions. An empty dbURL or nil cfg skips that
// source. saltKey is LITELLM_SALT_KEY (or the proxy master_key).
func NewCredentialStore(ctx context.Context, dbURL string, cfg *LiteLLMConfig, saltKey string) (*CredentialStore, error) {
	var db *gorm.DB
	if strings.TrimSpace(dbURL) != "" {
		var err error
		if db, err = OpenDB(dbURL); err != nil {
			return nil, err
		}
	}
	s := &CredentialStore{db: db, cfg: cfg, saltKey: saltKey, byName: map[string]*LiteLLMModelCredential{}}
	if err := s.loadNamed(ctx); err != nil {
		if s.db != nil {
			_ = s.Close()
		}
		return nil, err
	}
	return s, nil
}

// Named returns the resolved (decrypted) credentials keyed by credential_name.
func (s *CredentialStore) Named() map[string]*LiteLLMModelCredential { return s.byName }

func (s *CredentialStore) Close() error {
	if s.db == nil {
		return nil
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func (s *CredentialStore) loadNamed(ctx context.Context) error {
	if s.cfg != nil {
		for _, c := range s.cfg.CredentialList {
			name := strings.TrimSpace(c.CredentialName)
			if name == "" {
				continue
			}
			s.byName[name] = &LiteLLMModelCredential{
				CredentialName:   name,
				CredentialValues: &LiteLLMModelCredentialValues{ApiKey: strPtr(c.CredentialValues["api_key"]), ApiBase: strPtr(c.CredentialValues["api_base"])},
				CredentialInfo:   &LiteLLMModelCredentialInfo{CustomLLMProvider: c.CredentialInfo["custom_llm_provider"]},
			}
		}
	}

	if s.db == nil {
		return nil
	}
	var rows []credentialRow
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return fmt.Errorf("read LiteLLM_CredentialsTable: %w", err)
	}
	for _, r := range rows {
		name := strings.TrimSpace(r.CredentialName)
		if name == "" {
			continue
		}
		var vals dbCredentialValues
		if err := json.Unmarshal([]byte(r.CredentialValues), &vals); err != nil {
			return fmt.Errorf("credential %q values: %w", name, err)
		}
		apiKey, err := s.decryptPtr(vals.APIKey)
		if err != nil {
			return fmt.Errorf("credential %q api_key: %w", name, err)
		}
		apiBase, err := s.decryptPtr(vals.APIBase)
		if err != nil {
			return fmt.Errorf("credential %q api_base: %w", name, err)
		}
		var info dbCredentialInfo
		if r.CredentialInfo != "" {
			if err = json.Unmarshal([]byte(r.CredentialInfo), &info); err != nil {
				return fmt.Errorf("credential %q info: %w", name, err)
			}
		}
		provider := ""
		if info.CustomLLMProvider != nil {
			provider = *info.CustomLLMProvider
		}
		s.byName[name] = &LiteLLMModelCredential{
			CredentialName:   name,
			CredentialValues: &LiteLLMModelCredentialValues{ApiKey: apiKey, ApiBase: apiBase},
			CredentialInfo:   &LiteLLMModelCredentialInfo{CustomLLMProvider: provider},
		}
	}
	return nil
}

// ResolveCredential returns the fully resolved (decrypted) credential for in's
// credential_name, looked up in the config + database. in is returned unchanged
// when its credential is not found (e.g. an inline deployment with no name).
func (s *CredentialStore) ResolveCredential(in *LiteLLMModelCredential) *LiteLLMModelCredential {
	if in == nil {
		return nil
	}
	if c, ok := s.byName[strings.TrimSpace(in.CredentialName)]; ok {
		return c
	}
	return in
}

// Deployments builds the resolved per-deployment map (inline credential + budget)
// from litellm_params. Database deployments are keyed by model_id (decrypted);
// config deployments, which have no stable id, are keyed by model_name
// (plaintext). Callers look up by model_id first, then by model_name.
func (s *CredentialStore) Deployments(ctx context.Context) (map[string]*Deployment, error) {
	out := map[string]*Deployment{}

	if s.cfg != nil {
		for _, m := range s.cfg.ModelList {
			p := m.LiteLLMParams
			out[m.ModelName] = &Deployment{
				APIKey:             strings.TrimSpace(p.APIKey),
				APIBase:            strings.TrimSpace(p.APIBase),
				CustomLLMProvider:  p.CustomLLMProvider,
				MaxBudget:          p.MaxBudget,
				BudgetDuration:     p.BudgetDuration,
				AzureClientID:      strings.TrimSpace(p.AzureClientID),
				AzureClientSecret:  strings.TrimSpace(p.AzureClientSecret),
				AzureTenantID:      strings.TrimSpace(p.AzureTenantID),
				AzureADToken:       strings.TrimSpace(p.AzureADToken),
				AWSAccessKeyID:     strings.TrimSpace(p.AWSAccessKeyID),
				AWSSecretAccessKey: strings.TrimSpace(p.AWSSecretAccessKey),
				AWSRegionName:      strings.TrimSpace(p.AWSRegionName),
				AWSRoleName:        strings.TrimSpace(p.AWSRoleName),
				AWSSessionName:     strings.TrimSpace(p.AWSSessionName),
				AWSSessionToken:    strings.TrimSpace(p.AWSSessionToken),
				VertexProject:      strings.TrimSpace(p.VertexProject),
				VertexLocation:     strings.TrimSpace(p.VertexLocation),
				VertexCredentials:  strings.TrimSpace(p.VertexCredentials),
			}
		}
	}

	if s.db == nil {
		return out, nil
	}
	var rows []proxyModelRow
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read LiteLLM_ProxyModelTable: %w", err)
	}
	for _, r := range rows {
		var p dbLitellmParams
		if err := json.Unmarshal([]byte(r.LitellmParams), &p); err != nil {
			return nil, fmt.Errorf("model %q params: %w", r.ModelID, err)
		}
		apiKey, err := s.decryptPtr(p.APIKey)
		if err != nil {
			return nil, fmt.Errorf("model %q api_key: %w", r.ModelID, err)
		}
		apiBase, err := s.decryptPtr(p.APIBase)
		if err != nil {
			return nil, fmt.Errorf("model %q api_base: %w", r.ModelID, err)
		}
		provider := ""
		if p.CustomLLMProvider != nil {
			dec, err := s.decrypt(*p.CustomLLMProvider)
			if err != nil {
				return nil, fmt.Errorf("model %q custom_llm_provider: %w", r.ModelID, err)
			}
			provider = dec
		}
		budgetDuration, err := s.decryptPtr(p.BudgetDuration)
		if err != nil {
			return nil, fmt.Errorf("model %q budget_duration: %w", r.ModelID, err)
		}
		// Azure
		azureClientID, err := s.decryptPtr(p.AzureClientID)
		if err != nil {
			return nil, fmt.Errorf("model %q azure_client_id: %w", r.ModelID, err)
		}
		azureClientSecret, err := s.decryptPtr(p.AzureClientSecret)
		if err != nil {
			return nil, fmt.Errorf("model %q azure_client_secret: %w", r.ModelID, err)
		}
		azureTenantID, err := s.decryptPtr(p.AzureTenantID)
		if err != nil {
			return nil, fmt.Errorf("model %q azure_tenant_id: %w", r.ModelID, err)
		}
		azureADToken, err := s.decryptPtr(p.AzureADToken)
		if err != nil {
			return nil, fmt.Errorf("model %q azure_ad_token: %w", r.ModelID, err)
		}
		// Bedrock
		awsAccessKeyID, err := s.decryptPtr(p.AWSAccessKeyID)
		if err != nil {
			return nil, fmt.Errorf("model %q aws_access_key_id: %w", r.ModelID, err)
		}
		awsSecretAccessKey, err := s.decryptPtr(p.AWSSecretAccessKey)
		if err != nil {
			return nil, fmt.Errorf("model %q aws_secret_access_key: %w", r.ModelID, err)
		}
		awsRegionName, err := s.decryptPtr(p.AWSRegionName)
		if err != nil {
			return nil, fmt.Errorf("model %q aws_region_name: %w", r.ModelID, err)
		}
		awsRoleName, err := s.decryptPtr(p.AWSRoleName)
		if err != nil {
			return nil, fmt.Errorf("model %q aws_role_name: %w", r.ModelID, err)
		}
		awsSessionName, err := s.decryptPtr(p.AWSSessionName)
		if err != nil {
			return nil, fmt.Errorf("model %q aws_session_name: %w", r.ModelID, err)
		}
		awsSessionToken, err := s.decryptPtr(p.AWSSessionToken)
		if err != nil {
			return nil, fmt.Errorf("model %q aws_session_token: %w", r.ModelID, err)
		}
		// Vertex
		vertexProject, err := s.decryptPtr(p.VertexProject)
		if err != nil {
			return nil, fmt.Errorf("model %q vertex_project: %w", r.ModelID, err)
		}
		vertexLocation, err := s.decryptPtr(p.VertexLocation)
		if err != nil {
			return nil, fmt.Errorf("model %q vertex_location: %w", r.ModelID, err)
		}
		vertexCredentials, err := s.decryptPtr(p.VertexCredentials)
		if err != nil {
			return nil, fmt.Errorf("model %q vertex_credentials: %w", r.ModelID, err)
		}
		out[r.ModelID] = &Deployment{
			APIKey:             deref(apiKey),
			APIBase:            deref(apiBase),
			CustomLLMProvider:  provider,
			MaxBudget:          p.MaxBudget,
			BudgetDuration:     budgetDuration,
			AzureClientID:      deref(azureClientID),
			AzureClientSecret:  deref(azureClientSecret),
			AzureTenantID:      deref(azureTenantID),
			AzureADToken:       deref(azureADToken),
			AWSAccessKeyID:     deref(awsAccessKeyID),
			AWSSecretAccessKey: deref(awsSecretAccessKey),
			AWSRegionName:      deref(awsRegionName),
			AWSRoleName:        deref(awsRoleName),
			AWSSessionName:     deref(awsSessionName),
			AWSSessionToken:    deref(awsSessionToken),
			VertexProject:      deref(vertexProject),
			VertexLocation:     deref(vertexLocation),
			VertexCredentials:  deref(vertexCredentials),
		}
	}
	return out, nil
}

// decryptPtr decrypts a non-nil encrypted value, returning a pointer to the
// plaintext (nil stays nil).
func (s *CredentialStore) decryptPtr(v *string) (*string, error) {
	if v == nil {
		return nil, nil
	}
	dec, err := s.decrypt(*v)
	if err != nil {
		return nil, err
	}
	return &dec, nil
}

func (s *CredentialStore) decrypt(v string) (string, error) {
	return DecryptValue(v, s.saltKey)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
