package vertex

// Vertex AI Embedding API types

// VertexEmbeddingInstance represents a single embedding instance in the request
type VertexEmbeddingInstance struct {
	Content  string                   `json:"content"`             // The text to generate embeddings for
	TaskType *string `json:"task_type,omitempty"` // Intended downstream application (optional)
	Title    *string                  `json:"title,omitempty"`     // Used to help the model produce better embeddings (optional)
}

// VertexEmbeddingParameters represents the parameters for the embedding request
type VertexEmbeddingParameters struct {
	AutoTruncate         *bool `json:"autoTruncate,omitempty"`         // When true, input text will be truncated (defaults to true)
	OutputDimensionality *int  `json:"outputDimensionality,omitempty"` // Output embedding size (optional)
}

// VertexEmbeddingRequest represents the complete embedding request to Vertex AI
type VertexEmbeddingRequest struct {
	Instances  []VertexEmbeddingInstance  `json:"instances"`            // List of embedding instances
	Parameters *VertexEmbeddingParameters `json:"parameters,omitempty"` // Optional parameters
}

// VertexEmbeddingStatistics represents statistics computed from the input text
type VertexEmbeddingStatistics struct {
	Truncated  bool `json:"truncated"`   // Whether the input text was truncated
	TokenCount int  `json:"token_count"` // Number of tokens in the input text
}

// VertexEmbeddingValues represents the embedding result
type VertexEmbeddingValues struct {
	Values     []float64                  `json:"values"`     // The embedding vector (list of floats)
	Statistics *VertexEmbeddingStatistics `json:"statistics"` // Statistics about the input text
}

// VertexEmbeddingPrediction represents a single prediction in the response
type VertexEmbeddingPrediction struct {
	Embeddings *VertexEmbeddingValues `json:"embeddings"` // The embedding result
}

// VertexEmbeddingResponse represents the complete embedding response from Vertex AI
type VertexEmbeddingResponse struct {
	Predictions []VertexEmbeddingPrediction `json:"predictions"` // List of embedding predictions
}
