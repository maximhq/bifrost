package gemini

import (
	"fmt"
	"strconv"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ToGeminiError derives a GeminiChatRequestError from a BifrostError
func ToGeminiError(bifrostErr *schemas.BifrostError) *GeminiChatRequestError {
	if bifrostErr == nil {
		return nil
	}
	code := 500
	status := ""
	if bifrostErr.Error != nil && bifrostErr.Error.Type != nil {
		status = *bifrostErr.Error.Type
	}
	message := ""
	if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
		message = bifrostErr.Error.Message
	}
	if bifrostErr.StatusCode != nil {
		code = *bifrostErr.StatusCode
	}
	return &GeminiChatRequestError{
		Error: GeminiChatRequestErrorStruct{
			Code:    code,
			Message: message,
			Status:  status,
		},
	}
}

// parseStreamGeminiError parses Gemini streaming error responses
func parseStreamGeminiError(providerName schemas.ModelProvider, resp *fasthttp.Response) *schemas.BifrostError {
	body := append([]byte(nil), resp.Body()...)

	// Try to parse as JSON first
	var errorResp GeminiGenerationError
	if err := sonic.Unmarshal(body, &errorResp); err == nil {
		bifrostErr := &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(int(resp.StatusCode())),
			Error: &schemas.ErrorField{
				Code:    schemas.Ptr(strconv.Itoa(errorResp.Error.Code)),
				Message: errorResp.Error.Message,
			},
		}
		return bifrostErr
	}

	// If JSON parsing fails, use the raw response body
	var rawResponse interface{}
	if err := sonic.Unmarshal(body, &rawResponse); err != nil {
		return providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	return providerUtils.NewBifrostOperationError(fmt.Sprintf("Gemini streaming error (HTTP %d): %v", resp.StatusCode(), rawResponse), fmt.Errorf("HTTP %d", resp.StatusCode()), providerName)
}

// parseGeminiError parses Gemini error responses
func parseGeminiError(providerName schemas.ModelProvider, resp *fasthttp.Response) *schemas.BifrostError {
	body := append([]byte(nil), resp.Body()...)

	// Try to parse as JSON first
	var errorResp GeminiGenerationError
	if err := sonic.Unmarshal(body, &errorResp); err == nil {
		bifrostErr := &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(resp.StatusCode()),
			Error: &schemas.ErrorField{
				Code:    schemas.Ptr(strconv.Itoa(errorResp.Error.Code)),
				Message: errorResp.Error.Message,
			},
		}
		return bifrostErr
	}

	var rawResponse map[string]interface{}
	if err := sonic.Unmarshal(body, &rawResponse); err != nil {
		return providerUtils.NewBifrostOperationError("failed to parse error response", err, providerName)
	}

	return providerUtils.NewBifrostOperationError(fmt.Sprintf("Gemini error: %v", rawResponse), fmt.Errorf("HTTP %d", resp.StatusCode()), providerName)
}
