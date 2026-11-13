package voyage

import "encoding/json"

// ==================== TEXT EMBEDDINGS (/v1/embeddings) ====================

// VoyageEmbeddingRequest is the body of POST /v1/embeddings.
type VoyageEmbeddingRequest struct {
	Model           string                 `json:"model"`
	Input           []string               `json:"input"`
	InputType       *string                `json:"input_type,omitempty"`       // "query" or "document"; omitted when the caller did not ask
	Truncation      *bool                  `json:"truncation,omitempty"`       // API default is true
	OutputDimension *int                   `json:"output_dimension,omitempty"` // only sent when the caller asked
	OutputDtype     *string                `json:"output_dtype,omitempty"`     // float, int8, uint8, binary, ubinary
	EncodingFormat  *string                `json:"encoding_format,omitempty"`  // base64
	ExtraParams     map[string]interface{} `json:"-"`
}

// GetExtraParams returns the params merged into the body when passthrough is on.
func (r *VoyageEmbeddingRequest) GetExtraParams() map[string]interface{} { return r.ExtraParams }

// VoyageEmbeddingResponse is the body of a successful /v1/embeddings reply.
type VoyageEmbeddingResponse struct {
	Object string                `json:"object"`
	Data   []VoyageEmbeddingItem `json:"data"`
	Model  string                `json:"model"`
	Usage  *VoyageUsage          `json:"usage"`
}

// VoyageEmbeddingItem is one vector. Embedding stays raw until the requested dtype is known:
// the same field is a float array, an integer array or a base64 string.
type VoyageEmbeddingItem struct {
	Object    string          `json:"object"`
	Embedding json.RawMessage `json:"embedding"`
	Index     int             `json:"index"`
}

// VoyageUsage is the usage block of the embedding and rerank endpoints.
type VoyageUsage struct {
	TotalTokens *int `json:"total_tokens"`
}

// ==================== CONTEXTUALIZED EMBEDDINGS (/v1/contextualizedembeddings) ====================

// VoyageContextualizedRequest is the body of POST /v1/contextualizedembeddings. Inputs is a flat
// list of document strings when auto-chunking is on, and a list of single-query lists otherwise.
type VoyageContextualizedRequest struct {
	Model              string                 `json:"model"`
	Inputs             any                    `json:"inputs"`
	InputType          string                 `json:"input_type"`
	EnableAutoChunking *bool                  `json:"enable_auto_chunking,omitempty"`
	ChunkSize          *int                   `json:"chunk_size,omitempty"`
	OutputDimension    *int                   `json:"output_dimension,omitempty"`
	ExtraParams        map[string]interface{} `json:"-"`
}

// GetExtraParams returns the params merged into the body when passthrough is on.
func (r *VoyageContextualizedRequest) GetExtraParams() map[string]interface{} { return r.ExtraParams }

// VoyageContextualizedResponse is the body of a successful /v1/contextualizedembeddings reply.
type VoyageContextualizedResponse struct {
	Object string                      `json:"object"`
	Data   []VoyageContextualizedGroup `json:"data"`
	Model  string                      `json:"model"`
	Usage  *VoyageUsage                `json:"usage"`
}

// VoyageContextualizedGroup holds the vectors of one input: one per chunk of a document, or the
// single vector of a query.
type VoyageContextualizedGroup struct {
	Object string                     `json:"object"`
	Index  int                        `json:"index"`
	Data   []VoyageContextualizedItem `json:"data"`
}

// VoyageContextualizedItem is one chunk vector. Text is present only when the API chunked the
// document itself.
type VoyageContextualizedItem struct {
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
	Text      *string   `json:"text,omitempty"`
}

// ==================== MULTIMODAL EMBEDDINGS (/v1/multimodalembeddings) ====================

// VoyageMultimodalRequest is the body of POST /v1/multimodalembeddings.
type VoyageMultimodalRequest struct {
	Model           string                  `json:"model"`
	Inputs          []VoyageMultimodalInput `json:"inputs"`
	InputType       *string                 `json:"input_type,omitempty"`
	Truncation      *bool                   `json:"truncation,omitempty"`
	OutputEncoding  *string                 `json:"output_encoding,omitempty"` // base64
	OutputDimension *int                    `json:"output_dimension,omitempty"`
	ExtraParams     map[string]interface{}  `json:"-"`
}

// GetExtraParams returns the params merged into the body when passthrough is on.
func (r *VoyageMultimodalRequest) GetExtraParams() map[string]interface{} { return r.ExtraParams }

// VoyageMultimodalInput is one input: an ordered mix of text, image and video parts.
type VoyageMultimodalInput struct {
	Content []VoyageMultimodalPart `json:"content"`
}

// VoyageMultimodalPart is one content part. Type names the single payload field that is set.
type VoyageMultimodalPart struct {
	Type        string  `json:"type"` // text, image_url, image_base64, video_url, video_base64
	Text        *string `json:"text,omitempty"`
	ImageURL    *string `json:"image_url,omitempty"`
	ImageBase64 *string `json:"image_base64,omitempty"`
	VideoURL    *string `json:"video_url,omitempty"`
	VideoBase64 *string `json:"video_base64,omitempty"`
}

// VoyageMultimodalResponse is the body of a successful /v1/multimodalembeddings reply.
type VoyageMultimodalResponse struct {
	Object string                 `json:"object"`
	Data   []VoyageEmbeddingItem  `json:"data"`
	Model  string                 `json:"model"`
	Usage  *VoyageMultimodalUsage `json:"usage"`
}

// VoyageMultimodalUsage is the usage block of the multimodal endpoint. Text is billed per token
// and images and videos per pixel; TotalTokens folds both into tokens (560 image pixels or 1120
// video pixels per token).
type VoyageMultimodalUsage struct {
	TextTokens  *int `json:"text_tokens"`
	ImagePixels *int `json:"image_pixels"`
	VideoPixels *int `json:"video_pixels"`
	TotalTokens *int `json:"total_tokens"`
}

// ==================== RERANK (/v1/rerank) ====================

// VoyageRerankRequest is the body of POST /v1/rerank.
type VoyageRerankRequest struct {
	Model           string                 `json:"model"`
	Query           string                 `json:"query"`
	Documents       []string               `json:"documents"`
	TopK            *int                   `json:"top_k,omitempty"`
	ReturnDocuments *bool                  `json:"return_documents,omitempty"`
	Truncation      *bool                  `json:"truncation,omitempty"`
	ExtraParams     map[string]interface{} `json:"-"`
}

// GetExtraParams returns the params merged into the body when passthrough is on.
func (r *VoyageRerankRequest) GetExtraParams() map[string]interface{} { return r.ExtraParams }

// VoyageRerankResponse is the body of a successful /v1/rerank reply.
type VoyageRerankResponse struct {
	Object string              `json:"object"`
	Data   []VoyageRerankEntry `json:"data"`
	Model  string              `json:"model"`
	Usage  *VoyageUsage        `json:"usage"`
}

// VoyageRerankEntry is one scored document: Index is its position in the request.
type VoyageRerankEntry struct {
	Index          int      `json:"index"`
	RelevanceScore *float64 `json:"relevance_score"`
	Document       *string  `json:"document,omitempty"`
}

// ==================== ERRORS ====================

// VoyageError is the body Voyage returns on a failed request: {"detail": "<text>"}.
type VoyageError struct {
	Detail json.RawMessage `json:"detail"`
}
