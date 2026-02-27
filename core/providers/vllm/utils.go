package vllm

import (
	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func HandleVLLMResponse[T any](responseBody []byte, response *T, requestBody []byte, sendBackRawRequest bool, sendBackRawResponse bool) (rawRequest interface{}, rawResponse interface{}, bifrostErr *schemas.BifrostError) {
	var errorResp schemas.BifrostError
	rawRequest, rawResponse, bifrostErr = providerUtils.HandleProviderResponse(responseBody, response, requestBody, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return rawRequest, rawResponse, bifrostErr
	}
	if err := sonic.Unmarshal(responseBody, &errorResp); err == nil && errorResp.Error != nil && errorResp.Error.Message != "" {
		errorResp.ExtraFields = schemas.BifrostErrorExtraFields{
			Provider: schemas.VLLM,
		}
		return rawRequest, rawResponse, &errorResp
	}
	return rawRequest, rawResponse, nil
}
