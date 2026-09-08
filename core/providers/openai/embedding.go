package openai

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostEmbeddingRequest converts an OpenAI embedding request to Bifrost format.
func (request *OpenAIEmbeddingRequest) ToBifrostEmbeddingRequest(ctx *schemas.BifrostContext) *schemas.BifrostEmbeddingRequest {
	provider, model := schemas.ParseModelString(request.Model, "")

	var embeddingInput []schemas.EmbeddingInputItem
	if request.Input != nil {
		switch {
		case request.Input.Text != nil:
			t := *request.Input.Text
			embeddingInput = []schemas.EmbeddingInputItem{
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &t}}},
			}
		case request.Input.Texts != nil:
			embeddingInput = make([]schemas.EmbeddingInputItem, len(request.Input.Texts))
			for i, text := range request.Input.Texts {
				t := text
				embeddingInput[i] = schemas.EmbeddingInputItem{Content: schemas.EmbeddingContent{
					{Type: schemas.EmbeddingContentPartTypeText, Text: &t},
				}}
			}
		case request.Input.Embedding != nil:
			tokens := request.Input.Embedding
			embeddingInput = []schemas.EmbeddingInputItem{
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeTokens, Tokens: tokens}}},
			}
		case request.Input.Embeddings != nil:
			embeddingInput = make([]schemas.EmbeddingInputItem, len(request.Input.Embeddings))
			for i, tokens := range request.Input.Embeddings {
				t := tokens
				embeddingInput[i] = schemas.EmbeddingInputItem{Content: schemas.EmbeddingContent{
					{Type: schemas.EmbeddingContentPartTypeTokens, Tokens: t},
				}}
			}
		}
	}

	return &schemas.BifrostEmbeddingRequest{
		Provider:  provider,
		Model:     model,
		Input:     embeddingInput,
		Params:    &request.EmbeddingParameters,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}
}

// ToOpenAIEmbeddingRequest converts a Bifrost embedding request to OpenAI format.
func ToOpenAIEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*OpenAIEmbeddingRequest, error) {
	if bifrostReq == nil {
		return nil, nil
	}

	if err := schemas.EmbeddingInput(bifrostReq.Input).RejectPerItemParams("openai"); err != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s", err)
	}

	var input *OpenAIEmbeddingInput
	if len(bifrostReq.Input) > 0 {
		var texts []string
		var tokenBatches [][]int
		for _, item := range bifrostReq.Input {
			content := item.Content
			var sb strings.Builder
			var tokens []int
			for _, part := range content {
				switch part.Type {
				case schemas.EmbeddingContentPartTypeText:
					if part.Text != nil {
						if sb.Len() > 0 {
							sb.WriteString(" \n")
						}
						sb.WriteString(*part.Text)
					}
				case schemas.EmbeddingContentPartTypeTokens:
					if part.Tokens != nil {
						tokens = append(tokens, part.Tokens...)
					}
				default:
					return nil, providerUtils.InvalidRequestErrorf("openai embedding does not support %q input", part.Type)
				}
			}
			if sb.Len() > 0 && len(tokens) > 0 {
				return nil, providerUtils.InvalidRequestErrorf("openai embedding does not support mixing text and token inputs within a single content entry")
			}
			if sb.Len() > 0 {
				texts = append(texts, sb.String())
			} else if len(tokens) > 0 {
				tokenBatches = append(tokenBatches, tokens)
			}
		}

		if len(texts) > 0 && len(tokenBatches) > 0 {
			return nil, providerUtils.InvalidRequestErrorf("openai embedding does not support mixing text and token inputs in the same request")
		}
		switch {
		case len(texts) == 1:
			input = &OpenAIEmbeddingInput{Text: &texts[0]}
		case len(texts) > 1:
			input = &OpenAIEmbeddingInput{Texts: texts}
		case len(tokenBatches) == 1:
			input = &OpenAIEmbeddingInput{Embedding: tokenBatches[0]}
		case len(tokenBatches) > 1:
			input = &OpenAIEmbeddingInput{Embeddings: tokenBatches}
		}
	}

	openaiReq := &OpenAIEmbeddingRequest{
		Model: bifrostReq.Model,
		Input: input,
	}

	if bifrostReq.Params != nil {
		openaiReq.EmbeddingParameters = *bifrostReq.Params
		openaiReq.ExtraParams = bifrostReq.Params.ExtraParams
	}

	return openaiReq, nil
}
