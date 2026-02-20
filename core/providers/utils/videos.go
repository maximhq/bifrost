package utils

import (
	"net/url"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// StripVideoIDProviderSuffix removes ":<provider>" from a video ID if present.
func StripVideoIDProviderSuffix(videoID string, provider schemas.ModelProvider) string {
	suffix := ":" + string(provider)
	stripped := strings.TrimSuffix(videoID, suffix)
	// URL decode the ID to restore original characters (e.g., %2F -> /)
	if decoded, err := url.PathUnescape(stripped); err == nil {
		return decoded
	}
	return stripped
}

// AddVideoIDProviderSuffix ensures a video ID is scoped as "<id>:<provider>".
func AddVideoIDProviderSuffix(videoID string, provider schemas.ModelProvider) string {
	if videoID == "" {
		return videoID
	}
	suffix := ":" + string(provider)
	if strings.HasSuffix(videoID, suffix) {
		return videoID
	}
	// URL-encode the video ID to make it safe for URL paths
	// This converts / to %2F and other special characters
	escapedVideoID := url.PathEscape(videoID)
	return escapedVideoID + suffix
}
