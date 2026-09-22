package typesafe

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseTypesafeError parses a Typesafe error HTTP response into a BifrostError.
// Typesafe returns a JSON body detailing the issue on 401, 422, 429 and 529;
// the upstream status code and validation detail are preserved.
func parseTypesafeError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp TypesafeError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)

	message := errorResp.Message
	if message == "" && errorResp.Error != nil {
		message = errorResp.Error.Message
	}
	if message == "" {
		if detail, ok := errorResp.Detail.(string); ok {
			message = detail
		}
	}

	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	if message != "" {
		bifrostErr.Error.Message = message
	} else if bifrostErr.Error.Message == "" {
		bifrostErr.Error.Message = "Typesafe API request failed"
	}

	return bifrostErr
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

// ToTypesafeNativeError converts a Bifrost error into Typesafe's native error
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
