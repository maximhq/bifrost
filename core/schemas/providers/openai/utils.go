package openai

import "github.com/maximhq/bifrost/core/schemas"

// prepareOpenAIChatRequest formats messages for the OpenAI API.
// It handles both text and image content in messages.
// Returns a slice of formatted messages and any additional parameters.
// sanitizeBifrostMessages performs in-place image URL sanitization for any content blocks
// with type image_url in the provided Bifrost messages and returns the sanitized slice.
func sanitizeChatImageInputs(messages []schemas.ChatMessage) []schemas.ChatMessage {
	if len(messages) == 0 {
		return messages
	}
	for mi := range messages {
		content := &messages[mi].Content
		if content.ContentBlocks == nil {
			continue
		}
		blocks := *content.ContentBlocks
		for bi := range blocks {
			if blocks[bi].Type == schemas.ChatContentBlockTypeImage && blocks[bi].ImageURLStruct != nil {
				if sanitizedURL, _ := schemas.SanitizeImageURL(blocks[bi].ImageURLStruct.URL); sanitizedURL != "" {
					blocks[bi].ImageURLStruct = &schemas.ChatInputImage{
						URL:    sanitizedURL,
						Detail: blocks[bi].ImageURLStruct.Detail,
					}
				}
			}
		}
		*content.ContentBlocks = blocks
	}
	return messages
}

func sanitizeResponsesImageInputs(messages []schemas.ResponsesMessage) []schemas.ResponsesMessage {
	if len(messages) == 0 {
		return messages
	}
	for mi := range messages {
		// Check if ResponsesInputMessage is not nil before accessing Content
		if messages[mi].Content == nil {
			continue
		}
		content := messages[mi].Content
		if content.ContentBlocks == nil {
			continue
		}
		blocks := *content.ContentBlocks
		for bi := range blocks {
			if blocks[bi].Type == schemas.ResponsesInputMessageContentBlockTypeImage && blocks[bi].ResponsesInputMessageContentBlockImage != nil {
				if blocks[bi].ResponsesInputMessageContentBlockImage.ImageURL != nil {
					if sanitizedURL, _ := schemas.SanitizeImageURL(*blocks[bi].ResponsesInputMessageContentBlockImage.ImageURL); sanitizedURL != "" {
						blocks[bi].ResponsesInputMessageContentBlockImage = &schemas.ResponsesInputMessageContentBlockImage{
							ImageURL: &sanitizedURL,
							Detail:   blocks[bi].ResponsesInputMessageContentBlockImage.Detail,
						}
					}
				}
			}
		}
		*content.ContentBlocks = blocks
	}
	return messages
}
