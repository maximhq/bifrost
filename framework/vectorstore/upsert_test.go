package vectorstore

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/fault"
)

// upsertTestProperties is the schema the replace-on-existing-id tests write.
// Redis indexes by declared field, so the property has to be declared at
// namespace creation for the written value to be readable afterwards.
var upsertTestProperties = map[string]VectorStoreProperties{
	"marker": {DataType: VectorStorePropertyTypeString, Description: "upsert test marker"},
}

// upsertBackend is one backend's answer to "give me a store I can write to".
// Backends that need a server skip themselves; chromem always runs.
type upsertBackend struct {
	name      string
	dimension int
	setup     func(t *testing.T) (VectorStore, context.Context, string)
}

// TestVectorStoreAddReplacesExistingID pins Add's upsert semantics as an
// interface-wide contract rather than a per-backend accident.
//
// Callers derive record ids from content — the complexity router derives one
// per exemplar from its configuration fingerprint — so the same id is written
// again by an ordinary retry, by a resumed warmup, and by two nodes racing to
// warm the same generation into a shared store. Weaviate was the one backend
// that refused those writes (Creator() is a POST, rejected with 422), which
// turned a benign duplicate into a failed warmup on every node but one.
func TestVectorStoreAddReplacesExistingID(t *testing.T) {
	backends := []upsertBackend{
		{
			name:      "chromem",
			dimension: ChromemTestDimension,
			setup: func(t *testing.T) (VectorStore, context.Context, string) {
				setup := NewChromemTestSetup(t)
				t.Cleanup(func() { setup.Cleanup(t) })
				return setup.Store, setup.ctx, ChromemTestNamespace
			},
		},
		{
			name:      "weaviate",
			dimension: TestEmbeddingDim,
			setup: func(t *testing.T) (VectorStore, context.Context, string) {
				if testing.Short() {
					t.Skip("Skipping integration tests in short mode")
				}
				setup := NewTestSetup(t)
				t.Cleanup(func() { setup.Cleanup(t) })
				return setup.Store, setup.ctx, TestClassName
			},
		},
		{
			name:      "qdrant",
			dimension: QdrantTestDimension,
			setup: func(t *testing.T) (VectorStore, context.Context, string) {
				if testing.Short() {
					t.Skip("Skipping integration tests in short mode")
				}
				setup := NewQdrantTestSetup(t)
				t.Cleanup(func() { setup.Cleanup(t) })
				return setup.Store, setup.ctx, QdrantTestCollection
			},
		},
		{
			name:      "pinecone",
			dimension: PineconeTestDimension,
			setup: func(t *testing.T) (VectorStore, context.Context, string) {
				if testing.Short() {
					t.Skip("Skipping integration tests in short mode")
				}
				setup := NewPineconeTestSetup(t)
				t.Cleanup(func() { setup.Cleanup(t) })
				return setup.Store, setup.ctx, PineconeTestNamespace
			},
		},
		{
			name:      "redis",
			dimension: RedisTestDimension,
			setup: func(t *testing.T) (VectorStore, context.Context, string) {
				if testing.Short() {
					t.Skip("Skipping integration tests in short mode")
				}
				setup := NewRedisTestSetup(t)
				t.Cleanup(func() { setup.Cleanup(t) })
				return setup.Store, setup.ctx, TestNamespace
			},
		},
	}

	for _, backend := range backends {
		t.Run(backend.name, func(t *testing.T) {
			store, ctx, namespace := backend.setup(t)
			require.NoError(t, store.CreateNamespace(ctx, namespace, backend.dimension, upsertTestProperties))

			id := generateUUID()
			first := generateTestEmbedding(backend.dimension)
			second := generateTestEmbedding(backend.dimension)

			require.NoError(t, store.Add(ctx, namespace, id, first, map[string]interface{}{"marker": "first"}))
			// Rewriting the same id must succeed rather than conflict.
			require.NoError(t, store.Add(ctx, namespace, id, second, map[string]interface{}{"marker": "second"}),
				"a repeated Add of the same id must replace, not fail")

			// Several backends index asynchronously; the existing integration
			// tests in this package settle the same way.
			time.Sleep(200 * time.Millisecond)

			result, err := store.GetChunk(ctx, namespace, id)
			require.NoError(t, err)
			assert.Equal(t, "second", result.Properties["marker"], "the later write must win")

			t.Cleanup(func() {
				_ = store.Delete(ctx, namespace, id)
			})
		})
	}
}

// TestIsWeaviateAlreadyExists guards the narrow condition Add retries on. A 422
// is also how Weaviate reports a property that does not fit the class schema,
// and retrying that as a replace would swap a precise validation failure for a
// vaguer one.
func TestIsWeaviateAlreadyExists(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "duplicate id",
			err: &fault.WeaviateClientError{
				IsUnexpectedStatusCode: true,
				StatusCode:             http.StatusUnprocessableEntity,
				Msg:                    "id 'a1b2c3d4-0000-0000-0000-000000000000' already exists",
			},
			want: true,
		},
		{
			name: "schema violation shares the status code",
			err: &fault.WeaviateClientError{
				IsUnexpectedStatusCode: true,
				StatusCode:             http.StatusUnprocessableEntity,
				Msg:                    "invalid object: no such prop with name 'marker' found in class",
			},
			want: false,
		},
		{
			name: "not found",
			err: &fault.WeaviateClientError{
				IsUnexpectedStatusCode: true,
				StatusCode:             http.StatusNotFound,
				Msg:                    "object not found",
			},
			want: false,
		},
		{
			name: "transport failure carries no status code",
			err: &fault.WeaviateClientError{
				DerivedFromError: context.DeadlineExceeded,
				Msg:              "connection failed",
			},
			want: false,
		},
		{
			name: "unrelated error",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "no error",
			err:  nil,
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, isWeaviateAlreadyExists(test.err))
		})
	}
}

// TestVectorStoreListNamespaces pins enumeration as an interface-wide contract.
// Namespaces named from content cannot be reconstructed once the content
// changes, so without this an abandoned one is unreachable rather than merely
// unused — there is no way to ask a backend what it is still holding.
func TestVectorStoreListNamespaces(t *testing.T) {
	backends := []upsertBackend{
		{
			name:      "chromem",
			dimension: ChromemTestDimension,
			setup: func(t *testing.T) (VectorStore, context.Context, string) {
				setup := NewChromemTestSetup(t)
				t.Cleanup(func() { setup.Cleanup(t) })
				return setup.Store, setup.ctx, ChromemTestNamespace
			},
		},
		{
			name:      "weaviate",
			dimension: TestEmbeddingDim,
			setup: func(t *testing.T) (VectorStore, context.Context, string) {
				if testing.Short() {
					t.Skip("Skipping integration tests in short mode")
				}
				setup := NewTestSetup(t)
				t.Cleanup(func() { setup.Cleanup(t) })
				return setup.Store, setup.ctx, TestClassName
			},
		},
		{
			name:      "qdrant",
			dimension: QdrantTestDimension,
			setup: func(t *testing.T) (VectorStore, context.Context, string) {
				if testing.Short() {
					t.Skip("Skipping integration tests in short mode")
				}
				setup := NewQdrantTestSetup(t)
				t.Cleanup(func() { setup.Cleanup(t) })
				return setup.Store, setup.ctx, QdrantTestCollection
			},
		},
		{
			name:      "pinecone",
			dimension: PineconeTestDimension,
			setup: func(t *testing.T) (VectorStore, context.Context, string) {
				if testing.Short() {
					t.Skip("Skipping integration tests in short mode")
				}
				setup := NewPineconeTestSetup(t)
				t.Cleanup(func() { setup.Cleanup(t) })
				return setup.Store, setup.ctx, PineconeTestNamespace
			},
		},
		{
			name:      "redis",
			dimension: RedisTestDimension,
			setup: func(t *testing.T) (VectorStore, context.Context, string) {
				if testing.Short() {
					t.Skip("Skipping integration tests in short mode")
				}
				setup := NewRedisTestSetup(t)
				t.Cleanup(func() { setup.Cleanup(t) })
				return setup.Store, setup.ctx, TestNamespace
			},
		},
	}

	for _, backend := range backends {
		t.Run(backend.name, func(t *testing.T) {
			store, ctx, namespace := backend.setup(t)
			require.NoError(t, store.CreateNamespace(ctx, namespace, backend.dimension, upsertTestProperties))

			// Pinecone creates a namespace implicitly on first upsert and
			// reports only namespaces holding vectors, so CreateNamespace alone
			// leaves nothing to enumerate. Writing one record makes the
			// namespace real on every backend; it is harmless where creation
			// was already enough.
			probeID := generateUUID()
			require.NoError(t, store.Add(ctx, namespace, probeID,
				generateTestEmbedding(backend.dimension), map[string]interface{}{"marker": "listed"}))
			t.Cleanup(func() { _ = store.Delete(ctx, namespace, probeID) })

			var all []string
			var err error
			require.Eventually(t, func() bool {
				all, err = store.ListNamespaces(ctx, "")
				return err == nil && slices.Contains(all, namespace)
			}, 10*time.Second, 250*time.Millisecond,
				"namespace %q never appeared in the listing (last error: %v)", namespace, err)
			assert.Contains(t, all, namespace, "an existing namespace must be enumerable")
			assert.IsIncreasing(t, all, "results must be ordered so callers can diff them")

			// The prefix is what makes this usable: one deployment's namespaces
			// have to be separable from every other tenant of the same backend.
			matching, err := store.ListNamespaces(ctx, namespace)
			require.NoError(t, err)
			assert.Contains(t, matching, namespace)

			none, err := store.ListNamespaces(ctx, namespace+"-no-such-suffix")
			require.NoError(t, err)
			assert.Empty(t, none, "a prefix matching nothing is an empty result, not an error")
		})
	}
}
