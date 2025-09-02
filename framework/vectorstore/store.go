// Package vectorstore provides a generic interface for vector stores.
package vectorstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

type VectorStoreType string

const (
	VectorStoreTypeWeaviate VectorStoreType = "weaviate"
	VectorStoreTypeRedis    VectorStoreType = "redis"
)

// Query represents a query to the vector store.
type Query struct {
	Field    string
	Operator QueryOperator
	Value    interface{}
}

type QueryOperator string

const (
	QueryOperatorEqual              QueryOperator = "Equal"
	QueryOperatorNotEqual           QueryOperator = "NotEqual"
	QueryOperatorGreaterThan        QueryOperator = "GreaterThan"
	QueryOperatorLessThan           QueryOperator = "LessThan"
	QueryOperatorGreaterThanOrEqual QueryOperator = "GreaterThanOrEqual"
	QueryOperatorLessThanOrEqual    QueryOperator = "LessThanOrEqual"
	QueryOperatorLike               QueryOperator = "Like"
	QueryOperatorContainsAny        QueryOperator = "ContainsAny"
	QueryOperatorContainsAll        QueryOperator = "ContainsAll"
	QueryOperatorIsNull             QueryOperator = "IsNull"
	QueryOperatorIsNotNull          QueryOperator = "IsNotNull"
)

// SearchResult represents a search result with metadata.
type SearchResult struct {
	ID         string
	Score      *float64
	Properties map[string]interface{}
}

// DeleteResult represents the result of a delete operation.
type DeleteResult struct {
	ID     string
	Status DeleteStatus
	Error  string
}

type DeleteStatus string

const (
	DeleteStatusSuccess DeleteStatus = "success"
	DeleteStatusError   DeleteStatus = "error"
)

type VectorStoreProperties struct {
	DataType    VectorStorePropertyType `json:"data_type"`
	Description string                  `json:"description"`
}

type VectorStorePropertyType string

const (
	VectorStorePropertyTypeString      VectorStorePropertyType = "string"
	VectorStorePropertyTypeInteger     VectorStorePropertyType = "integer"
	VectorStorePropertyTypeBoolean     VectorStorePropertyType = "boolean"
	VectorStorePropertyTypeStringArray VectorStorePropertyType = "string[]"
)

// VectorStore represents the interface for the vector store.
type VectorStore interface {
	CreateNamespace(ctx context.Context, namespace string, dimension int, properties map[string]VectorStoreProperties) error
	DeleteNamespace(ctx context.Context, namespace string) error
	GetChunk(ctx context.Context, namespace string, id string) (SearchResult, error)
	GetChunks(ctx context.Context, namespace string, ids []string) ([]SearchResult, error)
	GetAll(ctx context.Context, namespace string, queries []Query, selectFields []string, cursor *string, limit int64) ([]SearchResult, *string, error)
	GetNearest(ctx context.Context, namespace string, vector []float32, queries []Query, selectFields []string, threshold float64, limit int64) ([]SearchResult, error)
	Add(ctx context.Context, namespace string, id string, embedding []float32, metadata map[string]interface{}) error
	Delete(ctx context.Context, namespace string, id string) error
	DeleteAll(ctx context.Context, namespace string, queries []Query) ([]DeleteResult, error)
	Close(ctx context.Context, namespace string) error
}

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
	case VectorStoreTypeWeaviate:
		var weaviateConfig WeaviateConfig
		if err := json.Unmarshal(temp.Config, &weaviateConfig); err != nil {
			return fmt.Errorf("failed to unmarshal weaviate config: %w", err)
		}
		c.Config = weaviateConfig
	case VectorStoreTypeRedis:
		var redisConfig RedisConfig
		if err := json.Unmarshal(temp.Config, &redisConfig); err != nil {
			return fmt.Errorf("failed to unmarshal redis config: %w", err)
		}
		c.Config = redisConfig
	default:
		return fmt.Errorf("unknown vector store type: %s", temp.Type)
	}

	return nil
}

// NewVectorStore returns a new vector store based on the configuration.
func NewVectorStore(ctx context.Context, config *Config, logger schemas.Logger) (VectorStore, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	if !config.Enabled {
		return nil, fmt.Errorf("vector store is disabled")
	}

	switch config.Type {
	case VectorStoreTypeWeaviate:
		if config.Config == nil {
			return nil, fmt.Errorf("weaviate config is required")
		}
		weaviateConfig, ok := config.Config.(WeaviateConfig)
		if !ok {
			return nil, fmt.Errorf("invalid weaviate config")
		}
		return newWeaviateStore(ctx, &weaviateConfig, logger)
	case VectorStoreTypeRedis:
		if config.Config == nil {
			return nil, fmt.Errorf("redis config is required")
		}
		redisConfig, ok := config.Config.(RedisConfig)
		if !ok {
			return nil, fmt.Errorf("invalid redis config")
		}
		return newRedisStore(ctx, redisConfig, logger)
	}
	return nil, fmt.Errorf("invalid vector store type: %s", config.Type)
}
