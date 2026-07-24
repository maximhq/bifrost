package ollama

import (
	"github.com/maximhq/bifrost/core/schemas"
)

func (req *OllamaChatRequest) ToBifrostChatRequest() *schemas.BifrostChatRequest {
	if req == nil {
		return nil
	}

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Ollama,
		Model:    req.Model,
		Input:    make([]schemas.ChatMessage, 0, len(req.Messages)),
		Params: &schemas.ChatParameters{
			LogProbs:    req.Logprobs,
			TopLogProbs: req.TopLogprobs,
			ExtraParams: make(map[string]any),
		},
		Fallbacks: schemas.ParseFallbacks(req.Fallbacks),
	}

	if req.Options != nil {
		bifrostReq.Params.FrequencyPenalty = req.Options.FrequencyPenalty
		bifrostReq.Params.PresencePenalty = req.Options.PresencePenalty
		bifrostReq.Params.Temperature = req.Options.Temperature
		bifrostReq.Params.Seed = req.Options.Seed
		if len(req.Options.Stop) > 0 {
			bifrostReq.Params.Stop = []string(req.Options.Stop)
		}
		bifrostReq.Params.TopP = req.Options.TopP
		bifrostReq.Params.TopK = req.Options.TopK
	}

	if req.KeepAlive != nil {
		bifrostReq.Params.ExtraParams["keep_alive"] = req.KeepAlive
	}

	if req.Think != nil {
		switch v := req.Think.(type) {
		case bool:
			bifrostReq.Params.Reasoning = &schemas.ChatReasoning{Enabled: &v}
		case string:
			bifrostReq.Params.Reasoning = &schemas.ChatReasoning{Effort: &v}
		}
	}

	for _, msg := range req.Messages {
		var bifrostMsg schemas.ChatMessage
		bifrostMsg.Role = schemas.ChatMessageRole(msg.Role)

		if len(msg.ToolCalls) == 0 && len(msg.Images) == 0 {
			if msg.Content != "" {
				bifrostMsg.Content = &schemas.ChatMessageContent{ContentStr: &msg.Content}
			}
			bifrostReq.Input = append(bifrostReq.Input, bifrostMsg)
			continue
		}

		var bifrostContentBlocks []schemas.ChatContentBlock
		var bifrostToolCalls []schemas.ChatAssistantMessageToolCall

		if msg.Content != "" {
			bifrostContentBlocks = append(bifrostContentBlocks, schemas.ChatContentBlock{
				Type: schemas.ChatContentBlockTypeText,
				Text: &msg.Content,
			})
		}

		if len(msg.Images) > 0 {
			for _, base64Image := range msg.Images {
				var bifrostContentBlock schemas.ChatContentBlock
				bifrostContentBlock.Type = schemas.ChatContentBlockTypeImage
				bifrostContentBlock.ImageURLStruct = &schemas.ChatInputImage{URL: base64Image}
				bifrostContentBlocks = append(bifrostContentBlocks, bifrostContentBlock)
			}
			bifrostMsg.Content = &schemas.ChatMessageContent{ContentBlocks: bifrostContentBlocks}
		}

		if len(msg.ToolCalls) > 0 {
			for _, toolCall := range msg.ToolCalls {
				var bifrostToolCall schemas.ChatAssistantMessageToolCall
				if toolCall.Function != nil {
					bifrostToolCall = schemas.ChatAssistantMessageToolCall{
						Type: schemas.Ptr("function"),
						Function: schemas.ChatAssistantMessageToolCallFunction{
							Name: &toolCall.Function.Name,
						},
					}
				}
				bifrostToolCalls = append(bifrostToolCalls, bifrostToolCall)
			}
			bifrostMsg.ChatAssistantMessage = &schemas.ChatAssistantMessage{ToolCalls: bifrostToolCalls}
		}
		bifrostReq.Input = append(bifrostReq.Input, bifrostMsg)
	}

	if len(req.Tools) > 0 {
		var tools []schemas.ChatTool
		for _, tool := range req.Tools {
			var bifrostTool schemas.ChatTool
			bifrostTool.Type = schemas.ChatToolType(tool.Type)
			bifrostTool.Function = &schemas.ChatToolFunction{
				Name:        tool.Function.Name,
				Description: &tool.Function.Description,
				Parameters:  tool.Function.Parameters,
			}
			tools = append(tools, bifrostTool)
		}
		bifrostReq.Params.Tools = tools
	}

	return bifrostReq
}

func ToOllamaChatResponse(resp *schemas.BifrostChatResponse) *OllamaChatResponse {
	msg := OllamaChatMessage{Role: OllamaMessageRoleAssistant}
	doneReason := ""
	promptEval, evalCount := 0, 0

	if len(resp.Choices) > 0 {
		c := resp.Choices[0]
		if c.ChatNonStreamResponseChoice != nil && c.ChatNonStreamResponseChoice.Message != nil {
			m := c.ChatNonStreamResponseChoice.Message
			if m.Content != nil && m.Content.ContentStr != nil {
				msg.Content = *m.Content.ContentStr
			}
			if m.ChatAssistantMessage != nil {
				msg.Thinking = m.ChatAssistantMessage.Reasoning
				msg.ToolCalls = bifrostToolCallsToOllamaToolCalls(m.ChatAssistantMessage.ToolCalls)
			}
		}
		if c.FinishReason != nil {
			doneReason = *c.FinishReason
		}
	}
	if resp.Usage != nil {
		promptEval = resp.Usage.PromptTokens
		evalCount = resp.Usage.CompletionTokens
	}
	return &OllamaChatResponse{
		Model:   resp.Model,
		Message: &msg,
		OllamaCommonResponseFields: &OllamaCommonResponseFields{
			Done:            true,
			DoneReason:      doneReason,
			PromptEvalCount: promptEval,
			EvalCount:       evalCount,
		},
	}
}

// ToOllamaChatStreamChunk converts a streaming BifrostChatResponse to an NDJSON line.
func ToOllamaChatStreamChunk(resp *schemas.BifrostChatResponse) (string, any, error) {
	msg := OllamaChatMessage{Role: OllamaMessageRoleAssistant}
	doneReason := ""
	done := false

	if len(resp.Choices) > 0 {
		c := resp.Choices[0]
		if c.ChatStreamResponseChoice != nil && c.ChatStreamResponseChoice.Delta != nil {
			d := c.ChatStreamResponseChoice.Delta
			if d.Content != nil {
				msg.Content = *d.Content
			}
			if d.Reasoning != nil {
				msg.Thinking = d.Reasoning
			}
			if len(d.ToolCalls) > 0 {
				msg.ToolCalls = bifrostToolCallsToOllamaToolCalls(d.ToolCalls)
			}
		}
		if c.FinishReason != nil {
			done = true
			doneReason = *c.FinishReason
		}
	}

	chunk := &OllamaChatResponse{
		Model:   resp.Model,
		Message: &msg,
		OllamaCommonResponseFields: &OllamaCommonResponseFields{
			Done:       done,
			DoneReason: doneReason,
		},
	}
	if done && resp.Usage != nil {
		chunk.PromptEvalCount = resp.Usage.PromptTokens
		chunk.EvalCount = resp.Usage.CompletionTokens
	}

	json, err := NdjsonLine(chunk)
	return "", json, err
}
