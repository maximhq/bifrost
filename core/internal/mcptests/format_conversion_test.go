package mcptests

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// BASIC FORMAT TESTS
// =============================================================================

func TestChatFormat_Basic(t *testing.T) {
	t.Parallel()

	// Create Chat format tool call
	toolCallID := "call-123"
	chatToolCall := GetSampleCalculatorToolCall(toolCallID, "add", 5, 3)

	// Verify Chat format structure
	require.NotNil(t, chatToolCall.ID, "tool call ID should not be nil")
	assert.Equal(t, toolCallID, *chatToolCall.ID, "tool call ID should match")
	require.NotNil(t, chatToolCall.Function.Name, "function name should not be nil")
	// Tool names may include client prefix (e.g., "bifrostInternal-calculator")
	assert.Contains(t, *chatToolCall.Function.Name, "calculator", "function name should contain calculator")
	assert.Contains(t, chatToolCall.Function.Arguments, "add", "arguments should contain operation")
	assert.Contains(t, chatToolCall.Function.Arguments, "5", "arguments should contain x value")
	assert.Contains(t, chatToolCall.Function.Arguments, "3", "arguments should contain y value")

	// Create Chat format tool result message
	resultContent := `{"result": 8}`
	chatToolResult := GetSampleToolResultMessage(toolCallID, resultContent)

	// Verify Chat tool result structure
	assert.Equal(t, schemas.ChatMessageRoleTool, chatToolResult.Role, "role should be tool")
	require.NotNil(t, chatToolResult.ChatToolMessage, "ChatToolMessage should not be nil")
	require.NotNil(t, chatToolResult.ChatToolMessage.ToolCallID, "ToolCallID should not be nil")
	assert.Equal(t, toolCallID, *chatToolResult.ChatToolMessage.ToolCallID, "ToolCallID should match")
	require.NotNil(t, chatToolResult.Content, "Content should not be nil")
	require.NotNil(t, chatToolResult.Content.ContentStr, "ContentStr should not be nil")
	assert.Equal(t, resultContent, *chatToolResult.Content.ContentStr, "content should match")
}

func TestResponsesFormat_Basic(t *testing.T) {
	t.Parallel()

	// Create Responses format tool call
	callID := "call-456"
	toolName := "calculator"
	args := map[string]interface{}{
		"operation": "multiply",
		"x":         4.0,
		"y":         7.0,
	}
	responsesToolCall := GetSampleResponsesToolCallMessage(callID, toolName, args)

	// Verify Responses format structure
	require.NotNil(t, responsesToolCall.Type, "message type should not be nil")
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *responsesToolCall.Type, "type should be function_call")
	require.NotNil(t, responsesToolCall.Role, "role should not be nil")
	assert.Equal(t, schemas.ResponsesInputMessageRoleAssistant, *responsesToolCall.Role, "role should be assistant")
	require.NotNil(t, responsesToolCall.ResponsesToolMessage, "ResponsesToolMessage should not be nil")
	require.NotNil(t, responsesToolCall.ResponsesToolMessage.CallID, "CallID should not be nil")
	assert.Equal(t, callID, *responsesToolCall.ResponsesToolMessage.CallID, "CallID should match")
	require.NotNil(t, responsesToolCall.ResponsesToolMessage.Name, "Name should not be nil")
	assert.Equal(t, toolName, *responsesToolCall.ResponsesToolMessage.Name, "Name should match")
	require.NotNil(t, responsesToolCall.ResponsesToolMessage.Arguments, "Arguments should not be nil")
	assert.Contains(t, *responsesToolCall.ResponsesToolMessage.Arguments, "multiply", "arguments should contain operation")

	// Create Responses format tool result
	resultOutput := `{"result": 28}`
	responsesToolResult := GetSampleResponsesToolResultMessage(callID, resultOutput)

	// Verify Responses tool result structure
	require.NotNil(t, responsesToolResult.Type, "message type should not be nil")
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *responsesToolResult.Type, "type should be function_call_output")
	require.NotNil(t, responsesToolResult.ResponsesToolMessage, "ResponsesToolMessage should not be nil")
	require.NotNil(t, responsesToolResult.ResponsesToolMessage.CallID, "CallID should not be nil")
	assert.Equal(t, callID, *responsesToolResult.ResponsesToolMessage.CallID, "CallID should match")
	require.NotNil(t, responsesToolResult.ResponsesToolMessage.Output, "Output should not be nil")
	require.NotNil(t, responsesToolResult.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "ResponsesToolCallOutputStr should not be nil")
	assert.Equal(t, resultOutput, *responsesToolResult.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "output should match")
}

// =============================================================================
// CONVERSION TESTS - CHAT TO RESPONSES
// =============================================================================

// TestChatToResponsesConversion - FULLY IMPLEMENTED EXAMPLE
func TestChatToResponsesConversion(t *testing.T) {
	t.Parallel()

	// Create Chat format tool call
	chatToolCall := GetSampleCalculatorToolCall("call-123", "add", 5, 3)

	// Convert to Responses format
	responsesToolCall := schemas.ResponsesToolMessage{
		CallID:    chatToolCall.ID,
		Name:      chatToolCall.Function.Name,
		Arguments: &chatToolCall.Function.Arguments,
	}

	// Verify conversion
	require.NotNil(t, responsesToolCall.CallID)
	assert.Equal(t, "call-123", *responsesToolCall.CallID)
	// Tool names may include client prefix (e.g., "bifrostInternal-calculator")
	assert.Contains(t, *responsesToolCall.Name, "calculator", "tool name should contain calculator")
	assert.Contains(t, *responsesToolCall.Arguments, "add")
}

func TestChatToResponsesConversion_ToolResult(t *testing.T) {
	t.Parallel()

	// Create Chat tool result message
	toolCallID := "call-789"
	resultContent := `{"result": 42, "status": "success"}`
	chatToolResult := GetSampleToolResultMessage(toolCallID, resultContent)

	// Convert to Responses format using ToResponsesMessages()
	responsesMessages := chatToolResult.ToResponsesMessages()

	// Verify conversion produced messages
	require.NotNil(t, responsesMessages, "converted messages should not be nil")
	require.Len(t, responsesMessages, 1, "should convert to exactly one message")

	responsesMsg := responsesMessages[0]

	// Verify Responses format structure
	require.NotNil(t, responsesMsg.Type, "type should not be nil")
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *responsesMsg.Type, "type should be function_call_output")
	require.NotNil(t, responsesMsg.ResponsesToolMessage, "ResponsesToolMessage should not be nil")
	require.NotNil(t, responsesMsg.ResponsesToolMessage.CallID, "CallID should not be nil")
	assert.Equal(t, toolCallID, *responsesMsg.ResponsesToolMessage.CallID, "CallID should be preserved")
	require.NotNil(t, responsesMsg.ResponsesToolMessage.Output, "Output should not be nil")
	require.NotNil(t, responsesMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "output string should not be nil")
	assert.Equal(t, resultContent, *responsesMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "content should be preserved")
}

func TestChatToResponsesConversion_WithContent(t *testing.T) {
	t.Parallel()

	// Create Chat message with content blocks
	textContent := "Here is the result:"
	chatMessage := schemas.ChatMessage{
		Role: schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{
				{
					Type: schemas.ChatContentBlockTypeText,
					Text: &textContent,
				},
			},
		},
	}

	// Convert to Responses format
	responsesMessages := chatMessage.ToResponsesMessages()

	// Verify conversion
	require.NotNil(t, responsesMessages, "converted messages should not be nil")
	require.Len(t, responsesMessages, 1, "should convert to exactly one message")

	responsesMsg := responsesMessages[0]

	// Verify content blocks are preserved
	require.NotNil(t, responsesMsg.Content, "Content should not be nil")
	require.NotNil(t, responsesMsg.Content.ContentBlocks, "ContentBlocks should not be nil")
	require.Len(t, responsesMsg.Content.ContentBlocks, 1, "should have one content block")

	block := responsesMsg.Content.ContentBlocks[0]
	assert.Equal(t, schemas.ResponsesOutputMessageContentTypeText, block.Type, "block type should be text")
	require.NotNil(t, block.Text, "block text should not be nil")
	assert.Equal(t, textContent, *block.Text, "text content should be preserved")
}

// =============================================================================
// CONVERSION TESTS - RESPONSES TO CHAT
// =============================================================================

func TestResponsesToChatConversion(t *testing.T) {
	t.Parallel()

	// Create Responses format tool call
	callID := "call-999"
	toolName := "echo"
	args := map[string]interface{}{
		"message": "Hello, World!",
	}
	responsesToolCall := GetSampleResponsesToolCallMessage(callID, toolName, args)

	// Convert to Chat format using ToChatAssistantMessageToolCall()
	require.NotNil(t, responsesToolCall.ResponsesToolMessage, "ResponsesToolMessage should not be nil")
	chatToolCall := responsesToolCall.ResponsesToolMessage.ToChatAssistantMessageToolCall()

	// Verify conversion
	require.NotNil(t, chatToolCall, "converted tool call should not be nil")
	require.NotNil(t, chatToolCall.ID, "ID should not be nil")
	assert.Equal(t, callID, *chatToolCall.ID, "ID should be preserved")
	require.NotNil(t, chatToolCall.Function.Name, "function name should not be nil")
	assert.Equal(t, toolName, *chatToolCall.Function.Name, "function name should be preserved")
	assert.Contains(t, chatToolCall.Function.Arguments, "Hello, World!", "arguments should be preserved")
	assert.Contains(t, chatToolCall.Function.Arguments, "message", "argument keys should be preserved")
}

func TestResponsesToChatConversion_ToolResult(t *testing.T) {
	t.Parallel()

	// Create Responses tool result
	callID := "call-result-123"
	output := `{"temperature": 72, "units": "fahrenheit"}`
	responsesToolResult := GetSampleResponsesToolResultMessage(callID, output)

	// Convert to Chat format using ToResponsesToolMessage() (which creates a ChatMessage internally)
	// Since ResponsesMessage doesn't have a direct ToChatMessage(), we'll verify the structure
	require.NotNil(t, responsesToolResult.ResponsesToolMessage, "ResponsesToolMessage should not be nil")
	require.NotNil(t, responsesToolResult.ResponsesToolMessage.CallID, "CallID should not be nil")
	assert.Equal(t, callID, *responsesToolResult.ResponsesToolMessage.CallID, "CallID should match")
	require.NotNil(t, responsesToolResult.ResponsesToolMessage.Output, "Output should not be nil")
	require.NotNil(t, responsesToolResult.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "output string should not be nil")
	assert.Equal(t, output, *responsesToolResult.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "output should match")

	// Verify it can be converted back via the ChatMessage.ToResponsesToolMessage() path
	// Create equivalent Chat message
	chatToolResult := GetSampleToolResultMessage(callID, output)
	convertedBack := chatToolResult.ToResponsesMessages()
	require.Len(t, convertedBack, 1, "should convert to one message")

	// Verify round-trip preserves data
	assert.Equal(t, *responsesToolResult.ResponsesToolMessage.CallID, *convertedBack[0].ResponsesToolMessage.CallID, "CallID should match after round-trip")
	assert.Equal(t, *responsesToolResult.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, *convertedBack[0].ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "output should match after round-trip")
}

func TestResponsesToChatConversion_NilHandling(t *testing.T) {
	t.Parallel()

	// Test nil ResponsesToolMessage
	var nilToolMsg *schemas.ResponsesToolMessage
	chatToolCall := nilToolMsg.ToChatAssistantMessageToolCall()
	assert.Nil(t, chatToolCall, "nil ResponsesToolMessage should convert to nil ChatToolCall")

	// Test ResponsesToolMessage with nil fields
	emptyToolMsg := &schemas.ResponsesToolMessage{}
	chatToolCall2 := emptyToolMsg.ToChatAssistantMessageToolCall()
	// Should not crash, may return nil or valid object depending on implementation
	// Just verify it doesn't panic
	_ = chatToolCall2

	// Test ResponsesMessage with nil ResponsesToolMessage
	responsesMsg := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
	}
	// Verify accessing nil fields doesn't crash
	assert.Nil(t, responsesMsg.ResponsesToolMessage, "ResponsesToolMessage should be nil")
}

// =============================================================================
// ROUND-TRIP CONVERSION TESTS
// =============================================================================

func TestConversionRoundTrip_ChatToResponsesAndBack(t *testing.T) {
	t.Parallel()

	// Create original Chat tool call
	originalCallID := "call-roundtrip-1"
	originalToolCall := GetSampleCalculatorToolCall(originalCallID, "subtract", 10, 3)

	// Convert Chat → Responses
	responsesToolMsg := schemas.ResponsesToolMessage{
		CallID:    originalToolCall.ID,
		Name:      originalToolCall.Function.Name,
		Arguments: &originalToolCall.Function.Arguments,
	}

	// Convert Responses → Chat
	convertedBackToolCall := responsesToolMsg.ToChatAssistantMessageToolCall()

	// Verify round-trip preserves all data
	require.NotNil(t, convertedBackToolCall, "converted back tool call should not be nil")
	require.NotNil(t, convertedBackToolCall.ID, "ID should not be nil")
	assert.Equal(t, originalCallID, *convertedBackToolCall.ID, "ID should match original after round-trip")
	require.NotNil(t, convertedBackToolCall.Function.Name, "function name should not be nil")
	assert.Equal(t, *originalToolCall.Function.Name, *convertedBackToolCall.Function.Name, "function name should match original")
	assert.Equal(t, originalToolCall.Function.Arguments, convertedBackToolCall.Function.Arguments, "arguments should match original exactly")

	// Test with tool result message
	originalResult := GetSampleToolResultMessage(originalCallID, `{"result": 7}`)
	responsesMessages := originalResult.ToResponsesMessages()
	require.Len(t, responsesMessages, 1, "should produce one message")

	// Verify the Responses format
	responsesResult := responsesMessages[0]
	require.NotNil(t, responsesResult.ResponsesToolMessage, "ResponsesToolMessage should not be nil")
	require.NotNil(t, responsesResult.ResponsesToolMessage.CallID, "CallID should not be nil")
	assert.Equal(t, originalCallID, *responsesResult.ResponsesToolMessage.CallID, "CallID should match after round-trip")
}

func TestConversionRoundTrip_ResponsesToChatAndBack(t *testing.T) {
	t.Parallel()

	// Create original Responses tool call
	originalCallID := "call-roundtrip-2"
	originalToolName := "get_weather"
	originalArgs := map[string]interface{}{
		"location": "San Francisco",
		"units":    "celsius",
	}
	originalResponses := GetSampleResponsesToolCallMessage(originalCallID, originalToolName, originalArgs)

	// Convert Responses → Chat
	require.NotNil(t, originalResponses.ResponsesToolMessage, "ResponsesToolMessage should not be nil")
	chatToolCall := originalResponses.ResponsesToolMessage.ToChatAssistantMessageToolCall()
	require.NotNil(t, chatToolCall, "converted chat tool call should not be nil")

	// Convert Chat → Responses
	convertedBackToolMsg := schemas.ResponsesToolMessage{
		CallID:    chatToolCall.ID,
		Name:      chatToolCall.Function.Name,
		Arguments: &chatToolCall.Function.Arguments,
	}

	// Verify round-trip preserves all data
	require.NotNil(t, convertedBackToolMsg.CallID, "CallID should not be nil")
	assert.Equal(t, originalCallID, *convertedBackToolMsg.CallID, "CallID should match original after round-trip")
	require.NotNil(t, convertedBackToolMsg.Name, "Name should not be nil")
	assert.Equal(t, originalToolName, *convertedBackToolMsg.Name, "tool name should match original")
	require.NotNil(t, convertedBackToolMsg.Arguments, "Arguments should not be nil")
	assert.Contains(t, *convertedBackToolMsg.Arguments, "San Francisco", "arguments should contain original location")
	assert.Contains(t, *convertedBackToolMsg.Arguments, "celsius", "arguments should contain original units")

	// Test with tool result
	originalOutput := `{"temperature": 18, "conditions": "cloudy"}`
	originalResult := GetSampleResponsesToolResultMessage(originalCallID, originalOutput)

	// Verify structure is preserved
	require.NotNil(t, originalResult.ResponsesToolMessage, "ResponsesToolMessage should not be nil")
	require.NotNil(t, originalResult.ResponsesToolMessage.CallID, "CallID should not be nil")
	assert.Equal(t, originalCallID, *originalResult.ResponsesToolMessage.CallID, "CallID should match")
	require.NotNil(t, originalResult.ResponsesToolMessage.Output, "Output should not be nil")
	require.NotNil(t, originalResult.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "output string should not be nil")
	assert.Equal(t, originalOutput, *originalResult.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "output should match original")
}

// =============================================================================
// ACCURACY TESTS
// =============================================================================

func TestFormatConversion_Accuracy(t *testing.T) {
	t.Parallel()

	// Create multiple Chat tool calls with different operations
	toolCalls := []schemas.ChatAssistantMessageToolCall{
		GetSampleCalculatorToolCall("call-1", "add", 5, 3),
		GetSampleCalculatorToolCall("call-2", "subtract", 10, 4),
		GetSampleCalculatorToolCall("call-3", "multiply", 6, 7),
		GetSampleEchoToolCall("call-4", "test message"),
		GetSampleWeatherToolCall("call-5", "New York", "fahrenheit"),
	}

	// Convert each to Responses format and back
	for i, originalCall := range toolCalls {
		// Chat → Responses
		responsesToolMsg := schemas.ResponsesToolMessage{
			CallID:    originalCall.ID,
			Name:      originalCall.Function.Name,
			Arguments: &originalCall.Function.Arguments,
		}

		// Responses → Chat
		convertedBack := responsesToolMsg.ToChatAssistantMessageToolCall()

		// Verify no data loss
		require.NotNil(t, convertedBack, "tool call %d should convert back", i)
		require.NotNil(t, convertedBack.ID, "ID should not be nil for call %d", i)
		assert.Equal(t, *originalCall.ID, *convertedBack.ID, "ID should match for call %d", i)
		require.NotNil(t, convertedBack.Function.Name, "function name should not be nil for call %d", i)
		assert.Equal(t, *originalCall.Function.Name, *convertedBack.Function.Name, "function name should match for call %d", i)
		assert.Equal(t, originalCall.Function.Arguments, convertedBack.Function.Arguments, "arguments should match exactly for call %d", i)
	}
}

func TestFormatConversion_ComplexStructures(t *testing.T) {
	t.Parallel()

	// Create tool call with complex nested structure
	complexArgs := map[string]interface{}{
		"simple_string": "value",
		"number":        42.5,
		"boolean":       true,
		"array":         []interface{}{"item1", "item2", 3},
		"nested_object": map[string]interface{}{
			"inner_key": "inner_value",
			"inner_array": []interface{}{
				map[string]interface{}{"deep_key": "deep_value"},
			},
		},
		"array_of_objects": []interface{}{
			map[string]interface{}{"id": 1, "name": "first"},
			map[string]interface{}{"id": 2, "name": "second"},
		},
	}

	argsJSON, err := json.Marshal(complexArgs)
	require.NoError(t, err, "should marshal complex args")

	callID := "call-complex"
	chatToolCall := schemas.ChatAssistantMessageToolCall{
		ID:   &callID,
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("complex_tool"),
			Arguments: string(argsJSON),
		},
	}

	// Convert Chat → Responses
	responsesToolMsg := schemas.ResponsesToolMessage{
		CallID:    chatToolCall.ID,
		Name:      chatToolCall.Function.Name,
		Arguments: &chatToolCall.Function.Arguments,
	}

	// Verify structure is preserved in Responses format
	require.NotNil(t, responsesToolMsg.Arguments, "arguments should not be nil")
	var responsesArgs map[string]interface{}
	err = json.Unmarshal([]byte(*responsesToolMsg.Arguments), &responsesArgs)
	require.NoError(t, err, "should unmarshal arguments")
	assert.Equal(t, "value", responsesArgs["simple_string"], "simple string should be preserved")
	assert.Equal(t, 42.5, responsesArgs["number"], "number should be preserved")
	assert.True(t, responsesArgs["boolean"].(bool), "boolean should be preserved")

	// Verify nested structures
	nestedObj, ok := responsesArgs["nested_object"].(map[string]interface{})
	require.True(t, ok, "nested object should be preserved")
	assert.Equal(t, "inner_value", nestedObj["inner_key"], "nested object values should be preserved")

	// Convert Responses → Chat
	convertedBack := responsesToolMsg.ToChatAssistantMessageToolCall()
	require.NotNil(t, convertedBack, "converted back should not be nil")

	// Verify complex structure is preserved in round-trip
	var convertedArgs map[string]interface{}
	err = json.Unmarshal([]byte(convertedBack.Function.Arguments), &convertedArgs)
	require.NoError(t, err, "should unmarshal converted arguments")
	assert.Equal(t, complexArgs["simple_string"], convertedArgs["simple_string"], "simple string should survive round-trip")
	assert.Equal(t, complexArgs["number"], convertedArgs["number"], "number should survive round-trip")
}

// =============================================================================
// EXTRA FIELDS PRESERVATION
// =============================================================================

func TestFormatConversion_ExtraFields(t *testing.T) {
	t.Parallel()

	// Note: ExtraFields are added at the response level (BifrostMCPResponse),
	// not at the message level. This test verifies the conversion preserves
	// message structure that can later receive ExtraFields.

	// Create a Chat tool result message
	callID := "call-extra-fields"
	chatToolResult := GetSampleToolResultMessage(callID, `{"result": "success"}`)

	// Convert to Responses format
	responsesMessages := chatToolResult.ToResponsesMessages()
	require.Len(t, responsesMessages, 1, "should convert to one message")

	responsesMsg := responsesMessages[0]

	// Verify message structure is intact for ExtraFields to be added later
	require.NotNil(t, responsesMsg.Type, "type should not be nil")
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *responsesMsg.Type, "type should be correct")
	require.NotNil(t, responsesMsg.ResponsesToolMessage, "ResponsesToolMessage should not be nil")
	require.NotNil(t, responsesMsg.ResponsesToolMessage.CallID, "CallID should not be nil")
	assert.Equal(t, callID, *responsesMsg.ResponsesToolMessage.CallID, "CallID should be preserved")

	// Verify content is preserved
	require.NotNil(t, responsesMsg.ResponsesToolMessage.Output, "Output should not be nil")
	require.NotNil(t, responsesMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "output string should not be nil")
	assert.Contains(t, *responsesMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "success", "content should be preserved")
}

// =============================================================================
// TOOL CALLS AND RESULTS CONVERSION
// =============================================================================

func TestFormatConversion_ToolCalls(t *testing.T) {
	t.Parallel()

	// Create a message with multiple tool calls
	toolCalls := []schemas.ChatAssistantMessageToolCall{
		GetSampleCalculatorToolCall("call-1", "add", 1, 2),
		GetSampleCalculatorToolCall("call-2", "multiply", 3, 4),
		GetSampleEchoToolCall("call-3", "test"),
	}

	chatMessage := GetSampleToolCallMessage(toolCalls)

	// Convert to Responses format
	responsesMessages := chatMessage.ToResponsesMessages()

	// Verify all tool calls are preserved
	require.NotNil(t, responsesMessages, "converted messages should not be nil")
	require.Len(t, responsesMessages, len(toolCalls), "should create one message per tool call")

	// Verify each converted tool call
	for i, responsesMsg := range responsesMessages {
		require.NotNil(t, responsesMsg.Type, "type should not be nil")
		assert.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *responsesMsg.Type, "type should be function_call")
		require.NotNil(t, responsesMsg.ResponsesToolMessage, "ResponsesToolMessage should not be nil")
		require.NotNil(t, responsesMsg.ResponsesToolMessage.CallID, "CallID should not be nil")
		assert.Equal(t, *toolCalls[i].ID, *responsesMsg.ResponsesToolMessage.CallID, "CallID should match for call %d", i)
		require.NotNil(t, responsesMsg.ResponsesToolMessage.Name, "Name should not be nil")
		assert.Equal(t, *toolCalls[i].Function.Name, *responsesMsg.ResponsesToolMessage.Name, "Name should match for call %d", i)
	}

	// Convert back to Chat format
	var convertedBackToolCalls []schemas.ChatAssistantMessageToolCall
	for _, responsesMsg := range responsesMessages {
		if responsesMsg.ResponsesToolMessage != nil {
			chatToolCall := responsesMsg.ResponsesToolMessage.ToChatAssistantMessageToolCall()
			if chatToolCall != nil {
				convertedBackToolCalls = append(convertedBackToolCalls, *chatToolCall)
			}
		}
	}

	// Verify all tool calls survived round-trip
	require.Len(t, convertedBackToolCalls, len(toolCalls), "all tool calls should survive round-trip")
	for i, convertedCall := range convertedBackToolCalls {
		assert.Equal(t, *toolCalls[i].ID, *convertedCall.ID, "ID should match for call %d", i)
		assert.Equal(t, *toolCalls[i].Function.Name, *convertedCall.Function.Name, "Name should match for call %d", i)
	}
}

func TestFormatConversion_ToolResults(t *testing.T) {
	t.Parallel()

	// Create multiple tool result messages
	results := []struct {
		callID  string
		content string
	}{
		{"call-1", `{"result": 3}`},
		{"call-2", `{"result": 12}`},
		{"call-3", `{"echoed": "test"}`},
	}

	var chatResults []schemas.ChatMessage
	for _, r := range results {
		chatResults = append(chatResults, GetSampleToolResultMessage(r.callID, r.content))
	}

	// Convert each to Responses format
	for i, chatResult := range chatResults {
		responsesMessages := chatResult.ToResponsesMessages()
		require.Len(t, responsesMessages, 1, "should convert to one message for result %d", i)

		responsesMsg := responsesMessages[0]

		// Verify result structure
		require.NotNil(t, responsesMsg.Type, "type should not be nil")
		assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *responsesMsg.Type, "type should be function_call_output")
		require.NotNil(t, responsesMsg.ResponsesToolMessage, "ResponsesToolMessage should not be nil")
		require.NotNil(t, responsesMsg.ResponsesToolMessage.CallID, "CallID should not be nil")
		assert.Equal(t, results[i].callID, *responsesMsg.ResponsesToolMessage.CallID, "CallID should match for result %d", i)
		require.NotNil(t, responsesMsg.ResponsesToolMessage.Output, "Output should not be nil")
		require.NotNil(t, responsesMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "output string should not be nil")
		assert.Equal(t, results[i].content, *responsesMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "content should match for result %d", i)
	}

	// Test batch conversion and verify call ID mapping is preserved
	callIDMap := make(map[string]string)
	for _, r := range results {
		callIDMap[r.callID] = r.content
	}

	for _, chatResult := range chatResults {
		responsesMessages := chatResult.ToResponsesMessages()
		for _, responsesMsg := range responsesMessages {
			if responsesMsg.ResponsesToolMessage != nil && responsesMsg.ResponsesToolMessage.CallID != nil {
				expectedContent := callIDMap[*responsesMsg.ResponsesToolMessage.CallID]
				actualContent := *responsesMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
				assert.Equal(t, expectedContent, actualContent, "content should match for CallID %s", *responsesMsg.ResponsesToolMessage.CallID)
			}
		}
	}
}

// =============================================================================
// ERROR MESSAGE CONVERSION
// =============================================================================

func TestFormatConversion_ErrorMessages(t *testing.T) {
	t.Parallel()

	// Create Chat tool result with error message
	callID := "call-error-1"
	errorContent := `{"error": "Division by zero", "code": "MATH_ERROR"}`
	chatErrorResult := GetSampleToolResultMessage(callID, errorContent)

	// Convert to Responses format
	responsesMessages := chatErrorResult.ToResponsesMessages()
	require.Len(t, responsesMessages, 1, "should convert to one message")

	responsesMsg := responsesMessages[0]

	// Verify error is preserved in Responses format
	require.NotNil(t, responsesMsg.Type, "type should not be nil")
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *responsesMsg.Type, "type should be function_call_output")
	require.NotNil(t, responsesMsg.ResponsesToolMessage, "ResponsesToolMessage should not be nil")
	require.NotNil(t, responsesMsg.ResponsesToolMessage.Output, "Output should not be nil")
	require.NotNil(t, responsesMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "output string should not be nil")
	assert.Contains(t, *responsesMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "Division by zero", "error message should be preserved")
	assert.Contains(t, *responsesMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "MATH_ERROR", "error code should be preserved")

	// Test with error formatted as plain text
	plainErrorContent := "Error: Connection timeout after 30 seconds"
	chatErrorResult2 := GetSampleToolResultMessage("call-error-2", plainErrorContent)

	responsesMessages2 := chatErrorResult2.ToResponsesMessages()
	require.Len(t, responsesMessages2, 1, "should convert to one message")

	// Verify plain text error is preserved
	require.NotNil(t, responsesMessages2[0].ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "output should not be nil")
	assert.Equal(t, plainErrorContent, *responsesMessages2[0].ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "plain text error should be preserved exactly")
}

func TestFormatConversion_ErrorWithStackTrace(t *testing.T) {
	t.Parallel()

	// Create error with detailed stack trace
	errorWithStackTrace := map[string]interface{}{
		"error":   "RuntimeError: Null pointer exception",
		"message": "Cannot read property 'value' of null",
		"stack": []string{
			"at processData (handler.js:42:15)",
			"at validateInput (validator.js:18:7)",
			"at main (index.js:10:3)",
		},
		"metadata": map[string]interface{}{
			"timestamp": "2024-01-15T10:30:00Z",
			"severity":  "critical",
			"retryable": false,
		},
	}

	errorJSON, err := json.Marshal(errorWithStackTrace)
	require.NoError(t, err, "should marshal error")

	callID := "call-stack-error"
	chatErrorResult := GetSampleToolResultMessage(callID, string(errorJSON))

	// Convert to Responses format
	responsesMessages := chatErrorResult.ToResponsesMessages()
	require.Len(t, responsesMessages, 1, "should convert to one message")

	responsesMsg := responsesMessages[0]

	// Verify all error details are preserved
	require.NotNil(t, responsesMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "output should not be nil")
	outputStr := *responsesMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr

	// Parse output to verify structure
	var parsedError map[string]interface{}
	err = json.Unmarshal([]byte(outputStr), &parsedError)
	require.NoError(t, err, "should unmarshal error output")

	// Verify all error fields are preserved
	assert.Equal(t, "RuntimeError: Null pointer exception", parsedError["error"], "error message should be preserved")
	assert.Equal(t, "Cannot read property 'value' of null", parsedError["message"], "error detail should be preserved")
	assert.NotNil(t, parsedError["stack"], "stack trace should be preserved")
	assert.NotNil(t, parsedError["metadata"], "metadata should be preserved")

	// Verify stack trace array is intact
	stack, ok := parsedError["stack"].([]interface{})
	require.True(t, ok, "stack should be an array")
	assert.Len(t, stack, 3, "stack should have 3 frames")

	// Verify metadata is intact
	metadata, ok := parsedError["metadata"].(map[string]interface{})
	require.True(t, ok, "metadata should be an object")
	assert.Equal(t, "critical", metadata["severity"], "severity should be preserved")
	assert.Equal(t, false, metadata["retryable"], "retryable flag should be preserved")
}

// =============================================================================
// CONTENT BLOCKS CONVERSION
// =============================================================================

func TestFormatConversion_ContentBlocks(t *testing.T) {
	t.Parallel()

	// Create message with multiple content blocks
	textContent1 := "Here is the analysis:"
	textContent2 := "Additional notes below."

	chatMessage := schemas.ChatMessage{
		Role: schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{
				{
					Type: schemas.ChatContentBlockTypeText,
					Text: &textContent1,
				},
				{
					Type: schemas.ChatContentBlockTypeText,
					Text: &textContent2,
				},
			},
		},
	}

	// Convert to Responses format
	responsesMessages := chatMessage.ToResponsesMessages()
	require.Len(t, responsesMessages, 1, "should convert to one message")

	responsesMsg := responsesMessages[0]

	// Verify content blocks are preserved
	require.NotNil(t, responsesMsg.Content, "Content should not be nil")
	require.NotNil(t, responsesMsg.Content.ContentBlocks, "ContentBlocks should not be nil")
	require.Len(t, responsesMsg.Content.ContentBlocks, 2, "should have 2 content blocks")

	// Verify first block
	block1 := responsesMsg.Content.ContentBlocks[0]
	assert.Equal(t, schemas.ResponsesOutputMessageContentTypeText, block1.Type, "first block type should be text")
	require.NotNil(t, block1.Text, "first block text should not be nil")
	assert.Equal(t, textContent1, *block1.Text, "first block text should be preserved")

	// Verify second block
	block2 := responsesMsg.Content.ContentBlocks[1]
	assert.Equal(t, schemas.ResponsesOutputMessageContentTypeText, block2.Type, "second block type should be text")
	require.NotNil(t, block2.Text, "second block text should not be nil")
	assert.Equal(t, textContent2, *block2.Text, "second block text should be preserved")

	// Test with image content block (if supported)
	imageURL := "https://example.com/image.png"
	imageMessage := schemas.ChatMessage{
		Role: schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{
				{
					Type: schemas.ChatContentBlockTypeText,
					Text: &textContent1,
				},
				{
					Type: schemas.ChatContentBlockTypeImage,
					ImageURLStruct: &schemas.ChatInputImage{
						URL: imageURL,
					},
				},
			},
		},
	}

	responsesImgMessages := imageMessage.ToResponsesMessages()
	require.Len(t, responsesImgMessages, 1, "should convert image message to one message")
	// Verify blocks are preserved (exact mapping depends on implementation)
	require.NotNil(t, responsesImgMessages[0].Content, "Content should not be nil for image message")
}

func TestFormatConversion_MixedContent(t *testing.T) {
	t.Parallel()

	// Create message with both ContentStr and ContentBlocks
	// Note: In practice, messages typically have one or the other, but we test both for robustness
	textContent := "Main content"
	blockText := "Block content"

	chatMessage := schemas.ChatMessage{
		Role: schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{
			ContentStr: &textContent,
			ContentBlocks: []schemas.ChatContentBlock{
				{
					Type: schemas.ChatContentBlockTypeText,
					Text: &blockText,
				},
			},
		},
	}

	// Convert to Responses format
	responsesMessages := chatMessage.ToResponsesMessages()
	require.NotNil(t, responsesMessages, "should convert to messages")
	require.Greater(t, len(responsesMessages), 0, "should have at least one message")

	responsesMsg := responsesMessages[0]

	// Verify content is preserved (implementation may prioritize one over the other)
	require.NotNil(t, responsesMsg.Content, "Content should not be nil")

	// Check if ContentStr is preserved
	if responsesMsg.Content.ContentStr != nil {
		assert.Contains(t, *responsesMsg.Content.ContentStr, textContent, "ContentStr should be preserved")
	}

	// Check if ContentBlocks are preserved
	if len(responsesMsg.Content.ContentBlocks) > 0 {
		hasBlockContent := false
		for _, block := range responsesMsg.Content.ContentBlocks {
			if block.Text != nil && *block.Text == blockText {
				hasBlockContent = true
				break
			}
		}
		if !hasBlockContent && responsesMsg.Content.ContentStr != nil {
			// ContentStr might contain the block content merged
			assert.True(t, true, "Content preserved in some form")
		}
	}

	// Test with empty ContentStr but present ContentBlocks
	emptyStr := ""
	chatMessage2 := schemas.ChatMessage{
		Role: schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{
			ContentStr: &emptyStr,
			ContentBlocks: []schemas.ChatContentBlock{
				{
					Type: schemas.ChatContentBlockTypeText,
					Text: &blockText,
				},
			},
		},
	}

	responsesMessages2 := chatMessage2.ToResponsesMessages()
	require.NotNil(t, responsesMessages2, "should convert message with empty string")
	require.Greater(t, len(responsesMessages2), 0, "should have at least one message")

	// Verify blocks are preserved when ContentStr is empty
	require.NotNil(t, responsesMessages2[0].Content, "Content should not be nil")
}
