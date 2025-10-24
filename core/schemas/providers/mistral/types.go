package mistral

// MistralModel represents a single model in the Mistral Models API response
type MistralModel struct {
	ID                          string       `json:"id"`
	Object                      string       `json:"object"`
	Created                     int64        `json:"created"`
	OwnedBy                     string       `json:"owned_by"`
	Capabilities                Capabilities `json:"capabilities"`
	Name                        string       `json:"name"`
	Description                 string       `json:"description"`
	MaxContextLength            int          `json:"max_context_length"`
	Aliases                     []string     `json:"aliases"`
	Deprecation                 *string      `json:"deprecation,omitempty"`
	DeprecationReplacementModel *string      `json:"deprecation_replacement_model,omitempty"`
	DefaultModelTemperature     float64      `json:"default_model_temperature"`
	Type                        string       `json:"type"`
}

// Capabilities describes the model's supported features
type Capabilities struct {
	CompletionChat  bool `json:"completion_chat"`
	CompletionFim   bool `json:"completion_fim"`
	FunctionCalling bool `json:"function_calling"`
	FineTuning      bool `json:"fine_tuning"`
	Vision          bool `json:"vision"`
	Classification  bool `json:"classification"`
}

// MistralListModelsResponse is the root response object from the Mistral Models API
type MistralListModelsResponse struct {
	Object string         `json:"object"`
	Data   []MistralModel `json:"data"`
}
