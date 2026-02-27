package mcp

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
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
