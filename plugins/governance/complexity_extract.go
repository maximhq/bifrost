package governance

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance/complexity"
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
//
// Streaming request types use the same initial request payload shapes as their non-streaming
// counterparts, so complexity analysis can run before the provider call regardless of whether
// the downstream response is streamed back over SSE.
func buildComplexityInput(ctx *schemas.BifrostContext, body map[string]any) (complexity.ComplexityInput, bool) {
	switch requestTypeFromContext(ctx) {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		return extractFromChatCompletion(body)
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		return extractFromTextCompletion(body)
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		// OpenAI-style responses traffic uses "input", while Anthropic-native messages are routed
		// through the same request type by the integration layer. GenAI native requests use
		// "contents", and Bedrock Converse uses "messages" with Bedrock-style content blocks.
		if hasField(body, "input") {
			return extractFromResponsesAPI(body)
		}
		if hasSliceField(body, "contents") {
			return extractFromGeminiContents(body)
		}
		if hasSliceField(body, "messages") {
			return extractFromChatCompletion(body)
		}
		return complexity.ComplexityInput{}, false
	default:
		return complexity.ComplexityInput{}, false
	}
}

// extractFromChatCompletion handles OpenAI chat, Anthropic messages, and
// Bedrock Converse-style requests. All use a "messages" array with "role" and
// content-bearing fields, though Bedrock content blocks are plain {text: "..."}.
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

// extractFromGeminiContents handles Gemini native generateContent/generateAnswer
// request shapes, where text lives under contents[].parts[].text and system
// prompt text optionally lives under systemInstruction.parts[].
func extractFromGeminiContents(body map[string]any) (complexity.ComplexityInput, bool) {
	contents, ok := body["contents"].([]interface{})
	if !ok || len(contents) == 0 {
		return complexity.ComplexityInput{}, false
	}

	var input complexity.ComplexityInput
	var userTexts []string

	if systemInstruction, ok := body["systemInstruction"]; ok {
		input.SystemText = extractTextContent(systemInstruction)
	}

	for _, item := range contents {
		content, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		role, _ := content["role"].(string)
		if role == "" {
			role = "user"
		}

		switch role {
		case "system", "developer":
			text := extractTextContent(content["parts"])
			if text != "" {
				if input.SystemText != "" {
					input.SystemText += " "
				}
				input.SystemText += text
			}
		case "user":
			text, ok := extractTextOnlyContent(content["parts"])
			if !ok || text == "" {
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
// "input" can be a string, array of message objects, or array of bare content
// blocks (e.g. [{type: "input_text", text: "..."}]). Bare block arrays are a
// shorthand for a single user input and are only analyzable when the entire
// array is text-only. "instructions" is the system prompt.
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
		var sawMessageItems bool
		var sawBareBlocks bool
		for _, item := range v {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if hasField(m, "role") || hasField(m, "content") {
				sawMessageItems = true
			} else {
				sawBareBlocks = true
			}
			if sawMessageItems && sawBareBlocks {
				return complexity.ComplexityInput{}, false
			}
		}
		if sawBareBlocks {
			text, ok := extractTextOnlyContent(v)
			if !ok || text == "" {
				return complexity.ComplexityInput{}, false
			}
			input.LastUserText = text
			return input, true
		}

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
	case map[string]interface{}:
		if parts, ok := c["parts"].([]interface{}); ok {
			return extractTextContent(parts)
		}
		if text, ok := extractTextPartString(c, true); ok {
			return text
		}
		return ""
	case []interface{}:
		// Chat APIs typically use {type: "text"}, Responses input blocks use
		// {type: "input_text"}, and Gemini/Bedrock native blocks often just use
		// {text: "..."} with no explicit type. We intentionally ignore non-text blocks.
		var sb strings.Builder
		for _, part := range c {
			p, ok := part.(map[string]interface{})
			if !ok {
				continue
			}
			if text, ok := extractTextPartString(p, true); ok {
				if sb.Len() > 0 {
					sb.WriteString(" ")
				}
				sb.WriteString(text)
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
	case map[string]interface{}:
		if parts, ok := c["parts"].([]interface{}); ok {
			return extractTextOnlyContent(parts)
		}
		return extractTextPartString(c, false)
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
			text, ok := extractTextPartString(p, false)
			if !ok {
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

func extractTextPartString(part map[string]interface{}, allowOutputText bool) (string, bool) {
	text, _ := part["text"].(string)
	if text == "" {
		return "", false
	}

	partType, _ := part["type"].(string)
	switch partType {
	case "", "text", "input_text":
		return text, true
	case "output_text":
		if allowOutputText {
			return text, true
		}
	}
	return "", false
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
