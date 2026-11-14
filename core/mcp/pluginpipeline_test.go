package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errorRecordingPipeline records the error its MCP post-hook is handed; short, when set, is what its
// pre-hook short-circuits with.
type errorRecordingPipeline struct {
	short *schemas.MCPPluginShortCircuit
	seen  *schemas.BifrostError
}

func (p *errorRecordingPipeline) RunMCPPreHooks(_ *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, int) {
	return req, p.short, 1
}

func (p *errorRecordingPipeline) RunMCPPostHooks(_ *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError, _ int) (*schemas.BifrostMCPResponse, *schemas.BifrostError) {
	p.seen = bifrostErr
	return resp, bifrostErr
}

func (p *errorRecordingPipeline) RunMCPPreConnectionHooks(_ *schemas.BifrostContext, req *schemas.BifrostMCPConnectRequest) (*schemas.BifrostMCPConnectRequest, *schemas.MCPConnectionShortCircuit, int) {
	return req, nil, 0
}

func (p *errorRecordingPipeline) RunMCPPostConnectionHooks(_ *schemas.BifrostContext, resp *schemas.BifrostMCPConnectResponse, bifrostErr *schemas.BifrostError, _ int) (*schemas.BifrostMCPConnectResponse, *schemas.BifrostError) {
	return resp, bifrostErr
}

// A failed MCP call's error names the client and tool it was for, the way its response would, so a
// post-hook can tell which call failed: when the call itself fails, when a pre-hook refuses it, and
// with no plugins at all.
func TestRunWithPluginPipeline_ErrorsNameTheClientAndTool(t *testing.T) {
	failing := func(*schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		return nil, errors.New("upstream failed")
	}
	for _, tc := range []struct {
		name     string
		pipeline *errorRecordingPipeline
	}{
		{"the call fails", &errorRecordingPipeline{}},
		{"a pre-hook refuses it", &errorRecordingPipeline{short: &schemas.MCPPluginShortCircuit{
			Error: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "refused"}},
		}}},
		{"no plugins", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := schemas.MCPConfig{}
			if tc.pipeline != nil {
				config.PluginPipelineProvider = func() interface{} { return tc.pipeline }
				config.ReleasePluginPipeline = func(interface{}) {}
			}
			m := NewMCPManager(context.Background(), config, nil, &MockLogger{}, nil)
			name := "notes-search"
			req := &schemas.BifrostMCPRequest{
				RequestType:                  schemas.MCPRequestTypeChatToolCall,
				ClientName:                   "notes",
				ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name}},
			}

			_, bErr := m.RunWithPluginPipeline(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), req, failing)
			require.NotNil(t, bErr)
			assert.Equal(t, "notes", bErr.ExtraFields.MCPClientName)
			assert.Equal(t, "notes-search", bErr.ExtraFields.MCPToolName)
			if tc.pipeline != nil {
				require.NotNil(t, tc.pipeline.seen, "the post-hook ran")
				assert.Equal(t, "notes", tc.pipeline.seen.ExtraFields.MCPClientName, "the post-hook sees the client")
				assert.Equal(t, "notes-search", tc.pipeline.seen.ExtraFields.MCPToolName, "and the tool")
			}
		})
	}
}
