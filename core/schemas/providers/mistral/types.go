package mistral

type MistralEmbeddingRequest struct {
	Model           string   `json:"model"`
	Input           []string `json:"input"`
	OutputDtype     *string  `json:"output_dtype,omitempty"`
	OutputDimension *int     `json:"output_dimension,omitempty"`
	User            *string  `json:"user,omitempty"`
}
