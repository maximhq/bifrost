package logstore

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

type SortBy string

const (
	SortByTimestamp SortBy = "timestamp"
	SortByLatency   SortBy = "latency"
	SortByTokens    SortBy = "tokens"
	SortByCost      SortBy = "cost"
)

type SortOrder string

const (
	SortAsc  SortOrder = "asc"
	SortDesc SortOrder = "desc"
)

// SearchFilters represents the available filters for log searches
type SearchFilters struct {
	Providers       []string   `json:"providers,omitempty"`
	Models          []string   `json:"models,omitempty"`
	Status          []string   `json:"status,omitempty"`
	Objects         []string   `json:"objects,omitempty"` // For filtering by request type (chat.completion, text.completion, embedding)
	SelectedKeyIDs  []string   `json:"selected_key_ids,omitempty"`
	VirtualKeyIDs   []string   `json:"virtual_key_ids,omitempty"`
	StartTime       *time.Time `json:"start_time,omitempty"`
	EndTime         *time.Time `json:"end_time,omitempty"`
	MinLatency      *float64   `json:"min_latency,omitempty"`
	MaxLatency      *float64   `json:"max_latency,omitempty"`
	MinTokens       *int       `json:"min_tokens,omitempty"`
	MaxTokens       *int       `json:"max_tokens,omitempty"`
	MinCost         *float64   `json:"min_cost,omitempty"`
	MaxCost         *float64   `json:"max_cost,omitempty"`
	MissingCostOnly bool       `json:"missing_cost_only,omitempty"`
	ContentSearch   string     `json:"content_search,omitempty"`
}

// PaginationOptions represents pagination parameters
type PaginationOptions struct {
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
	SortBy string `json:"sort_by"` // "timestamp", "latency", "tokens", "cost"
	Order  string `json:"order"`   // "asc", "desc"
}

// SearchResult represents the result of a log search
type SearchResult struct {
	Logs       []Log             `json:"logs"`
	Pagination PaginationOptions `json:"pagination"`
	Stats      SearchStats       `json:"stats"`
	HasLogs    bool              `json:"has_logs"`
}

type SearchStats struct {
	TotalRequests  int64   `json:"total_requests"`
	SuccessRate    float64 `json:"success_rate"`    // Percentage of successful requests
	AverageLatency float64 `json:"average_latency"` // Average latency in milliseconds
	TotalTokens    int64   `json:"total_tokens"`    // Total tokens used
	TotalCost      float64 `json:"total_cost"`      // Total cost in dollars
}

// Log represents a complete log entry for a request/response cycle
// This is the GORM model with appropriate tags
type Log struct {
	ID                    string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	ParentRequestID       *string   `gorm:"type:varchar(255)" json:"parent_request_id"`
	Timestamp             time.Time `gorm:"index;not null" json:"timestamp"`
	Object                string    `gorm:"type:varchar(255);index;not null;column:object_type" json:"object"` // text.completion, chat.completion, or embedding
	Provider              string    `gorm:"type:varchar(255);index;not null" json:"provider"`
	Model                 string    `gorm:"type:varchar(255);index;not null" json:"model"`
	NumberOfRetries       int       `gorm:"default:0" json:"number_of_retries"`
	FallbackIndex         int       `gorm:"default:0" json:"fallback_index"`
	SelectedKeyID         string    `gorm:"type:varchar(255);index:idx_logs_selected_key_id" json:"selected_key_id"`
	SelectedKeyName       string    `gorm:"type:varchar(255)" json:"selected_key_name"`
	VirtualKeyID          *string   `gorm:"type:varchar(255);index:idx_logs_virtual_key_id" json:"virtual_key_id"`
	VirtualKeyName        *string   `gorm:"type:varchar(255)" json:"virtual_key_name"`
	InputHistory          string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.ChatMessage
	ResponsesInputHistory string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.ResponsesMessage
	OutputMessage         string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.ChatMessage
	ResponsesOutput       string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.ResponsesMessage
	EmbeddingOutput       string    `gorm:"type:text" json:"-"` // JSON serialized [][]float32
	Params                string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.ModelParameters
	Tools                 string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.Tool
	ToolCalls             string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.ToolCall (For backward compatibility, tool calls are now in the content)
	SpeechInput           string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.SpeechInput
	TranscriptionInput    string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.TranscriptionInput
	ImageGenerationInput  string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.ImageGenerationInput
	SpeechOutput          string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostSpeech
	TranscriptionOutput   string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostTranscribe
	ImageGenerationOutput string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostImageGenerationResponse
	CacheDebug            string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostCacheDebug
	Latency               *float64  `gorm:"index:idx_logs_latency" json:"latency,omitempty"`
	TokenUsage            string    `gorm:"type:text" json:"-"`                            // JSON serialized *schemas.LLMUsage
	Cost                  *float64  `gorm:"index" json:"cost,omitempty"`                   // Cost in dollars (total cost of the request - includes cache lookup cost)
	Status                string    `gorm:"type:varchar(50);index;not null" json:"status"` // "processing", "success", or "error"
	ErrorDetails          string    `gorm:"type:text" json:"-"`                            // JSON serialized *schemas.BifrostError
	Stream                bool      `gorm:"default:false" json:"stream"`                   // true if this was a streaming response
	ContentSummary        string    `gorm:"type:text" json:"-"`
	RawRequest            string    `gorm:"type:text" json:"raw_request"`  // Populated when `send-back-raw-request` is on
	RawResponse           string    `gorm:"type:text" json:"raw_response"` // Populated when `send-back-raw-response` is on

	// Denormalized token fields for easier querying
	PromptTokens     int `gorm:"default:0" json:"-"`
	CompletionTokens int `gorm:"default:0" json:"-"`
	TotalTokens      int `gorm:"index:idx_logs_total_tokens;default:0" json:"-"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`

	// Virtual fields for JSON output - these will be populated when needed
	InputHistoryParsed          []schemas.ChatMessage                   `gorm:"-" json:"input_history,omitempty"`
	ResponsesInputHistoryParsed []schemas.ResponsesMessage              `gorm:"-" json:"responses_input_history,omitempty"`
	OutputMessageParsed         *schemas.ChatMessage                    `gorm:"-" json:"output_message,omitempty"`
	ResponsesOutputParsed       []schemas.ResponsesMessage              `gorm:"-" json:"responses_output,omitempty"`
	EmbeddingOutputParsed       []schemas.EmbeddingData                 `gorm:"-" json:"embedding_output,omitempty"`
	ParamsParsed                interface{}                             `gorm:"-" json:"params,omitempty"`
	ToolsParsed                 []schemas.ChatTool                      `gorm:"-" json:"tools,omitempty"`
	ToolCallsParsed             []schemas.ChatAssistantMessageToolCall  `gorm:"-" json:"tool_calls,omitempty"` // For backward compatibility, tool calls are now in the content
	TokenUsageParsed            *schemas.BifrostLLMUsage                `gorm:"-" json:"token_usage,omitempty"`
	ErrorDetailsParsed          *schemas.BifrostError                   `gorm:"-" json:"error_details,omitempty"`
	SpeechInputParsed           *schemas.SpeechInput                    `gorm:"-" json:"speech_input,omitempty"`
	TranscriptionInputParsed    *schemas.TranscriptionInput             `gorm:"-" json:"transcription_input,omitempty"`
	ImageGenerationInputParsed  *schemas.ImageGenerationInput           `gorm:"-" json:"image_generation_input,omitempty"`
	SpeechOutputParsed          *schemas.BifrostSpeechResponse          `gorm:"-" json:"speech_output,omitempty"`
	TranscriptionOutputParsed   *schemas.BifrostTranscriptionResponse   `gorm:"-" json:"transcription_output,omitempty"`
	ImageGenerationOutputParsed *schemas.BifrostImageGenerationResponse `gorm:"-" json:"image_generation_output,omitempty"`
	CacheDebugParsed            *schemas.BifrostCacheDebug              `gorm:"-" json:"cache_debug,omitempty"`

	// Populated in handlers after find using the virtual key id and key id
	VirtualKey  *tables.TableVirtualKey `gorm:"-" json:"virtual_key,omitempty"`  // redacted
	SelectedKey *schemas.Key            `gorm:"-" json:"selected_key,omitempty"` // redacted
}

// NewLogEntryFromMap creates a new Log from a map[string]interface{}
func NewLogEntryFromMap(entry map[string]interface{}) *Log {
	var log Log
	data, err := sonic.Marshal(entry)
	if err != nil {
		return nil
	}
	err = sonic.Unmarshal(data, &log)
	if err != nil {
		return nil
	}
	return &log
}

// TableName sets the table name for GORM
func (Log) TableName() string {
	return "logs"
}

// BeforeCreate GORM hook to set created_at and serialize JSON fields
func (l *Log) BeforeCreate(tx *gorm.DB) error {
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now().UTC()
	}
	return l.SerializeFields()
}

// BeforeSave GORM hook to serialize JSON fields
func (l *Log) BeforeSave(tx *gorm.DB) error {
	return l.SerializeFields()
}

// AfterFind GORM hook to deserialize JSON fields
func (l *Log) AfterFind(tx *gorm.DB) error {
	return l.DeserializeFields()
}

// SerializeFields converts Go structs to JSON strings for storage
func (l *Log) SerializeFields() error {
	if l.InputHistoryParsed != nil {
		if data, err := json.Marshal(l.InputHistoryParsed); err != nil {
			return err
		} else {
			l.InputHistory = string(data)
		}
	}

	if l.ResponsesInputHistoryParsed != nil {
		if data, err := json.Marshal(l.ResponsesInputHistoryParsed); err != nil {
			return err
		} else {
			l.ResponsesInputHistory = string(data)
		}
	}

	if l.OutputMessageParsed != nil {
		if data, err := json.Marshal(l.OutputMessageParsed); err != nil {
			return err
		} else {
			l.OutputMessage = string(data)
		}
	}

	if l.ResponsesOutputParsed != nil {
		if data, err := json.Marshal(l.ResponsesOutputParsed); err != nil {
			return err
		} else {
			l.ResponsesOutput = string(data)
		}
	}

	if l.EmbeddingOutputParsed != nil {
		if data, err := json.Marshal(l.EmbeddingOutputParsed); err != nil {
			return err
		} else {
			l.EmbeddingOutput = string(data)
		}
	}

	if l.SpeechInputParsed != nil {
		if data, err := json.Marshal(l.SpeechInputParsed); err != nil {
			return err
		} else {
			l.SpeechInput = string(data)
		}
	}

	if l.TranscriptionInputParsed != nil {
		if data, err := json.Marshal(l.TranscriptionInputParsed); err != nil {
			return err
		} else {
			l.TranscriptionInput = string(data)
		}
	}

	if l.ImageGenerationInputParsed != nil {
		if data, err := json.Marshal(l.ImageGenerationInputParsed); err != nil {
			return err
		} else {
			l.ImageGenerationInput = string(data)
		}
	}

	if l.SpeechOutputParsed != nil {
		if data, err := json.Marshal(l.SpeechOutputParsed); err != nil {
			return err
		} else {
			l.SpeechOutput = string(data)
		}
	}

	if l.TranscriptionOutputParsed != nil {
		if data, err := json.Marshal(l.TranscriptionOutputParsed); err != nil {
			return err
		} else {
			l.TranscriptionOutput = string(data)
		}
	}

	if l.ImageGenerationOutputParsed != nil {
		if data, err := json.Marshal(l.ImageGenerationOutputParsed); err != nil {
			return err
		} else {
			l.ImageGenerationOutput = string(data)
		}
	}

	if l.ParamsParsed != nil {
		if data, err := json.Marshal(l.ParamsParsed); err != nil {
			return err
		} else {
			l.Params = string(data)
		}
	}

	if l.ToolsParsed != nil {
		if data, err := json.Marshal(l.ToolsParsed); err != nil {
			return err
		} else {
			l.Tools = string(data)
		}
	}

	if l.ToolCallsParsed != nil {
		if data, err := json.Marshal(l.ToolCallsParsed); err != nil {
			return err
		} else {
			l.ToolCalls = string(data)
		}
	}

	if l.TokenUsageParsed != nil {
		if data, err := json.Marshal(l.TokenUsageParsed); err != nil {
			return err
		} else {
			l.TokenUsage = string(data)
		}
		// Update denormalized fields for easier querying
		l.PromptTokens = l.TokenUsageParsed.PromptTokens
		l.CompletionTokens = l.TokenUsageParsed.CompletionTokens
		l.TotalTokens = l.TokenUsageParsed.TotalTokens
	}

	if l.ErrorDetailsParsed != nil {
		if data, err := json.Marshal(l.ErrorDetailsParsed); err != nil {
			return err
		} else {
			l.ErrorDetails = string(data)
		}
	}

	if l.CacheDebugParsed != nil {
		if data, err := json.Marshal(l.CacheDebugParsed); err != nil {
			return err
		} else {
			l.CacheDebug = string(data)
		}
	}

	// Build content summary for search
	l.ContentSummary = l.BuildContentSummary()

	return nil
}

// DeserializeFields converts JSON strings back to Go structs
func (l *Log) DeserializeFields() error {
	if l.InputHistory != "" {
		if err := json.Unmarshal([]byte(l.InputHistory), &l.InputHistoryParsed); err != nil {
			// Log error but don't fail the operation - initialize as empty slice
			l.InputHistoryParsed = []schemas.ChatMessage{}
		}
	}

	if l.ResponsesInputHistory != "" {
		if err := json.Unmarshal([]byte(l.ResponsesInputHistory), &l.ResponsesInputHistoryParsed); err != nil {
			// Log error but don't fail the operation - initialize as empty slice
			l.ResponsesInputHistoryParsed = []schemas.ResponsesMessage{}
		}
	}

	if l.OutputMessage != "" {
		if err := json.Unmarshal([]byte(l.OutputMessage), &l.OutputMessageParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.OutputMessageParsed = nil
		}
	}

	if l.ResponsesOutput != "" {
		if err := json.Unmarshal([]byte(l.ResponsesOutput), &l.ResponsesOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ResponsesOutputParsed = []schemas.ResponsesMessage{}
		}
	}

	if l.EmbeddingOutput != "" {
		if err := json.Unmarshal([]byte(l.EmbeddingOutput), &l.EmbeddingOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.EmbeddingOutputParsed = nil
		}
	}

	if l.Params != "" {
		if err := json.Unmarshal([]byte(l.Params), &l.ParamsParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ParamsParsed = nil
		}
	}

	if l.Tools != "" {
		if err := json.Unmarshal([]byte(l.Tools), &l.ToolsParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ToolsParsed = nil
		}
	}

	if l.ToolCalls != "" {
		if err := json.Unmarshal([]byte(l.ToolCalls), &l.ToolCallsParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ToolCallsParsed = nil
		}
	}

	if l.TokenUsage != "" {
		if err := json.Unmarshal([]byte(l.TokenUsage), &l.TokenUsageParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.TokenUsageParsed = nil
		}
	}

	if l.ErrorDetails != "" {
		if err := json.Unmarshal([]byte(l.ErrorDetails), &l.ErrorDetailsParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ErrorDetailsParsed = nil
		}
	}

	// Deserialize speech and transcription fields
	if l.SpeechInput != "" {
		if err := json.Unmarshal([]byte(l.SpeechInput), &l.SpeechInputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.SpeechInputParsed = nil
		}
	}

	if l.TranscriptionInput != "" {
		if err := json.Unmarshal([]byte(l.TranscriptionInput), &l.TranscriptionInputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.TranscriptionInputParsed = nil
		}
	}

	if l.ImageGenerationInput != "" {
		if err := json.Unmarshal([]byte(l.ImageGenerationInput), &l.ImageGenerationInputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ImageGenerationInputParsed = nil
		}
	}

	if l.SpeechOutput != "" {
		if err := json.Unmarshal([]byte(l.SpeechOutput), &l.SpeechOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.SpeechOutputParsed = nil
		}
	}

	if l.TranscriptionOutput != "" {
		if err := json.Unmarshal([]byte(l.TranscriptionOutput), &l.TranscriptionOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.TranscriptionOutputParsed = nil
		}
	}

	if l.ImageGenerationOutput != "" {
		if err := json.Unmarshal([]byte(l.ImageGenerationOutput), &l.ImageGenerationOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ImageGenerationOutputParsed = nil
		}
	}

	if l.CacheDebug != "" {
		if err := json.Unmarshal([]byte(l.CacheDebug), &l.CacheDebugParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.CacheDebugParsed = nil
		}
	}

	return nil
}

// BuildContentSummary creates a searchable text summary
func (l *Log) BuildContentSummary() string {
	var parts []string

	// Add input messages
	for _, msg := range l.InputHistoryParsed {
		if msg.Content != nil {
			// Access content through the Content field
			if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
				parts = append(parts, *msg.Content.ContentStr)
			}
			// If content blocks exist, extract text from them
			if msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					if block.Text != nil && *block.Text != "" {
						parts = append(parts, *block.Text)
					}
				}
			}
		}
	}

	// Add responses input history
	if l.ResponsesInputHistoryParsed != nil {
		for _, msg := range l.ResponsesInputHistoryParsed {
			if msg.Content != nil {
				if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
					parts = append(parts, *msg.Content.ContentStr)
				}
				// If content blocks exist, extract text from them
				if msg.Content.ContentBlocks != nil {
					for _, block := range msg.Content.ContentBlocks {
						if block.Text != nil && *block.Text != "" {
							parts = append(parts, *block.Text)
						}
					}
				}
			}
			if msg.ResponsesReasoning != nil {
				for _, summary := range msg.ResponsesReasoning.Summary {
					parts = append(parts, summary.Text)
				}
			}
		}
	}

	// Add output message
	if l.OutputMessageParsed != nil {
		if l.OutputMessageParsed.Content != nil {
			if l.OutputMessageParsed.Content.ContentStr != nil && *l.OutputMessageParsed.Content.ContentStr != "" {
				parts = append(parts, *l.OutputMessageParsed.Content.ContentStr)
			}
			// If content blocks exist, extract text from them
			if l.OutputMessageParsed.Content.ContentBlocks != nil {
				for _, block := range l.OutputMessageParsed.Content.ContentBlocks {
					if block.Text != nil && *block.Text != "" {
						parts = append(parts, *block.Text)
					}
				}
			}
		}
	}

	// Add responses output content
	if l.ResponsesOutputParsed != nil {
		for _, msg := range l.ResponsesOutputParsed {
			if msg.Content != nil {
				if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
					parts = append(parts, *msg.Content.ContentStr)
				}
				// If content blocks exist, extract text from them
				if msg.Content.ContentBlocks != nil {
					for _, block := range msg.Content.ContentBlocks {
						if block.Text != nil && *block.Text != "" {
							parts = append(parts, *block.Text)
						}
					}
				}
			}
			if msg.ResponsesReasoning != nil {
				for _, summary := range msg.ResponsesReasoning.Summary {
					parts = append(parts, summary.Text)
				}
			}
		}
	}

	// Add speech input content
	if l.SpeechInputParsed != nil && l.SpeechInputParsed.Input != "" {
		parts = append(parts, l.SpeechInputParsed.Input)
	}

	// Add transcription output content
	if l.TranscriptionOutputParsed != nil && l.TranscriptionOutputParsed.Text != "" {
		parts = append(parts, l.TranscriptionOutputParsed.Text)
	}

	// Add image generation input prompt
	if l.ImageGenerationInputParsed != nil && l.ImageGenerationInputParsed.Prompt != "" {
		parts = append(parts, l.ImageGenerationInputParsed.Prompt)
	}

	// Add error details
	if l.ErrorDetailsParsed != nil && l.ErrorDetailsParsed.Error.Message != "" {
		parts = append(parts, l.ErrorDetailsParsed.Error.Message)
	}

	return strings.Join(parts, " ")
}

// HistogramBucket represents a single time bucket in the histogram
type HistogramBucket struct {
	Timestamp time.Time `json:"timestamp"`
	Count     int64     `json:"count"`
	Success   int64     `json:"success"`
	Error     int64     `json:"error"`
}

// HistogramResult represents the histogram query result
type HistogramResult struct {
	Buckets           []HistogramBucket `json:"buckets"`
	BucketSizeSeconds int64             `json:"bucket_size_seconds"`
}

// TokenHistogramBucket represents a single time bucket for token usage
type TokenHistogramBucket struct {
	Timestamp        time.Time `json:"timestamp"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	TotalTokens      int64     `json:"total_tokens"`
}

// TokenHistogramResult represents the token histogram query result
type TokenHistogramResult struct {
	Buckets           []TokenHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                  `json:"bucket_size_seconds"`
}

// CostHistogramBucket represents a single time bucket for cost data
type CostHistogramBucket struct {
	Timestamp time.Time          `json:"timestamp"`
	TotalCost float64            `json:"total_cost"`
	ByModel   map[string]float64 `json:"by_model"`
}

// CostHistogramResult represents the cost histogram query result
type CostHistogramResult struct {
	Buckets           []CostHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                 `json:"bucket_size_seconds"`
	Models            []string              `json:"models"`
}

// ModelUsageStats represents usage statistics for a single model
type ModelUsageStats struct {
	Total   int64 `json:"total"`
	Success int64 `json:"success"`
	Error   int64 `json:"error"`
}

// ModelHistogramBucket represents a single time bucket for model usage
type ModelHistogramBucket struct {
	Timestamp time.Time                  `json:"timestamp"`
	ByModel   map[string]ModelUsageStats `json:"by_model"`
}

// ModelHistogramResult represents the model histogram query result
type ModelHistogramResult struct {
	Buckets           []ModelHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                  `json:"bucket_size_seconds"`
	Models            []string               `json:"models"`
}
