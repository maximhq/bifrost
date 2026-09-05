package tables

import "time"

// WarpConfigRowID is the primary key of the one and only Warp config row.
// Warp is deployment-wide, so the table is a singleton: writes upsert this ID
// rather than inserting, which keeps "which row is current?" from ever being a
// question the read path has to answer.
const WarpConfigRowID uint = 1

// TableWarpConfig stores the dashboard agent's model settings.
//
// It is a dedicated table rather than rows in the generic key/value config table
// so the settings have real columns and real types, which matters for a block
// this size. It holds no secret - the key is stored as a reference to one of the
// provider's existing keys - so it needs no encryption hooks.
type TableWarpConfig struct {
	ID      uint `gorm:"primaryKey" json:"id"`
	Enabled bool `gorm:"default:false" json:"enabled"`

	Provider string `gorm:"type:varchar(64)" json:"provider"`
	Model    string `gorm:"type:varchar(255)" json:"model"`
	BaseURL  string `gorm:"type:varchar(2048)" json:"base_url,omitempty"`

	// APIKeyID names one of the provider's configured keys. It is a reference,
	// not a credential, which is why this table needs no encryption hooks: there
	// is nothing here worth encrypting.
	APIKeyID string `gorm:"type:varchar(255)" json:"api_key_id,omitempty"`

	MaxIterations         int `gorm:"default:0" json:"max_iterations,omitempty"`
	RequestTimeoutSeconds int `gorm:"default:0" json:"request_timeout_seconds,omitempty"`

	SystemPromptSuffix *string `gorm:"type:text" json:"system_prompt_suffix,omitempty"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
}

// TableName sets the table name for the Warp config model.
func (TableWarpConfig) TableName() string { return "warp_config" }
