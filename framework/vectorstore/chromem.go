package vectorstore

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/philippgille/chromem-go"
)

// ChromemConfig represents the configuration for the chromem-go vector store.
// chromem-go is an embeddable vector database that runs in-process with optional persistence.
type ChromemConfig struct {
	// PersistDirectory is the directory for persisting data to disk.
	// If empty, the store runs in-memory only.
	PersistDirectory *schemas.EnvVar `json:"persist_directory,omitempty"`
	// Compress enables gzip compression for persisted files.
	Compress bool `json:"compress,omitempty"`
}

// ChromemStore represents the chromem-go vector store.
type ChromemStore struct {
	db        *chromem.DB
	config    *ChromemConfig
	logger    schemas.Logger
	mu        sync.RWMutex
	dimension int // stored from CreateNamespace for zero-vector queries in GetAll
}

// Ping checks if the chromem-go store is operational.
// For in-memory chromem-go, this always succeeds.
func (s *ChromemStore) Ping(ctx context.Context) error {
	// chromem-go is embeddable/in-process - no network to ping
	return nil
}

// CreateNamespace creates a new collection (namespace) in the chromem-go store.
// chromem-go creates collections implicitly on first use; this ensures the collection exists.
func (s *ChromemStore) CreateNamespace(ctx context.Context, namespace string, dimension int, properties map[string]VectorStoreProperties) error {
	s.mu.Lock()
	s.dimension = dimension
	s.mu.Unlock()
	_, err := s.getOrCreateCollection(namespace)
	return err
}

// DeleteNamespace deletes a collection from the chromem-go store.
func (s *ChromemStore) DeleteNamespace(ctx context.Context, namespace string) error {
	return s.db.DeleteCollection(namespace)
}

// GetChunk retrieves a single document from the chromem-go store.
func (s *ChromemStore) GetChunk(ctx context.Context, namespace string, id string) (SearchResult, error) {
	if strings.TrimSpace(id) == "" {
		return SearchResult{}, fmt.Errorf("id is required")
	}

	col, err := s.getOrCreateCollection(namespace)
	if err != nil {
		return SearchResult{}, err
	}

	doc, err := col.GetByID(ctx, id)
	if err != nil {
		return SearchResult{}, fmt.Errorf("not found: %s", id)
	}

	return SearchResult{
		ID:         id,
		Properties: chromemMetadataToMap(doc.Metadata),
	}, nil
}

// GetChunks retrieves multiple documents from the chromem-go store.
func (s *ChromemStore) GetChunks(ctx context.Context, namespace string, ids []string) ([]SearchResult, error) {
	if len(ids) == 0 {
		return []SearchResult{}, nil
	}

	col, err := s.getOrCreateCollection(namespace)
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		doc, err := col.GetByID(ctx, id)
		if err != nil {
			s.logger.Debug(fmt.Sprintf("failed to get chunk %s: %v", id, err))
			continue
		}
		results = append(results, SearchResult{
			ID:         id,
			Properties: chromemMetadataToMap(doc.Metadata),
		})
	}
	return results, nil
}

// GetAll retrieves all documents with optional filtering and pagination.
// Uses QueryEmbedding with a zero vector since chromem-go v0.7.0 has no ListDocuments.
func (s *ChromemStore) GetAll(ctx context.Context, namespace string, queries []Query, selectFields []string, cursor *string, limit int64) ([]SearchResult, *string, error) {
	col, err := s.getOrCreateCollection(namespace)
	if err != nil {
		return nil, nil, err
	}

	count := col.Count()
	if count == 0 {
		return []SearchResult{}, nil, nil
	}

	s.mu.RLock()
	dim := s.dimension
	s.mu.RUnlock()
	if dim <= 0 {
		return nil, nil, fmt.Errorf("dimension not set: CreateNamespace must be called before GetAll to set the vector dimension")
	}

	// Use QueryEmbedding with zero vector to retrieve all documents (chromem has no ListDocuments)
	where := buildChromemWhere(queries)
	zeroVector := make([]float32, dim)
	results, err := col.QueryEmbedding(ctx, zeroVector, count, where, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query: %w", err)
	}

	// Filter by queries for operators not supported by chromem's where (exact match only)
	allDocs := make([]*chromemDocResult, 0, len(results))
	for _, r := range results {
		props := chromemMetadataToMap(r.Metadata)
		if matchesQueries(props, queries) {
			allDocs = append(allDocs, &chromemDocResult{ID: r.ID, Properties: props})
		}
	}

	// Apply cursor-based pagination
	offset := 0
	if cursor != nil && *cursor != "" {
		if parsed, err := strconv.ParseInt(*cursor, 10, 64); err == nil && parsed >= 0 {
			offset = int(parsed)
		}
	}
	if offset >= len(allDocs) {
		return []SearchResult{}, nil, nil
	}

	effectiveLimit := limit
	if limit <= 0 {
		effectiveLimit = 100
	}
	end := offset + int(effectiveLimit)
	if end > len(allDocs) {
		end = len(allDocs)
	}
	page := allDocs[offset:end]

	searchResults := make([]SearchResult, 0, len(page))
	for _, doc := range page {
		props := doc.Properties
		if len(selectFields) > 0 {
			props = filterPropertiesChromem(props, selectFields)
		}
		searchResults = append(searchResults, SearchResult{
			ID:         doc.ID,
			Properties: props,
		})
	}

	var nextCursor *string
	if end < len(allDocs) {
		next := strconv.FormatInt(int64(end), 10)
		nextCursor = &next
	}

	return searchResults, nextCursor, nil
}

// chromemDocResult holds a document for in-memory filtering.
type chromemDocResult struct {
	ID         string
	Properties map[string]interface{}
}

// GetNearest retrieves the nearest documents to a given vector.
func (s *ChromemStore) GetNearest(ctx context.Context, namespace string, vector []float32, queries []Query, selectFields []string, threshold float64, limit int64) ([]SearchResult, error) {
	col, err := s.getOrCreateCollection(namespace)
	if err != nil {
		return nil, err
	}

	// chromem-go requires nResults > 0 and nResults <= len(documents)
	if col.Count() == 0 {
		return []SearchResult{}, nil
	}

	nResults := limit
	if nResults <= 0 {
		nResults = 10
	}
	if nResults > int64(col.Count()) {
		nResults = int64(col.Count())
	}

	where := buildChromemWhere(queries)
	results, err := col.QueryEmbedding(ctx, vector, int(nResults), where, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query: %w", err)
	}

	// Apply threshold filter and convert to SearchResult
	searchResults := make([]SearchResult, 0, len(results))
	for _, r := range results {
		score := float64(r.Similarity)
		if score < threshold {
			continue
		}
		props := chromemMetadataToMap(r.Metadata)
		if len(selectFields) > 0 {
			props = filterPropertiesChromem(props, selectFields)
		}
		searchResults = append(searchResults, SearchResult{
			ID:         r.ID,
			Score:      &score,
			Properties: props,
		})
	}

	return searchResults, nil
}

// Add stores a new document in the chromem-go store.
func (s *ChromemStore) Add(ctx context.Context, namespace string, id string, embedding []float32, metadata map[string]interface{}) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}

	col, err := s.getOrCreateCollection(namespace)
	if err != nil {
		return err
	}

	doc := chromem.Document{
		ID:        id,
		Embedding: embedding,
		Metadata:  mapToChromemMetadata(metadata),
		Content:   "", // We provide embedding, content not needed
	}

	return col.AddDocument(ctx, doc)
}

// Delete removes a document from the chromem-go store.
func (s *ChromemStore) Delete(ctx context.Context, namespace string, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}

	col, err := s.getOrCreateCollection(namespace)
	if err != nil {
		return err
	}

	return col.Delete(ctx, nil, nil, id)
}

// DeleteAll removes documents matching the given queries.
// Uses chromem's Delete(where) for exact-match filters; for complex queries, uses QueryEmbedding + Delete by ids.
func (s *ChromemStore) DeleteAll(ctx context.Context, namespace string, queries []Query) ([]DeleteResult, error) {
	col, err := s.getOrCreateCollection(namespace)
	if err != nil {
		return nil, err
	}

	count := col.Count()
	if count == 0 {
		return []DeleteResult{}, nil
	}

	where := buildChromemWhere(queries)
	hasOnlyExactMatch := len(queries) == 0 || (len(where) == len(queries) && len(where) > 0)

	if hasOnlyExactMatch && len(queries) == 0 {
		// Delete all: chromem Delete with empty ids but we need where or whereDocument.
		// Pass a dummy where that matches nothing to avoid "must have at least one" - actually
		// the chromem Delete requires at least one of where, whereDocument, or ids.
		// For "delete all" we need to pass ids. Get ids via QueryEmbedding with zero vector.
		s.mu.RLock()
		dim := s.dimension
		s.mu.RUnlock()
		if dim > 0 {
			zeroVector := make([]float32, dim)
			results, qErr := col.QueryEmbedding(ctx, zeroVector, count, nil, nil)
			if qErr != nil {
				return nil, fmt.Errorf("failed to query for delete all: %w", qErr)
			}
			ids := make([]string, 0, len(results))
			for _, r := range results {
				ids = append(ids, r.ID)
			}
			if len(ids) > 0 {
				if err := col.Delete(ctx, nil, nil, ids...); err != nil {
					return nil, err
				}
				out := make([]DeleteResult, len(ids))
				for i, id := range ids {
					out[i] = DeleteResult{ID: id, Status: DeleteStatusSuccess}
				}
				return out, nil
			}
		}
		return []DeleteResult{}, nil
	}

	if hasOnlyExactMatch && len(where) > 0 {
		// chromem Delete supports where filter - use it directly
		if err := col.Delete(ctx, where, nil); err != nil {
			return nil, err
		}
		// chromem doesn't return which docs were deleted; return empty results
		return []DeleteResult{}, nil
	}

	// Complex queries: get docs via QueryEmbedding, filter in memory, delete by ids
	s.mu.RLock()
	dim := s.dimension
	s.mu.RUnlock()
	if dim <= 0 {
		return nil, fmt.Errorf("dimension not set: CreateNamespace must be called before DeleteAll")
	}
	zeroVector := make([]float32, dim)
	results, err := col.QueryEmbedding(ctx, zeroVector, count, where, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query: %w", err)
	}
	ids := make([]string, 0)
	for _, r := range results {
		props := chromemMetadataToMap(r.Metadata)
		if matchesQueries(props, queries) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return []DeleteResult{}, nil
	}
	if err := col.Delete(ctx, nil, nil, ids...); err != nil {
		return nil, err
	}
	out := make([]DeleteResult, len(ids))
	for i, id := range ids {
		out[i] = DeleteResult{ID: id, Status: DeleteStatusSuccess}
	}
	return out, nil
}

// Close closes the chromem-go store.
// chromem-go is in-process; there is nothing to close for in-memory mode.
func (s *ChromemStore) Close(ctx context.Context, namespace string) error {
	// chromem-go doesn't require explicit connection closing
	return nil
}

// RequiresVectors returns true because chromem-go requires embeddings for similarity search.
func (s *ChromemStore) RequiresVectors() bool {
	return true
}

// getOrCreateCollection returns the collection for the given namespace, creating it if needed.
func (s *ChromemStore) getOrCreateCollection(namespace string) (*chromem.Collection, error) {
	// chromem-go's GetOrCreateCollection uses default embedding func when nil.
	// We provide a no-op since we always pass our own embeddings.
	embedFunc := func(ctx context.Context, text string) ([]float32, error) {
		return nil, fmt.Errorf("chromem vector store uses provided embeddings only; embedding func should not be called")
	}
	return s.db.GetOrCreateCollection(namespace, nil, embedFunc)
}

// mapToChromemMetadata converts map[string]interface{} to map[string]string for chromem-go.
func mapToChromemMetadata(m map[string]interface{}) map[string]string {
	if m == nil {
		return nil
	}
	result := make(map[string]string, len(m))
	for k, v := range m {
		if v != nil {
			result[k] = fmt.Sprintf("%v", v)
		}
	}
	return result
}

// chromemMetadataToMap converts chromem's map[string]string to map[string]interface{}.
func chromemMetadataToMap(m map[string]string) map[string]interface{} {
	if m == nil {
		return make(map[string]interface{})
	}
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// buildChromemWhere converts Bifrost Query slice to chromem's map[string]string.
// chromem only supports exact metadata matches; other operators are handled via matchesQueries in memory.
func buildChromemWhere(queries []Query) map[string]string {
	if len(queries) == 0 {
		return nil
	}
	where := make(map[string]string)
	for _, q := range queries {
		if q.Operator == QueryOperatorEqual && q.Value != nil {
			where[q.Field] = fmt.Sprintf("%v", q.Value)
		}
	}
	if len(where) == 0 {
		return nil
	}
	return where
}

// filterPropertiesChromem filters properties to only include selectFields.
func filterPropertiesChromem(props map[string]interface{}, selectFields []string) map[string]interface{} {
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

// newChromemStore creates a new chromem-go vector store.
func newChromemStore(ctx context.Context, config *ChromemConfig, logger schemas.Logger) (*ChromemStore, error) {
	var db *chromem.DB
	if config.PersistDirectory != nil && strings.TrimSpace(config.PersistDirectory.GetValue()) != "" {
		var err error
		db, err = chromem.NewPersistentDB(config.PersistDirectory.GetValue(), config.Compress)
		if err != nil {
			return nil, fmt.Errorf("failed to create persistent chromem db: %w", err)
		}
	} else {
		db = chromem.NewDB()
	}

	return &ChromemStore{
		db:     db,
		config: config,
		logger: logger,
	}, nil
}
