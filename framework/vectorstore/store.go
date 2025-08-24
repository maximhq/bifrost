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
)

// SearchResult represents a search result with metadata.
type Query struct {
	Field    string
	Operator string
	Value    interface{}
}

type SearchResult struct {
	ID         string
	Score      float64
	Properties map[string]interface{}
}

// VectorStore represents the interface for the vector store.
type VectorStore interface {
	GetChunk(ctx context.Context, contextKey string) (any, error)
	GetChunks(ctx context.Context, chunkKeys []string) ([]any, error)
	GetAll(ctx context.Context, queries []Query, cursor *string, count int64) ([]any, *string, error)
	GetNearest(ctx context.Context, vector []float32, queries []Query, threshold float64, limit int64) ([]SearchResult, error)
	Add(ctx context.Context, key string, embedding []float32, metadata map[string]interface{}) error
	Delete(ctx context.Context, keys []string) error
	EnsureSchema(ctx context.Context) error
	Close(ctx context.Context) error
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
	default:
		return fmt.Errorf("unknown vector store type: %s", temp.Type)
	}

	return nil
}

// NewVectorStore returns a new vector store based on the configuration.
func NewVectorStore(ctx context.Context, config *Config, logger schemas.Logger) (VectorStore, error) {
	switch config.Type {
	case VectorStoreTypeWeaviate:
		if config.Config == nil {
			return nil, fmt.Errorf("weaviate config is required")
		}
		weaviateConfig, ok := config.Config.(WeaviateConfig)
		if !ok {
			return nil, fmt.Errorf("invalid weaviate config")
		}
		return newWeaviateStore(ctx, weaviateConfig, logger)
	}
	return nil, fmt.Errorf("invalid vector store type: %s", config.Type)
}
