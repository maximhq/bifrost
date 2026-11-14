package minimax

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseMiniMaxError converts a non-2xx MiniMax response into a BifrostError.
// MiniMax uses an OpenAI-style error object on newer routes and a base_resp envelope on older ones.
func parseMiniMaxError(resp *fasthttp.Response) *schemas.BifrostError {
	bifrostErr := openai.ParseOpenAIError(resp)
	if body, err := providerUtils.CheckAndDecodeBody(resp); err == nil {
		if msg := providerUtils.GetJSONField(body, "base_resp.status_msg").String(); msg != "" && strings.HasPrefix(bifrostErr.Error.Message, "provider API error") {
			bifrostErr.Error.Message = msg
			if code := providerUtils.GetJSONField(body, "base_resp.status_code"); code.Exists() {
				codeStr := code.String()
				bifrostErr.Error.Code = &codeStr
			}
		}
	}
	return bifrostErr
}

// miniMaxBaseRespError returns an error for a non-zero base_resp status, or nil on success.
func miniMaxBaseRespError(baseResp *MiniMaxBaseResp) *schemas.BifrostError {
	if baseResp == nil || baseResp.StatusCode == 0 {
		return nil
	}
	statusCode, errType := miniMaxStatusToHTTP(baseResp.StatusCode)
	message := baseResp.StatusMsg
	if message == "" {
		message = fmt.Sprintf("minimax error %d", baseResp.StatusCode)
	}
	bifrostErr := providerUtils.NewProviderAPIError(message, nil, statusCode, schemas.Ptr(errType), nil)
	bifrostErr.Error.Code = schemas.Ptr(fmt.Sprintf("%d", baseResp.StatusCode))
	return bifrostErr
}

// miniMaxBaseRespErrorFromBody reads base_resp out of a raw body and maps it like miniMaxBaseRespError.
func miniMaxBaseRespErrorFromBody(body []byte) *schemas.BifrostError {
	code := providerUtils.GetJSONField(body, "base_resp.status_code")
	if !code.Exists() || code.Int() == 0 {
		return nil
	}
	return miniMaxBaseRespError(&MiniMaxBaseResp{
		StatusCode: int(code.Int()),
		StatusMsg:  providerUtils.GetJSONField(body, "base_resp.status_msg").String(),
	})
}

// miniMaxStatusToHTTP maps MiniMax business status codes (platform.minimax.io/docs/api-reference/errorcode) to HTTP semantics.
func miniMaxStatusToHTTP(code int) (int, string) {
	switch code {
	case 1002, 1039, 1041, 2045, 2056:
		return fasthttp.StatusTooManyRequests, "rate_limit_error"
	case 1004, 2049:
		return fasthttp.StatusUnauthorized, "authentication_error"
	case 1008:
		return fasthttp.StatusPaymentRequired, "insufficient_balance"
	case 1026, 1027:
		return fasthttp.StatusBadRequest, "content_filter"
	case 1042, 2013, 20132, 2037, 2039, 2042, 2048, 2054:
		return fasthttp.StatusBadRequest, "invalid_request_error"
	case 1001:
		return fasthttp.StatusGatewayTimeout, "timeout_error"
	default:
		return fasthttp.StatusInternalServerError, "server_error"
	}
}
