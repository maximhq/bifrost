package logstore

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// payloadFields lists the DB column names of large TEXT fields that are
// offloaded to object storage in hybrid mode. These fields are never needed
// for analytics queries (histograms, search, rankings) — only for individual
// log detail views (FindByID).
var payloadFields = []string{
	"input_history",
	"responses_input_history",
	"output_message",
	"responses_output",
	"embedding_output",
	"rerank_output",
	"params",
	"tools",
	"tool_calls",
	"speech_input",
	"transcription_input",
	"image_generation_input",
	"video_generation_input",
	"speech_output",
	"transcription_output",
	"image_generation_output",
	"list_models_output",
	"video_generation_output",
	"video_retrieve_output",
	"video_download_output",
	"video_list_output",
	"video_delete_output",
	"cache_debug",
	"token_usage",
	"error_details",
	"raw_request",
	"raw_response",
	"passthrough_request_body",
	"passthrough_response_body",
	"routing_engine_logs",
}

// ExtractPayload reads the serialized TEXT payload fields from a Log into a map.
// The map keys are the DB column names.
func ExtractPayload(l *Log) map[string]string {
	m := make(map[string]string, len(payloadFields))
	m["input_history"] = l.InputHistory
	m["responses_input_history"] = l.ResponsesInputHistory
	m["output_message"] = l.OutputMessage
	m["responses_output"] = l.ResponsesOutput
	m["embedding_output"] = l.EmbeddingOutput
	m["rerank_output"] = l.RerankOutput
	m["params"] = l.Params
	m["tools"] = l.Tools
	m["tool_calls"] = l.ToolCalls
	m["speech_input"] = l.SpeechInput
	m["transcription_input"] = l.TranscriptionInput
	m["image_generation_input"] = l.ImageGenerationInput
	m["video_generation_input"] = l.VideoGenerationInput
	m["speech_output"] = l.SpeechOutput
	m["transcription_output"] = l.TranscriptionOutput
	m["image_generation_output"] = l.ImageGenerationOutput
	m["list_models_output"] = l.ListModelsOutput
	m["video_generation_output"] = l.VideoGenerationOutput
	m["video_retrieve_output"] = l.VideoRetrieveOutput
	m["video_download_output"] = l.VideoDownloadOutput
	m["video_list_output"] = l.VideoListOutput
	m["video_delete_output"] = l.VideoDeleteOutput
	m["cache_debug"] = l.CacheDebug
	m["token_usage"] = l.TokenUsage
	m["error_details"] = l.ErrorDetails
	m["raw_request"] = l.RawRequest
	m["raw_response"] = l.RawResponse
	m["passthrough_request_body"] = l.PassthroughRequestBody
	m["passthrough_response_body"] = l.PassthroughResponseBody
	m["routing_engine_logs"] = l.RoutingEngineLogs
	return m
}

// ClearPayload zeros out both the TEXT payload columns and the Parsed virtual
// fields on a Log struct. Clearing the Parsed fields is necessary to prevent
// GORM's BeforeCreate/SerializeFields from re-populating TEXT columns.
// After calling this, the struct only contains index-weight data suitable
// for a lightweight DB INSERT.
func ClearPayload(l *Log) {
	// Clear serialized TEXT columns.
	l.InputHistory = ""
	l.ResponsesInputHistory = ""
	l.OutputMessage = ""
	l.ResponsesOutput = ""
	l.EmbeddingOutput = ""
	l.RerankOutput = ""
	l.Params = ""
	l.Tools = ""
	l.ToolCalls = ""
	l.SpeechInput = ""
	l.TranscriptionInput = ""
	l.ImageGenerationInput = ""
	l.VideoGenerationInput = ""
	l.SpeechOutput = ""
	l.TranscriptionOutput = ""
	l.ImageGenerationOutput = ""
	l.ListModelsOutput = ""
	l.VideoGenerationOutput = ""
	l.VideoRetrieveOutput = ""
	l.VideoDownloadOutput = ""
	l.VideoListOutput = ""
	l.VideoDeleteOutput = ""
	l.CacheDebug = ""
	l.TokenUsage = ""
	l.ErrorDetails = ""
	l.RawRequest = ""
	l.RawResponse = ""
	l.PassthroughRequestBody = ""
	l.PassthroughResponseBody = ""
	l.RoutingEngineLogs = ""

	// Clear Parsed virtual fields so GORM's SerializeFields won't re-serialize them.
	l.InputHistoryParsed = nil
	l.ResponsesInputHistoryParsed = nil
	l.OutputMessageParsed = nil
	l.ResponsesOutputParsed = nil
	l.EmbeddingOutputParsed = nil
	l.RerankOutputParsed = nil
	l.ParamsParsed = nil
	l.ToolsParsed = nil
	l.ToolCallsParsed = nil
	l.SpeechInputParsed = nil
	l.TranscriptionInputParsed = nil
	l.ImageGenerationInputParsed = nil
	l.VideoGenerationInputParsed = nil
	l.SpeechOutputParsed = nil
	l.TranscriptionOutputParsed = nil
	l.ImageGenerationOutputParsed = nil
	l.ListModelsOutputParsed = nil
	l.VideoGenerationOutputParsed = nil
	l.VideoRetrieveOutputParsed = nil
	l.VideoDownloadOutputParsed = nil
	l.VideoListOutputParsed = nil
	l.VideoDeleteOutputParsed = nil
	l.CacheDebugParsed = nil
	l.TokenUsageParsed = nil
	l.ErrorDetailsParsed = nil
}

// MergePayloadFromJSON takes a JSON payload (as marshaled by MarshalPayload)
// and merges the fields back into the Log struct's serialized TEXT columns,
// then calls DeserializeFields to populate the Parsed virtual fields.
func MergePayloadFromJSON(l *Log, data []byte) error {
	var m map[string]string
	if err := sonic.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("logstore: unmarshal payload: %w", err)
	}
	if v, ok := m["input_history"]; ok && v != "" {
		l.InputHistory = v
	}
	if v, ok := m["responses_input_history"]; ok && v != "" {
		l.ResponsesInputHistory = v
	}
	if v, ok := m["output_message"]; ok && v != "" {
		l.OutputMessage = v
	}
	if v, ok := m["responses_output"]; ok && v != "" {
		l.ResponsesOutput = v
	}
	if v, ok := m["embedding_output"]; ok && v != "" {
		l.EmbeddingOutput = v
	}
	if v, ok := m["rerank_output"]; ok && v != "" {
		l.RerankOutput = v
	}
	if v, ok := m["params"]; ok && v != "" {
		l.Params = v
	}
	if v, ok := m["tools"]; ok && v != "" {
		l.Tools = v
	}
	if v, ok := m["tool_calls"]; ok && v != "" {
		l.ToolCalls = v
	}
	if v, ok := m["speech_input"]; ok && v != "" {
		l.SpeechInput = v
	}
	if v, ok := m["transcription_input"]; ok && v != "" {
		l.TranscriptionInput = v
	}
	if v, ok := m["image_generation_input"]; ok && v != "" {
		l.ImageGenerationInput = v
	}
	if v, ok := m["video_generation_input"]; ok && v != "" {
		l.VideoGenerationInput = v
	}
	if v, ok := m["speech_output"]; ok && v != "" {
		l.SpeechOutput = v
	}
	if v, ok := m["transcription_output"]; ok && v != "" {
		l.TranscriptionOutput = v
	}
	if v, ok := m["image_generation_output"]; ok && v != "" {
		l.ImageGenerationOutput = v
	}
	if v, ok := m["list_models_output"]; ok && v != "" {
		l.ListModelsOutput = v
	}
	if v, ok := m["video_generation_output"]; ok && v != "" {
		l.VideoGenerationOutput = v
	}
	if v, ok := m["video_retrieve_output"]; ok && v != "" {
		l.VideoRetrieveOutput = v
	}
	if v, ok := m["video_download_output"]; ok && v != "" {
		l.VideoDownloadOutput = v
	}
	if v, ok := m["video_list_output"]; ok && v != "" {
		l.VideoListOutput = v
	}
	if v, ok := m["video_delete_output"]; ok && v != "" {
		l.VideoDeleteOutput = v
	}
	if v, ok := m["cache_debug"]; ok && v != "" {
		l.CacheDebug = v
	}
	if v, ok := m["token_usage"]; ok && v != "" {
		l.TokenUsage = v
	}
	if v, ok := m["error_details"]; ok && v != "" {
		l.ErrorDetails = v
	}
	if v, ok := m["raw_request"]; ok && v != "" {
		l.RawRequest = v
	}
	if v, ok := m["raw_response"]; ok && v != "" {
		l.RawResponse = v
	}
	if v, ok := m["passthrough_request_body"]; ok && v != "" {
		l.PassthroughRequestBody = v
	}
	if v, ok := m["passthrough_response_body"]; ok && v != "" {
		l.PassthroughResponseBody = v
	}
	if v, ok := m["routing_engine_logs"]; ok && v != "" {
		l.RoutingEngineLogs = v
	}
	return l.DeserializeFields()
}

// MarshalPayload serializes the payload map (from ExtractPayload) to JSON.
func MarshalPayload(payload map[string]string) ([]byte, error) {
	return sonic.Marshal(payload)
}

// BuildInputContentSummary extracts the last user message text from input fields.
// This is used in hybrid mode for the content_summary column, which powers
// full-text search and serves as a display fallback in the log list table.
// Only the last message is kept — the full conversation history lives in
// object storage and is merged back on FindByID.
func (l *Log) BuildInputContentSummary() string {
	// Chat completions: last user message
	if idx := findLastUserMessageIndex(l.InputHistoryParsed); idx >= 0 {
		if text := extractChatMessageText(&l.InputHistoryParsed[idx]); text != "" {
			return text
		}
	}

	// Responses API: last user message
	for i := len(l.ResponsesInputHistoryParsed) - 1; i >= 0; i-- {
		if l.ResponsesInputHistoryParsed[i].Role != nil && *l.ResponsesInputHistoryParsed[i].Role == schemas.ResponsesInputMessageRoleUser {
			if text := extractResponsesMessageText(&l.ResponsesInputHistoryParsed[i]); text != "" {
				return text
			}
		}
	}

	// Speech input
	if l.SpeechInputParsed != nil && l.SpeechInputParsed.Input != "" {
		return l.SpeechInputParsed.Input
	}

	// Image generation input prompt
	if l.ImageGenerationInputParsed != nil && l.ImageGenerationInputParsed.Prompt != "" {
		return l.ImageGenerationInputParsed.Prompt
	}

	// Video generation input prompt
	if l.VideoGenerationInputParsed != nil && l.VideoGenerationInputParsed.Prompt != "" {
		return l.VideoGenerationInputParsed.Prompt
	}

	return ""
}

// extractChatMessageText returns the text content from a ChatMessage.
// It prefers ContentStr; falls back to the last text ContentBlock.
func extractChatMessageText(msg *schemas.ChatMessage) string {
	if msg.Content == nil {
		return ""
	}
	if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
		return *msg.Content.ContentStr
	}
	if msg.Content.ContentBlocks != nil {
		var lastText string
		for _, block := range msg.Content.ContentBlocks {
			if block.Text != nil && *block.Text != "" {
				lastText = *block.Text
			}
		}
		return lastText
	}
	return ""
}

// extractResponsesMessageText returns the text content from a ResponsesMessage.
// It prefers ContentStr; falls back to the last text ContentBlock.
func extractResponsesMessageText(msg *schemas.ResponsesMessage) string {
	if msg.Content == nil {
		return ""
	}
	if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
		return *msg.Content.ContentStr
	}
	if msg.Content.ContentBlocks != nil {
		var lastText string
		for _, block := range msg.Content.ContentBlocks {
			if block.Text != nil && *block.Text != "" {
				lastText = *block.Text
			}
		}
		return lastText
	}
	return ""
}

// findLastUserMessageIndex returns the index of the last ChatMessage with
// role "user", or -1 if none exists. Used by both BuildInputContentSummary
// and prepareDBEntry to avoid scanning the slice twice.
func findLastUserMessageIndex(msgs []schemas.ChatMessage) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == schemas.ChatMessageRoleUser {
			return i
		}
	}
	return -1
}

// BuildTags creates the S3 object tag map from a Log's index fields.
// S3 allows max 10 tags per object; chosen for lifecycle rules and
// S3 Metadata Tables queryability.
func BuildTags(l *Log) map[string]string {
	tags := make(map[string]string, 10)
	if l.Provider != "" {
		tags["provider"] = l.Provider
	}
	if l.Model != "" {
		tags["model"] = truncateTag(l.Model, 256)
	}
	if l.Status != "" {
		tags["status"] = l.Status
	}
	if l.Object != "" {
		tags["object_type"] = l.Object
	}
	if l.VirtualKeyID != nil && *l.VirtualKeyID != "" {
		tags["virtual_key_id"] = truncateTag(*l.VirtualKeyID, 256)
	}
	if l.SelectedKeyID != "" {
		tags["selected_key_id"] = truncateTag(l.SelectedKeyID, 256)
	}
	if l.RoutingRuleID != nil && *l.RoutingRuleID != "" {
		tags["routing_rule_id"] = truncateTag(*l.RoutingRuleID, 256)
	}
	if l.Stream {
		tags["stream"] = "true"
	} else {
		tags["stream"] = "false"
	}
	tags["has_error"] = "false"
	if l.Status == "error" {
		tags["has_error"] = "true"
	}
	tags["date"] = l.Timestamp.UTC().Format("2006-01-02")
	return tags
}

// ObjectKey constructs the S3 object key for a log entry.
func ObjectKey(prefix string, timestamp time.Time, logID string) string {
	ts := timestamp.UTC()
	return fmt.Sprintf("%s/logs/%04d/%02d/%02d/%02d/%s.json.gz",
		prefix,
		ts.Year(), ts.Month(), ts.Day(), ts.Hour(),
		logID,
	)
}

// PayloadFieldNames returns the list of DB column names that are payload fields.
func PayloadFieldNames() []string {
	cp := make([]string, len(payloadFields))
	copy(cp, payloadFields)
	return cp
}

// truncateTag ensures a tag value doesn't exceed the given max length.
func truncateTag(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// Truncate at a rune boundary without exceeding maxLen bytes.
	byteLen := 0
	for _, r := range s {
		rl := utf8.RuneLen(r)
		if byteLen+rl > maxLen {
			break
		}
		byteLen += rl
	}
	return s[:byteLen]
}
