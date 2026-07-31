package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
)

var (
	ErrPluginNotFound = fmt.Errorf("plugin not found")
)

// pluginCacheEntry holds state for a cached plugin download.
// The key is the content hash (SHA-256 of the .so bytes), which means
// the same binary downloaded from different URLs maps to one cache entry.
type pluginCacheEntry struct {
	stablePath   string
	contentHash  string
	loadedPlugin any
}

// pluginCache maps content hash → entry. Keyed by SHA-256, not URL.
var (
	pluginCache   = make(map[string]*pluginCacheEntry)
	pluginCacheMu sync.RWMutex
)



func getPluginCacheEntry(hash string) (*pluginCacheEntry, bool) {
	pluginCacheMu.RLock()
	defer pluginCacheMu.RUnlock()
	e, ok := pluginCache[hash]
	return e, ok
}

func setPluginCacheStablePath(hash, stablePath string) {
	pluginCacheMu.Lock()
	defer pluginCacheMu.Unlock()
	pluginCache[hash] = &pluginCacheEntry{
		stablePath:  stablePath,
		contentHash: hash,
	}
}

func getPluginCacheHashFromPath(stablePath string) (hash string, ok bool) {
	pluginCacheMu.RLock()
	defer pluginCacheMu.RUnlock()
	for h, e := range pluginCache {
		if e.stablePath == stablePath {
			return h, true
		}
	}
	return "", false
}

func setPluginCacheLoaded(hash, stablePath string, loadedPlugin any) {
	pluginCacheMu.Lock()
	defer pluginCacheMu.Unlock()
	pluginCache[hash] = &pluginCacheEntry{
		stablePath:   stablePath,
		contentHash:  hash,
		loadedPlugin: loadedPlugin,
	}
}

// pluginDownloadClient is a fasthttp client with a larger read buffer to handle
// responses with large headers.
var pluginDownloadClient = &fasthttp.Client{
	ReadBufferSize: 64 * 1024, // 64KB, matches the bifrost HTTP server setting
}

// DownloadPlugin downloads a plugin from a URL and returns the local file path
// and its content hash. The returned path is stable: it is based on the SHA-256
// of the plugin bytes (not a random temp name), so the same binary always maps
// to the same path. This prevents "plugin already loaded" errors on reload when
// Go's plugin.Open sees the same .so binary at a different temp path.
func DownloadPlugin(pluginURL string, extension string) (string, string, error) {
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	response := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(response)

	req.Header.SetMethod(fasthttp.MethodGet)
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	const maxRedirects = 5
	currentURL := pluginURL
	for i := 0; i <= maxRedirects; i++ {
		req.SetRequestURI(currentURL)
		if i > 0 {
			response.Reset()
		}

		if err := pluginDownloadClient.DoTimeout(req, response, 120*time.Second); err != nil {
			return "", "", err
		}

		statusCode := response.StatusCode()
		if statusCode == fasthttp.StatusOK {
			break
		}
		if statusCode >= 300 && statusCode < 400 {
			if i == maxRedirects {
				return "", "", fmt.Errorf("too many redirects downloading plugin")
			}
			location := string(response.Header.Peek("Location"))
			if location == "" {
				return "", "", fmt.Errorf("redirect response missing Location header: HTTP %d", statusCode)
			}
			loc, err := url.Parse(location)
			if err != nil {
				return "", "", fmt.Errorf("invalid Location header %q: %w", location, err)
			}
			base, err := url.Parse(currentURL)
			if err != nil {
				return "", "", fmt.Errorf("invalid request URL %q: %w", currentURL, err)
			}
			currentURL = base.ResolveReference(loc).String()
			continue
		}
		return "", "", fmt.Errorf("failed to download plugin: HTTP %d", statusCode)
	}

	// Decompress the response body if it was gzip/deflate compressed
	// BodyUncompressed handles both gzip and deflate encodings based on Content-Encoding header
	body, err := response.BodyUncompressed()
	if err != nil {
		return "", "", fmt.Errorf("failed to decompress response body: %w", err)
	}

	hash := sha256.Sum256(body)
	contentHash := hex.EncodeToString(hash[:])
	stablePath := filepath.Join(os.TempDir(), "bifrost-plugin-"+contentHash+extension)

	// Check the in-memory cache first. If the same content hash was already
	// downloaded, return the existing stable path — no file I/O needed.
	if existing, ok := getPluginCacheEntry(contentHash); ok && existing.stablePath != "" {
		if _, err := os.Stat(existing.stablePath); err == nil {
			return existing.stablePath, contentHash, nil
		}
	}

	// Also check whether the stable file already exists on disk from a previous
	// run (e.g. server restart). This handles the restart case without a download.
	if _, err := os.Stat(stablePath); err == nil {
		setPluginCacheStablePath(contentHash, stablePath)
		return stablePath, contentHash, nil
	}

	// Create a unique temporary file for the plugin and atomically rename it to
	// the stable path so the same binary always maps to the same path.
	tempFile, err := os.CreateTemp(os.TempDir(), "bifrost-plugin-*"+extension)
	if err != nil {
		return "", "", fmt.Errorf("failed to create temporary file: %w", err)
	}
	tempPath := tempFile.Name()

	// Write the downloaded body to the temporary file
	_, err = tempFile.Write(body)
	if err != nil {
		tempFile.Close()
		os.Remove(tempPath)
		return "", "", fmt.Errorf("failed to write plugin to temporary file: %w", err)
	}

	// Close the file
	err = tempFile.Close()
	if err != nil {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("failed to close temporary file: %w", err)
	}

	// Set file permissions to be executable (for .so files)
	if extension == ".so" {
		err = os.Chmod(tempPath, 0755)
		if err != nil {
			os.Remove(tempPath)
			return "", "", fmt.Errorf("failed to set executable permissions on plugin: %w", err)
		}
	}

	// Atomically rename to the stable content-hash-based path.
	if err := os.Rename(tempPath, stablePath); err != nil {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("failed to rename plugin to stable path: %w", err)
	}

	setPluginCacheStablePath(contentHash, stablePath)

	return stablePath, contentHash, nil
}