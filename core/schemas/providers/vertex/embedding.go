package vertex

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToVertexEmbeddingRequest converts a Bifrost embedding request to Vertex AI format
func ToVertexEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) *VertexEmbeddingRequest {
	if bifrostReq == nil || bifrostReq.Input == nil || (bifrostReq.Input.Text == nil && bifrostReq.Input.Texts == nil) {
		return nil
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
				instance.TaskType = taskTypeStr
			}
			if title, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["title"]); ok {
				instance.Title = title
			}
		}

		instances = append(instances, instance)
	}

	// Create the request
	vertexReq := &VertexEmbeddingRequest{
		Instances: instances,
	}

	// Add parameters if present
	if bifrostReq.Params != nil {
		parameters := &VertexEmbeddingParameters{}

		// Set autoTruncate (defaults to true)
		autoTruncate := true
		if bifrostReq.Params.ExtraParams != nil {
			if autoTruncateVal, ok := schemas.SafeExtractBool(bifrostReq.Params.ExtraParams["autoTruncate"]); ok {
				autoTruncate = autoTruncateVal
			}
		}
		parameters.AutoTruncate = &autoTruncate

		// Add outputDimensionality if specified
		if bifrostReq.Params.Dimensions != nil {
			parameters.OutputDimensionality = bifrostReq.Params.Dimensions
		}

		vertexReq.Parameters = parameters
	}

	return vertexReq
}

// ToBifrostResponse converts a Vertex AI embedding response to Bifrost format
func (vertexResp *VertexEmbeddingResponse) ToBifrostResponse() *schemas.BifrostResponse {
	if vertexResp == nil || len(vertexResp.Predictions) == 0 {
		return nil
	}

	// Convert predictions to Bifrost embeddings
	embeddings := make([]schemas.BifrostEmbedding, 0, len(vertexResp.Predictions))
	var usage *schemas.LLMUsage

	for i, prediction := range vertexResp.Predictions {
		if prediction.Embeddings == nil || len(prediction.Embeddings.Values) == 0 {
			continue
		}

		// Convert float64 values to float32 for Bifrost format
		embeddingFloat32 := make([]float32, 0, len(prediction.Embeddings.Values))
		for _, v := range prediction.Embeddings.Values {
			embeddingFloat32 = append(embeddingFloat32, float32(v))
		}

		// Create embedding object
		embedding := schemas.BifrostEmbedding{
			Object: "embedding",
			Embedding: schemas.BifrostEmbeddingResponse{
				EmbeddingArray: embeddingFloat32,
			},
			Index: i,
		}

		// Extract statistics if available
		if prediction.Embeddings.Statistics != nil {
			if usage == nil {
				usage = &schemas.LLMUsage{}
			}
			usage.TotalTokens += prediction.Embeddings.Statistics.TokenCount
			usage.PromptTokens += prediction.Embeddings.Statistics.TokenCount
		}

		embeddings = append(embeddings, embedding)
	}

	// Create final response
	response := &schemas.BifrostResponse{
		Object: "list",
		Data:   embeddings,
		Usage:  usage,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.EmbeddingRequest,
			Provider:    schemas.Vertex,
		},
	}

	return response
}
