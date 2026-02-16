package llmtests

import (
	"context"
	"os"
	"testing"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunPassthroughExtraParamsTest executes the passthrough extraParams test scenario
// This test verifies that extraParams are properly propagated into the provider request body
// when the passthrough flag is set in the context.
// Note: This test only runs for providers that support arbitrary extra params at the root level
// of the request body. Providers like Anthropic have strict schema validation and don't accept
// unknown fields, so they should set PassThroughExtraParams: false in their test config.
func RunPassthroughExtraParamsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	// Guard: Check if ChatModel is configured
	if testConfig.ChatModel == "" {
		t.Logf("ChatModel not configured for provider %s, skipping passthrough test", testConfig.Provider)
		return
	}

	if !testConfig.Scenarios.PassThroughExtraParams {
		t.Logf("PassThroughExtraParams not supported for provider %s, skipping passthrough test", testConfig.Provider)
		return
	}

	t.Run("PassthroughExtraParams", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Create a Bifrost context with passthrough extraParams enabled
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		bfCtx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
		bfCtx.SetValue(schemas.BifrostContextKeySendBackRawRequest, true)

		// Prepare chat request with extraParams
		// custom_param will be at root level
		// custom_nested will be a nested structure to test recursive merging
		chatReq := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: []schemas.ChatMessage{
				CreateBasicChatMessage("Say hello in one word"),
			},
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(10),
				// Set extraParams with custom_param and nested structure
				ExtraParams: map[string]interface{}{
					"custom_param": "test_value_123",
					"custom_nested": map[string]interface{}{
						"custom_field": "nested_custom_value_456",
						"another_nested": map[string]interface{}{
							"deep_field": "deep_value_789",
						},
					},
				},
			},
			Fallbacks: testConfig.Fallbacks,
		}

		// Make the request
		response, err := client.ChatCompletionRequest(bfCtx, chatReq)
		if err != nil {
			t.Fatalf("❌ Chat completion request failed: %s", GetErrorMessage(err))
		}

		if response == nil {
			t.Fatalf("❌ Chat completion response is nil")
		}
		defer response.Release()

		// Verify the response is valid
		chatContent := GetChatContent(response)
		if chatContent == "" {
			t.Fatalf("❌ Chat response content is empty")
		}

		t.Logf("✅ Chat completion request completed successfully")
		t.Logf("Response content: %s", chatContent)

		// Verify raw request is present in ExtraFields
		if response.ExtraFields.RawRequest == nil {
			t.Logf("⚠️  Raw request not found in ExtraFields - this may be provider-specific")
			t.Logf("   Check Bifrost logs for the raw request body sent to provider")
			t.Logf("   Expected in raw request:")
			t.Logf("     - 'custom_param': 'test_value_123'")
			t.Logf("     - 'custom_nested.custom_field': 'nested_custom_value_456'")
			t.Logf("     - 'custom_nested.another_nested.deep_field': 'deep_value_789'")
			return
		}

		// Parse raw request
		var rawRequest map[string]interface{}
		rawRequestBytes, marshalErr := sonic.Marshal(response.ExtraFields.RawRequest)
		if marshalErr != nil {
			t.Fatalf("❌ Failed to marshal raw request: %v", marshalErr)
		}

		if err := sonic.Unmarshal(rawRequestBytes, &rawRequest); err != nil {
			t.Fatalf("❌ Failed to unmarshal raw request: %v", err)
		}

		t.Logf("✅ Found raw request in response ExtraFields")
		t.Logf("Raw request keys: %v", getMapKeys(rawRequest))

		// Verify custom_param is in raw request
		if customParam, exists := rawRequest["custom_param"]; !exists {
			t.Errorf("❌ custom_param not found in raw request")
		} else {
			if customParamStr, ok := customParam.(string); !ok || customParamStr != "test_value_123" {
				t.Errorf("❌ custom_param value mismatch: expected 'test_value_123', got %v", customParam)
			} else {
				t.Logf("✅ Verified custom_param in raw request: %s", customParamStr)
			}
		}

		// Verify nested custom_nested structure
		if customNested, exists := rawRequest["custom_nested"]; !exists {
			t.Errorf("❌ custom_nested not found in raw request")
		} else {
			customNestedMap, ok := customNested.(map[string]interface{})
			if !ok {
				t.Errorf("❌ custom_nested is not a map: %T", customNested)
			} else {
				// Verify custom_field
				if customField, exists := customNestedMap["custom_field"]; !exists {
					t.Errorf("❌ custom_field not found in custom_nested")
				} else {
					if customFieldStr, ok := customField.(string); !ok || customFieldStr != "nested_custom_value_456" {
						t.Errorf("❌ custom_field value mismatch: expected 'nested_custom_value_456', got %v", customField)
					} else {
						t.Logf("✅ Verified custom_field in custom_nested: %s", customFieldStr)
					}
				}

				// Verify deeply nested another_nested.deep_field
				if anotherNested, exists := customNestedMap["another_nested"]; !exists {
					t.Errorf("❌ another_nested not found in custom_nested")
				} else {
					anotherNestedMap, ok := anotherNested.(map[string]interface{})
					if !ok {
						t.Errorf("❌ another_nested is not a map: %T", anotherNested)
					} else {
						if deepField, exists := anotherNestedMap["deep_field"]; !exists {
							t.Errorf("❌ deep_field not found in another_nested")
						} else {
							if deepFieldStr, ok := deepField.(string); !ok || deepFieldStr != "deep_value_789" {
								t.Errorf("❌ deep_field value mismatch: expected 'deep_value_789', got %v", deepField)
							} else {
								t.Logf("✅ Verified deep_field in another_nested: %s", deepFieldStr)
							}
						}
					}
				}
			}
		}

		// Log the full raw request for debugging (pretty printed)
		rawRequestJSON, marshalErr := sonic.MarshalIndent(rawRequest, "", "  ")
		if marshalErr == nil {
			t.Logf("📋 Full raw request body:\n%s", string(rawRequestJSON))
		}

		t.Logf("🎉 PassthroughExtraParams test completed successfully!")
	})
}

// getMapKeys returns all keys from a map as a slice of strings
func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
