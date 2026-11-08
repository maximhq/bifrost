package omlx

import (
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// omlxDecisionPath is where oMLX serves its local System One models.
const omlxDecisionPath = "/v1/systemone"

// parseOmlxDecisionError builds the error from an oMLX error reply. oMLX
// answers with either an OpenAI-shaped {"error":{"message":...}} or a native
// {"error":"..."}; the native string is read here since the OpenAI parser
// cannot decode it.
func parseOmlxDecisionError(resp *fasthttp.Response) *schemas.BifrostError {
	bifrostErr := openai.ParseOpenAIError(resp)
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return bifrostErr
	}
	if message := gjson.GetBytes(body, "error"); message.Type == gjson.String && message.String() != "" {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = message.String()
	}
	return bifrostErr
}
