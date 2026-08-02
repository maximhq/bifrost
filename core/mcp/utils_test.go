package mcp

import (
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConvertMCPToolToBifrostSchema_EmptyParameters tests that tools with no parameters
// get an empty properties map instead of nil, which is required by some providers like OpenAI
func TestConvertMCPToolToBifrostSchema_EmptyParameters(t *testing.T) {
	// Create a tool with no parameters (like return_special_chars or return_null)
	mcpTool := &mcp.Tool{
		Name:        "test_tool_no_params",
		Description: "A test tool with no parameters",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{}, // Empty properties
			Required:   []string{},
		},
	}

	// Convert the tool
	bifrostTool := convertMCPToolToBifrostSchema(mcpTool, defaultLogger)

	// Verify the function was created
	if bifrostTool.Function == nil {
		t.Fatal("Function should not be nil")
	}

	// Verify parameters were created
	if bifrostTool.Function.Parameters == nil {
		t.Fatal("Parameters should not be nil")
	}

	// Verify properties is not nil (this is the key fix)
	if bifrostTool.Function.Parameters.Properties == nil {
		t.Error("Properties should not be nil for object type, even if empty")
	}

	// Verify it's an empty map
	if bifrostTool.Function.Parameters.Properties != nil && bifrostTool.Function.Parameters.Properties.Len() != 0 {
		t.Errorf("Expected empty properties map, got %d properties", bifrostTool.Function.Parameters.Properties.Len())
	}

	// Verify the type is preserved
	if bifrostTool.Function.Parameters.Type != "object" {
		t.Errorf("Expected type 'object', got '%s'", bifrostTool.Function.Parameters.Type)
	}
}

// TestConvertMCPToolToBifrostSchema_WithAnnotations tests that MCP tool annotations
// are preserved on ChatTool.Annotations (not ChatToolFunction) and are absent from JSON.
func TestConvertMCPToolToBifrostSchema_WithAnnotations(t *testing.T) {
	readOnly := true
	destructive := false

	mcpTool := &mcp.Tool{
		Name:        "read_resource",
		Description: "Reads a resource",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
		Annotations: mcp.ToolAnnotation{
			Title:           "Resource Reader",
			ReadOnlyHint:    &readOnly,
			DestructiveHint: &destructive,
			IdempotentHint:  schemas.Ptr(true),
		},
	}

	bifrostTool := convertMCPToolToBifrostSchema(mcpTool, defaultLogger)

	// Annotations must be on ChatTool, not buried in Function
	require.NotNil(t, bifrostTool.Annotations, "Annotations should be set on ChatTool")
	assert.Equal(t, "Resource Reader", bifrostTool.Annotations.Title)
	require.NotNil(t, bifrostTool.Annotations.ReadOnlyHint)
	assert.True(t, *bifrostTool.Annotations.ReadOnlyHint)
	require.NotNil(t, bifrostTool.Annotations.DestructiveHint)
	assert.False(t, *bifrostTool.Annotations.DestructiveHint)
	require.NotNil(t, bifrostTool.Annotations.IdempotentHint)
	assert.True(t, *bifrostTool.Annotations.IdempotentHint)
	assert.Nil(t, bifrostTool.Annotations.OpenWorldHint)

	// The JSON sent to providers must not contain annotations
	toolJSON, err := json.Marshal(bifrostTool)
	require.NoError(t, err)
	s := string(toolJSON)
	assert.NotContains(t, s, "annotations", "annotations must be absent from provider JSON")
	assert.NotContains(t, s, "readOnlyHint", "readOnlyHint must be absent from provider JSON")
	assert.NotContains(t, s, "Resource Reader", "annotation title must be absent from provider JSON")
}

// TestConvertMCPToolToBifrostSchema_NilAnnotationsWhenAllZero verifies the nil guard:
// when all annotation fields are zero-valued, ChatTool.Annotations must remain nil.
func TestConvertMCPToolToBifrostSchema_NilAnnotationsWhenAllZero(t *testing.T) {
	mcpTool := &mcp.Tool{
		Name:        "no_hints_tool",
		Description: "A tool with no annotation hints",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
		Annotations: mcp.ToolAnnotation{}, // All zero values — Title empty, all hints nil
	}

	bifrostTool := convertMCPToolToBifrostSchema(mcpTool, defaultLogger)

	assert.Nil(t, bifrostTool.Annotations,
		"Annotations should be nil when all MCP annotation fields are zero")
}

// TestConvertMCPToolToBifrostSchema_WithParameters tests the normal case with parameters
func TestConvertMCPToolToBifrostSchema_WithParameters(t *testing.T) {
	// Create a tool with parameters
	mcpTool := &mcp.Tool{
		Name:        "test_tool_with_params",
		Description: "A test tool with parameters",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"param1": map[string]interface{}{
					"type":        "string",
					"description": "A string parameter",
				},
				"param2": map[string]interface{}{
					"type":        "number",
					"description": "A number parameter",
				},
			},
			Required: []string{"param1"},
		},
	}

	// Convert the tool
	bifrostTool := convertMCPToolToBifrostSchema(mcpTool, defaultLogger)

	// Verify the function was created
	if bifrostTool.Function == nil {
		t.Fatal("Function should not be nil")
	}

	// Verify parameters were created
	if bifrostTool.Function.Parameters == nil {
		t.Fatal("Parameters should not be nil")
	}

	// Verify properties is not nil
	if bifrostTool.Function.Parameters.Properties == nil {
		t.Fatal("Properties should not be nil")
	}

	// Verify the correct number of properties
	if bifrostTool.Function.Parameters.Properties.Len() != 2 {
		t.Errorf("Expected 2 properties, got %d", bifrostTool.Function.Parameters.Properties.Len())
	}

	// Verify required fields
	if len(bifrostTool.Function.Parameters.Required) != 1 {
		t.Errorf("Expected 1 required field, got %d", len(bifrostTool.Function.Parameters.Required))
	}

	if bifrostTool.Function.Parameters.Required[0] != "param1" {
		t.Errorf("Expected required field 'param1', got '%s'", bifrostTool.Function.Parameters.Required[0])
	}
}

// TestConvertMCPToolToBifrostSchema_PreservesDefs verifies that top-level JSON
// Schema definitions ($defs) on an MCP tool's input schema survive conversion.
// Without this, a $ref inside a property (which rides along in Properties) would
// be left dangling once the definitions it targets are dropped — the cause of
// Vertex Gemini rejecting such tools with INVALID_ARGUMENT.
func TestConvertMCPToolToBifrostSchema_PreservesDefs(t *testing.T) {
	mcpTool := &mcp.Tool{
		Name:        "suggest_time",
		Description: "Suggests time periods",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"preferences": map[string]interface{}{
					"$ref": "#/$defs/Preferences",
				},
			},
			Required: []string{"preferences"},
			Defs: map[string]interface{}{
				"Preferences": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"startHour": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
	}

	bifrostTool := convertMCPToolToBifrostSchema(mcpTool, defaultLogger)
	require.NotNil(t, bifrostTool.Function)
	require.NotNil(t, bifrostTool.Function.Parameters)
	require.NotNil(t, bifrostTool.Function.Parameters.Defs, "$defs must be preserved on conversion")

	data, err := json.Marshal(bifrostTool.Function.Parameters)
	require.NoError(t, err)
	s := string(data)
	assert.Contains(t, s, `"$defs"`, "marshalled schema must carry $defs")
	assert.Contains(t, s, "Preferences", "definition name must be present")
	assert.Contains(t, s, `"$ref"`, "the property $ref must still be present (resolution happens per-provider)")
}
