package governance

import (
	"strings"

	"github.com/maximhq/bifrost/core/complexity"
	"github.com/maximhq/bifrost/core/schemas"
)

// buildComplexityInput normalizes request payloads from supported text-generation request
// families into a ComplexityInput. Returns (input, true) for analyzable text-only requests,
// (empty, false) otherwise.
//
// Governance runs on raw HTTP payloads before provider-specific requests have been fully
// normalized, but the transport layer still classifies the operation type for us. We trust
// that request type and only analyze text-generation-style requests. Within those, user input
// must be text-only for this POC. Mixed or non-text user content is skipped so we do not
// accidentally reroute embeddings, images, speech, or multimodal chat/responses traffic.
func buildComplexityInput(ctx *schemas.BifrostContext, body map[string]any) (complexity.ComplexityInput, bool) {
	switch requestTypeFromContext(ctx) {
	case schemas.ChatCompletionRequest:
		return extractFromChatCompletion(body)
	case schemas.TextCompletionRequest:
		return extractFromTextCompletion(body)
	case schemas.ResponsesRequest:
		// OpenAI-style responses traffic uses "input", while Anthropic-native messages are routed
		// through the same request type by the integration layer.
		if hasField(body, "input") {
			return extractFromResponsesAPI(body)
		}
		if hasSliceField(body, "messages") {
			return extractFromChatCompletion(body)
		}
		return complexity.ComplexityInput{}, false
	default:
		return complexity.ComplexityInput{}, false
	}
}

// extractFromChatCompletion handles OpenAI chat and Anthropic message formats.
// Both use "messages" array with "role" and "content" fields.
func extractFromChatCompletion(body map[string]any) (complexity.ComplexityInput, bool) {
	messages, ok := body["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		return complexity.ComplexityInput{}, false
	}

	var input complexity.ComplexityInput
	var userTexts []string

	// Handle top-level "system" field (Anthropic format)
	if sys, ok := body["system"]; ok {
		input.SystemText = extractTextContent(sys)
	}

	for _, msg := range messages {
		m, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}

		role, _ := m["role"].(string)
		content := m["content"]

		switch role {
		case "system", "developer":
			text := extractTextContent(content)
			if text != "" {
				if input.SystemText != "" {
					input.SystemText += " "
				}
				input.SystemText += text
			}
		case "user":
			text, ok := extractTextOnlyContent(content)
			if !ok || text == "" {
				return complexity.ComplexityInput{}, false
			}
			userTexts = append(userTexts, text)
			// Skip assistant, tool, function roles — they would inflate scores
		}
	}

	if len(userTexts) == 0 {
		return complexity.ComplexityInput{}, false
	}

	// Last user message is scored separately; prior ones become conversation context
	input.LastUserText = userTexts[len(userTexts)-1]
	if len(userTexts) > 1 {
		input.PriorUserTexts = userTexts[:len(userTexts)-1]
	}

	return input, true
}

// extractFromTextCompletion handles OpenAI text completion format.
// The "prompt" field is a string or array of strings.
func extractFromTextCompletion(body map[string]any) (complexity.ComplexityInput, bool) {
	prompt, ok := body["prompt"]
	if !ok {
		return complexity.ComplexityInput{}, false
	}

	switch p := prompt.(type) {
	case string:
		if p == "" {
			return complexity.ComplexityInput{}, false
		}
		return complexity.ComplexityInput{LastUserText: p}, true
	case []interface{}:
		var sb strings.Builder
		for _, item := range p {
			s, ok := item.(string)
			if !ok {
				return complexity.ComplexityInput{}, false
			}
			if sb.Len() > 0 {
				sb.WriteString(" ")
			}
			sb.WriteString(s)
		}
		text := sb.String()
		if text == "" {
			return complexity.ComplexityInput{}, false
		}
		return complexity.ComplexityInput{LastUserText: text}, true
	default:
		return complexity.ComplexityInput{}, false
	}
}

// extractFromResponsesAPI handles OpenAI Responses API format.
// "input" can be a string or array of message objects, "instructions" is the system prompt.
func extractFromResponsesAPI(body map[string]any) (complexity.ComplexityInput, bool) {
	var input complexity.ComplexityInput

	// Extract system prompt from "instructions"
	if instructions, ok := body["instructions"].(string); ok {
		input.SystemText = instructions
	}

	rawInput, ok := body["input"]
	if !ok {
		return complexity.ComplexityInput{}, false
	}

	switch v := rawInput.(type) {
	case string:
		if v == "" {
			return complexity.ComplexityInput{}, false
		}
		input.LastUserText = v
		return input, true
	case []interface{}:
		var userTexts []string
		for _, msg := range v {
			m, ok := msg.(map[string]interface{})
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			switch role {
			case "user":
				text, ok := extractTextOnlyContent(m["content"])
				if !ok || text == "" {
					return complexity.ComplexityInput{}, false
				}
				userTexts = append(userTexts, text)
			case "system", "developer":
				text := extractTextContent(m["content"])
				if text != "" {
					if input.SystemText != "" {
						input.SystemText += " "
					}
					input.SystemText += text
				}
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
	default:
		return complexity.ComplexityInput{}, false
	}
}

// extractTextContent extracts text from a message content field.
// Handles string content and array content formats, extracting only text-bearing parts.
func extractTextContent(content interface{}) string {
	switch c := content.(type) {
	case string:
		return c
	case []interface{}:
		// Chat APIs typically use {type: "text"}, while Responses input blocks use
		// {type: "input_text"}. We intentionally ignore non-text blocks.
		var sb strings.Builder
		for _, part := range c {
			p, ok := part.(map[string]interface{})
			if !ok {
				continue
			}
			partType, _ := p["type"].(string)
			if partType == "text" || partType == "input_text" {
				text, _ := p["text"].(string)
				if text != "" {
					if sb.Len() > 0 {
						sb.WriteString(" ")
					}
					sb.WriteString(text)
				}
			}
			// Skip image_url, file_data, audio, etc.
		}
		return sb.String()
	default:
		return ""
	}
}

// extractTextOnlyContent extracts text while requiring the content to be text-only.
// Any non-text part makes the content unanalyzable for the text-only POC.
func extractTextOnlyContent(content interface{}) (string, bool) {
	switch c := content.(type) {
	case string:
		return c, true
	case []interface{}:
		if len(c) == 0 {
			return "", false
		}

		var sb strings.Builder
		for _, part := range c {
			p, ok := part.(map[string]interface{})
			if !ok {
				return "", false
			}
			partType, _ := p["type"].(string)
			if partType != "text" && partType != "input_text" {
				return "", false
			}
			text, _ := p["text"].(string)
			if text == "" {
				return "", false
			}
			if sb.Len() > 0 {
				sb.WriteString(" ")
			}
			sb.WriteString(text)
		}
		return sb.String(), true
	default:
		return "", false
	}
}

func requestTypeFromContext(ctx *schemas.BifrostContext) schemas.RequestType {
	if ctx == nil {
		return schemas.UnknownRequest
	}
	if val := ctx.Value(schemas.BifrostContextKeyHTTPRequestType); val != nil {
		if rt, ok := val.(schemas.RequestType); ok {
			return rt
		}
		if s, ok := val.(string); ok && s != "" {
			return schemas.RequestType(s)
		}
	}
	return schemas.UnknownRequest
}

func hasField(body map[string]any, key string) bool {
	_, ok := body[key]
	return ok
}

func hasSliceField(body map[string]any, key string) bool {
	value, ok := body[key]
	if !ok {
		return false
	}
	_, ok = value.([]interface{})
	return ok
}
