package openrouter

// OpenRouterModel represents a single model in the OpenRouter Models API response
type OpenRouterModel struct {
	ID string `json:"id"`

	// Permanent slug for the model that never changes
	CanonicalSlug string `json:"canonical_slug"`

	// Human-readable display name for the model
	Name string `json:"name"`

	// Unix timestamp of when the model was added to OpenRouter
	Created int `json:"created"`

	// Detailed description of the model's capabilities and characteristics
	Description string `json:"description"`

	// Maximum context window size in tokens
	ContextLength int `json:"context_length"`

	// Object describing the model's technical capabilities
	Architecture Architecture `json:"architecture"`

	// Lowest price structure for using this model
	Pricing Pricing `json:"pricing"`

	// Configuration details for the primary provider
	TopProvider TopProvider `json:"top_provider"`

	// Rate limiting information (null if no limits)
	PerRequestLimits *map[string]interface{} `json:"per_request_limits,omitempty"`

	// Array of supported API parameters for this model
	SupportedParameters []string `json:"supported_parameters"`
}

// Architecture describes the model's technical capabilities
type Architecture struct {
	// Supported input types (e.g., ["file", "image", "text"])
	InputModalities []string `json:"input_modalities"`

	// Supported output types (e.g., ["text"])
	OutputModalities []string `json:"output_modalities"`

	// Tokenization method used
	Tokenizer string `json:"tokenizer"`

	// Instruction format type (null if not applicable)
	InstructType *string `json:"instruct_type,omitempty"`
}

// Pricing structure with all values in USD per token/request/unit
// A value of "0" indicates the feature is free
type Pricing struct {
	// Cost per input token
	Prompt string `json:"prompt"`

	// Cost per output token
	Completion string `json:"completion"`

	// Fixed cost per API request
	Request string `json:"request"`

	// Cost per image input
	Image string `json:"image"`

	// Cost per web search operation
	WebSearch string `json:"web_search"`

	// Cost for internal reasoning tokens
	InternalReasoning string `json:"internal_reasoning"`

	// Cost per cached input token read
	InputCacheRead string `json:"input_cache_read"`

	// Cost per cached input token write
	InputCacheWrite string `json:"input_cache_write"`
}

// TopProvider contains configuration details for the primary provider
type TopProvider struct {
	// Provider-specific context limit
	ContextLength int `json:"context_length"`

	// Maximum tokens in response
	MaxCompletionTokens int `json:"max_completion_tokens"`

	// Whether content moderation is applied
	IsModerated bool `json:"is_moderated"`
}

// OpenRouterModelListResponse is the root response object from the Models API
type OpenRouterModelListResponse struct {
	Data []OpenRouterModel `json:"data"`
}
