package plugins

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakePluginBytes = "fake-plugin-binary-content"

func TestDownloadPlugin_DirectDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakePluginBytes))
	}))
	defer server.Close()

	path, err := DownloadPlugin(server.URL, ".so")
	require.NoError(t, err)
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, fakePluginBytes, string(data))
}

func TestDownloadPlugin_FollowsRedirect(t *testing.T) {
	// Final destination
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakePluginBytes))
	}))
	defer target.Close()

	// Redirect server (simulates GitHub → S3)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	path, err := DownloadPlugin(redirector.URL, ".so")
	require.NoError(t, err)
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, fakePluginBytes, string(data))
}

func TestDownloadPlugin_TooManyRedirects(t *testing.T) {
	// Server that always redirects to itself
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL, http.StatusFound)
	}))
	defer server.Close()

	_, err := DownloadPlugin(server.URL, ".so")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many redirects")
}

func TestDownloadPlugin_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := DownloadPlugin(server.URL, ".so")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestDownloadPlugin_FileExtensionPreserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakePluginBytes))
	}))
	defer server.Close()

	path, err := DownloadPlugin(server.URL, ".so")
	require.NoError(t, err)
	defer os.Remove(path)

	assert.Contains(t, path, ".so")
}
