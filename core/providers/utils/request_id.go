package utils

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ExtractProviderRequestID returns a trimmed request ID from a response header.
// Values over the bounded log-column size are ignored rather than truncated.
func ExtractProviderRequestID(headers map[string]string, headerName string) string {
	return extractProviderRequestID(headers, headerName, nil)
}

// ExtractProviderRequestIDWithLogger returns a trimmed request ID from a response
// header and warns when a non-empty value is too large to persist safely. The
// value itself is never logged because provider response headers may contain
// sensitive or customer-controlled data.
func ExtractProviderRequestIDWithLogger(headers map[string]string, headerName string, logger schemas.Logger) string {
	return extractProviderRequestID(headers, headerName, logger)
}

func extractProviderRequestID(headers map[string]string, headerName string, logger schemas.Logger) string {
	if headerName == "" {
		return ""
	}
	for key, value := range headers {
		if strings.EqualFold(key, headerName) {
			value = strings.TrimSpace(value)
			if value == "" {
				return ""
			}
			if len(value) > schemas.MaxProviderRequestIDLength {
				if logger != nil {
					logger.Warn(
						"provider request ID response header %q ignored because its value is %d bytes, exceeding the %d-byte limit",
						strings.ToLower(strings.TrimSpace(headerName)),
						len(value),
						schemas.MaxProviderRequestIDLength,
					)
				}
				return ""
			}
			return value
		}
	}
	return ""
}
