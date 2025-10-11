package schemas

import (
	"fmt"

	"github.com/bytedance/sonic"
)

// EmbeddingInput represents the input for an embedding request.
type EmbeddingInput struct {
	Text       *string
	Texts      []string
	Embedding  []int
	Embeddings [][]int
}

func (e *EmbeddingInput) MarshalJSON() ([]byte, error) {
	// enforce one-of
	set := 0
	if e.Text != nil {
		set++
	}
	if e.Texts != nil {
		set++
	}
	if e.Embedding != nil {
		set++
	}
	if e.Embeddings != nil {
		set++
	}
	if set == 0 {
		return nil, fmt.Errorf("embedding input is empty")
	}
	if set > 1 {
		return nil, fmt.Errorf("embedding input must set exactly one of: text, texts, embedding, embeddings")
	}

	if e.Text != nil {
		return sonic.Marshal(*e.Text)
	}
	if e.Texts != nil {
		return sonic.Marshal(e.Texts)
	}
	if e.Embedding != nil {
		return sonic.Marshal(e.Embedding)
	}
	if e.Embeddings != nil {
		return sonic.Marshal(e.Embeddings)
	}

	return nil, fmt.Errorf("invalid embedding input")
}

func (e *EmbeddingInput) UnmarshalJSON(data []byte) error {
	e.Text = nil
	e.Texts = nil
	e.Embedding = nil
	e.Embeddings = nil
	// Try string
	var s string
	if err := sonic.Unmarshal(data, &s); err == nil {
		e.Text = &s
		return nil
	}
	// Try []string
	var ss []string
	if err := sonic.Unmarshal(data, &ss); err == nil {
		e.Texts = ss
		return nil
	}
	// Try []int
	var i []int
	if err := sonic.Unmarshal(data, &i); err == nil {
		e.Embedding = i
		return nil
	}
	// Try [][]int
	var i2 [][]int
	if err := sonic.Unmarshal(data, &i2); err == nil {
		e.Embeddings = i2
		return nil
	}

	return fmt.Errorf("unsupported embedding input shape")
}

type EmbeddingParameters struct {
	EncodingFormat *string `json:"encoding_format,omitempty"` // Format for embedding output (e.g., "float", "base64")
	Dimensions     *int    `json:"dimensions,omitempty"`      // Number of dimensions for embedding output

	// Dynamic parameters that can be provider-specific, they are directly
	// added to the request as is.
	ExtraParams map[string]interface{} `json:"-"`
}

type BifrostEmbedding struct {
	Index     int                      `json:"index"`
	Object    string                   `json:"object"`    // embedding
	Embedding BifrostEmbeddingResponse `json:"embedding"` // can be []float32 or string
}

type BifrostEmbeddingResponse struct {
	EmbeddingStr     *string
	EmbeddingArray   []float32
	Embedding2DArray [][]float32
}

func (be BifrostEmbeddingResponse) MarshalJSON() ([]byte, error) {
	if be.EmbeddingStr != nil {
		return sonic.Marshal(be.EmbeddingStr)
	}
	if be.EmbeddingArray != nil {
		return sonic.Marshal(be.EmbeddingArray)
	}
	if be.Embedding2DArray != nil {
		return sonic.Marshal(be.Embedding2DArray)
	}
	return nil, fmt.Errorf("no embedding found")
}

func (be *BifrostEmbeddingResponse) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		be.EmbeddingStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of float32
	var arrayContent []float32
	if err := sonic.Unmarshal(data, &arrayContent); err == nil {
		be.EmbeddingArray = arrayContent
		return nil
	}

	// Try to unmarshal as a direct 2D array of float32
	var arrayContent2D [][]float32
	if err := sonic.Unmarshal(data, &arrayContent2D); err == nil {
		be.Embedding2DArray = arrayContent2D
		return nil
	}

	return fmt.Errorf("embedding field is neither a string nor an array of float32 nor a 2D array of float32")
}
