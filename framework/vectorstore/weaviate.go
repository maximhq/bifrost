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

	// Select fields
	Properties []WeaviateProperties `json:"properties,omitempty"`
}

type WeaviateProperties struct {
	Name     string   `json:"name"`
	DataType []string `json:"data_type"`
}

// WeaviateStore represents the Weaviate vector store.
type WeaviateStore struct {
	client    *weaviate.Client
	config    *WeaviateConfig
	logger    schemas.Logger
	className string
}

// Add stores a new object (with or without embedding)
func (s *WeaviateStore) Add(ctx context.Context, id string, embedding []float32, metadata map[string]interface{}) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}

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
	if len(embedding) > 0 {
		_, err = s.client.Data().Creator().
			WithClassName(s.className).
			WithID(id).
			WithProperties(obj.Properties).
			WithVector(embedding).
			Do(ctx)
	} else {
		_, err = s.client.Data().Creator().
			WithClassName(s.className).
			WithID(id).
			WithProperties(obj.Properties).
			Do(ctx)
	}

	return err
}

// GetChunk returns the "metadata" for a single key
func (s *WeaviateStore) GetChunk(ctx context.Context, id string) (SearchResult, error) {
	obj, err := s.client.Data().ObjectsGetter().
		WithClassName(s.className).
		WithID(id).
		Do(ctx)
	if err != nil {
		return SearchResult{}, err
	}
	if len(obj) == 0 {
		return SearchResult{}, fmt.Errorf("not found: %s", id)
	}

	props, ok := obj[0].Properties.(map[string]interface{})
	if !ok {
		return SearchResult{}, fmt.Errorf("invalid properties")
	}

	return SearchResult{
		ID:         id,
		Score:      nil,
		Properties: props,
	}, nil
}

// GetChunks returns multiple objects by ID
func (s *WeaviateStore) GetChunks(ctx context.Context, ids []string) ([]SearchResult, error) {
	out := make([]SearchResult, 0, len(ids))
	for _, id := range ids {
		obj, err := s.client.Data().ObjectsGetter().
			WithClassName(s.className).
			WithID(id).
			Do(ctx)
		if err != nil {
			return nil, err
		}
		if len(obj) > 0 {
			props, ok := obj[0].Properties.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("invalid properties")
			}
			out = append(out, SearchResult{
				ID:         id,
				Score:      nil,
				Properties: props,
			})
		}
	}
	return out, nil
}

// GetAll with filtering + pagination
func (s *WeaviateStore) GetAll(ctx context.Context, queries []Query, selectFields []string, cursor *string, count int64) ([]SearchResult, *string, error) {
	where := buildWeaviateFilter(queries)

	fields := []graphql.Field{
		{Name: "_additional", Fields: []graphql.Field{
			{Name: "id"},
		}},
	}
	for _, field := range selectFields {
		fields = append(fields, graphql.Field{Name: field})
	}

	search := s.client.GraphQL().Get().
		WithClassName(s.className).
		WithLimit(int(count)).
		WithFields(fields...)

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

	results := make([]SearchResult, 0, len(objs))
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
	selectFields []string,
	threshold float64,
	limit int64,
) ([]SearchResult, error) {
	where := buildWeaviateFilter(queries)

	fields := []graphql.Field{
		{Name: "_additional", Fields: []graphql.Field{
			{Name: "id"},
			{Name: "certainty"},
		}},
	}

	for _, field := range selectFields {
		fields = append(fields, graphql.Field{Name: field})
	}

	nearVector := s.client.GraphQL().NearVectorArgBuilder().
		WithVector(vector).
		WithCertainty(float32(threshold))

	search := s.client.GraphQL().Get().
		WithClassName(s.className).
		WithNearVector(nearVector).
		WithLimit(int(limit)).
		WithFields(fields...)

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

		additional, ok := obj["_additional"].(map[string]interface{})
		if !ok {
			continue
		}

		// Safely extract ID
		idRaw, exists := additional["id"]
		if !exists || idRaw == nil {
			continue
		}
		id, ok := idRaw.(string)
		if !ok {
			continue
		}

		// Safely extract certainty/score with default value
		var score float64
		if certaintyRaw, exists := additional["certainty"]; exists && certaintyRaw != nil {
			switch v := certaintyRaw.(type) {
			case float64:
				score = v
			case float32:
				score = float64(v)
			case int:
				score = float64(v)
			case int64:
				score = float64(v)
			default:
				score = 0.0 // Default score if type conversion fails
			}
		}

		results = append(results, SearchResult{
			ID:         id,
			Score:      &score,
			Properties: obj,
		})
	}

	return results, nil
}

// Delete removes multiple objects by ID
func (s *WeaviateStore) Delete(ctx context.Context, id string) error {
	return s.client.Data().Deleter().
		WithClassName(s.className).
		WithID(id).
		Do(ctx)
}

func (s *WeaviateStore) DeleteAll(ctx context.Context, queries []Query) ([]DeleteResult, error) {
	where := buildWeaviateFilter(queries)

	res, err := s.client.Batch().ObjectsBatchDeleter().
		WithClassName(s.className).
		WithWhere(where).
		Do(ctx)
	if err != nil {
		return nil, err
	}

	// NOTE: Weaviate is returning an empty array for Results.Objects, even on successful deletes.
	results := make([]DeleteResult, 0, len(res.Results.Objects))

	for _, obj := range res.Results.Objects {
		result := DeleteResult{
			ID: obj.ID.String(),
		}

		if obj.Status != nil {
			switch *obj.Status {
			case "SUCCESS":
				result.Status = DeleteStatusSuccess
			case "FAILED":
				result.Status = DeleteStatusError

				if obj.Errors != nil {
					var errorMsgs []string
					for _, err := range obj.Errors.Error {
						errorMsgs = append(errorMsgs, err.Message)
					}

					result.Error = strings.Join(errorMsgs, ", ")
				}
			}
		}

		results = append(results, result)
	}

	return results, nil
}

func (s *WeaviateStore) Close(ctx context.Context) error {
	// nothing to close
	return nil
}

// newWeaviateStore creates a new Weaviate vector store.
func newWeaviateStore(ctx context.Context, config *WeaviateConfig, logger schemas.Logger) (*WeaviateStore, error) {
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
	// Check if class exists
	exists, err := client.Schema().ClassExistenceChecker().
		WithClassName(config.ClassName).
		Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check class existence: %w", err)
	}

	if exists {
		return store, nil // Schema already exists
	}

	// Create properties
	properties := []*models.Property{}
	for _, prop := range config.Properties {
		properties = append(properties, &models.Property{
			Name:     prop.Name,
			DataType: prop.DataType,
		})
	}

	// Create class schema with all fields we need
	classSchema := &models.Class{
		Class:           config.ClassName,
		Properties:      properties,
		VectorIndexType: "hnsw",
		Vectorizer:      "none", // We provide our own vectors
	}

	err = client.Schema().ClassCreator().
		WithClass(classSchema).
		Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create class schema: %w", err)
	}

	return store, nil
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

		// Special handling for IsNull and IsNotNull
		switch q.Operator {
		case QueryOperatorIsNull:
			whereClause = whereClause.WithValueBoolean(true)
		case QueryOperatorIsNotNull:
			whereClause = whereClause.WithValueBoolean(false)
		default:
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
func convertOperator(op QueryOperator) filters.WhereOperator {
	switch op {
	case QueryOperatorEqual:
		return filters.Equal
	case QueryOperatorNotEqual:
		return filters.NotEqual
	case QueryOperatorLessThan:
		return filters.LessThan
	case QueryOperatorLessThanOrEqual:
		return filters.LessThanEqual
	case QueryOperatorGreaterThan:
		return filters.GreaterThan
	case QueryOperatorGreaterThanOrEqual:
		return filters.GreaterThanEqual
	case QueryOperatorLike:
		return filters.Like
	case QueryOperatorContainsAny:
		return filters.ContainsAny
	case QueryOperatorContainsAll:
		return filters.ContainsAll
	case QueryOperatorIsNull:
		return filters.IsNull
	case QueryOperatorIsNotNull: // IsNotNull is not supported by Weaviate, so we use IsNull and negate it.
		return filters.IsNull
	default:
		// Default to Equal if unknown
		return filters.Equal
	}
}
