package vectorstore

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPgVectorVersionAtLeast(t *testing.T) {
	assert.True(t, pgvectorVersionAtLeast("0.7.0", 0, 7))
	assert.True(t, pgvectorVersionAtLeast("0.8.7", 0, 7))
	assert.True(t, pgvectorVersionAtLeast("0.10.0", 0, 7))
	assert.True(t, pgvectorVersionAtLeast("1.0.0", 0, 7))
	assert.False(t, pgvectorVersionAtLeast("0.6.2", 0, 7))
	assert.False(t, pgvectorVersionAtLeast("0.5.1", 0, 7))
	assert.False(t, pgvectorVersionAtLeast("", 0, 7))
	assert.False(t, pgvectorVersionAtLeast("garbage", 0, 7))
}

func TestPgVectorConfigParsesNewOptions(t *testing.T) {
	var config Config
	require.NoError(t, config.UnmarshalJSON([]byte(`{"enabled":true,"type":"pgvector","config":{"host":"db","user":"u","database":"d","vector_type":"halfvec","row_level_security":true}}`)))
	parsed, ok := config.Config.(PgVectorConfig)
	require.True(t, ok)
	assert.Equal(t, "halfvec", parsed.VectorType)
	assert.True(t, parsed.RowLevelSecurity)

	require.NoError(t, config.UnmarshalJSON([]byte(`{"enabled":true,"type":"pgvector","config":{"host":"db","user":"u","database":"d"}}`)))
	parsed = config.Config.(PgVectorConfig)
	assert.Empty(t, parsed.VectorType, "the default is the plain vector type")
	assert.False(t, parsed.RowLevelSecurity, "row-level security is off unless asked for")
}

func TestPgVectorRejectsUnknownVectorType(t *testing.T) {
	config := pgConfigForRole("u", "p", false)
	config.VectorType = "bit"
	_, err := newPgVectorStore(context.Background(), &config, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vector_type")
}

func halfvecStore(t testing.TB, ctx context.Context) *PgVectorStore {
	t.Helper()
	config := pgConfigForRole(getEnvWithDefault("PGVECTOR_USER", "bifrost"), getEnvWithDefault("PGVECTOR_PASSWORD", "bifrost_password"), false)
	config.VectorType = "halfvec"
	return openRoleStore(t, ctx, config)
}

func TestPgVectorHalfvec_StoresSearchesAndReads(t *testing.T) {
	admin := NewPgVectorTestSetup(t)
	store := halfvecStore(t, admin.ctx)
	namespace := "bifrost_half_" + strings.ReplaceAll(generateUUID(), "-", "")[:12]
	require.NoError(t, store.CreateNamespace(admin.ctx, namespace, PgVectorTestDimension, nil))
	t.Cleanup(func() { _ = store.DeleteNamespace(context.Background(), namespace) })
	table := mustTable(t, store, namespace)

	var columnType, indexDef string
	require.NoError(t, store.pool.QueryRow(admin.ctx, `SELECT format_type(atttypid, atttypmod) FROM pg_attribute WHERE attrelid = to_regclass($1) AND attname = 'embedding'`, table).Scan(&columnType))
	assert.Equal(t, fmt.Sprintf("halfvec(%d)", PgVectorTestDimension), columnType)
	require.NoError(t, store.pool.QueryRow(admin.ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = $1`, namespace+"_emb_hnsw").Scan(&indexDef))
	assert.Contains(t, indexDef, "halfvec_cosine_ops")

	stored := unitVectorAtCosine(PgVectorTestDimension, 0.9)
	query := unitVectorAtCosine(PgVectorTestDimension, 1)
	for _, binary := range []bool{true, false} {
		store.binaryVectors = binary
		id := fmt.Sprintf("row-%t", binary)
		require.NoError(t, store.Add(admin.ctx, namespace, id, stored, map[string]interface{}{"cache_key": "k"}))
		results, err := store.GetNearest(admin.ctx, namespace, query, []Query{{Field: "cache_key", Operator: QueryOperatorEqual, Value: "k"}}, nil, 0.8, 5)
		require.NoError(t, err, "binary=%t", binary)
		require.NotEmpty(t, results)
		assert.InDelta(t, 0.9, *results[0].Score, 2e-3, "half precision keeps the cosine close (binary=%t)", binary)

		all, _, err := store.GetAll(WithIncludeVectors(admin.ctx), namespace, []Query{{Field: "cache_key", Operator: QueryOperatorEqual, Value: "k"}}, nil, nil, 10)
		require.NoError(t, err)
		require.NotEmpty(t, all)
		require.Len(t, all[0].Vector, PgVectorTestDimension)
		for i := range stored {
			assert.InDelta(t, stored[i], all[0].Vector[i], 1e-3, "binary=%t", binary)
		}
	}
	store.binaryVectors = true

	// A metadata-only entry (no embedding) still works.
	require.NoError(t, store.Add(admin.ctx, namespace, "meta-only", nil, map[string]interface{}{"cache_key": "k"}))
	_, err := store.GetChunk(admin.ctx, namespace, "meta-only")
	require.NoError(t, err)

	// An existing table keeps its type: asking for another type is an error, not a silent switch.
	plain := NewPgVectorTestSetup(t)
	err = plain.Store.CreateNamespace(plain.ctx, namespace, PgVectorTestDimension, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "halfvec embeddings but vector_type is \"vector\"")
	vectorNS := plain.newNamespace(t, PgVectorTestDimension)
	err = store.CreateNamespace(admin.ctx, vectorNS, PgVectorTestDimension, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vector embeddings but vector_type is \"halfvec\"")
}

// The index limits were checked on the container (pgvector 0.8.7): hnsw on halfvec stops at 4000
// dimensions, hnsw on vector at 2000, and a column of either type can have up to 16000.
func TestPgVectorHalfvec_IndexLimit(t *testing.T) {
	admin := NewPgVectorTestSetup(t)
	half := halfvecStore(t, admin.ctx)
	hasIndex := func(store *PgVectorStore, namespace string) bool {
		var count int
		require.NoError(t, store.pool.QueryRow(admin.ctx, `SELECT count(*) FROM pg_indexes WHERE indexname = $1`, namespace+"_emb_hnsw").Scan(&count))
		return count == 1
	}
	for _, c := range []struct {
		store     *PgVectorStore
		dimension int
		indexed   bool
	}{
		{half, 3072, true}, // text-embedding-3-large fits a halfvec index
		{half, 4000, true},
		{half, 4001, false},
		{admin.Store, 2000, true},
		{admin.Store, 2001, false},
	} {
		namespace := "bifrost_lim_" + strings.ReplaceAll(generateUUID(), "-", "")[:12]
		require.NoError(t, c.store.CreateNamespace(admin.ctx, namespace, c.dimension, nil), "dimension %d", c.dimension)
		t.Cleanup(func() { _ = c.store.DeleteNamespace(context.Background(), namespace) })
		assert.Equal(t, c.indexed, hasIndex(c.store, namespace), "%s dimension %d", c.store.vectorType, c.dimension)
	}
	namespace := "bifrost_lim_" + strings.ReplaceAll(generateUUID(), "-", "")[:12]
	require.NoError(t, half.CreateNamespace(admin.ctx, namespace, 16000, nil))
	t.Cleanup(func() { _ = half.DeleteNamespace(context.Background(), namespace) })
	assert.Error(t, half.CreateNamespace(admin.ctx, "bifrost_lim_toobig", 16001, nil))
}

// ---------------------------------------------------------------------------
// Measurements (opt in: BIFROST_PGVECTOR_MEASURE=1 go test -run Measure -v)
// ---------------------------------------------------------------------------

func normalVector(rng *rand.Rand, dimension int) []float32 {
	vector := make([]float32, dimension)
	var norm float64
	for i := range vector {
		x := rng.NormFloat64()
		vector[i] = float32(x)
		norm += x * x
	}
	norm = math.Sqrt(norm)
	for i := range vector {
		vector[i] = float32(float64(vector[i]) / norm)
	}
	return vector
}

// noisyCopy returns a unit vector close to v (about the cosine the semantic cache's threshold asks for).
func noisyCopy(rng *rand.Rand, v []float32, noise float64) []float32 {
	out := make([]float32, len(v))
	var norm float64
	for i := range v {
		x := float64(v[i]) + noise*rng.NormFloat64()/math.Sqrt(float64(len(v)))
		out[i] = float32(x)
		norm += x * x
	}
	norm = math.Sqrt(norm)
	for i := range out {
		out[i] = float32(float64(out[i]) / norm)
	}
	return out
}

func exactCosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func TestPgVectorHalfvec_MeasureSizeAndRecall(t *testing.T) {
	if getEnvWithDefault("BIFROST_PGVECTOR_MEASURE", "") == "" {
		t.Skip("set BIFROST_PGVECTOR_MEASURE=1 to run the measurements")
	}
	admin := NewPgVectorTestSetup(t)
	// Loading and searching thousands of rows takes longer than the default test context allows.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	half := halfvecStore(t, ctx)
	const rows, queries = 5000, 200

	for _, dimension := range []int{384, 1536} {
		rng := rand.New(rand.NewSource(int64(dimension)))
		corpus := make([][]float32, rows)
		for i := range corpus {
			corpus[i] = normalVector(rng, dimension)
		}
		// Two query sets: "near" are paraphrase-like (a stored vector plus noise, cosine about 0.9),
		// the cache's real job; "random" are unrelated vectors, the hardest case for recall because
		// the top candidates are almost tied.
		near := make([][]float32, queries)
		random := make([][]float32, queries)
		for i := range near {
			near[i] = noisyCopy(rng, corpus[rng.Intn(rows)], 0.5)
			random[i] = normalVector(rng, dimension)
		}

		truth := func(set [][]float32) ([]int, []float64) {
			ids := make([]int, len(set))
			sims := make([]float64, len(set))
			for q, v := range set {
				best, bestSim := -1, math.Inf(-1)
				for i, c := range corpus {
					if sim := exactCosine(v, c); sim > bestSim {
						best, bestSim = i, sim
					}
				}
				ids[q], sims[q] = best, bestSim
			}
			return ids, sims
		}
		nearIDs, nearSims := truth(near)
		randomIDs, _ := truth(random)

		type result struct {
			tableBytes, indexBytes, totalBytes int64
			recallNear, recallRandom           float64
			exactRecallNear, exactRecallRand   float64
			meanErr, maxErr                    float64
			median                             time.Duration
		}
		run := func(store *PgVectorStore) result {
			namespace := fmt.Sprintf("bifrost_m_%s_%d_%s", store.vectorType, dimension, strings.ReplaceAll(generateUUID(), "-", "")[:6])
			require.NoError(t, store.CreateNamespace(ctx, namespace, dimension, nil))
			t.Cleanup(func() { _ = store.DeleteNamespace(context.Background(), namespace) })
			table := mustTable(t, store, namespace)
			for i, vector := range corpus {
				require.NoError(t, store.Add(ctx, namespace, fmt.Sprintf("%05d", i), vector, map[string]interface{}{"cache_key": "k", "expires_at": time.Now().Add(time.Hour).Unix()}))
			}
			_, err := store.pool.Exec(ctx, `VACUUM (ANALYZE) `+table)
			require.NoError(t, err)

			var r result
			require.NoError(t, store.pool.QueryRow(ctx, `SELECT pg_table_size($1::regclass), pg_relation_size($2::regclass), pg_total_relation_size($1::regclass)`,
				table, pgvectorIndexName(store, namespace+"_emb_hnsw")).Scan(&r.tableBytes, &r.indexBytes, &r.totalBytes))

			recall := func(set [][]float32, want []int) (float64, []time.Duration) {
				hits := 0
				var durations []time.Duration
				for q, v := range set {
					start := time.Now()
					res, err := store.GetNearest(ctx, namespace, v, nil, nil, -1, 1)
					durations = append(durations, time.Since(start))
					require.NoError(t, err)
					if len(res) == 1 && res[0].ID == fmt.Sprintf("%05d", want[q]) {
						hits++
					}
				}
				return float64(hits) / float64(len(set)), durations
			}
			var durations []time.Duration
			r.recallNear, durations = recall(near, nearIDs)
			r.recallRandom, _ = recall(random, randomIDs)
			sortDurations(durations)
			r.median = durations[len(durations)/2]

			// Same questions answered by an exact scan of the stored (possibly half-precision) data, which
			// separates the cost of half precision from the cost of the approximate HNSW index.
			exact := func(set [][]float32, want []int) float64 {
				hits := 0
				for q, v := range set {
					tx, err := store.pool.Begin(ctx)
					require.NoError(t, err)
					_, err = tx.Exec(ctx, `SET LOCAL enable_indexscan = off`)
					require.NoError(t, err)
					var id string
					require.NoError(t, tx.QueryRow(ctx, fmt.Sprintf(`SELECT id FROM %s ORDER BY embedding <=> $1%s LIMIT 1`, table, store.vectorCast()), v).Scan(&id))
					require.NoError(t, tx.Rollback(ctx))
					if id == fmt.Sprintf("%05d", want[q]) {
						hits++
					}
				}
				return float64(hits) / float64(len(set))
			}
			r.exactRecallNear = exact(near, nearIDs)
			r.exactRecallRand = exact(random, randomIDs)

			// Similarity error: the score the store reports for the true nearest neighbour against the
			// exact float32 cosine, using the exact scan so the index plays no part.
			var sumErr float64
			for q, v := range near {
				tx, err := store.pool.Begin(ctx)
				require.NoError(t, err)
				_, err = tx.Exec(ctx, `SET LOCAL enable_indexscan = off`)
				require.NoError(t, err)
				var score float64
				require.NoError(t, tx.QueryRow(ctx, fmt.Sprintf(`SELECT 1 - (embedding <=> $1%s) FROM %s WHERE id = $2`, store.vectorCast(), table), v, fmt.Sprintf("%05d", nearIDs[q])).Scan(&score))
				require.NoError(t, tx.Rollback(ctx))
				e := math.Abs(score - nearSims[q])
				sumErr += e
				r.maxErr = math.Max(r.maxErr, e)
			}
			r.meanErr = sumErr / float64(len(near))
			return r
		}

		vec := run(admin.Store)
		hlf := run(half)
		t.Logf("MEASURE dim=%d rows=%d", dimension, rows)
		t.Logf("MEASURE   vector : table=%d B (pg_table_size) hnsw index=%d B total=%d B | recall@1 near=%.3f random=%.3f | exact-scan recall near=%.3f random=%.3f | score error mean=%.2e max=%.2e | median search %v",
			vec.tableBytes, vec.indexBytes, vec.totalBytes, vec.recallNear, vec.recallRandom, vec.exactRecallNear, vec.exactRecallRand, vec.meanErr, vec.maxErr, vec.median)
		t.Logf("MEASURE   halfvec: table=%d B (pg_table_size) hnsw index=%d B total=%d B | recall@1 near=%.3f random=%.3f | exact-scan recall near=%.3f random=%.3f | score error mean=%.2e max=%.2e | median search %v",
			hlf.tableBytes, hlf.indexBytes, hlf.totalBytes, hlf.recallNear, hlf.recallRandom, hlf.exactRecallNear, hlf.exactRecallRand, hlf.meanErr, hlf.maxErr, hlf.median)
		t.Logf("MEASURE   halfvec/vector size: table %.2f, index %.2f, total %.2f", float64(hlf.tableBytes)/float64(vec.tableBytes), float64(hlf.indexBytes)/float64(vec.indexBytes), float64(hlf.totalBytes)/float64(vec.totalBytes))
	}
}

// pgvectorIndexName returns the quoted, schema-qualified name of an index in the store's schema.
func pgvectorIndexName(store *PgVectorStore, name string) string {
	return `"` + store.schema + `"."` + name + `"`
}
