package opencode

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// opencodeErrorBody is the JSON envelope returned by Opencode Zen/Go API errors.
// Format: {"type": "error", "error": {"type": "...", "message": "..."}}
type opencodeErrorBody struct {
	Type  string            `json:"type"`
	Error opencodeErrorInner `json:"error"`
}

type opencodeErrorInner struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// parseOpencodeError parses Opencode-specific error responses.
// Opencode uses {"type":"error","error":{"type":"...","message":"..."}} instead
// of OpenAI's {"error":{"message":"...","type":"...","code":...}}.
func parseOpencodeError(resp *fasthttp.Response) *schemas.BifrostError {
	var bifrostErr schemas.BifrostError

	// First, let the generic handler parse HTTP status and set base fields.
	_ = providerUtils.HandleProviderAPIError(resp, &bifrostErr)

	// Ensure Error is non-nil before accessing its fields.
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}

	// Then overlay Opencode-specific error details from the body.
	if body := resp.Body(); len(body) > 0 {
		var parsed opencodeErrorBody
		if err := sonic.Unmarshal(body, &parsed); err == nil && parsed.Type == "error" {
			if parsed.Error.Message != "" {
				bifrostErr.Error.Message = parsed.Error.Message
			}
			if parsed.Error.Type != "" {
				bifrostErr.Error.Type = &parsed.Error.Type
			}
		}
	}

	// Ensure we always have a non-empty error message.
	if strings.TrimSpace(bifrostErr.Error.Message) == "" {
		if bifrostErr.StatusCode != nil {
			bifrostErr.Error.Message = fmt.Sprintf("provider API error (status %d)", *bifrostErr.StatusCode)
		} else {
			bifrostErr.Error.Message = "provider API error"
		}
	}

	return &bifrostErr
}
