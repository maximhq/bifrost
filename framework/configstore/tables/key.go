package tables

import (
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
	ID         uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	Name       string         `gorm:"type:varchar(255);uniqueIndex:idx_key_name;not null" json:"name"`
	ProviderID uint           `gorm:"index;not null" json:"provider_id"`
	Provider   string         `gorm:"index;type:varchar(50)" json:"provider"`                          // ModelProvider as string
	KeyID      string         `gorm:"type:varchar(255);uniqueIndex:idx_key_id;not null" json:"key_id"` // UUID from schemas.Key
	Value      schemas.EnvVar `gorm:"type:text;not null" json:"value"`
	ModelsJSON string         `gorm:"type:text" json:"-"` // JSON serialized []string
	Weight     *float64       `json:"weight"`
	Enabled    *bool          `gorm:"default:true" json:"enabled,omitempty"`
	CreatedAt  time.Time      `gorm:"index;not null" json:"created_at"`
	UpdatedAt  time.Time      `gorm:"index;not null" json:"updated_at"`

	// Config hash is used to detect changes synced from config.json file
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	// Azure config fields (embedded instead of separate table for simplicity)
	AzureEndpoint        *schemas.EnvVar `gorm:"type:text" json:"azure_endpoint,omitempty"`
	AzureAPIVersion      *schemas.EnvVar `gorm:"type:text" json:"azure_api_version,omitempty"`
	AzureDeploymentsJSON *string         `gorm:"type:text" json:"-"` // JSON serialized map[string]string
	AzureClientID        *schemas.EnvVar `gorm:"type:text" json:"azure_client_id,omitempty"`
	AzureClientSecret    *schemas.EnvVar `gorm:"type:text" json:"azure_client_secret,omitempty"`
	AzureTenantID        *schemas.EnvVar `gorm:"type:text" json:"azure_tenant_id,omitempty"`
	AzureScopesJSON      *string         `gorm:"column:azure_scopes;type:text" json:"-"` // JSON serialized []string

	// Vertex config fields (embedded)
	VertexProjectID       *schemas.EnvVar `gorm:"type:text" json:"vertex_project_id,omitempty"`
	VertexProjectNumber   *schemas.EnvVar `gorm:"type:text" json:"vertex_project_number,omitempty"`
	VertexRegion          *schemas.EnvVar `gorm:"type:text" json:"vertex_region,omitempty"`
	VertexAuthCredentials *schemas.EnvVar `gorm:"type:text" json:"vertex_auth_credentials,omitempty"`
	VertexDeploymentsJSON *string         `gorm:"type:text" json:"-"` // JSON serialized map[string]string

	// Bedrock config fields (embedded)
	BedrockAccessKey         *schemas.EnvVar `gorm:"type:text" json:"bedrock_access_key,omitempty"`
	BedrockSecretKey         *schemas.EnvVar `gorm:"type:text" json:"bedrock_secret_key,omitempty"`
	BedrockSessionToken      *schemas.EnvVar `gorm:"type:text" json:"bedrock_session_token,omitempty"`
	BedrockRegion            *schemas.EnvVar `gorm:"type:text" json:"bedrock_region,omitempty"`
	BedrockARN               *schemas.EnvVar `gorm:"type:text" json:"bedrock_arn,omitempty"`
	BedrockDeploymentsJSON   *string         `gorm:"type:text" json:"-"` // JSON serialized map[string]string
	BedrockBatchS3ConfigJSON *string         `gorm:"type:text" json:"-"` // JSON serialized schemas.BatchS3Config

	// Replicate config fields (embedded)
	ReplicateDeploymentsJSON *string `gorm:"type:text" json:"-"` // JSON serialized map[string]string

	// VLLM config fields (embedded)
	VLLMUrl       *schemas.EnvVar `gorm:"type:text" json:"vllm_url,omitempty"`
	VLLMModelName *string         `gorm:"type:varchar(255)" json:"vllm_model_name,omitempty"`

	// SAP AI Core config fields (embedded)
	SAPAICoreClientID        *schemas.EnvVar `gorm:"column:sapaicore_client_id;type:text" json:"sapaicore_client_id,omitempty"`
	SAPAICoreClientSecret    *schemas.EnvVar `gorm:"column:sapaicore_client_secret;type:text" json:"sapaicore_client_secret,omitempty"`
	SAPAICoreAuthURL         *schemas.EnvVar `gorm:"column:sapaicore_auth_url;type:text" json:"sapaicore_auth_url,omitempty"`
	SAPAICoreBaseURL         *schemas.EnvVar `gorm:"column:sapaicore_base_url;type:text" json:"sapaicore_base_url,omitempty"`
	SAPAICoreResourceGroup   *schemas.EnvVar `gorm:"column:sapaicore_resource_group;type:varchar(255)" json:"sapaicore_resource_group,omitempty"`
	SAPAICoreDeploymentsJSON *string         `gorm:"column:sapaicore_deployments_json;type:text" json:"-"`

	// Batch API configuration
	UseForBatchAPI *bool `gorm:"default:false" json:"use_for_batch_api,omitempty"` // Whether this key can be used for batch API operations

	Status      string `gorm:"type:varchar(50);default:'unknown'" json:"status"`
	Description string `gorm:"type:text" json:"description,omitempty"`

	EncryptionStatus string `gorm:"type:varchar(20);default:'plain_text'" json:"-"`

	// Virtual fields for runtime use (not stored in DB)
	Models             []string                    `gorm:"-" json:"models"`
	AzureKeyConfig     *schemas.AzureKeyConfig     `gorm:"-" json:"azure_key_config,omitempty"`
	VertexKeyConfig    *schemas.VertexKeyConfig    `gorm:"-" json:"vertex_key_config,omitempty"`
	BedrockKeyConfig   *schemas.BedrockKeyConfig   `gorm:"-" json:"bedrock_key_config,omitempty"`
	ReplicateKeyConfig *schemas.ReplicateKeyConfig `gorm:"-" json:"replicate_key_config,omitempty"`
	VLLMKeyConfig      *schemas.VLLMKeyConfig      `gorm:"-" json:"vllm_key_config,omitempty"`
	SAPAICoreKeyConfig *schemas.SAPAICoreKeyConfig `gorm:"-" json:"sapaicore_key_config,omitempty"`
}

// TableName sets the table name for each model
func (TableKey) TableName() string { return "config_keys" }

// BeforeSave is a GORM hook that serializes runtime config structs into JSON columns and
// encrypts sensitive fields (API key value, Azure endpoint/client ID/secret/tenant ID/API version,
// Vertex project ID/project number/region/credentials, Bedrock keys/region/ARN/deployments/
// batch S3 config) before writing to the database. Encryption runs last to ensure it
// operates on the final serialized values.
func (k *TableKey) BeforeSave(tx *gorm.DB) error {
	if k.Models != nil {
		data, err := json.Marshal(k.Models)
		if err != nil {
			return err
		}
		k.ModelsJSON = string(data)
	} else {
		k.ModelsJSON = "[]"
	}
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
		if k.AzureKeyConfig.Endpoint.GetValue() != "" {
			ep := k.AzureKeyConfig.Endpoint
			k.AzureEndpoint = &ep
		} else {
			k.AzureEndpoint = nil
		}
		if k.AzureKeyConfig.APIVersion != nil {
			av := *k.AzureKeyConfig.APIVersion
			k.AzureAPIVersion = &av
		} else {
			k.AzureAPIVersion = nil
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
		if k.AzureKeyConfig.Deployments != nil {
			data, err := json.Marshal(k.AzureKeyConfig.Deployments)
			if err != nil {
				return err
			}
			s := string(data)
			k.AzureDeploymentsJSON = &s
		} else {
			k.AzureDeploymentsJSON = nil
		}
	} else {
		k.AzureEndpoint = nil
		k.AzureAPIVersion = nil
		k.AzureDeploymentsJSON = nil
		k.AzureClientID = nil
		k.AzureClientSecret = nil
		k.AzureTenantID = nil
		k.AzureScopesJSON = nil
	}
	if k.VertexKeyConfig != nil {
		if k.VertexKeyConfig.ProjectID.GetValue() != "" {
			pid := k.VertexKeyConfig.ProjectID
			k.VertexProjectID = &pid
		} else {
			k.VertexProjectID = nil
		}
		if k.VertexKeyConfig.ProjectNumber.GetValue() != "" {
			pn := k.VertexKeyConfig.ProjectNumber
			k.VertexProjectNumber = &pn
		} else {
			k.VertexProjectNumber = nil
		}
		if k.VertexKeyConfig.Region.GetValue() != "" {
			vr := k.VertexKeyConfig.Region
			k.VertexRegion = &vr
		} else {
			k.VertexRegion = nil
		}
		if k.VertexKeyConfig.AuthCredentials.GetValue() != "" {
			ac := k.VertexKeyConfig.AuthCredentials
			k.VertexAuthCredentials = &ac
		} else {
			k.VertexAuthCredentials = nil
		}
		if k.VertexKeyConfig.Deployments != nil {
			data, err := json.Marshal(k.VertexKeyConfig.Deployments)
			if err != nil {
				return err
			}
			s := string(data)
			k.VertexDeploymentsJSON = &s
		} else {
			k.VertexDeploymentsJSON = nil
		}
	} else {
		k.VertexProjectID = nil
		k.VertexProjectNumber = nil
		k.VertexRegion = nil
		k.VertexAuthCredentials = nil
		k.VertexDeploymentsJSON = nil
	}
	if k.BedrockKeyConfig != nil {
		if k.BedrockKeyConfig.AccessKey.GetValue() != "" {
			// Copy to avoid encrypting the shared BedrockKeyConfig through the pointer
			ak := k.BedrockKeyConfig.AccessKey
			k.BedrockAccessKey = &ak
		} else {
			k.BedrockAccessKey = nil
		}
		if k.BedrockKeyConfig.SecretKey.GetValue() != "" {
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
		if k.BedrockKeyConfig.Deployments != nil {
			data, err := sonic.Marshal(k.BedrockKeyConfig.Deployments)
			if err != nil {
				return err
			}
			s := string(data)
			k.BedrockDeploymentsJSON = &s
		} else {
			k.BedrockDeploymentsJSON = nil
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
		k.BedrockDeploymentsJSON = nil
		k.BedrockBatchS3ConfigJSON = nil
	}

	if k.ReplicateKeyConfig != nil {
		if k.ReplicateKeyConfig.Deployments != nil {
			data, err := sonic.Marshal(k.ReplicateKeyConfig.Deployments)
			if err != nil {
				return err
			}
			s := string(data)
			k.ReplicateDeploymentsJSON = &s
		} else {
			k.ReplicateDeploymentsJSON = nil
		}
	} else {
		k.ReplicateDeploymentsJSON = nil
	}

	if k.VLLMKeyConfig != nil {
		if k.VLLMKeyConfig.URL.GetValue() != "" {
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

	// Serialize SAP AI Core config to DB columns
	if k.SAPAICoreKeyConfig != nil {
		if k.SAPAICoreKeyConfig.ClientID.GetValue() != "" {
			k.SAPAICoreClientID = &k.SAPAICoreKeyConfig.ClientID
		} else {
			k.SAPAICoreClientID = nil
		}
		if k.SAPAICoreKeyConfig.ClientSecret.GetValue() != "" {
			k.SAPAICoreClientSecret = &k.SAPAICoreKeyConfig.ClientSecret
		} else {
			k.SAPAICoreClientSecret = nil
		}
		if k.SAPAICoreKeyConfig.AuthURL.GetValue() != "" {
			k.SAPAICoreAuthURL = &k.SAPAICoreKeyConfig.AuthURL
		} else {
			k.SAPAICoreAuthURL = nil
		}
		if k.SAPAICoreKeyConfig.BaseURL.GetValue() != "" {
			k.SAPAICoreBaseURL = &k.SAPAICoreKeyConfig.BaseURL
		} else {
			k.SAPAICoreBaseURL = nil
		}
		if k.SAPAICoreKeyConfig.ResourceGroup.GetValue() != "" {
			k.SAPAICoreResourceGroup = &k.SAPAICoreKeyConfig.ResourceGroup
		} else {
			k.SAPAICoreResourceGroup = nil
		}
		if k.SAPAICoreKeyConfig.Deployments != nil {
			data, err := sonic.Marshal(k.SAPAICoreKeyConfig.Deployments)
			if err != nil {
				return err
			}
			s := string(data)
			k.SAPAICoreDeploymentsJSON = &s
		} else {
			k.SAPAICoreDeploymentsJSON = nil
		}
	} else {
		k.SAPAICoreClientID = nil
		k.SAPAICoreClientSecret = nil
		k.SAPAICoreAuthURL = nil
		k.SAPAICoreBaseURL = nil
		k.SAPAICoreResourceGroup = nil
		k.SAPAICoreDeploymentsJSON = nil
	}

	// Encrypt sensitive fields after serialization
	if encrypt.IsEnabled() {
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
		if err := encryptEnvVarPtr(&k.AzureAPIVersion); err != nil {
			return fmt.Errorf("failed to encrypt azure api version: %w", err)
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
		if err := encryptString(k.BedrockDeploymentsJSON); err != nil {
			return fmt.Errorf("failed to encrypt bedrock deployments: %w", err)
		}
		if err := encryptString(k.BedrockBatchS3ConfigJSON); err != nil {
			return fmt.Errorf("failed to encrypt bedrock batch s3 config: %w", err)
		}
		// VLLM
		if err := encryptEnvVarPtr(&k.VLLMUrl); err != nil {
			return fmt.Errorf("failed to encrypt vllm url: %w", err)
		}
		// SAP AI Core
		if err := encryptEnvVarPtr(&k.SAPAICoreClientID); err != nil {
			return fmt.Errorf("failed to encrypt sapaicore client id: %w", err)
		}
		if err := encryptEnvVarPtr(&k.SAPAICoreClientSecret); err != nil {
			return fmt.Errorf("failed to encrypt sapaicore client secret: %w", err)
		}
		if err := encryptEnvVarPtr(&k.SAPAICoreAuthURL); err != nil {
			return fmt.Errorf("failed to encrypt sapaicore auth url: %w", err)
		}
		if err := encryptEnvVarPtr(&k.SAPAICoreBaseURL); err != nil {
			return fmt.Errorf("failed to encrypt sapaicore base url: %w", err)
		}
		if err := encryptEnvVarPtr(&k.SAPAICoreResourceGroup); err != nil {
			return fmt.Errorf("failed to encrypt sapaicore resource group: %w", err)
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
	if k.EncryptionStatus == EncryptionStatusEncrypted {
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
		if err := decryptEnvVarPtr(&k.AzureAPIVersion); err != nil {
			return fmt.Errorf("failed to decrypt azure api version: %w", err)
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
		if err := decryptString(k.BedrockDeploymentsJSON); err != nil {
			return fmt.Errorf("failed to decrypt bedrock deployments: %w", err)
		}
		if err := decryptString(k.BedrockBatchS3ConfigJSON); err != nil {
			return fmt.Errorf("failed to decrypt bedrock batch s3 config: %w", err)
		}
		// VLLM
		if err := decryptEnvVarPtr(&k.VLLMUrl); err != nil {
			return fmt.Errorf("failed to decrypt vllm url: %w", err)
		}
		// SAP AI Core
		if err := decryptEnvVarPtr(&k.SAPAICoreClientID); err != nil {
			return fmt.Errorf("failed to decrypt sapaicore client id: %w", err)
		}
		if err := decryptEnvVarPtr(&k.SAPAICoreClientSecret); err != nil {
			return fmt.Errorf("failed to decrypt sapaicore client secret: %w", err)
		}
		if err := decryptEnvVarPtr(&k.SAPAICoreAuthURL); err != nil {
			return fmt.Errorf("failed to decrypt sapaicore auth url: %w", err)
		}
		if err := decryptEnvVarPtr(&k.SAPAICoreBaseURL); err != nil {
			return fmt.Errorf("failed to decrypt sapaicore base url: %w", err)
		}
		if err := decryptEnvVarPtr(&k.SAPAICoreResourceGroup); err != nil {
			return fmt.Errorf("failed to decrypt sapaicore resource group: %w", err)
		}
	}

	if k.ModelsJSON != "" {
		if err := json.Unmarshal([]byte(k.ModelsJSON), &k.Models); err != nil {
			return err
		}
	} else {
		k.Models = []string{}
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
	if k.AzureEndpoint != nil {
		var scopes []string
		if k.AzureScopesJSON != nil && *k.AzureScopesJSON != "" {
			if err := json.Unmarshal([]byte(*k.AzureScopesJSON), &scopes); err != nil {
				return err
			}
		}
		azureConfig := &schemas.AzureKeyConfig{
			Endpoint:     *schemas.NewEnvVar(""),
			APIVersion:   k.AzureAPIVersion,
			ClientID:     k.AzureClientID,
			ClientSecret: k.AzureClientSecret,
			TenantID:     k.AzureTenantID,
			Scopes:       scopes,
		}

		if k.AzureEndpoint != nil {
			azureConfig.Endpoint = *k.AzureEndpoint
		}

		if k.AzureDeploymentsJSON != nil {
			var deployments map[string]string
			if err := json.Unmarshal([]byte(*k.AzureDeploymentsJSON), &deployments); err != nil {
				return err
			}
			azureConfig.Deployments = deployments
		} else {
			azureConfig.Deployments = nil
		}

		k.AzureKeyConfig = azureConfig
	}
	// Reconstruct Vertex config if fields are present
	if k.VertexProjectID != nil || k.VertexProjectNumber != nil || k.VertexRegion != nil || k.VertexAuthCredentials != nil || (k.VertexDeploymentsJSON != nil && *k.VertexDeploymentsJSON != "") {
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
		if k.VertexDeploymentsJSON != nil {
			var deployments map[string]string
			if err := json.Unmarshal([]byte(*k.VertexDeploymentsJSON), &deployments); err != nil {
				return err
			}
			config.Deployments = deployments
		} else {
			config.Deployments = nil
		}

		k.VertexKeyConfig = config
	}
	// Reconstruct Bedrock config if fields are present
	if k.BedrockAccessKey != nil || k.BedrockSecretKey != nil || k.BedrockSessionToken != nil || k.BedrockRegion != nil || k.BedrockARN != nil || (k.BedrockDeploymentsJSON != nil && *k.BedrockDeploymentsJSON != "") || (k.BedrockBatchS3ConfigJSON != nil && *k.BedrockBatchS3ConfigJSON != "") {
		bedrockConfig := &schemas.BedrockKeyConfig{}

		if k.BedrockAccessKey != nil {
			bedrockConfig.AccessKey = *k.BedrockAccessKey
		}

		bedrockConfig.SessionToken = k.BedrockSessionToken
		bedrockConfig.Region = k.BedrockRegion
		bedrockConfig.ARN = k.BedrockARN

		if k.BedrockSecretKey != nil {
			bedrockConfig.SecretKey = *k.BedrockSecretKey
		}

		if k.BedrockDeploymentsJSON != nil {
			var deployments map[string]string
			if err := json.Unmarshal([]byte(*k.BedrockDeploymentsJSON), &deployments); err != nil {
				return err
			}
			bedrockConfig.Deployments = deployments
		} else {
			bedrockConfig.Deployments = nil
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
	// Reconstruct Replicate config if fields are present
	if k.ReplicateDeploymentsJSON != nil && *k.ReplicateDeploymentsJSON != "" {
		replicateConfig := &schemas.ReplicateKeyConfig{}
		var deployments map[string]string
		if err := json.Unmarshal([]byte(*k.ReplicateDeploymentsJSON), &deployments); err != nil {
			return err
		}
		replicateConfig.Deployments = deployments
		k.ReplicateKeyConfig = replicateConfig
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

	// Reconstruct SAP AI Core config if fields are present
	if k.SAPAICoreClientID != nil || k.SAPAICoreClientSecret != nil || k.SAPAICoreAuthURL != nil || k.SAPAICoreBaseURL != nil || k.SAPAICoreResourceGroup != nil {
		sapConfig := &schemas.SAPAICoreKeyConfig{}
		if k.SAPAICoreClientID != nil {
			sapConfig.ClientID = *k.SAPAICoreClientID
		}
		if k.SAPAICoreClientSecret != nil {
			sapConfig.ClientSecret = *k.SAPAICoreClientSecret
		}
		if k.SAPAICoreAuthURL != nil {
			sapConfig.AuthURL = *k.SAPAICoreAuthURL
		}
		if k.SAPAICoreBaseURL != nil {
			sapConfig.BaseURL = *k.SAPAICoreBaseURL
		}
		if k.SAPAICoreResourceGroup != nil {
			sapConfig.ResourceGroup = *k.SAPAICoreResourceGroup
		}
		if k.SAPAICoreDeploymentsJSON != nil && *k.SAPAICoreDeploymentsJSON != "" {
			var deployments map[string]string
			if err := json.Unmarshal([]byte(*k.SAPAICoreDeploymentsJSON), &deployments); err != nil {
				return err
			}
			sapConfig.Deployments = deployments
		}
		k.SAPAICoreKeyConfig = sapConfig
	} else {
		k.SAPAICoreKeyConfig = nil
	}
	return nil
}
