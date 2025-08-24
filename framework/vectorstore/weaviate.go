package vectorstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/weaviate/weaviate-go-client/v5/weaviate"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/auth"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/filters"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/graphql"
	"github.com/weaviate/weaviate/entities/models"
)

// Default values for Weaviate vector index configuration
const (
	// Default class names (Weaviate prefers PascalCase)
	DefaultClassName = "BifrostStore"
)

// WeaviateConfig represents the configuration for the Weaviate vector store.
type WeaviateConfig struct {
	// Connection settings
	Scheme string `json:"scheme"` // "http" or "https" - REQUIRED
	Host   string `json:"host"`   // "localhost:8080" - REQUIRED

	// Authentication settings (optional)
	ApiKey  string            `json:"api_key,omitempty"` // API key for authentication
	Headers map[string]string `json:"headers,omitempty"` // Additional headers

	// Connection settings
	Timeout time.Duration `json:"timeout,omitempty"` // Request timeout (optional)

	// Class name
	ClassName string `json:"class_name,omitempty"`
}

// WeaviateStore represents the Weaviate vector store.
type WeaviateStore struct {
	client    *weaviate.Client
	config    WeaviateConfig
	logger    schemas.Logger
	className string
}

// Add stores a new object (with or without embedding)
func (s *WeaviateStore) Add(ctx context.Context, key string, embedding []float32, metadata map[string]interface{}) error {
	// Store metadata fields at top level for easier querying
	properties := make(map[string]interface{})

	// Add all metadata fields as top-level properties
	for k, v := range metadata {
		if k == "params" && v != nil {
			// Only store individual param fields as top-level properties for querying
			// Don't store the JSON params field to avoid duplication
			if paramsMap, ok := v.(map[string]interface{}); ok {
				for paramKey, paramValue := range paramsMap {
					// Use underscores since dots aren't allowed in Weaviate property names
					properties["params_"+paramKey] = paramValue
				}
			}
		} else {
			properties[k] = v
		}
	}

	obj := &models.Object{
		Class:      s.className,
		Properties: properties,
	}

	var err error
	if embedding != nil {
		_, err = s.client.Data().Creator().
			WithClassName(s.className).
			WithID(key).
			WithProperties(obj.Properties).
			WithVector(embedding).
			Do(ctx)
	} else {
		_, err = s.client.Data().Creator().
			WithClassName(s.className).
			WithID(key).
			WithProperties(obj.Properties).
			Do(ctx)
	}

	return err
}

// GetChunk returns the "metadata" for a single key
func (s *WeaviateStore) GetChunk(ctx context.Context, contextKey string) (any, error) {
	obj, err := s.client.Data().ObjectsGetter().
		WithClassName(s.className).
		WithID(contextKey).
		Do(ctx)
	if err != nil {
		return "", err
	}
	if len(obj) == 0 {
		return "", fmt.Errorf("not found: %s", contextKey)
	}

	props, ok := obj[0].Properties.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid properties")
	}

	metadata := props["metadata"]
	if metadata == nil {
		return "", nil
	}

	return metadata, nil
}

// GetChunks returns multiple objects by ID
func (s *WeaviateStore) GetChunks(ctx context.Context, chunkKeys []string) ([]any, error) {
	out := make([]any, 0, len(chunkKeys))
	for _, key := range chunkKeys {
		obj, err := s.client.Data().ObjectsGetter().
			WithClassName(s.className).
			WithID(key).
			Do(ctx)
		if err != nil {
			return nil, err
		}
		if len(obj) > 0 {
			out = append(out, obj[0].Properties)
		}
	}
	return out, nil
}

// GetAll with filtering + pagination
func (s *WeaviateStore) GetAll(ctx context.Context, queries []Query, cursor *string, count int64) ([]any, *string, error) {
	where := buildWeaviateFilter(queries)

	search := s.client.GraphQL().Get().
		WithClassName(s.className).
		WithLimit(int(count)).
		WithFields(
			graphql.Field{Name: "_additional", Fields: []graphql.Field{
				{Name: "id"},
			}},
			graphql.Field{Name: "provider"},
			graphql.Field{Name: "model"},
			graphql.Field{Name: "request_hash"},
			graphql.Field{Name: "cache_key"},
			graphql.Field{Name: "response"},
			graphql.Field{Name: "stream_responses"},
			graphql.Field{Name: "expires_at"},
		)

	if where != nil {
		search = search.WithWhere(where)
	}
	if cursor != nil {
		search = search.WithAfter(*cursor)
	}

	resp, err := search.Do(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Check for GraphQL errors
	if len(resp.Errors) > 0 {
		var errorMsgs []string
		for _, err := range resp.Errors {
			errorMsgs = append(errorMsgs, err.Message)
		}
		return nil, nil, fmt.Errorf("graphql errors: %v", errorMsgs)
	}

	data, ok := resp.Data["Get"].(map[string]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("invalid graphql response: missing 'Get' key, got: %+v", resp.Data)
	}

	objsRaw, exists := data[s.className]
	if !exists {
		// No results for this class - this is normal, not an error
		s.logger.Debug(fmt.Sprintf("No results found for class '%s', available classes: %+v", s.className, data))
		return nil, nil, nil
	}

	objs, ok := objsRaw.([]interface{})
	if !ok {
		s.logger.Debug(fmt.Sprintf("Class '%s' exists but data is not an array: %+v", s.className, objsRaw))
		return nil, nil, nil
	}

	results := make([]any, 0, len(objs))
	var nextCursor *string
	for _, o := range objs {
		obj, ok := o.(map[string]interface{})
		if !ok {
			continue
		}

		// Convert to SearchResult format for consistency
		searchResult := SearchResult{
			Properties: obj,
		}

		if additional, ok := obj["_additional"].(map[string]interface{}); ok {
			if id, ok := additional["id"].(string); ok {
				searchResult.ID = id
				nextCursor = &id
			}
		}

		results = append(results, searchResult)
	}

	return results, nextCursor, nil
}

// GetNearest with explicit filters only
func (s *WeaviateStore) GetNearest(
	ctx context.Context,
	vector []float32,
	queries []Query,
	threshold float64,
	limit int64,
) ([]SearchResult, error) {
	where := buildWeaviateFilter(queries)

	nearVector := s.client.GraphQL().NearVectorArgBuilder().
		WithVector(vector).
		WithDistance(float32(threshold))

	search := s.client.GraphQL().Get().
		WithClassName(s.className).
		WithNearVector(nearVector).
		WithLimit(int(limit)).
		WithFields(
			graphql.Field{Name: "_additional", Fields: []graphql.Field{
				{Name: "id"},
				{Name: "distance"},
			}},
			graphql.Field{Name: "provider"},
			graphql.Field{Name: "model"},
			graphql.Field{Name: "request_hash"},
			graphql.Field{Name: "cache_key"},
			graphql.Field{Name: "response"},
			graphql.Field{Name: "stream_responses"},
			graphql.Field{Name: "expires_at"},
		)

	if where != nil {
		search = search.WithWhere(where)
	}

	resp, err := search.Do(ctx)
	if err != nil {
		return nil, err
	}

	// Check for GraphQL errors
	if len(resp.Errors) > 0 {
		var errorMsgs []string
		for _, err := range resp.Errors {
			errorMsgs = append(errorMsgs, err.Message)
		}
		return nil, fmt.Errorf("graphql errors: %v", errorMsgs)
	}

	data, ok := resp.Data["Get"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid graphql response: missing 'Get' key, got: %+v", resp.Data)
	}

	objsRaw, exists := data[s.className]
	if !exists {
		// No results for this class - this is normal, not an error
		s.logger.Debug(fmt.Sprintf("No results found for class '%s', available classes: %+v", s.className, data))
		return nil, nil
	}

	objs, ok := objsRaw.([]interface{})
	if !ok {
		s.logger.Debug(fmt.Sprintf("Class '%s' exists but data is not an array: %+v", s.className, objsRaw))
		return nil, nil
	}

	results := make([]SearchResult, 0, len(objs))
	for _, o := range objs {
		obj, ok := o.(map[string]interface{})
		if !ok {
			continue
		}
		additional := obj["_additional"].(map[string]interface{})
		results = append(results, SearchResult{
			ID:         additional["id"].(string),
			Score:      additional["distance"].(float64),
			Properties: obj,
		})
	}

	return results, nil
}

// Delete removes multiple objects by ID
func (s *WeaviateStore) Delete(ctx context.Context, keys []string) error {
	for _, key := range keys {
		err := s.client.Data().Deleter().
			WithClassName(s.className).
			WithID(key).
			Do(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *WeaviateStore) Close(ctx context.Context) error {
	// nothing to close
	return nil
}

// newWeaviateStore creates a new Weaviate vector store.
func newWeaviateStore(ctx context.Context, config WeaviateConfig, logger schemas.Logger) (*WeaviateStore, error) {
	// Validate required config
	if config.Scheme == "" || config.Host == "" {
		return nil, fmt.Errorf("weaviate scheme and host are required")
	}

	// Build client configuration
	cfg := weaviate.Config{
		Scheme: config.Scheme,
		Host:   config.Host,
	}

	// Add authentication if provided
	if config.ApiKey != "" {
		cfg.AuthConfig = auth.ApiKey{Value: config.ApiKey}
	}

	if config.ClassName == "" {
		config.ClassName = DefaultClassName
	}

	// Add custom headers if provided
	if len(config.Headers) > 0 {
		cfg.Headers = config.Headers
	}

	// Create client
	client, err := weaviate.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create weaviate client: %w", err)
	}

	// Test connection with meta endpoint
	testCtx := ctx
	if config.Timeout > 0 {
		var cancel context.CancelFunc
		testCtx, cancel = context.WithTimeout(ctx, config.Timeout)
		defer cancel()
	}

	_, err = client.Misc().MetaGetter().Do(testCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to weaviate: %w", err)
	}

	store := &WeaviateStore{
		client:    client,
		config:    config,
		logger:    logger,
		className: config.ClassName,
	}

	// Ensure schema exists with all required fields
	err = store.EnsureSchema(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure schema: %w", err)
	}

	return store, nil
}

// EnsureSchema creates the class schema with all required fields if it doesn't exist
func (s *WeaviateStore) EnsureSchema(ctx context.Context) error {
	// Check if class exists
	exists, err := s.client.Schema().ClassExistenceChecker().
		WithClassName(s.className).
		Do(ctx)
	if err != nil {
		return fmt.Errorf("failed to check class existence: %w", err)
	}

	if exists {
		return nil // Schema already exists
	}

	// Create class schema with all fields we need
	classSchema := &models.Class{
		Class: s.className,
		Properties: []*models.Property{
			{
				Name:     "provider",
				DataType: []string{"text"},
			},
			{
				Name:     "model",
				DataType: []string{"text"},
			},
			{
				Name:     "request_hash",
				DataType: []string{"text"},
			},
			{
				Name:     "cache_key",
				DataType: []string{"text"},
			},
			{
				Name:     "response",
				DataType: []string{"text"},
			},
			{
				Name:     "stream_responses",
				DataType: []string{"text[]"},
			},
			{
				Name:     "expires_at",
				DataType: []string{"number"},
			},
		},
		VectorIndexType: "hnsw",
		Vectorizer:      "none", // We provide our own vectors
	}

	err = s.client.Schema().ClassCreator().
		WithClass(classSchema).
		Do(ctx)
	if err != nil {
		return fmt.Errorf("failed to create class schema: %w", err)
	}

	return nil
}

// buildWeaviateFilter converts []Query → Weaviate WhereFilter
func buildWeaviateFilter(queries []Query) *filters.WhereBuilder {
	if len(queries) == 0 {
		return nil
	}

	var operands []*filters.WhereBuilder
	for _, q := range queries {
		// Convert string operator to filters operator
		operator := convertOperator(q.Operator)

		// Handle nested params fields: "params.user" → "params_user"
		var fieldPath []string
		if strings.HasPrefix(q.Field, "params.") {
			// Convert params.user to params_user for Weaviate compatibility
			fieldName := strings.Replace(q.Field, "params.", "params_", 1)
			fieldPath = []string{fieldName}
		} else {
			// For other fields, split normally
			fieldPath = strings.Split(q.Field, ".")
		}

		whereClause := filters.Where().
			WithPath(fieldPath).
			WithOperator(operator)

		// Set value based on type
		switch v := q.Value.(type) {
		case string:
			whereClause = whereClause.WithValueString(v)
		case int:
			whereClause = whereClause.WithValueNumber(float64(v))
		case int64:
			whereClause = whereClause.WithValueNumber(float64(v))
		case float32:
			whereClause = whereClause.WithValueNumber(float64(v))
		case float64:
			whereClause = whereClause.WithValueNumber(v)
		case bool:
			whereClause = whereClause.WithValueBoolean(v)
		default:
			// Fallback to string conversion
			whereClause = whereClause.WithValueString(fmt.Sprintf("%v", v))
		}

		operands = append(operands, whereClause)
	}

	if len(operands) == 1 {
		return operands[0]
	}

	// Create AND filter for multiple operands
	return filters.Where().
		WithOperator(filters.And).
		WithOperands(operands)
}

// convertOperator converts string operator to filters operator
func convertOperator(op string) filters.WhereOperator {
	switch op {
	case "Equal":
		return filters.Equal
	case "NotEqual":
		return filters.NotEqual
	case "LessThan":
		return filters.LessThan
	case "LessThanEqual":
		return filters.LessThanEqual
	case "GreaterThan":
		return filters.GreaterThan
	case "GreaterThanEqual":
		return filters.GreaterThanEqual
	case "Like":
		return filters.Like
	case "ContainsAny":
		return filters.ContainsAny
	case "ContainsAll":
		return filters.ContainsAll
	default:
		// Default to Equal if unknown
		return filters.Equal
	}
}
