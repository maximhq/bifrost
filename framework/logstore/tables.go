package logstore

import (
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"

	"gorm.io/gorm"
)

// sanitizeJSONForJSONB removes JSON escape sequences that PostgreSQL's jsonb
// type cannot represent. Specifically, jsonb rejects the NUL unicode escape
// (\u0000) with errcode 22P05 even though plain TEXT and the json type can
// store it. Stripping the escape from the marshaled JSON keeps subsequent
// TEXT->jsonb casts in list queries safe. The replacement is case-insensitive
// since JSON allows \u0000 or \U0000.
func sanitizeJSONForJSONB(s string) string {
	if !strings.Contains(s, `\u`) && !strings.Contains(s, `\U`) {
		return s
	}
	s = strings.ReplaceAll(s, `\u0000`, "")
	s = strings.ReplaceAll(s, `\U0000`, "")
	return s
}

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
	Providers         []string          `json:"providers,omitempty"`
	Models            []string          `json:"models,omitempty"`
	Aliases           []string          `json:"aliases,omitempty"`
	Status            []string          `json:"status,omitempty"`
	StopReasons       []string          `json:"stop_reasons,omitempty"` // For filtering by stop reason (stop, length, content_filter, refusal, tool_calls, etc.)
	Objects           []string          `json:"objects,omitempty"`      // For filtering by request type (chat.completion, text.completion, embedding)
	ParentRequestID   string            `json:"parent_request_id,omitempty"`
	SelectedKeyIDs    []string          `json:"selected_key_ids,omitempty"`
	VirtualKeyIDs     []string          `json:"virtual_key_ids,omitempty"`
	RoutingRuleIDs    []string          `json:"routing_rule_ids,omitempty"`
	TeamIDs           []string          `json:"team_ids,omitempty"`
	CustomerIDs       []string          `json:"customer_ids,omitempty"`
	UserIDs           []string          `json:"user_ids,omitempty"`
	BusinessUnitIDs   []string          `json:"business_unit_ids,omitempty"`
	RoutingEngineUsed []string          `json:"routing_engine_used,omitempty"` // For filtering by routing engine (routing-rule, governance, loadbalancing)
	StartTime         *time.Time        `json:"start_time,omitempty"`
	EndTime           *time.Time        `json:"end_time,omitempty"`
	MinLatency        *float64          `json:"min_latency,omitempty"`
	MaxLatency        *float64          `json:"max_latency,omitempty"`
	MinTokens         *int              `json:"min_tokens,omitempty"`
	MaxTokens         *int              `json:"max_tokens,omitempty"`
	MinCost           *float64          `json:"min_cost,omitempty"`
	MaxCost           *float64          `json:"max_cost,omitempty"`
	MissingCostOnly   bool              `json:"missing_cost_only,omitempty"`
	CacheHitTypes     []string          `json:"cache_hit_types,omitempty"` // For filtering by local-cache hit type ("direct", "semantic")
	ContentSearch     string            `json:"content_search,omitempty"`
	MetadataFilters   map[string]string `json:"metadata_filters,omitempty"` // key=metadataKey, value=metadataValue for filtering by metadata
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

type SessionDetailResult struct {
	SessionID     string            `json:"session_id"`
	Logs          []Log             `json:"logs"`
	Pagination    PaginationOptions `json:"pagination"`
	Count         int64             `json:"count"`
	ReturnedCount int               `json:"returned_count"`
	HasMore       bool              `json:"has_more"`
}

type SessionSummaryResult struct {
	SessionID   string  `json:"session_id"`
	Count       int64   `json:"count"`
	TotalCost   float64 `json:"total_cost"`
	TotalTokens int64   `json:"total_tokens"`
	StartedAt   string  `json:"started_at,omitempty"`
	LatestAt    string  `json:"latest_at,omitempty"`
	DurationMs  int64   `json:"duration_ms"`
}

type SearchStats struct {
	TotalRequests             int64   `json:"total_requests"`
	SuccessRate               float64 `json:"success_rate"`                            // Percentage of individual attempts that succeeded
	UserFacingSuccessRate     float64 `json:"user_facing_success_rate"`                // Percentage of user requests that ultimately succeeded (fallback chains counted as one request)
	UserFacingTotalRequests   int64   `json:"user_facing_total_requests"`              // Count of root requests (fallback_index = 0) used as denominator for UserFacingSuccessRate
	AverageLatency            float64 `json:"average_latency"`                         // Average latency in milliseconds
	TotalTokens               int64   `json:"total_tokens"`                            // Total tokens used
	PromptTokens              int64   `json:"prompt_tokens"`                           // Input tokens used
	CompletionTokens          int64   `json:"completion_tokens"`                       // Output tokens used
	TotalCost                 float64 `json:"total_cost"`                              // Total cost in dollars
	CacheHitRateTotalRequests *int64  `json:"cache_hit_rate_total_requests,omitempty"` // Completed requests used as local-cache hit-rate denominator
	DirectCacheHits           *int64  `json:"direct_cache_hits,omitempty"`             // Number of direct (exact) semantic cache hits
	SemanticCacheHits         *int64  `json:"semantic_cache_hits,omitempty"`           // Number of semantic (fuzzy) cache hits
}

// Log represents a complete log entry for a request/response cycle
// This is the GORM model with appropriate tags
type Log struct {
	ID                      string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	IncNumber               *int64    `gorm:"column:inc_number" json:"inc_number,omitempty"`
	ParentRequestID         *string   `gorm:"type:varchar(255);index" json:"parent_request_id"`
	Timestamp               time.Time `gorm:"index;index:idx_logs_ts_provider_status,priority:1;not null" json:"timestamp"`
	Object                  string    `gorm:"type:varchar(255);index;not null;column:object_type" json:"object"` // text.completion, chat.completion, or embedding
	Provider                string    `gorm:"type:varchar(255);index;index:idx_logs_ts_provider_status,priority:2;not null" json:"provider"`
	Model                   string    `gorm:"type:varchar(255);index;not null" json:"model"`
	Alias                   *string   `gorm:"type:varchar(255);index" json:"alias,omitempty"`          // Set when model was resolved via alias mapping; the original name the caller used
	CanonicalModelName      *string   `gorm:"type:varchar(255)" json:"canonical_model_name,omitempty"` // Canonical model name configured on the resolved alias, when set
	AliasModelFamily        *string   `gorm:"type:varchar(255)" json:"alias_model_family,omitempty"`   // Model family configured on the resolved alias, when set
	ServerSideFallbackModel *string   `gorm:"type:varchar(255)" json:"server_side_fallback_model,omitempty"`
	NumberOfRetries         int       `gorm:"default:0" json:"number_of_retries"`
	FallbackIndex           int       `gorm:"default:0" json:"fallback_index"`
	SelectedKeyID           string    `gorm:"type:varchar(255);index:idx_logs_selected_key_id" json:"selected_key_id"`
	SelectedKeyName         string    `gorm:"type:varchar(255)" json:"selected_key_name"`
	AttemptTrail            string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.KeyAttemptRecord
	VirtualKeyID            *string   `gorm:"type:varchar(255);index:idx_logs_virtual_key_id" json:"virtual_key_id"`
	VirtualKeyName          *string   `gorm:"type:varchar(255)" json:"virtual_key_name"`
	RoutingEnginesUsedStr   *string   `gorm:"type:varchar(255);column:routing_engines_used" json:"-"` // Comma-separated routing engines
	RoutingRuleID           *string   `gorm:"type:varchar(255);index:idx_logs_routing_rule_id" json:"routing_rule_id"`
	RoutingRuleName         *string   `gorm:"type:varchar(255)" json:"routing_rule_name"`
	SelectedPromptName      *string   `gorm:"type:varchar(255)" json:"selected_prompt_name"`
	SelectedPromptVersion   *string   `gorm:"type:varchar(64)" json:"selected_prompt_version"`
	SelectedPromptID        *string   `gorm:"type:varchar(36)" json:"selected_prompt_id"`
	UserID                  *string   `gorm:"type:varchar(255);index:idx_logs_user_id" json:"user_id"`
	UserName                *string   `gorm:"type:varchar(255)" json:"user_name"`
	TeamID                  *string   `gorm:"type:varchar(255);index:idx_logs_team_id" json:"team_id"`
	TeamName                *string   `gorm:"type:varchar(255)" json:"team_name"`
	CustomerID              *string   `gorm:"type:varchar(255);index:idx_logs_customer_id" json:"customer_id"`
	CustomerName            *string   `gorm:"type:varchar(255)" json:"customer_name"`
	BusinessUnitID          *string   `gorm:"type:varchar(255);index:idx_logs_business_unit_id" json:"business_unit_id"`
	BusinessUnitName        *string   `gorm:"type:varchar(255)" json:"business_unit_name"`
	TeamIDs                 *string   `gorm:"type:text" json:"-"`
	TeamNames               *string   `gorm:"type:text" json:"-"`
	CustomerIDs             *string   `gorm:"type:text" json:"-"`
	CustomerNames           *string   `gorm:"type:text" json:"-"`
	BusinessUnitIDs         *string   `gorm:"type:text" json:"-"`
	BusinessUnitNames       *string   `gorm:"type:text" json:"-"`
	InputHistory            string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.ChatMessage
	ResponsesInputHistory   string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.ResponsesMessage
	OutputMessage           string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.ChatMessage
	ResponsesOutput         string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.ResponsesMessage
	EmbeddingOutput         string    `gorm:"type:text" json:"-"` // JSON serialized [][]float32
	RerankOutput            string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.RerankResult
	OCROutput               string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostOCRResponse
	Params                  string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.ModelParameters
	Tools                   string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.Tool
	ToolCalls               string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.ToolCall (For backward compatibility, tool calls are now in the content)
	SpeechInput             string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.SpeechInput
	TranscriptionInput      string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.TranscriptionInput
	OCRInput                string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.OCRDocument
	ImageGenerationInput    string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.ImageGenerationInput
	ImageEditInput          string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.ImageEditInput
	ImageVariationInput     string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.ImageVariationInput
	VideoGenerationInput    string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.VideoGenerationInput
	SpeechOutput            string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostSpeech
	TranscriptionOutput     string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostTranscribe
	ImageGenerationOutput   string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostImageGenerationResponse
	ListModelsOutput        string    `gorm:"type:text" json:"-"` // JSON serialized []schemas.Model
	VideoGenerationOutput   string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostVideoGenerationResponse
	VideoRetrieveOutput     string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostVideoRetrieveResponse
	VideoDownloadOutput     string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostVideoDownloadResponse
	VideoListOutput         string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostVideoListResponse
	VideoDeleteOutput       string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostVideoDeleteResponse
	CacheDebug              string    `gorm:"type:text" json:"-"` // JSON serialized *schemas.BifrostCacheDebug
	Latency                 *float64  `gorm:"index:idx_logs_latency" json:"latency,omitempty"`
	TokenUsage              string    `gorm:"type:text" json:"-"`                                                                         // JSON serialized *schemas.LLMUsage
	Cost                    *float64  `gorm:"index" json:"cost,omitempty"`                                                                // Cost in dollars (total cost of the request - includes cache lookup cost)
	Status                  string    `gorm:"type:varchar(50);index;index:idx_logs_ts_provider_status,priority:3;not null" json:"status"` // "processing", "success", or "error"
	StopReason              *string   `gorm:"type:varchar(50);index:idx_logs_stop_reason" json:"stop_reason,omitempty"`                   // Why the model stopped: "stop", "length", "content_filter", "tool_calls", etc.
	ErrorDetails            string    `gorm:"type:text" json:"-"`                                                                         // JSON serialized *schemas.BifrostError
	Stream                  bool      `gorm:"default:false" json:"stream"`                                                                // true if this was a streaming response
	ContentSummary          string    `gorm:"type:text" json:"content_summary,omitempty"`                                                 // Last user message preview; UI log-list display fallback when payload fields are offloaded to object storage
	RawRequest              string    `gorm:"type:text" json:"raw_request"`                                                               // Populated when `send-back-raw-request` is on
	RawResponse             string    `gorm:"type:text" json:"raw_response"`                                                              // Populated when `send-back-raw-response` is on
	PassthroughRequestBody  string    `gorm:"type:text" json:"passthrough_request_body,omitempty"`                                        // Raw body for passthrough requests (UTF-8)
	PassthroughResponseBody string    `gorm:"type:text" json:"passthrough_response_body,omitempty"`                                       // Raw body for passthrough responses (UTF-8)
	RoutingEngineLogs       string    `gorm:"type:text" json:"routing_engine_logs,omitempty"`                                             // Formatted routing engine decision logs
	PluginLogs              string    `gorm:"type:text" json:"plugin_logs,omitempty"`                                                     // JSON serialized plugin log entries grouped by plugin name
	Metadata                *string   `gorm:"type:text" json:"-"`                                                                         // JSON serialized map[string]interface{}
	IsLargePayloadRequest   bool      `gorm:"default:false" json:"is_large_payload_request"`
	IsLargePayloadResponse  bool      `gorm:"default:false" json:"is_large_payload_response"`
	HasObject               bool      `gorm:"default:false" json:"-"`              // True when payload is stored in object storage
	ContentHidden           bool      `gorm:"default:false" json:"content_hidden"` // True when the payload is retained in object storage but must never be served back through the API/UI

	RedactionData          *schemas.RedactionData        `gorm:"-" json:"-"`                           // Transient guardrail redaction data consumed by enterprise logstore wrappers
	RedactionMapping       string                        `gorm:"type:text" json:"-"`                   // Reversible redaction mapping (encrypted when an encryption key is set), written by enterprise logstore wrappers; deleted with the row
	RevealRedactionMapping *schemas.RedactionMapsByPhase `gorm:"-" json:"redaction_mapping,omitempty"` // Virtual field populated only on permitted log-detail reads

	// Cluster governance fields - attached by the logging plugin when running in a cluster
	// so that leaders can recover disconnected node usage from the logs table.
	ClusterNodeID *string `gorm:"type:varchar(255)" json:"cluster_node_id,omitempty"`
	BudgetIDs     *string `gorm:"type:text" json:"-"` // JSON serialized []string of budget IDs applicable to this request
	RateLimitIDs  *string `gorm:"type:text" json:"-"` // JSON serialized []string of rate limit IDs applicable to this request

	// Denormalized token fields for easier querying
	PromptTokens     int `gorm:"default:0" json:"-"`
	CompletionTokens int `gorm:"default:0" json:"-"`
	TotalTokens      int `gorm:"index:idx_logs_total_tokens;default:0" json:"-"`
	CachedReadTokens int `gorm:"default:0" json:"-"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`

	// Virtual fields for JSON output - these will be populated when needed
	RoutingEnginesUsed          []string                                `gorm:"-" json:"routing_engines_used,omitempty"` // Virtual field deserialized from JSON
	InputHistoryParsed          []schemas.ChatMessage                   `gorm:"-" json:"input_history,omitempty"`
	ResponsesInputHistoryParsed []schemas.ResponsesMessage              `gorm:"-" json:"responses_input_history,omitempty"`
	OutputMessageParsed         *schemas.ChatMessage                    `gorm:"-" json:"output_message,omitempty"`
	ResponsesOutputParsed       []schemas.ResponsesMessage              `gorm:"-" json:"responses_output,omitempty"`
	EmbeddingOutputParsed       []schemas.EmbeddingData                 `gorm:"-" json:"embedding_output,omitempty"`
	RerankOutputParsed          []schemas.RerankResult                  `gorm:"-" json:"rerank_output,omitempty"`
	OCROutputParsed             *schemas.BifrostOCRResponse             `gorm:"-" json:"ocr_output,omitempty"`
	ParamsParsed                interface{}                             `gorm:"-" json:"params,omitempty"`
	ToolsParsed                 []schemas.ChatTool                      `gorm:"-" json:"tools,omitempty"`
	ToolCallsParsed             []schemas.ChatAssistantMessageToolCall  `gorm:"-" json:"tool_calls,omitempty"` // For backward compatibility, tool calls are now in the content
	TokenUsageParsed            *schemas.BifrostLLMUsage                `gorm:"-" json:"token_usage,omitempty"`
	ErrorDetailsParsed          *schemas.BifrostError                   `gorm:"-" json:"error_details,omitempty"`
	SpeechInputParsed           *schemas.SpeechInput                    `gorm:"-" json:"speech_input,omitempty"`
	TranscriptionInputParsed    *schemas.TranscriptionInput             `gorm:"-" json:"transcription_input,omitempty"`
	OCRInputParsed              *schemas.OCRDocument                    `gorm:"-" json:"ocr_input,omitempty"`
	ImageGenerationInputParsed  *schemas.ImageGenerationInput           `gorm:"-" json:"image_generation_input,omitempty"`
	ImageEditInputParsed        *schemas.ImageEditInput                 `gorm:"-" json:"image_edit_input,omitempty"`
	ImageVariationInputParsed   *schemas.ImageVariationInput            `gorm:"-" json:"image_variation_input,omitempty"`
	SpeechOutputParsed          *schemas.BifrostSpeechResponse          `gorm:"-" json:"speech_output,omitempty"`
	TranscriptionOutputParsed   *schemas.BifrostTranscriptionResponse   `gorm:"-" json:"transcription_output,omitempty"`
	ImageGenerationOutputParsed *schemas.BifrostImageGenerationResponse `gorm:"-" json:"image_generation_output,omitempty"`
	CacheDebugParsed            *schemas.BifrostCacheDebug              `gorm:"-" json:"cache_debug,omitempty"`
	ListModelsOutputParsed      []schemas.Model                         `gorm:"-" json:"list_models_output,omitempty"`
	MetadataParsed              map[string]interface{}                  `gorm:"-" json:"metadata,omitempty"`
	VideoGenerationInputParsed  *schemas.VideoGenerationInput           `gorm:"-" json:"video_generation_input,omitempty"`
	VideoGenerationOutputParsed *schemas.BifrostVideoGenerationResponse `gorm:"-" json:"video_generation_output,omitempty"`
	VideoRetrieveOutputParsed   *schemas.BifrostVideoGenerationResponse `gorm:"-" json:"video_retrieve_output,omitempty"`
	VideoDownloadOutputParsed   *schemas.BifrostVideoDownloadResponse   `gorm:"-" json:"video_download_output,omitempty"`
	VideoListOutputParsed       *schemas.BifrostVideoListResponse       `gorm:"-" json:"video_list_output,omitempty"`
	VideoDeleteOutputParsed     *schemas.BifrostVideoDeleteResponse     `gorm:"-" json:"video_delete_output,omitempty"`
	AttemptTrailParsed          []schemas.KeyAttemptRecord              `gorm:"-" json:"attempt_trail,omitempty"`
	BudgetIDsParsed             []string                                `gorm:"-" json:"budget_ids,omitempty"`
	RateLimitIDsParsed          []string                                `gorm:"-" json:"rate_limit_ids,omitempty"`
	TeamIDsParsed               []string                                `gorm:"-" json:"team_ids,omitempty"`
	TeamNamesParsed             []string                                `gorm:"-" json:"team_names,omitempty"`
	CustomerIDsParsed           []string                                `gorm:"-" json:"customer_ids,omitempty"`
	CustomerNamesParsed         []string                                `gorm:"-" json:"customer_names,omitempty"`
	BusinessUnitIDsParsed       []string                                `gorm:"-" json:"business_unit_ids,omitempty"`
	BusinessUnitNamesParsed     []string                                `gorm:"-" json:"business_unit_names,omitempty"`

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
		if data, err := sonic.Marshal(l.InputHistoryParsed); err != nil {
			return err
		} else {
			l.InputHistory = sanitizeJSONForJSONB(string(data))
		}
	}

	if l.ResponsesInputHistoryParsed != nil {
		if data, err := sonic.Marshal(l.ResponsesInputHistoryParsed); err != nil {
			return err
		} else {
			l.ResponsesInputHistory = sanitizeJSONForJSONB(string(data))
		}
	}

	if l.OutputMessageParsed != nil {
		if data, err := sonic.Marshal(l.OutputMessageParsed); err != nil {
			return err
		} else {
			l.OutputMessage = string(data)
		}
	}

	if l.ResponsesOutputParsed != nil {
		if data, err := sonic.Marshal(l.ResponsesOutputParsed); err != nil {
			return err
		} else {
			l.ResponsesOutput = string(data)
		}
	}

	if l.EmbeddingOutputParsed != nil {
		if data, err := sonic.Marshal(l.EmbeddingOutputParsed); err != nil {
			return err
		} else {
			l.EmbeddingOutput = string(data)
		}
	}

	if l.RerankOutputParsed != nil {
		if data, err := sonic.Marshal(l.RerankOutputParsed); err != nil {
			return err
		} else {
			l.RerankOutput = string(data)
		}
	}

	if l.OCROutputParsed != nil {
		if data, err := sonic.Marshal(l.OCROutputParsed); err != nil {
			return err
		} else {
			l.OCROutput = string(data)
		}
	}

	if l.SpeechInputParsed != nil {
		if data, err := sonic.Marshal(l.SpeechInputParsed); err != nil {
			return err
		} else {
			l.SpeechInput = string(data)
		}
	}

	if l.TranscriptionInputParsed != nil {
		if data, err := sonic.Marshal(l.TranscriptionInputParsed); err != nil {
			return err
		} else {
			l.TranscriptionInput = string(data)
		}
	}

	if l.OCRInputParsed != nil {
		if data, err := sonic.Marshal(l.OCRInputParsed); err != nil {
			return err
		} else {
			l.OCRInput = string(data)
		}
	}

	if l.ImageGenerationInputParsed != nil {
		if data, err := sonic.Marshal(l.ImageGenerationInputParsed); err != nil {
			return err
		} else {
			l.ImageGenerationInput = string(data)
		}
	}

	if l.ImageEditInputParsed != nil {
		if data, err := sonic.Marshal(l.ImageEditInputParsed); err != nil {
			return err
		} else {
			l.ImageEditInput = string(data)
		}
	}

	if l.ImageVariationInputParsed != nil {
		if data, err := sonic.Marshal(l.ImageVariationInputParsed); err != nil {
			return err
		} else {
			l.ImageVariationInput = string(data)
		}
	}

	if l.VideoGenerationInputParsed != nil {
		if data, err := sonic.Marshal(l.VideoGenerationInputParsed); err != nil {
			return err
		} else {
			l.VideoGenerationInput = string(data)
		}
	}

	if l.SpeechOutputParsed != nil {
		if data, err := sonic.Marshal(l.SpeechOutputParsed); err != nil {
			return err
		} else {
			l.SpeechOutput = string(data)
		}
	}

	if l.TranscriptionOutputParsed != nil {
		if data, err := sonic.Marshal(l.TranscriptionOutputParsed); err != nil {
			return err
		} else {
			l.TranscriptionOutput = string(data)
		}
	}

	if l.ImageGenerationOutputParsed != nil {
		if data, err := sonic.Marshal(l.ImageGenerationOutputParsed); err != nil {
			return err
		} else {
			l.ImageGenerationOutput = string(data)
		}
	}

	if l.VideoGenerationOutputParsed != nil {
		if data, err := sonic.Marshal(l.VideoGenerationOutputParsed); err != nil {
			return err
		} else {
			l.VideoGenerationOutput = string(data)
		}
	}

	if l.VideoRetrieveOutputParsed != nil {
		if data, err := sonic.Marshal(l.VideoRetrieveOutputParsed); err != nil {
			return err
		} else {
			l.VideoRetrieveOutput = string(data)
		}
	}

	if l.VideoDownloadOutputParsed != nil {
		if data, err := sonic.Marshal(l.VideoDownloadOutputParsed); err != nil {
			return err
		} else {
			l.VideoDownloadOutput = string(data)
		}
	}

	if l.VideoListOutputParsed != nil {
		if data, err := sonic.Marshal(l.VideoListOutputParsed); err != nil {
			return err
		} else {
			l.VideoListOutput = string(data)
		}
	}

	if l.VideoDeleteOutputParsed != nil {
		if data, err := sonic.Marshal(l.VideoDeleteOutputParsed); err != nil {
			return err
		} else {
			l.VideoDeleteOutput = string(data)
		}
	}

	if l.ListModelsOutputParsed != nil {
		if data, err := sonic.Marshal(l.ListModelsOutputParsed); err != nil {
			return err
		} else {
			l.ListModelsOutput = string(data)
		}
	}

	if l.ParamsParsed != nil {
		if data, err := sonic.Marshal(l.ParamsParsed); err != nil {
			return err
		} else {
			l.Params = string(data)
		}
	}

	if l.ToolsParsed != nil {
		if data, err := sonic.Marshal(l.ToolsParsed); err != nil {
			return err
		} else {
			l.Tools = string(data)
		}
	}

	if l.ToolCallsParsed != nil {
		if data, err := sonic.Marshal(l.ToolCallsParsed); err != nil {
			return err
		} else {
			l.ToolCalls = string(data)
		}
	}

	if l.TokenUsageParsed != nil {
		if data, err := sonic.Marshal(l.TokenUsageParsed); err != nil {
			return err
		} else {
			l.TokenUsage = string(data)
		}
		// Update denormalized fields for easier querying
		l.PromptTokens = l.TokenUsageParsed.PromptTokens
		l.CompletionTokens = l.TokenUsageParsed.CompletionTokens
		l.TotalTokens = l.TokenUsageParsed.TotalTokens
		if l.TokenUsageParsed.PromptTokensDetails != nil {
			l.CachedReadTokens = l.TokenUsageParsed.PromptTokensDetails.CachedReadTokens
		}
	}

	if l.ErrorDetailsParsed != nil {
		if data, err := sonic.Marshal(l.ErrorDetailsParsed); err != nil {
			return err
		} else {
			l.ErrorDetails = string(data)
		}
	}

	if l.CacheDebugParsed != nil {
		if data, err := sonic.Marshal(l.CacheDebugParsed); err != nil {
			return err
		} else {
			l.CacheDebug = string(data)
		}
	}

	if len(l.AttemptTrailParsed) > 0 {
		if data, err := sonic.Marshal(l.AttemptTrailParsed); err != nil {
			return err
		} else {
			l.AttemptTrail = string(data)
		}
	} else {
		l.AttemptTrail = ""
	}

	if l.MetadataParsed != nil {
		data, err := sonic.Marshal(l.MetadataParsed)
		if err != nil {
			// Metadata is supplementary — null it out rather than aborting the log write.
			l.Metadata = nil
			l.MetadataParsed = nil
		} else {
			metadata := string(data)
			l.Metadata = &metadata
		}
	}

	if len(l.BudgetIDsParsed) > 0 {
		if data, err := sonic.Marshal(l.BudgetIDsParsed); err != nil {
			return err
		} else {
			budgetIDs := string(data)
			l.BudgetIDs = &budgetIDs
		}
	}

	if len(l.RateLimitIDsParsed) > 0 {
		if data, err := sonic.Marshal(l.RateLimitIDsParsed); err != nil {
			return err
		} else {
			rateLimitIDs := string(data)
			l.RateLimitIDs = &rateLimitIDs
		}
	}

	if len(l.TeamIDsParsed) > 0 {
		if data, err := sonic.Marshal(l.TeamIDsParsed); err != nil {
			return err
		} else {
			s := string(data)
			l.TeamIDs = &s
		}
	}
	if len(l.TeamNamesParsed) > 0 {
		if data, err := sonic.Marshal(l.TeamNamesParsed); err != nil {
			return err
		} else {
			s := string(data)
			l.TeamNames = &s
		}
	}
	if len(l.CustomerIDsParsed) > 0 {
		if data, err := sonic.Marshal(l.CustomerIDsParsed); err != nil {
			return err
		} else {
			s := string(data)
			l.CustomerIDs = &s
		}
	}
	if len(l.CustomerNamesParsed) > 0 {
		if data, err := sonic.Marshal(l.CustomerNamesParsed); err != nil {
			return err
		} else {
			s := string(data)
			l.CustomerNames = &s
		}
	}
	if len(l.BusinessUnitIDsParsed) > 0 {
		if data, err := sonic.Marshal(l.BusinessUnitIDsParsed); err != nil {
			return err
		} else {
			s := string(data)
			l.BusinessUnitIDs = &s
		}
	}
	if len(l.BusinessUnitNamesParsed) > 0 {
		if data, err := sonic.Marshal(l.BusinessUnitNamesParsed); err != nil {
			return err
		} else {
			s := string(data)
			l.BusinessUnitNames = &s
		}
	}

	// Build content summary for search.
	// Skip if already set (e.g., by the hybrid log store which builds input-only summaries).
	if l.ContentSummary == "" {
		l.ContentSummary = l.BuildContentSummary()
	}

	return nil
}

// DeserializeFields converts JSON strings back to Go structs
func (l *Log) DeserializeFields() error {
	if l.InputHistory != "" {
		if err := sonic.Unmarshal([]byte(l.InputHistory), &l.InputHistoryParsed); err != nil {
			// Log error but don't fail the operation - initialize as empty slice
			l.InputHistoryParsed = []schemas.ChatMessage{}
		}
	}

	if l.ResponsesInputHistory != "" {
		if err := sonic.Unmarshal([]byte(l.ResponsesInputHistory), &l.ResponsesInputHistoryParsed); err != nil {
			// Log error but don't fail the operation - initialize as empty slice
			l.ResponsesInputHistoryParsed = []schemas.ResponsesMessage{}
		}
	}

	if l.OutputMessage != "" {
		if err := sonic.Unmarshal([]byte(l.OutputMessage), &l.OutputMessageParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.OutputMessageParsed = nil
		}
	}

	if l.ResponsesOutput != "" {
		if err := sonic.Unmarshal([]byte(l.ResponsesOutput), &l.ResponsesOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ResponsesOutputParsed = []schemas.ResponsesMessage{}
		}
	}

	if l.EmbeddingOutput != "" {
		if err := sonic.Unmarshal([]byte(l.EmbeddingOutput), &l.EmbeddingOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.EmbeddingOutputParsed = nil
		}
	}

	if l.RerankOutput != "" {
		if err := sonic.Unmarshal([]byte(l.RerankOutput), &l.RerankOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.RerankOutputParsed = nil
		}
	}

	if l.OCROutput != "" {
		if err := sonic.Unmarshal([]byte(l.OCROutput), &l.OCROutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.OCROutputParsed = nil
		}
	}

	if l.Params != "" {
		if err := sonic.Unmarshal([]byte(l.Params), &l.ParamsParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ParamsParsed = nil
		}
	}

	if l.Tools != "" {
		if err := sonic.Unmarshal([]byte(l.Tools), &l.ToolsParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ToolsParsed = nil
		}
	}

	if l.ToolCalls != "" {
		if err := sonic.Unmarshal([]byte(l.ToolCalls), &l.ToolCallsParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ToolCallsParsed = nil
		}
	}

	if l.TokenUsage != "" {
		if err := sonic.Unmarshal([]byte(l.TokenUsage), &l.TokenUsageParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.TokenUsageParsed = nil
		}
	}

	if l.ErrorDetails != "" {
		if err := sonic.Unmarshal([]byte(l.ErrorDetails), &l.ErrorDetailsParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ErrorDetailsParsed = nil
		}
	}

	if l.VideoGenerationOutput != "" {
		if err := sonic.Unmarshal([]byte(l.VideoGenerationOutput), &l.VideoGenerationOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.VideoGenerationOutputParsed = nil
		}
	}

	if l.VideoRetrieveOutput != "" {
		if err := sonic.Unmarshal([]byte(l.VideoRetrieveOutput), &l.VideoRetrieveOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.VideoRetrieveOutputParsed = nil
		}
	}

	if l.VideoDownloadOutput != "" {
		if err := sonic.Unmarshal([]byte(l.VideoDownloadOutput), &l.VideoDownloadOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.VideoDownloadOutputParsed = nil
		}
	}

	if l.VideoListOutput != "" {
		if err := sonic.Unmarshal([]byte(l.VideoListOutput), &l.VideoListOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.VideoListOutputParsed = nil
		}
	}

	if l.VideoDeleteOutput != "" {
		if err := sonic.Unmarshal([]byte(l.VideoDeleteOutput), &l.VideoDeleteOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.VideoDeleteOutputParsed = nil
		}
	}

	if l.VideoGenerationInput != "" {
		if err := sonic.Unmarshal([]byte(l.VideoGenerationInput), &l.VideoGenerationInputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.VideoGenerationInputParsed = nil
		}
	}

	if l.ListModelsOutput != "" {
		if err := sonic.Unmarshal([]byte(l.ListModelsOutput), &l.ListModelsOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ListModelsOutputParsed = nil
		}
	}

	// Deserialize speech and transcription fields
	if l.SpeechInput != "" {
		if err := sonic.Unmarshal([]byte(l.SpeechInput), &l.SpeechInputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.SpeechInputParsed = nil
		}
	}

	if l.TranscriptionInput != "" {
		if err := sonic.Unmarshal([]byte(l.TranscriptionInput), &l.TranscriptionInputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.TranscriptionInputParsed = nil
		}
	}

	if l.OCRInput != "" {
		if err := sonic.Unmarshal([]byte(l.OCRInput), &l.OCRInputParsed); err != nil {
			l.OCRInputParsed = nil
		}
	}

	if l.ImageGenerationInput != "" {
		if err := sonic.Unmarshal([]byte(l.ImageGenerationInput), &l.ImageGenerationInputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ImageGenerationInputParsed = nil
		}
	}

	if l.ImageEditInput != "" {
		if err := sonic.Unmarshal([]byte(l.ImageEditInput), &l.ImageEditInputParsed); err != nil {
			l.ImageEditInputParsed = nil
		}
	}

	if l.ImageVariationInput != "" {
		if err := sonic.Unmarshal([]byte(l.ImageVariationInput), &l.ImageVariationInputParsed); err != nil {
			l.ImageVariationInputParsed = nil
		}
	}

	if l.SpeechOutput != "" {
		if err := sonic.Unmarshal([]byte(l.SpeechOutput), &l.SpeechOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.SpeechOutputParsed = nil
		}
	}

	if l.TranscriptionOutput != "" {
		if err := sonic.Unmarshal([]byte(l.TranscriptionOutput), &l.TranscriptionOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.TranscriptionOutputParsed = nil
		}
	}

	if l.ImageGenerationOutput != "" {
		if err := sonic.Unmarshal([]byte(l.ImageGenerationOutput), &l.ImageGenerationOutputParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.ImageGenerationOutputParsed = nil
		}
	}

	if l.CacheDebug != "" {
		if err := sonic.Unmarshal([]byte(l.CacheDebug), &l.CacheDebugParsed); err != nil {
			// Log error but don't fail the operation - initialize as nil
			l.CacheDebugParsed = nil
		}
	}

	if l.AttemptTrail != "" {
		if err := sonic.Unmarshal([]byte(l.AttemptTrail), &l.AttemptTrailParsed); err != nil {
			l.AttemptTrailParsed = nil
		}
	}

	if l.Metadata != nil && *l.Metadata != "" {
		if err := sonic.Unmarshal([]byte(*l.Metadata), &l.MetadataParsed); err != nil {
			l.MetadataParsed = nil
		}
	}

	if l.BudgetIDs != nil && *l.BudgetIDs != "" {
		if err := sonic.Unmarshal([]byte(*l.BudgetIDs), &l.BudgetIDsParsed); err != nil {
			l.BudgetIDsParsed = nil
		}
	}

	if l.RateLimitIDs != nil && *l.RateLimitIDs != "" {
		if err := sonic.Unmarshal([]byte(*l.RateLimitIDs), &l.RateLimitIDsParsed); err != nil {
			l.RateLimitIDsParsed = nil
		}
	}

	if l.TeamIDs != nil && *l.TeamIDs != "" {
		if err := sonic.Unmarshal([]byte(*l.TeamIDs), &l.TeamIDsParsed); err != nil {
			l.TeamIDsParsed = nil
		}
	}
	if l.TeamNames != nil && *l.TeamNames != "" {
		if err := sonic.Unmarshal([]byte(*l.TeamNames), &l.TeamNamesParsed); err != nil {
			l.TeamNamesParsed = nil
		}
	}
	if l.CustomerIDs != nil && *l.CustomerIDs != "" {
		if err := sonic.Unmarshal([]byte(*l.CustomerIDs), &l.CustomerIDsParsed); err != nil {
			l.CustomerIDsParsed = nil
		}
	}
	if l.CustomerNames != nil && *l.CustomerNames != "" {
		if err := sonic.Unmarshal([]byte(*l.CustomerNames), &l.CustomerNamesParsed); err != nil {
			l.CustomerNamesParsed = nil
		}
	}
	if l.BusinessUnitIDs != nil && *l.BusinessUnitIDs != "" {
		if err := sonic.Unmarshal([]byte(*l.BusinessUnitIDs), &l.BusinessUnitIDsParsed); err != nil {
			l.BusinessUnitIDsParsed = nil
		}
	}
	if l.BusinessUnitNames != nil && *l.BusinessUnitNames != "" {
		if err := sonic.Unmarshal([]byte(*l.BusinessUnitNames), &l.BusinessUnitNamesParsed); err != nil {
			l.BusinessUnitNamesParsed = nil
		}
	}

	if l.RoutingEnginesUsedStr != nil && *l.RoutingEnginesUsedStr != "" {
		// Parse comma-separated routing engines
		l.RoutingEnginesUsed = strings.Split(*l.RoutingEnginesUsedStr, ",")
	} else {
		l.RoutingEnginesUsed = []string{}
	}

	// Hybrid log store offloads token_usage to object storage but keeps denormalized
	// prompt/completion/total/cached columns in the DB for analytics. Rebuild the virtual
	// field so list APIs and the UI can render tokens without hydrating from S3 —
	// same role content_summary plays for message previews. Only the cached-read detail
	// is denormalized; richer details (e.g. completion_tokens_details) live solely in the
	// offloaded payload and are restored on detail reads that hydrate from object storage.
	if l.TokenUsage == "" && l.TokenUsageParsed == nil && (l.PromptTokens != 0 || l.CompletionTokens != 0 || l.TotalTokens != 0) {
		usage := &schemas.BifrostLLMUsage{
			PromptTokens:     l.PromptTokens,
			CompletionTokens: l.CompletionTokens,
			TotalTokens:      l.TotalTokens,
		}
		if l.CachedReadTokens != 0 {
			usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
				CachedReadTokens: l.CachedReadTokens,
			}
		}
		l.TokenUsageParsed = usage
	}

	return nil
}

// MCPToolLog represents a log entry for MCP tool executions
// This is separate from the main Log table since MCP tool calls have different fields
type MCPToolLog struct {
	ID             string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	RequestID      string    `gorm:"type:varchar(255);column:request_id;index:idx_mcp_logs_request_id" json:"request_id,omitempty"`             // The original request ID from context
	LLMRequestID   *string   `gorm:"type:varchar(255);column:llm_request_id;index:idx_mcp_logs_llm_request_id" json:"llm_request_id,omitempty"` // Links to the LLM request that triggered this tool call
	Timestamp      time.Time `gorm:"index;not null" json:"timestamp"`
	ToolName       string    `gorm:"type:varchar(255);index:idx_mcp_logs_tool_name;not null" json:"tool_name"`
	ServerLabel    string    `gorm:"type:varchar(255);index:idx_mcp_logs_server_label" json:"server_label,omitempty"` // MCP server that provided the tool
	VirtualKeyID   *string   `gorm:"type:varchar(255);index:idx_mcp_logs_virtual_key_id" json:"virtual_key_id"`
	VirtualKeyName *string   `gorm:"type:varchar(255)" json:"virtual_key_name"`
	UserID         *string   `gorm:"type:varchar(255);index:idx_mcp_logs_user_id" json:"user_id"`
	TeamID         *string   `gorm:"type:varchar(255);index:idx_mcp_logs_team_id" json:"team_id"`
	CustomerID     *string   `gorm:"type:varchar(255);index:idx_mcp_logs_customer_id" json:"customer_id"`
	BusinessUnitID *string   `gorm:"type:varchar(255);index:idx_mcp_logs_business_unit_id" json:"business_unit_id"`
	Arguments      string    `gorm:"type:text" json:"-"`                                                // JSON serialized tool arguments
	Result         string    `gorm:"type:text" json:"-"`                                                // JSON serialized tool result
	ErrorDetails   string    `gorm:"type:text" json:"-"`                                                // JSON serialized *schemas.BifrostError
	Latency        *float64  `gorm:"index:idx_mcp_logs_latency" json:"latency,omitempty"`               // Execution time in milliseconds
	Cost           *float64  `gorm:"index:idx_mcp_logs_cost" json:"cost,omitempty"`                     // Cost in dollars (per execution cost)
	Status         string    `gorm:"type:varchar(50);index:idx_mcp_logs_status;not null" json:"status"` // "processing", "success", or "error"
	Metadata       string    `gorm:"type:text" json:"-"`                                                // JSON serialized map[string]interface{}
	HasObject      bool      `gorm:"default:false" json:"-"`                                            // True when payload is stored in object storage
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

// AfterFind GORM hook to deserialize JSON fields
func (l *MCPToolLog) AfterFind(tx *gorm.DB) error {
	return l.DeserializeFields()
}

// SerializeFields converts Go structs to JSON strings for storage
func (l *MCPToolLog) SerializeFields() error {
	if l.ArgumentsParsed != nil {
		if data, err := sonic.Marshal(l.ArgumentsParsed); err != nil {
			return err
		} else {
			l.Arguments = string(data)
		}
	}

	if l.ResultParsed != nil {
		if data, err := sonic.Marshal(l.ResultParsed); err != nil {
			return err
		} else {
			l.Result = string(data)
		}
	}

	if l.ErrorDetailsParsed != nil {
		if data, err := sonic.Marshal(l.ErrorDetailsParsed); err != nil {
			return err
		} else {
			l.ErrorDetails = string(data)
		}
	}

	if l.MetadataParsed != nil {
		data, err := sonic.Marshal(l.MetadataParsed)
		if err != nil {
			// Metadata is supplementary — null it out rather than aborting the log write.
			l.Metadata = ""
			l.MetadataParsed = nil
		} else {
			l.Metadata = string(data)
		}
	}

	return nil
}

// DeserializeFields converts JSON strings back to Go structs
func (l *MCPToolLog) DeserializeFields() error {
	if l.Arguments != "" {
		if err := sonic.Unmarshal([]byte(l.Arguments), &l.ArgumentsParsed); err != nil {
			l.ArgumentsParsed = nil
		}
	}

	if l.Result != "" {
		if err := sonic.Unmarshal([]byte(l.Result), &l.ResultParsed); err != nil {
			l.ResultParsed = nil
		}
	}

	if l.ErrorDetails != "" {
		if err := sonic.Unmarshal([]byte(l.ErrorDetails), &l.ErrorDetailsParsed); err != nil {
			l.ErrorDetailsParsed = nil
		}
	}

	if l.Metadata != "" {
		if err := sonic.Unmarshal([]byte(l.Metadata), &l.MetadataParsed); err != nil {
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
	RequestID    string                 `gorm:"type:varchar(255)" json:"request_id,omitempty"`
	RequestType  schemas.RequestType    `gorm:"type:varchar(50);index:idx_async_jobs_request_type;not null" json:"request_type"`
	Response     string                 `gorm:"type:text" json:"response"`
	StatusCode   int                    `gorm:"default:0" json:"status_code,omitempty"`
	Error        string                 `gorm:"type:text" json:"error,omitempty"`
	VirtualKeyID *string                `gorm:"type:varchar(255);index:idx_async_jobs_vk_id" json:"virtual_key_id,omitempty"`
	// WebhookEndpointID references the webhook endpoint to notify when this
	// job reaches a terminal state, when one was requested at submit time.
	WebhookEndpointID *string    `gorm:"type:varchar(36)" json:"webhook_endpoint_id,omitempty"`
	ResultTTL         int        `gorm:"default:3600" json:"-"` // TTL in seconds, used to calculate ExpiresAt on completion
	ExpiresAt         *time.Time `gorm:"index:idx_async_jobs_expires_at" json:"expires_at,omitempty"`
	CreatedAt         time.Time  `gorm:"index;not null" json:"created_at"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
}

// TableName sets the table name for GORM
func (AsyncJob) TableName() string {
	return "async_jobs"
}

// ToResponse converts an AsyncJob database record to an AsyncJobResponse for JSON output.
func (j *AsyncJob) ToResponse() *schemas.AsyncJobResponse {
	resp := &schemas.AsyncJobResponse{
		ID:          j.ID,
		RequestID:   j.RequestID,
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

// WebhookDeliveryOutcome classifies the result of one webhook delivery attempt.
type WebhookDeliveryOutcome string

const (
	// WebhookDeliveryOutcomeDelivered means the receiver acknowledged with a 2xx.
	WebhookDeliveryOutcomeDelivered WebhookDeliveryOutcome = "delivered"
	// WebhookDeliveryOutcomeRetryableFailure means the attempt failed but the
	// delivery has retries left.
	WebhookDeliveryOutcomeRetryableFailure WebhookDeliveryOutcome = "retryable_failure"
	// WebhookDeliveryOutcomePermanentFailure means the receiver rejected the
	// delivery with a non-retryable status; no further attempts are made.
	WebhookDeliveryOutcomePermanentFailure WebhookDeliveryOutcome = "permanent_failure"
	// WebhookDeliveryOutcomeExhausted means the final allowed attempt failed.
	WebhookDeliveryOutcomeExhausted WebhookDeliveryOutcome = "exhausted"
)

// WebhookDelivery records one webhook delivery attempt. Rows are insert-only
// — every attempt appends a new record and existing rows are never updated —
// and carry delivery metadata only, never event payloads or receiver
// response bodies. WebhookID is the delivery's `webhook-id` wire header,
// shared by every attempt of the same delivery.
type WebhookDelivery struct {
	ID         string `gorm:"primaryKey;type:varchar(36)" json:"id"`
	WebhookID  string `gorm:"type:varchar(36);index:idx_webhook_deliveries_webhook_id;not null" json:"webhook_id"`
	EndpointID string `gorm:"type:varchar(36);index:idx_webhook_deliveries_endpoint_id;not null" json:"endpoint_id"`
	AsyncJobID string `gorm:"type:varchar(255);not null" json:"async_job_id"`
	// RequestID is the async job's inference request id — the same id the
	// LLM logs record — copied here at attempt time. Empty when the job row
	// expired before the attempt.
	RequestID  string                 `gorm:"type:varchar(255)" json:"request_id,omitempty"`
	Event      tables.WebhookEvent    `gorm:"type:varchar(255);not null" json:"event"`
	AttemptNo  int                    `gorm:"not null;default:0" json:"attempt_no"`
	Outcome    WebhookDeliveryOutcome `gorm:"type:varchar(50);not null" json:"outcome"`
	StatusCode int                    `gorm:"default:0" json:"status_code,omitempty"`
	Error      string                 `gorm:"type:text" json:"error,omitempty"`
	CreatedAt  time.Time              `gorm:"index:idx_webhook_deliveries_created_at;not null" json:"created_at"`
	ExpiresAt  *time.Time             `gorm:"index:idx_webhook_deliveries_expires_at" json:"expires_at,omitempty"`
}

// TableName sets the table name for GORM
func (WebhookDelivery) TableName() string {
	return "webhook_deliveries"
}

// WebhookDeliverySearchResult represents one page of webhook delivery history.
type WebhookDeliverySearchResult struct {
	Deliveries []WebhookDelivery `json:"deliveries"`
	Pagination PaginationOptions `json:"pagination"`
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

	// Add OCR output content
	if l.OCROutputParsed != nil {
		for _, page := range l.OCROutputParsed.Pages {
			if page.Markdown != "" {
				parts = append(parts, page.Markdown)
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

	// Add image edit input prompt
	if l.ImageEditInputParsed != nil && l.ImageEditInputParsed.Prompt != "" {
		parts = append(parts, l.ImageEditInputParsed.Prompt)
	}

	// Add video generation input prompt
	if l.VideoGenerationInputParsed != nil && l.VideoGenerationInputParsed.Prompt != "" {
		parts = append(parts, l.VideoGenerationInputParsed.Prompt)
	}

	// Add error details
	if l.ErrorDetailsParsed != nil && l.ErrorDetailsParsed.Error != nil && l.ErrorDetailsParsed.Error.Message != "" {
		parts = append(parts, l.ErrorDetailsParsed.Error.Message)
	}

	return strings.Join(parts, " ")
}

// KeyPairResult represents an ID-Name pair returned from DISTINCT queries
type KeyPairResult struct {
	ID   string `gorm:"column:id"`
	Name string `gorm:"column:name"`
}

// HistogramBucket represents a single time bucket in the histogram
type HistogramBucket struct {
	Timestamp time.Time `json:"timestamp"`
	Count     int64     `json:"count"`
	Success   int64     `json:"success"`
	Error     int64     `json:"error"`
	Cancelled int64     `json:"cancelled"`
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
	CachedReadTokens int64     `json:"cached_read_tokens"`
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
	Total     int64 `json:"total"`
	Success   int64 `json:"success"`
	Error     int64 `json:"error"`
	Cancelled int64 `json:"cancelled"`
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

// LatencyHistogramBucket represents a single time bucket for latency data
type LatencyHistogramBucket struct {
	Timestamp     time.Time `json:"timestamp"`
	AvgLatency    float64   `json:"avg_latency"`
	P90Latency    float64   `json:"p90_latency"`
	P95Latency    float64   `json:"p95_latency"`
	P99Latency    float64   `json:"p99_latency"`
	TotalRequests int64     `json:"total_requests"`
}

// LatencyHistogramResult represents the latency histogram query result
type LatencyHistogramResult struct {
	Buckets           []LatencyHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                    `json:"bucket_size_seconds"`
}

// Provider-level histogram types

// ProviderCostHistogramBucket represents a single time bucket for provider cost data
type ProviderCostHistogramBucket struct {
	Timestamp  time.Time          `json:"timestamp"`
	TotalCost  float64            `json:"total_cost"`
	ByProvider map[string]float64 `json:"by_provider"`
}

// ProviderCostHistogramResult represents the provider cost histogram query result
type ProviderCostHistogramResult struct {
	Buckets           []ProviderCostHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                         `json:"bucket_size_seconds"`
	Providers         []string                      `json:"providers"`
}

// ProviderTokenStats represents token statistics for a single provider
type ProviderTokenStats struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// ProviderTokenHistogramBucket represents a single time bucket for provider token data
type ProviderTokenHistogramBucket struct {
	Timestamp  time.Time                     `json:"timestamp"`
	ByProvider map[string]ProviderTokenStats `json:"by_provider"`
}

// ProviderTokenHistogramResult represents the provider token histogram query result
type ProviderTokenHistogramResult struct {
	Buckets           []ProviderTokenHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                          `json:"bucket_size_seconds"`
	Providers         []string                       `json:"providers"`
}

// ProviderLatencyStats represents latency statistics for a single provider
type ProviderLatencyStats struct {
	AvgLatency    float64 `json:"avg_latency"`
	P90Latency    float64 `json:"p90_latency"`
	P95Latency    float64 `json:"p95_latency"`
	P99Latency    float64 `json:"p99_latency"`
	TotalRequests int64   `json:"total_requests"`
}

// ProviderLatencyHistogramBucket represents a single time bucket for provider latency data
type ProviderLatencyHistogramBucket struct {
	Timestamp  time.Time                       `json:"timestamp"`
	ByProvider map[string]ProviderLatencyStats `json:"by_provider"`
}

// ProviderLatencyHistogramResult represents the provider latency histogram query result
type ProviderLatencyHistogramResult struct {
	Buckets           []ProviderLatencyHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                            `json:"bucket_size_seconds"`
	Providers         []string                         `json:"providers"`
}

// Throughput (tokens/sec) histogram types
//
// TokensPerSecond is an aggregate rate for the bucket: total completion tokens
// divided by total generation latency in seconds (SUM(completion_tokens) /
// (SUM(latency_ms)/1000)), computed over terminal rows with latency > 0. It is
// the "overall" throughput definition (not a mean of per-request rates), which
// keeps it uniformly computable across streaming and non-streaming requests.

// ThroughputHistogramBucket represents a single time bucket for token-generation throughput
type ThroughputHistogramBucket struct {
	Timestamp             time.Time `json:"timestamp"`
	TokensPerSecond       float64   `json:"tokens_per_second"`
	TotalCompletionTokens int64     `json:"total_completion_tokens"`
	TotalRequests         int64     `json:"total_requests"`
}

// ThroughputHistogramResult represents the throughput histogram query result
type ThroughputHistogramResult struct {
	Buckets           []ThroughputHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                       `json:"bucket_size_seconds"`
}

// ProviderThroughputStats represents throughput statistics for a single provider
type ProviderThroughputStats struct {
	TokensPerSecond       float64 `json:"tokens_per_second"`
	TotalCompletionTokens int64   `json:"total_completion_tokens"`
	TotalRequests         int64   `json:"total_requests"`
}

// ProviderThroughputHistogramBucket represents a single time bucket for provider throughput data
type ProviderThroughputHistogramBucket struct {
	Timestamp  time.Time                          `json:"timestamp"`
	ByProvider map[string]ProviderThroughputStats `json:"by_provider"`
}

// ProviderThroughputHistogramResult represents the provider throughput histogram query result
type ProviderThroughputHistogramResult struct {
	Buckets           []ProviderThroughputHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                               `json:"bucket_size_seconds"`
	Providers         []string                            `json:"providers"`
}

// HistogramDimension represents a column that can be used as a grouping dimension in histograms
type HistogramDimension string

const (
	DimensionProvider     HistogramDimension = "provider"
	DimensionTeam         HistogramDimension = "team_id"
	DimensionCustomer     HistogramDimension = "customer_id"
	DimensionUser         HistogramDimension = "user_id"
	DimensionBusinessUnit HistogramDimension = "business_unit_id"
)

// ValidHistogramDimensions is the set of allowed dimension values
var ValidHistogramDimensions = map[HistogramDimension]bool{
	DimensionProvider:     true,
	DimensionTeam:         true,
	DimensionCustomer:     true,
	DimensionUser:         true,
	DimensionBusinessUnit: true,
}

// histogramDimensionColumn maps a validated dimension to its SQL column name.
// Query builders must use the returned literal, never string(dimension): the
// dimension value originates from a request query parameter, and returning a
// compile-time constant here is what keeps user input out of SQL text.
func histogramDimensionColumn(dimension HistogramDimension) (string, bool) {
	switch dimension {
	case DimensionProvider:
		return "provider", true
	case DimensionTeam:
		return "team_id", true
	case DimensionCustomer:
		return "customer_id", true
	case DimensionUser:
		return "user_id", true
	case DimensionBusinessUnit:
		return "business_unit_id", true
	}
	return "", false
}

// Dimension-level histogram types (generic version of Provider histograms)

// DimensionCostHistogramBucket represents a single time bucket for dimension-grouped cost data
type DimensionCostHistogramBucket struct {
	Timestamp   time.Time          `json:"timestamp"`
	TotalCost   float64            `json:"total_cost"`
	ByDimension map[string]float64 `json:"by_dimension"`
}

// DimensionCostHistogramResult represents the dimension cost histogram query result
type DimensionCostHistogramResult struct {
	Buckets           []DimensionCostHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                          `json:"bucket_size_seconds"`
	Dimension         HistogramDimension             `json:"dimension"`
	DimensionValues   []string                       `json:"dimension_values"`
}

// DimensionTokenStats represents token statistics for a single dimension value
type DimensionTokenStats struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// DimensionTokenHistogramBucket represents a single time bucket for dimension-grouped token data
type DimensionTokenHistogramBucket struct {
	Timestamp   time.Time                      `json:"timestamp"`
	ByDimension map[string]DimensionTokenStats `json:"by_dimension"`
}

// DimensionTokenHistogramResult represents the dimension token histogram query result
type DimensionTokenHistogramResult struct {
	Buckets           []DimensionTokenHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                           `json:"bucket_size_seconds"`
	Dimension         HistogramDimension              `json:"dimension"`
	DimensionValues   []string                        `json:"dimension_values"`
}

// DimensionLatencyStats represents latency statistics for a single dimension value
type DimensionLatencyStats struct {
	AvgLatency    float64 `json:"avg_latency"`
	P90Latency    float64 `json:"p90_latency"`
	P95Latency    float64 `json:"p95_latency"`
	P99Latency    float64 `json:"p99_latency"`
	TotalRequests int64   `json:"total_requests"`
}

// DimensionLatencyHistogramBucket represents a single time bucket for dimension-grouped latency data
type DimensionLatencyHistogramBucket struct {
	Timestamp   time.Time                        `json:"timestamp"`
	ByDimension map[string]DimensionLatencyStats `json:"by_dimension"`
}

// DimensionLatencyHistogramResult represents the dimension latency histogram query result
type DimensionLatencyHistogramResult struct {
	Buckets           []DimensionLatencyHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                             `json:"bucket_size_seconds"`
	Dimension         HistogramDimension                `json:"dimension"`
	DimensionValues   []string                          `json:"dimension_values"`
}

// MCPHistogramBucket represents a single time bucket for MCP tool call volume
type MCPHistogramBucket struct {
	Timestamp time.Time `json:"timestamp"`
	Count     int64     `json:"count"`
	Success   int64     `json:"success"`
	Error     int64     `json:"error"`
}

// MCPHistogramResult represents the MCP tool call volume histogram query result
type MCPHistogramResult struct {
	Buckets           []MCPHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                `json:"bucket_size_seconds"`
}

// MCPCostHistogramBucket represents a single time bucket for MCP cost data
type MCPCostHistogramBucket struct {
	Timestamp time.Time `json:"timestamp"`
	TotalCost float64   `json:"total_cost"`
}

// MCPCostHistogramResult represents the MCP cost histogram query result
type MCPCostHistogramResult struct {
	Buckets           []MCPCostHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                    `json:"bucket_size_seconds"`
}

// MCPTopToolResult represents a single tool's aggregated stats
type MCPTopToolResult struct {
	ToolName string  `json:"tool_name"`
	Count    int64   `json:"count"`
	Cost     float64 `json:"cost"`
}

// MCPTopToolsResult represents the top N MCP tools by call count
type MCPTopToolsResult struct {
	Tools []MCPTopToolResult `json:"tools"`
}

// ModelRankingEntry represents aggregated stats for a single model over a time period.
type ModelRankingEntry struct {
	Model              string  `json:"model"`
	CanonicalModelName *string `json:"canonical_model_name,omitempty"`
	Provider           string  `json:"provider"`
	TotalRequests      int64   `json:"total_requests"`
	SuccessCount       int64   `json:"success_count"`
	SuccessRate        float64 `json:"success_rate"`
	TotalTokens        int64   `json:"total_tokens"`
	TotalCost          float64 `json:"total_cost"`
	AvgLatency         float64 `json:"avg_latency"`
	// Throughput is aggregate token-generation rate (tokens/sec) for this model:
	// SUM(completion_tokens) / (SUM(latency_ms)/1000) over successful rows with
	// latency > 0 — the same definition as the throughput histogram.
	Throughput float64 `json:"throughput"`
}

// ModelRankingTrend represents the percentage change compared to the previous period.
type ModelRankingTrend struct {
	HasPreviousPeriod bool    `json:"has_previous_period"`
	RequestsTrend     float64 `json:"requests_trend"`
	TokensTrend       float64 `json:"tokens_trend"`
	CostTrend         float64 `json:"cost_trend"`
	LatencyTrend      float64 `json:"latency_trend"`
	ThroughputTrend   float64 `json:"throughput_trend"`
}

// ModelRankingWithTrend combines ranking entry with trend data.
type ModelRankingWithTrend struct {
	ModelRankingEntry
	Trend ModelRankingTrend `json:"trend"`
}

// ModelRankingResult is the response for the model rankings endpoint.
type ModelRankingResult struct {
	Rankings []ModelRankingWithTrend `json:"rankings"`
}

// UserRankingEntry represents a single user's usage statistics.
type UserRankingEntry struct {
	UserID        string  `json:"user_id"`
	TotalRequests int64   `json:"total_requests"`
	TotalTokens   int64   `json:"total_tokens"`
	TotalCost     float64 `json:"total_cost"`
}

// UserRankingTrend represents the percentage change compared to the previous period.
type UserRankingTrend struct {
	HasPreviousPeriod bool    `json:"has_previous_period"`
	RequestsTrend     float64 `json:"requests_trend"`
	TokensTrend       float64 `json:"tokens_trend"`
	CostTrend         float64 `json:"cost_trend"`
}

// UserRankingWithTrend combines ranking entry with trend data.
type UserRankingWithTrend struct {
	UserRankingEntry
	Trend UserRankingTrend `json:"trend"`
}

// UserRankingResult is the response for the user rankings endpoint.
type UserRankingResult struct {
	Rankings []UserRankingWithTrend `json:"rankings"`
}

// RankingDimension is the column used for grouping in dimension rankings.
type RankingDimension string

const (
	RankingDimensionTeam         RankingDimension = "team"
	RankingDimensionCustomer     RankingDimension = "customer"
	RankingDimensionBusinessUnit RankingDimension = "business_unit"
	RankingDimensionUser         RankingDimension = "user"
	RankingDimensionVirtualKey   RankingDimension = "virtual_key"
)

var ValidRankingDimensions = map[RankingDimension]bool{
	RankingDimensionTeam:         true,
	RankingDimensionCustomer:     true,
	RankingDimensionBusinessUnit: true,
	RankingDimensionUser:         true,
	RankingDimensionVirtualKey:   true,
}

type dimensionColumnDef struct {
	IDCol   string
	NameCol string
}

var dimensionColumns = map[RankingDimension]dimensionColumnDef{
	RankingDimensionTeam:         {IDCol: "team_id", NameCol: "team_name"},
	RankingDimensionCustomer:     {IDCol: "customer_id", NameCol: "customer_name"},
	RankingDimensionBusinessUnit: {IDCol: "business_unit_id", NameCol: "business_unit_name"},
	RankingDimensionUser:         {IDCol: "user_id", NameCol: "user_name"},
	RankingDimensionVirtualKey:   {IDCol: "virtual_key_id", NameCol: "virtual_key_name"},
}

func DimensionColumnDef(d RankingDimension) (idCol, nameCol string, ok bool) {
	def, exists := dimensionColumns[d]
	return def.IDCol, def.NameCol, exists
}

type DimensionRankingEntry struct {
	ID            string  `json:"id"`
	Name          string  `json:"name,omitempty"`
	TotalRequests int64   `json:"total_requests"`
	TotalTokens   int64   `json:"total_tokens"`
	TotalCost     float64 `json:"total_cost"`
}

type DimensionRankingTrend struct {
	HasPreviousPeriod bool    `json:"has_previous_period"`
	RequestsTrend     float64 `json:"requests_trend"`
	TokensTrend       float64 `json:"tokens_trend"`
	CostTrend         float64 `json:"cost_trend"`
}

type DimensionRankingWithTrend struct {
	DimensionRankingEntry
	Trend DimensionRankingTrend `json:"trend"`
}

type DimensionRankingResult struct {
	Rankings  []DimensionRankingWithTrend `json:"rankings"`
	Dimension RankingDimension            `json:"dimension"`
	// TotalActualRequests / TotalAttributedRequests are set for every rollup
	// dimension (team / business unit / customer / user / virtual key). These use
	// single-owner attribution (one request → one owner, with owner-less traffic
	// in an "Unassigned" bucket), so the rollup is additive and the two counts
	// are equal — both report the real total request count over the window,
	// including Unassigned.
	TotalActualRequests     int64 `json:"total_actual_requests,omitempty"`
	TotalAttributedRequests int64 `json:"total_attributed_requests,omitempty"`
}

// ==================== CONSOLIDATED DASHBOARD RESULT ====================
// The types below back the GET /api/logs/dashboard endpoint, which returns
// every metric shown on the /workspace/dashboard page in a single response.
// Each section reuses the same struct returned by its dedicated endpoint, so
// the consolidated payload stays byte-for-byte consistent with the per-tab
// endpoints. This is a stable, public-facing contract: add fields, do not
// rename or remove existing ones.

// DashboardMeta describes the parameters the dashboard data was computed with,
// so consumers can interpret the buckets and rankings without re-deriving them.
type DashboardMeta struct {
	GeneratedAt       time.Time  `json:"generated_at"`         // UTC time the response was assembled
	BucketSizeSeconds int64      `json:"bucket_size_seconds"`  // Width of every histogram bucket, derived from the time range
	StartTime         *time.Time `json:"start_time,omitempty"` // Resolved start of the queried range (from start_time/end_time or period)
	EndTime           *time.Time `json:"end_time,omitempty"`   // Resolved end of the queried range
}

// DashboardOverview holds the Overview tab metrics.
type DashboardOverview struct {
	Stats    *SearchStats            `json:"stats"`    // Totals + success/cache rates
	Requests *HistogramResult        `json:"requests"` // Request volume over time
	Tokens   *TokenHistogramResult   `json:"tokens"`   // Token usage over time
	Cost     *CostHistogramResult    `json:"cost"`     // Cost over time, broken down by model
	Models   *ModelHistogramResult   `json:"models"`   // Per-model usage over time
	Latency  *LatencyHistogramResult `json:"latency"`  // Latency percentiles over time
	// Throughput holds tokens/sec over time (aggregate rate per bucket).
	Throughput *ThroughputHistogramResult `json:"throughput"`
}

// DashboardProviderUsage holds the Provider Usage tab metrics.
type DashboardProviderUsage struct {
	Cost    *ProviderCostHistogramResult    `json:"cost"`
	Tokens  *ProviderTokenHistogramResult   `json:"tokens"`
	Latency *ProviderLatencyHistogramResult `json:"latency"`
	// Throughput holds per-provider tokens/sec over time.
	Throughput *ProviderThroughputHistogramResult `json:"throughput"`
}

// DashboardModelRankings holds the Model Rankings tab data.
type DashboardModelRankings struct {
	Rankings  *ModelRankingResult   `json:"rankings"`  // Ranked table with trends
	Histogram *ModelHistogramResult `json:"histogram"` // Backs the "Top Models" stacked bar chart
}

// DashboardMCP holds the MCP usage tab metrics.
type DashboardMCP struct {
	Volume   *MCPHistogramResult     `json:"volume"`    // Tool call volume over time
	Cost     *MCPCostHistogramResult `json:"cost"`      // MCP cost over time
	TopTools *MCPTopToolsResult      `json:"top_tools"` // Top tools by call count
}

// DashboardResult is the full consolidated payload for GET /api/logs/dashboard.
// DimensionRankings is keyed by RankingDimension ("team", "user", "virtual_key",
// "customer", "business_unit"); each value is that dimension's ranking table.
type DashboardResult struct {
	Meta              DashboardMeta                      `json:"meta"`
	Overview          DashboardOverview                  `json:"overview"`
	ProviderUsage     DashboardProviderUsage             `json:"provider_usage"`
	ModelRankings     DashboardModelRankings             `json:"model_rankings"`
	DimensionRankings map[string]*DimensionRankingResult `json:"dimension_rankings"`
	MCP               DashboardMCP                       `json:"mcp"`
}

// NodeUsageCursor identifies the last log row included in a node usage scan.
// The initial scan uses Timestamp + LogID because each ghost has a timestamp
// lower bound. Once rows written with IncNumber are seen, subsequent scans use
// IncNumber because it is assigned by the database at insert time and therefore
// does not skip late async log writes.
type NodeUsageCursor struct {
	Timestamp time.Time `json:"timestamp"`
	LogID     string    `json:"log_id"`
	IncNumber *int64    `json:"inc_number,omitempty"`
}

// NodeUsageAggregate represents aggregated usage for a specific node from the logs table,
// broken down by the budget and rate-limit IDs that each log entry was tagged with.
// This ensures usage is attributed to the correct governance resource rather than
// spread uniformly across all resources the node was tracking.
type NodeUsageAggregate struct {
	BudgetCosts       map[string]float64 `json:"budget_costs"`        // budget_id -> cumulative cost
	RateLimitRequests map[string]int64   `json:"rate_limit_requests"` // rate_limit_id -> successful request count
	RateLimitTokens   map[string]int64   `json:"rate_limit_tokens"`   // rate_limit_id -> total tokens
	RowCount          int                `json:"row_count"`           // number of log rows included in the aggregate
	MaxTimestamp      time.Time          `json:"max_timestamp"`       // highest log timestamp included in the aggregate
	MaxLogID          string             `json:"max_log_id"`          // log ID tiebreaker for MaxTimestamp
	NextCursor        NodeUsageCursor    `json:"next_cursor"`         // stable cursor for the next incremental query
}
