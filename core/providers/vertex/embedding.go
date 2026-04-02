package vertex

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/providers/gemini"
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
// Exactly one content entry is required; all parts must be text-only and are joined into a single
// instance string. Multiple contents should be sent as separate requests.
func ToVertexEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*VertexEmbeddingRequest, error) {
	if bifrostReq == nil || len(bifrostReq.Input) == 0 {
		return nil, fmt.Errorf("embedding input is not provided")
	}
	if len(bifrostReq.Input) > 1 {
		return nil, fmt.Errorf("vertex text embedding does not support batch inputs (multiple contents); use a single content entry")
	}

	var sb strings.Builder
	for _, part := range bifrostReq.Input[0] {
		if part.Type != schemas.EmbeddingContentPartTypeText || part.Text == nil {
			return nil, fmt.Errorf("vertex text embedding only supports text parts; got %q", part.Type)
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

	vertexReq := &VertexEmbeddingRequest{Instances: []VertexEmbeddingInstance{instance}}
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

// ToVertexGeminiEmbeddingRequest converts a Bifrost embedding request to Vertex Gemini embedding format.
// Only a single content entry is supported (len == 1); batch is not supported by this endpoint.
func ToVertexGeminiEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*VertexGeminiEmbeddingRequest, error) {
	if bifrostReq == nil || len(bifrostReq.Input) == 0 {
		return nil, fmt.Errorf("embedding input is not provided")
	}
	if len(bifrostReq.Input) > 1 {
		return nil, fmt.Errorf("vertex gemini embedding does not support batch inputs (multiple contents); use a single content entry")
	}

	content := bifrostReq.Input[0]
	params := bifrostReq.Params
	gemContent, err := gemini.EmbeddingContentToGeminiContent(content)
	if err != nil {
		return nil, err
	}
	req := &VertexGeminiEmbeddingRequest{
		Content: gemContent,
	}
	if params != nil {
		req.TaskType = params.TaskType
		req.Title = params.Title
		req.OutputDimensionality = params.Dimensions
		req.AutoTruncate = params.AutoTruncate

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
	return req, nil
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
		return nil, fmt.Errorf("embedding input is not provided")
	}
	if len(bifrostReq.Input) > 1 {
		return nil, fmt.Errorf("vertex multimodalembedding@001 supports only 1 instance per request; got %d contents", len(bifrostReq.Input))
	}

	instance := VertexEmbeddingInstance{}
	var textBuilder strings.Builder
	for _, part := range bifrostReq.Input[0] {
		switch part.Type {
		case schemas.EmbeddingContentPartTypeText:
			if part.Text == nil {
				return nil, fmt.Errorf("text part has no payload")
			}
			if textBuilder.Len() > 0 {
				textBuilder.WriteString("\n\n")
			}
			textBuilder.WriteString(*part.Text)
		case schemas.EmbeddingContentPartTypeImage:
			if part.Image == nil {
				return nil, fmt.Errorf("image part has no payload")
			}
			if instance.Image != nil {
				return nil, fmt.Errorf("vertex multimodalembedding@001 supports at most one image per content entry")
			}
			img := &VertexMultimodalImageInput{}
			if part.Image.Data != nil {
				b64 := extractBase64FromDataURI(*part.Image.Data)
				img.BytesBase64Encoded = &b64
			} else if part.Image.URL != nil {
				if !strings.HasPrefix(*part.Image.URL, "gs://") {
					return nil, fmt.Errorf("vertex multimodal embedding requires a GCS URI (gs://) for image URL input")
				}
				img.GCSUri = part.Image.URL
			} else {
				return nil, fmt.Errorf("image part must set data or url")
			}
			instance.Image = img
		case schemas.EmbeddingContentPartTypeVideo:
			if part.Video == nil {
				return nil, fmt.Errorf("video part has no payload")
			}
			if instance.Video != nil {
				return nil, fmt.Errorf("vertex multimodalembedding@001 supports at most one video per content entry")
			}
			vid := &VertexMultimodalVideoInput{}
			if part.Video.Data != nil {
				b64 := extractBase64FromDataURI(*part.Video.Data)
				vid.BytesBase64Encoded = &b64
			} else if part.Video.URL != nil {
				if !strings.HasPrefix(*part.Video.URL, "gs://") {
					return nil, fmt.Errorf("vertex multimodal embedding requires a GCS URI (gs://) for video URL input")
				}
				vid.GCSUri = part.Video.URL
			} else {
				return nil, fmt.Errorf("video part must set data or url")
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
			return nil, fmt.Errorf("vertex multimodalembedding@001 does not support %q parts", part.Type)
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
				Embedding: schemas.EmbeddingsByType{Float: append([]float64(nil), prediction.Embeddings.Values...)},
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
				Embedding: schemas.EmbeddingsByType{Float: append([]float64(nil), prediction.TextEmbedding...)},
				Index:     idx,
			})
			idx++
		}
		if len(prediction.ImageEmbedding) > 0 {
			embeddings = append(embeddings, schemas.EmbeddingData{
				Object:    "embedding",
				Modality:  schemas.EmbeddingModalityImage,
				Embedding: schemas.EmbeddingsByType{Float: append([]float64(nil), prediction.ImageEmbedding...)},
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
				Embedding: schemas.EmbeddingsByType{Float: append([]float64(nil), ve.Embedding...)},
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
