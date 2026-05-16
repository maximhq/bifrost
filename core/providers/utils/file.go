package utils

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"github.com/valyala/fasthttp"
)

// FileBytesToBase64DataURL converts raw file bytes to base64 data URL format
func FileBytesToBase64DataURL(fileBytes []byte) string {
	mimeType := http.DetectContentType(fileBytes)
	b64Data := base64.StdEncoding.EncodeToString(fileBytes)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, b64Data)
}

// DownloadURLToBase64 downloads content from a URL and returns it as a base64-encoded string.
func DownloadURLToBase64(ctx context.Context, fileURL string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("context is required")
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	req.SetRequestURI(fileURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetUserAgent("bifrost-fetch/1")

	type downloadResult struct {
		data string
		err  error
	}
	resultCh := make(chan downloadResult, 1)

	go func() {
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)

		client := &fasthttp.Client{
			ReadTimeout:         20 * time.Second,
			WriteTimeout:        10 * time.Second,
			MaxResponseBodySize: 25 * 1024 * 1024,
		}
		if err := client.DoRedirects(req, resp, 10); err != nil {
			resultCh <- downloadResult{err: fmt.Errorf("failed to download URL: %w", err)}
			return
		}
		if resp.StatusCode() < fasthttp.StatusOK || resp.StatusCode() >= fasthttp.StatusMultipleChoices {
			resultCh <- downloadResult{err: fmt.Errorf("failed to download URL: status=%d", resp.StatusCode())}
			return
		}
		body := resp.Body()
		resultCh <- downloadResult{data: base64.StdEncoding.EncodeToString(body)}
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-resultCh:
		return result.data, result.err
	}
}
