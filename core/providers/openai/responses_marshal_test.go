package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestOpenAIResponsesRequest_MarshalJSON_ReasoningMaxTokensAbsent(t *testing.T) {
	tests := []struct {
		name        string
		request     *OpenAIResponsesRequest
		description string
	}{
		{
			name: "reasoning with MaxTokens set should omit max_tokens from output",
			request: &OpenAIResponsesRequest{
				Model: "gpt-4o",
				Input: OpenAIResponsesRequestInput{
					OpenAIResponsesRequestInputStr: schemas.Ptr("test input"),
				},
				ResponsesParameters: schemas.ResponsesParameters{
					Reasoning: &schemas.ResponsesParametersReasoning{
						Effort:    schemas.Ptr("high"),
						MaxTokens: schemas.Ptr(1000),
						Summary:   schemas.Ptr("detailed"),
					},
				},
			},
			description: "When Reasoning.MaxTokens is set, it should be absent from JSON output",
		},
		{
			name: "reasoning with all fields set should omit only max_tokens",
			request: &OpenAIResponsesRequest{
				Model: "gpt-4o",
				Input: OpenAIResponsesRequestInput{
					OpenAIResponsesRequestInputStr: schemas.Ptr("test"),
				},
				ResponsesParameters: schemas.ResponsesParameters{
					Reasoning: &schemas.ResponsesParametersReasoning{
						Effort:          schemas.Ptr("medium"),
						GenerateSummary: schemas.Ptr("auto"),
						Summary:         schemas.Ptr("concise"),
						MaxTokens:       schemas.Ptr(500),
					},
				},
			},
			description: "All reasoning fields except MaxTokens should be present in output",
		},
		{
			name: "reasoning with nil MaxTokens should not include max_tokens",
			request: &OpenAIResponsesRequest{
				Model: "gpt-4o",
				Input: OpenAIResponsesRequestInput{
					OpenAIResponsesRequestInputStr: schemas.Ptr("test"),
				},
				ResponsesParameters: schemas.ResponsesParameters{
					Reasoning: &schemas.ResponsesParametersReasoning{
						Effort:    schemas.Ptr("low"),
						MaxTokens: nil,
					},
				},
			},
			description: "When Reasoning.MaxTokens is nil, max_tokens should not appear in output",
		},
		{
			name: "nil reasoning should not include reasoning field",
			request: &OpenAIResponsesRequest{
				Model: "gpt-4o",
				Input: OpenAIResponsesRequestInput{
					OpenAIResponsesRequestInputStr: schemas.Ptr("test"),
				},
				ResponsesParameters: schemas.ResponsesParameters{
					Reasoning: nil,
				},
			},
			description: "When Reasoning is nil, reasoning field should not appear in output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonBytes, err := tt.request.MarshalJSON()
			if err != nil {
				t.Fatalf("Failed to marshal JSON: %v", err)
			}

			// Parse the JSON to check structure
			var jsonMap map[string]interface{}
			if err := sonic.Unmarshal(jsonBytes, &jsonMap); err != nil {
				t.Fatalf("Failed to unmarshal marshaled JSON: %v", err)
			}

			// Check that reasoning.max_tokens is absent
			if reasoning, ok := jsonMap["reasoning"].(map[string]interface{}); ok {
				if maxTokens, exists := reasoning["max_tokens"]; exists {
					t.Errorf("%s: reasoning.max_tokens should be absent from JSON output, but found: %v", tt.description, maxTokens)
				}

				// Verify other reasoning fields are present when they should be
				if tt.request.Reasoning != nil {
					if tt.request.Reasoning.Effort != nil {
						if _, exists := reasoning["effort"]; !exists {
							t.Error("reasoning.effort should be present in output")
						}
					}
					if tt.request.Reasoning.Summary != nil {
						if _, exists := reasoning["summary"]; !exists {
							t.Error("reasoning.summary should be present in output")
						}
					}
					if tt.request.Reasoning.GenerateSummary != nil {
						if _, exists := reasoning["generate_summary"]; !exists {
							t.Error("reasoning.generate_summary should be present in output")
						}
					}
				}
			} else if tt.request.Reasoning != nil {
				// If reasoning is set, it should appear in JSON (unless all fields are nil/omitted)
				if tt.request.Reasoning.Effort != nil || tt.request.Reasoning.Summary != nil || tt.request.Reasoning.GenerateSummary != nil {
					t.Error("reasoning field should be present in JSON when Reasoning is set with non-nil fields")
				}
			}
		})
	}
}

func TestOpenAIResponsesRequest_MarshalJSON_InputStringForm(t *testing.T) {
	tests := []struct {
		name        string
		request     *OpenAIResponsesRequest
		expected    string
		description string
	}{
		{
			name: "input as string is correctly marshaled",
			request: &OpenAIResponsesRequest{
				Model: "gpt-4o",
				Input: OpenAIResponsesRequestInput{
					OpenAIResponsesRequestInputStr: schemas.Ptr("Hello, world!"),
				},
			},
			expected:    "Hello, world!",
			description: "Input field should be marshaled as a string when OpenAIResponsesRequestInputStr is set",
		},
		{
			name: "input as empty string is correctly marshaled",
			request: &OpenAIResponsesRequest{
				Model: "gpt-4o",
				Input: OpenAIResponsesRequestInput{
					OpenAIResponsesRequestInputStr: schemas.Ptr(""),
				},
			},
			expected:    "",
			description: "Input field should be marshaled as empty string when set to empty string",
		},
		{
			name: "input as string with special characters",
			request: &OpenAIResponsesRequest{
				Model: "gpt-4o",
				Input: OpenAIResponsesRequestInput{
					OpenAIResponsesRequestInputStr: schemas.Ptr(`{"key": "value"}`),
				},
			},
			expected:    `{"key": "value"}`,
			description: "Input field should correctly marshal strings with special characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonBytes, err := tt.request.MarshalJSON()
			if err != nil {
				t.Fatalf("Failed to marshal JSON: %v", err)
			}

			// Parse the JSON to check input field
			var jsonMap map[string]interface{}
			if err := sonic.Unmarshal(jsonBytes, &jsonMap); err != nil {
				t.Fatalf("Failed to unmarshal marshaled JSON: %v", err)
			}

			// Check that input is a string
			inputValue, exists := jsonMap["input"]
			if !exists {
				t.Fatalf("%s: input field should be present in JSON", tt.description)
			}

			inputStr, ok := inputValue.(string)
			if !ok {
				t.Errorf("%s: input field should be a string, got type %T", tt.description, inputValue)
			}

			if inputStr != tt.expected {
				t.Errorf("%s: expected input to be %q, got %q", tt.description, tt.expected, inputStr)
			}
		})
	}
}

func TestOpenAIResponsesRequest_MarshalJSON_InputArrayForm(t *testing.T) {
	tests := []struct {
		name        string
		request     *OpenAIResponsesRequest
		validate    func(t *testing.T, inputValue interface{})
		description string
	}{
		{
			name: "input as array is correctly marshaled",
			request: &OpenAIResponsesRequest{
				Model: "gpt-4o",
				Input: OpenAIResponsesRequestInput{
					OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{
						{
							Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
							Content: &schemas.ResponsesMessageContent{
								ContentStr: schemas.Ptr("Hello"),
							},
						},
					},
				},
			},
			validate: func(t *testing.T, inputValue interface{}) {
				inputArray, ok := inputValue.([]interface{})
				if !ok {
					t.Fatalf("Expected input to be an array, got type %T", inputValue)
				}
				if len(inputArray) != 1 {
					t.Errorf("Expected 1 message in array, got %d", len(inputArray))
				}
			},
			description: "Input field should be marshaled as an array when OpenAIResponsesRequestInputArray is set",
		},
		{
			name: "input as empty array is correctly marshaled",
			request: &OpenAIResponsesRequest{
				Model: "gpt-4o",
				Input: OpenAIResponsesRequestInput{
					OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{},
				},
			},
			validate: func(t *testing.T, inputValue interface{}) {
				inputArray, ok := inputValue.([]interface{})
				if !ok {
					t.Fatalf("Expected input to be an array, got type %T", inputValue)
				}
				if len(inputArray) != 0 {
					t.Errorf("Expected empty array, got %d elements", len(inputArray))
				}
			},
			description: "Input field should be marshaled as empty array when set to empty array",
		},
		{
			name: "input as array with multiple messages",
			request: &OpenAIResponsesRequest{
				Model: "gpt-4o",
				Input: OpenAIResponsesRequestInput{
					OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{
						{
							Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
							Content: &schemas.ResponsesMessageContent{
								ContentStr: schemas.Ptr("You are a helpful assistant."),
							},
						},
						{
							Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
							Content: &schemas.ResponsesMessageContent{
								ContentStr: schemas.Ptr("What is 2+2?"),
							},
						},
					},
				},
			},
			validate: func(t *testing.T, inputValue interface{}) {
				inputArray, ok := inputValue.([]interface{})
				if !ok {
					t.Fatalf("Expected input to be an array, got type %T", inputValue)
				}
				if len(inputArray) != 2 {
					t.Errorf("Expected 2 messages in array, got %d", len(inputArray))
				}
			},
			description: "Input field should correctly marshal arrays with multiple messages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonBytes, err := tt.request.MarshalJSON()
			if err != nil {
				t.Fatalf("Failed to marshal JSON: %v", err)
			}

			// Parse the JSON to check input field
			var jsonMap map[string]interface{}
			if err := sonic.Unmarshal(jsonBytes, &jsonMap); err != nil {
				t.Fatalf("Failed to unmarshal marshaled JSON: %v", err)
			}

			// Check that input is present
			inputValue, exists := jsonMap["input"]
			if !exists {
				t.Fatalf("%s: input field should be present in JSON", tt.description)
			}

			// Validate using the provided function
			tt.validate(t, inputValue)
		})
	}
}

func TestToOpenAIResponsesRequest_FireworksPreservesNativeFields(t *testing.T) {
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.Fireworks,
		Model:    "accounts/fireworks/models/deepseek-v3p2",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("hello"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			PreviousResponseID: schemas.Ptr("resp_previous"),
			MaxToolCalls:       schemas.Ptr(2),
			Store:              schemas.Ptr(true),
		},
	}

	request := ToOpenAIResponsesRequest(bifrostReq)
	if request == nil {
		t.Fatal("expected non-nil request")
	}

	jsonBytes, err := request.MarshalJSON()
	if err != nil {
		t.Fatalf("failed to marshal responses request: %v", err)
	}

	var jsonMap map[string]interface{}
	if err := sonic.Unmarshal(jsonBytes, &jsonMap); err != nil {
		t.Fatalf("failed to parse marshaled JSON: %v", err)
	}

	if got, ok := jsonMap["previous_response_id"].(string); !ok || got != "resp_previous" {
		t.Fatalf("expected previous_response_id to be preserved, got %#v", jsonMap["previous_response_id"])
	}
	if got, ok := jsonMap["max_tool_calls"].(float64); !ok || got != 2 {
		t.Fatalf("expected max_tool_calls to be preserved, got %#v", jsonMap["max_tool_calls"])
	}
	if got, ok := jsonMap["store"].(bool); !ok || !got {
		t.Fatalf("expected store=true to be preserved, got %#v", jsonMap["store"])
	}
}

func TestOpenAIResponsesRequest_MarshalJSON_FieldShadowingBehavior(t *testing.T) {
	// This test verifies that the field shadowing pattern works correctly
	// by ensuring that the aux struct properly shadows Input and Reasoning fields
	t.Run("field shadowing preserves other fields", func(t *testing.T) {
		request := &OpenAIResponsesRequest{
			Model: "gpt-4o",
			Input: OpenAIResponsesRequestInput{
				OpenAIResponsesRequestInputStr: schemas.Ptr("test input"),
			},
			ResponsesParameters: schemas.ResponsesParameters{
				MaxOutputTokens: schemas.Ptr(100),
				Temperature:     schemas.Ptr(0.7),
				Reasoning: &schemas.ResponsesParametersReasoning{
					Effort:    schemas.Ptr("high"),
					MaxTokens: schemas.Ptr(500), // This should be omitted
					Summary:   schemas.Ptr("detailed"),
				},
			},
			Stream:    schemas.Ptr(true),
			Fallbacks: []string{"fallback1", "fallback2"},
		}

		jsonBytes, err := request.MarshalJSON()
		if err != nil {
			t.Fatalf("Failed to marshal JSON: %v", err)
		}

		var jsonMap map[string]interface{}
		if err := sonic.Unmarshal(jsonBytes, &jsonMap); err != nil {
			t.Fatalf("Failed to unmarshal marshaled JSON: %v", err)
		}

		// Verify base fields are present
		if jsonMap["model"] != "gpt-4o" {
			t.Errorf("Expected model to be 'gpt-4o', got %v", jsonMap["model"])
		}

		if jsonMap["stream"] != true {
			t.Errorf("Expected stream to be true, got %v", jsonMap["stream"])
		}

		fallbacks, ok := jsonMap["fallbacks"].([]interface{})
		if !ok || len(fallbacks) != 2 {
			t.Errorf("Expected fallbacks to have 2 elements, got %v", jsonMap["fallbacks"])
		}

		// Verify ResponsesParameters fields are present
		if jsonMap["max_output_tokens"] != float64(100) {
			t.Errorf("Expected max_output_tokens to be 100, got %v", jsonMap["max_output_tokens"])
		}

		if jsonMap["temperature"] != 0.7 {
			t.Errorf("Expected temperature to be 0.7, got %v", jsonMap["temperature"])
		}

		// Verify reasoning.max_tokens is absent
		if reasoning, ok := jsonMap["reasoning"].(map[string]interface{}); ok {
			if _, exists := reasoning["max_tokens"]; exists {
				t.Error("reasoning.max_tokens should be absent from JSON output")
			}
			if reasoning["effort"] != "high" {
				t.Errorf("Expected reasoning.effort to be 'high', got %v", reasoning["effort"])
			}
			if reasoning["summary"] != "detailed" {
				t.Errorf("Expected reasoning.summary to be 'detailed', got %v", reasoning["summary"])
			}
		} else {
			t.Error("reasoning field should be present in JSON")
		}

		// Verify input is correctly marshaled
		if jsonMap["input"] != "test input" {
			t.Errorf("Expected input to be 'test input', got %v", jsonMap["input"])
		}
	})
}

func TestOpenAIResponsesRequest_MarshalJSON_RoundTrip(t *testing.T) {
	// Test that marshaling and unmarshaling preserves all fields except reasoning.max_tokens
	t.Run("round trip preserves fields except reasoning.max_tokens", func(t *testing.T) {
		original := &OpenAIResponsesRequest{
			Model: "gpt-4o",
			Input: OpenAIResponsesRequestInput{
				OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{
					{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Test message"),
						},
					},
				},
			},
			ResponsesParameters: schemas.ResponsesParameters{
				MaxOutputTokens: schemas.Ptr(200),
				Temperature:     schemas.Ptr(0.8),
				Reasoning: &schemas.ResponsesParametersReasoning{
					Effort:    schemas.Ptr("medium"),
					MaxTokens: schemas.Ptr(1000), // Should be omitted
					Summary:   schemas.Ptr("auto"),
				},
			},
			Stream: schemas.Ptr(false),
		}

		// Marshal
		jsonBytes, err := original.MarshalJSON()
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		// Verify reasoning.max_tokens is absent in the JSON string
		jsonStr := string(jsonBytes)
		if strings.Contains(jsonStr, `"max_tokens"`) {
			// Check if it's inside reasoning object
			if strings.Contains(jsonStr, `"reasoning"`) {
				// Parse to verify it's not in reasoning
				var jsonMap map[string]interface{}
				if err := json.Unmarshal(jsonBytes, &jsonMap); err == nil {
					if reasoning, ok := jsonMap["reasoning"].(map[string]interface{}); ok {
						if _, exists := reasoning["max_tokens"]; exists {
							t.Error("reasoning.max_tokens should not be present in marshaled JSON")
						}
					}
				}
			}
		}

		// Unmarshal back
		var unmarshaled OpenAIResponsesRequest
		if err := sonic.Unmarshal(jsonBytes, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		// Verify fields are preserved
		if unmarshaled.Model != original.Model {
			t.Errorf("Model not preserved: expected %q, got %q", original.Model, unmarshaled.Model)
		}

		if unmarshaled.Stream == nil || *unmarshaled.Stream != *original.Stream {
			t.Error("Stream not preserved")
		}

		if unmarshaled.MaxOutputTokens == nil || *unmarshaled.MaxOutputTokens != *original.MaxOutputTokens {
			t.Error("MaxOutputTokens not preserved")
		}

		if unmarshaled.Temperature == nil || *unmarshaled.Temperature != *original.Temperature {
			t.Error("Temperature not preserved")
		}

		// Verify reasoning fields except MaxTokens
		if unmarshaled.Reasoning == nil {
			t.Fatal("Reasoning should be present")
		}
		if unmarshaled.Reasoning.Effort == nil || *unmarshaled.Reasoning.Effort != *original.Reasoning.Effort {
			t.Error("Reasoning.Effort not preserved")
		}
		if unmarshaled.Reasoning.Summary == nil || *unmarshaled.Reasoning.Summary != *original.Reasoning.Summary {
			t.Error("Reasoning.Summary not preserved")
		}
		// MaxTokens should be nil after unmarshaling (since it wasn't in JSON)
		if unmarshaled.Reasoning.MaxTokens != nil {
			t.Error("Reasoning.MaxTokens should be nil after unmarshaling (was omitted from JSON)")
		}
	})
}

// Regression test for multi-turn Anthropic tool_result with array-form content.
// The OpenAI Responses API defines function_call_output.output as a string (see
// https://platform.openai.com/docs/api-reference/responses/create). When an
// Anthropic client sends a tool_result whose content is an array of text blocks,
// Bifrost's Anthropic→Responses translator populates
// ResponsesToolMessageOutputStruct.ResponsesFunctionToolCallOutputBlocks.
// Historically, that array was marshaled verbatim onto the wire, which some
// strict OpenAI-compat upstreams (e.g. Ollama Cloud) reject with an error like
//
//	json: cannot unmarshal array into Go struct field ResponsesFunctionCallOutput.output of type string
//
// The outgoing OpenAI Responses request must emit `output` as a string for
// text-only tool outputs.
func TestOpenAIResponsesRequestInput_MarshalJSON_FunctionCallOutputFlattensTextBlocksToString(t *testing.T) {
	outputText := "line1"
	callID := "toolu_abc123"
	functionName := "read_file"

	input := &OpenAIResponsesRequestInput{
		OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Read /tmp/test.txt and tell me what it contains."),
				},
			},
			{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr(callID),
					Name:      schemas.Ptr(functionName),
					Arguments: schemas.Ptr(`{"path":"/tmp/test.txt"}`),
				},
			},
			{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr(callID),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesInputMessageContentBlockTypeText,
								Text: schemas.Ptr(outputText),
							},
						},
					},
				},
			},
		},
	}

	jsonBytes, err := input.MarshalJSON()
	if err != nil {
		t.Fatalf("Failed to marshal OpenAIResponsesRequestInput: %v", err)
	}

	var messages []map[string]interface{}
	if err := sonic.Unmarshal(jsonBytes, &messages); err != nil {
		t.Fatalf("Failed to unmarshal marshaled input as array: %v\nraw=%s", err, string(jsonBytes))
	}

	var fcoMsg map[string]interface{}
	for _, m := range messages {
		if t, ok := m["type"].(string); ok && t == string(schemas.ResponsesMessageTypeFunctionCallOutput) {
			fcoMsg = m
			break
		}
	}
	if fcoMsg == nil {
		t.Fatalf("did not find function_call_output message in marshaled JSON: %s", string(jsonBytes))
	}

	outputVal, ok := fcoMsg["output"]
	if !ok {
		t.Fatalf("function_call_output message has no `output` field: %s", string(jsonBytes))
	}

	outputStr, isString := outputVal.(string)
	if !isString {
		t.Fatalf("function_call_output.output must be a string (OpenAI Responses API spec); got %T: %v\nraw=%s", outputVal, outputVal, string(jsonBytes))
	}
	if outputStr != outputText {
		t.Fatalf("function_call_output.output mismatch: want %q, got %q", outputText, outputStr)
	}
}

// Flattening must concatenate multiple text blocks with newline separators so
// every character from the upstream tool response reaches the model.
func TestOpenAIResponsesRequestInput_MarshalJSON_FunctionCallOutputConcatenatesMultipleTextBlocks(t *testing.T) {
	callID := "toolu_multi"
	input := &OpenAIResponsesRequestInput{
		OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{
			{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr(callID),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
							{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr("line1")},
							{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr("line2")},
						},
					},
				},
			},
		},
	}

	jsonBytes, err := input.MarshalJSON()
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}
	var messages []map[string]interface{}
	if err := sonic.Unmarshal(jsonBytes, &messages); err != nil {
		t.Fatalf("Failed to unmarshal: %v\nraw=%s", err, string(jsonBytes))
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	got, ok := messages[0]["output"].(string)
	if !ok {
		t.Fatalf("output must be string, got %T", messages[0]["output"])
	}
	if want := "line1\nline2"; got != want {
		t.Fatalf("flattened output mismatch: want %q, got %q", want, got)
	}
}

// When the tool result contains a non-text block (e.g. an image), flattening is
// unsafe — preserve the array form and let the upstream handle it. This keeps
// the fix scoped to the common text-only case without dropping rich content.
func TestOpenAIResponsesRequestInput_MarshalJSON_FunctionCallOutputPreservesNonTextBlocks(t *testing.T) {
	callID := "toolu_with_image"
	imageURL := "https://example.com/screenshot.png"
	input := &OpenAIResponsesRequestInput{
		OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{
			{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr(callID),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
							{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr("here is the screenshot:")},
							{
								Type: schemas.ResponsesInputMessageContentBlockTypeImage,
								ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
									ImageURL: &imageURL,
								},
							},
						},
					},
				},
			},
		},
	}
	jsonBytes, err := input.MarshalJSON()
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}
	var messages []map[string]interface{}
	if err := sonic.Unmarshal(jsonBytes, &messages); err != nil {
		t.Fatalf("Failed to unmarshal: %v\nraw=%s", err, string(jsonBytes))
	}
	if _, isString := messages[0]["output"].(string); isString {
		t.Fatalf("non-text blocks must not be flattened to string; raw=%s", string(jsonBytes))
	}
}

// TestOpenAIResponsesRequest_MarshalJSON_StripsAnthropicToolFlags ensures the
// Responses serializer drops the four Anthropic-native tool flags
// (defer_loading, allowed_callers, input_examples, eager_input_streaming)
// along with CacheControl before forwarding to OpenAI — mirroring the Chat
// path's behavior so Anthropic-flavored tools cannot 400 OpenAI via Responses.
func TestOpenAIResponsesRequest_MarshalJSON_StripsAnthropicToolFlags(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: OpenAIResponsesRequestInput{
			OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{
				{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{
						ContentStr: schemas.Ptr("hello"),
					},
				},
			},
		},
		ResponsesParameters: schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{
					Type:                schemas.ResponsesToolTypeFunction,
					Name:                schemas.Ptr("lookup"),
					Description:         schemas.Ptr("lookup something"),
					CacheControl:        &schemas.CacheControl{Type: "ephemeral"},
					DeferLoading:        schemas.Ptr(true),
					AllowedCallers:      []string{"direct", "agent"},
					EagerInputStreaming: schemas.Ptr(false),
					InputExamples: []schemas.ChatToolInputExample{
						{Input: json.RawMessage(`{"q":"hi"}`)},
					},
					ResponsesToolFunction: &schemas.ResponsesToolFunction{},
				},
			},
		},
	}

	jsonBytes, err := req.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	raw := string(jsonBytes)

	// None of the five Anthropic-only tool keys must survive on the wire.
	for _, key := range []string{`"cache_control"`, `"defer_loading"`, `"allowed_callers"`, `"input_examples"`, `"eager_input_streaming"`} {
		if strings.Contains(raw, key) {
			t.Errorf("OpenAI Responses serializer must strip %s; raw=%s", key, raw)
		}
	}
	// Function tool identity should be preserved.
	if !strings.Contains(raw, `"name":"lookup"`) {
		t.Errorf("tool identity lost after strip; raw=%s", raw)
	}
}

// TestOpenAIResponsesRequest_MarshalJSON_DropsAnthropicOnlyToolTypes verifies
// that Anthropic-only tool types (web_fetch, memory) are dropped entirely when
// serializing for OpenAI Responses. Per OpenAI's OpenAPI spec the Responses
// Tool discriminator union does not include web_fetch or memory, so forwarding
// them would trigger a 400 schema-validation error. Mirrors the Chat path's
// isAnthropicServerToolShape drop behavior.
func TestOpenAIResponsesRequest_MarshalJSON_DropsAnthropicOnlyToolTypes(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: OpenAIResponsesRequestInput{
			OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{
				{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{
						ContentStr: schemas.Ptr("hello"),
					},
				},
			},
		},
		ResponsesParameters: schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				// Kept: function (OpenAI-native).
				{
					Type:                  schemas.ResponsesToolTypeFunction,
					Name:                  schemas.Ptr("keeper_func"),
					ResponsesToolFunction: &schemas.ResponsesToolFunction{},
				},
				// Dropped: web_fetch (Anthropic-only).
				{
					Type:                  schemas.ResponsesToolTypeWebFetch,
					Name:                  schemas.Ptr("anthropic_webfetch"),
					ResponsesToolWebFetch: &schemas.ResponsesToolWebFetch{},
				},
				// Kept: web_search (both support).
				{
					Type:                   schemas.ResponsesToolTypeWebSearch,
					ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{},
				},
				// Dropped: memory (Anthropic-only).
				{
					Type: schemas.ResponsesToolTypeMemory,
					Name: schemas.Ptr("anthropic_memory"),
				},
				// Kept: tool_search (both support per OpenAI OpenAPI spec).
				{
					Type: schemas.ResponsesToolTypeToolSearch,
				},
			},
		},
	}

	jsonBytes, err := req.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	raw := string(jsonBytes)

	// Dropped types must not appear on the wire.
	for _, dropped := range []string{`"web_fetch"`, `"memory"`, `"anthropic_webfetch"`, `"anthropic_memory"`} {
		if strings.Contains(raw, dropped) {
			t.Errorf("Anthropic-only tool must be dropped; found %s in raw=%s", dropped, raw)
		}
	}
	// Kept types must still appear.
	for _, kept := range []string{`"function"`, `"web_search"`, `"tool_search"`, `"keeper_func"`} {
		if !strings.Contains(raw, kept) {
			t.Errorf("supported tool %s should be preserved; raw=%s", kept, raw)
		}
	}

	// Confirm the tools array is present and has exactly 3 entries (2 dropped of 5).
	var decoded struct {
		Tools []map[string]interface{} `json:"tools"`
	}
	if err := json.Unmarshal(jsonBytes, &decoded); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(decoded.Tools) != 3 {
		t.Errorf("expected 3 tools after drop (function, web_search, tool_search), got %d; tools=%+v", len(decoded.Tools), decoded.Tools)
	}
}

// TestOpenAIResponsesRequest_MarshalJSON_KeepsAllWhenAllSupported verifies the
// no-reshape fast path: if every tool is OpenAI-compatible with no
// Anthropic-only flags, the tools slice passes through unchanged (no copy,
// no drop).
func TestOpenAIResponsesRequest_MarshalJSON_KeepsAllWhenAllSupported(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-4o",
		Input: OpenAIResponsesRequestInput{
			OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{
				{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
				},
			},
		},
		ResponsesParameters: schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("f"), ResponsesToolFunction: &schemas.ResponsesToolFunction{}},
				{Type: schemas.ResponsesToolTypeWebSearch, ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{}},
				{Type: schemas.ResponsesToolTypeCodeInterpreter, ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{}},
			},
		},
	}

	jsonBytes, err := req.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var decoded struct {
		Tools []map[string]interface{} `json:"tools"`
	}
	if err := json.Unmarshal(jsonBytes, &decoded); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(decoded.Tools) != 3 {
		t.Errorf("expected 3 tools preserved, got %d", len(decoded.Tools))
	}
}
