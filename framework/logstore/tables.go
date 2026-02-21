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
	Providers         []string   `json:"providers,omitempty"`
	Models            []string   `json:"models,omitempty"`
	Status            []string   `json:"status,omitempty"`
	Objects           []string   `json:"objects,omitempty"` // For filtering by request type (chat.completion, text.completion, embedding)
	SelectedKeyIDs    []string   `json:"selected_key_ids,omitempty"`
	VirtualKeyIDs     []string   `json:"virtual_key_ids,omitempty"`
	RoutingRuleIDs    []string   `json:"routing_rule_ids,omitempty"`
	RoutingEngineUsed []string   `json:"routing_engine_used,omitempty"` // For filtering by routing engine (routing-rule, governance, loadbalancing)
	StartTime         *time.Time `json:"start_time,omitempty"`
	EndTime           *time.Time `json:"end_time,omitempty"`
	MinLatency        *float64   `json:"min_latency,omitempty"`
	MaxLatency        *float64   `json:"max_latency,omitempty"`
	MinTokens         *int       `json:"min_tokens,omitempty"`
	MaxTokens         *int       `json:"max_tokens,omitempty"`
	MinCost           *float64   `json:"min_cost,omitempty"`
	MaxCost           *float64   `json:"max_cost,omitempty"`
	MissingCostOnly   bool       `json:"missing_cost_only,omitempty"`
	ContentSearch     string     `json:"content_search,omitempty"`
}

// PaginationOptions represents pagination parameters
type PaginationOptions struct {
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
	SortBy     string `json:"sort_by"`     // "timestamp", "latency", "tokens", "cost"
	Order      string `json:"order"`       // "asc", "desc"
	TotalCount int64  `json:"total_count"` // Total number of items matching the query
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
	RoutingEnginesUsedStr *string   `gorm:"type:varchar(255);column:routing_engines_used" json:"-"` // Comma-separated routing engines
	RoutingRuleID         *string   `gorm:"type:varchar(255);index:idx_logs_routing_rule_id" json:"routing_rule_id"`
	RoutingRuleName       *string   `gorm:"type:varchar(255)" json:"routing_rule_name"`
	InputHistory          string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.ChatMessage
	ResponsesInputHistory string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.ResponsesMessage
	OutputMessage         string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.ChatMessage
	ResponsesOutput       string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.ResponsesMessage
	EmbeddingOutput       string    `gorm:"type:text" json:"-"` // JSON serialized [][]float32
	RerankOutput          string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.RerankResult
	Params                string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.ModelParameters
	Tools                 string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.Tool
	ToolCalls             string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.ToolCall (For backward compatibility, tool calls are now in the content)
	SpeechInput           string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.SpeechInput
	TranscriptionInput    string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.TranscriptionInput
	ImageGenerationInput  string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.ImageGenerationInput
	SpeechOutput          string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostSpeech
	TranscriptionOutput   string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostTranscribe
	ImageGenerationOutput string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostImageGenerationResponse
	ListModelsOutput      string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.Model
	CacheDebug            string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostCacheDebug
	Latency               *float64  `gorm:"index:idx_logs_latency" json:"latency,omitempty"`
	TokenUsage            string    `gorm:"type:text" json:"-"`                            // JSON serialized *schemas.LLMUsage
	Cost                  *float64  `gorm:"index" json:"cost,omitempty"`                   // Cost in dollars (total cost of the request - includes cache lookup cost)
	Status                string    `gorm:"type:varchar(50);index;not null" json:"status"` // "processing", "success", or "error"
	ErrorDetails          string    `gorm:"type:text" json:"-"`                            // JSON serialized *schemas.BifrostError
	Stream                bool      `gorm:"default:false" json:"stream"`                   // true if this was a streaming response
	ContentSummary        string    `gorm:"type:text" json:"-"`
	RawRequest            string    `gorm:"type:text" json:"raw_request"`                   // Populated when `send-back-raw-request` is on
	RawResponse           string    `gorm:"type:text" json:"raw_response"`                  // Populated when `send-back-raw-response` is on
	RoutingEngineLogs     string    `gorm:"type:text" json:"routing_engine_logs,omitempty"` // Formatted routing engine decision logs
	Metadata              string    `gorm:"type:text" json:"-"`                             // JSON serialized map[string]interface{}

	// Denormalized token fields for easier querying
	PromptTokens     int `gorm:"default:0" json:"-"`
	CompletionTokens int `gorm:"default:0" json:"-"`
	TotalTokens      int `gorm:"index:idx_logs_total_tokens;default:0" json:"-"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`

	// Virtual fields for JSON output - these will be populated when needed
	RoutingEnginesUsed          []string                                `gorm:"-" json:"routing_engines_used,omitempty"` // Virtual field deserialized from JSON
	InputHistoryParsed          []schemas.ChatMessage                   `gorm:"-" json:"input_history,omitempty"`
	ResponsesInputHistoryParsed []schemas.ResponsesMessage              `gorm:"-" json:"responses_input_history,omitempty"`
	OutputMessageParsed         *schemas.ChatMessage                    `gorm:"-" json:"output_message,omitempty"`
	ResponsesOutputParsed       []schemas.ResponsesMessage              `gorm:"-" json:"responses_output,omitempty"`
	EmbeddingOutputParsed       []schemas.EmbeddingData                 `gorm:"-" json:"embedding_output,omitempty"`
	RerankOutputParsed          []schemas.RerankResult                  `gorm:"-" json:"rerank_output,omitempty"`
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
	ListModelsOutputParsed      []schemas.Model                         `gorm:"-" json:"list_models_output,omitempty"`
	MetadataParsed              map[string]interface{}                  `gorm:"-" json:"metadata,omitempty"`
	// Populated in handlers after find using the virtual key id and key id
	VirtualKey  *tables.TableVirtualKey  `gorm:"-" json:"virtual_key,omitempty"`  // redacted
	SelectedKey *schemas.Key             `gorm:"-" json:"selected_key,omitempty"` // redacted
	RoutingRule *tables.TableRoutingRule `gorm:"-" json:"routing_rule,omitempty"` // redacted
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
	// Serialize routing engines to comma-separated string
	if len(l.RoutingEnginesUsed) > 0 {
		engineStr := strings.Join(l.RoutingEnginesUsed, ",")
		l.RoutingEnginesUsedStr = &engineStr
	} else {
		l.RoutingEnginesUsedStr = nil
	}

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

	if l.RerankOutputParsed != nil {
		if data, err := json.Marshal(l.RerankOutputParsed); err != nil {
			return err
		} else {
			l.RerankOutput = string(data)
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

	if l.ListModelsOutputParsed != nil {
		if data, err := json.Marshal(l.ListModelsOutputParsed); err != nil {
			return err
		} else {
			l.ListModelsOutput = string(data)
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

	if l.MetadataParsed != nil {
		if data, err := json.Marshal(l.MetadataParsed); err != nil {
			return err
		} else {
			l.Metadata = string(data)
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

	if l.RerankOutput != "" {
		if err := json.Unmarshal([]byte(l.RerankOutput), &l.RerankOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.RerankOutputParsed = nil
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

	if l.ListModelsOutput != "" {
		if err := json.Unmarshal([]byte(l.ListModelsOutput), &l.ListModelsOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ListModelsOutputParsed = nil
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

	if l.Metadata != "" {
		if err := json.Unmarshal([]byte(l.Metadata), &l.MetadataParsed); err != nil {
			l.MetadataParsed = nil
		}
	}

	if l.RoutingEnginesUsedStr != nil && *l.RoutingEnginesUsedStr != "" {
		// Parse comma-separated routing engines
		l.RoutingEnginesUsed = strings.Split(*l.RoutingEnginesUsedStr, ",")
	} else {
		l.RoutingEnginesUsed = []string{}
	}

	return nil
}

// MCPToolLog represents a log entry for MCP tool executions
// This is separate from the main Log table since MCP tool calls have different fields
type MCPToolLog struct {
	ID             string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	LLMRequestID   *string   `gorm:"type:varchar(255);column:llm_request_id;index:idx_mcp_logs_llm_request_id" json:"llm_request_id,omitempty"` // Links to the LLM request that triggered this tool call
	Timestamp      time.Time `gorm:"index;not null" json:"timestamp"`
	ToolName       string    `gorm:"type:varchar(255);index:idx_mcp_logs_tool_name;not null" json:"tool_name"`
	ServerLabel    string    `gorm:"type:varchar(255);index:idx_mcp_logs_server_label" json:"server_label,omitempty"` // MCP server that provided the tool
	VirtualKeyID   *string   `gorm:"type:varchar(255);index:idx_mcp_logs_virtual_key_id" json:"virtual_key_id"`
	VirtualKeyName *string   `gorm:"type:varchar(255)" json:"virtual_key_name"`
	Arguments      string    `gorm:"type:text" json:"-"`                                                // JSON serialized tool arguments
	Result         string    `gorm:"type:text" json:"-"`                                                // JSON serialized tool result
	ErrorDetails   string    `gorm:"type:text" json:"-"`                                                // JSON serialized *schemas.BifrostError
	Latency        *float64  `gorm:"index:idx_mcp_logs_latency" json:"latency,omitempty"`               // Execution time in milliseconds
	Cost           *float64  `gorm:"index:idx_mcp_logs_cost" json:"cost,omitempty"`                     // Cost in dollars (per execution cost)
	Status         string    `gorm:"type:varchar(50);index:idx_mcp_logs_status;not null" json:"status"` // "processing", "success", or "error"
	Metadata       string    `gorm:"type:text" json:"-"`                                               // JSON serialized map[string]interface{}
	CreatedAt      time.Time `gorm:"index;not null" json:"created_at"`

	// Virtual fields for JSON output - populated when needed
	ArgumentsParsed    interface{}             `gorm:"-" json:"arguments,omitempty"`
	ResultParsed       interface{}             `gorm:"-" json:"result,omitempty"`
	ErrorDetailsParsed *schemas.BifrostError   `gorm:"-" json:"error_details,omitempty"`
	MetadataParsed     map[string]interface{}  `gorm:"-" json:"metadata,omitempty"`
	VirtualKey         *tables.TableVirtualKey `gorm:"-" json:"virtual_key,omitempty"`
}

// TableName sets the table name for GORM
func (MCPToolLog) TableName() string {
	return "mcp_tool_logs"
}

// BeforeCreate GORM hook to set created_at and serialize JSON fields
func (l *MCPToolLog) BeforeCreate(tx *gorm.DB) error {
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now().UTC()
	}
	if l.Timestamp.IsZero() {
		l.Timestamp = time.Now().UTC()
	}
	return l.SerializeFields()
}

// BeforeSave GORM hook to serialize JSON fields
func (l *MCPToolLog) BeforeSave(tx *gorm.DB) error {
	return l.SerializeFields()
}

// AfterFind GORM hook to deserialize JSON fields
func (l *MCPToolLog) AfterFind(tx *gorm.DB) error {
	return l.DeserializeFields()
}

// SerializeFields converts Go structs to JSON strings for storage
func (l *MCPToolLog) SerializeFields() error {
	if l.ArgumentsParsed != nil {
		if data, err := json.Marshal(l.ArgumentsParsed); err != nil {
			return err
		} else {
			l.Arguments = string(data)
		}
	}

	if l.ResultParsed != nil {
		if data, err := json.Marshal(l.ResultParsed); err != nil {
			return err
		} else {
			l.Result = string(data)
		}
	}

	if l.ErrorDetailsParsed != nil {
		if data, err := json.Marshal(l.ErrorDetailsParsed); err != nil {
			return err
		} else {
			l.ErrorDetails = string(data)
		}
	}

	if l.MetadataParsed != nil {
		if data, err := json.Marshal(l.MetadataParsed); err != nil {
			return err
		} else {
			l.Metadata = string(data)
		}
	}

	return nil
}

// DeserializeFields converts JSON strings back to Go structs
func (l *MCPToolLog) DeserializeFields() error {
	if l.Arguments != "" {
		if err := json.Unmarshal([]byte(l.Arguments), &l.ArgumentsParsed); err != nil {
			l.ArgumentsParsed = nil
		}
	}

	if l.Result != "" {
		if err := json.Unmarshal([]byte(l.Result), &l.ResultParsed); err != nil {
			l.ResultParsed = nil
		}
	}

	if l.ErrorDetails != "" {
		if err := json.Unmarshal([]byte(l.ErrorDetails), &l.ErrorDetailsParsed); err != nil {
			l.ErrorDetailsParsed = nil
		}
	}

	if l.Metadata != "" {
		if err := json.Unmarshal([]byte(l.Metadata), &l.MetadataParsed); err != nil {
			l.MetadataParsed = nil
		}
	}

	return nil
}

// AsyncJob represents an asynchronous job record in the database.
// Jobs are created when requests are submitted to async endpoints and
// updated when the background operation completes or fails.
type AsyncJob struct {
	ID           string                 `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Status       schemas.AsyncJobStatus `gorm:"type:varchar(50);index:idx_async_jobs_status;not null" json:"status"`
	RequestType  schemas.RequestType    `gorm:"type:varchar(50);index:idx_async_jobs_request_type;not null" json:"request_type"`
	Response     string                 `gorm:"type:text" json:"response"`
	StatusCode   int                    `gorm:"default:0" json:"status_code,omitempty"`
	Error        string                 `gorm:"type:text" json:"error,omitempty"`
	VirtualKeyID *string                `gorm:"type:varchar(255);index:idx_async_jobs_vk_id" json:"virtual_key_id,omitempty"`
	ResultTTL    int                    `gorm:"default:3600" json:"-"` // TTL in seconds, used to calculate ExpiresAt on completion
	ExpiresAt    *time.Time             `gorm:"index:idx_async_jobs_expires_at" json:"expires_at,omitempty"`
	CreatedAt    time.Time              `gorm:"index;not null" json:"created_at"`
	CompletedAt  *time.Time             `json:"completed_at,omitempty"`
}

// TableName sets the table name for GORM
func (AsyncJob) TableName() string {
	return "async_jobs"
}

// ToResponse converts an AsyncJob database record to an AsyncJobResponse for JSON output.
func (j *AsyncJob) ToResponse() *schemas.AsyncJobResponse {
	resp := &schemas.AsyncJobResponse{
		ID:          j.ID,
		Status:      j.Status,
		ExpiresAt:   j.ExpiresAt,
		CreatedAt:   j.CreatedAt,
		CompletedAt: j.CompletedAt,
		StatusCode:  j.StatusCode,
	}

	if j.Response != "" {
		switch j.RequestType {
		case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
			var result schemas.BifrostResponsesResponse
			if err := sonic.Unmarshal([]byte(j.Response), &result); err == nil {
				resp.Result = &result
			}
		case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
			var result schemas.BifrostChatResponse
			if err := sonic.Unmarshal([]byte(j.Response), &result); err == nil {
				resp.Result = &result
			}
		case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
			var result schemas.BifrostTextCompletionResponse
			if err := sonic.Unmarshal([]byte(j.Response), &result); err == nil {
				resp.Result = &result
			}
		case schemas.EmbeddingRequest:
			var result schemas.BifrostEmbeddingResponse
			if err := sonic.Unmarshal([]byte(j.Response), &result); err == nil {
				resp.Result = &result
			}
		case schemas.SpeechRequest, schemas.SpeechStreamRequest:
			var result schemas.BifrostSpeechResponse
			if err := sonic.Unmarshal([]byte(j.Response), &result); err == nil {
				resp.Result = &result
			}
		case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
			var result schemas.BifrostTranscriptionResponse
			if err := sonic.Unmarshal([]byte(j.Response), &result); err == nil {
				resp.Result = &result
			}
		case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest:
			var result schemas.BifrostImageGenerationResponse
			if err := sonic.Unmarshal([]byte(j.Response), &result); err == nil {
				resp.Result = &result
			}
		case schemas.ImageEditRequest, schemas.ImageEditStreamRequest:
			var result schemas.BifrostImageGenerationResponse
			if err := sonic.Unmarshal([]byte(j.Response), &result); err == nil {
				resp.Result = &result
			}
		case schemas.ImageVariationRequest:
			var result schemas.BifrostImageGenerationResponse
			if err := sonic.Unmarshal([]byte(j.Response), &result); err == nil {
				resp.Result = &result
			}
		case schemas.CountTokensRequest:
			var result schemas.BifrostCountTokensResponse
			if err := sonic.Unmarshal([]byte(j.Response), &result); err == nil {
				resp.Result = &result
			}
		default:
			var result interface{}
			if err := sonic.Unmarshal([]byte(j.Response), &result); err == nil {
				resp.Result = result
			}
		}
		// Should never happen, but just in case
		if resp.Result == nil {
			var raw interface{}
			if err := sonic.Unmarshal([]byte(j.Response), &raw); err == nil {
				resp.Result = raw
			}
		}
	}

	if j.Error != "" {
		var bifrostErr schemas.BifrostError
		if err := sonic.Unmarshal([]byte(j.Error), &bifrostErr); err == nil {
			resp.Error = &bifrostErr
		}
	}

	return resp
}

// MCPToolLogSearchFilters represents the available filters for MCP tool log searches
type MCPToolLogSearchFilters struct {
	ToolNames     []string   `json:"tool_names,omitempty"`
	ServerLabels  []string   `json:"server_labels,omitempty"`
	Status        []string   `json:"status,omitempty"`
	VirtualKeyIDs []string   `json:"virtual_key_ids,omitempty"`
	LLMRequestIDs []string   `json:"llm_request_ids,omitempty"`
	StartTime     *time.Time `json:"start_time,omitempty"`
	EndTime       *time.Time `json:"end_time,omitempty"`
	MinLatency    *float64   `json:"min_latency,omitempty"`
	MaxLatency    *float64   `json:"max_latency,omitempty"`
	ContentSearch string     `json:"content_search,omitempty"`
}

// MCPToolLogSearchResult represents the result of an MCP tool log search
type MCPToolLogSearchResult struct {
	Logs       []MCPToolLog      `json:"logs"`
	Pagination PaginationOptions `json:"pagination"`
	Stats      MCPToolLogStats   `json:"stats"`
	HasLogs    bool              `json:"has_logs"`
}

// MCPToolLogStats represents statistics for MCP tool log searches
type MCPToolLogStats struct {
	TotalExecutions int64   `json:"total_executions"`
	SuccessRate     float64 `json:"success_rate"`
	AverageLatency  float64 `json:"average_latency"`
	TotalCost       float64 `json:"total_cost"` // Total cost in dollars
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

	// Add rerank output content
	if l.RerankOutputParsed != nil {
		for _, result := range l.RerankOutputParsed {
			if result.Document != nil && result.Document.Text != "" {
				parts = append(parts, result.Document.Text)
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
