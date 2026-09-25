package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertToolFunctionParametersToMCPInputSchemaPreservesDefs(t *testing.T) {
	params := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("preferences", map[string]any{"$ref": "#/$defs/Preferences"}),
		),
		Required: []string{"preferences"},
		Defs: schemas.NewOrderedMapFromPairs(
			schemas.KV("Preferences", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"startHour": map[string]any{"type": "string"},
				},
			}),
		),
	}

	inputSchema := convertToolFunctionParametersToMCPInputSchema(params)

	require.Contains(t, inputSchema.Defs, "Preferences")
	data, err := json.Marshal(inputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"$defs"`)
	assert.Contains(t, string(data), `"$ref":"#/$defs/Preferences"`)
}

func TestConvertToolFunctionParametersToMCPInputSchemaPreservesLegacyDefinitionsAsDefs(t *testing.T) {
	params := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("preferences", map[string]any{"$ref": "#/$defs/Preferences"}),
		),
		Definitions: schemas.NewOrderedMapFromPairs(
			schemas.KV("Preferences", map[string]any{"type": "object"}),
		),
	}

	inputSchema := convertToolFunctionParametersToMCPInputSchema(params)

	require.Contains(t, inputSchema.Defs, "Preferences")
	data, err := json.Marshal(inputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"$defs"`)
}

// Forwarding upstream MCP `instructions` on the gateway's own initialize response.

// fakeInstructionsToolManager reports a fixed aggregate and records the context it was asked
// with, so a test can assert the hook resolves the text per request rather than once at build.
type fakeInstructionsToolManager struct {
	instructions string
	seenCtx      context.Context
}

func (f *fakeInstructionsToolManager) GetAvailableMCPTools(context.Context) []schemas.ChatTool {
	return nil
}

func (f *fakeInstructionsToolManager) GetMCPServerInstructions(ctx context.Context) string {
	f.seenCtx = ctx
	return f.instructions
}

func (f *fakeInstructionsToolManager) ExecuteChatMCPTool(context.Context, *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	return nil, nil
}

func (f *fakeInstructionsToolManager) ExecuteResponsesMCPTool(context.Context, *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError) {
	return nil, nil
}

// initializeResult drives a real initialize through the built server and returns the decoded
// result, which is what a connecting MCP client would actually receive.
func initializeResult(t *testing.T, mode schemas.MCPServerInstructionsMode, tm MCPToolManager, ctx context.Context) map[string]any {
	t.Helper()
	h := &MCPServerHandler{
		toolManager: tm,
		config: &lib.Config{ClientConfig: &configstore.ClientConfig{
			MCPServerInstructionsMode: string(mode),
		}},
	}
	h.mcpServer.Store(h.buildServer(nil))

	raw := h.server().HandleMessage(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0.0.1"}}}`))
	require.NotNil(t, raw)

	encoded, err := json.Marshal(raw)
	require.NoError(t, err)
	var envelope struct {
		Result map[string]any `json:"result"`
	}
	require.NoError(t, json.Unmarshal(encoded, &envelope))
	return envelope.Result
}

func TestInitializeForwardsInstructionsWhenModeIsGateway(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: "<mcp_server name=\"github\">\nAlways call get_me first.\n</mcp_server>"}

	result := initializeResult(t, schemas.MCPServerInstructionsModeGateway, tm, context.Background())

	assert.Equal(t, "<mcp_server name=\"github\">\nAlways call get_me first.\n</mcp_server>", result["instructions"])
}

// Off is the default, and must leave the response byte-identical to what it was before this
// feature: the field absent entirely, not present and empty.
func TestInitializeOmitsInstructionsWhenModeIsOff(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: "<mcp_server name=\"github\">\nshould not appear\n</mcp_server>"}

	result := initializeResult(t, schemas.MCPServerInstructionsModeOff, tm, context.Background())

	_, present := result["instructions"]
	assert.False(t, present, "instructions must be absent at mode=off")
}

// Nothing to say means no field, rather than an empty one a client would have to special-case.
func TestInitializeOmitsInstructionsWhenNoUpstreamAdvertisesAny(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: ""}

	result := initializeResult(t, schemas.MCPServerInstructionsModeGateway, tm, context.Background())

	_, present := result["instructions"]
	assert.False(t, present)
}

// The hook must resolve against the per-request context — that is the whole reason it is a
// hook rather than server.WithInstructions, which is fixed at construction. If the request's
// narrowing did not reach the resolver, a caller scoped to one upstream would be handed every
// upstream's text.
func TestInitializeResolvesInstructionsFromTheRequestContext(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: "<mcp_server name=\"alpha\">\nscoped\n</mcp_server>"}
	ctx := context.WithValue(context.Background(), schemas.MCPContextKeyIncludeClients, []string{"alpha"})

	initializeResult(t, schemas.MCPServerInstructionsModeGateway, tm, ctx)

	require.NotNil(t, tm.seenCtx)
	assert.Equal(t, []string{"alpha"}, tm.seenCtx.Value(schemas.MCPContextKeyIncludeClients))
}

// A /mcp/<slug> request is narrowed by admitBySlug, which stamps the served tool list on
// MCPContextKeyIncludeTools rather than include-clients. The resolver has to see that narrowing
// too, or a Virtual MCP endpoint would answer initialize with every upstream's instructions.
func TestInitializeResolvesInstructionsForSlugNarrowedRequests(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: "<mcp_server name=\"alpha\">\nscoped\n</mcp_server>"}
	ctx := context.WithValue(context.Background(), schemas.MCPContextKeyIncludeTools, []string{"alpha-echo"})

	result := initializeResult(t, schemas.MCPServerInstructionsModeGateway, tm, ctx)

	assert.Equal(t, "<mcp_server name=\"alpha\">\nscoped\n</mcp_server>", result["instructions"])
	require.NotNil(t, tm.seenCtx)
	assert.Equal(t, []string{"alpha-echo"}, tm.seenCtx.Value(schemas.MCPContextKeyIncludeTools),
		"the slug's served tool list must reach the instructions resolver")
}

// mode=all is a superset of gateway: the handshake must still carry instructions, not just the
// LLM path. A mode check written as equality rather than a threshold would break this.
func TestInitializeForwardsInstructionsWhenModeIsAll(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: "<mcp_server name=\"github\">\ntext\n</mcp_server>"}

	result := initializeResult(t, schemas.MCPServerInstructionsModeAll, tm, context.Background())

	assert.Equal(t, "<mcp_server name=\"github\">\ntext\n</mcp_server>", result["instructions"])
}

func TestInitializeAppendsVirtualMCPInstructions(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: "<mcp_server name=\"github\">\ninherited\n</mcp_server>"}
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyMCPVirtualInstructions,
		schemas.MCPVirtualInstructions{Name: "finance", Text: "Never touch production.", Mode: schemas.MCPVirtualInstructionsModeAppend})

	result := initializeResult(t, schemas.MCPServerInstructionsModeGateway, tm, ctx)

	assert.Equal(t,
		"<mcp_server name=\"github\">\ninherited\n</mcp_server>\n\nNever touch production.", result["instructions"])
}

func TestInitializeReplacesInheritedInstructionsForVirtualMCP(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: "<mcp_server name=\"big\">\nuse dangerous with approval\n</mcp_server>"}
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyMCPVirtualInstructions,
		schemas.MCPVirtualInstructions{Name: "safe", Text: "Read-only.", Mode: schemas.MCPVirtualInstructionsModeReplace})

	result := initializeResult(t, schemas.MCPServerInstructionsModeGateway, tm, ctx)

	assert.Equal(t, "Read-only.", result["instructions"])
	assert.NotContains(t, result["instructions"], "dangerous")
}

func TestInitializeOmitsVirtualMCPInstructionsWhenModeIsOff(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: ""}
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyMCPVirtualInstructions,
		schemas.MCPVirtualInstructions{Name: "v", Text: "should not appear", Mode: schemas.MCPVirtualInstructionsModeReplace})

	result := initializeResult(t, schemas.MCPServerInstructionsModeOff, tm, ctx)

	_, present := result["instructions"]
	assert.False(t, present)
}

// fakeSlugResolver drives admitBySlug directly, and guards mcpSlugResolver: a method added
// there without every implementation gaining it 403s every /mcp/<slug> at runtime. This
// failing to compile is the early warning.
type fakeSlugResolver struct {
	fakeAdmitter
	served       []string
	instructions schemas.MCPVirtualInstructions
	assigned     bool
}

func (f *fakeSlugResolver) VirtualMCPToolAccess(_ *schemas.BifrostContext, _ string, _ schemas.Access) ([]string, schemas.MCPVirtualInstructions, bool) {
	return f.served, f.instructions, f.assigned
}

func (f *fakeSlugResolver) MCPClientToolAccess(_ *schemas.BifrostContext, _ string, _ schemas.Access) ([]string, bool) {
	return nil, false
}

func TestAdmitBySlugStampsVirtualMCPInstructions(t *testing.T) {
	for _, mode := range []schemas.MCPVirtualInstructionsMode{
		schemas.MCPVirtualInstructionsModeAppend,
		schemas.MCPVirtualInstructionsModeReplace,
	} {
		t.Run(string(mode), func(t *testing.T) {
			resolver := &fakeSlugResolver{
				served:       []string{"alpha-echo"},
				instructions: schemas.MCPVirtualInstructions{Name: "finance", Text: "Never touch production.", Mode: mode},
				assigned:     true,
			}
			h := &MCPServerHandler{admitter: resolver}
			_, bifrostCtx := newRequestCtx()

			require.Nil(t, h.admitBySlug(bifrostCtx, "finance", nil))

			assert.Equal(t, []string{"alpha-echo"}, bifrostCtx.Value(schemas.MCPContextKeyIncludeTools))
			got, ok := bifrostCtx.Value(schemas.BifrostContextKeyMCPVirtualInstructions).(schemas.MCPVirtualInstructions)
			require.True(t, ok, "the hook reads this key; without it the vMCP's text never reaches initialize")
			assert.Equal(t, "Never touch production.", got.Text)
			assert.Equal(t, mode, got.Mode)
		})
	}
}

// Unset, not empty: the hook must fall through to the inherited aggregate.
func TestAdmitBySlugLeavesKeyUnsetWithoutVirtualInstructions(t *testing.T) {
	resolver := &fakeSlugResolver{served: []string{"alpha-echo"}, assigned: true}
	h := &MCPServerHandler{admitter: resolver}
	_, bifrostCtx := newRequestCtx()

	require.Nil(t, h.admitBySlug(bifrostCtx, "plain", nil))

	assert.Nil(t, bifrostCtx.Value(schemas.BifrostContextKeyMCPVirtualInstructions))
}

func TestAdmitBySlugRefusesUnassignedSlugWithoutStamping(t *testing.T) {
	resolver := &fakeSlugResolver{
		instructions: schemas.MCPVirtualInstructions{Name: "secret", Text: "should not leak"},
	}
	h := &MCPServerHandler{admitter: resolver}
	_, bifrostCtx := newRequestCtx()

	refusal := h.admitBySlug(bifrostCtx, "secret", nil)

	require.NotNil(t, refusal)
	assert.Nil(t, bifrostCtx.Value(schemas.BifrostContextKeyMCPVirtualInstructions))
}
