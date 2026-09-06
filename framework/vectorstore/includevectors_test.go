package vectorstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Paging reads carry vectors only on request: a listing that wants ids must
// not pay for thousands of floats per row, and a clustering job that wants
// the vectors must not have to fetch each entry a second time.
func TestIncludeVectorsIsOptIn(t *testing.T) {
	require.False(t, IncludeVectorsRequested(context.Background()))
	require.False(t, IncludeVectorsRequested(nil))
	require.True(t, IncludeVectorsRequested(WithIncludeVectors(context.Background())))
	require.True(t, IncludeVectorsRequested(WithIncludeVectors(nil)))
}

// GraphQL decodes numbers as float64 inside []interface{}; anything else in
// the list means the backend did not return a vector, and nil is the honest
// answer rather than a partial one.
func TestVectorFromAdditional(t *testing.T) {
	require.Equal(t, []float32{0.5, -1, 2}, VectorFromAdditional([]interface{}{0.5, float64(-1), 2}))
	require.Nil(t, VectorFromAdditional(nil))
	require.Nil(t, VectorFromAdditional("not a vector"))
	require.Nil(t, VectorFromAdditional([]interface{}{}))
	require.Nil(t, VectorFromAdditional([]interface{}{1.0, "x"}))
}
