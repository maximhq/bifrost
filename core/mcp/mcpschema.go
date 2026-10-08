package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// discoveredMCPTool keeps the JSON Schema objects before the SDK's limited
// ToolArgumentsSchema projection discards unknown keywords. The typed view is
// still used by the existing LLM compatibility conversion.
type discoveredMCPTool struct{ mcp.Tool }

func canonicalMCPSchema(raw json.RawMessage) (json.RawMessage, error) {
	if raw == nil {
		return nil, nil
	}
	var schema any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&schema); err != nil {
		return nil, err
	}
	switch schema.(type) {
	case map[string]any, bool:
	default:
		return nil, fmt.Errorf("MCP schema must be an object or boolean")
	}
	return schemas.MarshalSorted(schema)
}

func (t *discoveredMCPTool) UnmarshalJSON(data []byte) error {
	// Decode the contract before the SDK's typed view. Valid JSON Schema may
	// contain boolean schemas or a type union the SDK cannot represent.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	input, output := fields["inputSchema"], fields["outputSchema"]
	delete(fields, "inputSchema")
	delete(fields, "outputSchema")
	rest, err := schemas.MarshalSorted(fields)
	if err != nil {
		return err
	}
	*t = discoveredMCPTool{}
	if err := json.Unmarshal(rest, &t.Tool); err != nil {
		return err
	}
	t.RawInputSchema, err = canonicalMCPSchema(input)
	if err != nil {
		return fmt.Errorf("input schema: %w", err)
	}
	t.RawOutputSchema, err = canonicalMCPSchema(output)
	if err != nil {
		return fmt.Errorf("output schema: %w", err)
	}
	// Preserve the existing provider projection when expressible. A richer MCP
	// contract always stays in raw metadata; it never leaks into provider JSON.
	var typed mcp.ToolInputSchema
	if len(input) > 0 && bytes.TrimSpace(input)[0] == '{' {
		if json.Unmarshal(input, &typed) == nil {
			t.InputSchema = typed
		}
	}
	// Provider tools require an object type even when the raw MCP schema cannot
	// be represented by the SDK projection. Keep an absent input schema absent.
	if len(input) > 0 && t.InputSchema.Type == "" {
		t.InputSchema.Type = "object"
	}
	return nil
}

// listToolsPreservingSchemas uses the already initialized SDK transport, so
// auth, sessions, protocol negotiation, cancellation and headers remain on the
// same connection. The SDK's ListTools has no raw-result hook; decoding its
// typed result and then re-encoding cannot recover schema keywords. UUID IDs
// cannot collide with that client's concurrent numeric request IDs.
func listToolsPreservingSchemas(ctx context.Context, c *client.Client, request mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	result := &mcp.ListToolsResult{}
	seenCursors := make(map[string]bool)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		response, err := c.GetTransport().SendRequest(ctx, transport.JSONRPCRequest{
			JSONRPC: mcp.JSONRPC_VERSION, ID: mcp.NewRequestId(uuid.NewString()), Method: string(mcp.MethodToolsList), Params: request.Params,
		})
		if err != nil {
			return nil, transport.NewError(err)
		}
		if response == nil {
			return nil, fmt.Errorf("empty tools/list response")
		}
		if response.Error != nil {
			return nil, response.Error.AsError()
		}
		var page struct {
			Tools      []discoveredMCPTool `json:"tools"`
			NextCursor string              `json:"nextCursor"`
		}
		if err := json.Unmarshal(response.Result, &page); err != nil {
			return nil, fmt.Errorf("failed to unmarshal tools/list: %w", err)
		}
		for _, tool := range page.Tools {
			result.Tools = append(result.Tools, tool.Tool)
		}
		if page.NextCursor == "" {
			return result, nil
		}
		if seenCursors[page.NextCursor] {
			return nil, fmt.Errorf("repeated tools/list cursor")
		}
		seenCursors[page.NextCursor] = true
		request.Params.Cursor = mcp.Cursor(page.NextCursor)
	}
}
