package cohere

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostResponsesRequest converts a Cohere count tokens request to Bifrost format.
func (req *CohereCountTokensRequest) ToBifrostResponsesRequest(ctx *schemas.BifrostContext) *schemas.BifrostResponsesRequest {
	if req == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(req.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.Cohere))

	userRole := schemas.ResponsesInputMessageRoleUser
	return &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ResponsesMessage{
			{
				Role: &userRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &req.Text,
				},
			},
		},
	}
}

// ToCohereCountTokensRequest converts a Bifrost count tokens request to Cohere's tokenize payload.
func ToCohereCountTokensRequest(bifrostReq *schemas.BifrostResponsesRequest) (*CohereCountTokensRequest, error) {
	if bifrostReq == nil {
		return nil, nil
	}

	if bifrostReq.Input == nil {
		return nil, fmt.Errorf("count tokens input is not provided")
	}

	text := buildCohereCountTokensText(bifrostReq.Input)
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, fmt.Errorf("count tokens text is empty after conversion")
	}
	runeCount := utf8.RuneCountInString(trimmed)
	if runeCount < cohereTokenizeMinTextLength || runeCount > cohereTokenizeMaxTextLength {
		return nil, fmt.Errorf("count tokens text length must be between %d and %d characters", cohereTokenizeMinTextLength, cohereTokenizeMaxTextLength)
	}

	return &CohereCountTokensRequest{
		Model: bifrostReq.Model,
		Text:  trimmed,
	}, nil
}

// ToBifrostCountTokensResponse converts a Cohere tokenize response to Bifrost format.
func (resp *CohereCountTokensResponse) ToBifrostCountTokensResponse(model string) *schemas.BifrostCountTokensResponse {
	if resp == nil {
		return nil
	}

	inputTokens := len(resp.Tokens)
	if inputTokens == 0 && len(resp.TokenStrings) > 0 {
		inputTokens = len(resp.TokenStrings)
	}
	totalTokens := inputTokens

	return &schemas.BifrostCountTokensResponse{
		Model:        model,
		InputTokens:  inputTokens,
		TotalTokens:  &totalTokens,
		TokenStrings: resp.TokenStrings,
		Tokens:       resp.Tokens,
		Object:       "response.input_tokens",
	}
}

// buildCohereCountTokensText flattens Responses messages into a plain text payload for tokenization.
func buildCohereCountTokensText(messages []schemas.ResponsesMessage) string {
	var parts []string

	for _, msg := range messages {
		var contentParts []string

		if msg.Content != nil {
			if msg.Content.ContentStr != nil {
				contentParts = append(contentParts, *msg.Content.ContentStr)
			}
			for _, block := range msg.Content.ContentBlocks {
				if block.Text != nil {
					contentParts = append(contentParts, *block.Text)
				}
				if block.ResponsesOutputMessageContentRefusal != nil && block.ResponsesOutputMessageContentRefusal.Refusal != "" {
					contentParts = append(contentParts, block.ResponsesOutputMessageContentRefusal.Refusal)
				}
			}
		}

		if msg.ResponsesReasoning != nil {
			for _, summary := range msg.ResponsesReasoning.Summary {
				if summary.Text != "" {
					contentParts = append(contentParts, summary.Text)
				}
			}
		}

		if len(contentParts) == 0 {
			continue
		}

		parts = append(parts, strings.Join(contentParts, "\n"))
	}

	return strings.TrimSpace(strings.Join(parts, "\n"))
}
