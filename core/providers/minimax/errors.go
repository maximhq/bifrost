package minimax

import (
	"fmt"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func miniMaxBusinessError(response *MiniMaxSpeechResponse, httpStatus int) *schemas.BifrostError {
	if response == nil {
		return nil
	}
	if response.BaseResp.StatusCode == 0 && response.Error == nil {
		return nil
	}
	message := response.BaseResp.StatusMsg
	if message == "" && response.Error != nil {
		if encoded, err := providerUtils.MarshalSorted(response.Error); err == nil {
			message = string(encoded)
		}
	}
	if message == "" {
		message = fmt.Sprintf("MiniMax API error %d", response.BaseResp.StatusCode)
	}
	errorType := fmt.Sprintf("minimax_%d", response.BaseResp.StatusCode)
	httpStatus = miniMaxBusinessHTTPStatus(response.BaseResp.StatusCode, httpStatus)
	return providerUtils.NewProviderAPIError(message, nil, httpStatus, &errorType, schemas.Ptr(response.TraceID))
}

func miniMaxBusinessHTTPStatus(code, fallback int) int {
	if fallback >= fasthttp.StatusBadRequest {
		return fallback
	}
	switch code {
	case 1001:
		return fasthttp.StatusGatewayTimeout
	case 1002, 1039:
		return fasthttp.StatusTooManyRequests
	case 1004:
		return fasthttp.StatusUnauthorized
	case 1042, 2013:
		return fasthttp.StatusBadRequest
	default:
		return fasthttp.StatusBadGateway
	}
}

func parseMiniMaxHTTPError(resp *fasthttp.Response) *schemas.BifrostError {
	var parsed MiniMaxErrorResponse
	if err := sonic.Unmarshal(resp.Body(), &parsed); err == nil && (parsed.BaseResp.StatusCode != 0 || parsed.BaseResp.StatusMsg != "" || parsed.Error != nil) {
		errorType := "minimax_api_error"
		if parsed.BaseResp.StatusCode != 0 {
			errorType = fmt.Sprintf("minimax_%d", parsed.BaseResp.StatusCode)
		}
		return providerUtils.NewProviderAPIError(parsed.message(), nil, resp.StatusCode(), &errorType, schemas.Ptr(parsed.TraceID))
	}
	return providerUtils.HandleProviderAPIError(resp, &parsed)
}
