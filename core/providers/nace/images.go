package nace

import (
	"encoding/base64"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// readsImages shares alias resolution with the model catalog.
func readsImages(model string) bool {
	switch resolveModelAlias(model) {
	case "drex-max", "drex-max-v1.1", "drex-max-finance":
		return true
	}
	return false
}

// splitImages preserves encounter order and never mutates the caller's input.
func splitImages(input schemas.DecisionInput) (schemas.DecisionInput, []string, error) {
	if input.NonTextPartType() != schemas.DecisionInputPartTypeImage {
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
			if part.ID != nil {
				return schemas.DecisionInput{}, nil, providerUtils.InvalidRequestErrorf("nace does not support image asset references; send ordered inline images")
			}
			var image string
			switch {
			case part.ImageURL != nil:
				image = *part.ImageURL
			case part.Data.File != nil:
				image = base64.StdEncoding.EncodeToString(part.Data.File)
			default:
				return schemas.DecisionInput{}, nil, providerUtils.InvalidRequestErrorf("nace image requires base64 data or a data URL")
			}
			if err := validateImage(image); err != nil {
				return schemas.DecisionInput{}, nil, err
			}
			images = append(images, image)
		}
		if len(parts) > 0 {
			message.Content.Parts = parts
			messages = append(messages, message)
		}
	}
	if len(images) > 8 {
		return schemas.DecisionInput{}, nil, providerUtils.InvalidRequestErrorf("nace accepts at most 8 images")
	}
	return schemas.DecisionInput{Messages: messages}, images, nil
}

func validateImage(image string) error {
	data := image
	if strings.HasPrefix(image, "data:") {
		header, body, ok := strings.Cut(image, ",")
		if !ok || (header != "data:image/png;base64" && header != "data:image/jpeg;base64" && header != "data:image/webp;base64") {
			return providerUtils.InvalidRequestErrorf("nace images must be PNG, JPEG or WebP base64 data URLs")
		}
		data = body
	}
	// Validate without allocating a decoded copy of a potentially large image.
	if len(data) == 0 || len(data)%4 != 0 || base64.StdEncoding.DecodedLen(len(data)) > 16*1024*1024+2 {
		return providerUtils.InvalidRequestErrorf("nace images must be base64 and at most 16 MiB each")
	}
	for i, c := range data {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' {
			continue
		}
		if c == '=' && i >= len(data)-2 && strings.Trim(data[i:], "=") == "" {
			continue
		}
		return providerUtils.InvalidRequestErrorf("nace images must be base64; URLs and paths are not accepted")
	}
	return nil
}
