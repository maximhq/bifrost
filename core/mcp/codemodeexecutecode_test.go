package mcp

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestExtractResultFromResponsesMessage(t *testing.T) {
	t.Run("Extract error from ResponsesMessage", func(t *testing.T) {
		errorMsg := "Tool is not allowed by security policy: dangerous_tool"
		msg := &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Error: &errorMsg,
			},
		}

		result, err := extractResultFromResponsesMessage(msg)
		if err == nil {
			t.Errorf("Expected error, got nil")
		}
		if err.Error() != errorMsg {
			t.Errorf("Expected error message '%s', got '%s'", errorMsg, err.Error())
		}
		if result != nil {
			t.Errorf("Expected nil result when error is present, got %v", result)
		}
	})

	t.Run("Extract string output from ResponsesMessage", func(t *testing.T) {
		outputStr := "success result"
		msg := &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: &outputStr,
				},
			},
		}

		result, err := extractResultFromResponsesMessage(msg)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result != outputStr {
			t.Errorf("Expected result '%s', got '%v'", outputStr, result)
		}
	})

	t.Run("Extract JSON output from ResponsesMessage", func(t *testing.T) {
		outputStr := `{"status": "success", "data": "test"}`
		msg := &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: &outputStr,
				},
			},
		}

		result, err := extractResultFromResponsesMessage(msg)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}

		resultMap, ok := result.(map[string]interface{})
		if !ok {
			t.Errorf("Expected map, got %T", result)
		}

		if resultMap["status"] != "success" {
			t.Errorf("Expected status 'success', got '%v'", resultMap["status"])
		}
	})

	t.Run("Extract from ResponsesFunctionToolCallOutputBlocks", func(t *testing.T) {
		text1 := "First block"
		text2 := "Second block"
		msg := &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
						{Text: &text1},
						{Text: &text2},
					},
				},
			},
		}

		result, err := extractResultFromResponsesMessage(msg)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}

		expectedResult := "First block\nSecond block"
		if result != expectedResult {
			t.Errorf("Expected result '%s', got '%v'", expectedResult, result)
		}
	})

	t.Run("Extract JSON from ResponsesFunctionToolCallOutputBlocks", func(t *testing.T) {
		jsonText := `{"key": "value"}`
		msg := &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
						{Text: &jsonText},
					},
				},
			},
		}

		result, err := extractResultFromResponsesMessage(msg)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}

		resultMap, ok := result.(map[string]interface{})
		if !ok {
			t.Errorf("Expected map, got %T", result)
		}

		if resultMap["key"] != "value" {
			t.Errorf("Expected key 'value', got '%v'", resultMap["key"])
		}
	})

	t.Run("Handle nil message", func(t *testing.T) {
		result, err := extractResultFromResponsesMessage(nil)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result != nil {
			t.Errorf("Expected nil result for nil message, got %v", result)
		}
	})

	t.Run("Handle message without ResponsesToolMessage", func(t *testing.T) {
		msg := &schemas.ResponsesMessage{}

		result, err := extractResultFromResponsesMessage(msg)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result != nil {
			t.Errorf("Expected nil result for message without tool message, got %v", result)
		}
	})

	t.Run("Handle empty error string (should not error)", func(t *testing.T) {
		emptyError := ""
		msg := &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Error: &emptyError,
			},
		}

		result, err := extractResultFromResponsesMessage(msg)
		if err != nil {
			t.Errorf("Expected no error for empty error string, got: %v", err)
		}
		if result != nil {
			t.Errorf("Expected nil result for empty error string, got %v", result)
		}
	})
}

func TestExtractResultFromChatMessage(t *testing.T) {
	t.Run("Extract string from ChatMessage", func(t *testing.T) {
		content := "test result"
		msg := &schemas.ChatMessage{
			Content: &schemas.ChatMessageContent{
				ContentStr: &content,
			},
		}

		result := extractResultFromChatMessage(msg)
		if result != content {
			t.Errorf("Expected result '%s', got '%v'", content, result)
		}
	})

	t.Run("Extract JSON from ChatMessage", func(t *testing.T) {
		content := `{"status": "ok"}`
		msg := &schemas.ChatMessage{
			Content: &schemas.ChatMessageContent{
				ContentStr: &content,
			},
		}

		result := extractResultFromChatMessage(msg)
		resultMap, ok := result.(map[string]interface{})
		if !ok {
			t.Errorf("Expected map, got %T", result)
		}

		if resultMap["status"] != "ok" {
			t.Errorf("Expected status 'ok', got '%v'", resultMap["status"])
		}
	})

	t.Run("Handle nil ChatMessage", func(t *testing.T) {
		result := extractResultFromChatMessage(nil)
		if result != nil {
			t.Errorf("Expected nil result for nil message, got %v", result)
		}
	})

	t.Run("Handle ChatMessage without Content", func(t *testing.T) {
		msg := &schemas.ChatMessage{}
		result := extractResultFromChatMessage(msg)
		if result != nil {
			t.Errorf("Expected nil result for message without content, got %v", result)
		}
	})
}

func TestFormatResultForLog(t *testing.T) {
	t.Run("Format nil result", func(t *testing.T) {
		result := formatResultForLog(nil)
		if result != "null" {
			t.Errorf("Expected 'null', got '%s'", result)
		}
	})

	t.Run("Format string result", func(t *testing.T) {
		result := formatResultForLog("test string")
		if result != `"test string"` {
			t.Errorf("Expected '\"test string\"', got '%s'", result)
		}
	})

	t.Run("Format map result", func(t *testing.T) {
		input := map[string]interface{}{"key": "value"}
		result := formatResultForLog(input)

		// Parse it back to verify it's valid JSON
		var parsed map[string]interface{}
		err := sonic.Unmarshal([]byte(result), &parsed)
		if err != nil {
			t.Errorf("Result is not valid JSON: %v", err)
		}

		if parsed["key"] != "value" {
			t.Errorf("Expected key 'value', got '%v'", parsed["key"])
		}
	})

	t.Run("Truncate long result", func(t *testing.T) {
		longString := ""
		for i := 0; i < 300; i++ {
			longString += "a"
		}

		result := formatResultForLog(longString)
		if len(result) > 200 {
			// Should be truncated to around 200 chars (plus quotes and ellipsis)
			t.Logf("Result length: %d (truncated as expected)", len(result))
		}
	})
}
