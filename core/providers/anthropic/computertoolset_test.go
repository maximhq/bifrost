package anthropic

import (
	"encoding/json"
	"fmt"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roundTripAnthropicBody parses a native Anthropic body, converts it to the
// neutral Responses shape and back, and returns what would go on the wire. The
// toolset's whole contract lives on that round trip: the generation is re-derived
// from the target model, and toolset_name has to survive on both halves of every
// call/result pair.
func roundTripAnthropicBody(t *testing.T, body string) *AnthropicMessageRequest {
	t.Helper()
	var req AnthropicMessageRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostReq := req.ToBifrostResponsesRequest(ctx)
	bifrostReq.Provider = schemas.Anthropic

	out, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, out)
	return out
}

// A toolset entry is bare. Sending a name gets "name is not accepted on a toolset
// entry", and display_width_px/display_height_px get "Extra inputs are not
// permitted" — both verified against the live API.
func TestComputerToolset_EmittedBare(t *testing.T) {
	out := roundTripAnthropicBody(t, `{"model":"claude-opus-5-5","max_tokens":64,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"computer_toolset_20260801"}]}`)

	require.Len(t, out.Tools, 1)
	tool := out.Tools[0]
	require.NotNil(t, tool.Type)
	assert.Equal(t, AnthropicToolTypeComputerToolset20260801, *tool.Type)
	assert.Empty(t, tool.Name, "a toolset entry rejects name outright")
	assert.Nil(t, tool.AnthropicToolComputerUse, "a toolset entry rejects the display_* geometry")

	encoded, err := json.Marshal(tool)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `"name"`, "name must be omitted, not sent empty: %s", encoded)
}

// The generation is a property of the target model, not of what the caller sent,
// so a dated tool aimed at Opus 5.5 is upgraded rather than forwarded into a 400.
func TestComputerToolset_DatedToolUpgradedForOpus55(t *testing.T) {
	out := roundTripAnthropicBody(t, `{"model":"claude-opus-5-5","max_tokens":64,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"computer_20251124","name":"computer","display_width_px":1280,"display_height_px":800}]}`)

	require.Len(t, out.Tools, 1)
	require.NotNil(t, out.Tools[0].Type)
	assert.Equal(t, AnthropicToolTypeComputerToolset20260801, *out.Tools[0].Type)
	assert.Empty(t, out.Tools[0].Name)
}

// The mirror of the case above: Opus 5 still accepts computer_20251124, so it
// must keep getting it rather than being moved onto the toolset.
func TestComputerToolset_Opus5KeepsDatedTool(t *testing.T) {
	out := roundTripAnthropicBody(t, `{"model":"claude-opus-5","max_tokens":64,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"computer_20251124","name":"computer","display_width_px":1280,"display_height_px":800}]}`)

	require.Len(t, out.Tools, 1)
	require.NotNil(t, out.Tools[0].Type)
	assert.Equal(t, AnthropicToolTypeComputer20251124, *out.Tools[0].Type)
	assert.Equal(t, "computer", out.Tools[0].Name)
}

// toolsetName returns the toolset_name on every tool_use / tool_result block of
// the request, keyed by the id the pair shares.
func toolsetNames(req *AnthropicMessageRequest) (calls, results map[string]string) {
	calls, results = map[string]string{}, map[string]string{}
	for _, msg := range req.Messages {
		if msg.Content.ContentBlocks == nil {
			continue
		}
		for _, block := range msg.Content.ContentBlocks {
			name := ""
			if block.ToolsetName != nil {
				name = *block.ToolsetName
			}
			switch block.Type {
			case AnthropicContentBlockTypeToolUse:
				if block.ID != nil {
					calls[*block.ID] = name
				}
			case AnthropicContentBlockTypeToolResult:
				if block.ToolUseID != nil {
					results[*block.ToolUseID] = name
				}
			}
		}
	}
	return calls, results
}

const toolsetReplayBody = `{"model":"claude-opus-5-5","max_tokens":64,
	"tools":[{"type":"computer_toolset_20260801"}],
	"messages":[
		{"role":"user","content":"screenshot then click"},
		{"role":"assistant","content":[
			{"type":"tool_use","id":"toolu_aaa","name":"screenshot","input":{},"toolset_name":"computer"},
			{"type":"tool_use","id":"toolu_bbb","name":"left_click","input":{"coordinate":[100,200]},"toolset_name":"computer"}
		]},
		{"role":"user","content":[%s]}
	]}`

// Anthropic rejects a pair whose halves disagree: a result answering a member
// call without toolset_name, and a result carrying one whose call does not, are
// both 400s. A faithful echo has to survive untouched.
func TestComputerToolset_NameSurvivesSymmetricReplay(t *testing.T) {
	results := `{"type":"tool_result","tool_use_id":"toolu_aaa","toolset_name":"computer","content":"ok"},
		{"type":"tool_result","tool_use_id":"toolu_bbb","toolset_name":"computer","content":"ok"}`
	calls, got := toolsetNames(roundTripAnthropicBody(t, fmt.Sprintf(toolsetReplayBody, results)))

	assert.Equal(t, map[string]string{"toolu_aaa": "computer", "toolu_bbb": "computer"}, calls)
	assert.Equal(t, map[string]string{"toolu_aaa": "computer", "toolu_bbb": "computer"}, got)
}

// A client speaking a dialect without toolset_name — an OpenAI-shaped
// function_call_output, say — returns only the call id. Forwarding that beside a
// member tool_use is the 400 above, so the value is restored from the call.
func TestComputerToolset_NameBackfilledOntoResults(t *testing.T) {
	results := `{"type":"tool_result","tool_use_id":"toolu_aaa","content":"ok"},
		{"type":"tool_result","tool_use_id":"toolu_bbb","content":"ok"}`
	calls, got := toolsetNames(roundTripAnthropicBody(t, fmt.Sprintf(toolsetReplayBody, results)))

	assert.Equal(t, map[string]string{"toolu_aaa": "computer", "toolu_bbb": "computer"}, calls)
	assert.Equal(t, map[string]string{"toolu_aaa": "computer", "toolu_bbb": "computer"},
		got, "a member result must carry its call's toolset_name")
}

// An ordinary function call is not a toolset member, so nothing may be invented
// for it — a toolset_name on a non-member result is equally a 400.
func TestComputerToolset_NameNotInventedForPlainToolUse(t *testing.T) {
	body := `{"model":"claude-opus-5-5","max_tokens":64,
		"messages":[
			{"role":"user","content":"weather?"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_ccc","name":"get_weather","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_ccc","content":"sunny"}]}
		]}`
	calls, results := toolsetNames(roundTripAnthropicBody(t, body))

	assert.Equal(t, map[string]string{"toolu_ccc": ""}, calls)
	assert.Equal(t, map[string]string{"toolu_ccc": ""}, results)
}

// The datasheet names the exact tool type, so a row moves a model onto the
// toolset (or holds it back) without a release.
func TestComputerToolset_DatasheetOutranksFallback(t *testing.T) {
	t.Run("row moves a model the fallback leaves on the dated tool", func(t *testing.T) {
		setOverride(t, "claude-opus-5", schemas.ModelCapabilities{
			ServerTools: map[string]string{"computer_use": string(AnthropicToolTypeComputerToolset20260801)},
		})
		assert.Equal(t, ComputerUseGenToolset20260801,
			ComputerUseGeneration(schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-5")))
	})

	t.Run("row holds a model back on the dated tool", func(t *testing.T) {
		setOverride(t, "claude-opus-5-5", schemas.ModelCapabilities{
			ServerTools: map[string]string{"computer_use": string(AnthropicToolTypeComputer20251124)},
		})
		assert.Equal(t, ComputerUseGen20251124,
			ComputerUseGeneration(schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-5-5")))
	})
}

// The chat converter drops any tool it cannot name, which would have swallowed
// the one entry that legitimately has no name.
func TestComputerToolset_ChatPathKeepsBareEntry(t *testing.T) {
	caps := schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-5-5")
	tool, ok := convertServerToolToAnthropic(schemas.ChatTool{
		Type: schemas.ChatToolType(AnthropicToolTypeComputer20251124),
	}, caps, schemas.Anthropic)

	require.True(t, ok, "the toolset must not be dropped for having no name")
	require.NotNil(t, tool.Type)
	assert.Equal(t, AnthropicToolTypeComputerToolset20260801, *tool.Type)
	assert.Empty(t, tool.Name)
}

// A streaming member call arrives with toolset_name on content_block_start. The
// gateway re-encodes the stream rather than forwarding bytes, so the field has to
// be threaded through the inbound parse and the outbound rebuild alike — dropping
// it leaves a streaming client unable to answer the call it was just handed.
func TestComputerToolset_NameSurvivesStreaming(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	state := &AnthropicResponsesStreamState{
		ContentIndexToOutputIndex: make(map[int]int),
		ContentIndexToBlockType:   make(map[int]AnthropicContentBlockType),
		ToolArgumentBuffers:       make(map[int]string),
		MCPCallOutputIndices:      make(map[int]bool),
		ItemIDs:                   make(map[int]string),
		OutputItems:               make(map[int]*schemas.ResponsesMessage),
		ReasoningSignatures:       make(map[int]string),
		TextContentIndices:        make(map[int]bool),
		ReasoningContentIndices:   make(map[int]bool),
		CompactionContentIndices:  make(map[int]*schemas.CacheControl),
		CreatedAt:                 1234567890,
		HasEmittedCreated:         true,
		HasEmittedInProgress:      true,
	}

	inbound := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockStart,
		Index: schemas.Ptr(0),
		ContentBlock: &AnthropicContentBlock{
			Type:        AnthropicContentBlockTypeToolUse,
			ID:          schemas.Ptr("toolu_stream1"),
			Name:        schemas.Ptr("screenshot"),
			Input:       json.RawMessage(`{}`),
			ToolsetName: schemas.Ptr("computer"),
		},
	}

	neutral, bifrostErr, _ := inbound.ToBifrostResponsesStream(ctx, 0, state)
	require.Nil(t, bifrostErr)
	require.NotEmpty(t, neutral, "content_block_start must produce a neutral event")

	var sawStart bool
	for _, event := range neutral {
		if event.Item != nil && event.Item.ResponsesToolMessage != nil {
			require.NotNil(t, event.Item.ResponsesToolMessage.ToolsetName,
				"the neutral function_call lost toolset_name")
			assert.Equal(t, "computer", *event.Item.ResponsesToolMessage.ToolsetName)
		}
		for _, out := range ToAnthropicResponsesStreamResponse(ctx, event) {
			if out.Type != AnthropicStreamEventTypeContentBlockStart || out.ContentBlock == nil {
				continue
			}
			if out.ContentBlock.Type != AnthropicContentBlockTypeToolUse {
				continue
			}
			sawStart = true
			require.NotNil(t, out.ContentBlock.ToolsetName,
				"content_block_start went back out without toolset_name")
			assert.Equal(t, "computer", *out.ContentBlock.ToolsetName)
		}
	}
	assert.True(t, sawStart, "no tool_use content_block_start was re-emitted")
}

// On the surfaces that remap raw bodies (Vertex, Bedrock, Mantle) a dated tool
// aimed at Opus 5.5 is upgraded rather than relayed into a 400, and the name and
// display_* geometry the toolset rejects are shed. Anthropic direct does not
// remap raw bodies, so its passthrough forwards the caller's bytes untouched.
func TestComputerToolset_RawBodyUpgradedToToolset(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","max_tokens":64,` +
		`"tools":[{"type":"computer_20251124","name":"computer","display_width_px":1280,"display_height_px":800,"display_number":1}]}`)

	out, err := RemapRawToolVersionsForProvider(body, schemas.Vertex, "claude-opus-5-5")
	require.NoError(t, err)
	t.Logf("normalized body: %s", out)

	assert.Equal(t, string(AnthropicToolTypeComputerToolset20260801),
		providerUtils.GetJSONField(out, "tools.0.type").String())
	for _, field := range []string{"name", "display_width_px", "display_height_px", "display_number"} {
		assert.False(t, providerUtils.JSONFieldExists(out, "tools.0."+field),
			"%s is rejected on a toolset entry but survived: %s", field, out)
	}
}

// A toolset carries no display geometry, and the dated computer tools validate
// display_*_px as >= 1, so one cannot be rewritten as the other. Where the target
// takes a toolset the caller's entry is forwarded as sent; where it takes neither
// form the tool is dropped, because a zero-sized display is a hard 400 and would
// turn a re-routed request into a failure rather than a degraded one.
func TestComputerToolset_ForwardedWhenTargetAcceptsIt(t *testing.T) {
	// Every model the computer-use docs list for the toolset, on the two surfaces
	// that serve it. All of these also accept the dated tool, so the generation
	// says "dated" — forwarding still has to win, or the caller loses the toolset
	// they explicitly asked for.
	cases := []struct {
		provider schemas.ModelProvider
		model    string
	}{
		{schemas.Anthropic, "claude-opus-5"},
		{schemas.Anthropic, "claude-sonnet-5"},
		{schemas.Anthropic, "claude-opus-4-8"},
		{schemas.Anthropic, "claude-fable-5-1"},
		{schemas.Vertex, "claude-opus-5"},
	}
	for _, tc := range cases {
		t.Run(string(tc.provider)+"/"+tc.model, func(t *testing.T) {
			caps := schemas.ResolveModelCaps(tc.provider, tc.model)
			tool := convertBifrostToolToAnthropic(caps, &schemas.ResponsesTool{
				Type:                            schemas.ResponsesToolTypeComputerUsePreview,
				ResponsesToolComputerUsePreview: &schemas.ResponsesToolComputerUsePreview{Environment: "browser"},
			}, tc.provider, false)

			require.NotNil(t, tool, "the model takes a toolset, so it must not be dropped")
			require.NotNil(t, tool.Type)
			assert.Equal(t, AnthropicToolTypeComputerToolset20260801, *tool.Type)
			assert.Empty(t, tool.Name, "a toolset entry rejects name")
			assert.Nil(t, tool.AnthropicToolComputerUse, "a toolset entry rejects the display_* geometry")
		})
	}
}

func TestComputerToolset_DroppedWhenTargetTakesNeitherForm(t *testing.T) {
	// AWS and Foundry serve only the dated tool, and Sonnet 4.6 and older reject
	// the toolset on every surface — so there is no shape left to send.
	cases := []struct {
		provider schemas.ModelProvider
		model    string
	}{
		{schemas.Bedrock, "claude-opus-5-5"},
		{schemas.BedrockMantle, "claude-opus-5-5"},
		{schemas.Azure, "claude-opus-5-5"},
		{schemas.Anthropic, "claude-sonnet-4-6"},
		{schemas.Vertex, "claude-sonnet-4-6"},
	}
	for _, tc := range cases {
		t.Run(string(tc.provider)+"/"+tc.model, func(t *testing.T) {
			caps := schemas.ResolveModelCaps(tc.provider, tc.model)
			require.False(t, AcceptsComputerToolset(caps), "case is only meaningful where the toolset is rejected")

			tool := convertBifrostToolToAnthropic(caps, &schemas.ResponsesTool{
				Type:                            schemas.ResponsesToolTypeComputerUsePreview,
				ResponsesToolComputerUsePreview: &schemas.ResponsesToolComputerUsePreview{Environment: "browser"},
			}, tc.provider, false)

			assert.Nil(t, tool, "a dimensionless computer tool must be dropped, not sent as a 0x0 display")
		})
	}
}

// supports_computer_toolset moves a model onto or off the toolset without a
// release, in both directions.
func TestComputerToolset_AcceptanceDatasheetOutranksFallback(t *testing.T) {
	t.Run("row grants the toolset to a model the fallback withholds it from", func(t *testing.T) {
		yes := true
		setOverride(t, "claude-sonnet-4-6", schemas.ModelCapabilities{SupportsComputerToolset: &yes})
		assert.True(t, AcceptsComputerToolset(schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-6")))
	})

	t.Run("row withholds it from a model the fallback grants", func(t *testing.T) {
		no := false
		setOverride(t, "claude-opus-5", schemas.ModelCapabilities{SupportsComputerToolset: &no})
		assert.False(t, AcceptsComputerToolset(schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-5")))
	})

	t.Run("a row cannot grant it on a surface that does not serve it", func(t *testing.T) {
		yes := true
		providerUtils.SetCapabilityResolver(func(p schemas.ModelProvider, m string) *schemas.ModelCapabilities {
			return &schemas.ModelCapabilities{SupportsComputerToolset: &yes}
		})
		t.Cleanup(func() { providerUtils.SetCapabilityResolver(nil) })
		assert.False(t, AcceptsComputerToolset(schemas.ResolveModelCaps(schemas.Bedrock, "claude-opus-5-5")),
			"AWS serves only the dated tool regardless of the row")
	})
}

// The guard keys off the geometry, not off where the tool came from, so a genuine
// computer_use_preview still converts untouched.
func TestComputerToolset_RealGeometryStillConverts(t *testing.T) {
	caps := schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-5")
	tool := convertBifrostToolToAnthropic(caps, &schemas.ResponsesTool{
		Type: schemas.ResponsesToolTypeComputerUsePreview,
		ResponsesToolComputerUsePreview: &schemas.ResponsesToolComputerUsePreview{
			Environment: "browser", DisplayWidth: 1280, DisplayHeight: 800,
		},
	}, schemas.Anthropic, false)

	require.NotNil(t, tool, "a tool with a real display must still convert")
	require.NotNil(t, tool.Type)
	assert.Equal(t, AnthropicToolTypeComputer20251124, *tool.Type)
	assert.Equal(t, "computer", tool.Name)
	require.NotNil(t, tool.AnthropicToolComputerUse)
	assert.Equal(t, 1280, *tool.AnthropicToolComputerUse.DisplayWidthPx)
	assert.Equal(t, 800, *tool.AnthropicToolComputerUse.DisplayHeightPx)
}

// The raw path downgrades by rewriting each tool in place, so it needs the same
// guard: a toolset entry there would otherwise become a dated tool with neither a
// name nor a display.
func TestComputerToolset_RawBodyDropsUndowngradableToolset(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":64,` +
		`"tools":[{"type":"computer_toolset_20260801"},` +
		`{"type":"text_editor_20250728","name":"str_replace_based_edit_tool"},` +
		`{"type":"bash_20250124","name":"bash"}]}`)

	out, err := RemapRawToolVersionsForProvider(body, schemas.Vertex, "claude-sonnet-4-6")
	require.NoError(t, err)

	types := []string{}
	for _, tool := range providerUtils.GetJSONField(out, "tools").Array() {
		types = append(types, tool.Get("type").String())
	}
	assert.NotContains(t, types, "computer_20251124", "undowngradable toolset must not become a dated tool: %s", out)
	assert.NotContains(t, types, "computer_toolset_20260801", "the target cannot take a toolset either: %s", out)
	assert.Contains(t, types, "text_editor_20250728", "sibling tools carry no geometry and must survive: %s", out)
	assert.Contains(t, types, "bash_20250124", "sibling tools carry no geometry and must survive: %s", out)
}
