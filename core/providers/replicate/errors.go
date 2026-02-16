package replicate

import (
	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// parseReplicateError parses Replicate API error response
func parseReplicateError(body []byte, statusCode int) *schemas.BifrostError {
	var replicateErr ReplicateError
	if err := sonic.Unmarshal(body, &replicateErr); err == nil && replicateErr.Detail != "" {
		bifrostErr := schemas.AcquireBifrostError()
		bifrostErr.IsBifrostError = false
		bifrostErr.StatusCode = &statusCode
		if bifrostErr.Error == nil {
			bifrostErr.Error = schemas.AcquireBifrostErrorField()
		}
		bifrostErr.Error.Message = replicateErr.Detail
		if bifrostErr.ExtraFields == nil {
			bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		bifrostErr.ExtraFields.Provider = schemas.Replicate
		return bifrostErr
	}

	// Fallback to generic error
	bifrostErr := schemas.AcquireBifrostError()
	bifrostErr.IsBifrostError = false
	bifrostErr.StatusCode = &statusCode
	if bifrostErr.Error == nil {
		bifrostErr.Error = schemas.AcquireBifrostErrorField()
	}
	bifrostErr.Error.Message = string(body)
	if bifrostErr.ExtraFields == nil {
		bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
	}
	bifrostErr.ExtraFields.Provider = schemas.Replicate
	return bifrostErr
}
