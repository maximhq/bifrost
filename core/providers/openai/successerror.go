package openai

import (
	"net/http"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// ErrorInSuccessfulChatBody returns a BifrostError when a 2xx chat-completions body
// carries an in-band error object, and nil otherwise.
//
// The non-streaming handler treats any 200 as success and unmarshals the body into a
// BifrostChatResponse, so a provider that reports failures in-band produces a 200
// whose choices and usage are both null - a silent failure the caller cannot detect.
// OpenRouter documents exactly this: once generation has started "the HTTP 200 OK
// status and headers are already committed - they can't be changed", so the failure
// is delivered as {"error": {code, message}} next to a 200
// (https://openrouter.ai/docs/api-reference/errors).
//
// Deliberately narrow, because it runs on every successful response: it fires only
// when a top-level "error" is a JSON object carrying a non-empty message. A body with
// no error object is left alone even if choices is empty, so providers that legitimately
// return an empty completion keep working exactly as before.
func ErrorInSuccessfulChatBody(body []byte) *schemas.BifrostError {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil
	}

	errObj := gjson.GetBytes(body, "error")
	if !errObj.Exists() || !errObj.IsObject() {
		return nil
	}
	message := errObj.Get("message").String()
	if message == "" {
		return nil
	}

	// OpenRouter types error.code as a number and sets the HTTP status to match it
	// on pre-stream failures; OpenAI uses a string slug there instead. Honour the
	// numeric form as a status code and keep either form as the code field.
	statusCode := http.StatusBadGateway
	code := errObj.Get("code")
	if code.Type == gjson.Number {
		if n := int(code.Int()); n >= 400 && n <= 599 {
			statusCode = n
		}
	}

	errField := &schemas.ErrorField{Message: message}
	if code.Exists() && code.String() != "" {
		errField.Code = schemas.Ptr(code.String())
	}
	if errType := errObj.Get("metadata.error_type"); errType.Exists() && errType.String() != "" {
		errField.Type = schemas.Ptr(errType.String())
	} else if errType := errObj.Get("type"); errType.Exists() && errType.String() != "" {
		errField.Type = schemas.Ptr(errType.String())
	}

	return &schemas.BifrostError{
		// The upstream produced this, so it is not attributed to Bifrost.
		IsBifrostError: false,
		StatusCode:     schemas.Ptr(statusCode),
		Error:          errField,
	}
}
