package ollama

import (
	"maps"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// ollamaDecisionPath is where Ollama serves its local System One models.
const ollamaDecisionPath = "/v1/systemone"

// ToOllamaDecisionRequest converts a normalized decision request into Ollama's
// body. Questions and state go through Typesafe's converter, so they validate
// exactly as they do on Typesafe. Image parts are moved, in order, into the
// top-level images array, which every question shares; images given natively
// in extra params follow them. keep_alive and images are Ollama's own fields,
// so they are read from extra params whether or not passthrough is on. The
// request is not modified.
func ToOllamaDecisionRequest(request *schemas.BifrostDecisionRequest) (*OllamaDecisionRequest, error) {
	input, images, err := extractOllamaDecisionImages(request.Input)
	if err != nil {
		return nil, err
	}
	extraParams, keepAlive, nativeImages, err := liftOllamaDecisionParams(request.ExtraParams)
	if err != nil {
		return nil, err
	}
	textOnly := *request
	textOnly.Input = input
	textOnly.ExtraParams = extraParams
	native, err := typesafe.ToTypesafeDecisionRequest(&textOnly)
	if err != nil {
		return nil, err
	}
	return &OllamaDecisionRequest{
		TypesafeDecisionRequest: native,
		Images:                  append(images, nativeImages...),
		KeepAlive:               keepAlive,
	}, nil
}

// extractOllamaDecisionImages removes the image parts from the input and
// returns them as Ollama's images. Ollama reads only raw base64, so a base64
// data URL is sent as its payload; anything else, such as an https URL, is
// sent unchanged and Ollama's own error comes back. Text and image order is
// not kept, since Ollama shares the images across the whole request. A message
// left with no parts is dropped.
func extractOllamaDecisionImages(input schemas.DecisionInput) (schemas.DecisionInput, []string, error) {
	if !hasOllamaDecisionImage(input.Messages) {
		return input, nil, nil
	}
	messages := make([]schemas.DecisionInputMessage, 0, len(input.Messages))
	var images []string
	for _, message := range input.Messages {
		if message.Content.Parts == nil {
			messages = append(messages, message)
			continue
		}
		parts := make([]schemas.DecisionInputPart, 0, len(message.Content.Parts))
		for _, part := range message.Content.Parts {
			if part.Type != schemas.DecisionInputPartTypeImage {
				parts = append(parts, part)
				continue
			}
			if part.ImageURL == nil || *part.ImageURL == "" {
				return schemas.DecisionInput{}, nil, providerUtils.InvalidRequestErrorf("decision input carries an input_image part without an image_url")
			}
			image := *part.ImageURL
			if _, isBase64, payload, ok := schemas.ParseDataURL(image); ok && isBase64 {
				image = payload
			}
			images = append(images, image)
		}
		if len(parts) > 0 {
			message.Content.Parts = parts
			messages = append(messages, message)
		}
	}
	return schemas.DecisionInput{Messages: messages}, images, nil
}

// hasOllamaDecisionImage reports whether any message carries an image part.
func hasOllamaDecisionImage(messages []schemas.DecisionInputMessage) bool {
	for _, message := range messages {
		for _, part := range message.Content.Parts {
			if part.Type == schemas.DecisionInputPartTypeImage {
				return true
			}
		}
	}
	return false
}

// liftOllamaDecisionParams reads keep_alive and images out of the extra
// params and returns the rest, so passthrough cannot send them a second time.
// Values arrive decoded (from /v1/decisions) or as raw JSON (from the Typesafe
// route), so each is re-encoded and decoded into its typed field. The input
// map is not modified.
func liftOllamaDecisionParams(extraParams map[string]interface{}) (map[string]interface{}, *OllamaKeepAlive, []string, error) {
	rawKeepAlive, hasKeepAlive := extraParams["keep_alive"]
	rawImages, hasImages := extraParams["images"]
	if !hasKeepAlive && !hasImages {
		return extraParams, nil, nil, nil
	}
	rest := maps.Clone(extraParams)
	delete(rest, "keep_alive")
	delete(rest, "images")

	var keepAlive *OllamaKeepAlive
	if hasKeepAlive && rawKeepAlive != nil {
		var decoded OllamaKeepAlive
		if err := decodeOllamaExtraParam(rawKeepAlive, &decoded); err != nil {
			return nil, nil, nil, providerUtils.InvalidRequestErrorf("keep_alive must be a duration string such as \"5m\" or a number of seconds")
		}
		if decoded.Duration != nil || decoded.Seconds != nil {
			keepAlive = &decoded
		}
	}
	var images []string
	if hasImages && rawImages != nil {
		if err := decodeOllamaExtraParam(rawImages, &images); err != nil {
			return nil, nil, nil, providerUtils.InvalidRequestErrorf("images must be an array of base64-encoded strings")
		}
	}
	return rest, keepAlive, images, nil
}

// decodeOllamaExtraParam decodes one extra param, decoded or raw JSON, into target.
func decodeOllamaExtraParam(value interface{}, target interface{}) error {
	data, err := sonic.Marshal(value)
	if err != nil {
		return err
	}
	return sonic.Unmarshal(data, target)
}

// parseOllamaDecisionError builds the error from an Ollama error reply. Ollama
// answers with either an OpenAI-shaped {"error":{"message":...}} or its native
// {"error":"..."}; the native string is read here since the OpenAI parser
// cannot decode it.
func parseOllamaDecisionError(resp *fasthttp.Response) *schemas.BifrostError {
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
