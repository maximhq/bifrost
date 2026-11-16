package vectorstore

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	chromem "github.com/philippgille/chromem-go"
)

// ChromemConfig represents the configuration for the embedded chromem vector store.
// Chromem runs in-process (pure Go, no external service). With an empty Path the
// store is memory-only; with a Path set, documents are persisted to disk and
// reloaded on startup.
type ChromemConfig struct {
	Path     string `json:"path,omitempty"`
	Compress bool   `json:"compress,omitempty"`
}

// ChromemStore is an embedded VectorStore backed by chromem-go.
//
// Chromem has no document-enumeration API and no typed metadata, so this
// backend bridges both gaps itself:
//   - metadata values are JSON-encoded into chromem's map[string]string and
//     decoded back to typed values on read;
//   - GetAll/DeleteAll/GetNearest enumerate via an exhaustive QueryEmbedding
//     with a probe vector, which requires the namespace dimension. Like the
//     Pinecone backend, the dimension is recorded in memory by CreateNamespace,
//     so callers must CreateNamespace after every process start (all current
//     callers already do);
//   - a paged GetAll enumerates once, on its first page, and later pages read
//     only their own documents (see chromemPageSnapshot).
type ChromemStore struct {
	db     *chromem.DB
	config *ChromemConfig
	logger schemas.Logger

	mu                  sync.RWMutex
	namespaceDimensions map[string]int

	snapshotsMu sync.Mutex
	snapshots   map[string]*chromemPageSnapshot
}

// chromemPageSnapshot is the sorted ids a paged GetAll matched on its first
// page. Enumerating is an exhaustive similarity query plus a metadata decode
// and a sort over the whole namespace, so doing it on every page made reading
// a namespace of N documents cost N squared; the cursor names the snapshot
// instead, and a later page fetches only its own documents by id.
//
// It holds the ids alone - string headers sharing chromem's own id bytes, 16
// bytes a document - and is dropped when its last page is served, when it has
// sat unused for chromemSnapshotTTL, or when chromemMaxSnapshots newer ones
// push it out.
type chromemPageSnapshot struct {
	namespace string
	ids       []string
	lastUsed  time.Time
}

const (
	chromemSnapshotTTL  = 10 * time.Minute
	chromemMaxSnapshots = 8
)

// chromemStubEmbeddingFunc is passed to chromem collection constructors.
// All entries carry precomputed vectors (RequiresVectors is true), so chromem
// should never need to embed on our behalf.
func chromemStubEmbeddingFunc(ctx context.Context, text string) ([]float32, error) {
	return nil, fmt.Errorf("chromem backend requires precomputed embeddings")
}

func newChromemStore(_ context.Context, config *ChromemConfig, logger schemas.Logger) (*ChromemStore, error) {
	if config == nil {
		return nil, fmt.Errorf("chromem config is required")
	}

	var db *chromem.DB
	var err error
	if strings.TrimSpace(config.Path) != "" {
		db, err = chromem.NewPersistentDB(config.Path, config.Compress)
		if err != nil {
			return nil, fmt.Errorf("failed to open chromem persistent db at %s: %w", config.Path, err)
		}
	} else {
		db = chromem.NewDB()
	}

	return &ChromemStore{
		db:                  db,
		config:              config,
		logger:              logger,
		namespaceDimensions: make(map[string]int),
		snapshots:           make(map[string]*chromemPageSnapshot),
	}, nil
}

// Ping checks the health of the store. Chromem is in-process, so this only
// verifies the store was initialized.
func (s *ChromemStore) Ping(ctx context.Context) error {
	if s.db == nil {
		return fmt.Errorf("chromem db is not initialized")
	}
	return nil
}

// maxChromemDimension bounds the probe vector CreateNamespace allocates. Embedding models
// in use top out at a few thousand dimensions, so this only rejects a corrupt config.
const maxChromemDimension = 65536

// CreateNamespace creates a collection if it does not exist and records its
// dimension. Idempotent: callers invoke it on every startup.
func (s *ChromemStore) CreateNamespace(ctx context.Context, namespace string, dimension int, properties map[string]VectorStoreProperties) error {
	if strings.TrimSpace(namespace) == "" {
		return fmt.Errorf("namespace is required")
	}
	if dimension <= 0 {
		return fmt.Errorf("dimension must be positive, got %d", dimension)
	}
	if dimension > maxChromemDimension {
		return fmt.Errorf("dimension must be at most %d, got %d", maxChromemDimension, dimension)
	}

	col, err := s.db.GetOrCreateCollection(namespace, nil, chromemStubEmbeddingFunc)
	if err != nil {
		return fmt.Errorf("failed to create chromem collection %s: %w", namespace, err)
	}

	// Refuse to reopen a collection at a dimension other than the one its
	// vectors already have, as every store does. chromem records no dimension
	// and has no listing call, so one query at the requested size is the
	// probe: chromem compares it against every stored vector and fails on a
	// length difference. An empty collection has nothing to compare and passes.
	if col.Count() > 0 {
		probe := make([]float32, dimension)
		probe[0] = 1
		if _, err := col.QueryEmbedding(ctx, probe, 1, nil, nil); err != nil {
			if strings.Contains(err.Error(), "same length") {
				return dimensionMismatchError(namespace, 0, dimension, "collection")
			}
			return fmt.Errorf("failed to validate chromem collection %s dimension: %w", namespace, err)
		}
	}

	s.mu.Lock()
	s.namespaceDimensions[namespace] = dimension
	s.mu.Unlock()
	return nil
}

// ListNamespaces returns the collections chromem currently holds. A persistent
// chromem database reloads its collections at open, so this reflects what is on
// disk as well as what this process created.
func (s *ChromemStore) ListNamespaces(ctx context.Context, prefix string) ([]string, error) {
	if s.db == nil {
		return nil, fmt.Errorf("chromem db is not initialized")
	}
	collections := s.db.ListCollections()
	namespaces := make([]string, 0, len(collections))
	for name := range collections {
		if strings.HasPrefix(name, prefix) {
			namespaces = append(namespaces, name)
		}
	}
	sort.Strings(namespaces)
	return namespaces, nil
}

// DeleteNamespace removes a collection. Idempotent: deleting a missing
// namespace is not an error.
func (s *ChromemStore) DeleteNamespace(ctx context.Context, namespace string) error {
	if s.db.GetCollection(namespace, chromemStubEmbeddingFunc) == nil {
		return nil
	}
	if err := s.db.DeleteCollection(namespace); err != nil {
		return fmt.Errorf("failed to delete chromem collection %s: %w", namespace, err)
	}
	s.mu.Lock()
	delete(s.namespaceDimensions, namespace)
	s.mu.Unlock()
	return nil
}

// GetChunk retrieves a single document by ID.
func (s *ChromemStore) GetChunk(ctx context.Context, namespace string, id string) (SearchResult, error) {
	if strings.TrimSpace(id) == "" {
		return SearchResult{}, fmt.Errorf("id is required")
	}
	col := s.db.GetCollection(namespace, chromemStubEmbeddingFunc)
	if col == nil {
		return SearchResult{}, fmt.Errorf("%w: namespace %s", ErrNotFound, namespace)
	}
	doc, err := col.GetByID(ctx, id)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return SearchResult{
		ID:         doc.ID,
		Properties: decodeChromemMetadata(doc.Metadata),
	}, nil
}

// GetChunks retrieves multiple documents by ID. Missing IDs are skipped,
// matching the behavior of the other backends.
func (s *ChromemStore) GetChunks(ctx context.Context, namespace string, ids []string) ([]SearchResult, error) {
	if len(ids) == 0 {
		return []SearchResult{}, nil
	}
	col := s.db.GetCollection(namespace, chromemStubEmbeddingFunc)
	if col == nil {
		return nil, fmt.Errorf("%w: namespace %s", ErrNotFound, namespace)
	}
	results := make([]SearchResult, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		doc, err := col.GetByID(ctx, id)
		if err != nil {
			s.logger.Debug(fmt.Sprintf("chromem: skipping missing id %s", id))
			continue
		}
		results = append(results, SearchResult{
			ID:         doc.ID,
			Properties: decodeChromemMetadata(doc.Metadata),
		})
	}
	return results, nil
}

// GetAll retrieves documents with optional filtering, using an offset cursor
// for pagination. Results are ordered by ID for deterministic paging.
//
// The cursor is "<snapshot>:<offset>". A snapshot that has expired, or a bare
// offset, is rebuilt from the namespace as it is now and read from the same
// offset, which is what every page cost before snapshots. A document deleted,
// or changed so it no longer matches, after the first page is skipped, and
// the page is filled from further on so it is never short before the end.
func (s *ChromemStore) GetAll(ctx context.Context, namespace string, queries []Query, selectFields []string, cursor *string, limit int64) ([]SearchResult, *string, error) {
	token, offset, err := parseChromemCursor(cursor)
	if err != nil {
		return nil, nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	col := s.db.GetCollection(namespace, chromemStubEmbeddingFunc)
	if col == nil {
		return nil, nil, fmt.Errorf("%w: namespace %s", ErrNotFound, namespace)
	}

	ids := s.takeSnapshot(token, namespace)
	if ids == nil {
		docs, err := s.enumerateAll(ctx, namespace)
		if err != nil {
			return nil, nil, err
		}
		ids = make([]string, 0, len(docs))
		for _, doc := range docs {
			if matchesChromemQueries(decodeChromemMetadata(doc.Metadata), queries) {
				ids = append(ids, doc.ID)
			}
		}
		sort.Strings(ids)
		token = ""
	}

	total := int64(len(ids))
	if offset >= total {
		s.dropSnapshot(token)
		return []SearchResult{}, nil, nil
	}
	includeVectors := IncludeVectorsRequested(ctx)
	// Compared against what is left rather than forming offset+limit: limit is
	// caller-supplied and unbounded, so the sum would overflow int64.
	capacity := limit
	if remaining := total - offset; remaining < capacity {
		capacity = remaining
	}
	page := make([]SearchResult, 0, capacity)
	next := offset
	for ; next < total && int64(len(page)) < limit; next++ {
		// GetByID hands back its own copy of the vector, so nothing a caller
		// does to the page reaches the collection.
		doc, err := col.GetByID(ctx, ids[next])
		if err != nil {
			continue
		}
		props := decodeChromemMetadata(doc.Metadata)
		if !matchesChromemQueries(props, queries) {
			continue
		}
		result := SearchResult{ID: doc.ID, Properties: applyChromemSelectFields(props, selectFields)}
		if includeVectors {
			result.Vector = doc.Embedding
		}
		page = append(page, result)
	}

	if next >= total {
		s.dropSnapshot(token)
		return page, nil, nil
	}
	if token == "" {
		token = s.saveSnapshot(namespace, ids)
	}
	nextCursor := token + ":" + strconv.FormatInt(next, 10)
	return page, &nextCursor, nil
}

// parseChromemCursor splits a GetAll cursor into its snapshot and offset. A
// bare offset has no snapshot.
func parseChromemCursor(cursor *string) (string, int64, error) {
	if cursor == nil || *cursor == "" {
		return "", 0, nil
	}
	token, rawOffset, found := strings.Cut(*cursor, ":")
	if !found {
		token, rawOffset = "", *cursor
	}
	offset, err := strconv.ParseInt(rawOffset, 10, 64)
	if err != nil || offset < 0 {
		return "", 0, fmt.Errorf("%w: invalid cursor %q", ErrQuerySyntax, *cursor)
	}
	return token, offset, nil
}

// takeSnapshot returns the ids a snapshot holds and marks it used, or nil when
// there is no live snapshot by that token for this namespace.
func (s *ChromemStore) takeSnapshot(token, namespace string) []string {
	if token == "" {
		return nil
	}
	s.snapshotsMu.Lock()
	defer s.snapshotsMu.Unlock()
	snapshot, ok := s.snapshots[token]
	if !ok || snapshot.namespace != namespace {
		return nil
	}
	now := time.Now()
	if now.Sub(snapshot.lastUsed) > chromemSnapshotTTL {
		delete(s.snapshots, token)
		return nil
	}
	snapshot.lastUsed = now
	return snapshot.ids
}

// saveSnapshot keeps ids for the pages after the first and returns its token,
// first making room: expired snapshots go, then the least recently used.
func (s *ChromemStore) saveSnapshot(namespace string, ids []string) string {
	token := uuid.NewString()
	now := time.Now()
	s.snapshotsMu.Lock()
	defer s.snapshotsMu.Unlock()
	for key, snapshot := range s.snapshots {
		if now.Sub(snapshot.lastUsed) > chromemSnapshotTTL {
			delete(s.snapshots, key)
		}
	}
	for len(s.snapshots) >= chromemMaxSnapshots {
		oldest := ""
		for key, snapshot := range s.snapshots {
			if oldest == "" || snapshot.lastUsed.Before(s.snapshots[oldest].lastUsed) {
				oldest = key
			}
		}
		delete(s.snapshots, oldest)
	}
	s.snapshots[token] = &chromemPageSnapshot{namespace: namespace, ids: ids, lastUsed: now}
	return token
}

// dropSnapshot forgets a snapshot whose last page has been served.
func (s *ChromemStore) dropSnapshot(token string) {
	if token == "" {
		return
	}
	s.snapshotsMu.Lock()
	delete(s.snapshots, token)
	s.snapshotsMu.Unlock()
}

// GetNearest performs an exhaustive cosine-similarity search.
func (s *ChromemStore) GetNearest(ctx context.Context, namespace string, vector []float32, queries []Query, selectFields []string, threshold float64, limit int64) ([]SearchResult, error) {
	if len(vector) == 0 {
		return nil, fmt.Errorf("vector is required")
	}
	col := s.db.GetCollection(namespace, chromemStubEmbeddingFunc)
	if col == nil {
		return nil, fmt.Errorf("%w: namespace %s", ErrNotFound, namespace)
	}
	// Query everything, then filter/threshold/limit here: chromem's own where
	// filter is equality-only over encoded strings, so pushing filters down
	// would change semantics.
	results, err := chromemQueryAll(ctx, col, vector)
	if err != nil {
		return nil, fmt.Errorf("chromem query failed: %w", err)
	}
	if len(results) == 0 {
		return []SearchResult{}, nil
	}

	if limit <= 0 {
		limit = 10
	}
	// Size the preallocation by what the search can actually return, not by the
	// caller's limit: nothing bounds limit, and an oversized one would reserve —
	// or on an extreme value panic trying to reserve — memory no result set could
	// ever fill. The loop below still honors limit itself. Every other backend
	// sizes its result slice on its own result count for the same reason.
	out := make([]SearchResult, 0, min(int64(len(results)), limit))
	for _, res := range results {
		if float64(res.Similarity) < threshold {
			// Results are ranked by similarity; everything after is below threshold too.
			break
		}
		props := decodeChromemMetadata(res.Metadata)
		if !matchesChromemQueries(props, queries) {
			continue
		}
		score := float64(res.Similarity)
		out = append(out, SearchResult{
			ID:         res.ID,
			Score:      &score,
			Properties: applyChromemSelectFields(props, selectFields),
		})
		if int64(len(out)) >= limit {
			break
		}
	}
	return out, nil
}

// RequiresVectors returns true: chromem entries always carry vectors.
func (s *ChromemStore) RequiresVectors() bool {
	return true
}

// Add upserts a document with a precomputed embedding.
func (s *ChromemStore) Add(ctx context.Context, namespace string, id string, embedding []float32, metadata map[string]interface{}) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}
	if len(embedding) == 0 {
		return fmt.Errorf("embedding is required")
	}
	s.mu.RLock()
	dimension, hasDimension := s.namespaceDimensions[namespace]
	s.mu.RUnlock()
	if hasDimension && len(embedding) != dimension {
		return fmt.Errorf("embedding dimension %d does not match namespace dimension %d", len(embedding), dimension)
	}

	col, err := s.db.GetOrCreateCollection(namespace, nil, chromemStubEmbeddingFunc)
	if err != nil {
		return fmt.Errorf("failed to get chromem collection %s: %w", namespace, err)
	}
	encoded, err := encodeChromemMetadata(metadata)
	if err != nil {
		return err
	}
	if err := col.AddDocument(ctx, chromem.Document{
		ID:        id,
		Metadata:  encoded,
		Embedding: embedding,
	}); err != nil {
		return fmt.Errorf("failed to add document %s: %w", id, err)
	}
	return nil
}

// Delete removes a single document. Deleting a missing document is not an error.
func (s *ChromemStore) Delete(ctx context.Context, namespace string, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}
	col := s.db.GetCollection(namespace, chromemStubEmbeddingFunc)
	if col == nil {
		return nil
	}
	if err := col.Delete(ctx, nil, nil, id); err != nil {
		return fmt.Errorf("failed to delete document %s: %w", id, err)
	}
	return nil
}

// DeleteAll removes all documents matching the queries (all documents when no
// queries are given) and reports per-document status.
func (s *ChromemStore) DeleteAll(ctx context.Context, namespace string, queries []Query) ([]DeleteResult, error) {
	docs, err := s.enumerateAll(ctx, namespace)
	if err != nil {
		return nil, err
	}
	// enumerateAll already rejected a missing namespace, so this can only be nil
	// if a concurrent DeleteNamespace dropped the collection in between; there is
	// then nothing left to delete.
	col := s.db.GetCollection(namespace, chromemStubEmbeddingFunc)
	if col == nil {
		return []DeleteResult{}, nil
	}

	matched := make([]string, 0, len(docs))
	for _, doc := range docs {
		if matchesChromemQueries(decodeChromemMetadata(doc.Metadata), queries) {
			matched = append(matched, doc.ID)
		}
	}
	// Never call chromem Delete with zero ids: chromem treats that as
	// "delete everything in the collection".
	if len(matched) == 0 {
		return []DeleteResult{}, nil
	}

	results := make([]DeleteResult, 0, len(matched))
	for _, id := range matched {
		if err := col.Delete(ctx, nil, nil, id); err != nil {
			results = append(results, DeleteResult{ID: id, Status: DeleteStatusError, Error: err.Error()})
			continue
		}
		results = append(results, DeleteResult{ID: id, Status: DeleteStatusSuccess})
	}
	return results, nil
}

// Close is a no-op: chromem persists synchronously on each write.
func (s *ChromemStore) Close(ctx context.Context, namespace string) error {
	return nil
}

// enumerateAll returns every document in the namespace. Chromem has no listing
// API, so this runs an exhaustive query with a probe vector and
// nResults == Count(). Requires the namespace dimension recorded by
// CreateNamespace.
//
// A missing namespace is ErrNotFound, matching GetChunk/GetChunks/GetNearest:
// "the namespace does not exist" must not read back as "the namespace is
// empty". An existing but empty collection still returns nil results.
func (s *ChromemStore) enumerateAll(ctx context.Context, namespace string) ([]chromem.Result, error) {
	col := s.db.GetCollection(namespace, chromemStubEmbeddingFunc)
	if col == nil {
		return nil, fmt.Errorf("%w: namespace %s", ErrNotFound, namespace)
	}
	if col.Count() == 0 {
		return nil, nil
	}

	s.mu.RLock()
	dimension, ok := s.namespaceDimensions[namespace]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("dimension not set for namespace %s: call CreateNamespace first", namespace)
	}

	probe := make([]float32, dimension)
	probe[0] = 1
	results, err := chromemQueryAll(ctx, col, probe)
	if err != nil {
		return nil, fmt.Errorf("chromem enumeration failed: %w", err)
	}
	return results, nil
}

// chromemQueryAll runs an exhaustive QueryEmbedding with nResults == Count().
// Chromem rejects nResults larger than the collection, so if a concurrent
// delete shrinks the collection between the count and the query, the query is
// retried with the refreshed count. Returns nil results for an empty collection.
func chromemQueryAll(ctx context.Context, col *chromem.Collection, vector []float32) ([]chromem.Result, error) {
	count := col.Count()
	for {
		if count == 0 {
			return nil, nil
		}
		results, err := col.QueryEmbedding(ctx, vector, count, nil, nil)
		if err == nil {
			return results, nil
		}
		if refreshed := col.Count(); refreshed < count {
			count = refreshed
			continue
		}
		return nil, err
	}
}

// encodeChromemMetadata JSON-encodes each typed value into chromem's
// map[string]string metadata.
func encodeChromemMetadata(metadata map[string]interface{}) (map[string]string, error) {
	if len(metadata) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(metadata))
	for key, value := range metadata {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("failed to encode metadata field %s: %w", key, err)
		}
		out[key] = string(encoded)
	}
	return out, nil
}

// decodeChromemMetadata reverses encodeChromemMetadata. Values that fail to
// decode are returned as raw strings.
func decodeChromemMetadata(metadata map[string]string) map[string]interface{} {
	if len(metadata) == 0 {
		return map[string]interface{}{}
	}
	props := make(map[string]interface{}, len(metadata))
	for key, raw := range metadata {
		props[key] = decodeChromemValue(raw)
	}
	return props
}

func decodeChromemValue(raw string) interface{} {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value interface{}
	if err := decoder.Decode(&value); err != nil {
		return raw
	}
	return normalizeChromemDecoded(value)
}

// normalizeChromemDecoded maps decoded JSON onto the property types the
// interface declares: integral numbers become int64, other numbers float64,
// and all-string arrays become []string.
func normalizeChromemDecoded(value interface{}) interface{} {
	switch v := value.(type) {
	case json.Number:
		if i, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			return i
		}
		f, err := v.Float64()
		if err != nil {
			return string(v)
		}
		return f
	case []interface{}:
		allStrings := true
		for i, elem := range v {
			v[i] = normalizeChromemDecoded(elem)
			if _, ok := v[i].(string); !ok {
				allStrings = false
			}
		}
		if allStrings {
			out := make([]string, len(v))
			for i, elem := range v {
				out[i] = elem.(string)
			}
			return out
		}
		return v
	default:
		return value
	}
}

func applyChromemSelectFields(props map[string]interface{}, selectFields []string) map[string]interface{} {
	if len(selectFields) == 0 {
		return props
	}
	out := make(map[string]interface{}, len(selectFields))
	for _, field := range selectFields {
		if value, ok := props[field]; ok {
			out[field] = value
		}
	}
	return out
}

func matchesChromemQueries(props map[string]interface{}, queries []Query) bool {
	for _, query := range queries {
		if !matchesChromemQuery(props, query) {
			return false
		}
	}
	return true
}

func matchesChromemQuery(props map[string]interface{}, query Query) bool {
	value, exists := props[query.Field]

	switch query.Operator {
	case QueryOperatorIsNull:
		return !exists || value == nil
	case QueryOperatorIsNotNull:
		return exists && value != nil
	}
	if !exists || value == nil {
		return false
	}

	switch query.Operator {
	case QueryOperatorEqual:
		cmp, ok := compareChromemValues(value, query.Value)
		return ok && cmp == 0
	case QueryOperatorNotEqual:
		cmp, ok := compareChromemValues(value, query.Value)
		// Incomparable values (mismatched types, nil query value) are not equal.
		return !ok || cmp != 0
	case QueryOperatorGreaterThan:
		cmp, ok := compareChromemValues(value, query.Value)
		return ok && cmp > 0
	case QueryOperatorLessThan:
		cmp, ok := compareChromemValues(value, query.Value)
		return ok && cmp < 0
	case QueryOperatorGreaterThanOrEqual:
		cmp, ok := compareChromemValues(value, query.Value)
		return ok && cmp >= 0
	case QueryOperatorLessThanOrEqual:
		cmp, ok := compareChromemValues(value, query.Value)
		return ok && cmp <= 0
	case QueryOperatorLike:
		text, textOK := value.(string)
		pattern, patternOK := query.Value.(string)
		return textOK && patternOK && matchesChromemLike(text, pattern)
	case QueryOperatorContainsAny:
		return chromemContains(value, query.Value, false)
	case QueryOperatorContainsAll:
		return chromemContains(value, query.Value, true)
	default:
		return false
	}
}

// compareChromemValues compares a stored value against a query value,
// tolerating numeric type mismatches (int64 vs float64 vs int).
func compareChromemValues(stored, queried interface{}) (int, bool) {
	if storedNum, ok := chromemToFloat(stored); ok {
		if queriedNum, ok := chromemToFloat(queried); ok {
			switch {
			case storedNum < queriedNum:
				return -1, true
			case storedNum > queriedNum:
				return 1, true
			default:
				return 0, true
			}
		}
		return 0, false
	}
	if storedStr, ok := stored.(string); ok {
		if queriedStr, ok := queried.(string); ok {
			return strings.Compare(storedStr, queriedStr), true
		}
		return 0, false
	}
	if storedBool, ok := stored.(bool); ok {
		if queriedBool, ok := queried.(bool); ok {
			if storedBool == queriedBool {
				return 0, true
			}
			return 1, true
		}
	}
	return 0, false
}

func chromemToFloat(value interface{}) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	default:
		return 0, false
	}
}

// matchesChromemLike supports SQL-style % and glob-style * wildcards; a
// pattern without wildcards matches as a substring.
func matchesChromemLike(text, pattern string) bool {
	if !strings.ContainsAny(pattern, "%*") {
		return strings.Contains(text, pattern)
	}
	var builder strings.Builder
	builder.WriteString("^")
	for _, part := range strings.FieldsFunc(pattern, func(r rune) bool { return r == '%' || r == '*' }) {
		builder.WriteString(regexp.QuoteMeta(part))
		builder.WriteString(".*")
	}
	expr := builder.String()
	if !strings.HasSuffix(pattern, "%") && !strings.HasSuffix(pattern, "*") {
		expr = strings.TrimSuffix(expr, ".*") + "$"
	}
	if strings.HasPrefix(pattern, "%") || strings.HasPrefix(pattern, "*") {
		expr = "^.*" + strings.TrimPrefix(expr, "^")
	}
	matched, err := regexp.MatchString(expr, text)
	return err == nil && matched
}

// chromemContains implements ContainsAny/ContainsAll. The stored value may be
// an array or a scalar; the query value may be a slice or a single value.
func chromemContains(stored, queried interface{}, requireAll bool) bool {
	queryValues := chromemToSlice(queried)
	if len(queryValues) == 0 {
		return false
	}
	storedValues := chromemToSlice(stored)
	for _, queryValue := range queryValues {
		found := false
		for _, storedValue := range storedValues {
			if cmp, ok := compareChromemValues(storedValue, queryValue); ok && cmp == 0 {
				found = true
				break
			}
		}
		if requireAll && !found {
			return false
		}
		if !requireAll && found {
			return true
		}
	}
	return requireAll
}

func chromemToSlice(value interface{}) []interface{} {
	switch v := value.(type) {
	case []interface{}:
		return v
	case []string:
		out := make([]interface{}, len(v))
		for i, s := range v {
			out[i] = s
		}
		return out
	default:
		return []interface{}{value}
	}
}
