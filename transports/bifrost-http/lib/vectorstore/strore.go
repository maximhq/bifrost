package vectorstore

import (
	"context"
	"fmt"
	"time"
)

const (
	VectorStoreTypeRedis string = "redis"
)

// VectorStore is the interface for the vector store.
// VectorStore represents the interface for the vector store.
type VectorStore interface {
	GetChunk(ctx context.Context, contextKey string) (string, error)
	GetChunks(ctx context.Context, chunkKeys []string) ([]any, error)
	Add(ctx context.Context, key string, value string, ttl time.Duration) error
	Delete(ctx context.Context, keys []string) error
	GetAll(ctx context.Context, pattern string, cursor *string, count int64) ([]string, *string, error)
	Close(ctx context.Context) error
}

// NewVectorStore creates a new vector store based on the configuration
func NewVectorStore(config *Config) (VectorStore, error) {
	switch config.Type {
	case VectorStoreTypeRedis:
		if redisConfig, ok := config.Config.(RedisVectorStoreConfig); ok {
			return newRedisVectorStore(&redisConfig)
		}
		return nil, fmt.Errorf("invalid redis config: %T", config.Config)
	}
	return nil, fmt.Errorf("unsupported vector store type: %s", config.Type)
}
