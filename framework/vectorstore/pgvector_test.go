package vectorstore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	PgVectorTestTimeout     = 120 * time.Second
	PgVectorTestDefaultHost = "localhost"
	PgVectorTestDefaultPort = "5433"
	PgVectorTestDimension   = 8
)

// ---------------------------------------------------------------------------
// Unit tests (no database)
// ---------------------------------------------------------------------------

func TestPgVectorLiteral(t *testing.T) {
	literal, err := pgvectorLiteral([]float32{1, 0.5, -2.25})
	require.NoError(t, err)
	assert.Equal(t, "[1,0.5,-2.25]", literal)

	assert.Equal(t, []float32{1, 0.5, -2.25}, parsePgVectorLiteral(literal))
	assert.Nil(t, parsePgVectorLiteral("[1,oops]"))
	assert.Nil(t, parsePgVectorLiteral("[]"))

	_, err = pgvectorLiteral([]float32{1, float32(math.NaN())})
	assert.Error(t, err, "NaN must be rejected")
	_, err = pgvectorLiteral([]float32{float32(math.Inf(1))})
	assert.Error(t, err, "Inf must be rejected")
}

func TestPgVectorLiteralRoundTripKeepsPrecision(t *testing.T) {
	original := generateTestEmbedding(64)
	literal, err := pgvectorLiteral(original)
	require.NoError(t, err)
	assert.Equal(t, original, parsePgVectorLiteral(literal), "float32 values must survive the text round trip exactly")
}

func TestPgVectorBinaryCodec(t *testing.T) {
	vector := []float32{1, -0.5, 2.25}
	encoded, err := pgvectorEncodePlan{}.Encode(vector, nil)
	require.NoError(t, err)
	assert.Equal(t, []byte{
		0x00, 0x03, // dimension
		0x00, 0x00, // reserved, always 0
		0x3f, 0x80, 0x00, 0x00, // 1
		0xbf, 0x00, 0x00, 0x00, // -0.5
		0x40, 0x10, 0x00, 0x00, // 2.25
	}, encoded, "pgvector's binary format: int16 dim, int16 0, big-endian float4s")

	var decoded []float32
	require.NoError(t, pgvectorScanPlan{}.Scan(encoded, &decoded))
	assert.Equal(t, vector, decoded)
	require.NoError(t, pgvectorScanPlan{}.Scan(nil, &decoded))
	assert.Nil(t, decoded, "NULL scans to a nil vector")

	_, err = decodePgVectorBinary([]byte{0x00, 0x02, 0x00, 0x00, 0x3f, 0x80, 0x00, 0x00})
	assert.Error(t, err, "a truncated value is rejected")
	_, err = decodePgVectorBinary([]byte{0x00})
	assert.Error(t, err)

	codec := pgvectorCodec{}
	assert.Nil(t, codec.PlanEncode(nil, 0, pgtype.TextFormatCode, vector), "text goes through the literal path")
	assert.Nil(t, codec.PlanEncode(nil, 0, pgtype.BinaryFormatCode, "not a vector"))
	assert.NotNil(t, codec.PlanEncode(nil, 0, pgtype.BinaryFormatCode, vector))
}

func TestValidatePgVectorNamespace(t *testing.T) {
	for _, valid := range []string{"BifrostSemanticCachePlugin", "cache_1", "_x", strings.Repeat("a", pgvectorMaxNamespaceLen)} {
		assert.NoError(t, validatePgVectorNamespace(valid), valid)
	}
	assert.NoError(t, validatePgVectorNamespace(strings.Repeat("a", 100)), "long names are mapped to a hashed table")
	for _, invalid := range []string{"", "1abc", "a-b", "a b", `x"; DROP TABLE y; --`, "a.b", strings.Repeat("a", pgvectorMaxNamespaceLen+1)} {
		assert.Error(t, validatePgVectorNamespace(invalid), invalid)
	}
}

func TestPgVectorSupportsIterativeScan(t *testing.T) {
	assert.True(t, pgvectorSupportsIterativeScan("0.8.0"))
	assert.True(t, pgvectorSupportsIterativeScan("0.8.7"))
	assert.True(t, pgvectorSupportsIterativeScan("0.10.1"))
	assert.True(t, pgvectorSupportsIterativeScan("1.0.0"))
	assert.False(t, pgvectorSupportsIterativeScan("0.7.4"))
	assert.False(t, pgvectorSupportsIterativeScan("0.5.1"))
	assert.False(t, pgvectorSupportsIterativeScan(""))
	assert.False(t, pgvectorSupportsIterativeScan("garbage"))
}

func TestPgVectorLikePattern(t *testing.T) {
	assert.Equal(t, "%ello%", pgvectorLikePattern("ello"), "no wildcard means substring")
	assert.Equal(t, `%a\_b%`, pgvectorLikePattern("a_b"), "underscore is literal")
	assert.Equal(t, "Hello%", pgvectorLikePattern("Hello*"))
	assert.Equal(t, "%x%", pgvectorLikePattern("%x%"))
	assert.Equal(t, `%a\\b%`, pgvectorLikePattern(`a\b`), "backslash is literal")
}

func TestPgVectorFilterBuilderParameterisesEverything(t *testing.T) {
	hostile := `x'; DROP TABLE t; --`
	builder := &pgFilterBuilder{}
	conditions, err := builder.conditions([]Query{
		{Field: hostile, Operator: QueryOperatorEqual, Value: hostile},
		{Field: "size", Operator: QueryOperatorGreaterThan, Value: 5},
		{Field: "title", Operator: QueryOperatorLike, Value: hostile},
		{Field: "tags", Operator: QueryOperatorContainsAny, Value: []string{hostile}},
		{Field: "gone", Operator: QueryOperatorIsNull},
	})
	require.NoError(t, err)
	sql := strings.Join(conditions, " AND ")

	assert.NotContains(t, sql, "DROP TABLE", "values must travel as bind parameters")
	assert.NotContains(t, sql, "size", "field names must travel as bind parameters")
	assert.NotContains(t, sql, "title")
	assert.NotContains(t, sql, "tags")
	assert.NotEmpty(t, builder.args)
	for i := range builder.args {
		assert.Contains(t, sql, fmt.Sprintf("$%d", i+1), "every argument is referenced")
	}
}

func TestPgVectorFilterBuilderRejectsBadQueries(t *testing.T) {
	for name, query := range map[string]Query{
		"no field":          {Operator: QueryOperatorEqual, Value: "x"},
		"unknown operator":  {Field: "f", Operator: QueryOperator("Regex"), Value: "x"},
		"like needs string": {Field: "f", Operator: QueryOperatorLike, Value: 5},
		"gt needs number":   {Field: "f", Operator: QueryOperatorGreaterThan, Value: []string{"a"}},
		"contains empty":    {Field: "f", Operator: QueryOperatorContainsAny, Value: []string{}},
	} {
		builder := &pgFilterBuilder{}
		_, err := builder.conditions([]Query{query})
		require.Error(t, err, name)
		assert.True(t, errors.Is(err, ErrQuerySyntax), name)
	}
}

func TestPgVectorConfigValidation(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	secret := func(v string) schemas.SecretVar { return *schemas.NewSecretVar(v) }

	_, err := newPgVectorStore(context.Background(), &PgVectorConfig{User: secret("u"), Database: secret("d")}, logger)
	assert.ErrorContains(t, err, "host is required")
	_, err = newPgVectorStore(context.Background(), &PgVectorConfig{Host: secret("h"), Database: secret("d")}, logger)
	assert.ErrorContains(t, err, "user is required")
	_, err = newPgVectorStore(context.Background(), &PgVectorConfig{Host: secret("h"), User: secret("u")}, logger)
	assert.ErrorContains(t, err, "database is required")
	_, err = newPgVectorStore(context.Background(), &PgVectorConfig{Host: secret("h"), User: secret("u"), Database: secret("d"), Schema: secret("bad-schema")}, logger)
	assert.ErrorContains(t, err, "invalid pgvector schema")
	_, err = newPgVectorStore(context.Background(), &PgVectorConfig{Host: secret("h"), User: secret("u"), Database: secret("d"), Port: secret("99999")}, logger)
	assert.ErrorContains(t, err, "invalid pgvector port")
}

func TestPgVectorConfigParsesFromJSON(t *testing.T) {
	var config Config
	err := config.UnmarshalJSON([]byte(`{"enabled": true, "type": "pgvector", "config": {"host": "db", "port": "5432", "user": "u", "password": "p", "database": "d"}}`))
	require.NoError(t, err)
	assert.Equal(t, VectorStoreTypePgVector, config.Type)
	parsed, ok := config.Config.(PgVectorConfig)
	require.True(t, ok)
	assert.Equal(t, "db", parsed.Host.GetValue())
	assert.Equal(t, 5432, parsed.Port.CoerceInt(0))
}

// ---------------------------------------------------------------------------
// Integration tests (need PostgreSQL with the pgvector extension)
// ---------------------------------------------------------------------------

type PgVectorTestSetup struct {
	Store  *PgVectorStore
	ctx    context.Context
	cancel context.CancelFunc
}

func NewPgVectorTestSetup(t testing.TB) *PgVectorTestSetup {
	t.Helper()
	config := PgVectorConfig{
		Host:     *schemas.NewSecretVar(getEnvWithDefault("PGVECTOR_HOST", PgVectorTestDefaultHost)),
		Port:     *schemas.NewSecretVar(getEnvWithDefault("PGVECTOR_PORT", PgVectorTestDefaultPort)),
		User:     *schemas.NewSecretVar(getEnvWithDefault("PGVECTOR_USER", "bifrost")),
		Password: *schemas.NewSecretVar(getEnvWithDefault("PGVECTOR_PASSWORD", "bifrost_password")),
		Database: *schemas.NewSecretVar(getEnvWithDefault("PGVECTOR_DATABASE", "bifrost")),
		SSLMode:  *schemas.NewSecretVar(getEnvWithDefault("PGVECTOR_SSLMODE", "disable")),
	}
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	timeout := PgVectorTestTimeout
	if _, isBenchmark := t.(*testing.B); isBenchmark {
		// Benchmarks load thousands of entries before timing starts.
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	store, err := newPgVectorStore(ctx, &config, logger)
	if err != nil {
		cancel()
		t.Fatalf("Failed to create pgvector store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close(context.Background(), "")
		cancel()
	})
	return &PgVectorTestSetup{Store: store, ctx: ctx, cancel: cancel}
}

// newNamespace creates a uniquely named namespace and drops it when the test ends.
func (ts *PgVectorTestSetup) newNamespace(t testing.TB, dimension int) string {
	t.Helper()
	namespace := "bifrost_pgv_" + strings.ReplaceAll(generateUUID(), "-", "")[:16]
	require.NoError(t, ts.Store.CreateNamespace(ts.ctx, namespace, dimension, nil))
	t.Cleanup(func() { _ = ts.Store.DeleteNamespace(context.Background(), namespace) })
	return namespace
}

func resultIDs(results []SearchResult) []string {
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestPgVectorStore_PingAndCapabilities(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	assert.NoError(t, ts.Store.Ping(ts.ctx))
	assert.False(t, ts.Store.RequiresVectors(), "the embedding column is nullable")
	assert.True(t, FiltersVectorReadsOnServer(ts.Store))
	assert.True(t, ts.Store.iterativeScan, "the test image ships pgvector 0.8 or later")
	assert.True(t, ts.Store.binaryVectors, "connections register the vector type and send vectors in binary")

	var _ VectorStore = ts.Store
}

// The text path is the fallback when the vector type cannot be registered; it must behave the
// same as the binary one.
func TestPgVectorStore_TextFallbackBehavesTheSame(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := ts.newNamespace(t, PgVectorTestDimension)
	query := unitVectorAtCosine(PgVectorTestDimension, 1)
	stored := unitVectorAtCosine(PgVectorTestDimension, 0.9)

	ts.Store.binaryVectors = false
	defer func() { ts.Store.binaryVectors = true }()
	require.NoError(t, ts.Store.Add(ts.ctx, namespace, "text-written", stored, map[string]interface{}{"cache_key": "k"}))
	textResults, err := ts.Store.GetNearest(ts.ctx, namespace, query, nil, nil, 0.8, 1)
	require.NoError(t, err)
	textAll, _, err := ts.Store.GetAll(WithIncludeVectors(ts.ctx), namespace, nil, nil, nil, 10)
	require.NoError(t, err)

	ts.Store.binaryVectors = true
	binaryResults, err := ts.Store.GetNearest(ts.ctx, namespace, query, nil, nil, 0.8, 1)
	require.NoError(t, err)
	binaryAll, _, err := ts.Store.GetAll(WithIncludeVectors(ts.ctx), namespace, nil, nil, nil, 10)
	require.NoError(t, err)

	require.Len(t, textResults, 1)
	require.Len(t, binaryResults, 1)
	assert.InDelta(t, 0.9, *textResults[0].Score, 1e-4)
	assert.InDelta(t, *textResults[0].Score, *binaryResults[0].Score, 1e-9, "both encodings give the same score")
	require.Len(t, textAll, 1)
	require.Len(t, binaryAll, 1)
	assert.Equal(t, stored, textAll[0].Vector)
	assert.Equal(t, stored, binaryAll[0].Vector, "binary reads return the exact stored floats")
}

func TestPgVectorStore_BadCredentialsGiveCleanError(t *testing.T) {
	config := PgVectorConfig{
		Host:     *schemas.NewSecretVar(getEnvWithDefault("PGVECTOR_HOST", PgVectorTestDefaultHost)),
		Port:     *schemas.NewSecretVar(getEnvWithDefault("PGVECTOR_PORT", PgVectorTestDefaultPort)),
		User:     *schemas.NewSecretVar("bifrost"),
		Password: *schemas.NewSecretVar("definitely-the-wrong-password"),
		Database: *schemas.NewSecretVar("bifrost"),
		SSLMode:  *schemas.NewSecretVar("disable"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := newPgVectorStore(ctx, &config, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "definitely-the-wrong-password", "the password must never appear in an error")
}

func TestPgVectorStore_NamespaceLifecycle(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := ts.newNamespace(t, PgVectorTestDimension)

	// Creating it again with the same dimension is a no-op.
	require.NoError(t, ts.Store.CreateNamespace(ts.ctx, namespace, PgVectorTestDimension, nil))

	// A different dimension is reported as a configuration error.
	err := ts.Store.CreateNamespace(ts.ctx, namespace, PgVectorTestDimension+1, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDimensionMismatch))

	namespaces, err := ts.Store.ListNamespaces(ts.ctx, "bifrost_pgv_")
	require.NoError(t, err)
	assert.Contains(t, namespaces, namespace)
	none, err := ts.Store.ListNamespaces(ts.ctx, "no_such_prefix_")
	require.NoError(t, err)
	assert.Empty(t, none)

	require.NoError(t, ts.Store.DeleteNamespace(ts.ctx, namespace))
	namespaces, err = ts.Store.ListNamespaces(ts.ctx, "bifrost_pgv_")
	require.NoError(t, err)
	assert.NotContains(t, namespaces, namespace)
	// Deleting a namespace that is gone is not an error.
	assert.NoError(t, ts.Store.DeleteNamespace(ts.ctx, namespace))
}

func TestPgVectorStore_NeverTouchesTablesItDidNotCreate(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	foreign := "foreign_tbl_" + strings.ReplaceAll(generateUUID(), "-", "")[:12]
	_, err := ts.Store.pool.Exec(ts.ctx, fmt.Sprintf(`CREATE TABLE %s (id int)`, foreign))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = ts.Store.pool.Exec(context.Background(), `DROP TABLE IF EXISTS `+foreign) })

	err = ts.Store.CreateNamespace(ts.ctx, foreign, PgVectorTestDimension, nil)
	require.Error(t, err, "must not adopt a table Bifrost did not create")
	assert.Contains(t, err.Error(), "not created by Bifrost")

	err = ts.Store.DeleteNamespace(ts.ctx, foreign)
	require.Error(t, err, "must not drop a table Bifrost did not create")

	var stillThere bool
	require.NoError(t, ts.Store.pool.QueryRow(ts.ctx, `SELECT to_regclass($1) IS NOT NULL`, foreign).Scan(&stillThere))
	assert.True(t, stillThere)
}

func TestPgVectorStore_ConcurrentNamespaceCreation(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := "bifrost_pgv_" + strings.ReplaceAll(generateUUID(), "-", "")[:16]
	t.Cleanup(func() { _ = ts.Store.DeleteNamespace(context.Background(), namespace) })

	const replicas = 8
	errs := make([]error, replicas)
	var wg sync.WaitGroup
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = ts.Store.CreateNamespace(ts.ctx, namespace, PgVectorTestDimension, nil)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		assert.NoError(t, err, "replica %d", i)
	}
	namespaces, err := ts.Store.ListNamespaces(ts.ctx, namespace)
	require.NoError(t, err)
	assert.Equal(t, []string{namespace}, namespaces)
}

func TestPgVectorStore_RejectsUnsafeNamespaces(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	vector := generateTestEmbedding(PgVectorTestDimension)
	for _, namespace := range []string{"", `x"; DROP TABLE y; --`, "a-b", "a.b", strings.Repeat("a", pgvectorMaxNamespaceLen+1)} {
		assert.Error(t, ts.Store.CreateNamespace(ts.ctx, namespace, PgVectorTestDimension, nil), namespace)
		assert.Error(t, ts.Store.Add(ts.ctx, namespace, "id", vector, nil), namespace)
		_, err := ts.Store.GetNearest(ts.ctx, namespace, vector, nil, nil, 0.5, 1)
		assert.Error(t, err, namespace)
		assert.Error(t, ts.Store.DeleteNamespace(ts.ctx, namespace), namespace)
	}
}

func TestPgVectorStore_AddGetDelete(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := ts.newNamespace(t, PgVectorTestDimension)
	vector := generateTestEmbedding(PgVectorTestDimension)

	require.NoError(t, ts.Store.Add(ts.ctx, namespace, "one", vector, map[string]interface{}{"response": "hello", "expires_at": int64(1900000000), "stream_chunks": []string{"a", "b"}}))

	chunk, err := ts.Store.GetChunk(ts.ctx, namespace, "one")
	require.NoError(t, err)
	assert.Equal(t, "one", chunk.ID)
	assert.Equal(t, "hello", chunk.Properties["response"])
	assert.Equal(t, float64(1900000000), chunk.Properties["expires_at"], "numbers decode as float64, which the semantic cache accepts")
	assert.Equal(t, []interface{}{"a", "b"}, chunk.Properties["stream_chunks"])

	// Adding the same id again replaces the entry.
	require.NoError(t, ts.Store.Add(ts.ctx, namespace, "one", vector, map[string]interface{}{"response": "replaced"}))
	chunk, err = ts.Store.GetChunk(ts.ctx, namespace, "one")
	require.NoError(t, err)
	assert.Equal(t, "replaced", chunk.Properties["response"])
	assert.NotContains(t, chunk.Properties, "expires_at")

	require.NoError(t, ts.Store.Add(ts.ctx, namespace, "two", vector, map[string]interface{}{"response": "second"}))
	chunks, err := ts.Store.GetChunks(ts.ctx, namespace, []string{"one", "two", "missing"})
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two"}, resultIDs(chunks))
	empty, err := ts.Store.GetChunks(ts.ctx, namespace, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)

	_, err = ts.Store.GetChunk(ts.ctx, namespace, "missing")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound))
	assert.Contains(t, strings.ToLower(err.Error()), "not found", "the semantic cache also matches on this text")

	require.NoError(t, ts.Store.Delete(ts.ctx, namespace, "one"))
	_, err = ts.Store.GetChunk(ts.ctx, namespace, "one")
	assert.True(t, errors.Is(err, ErrNotFound))
	assert.NoError(t, ts.Store.Delete(ts.ctx, namespace, "one"), "deleting a missing id is not an error")

	assert.Error(t, ts.Store.Add(ts.ctx, namespace, "", vector, nil))
	assert.Error(t, ts.Store.Delete(ts.ctx, namespace, " "))
	_, err = ts.Store.GetChunk(ts.ctx, namespace, "")
	assert.Error(t, err)
}

func TestPgVectorStore_RejectsInvalidVectors(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := ts.newNamespace(t, PgVectorTestDimension)

	bad := generateTestEmbedding(PgVectorTestDimension)
	bad[2] = float32(math.NaN())
	assert.Error(t, ts.Store.Add(ts.ctx, namespace, "nan", bad, nil))
	_, err := ts.Store.GetNearest(ts.ctx, namespace, bad, nil, nil, 0.5, 1)
	assert.Error(t, err)

	wrongSize := generateTestEmbedding(PgVectorTestDimension - 3)
	assert.Error(t, ts.Store.Add(ts.ctx, namespace, "short", wrongSize, nil), "a vector of the wrong dimension is rejected, not truncated")
	_, err = ts.Store.GetNearest(ts.ctx, namespace, wrongSize, nil, nil, 0.5, 1)
	assert.Error(t, err)

	_, err = ts.Store.GetNearest(ts.ctx, namespace, nil, nil, nil, 0.5, 1)
	assert.Error(t, err)
}

func TestPgVectorStore_MetadataOnlyEntries(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := ts.newNamespace(t, PgVectorTestDimension)

	require.NoError(t, ts.Store.Add(ts.ctx, namespace, "direct", nil, map[string]interface{}{"response": "direct only"}))
	chunk, err := ts.Store.GetChunk(ts.ctx, namespace, "direct")
	require.NoError(t, err)
	assert.Equal(t, "direct only", chunk.Properties["response"])

	results, err := ts.Store.GetNearest(ts.ctx, namespace, unitVectorAtCosine(PgVectorTestDimension, 1), nil, nil, -1, 10)
	require.NoError(t, err)
	assert.Empty(t, results, "an entry without an embedding is never a nearest neighbour")
}

func TestPgVectorStore_GetNearestUsesCosineSimilarity(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := ts.newNamespace(t, PgVectorTestDimension)

	query := unitVectorAtCosine(PgVectorTestDimension, 1)
	entries := map[string]float64{"identical": 1.0, "close": 0.95, "near": 0.85, "far": 0.5, "opposite": -0.9}
	for id, cosine := range entries {
		require.NoError(t, ts.Store.Add(ts.ctx, namespace, id, unitVectorAtCosine(PgVectorTestDimension, cosine), map[string]interface{}{"cache_key": "k"}))
	}

	results, err := ts.Store.GetNearest(ts.ctx, namespace, query, nil, nil, 0.8, 10)
	require.NoError(t, err)
	require.Len(t, results, 3, "only similarity >= 0.8 matches")
	assert.Equal(t, []string{"identical", "close", "near"}, []string{results[0].ID, results[1].ID, results[2].ID}, "best match first")
	for _, result := range results {
		require.NotNil(t, result.Score)
		assert.InDelta(t, entries[result.ID], *result.Score, 1e-4, "Score is cosine similarity, not distance")
	}

	// The threshold is inclusive and applied in the right direction.
	results, err = ts.Store.GetNearest(ts.ctx, namespace, query, nil, nil, 0.99, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"identical"}, resultIDs(results))

	results, err = ts.Store.GetNearest(ts.ctx, namespace, query, nil, nil, 0.9, 1)
	require.NoError(t, err)
	require.Len(t, results, 1, "limit is honoured")
	assert.Equal(t, "identical", results[0].ID)

	results, err = ts.Store.GetNearest(ts.ctx, namespace, query, nil, nil, 1.01, 10)
	require.NoError(t, err)
	assert.Empty(t, results, "no similarity can reach above 1")

	// A threshold below -1 returns everything.
	results, err = ts.Store.GetNearest(ts.ctx, namespace, query, nil, nil, -1, 10)
	require.NoError(t, err)
	assert.Len(t, results, len(entries))
}

func TestPgVectorStore_GetNearestAppliesFiltersAndSelectFields(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := ts.newNamespace(t, PgVectorTestDimension)

	require.NoError(t, ts.Store.Add(ts.ctx, namespace, "a", unitVectorAtCosine(PgVectorTestDimension, 0.95), map[string]interface{}{"cache_key": "k1", "params_hash": "p1", "from_bifrost_semantic_cache_plugin": true, "response": "A"}))
	require.NoError(t, ts.Store.Add(ts.ctx, namespace, "b", unitVectorAtCosine(PgVectorTestDimension, 0.85), map[string]interface{}{"cache_key": "k1", "params_hash": "p2", "from_bifrost_semantic_cache_plugin": true, "response": "B"}))
	require.NoError(t, ts.Store.Add(ts.ctx, namespace, "c", unitVectorAtCosine(PgVectorTestDimension, 1), map[string]interface{}{"cache_key": "k2", "params_hash": "p1", "from_bifrost_semantic_cache_plugin": true, "response": "C"}))

	// The same shape of filter the semantic cache sends.
	filters := []Query{
		{Field: "cache_key", Operator: QueryOperatorEqual, Value: "k1"},
		{Field: "params_hash", Operator: QueryOperatorEqual, Value: "p1"},
		{Field: "from_bifrost_semantic_cache_plugin", Operator: QueryOperatorEqual, Value: true},
	}
	results, err := ts.Store.GetNearest(ts.ctx, namespace, unitVectorAtCosine(PgVectorTestDimension, 1), filters, []string{"response", "cache_key"}, 0.8, 1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "a", results[0].ID, "c is nearer but belongs to another cache key")
	assert.Equal(t, map[string]interface{}{"response": "A", "cache_key": "k1"}, results[0].Properties, "only the selected fields are returned")
}

func TestPgVectorStore_FiltersOnGetAll(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := ts.newNamespace(t, PgVectorTestDimension)

	docs := map[string]map[string]interface{}{
		"doc1": {"cache_key": "k1", "size": 10, "public": true, "tags": []string{"go", "db"}, "title": "Hello World"},
		"doc2": {"cache_key": "k1", "size": 20, "public": false, "tags": []string{"go"}, "title": "hello_there"},
		"doc3": {"cache_key": "k2", "size": 30, "public": true, "tags": []string{"rust"}, "title": "Other"},
		"doc4": {"cache_key": "k2", "title": nil},
		"doc5": {"cache_key": "k3"},
	}
	for id, metadata := range docs {
		require.NoError(t, ts.Store.Add(ts.ctx, namespace, id, nil, metadata))
	}

	cases := []struct {
		name    string
		queries []Query
		want    []string
	}{
		{"no filter", nil, []string{"doc1", "doc2", "doc3", "doc4", "doc5"}},
		{"equal string", []Query{{Field: "cache_key", Operator: QueryOperatorEqual, Value: "k1"}}, []string{"doc1", "doc2"}},
		{"not equal", []Query{{Field: "cache_key", Operator: QueryOperatorNotEqual, Value: "k1"}}, []string{"doc3", "doc4", "doc5"}},
		{"equal bool", []Query{{Field: "public", Operator: QueryOperatorEqual, Value: true}}, []string{"doc1", "doc3"}},
		{"equal int", []Query{{Field: "size", Operator: QueryOperatorEqual, Value: 20}}, []string{"doc2"}},
		{"greater than", []Query{{Field: "size", Operator: QueryOperatorGreaterThan, Value: 15}}, []string{"doc2", "doc3"}},
		{"greater or equal", []Query{{Field: "size", Operator: QueryOperatorGreaterThanOrEqual, Value: 20}}, []string{"doc2", "doc3"}},
		{"less than", []Query{{Field: "size", Operator: QueryOperatorLessThan, Value: 20}}, []string{"doc1"}},
		{"less or equal skips entries without the field", []Query{{Field: "size", Operator: QueryOperatorLessThanOrEqual, Value: 20}}, []string{"doc1", "doc2"}},
		{"like substring", []Query{{Field: "title", Operator: QueryOperatorLike, Value: "ello"}}, []string{"doc1", "doc2"}},
		{"like prefix wildcard", []Query{{Field: "title", Operator: QueryOperatorLike, Value: "Hello*"}}, []string{"doc1"}},
		{"like underscore is literal", []Query{{Field: "title", Operator: QueryOperatorLike, Value: "hello_"}}, []string{"doc2"}},
		{"contains any", []Query{{Field: "tags", Operator: QueryOperatorContainsAny, Value: []string{"go", "rust"}}}, []string{"doc1", "doc2", "doc3"}},
		{"contains all", []Query{{Field: "tags", Operator: QueryOperatorContainsAll, Value: []string{"go", "db"}}}, []string{"doc1"}},
		{"is null", []Query{{Field: "title", Operator: QueryOperatorIsNull}}, []string{"doc4", "doc5"}},
		{"is not null", []Query{{Field: "title", Operator: QueryOperatorIsNotNull}}, []string{"doc1", "doc2", "doc3"}},
		{"filters are ANDed", []Query{{Field: "cache_key", Operator: QueryOperatorEqual, Value: "k1"}, {Field: "public", Operator: QueryOperatorEqual, Value: true}}, []string{"doc1"}},
		{"hostile field name matches nothing", []Query{{Field: `x') OR 1=1 --`, Operator: QueryOperatorEqual, Value: "y"}}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results, _, err := ts.Store.GetAll(ts.ctx, namespace, tc.queries, nil, nil, 100)
			require.NoError(t, err)
			assert.Equal(t, tc.want, resultIDs(results))
		})
	}

	_, _, err := ts.Store.GetAll(ts.ctx, namespace, []Query{{Field: "f", Operator: QueryOperator("Regex"), Value: "x"}}, nil, nil, 10)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrQuerySyntax))
}

func TestPgVectorStore_GetAllPagination(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := ts.newNamespace(t, PgVectorTestDimension)

	const total = 7
	for i := 0; i < total; i++ {
		require.NoError(t, ts.Store.Add(ts.ctx, namespace, fmt.Sprintf("id%02d", i), generateTestEmbedding(PgVectorTestDimension), map[string]interface{}{"n": i}))
	}

	var seen []string
	var pageSizes []int
	var cursor *string
	for page := 0; page < 10; page++ {
		results, next, err := ts.Store.GetAll(ts.ctx, namespace, nil, nil, cursor, 3)
		require.NoError(t, err)
		pageSizes = append(pageSizes, len(results))
		for _, result := range results {
			seen = append(seen, result.ID)
		}
		if next == nil {
			break
		}
		cursor = next
	}
	assert.Equal(t, []int{3, 3, 1}, pageSizes, "no empty trailing page")
	assert.Len(t, seen, total)
	assert.True(t, sort.StringsAreSorted(seen), "pages follow id order")
	seenSet := map[string]bool{}
	for _, id := range seen {
		assert.False(t, seenSet[id], "no id is returned twice: %s", id)
		seenSet[id] = true
	}

	// A page that exactly fills the limit does not invent another page.
	results, next, err := ts.Store.GetAll(ts.ctx, namespace, nil, nil, nil, total)
	require.NoError(t, err)
	assert.Len(t, results, total)
	assert.Nil(t, next)
}

func TestPgVectorStore_GetAllIncludeVectors(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := ts.newNamespace(t, PgVectorTestDimension)
	vector := generateTestEmbedding(PgVectorTestDimension)
	require.NoError(t, ts.Store.Add(ts.ctx, namespace, "with", vector, nil))
	require.NoError(t, ts.Store.Add(ts.ctx, namespace, "without", nil, nil))

	results, _, err := ts.Store.GetAll(ts.ctx, namespace, nil, nil, nil, 10)
	require.NoError(t, err)
	for _, result := range results {
		assert.Nil(t, result.Vector, "vectors are left out unless asked for")
	}

	results, _, err = ts.Store.GetAll(WithIncludeVectors(ts.ctx), namespace, nil, nil, nil, 10)
	require.NoError(t, err)
	require.Len(t, results, 2)
	byID := map[string]SearchResult{}
	for _, result := range results {
		byID[result.ID] = result
	}
	assert.Equal(t, vector, byID["with"].Vector)
	assert.Nil(t, byID["without"].Vector)
}

func TestPgVectorStore_DeleteAll(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := ts.newNamespace(t, PgVectorTestDimension)
	for i := 0; i < 6; i++ {
		key := "keep"
		if i%2 == 0 {
			key = "drop"
		}
		require.NoError(t, ts.Store.Add(ts.ctx, namespace, fmt.Sprintf("id%d", i), generateTestEmbedding(PgVectorTestDimension), map[string]interface{}{"cache_key": key}))
	}

	deleted, err := ts.Store.DeleteAll(ts.ctx, namespace, []Query{{Field: "cache_key", Operator: QueryOperatorEqual, Value: "drop"}})
	require.NoError(t, err)
	require.Len(t, deleted, 3)
	for _, result := range deleted {
		assert.Equal(t, DeleteStatusSuccess, result.Status)
	}
	left, _, err := ts.Store.GetAll(ts.ctx, namespace, nil, nil, nil, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"id1", "id3", "id5"}, resultIDs(left))

	deleted, err = ts.Store.DeleteAll(ts.ctx, namespace, nil)
	require.NoError(t, err)
	assert.Len(t, deleted, 3)
	left, _, err = ts.Store.GetAll(ts.ctx, namespace, nil, nil, nil, 100)
	require.NoError(t, err)
	assert.Empty(t, left)

	deleted, err = ts.Store.DeleteAll(ts.ctx, namespace, nil)
	require.NoError(t, err)
	assert.Empty(t, deleted, "nothing left to delete")
}

func TestPgVectorStore_NamespacesAreIsolated(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	first := ts.newNamespace(t, PgVectorTestDimension)
	second := ts.newNamespace(t, PgVectorTestDimension)
	vector := unitVectorAtCosine(PgVectorTestDimension, 1)

	require.NoError(t, ts.Store.Add(ts.ctx, first, "only-in-first", vector, nil))
	results, err := ts.Store.GetNearest(ts.ctx, second, vector, nil, nil, 0.5, 10)
	require.NoError(t, err)
	assert.Empty(t, results)
	_, err = ts.Store.GetChunk(ts.ctx, second, "only-in-first")
	assert.True(t, errors.Is(err, ErrNotFound))
}

// A very selective metadata filter must still find its match when many closer vectors fail the
// filter. PostgreSQL answers this shape through the metadata index followed by an exact sort.
func TestPgVectorStore_SelectiveFilterStillFindsMatch(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	const dimension = 16
	namespace := ts.newNamespace(t, dimension)
	query := unitVectorAtCosine(dimension, 1)

	for i := 0; i < 400; i++ {
		noise := unitVectorAtCosine(dimension, 0.9+rand.Float64()*0.09)
		require.NoError(t, ts.Store.Add(ts.ctx, namespace, fmt.Sprintf("noise%03d", i), noise, map[string]interface{}{"cache_key": "noise"}))
	}
	target := make([]float32, dimension)
	target[0] = 0.85
	target[2] = float32(math.Sqrt(1 - 0.85*0.85))
	require.NoError(t, ts.Store.Add(ts.ctx, namespace, "target", target, map[string]interface{}{"cache_key": "target"}))
	_, err := ts.Store.pool.Exec(ts.ctx, `ANALYZE `+mustTable(t, ts.Store, namespace))
	require.NoError(t, err)

	results, err := ts.Store.GetNearest(ts.ctx, namespace, query, []Query{{Field: "cache_key", Operator: QueryOperatorEqual, Value: "target"}}, nil, 0.8, 1)
	require.NoError(t, err)
	require.Len(t, results, 1, "the filtered match must be found")
	assert.Equal(t, "target", results[0].ID)
	assert.InDelta(t, 0.85, *results[0].Score, 1e-4)
}

// A filter that matches many rows sends PostgreSQL down the HNSW index. When the nearest vectors
// all belong to another cache key, a plain index scan reads only ef_search candidates, filters
// every one of them out and returns nothing although thousands of rows match: a false miss. The
// store enables iterative index scans so the search keeps going; this pins that down against
// the exact brute-force answer.
func TestPgVectorStore_BusyNeighbourKeyDoesNotHideMatches(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	if !ts.Store.iterativeScan {
		t.Skip("iterative index scans need pgvector 0.8 or later")
	}
	const dimension = 16
	namespace := ts.newNamespace(t, dimension)
	table := mustTable(t, ts.Store, namespace)

	// 10,000 vectors near the query under another key, 10,000 farther ones (cosine 0.80-0.86)
	// under ours, in a different plane so none of ours is among the nearest overall.
	_, err := ts.Store.pool.Exec(ts.ctx, fmt.Sprintf(`
		INSERT INTO %s (id, embedding, metadata)
		SELECT 'busy' || g, ('[' || c || ',' || sqrt(1 - c*c) || '%s]')::vector, '{"cache_key":"busy"}'::jsonb
		FROM (SELECT g, 0.9 + random() * 0.09 AS c FROM generate_series(1, 10000) g) s`,
		table, strings.Repeat(",0", dimension-2)))
	require.NoError(t, err)
	_, err = ts.Store.pool.Exec(ts.ctx, fmt.Sprintf(`
		INSERT INTO %s (id, embedding, metadata)
		SELECT 'mine' || g, ('[' || c || ',0,' || sqrt(1 - c*c) || '%s]')::vector, '{"cache_key":"mine"}'::jsonb
		FROM (SELECT g, 0.80 + random() * 0.06 AS c FROM generate_series(1, 10000) g) s`,
		table, strings.Repeat(",0", dimension-3)))
	require.NoError(t, err)
	_, err = ts.Store.pool.Exec(ts.ctx, `ANALYZE `+table)
	require.NoError(t, err)

	query := unitVectorAtCosine(dimension, 1)
	literal, err := pgvectorLiteral(query)
	require.NoError(t, err)
	filters := []Query{{Field: "cache_key", Operator: QueryOperatorEqual, Value: "mine"}}

	// The exact best match, with index scans switched off.
	tx, err := ts.Store.pool.Begin(ts.ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ts.ctx, `SET LOCAL enable_indexscan = off`)
	require.NoError(t, err)
	_, err = tx.Exec(ts.ctx, `SET LOCAL enable_bitmapscan = off`)
	require.NoError(t, err)
	var exactSimilarity float64
	err = tx.QueryRow(ts.ctx, fmt.Sprintf(`SELECT 1 - (embedding <=> $1::text::vector) FROM %s WHERE metadata @> '{"cache_key":"mine"}' ORDER BY embedding <=> $1::text::vector LIMIT 1`, table), literal).Scan(&exactSimilarity)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ts.ctx))

	results, err := ts.Store.GetNearest(ts.ctx, namespace, query, filters, nil, 0.8, 1)
	require.NoError(t, err)
	require.Len(t, results, 1, "thousands of entries under this key pass the threshold; a miss here is a false miss")
	assert.True(t, strings.HasPrefix(results[0].ID, "mine"))
	assert.InDelta(t, exactSimilarity, *results[0].Score, 0.005, "the match is as good as the exact best")

	// Every pooled connection has iterative scans switched on.
	var setting string
	require.NoError(t, ts.Store.pool.QueryRow(ts.ctx, `SHOW hnsw.iterative_scan`).Scan(&setting))
	assert.Equal(t, "strict_order", setting)

	// For the record: the same search with iterative scans off. Whether it misses depends on the
	// planner and the graph, so this is logged rather than asserted.
	plainTx, err := ts.Store.pool.Begin(ts.ctx)
	require.NoError(t, err)
	defer func() { _ = plainTx.Rollback(ts.ctx) }()
	_, err = plainTx.Exec(ts.ctx, `SET LOCAL hnsw.iterative_scan = off`)
	require.NoError(t, err)
	var plainCount int
	err = plainTx.QueryRow(ts.ctx, fmt.Sprintf(`SELECT count(*) FROM (SELECT id FROM %s WHERE embedding IS NOT NULL AND metadata @> '{"cache_key":"mine"}' AND (embedding <=> $1::text::vector) <= 0.2 ORDER BY embedding <=> $1::text::vector LIMIT 1) q`, table), literal).Scan(&plainCount)
	require.NoError(t, err)
	t.Logf("without iterative scan the same search returned %d result(s); 0 reproduces the false miss this setting prevents", plainCount)
}

// Some callers derive namespaces from a hash (the complexity router uses 88 characters). Names
// longer than a comfortable table name map to a hashed table and still round-trip.
func TestPgVectorStore_LongNamespaces(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	long := "BifrostComplexityRouter_" + strings.Repeat("ab12", 16) + strings.ReplaceAll(generateUUID(), "-", "")[:8]
	require.Greater(t, len(long), 63)
	require.NoError(t, ts.Store.CreateNamespace(ts.ctx, long, PgVectorTestDimension, nil))
	t.Cleanup(func() { _ = ts.Store.DeleteNamespace(context.Background(), long) })
	require.NoError(t, ts.Store.CreateNamespace(ts.ctx, long, PgVectorTestDimension, nil), "idempotent")

	vector := unitVectorAtCosine(PgVectorTestDimension, 1)
	require.NoError(t, ts.Store.Add(ts.ctx, long, "x", vector, map[string]interface{}{"k": "v"}))
	results, err := ts.Store.GetNearest(ts.ctx, long, vector, nil, nil, 0.9, 1)
	require.NoError(t, err)
	require.Len(t, results, 1)

	namespaces, err := ts.Store.ListNamespaces(ts.ctx, "BifrostComplexityRouter_")
	require.NoError(t, err)
	assert.Contains(t, namespaces, long, "the full namespace is listed, not the hashed table name")

	// A different long name sharing the first 32 characters gets its own table.
	sibling := long[:40] + "zz"
	assert.NotEqual(t, pgvectorTableName(long), pgvectorTableName(sibling))
	assert.LessOrEqual(t, len(pgvectorTableName(long)), pgvectorMaxTableNameLen)

	require.NoError(t, ts.Store.DeleteNamespace(ts.ctx, long))
	namespaces, err = ts.Store.ListNamespaces(ts.ctx, "BifrostComplexityRouter_")
	require.NoError(t, err)
	assert.NotContains(t, namespaces, long)
}

func TestPgVectorInvalidNamespaceIsAClientError(t *testing.T) {
	for _, namespace := range []string{"", "my-cache", strings.Repeat("a", pgvectorMaxNamespaceLen+1)} {
		err := validatePgVectorNamespace(namespace)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidNamespace), namespace)
	}
}

func mustTable(t testing.TB, store *PgVectorStore, namespace string) string {
	t.Helper()
	table, err := store.table(namespace)
	require.NoError(t, err)
	return table
}

func TestPgVectorStore_ClosedContextIsReported(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := ts.newNamespace(t, PgVectorTestDimension)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	assert.Error(t, ts.Store.Add(cancelled, namespace, "x", generateTestEmbedding(PgVectorTestDimension), nil))
	_, err := ts.Store.GetNearest(cancelled, namespace, generateTestEmbedding(PgVectorTestDimension), nil, nil, 0.5, 1)
	assert.Error(t, err, "a database failure is an error, never an empty hit")
}

// ---------------------------------------------------------------------------
// Benchmarks: the semantic cache's request path (a lookup per request, a write per miss)
// ---------------------------------------------------------------------------

// PgVectorBenchDimension is the size of a common embedding model (text-embedding-3-small).
const PgVectorBenchDimension = 1536

// BenchmarkPgVectorBinaryEncode measures encoding a vector in pgvector's binary format, done for
// every lookup and write when connections know the vector type.
func BenchmarkPgVectorBinaryEncode(b *testing.B) {
	vector := generateTestEmbedding(PgVectorBenchDimension)
	buf := make([]byte, 0, 4+4*PgVectorBenchDimension)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		var err error
		if buf, err = (pgvectorEncodePlan{}).Encode(vector, buf[:0]); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPgVectorLiteral measures encoding a vector as text, the fallback path.
func BenchmarkPgVectorLiteral(b *testing.B) {
	vector := generateTestEmbedding(PgVectorBenchDimension)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := pgvectorLiteral(vector); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPgVectorFilterBuilder measures translating the semantic cache's lookup filters.
func BenchmarkPgVectorFilterBuilder(b *testing.B) {
	queries := []Query{
		{Field: "cache_key", Operator: QueryOperatorEqual, Value: "tenant-a"},
		{Field: "params_hash", Operator: QueryOperatorEqual, Value: "f00dfeed"},
		{Field: "from_bifrost_semantic_cache_plugin", Operator: QueryOperatorEqual, Value: true},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		builder := &pgFilterBuilder{}
		if _, err := builder.conditions(queries); err != nil {
			b.Fatal(err)
		}
	}
}

// newPgVectorBenchNamespace loads entries spread over 20 cache keys, shaped like the semantic
// cache's writes, and returns one stored vector per key to query with.
func newPgVectorBenchNamespace(b *testing.B, entries int) (*PgVectorTestSetup, string, map[string][]float32) {
	b.Helper()
	ts := NewPgVectorTestSetup(b)
	namespace := ts.newNamespace(b, PgVectorBenchDimension)
	probes := map[string][]float32{}
	for i := 0; i < entries; i++ {
		key := fmt.Sprintf("tenant-%02d", i%20)
		vector := generateTestEmbedding(PgVectorBenchDimension)
		if _, ok := probes[key]; !ok {
			probes[key] = vector
		}
		require.NoError(b, ts.Store.Add(ts.ctx, namespace, generateUUID(), vector, map[string]interface{}{
			"response":                           "cached response body",
			"cache_key":                          key,
			"params_hash":                        "f00dfeed",
			"provider":                           "openai",
			"model":                              "gpt-4o-mini",
			"from_bifrost_semantic_cache_plugin": true,
			"expires_at":                         time.Now().Add(time.Hour).Unix(),
		}))
	}
	return ts, namespace, probes
}

// BenchmarkPgVectorStore_GetNearestCacheLookup is the per-request semantic lookup: the cache's
// three equality filters, limit 1, threshold 0.8.
func BenchmarkPgVectorStore_GetNearestCacheLookup(b *testing.B) {
	ts, namespace, probes := newPgVectorBenchNamespace(b, 2000)
	query := probes["tenant-07"]
	filters := []Query{
		{Field: "cache_key", Operator: QueryOperatorEqual, Value: "tenant-07"},
		{Field: "params_hash", Operator: QueryOperatorEqual, Value: "f00dfeed"},
		{Field: "from_bifrost_semantic_cache_plugin", Operator: QueryOperatorEqual, Value: true},
	}
	selectFields := []string{"response", "expires_at", "cache_key", "provider", "model"}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		results, err := ts.Store.GetNearest(ts.ctx, namespace, query, filters, selectFields, 0.8, 1)
		if err != nil {
			b.Fatal(err)
		}
		if len(results) != 1 {
			b.Fatalf("expected the stored vector to match itself, got %d results", len(results))
		}
	}
}

// BenchmarkPgVectorStore_GetChunkDirectLookup is the per-request direct (hash) lookup.
func BenchmarkPgVectorStore_GetChunkDirectLookup(b *testing.B) {
	ts := NewPgVectorTestSetup(b)
	namespace := ts.newNamespace(b, PgVectorBenchDimension)
	require.NoError(b, ts.Store.Add(ts.ctx, namespace, "direct-entry", nil, map[string]interface{}{"response": "cached response body", "cache_key": "tenant-00"}))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := ts.Store.GetChunk(ts.ctx, namespace, "direct-entry"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPgVectorStore_Add is the write made once per cache miss.
func BenchmarkPgVectorStore_Add(b *testing.B) {
	ts := NewPgVectorTestSetup(b)
	namespace := ts.newNamespace(b, PgVectorBenchDimension)
	vector := generateTestEmbedding(PgVectorBenchDimension)
	metadata := map[string]interface{}{
		"response":                           "cached response body",
		"cache_key":                          "tenant-00",
		"params_hash":                        "f00dfeed",
		"from_bifrost_semantic_cache_plugin": true,
		"expires_at":                         time.Now().Add(time.Hour).Unix(),
	}
	i := 0
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		i++
		if err := ts.Store.Add(ts.ctx, namespace, fmt.Sprintf("entry-%d", i), vector, metadata); err != nil {
			b.Fatal(err)
		}
	}
}
