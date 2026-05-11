package logstore

import (
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractPayload_RoundTrip(t *testing.T) {
	log := &Log{
		ID:                      "test-1",
		InputHistory:            `[{"role":"user","content":"hello"}]`,
		ResponsesInputHistory:   `[{"role":"user","content":"hi"}]`,
		OutputMessage:           `{"role":"assistant","content":"world"}`,
		ResponsesOutput:         `[{"role":"assistant","content":"there"}]`,
		EmbeddingOutput:         `[{"embedding":[0.1]}]`,
		RerankOutput:            `[{"score":0.9}]`,
		Params:                  `{"temperature":0.7}`,
		Tools:                   `[{"name":"tool1"}]`,
		ToolCalls:               `[{"id":"tc1"}]`,
		SpeechInput:             `{"input":"text"}`,
		TranscriptionInput:      `{"file":"test.mp3"}`,
		ImageGenerationInput:    `{"prompt":"cat"}`,
		ImageEditInput:          `{"prompt":"edit cat"}`,
		ImageVariationInput:     `{"image":"base64img"}`,
		VideoGenerationInput:    `{"prompt":"dog"}`,
		SpeechOutput:            `{"audio":"base64"}`,
		TranscriptionOutput:     `{"text":"hello"}`,
		ImageGenerationOutput:   `{"url":"http://img"}`,
		ListModelsOutput:        `[{"id":"model1"}]`,
		VideoGenerationOutput:   `{"id":"vid1"}`,
		VideoRetrieveOutput:     `{"status":"ready"}`,
		VideoDownloadOutput:     `{"url":"http://vid"}`,
		VideoListOutput:         `{"videos":[]}`,
		VideoDeleteOutput:       `{"deleted":true}`,
		CacheDebug:              `{"hit":true}`,
		TokenUsage:              `{"total_tokens":100}`,
		ErrorDetails:            `{"error":"bad"}`,
		RawRequest:              `{"method":"POST"}`,
		RawResponse:             `{"status":200}`,
		PassthroughRequestBody:  `body-req`,
		PassthroughResponseBody: `body-resp`,
		RoutingEngineLogs:       `routing log`,
	}

	payload := ExtractPayload(log)
	assert.Equal(t, len(payloadFields), len(payload), "payload map should have all payload fields")
	assert.Equal(t, `[{"role":"user","content":"hello"}]`, payload["input_history"])
	assert.Equal(t, `{"role":"assistant","content":"world"}`, payload["output_message"])
	assert.Equal(t, `routing log`, payload["routing_engine_logs"])

	// Clear and verify.
	ClearPayload(log)
	assert.Empty(t, log.InputHistory)
	assert.Empty(t, log.OutputMessage)
	assert.Empty(t, log.RawRequest)
	assert.Empty(t, log.RoutingEngineLogs)

	// Marshal and merge back.
	data, err := MarshalPayload(payload)
	require.NoError(t, err)

	err = MergePayloadFromJSON(log, data)
	require.NoError(t, err)
	assert.Equal(t, `[{"role":"user","content":"hello"}]`, log.InputHistory)
	assert.Equal(t, `{"role":"assistant","content":"world"}`, log.OutputMessage)
	assert.Equal(t, `routing log`, log.RoutingEngineLogs)
}

func TestClearPayload_DoesNotTouchIndexFields(t *testing.T) {
	log := &Log{
		ID:           "test-1",
		Provider:     "anthropic",
		Model:        "claude-3",
		Status:       "success",
		InputHistory: `[{"role":"user","content":"hello"}]`,
	}
	ClearPayload(log)
	assert.Equal(t, "test-1", log.ID)
	assert.Equal(t, "anthropic", log.Provider)
	assert.Equal(t, "claude-3", log.Model)
	assert.Equal(t, "success", log.Status)
	assert.Empty(t, log.InputHistory)
}

func TestBuildInputContentSummary(t *testing.T) {
	content := "What is the weather?"
	log := &Log{
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &content}},
		},
		OutputMessageParsed: &schemas.ChatMessage{
			Content: &schemas.ChatMessageContent{ContentStr: strPtr("It's sunny")},
		},
	}

	summary := log.BuildInputContentSummary()
	assert.Contains(t, summary, "What is the weather?")
	assert.NotContains(t, summary, "It's sunny", "BuildInputContentSummary should not include output")
}

func TestBuildTags(t *testing.T) {
	vkID := "vk_123"
	rrID := "rr_456"
	log := &Log{
		Provider:      "anthropic",
		Model:         "claude-3-sonnet",
		Status:        "success",
		Object:        "chat.completion",
		VirtualKeyID:  &vkID,
		SelectedKeyID: "sk_789",
		RoutingRuleID: &rrID,
		Stream:        true,
		Timestamp:     time.Date(2026, 4, 3, 14, 0, 0, 0, time.UTC),
	}

	tags := BuildTags(log)
	assert.Equal(t, "anthropic", tags["provider"])
	assert.Equal(t, "claude-3-sonnet", tags["model"])
	assert.Equal(t, "success", tags["status"])
	assert.Equal(t, "chat.completion", tags["object_type"])
	assert.Equal(t, "vk_123", tags["virtual_key_id"])
	assert.Equal(t, "sk_789", tags["selected_key_id"])
	assert.Equal(t, "rr_456", tags["routing_rule_id"])
	assert.Equal(t, "true", tags["stream"])
	assert.Equal(t, "false", tags["has_error"])
	assert.Equal(t, "2026-04-03", tags["date"])
	assert.LessOrEqual(t, len(tags), 10, "S3 allows max 10 tags")
}

func TestBuildTags_ErrorStatus(t *testing.T) {
	log := &Log{Status: "error", Timestamp: time.Now()}
	tags := BuildTags(log)
	assert.Equal(t, "true", tags["has_error"])
}

func TestObjectKey(t *testing.T) {
	ts := time.Date(2026, 4, 3, 14, 0, 0, 0, time.UTC)
	key := ObjectKey("bifrost", ts, "req_abc123")
	assert.Equal(t, "bifrost/logs/2026/04/03/14/req_abc123.json.gz", key)
}

func TestMCPToolObjectKey(t *testing.T) {
	ts := time.Date(2026, 4, 3, 14, 0, 0, 0, time.UTC)
	key := MCPToolObjectKey("bifrost", ts, "mcp_abc123")
	assert.Equal(t, "bifrost/mcp-logs/2026/04/03/14/mcp_abc123.json.gz", key)
}

func TestMCPToolLogPayload_RoundTripFullLog(t *testing.T) {
	vkID := "vk_123"
	cost := 0.01
	latency := 42.5
	entry := &MCPToolLog{
		ID:             "mcp-1",
		RequestID:      "req-1",
		LLMRequestID:   strPtr("llm-1"),
		Timestamp:      time.Date(2026, 4, 3, 14, 0, 0, 0, time.UTC),
		ToolName:       "search",
		ServerLabel:    "docs",
		VirtualKeyID:   &vkID,
		VirtualKeyName: strPtr("prod-key"),
		Latency:        &latency,
		Cost:           &cost,
		Status:         "success",
		CreatedAt:      time.Date(2026, 4, 3, 14, 0, 1, 0, time.UTC),
		ArgumentsParsed: map[string]any{
			"query": "full input",
		},
		ResultParsed: map[string]any{
			"ok": true,
		},
		ErrorDetailsParsed: &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "stored for round trip",
			},
		},
		MetadataParsed: map[string]interface{}{
			"trace": "abc",
		},
	}

	data, err := MarshalMCPToolLogPayload(entry)
	require.NoError(t, err)

	dbEntry := &MCPToolLog{HasObject: true}
	err = MergeMCPToolLogPayloadFromJSON(dbEntry, data)
	require.NoError(t, err)

	assert.True(t, dbEntry.HasObject)
	assert.Equal(t, entry.ID, dbEntry.ID)
	assert.Equal(t, entry.RequestID, dbEntry.RequestID)
	assert.Equal(t, entry.ToolName, dbEntry.ToolName)
	assert.Equal(t, entry.ServerLabel, dbEntry.ServerLabel)
	assert.Equal(t, entry.Status, dbEntry.Status)
	assert.Equal(t, "full input", dbEntry.ArgumentsParsed.(map[string]interface{})["query"])
	assert.Equal(t, true, dbEntry.ResultParsed.(map[string]interface{})["ok"])
	assert.Equal(t, "stored for round trip", dbEntry.ErrorDetailsParsed.Error.Message)
	assert.Equal(t, "abc", dbEntry.MetadataParsed["trace"])
}

func TestPrepareMCPToolDBEntry_KeepsOnlyInputPreview(t *testing.T) {
	longInput := ""
	for i := 0; i < 260; i++ {
		longInput += "a"
	}
	entry := &MCPToolLog{
		ID:          "mcp-preview",
		RequestID:   "req-preview",
		Timestamp:   time.Date(2026, 4, 3, 14, 0, 0, 0, time.UTC),
		ToolName:    "echo",
		ServerLabel: "local",
		Status:      "success",
		ArgumentsParsed: map[string]any{
			"input": longInput,
		},
		ResultParsed: map[string]any{
			"secret": "large result",
		},
		ErrorDetailsParsed: &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "large error",
			},
		},
		MetadataParsed: map[string]interface{}{
			"trace": "abc",
		},
	}

	PrepareMCPToolDBEntry(entry)

	assert.Equal(t, "mcp-preview", entry.ID)
	assert.Equal(t, "req-preview", entry.RequestID)
	assert.Equal(t, "echo", entry.ToolName)
	assert.Equal(t, "local", entry.ServerLabel)
	assert.Empty(t, entry.Result)
	assert.Empty(t, entry.ErrorDetails)
	assert.Nil(t, entry.ResultParsed)
	assert.Nil(t, entry.ErrorDetailsParsed)
	assert.NotEmpty(t, entry.Arguments)
	assert.NotEmpty(t, entry.Metadata)

	var preview string
	require.NoError(t, sonic.Unmarshal([]byte(entry.Arguments), &preview))
	assert.Len(t, []rune(preview), 200)
	assert.Contains(t, preview, `"input":`)
}

func TestPayloadFieldNames(t *testing.T) {
	fields := PayloadFieldNames()
	assert.True(t, len(fields) > 0)
	// Verify it's a copy.
	fields[0] = "modified"
	assert.NotEqual(t, "modified", payloadFields[0])
}

func strPtr(s string) *string {
	return &s
}
