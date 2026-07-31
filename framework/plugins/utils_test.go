package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakePluginBytes = "fake-plugin-binary-content"

func resetPluginCacheForTest() {
	pluginCacheMu.Lock()
	defer pluginCacheMu.Unlock()
	for _, e := range pluginCache {
		if e.stablePath != "" {
			os.Remove(e.stablePath)
		}
	}
	pluginCache = make(map[string]*pluginCacheEntry)
}

func TestDownloadPlugin_DirectDownload(t *testing.T) {
	resetPluginCacheForTest()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakePluginBytes))
	}))
	defer server.Close()

	path, contentHash, err := DownloadPlugin(server.URL, ".so")
	require.NoError(t, err)
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, fakePluginBytes, string(data))
	assert.Len(t, contentHash, 64) // SHA-256 hex is 64 chars

	// Verify stable path format
	assert.True(t, strings.HasSuffix(path, ".so"))
	assert.Contains(t, path, "bifrost-plugin-")
}

func TestDownloadPlugin_FollowsRedirect(t *testing.T) {
	resetPluginCacheForTest()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakePluginBytes))
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	path, _, err := DownloadPlugin(redirector.URL, ".so")
	require.NoError(t, err)
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, fakePluginBytes, string(data))
}

func TestDownloadPlugin_TooManyRedirects(t *testing.T) {
	resetPluginCacheForTest()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL, http.StatusFound)
	}))
	defer server.Close()

	_, _, err := DownloadPlugin(server.URL, ".so")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many redirects")
}

func TestDownloadPlugin_NonOKStatus(t *testing.T) {
	resetPluginCacheForTest()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, _, err := DownloadPlugin(server.URL, ".so")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestDownloadPlugin_FileExtensionPreserved(t *testing.T) {
	resetPluginCacheForTest()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakePluginBytes))
	}))
	defer server.Close()

	path, _, err := DownloadPlugin(server.URL, ".so")
	require.NoError(t, err)
	defer os.Remove(path)

	assert.Contains(t, path, ".so")
}

func TestDownloadPlugin_StablePathByContentHash(t *testing.T) {
	resetPluginCacheForTest()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakePluginBytes))
	}))
	defer server.Close()

	path1, hash1, err := DownloadPlugin(server.URL, ".so")
	require.NoError(t, err)
	defer os.Remove(path1)

	path2, hash2, err := DownloadPlugin(server.URL, ".so")
	require.NoError(t, err)
	defer os.Remove(path2)

	assert.Equal(t, hash1, hash2)
	assert.Equal(t, path1, path2, "same content should produce same stable path")

	expectedHash := sha256.Sum256([]byte(fakePluginBytes))
	assert.Equal(t, hex.EncodeToString(expectedHash[:]), hash1)
}

func TestDownloadPlugin_DifferentContentDifferentPath(t *testing.T) {
	resetPluginCacheForTest()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("plugin-v1-content"))
	}))
	defer server.Close()

	path1, hash1, err := DownloadPlugin(server.URL, ".so")
	require.NoError(t, err)
	defer os.Remove(path1)

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("plugin-v2-content"))
	}))
	defer server2.Close()

	path2, hash2, err := DownloadPlugin(server2.URL, ".so")
	require.NoError(t, err)
	defer os.Remove(path2)

	assert.NotEqual(t, hash1, hash2)
	assert.NotEqual(t, path1, path2)
	assert.Equal(t, "plugin-v1-content", readFile(t, path1))
	assert.Equal(t, "plugin-v2-content", readFile(t, path2))
}

func TestDownloadPlugin_IdenticalContentDifferentURLs(t *testing.T) {
	resetPluginCacheForTest()
	// Two different URLs serving identical content.
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakePluginBytes))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakePluginBytes))
	}))
	defer server2.Close()

	path1, hash1, err := DownloadPlugin(server1.URL, ".so")
	require.NoError(t, err)
	defer os.Remove(path1)

	path2, hash2, err := DownloadPlugin(server2.URL, ".so")
	require.NoError(t, err)
	defer os.Remove(path2)

	// Same content → same hash → same stable path.
	assert.Equal(t, hash1, hash2)
	assert.Equal(t, path1, path2)

	// Only one file on disk.
	_, err = os.Stat(path2)
	require.NoError(t, err)
}

func TestDownloadPlugin_PathContainsHash(t *testing.T) {
	resetPluginCacheForTest()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakePluginBytes))
	}))
	defer server.Close()

	path, hash, err := DownloadPlugin(server.URL, ".so")
	require.NoError(t, err)
	defer os.Remove(path)

	// The stable path should contain the hash as a path component.
	assert.True(t, strings.Contains(path, hash),
		"stable path %q should contain content hash %q", path, hash)
	assert.Equal(t, filepath.Join(os.TempDir(), "bifrost-plugin-"+hash+".so"), path)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}
