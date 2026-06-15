package governance

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance/complexity"
)

// buildComplexityInput extracts text from normalized BifrostRequest values for
// complexity_tier routing. It intentionally runs after the transport converters
// have produced Bifrost's typed request shape, so governance does not duplicate
// provider-specific raw payload parsing.
func buildComplexityInput(req *schemas.BifrostRequest) (complexity.ComplexityInput, bool) {
	if req == nil {
		return complexity.ComplexityInput{}, false
	}

	switch req.RequestType {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		if req.ChatRequest == nil {
			return complexity.ComplexityInput{}, false
		}
		return extractFromChatMessages(req.ChatRequest.Input)
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		if req.TextCompletionRequest == nil {
			return complexity.ComplexityInput{}, false
		}
		return extractFromTextCompletionRequest(req.TextCompletionRequest)
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		if req.ResponsesRequest == nil {
			return complexity.ComplexityInput{}, false
		}
		return extractFromResponsesRequest(req.ResponsesRequest)
	default:
		return complexity.ComplexityInput{}, false
	}
}

// extractFromChatMessages builds a complexity input from chat messages by
// preserving system/developer context and tracking only text-only user turns.
func extractFromChatMessages(messages []schemas.ChatMessage) (complexity.ComplexityInput, bool) {
	if len(messages) == 0 {
		return complexity.ComplexityInput{}, false
	}

	var input complexity.ComplexityInput
	var userTexts []string

	for _, msg := range messages {
		switch msg.Role {
		case schemas.ChatMessageRoleSystem, schemas.ChatMessageRoleDeveloper:
			input.SystemText = appendText(input.SystemText, extractChatText(msg.Content))
		case schemas.ChatMessageRoleUser:
			text, ok := extractChatTextOnly(msg.Content)
			if !ok || strings.TrimSpace(text) == "" {
				return complexity.ComplexityInput{}, false
			}
			userTexts = append(userTexts, text)
		}
	}

	if len(userTexts) == 0 {
		return complexity.ComplexityInput{}, false
	}

	input.LastUserText = userTexts[len(userTexts)-1]
	if len(userTexts) > 1 {
		input.PriorUserTexts = userTexts[:len(userTexts)-1]
	}
	return input, true
}

// extractFromTextCompletionRequest builds a complexity input from a single text
// completion prompt and deliberately skips batched prompt arrays.
func extractFromTextCompletionRequest(req *schemas.BifrostTextCompletionRequest) (complexity.ComplexityInput, bool) {
	if req == nil || req.Input == nil || req.Input.PromptStr == nil || strings.TrimSpace(*req.Input.PromptStr) == "" {
		return complexity.ComplexityInput{}, false
	}

	// PromptArray represents batched completions, not one logical prompt. Do not
	// synthesize a single routing input by joining unrelated batch entries.
	return complexity.ComplexityInput{LastUserText: *req.Input.PromptStr}, true
}

// extractFromResponsesRequest builds a complexity input from Responses API
// messages while combining instructions with system/developer message text.
func extractFromResponsesRequest(req *schemas.BifrostResponsesRequest) (complexity.ComplexityInput, bool) {
	if req == nil || len(req.Input) == 0 {
		return complexity.ComplexityInput{}, false
	}

	var input complexity.ComplexityInput
	if req.Params != nil && req.Params.Instructions != nil {
		input.SystemText = *req.Params.Instructions
	}

	var userTexts []string
	for _, msg := range req.Input {
		if msg.Role == nil {
			continue
		}

		switch *msg.Role {
		case schemas.ResponsesInputMessageRoleSystem, schemas.ResponsesInputMessageRoleDeveloper:
			input.SystemText = appendText(input.SystemText, extractResponsesText(msg.Content))
		case schemas.ResponsesInputMessageRoleUser:
			text, ok := extractResponsesTextOnly(msg.Content)
			if !ok || strings.TrimSpace(text) == "" {
				return complexity.ComplexityInput{}, false
			}
			userTexts = append(userTexts, text)
		}
	}

	if len(userTexts) == 0 {
		return complexity.ComplexityInput{}, false
	}

	input.LastUserText = userTexts[len(userTexts)-1]
	if len(userTexts) > 1 {
		input.PriorUserTexts = userTexts[:len(userTexts)-1]
	}
	return input, true
}

// extractChatText returns the text portions of chat content and ignores
// non-text blocks so system/developer context can still be used.
func extractChatText(content *schemas.ChatMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}

	var text string
	for _, block := range content.ContentBlocks {
		if isChatTextBlock(block) && block.Text != nil && *block.Text != "" {
			text = appendText(text, *block.Text)
		}
	}
	return text
}

// extractChatTextOnly returns chat content only when every block is text,
// allowing mixed-modality user prompts to opt out of complexity routing.
func extractChatTextOnly(content *schemas.ChatMessageContent) (string, bool) {
	if content == nil {
		return "", false
	}
	if content.ContentStr != nil {
		return *content.ContentStr, true
	}
	if len(content.ContentBlocks) == 0 {
		return "", false
	}

	var text string
	for _, block := range content.ContentBlocks {
		if !isChatTextBlock(block) || block.Text == nil || *block.Text == "" {
			return "", false
		}
		text = appendText(text, *block.Text)
	}
	return text, true
}

// extractResponsesText returns the text portions of Responses content and
// ignores non-input-text blocks used by non-user context.
func extractResponsesText(content *schemas.ResponsesMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}

	var text string
	for _, block := range content.ContentBlocks {
		if isResponsesInputTextBlock(block) && block.Text != nil && *block.Text != "" {
			text = appendText(text, *block.Text)
		}
	}
	return text
}

// extractResponsesTextOnly returns Responses content only when every block is
// input text, avoiding synthesized prompts for mixed-modality user requests.
func extractResponsesTextOnly(content *schemas.ResponsesMessageContent) (string, bool) {
	if content == nil {
		return "", false
	}
	if content.ContentStr != nil {
		return *content.ContentStr, true
	}
	if len(content.ContentBlocks) == 0 {
		return "", false
	}

	var text string
	for _, block := range content.ContentBlocks {
		if !isResponsesInputTextBlock(block) || block.Text == nil || *block.Text == "" {
			return "", false
		}
		text = appendText(text, *block.Text)
	}
	return text, true
}

// isChatTextBlock reports whether a chat content block is plain text, treating
// an empty type as text for compatibility with normalized request payloads.
func isChatTextBlock(block schemas.ChatContentBlock) bool {
	return block.Type == "" || block.Type == schemas.ChatContentBlockTypeText
}

// isResponsesInputTextBlock reports whether a Responses content block is input
// text, treating an empty type as text for compatibility with normalized input.
func isResponsesInputTextBlock(block schemas.ResponsesMessageContentBlock) bool {
	return block.Type == "" || block.Type == schemas.ResponsesInputMessageContentBlockTypeText
}

// appendText joins adjacent text fragments with one separating space while
// preserving empty existing or next values.
func appendText(existing, next string) string {
	if next == "" {
		return existing
	}
	if existing == "" {
		return next
	}
	return existing + " " + next
}
