package plugins

import (
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/valyala/fasthttp"
)

var (
	ErrPluginNotFound = fmt.Errorf("plugin not found")
)

// pluginDownloadClient is a fasthttp client with a larger read buffer to handle
// responses with large headers.
var pluginDownloadClient = &fasthttp.Client{
	ReadBufferSize: 64 * 1024, // 64KB, matches the bifrost HTTP server setting
}

// DownloadPlugin downloads a plugin from a URL and returns the local file path
func DownloadPlugin(pluginURL string, extension string) (string, error) {
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
			return "", err
		}

		statusCode := response.StatusCode()
		if statusCode == fasthttp.StatusOK {
			break
		}
		if statusCode >= 300 && statusCode < 400 {
			if i == maxRedirects {
				return "", fmt.Errorf("too many redirects downloading plugin")
			}
			location := string(response.Header.Peek("Location"))
			if location == "" {
				return "", fmt.Errorf("redirect response missing Location header: HTTP %d", statusCode)
			}
			loc, err := url.Parse(location)
			if err != nil {
				return "", fmt.Errorf("invalid Location header %q: %w", location, err)
			}
			base, err := url.Parse(currentURL)
			if err != nil {
				return "", fmt.Errorf("invalid request URL %q: %w", currentURL, err)
			}
			currentURL = base.ResolveReference(loc).String()
			continue
		}
		return "", fmt.Errorf("failed to download plugin: HTTP %d", statusCode)
	}

	// Decompress the response body if it was gzip/deflate compressed
	// BodyUncompressed handles both gzip and deflate encodings based on Content-Encoding header
	body, err := response.BodyUncompressed()
	if err != nil {
		return "", fmt.Errorf("failed to decompress response body: %w", err)
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
