package openrouter

import (
	"encoding/json"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// openRouterErrorEnvelope is the part of OpenRouter's error body that the OpenAI
// envelope has no field for. When the upstream provider fails, OpenRouter answers
// with its own generic message ("Provider returned error") and files the provider's
// actual response under error.metadata.raw, either as a string or as the provider's
// JSON object. Everything Bifrost needs to explain or heal the failure is in there:
// an Anthropic "Invalid `signature` in `thinking` block" arrives only this way.
type openRouterErrorEnvelope struct {
	Error struct {
		Message  string `json:"message"`
		Metadata *struct {
			ProviderName string          `json:"provider_name"`
			Raw          json.RawMessage `json:"raw"`
		} `json:"metadata"`
	} `json:"error"`
}

// parseOpenRouterError parses an OpenRouter error response like the OpenAI parser
// and then lifts the upstream provider's own message out of error.metadata.raw into
// the message, keeping OpenRouter's code and type. Without this the user sees
// "Provider returned error" with no cause, and the encrypted-reasoning fail-soft
// never sees the words that would let it retry.
func parseOpenRouterError(resp *fasthttp.Response) *schemas.BifrostError {
	bifrostErr := openai.ParseOpenAIError(resp)
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil || len(body) == 0 {
		return bifrostErr
	}
	var envelope openRouterErrorEnvelope
	if sonic.Unmarshal(body, &envelope) != nil || envelope.Error.Metadata == nil {
		return bifrostErr
	}
	inner := upstreamErrorMessage(envelope.Error.Metadata.Raw, 0)
	if inner == "" {
		return bifrostErr
	}
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	outer := strings.TrimSpace(bifrostErr.Error.Message)
	if outer == "" || strings.Contains(outer, inner) {
		outer = "Provider returned error"
	}
	if name := strings.TrimSpace(envelope.Error.Metadata.ProviderName); name != "" {
		outer += " (" + name + ")"
	}
	bifrostErr.Error.Message = outer + ": " + inner
	return bifrostErr
}

// upstreamErrorMessage extracts a human-readable message from the raw upstream
// payload OpenRouter forwards. It accepts the shapes providers actually return: a
// plain string, a JSON object with error.message (OpenAI, Anthropic, Bedrock), a
// top-level message, or an error field that is itself a string. A string that is
// itself JSON, which OpenRouter produces when it stringifies the provider body, is
// unwrapped once. Anything else yields "" rather than a dump of arbitrary JSON.
//
// depth bounds the walk at three levels: a stringified body, its error object, and
// that object's own error field, which is as deep as any provider envelope goes.
func upstreamErrorMessage(raw json.RawMessage, depth int) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || depth > 2 {
		return ""
	}
	if trimmed[0] == '"' {
		var text string
		if sonic.Unmarshal(raw, &text) != nil {
			return ""
		}
		text = strings.TrimSpace(text)
		// A stringified body with nothing recognisable in it is still arbitrary
		// JSON: return "" so the caller keeps OpenRouter's own message.
		if strings.HasPrefix(text, "{") {
			return upstreamErrorMessage(json.RawMessage(text), depth+1)
		}
		return text
	}
	if trimmed[0] != '{' {
		return ""
	}
	var object struct {
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if sonic.Unmarshal(raw, &object) != nil {
		return ""
	}
	if nested := upstreamErrorMessage(object.Error, depth+1); nested != "" {
		return nested
	}
	return strings.TrimSpace(object.Message)
}
