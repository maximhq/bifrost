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
