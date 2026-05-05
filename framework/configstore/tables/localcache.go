package tables

import (
	"time"
)

// TableLocalCacheConfig holds the persisted configuration for the local cache
// plugin. Single-row table (mirrors TableClientConfig); updates use
// DELETE-then-INSERT in a transaction so callers always observe exactly one
// row. Fields are stored as typed columns rather than a JSON blob so future
// migrations can target individual columns.
type TableLocalCacheConfig struct {
	ID                           uint    `gorm:"primaryKey;autoIncrement" json:"id"`
	Provider                     string  `gorm:"type:varchar(64);default:''" json:"provider"`
	EmbeddingModel               string  `gorm:"type:varchar(255);default:''" json:"embedding_model"`
	CleanUpOnShutdown            bool    `gorm:"default:false" json:"cleanup_on_shutdown"`
	TTLSeconds                   int64   `gorm:"default:0" json:"ttl_seconds"`
	Threshold                    float64 `gorm:"default:0" json:"threshold"`
	VectorStoreNamespace         string  `gorm:"type:varchar(255);default:''" json:"vector_store_namespace"`
	Dimension                    int     `gorm:"default:0" json:"dimension"`
	DefaultCacheKey              string  `gorm:"type:varchar(255);default:''" json:"default_cache_key"`
	ConversationHistoryThreshold int     `gorm:"default:0" json:"conversation_history_threshold"`
	// Nullable so callers can distinguish "default" (nil) from an explicit
	// false. Plugin defaults: CacheByModel=true, CacheByProvider=true,
	// ExcludeSystemPrompt=false.
	CacheByModel        *bool `gorm:"" json:"cache_by_model"`
	CacheByProvider     *bool `gorm:"" json:"cache_by_provider"`
	ExcludeSystemPrompt *bool `gorm:"" json:"exclude_system_prompt"`

	// ConfigHash detects changes synced from config.json.
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

func (TableLocalCacheConfig) TableName() string { return "config_local_cache" }
