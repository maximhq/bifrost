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
	VectorStoreTypeQdrant   VectorStoreType = "qdrant"
	VectorStoreTypePinecone VectorStoreType = "pinecone"
	VectorStoreTypeChromem  VectorStoreType = "chromem"
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
	// Vector is the stored embedding. Populated only when the read was made
	// with WithIncludeVectors; vectors are large and most callers want the
	// properties alone.
	Vector []float32
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
	// Filterable marks a property used in Query filters; stores may encode it.
	Filterable bool `json:"filterable,omitempty"`
}

type VectorStorePropertyType string

const (
	VectorStorePropertyTypeString      VectorStorePropertyType = "string"
	VectorStorePropertyTypeInteger     VectorStorePropertyType = "integer"
	VectorStorePropertyTypeBoolean     VectorStorePropertyType = "boolean"
	VectorStorePropertyTypeStringArray VectorStorePropertyType = "string[]"
)

type disableScanFallbackContextKey struct{}

// VectorStore represents the interface for the vector store.
type VectorStore interface {
	// Health check
	Ping(ctx context.Context) error
	// CreateNamespace creates a new namespace in the vector store.
	CreateNamespace(ctx context.Context, namespace string, dimension int, properties map[string]VectorStoreProperties) error
	// DeleteNamespace deletes a namespace from the vector store.
	DeleteNamespace(ctx context.Context, namespace string) error
	// ListNamespaces returns the names of existing namespaces beginning with
	// prefix, sorted. An empty prefix lists everything the backend exposes.
	//
	// Callers that create namespaces under a naming scheme of their own need
	// this to find the ones they left behind: a name derived from content
	// cannot be reconstructed once the content has changed, so without
	// enumeration an abandoned namespace becomes unreachable rather than
	// merely unused. Filtering happens in the backend where the API supports
	// it and here where it does not, so the result is the same either way.
	ListNamespaces(ctx context.Context, prefix string) ([]string, error)
	// GetChunk retrieves a single vector from the vector store.
	GetChunk(ctx context.Context, namespace string, id string) (SearchResult, error)
	// GetChunks retrieves multiple vectors from the vector store.
	GetChunks(ctx context.Context, namespace string, ids []string) ([]SearchResult, error)
	// GetAll retrieves all vectors from the vector store.
	GetAll(ctx context.Context, namespace string, queries []Query, selectFields []string, cursor *string, limit int64) ([]SearchResult, *string, error)
	// GetNearest retrieves the nearest vectors from the vector store.
	GetNearest(ctx context.Context, namespace string, vector []float32, queries []Query, selectFields []string, threshold float64, limit int64) ([]SearchResult, error)
	// RequiresVectors returns true if the vector store requires vectors for all entries.
	// Dedicated vector databases like Qdrant and Pinecone require vectors, while
	// more flexible stores like Weaviate and Redis can store metadata-only entries.
	RequiresVectors() bool
	// Add stores a new vector in the vector store.
	Add(ctx context.Context, namespace string, id string, embedding []float32, metadata map[string]interface{}) error
	// Delete removes a vector from the vector store.
	Delete(ctx context.Context, namespace string, id string) error
	// DeleteAll deletes all vectors from the vector store.
	DeleteAll(ctx context.Context, namespace string, queries []Query) ([]DeleteResult, error)
	// Close closes the vector store.
	Close(ctx context.Context, namespace string) error
}

// WithDisableScanFallback returns a derived context that tells vector stores not
// to fall back to full scans when indexed search fails.
func WithDisableScanFallback(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, disableScanFallbackContextKey{}, true)
}

type includeVectorsContextKey struct{}

// WithIncludeVectors asks paging reads (GetAll) to return each entry's stored
// vector alongside its properties. Off by default: a vector is thousands of
// floats, and a listing that only wants ids should not carry them.
func WithIncludeVectors(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, includeVectorsContextKey{}, true)
}

// ServerFilteredReader is implemented by a store that can say whether it
// applies a filter on the server while paging a read that carries vectors.
//
// It decides how a caller should take a large namespace out. A store that
// filters on the server is read a slice at a time, each costing what the
// slice holds. One that does not pays for the whole namespace on every
// filtered read - it walks everything and drops what does not match - so it
// is read once, unfiltered, and the caller does the dropping.
type ServerFilteredReader interface {
	FiltersVectorReadsOnServer() bool
}

// FiltersVectorReadsOnServer reports whether store filters a paged read with
// vectors on the server. A store that does not say is taken not to.
func FiltersVectorReadsOnServer(store VectorStore) bool {
	reader, ok := store.(ServerFilteredReader)
	return ok && reader.FiltersVectorReadsOnServer()
}

// IncludeVectorsRequested reports whether the current read asked for vectors.
func IncludeVectorsRequested(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	include, _ := ctx.Value(includeVectorsContextKey{}).(bool)
	return include
}

// VectorFromAdditional converts the vector a backend returns in its
// "_additional" block (a []interface{} of float64, as GraphQL decodes numbers)
// into []float32. Anything that is not a numeric list yields nil.
func VectorFromAdditional(raw interface{}) []float32 {
	values, ok := raw.([]interface{})
	if !ok || len(values) == 0 {
		return nil
	}
	vector := make([]float32, 0, len(values))
	for _, value := range values {
		switch number := value.(type) {
		case float64:
			vector = append(vector, float32(number))
		case float32:
			vector = append(vector, number)
		case int:
			vector = append(vector, float32(number))
		default:
			return nil
		}
	}
	return vector
}

// IsScanFallbackDisabled reports whether scan fallback has been disabled for
// the current vector store operation.
func IsScanFallbackDisabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	disabled, _ := ctx.Value(disableScanFallbackContextKey{}).(bool)
	return disabled
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
		// Process env. values for sensitive fields
		c.Config = redisConfig
	case VectorStoreTypeQdrant:
		var qdrantConfig QdrantConfig
		if err := json.Unmarshal(temp.Config, &qdrantConfig); err != nil {
			return fmt.Errorf("failed to unmarshal qdrant config: %w", err)
		}
		c.Config = qdrantConfig
	case VectorStoreTypePinecone:
		var pineconeConfig PineconeConfig
		if err := json.Unmarshal(temp.Config, &pineconeConfig); err != nil {
			return fmt.Errorf("failed to unmarshal pinecone config: %w", err)
		}
		c.Config = pineconeConfig
	case VectorStoreTypeChromem:
		var chromemConfig ChromemConfig
		// Chromem has no required fields, so a missing config block is valid.
		if len(temp.Config) > 0 {
			if err := json.Unmarshal(temp.Config, &chromemConfig); err != nil {
				return fmt.Errorf("failed to unmarshal chromem config: %w", err)
			}
		}
		c.Config = chromemConfig
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
	case VectorStoreTypeQdrant:
		if config.Config == nil {
			return nil, fmt.Errorf("qdrant config is required")
		}
		qdrantConfig, ok := config.Config.(QdrantConfig)
		if !ok {
			return nil, fmt.Errorf("invalid qdrant config")
		}
		return newQdrantStore(ctx, &qdrantConfig, logger)
	case VectorStoreTypePinecone:
		if config.Config == nil {
			return nil, fmt.Errorf("pinecone config is required")
		}
		pineconeConfig, ok := config.Config.(PineconeConfig)
		if !ok {
			return nil, fmt.Errorf("invalid pinecone config")
		}
		return newPineconeStore(ctx, &pineconeConfig, logger)
	case VectorStoreTypeChromem:
		// A nil config block is valid: chromem defaults to memory-only mode.
		chromemConfig, ok := config.Config.(ChromemConfig)
		if !ok && config.Config != nil {
			return nil, fmt.Errorf("invalid chromem config")
		}
		return newChromemStore(ctx, &chromemConfig, logger)
	}
	return nil, fmt.Errorf("invalid vector store type: %s", config.Type)
}
