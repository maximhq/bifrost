package plugins

import (
	"fmt"
	"os"
	"time"

	"github.com/valyala/fasthttp"
)

var (
	ErrPluginNotFound = fmt.Errorf("plugin not found")
)

// DownloadPlugin downloads a plugin from a URL and returns the local file path
func DownloadPlugin(url string, extension string) (string, error) {
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	response := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(response)

	req.SetRequestURI(url)
	req.Header.SetMethod(fasthttp.MethodGet)
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	err := fasthttp.DoTimeout(req, response, 120*time.Second)
	if err != nil {
		return "", err
	}

	if response.StatusCode() != fasthttp.StatusOK {
		return "", fmt.Errorf("failed to download plugin: %d", response.StatusCode())
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
