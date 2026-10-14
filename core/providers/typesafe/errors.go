package typesafe

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseTypesafeError parses a Typesafe error HTTP response into a BifrostError.
// The upstream status code, the native error_type, and the message are
// preserved for the shared shape, and the body is kept verbatim so the native
// /typesafe route can relay it unchanged.
func parseTypesafeError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp TypesafeError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)

	message := errorResp.Message
	if message == "" && errorResp.Error != nil {
		message = errorResp.Error.Message
	}
	detailMessage, errorType := describeTypesafeDetail(errorResp.Detail)
	if message == "" {
		message = detailMessage
	}
	// A Cloudflare Workers AI envelope carries its reasons in errors[]. Its body is
	// not a Typesafe error, so the native route rebuilds one instead of relaying it.
	envelope := message == "" && len(errorResp.Errors) > 0
	if envelope {
		messages := make([]string, 0, len(errorResp.Errors))
		for _, entry := range errorResp.Errors {
			if entry.Message != "" {
				messages = append(messages, entry.Message)
			}
		}
		message = strings.Join(messages, "; ")
	}

	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	if message != "" {
		bifrostErr.Error.Message = message
	} else if bifrostErr.Error.Message == "" {
		bifrostErr.Error.Message = "Typesafe API request failed"
	}
	if errorType != "" && bifrostErr.Error.Type == nil {
		bifrostErr.Error.Type = &errorType
	}

	if envelope {
		return bifrostErr
	}
	if body, err := providerUtils.CheckAndDecodeBody(resp); err == nil && gjson.ValidBytes(body) {
		var buf bytes.Buffer
		if err := json.Compact(&buf, body); err == nil {
			bifrostErr.ExtraFields.NativeErrorResponse = json.RawMessage(buf.Bytes())
		}
	}

	return bifrostErr
}

// parseTypesafeEnvelopeFailure builds the error for a Cloudflare envelope that
// declares success:false on an HTTP 200. The upstream status says "ok", so it is
// reported as 502 Bad Gateway: the provider answered, but not with a result.
func parseTypesafeEnvelopeFailure(resp *fasthttp.Response) *schemas.BifrostError {
	bifrostErr := parseTypesafeError(resp)
	bifrostErr.StatusCode = schemas.Ptr(fasthttp.StatusBadGateway)
	return bifrostErr
}

// describeTypesafeDetail reads the message and error_type out of a native
// "detail" value the way the official SDKs do: a string is the message, an
// object carries message and error_type, and a validation array of {loc, msg}
// entries is joined as "path: msg; path: msg".
func describeTypesafeDetail(detail interface{}) (message string, errorType string) {
	switch typed := detail.(type) {
	case string:
		return typed, ""
	case map[string]interface{}:
		message, _ = typed["message"].(string)
		errorType, _ = typed["error_type"].(string)
		return message, errorType
	case []interface{}:
		parts := make([]string, 0, len(typed))
		for _, entry := range typed {
			item, ok := entry.(map[string]interface{})
			if !ok {
				continue
			}
			msg, ok := item["msg"].(string)
			if !ok {
				continue
			}
			var loc []string
			if rawLoc, ok := item["loc"].([]interface{}); ok {
				for _, segment := range rawLoc {
					if text, ok := segment.(string); ok && text != "body" {
						loc = append(loc, text)
					}
				}
			}
			if len(loc) > 0 {
				parts = append(parts, strings.Join(loc, ".")+": "+msg)
			} else {
				parts = append(parts, msg)
			}
		}
		return strings.Join(parts, "; "), ""
	}
	return "", ""
}

// TypesafeNativeErrorDetail is the payload of Typesafe's native error body.
type TypesafeNativeErrorDetail struct {
	ErrorType string `json:"error_type"`
	Message   string `json:"message"`
}

// TypesafeNativeError is the error body Typesafe's API returns and its SDKs
// parse: {"detail": {"error_type": ..., "message": ...}} (observed on the live
// API; the docs describe JSON error bodies without pinning the schema).
type TypesafeNativeError struct {
	Detail TypesafeNativeErrorDetail `json:"detail"`
}

// ToTypesafeNativeError rebuilds a Bifrost error into Typesafe's native error
// body so the /typesafe drop-in surface stays parseable by Typesafe's SDKs.
// The HTTP status code rides on the response as usual; this shapes the body.
func ToTypesafeNativeError(bifrostErr *schemas.BifrostError) *TypesafeNativeError {
	native := &TypesafeNativeError{
		Detail: TypesafeNativeErrorDetail{ErrorType: "api_error"},
	}
	if bifrostErr == nil {
		native.Detail.Message = "unknown error"
		return native
	}
	if bifrostErr.Error != nil {
		native.Detail.Message = bifrostErr.Error.Message
		if bifrostErr.Error.Type != nil && *bifrostErr.Error.Type != "" {
			native.Detail.ErrorType = *bifrostErr.Error.Type
		}
	}
	if native.Detail.Message == "" {
		native.Detail.Message = "Typesafe API request failed"
	}
	return native
}

// ToTypesafeNativeErrorBody is the /typesafe route's error body: the
// endpoint's own error body verbatim when the provider kept one (string,
// object, or validation-array detail all pass through unchanged), otherwise
// the rebuilt native envelope for errors Bifrost raised itself.
func ToTypesafeNativeErrorBody(bifrostErr *schemas.BifrostError) interface{} {
	if bifrostErr != nil && len(bifrostErr.ExtraFields.NativeErrorResponse) > 0 {
		return bifrostErr.ExtraFields.NativeErrorResponse
	}
	return ToTypesafeNativeError(bifrostErr)
}
