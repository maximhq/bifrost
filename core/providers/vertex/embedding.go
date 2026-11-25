package vertex

import (
	"maps"
	"strings"

	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// isVertexNativeMultimodalEmbeddingModel returns true for the Vertex-native
// multimodal embedding model (multimodalembedding@001). This model uses the
// :predict endpoint but with a different instance format (text/image/video fields
// instead of content) and a different response format (textEmbedding/imageEmbedding).
func isVertexNativeMultimodalEmbeddingModel(model string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(model)), "multimodalembedding")
}

func isVertexGeminiEmbeddingModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "gemini-embedding-2")
}

// ToVertexEmbeddingRequest converts a Bifrost embedding request to Vertex AI text embedding format.
func ToVertexEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*VertexEmbeddingRequest, error) {
	if bifrostReq == nil || len(bifrostReq.Input) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("embedding input is not provided")
	}

	if err := schemas.EmbeddingInput(bifrostReq.Input).RejectPerItemParams(); err != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s", err)
	}

	instances := make([]VertexEmbeddingInstance, 0, len(bifrostReq.Input))
	for _, item := range bifrostReq.Input {
		var sb strings.Builder
		for _, part := range item.Content {
			if part.Type != schemas.EmbeddingContentPartTypeText || part.Text == nil {
				return nil, providerUtils.InvalidRequestErrorf("this model only supports text parts; got %q", part.Type)
			}
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(*part.Text)
		}

		instance := VertexEmbeddingInstance{Content: sb.String()}
		if bifrostReq.Params != nil {
			instance.TaskType = bifrostReq.Params.TaskType
			instance.Title = bifrostReq.Params.Title
		}
		instances = append(instances, instance)
	}

	vertexReq := &VertexEmbeddingRequest{Instances: instances}
	if bifrostReq.Params != nil {
		vertexReq.ExtraParams = bifrostReq.Params.ExtraParams
		autoTruncate := true
		if bifrostReq.Params.AutoTruncate != nil {
			autoTruncate = *bifrostReq.Params.AutoTruncate
		}
		vertexReq.Parameters = &VertexEmbeddingParameters{
			AutoTruncate:         &autoTruncate,
			OutputDimensionality: bifrostReq.Params.Dimensions,
		}
	}

	return vertexReq, nil
}

// ToVertexGeminiEmbeddingRequest converts a Bifrost embedding request to the Vertex :embedContent format.
// Callers route several inputs to ToVertexGeminiBatchEmbeddingRequest.
func ToVertexGeminiEmbeddingRequest(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostEmbeddingRequest) (*VertexGeminiEmbeddingRequest, error) {
	if bifrostReq == nil || len(bifrostReq.Input) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("embedding input is not provided")
	}

	if err := schemas.EmbeddingInput(bifrostReq.Input).RejectPerItemParams(); err != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s", err)
	}

	req, err := toVertexGeminiEmbeddingEntry(bifrostReq.Input[0].Content, bifrostReq.Params, isPassthroughExtraParams(ctx))
	if err != nil {
		return nil, err
	}
	if bifrostReq.Params != nil {
		req.ExtraParams = bifrostReq.Params.ExtraParams
	}
	return req, nil
}

// ToVertexGeminiBatchEmbeddingRequest converts a multi-input Bifrost embedding request to the Vertex :batchEmbedContents format.
func ToVertexGeminiBatchEmbeddingRequest(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostEmbeddingRequest) (*VertexGeminiBatchEmbeddingRequest, error) {
	if bifrostReq == nil || len(bifrostReq.Input) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("embedding input is not provided")
	}

	if err := schemas.EmbeddingInput(bifrostReq.Input).RejectPerItemParams(); err != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s", err)
	}

	batch := &VertexGeminiBatchEmbeddingRequest{Requests: make([]VertexGeminiEmbeddingRequest, 0, len(bifrostReq.Input))}
	if bifrostReq.Params != nil && bifrostReq.Params.ExtraParams != nil {
		// These are applied per entry; Vertex rejects them at the batch top level.
		batch.ExtraParams = withoutKeys(bifrostReq.Params.ExtraParams, vertexBatchEntryExtraKeys...)
	}
	passthrough := isPassthroughExtraParams(ctx)
	for _, item := range bifrostReq.Input {
		entry, err := toVertexGeminiEmbeddingEntry(item.Content, bifrostReq.Params, passthrough)
		if err != nil {
			return nil, err
		}
		batch.Requests = append(batch.Requests, *entry)
	}
	return batch, nil
}

// vertexEmbeddingEntryExtraKeys are native extra params lifted onto each entry under passthrough.
var vertexEmbeddingEntryExtraKeys = []string{"taskType", "title", "outputDimensionality", "autoTruncate"}

// vertexBatchEntryExtraKeys are the extra params that belong on batch entries, never the batch top level.
var vertexBatchEntryExtraKeys = append([]string{"embedContentConfig"}, vertexEmbeddingEntryExtraKeys...)

// toVertexGeminiEmbeddingEntry builds one embedContent body (also a batchEmbedContents entry) without extra params.
// Under passthrough, vertexEmbeddingEntryExtraKeys fill the fields the typed params leave unset.
func toVertexGeminiEmbeddingEntry(content schemas.EmbeddingContent, params *schemas.EmbeddingParameters, passthrough bool) (*VertexGeminiEmbeddingRequest, error) {
	gemContent, err := gemini.EmbeddingContentToGeminiContent(content, geminiImageURLSchemes...)
	if err != nil {
		return nil, err
	}
	req := &VertexGeminiEmbeddingRequest{Content: gemContent}
	if params != nil {
		req.TaskType = params.TaskType
		req.Title = params.Title
		req.OutputDimensionality = params.Dimensions
		req.AutoTruncate = params.AutoTruncate
		if params.ExtraParams != nil {
			if passthrough {
				extra := params.ExtraParams
				if req.TaskType == nil {
					req.TaskType, _ = schemas.SafeExtractStringPointer(extra["taskType"])
				}
				if req.Title == nil {
					req.Title, _ = schemas.SafeExtractStringPointer(extra["title"])
				}
				if req.OutputDimensionality == nil {
					req.OutputDimensionality, _ = schemas.SafeExtractIntPointer(extra["outputDimensionality"])
				}
				if req.AutoTruncate == nil {
					req.AutoTruncate, _ = schemas.SafeExtractBoolPointer(extra["autoTruncate"])
				}
			}
			req.EmbedContentConfig = gemini.ReadEmbedContentConfig(params.ExtraParams)
		}
	}
	return req, nil
}

// withoutKeys returns m minus keys, cloning only when one is present so the caller's map is never mutated.
func withoutKeys(m map[string]interface{}, keys ...string) map[string]interface{} {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			m = maps.Clone(m)
			for _, k := range keys {
				delete(m, k)
			}
			return m
		}
	}
	return m
}

// isPassthroughExtraParams reports whether the caller asked for extra params to be forwarded.
func isPassthroughExtraParams(ctx *schemas.BifrostContext) bool {
	return ctx != nil && ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams) == true
}

// extractBase64FromDataURI strips the "data:<mime>;base64," prefix from a data URI,
// returning the raw base64 string that Vertex multimodal embedding expects.
func extractBase64FromDataURI(dataURI string) string {
	if !strings.HasPrefix(dataURI, "data:") {
		return dataURI // already raw base64 or a GCS URI
	}
	info := schemas.ExtractURLTypeInfo(dataURI)
	if info.DataURLWithoutPrefix != nil {
		return *info.DataURLWithoutPrefix
	}
	return dataURI
}

// ToVertexMultimodalEmbeddingRequest converts a Bifrost embedding request to the
// Vertex native multimodal embedding format (multimodalembedding@001).
// Exactly one content entry is required (the API supports only 1 instance per request).
// Parts within the content are merged into a single instance (text, image, video).
// Only text, image, and video are supported; audio and file parts will return an error.
func ToVertexMultimodalEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*VertexEmbeddingRequest, error) {
	if bifrostReq == nil || len(bifrostReq.Input) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("embedding input is not provided")
	}

	if len(bifrostReq.Input) > 1 {
		return nil, providerUtils.InvalidRequestErrorf("this model supports only 1 instance per request; got %d contents", len(bifrostReq.Input))
	}

	if err := schemas.EmbeddingInput(bifrostReq.Input).RejectPerItemParams(); err != nil {
		return nil, providerUtils.InvalidRequestErrorf("%s", err)
	}

	instance := VertexEmbeddingInstance{}
	var textBuilder strings.Builder
	for _, part := range bifrostReq.Input[0].Content {
		switch part.Type {
		case schemas.EmbeddingContentPartTypeText:
			if part.Text == nil {
				return nil, providerUtils.InvalidRequestErrorf("text part has no payload")
			}
			if textBuilder.Len() > 0 {
				textBuilder.WriteString("\n\n")
			}
			textBuilder.WriteString(*part.Text)
		case schemas.EmbeddingContentPartTypeImage:
			if part.Image == nil {
				return nil, providerUtils.InvalidRequestErrorf("image part has no payload")
			}
			if instance.Image != nil {
				return nil, providerUtils.InvalidRequestErrorf("this model supports at most one image per content entry")
			}
			img := &VertexMultimodalImageInput{}
			if part.Image.Data != nil {
				b64 := extractBase64FromDataURI(*part.Image.Data)
				img.BytesBase64Encoded = &b64
			} else if part.Image.URL != nil {
				if !strings.HasPrefix(*part.Image.URL, "gs://") {
					return nil, providerUtils.InvalidRequestErrorf("this model requires a GCS URI (gs://) for image URL input")
				}
				img.GCSUri = part.Image.URL
			} else {
				return nil, providerUtils.InvalidRequestErrorf("image part must set data or url")
			}
			instance.Image = img
		case schemas.EmbeddingContentPartTypeVideo:
			if part.Video == nil {
				return nil, providerUtils.InvalidRequestErrorf("video part has no payload")
			}
			if instance.Video != nil {
				return nil, providerUtils.InvalidRequestErrorf("this model supports at most one video per content entry")
			}
			vid := &VertexMultimodalVideoInput{}
			if part.Video.Data != nil {
				b64 := extractBase64FromDataURI(*part.Video.Data)
				vid.BytesBase64Encoded = &b64
			} else if part.Video.URL != nil {
				if !strings.HasPrefix(*part.Video.URL, "gs://") {
					return nil, providerUtils.InvalidRequestErrorf("this model requires a GCS URI (gs://) for video URL input")
				}
				vid.GCSUri = part.Video.URL
			} else {
				return nil, providerUtils.InvalidRequestErrorf("video part must set data or url")
			}
			if part.VideoConfig != nil {
				vid.VideoSegmentConfig = &VertexVideoSegmentConfig{
					StartOffsetSec: part.VideoConfig.StartOffsetSec,
					EndOffsetSec:   part.VideoConfig.EndOffsetSec,
					IntervalSec:    part.VideoConfig.IntervalSec,
				}
			}
			instance.Video = vid
		default:
			return nil, providerUtils.InvalidRequestErrorf("this model does not support %q parts", part.Type)
		}
	}
	if textBuilder.Len() > 0 {
		text := textBuilder.String()
		instance.Text = &text
	}

	req := &VertexEmbeddingRequest{Instances: []VertexEmbeddingInstance{instance}}
	if bifrostReq.Params != nil {
		req.Parameters = &VertexEmbeddingParameters{
			Dimension: bifrostReq.Params.Dimensions,
		}
		req.ExtraParams = bifrostReq.Params.ExtraParams
	}
	return req, nil
}

// ToBifrostEmbeddingResponse converts a Vertex AI embedding response to Bifrost format.
// Handles both text embedding responses (Embeddings.Values) and native multimodal
// responses (TextEmbedding / ImageEmbedding / VideoEmbeddings).
func (response *VertexEmbeddingResponse) ToBifrostEmbeddingResponse() *schemas.BifrostEmbeddingResponse {
	if response == nil || len(response.Predictions) == 0 {
		return nil
	}

	embeddings := make([]schemas.EmbeddingData, 0, len(response.Predictions))
	var usage *schemas.BifrostLLMUsage
	idx := 0

	for _, prediction := range response.Predictions {
		// Text embedding model response
		if prediction.Embeddings != nil && len(prediction.Embeddings.Values) > 0 {
			embeddings = append(embeddings, schemas.EmbeddingData{
				Object:    "embedding",
				Embedding: schemas.EmbeddingStruct{EmbeddingArray: append([]float64(nil), prediction.Embeddings.Values...)},
				Index:     idx,
			})
			idx++
			if prediction.Embeddings.Statistics != nil {
				if usage == nil {
					usage = &schemas.BifrostLLMUsage{}
				}
				usage.TotalTokens += prediction.Embeddings.Statistics.TokenCount
				usage.PromptTokens += prediction.Embeddings.Statistics.TokenCount
			}
			continue
		}

		// Native multimodal model response — textEmbedding, imageEmbedding, videoEmbeddings
		// are all in the same embedding space so each is returned as a separate EmbeddingData.
		if len(prediction.TextEmbedding) > 0 {
			embeddings = append(embeddings, schemas.EmbeddingData{
				Object:    "embedding",
				Modality:  schemas.EmbeddingModalityText,
				Embedding: schemas.EmbeddingStruct{EmbeddingArray: append([]float64(nil), prediction.TextEmbedding...)},
				Index:     idx,
			})
			idx++
		}
		if len(prediction.ImageEmbedding) > 0 {
			embeddings = append(embeddings, schemas.EmbeddingData{
				Object:    "embedding",
				Modality:  schemas.EmbeddingModalityImage,
				Embedding: schemas.EmbeddingStruct{EmbeddingArray: append([]float64(nil), prediction.ImageEmbedding...)},
				Index:     idx,
			})
			idx++
		}
		for _, ve := range prediction.VideoEmbeddings {
			embeddings = append(embeddings, schemas.EmbeddingData{
				Object:   "embedding",
				Modality: schemas.EmbeddingModalityVideo,
				VideoSegment: &schemas.EmbeddingVideoSegment{
					StartOffsetSec: ve.StartOffsetSec,
					EndOffsetSec:   ve.EndOffsetSec,
				},
				Embedding: schemas.EmbeddingStruct{EmbeddingArray: append([]float64(nil), ve.Embedding...)},
				Index:     idx,
			})
			idx++
		}
	}

	return &schemas.BifrostEmbeddingResponse{
		Object:      "list",
		Data:        embeddings,
		Usage:       usage,
		ExtraFields: schemas.BifrostResponseExtraFields{},
	}
}
