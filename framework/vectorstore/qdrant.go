package vectorstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/qdrant/go-client/qdrant"
)

// QdrantConfig represents the configuration for the Qdrant vector store.
type QdrantConfig struct {
	Host   schemas.EnvVar `json:"host"`              // Qdrant server host - REQUIRED
	Port   schemas.EnvVar `json:"port"`              // Qdrant server port  (fallback to 6334 for gRPC)
	APIKey schemas.EnvVar `json:"api_key,omitempty"` // API key for authentication - Optional
	UseTLS schemas.EnvVar `json:"use_tls,omitempty"` // Use TLS for connection - Optional
}

// QdrantStore represents the Qdrant vector store.
type QdrantStore struct {
	client *qdrant.Client
	logger schemas.Logger
}

// Ping checks if the Qdrant server is reachable.
func (s *QdrantStore) Ping(ctx context.Context) error {
	_, err := s.client.HealthCheck(ctx)
	return err
}

// CreateNamespace creates a new collection in the Qdrant vector store.
func (s *QdrantStore) CreateNamespace(ctx context.Context, namespace string, dimension int, properties map[string]VectorStoreProperties) error {
	exists, err := s.client.CollectionExists(ctx, namespace)
	if err != nil {
		return fmt.Errorf("failed to check collection existence: %w", err)
	}

	if !exists {
		err = s.client.CreateCollection(ctx, &qdrant.CreateCollection{
			CollectionName: namespace,
			VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
				Size:     uint64(dimension),
				Distance: qdrant.Distance_Cosine,
			}),
		})
		if err != nil {
			return fmt.Errorf("failed to create collection: %w", err)
		}
	}

	for fieldName, prop := range properties {
		var fieldType qdrant.FieldType
		switch prop.DataType {
		case VectorStorePropertyTypeInteger:
			fieldType = qdrant.FieldType_FieldTypeInteger
		case VectorStorePropertyTypeBoolean:
			fieldType = qdrant.FieldType_FieldTypeBool
		default:
			fieldType = qdrant.FieldType_FieldTypeKeyword
		}

		_, err = s.client.CreateFieldIndex(ctx, &qdrant.CreateFieldIndexCollection{
			CollectionName: namespace,
			FieldName:      fieldName,
			FieldType:      &fieldType,
		})
		if err != nil {
			s.logger.Debug(fmt.Sprintf("failed to create index for field %s: %v", fieldName, err))
		}
	}

	return nil
}

// DeleteNamespace deletes a collection from the Qdrant vector store.
func (s *QdrantStore) DeleteNamespace(ctx context.Context, namespace string) error {
	exists, err := s.client.CollectionExists(ctx, namespace)
	if err != nil {
		return fmt.Errorf("failed to check collection existence: %w", err)
	}
	if !exists {
		return nil
	}
	return s.client.DeleteCollection(ctx, namespace)
}

// GetChunk retrieves a single point from the Qdrant vector store.
func (s *QdrantStore) GetChunk(ctx context.Context, namespace string, id string) (SearchResult, error) {
	if strings.TrimSpace(id) == "" {
		return SearchResult{}, fmt.Errorf("id is required")
	}

	pointID, err := parsePointID(id)
	if err != nil {
		return SearchResult{}, fmt.Errorf("invalid id format: %w", err)
	}

	points, err := s.client.Get(ctx, &qdrant.GetPoints{
		CollectionName: namespace,
		Ids:            []*qdrant.PointId{pointID},
		WithPayload:    qdrant.NewWithPayload(true),
	})
	if err != nil {
		return SearchResult{}, fmt.Errorf("failed to get point: %w", err)
	}

	if len(points) == 0 {
		return SearchResult{}, fmt.Errorf("not found: %s", id)
	}

	return SearchResult{
		ID:         id,
		Properties: payloadToMap(points[0].Payload),
	}, nil
}

// GetChunks retrieves multiple points from the Qdrant vector store.
func (s *QdrantStore) GetChunks(ctx context.Context, namespace string, ids []string) ([]SearchResult, error) {
	if len(ids) == 0 {
		return []SearchResult{}, nil
	}

	pointIDs := make([]*qdrant.PointId, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		pointID, err := parsePointID(id)
		if err != nil {
			s.logger.Debug(fmt.Sprintf("skipping invalid id %s: %v", id, err))
			continue
		}
		pointIDs = append(pointIDs, pointID)
	}

	if len(pointIDs) == 0 {
		return []SearchResult{}, nil
	}

	points, err := s.client.Get(ctx, &qdrant.GetPoints{
		CollectionName: namespace,
		Ids:            pointIDs,
		WithPayload:    qdrant.NewWithPayload(true),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get points: %w", err)
	}

	results := make([]SearchResult, 0, len(points))
	for _, point := range points {
		results = append(results, SearchResult{
			ID:         pointIDToString(point.Id),
			Properties: payloadToMap(point.Payload),
		})
	}
	return results, nil
}

// GetAll retrieves all points with optional filtering and pagination.
func (s *QdrantStore) GetAll(ctx context.Context, namespace string, queries []Query, selectFields []string, cursor *string, limit int64) ([]SearchResult, *string, error) {
	filter := buildQdrantFilter(queries)

	var offset *qdrant.PointId
	if cursor != nil && *cursor != "" {
		var err error
		offset, err = parsePointID(*cursor)
		if err != nil {
			s.logger.Debug(fmt.Sprintf("invalid cursor format: %v", err))
		}
	}

	scrollLimit := uint32(limit)
	if limit <= 0 {
		scrollLimit = 100
	}

	scrollResult, err := s.client.Scroll(ctx, &qdrant.ScrollPoints{
		CollectionName: namespace,
		Filter:         filter,
		Limit:          &scrollLimit,
		Offset:         offset,
		WithPayload:    qdrant.NewWithPayload(true),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to scroll points: %w", err)
	}

	results := make([]SearchResult, 0, len(scrollResult))
	var lastID string

	for _, point := range scrollResult {
		lastID = pointIDToString(point.Id)
		results = append(results, SearchResult{
			ID:         lastID,
			Properties: filterProperties(payloadToMap(point.Payload), selectFields),
		})
	}

	if len(scrollResult) >= int(scrollLimit) {
		return results, &lastID, nil
	}
	return results, nil, nil
}

// GetNearest retrieves the nearest points to a vector.
func (s *QdrantStore) GetNearest(ctx context.Context, namespace string, vector []float32, queries []Query, selectFields []string, threshold float64, limit int64) ([]SearchResult, error) {
	filter := buildQdrantFilter(queries)

	searchLimit := uint64(limit)
	if limit <= 0 {
		searchLimit = 10
	}

	searchResult, err := s.client.Query(ctx, &qdrant.QueryPoints{
		CollectionName: namespace,
		Query:          qdrant.NewQuery(vector...),
		Filter:         filter,
		Limit:          &searchLimit,
		WithPayload:    qdrant.NewWithPayload(true),
		ScoreThreshold: qdrant.PtrOf(float32(threshold)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search points: %w", err)
	}

	results := make([]SearchResult, 0, len(searchResult))
	for _, point := range searchResult {
		score := float64(point.Score)
		results = append(results, SearchResult{
			ID:         pointIDToString(point.Id),
			Score:      &score,
			Properties: filterProperties(payloadToMap(point.Payload), selectFields),
		})
	}

	return results, nil
}

// Add stores a new point in the Qdrant vector store.
func (s *QdrantStore) Add(ctx context.Context, namespace string, id string, embedding []float32, metadata map[string]interface{}) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}

	pointID, err := parsePointID(id)
	if err != nil {
		return fmt.Errorf("invalid id format (must be UUID): %w", err)
	}

	point := &qdrant.PointStruct{
		Id:      pointID,
		Payload: mapToPayload(metadata),
	}
	if len(embedding) > 0 {
		point.Vectors = qdrant.NewVectors(embedding...)
	}

	_, err = s.client.Upsert(ctx, &qdrant.UpsertPoints{
		CollectionName: namespace,
		Points:         []*qdrant.PointStruct{point},
		Wait:           qdrant.PtrOf(true),
	})
	if err != nil {
		return fmt.Errorf("failed to upsert point: %w", err)
	}
	return nil
}

// Delete removes a point from the Qdrant vector store.
func (s *QdrantStore) Delete(ctx context.Context, namespace string, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}

	pointID, err := parsePointID(id)
	if err != nil {
		return fmt.Errorf("invalid id format: %w", err)
	}

	_, err = s.client.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: namespace,
		Points:         qdrant.NewPointsSelector(pointID),
	})
	return err
}

// DeleteAll removes multiple points matching the filter.
func (s *QdrantStore) DeleteAll(ctx context.Context, namespace string, queries []Query) ([]DeleteResult, error) {
	filter := buildQdrantFilter(queries)

	scrollResult, err := s.client.Scroll(ctx, &qdrant.ScrollPoints{
		CollectionName: namespace,
		Filter:         filter,
		WithPayload:    qdrant.NewWithPayload(false),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to scroll points: %w", err)
	}

	if len(scrollResult) == 0 {
		return []DeleteResult{}, nil
	}

	results := make([]DeleteResult, len(scrollResult))
	for i, point := range scrollResult {
		results[i] = DeleteResult{
			ID:     pointIDToString(point.Id),
			Status: DeleteStatusSuccess,
		}
	}

	var deleteErr error
	if filter != nil {
		_, deleteErr = s.client.Delete(ctx, &qdrant.DeletePoints{
			CollectionName: namespace,
			Points:         qdrant.NewPointsSelectorFilter(filter),
		})
	} else {
		pointIDs := make([]*qdrant.PointId, len(scrollResult))
		for i, point := range scrollResult {
			pointIDs[i] = point.Id
		}
		_, deleteErr = s.client.Delete(ctx, &qdrant.DeletePoints{
			CollectionName: namespace,
			Points:         qdrant.NewPointsSelectorIDs(pointIDs),
		})
	}

	if deleteErr != nil {
		for i := range results {
			results[i].Status = DeleteStatusError
			results[i].Error = deleteErr.Error()
		}
	}

	return results, nil
}

// Close closes the Qdrant client connection.
func (s *QdrantStore) Close(ctx context.Context, namespace string) error {
	return s.client.Close()
}

// newQdrantStore creates a new Qdrant vector store.
func newQdrantStore(ctx context.Context, config *QdrantConfig, logger schemas.Logger) (*QdrantStore, error) {
	if strings.TrimSpace(config.Host.GetValue()) == "" {
		return nil, fmt.Errorf("qdrant host is required")
	}
	client, err := qdrant.NewClient(&qdrant.Config{
		Host:                   config.Host.GetValue(),
		Port:                   config.Port.CoerceInt(6334),
		APIKey:                 config.APIKey.GetValue(),
		UseTLS:                 config.UseTLS.CoerceBool(false),
		SkipCompatibilityCheck: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create qdrant client: %w", err)
	}

	_, err = client.HealthCheck(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to qdrant: %w", err)
	}

	return &QdrantStore{
		client: client,
		logger: logger,
	}, nil
}

func parsePointID(id string) (*qdrant.PointId, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, err
	}
	return qdrant.NewID(id), nil
}

func pointIDToString(id *qdrant.PointId) string {
	if id == nil {
		return ""
	}
	switch v := id.PointIdOptions.(type) {
	case *qdrant.PointId_Uuid:
		return v.Uuid
	case *qdrant.PointId_Num:
		return fmt.Sprintf("%d", v.Num)
	default:
		return ""
	}
}

func payloadToMap(payload map[string]*qdrant.Value) map[string]interface{} {
	if payload == nil {
		return make(map[string]interface{})
	}

	result := make(map[string]interface{}, len(payload))
	for k, v := range payload {
		result[k] = valueToInterface(v)
	}
	return result
}

func valueToInterface(v *qdrant.Value) interface{} {
	if v == nil {
		return nil
	}
	switch val := v.Kind.(type) {
	case *qdrant.Value_StringValue:
		return val.StringValue
	case *qdrant.Value_IntegerValue:
		return val.IntegerValue
	case *qdrant.Value_DoubleValue:
		return val.DoubleValue
	case *qdrant.Value_BoolValue:
		return val.BoolValue
	case *qdrant.Value_ListValue:
		list := make([]interface{}, len(val.ListValue.Values))
		for i, item := range val.ListValue.Values {
			list[i] = valueToInterface(item)
		}
		return list
	case *qdrant.Value_StructValue:
		return payloadToMap(val.StructValue.Fields)
	default:
		return nil
	}
}

func mapToPayload(m map[string]interface{}) map[string]*qdrant.Value {
	if m == nil {
		return make(map[string]*qdrant.Value)
	}
	return qdrant.NewValueMap(m)
}

func filterProperties(props map[string]interface{}, selectFields []string) map[string]interface{} {
	if len(selectFields) == 0 {
		return props
	}
	filtered := make(map[string]interface{}, len(selectFields))
	for _, field := range selectFields {
		if val, ok := props[field]; ok {
			filtered[field] = val
		}
	}
	return filtered
}

func buildQdrantFilter(queries []Query) *qdrant.Filter {
	if len(queries) == 0 {
		return nil
	}

	var conditions []*qdrant.Condition
	for _, q := range queries {
		condition := buildQdrantCondition(q)
		if condition != nil {
			conditions = append(conditions, condition)
		}
	}

	if len(conditions) == 0 {
		return nil
	}

	return &qdrant.Filter{
		Must: conditions,
	}
}

func buildQdrantCondition(q Query) *qdrant.Condition {
	field := q.Field

	switch q.Operator {
	case QueryOperatorEqual:
		return buildMatchCondition(field, q.Value)
	case QueryOperatorNotEqual:
		matchCond := buildMatchCondition(field, q.Value)
		if matchCond == nil {
			return nil
		}
		return qdrant.NewFilterAsCondition(&qdrant.Filter{
			MustNot: []*qdrant.Condition{matchCond},
		})
	case QueryOperatorGreaterThan:
		return buildRangeCondition(field, q.Value, "gt")
	case QueryOperatorGreaterThanOrEqual:
		return buildRangeCondition(field, q.Value, "gte")
	case QueryOperatorLessThan:
		return buildRangeCondition(field, q.Value, "lt")
	case QueryOperatorLessThanOrEqual:
		return buildRangeCondition(field, q.Value, "lte")
	case QueryOperatorIsNull:
		return qdrant.NewIsNull(field)
	case QueryOperatorIsNotNull:
		return qdrant.NewFilterAsCondition(&qdrant.Filter{
			MustNot: []*qdrant.Condition{qdrant.NewIsNull(field)},
		})
	case QueryOperatorContainsAny:
		switch v := q.Value.(type) {
		case []string:
			return qdrant.NewMatchKeywords(field, v...)
		case []int:
			int64s := make([]int64, len(v))
			for i, val := range v {
				int64s[i] = int64(val)
			}
			return qdrant.NewMatchInts(field, int64s...)
		case []int64:
			return qdrant.NewMatchInts(field, v...)
		}
		return buildMatchCondition(field, q.Value)
	case QueryOperatorContainsAll:
		if values, ok := q.Value.([]interface{}); ok {
			var mustConditions []*qdrant.Condition
			for _, v := range values {
				cond := buildMatchCondition(field, v)
				if cond != nil {
					mustConditions = append(mustConditions, cond)
				}
			}
			if len(mustConditions) > 0 {
				return qdrant.NewFilterAsCondition(&qdrant.Filter{
					Must: mustConditions,
				})
			}
		}
		return buildMatchCondition(field, q.Value)
	case QueryOperatorLike:
		if str, ok := q.Value.(string); ok {
			return qdrant.NewMatchText(field, str)
		}
		return nil
	default:
		return buildMatchCondition(field, q.Value)
	}
}

func buildMatchCondition(field string, value interface{}) *qdrant.Condition {
	switch v := value.(type) {
	case string:
		return qdrant.NewMatchKeyword(field, v)
	case int:
		return qdrant.NewMatchInt(field, int64(v))
	case int32:
		return qdrant.NewMatchInt(field, int64(v))
	case int64:
		return qdrant.NewMatchInt(field, v)
	case bool:
		return qdrant.NewMatchBool(field, v)
	default:
		return qdrant.NewMatchKeyword(field, fmt.Sprintf("%v", v))
	}
}

func buildRangeCondition(field string, value interface{}, op string) *qdrant.Condition {
	var floatVal float64
	switch v := value.(type) {
	case int:
		floatVal = float64(v)
	case int32:
		floatVal = float64(v)
	case int64:
		floatVal = float64(v)
	case float32:
		floatVal = float64(v)
	case float64:
		floatVal = v
	default:
		return nil
	}

	r := &qdrant.Range{}
	switch op {
	case "gt":
		r.Gt = qdrant.PtrOf(floatVal)
	case "gte":
		r.Gte = qdrant.PtrOf(floatVal)
	case "lt":
		r.Lt = qdrant.PtrOf(floatVal)
	case "lte":
		r.Lte = qdrant.PtrOf(floatVal)
	}
	return qdrant.NewRange(field, r)
}
