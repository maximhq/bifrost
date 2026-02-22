package tables

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TablePlugin represents a plugin configuration in the database

type TablePlugin struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Name       string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"name"`
	Enabled    bool      `json:"enabled"`
	Path       *string   `json:"path,omitempty"`
	ConfigJSON string    `gorm:"type:text" json:"-"` // JSON serialized plugin.Config
	CreatedAt  time.Time `gorm:"index;not null" json:"created_at"`
	Version    int16     `gorm:"not null;default:1" json:"version"`
	UpdatedAt  time.Time `gorm:"index;not null" json:"updated_at"`
	IsCustom   bool      `gorm:"not null;default:false" json:"isCustom"`

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	EncryptionStatus string `gorm:"type:varchar(20);default:'plain_text'" json:"-"`

	// Virtual fields for runtime use (not stored in DB)
	Config any `gorm:"-" json:"config,omitempty"`
}

// TableName sets the table name for each model
func (TablePlugin) TableName() string { return "config_plugins" }

// BeforeSave is a GORM hook that serializes the plugin Config into a JSON column and
// encrypts it before writing to the database. Empty configs ("{}") are not encrypted.
func (p *TablePlugin) BeforeSave(tx *gorm.DB) error {
	if p.Config != nil {
		data, err := json.Marshal(p.Config)
		if err != nil {
			return err
		}
		p.ConfigJSON = string(data)
	} else {
		p.ConfigJSON = "{}"
	}

	// Encrypt config after serialization
	if encrypt.IsEnabled() && p.ConfigJSON != "" && p.ConfigJSON != "{}" {
		encrypted, err := encrypt.Encrypt(p.ConfigJSON)
		if err != nil {
			return fmt.Errorf("failed to encrypt plugin config: %w", err)
		}
		p.ConfigJSON = encrypted
		p.EncryptionStatus = EncryptionStatusEncrypted
	}

	return nil
}

// AfterFind is a GORM hook that decrypts the plugin config JSON (if encrypted) and
// deserializes it back into the runtime Config field after reading from the database.
func (p *TablePlugin) AfterFind(tx *gorm.DB) error {
	if p.EncryptionStatus == "encrypted" && p.ConfigJSON != "" {
		decrypted, err := encrypt.Decrypt(p.ConfigJSON)
		if err != nil {
			return fmt.Errorf("failed to decrypt plugin config: %w", err)
		}
		p.ConfigJSON = decrypted
	}
	if p.ConfigJSON != "" {
		if err := json.Unmarshal([]byte(p.ConfigJSON), &p.Config); err != nil {
			return err
		}
	} else {
		p.Config = nil
	}

	return nil
}
