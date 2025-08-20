package vectorstore

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/redis/go-redis/v9"
)

type RedisConfig struct {
	// Connection settings
	Addr     string `json:"addr"`               // Redis server address (host:port) - REQUIRED
	Username string `json:"username,omitempty"` // Username for Redis AUTH (optional)
	Password string `json:"password,omitempty"` // Password for Redis AUTH (optional)
	DB       int    `json:"db,omitempty"`       // Redis database number (default: 0)

	// Connection pool and timeout settings (passed directly to Redis client)
	PoolSize        int           `json:"pool_size,omitempty"`          // Maximum number of socket connections (optional)
	MinIdleConns    int           `json:"min_idle_conns,omitempty"`     // Minimum number of idle connections (optional)
	MaxIdleConns    int           `json:"max_idle_conns,omitempty"`     // Maximum number of idle connections (optional)
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime,omitempty"`  // Connection maximum lifetime (optional)
	ConnMaxIdleTime time.Duration `json:"conn_max_idle_time,omitempty"` // Connection maximum idle time (optional)
	DialTimeout     time.Duration `json:"dial_timeout,omitempty"`       // Timeout for socket connection (optional)
	ReadTimeout     time.Duration `json:"read_timeout,omitempty"`       // Timeout for socket reads (optional)
	WriteTimeout    time.Duration `json:"write_timeout,omitempty"`      // Timeout for socket writes (optional)
	ContextTimeout  time.Duration `json:"context_timeout,omitempty"`    // Timeout for Redis operations (optional)
}

// RedisStore represents the Redis vector store.
type RedisStore struct {
	client *redis.Client
	config RedisConfig
	logger schemas.Logger
}

// withTimeout adds a timeout to the context if it is set.
func (s *RedisStore) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.config.ContextTimeout > 0 {
		return context.WithTimeout(ctx, s.config.ContextTimeout)
	}
	// No-op cancel to simplify call sites.
	return ctx, func() {}
}

func (s *RedisStore) GetChunk(ctx context.Context, contextKey string) (string, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	val, err := s.client.Get(ctx, contextKey).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrNotFound
	}
	return val, err
}

// GetChunks retrieves a value from Redis.
func (s *RedisStore) GetChunks(ctx context.Context, chunkKeys []string) ([]any, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	vals, err := s.client.MGet(ctx, chunkKeys...).Result()
	if err != nil {
		return nil, err
	}
	return vals, nil
}

// Add adds a value to Redis.
func (s *RedisStore) Add(ctx context.Context, key string, value string, ttl time.Duration) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return s.client.Set(ctx, key, value, ttl).Err()
}

// Delete deletes a value from Redis.
func (s *RedisStore) Delete(ctx context.Context, keys []string) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return s.client.Del(ctx, keys...).Err()
}

// GetAll retrieves all keys matching a pattern from Redis.
func (s *RedisStore) GetAll(ctx context.Context, pattern string, cursor *string, count int64) ([]string, *string, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	var err error
	var redisCursor uint64
	if cursor != nil {
		redisCursor, err = strconv.ParseUint(*cursor, 10, 64)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("invalid cursor value: %w", err)
	}
	keys, c, err := s.client.Scan(ctx, redisCursor, pattern, count).Result()
	var nextCursor *string
	if c == 0 {
		nextCursor = nil
	} else {
		nxCursor := strconv.FormatUint(c, 10)
		nextCursor = &nxCursor
	}
	return keys, nextCursor, err
}

// Close closes the Redis connection.
func (s *RedisStore) Close(_ context.Context) error {
	return s.client.Close()
}

// Binary embedding conversion helpers
func float32SliceToBytes(floats []float32) []byte {
	bytes := make([]byte, len(floats)*4)
	for i, f := range floats {
		binary.LittleEndian.PutUint32(bytes[i*4:], math.Float32bits(f))
	}
	return bytes
}

func bytesToFloat32Slice(bytes []byte) []float32 {
	floats := make([]float32, len(bytes)/4)
	for i := 0; i < len(floats); i++ {
		bits := binary.LittleEndian.Uint32(bytes[i*4:])
		floats[i] = math.Float32frombits(bits)
	}
	return floats
}

// AddSemanticCache stores an embedding with metadata in a single Redis hash, optimized for native vector search.
func (s *RedisStore) AddSemanticCache(ctx context.Context, key string, embedding []float32, metadata map[string]interface{}, ttl time.Duration) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	// Convert embedding to binary format for native vector indexing
	embeddingBytes := float32SliceToBytes(embedding)

	// Prepare hash fields: binary embedding + metadata
	fields := make(map[string]interface{})
	fields["embedding"] = embeddingBytes

	// Add metadata fields directly (no prefix needed with proper indexing)
	for k, v := range metadata {
		switch val := v.(type) {
		case string:
			fields[k] = val
		case int, int64, float64, bool:
			fields[k] = fmt.Sprintf("%v", val)
		default:
			// JSON encode complex types
			jsonData, err := json.Marshal(val)
			if err != nil {
				return fmt.Errorf("failed to marshal metadata field %s: %w", k, err)
			}
			fields[k] = string(jsonData)
		}
	}

	// Store as hash for efficient native vector search
	if err := s.client.HMSet(ctx, key, fields).Err(); err != nil {
		return fmt.Errorf("failed to store semantic cache entry: %w", err)
	}

	// Set TTL if specified
	if ttl > 0 {
		if err := s.client.Expire(ctx, key, ttl).Err(); err != nil {
			return fmt.Errorf("failed to set TTL: %w", err)
		}
	}

	return nil
}

// SearchSemanticCache performs native vector similarity search with metadata filtering using RediSearch.
func (s *RedisStore) SearchSemanticCache(ctx context.Context, indexName string, queryEmbedding []float32, metadata map[string]interface{}, threshold float64, limit int64) ([]SearchResult, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	// Build metadata filter query with correct TEXT field syntax
	var metadataQuery string
	if len(metadata) > 0 {
		var conditions []string
		for k, v := range metadata {
			// Convert to string consistently with storage
			var stringValue string
			switch val := v.(type) {
			case string:
				stringValue = val
			case int, int64, float64, bool:
				stringValue = fmt.Sprintf("%v", val)
			default:
				// JSON encode complex types (same as storage)
				jsonData, _ := json.Marshal(val)
				stringValue = string(jsonData)
			}

			// For TEXT fields, use exact string matching syntax
			escapedValue := s.escapeSearchValue(stringValue)
			condition := fmt.Sprintf("@%s:\"%s\"", k, escapedValue)
			conditions = append(conditions, condition)
		}
		metadataQuery = strings.Join(conditions, " ")
	} else {
		metadataQuery = "*"
	}

	// Convert query embedding to binary format
	queryBytes := float32SliceToBytes(queryEmbedding)

	// Build hybrid FT.SEARCH query: metadata filters + KNN vector search
	// The correct syntax is: (metadata_filter)=>[KNN k @embedding $vec AS score]
	var hybridQuery string
	if len(metadata) > 0 {
		// Wrap metadata query in parentheses for hybrid syntax
		hybridQuery = fmt.Sprintf("(%s)", metadataQuery)
	} else {
		// Wildcard for pure vector search
		hybridQuery = "*"
	}

	// Execute FT.SEARCH with KNN
	args := []interface{}{
		"FT.SEARCH", indexName,
		fmt.Sprintf("%s=>[KNN %d @embedding $vec AS score]", hybridQuery, limit),
		"PARAMS", "2", "vec", queryBytes,
		"SORTBY", "score",
		"RETURN", "1", "score",
		"DIALECT", "2",
	}

	result := s.client.Do(ctx, args...)
	if result.Err() != nil {
		return nil, fmt.Errorf("native vector search failed: %w", result.Err())
	}

	return s.parseSemanticSearchResults(result.Val(), threshold)
}

// EnsureSemanticIndex creates a RediSearch index with native VECTOR field support.
func (s *RedisStore) EnsureSemanticIndex(ctx context.Context, indexName string, keyPrefix string, embeddingDim int, metadataFields []string) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	// Check if index exists
	infoResult := s.client.Do(ctx, "FT.INFO", indexName)
	if infoResult.Err() == nil {
		return nil
	}

	// Create index with VECTOR field + metadata fields
	args := []interface{}{
		"FT.CREATE", indexName,
		"ON", "HASH",
		"PREFIX", "1", keyPrefix,
		"SCHEMA",
		// Native vector field with HNSW algorithm
		"embedding", "VECTOR", "HNSW", "6",
		"TYPE", "FLOAT32",
		"DIM", embeddingDim,
		"DISTANCE_METRIC", "COSINE",
	}

	// Add all metadata fields as TEXT with exact matching
	// All values are converted to strings for consistent searching
	for _, field := range metadataFields {
		args = append(args, field, "TEXT", "NOSTEM")
	}

	// Create the index
	if err := s.client.Do(ctx, args...).Err(); err != nil {
		return fmt.Errorf("failed to create semantic vector index %s: %w", indexName, err)
	}

	return nil
}

// DropSemanticIndex deletes the semantic vector search index.
func (s *RedisStore) DropSemanticIndex(ctx context.Context, indexName string) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	// Drop the index using FT.DROPINDEX
	if err := s.client.Do(ctx, "FT.DROPINDEX", indexName).Err(); err != nil {
		// Check if error is "Unknown Index name" - that's OK, index doesn't exist
		if strings.Contains(err.Error(), "Unknown Index name") {
			return nil // Index doesn't exist, nothing to drop
		}
		return fmt.Errorf("failed to drop semantic index %s: %w", indexName, err)
	}

	return nil
}

// parseSemanticSearchResults parses the result from FT.SEARCH with vector similarity scores.
func (s *RedisStore) parseSemanticSearchResults(result interface{}, threshold float64) ([]SearchResult, error) {
	// RediSearch returns a map with results array (Redis client uses interface{} keys)
	resultMap, ok := result.(map[interface{}]interface{})
	if !ok {
		return []SearchResult{}, nil
	}

	// Get total count
	totalCount, ok := resultMap["total_results"].(int64)
	if !ok {
		return nil, fmt.Errorf("invalid search result format: expected total_results")
	}

	if totalCount == 0 {
		return []SearchResult{}, nil
	}

	// Get results array
	resultsArray, ok := resultMap["results"].([]interface{})
	if !ok {
		return []SearchResult{}, nil
	}

	var results []SearchResult

	// Parse each result
	for _, resultItem := range resultsArray {
		resultItemMap, ok := resultItem.(map[interface{}]interface{})
		if !ok {
			continue
		}

		key, ok := resultItemMap["id"].(string)
		if !ok {
			continue
		}

		// Get extra_attributes which contains the score
		extraAttrs, ok := resultItemMap["extra_attributes"].(map[interface{}]interface{})
		if !ok {
			continue
		}

		result := SearchResult{
			Key:      key,
			Metadata: make(map[string]interface{}),
		}

		// Get score from extra_attributes (try both field names)
		var scoreValue interface{}
		var scoreOk bool
		if scoreValue, scoreOk = extraAttrs["__embedding_score"]; !scoreOk {
			scoreValue, scoreOk = extraAttrs["score"]
		}
		if scoreOk {
			var score float64
			switch v := scoreValue.(type) {
			case float64:
				score = v
			case string:
				if parsedScore, err := strconv.ParseFloat(v, 64); err == nil {
					score = parsedScore
				}
			}

			// Convert cosine distance to similarity: similarity = 1 - distance
			similarity := 1.0 - score
			result.Similarity = similarity
			result.Value = fmt.Sprintf("%.4f", similarity)
		}

		// Apply threshold filter (convert similarity threshold to distance threshold)
		if result.Similarity >= threshold {
			results = append(results, result)
		}
	}

	return results, nil
}

// escapeSearchValue escapes special characters in search values.
func (s *RedisStore) escapeSearchValue(value string) string {
	// Escape special RediSearch characters
	replacer := strings.NewReplacer(
		"(", "\\(",
		")", "\\)",
		"[", "\\[",
		"]", "\\]",
		"{", "\\{",
		"}", "\\}",
		"*", "\\*",
		"?", "\\?",
		"|", "\\|",
		"&", "\\&",
		"!", "\\!",
		"@", "\\@",
		"#", "\\#",
		"$", "\\$",
		"%", "\\%",
		"^", "\\^",
		"~", "\\~",
		"`", "\\`",
		"\"", "\\\"",
		"'", "\\'",
		" ", "\\ ",
	)
	return replacer.Replace(value)
}

// Removed manual cosineSimilarity - now using Redis native vector search

// newRedisStore creates a new Redis vector store.
func newRedisStore(ctx context.Context, config RedisConfig, logger schemas.Logger) (*RedisStore, error) {

	client := redis.NewClient(&redis.Options{
		Addr:            config.Addr,
		Username:        config.Username,
		Password:        config.Password,
		DB:              config.DB,
		MaxActiveConns:  config.PoolSize,
		MinIdleConns:    config.MinIdleConns,
		MaxIdleConns:    config.MaxIdleConns,
		ConnMaxLifetime: config.ConnMaxLifetime,
		ConnMaxIdleTime: config.ConnMaxIdleTime,
		DialTimeout:     config.DialTimeout,
		ReadTimeout:     config.ReadTimeout,
		WriteTimeout:    config.WriteTimeout,
	})

	// Test the connection with a reasonable timeout (not using ContextTimeout for initial connection)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	store := &RedisStore{
		client: client,
		config: config,
		logger: logger,
	}

	return store, nil
}
