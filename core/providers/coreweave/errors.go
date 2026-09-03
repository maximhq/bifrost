package coreweave

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// coreweaveErrorBody accepts every envelope the gateway emits: the OpenAI
// wrapper {"error":{...}} on 401, a flat {"code":"","type","message"} on 404,
// and {"message","type","code":403} from the edge. Code is untyped because
// that last one is a number.
type coreweaveErrorBody struct {
	Error   *schemas.ErrorField `json:"error"`
	Message string              `json:"message"`
	Type    *string             `json:"type"`
	Code    any                 `json:"code"`
}

// ParseCoreWeaveError parses wrapped and flat gateway error bodies.
func ParseCoreWeaveError(resp *fasthttp.Response) *schemas.BifrostError {
	var body coreweaveErrorBody
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &body)
	if bifrostErr == nil {
		return nil
	}
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}

	switch {
	case body.Error != nil:
		bifrostErr.Error.Type = body.Error.Type
		bifrostErr.Error.Code = body.Error.Code
		bifrostErr.Error.Param = body.Error.Param
		if body.Error.Message != "" {
			bifrostErr.Error.Message = body.Error.Message
		}
	case body.Message != "":
		bifrostErr.Error.Message = body.Message
		bifrostErr.Error.Type = body.Type
		if code := errorCodeString(body.Code); code != "" {
			bifrostErr.Error.Code = schemas.Ptr(code)
		}
	}

	if strings.TrimSpace(bifrostErr.Error.Message) == "" {
		if bifrostErr.StatusCode != nil {
			bifrostErr.Error.Message = fmt.Sprintf("provider API error (status %d)", *bifrostErr.StatusCode)
		} else {
			bifrostErr.Error.Message = "provider API error"
		}
	}

	return bifrostErr
}

// errorCodeString renders the flat envelope's code: a string on 404, an integer on 403.
func errorCodeString(code any) string {
	switch c := code.(type) {
	case string:
		return c
	case float64:
		return strconv.FormatFloat(c, 'f', -1, 64)
	case json.Number:
		return c.String()
	default:
		return ""
	}
}
