package sgl

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// sglFlatError matches sglang's flat error envelope:
//
//	{"object":"error","message":"...","type":"BadRequestError","code":400}
//
// See python/sglang/srt/entrypoints/openai/serving_base.py upstream
// (function create_error_response, lines ~209-225).
type sglFlatError struct {
	Object  string      `json:"object"`
	Message string      `json:"message"`
	Type    string      `json:"type"`
	Code    interface{} `json:"code"`
}

// ParseSGLError parses sglang error responses.
//
// It handles both error envelope shapes used by sglang and its forks:
//   - Flat:    {"object":"error","message":"...","type":"BadRequestError","code":400}
//   - Wrapped: {"error":{"message":"...","type":"...","code":"..."}}
//
// Well-known message substrings are mapped to OpenAI-style codes:
//   - "longer than the model's context length" -> context_length_exceeded / invalid_request_error
//   - "out of memory"                          -> out_of_memory          / server_error
//   - "model is not loaded"                    -> model_not_found        / invalid_request_error
//
// The raw sglang `message` is always preserved on the returned BifrostError so
// callers see the actual server explanation rather than a generic "status N".
func ParseSGLError(resp *fasthttp.Response) *schemas.BifrostError {
	// Delegate to the shared OpenAI-shape parser first. This handles the
	// wrapped {"error":{...}} envelope, decodes gzip bodies, fills in
	// StatusCode / ExtraFields, and applies sane HTTP-status fallbacks.
	// The decoded body is stashed on ExtraFields.RawResponse, so we read
	// from there rather than re-snapshotting resp.Body() (which would be
	// the still-compressed bytes if Content-Encoding was gzip).
	bifrostErr := openai.ParseOpenAIError(resp)
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}

	// If the wrapped parser did not pick up a useful message, try sglang's
	// flat envelope. We treat the generic "provider API error (status N)" /
	// "provider API error" fallback as "no useful message" for this purpose.
	currentMsg := strings.TrimSpace(bifrostErr.Error.Message)
	wrappedHadMessage := currentMsg != "" && !strings.HasPrefix(currentMsg, "provider API error")
	if !wrappedHadMessage {
		if flat, ok := extractFlatSGLErrorFromRaw(bifrostErr.ExtraFields.RawResponse); ok && flat.Message != "" {
			bifrostErr.Error.Message = flat.Message
			if flat.Type != "" {
				t := flat.Type
				bifrostErr.Error.Type = &t
			}
			if flat.Code != nil {
				if codeStr := codeToString(flat.Code); codeStr != "" {
					bifrostErr.Error.Code = &codeStr
				}
			}
		}
	}

	msg := bifrostErr.Error.Message
	switch {
	case strings.Contains(msg, "longer than the model's context length"):
		setSGLErrorCode(bifrostErr.Error, "context_length_exceeded", "invalid_request_error")
	case strings.Contains(msg, "out of memory"):
		setSGLErrorCode(bifrostErr.Error, "out_of_memory", "server_error")
	case strings.Contains(msg, "model is not loaded"):
		setSGLErrorCode(bifrostErr.Error, "model_not_found", "invalid_request_error")
	}

	return bifrostErr
}

func setSGLErrorCode(field *schemas.ErrorField, code, typ string) {
	c := code
	t := typ
	field.Code = &c
	field.Type = &t
}

// extractFlatSGLErrorFromRaw pulls a flat sglang error envelope out of the
// already-decoded body that openai.ParseOpenAIError stashed on
// ExtraFields.RawResponse. RawResponse may be a string (when JSON parsing
// failed upstream) or a map[string]interface{} (the parsed body). We handle
// both so a gzipped 4xx still yields the sglang message/type/code rather
// than just the generic HTTP-status fallback.
func extractFlatSGLErrorFromRaw(raw interface{}) (sglFlatError, bool) {
	switch v := raw.(type) {
	case string:
		var flat sglFlatError
		if err := sonic.Unmarshal([]byte(v), &flat); err == nil {
			return flat, true
		}
	case map[string]interface{}:
		flat := sglFlatError{}
		if s, ok := v["object"].(string); ok {
			flat.Object = s
		}
		if s, ok := v["message"].(string); ok {
			flat.Message = s
		}
		if s, ok := v["type"].(string); ok {
			flat.Type = s
		}
		if c, ok := v["code"]; ok {
			flat.Code = c
		}
		return flat, true
	}
	return sglFlatError{}, false
}

func codeToString(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%d", int(x))
	case float32:
		return fmt.Sprintf("%d", int(x))
	case int:
		return fmt.Sprintf("%d", x)
	case int64:
		return fmt.Sprintf("%d", x)
	}
	return ""
}
