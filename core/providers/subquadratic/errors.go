package subquadratic

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// parseSubquadraticError parses an OpenAI-shaped error body (chat, responses, files).
func parseSubquadraticError(resp *fasthttp.Response) *schemas.BifrostError {
	return openai.ParseOpenAIError(resp)
}

// parseSubquadraticDecisionError parses a Decisions API error. SubQ returns the
// System One detail shape: {"detail":{"error_type":"...","message":"..."}}.
func parseSubquadraticDecisionError(resp *fasthttp.Response) *schemas.BifrostError {
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &struct{}{})
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return bifrostErr
	}
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	detail := gjson.GetBytes(body, "detail")
	if detail.Exists() {
		if detail.IsObject() {
			if msg := detail.Get("message").String(); msg != "" {
				bifrostErr.Error.Message = msg
			}
			if errType := detail.Get("error_type").String(); errType != "" {
				bifrostErr.Error.Type = &errType
			}
		} else if detail.Type == gjson.String && detail.String() != "" {
			bifrostErr.Error.Message = detail.String()
		} else if detail.IsArray() {
			parts := make([]string, 0)
			detail.ForEach(func(_, value gjson.Result) bool {
				msg := value.Get("msg").String()
				if msg == "" {
					return true
				}
				locParts := make([]string, 0)
				value.Get("loc").ForEach(func(_, segment gjson.Result) bool {
					if text := segment.String(); text != "" && text != "body" {
						locParts = append(locParts, text)
					}
					return true
				})
				if len(locParts) > 0 {
					parts = append(parts, strings.Join(locParts, ".")+": "+msg)
				} else {
					parts = append(parts, msg)
				}
				return true
			})
			if len(parts) > 0 {
				bifrostErr.Error.Message = strings.Join(parts, "; ")
			}
		}
	}
	if bifrostErr.Error.Message == "" {
		if msg := gjson.GetBytes(body, "error.message").String(); msg != "" {
			bifrostErr.Error.Message = msg
		} else {
			bifrostErr.Error.Message = "subquadratic api request failed"
		}
	}
	return bifrostErr
}
