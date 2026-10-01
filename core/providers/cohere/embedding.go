package cohere

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// cohereImageURL returns the image as a data URI, since Cohere rejects bare base64.
func cohereImageURL(media *schemas.EmbeddingMediaPart) (string, bool) {
	if media.URL != nil {
		return *media.URL, true
	}
	if media.Data == nil {
		return "", false
	}
	if media.MIMEType != nil && !strings.HasPrefix(*media.Data, "data:") {
		return "data:" + *media.MIMEType + ";base64," + *media.Data, true
	}
	return *media.Data, true
}

func cohereContentBlockFromEmbeddingPart(part schemas.EmbeddingContentPart) (*CohereContentBlock, error) {
	if err := part.Validate(); err != nil {
		return nil, err
	}
	switch part.Type {
	case schemas.EmbeddingContentPartTypeText:
		text := *part.Text
		return &CohereContentBlock{Type: CohereContentBlockTypeText, Text: &text}, nil
	case schemas.EmbeddingContentPartTypeImage:
		if url, ok := cohereImageURL(part.Image); ok {
			return &CohereContentBlock{
				Type:     CohereContentBlockTypeImage,
				ImageURL: &CohereImageURL{URL: url},
			}, nil
		}
		return nil, providerUtils.InvalidRequestErrorf("cohere image part missing data")
	default:
		return nil, providerUtils.InvalidRequestErrorf("cohere embeddings support only text and image parts")
	}
}

func embeddingContentFromCohereBlocks(blocks []CohereContentBlock) (schemas.EmbeddingContent, error) {
	result := make(schemas.EmbeddingContent, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case CohereContentBlockTypeText:
			if block.Text == nil {
				return nil, providerUtils.InvalidRequestErrorf("cohere text block missing text")
			}
			text := *block.Text
			result = append(result, schemas.EmbeddingContentPart{
				Type: schemas.EmbeddingContentPartTypeText,
				Text: &text,
			})
		case CohereContentBlockTypeImage:
			if block.ImageURL == nil {
				return nil, providerUtils.InvalidRequestErrorf("cohere image block missing image_url")
			}
			url := block.ImageURL.URL
			result = append(result, schemas.EmbeddingContentPart{
				Type:  schemas.EmbeddingContentPartTypeImage,
				Image: &schemas.EmbeddingMediaPart{URL: &url},
			})
		default:
			return nil, providerUtils.InvalidRequestErrorf("unsupported cohere embedding block type %q", block.Type)
		}
	}
	return result, nil
}

// allSingleImages returns every item's image when each item is exactly one image part.
func allSingleImages(contents []schemas.EmbeddingContent) ([]string, bool) {
	images := make([]string, 0, len(contents))
	for _, content := range contents {
		url, ok := isSingleImageContent(content)
		if !ok {
			return nil, false
		}
		images = append(images, url)
	}
	return images, true
}

func isSingleImageContent(content schemas.EmbeddingContent) (string, bool) {
	if len(content) != 1 || content[0].Type != schemas.EmbeddingContentPartTypeImage || content[0].Image == nil {
		return "", false
	}
	return cohereImageURL(content[0].Image)
}

// ToCohereEmbeddingRequest converts a Bifrost embedding request to Cohere format.
func ToCohereEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*CohereEmbeddingRequest, error) {
	if bifrostReq == nil || len(bifrostReq.Input) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("embedding input is not provided")
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

	if len(cohereReq.EmbeddingTypes) == 0 && bifrostReq.Params != nil && bifrostReq.Params.EncodingFormat != nil {
		cohereReq.EmbeddingTypes = []string{*bifrostReq.Params.EncodingFormat}
	}

	if err := schemas.EmbeddingInput(bifrostReq.Input).RejectPerItemParams("cohere"); err != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s", err)
	}
	contents := schemas.EmbeddingInput(bifrostReq.Input).Contents()

	// Pick the shape first: all single-text → texts[], all lone images → images[], else inputs[].
	if schemas.EmbeddingInput(bifrostReq.Input).AllSingleText() {
		cohereReq.Texts = make([]string, len(contents))
		for i, content := range contents {
			cohereReq.Texts[i] = *content[0].Text
		}
	} else if images, ok := allSingleImages(contents); ok {
		cohereReq.Images = images
	} else {
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

	provider, model := schemas.ParseModelString(req.Model, "")

	bifrostReq := &schemas.BifrostEmbeddingRequest{
		Provider: provider,
		Model:    model,

		Params: &schemas.EmbeddingParameters{},
	}

	switch {
	case len(req.Texts) > 0:
		items := make([]schemas.EmbeddingInputItem, len(req.Texts))
		for i, text := range req.Texts {
			t := text
			items[i] = schemas.EmbeddingInputItem{Content: schemas.EmbeddingContent{{
				Type: schemas.EmbeddingContentPartTypeText,
				Text: &t,
			}}}
		}
		bifrostReq.Input = items
	case len(req.Images) > 0:
		items := make([]schemas.EmbeddingInputItem, len(req.Images))
		for i, imgURL := range req.Images {
			u := imgURL
			items[i] = schemas.EmbeddingInputItem{Content: schemas.EmbeddingContent{{
				Type:  schemas.EmbeddingContentPartTypeImage,
				Image: &schemas.EmbeddingMediaPart{URL: &u},
			}}}
		}
		bifrostReq.Input = items
	case len(req.Inputs) > 0:
		items := make([]schemas.EmbeddingInputItem, 0, len(req.Inputs))
		for _, input := range req.Inputs {
			content, err := embeddingContentFromCohereBlocks(input.Content)
			if err != nil {
				return nil, fmt.Errorf("cohere embedding input conversion failed: %w", err)
			}
			items = append(items, schemas.EmbeddingInputItem{Content: content})
		}
		bifrostReq.Input = items
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
		extraParams["priority"] = *req.Priority
	}
	if req.MaxTokens != nil {
		extraParams["max_tokens"] = *req.MaxTokens
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

	if emb := response.Embeddings; emb != nil {
		// One entry per representation, labelled with the encoding it arrived as.
		for i, e := range emb.Float {
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:         "embedding",
				Index:          i,
				Embedding:      schemas.EmbeddingStruct{EmbeddingArray: e},
				EncodingFormat: schemas.EmbeddingEncodingFloat,
			})
		}
		for i, e := range emb.Base64 {
			v := e
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:         "embedding",
				Index:          i,
				Embedding:      schemas.EmbeddingStruct{EmbeddingStr: &v},
				EncodingFormat: schemas.EmbeddingEncodingBase64,
			})
		}
		for i, e := range emb.Int8 {
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:         "embedding",
				Index:          i,
				Embedding:      schemas.EmbeddingStruct{EmbeddingInt8Array: e},
				EncodingFormat: schemas.EmbeddingEncodingInt8,
			})
		}
		for i, e := range emb.Binary {
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:         "embedding",
				Index:          i,
				Embedding:      schemas.EmbeddingStruct{EmbeddingInt8Array: e},
				EncodingFormat: schemas.EmbeddingEncodingBinary,
			})
		}
		for i, e := range emb.Uint8 {
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:         "embedding",
				Index:          i,
				Embedding:      schemas.EmbeddingStruct{EmbeddingInt32Array: e},
				EncodingFormat: schemas.EmbeddingEncodingUint8,
			})
		}
		for i, e := range emb.Ubinary {
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:         "embedding",
				Index:          i,
				Embedding:      schemas.EmbeddingStruct{EmbeddingInt32Array: e},
				EncodingFormat: schemas.EmbeddingEncodingUbinary,
			})
		}
	}

	if response.Meta != nil {
		var usage *schemas.BifrostLLMUsage
		if response.Meta.Tokens != nil {
			usage = &schemas.BifrostLLMUsage{}
			if response.Meta.Tokens.InputTokens != nil {
				usage.PromptTokens = *response.Meta.Tokens.InputTokens
			}
			if response.Meta.Tokens.OutputTokens != nil {
				usage.CompletionTokens = *response.Meta.Tokens.OutputTokens
			}
		} else if response.Meta.BilledUnits != nil {
			usage = &schemas.BifrostLLMUsage{}
			if response.Meta.BilledUnits.InputTokens != nil {
				usage.PromptTokens = *response.Meta.BilledUnits.InputTokens
			}
			if response.Meta.BilledUnits.OutputTokens != nil {
				usage.CompletionTokens = *response.Meta.BilledUnits.OutputTokens
			}
		}
		// Images bill on their own counter, outside input_tokens, and only ever appear
		// on billed_units. Kept out of PromptTokens so embedding cost stays text-only.
		if response.Meta.BilledUnits != nil && response.Meta.BilledUnits.ImageTokens != nil && *response.Meta.BilledUnits.ImageTokens > 0 {
			if usage == nil {
				usage = &schemas.BifrostLLMUsage{}
			}
			usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{ImageTokens: *response.Meta.BilledUnits.ImageTokens}
		}
		if usage != nil {
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			if usage.PromptTokensDetails != nil {
				usage.TotalTokens += usage.PromptTokensDetails.ImageTokens
			}
			bifrostResponse.Usage = usage
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

	// Cohere arrays carry no index, so position must follow the input index.
	data := slices.Clone(bifrostResp.Data)
	slices.SortStableFunc(data, func(a, b schemas.EmbeddingData) int { return cmp.Compare(a.Index, b.Index) })

	// Unlabelled entries mean the provider returned only plain float vectors.
	isTyped := false
	for _, item := range data {
		emb := item.Embedding
		switch item.EncodingFormat {
		case schemas.EmbeddingEncodingFloat:
			isTyped = true
			cohereResp.Embeddings.Float = append(cohereResp.Embeddings.Float, emb.EmbeddingArray)
		case schemas.EmbeddingEncodingBase64:
			isTyped = true
			if emb.EmbeddingStr != nil {
				cohereResp.Embeddings.Base64 = append(cohereResp.Embeddings.Base64, *emb.EmbeddingStr)
			}
		case schemas.EmbeddingEncodingInt8:
			isTyped = true
			cohereResp.Embeddings.Int8 = append(cohereResp.Embeddings.Int8, emb.EmbeddingInt8Array)
		case schemas.EmbeddingEncodingBinary:
			isTyped = true
			cohereResp.Embeddings.Binary = append(cohereResp.Embeddings.Binary, emb.EmbeddingInt8Array)
		case schemas.EmbeddingEncodingUint8:
			isTyped = true
			cohereResp.Embeddings.Uint8 = append(cohereResp.Embeddings.Uint8, emb.EmbeddingInt32Array)
		case schemas.EmbeddingEncodingUbinary:
			isTyped = true
			cohereResp.Embeddings.Ubinary = append(cohereResp.Embeddings.Ubinary, emb.EmbeddingInt32Array)
		default:
			if emb.EmbeddingArray != nil {
				cohereResp.Embeddings.Float = append(cohereResp.Embeddings.Float, emb.EmbeddingArray)
			}
		}
	}

	if isTyped {
		cohereResp.ResponseType = schemas.Ptr("embeddings_by_type")
	} else {
		cohereResp.ResponseType = schemas.Ptr("embeddings_floats")
	}

	// /v2/embed reports usage as billed_units only.
	if bifrostResp.Usage != nil {
		inputTokens := bifrostResp.Usage.PromptTokens
		imageTokens := 0
		if details := bifrostResp.Usage.PromptTokensDetails; details != nil {
			imageTokens = details.ImageTokens
		}
		cohereResp.Meta = &CohereEmbeddingMeta{
			BilledUnits: &CohereBilledUnits{
				InputTokens: &inputTokens,
				ImageTokens: &imageTokens,
			},
		}
	}

	return cohereResp
}
