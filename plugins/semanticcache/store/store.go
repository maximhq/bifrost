// Package store provides a generic interface for vector stores.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

type VectorStoreType string

const (
	VectorStoreTypeRedis        VectorStoreType = "redis"
	VectorStoreTypeRedisCluster VectorStoreType = "redis-cluster"
)

// Config represents the configuration for the vector store.
type Config struct {
	Type   VectorStoreType `json:"type"`
	Config any             `json:"config"`
}

// VectorStore represents the interface for the vector store.
type VectorStore interface {
	GetChunk(ctx context.Context, contextKey string) (string, error)
	GetChunks(ctx context.Context, chunkKeys []string) ([]any, error)
	Add(ctx context.Context, key string, value string, ttl time.Duration) error
	Delete(ctx context.Context, keys []string) error
	GetAll(ctx context.Context, pattern string, cursor *string, count int64) ([]string, *string, error)
	Close(ctx context.Context) error
}

func NewVectorStore(ctx context.Context, config Config, logger schemas.Logger) (VectorStore, error) {
	switch config.Type {
	case VectorStoreTypeRedis:
		redisConfig, ok := config.Config.(RedisConfig)
		if !ok {
			return nil, fmt.Errorf("invalid redis config")
		}
		return newRedisStore(ctx, redisConfig, logger)
	}

	return nil, fmt.Errorf("unsupported vector store type: %s", config.Type)
}
