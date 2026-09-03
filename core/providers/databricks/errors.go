package databricks

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// DatabricksError is the error envelope the Databricks workspace REST APIs return. The
// inference surfaces return the OpenAI-shaped {"error": {...}} instead, so both shapes are
// declared here and whichever is populated wins.
type DatabricksError struct {
	// Workspace REST API shape (serving-endpoints, unity-catalog).
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message,omitempty"`
	Details   any    `json:"details,omitempty"`

	// OpenAI-compatible inference shape.
	Error *struct {
		Message string  `json:"message"`
		Type    *string `json:"type,omitempty"`
		Code    *string `json:"code,omitempty"`
	} `json:"error,omitempty"`
}

// parseDatabricksError normalises a Databricks HTTP error response, preserving the Databricks
// error code so an authorization failure stays an authorization failure rather than being
// flattened into a generic "model not found".
func parseDatabricksError(resp *fasthttp.Response) *schemas.BifrostError {
	var dbErr DatabricksError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &dbErr)

	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}

	if dbErr.Error != nil {
		if dbErr.Error.Message != "" {
			bifrostErr.Error.Message = dbErr.Error.Message
		}
		if dbErr.Error.Type != nil {
			bifrostErr.Error.Type = dbErr.Error.Type
		}
		if dbErr.Error.Code != nil {
			bifrostErr.Error.Code = dbErr.Error.Code
		}
		return bifrostErr
	}

	if dbErr.Message != "" {
		bifrostErr.Error.Message = dbErr.Message
	}
	if dbErr.ErrorCode != "" {
		bifrostErr.Error.Code = schemas.Ptr(dbErr.ErrorCode)
	}
	return bifrostErr
}
