package cloudflare

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// cloudflareModelNamespace prefixes Cloudflare's full IDs for its own models,
// such as "@cf/cloudflare/clef-flash".
const cloudflareModelNamespace = "@cf/cloudflare/"

// cloudflareDecisionPathPrefix is where Workers AI serves Clef, followed by the bare model name.
const cloudflareDecisionPathPrefix = "/ai/run/" + cloudflareModelNamespace

// clefModelName returns the bare Clef name the URL and the body both use: "clef-flash"
// and "@cf/cloudflare/clef-flash" give "clef-flash". Casing is kept as sent.
func clefModelName(model string) string {
	return strings.TrimPrefix(model, cloudflareModelNamespace)
}

// ToCloudflareDecisionRequest converts a normalized decision request into Clef's body:
// input_image parts become images, and the rest goes through Typesafe's converter, so
// questions validate exactly as they do on Typesafe. The request is not modified.
func ToCloudflareDecisionRequest(request *schemas.BifrostDecisionRequest) (*CloudflareDecisionRequest, error) {
	input, images, err := splitDecisionImages(request.Input)
	if err != nil {
		return nil, err
	}
	textOnly := *request
	textOnly.Input = input
	textOnly.Model = clefModelName(request.Model)
	native, err := typesafe.ToTypesafeDecisionRequest(&textOnly)
	if err != nil {
		return nil, err
	}
	return &CloudflareDecisionRequest{TypesafeDecisionRequest: native, Images: images}, nil
}

// splitDecisionImages returns the input without its input_image parts, and those parts'
// data URLs in order. A message left with no parts is dropped, and an input left with no
// messages becomes empty text, since Clef rejects a null state. Text and structured
// inputs are returned unchanged.
func splitDecisionImages(input schemas.DecisionInput) (schemas.DecisionInput, []string, error) {
	if input.Messages == nil {
		return input, nil, nil
	}
	var images []string
	messages := make([]schemas.DecisionInputMessage, 0, len(input.Messages))
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
			images = append(images, *part.ImageURL)
		}
		if len(parts) == 0 {
			continue
		}
		message.Content.Parts = parts
		messages = append(messages, message)
	}
	if len(messages) == 0 && len(images) > 0 {
		return schemas.DecisionInput{Text: schemas.Ptr("")}, images, nil
	}
	return schemas.DecisionInput{Messages: messages}, images, nil
}
