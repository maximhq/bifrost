package vectorstore

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/pinecone-io/go-pinecone/v5/pinecone"
	"google.golang.org/protobuf/types/known/structpb"
)

// PineconeConfig represents the configuration for the Pinecone vector store.
type PineconeConfig struct {
	APIKey    schemas.EnvVar `json:"api_key"`    // Pinecone API key - REQUIRED
	IndexHost schemas.EnvVar `json:"index_host"` // Index host URL from Pinecone console - REQUIRED
}

// PineconeStore represents the Pinecone vector store.
type PineconeStore struct {
	client     *pinecone.Client
	indexConn  *pinecone.IndexConnection
	config     *PineconeConfig
	logger     schemas.Logger
	mu         sync.RWMutex // Protects namespaces and dimension
	namespaces map[string]*pinecone.IndexConnection
	dimension  int // Store dimension for zero vector queries in GetAll
}

// Ping checks if the Pinecone server is reachable.
func (s *PineconeStore) Ping(ctx context.Context) error {
	_, err := s.indexConn.DescribeIndexStats(ctx)
	return err
}

// CreateNamespace creates a new namespace in the Pinecone vector store.
// Note: Pinecone namespaces are created implicitly when upserting vectors.
// This method is a no-op but ensures the connection is valid.
func (s *PineconeStore) CreateNamespace(ctx context.Context, namespace string, dimension int, properties map[string]VectorStoreProperties) error {
	// Store dimension for use in GetAll (zero vector queries)
	s.mu.Lock()
	s.dimension = dimension
	s.mu.Unlock()

	// Pinecone namespaces are created automatically on first upsert.
	// We just verify the index connection is valid.
	_, err := s.indexConn.DescribeIndexStats(ctx)
	if err != nil {
		return fmt.Errorf("failed to verify index connection: %w", err)
	}
	return nil
}

// DeleteNamespace deletes a namespace from the Pinecone vector store.
func (s *PineconeStore) DeleteNamespace(ctx context.Context, namespace string) error {
	idxConn, err := s.getNamespaceConnection(namespace)
	if err != nil {
		return err
	}
	return idxConn.DeleteAllVectorsInNamespace(ctx)
}

// GetChunk retrieves a single vector from the Pinecone vector store.
func (s *PineconeStore) GetChunk(ctx context.Context, namespace string, id string) (SearchResult, error) {
	if strings.TrimSpace(id) == "" {
		return SearchResult{}, fmt.Errorf("id is required")
	}

	idxConn, err := s.getNamespaceConnection(namespace)
	if err != nil {
		return SearchResult{}, err
	}

	res, err := idxConn.FetchVectors(ctx, []string{id})
	if err != nil {
		return SearchResult{}, fmt.Errorf("failed to fetch vector: %w", err)
	}

	if len(res.Vectors) == 0 {
		return SearchResult{}, fmt.Errorf("not found: %s", id)
	}

	vec, exists := res.Vectors[id]
	if !exists || vec == nil {
		return SearchResult{}, fmt.Errorf("not found: %s", id)
	}

	return SearchResult{
		ID:         id,
		Properties: metadataToMap(vec.Metadata),
	}, nil
}

// GetChunks retrieves multiple vectors from the Pinecone vector store.
func (s *PineconeStore) GetChunks(ctx context.Context, namespace string, ids []string) ([]SearchResult, error) {
	if len(ids) == 0 {
		return []SearchResult{}, nil
	}

	// Filter out empty IDs
	validIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			validIDs = append(validIDs, id)
		}
	}

	if len(validIDs) == 0 {
		return []SearchResult{}, nil
	}

	idxConn, err := s.getNamespaceConnection(namespace)
	if err != nil {
		return nil, err
	}

	res, err := idxConn.FetchVectors(ctx, validIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch vectors: %w", err)
	}

	results := make([]SearchResult, 0, len(res.Vectors))
	for id, vec := range res.Vectors {
		if vec != nil {
			results = append(results, SearchResult{
				ID:         id,
				Properties: metadataToMap(vec.Metadata),
			})
		}
	}

	return results, nil
}

// GetAll retrieves all vectors with optional filtering and pagination.
// Note: This implementation uses QueryByVectorValues with a zero vector instead of ListVectors
// because ListVectors has severe eventual consistency issues on Pinecone Serverless/Starter indexes.
// The metadata filtering is done server-side by Pinecone, providing much better consistency.
func (s *PineconeStore) GetAll(ctx context.Context, namespace string, queries []Query, selectFields []string, cursor *string, limit int64) ([]SearchResult, *string, error) {
	idxConn, err := s.getNamespaceConnection(namespace)
	if err != nil {
		return nil, nil, err
	}

	topK := uint32(limit)
	if limit <= 0 {
		topK = 100
	}

	// Create zero vector for query - this allows us to use QueryByVectorValues
	// which has much better consistency than ListVectors
	s.mu.RLock()
	dim := s.dimension
	s.mu.RUnlock()
	if dim <= 0 {
		return nil, nil, fmt.Errorf("dimension not set: CreateNamespace must be called before GetAll to set the vector dimension")
	}
	zeroVector := make([]float32, dim)

	queryReq := &pinecone.QueryByVectorValuesRequest{
		Vector:          zeroVector,
		TopK:            topK,
		IncludeValues:   false,
		IncludeMetadata: true,
	}

	// Build metadata filter from queries - filtering is done server-side
	if len(queries) > 0 {
		filter, filterErr := buildPineconeFilter(queries)
		if filterErr != nil {
			s.logger.Warn("failed to build pinecone filter, queries may not be applied: %v", filterErr)
		}
		if filter != nil {
			queryReq.MetadataFilter = filter
		}
	}

	res, err := idxConn.QueryByVectorValues(ctx, queryReq)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query vectors: %w", err)
	}

	results := make([]SearchResult, 0, len(res.Matches))
	for _, match := range res.Matches {
		if match.Vector == nil {
			continue
		}

		props := metadataToMap(match.Vector.Metadata)
		filteredProps := filterPropertiesPinecone(props, selectFields)

		results = append(results, SearchResult{
			ID:         match.Vector.Id,
			Properties: filteredProps,
		})
	}

	// Note: QueryByVectorValues doesn't support pagination tokens like ListVectors
	// For direct hash lookup (the main use case), we only need 1 result anyway
	return results, nil, nil
}

// GetNearest retrieves the nearest vectors to a given vector.
func (s *PineconeStore) GetNearest(ctx context.Context, namespace string, vector []float32, queries []Query, selectFields []string, threshold float64, limit int64) ([]SearchResult, error) {
	idxConn, err := s.getNamespaceConnection(namespace)
	if err != nil {
		return nil, err
	}

	topK := uint32(limit)
	if limit <= 0 {
		topK = 10
	}

	queryReq := &pinecone.QueryByVectorValuesRequest{
		Vector:          vector,
		TopK:            topK,
		IncludeValues:   false,
		IncludeMetadata: true,
	}

	// Build metadata filter from queries
	if len(queries) > 0 {
		filter, err := buildPineconeFilter(queries)
		if err != nil {
			s.logger.Debug(fmt.Sprintf("failed to build pinecone filter: %v", err))
		} else if filter != nil {
			queryReq.MetadataFilter = filter
		}
	}

	res, err := idxConn.QueryByVectorValues(ctx, queryReq)
	if err != nil {
		return nil, fmt.Errorf("failed to query vectors: %w", err)
	}

	results := make([]SearchResult, 0, len(res.Matches))
	for _, match := range res.Matches {
		if match.Vector == nil {
			continue
		}

		score := float64(match.Score)

		// Apply threshold filter
		if score < threshold {
			continue
		}

		props := metadataToMap(match.Vector.Metadata)
		filteredProps := filterPropertiesPinecone(props, selectFields)

		results = append(results, SearchResult{
			ID:         match.Vector.Id,
			Score:      &score,
			Properties: filteredProps,
		})
	}

	return results, nil
}

// convertMetadataForStructpb converts metadata map to be compatible with structpb.NewStruct.
// Specifically, it converts []string to []interface{} since structpb doesn't handle []string directly.
func convertMetadataForStructpb(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	converted := make(map[string]interface{}, len(metadata))
	for k, v := range metadata {
		switch val := v.(type) {
		case []string:
			// Convert []string to []interface{}
			interfaceSlice := make([]interface{}, len(val))
			for i, s := range val {
				interfaceSlice[i] = s
			}
			converted[k] = interfaceSlice
		default:
			converted[k] = v
		}
	}
	return converted
}

// Add stores a new vector in the Pinecone vector store.
func (s *PineconeStore) Add(ctx context.Context, namespace string, id string, embedding []float32, metadata map[string]interface{}) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}

	idxConn, err := s.getNamespaceConnection(namespace)
	if err != nil {
		return err
	}

	// Convert metadata to structpb (handle []string -> []interface{} conversion)
	var pbMetadata *structpb.Struct
	if len(metadata) > 0 {
		convertedMetadata := convertMetadataForStructpb(metadata)
		pbMetadata, err = structpb.NewStruct(convertedMetadata)
		if err != nil {
			return fmt.Errorf("failed to convert metadata: %w", err)
		}
	}

	vec := &pinecone.Vector{
		Id:       id,
		Metadata: pbMetadata,
	}

	if len(embedding) > 0 {
		vec.Values = &embedding
	}

	_, err = idxConn.UpsertVectors(ctx, []*pinecone.Vector{vec})
	if err != nil {
		return fmt.Errorf("failed to upsert vector: %w", err)
	}

	return nil
}

// Delete removes a vector from the Pinecone vector store.
func (s *PineconeStore) Delete(ctx context.Context, namespace string, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}

	idxConn, err := s.getNamespaceConnection(namespace)
	if err != nil {
		return err
	}

	return idxConn.DeleteVectorsById(ctx, []string{id})
}

// DeleteAll removes multiple vectors matching the filter.
func (s *PineconeStore) DeleteAll(ctx context.Context, namespace string, queries []Query) ([]DeleteResult, error) {
	idxConn, err := s.getNamespaceConnection(namespace)
	if err != nil {
		return nil, err
	}

	// If we have queries, use filter-based deletion
	if len(queries) > 0 {
		filter, err := buildPineconeFilter(queries)
		if err != nil {
			return nil, fmt.Errorf("failed to build filter: %w", err)
		}

		if filter != nil {
			err = idxConn.DeleteVectorsByFilter(ctx, filter)
			if err != nil {
				return nil, fmt.Errorf("failed to delete vectors by filter: %w", err)
			}
			// Pinecone doesn't return individual results for filter-based deletion
			return []DeleteResult{}, nil
		}
	}

	// If no queries, list and delete all vectors in the namespace
	listRes, err := idxConn.ListVectors(ctx, &pinecone.ListVectorsRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list vectors: %w", err)
	}

	if len(listRes.VectorIds) == 0 {
		return []DeleteResult{}, nil
	}

	// Convert []*string to []string
	deleteIDs := make([]string, 0, len(listRes.VectorIds))
	for _, id := range listRes.VectorIds {
		if id != nil {
			deleteIDs = append(deleteIDs, *id)
		}
	}

	results := make([]DeleteResult, len(deleteIDs))
	for i, id := range deleteIDs {
		results[i] = DeleteResult{
			ID:     id,
			Status: DeleteStatusSuccess,
		}
	}

	err = idxConn.DeleteVectorsById(ctx, deleteIDs)
	if err != nil {
		for i := range results {
			results[i].Status = DeleteStatusError
			results[i].Error = err.Error()
		}
	}

	return results, nil
}

// Close closes the Pinecone client connection.
// If namespace is non-empty, only that namespace connection is closed.
// If namespace is empty, all connections (indexConn and all namespaces) are closed.
func (s *PineconeStore) Close(ctx context.Context, namespace string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// If a specific namespace is provided, close only that connection
	if namespace != "" {
		if conn, exists := s.namespaces[namespace]; exists && conn != nil {
			conn.Close()
			delete(s.namespaces, namespace)
		}
		return nil
	}
	// Close all connections when namespace is empty
	var errs []error
	// Close the main index connection
	if s.indexConn != nil {
		s.indexConn.Close()
		s.indexConn = nil
	}
	// Close and remove all namespace connections
	for ns, conn := range s.namespaces {
		if conn != nil {
			conn.Close()
		}
		delete(s.namespaces, ns)
	}
	// Return aggregated errors if any occurred
	if len(errs) > 0 {
		return fmt.Errorf("errors closing connections: %v", errs)
	}
	return nil
}

// RequiresVectors returns true because Pinecone is a dedicated vector database
// that requires vectors for all entries with a specific dimension.
func (s *PineconeStore) RequiresVectors() bool {
	return true
}

// newPineconeStore creates a new Pinecone vector store.
func newPineconeStore(ctx context.Context, config *PineconeConfig, logger schemas.Logger) (*PineconeStore, error) {
	if strings.TrimSpace(config.APIKey.GetValue()) == "" {
		return nil, fmt.Errorf("pinecone api_key is required")
	}
	if strings.TrimSpace(config.IndexHost.GetValue()) == "" {
		return nil, fmt.Errorf("pinecone index_host is required")
	}
	// Creating new client
	client, err := pinecone.NewClient(pinecone.NewClientParams{
		ApiKey: config.APIKey.GetValue(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create pinecone client: %w", err)
	}
	// Prepare the host URL
	// For local connections (Pinecone Local), prefix with http:// to disable TLS
	// See: https://docs.pinecone.io/guides/operations/local-development
	host := config.IndexHost.GetValue()
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		// Check if this looks like a local connection
		if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1") {
			host = "http://" + host
		}
	}
	// Create index connection
	idxConn, err := client.Index(pinecone.NewIndexConnParams{
		Host: host,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create index connection: %w", err)
	}
	// Verify connection by getting index stats
	_, err = idxConn.DescribeIndexStats(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to pinecone index: %w", err)
	}
	return &PineconeStore{
		client:     client,
		indexConn:  idxConn,
		config:     config,
		logger:     logger,
		namespaces: make(map[string]*pinecone.IndexConnection),
	}, nil
}

// getHostWithScheme returns the host with the appropriate scheme.
// For local connections (localhost/127.0.0.1), it adds http:// to disable TLS.
func (s *PineconeStore) getHostWithScheme() string {
	host := s.config.IndexHost.GetValue()
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1") {
			return "http://" + host
		}
	}
	return host
}

// getNamespaceConnection returns or creates a connection for the given namespace.
func (s *PineconeStore) getNamespaceConnection(namespace string) (*pinecone.IndexConnection, error) {
	if namespace == "" {
		return s.indexConn, nil
	}
	// Check if we already have a connection for this namespace (optimistic read)
	s.mu.RLock()
	if conn, exists := s.namespaces[namespace]; exists {
		s.mu.RUnlock()
		return conn, nil
	}
	s.mu.RUnlock()
	// Acquire write lock to create new connection
	s.mu.Lock()
	defer s.mu.Unlock()
	// Double-check after acquiring write lock (another goroutine may have created it)
	if conn, exists := s.namespaces[namespace]; exists {
		return conn, nil
	}
	// Create a new connection for this namespace
	conn, err := s.client.Index(pinecone.NewIndexConnParams{
		Host:      s.getHostWithScheme(),
		Namespace: namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create namespace connection: %w", err)
	}
	s.namespaces[namespace] = conn
	return conn, nil
}

// metadataToMap converts protobuf Struct to map[string]interface{}.
func metadataToMap(metadata *structpb.Struct) map[string]interface{} {
	if metadata == nil {
		return make(map[string]interface{})
	}
	return metadata.AsMap()
}

// filterPropertiesPinecone filters properties based on selected fields.
func filterPropertiesPinecone(props map[string]interface{}, selectFields []string) map[string]interface{} {
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

// matchesQueries checks if properties match all query conditions.
func matchesQueries(props map[string]interface{}, queries []Query) bool {
	if len(queries) == 0 {
		return true
	}
	for _, q := range queries {
		val, exists := props[q.Field]
		if !matchesQuery(val, exists, q) {
			return false
		}
	}
	return true
}

// matchesQuery checks if a single value matches a query condition.
func matchesQuery(val interface{}, exists bool, q Query) bool {
	switch q.Operator {
	case QueryOperatorIsNull:
		return !exists || val == nil
	case QueryOperatorIsNotNull:
		return exists && val != nil
	case QueryOperatorEqual:
		return exists && fmt.Sprintf("%v", val) == fmt.Sprintf("%v", q.Value)
	case QueryOperatorNotEqual:
		return !exists || fmt.Sprintf("%v", val) != fmt.Sprintf("%v", q.Value)
	default:
		// For complex operators, default to true (filter at query time)
		return true
	}
}

// buildPineconeFilter converts queries to Pinecone metadata filter.
func buildPineconeFilter(queries []Query) (*structpb.Struct, error) {
	if len(queries) == 0 {
		return nil, nil
	}

	filterMap := make(map[string]interface{})

	for _, q := range queries {
		condition := buildPineconeCondition(q)
		if condition != nil {
			filterMap[q.Field] = condition
		}
	}

	if len(filterMap) == 0 {
		return nil, nil
	}

	return structpb.NewStruct(filterMap)
}

// buildPineconeCondition builds a single Pinecone filter condition.
func buildPineconeCondition(q Query) interface{} {
	switch q.Operator {
	case QueryOperatorEqual:
		return map[string]interface{}{"$eq": q.Value}
	case QueryOperatorNotEqual:
		return map[string]interface{}{"$ne": q.Value}
	case QueryOperatorGreaterThan:
		return map[string]interface{}{"$gt": q.Value}
	case QueryOperatorGreaterThanOrEqual:
		return map[string]interface{}{"$gte": q.Value}
	case QueryOperatorLessThan:
		return map[string]interface{}{"$lt": q.Value}
	case QueryOperatorLessThanOrEqual:
		return map[string]interface{}{"$lte": q.Value}
	case QueryOperatorIsNull:
		return map[string]interface{}{"$eq": nil}
	case QueryOperatorIsNotNull:
		return map[string]interface{}{"$ne": nil}
	case QueryOperatorContainsAny:
		return map[string]interface{}{"$in": q.Value}
	case QueryOperatorContainsAll:
		// Build an $and array of equality checks so all values must match
		values, ok := q.Value.([]interface{})
		if !ok {
			// Try to convert []string to []interface{}
			if strValues, ok := q.Value.([]string); ok {
				values = make([]interface{}, len(strValues))
				for i, v := range strValues {
					values[i] = v
				}
			} else {
				// Fallback to single value equality
				return map[string]interface{}{"$eq": q.Value}
			}
		}
		andConditions := make([]interface{}, len(values))
		for i, v := range values {
			andConditions[i] = map[string]interface{}{"$eq": v}
		}
		return map[string]interface{}{"$and": andConditions}
	default:
		return map[string]interface{}{"$eq": q.Value}
	}
}
