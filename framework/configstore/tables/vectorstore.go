package tables

import (
	"fmt"
	"time"

	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TableVectorStoreConfig represents Cache plugin configuration in the database
type TableVectorStoreConfig struct {
	ID              uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Enabled         bool      `json:"enabled"`                               // Enable vector store
	Type            string    `gorm:"type:varchar(50);not null" json:"type"` // "weaviate, redis, qdrant."
	TTLSeconds      int       `gorm:"default:300" json:"ttl_seconds"`        // TTL in seconds (default: 5 minutes)
	CacheByModel    bool      `gorm:"" json:"cache_by_model"`                // Include model in cache key
	CacheByProvider bool      `gorm:"" json:"cache_by_provider"`             // Include provider in cache key
	Config           *string   `gorm:"type:text" json:"config"` // JSON serialized schemas.RedisVectorStoreConfig
	EncryptionStatus string    `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	CreatedAt        time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for each model
func (TableVectorStoreConfig) TableName() string { return "config_vector_store" }

// BeforeSave hook to encrypt sensitive config
func (vs *TableVectorStoreConfig) BeforeSave(tx *gorm.DB) error {
	if encrypt.IsEnabled() && vs.Config != nil && *vs.Config != "" {
		if err := encryptString(vs.Config); err != nil {
			return fmt.Errorf("failed to encrypt vector store config: %w", err)
		}
		vs.EncryptionStatus = EncryptionStatusEncrypted
	}
	return nil
}

// AfterFind hook to decrypt sensitive config
func (vs *TableVectorStoreConfig) AfterFind(tx *gorm.DB) error {
	if vs.EncryptionStatus == EncryptionStatusEncrypted && vs.Config != nil && *vs.Config != "" {
		if err := decryptString(vs.Config); err != nil {
			return fmt.Errorf("failed to decrypt vector store config: %w", err)
		}
	}
	return nil
}
