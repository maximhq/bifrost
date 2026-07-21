package mcp

import (
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestMCPErrorTypeClassification asserts that mcpErrorType maps failed MCP ops onto the
// low-cardinality error.type values used by the mcp.client.operation.duration metric,
// including the sentinel-wrapped wire errors and the _OTHER catch-all.
func TestMCPErrorTypeClassification(t *testing.T) {
	tests := []struct {
		name string
		err  *schemas.BifrostError
		want string
	}{
		{
			name: "nil error",
			err:  nil,
			want: "_OTHER",
		},
		{
			name: "auth required takes precedence",
			err: &schemas.BifrostError{
				Error:       &schemas.ErrorField{Message: "auth", Error: ErrMCPToolTimeout},
				ExtraFields: schemas.BifrostErrorExtraFields{MCPAuthRequired: &schemas.MCPAuthRequiredError{}},
			},
			want: "auth_required",
		},
		{
			name: "timeout sentinel through fmt wrap",
			err: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Message: "MCP tool call timed out after 30s: search",
					Error:   fmt.Errorf("MCP tool call timed out after 30s: search: %w", ErrMCPToolTimeout),
				},
			},
			want: "timeout",
		},
		{
			name: "tool_error sentinel through fmt wrap",
			err: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Message: "MCP tool call failed for search: boom",
					Error:   fmt.Errorf("MCP tool call failed for search: boom: %w", ErrMCPToolCallFailed),
				},
			},
			want: "tool_error",
		},
		{
			name: "unclassified error falls back to _OTHER",
			err: &schemas.BifrostError{
				Error: &schemas.ErrorField{Message: "tool execution returned nil result", Error: fmt.Errorf("nil result")},
			},
			want: "_OTHER",
		},
		{
			name: "error present but no wrapped chain",
			err: &schemas.BifrostError{
				Error: &schemas.ErrorField{Message: "plugin denied"},
			},
			want: "_OTHER",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mcpErrorType(tt.err); got != tt.want {
				t.Errorf("mcpErrorType() = %q, want %q", got, tt.want)
			}
		})
	}
}
