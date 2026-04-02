package cohere

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func cohereContentBlockFromEmbeddingPart(part schemas.EmbeddingContentPart) (*CohereContentBlock, error) {
	if err := part.Validate(); err != nil {
		return nil, err
	}
	switch part.Type {
	case schemas.EmbeddingContentPartTypeText:
		text := *part.Text
		return &CohereContentBlock{Type: CohereContentBlockTypeText, Text: &text}, nil
	case schemas.EmbeddingContentPartTypeImage:
		if part.Image.URL != nil {
			return &CohereContentBlock{
				Type:     CohereContentBlockTypeImage,
				ImageURL: &CohereImageURL{URL: *part.Image.URL},
			}, nil
		}
		if part.Image.Data != nil {
			return &CohereContentBlock{
				Type:     CohereContentBlockTypeImage,
				ImageURL: &CohereImageURL{URL: *part.Image.Data},
			}, nil
		}
		return nil, fmt.Errorf("cohere image part missing data")
	default:
		return nil, fmt.Errorf("cohere embeddings support only text and image parts")
	}
}

func embeddingContentFromCohereBlocks(blocks []CohereContentBlock) (schemas.EmbeddingContent, error) {
	result := make(schemas.EmbeddingContent, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case CohereContentBlockTypeText:
			if block.Text == nil {
				return nil, fmt.Errorf("cohere text block missing text")
			}
			text := *block.Text
			result = append(result, schemas.EmbeddingContentPart{
				Type: schemas.EmbeddingContentPartTypeText,
				Text: &text,
			})
		case CohereContentBlockTypeImage:
			if block.ImageURL == nil {
				return nil, fmt.Errorf("cohere image block missing image_url")
			}
			url := block.ImageURL.URL
			result = append(result, schemas.EmbeddingContentPart{
				Type:  schemas.EmbeddingContentPartTypeImage,
				Image: &schemas.EmbeddingMediaPart{URL: &url},
			})
		default:
			return nil, fmt.Errorf("unsupported cohere embedding block type %q", block.Type)
		}
	}
	return result, nil
}

func isSingleImageContent(content schemas.EmbeddingContent) (string, bool) {
	if len(content) != 1 || content[0].Type != schemas.EmbeddingContentPartTypeImage || content[0].Image == nil {
		return "", false
	}
	if content[0].Image.URL != nil {
		return *content[0].Image.URL, true
	}
	if content[0].Image.Data != nil {
		return *content[0].Image.Data, true
	}
	return "", false
}

// ToCohereEmbeddingRequest converts a Bifrost embedding request to Cohere format.
func ToCohereEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*CohereEmbeddingRequest, error) {
	if bifrostReq == nil || len(bifrostReq.Input) == 0 {
		return nil, fmt.Errorf("embedding input is not provided")
	}

	cohereReq := &CohereEmbeddingRequest{
		Model:     bifrostReq.Model,
		InputType: "search_document",
	}
	if bifrostReq.Params != nil {
		cohereReq.OutputDimension = bifrostReq.Params.Dimensions

		if bifrostReq.Params.ExtraParams != nil {
			cohereReq.ExtraParams = bifrostReq.Params.ExtraParams

			if embeddingTypes, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["embedding_types"]); ok {
				delete(cohereReq.ExtraParams, "embedding_types")
				cohereReq.EmbeddingTypes = embeddingTypes
			}
			if inputType, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["input_type"]); ok {
				delete(cohereReq.ExtraParams, "input_type")
				cohereReq.InputType = inputType
			}
			if priority, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["priority"]); ok {
				delete(cohereReq.ExtraParams, "priority")
				cohereReq.Priority = priority
			}
			if maxTokens, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["max_tokens"]); ok {
				delete(cohereReq.ExtraParams, "max_tokens")
				cohereReq.MaxTokens = maxTokens
			}
			if truncate, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["truncate"]); ok {
				delete(cohereReq.ExtraParams, "truncate")
				cohereReq.Truncate = truncate
			}
		}
	}

	contents := bifrostReq.Input

	// All single-text contents → texts[]
	texts := make([]string, 0, len(contents))
	allSingleText := true
	for _, content := range contents {
		if len(content) == 1 && content[0].Type == schemas.EmbeddingContentPartTypeText && content[0].Text != nil {
			texts = append(texts, *content[0].Text)
		} else {
			allSingleText = false
			break
		}
	}
	if allSingleText {
		cohereReq.Texts = texts
	} else if len(contents) == 1 {
		// Single content with single image → images[]
		if imageURL, ok := isSingleImageContent(contents[0]); ok {
			cohereReq.Images = []string{imageURL}
		} else {
			// Single multimodal content → inputs[] with one entry
			blocks := make([]CohereContentBlock, 0, len(contents[0]))
			for _, part := range contents[0] {
				block, err := cohereContentBlockFromEmbeddingPart(part)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, *block)
			}
			cohereReq.Inputs = []CohereEmbeddingInput{{Content: blocks}}
		}
	} else {
		// Batch multimodal → inputs[], one entry per content
		inputs := make([]CohereEmbeddingInput, 0, len(contents))
		for _, content := range contents {
			blocks := make([]CohereContentBlock, 0, len(content))
			for _, part := range content {
				block, err := cohereContentBlockFromEmbeddingPart(part)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, *block)
			}
			inputs = append(inputs, CohereEmbeddingInput{Content: blocks})
		}
		cohereReq.Inputs = inputs
	}

	return cohereReq, nil
}

// ToBifrostEmbeddingRequest converts a Cohere embedding request to Bifrost format.
// Each Cohere input entry maps to one element in Contents (one output embedding).
func (req *CohereEmbeddingRequest) ToBifrostEmbeddingRequest(ctx *schemas.BifrostContext) (*schemas.BifrostEmbeddingRequest, error) {
	if req == nil {
		return nil, nil
	}

	provider, model := schemas.ParseModelString(req.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.Cohere))
	bifrostReq := &schemas.BifrostEmbeddingRequest{
		Provider: provider,
		Model:    model,
		
		Params:   &schemas.EmbeddingParameters{},
	}

	switch {
	case len(req.Texts) > 0:
		contents := make([]schemas.EmbeddingContent, len(req.Texts))
		for i, text := range req.Texts {
			t := text
			contents[i] = schemas.EmbeddingContent{{
				Type: schemas.EmbeddingContentPartTypeText,
				Text: &t,
			}}
		}
		bifrostReq.Input = contents
	case len(req.Images) > 0:
		contents := make([]schemas.EmbeddingContent, len(req.Images))
		for i, imgURL := range req.Images {
			u := imgURL
			contents[i] = schemas.EmbeddingContent{{
				Type:  schemas.EmbeddingContentPartTypeImage,
				Image: &schemas.EmbeddingMediaPart{URL: &u},
			}}
		}
		bifrostReq.Input = contents
	case len(req.Inputs) > 0:
		contents := make([]schemas.EmbeddingContent, 0, len(req.Inputs))
		for _, input := range req.Inputs {
			content, err := embeddingContentFromCohereBlocks(input.Content)
			if err != nil {
				return nil, fmt.Errorf("cohere embedding input conversion failed: %w", err)
			}
			contents = append(contents, content)
		}
		bifrostReq.Input = contents
	}

	bifrostReq.Params.Dimensions = req.OutputDimension

	extraParams := req.ExtraParams
	if extraParams == nil {
		extraParams = make(map[string]interface{})
	}
	if req.InputType != "" {
		extraParams["input_type"] = req.InputType
	}
	if len(req.EmbeddingTypes) > 0 {
		extraParams["embedding_types"] = req.EmbeddingTypes
	}
	if req.Priority != nil {
		extraParams["priority"] = req.Priority
	}
	if req.MaxTokens != nil {
		extraParams["max_tokens"] = req.MaxTokens
	}
	if req.Truncate != nil {
		extraParams["truncate"] = req.Truncate
	}
	bifrostReq.Params.ExtraParams = extraParams

	return bifrostReq, nil
}

// ToBifrostEmbeddingResponse converts a Cohere embedding response to Bifrost format
func (response *CohereEmbeddingResponse) ToBifrostEmbeddingResponse() *schemas.BifrostEmbeddingResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostEmbeddingResponse{
		Object: "list",
	}

	if response.Embeddings != nil {
		emb := response.Embeddings

		// Determine the number of entries from whichever type is populated
		count := 0
		switch {
		case len(emb.Float) > 0:
			count = len(emb.Float)
		case len(emb.Int8) > 0:
			count = len(emb.Int8)
		case len(emb.Uint8) > 0:
			count = len(emb.Uint8)
		case len(emb.Binary) > 0:
			count = len(emb.Binary)
		case len(emb.Ubinary) > 0:
			count = len(emb.Ubinary)
		case len(emb.Base64) > 0:
			count = len(emb.Base64)
		}

		bifrostResponse.Data = make([]schemas.EmbeddingData, count)
		for i := 0; i < count; i++ {
			entry := schemas.EmbeddingData{
				Index:     i,
				Object:    "embedding",
				Embedding: schemas.EmbeddingsByType{},
			}

			if len(emb.Float) > i {
				entry.Embedding.Float = emb.Float[i]
			}
			if len(emb.Int8) > i {
				entry.Embedding.Int8 = emb.Int8[i]
			}
			if len(emb.Uint8) > i {
				entry.Embedding.Uint8 = emb.Uint8[i]
			}
			if len(emb.Binary) > i {
				entry.Embedding.Binary = emb.Binary[i]
			}
			if len(emb.Ubinary) > i {
				entry.Embedding.Ubinary = emb.Ubinary[i]
			}
			if len(emb.Base64) > i {
				entry.Embedding.Base64 = &emb.Base64[i]
			}

			bifrostResponse.Data[i] = entry
		}
	}

	if response.Meta != nil {
		if response.Meta.Tokens != nil {
			bifrostResponse.Usage = &schemas.BifrostLLMUsage{}
			if response.Meta.Tokens.InputTokens != nil {
				bifrostResponse.Usage.PromptTokens = int(*response.Meta.Tokens.InputTokens)
			}
			if response.Meta.Tokens.OutputTokens != nil {
				bifrostResponse.Usage.CompletionTokens = int(*response.Meta.Tokens.OutputTokens)
			}
			bifrostResponse.Usage.TotalTokens = bifrostResponse.Usage.PromptTokens + bifrostResponse.Usage.CompletionTokens
		} else if response.Meta.BilledUnits != nil {
			bifrostResponse.Usage = &schemas.BifrostLLMUsage{}
			if response.Meta.BilledUnits.InputTokens != nil {
				bifrostResponse.Usage.PromptTokens = int(*response.Meta.BilledUnits.InputTokens)
			}
			if response.Meta.BilledUnits.OutputTokens != nil {
				bifrostResponse.Usage.CompletionTokens = int(*response.Meta.BilledUnits.OutputTokens)
			}
			bifrostResponse.Usage.TotalTokens = bifrostResponse.Usage.PromptTokens + bifrostResponse.Usage.CompletionTokens
		}
	}

	return bifrostResponse
}

// ToCohereEmbeddingResponse converts a BifrostEmbeddingResponse to Cohere's native embedding response format.
func ToCohereEmbeddingResponse(bifrostResp *schemas.BifrostEmbeddingResponse) *CohereEmbeddingResponse {
	if bifrostResp == nil || len(bifrostResp.Data) == 0 {
		return nil
	}

	cohereResp := &CohereEmbeddingResponse{
		ID:         uuid.New().String(),
		Embeddings: &CohereEmbeddingData{},
	}

	for _, item := range bifrostResp.Data {
		emb := item.Embedding

		if emb.Float != nil {
			cohereResp.Embeddings.Float = append(cohereResp.Embeddings.Float, emb.Float)
		}
		if emb.Int8 != nil {
			cohereResp.Embeddings.Int8 = append(cohereResp.Embeddings.Int8, emb.Int8)
		}
		if emb.Uint8 != nil {
			cohereResp.Embeddings.Uint8 = append(cohereResp.Embeddings.Uint8, emb.Uint8)
		}
		if emb.Binary != nil {
			cohereResp.Embeddings.Binary = append(cohereResp.Embeddings.Binary, emb.Binary)
		}
		if emb.Ubinary != nil {
			cohereResp.Embeddings.Ubinary = append(cohereResp.Embeddings.Ubinary, emb.Ubinary)
		}
		if emb.Base64 != nil {
			cohereResp.Embeddings.Base64 = append(cohereResp.Embeddings.Base64, *emb.Base64)
		}
	}

	cohereResp.ResponseType = schemas.Ptr("embeddings_by_type")

	if bifrostResp.Usage != nil {
		inputTokens := bifrostResp.Usage.PromptTokens
		outputTokens := bifrostResp.Usage.CompletionTokens
		cohereResp.Meta = &CohereEmbeddingMeta{
			BilledUnits: &CohereBilledUnits{
				InputTokens:  &inputTokens,
				OutputTokens: &outputTokens,
			},
			Tokens: &CohereTokenUsage{
				InputTokens:  &inputTokens,
				OutputTokens: &outputTokens,
			},
		}
	}

	return cohereResp
}
