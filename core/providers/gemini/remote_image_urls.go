package gemini

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

var fetchAndEncodeGeminiImageURL = providerUtils.FetchAndEncodeImageURL

type geminiImageURLDisposition int

const (
	geminiImageURLForward geminiImageURLDisposition = iota
	geminiImageURLFetchInline
)

func normalizeImageURLsForGeminiChat(ctx *schemas.BifrostContext, request *schemas.BifrostChatRequest) error {
	if request == nil || shouldSkipGeminiImageURLInlining(ctx) {
		return nil
	}

	for mi := range request.Input {
		msg := &request.Input[mi]
		if msg.Content == nil || msg.Content.ContentBlocks == nil {
			continue
		}
		for bi := range msg.Content.ContentBlocks {
			img := msg.Content.ContentBlocks[bi].ImageURLStruct
			if img == nil {
				continue
			}
			normalizedURL, changed, err := normalizeGeminiImageURL(ctx, img.URL)
			if err != nil {
				return providerUtils.InvalidRequestErrorf("messages[%d].content[%d].image_url: %s", mi, bi, err)
			}
			if changed {
				img.URL = normalizedURL
			}
		}
	}
	return nil
}

func normalizeImageURLsForGeminiResponses(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest) error {
	if request == nil || shouldSkipGeminiImageURLInlining(ctx) {
		return nil
	}

	for mi := range request.Input {
		msg := &request.Input[mi]
		if msg.Content == nil || msg.Content.ContentBlocks == nil {
			continue
		}
		for bi := range msg.Content.ContentBlocks {
			img := msg.Content.ContentBlocks[bi].ResponsesInputMessageContentBlockImage
			if img == nil || img.ImageURL == nil {
				continue
			}
			normalizedURL, changed, err := normalizeGeminiImageURL(ctx, *img.ImageURL)
			if err != nil {
				return providerUtils.InvalidRequestErrorf("input[%d].content[%d].image_url: %s", mi, bi, err)
			}
			if changed {
				*img.ImageURL = normalizedURL
			}
		}
	}
	return nil
}

func normalizeGeminiImageURL(ctx *schemas.BifrostContext, imageURL string) (string, bool, error) {
	sanitizedURL, err := schemas.SanitizeImageURL(imageURL)
	if err != nil {
		return "", false, fmt.Errorf("invalid image_url %q: %s", providerUtils.RedactURLForError(imageURL), err)
	}

	switch classifyGeminiImageURL(sanitizedURL) {
	case geminiImageURLForward:
		return sanitizedURL, sanitizedURL != imageURL, nil
	}

	mediaType, encoded, err := fetchAndEncodeGeminiImageURL(geminiFetchContext(ctx), sanitizedURL)
	if err != nil {
		return "", false, fmt.Errorf("failed to fetch image_url %q: %w", providerUtils.RedactURLForError(sanitizedURL), err)
	}

	return "data:" + mediaType + ";base64," + encoded, true, nil
}

func classifyGeminiImageURL(imageURL string) geminiImageURLDisposition {
	if strings.HasPrefix(imageURL, "data:") || isGeminiFileAPIURL(imageURL) {
		return geminiImageURLForward
	}
	return geminiImageURLFetchInline
}

func isGeminiFileAPIURL(imageURL string) bool {
	parsed, err := url.Parse(imageURL)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	if !strings.EqualFold(parsed.Hostname(), "generativelanguage.googleapis.com") {
		return false
	}
	return strings.Contains(parsed.EscapedPath(), "/files/")
}

func shouldSkipGeminiImageURLInlining(ctx *schemas.BifrostContext) bool {
	if ctx == nil {
		return false
	}
	if useRawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && useRawBody {
		return true
	}
	return providerUtils.IsLargePayloadPassthroughEnabled(ctx)
}

func geminiFetchContext(ctx *schemas.BifrostContext) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func geminiImageInliningError(message string, err error) *schemas.BifrostError {
	if badRequest, ok := providerUtils.AsBifrostBadRequestError(err); ok {
		return badRequest
	}
	return providerUtils.NewBifrostOperationError(message, err)
}
