package logstore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractPayload_RoundTrip(t *testing.T) {
	metadata := `{"cortex-user-id":"user-123"}`
	log := &Log{
		ID:                      "test-1",
		InputHistory:            `[{"role":"user","content":"hello"}]`,
		ResponsesInputHistory:   `[{"role":"user","content":"hi"}]`,
		OutputMessage:           `{"role":"assistant","content":"world"}`,
		ResponsesOutput:         `[{"role":"assistant","content":"there"}]`,
		EmbeddingInput:          `[{"content":[{"type":"text","text":"embed me"}]}]`,
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
		GuardrailDebug:          `{"judge_calls":[{"total_tokens":18}]}`,
		TokenUsage:              `{"total_tokens":100}`,
		ErrorDetails:            `{"error":"bad"}`,
		RawRequest:              `{"method":"POST"}`,
		RawResponse:             `{"status":200}`,
		PassthroughRequestBody:  `body-req`,
		PassthroughResponseBody: `body-resp`,
		RoutingEngineLogs:       `routing log`,
		Metadata:                &metadata,
		PluginLogs:              `{"guardrails":[{"message":"pii detected"}]}`,
	}

	payload := ExtractPayload(log)
	// +1 for metadata, +6 for the always-present index fields (provider, model,
	// status, timestamp, selected_key_id, selected_key_name) added in #6070.
	assert.Equal(t, len(payloadFields)+1+6, len(payload), "payload map should have all payload fields plus metadata and index fields")
	assert.Equal(t, `[{"role":"user","content":"hello"}]`, payload["input_history"])
	assert.Equal(t, `{"role":"assistant","content":"world"}`, payload["output_message"])
	assert.Equal(t, `[{"content":[{"type":"text","text":"embed me"}]}]`, payload["embedding_input"])
	assert.Equal(t, `{"judge_calls":[{"total_tokens":18}]}`, payload["guardrail_debug"])
	assert.Equal(t, `routing log`, payload["routing_engine_logs"])
	assert.Equal(t, metadata, payload["metadata"], "metadata must be written to the snapshot for object consumers")
	assert.Equal(t, `{"guardrails":[{"message":"pii detected"}]}`, payload["plugin_logs"])

	// Clear and verify.
	ClearPayload(log)
	assert.Empty(t, log.InputHistory)
	assert.Empty(t, log.OutputMessage)
	assert.Empty(t, log.EmbeddingInput)
	assert.Nil(t, log.EmbeddingInputParsed)
	assert.Empty(t, log.RawRequest)
	assert.Empty(t, log.GuardrailDebug)
	assert.Empty(t, log.RoutingEngineLogs)
	require.NotNil(t, log.Metadata)
	assert.Equal(t, metadata, *log.Metadata)
	assert.Empty(t, log.PluginLogs)

	// Marshal and merge back.
	data, err := MarshalPayload(payload)
	require.NoError(t, err)

	// Metadata is DB-authoritative: the snapshot carries a backup copy, but
	// MergePayloadFromJSON must NOT override the live DB value with it. Simulate
	// a DB row whose metadata was updated after the snapshot was written, then
	// confirm the merge leaves it untouched.
	dbMetadata := `{"cortex-user-id":"user-456"}`
	log.Metadata = &dbMetadata
	log.MetadataParsed = nil
	err = MergePayloadFromJSON(log, data)
	require.NoError(t, err)
	assert.Equal(t, `[{"role":"user","content":"hello"}]`, log.InputHistory)
	assert.Equal(t, `{"role":"assistant","content":"world"}`, log.OutputMessage)
	assert.Equal(t, `[{"content":[{"type":"text","text":"embed me"}]}]`, log.EmbeddingInput)
	assert.Equal(t, `{"judge_calls":[{"total_tokens":18}]}`, log.GuardrailDebug)
	assert.Equal(t, `routing log`, log.RoutingEngineLogs)
	assert.Equal(t, `{"guardrails":[{"message":"pii detected"}]}`, log.PluginLogs)
	require.NotNil(t, log.Metadata)
	assert.Equal(t, dbMetadata, *log.Metadata, "merge must not override DB-authoritative metadata with the snapshot")
	assert.Equal(t, "user-456", log.MetadataParsed["cortex-user-id"])
}

func TestExtractPayload_NilOrEmptyMetadataOmittedFromSnapshot(t *testing.T) {
	payload := ExtractPayload(&Log{ID: "x"})
	assert.NotContains(t, payload, "metadata", "nil Metadata must not appear in snapshot")

	empty := ""
	payload = ExtractPayload(&Log{ID: "y", Metadata: &empty})
	assert.NotContains(t, payload, "metadata", "empty Metadata must not appear in snapshot")
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
		PluginLogs: `{"guardrails":[{"plugin_name":"guardrails","level":"info","message":"arguments redacted","timestamp":1}]}`,
		RedactionData: &schemas.RedactionData{
			ReversibleMappings: schemas.RedactionMapsByPhase{Input: map[string]string{"EMAIL-1": "private@example.com"}},
		},
		RedactionMapping: `plain:{"input":{"EMAIL-1":"private@example.com"}}`,
	}

	data, err := MarshalMCPToolLogPayload(entry)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "private@example.com")

	dbEntry := &MCPToolLog{HasObject: true, RedactionMapping: entry.RedactionMapping}
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
	assert.Equal(t, entry.PluginLogs, dbEntry.PluginLogs)
	assert.Equal(t, entry.RedactionMapping, dbEntry.RedactionMapping)
	assert.Nil(t, dbEntry.RedactionData)
}

// TestMCPToolLogRedactionMappingJSONVisibility verifies only the authorized virtual mapping is API-visible.
func TestMCPToolLogRedactionMappingJSONVisibility(t *testing.T) {
	entry := &MCPToolLog{
		RedactionMapping: `plain:{"input":{"EMAIL-1":"private@example.com"}}`,
		RevealRedactionMapping: &schemas.RedactionMapsByPhase{
			Input: map[string]string{"EMAIL-1": "revealed@example.com"},
		},
	}

	data, err := sonic.Marshal(entry)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "private@example.com")
	assert.Contains(t, string(data), `"redaction_mapping":{"input":{"EMAIL-1":"revealed@example.com"}}`)
}

func TestAgentLogPayload_StrictV1SDKTypesRoundTrip(t *testing.T) {
	task := &a2a.Task{
		ID:        "task-1",
		ContextID: "context-1",
		Status: a2a.TaskStatus{
			State:   a2a.TaskStateCompleted,
			Message: &a2a.Message{ID: "status-message-1", Role: a2a.MessageRoleAgent, Parts: a2a.ContentParts{a2a.NewTextPart("completed")}},
		},
		History:   []*a2a.Message{{ID: "history-message-1", Role: a2a.MessageRoleUser, Parts: a2a.ContentParts{a2a.NewTextPart("history")}}},
		Artifacts: []*a2a.Artifact{{ID: "artifact-1", Name: "report", Parts: a2a.ContentParts{a2a.NewTextPart("artifact body")}}},
	}
	message := &a2a.Message{
		ID:        "response-message-1",
		ContextID: "context-1",
		TaskID:    "task-1",
		Role:      a2a.MessageRoleAgent,
		Parts:     a2a.ContentParts{a2a.NewTextPart("direct response")},
	}
	card := &a2a.AgentCard{
		Name:                "fixture",
		Version:             "1.0",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("https://agent.example/rpc", a2a.TransportProtocolJSONRPC)},
		Capabilities:        a2a.AgentCapabilities{Streaming: true, ExtendedAgentCard: true},
		DefaultInputModes:   []string{"text/plain"},
		DefaultOutputModes:  []string{"text/plain"},
	}
	historyLength := 2
	sendRequest := &a2a.SendMessageRequest{
		Message:  &a2a.Message{ID: "request-message-1", ContextID: "context-1", TaskID: "task-1", Role: a2a.MessageRoleUser, Parts: a2a.ContentParts{a2a.NewTextPart("hello")}},
		Metadata: map[string]any{"operation": "send"},
	}

	tests := []struct {
		name           string
		request        any
		response       any
		assertRequest  func(*testing.T, []byte)
		assertResponse func(*testing.T, []byte)
	}{
		{
			name: "send_message_direct_result", request: sendRequest, response: message,
			assertRequest: func(t *testing.T, data []byte) {
				var got a2a.SendMessageRequest
				require.NoError(t, json.Unmarshal(data, &got))
				require.NotNil(t, got.Message)
				assert.Equal(t, "request-message-1", got.Message.ID)
				assert.Equal(t, "context-1", got.Message.ContextID)
				assert.Equal(t, a2a.TaskID("task-1"), got.Message.TaskID)
				assert.Equal(t, "hello", got.Message.Parts[0].Text())
				assert.Equal(t, "send", got.Metadata["operation"])
			},
			assertResponse: func(t *testing.T, data []byte) {
				var got a2a.Message
				require.NoError(t, json.Unmarshal(data, &got))
				assert.Equal(t, "response-message-1", got.ID)
				assert.Equal(t, "context-1", got.ContextID)
				assert.Equal(t, a2a.TaskID("task-1"), got.TaskID)
				assert.Equal(t, "direct response", got.Parts[0].Text())
			},
		},
		{
			name: "send_message_task_result", request: sendRequest, response: task,
			assertRequest: func(t *testing.T, data []byte) {
				var got a2a.SendMessageRequest
				require.NoError(t, json.Unmarshal(data, &got))
				assert.Equal(t, "request-message-1", got.Message.ID)
			},
			assertResponse: assertStrictV1Task("task-1", a2a.TaskStateCompleted),
		},
		{
			name: "get_task", request: &a2a.GetTaskRequest{Tenant: "tenant-1", ID: "task-1", HistoryLength: &historyLength}, response: task,
			assertRequest: func(t *testing.T, data []byte) {
				var got a2a.GetTaskRequest
				require.NoError(t, json.Unmarshal(data, &got))
				assert.Equal(t, "tenant-1", got.Tenant)
				assert.Equal(t, a2a.TaskID("task-1"), got.ID)
				require.NotNil(t, got.HistoryLength)
				assert.Equal(t, 2, *got.HistoryLength)
			},
			assertResponse: assertStrictV1Task("task-1", a2a.TaskStateCompleted),
		},
		{
			name: "list_tasks", request: &a2a.ListTasksRequest{Tenant: "tenant-1", ContextID: "context-1", Status: a2a.TaskStateCompleted, PageSize: 25, PageToken: "page-1", HistoryLength: &historyLength}, response: &a2a.ListTasksResponse{Tasks: []*a2a.Task{task}, TotalSize: 3, PageSize: 25, NextPageToken: "page-2"},
			assertRequest: func(t *testing.T, data []byte) {
				var got a2a.ListTasksRequest
				require.NoError(t, json.Unmarshal(data, &got))
				assert.Equal(t, "context-1", got.ContextID)
				assert.Equal(t, a2a.TaskStateCompleted, got.Status)
				assert.Equal(t, 25, got.PageSize)
				assert.Equal(t, "page-1", got.PageToken)
			},
			assertResponse: func(t *testing.T, data []byte) {
				var got a2a.ListTasksResponse
				require.NoError(t, json.Unmarshal(data, &got))
				require.Len(t, got.Tasks, 1)
				assert.Equal(t, a2a.TaskID("task-1"), got.Tasks[0].ID)
				assert.Equal(t, a2a.ArtifactID("artifact-1"), got.Tasks[0].Artifacts[0].ID)
				assert.Equal(t, 3, got.TotalSize)
				assert.Equal(t, 25, got.PageSize)
				assert.Equal(t, "page-2", got.NextPageToken)
			},
		},
		{
			name: "cancel_task", request: &a2a.CancelTaskRequest{Tenant: "tenant-1", ID: "task-1"}, response: &a2a.Task{ID: "task-1", ContextID: "context-1", Status: a2a.TaskStatus{State: a2a.TaskStateCanceled, Message: message}, Artifacts: task.Artifacts},
			assertRequest: func(t *testing.T, data []byte) {
				var got a2a.CancelTaskRequest
				require.NoError(t, json.Unmarshal(data, &got))
				assert.Equal(t, "tenant-1", got.Tenant)
				assert.Equal(t, a2a.TaskID("task-1"), got.ID)
			},
			assertResponse: assertStrictV1Task("task-1", a2a.TaskStateCanceled),
		},
		{
			name: "send_streaming_message_aggregate", request: sendRequest,
			assertRequest: func(t *testing.T, data []byte) {
				var got a2a.SendMessageRequest
				require.NoError(t, json.Unmarshal(data, &got))
				assert.Equal(t, "request-message-1", got.Message.ID)
				assert.Equal(t, "hello", got.Message.Parts[0].Text())
			},
		},
		{
			name: "subscribe_to_task_aggregate", request: &a2a.SubscribeToTaskRequest{Tenant: "tenant-1", ID: "task-1"},
			assertRequest: func(t *testing.T, data []byte) {
				var got a2a.SubscribeToTaskRequest
				require.NoError(t, json.Unmarshal(data, &got))
				assert.Equal(t, "tenant-1", got.Tenant)
				assert.Equal(t, a2a.TaskID("task-1"), got.ID)
			},
		},
		{
			name: "get_extended_agent_card", request: &a2a.GetExtendedAgentCardRequest{Tenant: "tenant-1"}, response: card,
			assertRequest: func(t *testing.T, data []byte) {
				var got a2a.GetExtendedAgentCardRequest
				require.NoError(t, json.Unmarshal(data, &got))
				assert.Equal(t, "tenant-1", got.Tenant)
			},
			assertResponse: func(t *testing.T, data []byte) {
				var got a2a.AgentCard
				require.NoError(t, json.Unmarshal(data, &got))
				assert.Equal(t, "fixture", got.Name)
				assert.Equal(t, "1.0", got.Version)
				require.Len(t, got.SupportedInterfaces, 1)
				assert.Equal(t, a2a.TransportProtocolJSONRPC, got.SupportedInterfaces[0].ProtocolBinding)
				assert.True(t, got.Capabilities.Streaming)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := &AgentLog{RequestBody: marshalStrictV1TestPayload(t, test.request), ResponseBody: marshalStrictV1TestPayload(t, test.response)}
			stored, err := MarshalAgentLogPayload(entry)
			require.NoError(t, err)
			hydrated := &AgentLog{}
			require.NoError(t, MergeAgentLogPayloadFromJSON(hydrated, stored))

			if test.assertRequest == nil {
				assert.Nil(t, hydrated.RequestBody)
			} else {
				require.NotNil(t, hydrated.RequestBody)
				test.assertRequest(t, []byte(*hydrated.RequestBody))
			}
			if test.assertResponse == nil {
				assert.Nil(t, hydrated.ResponseBody, "stream logs retain the typed request and aggregate metadata, not a materialized event stream")
			} else {
				require.NotNil(t, hydrated.ResponseBody)
				test.assertResponse(t, []byte(*hydrated.ResponseBody))
			}
		})
	}
}

func TestMergeAgentLogPayloadFromJSONRejectsMalformedAndUnsupportedSchema(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
		want string
	}{
		{name: "malformed_json", data: `{"schema_version":1`, want: "unmarshal A2A log payload"},
		{name: "unsupported_schema_version", data: `{"schema_version":2}`, want: "unsupported A2A object schema version 2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := MergeAgentLogPayloadFromJSON(&AgentLog{}, []byte(test.data))
			require.ErrorContains(t, err, test.want)
		})
	}
}

func marshalStrictV1TestPayload(t *testing.T, value any) *string {
	t.Helper()
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	require.NoError(t, err)
	body := string(data)
	return &body
}

func assertStrictV1Task(taskID string, state a2a.TaskState) func(*testing.T, []byte) {
	return func(t *testing.T, data []byte) {
		var got a2a.Task
		require.NoError(t, json.Unmarshal(data, &got))
		assert.Equal(t, a2a.TaskID(taskID), got.ID)
		assert.Equal(t, "context-1", got.ContextID)
		assert.Equal(t, state, got.Status.State)
		require.NotNil(t, got.Status.Message)
		assert.NotEmpty(t, got.Status.Message.ID)
		assert.Equal(t, a2a.MessageRoleAgent, got.Status.Message.Role)
		assert.NotEmpty(t, got.Status.Message.Parts[0].Text())
		require.Len(t, got.Artifacts, 1)
		assert.Equal(t, a2a.ArtifactID("artifact-1"), got.Artifacts[0].ID)
		assert.Equal(t, "artifact body", got.Artifacts[0].Parts[0].Text())
	}
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
		PluginLogs: `{"guardrails":[{"message":"arguments redacted"}]}`,
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
	assert.Equal(t, `{"guardrails":[{"message":"arguments redacted"}]}`, entry.PluginLogs)

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

// TestIsPayloadEmpty_PluginLogsOnly verifies a log whose only payload content is
// plugin logs still uploads, so object-store consumers receive its plugin logs.
func TestIsPayloadEmpty_PluginLogsOnly(t *testing.T) {
	assert.True(t, isPayloadEmpty(ExtractPayload(&Log{ID: "empty"})))
	assert.False(t, isPayloadEmpty(ExtractPayload(&Log{ID: "plugin-only", PluginLogs: `{"guardrails":[{"message":"pii detected"}]}`})))
}
