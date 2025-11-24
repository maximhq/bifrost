package elevenlabs

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/valyala/fasthttp"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func parseElevenlabsError(providerName schemas.ModelProvider, resp *fasthttp.Response) *schemas.BifrostError {
	body := append([]byte(nil), resp.Body()...)

	var message string
	// Try to parse as JSON first
	var errorResp ElevenlabsError
	if err := sonic.Unmarshal(body, &errorResp); err == nil {
		location := ""
		if len(errorResp.Detail.Loc) > 0 {
			location = strings.Join(errorResp.Detail.Loc, ".")
		}
		if errorResp.Detail.Message != nil {
			message = *errorResp.Detail.Message
		}
		if location != "" {
			message = message + " [" + location + "]"
		}
		errorType := ""
		if errorResp.Detail.Type != nil {
			errorType = *errorResp.Detail.Type
		}
		if errorResp.Detail.Status != nil {
			errorType = *errorResp.Detail.Status
		}
		if message != "" {
			return &schemas.BifrostError{
				IsBifrostError: false,
				StatusCode:     schemas.Ptr(resp.StatusCode()),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(errorType),
					Message: message,
				},
			}
		}
	}

	var rawResponse map[string]interface{}
	if err := sonic.Unmarshal(body, &rawResponse); err != nil {
		return providerUtils.NewBifrostOperationError("failed to parse Elevenlabs error response", err, providerName)
	}

	return providerUtils.NewBifrostOperationError(fmt.Sprintf("Elevenlabs error: %v", rawResponse), fmt.Errorf("HTTP %d", resp.StatusCode()), providerName)
}
