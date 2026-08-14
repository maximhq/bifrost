package anthropic

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// deepSeekV4UnsupportedContentKind identifies a request category that the
// reviewed DeepSeek Anthropic-compatible surface cannot accept.
type deepSeekV4UnsupportedContentKind string

const (
	// deepSeekV4UnsupportedImage identifies image input.
	deepSeekV4UnsupportedImage deepSeekV4UnsupportedContentKind = "image"
	// deepSeekV4UnsupportedDocument identifies document or file input.
	deepSeekV4UnsupportedDocument deepSeekV4UnsupportedContentKind = "document"
	// deepSeekV4UninspectablePayload identifies a one-shot request body that the
	// typed adapter cannot classify without consuming it.
	deepSeekV4UninspectablePayload deepSeekV4UnsupportedContentKind = "uninspectable large-request"
)

// rejectDeepSeekV4UnsupportedChatContent fails an exact DeepSeek V4 attempt
// before request conversion or network egress when its neutral chat envelope
// contains a content kind the provider documents as unsupported.
func rejectDeepSeekV4UnsupportedChatContent(ctx *schemas.BifrostContext, provider schemas.ModelProvider, request *schemas.BifrostChatRequest) *schemas.BifrostError {
	if request == nil || !isExactDeepSeekV4ContentAttempt(ctx, provider, request.Model) {
		return nil
	}
	if isDeepSeekV4UninspectableLargePayload(ctx) {
		return newDeepSeekV4UnsupportedContentError(deepSeekV4UninspectablePayload)
	}
	for i := range request.Input {
		if request.Input[i].Content == nil {
			continue
		}
		if kind := unsupportedDeepSeekV4ChatContentKind(request.Input[i].Content.ContentBlocks); kind != "" {
			return newDeepSeekV4UnsupportedContentError(kind)
		}
	}
	return nil
}

// rejectDeepSeekV4UnsupportedResponsesContent applies the same pre-egress
// contract to Responses requests, including rich function and computer output.
func rejectDeepSeekV4UnsupportedResponsesContent(ctx *schemas.BifrostContext, provider schemas.ModelProvider, request *schemas.BifrostResponsesRequest) *schemas.BifrostError {
	if request == nil || !isExactDeepSeekV4ContentAttempt(ctx, provider, request.Model) {
		return nil
	}
	if isDeepSeekV4UninspectableLargePayload(ctx) {
		return newDeepSeekV4UnsupportedContentError(deepSeekV4UninspectablePayload)
	}
	for i := range request.Input {
		message := &request.Input[i]
		if message.Content != nil {
			if kind := unsupportedDeepSeekV4ResponsesContentKind(message.Content.ContentBlocks); kind != "" {
				return newDeepSeekV4UnsupportedContentError(kind)
			}
		}
		if message.ResponsesToolMessage == nil || message.ResponsesToolMessage.Output == nil {
			continue
		}
		output := message.ResponsesToolMessage.Output
		if kind := unsupportedDeepSeekV4ResponsesContentKind(output.ResponsesFunctionToolCallOutputBlocks); kind != "" {
			return newDeepSeekV4UnsupportedContentError(kind)
		}
		if output.ResponsesComputerToolCallOutput != nil &&
			(output.ResponsesComputerToolCallOutput.ImageURL != nil || output.ResponsesComputerToolCallOutput.FileID != nil) {
			return newDeepSeekV4UnsupportedContentError(deepSeekV4UnsupportedImage)
		}
	}
	return nil
}

// isDeepSeekV4UninspectableLargePayload reports whether the HTTP integration
// skipped typed parsing and exposed only a one-shot body reader. The adapter
// must leave that reader untouched for the next enumerated provider.
func isDeepSeekV4UninspectableLargePayload(ctx *schemas.BifrostContext) bool {
	if ctx == nil {
		return false
	}
	isLargePayload, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool)
	return isLargePayload
}

// isExactDeepSeekV4ContentAttempt keeps the guard closed over the official
// provider and exact canonical model identities. Alias resolution happens per
// attempt; case variants and version-like near misses do not activate it.
func isExactDeepSeekV4ContentAttempt(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) bool {
	return isDeepSeekV4Request(provider, schemas.ResolveCanonicalModel(ctx, model))
}

// unsupportedDeepSeekV4ChatContentKind inspects discriminators only. It never
// reads URLs, base64 data, filenames, text, or any payload-bearing field.
func unsupportedDeepSeekV4ChatContentKind(blocks []schemas.ChatContentBlock) deepSeekV4UnsupportedContentKind {
	for i := range blocks {
		switch blocks[i].Type {
		case schemas.ChatContentBlockTypeImage:
			return deepSeekV4UnsupportedImage
		case schemas.ChatContentBlockTypeFile:
			return deepSeekV4UnsupportedDocument
		}
	}
	return ""
}

// unsupportedDeepSeekV4ResponsesContentKind inspects only neutral Responses
// content discriminators and cannot retain content values.
func unsupportedDeepSeekV4ResponsesContentKind(blocks []schemas.ResponsesMessageContentBlock) deepSeekV4UnsupportedContentKind {
	for i := range blocks {
		switch blocks[i].Type {
		case schemas.ResponsesInputMessageContentBlockTypeImage:
			return deepSeekV4UnsupportedImage
		case schemas.ResponsesInputMessageContentBlockTypeFile:
			return deepSeekV4UnsupportedDocument
		}
	}
	return ""
}

// newDeepSeekV4UnsupportedContentError returns a payload-free typed error that
// permits the core to continue through the enumerated fallback chain. A direct
// request with no fallbacks receives the same local 415.
func newDeepSeekV4UnsupportedContentError(kind deepSeekV4UnsupportedContentKind) *schemas.BifrostError {
	statusCode := 415
	errorType := "unsupported_content_kind"
	allowFallbacks := true
	return &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		AllowFallbacks: &allowFallbacks,
		Error: &schemas.ErrorField{
			Type:    &errorType,
			Code:    schemas.Ptr("deepseek_unsupported_content_kind"),
			Message: fmt.Sprintf("deepseek v4 does not support %s content; an enumerated fallback is required", kind),
		},
	}
}
