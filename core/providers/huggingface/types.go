package huggingface

type HuggingFaceError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// refered from https://huggingface.co/api/models
type HuggingFaceModel struct {
	ID            string   `json:"_id"`
	ModelID       string   `json:"modelId"`
	Likes         int      `json:"likes"`
	TrendingScore int      `json:"trendingScore"`
	Private       bool     `json:"private"`
	Downloads     int      `json:"downloads"`
	Tags          []string `json:"tags"`
	PipelineTag   string   `json:"pipeline_tag"`
	LibraryName   string   `json:"library_name"`
	CreatedAt     string   `json:"createdAt"`
}

type huggingFaceErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

type HuggingFaceListModelsResponse struct {
	Models        []HuggingFaceModel `json:"models"`
	NextPageToken string             `json:"nextPageToken"`
}

// huggingFaceEmbeddingRequest represents the request format for HuggingFace feature extraction API
type huggingFaceEmbeddingRequest struct {
	Inputs         interface{} `json:"inputs"` // Can be string or []string
	Normalize      *bool       `json:"normalize,omitempty"`
	PromptName     *string     `json:"prompt_name,omitempty"`
	Truncate       *bool       `json:"truncate,omitempty"`
	TruncDirection *string     `json:"truncation_direction,omitempty"`
}

// huggingFaceEmbeddingResponse represents the response from HuggingFace feature extraction API
// The response is a 2D array of floats ([][]float32)
type huggingFaceEmbeddingResponse [][]float32
