package tables

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TableKey represents an API key configuration in the database
type TableKey struct {
	ID                    uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	Name                  string         `gorm:"type:varchar(255);uniqueIndex:idx_key_name;not null" json:"name"`
	ProviderID            uint           `gorm:"index;not null" json:"provider_id"`
	Provider              string         `gorm:"index;type:varchar(50)" json:"provider"`                          // ModelProvider as string
	KeyID                 string         `gorm:"type:varchar(255);uniqueIndex:idx_key_id;not null" json:"key_id"` // UUID from schemas.Key
	Value                 schemas.EnvVar `gorm:"type:text;not null" json:"value"`
	ModelsJSON            string         `gorm:"type:text" json:"-"` // JSON serialized []string
	BlacklistedModelsJSON string         `gorm:"type:text" json:"-"` // JSON serialized []string
	Weight                *float64       `json:"weight"`
	Enabled               *bool          `gorm:"default:true" json:"enabled,omitempty"`
	CreatedAt             time.Time      `gorm:"index;not null" json:"created_at"`
	UpdatedAt             time.Time      `gorm:"index;not null" json:"updated_at"`

	// Config hash is used to detect changes synced from config.json file
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	// Unified aliases
	AliasesJSON *string `gorm:"type:text" json:"-"` // JSON serialized schemas.KeyAliases

	// Azure config fields (embedded instead of separate table for simplicity)
	AzureEndpoint     *schemas.EnvVar `gorm:"type:text" json:"azure_endpoint,omitempty"`
	AzureClientID     *schemas.EnvVar `gorm:"type:text" json:"azure_client_id,omitempty"`
	AzureClientSecret *schemas.EnvVar `gorm:"type:text" json:"azure_client_secret,omitempty"`
	AzureTenantID     *schemas.EnvVar `gorm:"type:text" json:"azure_tenant_id,omitempty"`
	AzureScopesJSON   *string         `gorm:"column:azure_scopes;type:text" json:"-"` // JSON serialized []string

	// Vertex config fields (embedded)
	VertexProjectID       *schemas.EnvVar `gorm:"type:text" json:"vertex_project_id,omitempty"`
	VertexProjectNumber   *schemas.EnvVar `gorm:"type:text" json:"vertex_project_number,omitempty"`
	VertexRegion          *schemas.EnvVar `gorm:"type:text" json:"vertex_region,omitempty"`
	VertexAuthCredentials *schemas.EnvVar `gorm:"type:text" json:"vertex_auth_credentials,omitempty"`

	// Bedrock config fields (embedded)
	BedrockAccessKey         *schemas.EnvVar `gorm:"type:text" json:"bedrock_access_key,omitempty"`
	BedrockSecretKey         *schemas.EnvVar `gorm:"type:text" json:"bedrock_secret_key,omitempty"`
	BedrockSessionToken      *schemas.EnvVar `gorm:"type:text" json:"bedrock_session_token,omitempty"`
	BedrockRegion            *schemas.EnvVar `gorm:"type:text" json:"bedrock_region,omitempty"`
	BedrockARN               *schemas.EnvVar `gorm:"type:text" json:"bedrock_arn,omitempty"`
	BedrockRoleARN           *schemas.EnvVar `gorm:"type:text" json:"bedrock_role_arn,omitempty"`
	BedrockExternalID        *schemas.EnvVar `gorm:"type:text" json:"bedrock_external_id,omitempty"`
	BedrockRoleSessionName   *schemas.EnvVar `gorm:"type:text" json:"bedrock_role_session_name,omitempty"`
	BedrockBatchS3ConfigJSON *string         `gorm:"type:text" json:"-"` // JSON serialized schemas.BatchS3Config

	// VLLM config fields (embedded)
	VLLMUrl       *schemas.EnvVar `gorm:"type:text" json:"vllm_url,omitempty"`
	VLLMModelName *string         `gorm:"type:varchar(255)" json:"vllm_model_name,omitempty"`

	// Replicate config fields (embedded)
	ReplicateUseDeploymentsEndpoint *bool `gorm:"column:replicate_use_deployments_endpoint" json:"replicate_use_deployments_endpoint,omitempty"`

	// Ollama config fields (embedded)
	OllamaUrl *schemas.EnvVar `gorm:"type:text" json:"ollama_url,omitempty"`

	// SGL config fields (embedded)
	SGLUrl *schemas.EnvVar `gorm:"type:text" json:"sgl_url,omitempty"`

	// Batch API configuration
	UseForBatchAPI *bool `gorm:"default:false" json:"use_for_batch_api,omitempty"` // Whether this key can be used for batch API operations

	Status      string `gorm:"type:varchar(50);default:'unknown'" json:"status"`
	Description string `gorm:"type:text" json:"description,omitempty"`

	EncryptionStatus string `gorm:"type:varchar(20);default:'plain_text'" json:"-"`

	// Virtual fields for runtime use (not stored in DB)
	Models             schemas.WhiteList           `gorm:"-" json:"models"` // ["*"] allows all models; empty denies all (deny-by-default)
	BlacklistedModels  schemas.BlackList           `gorm:"-" json:"blacklisted_models"`
	Aliases            schemas.KeyAliases          `gorm:"-" json:"aliases,omitempty"`
	AzureKeyConfig     *schemas.AzureKeyConfig     `gorm:"-" json:"azure_key_config,omitempty"`
	VertexKeyConfig    *schemas.VertexKeyConfig    `gorm:"-" json:"vertex_key_config,omitempty"`
	BedrockKeyConfig   *schemas.BedrockKeyConfig   `gorm:"-" json:"bedrock_key_config,omitempty"`
	VLLMKeyConfig      *schemas.VLLMKeyConfig      `gorm:"-" json:"vllm_key_config,omitempty"`
	ReplicateKeyConfig *schemas.ReplicateKeyConfig `gorm:"-" json:"replicate_key_config,omitempty"`
	OllamaKeyConfig    *schemas.OllamaKeyConfig    `gorm:"-" json:"ollama_key_config,omitempty"`
	SGLKeyConfig       *schemas.SGLKeyConfig       `gorm:"-" json:"sgl_key_config,omitempty"`
}

// TableName sets the table name for each model
func (TableKey) TableName() string { return "config_keys" }

// BeforeSave is a GORM hook that serializes runtime config structs into JSON columns and
// encrypts sensitive fields (API key value, Azure endpoint/client ID/secret/tenant ID/API version,
// Vertex project ID/project number/region/credentials, Bedrock keys/region/ARN/deployments/
// batch S3 config) before writing to the database. Encryption runs last to ensure it
// operates on the final serialized values.
func (k *TableKey) BeforeSave(tx *gorm.DB) error {
	if err := k.Models.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(k.Models)
	if err != nil {
		return err
	}
	k.ModelsJSON = string(data)
	if err := k.BlacklistedModels.Validate(); err != nil {
		return err
	}
	data, err = json.Marshal(k.BlacklistedModels)
	if err != nil {
		return err
	}
	k.BlacklistedModelsJSON = string(data)
	if k.Enabled == nil {
		enabled := true // DB default
		k.Enabled = &enabled
	}
	if k.UseForBatchAPI == nil {
		useForBatchAPI := false // DB default
		k.UseForBatchAPI = &useForBatchAPI
	}
	// IMPORTANT: All *EnvVar fields assigned from provider config structs (AzureKeyConfig,
	// VertexKeyConfig, BedrockKeyConfig) MUST be value-copied before assignment. The caller
	// may retain the config struct pointer; if BeforeSave (or future encryption) mutates a
	// shared pointer, the caller's in-memory config is silently corrupted.
	// See: TestBeforeSave_DoesNotMutateSharedProviderConfigs
	if k.AzureKeyConfig != nil {
		if k.AzureKeyConfig.Endpoint.IsSet() {
			ep := k.AzureKeyConfig.Endpoint
			k.AzureEndpoint = &ep
		} else {
			k.AzureEndpoint = nil
		}
		if k.AzureKeyConfig.ClientID != nil {
			cid := *k.AzureKeyConfig.ClientID
			k.AzureClientID = &cid
		} else {
			k.AzureClientID = nil
		}
		if k.AzureKeyConfig.ClientSecret != nil {
			cs := *k.AzureKeyConfig.ClientSecret
			k.AzureClientSecret = &cs
		} else {
			k.AzureClientSecret = nil
		}
		if k.AzureKeyConfig.TenantID != nil {
			tid := *k.AzureKeyConfig.TenantID
			k.AzureTenantID = &tid
		} else {
			k.AzureTenantID = nil
		}
		if len(k.AzureKeyConfig.Scopes) > 0 {
			data, err := json.Marshal(k.AzureKeyConfig.Scopes)
			if err != nil {
				return err
			}
			s := string(data)
			k.AzureScopesJSON = &s
		} else {
			k.AzureScopesJSON = nil
		}
	} else {
		k.AzureEndpoint = nil
		k.AzureClientID = nil
		k.AzureClientSecret = nil
		k.AzureTenantID = nil
		k.AzureScopesJSON = nil
	}
	if k.VertexKeyConfig != nil {
		if k.VertexKeyConfig.ProjectID.IsSet() {
			pid := k.VertexKeyConfig.ProjectID
			k.VertexProjectID = &pid
		} else {
			k.VertexProjectID = nil
		}
		if k.VertexKeyConfig.ProjectNumber.IsSet() {
			pn := k.VertexKeyConfig.ProjectNumber
			k.VertexProjectNumber = &pn
		} else {
			k.VertexProjectNumber = nil
		}
		if k.VertexKeyConfig.Region.IsSet() {
			vr := k.VertexKeyConfig.Region
			k.VertexRegion = &vr
		} else {
			k.VertexRegion = nil
		}
		if k.VertexKeyConfig.AuthCredentials.IsSet() {
			ac := k.VertexKeyConfig.AuthCredentials
			k.VertexAuthCredentials = &ac
		} else {
			k.VertexAuthCredentials = nil
		}
	} else {
		k.VertexProjectID = nil
		k.VertexProjectNumber = nil
		k.VertexRegion = nil
		k.VertexAuthCredentials = nil
	}
	if k.BedrockKeyConfig != nil {
		if k.BedrockKeyConfig.AccessKey.IsSet() {
			// Copy to avoid encrypting the shared BedrockKeyConfig through the pointer
			ak := k.BedrockKeyConfig.AccessKey
			k.BedrockAccessKey = &ak
		} else {
			k.BedrockAccessKey = nil
		}
		if k.BedrockKeyConfig.SecretKey.IsSet() {
			// Copy to avoid encrypting the shared BedrockKeyConfig through the pointer
			sk := k.BedrockKeyConfig.SecretKey
			k.BedrockSecretKey = &sk
		} else {
			k.BedrockSecretKey = nil
		}
		// Copy to avoid encrypting the shared BedrockKeyConfig through the pointer
		if k.BedrockKeyConfig.SessionToken != nil {
			st := *k.BedrockKeyConfig.SessionToken
			k.BedrockSessionToken = &st
		} else {
			k.BedrockSessionToken = nil
		}
		if k.BedrockKeyConfig.Region != nil {
			br := *k.BedrockKeyConfig.Region
			k.BedrockRegion = &br
		} else {
			k.BedrockRegion = nil
		}
		if k.BedrockKeyConfig.ARN != nil {
			ba := *k.BedrockKeyConfig.ARN
			k.BedrockARN = &ba
		} else {
			k.BedrockARN = nil
		}
		if k.BedrockKeyConfig.RoleARN != nil {
			bra := *k.BedrockKeyConfig.RoleARN
			k.BedrockRoleARN = &bra
		} else {
			k.BedrockRoleARN = nil
		}
		if k.BedrockKeyConfig.ExternalID != nil {
			ei := *k.BedrockKeyConfig.ExternalID
			k.BedrockExternalID = &ei
		} else {
			k.BedrockExternalID = nil
		}
		if k.BedrockKeyConfig.RoleSessionName != nil {
			rsn := *k.BedrockKeyConfig.RoleSessionName
			k.BedrockRoleSessionName = &rsn
		} else {
			k.BedrockRoleSessionName = nil
		}
		if k.BedrockKeyConfig.BatchS3Config != nil {
			data, err := sonic.Marshal(k.BedrockKeyConfig.BatchS3Config)
			if err != nil {
				return err
			}
			s := string(data)
			k.BedrockBatchS3ConfigJSON = &s
		} else {
			k.BedrockBatchS3ConfigJSON = nil
		}
	} else {
		k.BedrockAccessKey = nil
		k.BedrockSecretKey = nil
		k.BedrockSessionToken = nil
		k.BedrockRegion = nil
		k.BedrockARN = nil
		k.BedrockRoleARN = nil
		k.BedrockExternalID = nil
		k.BedrockRoleSessionName = nil
		k.BedrockBatchS3ConfigJSON = nil
	}

	if k.Aliases != nil {
		data, err := sonic.Marshal(k.Aliases)
		if err != nil {
			return err
		}
		s := string(data)
		k.AliasesJSON = &s
	} else {
		k.AliasesJSON = nil
	}

	if k.VLLMKeyConfig != nil {
		if k.VLLMKeyConfig.URL.IsSet() {
			u := k.VLLMKeyConfig.URL // Value-copy to prevent shared pointer mutation
			k.VLLMUrl = &u
		} else {
			k.VLLMUrl = nil
		}
		if k.VLLMKeyConfig.ModelName != "" {
			mn := k.VLLMKeyConfig.ModelName
			k.VLLMModelName = &mn
		} else {
			k.VLLMModelName = nil
		}
	} else {
		k.VLLMUrl = nil
		k.VLLMModelName = nil
	}

	if k.ReplicateKeyConfig != nil {
		v := k.ReplicateKeyConfig.UseDeploymentsEndpoint
		k.ReplicateUseDeploymentsEndpoint = &v
	} else {
		k.ReplicateUseDeploymentsEndpoint = nil
	}

	if k.OllamaKeyConfig != nil && k.OllamaKeyConfig.URL.IsSet() {
		u := k.OllamaKeyConfig.URL
		k.OllamaUrl = &u
	} else {
		k.OllamaUrl = nil
	}

	if k.SGLKeyConfig != nil && k.SGLKeyConfig.URL.IsSet() {
		u := k.SGLKeyConfig.URL
		k.SGLUrl = &u
	} else {
		k.SGLUrl = nil
	}

	// Encrypt sensitive fields after serialization
	if VaultIsEnabled() {
		base := fmt.Sprintf("%s/%s/%s", VaultPrefix(), k.TableName(), k.KeyID)
		namer := tx.Statement.DB.NamingStrategy
		col := func(field string) string { return base + "/" + namer.ColumnName("", field) }
		// For each field: best-effort remove stale vault entry when the field is being cleared
		// or switched to an env-var, then store the new value (no-op if nil/empty/env-backed).
		removeVaultEnvVar(tx.Statement.Context, col("Value"), &k.Value)
		if err := vaultEnvVar(tx.Statement.Context, col("Value"), &k.Value); err != nil {
			return fmt.Errorf("failed to vault key value: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("AzureEndpoint"), k.AzureEndpoint)
		if err := vaultEnvVar(tx.Statement.Context, col("AzureEndpoint"), k.AzureEndpoint); err != nil {
			return fmt.Errorf("failed to vault azure endpoint: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("AzureClientID"), k.AzureClientID)
		if err := vaultEnvVar(tx.Statement.Context, col("AzureClientID"), k.AzureClientID); err != nil {
			return fmt.Errorf("failed to vault azure client id: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("AzureClientSecret"), k.AzureClientSecret)
		if err := vaultEnvVar(tx.Statement.Context, col("AzureClientSecret"), k.AzureClientSecret); err != nil {
			return fmt.Errorf("failed to vault azure client secret: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("AzureTenantID"), k.AzureTenantID)
		if err := vaultEnvVar(tx.Statement.Context, col("AzureTenantID"), k.AzureTenantID); err != nil {
			return fmt.Errorf("failed to vault azure tenant id: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("VertexProjectID"), k.VertexProjectID)
		if err := vaultEnvVar(tx.Statement.Context, col("VertexProjectID"), k.VertexProjectID); err != nil {
			return fmt.Errorf("failed to vault vertex project id: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("VertexProjectNumber"), k.VertexProjectNumber)
		if err := vaultEnvVar(tx.Statement.Context, col("VertexProjectNumber"), k.VertexProjectNumber); err != nil {
			return fmt.Errorf("failed to vault vertex project number: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("VertexRegion"), k.VertexRegion)
		if err := vaultEnvVar(tx.Statement.Context, col("VertexRegion"), k.VertexRegion); err != nil {
			return fmt.Errorf("failed to vault vertex region: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("VertexAuthCredentials"), k.VertexAuthCredentials)
		if err := vaultEnvVar(tx.Statement.Context, col("VertexAuthCredentials"), k.VertexAuthCredentials); err != nil {
			return fmt.Errorf("failed to vault vertex auth credentials: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("BedrockAccessKey"), k.BedrockAccessKey)
		if err := vaultEnvVar(tx.Statement.Context, col("BedrockAccessKey"), k.BedrockAccessKey); err != nil {
			return fmt.Errorf("failed to vault bedrock access key: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("BedrockSecretKey"), k.BedrockSecretKey)
		if err := vaultEnvVar(tx.Statement.Context, col("BedrockSecretKey"), k.BedrockSecretKey); err != nil {
			return fmt.Errorf("failed to vault bedrock secret key: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("BedrockSessionToken"), k.BedrockSessionToken)
		if err := vaultEnvVar(tx.Statement.Context, col("BedrockSessionToken"), k.BedrockSessionToken); err != nil {
			return fmt.Errorf("failed to vault bedrock session token: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("BedrockRegion"), k.BedrockRegion)
		if err := vaultEnvVar(tx.Statement.Context, col("BedrockRegion"), k.BedrockRegion); err != nil {
			return fmt.Errorf("failed to vault bedrock region: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("BedrockARN"), k.BedrockARN)
		if err := vaultEnvVar(tx.Statement.Context, col("BedrockARN"), k.BedrockARN); err != nil {
			return fmt.Errorf("failed to vault bedrock arn: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("BedrockRoleARN"), k.BedrockRoleARN)
		if err := vaultEnvVar(tx.Statement.Context, col("BedrockRoleARN"), k.BedrockRoleARN); err != nil {
			return fmt.Errorf("failed to vault bedrock role arn: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("BedrockExternalID"), k.BedrockExternalID)
		if err := vaultEnvVar(tx.Statement.Context, col("BedrockExternalID"), k.BedrockExternalID); err != nil {
			return fmt.Errorf("failed to vault bedrock external id: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("BedrockRoleSessionName"), k.BedrockRoleSessionName)
		if err := vaultEnvVar(tx.Statement.Context, col("BedrockRoleSessionName"), k.BedrockRoleSessionName); err != nil {
			return fmt.Errorf("failed to vault bedrock role session name: %w", err)
		}
		removeVaultString(tx.Statement.Context, col("BedrockBatchS3ConfigJSON"), k.BedrockBatchS3ConfigJSON)
		if err := vaultString(tx.Statement.Context, col("BedrockBatchS3ConfigJSON"), k.BedrockBatchS3ConfigJSON); err != nil {
			return fmt.Errorf("failed to vault bedrock batch s3 config: %w", err)
		}
		removeVaultString(tx.Statement.Context, col("AliasesJSON"), k.AliasesJSON)
		if err := vaultString(tx.Statement.Context, col("AliasesJSON"), k.AliasesJSON); err != nil {
			return fmt.Errorf("failed to vault aliases: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("VLLMUrl"), k.VLLMUrl)
		if err := vaultEnvVar(tx.Statement.Context, col("VLLMUrl"), k.VLLMUrl); err != nil {
			return fmt.Errorf("failed to vault vllm url: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("OllamaUrl"), k.OllamaUrl)
		if err := vaultEnvVar(tx.Statement.Context, col("OllamaUrl"), k.OllamaUrl); err != nil {
			return fmt.Errorf("failed to vault ollama url: %w", err)
		}
		removeVaultEnvVar(tx.Statement.Context, col("SGLUrl"), k.SGLUrl)
		if err := vaultEnvVar(tx.Statement.Context, col("SGLUrl"), k.SGLUrl); err != nil {
			return fmt.Errorf("failed to vault sgl url: %w", err)
		}
		k.EncryptionStatus = EncryptionStatusVault
	} else if encrypt.IsEnabled() {
		if err := encryptEnvVar(&k.Value); err != nil {
			return fmt.Errorf("failed to encrypt key value: %w", err)
		}
		// Azure
		if err := encryptEnvVarPtr(&k.AzureEndpoint); err != nil {
			return fmt.Errorf("failed to encrypt azure endpoint: %w", err)
		}
		if err := encryptEnvVarPtr(&k.AzureClientID); err != nil {
			return fmt.Errorf("failed to encrypt azure client id: %w", err)
		}
		if err := encryptEnvVarPtr(&k.AzureClientSecret); err != nil {
			return fmt.Errorf("failed to encrypt azure client secret: %w", err)
		}
		if err := encryptEnvVarPtr(&k.AzureTenantID); err != nil {
			return fmt.Errorf("failed to encrypt azure tenant id: %w", err)
		}
		// Vertex
		if err := encryptEnvVarPtr(&k.VertexProjectID); err != nil {
			return fmt.Errorf("failed to encrypt vertex project id: %w", err)
		}
		if err := encryptEnvVarPtr(&k.VertexProjectNumber); err != nil {
			return fmt.Errorf("failed to encrypt vertex project number: %w", err)
		}
		if err := encryptEnvVarPtr(&k.VertexRegion); err != nil {
			return fmt.Errorf("failed to encrypt vertex region: %w", err)
		}
		if err := encryptEnvVarPtr(&k.VertexAuthCredentials); err != nil {
			return fmt.Errorf("failed to encrypt vertex auth credentials: %w", err)
		}
		// Bedrock
		if err := encryptEnvVarPtr(&k.BedrockAccessKey); err != nil {
			return fmt.Errorf("failed to encrypt bedrock access key: %w", err)
		}
		if err := encryptEnvVarPtr(&k.BedrockSecretKey); err != nil {
			return fmt.Errorf("failed to encrypt bedrock secret key: %w", err)
		}
		if err := encryptEnvVarPtr(&k.BedrockSessionToken); err != nil {
			return fmt.Errorf("failed to encrypt bedrock session token: %w", err)
		}
		if err := encryptEnvVarPtr(&k.BedrockRegion); err != nil {
			return fmt.Errorf("failed to encrypt bedrock region: %w", err)
		}
		if err := encryptEnvVarPtr(&k.BedrockARN); err != nil {
			return fmt.Errorf("failed to encrypt bedrock arn: %w", err)
		}
		if err := encryptEnvVarPtr(&k.BedrockRoleARN); err != nil {
			return fmt.Errorf("failed to encrypt bedrock role arn: %w", err)
		}
		if err := encryptEnvVarPtr(&k.BedrockExternalID); err != nil {
			return fmt.Errorf("failed to encrypt bedrock external id: %w", err)
		}
		if err := encryptEnvVarPtr(&k.BedrockRoleSessionName); err != nil {
			return fmt.Errorf("failed to encrypt bedrock role session name: %w", err)
		}
		if err := encryptString(k.BedrockBatchS3ConfigJSON); err != nil {
			return fmt.Errorf("failed to encrypt bedrock batch s3 config: %w", err)
		}
		// Aliases
		if err := encryptString(k.AliasesJSON); err != nil {
			return fmt.Errorf("failed to encrypt aliases: %w", err)
		}
		// VLLM
		if err := encryptEnvVarPtr(&k.VLLMUrl); err != nil {
			return fmt.Errorf("failed to encrypt vllm url: %w", err)
		}
		// Ollama
		if err := encryptEnvVarPtr(&k.OllamaUrl); err != nil {
			return fmt.Errorf("failed to encrypt ollama url: %w", err)
		}
		// SGL
		if err := encryptEnvVarPtr(&k.SGLUrl); err != nil {
			return fmt.Errorf("failed to encrypt sgl url: %w", err)
		}
		k.EncryptionStatus = EncryptionStatusEncrypted
	}
	return nil
}

// AfterFind is a GORM hook that decrypts sensitive fields and reconstructs runtime config
// structs after reading from the database. Decryption runs first so that value copies into
// AzureKeyConfig, VertexKeyConfig, etc. receive plaintext data.
func (k *TableKey) AfterFind(tx *gorm.DB) error {
	// Decrypt sensitive fields before deserialization/reconstruction
	switch k.EncryptionStatus {
	case EncryptionStatusVault:
		ctx := context.Background()
		if tx != nil && tx.Statement != nil && tx.Statement.Context != nil {
			ctx = tx.Statement.Context
		}
		if err := resolveVaultEnvVar(ctx, &k.Value); err != nil {
			return fmt.Errorf("failed to resolve vault key value: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.AzureEndpoint); err != nil {
			return fmt.Errorf("failed to resolve vault azure endpoint: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.AzureClientID); err != nil {
			return fmt.Errorf("failed to resolve vault azure client id: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.AzureClientSecret); err != nil {
			return fmt.Errorf("failed to resolve vault azure client secret: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.AzureTenantID); err != nil {
			return fmt.Errorf("failed to resolve vault azure tenant id: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.VertexProjectID); err != nil {
			return fmt.Errorf("failed to resolve vault vertex project id: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.VertexProjectNumber); err != nil {
			return fmt.Errorf("failed to resolve vault vertex project number: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.VertexRegion); err != nil {
			return fmt.Errorf("failed to resolve vault vertex region: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.VertexAuthCredentials); err != nil {
			return fmt.Errorf("failed to resolve vault vertex auth credentials: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.BedrockAccessKey); err != nil {
			return fmt.Errorf("failed to resolve vault bedrock access key: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.BedrockSecretKey); err != nil {
			return fmt.Errorf("failed to resolve vault bedrock secret key: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.BedrockSessionToken); err != nil {
			return fmt.Errorf("failed to resolve vault bedrock session token: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.BedrockRegion); err != nil {
			return fmt.Errorf("failed to resolve vault bedrock region: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.BedrockARN); err != nil {
			return fmt.Errorf("failed to resolve vault bedrock arn: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.BedrockRoleARN); err != nil {
			return fmt.Errorf("failed to resolve vault bedrock role arn: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.BedrockExternalID); err != nil {
			return fmt.Errorf("failed to resolve vault bedrock external id: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.BedrockRoleSessionName); err != nil {
			return fmt.Errorf("failed to resolve vault bedrock role session name: %w", err)
		}
		if err := resolveVaultString(ctx, k.BedrockBatchS3ConfigJSON); err != nil {
			return fmt.Errorf("failed to resolve vault bedrock batch s3 config: %w", err)
		}
		if err := resolveVaultString(ctx, k.AliasesJSON); err != nil {
			return fmt.Errorf("failed to resolve vault aliases: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.VLLMUrl); err != nil {
			return fmt.Errorf("failed to resolve vault vllm url: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.OllamaUrl); err != nil {
			return fmt.Errorf("failed to resolve vault ollama url: %w", err)
		}
		if err := resolveVaultEnvVar(ctx, k.SGLUrl); err != nil {
			return fmt.Errorf("failed to resolve vault sgl url: %w", err)
		}
	case EncryptionStatusEncrypted:
		if err := decryptEnvVar(&k.Value); err != nil {
			return fmt.Errorf("failed to decrypt key value: %w", err)
		}
		// Azure
		if err := decryptEnvVarPtr(&k.AzureEndpoint); err != nil {
			return fmt.Errorf("failed to decrypt azure endpoint: %w", err)
		}
		if err := decryptEnvVarPtr(&k.AzureClientID); err != nil {
			return fmt.Errorf("failed to decrypt azure client id: %w", err)
		}
		if err := decryptEnvVarPtr(&k.AzureClientSecret); err != nil {
			return fmt.Errorf("failed to decrypt azure client secret: %w", err)
		}
		if err := decryptEnvVarPtr(&k.AzureTenantID); err != nil {
			return fmt.Errorf("failed to decrypt azure tenant id: %w", err)
		}
		// Vertex
		if err := decryptEnvVarPtr(&k.VertexProjectID); err != nil {
			return fmt.Errorf("failed to decrypt vertex project id: %w", err)
		}
		if err := decryptEnvVarPtr(&k.VertexProjectNumber); err != nil {
			return fmt.Errorf("failed to decrypt vertex project number: %w", err)
		}
		if err := decryptEnvVarPtr(&k.VertexRegion); err != nil {
			return fmt.Errorf("failed to decrypt vertex region: %w", err)
		}
		if err := decryptEnvVarPtr(&k.VertexAuthCredentials); err != nil {
			return fmt.Errorf("failed to decrypt vertex auth credentials: %w", err)
		}
		// Bedrock
		if err := decryptEnvVarPtr(&k.BedrockAccessKey); err != nil {
			return fmt.Errorf("failed to decrypt bedrock access key: %w", err)
		}
		if err := decryptEnvVarPtr(&k.BedrockSecretKey); err != nil {
			return fmt.Errorf("failed to decrypt bedrock secret key: %w", err)
		}
		if err := decryptEnvVarPtr(&k.BedrockSessionToken); err != nil {
			return fmt.Errorf("failed to decrypt bedrock session token: %w", err)
		}
		if err := decryptEnvVarPtr(&k.BedrockRegion); err != nil {
			return fmt.Errorf("failed to decrypt bedrock region: %w", err)
		}
		if err := decryptEnvVarPtr(&k.BedrockARN); err != nil {
			return fmt.Errorf("failed to decrypt bedrock arn: %w", err)
		}
		if err := decryptEnvVarPtr(&k.BedrockRoleARN); err != nil {
			return fmt.Errorf("failed to decrypt bedrock role arn: %w", err)
		}
		if err := decryptEnvVarPtr(&k.BedrockExternalID); err != nil {
			return fmt.Errorf("failed to decrypt bedrock external id: %w", err)
		}
		if err := decryptEnvVarPtr(&k.BedrockRoleSessionName); err != nil {
			return fmt.Errorf("failed to decrypt bedrock role session name: %w", err)
		}
		if err := decryptString(k.BedrockBatchS3ConfigJSON); err != nil {
			return fmt.Errorf("failed to decrypt bedrock batch s3 config: %w", err)
		}
		// Aliases
		if err := decryptString(k.AliasesJSON); err != nil {
			return fmt.Errorf("failed to decrypt aliases: %w", err)
		}
		// VLLM
		if err := decryptEnvVarPtr(&k.VLLMUrl); err != nil {
			return fmt.Errorf("failed to decrypt vllm url: %w", err)
		}
		// Ollama
		if err := decryptEnvVarPtr(&k.OllamaUrl); err != nil {
			return fmt.Errorf("failed to decrypt ollama url: %w", err)
		}
		// SGL
		if err := decryptEnvVarPtr(&k.SGLUrl); err != nil {
			return fmt.Errorf("failed to decrypt sgl url: %w", err)
		}
	}

	if k.ModelsJSON != "" {
		if err := json.Unmarshal([]byte(k.ModelsJSON), &k.Models); err != nil {
			return err
		}
	}
	if k.BlacklistedModelsJSON != "" {
		if err := json.Unmarshal([]byte(k.BlacklistedModelsJSON), &k.BlacklistedModels); err != nil {
			return err
		}
	}
	if k.Enabled == nil {
		enabled := true // DB default
		k.Enabled = &enabled
	}
	if k.UseForBatchAPI == nil {
		useForBatchAPI := false // DB default
		k.UseForBatchAPI = &useForBatchAPI
	}
	// Reconstruct Azure config if fields are present
	if k.AzureEndpoint != nil || k.AzureClientID != nil || k.AzureClientSecret != nil || k.AzureTenantID != nil || (k.AzureScopesJSON != nil && *k.AzureScopesJSON != "") {
		var scopes []string
		if k.AzureScopesJSON != nil && *k.AzureScopesJSON != "" {
			if err := json.Unmarshal([]byte(*k.AzureScopesJSON), &scopes); err != nil {
				return err
			}
		}
		azureConfig := &schemas.AzureKeyConfig{
			Endpoint:     *schemas.NewEnvVar(""),
			ClientID:     k.AzureClientID,
			ClientSecret: k.AzureClientSecret,
			TenantID:     k.AzureTenantID,
			Scopes:       scopes,
		}

		if k.AzureEndpoint != nil {
			azureConfig.Endpoint = *k.AzureEndpoint
		}

		k.AzureKeyConfig = azureConfig
	}
	// Reconstruct Vertex config if fields are present
	if k.VertexProjectID != nil || k.VertexProjectNumber != nil || k.VertexRegion != nil || k.VertexAuthCredentials != nil {
		config := &schemas.VertexKeyConfig{}

		if k.VertexProjectID != nil {
			config.ProjectID = *k.VertexProjectID
		}

		if k.VertexProjectNumber != nil {
			config.ProjectNumber = *k.VertexProjectNumber
		}

		if k.VertexRegion != nil {
			config.Region = *k.VertexRegion
		}
		if k.VertexAuthCredentials != nil {
			config.AuthCredentials = *k.VertexAuthCredentials
		}
		k.VertexKeyConfig = config
	}
	// Reconstruct Bedrock config if fields are present
	if k.BedrockAccessKey != nil || k.BedrockSecretKey != nil || k.BedrockSessionToken != nil || k.BedrockRegion != nil || k.BedrockARN != nil || k.BedrockRoleARN != nil || k.BedrockExternalID != nil || k.BedrockRoleSessionName != nil || (k.BedrockBatchS3ConfigJSON != nil && *k.BedrockBatchS3ConfigJSON != "") {
		bedrockConfig := &schemas.BedrockKeyConfig{}

		if k.BedrockAccessKey != nil {
			bedrockConfig.AccessKey = *k.BedrockAccessKey
		}

		bedrockConfig.SessionToken = k.BedrockSessionToken
		bedrockConfig.Region = k.BedrockRegion
		bedrockConfig.ARN = k.BedrockARN
		bedrockConfig.RoleARN = k.BedrockRoleARN
		bedrockConfig.ExternalID = k.BedrockExternalID
		bedrockConfig.RoleSessionName = k.BedrockRoleSessionName

		if k.BedrockSecretKey != nil {
			bedrockConfig.SecretKey = *k.BedrockSecretKey
		}

		if k.BedrockBatchS3ConfigJSON != nil && *k.BedrockBatchS3ConfigJSON != "" {
			var batchS3Config schemas.BatchS3Config
			if err := json.Unmarshal([]byte(*k.BedrockBatchS3ConfigJSON), &batchS3Config); err != nil {
				return err
			}
			bedrockConfig.BatchS3Config = &batchS3Config
		}

		k.BedrockKeyConfig = bedrockConfig
	}
	// Reconstruct Aliases
	if k.AliasesJSON != nil && *k.AliasesJSON != "" {
		var aliases schemas.KeyAliases
		if err := sonic.Unmarshal([]byte(*k.AliasesJSON), &aliases); err != nil {
			return err
		}
		k.Aliases = aliases
	} else {
		k.Aliases = nil
	}
	// Reconstruct VLLM config if fields are present
	if k.VLLMUrl != nil || (k.VLLMModelName != nil && *k.VLLMModelName != "") {
		vllmConfig := &schemas.VLLMKeyConfig{}
		if k.VLLMUrl != nil {
			vllmConfig.URL = *k.VLLMUrl
		}
		if k.VLLMModelName != nil {
			vllmConfig.ModelName = *k.VLLMModelName
		}
		k.VLLMKeyConfig = vllmConfig
	} else {
		k.VLLMKeyConfig = nil
	}
	// Reconstruct Replicate config if fields are present
	if k.ReplicateUseDeploymentsEndpoint != nil {
		k.ReplicateKeyConfig = &schemas.ReplicateKeyConfig{
			UseDeploymentsEndpoint: *k.ReplicateUseDeploymentsEndpoint,
		}
	} else {
		k.ReplicateKeyConfig = nil
	}
	// Reconstruct Ollama config if fields are present
	if k.OllamaUrl != nil {
		k.OllamaKeyConfig = &schemas.OllamaKeyConfig{
			URL: *k.OllamaUrl,
		}
	} else {
		k.OllamaKeyConfig = nil
	}
	// Reconstruct SGL config if fields are present
	if k.SGLUrl != nil {
		k.SGLKeyConfig = &schemas.SGLKeyConfig{
			URL: *k.SGLUrl,
		}
	} else {
		k.SGLKeyConfig = nil
	}
	return nil
}

// AfterDelete hook for best-effort vault cleanup on row deletion.
func (k *TableKey) AfterDelete(tx *gorm.DB) error {
	if k.EncryptionStatus != EncryptionStatusVault || VaultHooks.Remove == nil {
		return nil
	}
	colName := func(field string) string {
		return tx.Statement.DB.NamingStrategy.ColumnName("", field)
	}
	base := fmt.Sprintf("%s/%s/%s", VaultPrefix(), k.TableName(), k.KeyID)
	for _, field := range []string{
		"Value",
		"AzureEndpoint", "AzureClientID", "AzureClientSecret", "AzureTenantID",
		"VertexProjectID", "VertexProjectNumber", "VertexRegion", "VertexAuthCredentials",
		"BedrockAccessKey", "BedrockSecretKey", "BedrockSessionToken", "BedrockRegion",
		"BedrockARN", "BedrockRoleARN", "BedrockExternalID", "BedrockRoleSessionName",
		"BedrockBatchS3ConfigJSON", "AliasesJSON",
		"VLLMUrl", "OllamaUrl", "SGLUrl",
	} {
		_ = VaultHooks.Remove(tx.Statement.Context, fmt.Sprintf("%s/%s", base, colName(field)))
	}
	return nil
}
