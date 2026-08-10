package vertex

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"
)

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
	normalized := strings.ToLower(gemini.NormalizeModelName(model))
	// GenAI / some clients pass "models/{id}" — strip before matching.
	normalized = strings.TrimPrefix(normalized, "models/")
	return strings.HasPrefix(normalized, "gemini-embedding")
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

// ToVertexGeminiBatchEmbeddingRequest converts a Bifrost embedding request to
// the Gemini :batchEmbedContents body used on Vertex for gemini-embedding-* models.
//
// On Vertex the model field inside each request must be a fully-qualified
// publisher model resource name (projects/.../locations/.../publishers/google/models/...).
// Google AI Studio accepts the short "models/{id}" form; Vertex rejects it.
func ToVertexGeminiBatchEmbeddingRequest(
	bifrostReq *schemas.BifrostEmbeddingRequest,
	projectID string,
	region string,
) *gemini.GeminiBatchEmbeddingRequest {
	batch := gemini.ToGeminiEmbeddingRequest(bifrostReq)
	if batch == nil {
		return nil
	}
	raw := gemini.NormalizeModelName(bifrostReq.Model)
	if len(raw) >= len("models/") && strings.EqualFold(raw[:len("models/")], "models/") {
		raw = raw[len("models/"):]
	}
	modelID := raw
	fqModel := "projects/" + projectID + "/locations/" + region + "/publishers/google/models/" + modelID
	for i := range batch.Requests {
		batch.Requests[i].Model = fqModel
	}
	return batch
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
