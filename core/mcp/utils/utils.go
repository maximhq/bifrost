package utils

import (
	"net/http"

	"github.com/maximhq/bifrost/core/schemas"
)

// GetHeadersForToolExecution sets additional headers for tool execution.
// It returns the headers for the tool execution.
func GetHeadersForToolExecution(ctx *schemas.BifrostContext, client *schemas.MCPClientState) http.Header {
	if ctx == nil || client == nil || client.ExecutionConfig == nil {
		return make(http.Header)
	}
	headers := make(http.Header)
	if client.ExecutionConfig.Headers != nil {
		for key, value := range client.ExecutionConfig.Headers {
			headers.Add(key, value.GetValue())
		}
	}
	// Give priority to extra headers in the context
	if extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyMCPExtraHeaders).(map[string][]string); ok {
		filteredHeaders := make(http.Header)
		for key, values := range extraHeaders {
			if client.ExecutionConfig.AllowedExtraHeaders.IsAllowed(key) {
				for i, value := range values {
					if i == 0 {
						filteredHeaders.Set(key, value)
					} else {
						filteredHeaders.Add(key, value)
					}
				}
			}
		}
		// Add the filtered headers to the headers
		if len(filteredHeaders) > 0 {
			for k, values := range filteredHeaders {
				for i, v := range values {
					if i == 0 {
						headers.Set(k, v)
					} else {
						headers.Add(k, v)
					}
				}
			}
		}
	}
	return headers
}
