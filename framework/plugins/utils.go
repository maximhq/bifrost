package plugins

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/maximhq/bifrost/core/network"
)

var (
	ErrPluginNotFound = fmt.Errorf("plugin not found")
)

// maxPluginDownloadBytes bounds how much a plugin download response body can be, so a
// malicious or misbehaving server can't exhaust memory with an unbounded response. Sized
// generously above typical -buildmode=plugin .so sizes (which bundle their dependencies).
const maxPluginDownloadBytes int64 = 200 * 1024 * 1024

// NewPluginDownloadClient builds the SSRF-hardened HTTP client used to download plugin
// binaries, the same way as core/providers/utils.FetchAndEncodeURL: only http/https schemes
// are accepted, and the dialer rejects connections to loopback, private, CGNAT, link-local,
// and unspecified addresses (including IPv4 targets smuggled inside IPv6 transition
// addresses). The IP check runs at dial time, not just at lookup time, so DNS rebinding does
// not bypass it. Redirect targets get the same treatment.
//
// allow is an optional operator-configured allowlist of internal hosts/CIDRs permitted to
// bypass the default private-network block (see ServerConfig.PluginDownloadPrivateAllowlist);
// pass nil for the fully locked-down default.
func NewPluginDownloadClient(allow *network.Allowlist) *http.Client {
	return &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			DialContext: network.SSRFSafeDialContextWithAllowlist(10*time.Second, allow),
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("blocked redirect to unsupported scheme %q", req.URL.Scheme)
			}
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects downloading plugin")
			}
			return nil
		},
	}
}

// DownloadPlugin downloads a plugin from a URL using client and returns the local file path
func DownloadPlugin(pluginURL string, extension string, client *http.Client) (string, error) {
	parsed, err := url.Parse(pluginURL)
	if err != nil {
		return "", fmt.Errorf("invalid plugin URL %q: %w", pluginURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported plugin URL scheme %q (only http/https allowed)", parsed.Scheme)
	}

	req, err := http.NewRequest(http.MethodGet, pluginURL, nil)
	if err != nil {
		return "", fmt.Errorf("invalid plugin URL %q: %w", pluginURL, err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download plugin: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download plugin: HTTP %d", resp.StatusCode)
	}

	// net/http's Transport negotiates and transparently decompresses gzip on our behalf
	// here (no Accept-Encoding was set above), so body is already decompressed.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPluginDownloadBytes+1))
	if err != nil {
		return "", fmt.Errorf("failed to read plugin body: %w", err)
	}
	if int64(len(body)) > maxPluginDownloadBytes {
		return "", fmt.Errorf("plugin at %q exceeds %d-byte limit", pluginURL, maxPluginDownloadBytes)
	}

	// Create a unique temporary file for the plugin
	tempFile, err := os.CreateTemp(os.TempDir(), "bifrost-plugin-*"+extension)
	if err != nil {
		return "", fmt.Errorf("failed to create temporary file: %w", err)
	}
	tempPath := tempFile.Name()

	// Write the downloaded body to the temporary file
	_, err = tempFile.Write(body)
	if err != nil {
		tempFile.Close()
		os.Remove(tempPath)
		return "", fmt.Errorf("failed to write plugin to temporary file: %w", err)
	}

	// Close the file
	err = tempFile.Close()
	if err != nil {
		os.Remove(tempPath)
		return "", fmt.Errorf("failed to close temporary file: %w", err)
	}

	// Set file permissions to be executable (for .so files)
	if extension == ".so" {
		err = os.Chmod(tempPath, 0755)
		if err != nil {
			os.Remove(tempPath)
			return "", fmt.Errorf("failed to set executable permissions on plugin: %w", err)
		}
	}

	return tempPath, nil
}
