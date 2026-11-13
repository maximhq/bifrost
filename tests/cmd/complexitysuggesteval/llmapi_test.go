package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAndDot(t *testing.T) {
	v := normalize([]float32{3, 4})
	assert.InDelta(t, 0.6, v[0], 1e-6)
	assert.InDelta(t, 0.8, v[1], 1e-6)
	assert.InDelta(t, 1, dot(v, v), 1e-6)
	zero := []float32{0, 0}
	assert.Equal(t, zero, normalize(zero), "a zero vector is returned unchanged")
	assert.InDelta(t, 11, dot([]float32{1, 2}, []float32{3, 4}), 1e-6)
}

func TestParallelRunsEveryIndex(t *testing.T) {
	seen := make([]atomic.Bool, 50)
	require.NoError(t, parallel(len(seen), 4, func(i int) error {
		seen[i].Store(true)
		return nil
	}))
	for i := range seen {
		assert.True(t, seen[i].Load(), i)
	}
}

func TestParallelStopsAfterFirstError(t *testing.T) {
	var calls atomic.Int32
	boom := errors.New("boom")
	err := parallel(100, 1, func(i int) error {
		calls.Add(1)
		return boom
	})
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, int32(1), calls.Load(), "no work is started once an error is recorded")
}

func TestSaveKeepsCacheDirtyWhenRenameFails(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cache.gob")
	// A non-empty directory at the cache path makes the rename fail.
	require.NoError(t, os.MkdirAll(filepath.Join(target, "x"), 0o755))
	c := newAPIClient("http://unused", "", "m", "m", target)
	c.cache.Chats["k"] = "v"
	c.dirty = true
	require.Error(t, c.save())
	assert.True(t, c.dirty, "a failed save stays retryable")

	require.NoError(t, os.RemoveAll(target))
	require.NoError(t, c.save())
	assert.False(t, c.dirty)
	assert.Equal(t, "v", newAPIClient("http://unused", "", "m", "m", target).cache.Chats["k"])
}

func embedServer(t *testing.T, body string) *apiClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return newAPIClient(srv.URL, "", "m", "m", filepath.Join(t.TempDir(), "cache.gob"))
}

func TestEmbedMapsResultsByIndex(t *testing.T) {
	c := embedServer(t, `{"data":[{"index":1,"embedding":[0,2]},{"index":0,"embedding":[3,0]}]}`)
	got, err := c.embed(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	assert.Equal(t, [][]float32{{1, 0}, {0, 1}}, got)
}

func TestEmbedRejectsInvalidResults(t *testing.T) {
	for name, body := range map[string]string{
		"negative index":  `{"data":[{"index":-1,"embedding":[1]},{"index":1,"embedding":[1]}]}`,
		"index too large": `{"data":[{"index":0,"embedding":[1]},{"index":2,"embedding":[1]}]}`,
		"duplicate index": `{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[1]}]}`,
		"empty vector":    `{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[]}]}`,
		"short response":  `{"data":[{"index":0,"embedding":[1]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := embedServer(t, body)
			_, err := c.embed(context.Background(), []string{"a", "b"})
			assert.Error(t, err)
			assert.Empty(t, c.cache.Embeddings, "nothing is cached from a rejected response")
			assert.False(t, c.dirty)
		})
	}
}
