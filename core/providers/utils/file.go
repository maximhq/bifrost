package utils

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
)

// FileBytesToBase64DataURL converts raw file bytes to base64 data URL format
func FileBytesToBase64DataURL(fileBytes []byte) string {
	mimeType := http.DetectContentType(fileBytes)
	b64Data := base64.StdEncoding.EncodeToString(fileBytes)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, b64Data)
}

// DownloadURLToBase64 downloads content from a URL and returns it as a base64-encoded string.
func DownloadURLToBase64(ctx context.Context, fileURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download URL: status=%d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read URL body: %w", err)
	}

	return base64.StdEncoding.EncodeToString(body), nil
}
