package replicate

import (
	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// parseReplicateError parses Replicate API error response
func parseReplicateError(requestBody []byte, body []byte, statusCode int) *schemas.BifrostError {
	var rawRequest interface{}
	if len(requestBody) > 0 {
		rawRequest = providerUtils.CompactRawJSON(requestBody)
	}

	var rawResponse interface{}
	if len(body) > 0 {
		if err := sonic.Unmarshal(body, &rawResponse); err != nil {
			rawResponse = string(body)
		}
	}

	var replicateErr ReplicateError
	if err := sonic.Unmarshal(body, &replicateErr); err == nil && replicateErr.Detail != "" {
		return &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Message: replicateErr.Detail,
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				RawRequest:  rawRequest,
				RawResponse: rawResponse,
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
			RawRequest:  rawRequest,
			RawResponse: rawResponse,
		},
	}
}
