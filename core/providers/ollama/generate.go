package ollama

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostTextCompletionRequest converts an OllamaGenerateRequest to a BifrostTextCompletionRequest.
func (req *OllamaGenerateRequest) ToBifrostTextCompletionRequest() *schemas.BifrostTextCompletionRequest {
	if req == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(req.Model, schemas.Ollama)

	prompt := req.Prompt
	if req.System != "" {
		prompt = req.System + "\n\n" + prompt
	}

	bifrostReq := &schemas.BifrostTextCompletionRequest{
		Provider: provider,
		Model:    model,
		Input:    &schemas.TextCompletionInput{PromptStr: &prompt},
	}

	params := &schemas.TextCompletionParameters{}
	extra := make(map[string]any)

	if req.Options != nil {
		o := req.Options
		params.Temperature = o.Temperature
		params.TopP = o.TopP
		params.Seed = o.Seed
		params.MaxTokens = o.NumPredict
		params.FrequencyPenalty = o.FrequencyPenalty
		params.PresencePenalty = o.PresencePenalty
		if len(o.Stop) > 0 {
			params.Stop = []string(o.Stop)
		}
	}
	if req.Suffix != "" {
		params.Suffix = &req.Suffix
	}
	if req.TopLogprobs != nil {
		params.LogProbs = req.TopLogprobs
	}
	if len(extra) > 0 {
		params.ExtraParams = extra
	}

	bifrostReq.Params = params
	return bifrostReq
}

// ToOllamaGenerateResponse converts a BifrostTextCompletionResponse to an OllamaGenerateResponse.
func ToOllamaGenerateResponse(resp *schemas.BifrostTextCompletionResponse) *OllamaGenerateResponse {
	text := ""
	thinking := ""
	doneReason := ""
	promptEval, evalCount := 0, 0

	if len(resp.Choices) > 0 {
		c := resp.Choices[0]
		text = extractGenerateText(c)
		thinking = extractGenerateThinking(c)
		if c.FinishReason != nil {
			doneReason = *c.FinishReason
		}
	}
	if resp.Usage != nil {
		promptEval = resp.Usage.PromptTokens
		evalCount = resp.Usage.CompletionTokens
	}
	return &OllamaGenerateResponse{
		Model:           resp.Model,
		Response:        text,
		Thinking:        thinking,
		Done:            true,
		DoneReason:      doneReason,
		PromptEvalCount: promptEval,
		EvalCount:       evalCount,
	}
}

// ToOllamaGenerateStreamChunk converts a streaming BifrostTextCompletionResponse to an NDJSON line.
func ToOllamaGenerateStreamChunk(resp *schemas.BifrostTextCompletionResponse) (string, any, error) {
	text := ""
	thinking := ""
	doneReason := ""
	done := false

	if len(resp.Choices) > 0 {
		c := resp.Choices[0]
		if c.ChatStreamResponseChoice != nil && c.ChatStreamResponseChoice.Delta != nil {
			d := c.ChatStreamResponseChoice.Delta
			if d.Content != nil {
				text = *d.Content
			}
			if d.Reasoning != nil {
				thinking = *d.Reasoning
			}
		} else {
			text = extractGenerateText(c)
			thinking = extractGenerateThinking(c)
		}
		if c.FinishReason != nil {
			done = true
			doneReason = *c.FinishReason
		}
	}

	chunk := &OllamaGenerateResponse{
		Model:      resp.Model,
		Response:   text,
		Thinking:   thinking,
		Done:       done,
		DoneReason: doneReason,
	}
	if done && resp.Usage != nil {
		chunk.PromptEvalCount = resp.Usage.PromptTokens
		chunk.EvalCount = resp.Usage.CompletionTokens
	}

	json, err := NdjsonLine(chunk)
	return "", json, err
}
