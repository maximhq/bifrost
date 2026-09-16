package azure

import (
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// getAzureScopes returns the configured scopes or the default scope if none are valid.
// It filters out empty/whitespace-only strings.
func getAzureScopes(configuredScopes []string) []string {
	scopes := []string{DefaultAzureScope}
	if len(configuredScopes) > 0 {
		cleaned := make([]string, 0, len(configuredScopes))
		for _, s := range configuredScopes {
			if strings.TrimSpace(s) != "" {
				cleaned = append(cleaned, strings.TrimSpace(s))
			}
		}
		if len(cleaned) > 0 {
			scopes = cleaned
		}
	}
	return scopes
}

// resolveAnthropicVersion returns the anthropic-version header value for the
// current attempt. Uses the AzureAliasCfg.AnthropicVersion override from the
// resolved alias when present, otherwise the Azure default.
func resolveAnthropicVersion(ctx *schemas.BifrostContext) string {
	if ra := schemas.GetResolvedAlias(ctx); ra != nil && ra.Config != nil && ra.Config.AzureAliasCfg != nil && ra.Config.AzureAliasCfg.AnthropicVersion != nil && *ra.Config.AzureAliasCfg.AnthropicVersion != "" {
		return *ra.Config.AzureAliasCfg.AnthropicVersion
	}
	return AzureAnthropicAPIVersionDefault
}

// resolveAPIVersion returns the Azure api-version query parameter value for
// the current attempt. Uses the AzureAliasCfg.APIVersion override from the
// resolved alias when present, otherwise the provided default. Different
// Azure routes have different defaults (DefaultAzureAPIVersion for classic
// /openai/deployments/, AzureAPIVersionPreview for /openai/v1/responses);
// callers pass the route's default so the override can take precedence
// without losing the route-specific fallback.
func resolveAPIVersion(ctx *schemas.BifrostContext, defaultVersion string) string {
	if ra := schemas.GetResolvedAlias(ctx); ra != nil && ra.Config != nil && ra.Config.AzureAliasCfg != nil && ra.Config.AzureAliasCfg.APIVersion != nil && *ra.Config.AzureAliasCfg.APIVersion != "" {
		return *ra.Config.AzureAliasCfg.APIVersion
	}
	return defaultVersion
}

// resolveAzureEndpoint returns the Azure cognitive-services endpoint URL for
// the current attempt. Uses the AzureAliasCfg.Endpoint override from the
// resolved alias when present, otherwise the key-level endpoint. Lets one
// Azure credential (ClientID/Secret/TenantID or API key) span deployments
// hosted on different Azure resources (e.g. OpenAI on east-us, Anthropic on
// west-us2).
func resolveAzureEndpoint(ctx *schemas.BifrostContext, key schemas.Key) string {
	if ra := schemas.GetResolvedAlias(ctx); ra != nil && ra.Config != nil && ra.Config.AzureAliasCfg != nil && ra.Config.AzureAliasCfg.Endpoint != nil {
		if v := ra.Config.AzureAliasCfg.Endpoint.GetValue(); v != "" {
			return v
		}
	}
	if key.AzureKeyConfig != nil {
		return key.AzureKeyConfig.Endpoint.GetValue()
	}
	return ""
}

// isAzureChatStreamPreamble identifies chat chunks safe to withhold
// while checking for a startup error.
func isAzureChatStreamPreamble(response *schemas.BifrostChatResponse) bool {
	if response == nil || response.Usage != nil {
		return false
	}
	if len(response.SearchResults) > 0 || len(response.Videos) > 0 ||
		len(response.Citations) > 0 || len(response.ExtraParams) > 0 {
		return false
	}

	hasText := func(value *string) bool {
		return value != nil && *value != ""
	}
	for _, choice := range response.Choices {
		if hasText(choice.FinishReason) || choice.LogProbs != nil ||
			choice.ChatNonStreamResponseChoice != nil ||
			choice.TextCompletionResponseChoice != nil {
			return false
		}
		if choice.ChatStreamResponseChoice == nil ||
			choice.ChatStreamResponseChoice.Delta == nil {
			continue
		}
		delta := choice.ChatStreamResponseChoice.Delta
		if hasText(delta.Role) && *delta.Role != "assistant" {
			return false
		}
		if hasText(delta.Content) || hasText(delta.Refusal) ||
			hasText(delta.Reasoning) || delta.Audio != nil ||
			len(delta.ReasoningDetails) > 0 || len(delta.Annotations) > 0 ||
			len(delta.ToolCalls) > 0 || len(delta.ExtraContent) > 0 {
			return false
		}
	}
	return true
}

// isAzureResponsesStreamPreamble recognizes startup events without output.
// Unrecognized events commit the stream rather than risk replaying work.
func isAzureResponsesStreamPreamble(response *schemas.BifrostResponsesStreamResponse) bool {
	if response == nil {
		return false
	}
	switch response.Type {
	case schemas.ResponsesStreamResponseTypeCreated,
		schemas.ResponsesStreamResponseTypeInProgress,
		schemas.ResponsesStreamResponseTypeQueued,
		schemas.ResponsesStreamResponseTypePing,
		schemas.ResponsesStreamResponseTypeOutputItemAdded,
		schemas.ResponsesStreamResponseTypeContentPartAdded:
	default:
		return false
	}

	hasText := func(value *string) bool {
		return value != nil && *value != ""
	}
	if response.Error != nil || response.Code != nil ||
		response.Message != nil || response.Param != nil ||
		hasText(response.Delta) || hasText(response.Text) ||
		hasText(response.Refusal) || hasText(response.Arguments) ||
		hasText(response.Signature) || hasText(response.PartialImageB64) ||
		response.Annotation != nil || len(response.LogProbs) > 0 ||
		len(response.SearchResults) > 0 || len(response.Videos) > 0 ||
		len(response.Citations) > 0 {
		return false
	}
	if result := response.Response; result != nil {
		if result.Error != nil || result.IncompleteDetails != nil ||
			result.CompletedAt != nil || len(result.Output) > 0 ||
			len(result.SearchResults) > 0 || len(result.Videos) > 0 ||
			len(result.Citations) > 0 || len(result.ProviderExtraFields) > 0 {
			return false
		}
		if result.Status != nil && *result.Status != "queued" &&
			*result.Status != "in_progress" {
			return false
		}
	}
	switch response.Type {
	case schemas.ResponsesStreamResponseTypeOutputItemAdded:
		item := response.Item
		if response.Response != nil || response.Part != nil || item == nil ||
			item.Type == nil || *item.Type != schemas.ResponsesMessageTypeMessage ||
			item.Role == nil || *item.Role != schemas.ResponsesInputMessageRoleAssistant ||
			item.ResponsesToolMessage != nil || item.ResponsesReasoning != nil ||
			len(item.Author) > 0 || len(item.Recipient) > 0 ||
			len(item.ToolSearchOutputTools) > 0 || len(item.AdditionalTools) > 0 ||
			len(item.ProviderNativeParts) > 0 || item.CacheControl != nil {
			return false
		}
		if item.Status != nil && *item.Status != "in_progress" {
			return false
		}
		return item.Content == nil ||
			(!hasText(item.Content.ContentStr) && len(item.Content.ContentBlocks) == 0)

	case schemas.ResponsesStreamResponseTypeContentPartAdded:
		part := response.Part
		if response.Response != nil || response.Item != nil || part == nil ||
			part.Type != schemas.ResponsesOutputMessageContentTypeText ||
			hasText(part.Text) || part.FileID != nil || part.Signature != nil ||
			part.EncryptedContent != nil || part.Audio != nil ||
			part.ResponsesInputMessageContentBlockImage != nil ||
			part.ResponsesInputMessageContentBlockFile != nil ||
			part.ResponsesOutputMessageContentRefusal != nil ||
			part.ResponsesOutputMessageContentRenderedContent != nil ||
			part.ResponsesOutputMessageContentCompaction != nil ||
			part.ResponsesOutputMessageContentFallback != nil ||
			part.CacheControl != nil || part.Citations != nil ||
			part.PromptCacheBreakpoint != nil {
			return false
		}
		return part.ResponsesOutputMessageContentText == nil ||
			(len(part.ResponsesOutputMessageContentText.Annotations) == 0 &&
				len(part.ResponsesOutputMessageContentText.LogProbs) == 0)

	default:
		return response.Item == nil && response.Part == nil
	}
}

// isAzureTextStreamPreamble checks for azure ttext streaming preamble
func isAzureTextStreamPreamble(response *schemas.BifrostTextCompletionResponse) bool {
	if response == nil {
		return false
	}
	for _, choice := range response.Choices {
		if choice.FinishReason != nil || choice.LogProbs != nil ||
			choice.ChatNonStreamResponseChoice != nil ||
			choice.ChatStreamResponseChoice != nil {
			return false
		}
		if choice.TextCompletionResponseChoice != nil &&
			choice.Text != nil && *choice.Text != "" {
			return false
		}
	}
	return true
}

func isAzureSpeechStreamPreamble(response *schemas.BifrostSpeechStreamResponse) bool {
	return response != nil &&
		response.Type == schemas.SpeechStreamResponseTypeDelta &&
		len(response.Audio) == 0 && response.Usage == nil
}

func isAzureImageStreamPreamble(response *schemas.BifrostImageGenerationStreamResponse) bool {
	if response == nil || response.Error != nil || response.Usage != nil ||
		response.B64JSON != "" || response.URL != "" || response.RevisedPrompt != "" {
		return false
	}
	return response.Type == schemas.ImageGenerationEventTypePartial ||
		response.Type == schemas.ImageEditEventTypePartial
}

// IsStreamPreamble reports whether a chunk contains only Azure startup metadata.
// Errors, unsupported response types, and mixed payloads are not preambles.
func IsStreamPreamble(chunk *schemas.BifrostStreamChunk) bool {
	if chunk == nil || chunk.BifrostError != nil ||
		chunk.BifrostTranscriptionStreamResponse != nil ||
		chunk.BifrostPassthroughResponse != nil {
		return false
	}
	if chunk.BifrostSpeechStreamResponse != nil {
		if chunk.BifrostTextCompletionResponse != nil ||
			chunk.BifrostChatResponse != nil ||
			chunk.BifrostResponsesStreamResponse != nil ||
			chunk.BifrostImageGenerationStreamResponse != nil {
			return false
		}
		return isAzureSpeechStreamPreamble(chunk.BifrostSpeechStreamResponse)
	}
	if chunk.BifrostImageGenerationStreamResponse != nil {
		if chunk.BifrostTextCompletionResponse != nil ||
			chunk.BifrostChatResponse != nil ||
			chunk.BifrostResponsesStreamResponse != nil {
			return false
		}
		return isAzureImageStreamPreamble(chunk.BifrostImageGenerationStreamResponse)
	}
	if chunk.BifrostTextCompletionResponse != nil {
		if chunk.BifrostChatResponse != nil ||
			chunk.BifrostResponsesStreamResponse != nil {
			return false
		}
		return isAzureTextStreamPreamble(chunk.BifrostTextCompletionResponse)
	}
	if chunk.BifrostChatResponse != nil {
		if chunk.BifrostResponsesStreamResponse != nil {
			return false
		}
		return isAzureChatStreamPreamble(chunk.BifrostChatResponse)
	}
	return isAzureResponsesStreamPreamble(chunk.BifrostResponsesStreamResponse)
}
