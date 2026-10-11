package utils

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// EscapeResourceID validates a provider object ID as one opaque URL path segment and escapes it.
// fasthttp percent-decodes and normalizes outbound paths, so escaping alone does not contain an ID.
func EscapeResourceID(id, field string) (string, *schemas.BifrostError) {
	if err := validateOpaquePathSegment(id); err != nil {
		return "", NewBifrostBadRequestError(fmt.Sprintf("invalid %s: %v", field, err))
	}
	return url.PathEscape(id), nil
}

// EscapeResourcePath validates a namespaced ID ("openai/gpt-oss-120b") segment by segment and escapes each.
// Every segment passes the EscapeResourceID checks, so no empty, "." or ".." segment reaches fasthttp.
func EscapeResourcePath(id, field string) (string, *schemas.BifrostError) {
	segments := strings.Split(id, "/")
	for i, segment := range segments {
		if err := validateOpaquePathSegment(segment); err != nil {
			return "", NewBifrostBadRequestError(fmt.Sprintf("invalid %s: %v", field, err))
		}
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/"), nil
}

func validateOpaquePathSegment(id string) error {
	if id == "" {
		return fmt.Errorf("must not be empty")
	}
	if id == "." || id == ".." {
		return fmt.Errorf("dot segments are not allowed")
	}
	for _, r := range id {
		switch r {
		case '/', '\\', '?', '#', '%':
			return fmt.Errorf("path delimiters and percent-encoding are not allowed")
		}
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("control characters are not allowed")
		}
	}
	return nil
}
