package replicate

import (
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ==================== REQUEST TYPES ====================

// ReplicatePredictionRequest represents a request to create a prediction
type ReplicatePredictionRequest struct {
	Version             *string                          `json:"version,omitempty"`                // Required: Model version ID
	Input               *ReplicatePredictionRequestInput `json:"input"`                            // Required: Input parameters for the model
	Stream              *bool                            `json:"stream,omitempty"`                 // Enable streaming output
	Webhook             *string                          `json:"webhook,omitempty"`                // Webhook URL for notifications
	WebhookEventsFilter []string                         `json:"webhook_events_filter,omitempty"`  // Filter webhook events: start, output, logs, completed
	OutputFileURLPrefix *string                          `json:"output_file_url_prefix,omitempty"` // Custom prefix for output file URLs
	PollTimeout         *int                             `json:"poll_timeout,omitempty"`           // Timeout in seconds for polling (used with Prefer: wait header)
	UseFileOutput       *bool                            `json:"use_file_output,omitempty"`        // Output files as URLs instead of data URIs
	ExtraParams         map[string]interface{}           `json:"-"`                                // Extra parameters to merge into the request
}

// GetExtraParams implements the RequestBodyWithExtraParams interface
func (req *ReplicatePredictionRequest) GetExtraParams() map[string]interface{} {
	return req.ExtraParams
}

// ReplicatePredictionRequestInput represents the input parameters for a model prediction
// This is flexible to support different model types - exact fields depend on the model
type ReplicatePredictionRequestInput struct {
	Prompt           *string               `json:"prompt,omitempty"`
	Messages         []schemas.ChatMessage `json:"messages,omitempty"`
	SystemPrompt     *string               `json:"system_prompt,omitempty"`
	Image            *string               `json:"image,omitempty"`               // URL or data URI
	NumberOfImages   *int                  `json:"number_of_images,omitempty"`    // Number of images to generate
	Quality          *string               `json:"quality,omitempty"`             // Quality of the image
	Background       *string               `json:"background,omitempty"`          // Background of the image
	Seed             *int                  `json:"seed,omitempty"`                // Random seed
	ReasoningEffort  *string               `json:"reasoning_effort,omitempty"`    // Reasoning effort
	NumInferenceStep *int                  `json:"num_inference_steps,omitempty"` // Number of inference steps
	NegativePrompt   *string               `json:"negative_prompt,omitempty"`     // Negative prompt

	// Responses parameters
	Instructions    *string                      `json:"instructions,omitempty"`
	InputItemList   []schemas.ResponsesMessage   `json:"input_item_list,omitempty"`
	Tools           []schemas.ResponsesTool      `json:"tools,omitempty"`
	MaxOutputTokens *int                         `json:"max_output_tokens,omitempty"`
	JsonSchema      *schemas.ResponsesTextConfig `json:"json_schema,omitempty"`

	// Chat parameters
	Temperature         *float64 `json:"temperature,omitempty"`           // Temperature for sampling
	TopP                *float64 `json:"top_p,omitempty"`                 // Top-p sampling
	TopK                *int     `json:"top_k,omitempty"`                 // Top-k sampling
	MaxTokens           *int     `json:"max_tokens,omitempty"`            // Maximum tokens to generate
	MaxCompletionTokens *int     `json:"max_completion_tokens,omitempty"` // Maximum completion tokens to generate
	PresencePenalty     *float64 `json:"presence_penalty,omitempty"`      // Presence penalty
	FrequencyPenalty    *float64 `json:"frequency_penalty,omitempty"`     // Frequency penalty

	// Image generation parameters
	AspectRatio  *string  `json:"aspect_ratio,omitempty"`
	OutputFormat *string  `json:"output_format,omitempty"`
	Resolution   *string  `json:"resolution,omitempty"`   // e.g., "1 MP"
	InputImages  []string `json:"input_images,omitempty"` // Image input for image-to-image models
	ImagePrompt  *string  `json:"image_prompt,omitempty"` // Image prompt for image models (flux family)
	ImageInput   []string `json:"image_input,omitempty"`  // Image input for chat models (openai family)
	InputImage   *string  `json:"input_image,omitempty"`  // Image input for image-to-image models

	// video generation parameters
	Duration       *int                   `json:"duration,omitempty"`
	InputReference *string                `json:"input_reference,omitempty"`
	ExtraParams    map[string]interface{} `json:"-"` // Additional model-specific parameters
}

// MarshalJSON implements custom JSON marshalling for ReplicatePredictionRequestInput.
// It marshals all defined fields and then flattens ExtraParams at the top level.
func (r *ReplicatePredictionRequestInput) MarshalJSON() ([]byte, error) {
	if r == nil {
		return []byte("null"), nil
	}

	// Create a temporary type to avoid infinite recursion
	type Alias ReplicatePredictionRequestInput

	// Marshal the struct normally (ExtraParams will be omitted due to json:"-" tag)
	aliasData, err := sonic.Marshal((*Alias)(r))
	if err != nil {
		return nil, err
	}

	// If there are no ExtraParams, return the marshaled data as-is
	if len(r.ExtraParams) == 0 {
		return aliasData, nil
	}

	// Unmarshal into a map to merge with ExtraParams
	var result map[string]interface{}
	if err := sonic.Unmarshal(aliasData, &result); err != nil {
		return nil, err
	}

	// Add all ExtraParams to the top level
	for key, value := range r.ExtraParams {
		result[key] = value
	}

	// Marshal the final result
	return sonic.Marshal(result)
}

// UnmarshalJSON implements custom JSON unmarshalling for ReplicatePredictionRequestInput.
// It unmarshals known fields and captures any unrecognized fields in ExtraParams.
func (r *ReplicatePredictionRequestInput) UnmarshalJSON(data []byte) error {
	// Create a temporary type to avoid infinite recursion
	type Alias ReplicatePredictionRequestInput

	// Unmarshal into the alias type
	aux := (*Alias)(r)
	if err := sonic.Unmarshal(data, aux); err != nil {
		return err
	}

	// Unmarshal into a map to find extra fields
	var rawMap map[string]interface{}
	if err := sonic.Unmarshal(data, &rawMap); err != nil {
		return err
	}

	// List of known field names (in JSON format)
	knownFields := map[string]bool{
		"prompt":                true,
		"messages":              true,
		"system_prompt":         true,
		"image":                 true,
		"number_of_images":      true,
		"quality":               true,
		"background":            true,
		"seed":                  true,
		"reasoning_effort":      true,
		"num_inference_steps":   true,
		"negative_prompt":       true,
		"instructions":          true,
		"input_item_list":       true,
		"tools":                 true,
		"max_output_tokens":     true,
		"json_schema":           true,
		"temperature":           true,
		"top_p":                 true,
		"top_k":                 true,
		"max_tokens":            true,
		"max_completion_tokens": true,
		"presence_penalty":      true,
		"frequency_penalty":     true,
		"aspect_ratio":          true,
		"output_format":         true,
		"resolution":            true,
		"input_images":          true,
		"image_prompt":          true,
		"input_image":           true,
		"image_input":           true,
		"duration":              true,
		"input_reference":       true,
	}

	// Collect extra fields
	r.ExtraParams = make(map[string]interface{})
	for key, value := range rawMap {
		if !knownFields[key] {
			r.ExtraParams[key] = value
		}
	}

	// If no extra params found, keep it as nil instead of empty map
	if len(r.ExtraParams) == 0 {
		r.ExtraParams = nil
	}

	return nil
}

// ReplicateModelListRequest represents a request to list/search models
type ReplicateModelListRequest struct {
	Query *string `json:"query,omitempty"` // Search query
	Limit *int    `json:"limit,omitempty"` // Maximum results (1-50, default 20)
}

// ==================== RESPONSE TYPES ====================

// ReplicatePredictionStatus represents the status of a prediction
type ReplicatePredictionStatus string

const (
	ReplicatePredictionStatusStarting   ReplicatePredictionStatus = "starting"
	ReplicatePredictionStatusProcessing ReplicatePredictionStatus = "processing"
	ReplicatePredictionStatusSucceeded  ReplicatePredictionStatus = "succeeded"
	ReplicatePredictionStatusFailed     ReplicatePredictionStatus = "failed"
	ReplicatePredictionStatusCanceled   ReplicatePredictionStatus = "canceled"
)

// ReplicatePredictionResponse represents a prediction response
type ReplicatePredictionResponse struct {
	ID               string                    `json:"id"`
	Model            string                    `json:"model"`                       // Model identifier (owner/name or owner/name:version)
	Version          string                    `json:"version"`                     // Model version ID
	Input            map[string]interface{}    `json:"input"`                       // Input parameters used
	Output           *ReplicateOutput          `json:"output,omitempty"`            // Output data (can be various types)
	Logs             *string                   `json:"logs,omitempty"`              // Execution logs
	Error            *string                   `json:"error,omitempty"`             // Error message if failed
	Status           ReplicatePredictionStatus `json:"status"`                      // Current status
	CreatedAt        string                    `json:"created_at"`                  // ISO 8601 timestamp
	StartedAt        *string                   `json:"started_at,omitempty"`        // ISO 8601 timestamp
	CompletedAt      *string                   `json:"completed_at,omitempty"`      // ISO 8601 timestamp
	URLs             *ReplicatePredictionURLs  `json:"urls,omitempty"`              // URLs for API operations
	Metrics          *ReplicateMetrics         `json:"metrics,omitempty"`           // Execution metrics
	DataRemoved      *bool                     `json:"data_removed,omitempty"`      // Whether data has been removed
	Source           *string                   `json:"source,omitempty"`            // Source of the prediction (web/api)
	WebhookCompleted *bool                     `json:"webhook_completed,omitempty"` // Whether webhook was completed
	Stream           *bool                     `json:"stream,omitempty"`            // Whether the prediction is streaming
}

type ReplicateOutputText struct {
	ResponseId *string `json:"response_id,omitempty"`
	Text       *string `json:"text,omitempty"`
}
type ReplicateOutput struct {
	OutputStr    *string
	OutputArray  []string
	OutputObject *ReplicateOutputText
}

// MarshalJSON implements custom JSON marshalling for ReplicateOutput.
// It marshals either OutputStr, OutputArray, or OutputObject directly without wrapping.
func (mc ReplicateOutput) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	fieldsSet := 0
	if mc.OutputStr != nil {
		fieldsSet++
	}
	if mc.OutputArray != nil {
		fieldsSet++
	}
	if mc.OutputObject != nil {
		fieldsSet++
	}
	if fieldsSet > 1 {
		return nil, fmt.Errorf("multiple output fields are set; only one should be non-nil")
	}

	if mc.OutputStr != nil {
		return sonic.Marshal(*mc.OutputStr)
	}
	if mc.OutputArray != nil {
		return sonic.Marshal(mc.OutputArray)
	}
	if mc.OutputObject != nil {
		return sonic.Marshal(mc.OutputObject)
	}
	// If all are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ReplicateOutput.
// It determines whether "content" is a string, array, or object and assigns to the appropriate field.
func (mc *ReplicateOutput) UnmarshalJSON(data []byte) error {
	mc.OutputStr = nil
	mc.OutputArray = nil
	mc.OutputObject = nil

	if string(data) == "null" || len(data) == 0 {
		return nil
	}

	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		mc.OutputStr = schemas.Ptr(stringContent)
		return nil
	}

	// Try to unmarshal as a direct array of strings
	var arrayContent []string
	if err := sonic.Unmarshal(data, &arrayContent); err == nil {
		mc.OutputArray = arrayContent
		return nil
	}

	// Try to unmarshal as an object (ReplicateOutputText)
	var objectContent ReplicateOutputText
	if err := sonic.Unmarshal(data, &objectContent); err == nil {
		mc.OutputObject = &objectContent
		return nil
	}

	return fmt.Errorf("output field is neither a string, array of strings, nor a valid object")
}

// ReplicatePredictionURLs represents URLs for prediction operations
type ReplicatePredictionURLs struct {
	Get    string  `json:"get"`              // URL to get prediction details
	Cancel string  `json:"cancel"`           // URL to cancel prediction
	Stream *string `json:"stream,omitempty"` // URL for streaming output (if applicable)
	Web    *string `json:"web,omitempty"`    // URL for web output (if applicable)
}

// ReplicateMetrics represents execution metrics
type ReplicateMetrics struct {
	PredictTime      *float64 `json:"predict_time,omitempty"`        // Time spent in prediction (seconds)
	TotalTime        *float64 `json:"total_time,omitempty"`          // Total time including queue (seconds)
	ImageCount       *int     `json:"image_count,omitempty"`         // Number of images generated
	TimeToFirstToken *float64 `json:"time_to_first_token,omitempty"` // Time to first token (seconds)
	TokensPerSecond  *float64 `json:"tokens_per_second,omitempty"`   // Tokens generated per second
}

// ReplicatePredictionListResponse represents a paginated list of predictions
type ReplicatePredictionListResponse struct {
	Next     *string                       `json:"next"`     // URL for next page
	Previous *string                       `json:"previous"` // URL for previous page
	Results  []ReplicatePredictionResponse `json:"results"`  // List of predictions
}

// ReplicateModelResponse represents a model response
type ReplicateModelResponse struct {
	URL             string                  `json:"url"`                        // Model API URL
	Owner           string                  `json:"owner"`                      // Owner username or org name
	Name            string                  `json:"name"`                       // Model name
	Description     *string                 `json:"description,omitempty"`      // Model description
	Visibility      string                  `json:"visibility"`                 // "public" or "private"
	GithubURL       *string                 `json:"github_url,omitempty"`       // GitHub repository URL
	PaperURL        *string                 `json:"paper_url,omitempty"`        // Research paper URL
	LicenseURL      *string                 `json:"license_url,omitempty"`      // License URL
	RunCount        *int                    `json:"run_count,omitempty"`        // Number of times run
	CoverImageURL   *string                 `json:"cover_image_url,omitempty"`  // Cover image URL
	DefaultExample  *map[string]interface{} `json:"default_example,omitempty"`  // Default example prediction
	LatestVersion   *ReplicateModelVersion  `json:"latest_version,omitempty"`   // Latest version details
	FeaturedVersion *ReplicateModelVersion  `json:"featured_version,omitempty"` // Featured version details
}

// ReplicateModelVersion represents a model version
type ReplicateModelVersion struct {
	ID            string                 `json:"id"`                        // Version ID
	CreatedAt     string                 `json:"created_at"`                // ISO 8601 timestamp
	CogVersion    *string                `json:"cog_version,omitempty"`     // Cog version used
	OpenAPISchema map[string]interface{} `json:"openapi_schema,omitempty"`  // OpenAPI schema for the model
	DockerImageID *string                `json:"docker_image_id,omitempty"` // Docker image ID
}

// ReplicateModelListResponse represents a paginated list of models
type ReplicateModelListResponse struct {
	Next     *string                  `json:"next"`     // URL for next page
	Previous *string                  `json:"previous"` // URL for previous page
	Results  []ReplicateModelResponse `json:"results"`  // List of models
}

// ReplicateDeploymentOwner represents the owner of a deployment
type ReplicateDeploymentOwner struct {
	Type      string  `json:"type"`                 // "user" or "organization"
	Username  string  `json:"username"`             // Username or organization name
	Name      *string `json:"name,omitempty"`       // Display name
	AvatarURL *string `json:"avatar_url,omitempty"` // Avatar URL
	GithubURL *string `json:"github_url,omitempty"` // GitHub URL
}

// ReplicateDeploymentConfiguration represents the deployment configuration
type ReplicateDeploymentConfiguration struct {
	Hardware     string `json:"hardware"`      // Hardware type (e.g., "gpu-t4")
	MinInstances int    `json:"min_instances"` // Minimum number of instances
	MaxInstances int    `json:"max_instances"` // Maximum number of instances
}

// ReplicateDeploymentRelease represents a deployment release
type ReplicateDeploymentRelease struct {
	Number        int                               `json:"number"`        // Release number
	Model         string                            `json:"model"`         // Model identifier (owner/name)
	Version       string                            `json:"version"`       // Model version ID
	CreatedAt     string                            `json:"created_at"`    // ISO 8601 timestamp
	CreatedBy     *ReplicateDeploymentOwner         `json:"created_by"`    // User or organization that created the release
	Configuration *ReplicateDeploymentConfiguration `json:"configuration"` // Deployment configuration
}

// ReplicateDeployment represents a deployment
type ReplicateDeployment struct {
	Owner          string                      `json:"owner"`           // Owner username or org name
	Name           string                      `json:"name"`            // Deployment name
	CurrentRelease *ReplicateDeploymentRelease `json:"current_release"` // Current active release
}

// ReplicateDeploymentListResponse represents a paginated list of deployments
type ReplicateDeploymentListResponse struct {
	Next     *string               `json:"next"`     // URL for next page
	Previous *string               `json:"previous"` // URL for previous page
	Results  []ReplicateDeployment `json:"results"`  // List of deployments
}

// ==================== ERROR TYPES ====================

// ReplicateError represents an error response from the Replicate API
type ReplicateError struct {
	Detail string  `json:"detail"`          // Error message
	Status int     `json:"status"`          // HTTP status code
	Title  *string `json:"title,omitempty"` // Error title
	Type   *string `json:"type,omitempty"`  // Error type
}

// ==================== STREAMING TYPES ====================

// ReplicateStreamEvent represents a streaming event
type ReplicateStreamEvent struct {
	Event string      `json:"event,omitempty"` // Event type (output, logs, done, error)
	Data  interface{} `json:"data,omitempty"`  // Event data
	Error *string     `json:"error,omitempty"` // Error message if event is error
}

// ==================== WEBHOOK TYPES ====================

// ReplicateWebhookPayload represents a webhook payload
type ReplicateWebhookPayload struct {
	ID          string                    `json:"id"`
	Model       string                    `json:"model"`
	Version     string                    `json:"version"`
	Input       map[string]interface{}    `json:"input"`
	Output      interface{}               `json:"output,omitempty"`
	Logs        *string                   `json:"logs,omitempty"`
	Error       *string                   `json:"error,omitempty"`
	Status      ReplicatePredictionStatus `json:"status"`
	CreatedAt   string                    `json:"created_at"`
	StartedAt   *string                   `json:"started_at,omitempty"`
	CompletedAt *string                   `json:"completed_at,omitempty"`
	URLs        *ReplicatePredictionURLs  `json:"urls,omitempty"`
	Metrics     *ReplicateMetrics         `json:"metrics,omitempty"`
}

// ==================== SSE TYPES ====================

// ReplicateSSEEvent represents a Server-Sent Event from Replicate streaming API
type ReplicateSSEEvent struct {
	Event string // Event type: "output", "done", "error"
	Data  string // Event data - can be plain text, JSON object, or data URI
	ID    string // Event ID (e.g., "1690212292:0")
}

// ReplicateDoneEvent represents the data payload of a "done" event
type ReplicateDoneEvent struct {
	Reason string      `json:"reason,omitempty"` // Reason for completion: "canceled", "error", or empty for success
	Output interface{} `json:"output,omitempty"` // Output data if available (e.g., error message)
}

// ReplicateErrorEvent represents the data payload of an "error" event
type ReplicateErrorEvent struct {
	Detail string `json:"detail"` // Error message
}

// ==================== UTILITY FUNCTIONS ====================

// ParseReplicateTimestamp parses a Replicate ISO 8601 timestamp to Unix timestamp
func ParseReplicateTimestamp(timestamp string) int64 {
	if timestamp == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// ToBifrostPredictionStatus converts Replicate status to Bifrost status
func ToBifrostPredictionStatus(status ReplicatePredictionStatus) string {
	switch status {
	case ReplicatePredictionStatusStarting:
		return "starting"
	case ReplicatePredictionStatusProcessing:
		return "processing"
	case ReplicatePredictionStatusSucceeded:
		return "succeeded"
	case ReplicatePredictionStatusFailed:
		return "failed"
	case ReplicatePredictionStatusCanceled:
		return "canceled"
	default:
		return string(status)
	}
}

// ==================== FILE API TYPES ====================

// ReplicateFileResponse represents a file resource from Replicate API
type ReplicateFileResponse struct {
	ID          string                 `json:"id"`                   // Unique file identifier
	Checksums   *ReplicateFileChecksum `json:"checksums,omitempty"`  // File checksums
	ContentType string                 `json:"content_type"`         // MIME type
	CreatedAt   string                 `json:"created_at"`           // ISO 8601 timestamp
	ExpiresAt   string                 `json:"expires_at,omitempty"` // ISO 8601 timestamp
	Metadata    map[string]interface{} `json:"metadata,omitempty"`   // User-provided metadata
	Name        string                 `json:"name,omitempty"`       // File name
	Size        int64                  `json:"size"`                 // File size in bytes
	URLs        *ReplicateFileURLs     `json:"urls,omitempty"`       // Associated URLs
}

// ReplicateFileChecksum represents checksums for a file
type ReplicateFileChecksum struct {
	SHA256 string `json:"sha256,omitempty"` // SHA256 checksum
}

// ReplicateFileURLs represents URLs associated with a file
type ReplicateFileURLs struct {
	Get string `json:"get"` // URL to retrieve file metadata
}

// ReplicateFileListResponse represents a paginated list of files
type ReplicateFileListResponse struct {
	Next     *string                 `json:"next,omitempty"`     // URL for next page
	Previous *string                 `json:"previous,omitempty"` // URL for previous page
	Results  []ReplicateFileResponse `json:"results"`            // List of files
}
