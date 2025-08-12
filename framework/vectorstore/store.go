// Package vectorstore provides a generic interface for vector stores.
package vectorstore

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// VectorStore represents the interface for the vector store.
type VectorStore interface {
	GetChunk(ctx context.Context, contextKey string) (string, error)
	GetChunks(ctx context.Context, chunkKeys []string) ([]any, error)
	Add(ctx context.Context, key string, value string, ttl time.Duration) error
	Delete(ctx context.Context, keys []string) error
	GetAll(ctx context.Context, pattern string, cursor *string, count int64) ([]string, *string, error)
	Close(ctx context.Context) error
}

type Config struct {
	Type   string `json:"type"`
	Config any    `json:"config"`
}

// GetVectorStore returns a new vector store based on the configuration.
func GetVectorStore(ctx context.Context, config Config, logger schemas.Logger) (VectorStore, error) {
	switch config.Type {
	case "redis":
		if config.Config == nil {
			return nil, fmt.Errorf("redis config is required")
		}
		redisConfig, ok := config.Config.(RedisConfig)
		if !ok {
			return nil, fmt.Errorf("invalid redis config")
		}
		return newRedisStore(ctx, redisConfig, logger)
	case "redis_cluster":
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
