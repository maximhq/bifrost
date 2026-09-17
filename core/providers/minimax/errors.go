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
	return newMiniMaxBusinessError(response.BaseResp, response.Error, response.TraceID, httpStatus)
}

func miniMaxBusinessErrorFromBody(responseBody []byte, httpStatus int) *schemas.BifrostError {
	statusCode := providerUtils.GetJSONField(responseBody, "base_resp.status_code")
	errorValue := providerUtils.GetJSONField(responseBody, "error")
	hasError := errorValue.Exists() && errorValue.Raw != "null"
	if statusCode.Int() == 0 && !hasError {
		return nil
	}

	var parsedError interface{}
	if hasError {
		if err := sonic.Unmarshal([]byte(errorValue.Raw), &parsedError); err != nil {
			parsedError = errorValue.String()
		}
	}
	return newMiniMaxBusinessError(
		MiniMaxBaseResponse{
			StatusCode: int(statusCode.Int()),
			StatusMsg:  providerUtils.GetJSONField(responseBody, "base_resp.status_msg").String(),
		},
		parsedError,
		providerUtils.GetJSONField(responseBody, "trace_id").String(),
		httpStatus,
	)
}

func newMiniMaxBusinessError(baseResp MiniMaxBaseResponse, errorValue interface{}, traceID string, httpStatus int) *schemas.BifrostError {
	if baseResp.StatusCode == 0 && errorValue == nil {
		return nil
	}
	message := baseResp.StatusMsg
	if message == "" && errorValue != nil {
		if encoded, err := providerUtils.MarshalSorted(errorValue); err == nil {
			message = string(encoded)
		}
	}
	if message == "" {
		message = fmt.Sprintf("MiniMax API error %d", baseResp.StatusCode)
	}
	errorType := "minimax_api_error"
	if baseResp.StatusCode != 0 {
		errorType = fmt.Sprintf("minimax_%d", baseResp.StatusCode)
	}
	httpStatus = miniMaxBusinessHTTPStatus(baseResp.StatusCode, httpStatus)
	var providerRequestID *string
	if traceID != "" {
		providerRequestID = schemas.Ptr(traceID)
	}
	return providerUtils.NewProviderAPIError(message, nil, httpStatus, &errorType, providerRequestID)
}

func handleMiniMaxOpenAIResponse[T any](responseBody []byte, response *T, requestBody []byte, sendBackRawRequest bool, sendBackRawResponse bool) (rawRequest interface{}, rawResponse interface{}, bifrostErr *schemas.BifrostError) {
	rawRequest, rawResponse, bifrostErr = providerUtils.HandleProviderResponse(responseBody, response, requestBody, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return rawRequest, rawResponse, bifrostErr
	}
	if businessErr := miniMaxBusinessErrorFromBody(responseBody, fasthttp.StatusOK); businessErr != nil {
		return rawRequest, rawResponse, businessErr
	}
	return rawRequest, rawResponse, nil
}

func miniMaxBusinessHTTPStatus(code, fallback int) int {
	if fallback >= fasthttp.StatusBadRequest {
		return fallback
	}
	switch code {
	case 1001:
		return fasthttp.StatusGatewayTimeout
	case 1002, 1039, 1041, 2045, 2056:
		return fasthttp.StatusTooManyRequests
	case 1004, 2049:
		return fasthttp.StatusUnauthorized
	case 1008:
		return fasthttp.StatusPaymentRequired
	case 1026, 1027, 1042, 1043, 1044, 2013, 20132, 2037, 2039, 2042, 2048:
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
