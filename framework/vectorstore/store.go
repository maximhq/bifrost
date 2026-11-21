// Package vectorstore provides a generic interface for vector stores.
package vectorstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

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
	// GetNearest retrieves the nearest vectors from the vector store. threshold and each result's
	// Score are cosine similarity on every store.
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

// Counter is a store that can say how many vectors in a namespace match a
// filter. Every built-in backend is one; it is a separate interface so a store
// that is not (a test fake, an out-of-tree backend) still satisfies VectorStore.
type Counter interface {
	// Count returns how many vectors in the namespace match every query (all
	// of them, with none); 0 for a namespace that does not exist.
	Count(ctx context.Context, namespace string, queries []Query) (int64, error)
}

// ErrCountUnsupported is Count's answer for a store that is not a Counter.
var ErrCountUnsupported = errors.New("vector store cannot count vectors")

// Count returns how many vectors in a namespace match the queries, or
// ErrCountUnsupported.
func Count(ctx context.Context, store VectorStore, namespace string, queries []Query) (int64, error) {
	counter, ok := store.(Counter)
	if !ok {
		return 0, ErrCountUnsupported
	}
	return counter.Count(ctx, namespace, queries)
}

// countByPaging counts the vectors matching queries by reading them, for a
// backend with no filtered count of its own. It asks for the vectors: that is
// the read every backend pages to the end (Pinecone's plain GetAll is one
// capped query with no continuation), and it carries the filter across pages.
func countByPaging(ctx context.Context, store VectorStore, namespace string, queries []Query) (int64, error) {
	ctx = WithIncludeVectors(ctx)
	var count int64
	var cursor *string
	for {
		results, next, err := store.GetAll(ctx, namespace, queries, nil, cursor, 1000)
		if errors.Is(err, ErrNotFound) && cursor == nil {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		count += int64(len(results))
		if next == nil || *next == "" || len(results) == 0 {
			return count, nil
		}
		cursor = next
	}
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

// Location names where a store keeps its data: its type and endpoint, never its
// credentials. Two configurations with the same Location reach the same data, so
// a caller can tell a moved store from a rotated key. Each part is the one the
// client connects with, defaults filled in: an omitted Qdrant port is 6334 and
// an omitted Redis database is 0, as the clients use them, and the Weaviate
// scheme is kept, since http and https on one host are different endpoints. An
// unknown type, or a config that does not match its type, has no location.
func (c *Config) Location() string {
	if c == nil {
		return ""
	}
	value := func(v *schemas.SecretVar) string {
		if v == nil {
			return ""
		}
		return strings.TrimSpace(v.GetValue())
	}
	switch cfg := c.Config.(type) {
	case WeaviateConfig:
		return "weaviate+" + strings.ToLower(strings.TrimSpace(cfg.Scheme)) + "://" + value(cfg.Host)
	case QdrantConfig:
		return "qdrant://" + value(&cfg.Host) + ":" + strconv.Itoa(cfg.Port.CoerceInt(qdrantDefaultPort))
	case RedisConfig:
		db := value(cfg.DB)
		if db == "" {
			db = "0"
		}
		return "redis://" + value(cfg.Addr) + "/" + db
	case PineconeConfig:
		return "pinecone://" + value(&cfg.IndexHost)
	case ChromemConfig:
		// One directory written two ways ("/data/vectors/", "/data/./vectors")
		// is one store. No path is memory-only and stays as it is.
		path := strings.TrimSpace(cfg.Path)
		if path != "" {
			path = filepath.Clean(path)
		}
		return "chromem://" + path
	case nil:
		if c.Type == VectorStoreTypeChromem {
			return "chromem://"
		}
	}
	return ""
}

// InMemory reports whether the store keeps nothing across a restart: Chromem
// with no path. Each start of one is a new, empty store, whatever its Location.
func (c *Config) InMemory() bool {
	if c == nil || c.Type != VectorStoreTypeChromem {
		return false
	}
	cfg, _ := c.Config.(ChromemConfig)
	return strings.TrimSpace(cfg.Path) == ""
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
