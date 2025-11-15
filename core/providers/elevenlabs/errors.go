package elevenlabs

import (
	"fmt"
	"strings"

	"github.com/valyala/fasthttp"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func parseElevenlabsError(providerName schemas.ModelProvider, resp *fasthttp.Response) *schemas.BifrostError {
	body := resp.Body()

	var errorResp ElevenlabsError
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &errorResp, true)
	if bifrostErr != nil {
		return providerUtils.NewBifrostOperationError(
			fmt.Sprintf("failed to parse error response: %v", bifrostErr.Error.Error),
			fmt.Errorf("HTTP %d", resp.StatusCode()),
			providerName,
		)
	}

	if len(errorResp.Detail) > 0 {
		var messages []string
		for _, detail := range errorResp.Detail {
			location := "unknown"
			if len(detail.Loc) > 0 {
				location = strings.Join(detail.Loc, ".")
			}
			messages = append(messages, fmt.Sprintf("[%s] %s (%s)", location, *detail.Msg, *detail.Type))
		}
		errorMessage := strings.Join(messages, "; ")
		return &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(resp.StatusCode()),
			Error: &schemas.ErrorField{
				Code:    schemas.Ptr("validation_error"),
				Message: fmt.Sprintf("Elevenlabs validation error: %s", errorMessage),
			},
		}
	}

	if rawResponse != nil {
		return providerUtils.NewBifrostOperationError(fmt.Sprintf("Elevenlabs error: %v", rawResponse), fmt.Errorf("HTTP %d", resp.StatusCode()), providerName)
	}

	return providerUtils.NewBifrostOperationError("Elevenlabs error: no response", fmt.Errorf("HTTP %d", resp.StatusCode()), providerName)
}
