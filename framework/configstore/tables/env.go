package tables

import "time"

// TableEnvKey represents environment variable tracking in the database
type TableEnvKey struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	EnvVar     string    `gorm:"type:varchar(255);index;not null" json:"env_var"`
	Provider   string    `gorm:"type:varchar(50);index" json:"provider"`        // Empty for MCP/client configs
	KeyType    string    `gorm:"type:varchar(50);not null" json:"key_type"`     // "api_key", "azure_config", "vertex_config", "bedrock_config", "connection_string"
	ConfigPath string    `gorm:"type:varchar(500);not null" json:"config_path"` // Descriptive path of where this env var is used
	KeyID      string    `gorm:"type:varchar(255);index" json:"key_id"`         // Key UUID (empty for non-key configs)
	CreatedAt  time.Time `gorm:"index;not null" json:"created_at"`
}

// TableName sets the table name for each model
func (TableEnvKey) TableName() string { return "config_env_keys" }
