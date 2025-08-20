// Package vectorstore provides a generic interface for vector stores.
package vectorstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

type VectorStoreType string

const (
	VectorStoreTypeRedis        VectorStoreType = "redis"
	VectorStoreTypeRedisCluster VectorStoreType = "redis_cluster"
)

// SearchResult represents a search result with metadata.
type SearchResult struct {
	Key        string                 `json:"key"`
	Value      string                 `json:"value"`
	Similarity float64                `json:"similarity"`
	Metadata   map[string]interface{} `json:"metadata"`
}

// VectorStore represents the interface for the vector store.
type VectorStore interface {
	// Basic operations (for response caching)
	GetChunk(ctx context.Context, contextKey string) (string, error)
	GetChunks(ctx context.Context, chunkKeys []string) ([]any, error)
	Add(ctx context.Context, key string, value string, ttl time.Duration) error
	Delete(ctx context.Context, keys []string) error
	GetAll(ctx context.Context, pattern string, cursor *string, count int64) ([]string, *string, error)
	Close(ctx context.Context) error

	// Unified semantic cache operations (with native vector search)
	AddSemanticCache(ctx context.Context, key string, embedding []float32, metadata map[string]interface{}, ttl time.Duration) error
	SearchSemanticCache(ctx context.Context, indexName string, queryEmbedding []float32, metadata map[string]interface{}, threshold float64, limit int64) ([]SearchResult, error)

	// Index management
	EnsureSemanticIndex(ctx context.Context, indexName string, keyPrefix string, embeddingDim int, metadataFields []string) error
	DropSemanticIndex(ctx context.Context, indexName string) error
}

// IMPORTANT: VectorStore user should call EnsureSemanticIndex before using the vector store.
// This is because the vector store does not create the index automatically.

// Config represents the configuration for the vector store.
type Config struct {
	Enabled bool            `json:"enabled"`
	Type    VectorStoreType `json:"type"`
	Config  any             `json:"config"`
}

// UnmarshalJSON unmarshals the config from JSON.
func (c *Config) UnmarshalJSON(data []byte) error {
	// First, unmarshal into a temporary struct to get the basic fields
	type TempConfig struct {
		Enabled bool            `json:"enabled"`
		Type    string          `json:"type"`
		Config  json.RawMessage `json:"config"` // Keep as raw JSON
	}

	var temp TempConfig
	if err := json.Unmarshal(data, &temp); err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Set basic fields
	c.Enabled = temp.Enabled
	c.Type = VectorStoreType(temp.Type)

	// Parse the config field based on type
	switch c.Type {
	case VectorStoreTypeRedis:
		var redisConfig RedisConfig
		if err := json.Unmarshal(temp.Config, &redisConfig); err != nil {
			return fmt.Errorf("failed to unmarshal redis config: %w", err)
		}
		c.Config = redisConfig

	case VectorStoreTypeRedisCluster:
		var redisClusterConfig RedisClusterConfig
		if err := json.Unmarshal(temp.Config, &redisClusterConfig); err != nil {
			return fmt.Errorf("failed to unmarshal redis cluster config: %w", err)
		}
		c.Config = redisClusterConfig
	default:
		return fmt.Errorf("unknown vector store type: %s", temp.Type)
	}

	return nil
}

// NewVectorStore returns a new vector store based on the configuration.
func NewVectorStore(ctx context.Context, config *Config, logger schemas.Logger) (VectorStore, error) {
	switch config.Type {
	case VectorStoreTypeRedis:
		if config.Config == nil {
			return nil, fmt.Errorf("redis config is required")
		}
		redisConfig, ok := config.Config.(RedisConfig)
		if !ok {
			return nil, fmt.Errorf("invalid redis config")
		}
		return newRedisStore(ctx, redisConfig, logger)
	case VectorStoreTypeRedisCluster:
		if config.Config == nil {
			return nil, fmt.Errorf("redis cluster config is required")
		}
		redisClusterConfig, ok := config.Config.(RedisClusterConfig)
		if !ok {
			return nil, fmt.Errorf("invalid redis cluster config")
		}
		return newRedisClusterStore(ctx, redisClusterConfig, logger)
	}
	return nil, fmt.Errorf("invalid vector store type: %s", config.Type)
}
