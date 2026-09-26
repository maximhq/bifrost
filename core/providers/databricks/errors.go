package databricks

import (
	"fmt"
	"strings"

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

	// The OpenAI-shaped envelope wins when present; the workspace REST fields fill in
	// otherwise. Databricks messages routinely carry a trailing newline.
	if dbErr.Error != nil {
		if dbErr.Error.Message != "" {
			bifrostErr.Error.Message = strings.TrimSpace(dbErr.Error.Message)
		}
		if dbErr.Error.Type != nil {
			bifrostErr.Error.Type = dbErr.Error.Type
		}
		if dbErr.Error.Code != nil {
			bifrostErr.Error.Code = dbErr.Error.Code
		}
	} else {
		if dbErr.Message != "" {
			bifrostErr.Error.Message = strings.TrimSpace(dbErr.Message)
		}
		if dbErr.ErrorCode != "" {
			bifrostErr.Error.Code = schemas.Ptr(dbErr.ErrorCode)
		}
	}

	// Neither shape carried a message — HandleProviderAPIError leaves it empty on a body
	// that parses but matches nothing we know, so fall back to something that at least
	// names the status rather than surfacing an empty error.
	if strings.TrimSpace(bifrostErr.Error.Message) == "" {
		if bifrostErr.StatusCode != nil {
			bifrostErr.Error.Message = fmt.Sprintf("provider API error (status %d)", *bifrostErr.StatusCode)
		} else {
			bifrostErr.Error.Message = "provider API error"
		}
	}
	return bifrostErr
}
