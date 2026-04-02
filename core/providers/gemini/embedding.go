package gemini

import (
	"fmt"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func mediaPartToGeminiPart(partType schemas.EmbeddingContentPartType, media *schemas.EmbeddingMediaPart) (*Part, error) {
	if err := media.Validate(); err != nil {
		return nil, err
	}

	defaultMime := map[schemas.EmbeddingContentPartType]string{
		schemas.EmbeddingContentPartTypeImage: "image/jpeg",
		schemas.EmbeddingContentPartTypeAudio: "audio/mpeg",
		schemas.EmbeddingContentPartTypeFile:  "application/pdf",
		schemas.EmbeddingContentPartTypeVideo: "video/mp4",
	}[partType]

	if media.Data != nil {
		dataBytes, extractedMime := convertFileDataToBytes(*media.Data)
		if len(dataBytes) == 0 {
			return nil, fmt.Errorf("empty media data for %s part", partType)
		}
		mimeType := defaultMime
		if media.MIMEType != nil && strings.TrimSpace(*media.MIMEType) != "" {
			mimeType = *media.MIMEType
		} else if extractedMime != "" {
			mimeType = extractedMime
		}
		return &Part{
			InlineData: &Blob{
				MIMEType: mimeType,
				Data:     encodeBytesToBase64String(dataBytes),
			},
		}, nil
	}

	mimeType := defaultMime
	if media.MIMEType != nil && strings.TrimSpace(*media.MIMEType) != "" {
		mimeType = *media.MIMEType
	}
	url := *media.URL
	if partType == schemas.EmbeddingContentPartTypeImage {
		sanitizedURL, err := schemas.SanitizeImageURL(url)
		if err != nil {
			return nil, err
		}
		urlInfo := schemas.ExtractURLTypeInfo(sanitizedURL)
		if urlInfo.Type == schemas.ImageContentTypeBase64 {
			data := ""
			if urlInfo.DataURLWithoutPrefix != nil {
				data = *urlInfo.DataURLWithoutPrefix
			}
			decoded, err := decodeBase64StringToBytes(data)
			if err != nil {
				return nil, err
			}
			if urlInfo.MediaType != nil && (media.MIMEType == nil || *media.MIMEType == "") {
				mimeType = *urlInfo.MediaType
			}
			return &Part{
				InlineData: &Blob{
					MIMEType: mimeType,
					Data:     encodeBytesToBase64String(decoded),
				},
			}, nil
		}
		url = sanitizedURL
	}

	return &Part{
		FileData: &FileData{
			FileURI:  url,
			MIMEType: mimeType,
			DisplayName: func() string {
				if media.Filename != nil {
					return *media.Filename
				}
				return ""
			}(),
		},
	}, nil
}

func embeddingContentPartToGeminiPart(part schemas.EmbeddingContentPart) (*Part, error) {
	if err := part.Validate(); err != nil {
		return nil, err
	}

	switch part.Type {
	case schemas.EmbeddingContentPartTypeText:
		return &Part{Text: *part.Text}, nil
	case schemas.EmbeddingContentPartTypeImage:
		return mediaPartToGeminiPart(part.Type, part.Image)
	case schemas.EmbeddingContentPartTypeAudio:
		return mediaPartToGeminiPart(part.Type, part.Audio)
	case schemas.EmbeddingContentPartTypeFile:
		return mediaPartToGeminiPart(part.Type, part.File)
	case schemas.EmbeddingContentPartTypeVideo:
		return mediaPartToGeminiPart(part.Type, part.Video)
	default:
		return nil, fmt.Errorf("unsupported embedding content part type %q", part.Type)
	}
}

// EmbeddingContentToGeminiContent converts a Bifrost EmbeddingContent (a slice
// of typed parts) into the Gemini Content struct used by both the embedContent
// and batchEmbedContents endpoints.
func EmbeddingContentToGeminiContent(content schemas.EmbeddingContent) (*Content, error) {
	if err := content.Validate(); err != nil {
		return nil, err
	}
	parts := make([]*Part, 0, len(content))
	for _, contentPart := range content {
		part, err := embeddingContentPartToGeminiPart(contentPart)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return &Content{Parts: parts}, nil
}

func applyGeminiEmbeddingParams(req *GeminiEmbeddingRequest, params *schemas.EmbeddingParameters) {
	if params == nil {
		return
	}
	req.OutputDimensionality = params.Dimensions
	req.TaskType = params.TaskType
	req.Title = params.Title

	if params.ExtraParams != nil {
		req.ExtraParams = params.ExtraParams
		if documentOCR, ok := schemas.SafeExtractBoolPointer(params.ExtraParams["documentOcr"]); ok {
			delete(req.ExtraParams, "documentOcr")
			req.DocumentOCR = documentOCR
		}
		if audioTrackExtraction, ok := schemas.SafeExtractBoolPointer(params.ExtraParams["audioTrackExtraction"]); ok {
			delete(req.ExtraParams, "audioTrackExtraction")
			req.AudioTrackExtraction = audioTrackExtraction
		}
	}
}

// ToGeminiEmbeddingRequest converts a Bifrost embedding request to Gemini request format.
// Each element in Contents maps to one GeminiEmbeddingRequest (one output embedding).
// Parts within a single content are aggregated into one embedding by Gemini.
func ToGeminiEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*GeminiBatchEmbeddingRequest, error) {
	if bifrostReq == nil || len(bifrostReq.Input) == 0 {
		return nil, fmt.Errorf("bifrost request is nil or input is nil")
	}

	contents := bifrostReq.Input

	batchRequest := &GeminiBatchEmbeddingRequest{
		Requests: make([]GeminiEmbeddingRequest, 0, len(contents)),
	}
	if bifrostReq.Params != nil {
		batchRequest.ExtraParams = bifrostReq.Params.ExtraParams
	}
	for _, contentItem := range contents {
		content, err := EmbeddingContentToGeminiContent(contentItem)
		if err != nil {
			return nil, fmt.Errorf("error converting embedding content to gemini content: %w", err)
		}
		req := GeminiEmbeddingRequest{
			Model:   "models/" + bifrostReq.Model,
			Content: content,
		}
		applyGeminiEmbeddingParams(&req, bifrostReq.Params)
		batchRequest.Requests = append(batchRequest.Requests, req)
	}
	return batchRequest, nil
}

// ToGeminiBatchEmbeddingRequest converts a BifrostBatchEmbeddingRequest to Gemini's batchEmbedContents format.
// Each item maps to one GeminiEmbeddingRequest. Item-level Params overrides the batch-level default.
func ToGeminiBatchEmbeddingRequest(bifrostReq *schemas.BifrostBatchEmbeddingRequest) (*GeminiBatchEmbeddingRequest, error) {
	if bifrostReq == nil || len(bifrostReq.Items) == 0 {
		return nil, fmt.Errorf("batch embedding request has no items")
	}

	batchRequest := &GeminiBatchEmbeddingRequest{
		Requests: make([]GeminiEmbeddingRequest, 0, len(bifrostReq.Items)),
	}
	if bifrostReq.Params != nil {
		batchRequest.ExtraParams = bifrostReq.Params.ExtraParams
	}

	for _, item := range bifrostReq.Items {
		content, err := EmbeddingContentToGeminiContent(item.Content)
		if err != nil {
			return nil, fmt.Errorf("error converting embedding content to gemini content: %w", err)
		}
		req := GeminiEmbeddingRequest{
			Model:   "models/" + bifrostReq.Model,
			Content: content,
		}
		applyGeminiEmbeddingParams(&req, item.EffectiveParams(bifrostReq.Params))
		batchRequest.Requests = append(batchRequest.Requests, req)
	}
	return batchRequest, nil
}

// ToGeminiEmbeddingResponse converts a BifrostResponse with embedding data to Gemini's embedding response format
func ToGeminiEmbeddingResponse(bifrostResp *schemas.BifrostEmbeddingResponse) *GeminiEmbeddingResponse {
	if bifrostResp == nil || len(bifrostResp.Data) == 0 {
		return nil
	}

	geminiResp := &GeminiEmbeddingResponse{
		Embeddings: make([]GeminiEmbedding, len(bifrostResp.Data)),
	}

	for i, embedding := range bifrostResp.Data {
		geminiEmbedding := GeminiEmbedding{Values: append([]float64(nil), embedding.Embedding.Float...)}
		if bifrostResp.Usage != nil && len(bifrostResp.Data) == 1 {
			geminiEmbedding.Statistics = &ContentEmbeddingStatistics{
				TokenCount: int32(bifrostResp.Usage.PromptTokens),
			}
		}
		geminiResp.Embeddings[i] = geminiEmbedding
	}

	if len(geminiResp.Embeddings) == 1 {
		geminiResp.Embedding = &geminiResp.Embeddings[0]
	}
	if bifrostResp.Usage != nil {
		geminiResp.Metadata = &EmbedContentMetadata{
			BillableCharacterCount: int32(bifrostResp.Usage.PromptTokens),
		}
	}
	return geminiResp
}

func geminiResponseEmbeddings(resp *GeminiEmbeddingResponse) []GeminiEmbedding {
	if resp == nil {
		return nil
	}
	if len(resp.Embeddings) > 0 {
		return resp.Embeddings
	}
	if resp.Embedding != nil {
		return []GeminiEmbedding{*resp.Embedding}
	}
	return nil
}

// ToBifrostEmbeddingResponse converts a Gemini embedding response to BifrostEmbeddingResponse format
func ToBifrostEmbeddingResponse(geminiResp *GeminiEmbeddingResponse, model string) *schemas.BifrostEmbeddingResponse {
	embeddings := geminiResponseEmbeddings(geminiResp)
	if len(embeddings) == 0 {
		return nil
	}

	bifrostResp := &schemas.BifrostEmbeddingResponse{
		Data:   make([]schemas.EmbeddingData, len(embeddings)),
		Model:  model,
		Object: "list",
	}

	for i, geminiEmbedding := range embeddings {
		bifrostResp.Data[i] = schemas.EmbeddingData{
			Index:  i,
			Object: "embedding",
			Embedding: schemas.EmbeddingsByType{
				Float: geminiEmbedding.Values,
			},
		}
	}

	hasStats := false
	for _, emb := range embeddings {
		if emb.Statistics != nil {
			hasStats = true
			break
		}
	}
	if geminiResp.Metadata != nil || hasStats {
		bifrostResp.Usage = &schemas.BifrostLLMUsage{}
		var totalTokens int
		for _, emb := range embeddings {
			if emb.Statistics != nil {
				totalTokens += int(emb.Statistics.TokenCount)
			}
		}
		if totalTokens > 0 {
			bifrostResp.Usage.PromptTokens = totalTokens
		} else if geminiResp.Metadata != nil {
			bifrostResp.Usage.PromptTokens = int(geminiResp.Metadata.BillableCharacterCount)
		}
		bifrostResp.Usage.TotalTokens = bifrostResp.Usage.PromptTokens
	}

	return bifrostResp
}

func geminiPartToEmbeddingContentPart(part *Part) (*schemas.EmbeddingContentPart, error) {
	if part == nil {
		return nil, fmt.Errorf("gemini part is nil")
	}
	switch {
	case part.Text != "":
		text := part.Text
		return &schemas.EmbeddingContentPart{
			Type: schemas.EmbeddingContentPartTypeText,
			Text: &text,
		}, nil
	case part.InlineData != nil:
		mimeType := strings.ToLower(strings.TrimSpace(part.InlineData.MIMEType))
		data := fmt.Sprintf("data:%s;base64,%s", part.InlineData.MIMEType, part.InlineData.Data)
		mime := part.InlineData.MIMEType
		media := &schemas.EmbeddingMediaPart{
			Data:     &data,
			MIMEType: &mime,
		}
		switch {
		case strings.HasPrefix(mimeType, "image/"):
			return &schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeImage, Image: media}, nil
		case strings.HasPrefix(mimeType, "audio/"):
			return &schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeAudio, Audio: media}, nil
		case strings.HasPrefix(mimeType, "video/"):
			return &schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeVideo, Video: media}, nil
		default:
			return &schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeFile, File: media}, nil
		}
	case part.FileData != nil:
		uri := part.FileData.FileURI
		mime := part.FileData.MIMEType
		media := &schemas.EmbeddingMediaPart{
			URL:      &uri,
			MIMEType: &mime,
		}
		if part.FileData.DisplayName != "" {
			name := part.FileData.DisplayName
			media.Filename = &name
		}
		mimeType := strings.ToLower(strings.TrimSpace(part.FileData.MIMEType))
		switch {
		case strings.HasPrefix(mimeType, "image/"):
			return &schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeImage, Image: media}, nil
		case strings.HasPrefix(mimeType, "audio/"):
			return &schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeAudio, Audio: media}, nil
		case strings.HasPrefix(mimeType, "video/"):
			return &schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeVideo, Video: media}, nil
		default:
			return &schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeFile, File: media}, nil
		}
	default:
		return nil, fmt.Errorf("unsupported gemini embedding part")
	}
}

func geminiContentToEmbeddingContent(content *Content) (schemas.EmbeddingContent, error) {
	if content == nil {
		return nil, fmt.Errorf("gemini embedding content is nil")
	}
	result := make(schemas.EmbeddingContent, 0, len(content.Parts))
	for _, part := range content.Parts {
		converted, err := geminiPartToEmbeddingContentPart(part)
		if err != nil {
			return nil, err
		}
		result = append(result, *converted)
	}
	return result, nil
}

func applyBifrostEmbeddingParams(params *schemas.EmbeddingParameters, req GeminiEmbeddingRequest) *schemas.EmbeddingParameters {
	if params == nil {
		params = &schemas.EmbeddingParameters{}
	}
	changed := false
	if req.OutputDimensionality != nil {
		params.Dimensions = req.OutputDimensionality
		changed = true
	}
	if req.TaskType != nil {
		params.TaskType = req.TaskType
		changed = true
	}
	if req.Title != nil {
		params.Title = req.Title
		changed = true
	}
	if req.DocumentOCR != nil {
		if params.ExtraParams == nil {
			params.ExtraParams = map[string]interface{}{}
		}
		params.ExtraParams["documentOcr"] = req.DocumentOCR
		changed = true
	}
	if req.AudioTrackExtraction != nil {
		if params.ExtraParams == nil {
			params.ExtraParams = map[string]interface{}{}
		}
		params.ExtraParams["audioTrackExtraction"] = req.AudioTrackExtraction
		changed = true
	}
	if !changed {
		return nil
	}
	return params
}

// ToBifrostBatchEmbeddingRequest converts a GeminiBatchEmbeddingRequest (wire format from
// :batchEmbedContents) to BifrostBatchEmbeddingRequest. Per-item taskType/title/dimensions
// are preserved as item-level Params; shared params across all items become the batch default.
func (r *GeminiBatchEmbeddingRequest) ToBifrostBatchEmbeddingRequest(ctx *schemas.BifrostContext) (*schemas.BifrostBatchEmbeddingRequest, error) {
	if r == nil || len(r.Requests) == 0 {
		return nil, fmt.Errorf("batch embedding request is empty")
	}

	provider, model := schemas.ParseModelString(r.Model, providerUtils.CheckAndSetDefaultProvider(ctx, schemas.Gemini))

	bifrostReq := &schemas.BifrostBatchEmbeddingRequest{
		Provider:  provider,
		Model:     model,
		Params:    applyBifrostEmbeddingParams(nil, r.Requests[0]), // first request params as batch-level default
		Fallbacks: schemas.ParseFallbacks(nil),
		Items:     make([]schemas.BifrostEmbeddingBatchItem, 0, len(r.Requests)),
	}

	for _, req := range r.Requests {
		content, err := geminiContentToEmbeddingContent(req.Content)
		if err != nil {
			return nil, fmt.Errorf("error converting embedding content: %w", err)
		}
		bifrostReq.Items = append(bifrostReq.Items, schemas.BifrostEmbeddingBatchItem{
			Content: content,
			Params:  applyBifrostEmbeddingParams(nil, req),
		})
	}

	return bifrostReq, nil
}

// ToBifrostEmbeddingRequest converts a GeminiGenerationRequest to BifrostEmbeddingRequest format.
// Each request entry maps to one element in Contents (one output embedding).
func (request *GeminiGenerationRequest) ToBifrostEmbeddingRequest(ctx *schemas.BifrostContext) *schemas.BifrostEmbeddingRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, providerUtils.CheckAndSetDefaultProvider(ctx, schemas.Gemini))
	bifrostReq := &schemas.BifrostEmbeddingRequest{
		Provider:  provider,
		Model:     model,
		
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}

	if len(request.Requests) > 0 {
		contents := make([]schemas.EmbeddingContent, 0, len(request.Requests))
		for _, req := range request.Requests {
			content, err := geminiContentToEmbeddingContent(req.Content)
			if err != nil {
				return nil
			}
			contents = append(contents, content)
		}
		bifrostReq.Input = contents
		bifrostReq.Params = applyBifrostEmbeddingParams(bifrostReq.Params, request.Requests[0])
		return bifrostReq
	}

	if len(request.Contents) > 0 {
		contents := make([]schemas.EmbeddingContent, 0, len(request.Contents))
		for _, content := range request.Contents {
			converted, err := geminiContentToEmbeddingContent(&content)
			if err != nil {
				return nil
			}
			contents = append(contents, converted)
		}
		bifrostReq.Input = contents
	}

	return bifrostReq
}
