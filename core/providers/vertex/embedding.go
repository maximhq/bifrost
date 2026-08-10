package vertex

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"
)

// canonicalVertexModelID normalizes a client-supplied model string for Vertex
// publisher model paths and resource names.
//
// Handles:
//   - whitespace trim + gemini.NormalizeModelName (strips google/ when applicable)
//   - optional "models/" prefix used by GenAI clients / Gemini wire format
//
// Does not lower-case the id — Vertex model resource names are case-sensitive
// as published (e.g. gemini-embedding-2). Callers that only need family matching
// should still compare with equal-fold / ToLower themselves.
func canonicalVertexModelID(model string) string {
	id := gemini.NormalizeModelName(model)
	if len(id) >= len("models/") && strings.EqualFold(id[:len("models/")], "models/") {
		id = id[len("models/"):]
	}
	return id
}

// usesGeminiEmbedContentAPI reports whether the model should be called via the
// Gemini Generative Language embedding surface on Vertex
// (:batchEmbedContents / :embedContent) rather than the legacy Vertex
// prediction embedding API (:predict + instances).
//
// Legacy :predict models include text-embedding-004, text-embedding-005,
// text-multilingual-embedding-002, multimodalembedding@001, etc.
// Gemini-native embedding models are named gemini-embedding-* (e.g.
// gemini-embedding-001, gemini-embedding-2). Calling those with :predict and an
// instances body yields 400 "Precondition check failed" from Vertex.
// See https://github.com/maximhq/bifrost/issues/5003.
func usesGeminiEmbedContentAPI(model string) bool {
	return strings.HasPrefix(strings.ToLower(canonicalVertexModelID(model)), "gemini-embedding")
}

// ToVertexEmbeddingRequest converts a Bifrost embedding request to Vertex AI
// legacy :predict format (instances/parameters).
func ToVertexEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) *VertexEmbeddingRequest {
	if bifrostReq == nil || bifrostReq.Input == nil || (bifrostReq.Input.Text == nil && bifrostReq.Input.Texts == nil) {
		return nil
	}
	// Create the request
	vertexReq := &VertexEmbeddingRequest{}
	if bifrostReq.Params != nil {
		vertexReq.ExtraParams = bifrostReq.Params.ExtraParams
	}
	var texts []string
	if bifrostReq.Input.Text != nil {
		texts = []string{*bifrostReq.Input.Text}
	} else {
		texts = bifrostReq.Input.Texts
	}

	// Create instances for each text
	instances := make([]VertexEmbeddingInstance, 0, len(texts))
	for _, text := range texts {
		instance := VertexEmbeddingInstance{
			Content: text,
		}

		// Add optional task_type and title from params
		if bifrostReq.Params != nil {
			if taskTypeStr, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["task_type"]); ok {
				delete(vertexReq.ExtraParams, "task_type")
				instance.TaskType = taskTypeStr
			}
			if title, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["title"]); ok {
				delete(vertexReq.ExtraParams, "title")
				instance.Title = title
			}
		}

		instances = append(instances, instance)
	}
	vertexReq.Instances = instances
	// Add parameters if present
	if bifrostReq.Params != nil {
		parameters := &VertexEmbeddingParameters{}

		// Set autoTruncate (defaults to true)
		autoTruncate := true
		if bifrostReq.Params.ExtraParams != nil {
			if autoTruncateVal, ok := schemas.SafeExtractBool(bifrostReq.Params.ExtraParams["autoTruncate"]); ok {
				delete(vertexReq.ExtraParams, "autoTruncate")
				autoTruncate = autoTruncateVal
			}
		}
		parameters.AutoTruncate = &autoTruncate

		// Add outputDimensionality if specified
		if bifrostReq.Params.Dimensions != nil {
			delete(vertexReq.ExtraParams, "dimensions")
			parameters.OutputDimensionality = bifrostReq.Params.Dimensions
		}

		vertexReq.Parameters = parameters
	}

	return vertexReq
}

// vertexGeminiEmbedTexts extracts the ordered list of input texts from a Bifrost
// embedding request (single Text and/or Texts).
func vertexGeminiEmbedTexts(bifrostReq *schemas.BifrostEmbeddingRequest) []string {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}
	var texts []string
	if bifrostReq.Input.Text != nil && *bifrostReq.Input.Text != "" {
		texts = append(texts, *bifrostReq.Input.Text)
	}
	if len(bifrostReq.Input.Texts) > 0 {
		texts = append(texts, bifrostReq.Input.Texts...)
	}
	return texts
}

// ToVertexGeminiEmbedContentRequest builds a single Vertex :embedContent body.
//
// Live Vertex AI (global / multi-region eu|us) serves gemini-embedding-* on
// :embedContent with response shape {"embedding":{"values":[…]}}. The Google AI
// Studio :batchEmbedContents endpoint is not available on the Vertex publisher
// path (HTML 404) — using it and unmarshaling as GeminiEmbeddingResponse yields
// "failed to unmarshal response from provider API".
//
// The model is taken from the URL path only. Putting "model" in the JSON body
// triggers Vertex INVALID_ARGUMENT:
//   Invalid value (oneof), oneof field '_model' is already set. Cannot set 'model'
// (modelID/projectID/region are kept in the signature for call-site clarity and
// future use; they intentionally do not appear in the wire body).
func ToVertexGeminiEmbedContentRequest(
	bifrostReq *schemas.BifrostEmbeddingRequest,
	text string,
	projectID string,
	region string,
	modelID string,
) *gemini.GeminiEmbeddingRequest {
	if bifrostReq == nil || text == "" {
		return nil
	}
	_ = projectID
	_ = region
	_ = modelID
	embeddingReq := &gemini.GeminiEmbeddingRequest{
		// Model intentionally empty — omitempty; identity is the URL resource.
		Content: &gemini.Content{
			Parts: []*gemini.Part{
				{Text: text},
			},
		},
	}
	if bifrostReq.Params != nil {
		embeddingReq.ExtraParams = bifrostReq.Params.ExtraParams
		if bifrostReq.Params.Dimensions != nil {
			embeddingReq.OutputDimensionality = bifrostReq.Params.Dimensions
		}
		if bifrostReq.Params.ExtraParams != nil {
			if taskType, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["taskType"]); ok {
				embeddingReq.TaskType = taskType
			}
			if title, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["title"]); ok {
				embeddingReq.Title = title
			}
		}
	}
	return embeddingReq
}

// bifrostEmbeddingFromGeminiEmbedContent converts a Vertex/Google :embedContent
// response into Bifrost form for one input at the given index.
func bifrostEmbeddingFromGeminiEmbedContent(
	resp *gemini.GeminiEmbedContentResponse,
	model string,
	index int,
) *schemas.BifrostEmbeddingResponse {
	if resp == nil || len(resp.Embedding.Values) == 0 {
		return nil
	}
	out := &schemas.BifrostEmbeddingResponse{
		Object: "list",
		Model:  model,
		Data: []schemas.EmbeddingData{
			{
				Object: "embedding",
				Index:  index,
				Embedding: schemas.EmbeddingStruct{
					EmbeddingArray: append([]float64(nil), resp.Embedding.Values...),
				},
			},
		},
	}
	if resp.Embedding.Statistics != nil {
		tokens := int(resp.Embedding.Statistics.TokenCount)
		out.Usage = &schemas.BifrostLLMUsage{
			PromptTokens: tokens,
			TotalTokens:  tokens,
		}
	}
	return out
}

// mergeBifrostEmbeddingResponses concatenates Data slices (preserving order)
// and sums usage token counts. Used when multi-input embeddings are issued as
// sequential :embedContent calls on Vertex.
func mergeBifrostEmbeddingResponses(parts []*schemas.BifrostEmbeddingResponse, model string) *schemas.BifrostEmbeddingResponse {
	if len(parts) == 0 {
		return nil
	}
	out := &schemas.BifrostEmbeddingResponse{
		Object: "list",
		Model:  model,
		Data:   make([]schemas.EmbeddingData, 0, len(parts)),
	}
	var usage *schemas.BifrostLLMUsage
	for _, p := range parts {
		if p == nil {
			continue
		}
		out.Data = append(out.Data, p.Data...)
		if p.Usage != nil {
			if usage == nil {
				usage = &schemas.BifrostLLMUsage{}
			}
			usage.PromptTokens += p.Usage.PromptTokens
			usage.TotalTokens += p.Usage.TotalTokens
		}
	}
	out.Usage = usage
	if len(out.Data) == 0 {
		return nil
	}
	return out
}

// ToBifrostEmbeddingResponse converts a Vertex AI legacy :predict embedding
// response to Bifrost format.
func (response *VertexEmbeddingResponse) ToBifrostEmbeddingResponse() *schemas.BifrostEmbeddingResponse {
	if response == nil || len(response.Predictions) == 0 {
		return nil
	}

	// Convert predictions to Bifrost embeddings
	embeddings := make([]schemas.EmbeddingData, 0, len(response.Predictions))
	var usage *schemas.BifrostLLMUsage

	for i, prediction := range response.Predictions {
		if prediction.Embeddings == nil || len(prediction.Embeddings.Values) == 0 {
			continue
		}

		// Create embedding object
		embedding := schemas.EmbeddingData{
			Object: "embedding",
			Embedding: schemas.EmbeddingStruct{
				EmbeddingArray: append([]float64(nil), prediction.Embeddings.Values...),
			},
			Index: i,
		}

		// Extract statistics if available
		if prediction.Embeddings.Statistics != nil {
			if usage == nil {
				usage = &schemas.BifrostLLMUsage{}
			}
			usage.TotalTokens += prediction.Embeddings.Statistics.TokenCount
			usage.PromptTokens += prediction.Embeddings.Statistics.TokenCount
		}

		embeddings = append(embeddings, embedding)
	}

	return &schemas.BifrostEmbeddingResponse{
		Object: "list",
		Data:   embeddings,
		Usage:  usage,
		ExtraFields: schemas.BifrostResponseExtraFields{
		},
	}
}
