package perplexity

import "github.com/maximhq/bifrost/core/schemas"

// PerplexityChatRequest represents a Perplexity chat completion request
type PerplexityChatRequest struct {
	Model                   string                   `json:"model"`                                // Required: Model to use for chat completion
	Messages                []schemas.ChatMessage    `json:"messages"`                             // Required: Array of message objects
	SearchMode              *string                  `json:"search_mode"`                          // Required: Search mode
	ReasoningEffort         *string                  `json:"reasoning_effort"`                     // Required: Reasoning effort (low, medium, high)
	MaxTokens               *int                     `json:"max_tokens,omitempty"`                 // Optional: Maximum tokens to generate
	Temperature             *float64                 `json:"temperature,omitempty"`                // Optional: Sampling temperature
	TopP                    *float64                 `json:"top_p,omitempty"`                      // Optional: Top-p sampling
	LanguagePreference      *string                  `json:"language_preference,omitempty"`        // Optional: Language preference
	SearchDomainFilter      []string                 `json:"search_domain_filter,omitempty"`       // Optional: Search domain filter
	ReturnImages            *bool                    `json:"return_images,omitempty"`              // Optional: Return images
	ReturnRelatedQuestions  *bool                    `json:"return_related_questions,omitempty"`   // Optional: Return related questions
	SearchRecencyFilter     *string                  `json:"search_recency_filter,omitempty"`      // Optional: Search recency filter
	SearchAfterDateFilter   *string                  `json:"search_after_date_filter,omitempty"`   // Optional: Search after date filter
	SearchBeforeDateFilter  *string                  `json:"search_before_date_filter,omitempty"`  // Optional: Search before date filter
	LastUpdatedAfterFilter  *string                  `json:"last_updated_after_filter,omitempty"`  // Optional: Last updated after filter
	LastUpdatedBeforeFilter *string                  `json:"last_updated_before_filter,omitempty"` // Optional: Last updated before filter
	TopK                    *int                     `json:"top_k,omitempty"`                      // Optional: Top-k sampling
	Stream                  *bool                    `json:"stream,omitempty"`                     // Optional: Enable streaming
	PresencePenalty         *float64                 `json:"presence_penalty,omitempty"`           // Optional: Presence penalty
	FrequencyPenalty        *float64                 `json:"frequency_penalty,omitempty"`          // Optional: Frequency penalty
	ResponseFormat          *interface{}             `json:"response_format,omitempty"`            // Format for the response
	DisableSearch           *bool                    `json:"disable_search,omitempty"`             // Optional: Disable search
	EnableSearchClassifier  *bool                    `json:"enable_search_classifier,omitempty"`   // Optional: Enable search classifier
	WebSearchOptions        []WebSearchOption        `json:"web_search_options,omitempty"`         // Optional: Web search options
	MediaResponse           *MediaResponse           `json:"media_response,omitempty"`             // Optional: Media response
	Tools                   []schemas.ChatTool       `json:"tools,omitempty"`                      // Optional: Tools available for the model
	ToolChoice              *schemas.ChatToolChoice  `json:"tool_choice,omitempty"`                // Optional: Whether to call a tool
	ParallelToolCalls       *bool                    `json:"parallel_tool_calls,omitempty"`        // Optional: Enable parallel tool calls
	Stop                    []string                 `json:"stop,omitempty"`                       // Optional: Stop sequences
	LogProbs                *bool                    `json:"logprobs,omitempty"`                   // Optional: Return log probabilities
	TopLogProbs             *int                     `json:"top_logprobs,omitempty"`               // Optional: Number of top log probabilities
	NumSearchResults        *int                     `json:"num_search_results,omitempty"`         // Optional: Number of search results
	NumImages               *int                     `json:"num_images,omitempty"`                 // Optional: Number of images
	SearchLanguageFilter    []string                 `json:"search_language_filter,omitempty"`     // Optional: Search language filter
	ImageFormatFilter       []string                 `json:"image_format_filter,omitempty"`        // Optional: Image format filter
	ImageDomainFilter       []string                 `json:"image_domain_filter,omitempty"`        // Optional: Image domain filter
	SafeSearch              *bool                    `json:"safe_search,omitempty"`                // Optional: Enable safe search
	StreamMode              *string                  `json:"stream_mode,omitempty"`                // Optional: Stream mode
	ExtraParams             map[string]interface{}   `json:"-"`
}

// GetExtraParams implements the RequestBodyWithExtraParams interface
func (r *PerplexityChatRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

type WebSearchOption struct {
	SearchContextSize             *string                      `json:"search_context_size,omitempty"`              // "low" | "medium" | "high"
	UserLocation                  *WebSearchOptionUserLocation `json:"user_location,omitempty"`                    // The approximate location of the user
	ImageResultsEnhancedRelevance *bool                        `json:"image_results_enhanced_relevance,omitempty"` // Optional: Image results enhanced relevance
	SearchType                    *string                      `json:"search_type,omitempty"`                      // Optional: "general" | "news" | "academic" | "social" | "writing"
}

type WebSearchOptionUserLocation struct {
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
	City      *string  `json:"city,omitempty"`    // Free text input for the city
	Country   *string  `json:"country,omitempty"` // Two-letter ISO country code
	Region    *string  `json:"region,omitempty"`  // Free text input for the region
}

type MediaResponse struct {
	Overrides MediaResponseOverrides `json:"overrides,omitempty"` // Optional: Overrides for the media response
}

type MediaResponseOverrides struct {
	ReturnVideos *bool `json:"return_videos,omitempty"` // Optional: Return videos
	ReturnImages *bool `json:"return_images,omitempty"` // Optional: Return images
}

type PerplexityChatResponse struct {
	ID            string                          `json:"id"`
	Choices       []schemas.BifrostResponseChoice `json:"choices"`
	Created       int                             `json:"created"` // The Unix timestamp (in seconds).
	Model         string                          `json:"model"`
	Object        string                          `json:"object"` // "chat.completion" or "chat.completion.chunk"
	Citations     []string                        `json:"citations,omitempty"`
	SearchResults []schemas.SearchResult          `json:"search_results,omitempty"`
	Videos        []schemas.VideoResult           `json:"videos,omitempty"`
	Usage         *Usage                          `json:"usage,omitempty"`
}

type Usage struct {
	PromptTokens      int                  `json:"prompt_tokens"`
	CompletionTokens  int                  `json:"completion_tokens"`
	TotalTokens       int                  `json:"total_tokens"`
	SearchContextSize *string              `json:"search_context_size,omitempty"`
	CitationTokens    *int                 `json:"citation_tokens,omitempty"`
	NumSearchQueries  *int                 `json:"num_search_queries,omitempty"`
	ReasoningTokens   *int                 `json:"reasoning_tokens,omitempty"`
	Cost              *schemas.BifrostCost `json:"cost,omitempty"`
}
