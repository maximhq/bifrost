package openapimcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadBytes(t *testing.T) {
	_, err := LoadBytes([]byte("  \n"))
	require.ErrorContains(t, err, "empty")

	_, err = LoadBytes(make([]byte, MaxSpecBytes+1))
	require.ErrorIs(t, err, ErrSpecTooLarge)

	got, err := LoadBytes([]byte(`{"openapi":"3.0.0"}`))
	require.NoError(t, err)
	assert.Equal(t, `{"openapi":"3.0.0"}`, string(got))
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "specs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "specs", "api.yaml"), []byte("openapi: 3.0.0\n"), 0o644))
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "evil.yaml"), []byte("openapi: 3.0.0\n"), 0o644))

	got, err := LoadFile("specs/api.yaml", dir)
	require.NoError(t, err)
	assert.Equal(t, "openapi: 3.0.0\n", string(got))

	got, err = LoadFile(filepath.Join(dir, "specs", "api.yaml"), dir)
	require.NoError(t, err, "absolute path inside the config dir is fine")
	assert.NotEmpty(t, got)

	_, err = LoadFile("../"+filepath.Base(outside)+"/evil.yaml", dir)
	require.ErrorContains(t, err, "outside the config directory")

	_, err = LoadFile(filepath.Join(outside, "evil.yaml"), dir)
	require.ErrorContains(t, err, "outside the config directory")

	_, err = LoadFile("specs/missing.yaml", dir)
	require.Error(t, err)

	_, err = LoadFile("specs", dir)
	require.ErrorContains(t, err, "is a directory")

	_, err = LoadFile("specs/api.yaml", "")
	require.ErrorContains(t, err, "no config directory")
}

func TestLoadURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi.yaml":
			assert.Contains(t, r.Header.Get("Accept"), "yaml")
			_, _ = w.Write([]byte("openapi: 3.0.0\n"))
		case "/huge":
			_, _ = w.Write(make([]byte, MaxSpecBytes+10))
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)

	got, err := LoadURL(context.Background(), ts.Client(), ts.URL+"/openapi.yaml")
	require.NoError(t, err)
	assert.Equal(t, "openapi: 3.0.0\n", string(got))

	_, err = LoadURL(context.Background(), ts.Client(), ts.URL+"/huge")
	require.ErrorIs(t, err, ErrSpecTooLarge)

	_, err = LoadURL(context.Background(), ts.Client(), ts.URL+"/missing")
	require.ErrorContains(t, err, "HTTP 404")

	_, err = LoadURL(context.Background(), ts.Client(), "ftp://example.com/spec")
	require.ErrorContains(t, err, "must be an http(s) URL")

	_, err = LoadURL(context.Background(), nil, "   ")
	require.ErrorContains(t, err, "empty")
}

func TestResolveSpecPriority(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "api.json"), []byte(`{"from":"file"}`), 0o644))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"from":"url"}`))
	}))
	t.Cleanup(ts.Close)

	cfg := &schemas.MCPOpenAPIConfig{Spec: `{"from":"inline"}`, SpecFile: str("api.json"), SpecURL: str(ts.URL)}
	data, source, err := ResolveSpec(context.Background(), cfg, ts.Client(), dir)
	require.NoError(t, err)
	assert.Equal(t, "inline", source)
	assert.True(t, strings.Contains(string(data), "inline"))

	cfg.Spec = ""
	data, source, err = ResolveSpec(context.Background(), cfg, ts.Client(), dir)
	require.NoError(t, err)
	assert.Equal(t, "file", source)
	assert.Contains(t, string(data), "file")

	cfg.SpecFile = nil
	data, source, err = ResolveSpec(context.Background(), cfg, ts.Client(), dir)
	require.NoError(t, err)
	assert.Equal(t, "url", source)
	assert.Contains(t, string(data), "url")

	cfg.SpecURL = nil
	_, _, err = ResolveSpec(context.Background(), cfg, ts.Client(), dir)
	require.ErrorContains(t, err, "no spec")

	_, _, err = ResolveSpec(context.Background(), nil, nil, "")
	require.Error(t, err)
}
