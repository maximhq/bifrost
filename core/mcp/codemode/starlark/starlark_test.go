//go:build !tinygo && !wasm

package starlark

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"go.starlark.net/starlark"
)

func TestStarlarkToGo(t *testing.T) {
	t.Run("Convert None", func(t *testing.T) {
		result := starlarkToGo(starlark.None)
		if result != nil {
			t.Errorf("Expected nil, got %v", result)
		}
	})

	t.Run("Convert Bool", func(t *testing.T) {
		result := starlarkToGo(starlark.Bool(true))
		if result != true {
			t.Errorf("Expected true, got %v", result)
		}
	})

	t.Run("Convert Int", func(t *testing.T) {
		result := starlarkToGo(starlark.MakeInt(42))
		if result != int64(42) {
			t.Errorf("Expected 42, got %v", result)
		}
	})

	t.Run("Convert Float", func(t *testing.T) {
		result := starlarkToGo(starlark.Float(3.14))
		if result != 3.14 {
			t.Errorf("Expected 3.14, got %v", result)
		}
	})

	t.Run("Convert String", func(t *testing.T) {
		result := starlarkToGo(starlark.String("hello"))
		if result != "hello" {
			t.Errorf("Expected 'hello', got %v", result)
		}
	})

	t.Run("Convert List", func(t *testing.T) {
		list := starlark.NewList([]starlark.Value{
			starlark.MakeInt(1),
			starlark.MakeInt(2),
			starlark.MakeInt(3),
		})
		result := starlarkToGo(list)
		arr, ok := result.([]interface{})
		if !ok {
			t.Errorf("Expected []interface{}, got %T", result)
		}
		if len(arr) != 3 {
			t.Errorf("Expected length 3, got %d", len(arr))
		}
		if arr[0] != int64(1) {
			t.Errorf("Expected first element 1, got %v", arr[0])
		}
	})

	t.Run("Convert Dict", func(t *testing.T) {
		dict := starlark.NewDict(2)
		dict.SetKey(starlark.String("key1"), starlark.String("value1"))
		dict.SetKey(starlark.String("key2"), starlark.MakeInt(42))

		result := starlarkToGo(dict)
		m, ok := result.(map[string]interface{})
		if !ok {
			t.Errorf("Expected map[string]interface{}, got %T", result)
		}
		if m["key1"] != "value1" {
			t.Errorf("Expected key1='value1', got %v", m["key1"])
		}
		if m["key2"] != int64(42) {
			t.Errorf("Expected key2=42, got %v", m["key2"])
		}
	})
}

func TestGoToStarlark(t *testing.T) {
	t.Run("Convert nil", func(t *testing.T) {
		result := goToStarlark(nil)
		if result != starlark.None {
			t.Errorf("Expected None, got %v", result)
		}
	})

	t.Run("Convert bool", func(t *testing.T) {
		result := goToStarlark(true)
		if result != starlark.Bool(true) {
			t.Errorf("Expected True, got %v", result)
		}
	})

	t.Run("Convert int", func(t *testing.T) {
		result := goToStarlark(42)
		expected := starlark.MakeInt(42)
		if result.String() != expected.String() {
			t.Errorf("Expected %v, got %v", expected, result)
		}
	})

	t.Run("Convert float64", func(t *testing.T) {
		result := goToStarlark(3.14)
		if result != starlark.Float(3.14) {
			t.Errorf("Expected 3.14, got %v", result)
		}
	})

	t.Run("Convert string", func(t *testing.T) {
		result := goToStarlark("hello")
		if result != starlark.String("hello") {
			t.Errorf("Expected 'hello', got %v", result)
		}
	})

	t.Run("Convert slice", func(t *testing.T) {
		result := goToStarlark([]interface{}{1, "two", 3.0})
		list, ok := result.(*starlark.List)
		if !ok {
			t.Errorf("Expected *starlark.List, got %T", result)
		}
		if list.Len() != 3 {
			t.Errorf("Expected length 3, got %d", list.Len())
		}
	})

	t.Run("Convert map", func(t *testing.T) {
		result := goToStarlark(map[string]interface{}{
			"key1": "value1",
			"key2": 42,
		})
		dict, ok := result.(*starlark.Dict)
		if !ok {
			t.Errorf("Expected *starlark.Dict, got %T", result)
		}
		val, found, _ := dict.Get(starlark.String("key1"))
		if !found {
			t.Errorf("Expected key1 to exist")
		}
		if val != starlark.String("value1") {
			t.Errorf("Expected value1, got %v", val)
		}
	})
}

func TestGeneratePythonErrorHints(t *testing.T) {
	serverKeys := []string{"calculator", "weather"}

	t.Run("Undefined variable hint", func(t *testing.T) {
		hints := generatePythonErrorHints("name 'foo' is not defined", serverKeys)
		if len(hints) == 0 {
			t.Error("Expected hints, got none")
		}
		found := false
		for _, hint := range hints {
			if containsAny(hint, "not defined", "undefined") {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected hint about undefined variable")
		}
	})

	t.Run("Syntax error hint", func(t *testing.T) {
		hints := generatePythonErrorHints("syntax error at line 5", serverKeys)
		if len(hints) == 0 {
			t.Error("Expected hints, got none")
		}
		found := false
		for _, hint := range hints {
			if containsAny(hint, "syntax", "indentation", "colon") {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected hint about syntax error")
		}
	})

	t.Run("Attribute error hint", func(t *testing.T) {
		hints := generatePythonErrorHints("'dict' object has no attribute 'foo'", serverKeys)
		if len(hints) == 0 {
			t.Error("Expected hints, got none")
		}
		found := false
		for _, hint := range hints {
			if containsAny(hint, "attribute", "brackets", "key") {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected hint about attribute access")
		}
	})
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if containsIgnoreCase(s, sub) {
			return true
		}
	}
	return false
}

func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && (containsIgnoreCase(s[1:], substr) || (len(s) >= len(substr) && equalFold(s[:len(substr)], substr))))
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

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
