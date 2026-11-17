package utils

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func customResponsesTool(name string) schemas.ResponsesTool {
	return schemas.ResponsesTool{
		Type:        schemas.ResponsesToolTypeCustom,
		Name:        schemas.Ptr(name),
		Description: schemas.Ptr("apply a patch"),
		ResponsesToolCustom: &schemas.ResponsesToolCustom{Format: &schemas.ResponsesToolCustomFormat{
			Type:       "grammar",
			Syntax:     schemas.Ptr("lark"),
			Definition: schemas.Ptr("start: /.+/"),
		}},
	}
}

func customToolsResponsesRequest(provider schemas.ModelProvider, model string) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ResponsesMessage{
			{
				ID:   schemas.Ptr("ctc_1"),
				Type: schemas.Ptr(schemas.ResponsesMessageTypeCustomToolCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:                  schemas.Ptr("call_1"),
					Name:                    schemas.Ptr("apply_patch"),
					ResponsesCustomToolCall: &schemas.ResponsesCustomToolCall{Input: "*** Begin Patch \"x\""},
				},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeCustomToolCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("call_1"),
					Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("done")},
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{customResponsesTool("apply_patch")},
			ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
				Type: schemas.ResponsesToolChoiceTypeCustom,
				Name: schemas.Ptr("apply_patch"),
			}},
		},
	}
}

func customChatRequest(provider schemas.ModelProvider, model string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{
				Type: schemas.ChatToolTypeCustom,
				Custom: &schemas.ChatToolCustom{Name: "ApplyPatch", Description: schemas.Ptr("Apply a patch"), Format: &schemas.ChatToolCustomFormat{
					Type:    "grammar",
					Grammar: &schemas.ChatToolCustomGrammarFormat{Syntax: "regex", Definition: "^\\d+$"},
				}},
			}},
			ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
				Type:   schemas.ChatToolChoiceTypeCustom,
				Custom: &schemas.ChatToolChoiceCustom{Name: "ApplyPatch"},
			}},
		},
	}
}

// stubCustomToolsDatasheet gives openai/gpt-5 a true row and openai/gpt-4o a false
// row; every other pair has no row.
func stubCustomToolsDatasheet(t *testing.T) {
	schemas.SetCapabilityResolver(func(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		if provider != schemas.OpenAI {
			return nil
		}
		switch model {
		case "gpt-5":
			return &schemas.ModelCapabilities{SupportsCustomTools: schemas.Ptr(true)}
		case "gpt-4o":
			return &schemas.ModelCapabilities{SupportsCustomTools: schemas.Ptr(false)}
		}
		return nil
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
}

func TestRewriteResponsesCustomTools(t *testing.T) {
	stubCustomToolsDatasheet(t)
	for _, target := range []struct {
		provider schemas.ModelProvider
		model    string
	}{
		{schemas.OpenAI, "gpt-4o"},       // false row
		{schemas.OpenAI, "gpt-4.1-mini"}, // no row: unsupported by default
		{schemas.Anthropic, "claude-sonnet-4-5"},
		{schemas.Groq, "openai/gpt-oss-120b"},
	} {
		original := customToolsResponsesRequest(target.provider, target.model)
		got := RewriteResponsesCustomTools(nil, target.provider, original)
		require.NotSame(t, original, got, target.provider)

		tool := got.Params.Tools[0]
		require.Equal(t, schemas.ResponsesToolTypeFunction, tool.Type)
		require.Nil(t, tool.ResponsesToolCustom)
		require.Equal(t, "apply_patch", *tool.Name)
		require.Equal(t, "apply a patch", *tool.Description)
		require.Equal(t, []string{customToolInputParam}, tool.ResponsesToolFunction.Parameters.Required)
		input, ok := tool.ResponsesToolFunction.Parameters.Properties.Get(customToolInputParam)
		require.True(t, ok)
		description, _ := input.(*schemas.OrderedMap).Get("description")
		require.Contains(t, description, "lark")
		require.Contains(t, description, "start: /.+/")

		choice := got.Params.ToolChoice.ResponsesToolChoiceStruct
		require.Equal(t, schemas.ResponsesToolChoiceTypeFunction, choice.Type)
		require.Equal(t, "apply_patch", *choice.Name)

		require.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *got.Input[0].Type)
		require.Nil(t, got.Input[0].ID, "a ctc_ id is rejected on a function_call input")
		require.JSONEq(t, `{"input":"*** Begin Patch \"x\""}`, *got.Input[0].Arguments)
		require.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *got.Input[1].Type)
		require.Equal(t, map[string]struct{}{"apply_patch": {}}, got.CustomToolNames)

		// The caller's request is untouched, since fallbacks re-run the preparation.
		require.Equal(t, schemas.ResponsesToolTypeCustom, original.Params.Tools[0].Type)
		require.Equal(t, schemas.ResponsesToolChoiceTypeCustom, original.Params.ToolChoice.ResponsesToolChoiceStruct.Type)
		require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *original.Input[0].Type)
		require.Equal(t, "ctc_1", *original.Input[0].ID)
		require.Nil(t, original.Input[0].Arguments)
		require.Equal(t, schemas.ResponsesMessageTypeCustomToolCallOutput, *original.Input[1].Type)
		require.Nil(t, original.CustomToolNames)
	}
}

func TestRewriteResponsesCustomTools_LeavesRequestAlone(t *testing.T) {
	stubCustomToolsDatasheet(t)
	supported := customToolsResponsesRequest(schemas.OpenAI, "gpt-5")
	require.Same(t, supported, RewriteResponsesCustomTools(nil, schemas.OpenAI, supported), "a true row keeps custom tools")

	plain := &schemas.BifrostResponsesRequest{Model: "claude-sonnet-4-5", Params: &schemas.ResponsesParameters{
		Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("search")}},
	}}
	require.Same(t, plain, RewriteResponsesCustomTools(nil, schemas.Anthropic, plain))
}

// A custom tool nested in a namespace is rewritten in a copy and keyed by its namespace.
func TestRewriteResponsesCustomTools_Namespace(t *testing.T) {
	original := &schemas.BifrostResponsesRequest{Model: "m", Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{
		Type: schemas.ResponsesToolTypeNamespace,
		Name: schemas.Ptr("shell"),
		ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{
			customResponsesTool("run"),
			{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("wait"), ResponsesToolFunction: &schemas.ResponsesToolFunction{}},
		}},
	}}}}
	got := RewriteResponsesCustomTools(nil, schemas.Bedrock, original)
	members := got.Params.Tools[0].ResponsesToolNamespace.Tools
	require.Equal(t, schemas.ResponsesToolTypeFunction, members[0].Type)
	require.Equal(t, schemas.ResponsesToolTypeFunction, members[1].Type)
	require.Equal(t, map[string]struct{}{"shell\x00run": {}}, got.CustomToolNames)
	require.Equal(t, schemas.ResponsesToolTypeCustom, original.Params.Tools[0].ResponsesToolNamespace.Tools[0].Type)
}

func TestResponsesCustomToolChoiceAsFunction_AllowedTools(t *testing.T) {
	original := &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
		Type: schemas.ResponsesToolChoiceTypeAllowedTools,
		Mode: schemas.Ptr("required"),
		Tools: []schemas.ResponsesToolChoiceAllowedToolDef{
			{Type: "custom", Name: schemas.Ptr("apply_patch")},
			{Type: "function", Name: schemas.Ptr("search")},
		},
	}}
	got := responsesCustomToolChoiceAsFunction(original)
	require.Equal(t, "function", got.ResponsesToolChoiceStruct.Tools[0].Type)
	require.Equal(t, "function", got.ResponsesToolChoiceStruct.Tools[1].Type)
	require.Equal(t, "custom", original.ResponsesToolChoiceStruct.Tools[0].Type)
}

func TestResponsesAdditionalToolsNeedHoist(t *testing.T) {
	stubCustomToolsDatasheet(t)
	item := func(tools string) *schemas.BifrostResponsesRequest {
		return &schemas.BifrostResponsesRequest{Model: "gpt-4o", Input: []schemas.ResponsesMessage{{
			Type:            schemas.Ptr(schemas.ResponsesMessageTypeAdditionalTools),
			AdditionalTools: []byte(tools),
		}}}
	}
	custom := `[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}]`
	require.True(t, ResponsesAdditionalToolsNeedHoist(nil, schemas.OpenAI, item(custom)))
	require.False(t, ResponsesAdditionalToolsNeedHoist(nil, schemas.OpenAI, item(`[{"type":"function","name":"wait"}]`)))
	supported := item(custom)
	supported.Model = "gpt-5"
	require.False(t, ResponsesAdditionalToolsNeedHoist(nil, schemas.OpenAI, supported))

	// Only a decoded tool type counts: a name, description or schema value of "custom" does not.
	for _, tools := range []string{
		`[{"type":"function","name":"custom","description":"\"custom\"","parameters":{"type":"object","properties":{"kind":{"enum":["custom"]}}}}]`,
		`[{"type":"namespace","name":"custom","tools":[{"type":"function","name":"custom"}]}]`,
	} {
		require.False(t, ResponsesAdditionalToolsNeedHoist(nil, schemas.OpenAI, item(tools)), tools)
	}
	// An escaped type value and a custom tool in a nested namespace are still found.
	escaped := item(`[{"type":"custom","name":"exec"}]`)
	require.True(t, ResponsesAdditionalToolsNeedHoist(nil, schemas.OpenAI, escaped))
	require.Equal(t, `[{"type":"custom","name":"exec"}]`, string(escaped.Input[0].AdditionalTools), "the raw item is left untouched")
	nested := `[{"type":"namespace","name":"outer","tools":[{"type":"namespace","name":"inner","tools":[{"type":"custom","name":"exec"}]}]}]`
	require.True(t, ResponsesAdditionalToolsNeedHoist(nil, schemas.OpenAI, item(nested)))
	require.False(t, ResponsesAdditionalToolsNeedHoist(nil, schemas.OpenAI, item(`not json`)))
}

func TestRewriteChatCustomTools(t *testing.T) {
	stubCustomToolsDatasheet(t)
	original := customChatRequest(schemas.Anthropic, "claude-sonnet-4-5")
	got := RewriteChatCustomTools(nil, schemas.Anthropic, original)

	tool := got.Params.Tools[0]
	require.Equal(t, schemas.ChatToolTypeFunction, tool.Type)
	require.Nil(t, tool.Custom)
	require.Equal(t, "ApplyPatch", tool.Function.Name)
	require.Equal(t, "Apply a patch", *tool.Function.Description)
	input, ok := tool.Function.Parameters.Properties.Get(customToolInputParam)
	require.True(t, ok)
	description, _ := input.(*schemas.OrderedMap).Get("description")
	require.Contains(t, description, "^\\d+$")
	body, err := schemas.MarshalSorted(tool)
	require.NoError(t, err)
	require.NotContains(t, string(body), `"custom"`)

	choice := got.Params.ToolChoice.ChatToolChoiceStruct
	require.Equal(t, schemas.ChatToolChoiceTypeFunction, choice.Type)
	require.Equal(t, "ApplyPatch", choice.Function.Name)

	require.Equal(t, schemas.ChatToolTypeCustom, original.Params.Tools[0].Type)
	require.Equal(t, schemas.ChatToolChoiceTypeCustom, original.Params.ToolChoice.ChatToolChoiceStruct.Type)

	supported := customChatRequest(schemas.OpenAI, "gpt-5")
	require.Same(t, supported, RewriteChatCustomTools(nil, schemas.OpenAI, supported))
}

func TestRestoreResponsesCustomToolCalls(t *testing.T) {
	names := map[string]struct{}{"apply_patch": {}}
	resp := &schemas.BifrostResponsesResponse{Output: []schemas.ResponsesMessage{
		{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    schemas.Ptr("call_1"),
				Name:      schemas.Ptr("apply_patch"),
				Arguments: schemas.Ptr(`{"input":"line one\nline two"}`),
			},
		},
		{
			Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: schemas.Ptr("apply_patch"), Arguments: schemas.Ptr(`not json`)},
		},
		{
			Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: schemas.Ptr("search"), Arguments: schemas.Ptr(`{"q":"x"}`)},
		},
	}}
	restoreResponsesCustomToolCalls(names, resp)
	output := resp.Output

	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *output[0].Type)
	require.Nil(t, output[0].Arguments)
	require.Equal(t, "call_1", *output[0].CallID)
	require.Equal(t, "line one\nline two", output[0].ResponsesCustomToolCall.Input)

	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *output[1].Type)
	require.Equal(t, "not json", output[1].ResponsesCustomToolCall.Input)

	require.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *output[2].Type)
	require.Equal(t, `{"q":"x"}`, *output[2].Arguments)
}

// Compaction replays the conversation without tools: custom calls are rewritten in a
// copy, items without a type are skipped, and only the converted calls and their outputs
// are restored, by call_id, so a genuine function call with the same name stays as it is.
func TestRewriteCompactionCustomCalls(t *testing.T) {
	stubCustomToolsDatasheet(t)
	history := customToolsResponsesRequest(schemas.XAI, "grok-4").Input
	history[0].Namespace = schemas.Ptr("tools")
	untyped := schemas.ResponsesMessage{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")}}
	genuineCall := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: schemas.Ptr("call_2"), Namespace: schemas.Ptr("tools"), Name: schemas.Ptr("apply_patch"), Arguments: schemas.Ptr(`{"input":"keep"}`),
		},
	}
	genuineOutput := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: schemas.Ptr("call_2"), Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("ok")},
		},
	}
	req := &schemas.BifrostCompactionRequest{Provider: schemas.XAI, Model: "grok-4", Input: append([]schemas.ResponsesMessage{untyped}, append(history, genuineCall, genuineOutput)...)}

	got, restore := RewriteCompactionCustomCalls(nil, schemas.XAI, req)
	require.NotSame(t, req, got)
	require.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *got.Input[1].Type)
	require.Nil(t, got.Input[1].ID, "a ctc_ id is rejected on a function_call input")
	require.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *got.Input[2].Type)
	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *req.Input[1].Type, "the caller's request is untouched")

	resp := &schemas.BifrostCompactionResponse{Output: slices.Clone(got.Input[1:])}
	restore(resp)
	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *resp.Output[0].Type)
	require.Equal(t, `*** Begin Patch "x"`, resp.Output[0].ResponsesCustomToolCall.Input)
	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCallOutput, *resp.Output[1].Type, "the converted call's output is restored with it")
	require.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *resp.Output[2].Type, "a genuine function call with the same namespace and name stays a function call")
	require.Equal(t, `{"input":"keep"}`, *resp.Output[2].Arguments, "its arguments are not unwrapped")
	require.Nil(t, resp.Output[2].ResponsesCustomToolCall)
	require.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *resp.Output[3].Type)
}

// Nothing to rewrite, or a model with a true row, returns the request itself.
func TestRewriteCompactionCustomCalls_LeavesRequestAlone(t *testing.T) {
	stubCustomToolsDatasheet(t)
	plain := &schemas.BifrostCompactionRequest{Provider: schemas.XAI, Model: "grok-4", Input: []schemas.ResponsesMessage{{Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")}}}}
	noop := reflect.ValueOf(noCompactionRestore).Pointer()
	got, restore := RewriteCompactionCustomCalls(nil, schemas.XAI, plain)
	require.Same(t, plain, got)
	require.Equal(t, noop, reflect.ValueOf(restore).Pointer())

	supported := &schemas.BifrostCompactionRequest{Provider: schemas.OpenAI, Model: "gpt-5", Input: customToolsResponsesRequest(schemas.OpenAI, "gpt-5").Input}
	got, restore = RewriteCompactionCustomCalls(nil, schemas.OpenAI, supported)
	require.Same(t, supported, got)
	require.Equal(t, noop, reflect.ValueOf(restore).Pointer())

	got, restore = RewriteCompactionCustomCalls(nil, schemas.OpenAI, nil)
	require.Nil(t, got)
	restore(nil)
	restoreCompactionCustomToolCalls(map[string]struct{}{"x": {}}, nil)
}

// A custom tool and a function tool may share a name in different namespaces;
// only the call to the rewritten custom tool is restored, and it keeps its namespace.
func TestRestoreCustomToolCall_MatchesNamespace(t *testing.T) {
	names := map[string]struct{}{}
	responsesCustomToolsAsFunctions([]schemas.ResponsesTool{
		{Type: schemas.ResponsesToolTypeNamespace, Name: schemas.Ptr("shell"), ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeCustom, Name: schemas.Ptr("run")},
		}}},
		{Type: schemas.ResponsesToolTypeNamespace, Name: schemas.Ptr("jobs"), ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("run"), ResponsesToolFunction: &schemas.ResponsesToolFunction{}},
		}}},
		{Type: schemas.ResponsesToolTypeNamespace, Name: schemas.Ptr("functions"), ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeCustom, Name: schemas.Ptr("exec")},
		}}},
	}, "", names)

	call := func(name, namespace string) *schemas.ResponsesMessage {
		return &schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Name:      schemas.Ptr(name),
				Namespace: schemas.Ptr(namespace),
				Arguments: schemas.Ptr(`{"input":"ls"}`),
			},
		}
	}
	custom, function, defaultNamespace := call("run", "shell"), call("run", "jobs"), call("exec", "functions")
	require.True(t, restoreCustomToolCall(custom, names))
	require.Equal(t, "ls", custom.ResponsesCustomToolCall.Input)
	require.Equal(t, "shell", *custom.Namespace)
	require.False(t, restoreCustomToolCall(function, names))
	require.Equal(t, `{"input":"ls"}`, *function.Arguments)
	require.True(t, restoreCustomToolCall(defaultNamespace, names), "functions is the default namespace")
}

// runStream feeds events through the restore wrapper and returns what reaches the post hooks.
func runStream(t *testing.T, names map[string]struct{}, events []*schemas.BifrostResponsesStreamResponse) []*schemas.BifrostResponsesStreamResponse {
	t.Helper()
	var sent []*schemas.BifrostResponsesStreamResponse
	runner := wrapCustomToolRestorePostHookRunner(func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		sent = append(sent, result.ResponsesStreamResponse)
		return result, err
	}, names)
	for _, event := range events {
		_, err := runner(nil, &schemas.BifrostResponse{ResponsesStreamResponse: event}, nil)
		if err != nil {
			require.True(t, isStreamControlSkip(err), "only skip controls are expected")
		}
	}
	return sent
}

func TestWrapCustomToolRestorePostHookRunner(t *testing.T) {
	inner := func(_ *schemas.BifrostContext, r *schemas.BifrostResponse, e *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return r, e
	}
	require.NotNil(t, wrapCustomToolRestorePostHookRunner(inner, nil))

	itemID := schemas.Ptr("fc_1")
	call := func(eventType schemas.ResponsesStreamResponseType, args string) *schemas.BifrostResponsesStreamResponse {
		return &schemas.BifrostResponsesStreamResponse{Type: eventType, Item: &schemas.ResponsesMessage{
			ID:                   itemID,
			Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: schemas.Ptr("apply_patch"), Arguments: schemas.Ptr(args)},
		}}
	}
	delta := func(id *string, d string) *schemas.BifrostResponsesStreamResponse {
		return &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, ItemID: id, Delta: schemas.Ptr(d)}
	}
	events := []*schemas.BifrostResponsesStreamResponse{
		call(schemas.ResponsesStreamResponseTypeOutputItemAdded, ""),
		delta(itemID, `{"inp`),
		delta(itemID, `ut": "ab`),
		delta(schemas.Ptr("fc_other"), `{}`),
		delta(itemID, `c\n`),
		delta(itemID, `d"}`),
		{Type: schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone, ItemID: itemID, Arguments: schemas.Ptr(`{"input": "abc\nd"}`)},
		call(schemas.ResponsesStreamResponseTypeOutputItemDone, `{"input": "abc\nd"}`),
		{Type: schemas.ResponsesStreamResponseTypeCompleted, Response: &schemas.BifrostResponsesResponse{Output: []schemas.ResponsesMessage{*call("", `{"input": "abc\nd"}`).Item}}},
	}
	sent := runStream(t, map[string]struct{}{"apply_patch": {}}, events)

	var types []schemas.ResponsesStreamResponseType
	for _, event := range sent {
		types = append(types, event.Type)
	}
	require.Equal(t, []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeOutputItemAdded,
		schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, // an unrelated call passes through
		schemas.ResponsesStreamResponseTypeCustomToolCallInputDone,
		schemas.ResponsesStreamResponseTypeOutputItemDone,
		schemas.ResponsesStreamResponseTypeCompleted,
	}, types, "argument deltas of a restored call are dropped")
	require.Equal(t, "fc_other", *sent[1].ItemID)
	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *sent[0].Item.Type)
	require.Equal(t, "abc\nd", *sent[2].Input)
	require.Nil(t, sent[2].Arguments)
	require.Equal(t, "abc\nd", sent[3].Item.ResponsesCustomToolCall.Input)
	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *sent[4].Response.Output[0].Type)
}

// A stored response only carries the rewritten tools, so the names are rebuilt from the schema
// Bifrost generated, after a JSON round trip, and never from an ordinary function tool.
func TestCustomToolNamesFromTools(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "gpt-4.1", Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{
		{Type: schemas.ResponsesToolTypeCustom, Name: schemas.Ptr("apply_patch"), ResponsesToolCustom: &schemas.ResponsesToolCustom{Format: &schemas.ResponsesToolCustomFormat{
			Type: "grammar", Syntax: schemas.Ptr("lark"), Definition: schemas.Ptr("start: /.+/"),
		}}},
		{Type: schemas.ResponsesToolTypeCustom, Name: schemas.Ptr("freeform")},
		{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("lookalike"), ResponsesToolFunction: &schemas.ResponsesToolFunction{
			Parameters: &schemas.ToolFunctionParameters{Type: "object", Required: []string{"input"}, Properties: schemas.NewOrderedMapFromPairs(
				schemas.KV("input", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"), schemas.KV("description", "The query."))),
			)},
		}},
		{Type: schemas.ResponsesToolTypeWebSearch},
	}}}
	rewritten := RewriteResponsesCustomTools(nil, schemas.OpenAI, req)
	require.Equal(t, map[string]struct{}{"apply_patch": {}, "freeform": {}}, rewritten.CustomToolNames)
	require.Equal(t, rewritten.CustomToolNames, customToolNamesFromTools(rewritten.Params.Tools))

	encoded, err := MarshalSorted(rewritten.Params.Tools)
	require.NoError(t, err)
	var stored []schemas.ResponsesTool
	require.NoError(t, json.Unmarshal(encoded, &stored))
	require.Equal(t, rewritten.CustomToolNames, customToolNamesFromTools(stored))

	require.Nil(t, customToolNamesFromTools(req.Params.Tools[2:]), "an ordinary function tool is not a rewritten custom tool")
}

func TestWrapRetrievedCustomToolRestorePostHookRunner(t *testing.T) {
	var sent []*schemas.BifrostResponsesStreamResponse
	runner := WrapRetrievedCustomToolRestorePostHookRunner(func(_ *schemas.BifrostContext, r *schemas.BifrostResponse, e *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		sent = append(sent, r.ResponsesStreamResponse)
		return r, e
	})
	tools := []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("apply_patch"),
		ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: customToolParameters("lark", "start: /.+/")}}}
	itemID := schemas.Ptr("fc_1")
	item := func(args string) *schemas.ResponsesMessage {
		return &schemas.ResponsesMessage{ID: itemID, Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: schemas.Ptr("apply_patch"), Arguments: schemas.Ptr(args)}}
	}
	for _, event := range []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeCreated, Response: &schemas.BifrostResponsesResponse{Tools: tools}},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, Item: item("")},
		{Type: schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone, ItemID: itemID, Arguments: schemas.Ptr(`{"input":"abc"}`)},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, Item: item(`{"input":"abc"}`)},
	} {
		_, err := runner(nil, &schemas.BifrostResponse{ResponsesStreamResponse: event}, nil)
		require.Nil(t, err)
	}
	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *sent[1].Item.Type)
	require.Equal(t, schemas.ResponsesStreamResponseTypeCustomToolCallInputDone, sent[2].Type)
	require.Equal(t, "abc", *sent[2].Input)
	require.Equal(t, "abc", sent[3].Item.ResponsesCustomToolCall.Input)
}

// BenchmarkRewriteCustomTools_NoCustomTools measures the common case on both routes:
// requests that declare no custom tool must cost a scan and nothing else.
func BenchmarkRewriteCustomTools_NoCustomTools(b *testing.B) {
	responses := &schemas.BifrostResponsesRequest{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{
		{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("shell"), ResponsesToolFunction: &schemas.ResponsesToolFunction{}},
		{Type: schemas.ResponsesToolTypeWebSearch},
	}}}
	chat := &schemas.BifrostChatRequest{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{
		{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "shell"}},
	}}}
	b.ReportAllocs()
	for b.Loop() {
		_ = ResponsesAdditionalToolsNeedHoist(nil, schemas.Anthropic, responses)
		_ = RewriteResponsesCustomTools(nil, schemas.Anthropic, responses)
		_ = RewriteChatCustomTools(nil, schemas.Anthropic, chat)
	}
}

// BenchmarkRewriteResponsesCustomTools_Rewrite measures a request whose custom tool is rewritten.
func BenchmarkRewriteResponsesCustomTools_Rewrite(b *testing.B) {
	request := customToolsResponsesRequest(schemas.Anthropic, "claude-sonnet-4-5")
	b.ReportAllocs()
	for b.Loop() {
		_ = RewriteResponsesCustomTools(nil, schemas.Anthropic, request)
	}
}

// BenchmarkCustomToolStreamRestore measures dropping one argument delta of a restored custom call.
func BenchmarkCustomToolStreamRestore(b *testing.B) {
	names := map[string]struct{}{"apply_patch": {}}
	runner := wrapCustomToolRestorePostHookRunner(func(_ *schemas.BifrostContext, r *schemas.BifrostResponse, e *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return r, e
	}, names)
	itemID := schemas.Ptr("fc_1")
	added := &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, Item: &schemas.ResponsesMessage{
		ID: itemID, Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: schemas.Ptr("apply_patch")},
	}}
	_, _ = runner(nil, &schemas.BifrostResponse{ResponsesStreamResponse: added}, nil)
	b.ReportAllocs()
	for b.Loop() {
		event := &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, ItemID: itemID, Delta: schemas.Ptr("+ line of patch\\n")}
		_, _ = runner(nil, &schemas.BifrostResponse{ResponsesStreamResponse: event}, nil)
	}
}

func customChatToolCall(input string) schemas.ChatAssistantMessageToolCall {
	return schemas.ChatAssistantMessageToolCall{
		ID:     schemas.Ptr("call_1"),
		Type:   schemas.Ptr(string(schemas.ChatToolTypeCustom)),
		Custom: &schemas.ChatAssistantMessageToolCallCustom{Name: schemas.Ptr("ApplyPatch"), Input: input},
	}
}

// Replayed custom calls go to a model without custom tools as function calls, and the
// rewritten names are kept so the response can be restored.
func TestRewriteChatCustomTools_History(t *testing.T) {
	stubCustomToolsDatasheet(t)
	original := customChatRequest(schemas.Anthropic, "claude-sonnet-4-5")
	original.Input = []schemas.ChatMessage{
		{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{customChatToolCall("1\"2")}}},
		{Role: schemas.ChatMessageRoleTool, ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_1")}, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("ok")}},
	}
	got := RewriteChatCustomTools(nil, schemas.Anthropic, original)

	require.Equal(t, map[string]struct{}{"ApplyPatch": {}}, got.CustomToolNames)
	call := got.Input[0].ChatAssistantMessage.ToolCalls[0]
	require.Equal(t, "function", *call.Type)
	require.Nil(t, call.Custom)
	require.Equal(t, "ApplyPatch", *call.Function.Name)
	require.JSONEq(t, `{"input":"1\"2"}`, call.Function.Arguments)
	require.Equal(t, "call_1", *call.ID)
	require.NotNil(t, original.Input[0].ChatAssistantMessage.ToolCalls[0].Custom, "the caller's request is not mutated")

	historyOnly := &schemas.BifrostChatRequest{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Input: original.Input}
	require.Nil(t, RewriteChatCustomTools(nil, schemas.Anthropic, historyOnly).Input[0].ChatAssistantMessage.ToolCalls[0].Custom)

	supported := customChatRequest(schemas.OpenAI, "gpt-5")
	supported.Input = original.Input
	require.Same(t, supported, RewriteChatCustomTools(nil, schemas.OpenAI, supported))
}

func TestRestoreChatCustomToolCalls(t *testing.T) {
	resp := &schemas.BifrostChatResponse{Choices: []schemas.BifrostResponseChoice{{
		ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: &schemas.ChatMessage{
			Role: schemas.ChatMessageRoleAssistant,
			ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{
				{ID: schemas.Ptr("call_1"), Type: schemas.Ptr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("ApplyPatch"), Arguments: `{"input":"a\nb"}`}},
				{ID: schemas.Ptr("call_2"), Type: schemas.Ptr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("get_weather"), Arguments: `{"city":"x"}`}},
			}},
		}},
	}}}
	RestoreChatCustomToolCalls(&schemas.BifrostChatRequest{CustomToolNames: map[string]struct{}{"ApplyPatch": {}}}, resp)

	calls := resp.Choices[0].Message.ChatAssistantMessage.ToolCalls
	require.Equal(t, "custom", *calls[0].Type)
	require.Equal(t, "ApplyPatch", *calls[0].Custom.Name)
	require.Equal(t, "a\nb", calls[0].Custom.Input)
	require.Equal(t, schemas.ChatAssistantMessageToolCallFunction{}, calls[0].Function)
	require.Equal(t, "function", *calls[1].Type, "an ordinary function call is left alone")
	require.Nil(t, calls[1].Custom)
}

// Streamed function calls of a rewritten custom tool reach the caller as a custom call: the
// header becomes a custom delta, argument fragments are held back, and the unwrapped input
// is sent with the finish chunk.
func TestWrapChatCustomToolRestorePostHookRunner(t *testing.T) {
	inner := func(_ *schemas.BifrostContext, r *schemas.BifrostResponse, e *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return r, e
	}
	require.NotNil(t, WrapChatCustomToolRestorePostHookRunner(inner, nil))

	chunk := func(finish *string, calls ...schemas.ChatAssistantMessageToolCall) *schemas.BifrostChatResponse {
		return &schemas.BifrostChatResponse{Choices: []schemas.BifrostResponseChoice{{
			FinishReason:             finish,
			ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{ToolCalls: calls}},
		}}}
	}
	fn := func(index uint16, id, name *string, args string) schemas.ChatAssistantMessageToolCall {
		tc := schemas.ChatAssistantMessageToolCall{Index: index, ID: id, Function: schemas.ChatAssistantMessageToolCallFunction{Name: name, Arguments: args}}
		if name != nil {
			tc.Type = schemas.Ptr("function")
		}
		return tc
	}
	// run sends chunks through the wrapper and returns what reached the inner runner and how many were skipped.
	run := func(chunks ...*schemas.BifrostChatResponse) ([]*schemas.BifrostChatResponse, int) {
		var sent []*schemas.BifrostChatResponse
		runner := WrapChatCustomToolRestorePostHookRunner(func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
			sent = append(sent, result.ChatResponse)
			return result, err
		}, &schemas.BifrostChatRequest{CustomToolNames: map[string]struct{}{"ApplyPatch": {}}})
		skipped := 0
		for _, c := range chunks {
			_, err := runner(nil, &schemas.BifrostResponse{ChatResponse: c}, nil)
			if err != nil {
				require.NotNil(t, err.StreamControl)
				require.True(t, *err.StreamControl.SkipStream)
				skipped++
			}
		}
		return sent, skipped
	}

	t.Run("fragment chunks are skipped", func(t *testing.T) {
		sent, skipped := run(
			chunk(nil, fn(0, schemas.Ptr("call_1"), schemas.Ptr("ApplyPatch"), "")),
			chunk(nil, fn(0, nil, nil, `{"inp`)),
			chunk(nil, fn(0, nil, nil, `ut":"a\n`)),
			chunk(nil, fn(0, nil, nil, `b"}`)),
			chunk(schemas.Ptr("tool_calls")),
		)
		require.Equal(t, 3, skipped)
		require.Len(t, sent, 2)

		header := sent[0].Choices[0].Delta.ToolCalls
		require.Len(t, header, 1)
		require.Equal(t, "custom", *header[0].Type)
		require.Equal(t, "call_1", *header[0].ID)
		require.Equal(t, "ApplyPatch", *header[0].Custom.Name)
		require.Equal(t, "", header[0].Custom.Input)

		final := sent[1].Choices[0]
		require.Equal(t, "tool_calls", *final.FinishReason)
		require.Len(t, final.Delta.ToolCalls, 1)
		require.Equal(t, uint16(0), final.Delta.ToolCalls[0].Index)
		require.Nil(t, final.Delta.ToolCalls[0].Custom.Name)
		require.Equal(t, "a\nb", final.Delta.ToolCalls[0].Custom.Input)
	})

	t.Run("a fragment chunk with content is sent without tool calls", func(t *testing.T) {
		withContent := chunk(nil, fn(0, nil, nil, `{"input":"x"}`))
		withContent.Choices[0].Delta.Content = schemas.Ptr("thinking out loud")
		sent, skipped := run(
			chunk(nil, fn(0, schemas.Ptr("call_1"), schemas.Ptr("ApplyPatch"), "")),
			withContent,
			chunk(schemas.Ptr("tool_calls")),
		)
		require.Equal(t, 0, skipped)
		require.Len(t, sent, 3)
		require.Nil(t, sent[1].Choices[0].Delta.ToolCalls)
		require.Equal(t, "thinking out loud", *sent[1].Choices[0].Delta.Content)
		require.Equal(t, "x", sent[2].Choices[0].Delta.ToolCalls[0].Custom.Input)
	})

	t.Run("an ordinary call streams unchanged", func(t *testing.T) {
		sent, skipped := run(chunk(nil, fn(1, schemas.Ptr("call_2"), schemas.Ptr("get_weather"), `{"city":"x"}`)))
		require.Equal(t, 0, skipped)
		require.Equal(t, "get_weather", *sent[0].Choices[0].Delta.ToolCalls[0].Function.Name)
	})
}

func TestRewriteChatCustomTools_AllowedTools(t *testing.T) {
	stubCustomToolsDatasheet(t)
	original := customChatRequest(schemas.Anthropic, "claude-sonnet-4-5")
	original.Params.ToolChoice = &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
		Type: schemas.ChatToolChoiceTypeAllowedTools,
		AllowedTools: &schemas.ChatToolChoiceAllowedTools{Mode: "required", Tools: []schemas.ChatToolChoiceAllowedToolsTool{
			{Type: "custom", Custom: &schemas.ChatToolChoiceCustom{Name: "ApplyPatch"}},
			{Type: "function", Function: schemas.ChatToolChoiceFunction{Name: "get_weather"}},
		}},
	}}
	got := RewriteChatCustomTools(nil, schemas.Anthropic, original)

	entries := got.Params.ToolChoice.ChatToolChoiceStruct.AllowedTools.Tools
	require.Equal(t, "function", entries[0].Type)
	require.Equal(t, "ApplyPatch", entries[0].Function.Name)
	require.Nil(t, entries[0].Custom)
	require.Equal(t, "get_weather", entries[1].Function.Name)

	orig := original.Params.ToolChoice.ChatToolChoiceStruct.AllowedTools.Tools[0]
	require.Equal(t, "custom", orig.Type)
	require.Equal(t, "ApplyPatch", orig.Custom.Name)
	require.Equal(t, "", orig.Function.Name)
}

func TestRewriteChatCustomTools_NamelessTool(t *testing.T) {
	stubCustomToolsDatasheet(t)
	original := customChatRequest(schemas.Anthropic, "claude-sonnet-4-5")
	original.Params.ToolChoice = nil
	original.Params.Tools = append(original.Params.Tools, schemas.ChatTool{Type: schemas.ChatToolTypeCustom, Custom: &schemas.ChatToolCustom{}})
	got := RewriteChatCustomTools(nil, schemas.Anthropic, original)

	require.Equal(t, schemas.ChatToolTypeFunction, got.Params.Tools[0].Type)
	require.Equal(t, schemas.ChatToolTypeCustom, got.Params.Tools[1].Type, "a nameless custom tool is left for the upstream to reject")
	require.Equal(t, map[string]struct{}{"ApplyPatch": {}}, got.CustomToolNames)
}

// Namespace restore runs first, so the custom restore matches on the restored namespace and name.
func TestRestoreResponsesToolCalls(t *testing.T) {
	prepared := &schemas.BifrostResponsesRequest{
		NamespaceToolAliases: map[string]schemas.NamespaceToolAlias{"shell__run": {Namespace: "shell", Name: "run"}},
		CustomToolNames:      map[string]struct{}{customToolKey("shell", "run"): {}},
	}
	resp := &schemas.BifrostResponsesResponse{Output: []schemas.ResponsesMessage{{
		Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: schemas.Ptr("shell__run"), Arguments: schemas.Ptr(`{"input":"ls"}`)},
	}}}
	RestoreResponsesToolCalls(prepared, resp)
	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *resp.Output[0].Type)
	require.Equal(t, "run", *resp.Output[0].Name)
	require.Equal(t, "shell", *resp.Output[0].Namespace)
	require.Equal(t, "ls", resp.Output[0].ResponsesCustomToolCall.Input)

	RestoreResponsesToolCalls(nil, resp)
	RestoreResponsesToolCalls(prepared, nil)
	require.Zero(t, testing.AllocsPerRun(100, func() { RestoreResponsesToolCalls(&schemas.BifrostResponsesRequest{}, resp) }))

	inner := func(_ *schemas.BifrostContext, r *schemas.BifrostResponse, e *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return r, e
	}
	same := reflect.ValueOf(inner).Pointer()
	require.Equal(t, same, reflect.ValueOf(WrapResponsesToolRestorePostHookRunner(inner, nil)).Pointer())
	require.Equal(t, same, reflect.ValueOf(WrapResponsesToolRestorePostHookRunner(inner, &schemas.BifrostResponsesRequest{})).Pointer())
	require.NotEqual(t, same, reflect.ValueOf(WrapResponsesToolRestorePostHookRunner(inner, prepared)).Pointer())
}

func TestRestoreRetrievedCustomToolCalls(t *testing.T) {
	stubCustomToolsDatasheet(t)
	rewritten := RewriteResponsesCustomTools(nil, schemas.XAI, customToolsResponsesRequest(schemas.XAI, "grok-4"))
	resp := &schemas.BifrostResponsesResponse{Tools: rewritten.Params.Tools, Output: []schemas.ResponsesMessage{{
		Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: rewritten.Params.Tools[0].Name, Arguments: schemas.Ptr(`{"input":"x"}`)},
	}}}
	RestoreRetrievedCustomToolCalls(resp)
	require.Equal(t, schemas.ResponsesMessageTypeCustomToolCall, *resp.Output[0].Type)
	require.Equal(t, "x", resp.Output[0].ResponsesCustomToolCall.Input)
	RestoreRetrievedCustomToolCalls(nil)
}
