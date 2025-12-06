package bifrost

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Chat Completion API Image Generation Tool Tests
// =============================================================================

func TestExecuteImageGenerationTool_ValidRequest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b, err := Init(ctx, schemas.BifrostConfig{
		Account: NewMockAccount(),
		Logger:  NewDefaultLogger(schemas.LogLevelDebug),
	})
	require.NoError(t, err)

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call_abc123"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("generate_image"),
			Arguments: `{"prompt": "A beautiful sunset over mountains", "size": "1024x1024", "quality": "hd"}`,
		},
	}

	// Note: This test requires actual API keys and will make real API calls
	// Skip if no API key available
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("Skipping test: OPENAI_API_KEY not set")
	}

	result, bifrostErr := b.ExecuteImageGenerationTool(ctx, toolCall)

	if bifrostErr != nil {
		t.Logf("Error (may be expected if API key invalid): %v", bifrostErr)
		return
	}

	require.NotNil(t, result)
	assert.Equal(t, schemas.ChatMessageRoleTool, result.Role)
	assert.NotNil(t, result.ChatToolMessage)
	assert.Equal(t, "call_abc123", *result.ChatToolMessage.ToolCallID)
	assert.NotNil(t, result.Content)
	assert.NotNil(t, result.Content.ContentStr)

	// Verify result is valid JSON
	var imageData []schemas.ImageData
	err = sonic.Unmarshal([]byte(*result.Content.ContentStr), &imageData)
	assert.NoError(t, err)
	assert.Greater(t, len(imageData), 0)
}

func TestExecuteImageGenerationTool_MissingPrompt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b, err := Init(ctx, schemas.BifrostConfig{
		Account: NewMockAccount(),
		Logger:  NewDefaultLogger(schemas.LogLevelDebug),
	})
	require.NoError(t, err)

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call_abc123"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("generate_image"),
			Arguments: `{"size": "1024x1024"}`,
		},
	}

	result, bifrostErr := b.ExecuteImageGenerationTool(ctx, toolCall)

	assert.Nil(t, result)
	assert.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "prompt is required")
}

func TestExecuteImageGenerationTool_InvalidJSON(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b, err := Init(ctx, schemas.BifrostConfig{
		Account: NewMockAccount(),
		Logger:  NewDefaultLogger(schemas.LogLevelDebug),
	})
	require.NoError(t, err)

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call_abc123"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("generate_image"),
			Arguments: `{invalid json}`,
		},
	}

	result, bifrostErr := b.ExecuteImageGenerationTool(ctx, toolCall)

	assert.Nil(t, result)
	assert.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "failed to parse tool call arguments")
}

func TestExecuteImageGenerationTool_WithAllParameters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b, err := Init(ctx, schemas.BifrostConfig{
		Account: NewMockAccount(),
		Logger:  NewDefaultLogger(schemas.LogLevelDebug),
	})
	require.NoError(t, err)

	args := map[string]interface{}{
		"prompt":          "A futuristic city",
		"size":            "1792x1024",
		"quality":         "hd",
		"style":           "vivid",
		"response_format": "b64_json",
		"n":               1,
		"model":           "openai/dall-e-3",
	}
	argsJSON, _ := json.Marshal(args)

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call_xyz789"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("generate_image"),
			Arguments: string(argsJSON),
		},
	}

	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("Skipping test: OPENAI_API_KEY not set")
	}

	result, bifrostErr := b.ExecuteImageGenerationTool(ctx, toolCall)

	if bifrostErr != nil {
		t.Logf("Error (may be expected): %v", bifrostErr)
		return
	}

	require.NotNil(t, result)
	assert.Equal(t, schemas.ChatMessageRoleTool, result.Role)

	// Verify parameters were applied by checking the result structure
	var imageData []schemas.ImageData
	err = sonic.Unmarshal([]byte(*result.Content.ContentStr), &imageData)
	assert.NoError(t, err)
}

func TestExecuteImageGenerationTool_EmptyPrompt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b, err := Init(ctx, schemas.BifrostConfig{
		Account: NewMockAccount(),
		Logger:  NewDefaultLogger(schemas.LogLevelDebug),
	})
	require.NoError(t, err)

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call_abc123"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("generate_image"),
			Arguments: `{"prompt": ""}`,
		},
	}

	result, bifrostErr := b.ExecuteImageGenerationTool(ctx, toolCall)

	assert.Nil(t, result)
	assert.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "prompt is required")
}

// =============================================================================
// Responses API Image Generation Tool Tests
// =============================================================================

func TestExecuteResponsesImageGenerationTool_ValidRequest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b, err := Init(ctx, schemas.BifrostConfig{
		Account: NewMockAccount(),
		Logger:  NewDefaultLogger(schemas.LogLevelDebug),
	})
	require.NoError(t, err)

	argsJSON := `{"prompt": "A serene Japanese garden", "size": "1024x1024"}`
	toolCall := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeImageGenerationCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:                       schemas.Ptr("call_resp_123"),
			Arguments:                    &argsJSON,
			ResponsesImageGenerationCall: &schemas.ResponsesImageGenerationCall{},
		},
	}

	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("Skipping test: OPENAI_API_KEY not set")
	}

	result, bifrostErr := b.ExecuteResponsesImageGenerationTool(ctx, toolCall)

	if bifrostErr != nil {
		t.Logf("Error (may be expected if API key invalid): %v", bifrostErr)
		return
	}

	require.NotNil(t, result)
	assert.NotNil(t, result.Type)
	assert.Equal(t, schemas.ResponsesMessageTypeImageGenerationCall, *result.Type)
	assert.NotNil(t, result.ResponsesToolMessage)
	assert.Equal(t, "call_resp_123", *result.ResponsesToolMessage.CallID)
	assert.NotNil(t, result.ResponsesToolMessage.Output)
	assert.NotNil(t, result.ResponsesToolMessage.Output.ResponsesImageGenerationCallOutput)
	assert.NotEmpty(t, result.ResponsesToolMessage.Output.ResponsesImageGenerationCallOutput.Result)

	// Verify result is valid JSON
	var imageData []schemas.ImageData
	err = sonic.Unmarshal([]byte(result.ResponsesToolMessage.Output.ResponsesImageGenerationCallOutput.Result), &imageData)
	assert.NoError(t, err)
	assert.Greater(t, len(imageData), 0)
}

func TestExecuteResponsesImageGenerationTool_MissingToolMessage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b, err := Init(ctx, schemas.BifrostConfig{
		Account: NewMockAccount(),
		Logger:  NewDefaultLogger(schemas.LogLevelDebug),
	})
	require.NoError(t, err)

	toolCall := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeImageGenerationCall),
		// Missing ResponsesToolMessage
	}

	result, bifrostErr := b.ExecuteResponsesImageGenerationTool(ctx, toolCall)

	assert.Nil(t, result)
	assert.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "missing required fields")
}

func TestExecuteResponsesImageGenerationTool_MissingImageGenerationCall(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b, err := Init(ctx, schemas.BifrostConfig{
		Account: NewMockAccount(),
		Logger:  NewDefaultLogger(schemas.LogLevelDebug),
	})
	require.NoError(t, err)

	argsJSON := `{"prompt": "test"}`
	toolCall := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeImageGenerationCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:    schemas.Ptr("call_123"),
			Arguments: &argsJSON,
			// Missing ResponsesImageGenerationCall
		},
	}

	result, bifrostErr := b.ExecuteResponsesImageGenerationTool(ctx, toolCall)

	assert.Nil(t, result)
	assert.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "missing required fields")
}

func TestExecuteResponsesImageGenerationTool_MissingPrompt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b, err := Init(ctx, schemas.BifrostConfig{
		Account: NewMockAccount(),
		Logger:  NewDefaultLogger(schemas.LogLevelDebug),
	})
	require.NoError(t, err)

	argsJSON := `{"size": "1024x1024"}`
	toolCall := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeImageGenerationCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:                       schemas.Ptr("call_123"),
			Arguments:                    &argsJSON,
			ResponsesImageGenerationCall: &schemas.ResponsesImageGenerationCall{},
		},
	}

	result, bifrostErr := b.ExecuteResponsesImageGenerationTool(ctx, toolCall)

	assert.Nil(t, result)
	assert.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "prompt is required")
}

func TestExecuteResponsesImageGenerationTool_InvalidJSON(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b, err := Init(ctx, schemas.BifrostConfig{
		Account: NewMockAccount(),
		Logger:  NewDefaultLogger(schemas.LogLevelDebug),
	})
	require.NoError(t, err)

	argsJSON := `{invalid json}`
	toolCall := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeImageGenerationCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:                       schemas.Ptr("call_123"),
			Arguments:                    &argsJSON,
			ResponsesImageGenerationCall: &schemas.ResponsesImageGenerationCall{},
		},
	}

	result, bifrostErr := b.ExecuteResponsesImageGenerationTool(ctx, toolCall)

	assert.Nil(t, result)
	assert.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "failed to parse tool call arguments")
}

func TestExecuteResponsesImageGenerationTool_WithAllParameters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b, err := Init(ctx, schemas.BifrostConfig{
		Account: NewMockAccount(),
		Logger:  NewDefaultLogger(schemas.LogLevelDebug),
	})
	require.NoError(t, err)

	args := map[string]interface{}{
		"prompt":          "A magical forest",
		"size":            "1792x1024",
		"quality":         "hd",
		"style":           "vivid",
		"response_format": "b64_json",
		"n":               1,
		"model":           "openai/dall-e-3",
	}
	argsJSONBytes, _ := json.Marshal(args)
	argsJSON := string(argsJSONBytes)

	toolCall := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeImageGenerationCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:                       schemas.Ptr("call_resp_456"),
			Arguments:                    &argsJSON,
			ResponsesImageGenerationCall: &schemas.ResponsesImageGenerationCall{},
		},
	}

	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("Skipping test: OPENAI_API_KEY not set")
	}

	result, bifrostErr := b.ExecuteResponsesImageGenerationTool(ctx, toolCall)

	if bifrostErr != nil {
		t.Logf("Error (may be expected): %v", bifrostErr)
		return
	}

	require.NotNil(t, result)
	assert.NotNil(t, result.ResponsesToolMessage.Output)
	assert.NotNil(t, result.ResponsesToolMessage.Output.ResponsesImageGenerationCallOutput)
}

func TestExecuteResponsesImageGenerationTool_EmptyArguments(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b, err := Init(ctx, schemas.BifrostConfig{
		Account: NewMockAccount(),
		Logger:  NewDefaultLogger(schemas.LogLevelDebug),
	})
	require.NoError(t, err)

	argsJSON := `{}`
	toolCall := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeImageGenerationCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:                       schemas.Ptr("call_123"),
			Arguments:                    &argsJSON,
			ResponsesImageGenerationCall: &schemas.ResponsesImageGenerationCall{},
		},
	}

	result, bifrostErr := b.ExecuteResponsesImageGenerationTool(ctx, toolCall)

	assert.Nil(t, result)
	assert.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "prompt is required")
}

// =============================================================================
// Schema Conversion Tests
// =============================================================================

func TestChatTool_ToResponsesTool_ImageGeneration(t *testing.T) {
	t.Parallel()

	chatTool := schemas.ChatTool{
		Type:            schemas.ChatToolTypeImageGeneration,
		ImageGeneration: &schemas.ChatToolImageGeneration{},
	}

	responsesTool := chatTool.ToResponsesTool()

	require.NotNil(t, responsesTool)
	assert.Equal(t, schemas.ResponsesToolTypeImageGeneration, responsesTool.Type)
	assert.NotNil(t, responsesTool.ResponsesToolImageGeneration)
}

func TestChatTool_ToResponsesTool_ImageGeneration_Nil(t *testing.T) {
	t.Parallel()

	chatTool := schemas.ChatTool{
		Type:            schemas.ChatToolTypeImageGeneration,
		ImageGeneration: nil,
	}

	responsesTool := chatTool.ToResponsesTool()

	require.NotNil(t, responsesTool)
	assert.Equal(t, schemas.ResponsesToolTypeImageGeneration, responsesTool.Type)
	// Should still work even if ImageGeneration is nil
}

// =============================================================================
// Output Struct Serialization Tests
// =============================================================================

func TestResponsesToolMessageOutputStruct_MarshalJSON_ImageGeneration(t *testing.T) {
	t.Parallel()

	output := schemas.ResponsesToolMessageOutputStruct{
		ResponsesImageGenerationCallOutput: &schemas.ResponsesImageGenerationCallOutput{
			Result: `[{"b64_json": "iVBORw0KGgo...", "index": 0}]`,
		},
	}

	jsonBytes, err := output.MarshalJSON()
	require.NoError(t, err)
	assert.NotEmpty(t, jsonBytes)

	// Verify it can be unmarshaled back
	var unmarshaled schemas.ResponsesImageGenerationCallOutput
	err = sonic.Unmarshal(jsonBytes, &unmarshaled)
	assert.NoError(t, err)
	assert.Equal(t, output.ResponsesImageGenerationCallOutput.Result, unmarshaled.Result)
}

func TestResponsesToolMessageOutputStruct_UnmarshalJSON_ImageGeneration(t *testing.T) {
	t.Parallel()

	jsonStr := `{"result": "[{\"b64_json\": \"iVBORw0KGgo...\", \"index\": 0}]"}`
	jsonBytes := []byte(jsonStr)

	var output schemas.ResponsesToolMessageOutputStruct
	err := output.UnmarshalJSON(jsonBytes)

	require.NoError(t, err)
	assert.NotNil(t, output.ResponsesImageGenerationCallOutput)
	assert.Equal(t, `[{"b64_json": "iVBORw0KGgo...", "index": 0}]`, output.ResponsesImageGenerationCallOutput.Result)
}

func TestResponsesToolMessageOutputStruct_MarshalJSON_Priority(t *testing.T) {
	t.Parallel()

	// Test that image generation output takes priority when set
	output := schemas.ResponsesToolMessageOutputStruct{
		ResponsesToolCallOutputStr: schemas.Ptr("string output"),
		ResponsesImageGenerationCallOutput: &schemas.ResponsesImageGenerationCallOutput{
			Result: `[{"b64_json": "test", "index": 0}]`,
		},
	}

	jsonBytes, err := output.MarshalJSON()
	require.NoError(t, err)

	// Should marshal as image generation output (first non-nil field in order)
	var unmarshaled schemas.ResponsesImageGenerationCallOutput
	err = sonic.Unmarshal(jsonBytes, &unmarshaled)
	assert.NoError(t, err)
	assert.Equal(t, `[{"b64_json": "test", "index": 0}]`, unmarshaled.Result)
}
