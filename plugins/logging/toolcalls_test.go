package logging

import (
	"reflect"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/streaming"
)

func chatToolCall(name, args string) schemas.ChatAssistantMessageToolCall {
	return schemas.ChatAssistantMessageToolCall{
		Type:     schemas.Ptr("function"),
		ID:       schemas.Ptr("call_" + name),
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr(name), Arguments: args},
	}
}

func assistantWithToolCalls(calls ...schemas.ChatAssistantMessageToolCall) *schemas.ChatMessage {
	return &schemas.ChatMessage{
		Role:                 schemas.ChatMessageRoleAssistant,
		ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: calls},
	}
}

func responsesFunctionCall(name, args string, callID, id *string) schemas.ResponsesMessage {
	fnType := schemas.ResponsesMessageTypeFunctionCall
	return schemas.ResponsesMessage{
		Type: &fnType,
		ID:   id,
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:    callID,
			Name:      schemas.Ptr(name),
			Arguments: schemas.Ptr(args),
		},
	}
}

func TestChatToolCallsFromResponsesOutput(t *testing.T) {
	msgType := schemas.ResponsesMessageTypeMessage
	output := []schemas.ResponsesMessage{
		{Type: &msgType},
		responsesFunctionCall("get_weather", `{"city":"Paris"}`, schemas.Ptr(" call_1 "), schemas.Ptr("fc_1")),
		responsesFunctionCall("search", `{}`, nil, schemas.Ptr("fc_2")),
		{Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall), ResponsesToolMessage: &schemas.ResponsesToolMessage{}}, // no name: skipped
	}

	got := chatToolCallsFromResponsesOutput(output)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	if *got[0].Function.Name != "get_weather" || got[0].Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("got[0] = %+v", got[0])
	}
	if got[0].ID == nil || *got[0].ID != "call_1" {
		t.Fatalf("got[0].ID = %v, want call_id to win over id", got[0].ID)
	}
	if got[1].ID == nil || *got[1].ID != "fc_2" {
		t.Fatalf("got[1].ID = %v, want fallback to item id", got[1].ID)
	}
	if got[0].Index != 0 || got[1].Index != 1 {
		t.Fatalf("indices = %d,%d", got[0].Index, got[1].Index)
	}
}

func TestCollectToolCallsPrefersChatMessage(t *testing.T) {
	msg := assistantWithToolCalls(chatToolCall("a", "{}"))
	out := []schemas.ResponsesMessage{responsesFunctionCall("b", "{}", nil, nil)}
	got := collectToolCalls(msg, out)
	if len(got) != 1 || *got[0].Function.Name != "a" {
		t.Fatalf("got = %+v, want chat message tool calls", got)
	}
	got = collectToolCalls(&schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant}, out)
	if len(got) != 1 || *got[0].Function.Name != "b" {
		t.Fatalf("got = %+v, want responses projection when chat message has none", got)
	}
	if got := collectToolCalls(nil, nil); got != nil {
		t.Fatalf("got = %+v, want nil", got)
	}
}

func TestToolCallNames(t *testing.T) {
	calls := []schemas.ChatAssistantMessageToolCall{
		chatToolCall(" get_weather ", "{}"),
		chatToolCall("search", "{}"),
		chatToolCall("get_weather", "{}"),
		chatToolCall("", "{}"),
		chatToolCall("bad,name", "{}"),
		{Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: "{}"}},
	}
	if got, want := toolCallNames(calls), []string{"get_weather", "search"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("toolCallNames = %#v, want %#v", got, want)
	}
	if got := toolCallNames(nil); got != nil {
		t.Fatalf("toolCallNames(nil) = %#v, want nil", got)
	}
}

func TestApplyToolCallsToEntryGating(t *testing.T) {
	calls := []schemas.ChatAssistantMessageToolCall{chatToolCall("get_weather", `{"city":"Paris"}`)}

	off := &logstore.Log{}
	applyToolCallsToEntry(off, calls, false)
	if got, want := off.ToolCallNames, []string{"get_weather"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names with content logging off = %#v, want %#v", got, want)
	}
	if off.ToolCallsParsed != nil {
		t.Fatalf("ToolCallsParsed = %+v, want nil when content logging is off", off.ToolCallsParsed)
	}

	on := &logstore.Log{}
	applyToolCallsToEntry(on, calls, true)
	if !reflect.DeepEqual(on.ToolCallNames, []string{"get_weather"}) {
		t.Fatalf("names with content logging on = %#v", on.ToolCallNames)
	}
	if len(on.ToolCallsParsed) != 1 || on.ToolCallsParsed[0].Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("ToolCallsParsed = %+v, want the full tool call", on.ToolCallsParsed)
	}
	if err := on.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields: %v", err)
	}
	if on.ToolCalls == "" {
		t.Fatal("tool_calls column empty after SerializeFields")
	}
	if on.ToolCallNamesStr == nil || *on.ToolCallNamesStr != "get_weather" {
		t.Fatalf("tool_call_names column = %v, want get_weather", on.ToolCallNamesStr)
	}

	untouched := &logstore.Log{}
	applyToolCallsToEntry(untouched, nil, true)
	if untouched.ToolCallNames != nil || untouched.ToolCallsParsed != nil {
		t.Fatalf("entry modified with no tool calls: %+v", untouched)
	}
}

func TestApplyNonStreamingOutputToEntryRecordsChatToolCalls(t *testing.T) {
	plugin := &LoggerPlugin{}
	result := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				FinishReason: schemas.Ptr("tool_calls"),
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: assistantWithToolCalls(chatToolCall("get_weather", `{"city":"Paris"}`), chatToolCall("search", `{}`)),
				},
			}},
		},
	}

	for _, contentLogging := range []bool{true, false} {
		entry := &logstore.Log{}
		plugin.applyNonStreamingOutputToEntry(entry, result, false, contentLogging)
		if got, want := entry.ToolCallNames, []string{"get_weather", "search"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("contentLogging=%v: ToolCallNames = %#v, want %#v", contentLogging, got, want)
		}
		if contentLogging && len(entry.ToolCallsParsed) != 2 {
			t.Fatalf("ToolCallsParsed = %+v, want 2 calls", entry.ToolCallsParsed)
		}
		if !contentLogging && entry.ToolCallsParsed != nil {
			t.Fatalf("ToolCallsParsed = %+v, want nil without content logging", entry.ToolCallsParsed)
		}
	}
}

func TestApplyNonStreamingOutputToEntryRecordsResponsesFunctionCalls(t *testing.T) {
	plugin := &LoggerPlugin{}
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{responsesFunctionCall("lookup_order", `{"id":1}`, schemas.Ptr("call_9"), nil)},
		},
	}
	entry := &logstore.Log{}
	plugin.applyNonStreamingOutputToEntry(entry, result, false, true)
	if got, want := entry.ToolCallNames, []string{"lookup_order"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolCallNames = %#v, want %#v", got, want)
	}
	if len(entry.ToolCallsParsed) != 1 || entry.ToolCallsParsed[0].ID == nil || *entry.ToolCallsParsed[0].ID != "call_9" {
		t.Fatalf("ToolCallsParsed = %+v, want projected function_call", entry.ToolCallsParsed)
	}
}

func TestApplyStreamingOutputToEntryRecordsToolCalls(t *testing.T) {
	plugin := &LoggerPlugin{}

	t.Run("chat", func(t *testing.T) {
		streamResponse := &streaming.ProcessedStreamResponse{
			Data: &streaming.AccumulatedData{
				OutputMessage: assistantWithToolCalls(chatToolCall("get_weather", `{"city":"Paris"}`)),
			},
		}
		entry := &logstore.Log{}
		plugin.applyStreamingOutputToEntry(entry, streamResponse, false, false)
		if got, want := entry.ToolCallNames, []string{"get_weather"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("ToolCallNames = %#v, want %#v", got, want)
		}
		if entry.ToolCallsParsed != nil {
			t.Fatalf("ToolCallsParsed = %+v, want nil without content logging", entry.ToolCallsParsed)
		}
		entry = &logstore.Log{}
		plugin.applyStreamingOutputToEntry(entry, streamResponse, false, true)
		if len(entry.ToolCallsParsed) != 1 {
			t.Fatalf("ToolCallsParsed = %+v, want 1 call", entry.ToolCallsParsed)
		}
	})

	t.Run("responses", func(t *testing.T) {
		streamResponse := &streaming.ProcessedStreamResponse{
			Data: &streaming.AccumulatedData{
				OutputMessages: []schemas.ResponsesMessage{responsesFunctionCall("search", `{}`, schemas.Ptr("call_2"), nil)},
			},
		}
		entry := &logstore.Log{}
		plugin.applyStreamingOutputToEntry(entry, streamResponse, false, true)
		if got, want := entry.ToolCallNames, []string{"search"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("ToolCallNames = %#v, want %#v", got, want)
		}
		if len(entry.ToolCallsParsed) != 1 || *entry.ToolCallsParsed[0].Function.Name != "search" {
			t.Fatalf("ToolCallsParsed = %+v", entry.ToolCallsParsed)
		}
	})
}

func TestApplyRealtimeOutputToEntryRecordsToolCalls(t *testing.T) {
	plugin := &LoggerPlugin{}
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{responsesFunctionCall("set_alarm", `{"at":"7am"}`, schemas.Ptr("call_rt"), nil)},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RealtimeRequest,
			},
		},
	}

	entry := &logstore.Log{}
	plugin.applyRealtimeOutputToEntry(entry, result, false, true)
	if got, want := entry.ToolCallNames, []string{"set_alarm"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolCallNames = %#v, want %#v", got, want)
	}
	if len(entry.ToolCallsParsed) != 1 {
		t.Fatalf("ToolCallsParsed = %+v, want 1 call", entry.ToolCallsParsed)
	}
	if entry.OutputMessageParsed == nil || entry.OutputMessageParsed.ChatAssistantMessage == nil || len(entry.OutputMessageParsed.ChatAssistantMessage.ToolCalls) != 1 {
		t.Fatalf("OutputMessageParsed = %+v, want tool call preserved on the output message", entry.OutputMessageParsed)
	}

	entry = &logstore.Log{}
	plugin.applyRealtimeOutputToEntry(entry, result, false, false)
	if !reflect.DeepEqual(entry.ToolCallNames, []string{"set_alarm"}) || entry.ToolCallsParsed != nil {
		t.Fatalf("without content logging: names=%#v calls=%+v", entry.ToolCallNames, entry.ToolCallsParsed)
	}
}
