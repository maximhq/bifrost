package baseten

import (
	"fmt"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// basetenDecisionPath is where Model APIs serves decision models in TypeSafe's
// System One shape.
const basetenDecisionPath = "/systemone"

// basetenDecisionMaxImages is System One's per-request image limit.
const basetenDecisionMaxImages = 16

// ToBasetenDecisionRequest converts a normalized decision request into Baseten's
// System One body. Questions go through Typesafe's converter, so they validate
// exactly as they do on Typesafe. A message input differs from Typesafe's: Baseten
// reads a top-level array as structured data, so the conversation is wrapped as
// {"messages": [...]}, with text parts as {"type":"text"} and images inline as
// {"type":"image_url"} on the message that carried them. Images sent by reference
// and tool items have no System One equivalent and are rejected. The request is not
// modified.
func ToBasetenDecisionRequest(request *schemas.BifrostDecisionRequest) (*typesafe.TypesafeDecisionRequest, error) {
	if request.Input.HasAssetParts() {
		return nil, providerUtils.InvalidRequestErrorf("decision input carries an image sent by reference, which baseten cannot read; send it inline as an input_image with image_url")
	}
	if request.Input.Messages == nil {
		return typesafe.ToTypesafeDecisionRequest(request)
	}
	messages := make([]map[string]any, 0, len(request.Input.Messages))
	images := 0
	for i, message := range request.Input.Messages {
		converted, n, err := toBasetenDecisionMessage(i, message)
		if err != nil {
			return nil, err
		}
		images += n
		messages = append(messages, converted)
	}
	if images > basetenDecisionMaxImages {
		return nil, providerUtils.InvalidRequestErrorf("decision input carries %d images; baseten accepts at most %d per request", images, basetenDecisionMaxImages)
	}
	wrapped := *request
	wrapped.Input = schemas.DecisionInput{Structured: map[string]any{"messages": messages}}
	return typesafe.ToTypesafeDecisionRequest(&wrapped)
}

// toBasetenDecisionMessage converts one input message to a System One conversation
// message and returns how many images it carries. System One accepts only user and
// assistant roles, and images only on user messages, so anything else fails here
// rather than as Baseten's 400. An empty role is read as user.
func toBasetenDecisionMessage(index int, message schemas.DecisionInputMessage) (map[string]any, int, error) {
	if message.Type != nil && *message.Type != schemas.DecisionInputItemTypeMessage {
		return nil, 0, providerUtils.InvalidRequestErrorf("decision input item %d is a %q, which baseten cannot read; send only user and assistant messages", index, *message.Type)
	}
	role := message.Role
	switch role {
	case "":
		role = "user"
	case "user", "assistant":
	default:
		return nil, 0, providerUtils.InvalidRequestErrorf("decision input message %d has role %q; baseten accepts only user and assistant", index, role)
	}
	if message.Content.Parts == nil {
		var content any
		if message.Content.Text != nil {
			content = *message.Content.Text
		}
		return map[string]any{"role": role, "content": content}, 0, nil
	}
	parts := make([]map[string]any, 0, len(message.Content.Parts))
	images := 0
	for _, part := range message.Content.Parts {
		converted, err := toBasetenDecisionPart(index, part)
		if err != nil {
			return nil, 0, err
		}
		if part.Type == schemas.DecisionInputPartTypeImage {
			if role != "user" {
				return nil, 0, providerUtils.InvalidRequestErrorf("decision input message %d is an assistant message with an image; baseten accepts images only on user messages", index)
			}
			images++
		}
		parts = append(parts, converted)
	}
	return map[string]any{"role": role, "content": parts}, images, nil
}

// toBasetenDecisionPart converts one content part. Text, output text, refusals and
// JSON become text parts (a structured value as its JSON text); an inline image
// becomes an image_url part. System One takes only "auto" as an image detail, so a
// low or high detail is dropped rather than failing the request.
func toBasetenDecisionPart(index int, part schemas.DecisionInputPart) (map[string]any, error) {
	switch part.Type {
	case schemas.DecisionInputPartTypeText, schemas.DecisionInputPartTypeOutputText:
		text, err := decisionTextString(part.Text.Value())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "text", "text": text}, nil
	case schemas.DecisionInputPartTypeRefusal:
		text := ""
		if part.Refusal != nil {
			text = *part.Refusal
		}
		return map[string]any{"type": "text", "text": text}, nil
	case schemas.DecisionInputPartTypeJSON:
		text, err := decisionTextString(part.Data.Structured)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "text", "text": text}, nil
	case schemas.DecisionInputPartTypeImage:
		if part.ImageURL == nil || *part.ImageURL == "" {
			return nil, providerUtils.InvalidRequestErrorf("decision input message %d carries an input_image part without an image_url", index)
		}
		image := map[string]any{"url": *part.ImageURL}
		if part.Detail != nil && *part.Detail == "auto" {
			image["detail"] = "auto"
		}
		return map[string]any{"type": "image_url", "image_url": image}, nil
	case "":
		return nil, providerUtils.InvalidRequestErrorf("decision input message %d carries a part without a type", index)
	}
	return nil, providerUtils.InvalidRequestErrorf("decision input message %d carries an %q part, which baseten cannot read; route the request to a provider that accepts it", index, part.Type)
}

// decisionTextString returns a part's text: a string as is, a structured value as
// sorted JSON text, and nil as "".
func decisionTextString(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	}
	encoded, err := providerUtils.MarshalSorted(value)
	if err != nil {
		return "", fmt.Errorf("decision input part: %w", err)
	}
	return string(encoded), nil
}

// parseBasetenDecisionError reads System One's {"error": "...", "error_type": "..."}
// body, which carries the message as a string rather than OpenAI's object.
func parseBasetenDecisionError(resp *fasthttp.Response) *schemas.BifrostError {
	bifrostErr := openai.ParseOpenAIError(resp)
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return bifrostErr
	}
	if message := gjson.GetBytes(body, "error"); message.Type == gjson.String && message.String() != "" {
		bifrostErr.Error.Message = message.String()
	}
	if errorType := gjson.GetBytes(body, "error_type"); errorType.Type == gjson.String && errorType.String() != "" && bifrostErr.Error.Type == nil {
		bifrostErr.Error.Type = new(errorType.String())
	}
	return bifrostErr
}

// Decision answers a decision request on Model APIs' /systemone. Decision models run
// only on Model APIs, so an alias deployment_url is not consulted.
func (provider *BasetenProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	return typesafe.HandleTypesafeDecisionRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, basetenDecisionPath),
		request,
		ToBasetenDecisionRequest,
		openai.BearerAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		parseBasetenDecisionError,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		false,
	)
}
