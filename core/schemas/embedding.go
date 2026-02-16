package schemas

import (
	"fmt"
	"sync"
)

type BifrostEmbeddingRequest struct {
	Provider       ModelProvider        `json:"provider"`
	Model          string               `json:"model"`
	Input          *EmbeddingInput      `json:"input,omitempty"`
	Params         *EmbeddingParameters `json:"params,omitempty"`
	Fallbacks      []Fallback           `json:"fallbacks,omitempty"`
	RawRequestBody []byte               `json:"-"` // set bifrost-use-raw-request-body to true in ctx to use the raw request body. Bifrost will directly send this to the downstream provider.
}

func (r *BifrostEmbeddingRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

type BifrostEmbeddingResponse struct {
	Data        []EmbeddingData            `json:"data"` // Maps to "data" field in provider responses (e.g., OpenAI embedding format)
	Model       string                     `json:"model"`
	Object      string                     `json:"object"` // "list"
	Usage       *BifrostLLMUsage           `json:"usage"`
	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// bifrostEmbeddingResponsePool provides a pool for BifrostEmbeddingResponse objects.
var bifrostEmbeddingResponsePool = sync.Pool{
	New: func() interface{} {
		return &BifrostEmbeddingResponse{}
	},
}

// AcquireBifrostEmbeddingResponse gets a BifrostEmbeddingResponse from the pool and resets it.
func AcquireBifrostEmbeddingResponse() *BifrostEmbeddingResponse {
	r := bifrostEmbeddingResponsePool.Get().(*BifrostEmbeddingResponse)
	*r = BifrostEmbeddingResponse{}
	return r
}

// ReleaseBifrostEmbeddingResponse returns a BifrostEmbeddingResponse to the pool after clearing all fields.
// The caller must ensure no other goroutine holds a reference to this response.
// Do NOT use the response after calling Release().
func ReleaseBifrostEmbeddingResponse(r *BifrostEmbeddingResponse) {
	if r == nil {
		return
	}
	r.Data = nil
	r.Model = ""
	r.Object = ""
	r.Usage = nil
	r.ExtraFields = BifrostResponseExtraFields{}
	bifrostEmbeddingResponsePool.Put(r)
}

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
		return Marshal(*e.Text)
	}
	if e.Texts != nil {
		return Marshal(e.Texts)
	}
	if e.Embedding != nil {
		return Marshal(e.Embedding)
	}
	if e.Embeddings != nil {
		return Marshal(e.Embeddings)
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
	if err := Unmarshal(data, &s); err == nil {
		e.Text = &s
		return nil
	}
	// Try []string
	var ss []string
	if err := Unmarshal(data, &ss); err == nil {
		e.Texts = ss
		return nil
	}
	// Try []int
	var i []int
	if err := Unmarshal(data, &i); err == nil {
		e.Embedding = i
		return nil
	}
	// Try [][]int
	var i2 [][]int
	if err := Unmarshal(data, &i2); err == nil {
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

type EmbeddingData struct {
	Index     int             `json:"index"`
	Object    string          `json:"object"`    // "embedding"
	Embedding EmbeddingStruct `json:"embedding"` // can be string, []float32 or [][]float32
}

type EmbeddingStruct struct {
	EmbeddingStr     *string
	EmbeddingArray   []float32
	Embedding2DArray [][]float32
}

func (be EmbeddingStruct) MarshalJSON() ([]byte, error) {
	if be.EmbeddingStr != nil {
		return Marshal(be.EmbeddingStr)
	}
	if be.EmbeddingArray != nil {
		return Marshal(be.EmbeddingArray)
	}
	if be.Embedding2DArray != nil {
		return Marshal(be.Embedding2DArray)
	}
	return nil, fmt.Errorf("no embedding found")
}

func (be *EmbeddingStruct) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := Unmarshal(data, &stringContent); err == nil {
		be.EmbeddingStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of float32
	var arrayContent []float32
	if err := Unmarshal(data, &arrayContent); err == nil {
		be.EmbeddingArray = arrayContent
		return nil
	}

	// Try to unmarshal as a direct 2D array of float32
	var arrayContent2D [][]float32
	if err := Unmarshal(data, &arrayContent2D); err == nil {
		be.Embedding2DArray = arrayContent2D
		return nil
	}

	return fmt.Errorf("embedding field is neither a string nor an array of float32 nor a 2D array of float32")
}
