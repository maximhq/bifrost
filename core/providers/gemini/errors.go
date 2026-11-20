package gemini

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// GeminiStreamAPIError is an error payload Gemini delivers inside an HTTP 200
// stream body (e.g. mid-stream 429 RESOURCE_EXHAUSTED quota aborts). It keeps
// the upstream code/status/message so they can be preserved on the wire.
type GeminiStreamAPIError struct {
	Err *GeminiGenerationErrorStruct
}

func (e *GeminiStreamAPIError) Error() string {
	return fmt.Sprintf("gemini api error: %d %s - %s", e.Err.Code, e.Err.Status, e.Err.Message)
}

// ApplyRetryInfo records the delay a google.rpc.RetryInfo error detail asks for as the
// error's retry hint. It is Google's own answer for that error, so it replaces a hint read
// from a retry header.
func ApplyRetryInfo(bifrostErr *schemas.BifrostError, details []GeminiGenerationErrorDetails) {
	for _, detail := range details {
		if detail.Type != googleRetryInfoType {
			continue
		}
		if delay, err := time.ParseDuration(detail.RetryDelay); err == nil {
			providerUtils.SetRetryAfter(bifrostErr, delay)
		}
		return
	}
}

// toGeminiStreamBifrostError builds the BifrostError for an error payload
// detected in a stream chunk, preserving the upstream code/status/message
// (e.g. mid-stream 429s) when the payload parsed as a typed API error.
func toGeminiStreamBifrostError(err error) *schemas.BifrostError {
	bifrostErr := &schemas.BifrostError{
		Type:           schemas.Ptr("gemini_api_error"),
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: err.Error(),
			Error:   err,
		},
	}
	var apiErr *GeminiStreamAPIError
	if errors.As(err, &apiErr) {
		bifrostErr.StatusCode = schemas.Ptr(apiErr.Err.Code)
		bifrostErr.Error.Code = schemas.Ptr(strconv.Itoa(apiErr.Err.Code))
		bifrostErr.Error.Message = apiErr.Err.Message
		if apiErr.Err.Status != "" {
			bifrostErr.Error.Type = schemas.Ptr(apiErr.Err.Status)
		}
		ApplyRetryInfo(bifrostErr, apiErr.Err.Details)
	}
	// No usable status: the payload did not parse as a typed API error, or it parsed
	// with "code" omitted, which Code's int zero value cannot distinguish from a real
	// status. Either way ClassifyFailure has nothing to act on, so the failure is never
	// retried — and a zero here is only saved from a caller 400 by status normalization.
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode == 0 {
		bifrostErr.StatusCode = schemas.Ptr(fasthttp.StatusBadGateway)
	}
	return bifrostErr
}

// googleRPCStatusHTTP maps each google.rpc.Code error name to its HTTP status (googleapis
// google/rpc/code.proto). OK is left out: it means success and never describes an error.
var googleRPCStatusHTTP = map[string]int{
	"CANCELLED": 499, "UNKNOWN": 500, "INVALID_ARGUMENT": 400, "DEADLINE_EXCEEDED": 504,
	"NOT_FOUND": 404, "ALREADY_EXISTS": 409, "PERMISSION_DENIED": 403, "UNAUTHENTICATED": 401,
	"RESOURCE_EXHAUSTED": 429, "FAILED_PRECONDITION": 400, "ABORTED": 409, "OUT_OF_RANGE": 400,
	"UNIMPLEMENTED": 501, "INTERNAL": 500, "UNAVAILABLE": 503, "DATA_LOSS": 500,
}

// googleRPCStatusOf returns the error's type when it is a google.rpc.Code name, else "".
func googleRPCStatusOf(bifrostErr *schemas.BifrostError) string {
	if bifrostErr.Error == nil || bifrostErr.Error.Type == nil {
		return ""
	}
	if _, ok := googleRPCStatusHTTP[*bifrostErr.Error.Type]; !ok {
		return ""
	}
	return *bifrostErr.Error.Type
}

// googleRPCStatusForHTTP inverts code.proto's HTTP mapping. 400 and 409 have several codes;
// the general one is used. Statuses code.proto does not map fall back to UNKNOWN.
func googleRPCStatusForHTTP(code int) string {
	switch code {
	case fasthttp.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case fasthttp.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case fasthttp.StatusForbidden:
		return "PERMISSION_DENIED"
	case fasthttp.StatusNotFound:
		return "NOT_FOUND"
	case fasthttp.StatusConflict:
		return "ABORTED"
	case fasthttp.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case 499:
		return "CANCELLED"
	case fasthttp.StatusInternalServerError:
		return "INTERNAL"
	case fasthttp.StatusNotImplemented:
		return "UNIMPLEMENTED"
	case fasthttp.StatusServiceUnavailable:
		return "UNAVAILABLE"
	case fasthttp.StatusGatewayTimeout:
		return "DEADLINE_EXCEEDED"
	default:
		return "UNKNOWN"
	}
}

// ToGeminiError derives a GeminiGenerationError from a BifrostError
func ToGeminiError(bifrostErr *schemas.BifrostError) *GeminiGenerationError {
	if bifrostErr == nil {
		return nil
	}
	// AIP-193: code is the HTTP status the response is written with, status its google.rpc.Code name.
	return newGeminiError(bifrostErr, bifrostErr.EffectiveHTTPStatus())
}

// ToGeminiStreamError derives the error event for a stream that has already started. The HTTP
// status is 200 by then, so SDKs classify the error from this code alone: an explicit error status
// (400 or above) wins, then the HTTP status of a google.rpc.Code type, then 500.
func ToGeminiStreamError(bifrostErr *schemas.BifrostError) *GeminiGenerationError {
	if bifrostErr == nil {
		return nil
	}
	code := fasthttp.StatusInternalServerError
	if bifrostErr.StatusCode != nil && *bifrostErr.StatusCode >= fasthttp.StatusBadRequest {
		code = *bifrostErr.StatusCode
	} else if typeCode, ok := googleRPCStatusHTTP[googleRPCStatusOf(bifrostErr)]; ok {
		code = typeCode
	}
	return newGeminiError(bifrostErr, code)
}

// newGeminiError builds the Gemini error body for code, keeping a google.rpc.Code type as status.
func newGeminiError(bifrostErr *schemas.BifrostError, code int) *GeminiGenerationError {
	status := googleRPCStatusOf(bifrostErr)
	if status == "" {
		status = googleRPCStatusForHTTP(code)
	}
	message := ""
	if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
		message = bifrostErr.Error.Message
	}
	return &GeminiGenerationError{
		Error: &GeminiGenerationErrorStruct{
			Code:    code,
			Message: message,
			Status:  status,
		},
	}
}

// parseGeminiDecisionError parses a decisions error. The decisions endpoint
// answers with {"error": {"code": "invalid_request", "message": ...}}, whose code
// is a string ("invalid_request", "not_found", or a safety reason such as
// "safety") that parseGeminiError's numeric code cannot hold, so the string
// becomes the error's code. An error with a numeric code, such as a rejected API
// key, is Google's usual shape and goes through parseGeminiError unchanged.
func parseGeminiDecisionError(resp *fasthttp.Response) *schemas.BifrostError {
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil || gjson.GetBytes(body, "error.code").Type != gjson.String {
		return parseGeminiError(resp)
	}
	var errorResp GeminiDecisionError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	if errorResp.Error != nil {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Code = schemas.Ptr(errorResp.Error.Code)
		bifrostErr.Error.Message = errorResp.Error.Message
	}
	return bifrostErr
}

// parseGeminiError parses Gemini error responses
func parseGeminiError(resp *fasthttp.Response) *schemas.BifrostError {
	// Try to parse as []GeminiGenerationError
	var errorResps []GeminiGenerationError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResps)
	if len(errorResps) > 0 {
		var message string
		var firstError *GeminiGenerationErrorStruct
		for _, errorResp := range errorResps {
			if errorResp.Error != nil {
				if firstError == nil {
					firstError = errorResp.Error
				}
				message = message + errorResp.Error.Message + "\n"
			}
		}
		// Trim trailing newline
		message = strings.TrimSuffix(message, "\n")
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		// Set Code from first error if available
		if firstError != nil {
			bifrostErr.Error.Code = schemas.Ptr(strconv.Itoa(firstError.Code))
			if firstError.Status != "" {
				bifrostErr.Error.Type = schemas.Ptr(firstError.Status)
			}
			ApplyRetryInfo(bifrostErr, firstError.Details)
		}
		// Set Message to trimmed concatenated message
		bifrostErr.Error.Message = message
		return bifrostErr
	}

	// Try to parse as GeminiGenerationError
	var errorResp GeminiGenerationError
	bifrostErr = providerUtils.HandleProviderAPIError(resp, &errorResp)
	if errorResp.Error != nil {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Code = schemas.Ptr(strconv.Itoa(errorResp.Error.Code))
		bifrostErr.Error.Message = errorResp.Error.Message
		if errorResp.Error.Status != "" {
			bifrostErr.Error.Type = schemas.Ptr(errorResp.Error.Status)
		}
		ApplyRetryInfo(bifrostErr, errorResp.Error.Details)
	}
	return bifrostErr
}
