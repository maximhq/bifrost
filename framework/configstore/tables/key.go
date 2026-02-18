package tables

import (
	"encoding/json"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
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
	AzureAPIVersion      *schemas.EnvVar `gorm:"type:varchar(50)" json:"azure_api_version,omitempty"`
	AzureDeploymentsJSON *string         `gorm:"type:text" json:"-"` // JSON serialized map[string]string
	AzureClientID        *schemas.EnvVar `gorm:"type:varchar(255)" json:"azure_client_id,omitempty"`
	AzureClientSecret    *schemas.EnvVar `gorm:"type:text" json:"azure_client_secret,omitempty"`
	AzureTenantID        *schemas.EnvVar `gorm:"type:varchar(255)" json:"azure_tenant_id,omitempty"`
	AzureScopesJSON      *string         `gorm:"column:azure_scopes;type:text" json:"-"` // JSON serialized []string

	// Vertex config fields (embedded)
	VertexProjectID       *schemas.EnvVar `gorm:"type:varchar(255)" json:"vertex_project_id,omitempty"`
	VertexProjectNumber   *schemas.EnvVar `gorm:"type:varchar(255)" json:"vertex_project_number,omitempty"`
	VertexRegion          *schemas.EnvVar `gorm:"type:varchar(100)" json:"vertex_region,omitempty"`
	VertexAuthCredentials *schemas.EnvVar `gorm:"type:text" json:"vertex_auth_credentials,omitempty"`
	VertexDeploymentsJSON *string         `gorm:"type:text" json:"-"` // JSON serialized map[string]string

	// Bedrock config fields (embedded)
	BedrockAccessKey         *schemas.EnvVar `gorm:"type:varchar(255)" json:"bedrock_access_key,omitempty"`
	BedrockSecretKey         *schemas.EnvVar `gorm:"type:text" json:"bedrock_secret_key,omitempty"`
	BedrockSessionToken      *schemas.EnvVar `gorm:"type:text" json:"bedrock_session_token,omitempty"`
	BedrockRegion            *schemas.EnvVar `gorm:"type:varchar(100)" json:"bedrock_region,omitempty"`
	BedrockARN               *schemas.EnvVar `gorm:"type:text" json:"bedrock_arn,omitempty"`
	BedrockDeploymentsJSON   *string         `gorm:"type:text" json:"-"` // JSON serialized map[string]string
	BedrockBatchS3ConfigJSON *string         `gorm:"type:text" json:"-"` // JSON serialized schemas.BatchS3Config

	// Replicate config fields (embedded)
	ReplicateDeploymentsJSON *string `gorm:"type:text" json:"-"` // JSON serialized map[string]string

	// SAP AI Core config fields (embedded)
	SAPAICoreClientID        *schemas.EnvVar `gorm:"column:sapaicore_client_id;type:text" json:"sapaicore_client_id,omitempty"`
	SAPAICoreClientSecret    *schemas.EnvVar `gorm:"column:sapaicore_client_secret;type:text" json:"sapaicore_client_secret,omitempty"`
	SAPAICoreAuthURL         *schemas.EnvVar `gorm:"column:sapaicore_auth_url;type:text" json:"sapaicore_auth_url,omitempty"`
	SAPAICoreBaseURL         *schemas.EnvVar `gorm:"column:sapaicore_base_url;type:text" json:"sapaicore_base_url,omitempty"`
	SAPAICoreResourceGroup   *schemas.EnvVar `gorm:"column:sapaicore_resource_group;type:varchar(255)" json:"sapaicore_resource_group,omitempty"`
	SAPAICoreDeploymentsJSON *string         `gorm:"column:sapaicore_deployments_json;type:text" json:"-"` // JSON serialized map[string]string

	// Batch API configuration
	UseForBatchAPI *bool `gorm:"default:false" json:"use_for_batch_api,omitempty"` // Whether this key can be used for batch API operations

	Status      string `gorm:"type:varchar(50);default:'unknown'" json:"status"`
	Description string `gorm:"type:text" json:"description,omitempty"`

	// Virtual fields for runtime use (not stored in DB)
	Models             []string                    `gorm:"-" json:"models"`
	AzureKeyConfig     *schemas.AzureKeyConfig     `gorm:"-" json:"azure_key_config,omitempty"`
	VertexKeyConfig    *schemas.VertexKeyConfig    `gorm:"-" json:"vertex_key_config,omitempty"`
	BedrockKeyConfig   *schemas.BedrockKeyConfig   `gorm:"-" json:"bedrock_key_config,omitempty"`
	ReplicateKeyConfig *schemas.ReplicateKeyConfig `gorm:"-" json:"replicate_key_config,omitempty"`
	SAPAICoreKeyConfig *schemas.SAPAICoreKeyConfig `gorm:"-" json:"sapaicore_key_config,omitempty"`
}

// TableName sets the table name for each model
func (TableKey) TableName() string { return "config_keys" }

// BeforeSave is called before saving the key to the database
func (k *TableKey) BeforeSave(tx *gorm.DB) error {
	// BeforeSave is called before saving the key to the database
	if k.Models != nil {
		data, err := json.Marshal(k.Models)
		if err != nil {
			return err
		}
		k.ModelsJSON = string(data)
	} else {
		k.ModelsJSON = "[]"
	}
	// BeforeSave is called before saving the key to the database
	if k.Enabled == nil {
		enabled := true // DB default
		k.Enabled = &enabled
	}
	if k.UseForBatchAPI == nil {
		useForBatchAPI := false // DB default
		k.UseForBatchAPI = &useForBatchAPI
	}
	// BeforeSave is called before saving the key to the database
	if k.AzureKeyConfig != nil {
		if k.AzureKeyConfig.Endpoint.GetValue() != "" {
			k.AzureEndpoint = &k.AzureKeyConfig.Endpoint
		} else {
			k.AzureEndpoint = nil
		}
		k.AzureAPIVersion = k.AzureKeyConfig.APIVersion
		k.AzureClientID = k.AzureKeyConfig.ClientID
		k.AzureClientSecret = k.AzureKeyConfig.ClientSecret
		k.AzureTenantID = k.AzureKeyConfig.TenantID
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
	// BeforeSave is called before saving the key to the database
	if k.VertexKeyConfig != nil {
		if k.VertexKeyConfig.ProjectID.GetValue() != "" {
			k.VertexProjectID = &k.VertexKeyConfig.ProjectID
		} else {
			k.VertexProjectID = nil
		}
		if k.VertexKeyConfig.ProjectNumber.GetValue() != "" {
			k.VertexProjectNumber = &k.VertexKeyConfig.ProjectNumber
		} else {
			k.VertexProjectNumber = nil
		}
		if k.VertexKeyConfig.Region.GetValue() != "" {
			k.VertexRegion = &k.VertexKeyConfig.Region
		} else {
			k.VertexRegion = nil
		}
		if k.VertexKeyConfig.AuthCredentials.GetValue() != "" {
			k.VertexAuthCredentials = &k.VertexKeyConfig.AuthCredentials
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
	// BeforeSave is called before saving the key to the database
	if k.BedrockKeyConfig != nil {
		if k.BedrockKeyConfig.AccessKey.GetValue() != "" {
			k.BedrockAccessKey = &k.BedrockKeyConfig.AccessKey
		} else {
			k.BedrockAccessKey = nil
		}
		if k.BedrockKeyConfig.SecretKey.GetValue() != "" {
			k.BedrockSecretKey = &k.BedrockKeyConfig.SecretKey
		} else {
			k.BedrockSecretKey = nil
		}
		k.BedrockSessionToken = k.BedrockKeyConfig.SessionToken
		k.BedrockRegion = k.BedrockKeyConfig.Region
		k.BedrockARN = k.BedrockKeyConfig.ARN
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
	// BeforeSave is called before saving the key to the database
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
	return nil
}

func (k *TableKey) AfterFind(tx *gorm.DB) error {
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
	// Reconstruct SAP AI Core config if fields are present
	if k.SAPAICoreClientID != nil || k.SAPAICoreClientSecret != nil || k.SAPAICoreAuthURL != nil || k.SAPAICoreBaseURL != nil || k.SAPAICoreResourceGroup != nil || (k.SAPAICoreDeploymentsJSON != nil && *k.SAPAICoreDeploymentsJSON != "") {
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
	}
	return nil
}
