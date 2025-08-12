package vectorstore

import "fmt"

const (
	VectorStoreTypeRedis string = "redis"
)

// VectorStore is the interface for the vector store.
type VectorStore interface {
	Get(key string) (string, error)
	Set(key string, value string) error
	Delete(key string) error
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
