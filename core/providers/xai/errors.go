package xai

import (
	"net/http"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// XAIErrorResponse represents xAI's error response format
type XAIErrorResponse struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

// ParseXAIError parses xAI-specific error responses.
// xAI returns errors in format: {"code": "...", "error": "..."}
// Unlike OpenAI which uses: {"error": {"message": "...", "type": "...", "code": "..."}}
func ParseXAIError(resp *fasthttp.Response) *schemas.BifrostError {
	// Try to parse xAI error format
	var xaiErr XAIErrorResponse
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &xaiErr)

	if bifrostErr == nil {
		return nil
	}

	// If we successfully parsed xAI format, extract the fields
	if xaiErr.Error != "" {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = xaiErr.Error
		if xaiErr.Code != "" {
			bifrostErr.Error.Code = schemas.Ptr(xaiErr.Code)
		}
	}

	return bifrostErr
}

// errorInSuccessfulBody returns the error a 2xx body carries in place of a result, and nil
// when it carries none. It reads xAI's own {"code","error"} shape and OpenAI's
// {"error":{"message",...}} envelope, which a proxy in front of xAI may answer with.
// The status is a numeric code in 400-599 when the body names one, else 502: the
// upstream failed but reported success.
func errorInSuccessfulBody(body []byte) *schemas.BifrostError {
	errVal := gjson.GetBytes(body, "error")
	if errVal.IsObject() {
		return openai.ErrorInSuccessfulChatBody(body)
	}
	if errVal.Type != gjson.String || errVal.String() == "" {
		return nil
	}
	statusCode := http.StatusBadGateway
	errField := &schemas.ErrorField{Message: errVal.String()}
	if code := gjson.GetBytes(body, "code"); code.Exists() && code.String() != "" {
		errField.Code = schemas.Ptr(code.String())
		if n := int(code.Int()); code.Type == gjson.Number && n >= 400 && n <= 599 {
			statusCode = n
		}
	}
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     schemas.Ptr(statusCode),
		Error:          errField,
	}
}
