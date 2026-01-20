package replicate

import (
	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// parseReplicateError parses Replicate API error response
func parseReplicateError(body []byte, statusCode int) *schemas.BifrostError {
	var replicateErr ReplicateError
	if err := sonic.Unmarshal(body, &replicateErr); err == nil && replicateErr.Detail != "" {
		return &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Message: replicateErr.Detail,
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				Provider: schemas.Replicate,
			},
		}
	}

	// Fallback to generic error
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Message: string(body),
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider: schemas.Replicate,
		},
	}
}
