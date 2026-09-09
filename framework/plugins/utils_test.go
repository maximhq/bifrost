package plugins

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakePluginBytes = "fake-plugin-binary-content"

// newPlainClient returns the plugin-download client with the SSRF guard swapped for a
// plain dialer (CheckRedirect/Timeout unchanged), for tests that exercise download
// mechanics (redirects, status handling, extensions) against a local httptest server -
// httptest binds to loopback, which the production dialer correctly refuses to reach.
// TestDownloadPlugin_BlocksSSRFToLoopback below verifies the guard is on by default.
func newPlainClient() *http.Client {
	client := NewPluginDownloadClient(nil)
	client.Transport = &http.Transport{
		DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	}
	return client
}

func TestDownloadPlugin_DirectDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakePluginBytes))
	}))
	defer server.Close()

	path, err := DownloadPlugin(server.URL, ".so", newPlainClient())
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

	path, err := DownloadPlugin(redirector.URL, ".so", newPlainClient())
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

	_, err := DownloadPlugin(server.URL, ".so", newPlainClient())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many redirects")
}

func TestDownloadPlugin_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := DownloadPlugin(server.URL, ".so", newPlainClient())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestDownloadPlugin_FileExtensionPreserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakePluginBytes))
	}))
	defer server.Close()

	path, err := DownloadPlugin(server.URL, ".so", newPlainClient())
	require.NoError(t, err)
	defer os.Remove(path)

	assert.Contains(t, path, ".so")
}

// TestDownloadPlugin_BlocksSSRFToLoopback verifies the production client (no allowlist)
// refuses a plugin URL pointing at a loopback address, since that's exactly the class of
// internal/metadata-endpoint target this guard exists to block.
func TestDownloadPlugin_BlocksSSRFToLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakePluginBytes))
	}))
	defer server.Close()

	_, err := DownloadPlugin(server.URL, ".so", NewPluginDownloadClient(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-public address")
}

// TestDownloadPlugin_RejectsNonHTTPScheme verifies non-http(s) schemes are rejected
// before any network call is made.
func TestDownloadPlugin_RejectsNonHTTPScheme(t *testing.T) {
	_, err := DownloadPlugin("file:///etc/passwd", ".so", NewPluginDownloadClient(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported plugin URL scheme")
}

// TestDownloadPlugin_AllowlistPermitsLoopbackWhenHostAllowlisted verifies that a plugin
// download to a private/loopback address succeeds once that exact host is allowlisted.
func TestDownloadPlugin_AllowlistPermitsLoopbackWhenHostAllowlisted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakePluginBytes))
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	allow, err := network.NewAllowlist([]string{u.Hostname()})
	require.NoError(t, err)

	path, err := DownloadPlugin(server.URL, ".so", NewPluginDownloadClient(allow))
	require.NoError(t, err)
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, fakePluginBytes, string(data))
}

// TestDownloadPlugin_AllowlistDoesNotPermitDifferentPrivateHost verifies the allowlist is
// host-specific - allowlisting a different private address does not open up all private
// targets.
func TestDownloadPlugin_AllowlistDoesNotPermitDifferentPrivateHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakePluginBytes))
	}))
	defer server.Close()

	allow, err := network.NewAllowlist([]string{"10.99.99.99"})
	require.NoError(t, err)

	_, err = DownloadPlugin(server.URL, ".so", NewPluginDownloadClient(allow))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-public address")
}
