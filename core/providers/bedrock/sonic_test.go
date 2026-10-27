package bedrock

import (
	"strings"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/tidwall/gjson"
)

// sonicInputNames returns the Nova Sonic event name of each input event, e.g. "contentStart".
func sonicInputNames(t *testing.T, events [][]byte) []string {
	t.Helper()
	names := make([]string, 0, len(events))
	for _, event := range events {
		var name string
		gjson.GetBytes(event, "event").ForEach(func(key, _ gjson.Result) bool {
			name = key.String()
			return false
		})
		names = append(names, name)
	}
	return names
}

// clientTypes returns the type of each OpenAI Realtime event sent to the client.
func clientTypes(events [][]byte) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, gjson.GetBytes(event, "type").String())
	}
	return types
}

func feedModel(s *sonicSession, events ...string) [][]byte {
	var out [][]byte
	for _, event := range events {
		out = append(out, s.modelEvent([]byte(event))...)
	}
	return out
}

func findClientEvent(t *testing.T, events [][]byte, eventType string) gjson.Result {
	t.Helper()
	for _, event := range events {
		if gjson.GetBytes(event, "type").String() == eventType {
			return gjson.ParseBytes(event)
		}
	}
	t.Fatalf("no %s event in %v", eventType, clientTypes(events))
	return gjson.Result{}
}

func TestSonicSessionStartsLazilyWithConfiguredPrompt(t *testing.T) {
	s := newSonicSession("amazon.nova-2-sonic-v1:0")
	upstream, replies := s.clientEvent([]byte(`{"type":"session.update","session":{
		"type":"realtime","instructions":"Be brief.",
		"audio":{"input":{"format":{"type":"audio/pcm","rate":16000},"turn_detection":{"type":"semantic_vad","eagerness":"high"}},
		         "output":{"voice":"tiffany"}},
		"max_output_tokens":512,
		"tools":[{"type":"function","name":"get_weather","description":"Weather lookup","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}}`))
	if len(upstream) != 0 {
		t.Fatalf("session.update must not open the stream, got %v", sonicInputNames(t, upstream))
	}
	updated := gjson.ParseBytes(replies[0])
	if updated.Get("type").String() != "session.updated" || updated.Get("session.audio.output.voice").String() != "tiffany" || updated.Get("session.audio.input.format.rate").Int() != 16000 {
		t.Fatalf("unexpected session.updated: %s", replies[0])
	}

	upstream, replies = s.clientEvent([]byte(`{"type":"input_audio_buffer.append","audio":"AAAA"}`))
	if len(replies) != 0 {
		t.Fatalf("unexpected replies: %s", replies)
	}
	want := []string{"sessionStart", "promptStart", "contentStart", "textInput", "contentEnd", "contentStart", "audioInput"}
	if got := sonicInputNames(t, upstream); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("start sequence = %v, want %v", got, want)
	}
	start := gjson.ParseBytes(upstream[0]).Get("event.sessionStart")
	if start.Get("inferenceConfiguration.maxTokens").Int() != 512 || start.Get("turnDetectionConfiguration.endpointingSensitivity").String() != "HIGH" {
		t.Fatalf("unexpected sessionStart: %s", start.Raw)
	}
	prompt := gjson.ParseBytes(upstream[1]).Get("event.promptStart")
	if prompt.Get("audioOutputConfiguration.voiceId").String() != "tiffany" || prompt.Get("audioOutputConfiguration.sampleRateHertz").Int() != 24000 {
		t.Fatalf("unexpected promptStart audio config: %s", prompt.Raw)
	}
	tool := prompt.Get("toolConfiguration.tools.0.toolSpec")
	if tool.Get("name").String() != "get_weather" || tool.Get("inputSchema.json").String() != `{"properties":{"city":{"type":"string"}},"type":"object"}` {
		t.Fatalf("unexpected tool spec: %s", tool.Raw)
	}
	system := gjson.ParseBytes(upstream[2]).Get("event.contentStart")
	if system.Get("role").String() != "SYSTEM" || system.Get("interactive").Bool() || gjson.ParseBytes(upstream[3]).Get("event.textInput.content").String() != "Be brief." {
		t.Fatalf("unexpected system prompt block: %s %s", upstream[2], upstream[3])
	}
	audio := gjson.ParseBytes(upstream[5]).Get("event.contentStart")
	if audio.Get("type").String() != "AUDIO" || !audio.Get("interactive").Bool() || audio.Get("audioInputConfiguration.sampleRateHertz").Int() != 16000 {
		t.Fatalf("unexpected audio block: %s", audio.Raw)
	}
	if gjson.ParseBytes(upstream[6]).Get("event.audioInput.contentName").String() != audio.Get("contentName").String() {
		t.Fatal("audioInput must reuse the audio block's contentName")
	}
	for _, event := range upstream[1:] {
		if name := gjson.GetBytes(event, "event.*.promptName").String(); name != s.promptName {
			t.Fatalf("event %s uses promptName %q, want %q", event, name, s.promptName)
		}
	}

	upstream, _ = s.clientEvent([]byte(`{"type":"input_audio_buffer.append","audio":"BBBB"}`))
	if got := sonicInputNames(t, upstream); len(got) != 1 || got[0] != "audioInput" {
		t.Fatalf("later audio = %v, want only audioInput", got)
	}
}

func TestSonicSessionRejectsUnsupportedConfig(t *testing.T) {
	for _, tc := range []struct{ name, event string }{
		{"g711 GA format", `{"type":"session.update","session":{"audio":{"input":{"format":{"type":"audio/pcmu"}}}}}`},
		{"unsupported rate", `{"type":"session.update","session":{"audio":{"output":{"format":{"type":"audio/pcm","rate":44100}}}}}`},
		{"g711 beta format", `{"type":"session.update","session":{"input_audio_format":"g711_ulaw"}}`},
		{"non-function tool", `{"type":"session.update","session":{"tools":[{"type":"mcp","server_label":"x"}]}}`},
		// Nova Sonic's live limits; OpenAI allows temperatures up to 1.2.
		{"temperature above 1", `{"type":"session.update","session":{"temperature":1.1}}`},
		{"negative temperature", `{"type":"session.update","session":{"temperature":-0.1}}`},
		{"zero max tokens", `{"type":"session.update","session":{"max_output_tokens":0}}`},
		{"max tokens above 65536", `{"type":"session.update","session":{"max_output_tokens":100000}}`},
		{"named tool choice without the tool", `{"type":"session.update","session":{"tool_choice":{"type":"function","name":"get_weather"}}}`},
		{"mcp tool choice", `{"type":"session.update","session":{"tool_choice":{"type":"mcp","server_label":"x"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSonicSession("amazon.nova-2-sonic-v1:0")
			upstream, replies := s.clientEvent([]byte(tc.event))
			if len(upstream) != 0 || len(replies) != 1 || gjson.GetBytes(replies[0], "error.code").String() != "invalid_session_config" {
				t.Fatalf("got upstream=%d replies=%s", len(upstream), replies)
			}
			if s.config.inputRate != sonicDefaultSampleRate || s.config.outputRate != sonicDefaultSampleRate || s.config.tools != nil ||
				s.config.temperature != nil || s.config.maxTokens != sonicDefaultMaxTokens {
				t.Fatal("a rejected session.update must not change the configuration")
			}
		})
	}
}

// tool_choice "none" keeps the tools out of promptStart so Nova Sonic cannot call them.
func TestSonicSessionToolChoiceNoneWithholdsTools(t *testing.T) {
	s := newSonicSession("amazon.nova-2-sonic-v1:0")
	_, replies := s.clientEvent([]byte(`{"type":"session.update","session":{"tool_choice":"none",
		"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}]}}`))
	if len(replies) != 1 || gjson.GetBytes(replies[0], "session.tool_choice").String() != "none" {
		t.Fatalf("unexpected session.updated: %s", replies)
	}
	upstream, _ := s.clientEvent([]byte(`{"type":"input_audio_buffer.append","audio":"AAAA"}`))
	prompt := gjson.ParseBytes(upstream[1]).Get("event.promptStart")
	if prompt.Get("toolConfiguration").Exists() || prompt.Get("toolUseOutputConfiguration").Exists() {
		t.Fatalf("tools reached Nova Sonic despite tool_choice none: %s", prompt.Raw)
	}
}

// required and named choices map to Nova Sonic's undocumented toolConfiguration.toolChoice.
func TestSonicSessionToolChoiceForcesTools(t *testing.T) {
	for choice, want := range map[string]string{
		`"required"`: `{"any":{}}`,
		`{"type":"function","name":"get_weather"}`: `{"tool":{"name":"get_weather"}}`,
	} {
		s := newSonicSession("amazon.nova-2-sonic-v1:0")
		_, replies := s.clientEvent([]byte(`{"type":"session.update","session":{"tool_choice":` + choice + `,
			"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}]}}`))
		if len(replies) != 1 || gjson.GetBytes(replies[0], "type").String() != "session.updated" || gjson.GetBytes(replies[0], "session.tool_choice").Raw != choice {
			t.Fatalf("%s: unexpected reply: %s", choice, replies)
		}
		upstream, _ := s.clientEvent([]byte(`{"type":"input_audio_buffer.append","audio":"AAAA"}`))
		if got := gjson.GetBytes(upstream[1], "event.promptStart.toolConfiguration.toolChoice").Raw; got != want {
			t.Fatalf("%s: toolChoice = %s, want %s", choice, got, want)
		}
	}
}

// The boundaries live Nova Sonic accepts; "inf" keeps the default.
func TestSonicSessionAcceptsInferenceLimits(t *testing.T) {
	for _, tc := range []struct {
		event       string
		temperature float64
		maxTokens   int
	}{
		{`{"type":"session.update","session":{"temperature":0,"max_output_tokens":1}}`, 0, 1},
		{`{"type":"session.update","session":{"temperature":1,"max_output_tokens":65536}}`, 1, 65536},
		{`{"type":"session.update","session":{"temperature":0.8,"max_output_tokens":"inf"}}`, 0.8, sonicDefaultMaxTokens},
	} {
		s := newSonicSession("amazon.nova-2-sonic-v1:0")
		upstream, replies := s.clientEvent([]byte(tc.event))
		if len(upstream) != 0 || len(replies) != 1 || gjson.GetBytes(replies[0], "type").String() != "session.updated" {
			t.Fatalf("%s: got upstream=%d replies=%s", tc.event, len(upstream), replies)
		}
		if s.config.temperature == nil || *s.config.temperature != tc.temperature || s.config.maxTokens != tc.maxTokens {
			t.Fatalf("%s: temperature=%v maxTokens=%d", tc.event, s.config.temperature, s.config.maxTokens)
		}
	}
}

func TestSonicSessionUpdateAfterStartIsRejected(t *testing.T) {
	s := newSonicSession("amazon.nova-2-sonic-v1:0")
	s.clientEvent([]byte(`{"type":"input_audio_buffer.append","audio":"AAAA"}`))
	upstream, replies := s.clientEvent([]byte(`{"type":"session.update","session":{"instructions":"late"}}`))
	if len(upstream) != 0 || gjson.GetBytes(replies[0], "error.code").String() != "session_update_after_start" {
		t.Fatalf("got upstream=%d replies=%s", len(upstream), replies)
	}
}

func TestSonicHistoryThenResponseCreateAnswersTrailingUserText(t *testing.T) {
	s := newSonicSession("amazon.nova-2-sonic-v1:0")
	for _, event := range []string{
		`{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"My name is Ada."}]}}`,
		`{"type":"conversation.item.create","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Nice to meet you."}]}}`,
		`{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"What is my name?"}]}}`,
	} {
		if upstream, replies := s.clientEvent([]byte(event)); len(upstream) != 0 || len(replies) != 0 {
			t.Fatalf("history before start must be buffered, got upstream=%d replies=%s", len(upstream), replies)
		}
	}
	upstream, _ := s.clientEvent([]byte(`{"type":"response.create"}`))

	var blocks []string
	for _, event := range upstream {
		if start := gjson.GetBytes(event, "event.contentStart"); start.Exists() {
			blocks = append(blocks, start.Get("type").String()+"/"+start.Get("role").String()+"/"+start.Get("interactive").String())
		}
	}
	want := []string{"TEXT/USER/false", "TEXT/ASSISTANT/false", "AUDIO/USER/true", "TEXT/USER/true"}
	if strings.Join(blocks, ",") != strings.Join(want, ",") {
		t.Fatalf("content blocks = %v, want %v", blocks, want)
	}
	if last := gjson.ParseBytes(upstream[len(upstream)-2]).Get("event.textInput.content").String(); last != "What is my name?" {
		t.Fatalf("trailing user text = %q", last)
	}

	if more, _ := s.clientEvent([]byte(`{"type":"response.create"}`)); len(more) != 0 {
		t.Fatal("response.create after start must not send anything upstream")
	}
	_, replies := s.clientEvent([]byte(`{"type":"conversation.item.create","item":{"type":"message","role":"assistant","content":[{"type":"text","text":"late"}]}}`))
	if gjson.GetBytes(replies[0], "error.code").String() != "history_after_start" {
		t.Fatalf("assistant history after start must be rejected, got %s", replies)
	}
	upstream, _ = s.clientEvent([]byte(`{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"Hello again"}]}}`))
	if start := gjson.GetBytes(upstream[0], "event.contentStart"); !start.Get("interactive").Bool() || start.Get("role").String() != "USER" {
		t.Fatalf("user text after start must be cross-modal input, got %s", upstream[0])
	}
}

func TestSonicFunctionCallOutputBecomesToolResult(t *testing.T) {
	s := newSonicSession("amazon.nova-2-sonic-v1:0")
	output := `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"tool-1","output":"{\"temp\":21}"}}`
	if _, replies := s.clientEvent([]byte(output)); gjson.GetBytes(replies[0], "error.code").String() != "tool_output_before_start" {
		t.Fatalf("tool output before start must be rejected, got %s", replies)
	}
	s.clientEvent([]byte(`{"type":"input_audio_buffer.append","audio":"AAAA"}`))
	upstream, _ := s.clientEvent([]byte(output))
	if got := strings.Join(sonicInputNames(t, upstream), ","); got != "contentStart,toolResult,contentEnd" {
		t.Fatalf("tool result events = %s", got)
	}
	start := gjson.ParseBytes(upstream[0]).Get("event.contentStart")
	if start.Get("type").String() != "TOOL" || start.Get("toolResultInputConfiguration.toolUseId").String() != "tool-1" {
		t.Fatalf("unexpected tool contentStart: %s", start.Raw)
	}
	if gjson.ParseBytes(upstream[1]).Get("event.toolResult.content").String() != `{"temp":21}` {
		t.Fatalf("unexpected toolResult: %s", upstream[1])
	}
}

func TestSonicClientEventsWithoutUpstreamEquivalent(t *testing.T) {
	s := newSonicSession("amazon.nova-2-sonic-v1:0")
	for _, eventType := range []string{"input_audio_buffer.commit", "input_audio_buffer.clear", "response.cancel", "conversation.item.truncate"} {
		if upstream, replies := s.clientEvent([]byte(`{"type":"` + eventType + `"}`)); len(upstream) != 0 || len(replies) != 0 {
			t.Fatalf("%s must be a no-op, got upstream=%d replies=%s", eventType, len(upstream), replies)
		}
	}
	if _, replies := s.clientEvent([]byte(`{"type":"transcription_session.update"}`)); gjson.GetBytes(replies[0], "error.code").String() != "unsupported_event" {
		t.Fatalf("unknown events must be rejected, got %s", replies)
	}
}

// novaVoiceTurn is a Nova Sonic response to one spoken user turn, in wire order.
var novaVoiceTurn = []string{
	`{"event":{"usageEvent":{"details":{"total":{"input":{"speechTokens":40,"textTokens":10},"output":{"speechTokens":0,"textTokens":0}}}}}}`,
	`{"event":{"completionStart":{"sessionId":"s1","completionId":"c1"}}}`,
	`{"event":{"contentStart":{"contentId":"u1","type":"TEXT","role":"USER","additionalModelFields":"{\"generationStage\":\"FINAL\"}"}}}`,
	`{"event":{"textOutput":{"contentId":"u1","role":"USER","content":"what's the weather"}}}`,
	`{"event":{"contentEnd":{"contentId":"u1","type":"TEXT","stopReason":"END_TURN"}}}`,
	`{"event":{"contentStart":{"contentId":"a1","type":"TEXT","role":"ASSISTANT","additionalModelFields":"{\"generationStage\":\"SPECULATIVE\"}"}}}`,
	`{"event":{"textOutput":{"contentId":"a1","role":"ASSISTANT","content":"It is sunny"}}}`,
	`{"event":{"contentEnd":{"contentId":"a1","type":"TEXT","stopReason":"PARTIAL_TURN"}}}`,
	`{"event":{"contentStart":{"contentId":"a2","type":"AUDIO","role":"ASSISTANT"}}}`,
	`{"event":{"audioOutput":{"contentId":"a2","content":"UklGRg=="}}}`,
	`{"event":{"contentEnd":{"contentId":"a2","type":"AUDIO","stopReason":"PARTIAL_TURN"}}}`,
	`{"event":{"usageEvent":{"details":{"total":{"input":{"speechTokens":50,"textTokens":12},"output":{"speechTokens":30,"textTokens":5}}}}}}`,
	`{"event":{"contentStart":{"contentId":"a3","type":"TEXT","role":"ASSISTANT","additionalModelFields":"{\"generationStage\":\"FINAL\"}"}}}`,
	`{"event":{"textOutput":{"contentId":"a3","role":"ASSISTANT","content":"It is sunny."}}}`,
	`{"event":{"contentEnd":{"contentId":"a3","type":"TEXT","stopReason":"END_TURN"}}}`,
}

func TestSonicVoiceTurnTranslatesToRealtimeEvents(t *testing.T) {
	s := newSonicSession("amazon.nova-2-sonic-v1:0")
	out := feedModel(s, novaVoiceTurn...)
	want := []string{
		"input_audio_buffer.committed",
		"conversation.item.input_audio_transcription.delta",
		"conversation.item.input_audio_transcription.completed",
		"response.created",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_audio_transcript.delta",
		"response.output_audio.delta",
		"response.output_audio.done",
		"response.output_audio_transcript.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.done",
	}
	if got := clientTypes(out); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("client events =\n%v\nwant\n%v", got, want)
	}
	committed := findClientEvent(t, out, "input_audio_buffer.committed")
	completed := findClientEvent(t, out, "conversation.item.input_audio_transcription.completed")
	if completed.Get("transcript").String() != "what's the weather" || completed.Get("item_id").String() != committed.Get("item_id").String() {
		t.Fatalf("unexpected transcription completion: %s", completed.Raw)
	}
	if part := findClientEvent(t, out, "response.content_part.done"); part.Get("content_index").Int() != 0 || part.Get("part.transcript").String() != "It is sunny." {
		t.Fatalf("the audio part must close with its transcript: %s", part.Raw)
	}
	if findClientEvent(t, out, "response.output_audio.delta").Get("delta").String() != "UklGRg==" {
		t.Fatal("audio delta must carry Nova's base64 audio unchanged")
	}

	done := []byte(findClientEvent(t, out, "response.done").Raw)
	if status := gjson.GetBytes(done, "response.status").String(); status != "completed" {
		t.Fatalf("response.status = %q", status)
	}
	codec := realtimeCodec
	usage := codec.ExtractRealtimeTurnUsage(done)
	if usage == nil || usage.PromptTokens != 62 || usage.CompletionTokens != 35 || usage.TotalTokens != 97 ||
		usage.PromptTokensDetails.AudioTokens != 50 || usage.CompletionTokensDetails.AudioTokens != 30 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	message := codec.ExtractRealtimeTurnOutput(done)
	if message == nil || message.Content == nil || *message.Content.ContentStr != "It is sunny." {
		t.Fatalf("turn output must be the FINAL transcript, got %+v", message)
	}

	// The next response bills only the growth in Nova's cumulative totals.
	out = feedModel(s,
		`{"event":{"contentStart":{"contentId":"b1","type":"AUDIO","role":"ASSISTANT"}}}`,
		`{"event":{"usageEvent":{"details":{"total":{"input":{"speechTokens":60,"textTokens":12},"output":{"speechTokens":40,"textTokens":5}}}}}}`,
		`{"event":{"completionEnd":{"completionId":"c1","stopReason":"END_TURN"}}}`,
	)
	usage = codec.ExtractRealtimeTurnUsage([]byte(findClientEvent(t, out, "response.done").Raw))
	if usage == nil || usage.PromptTokens != 10 || usage.CompletionTokens != 10 {
		t.Fatalf("second response usage = %+v, want 10 in / 10 out", usage)
	}
}

func TestSonicBargeInCancelsResponse(t *testing.T) {
	s := newSonicSession("amazon.nova-2-sonic-v1:0")
	out := feedModel(s,
		`{"event":{"contentStart":{"contentId":"a1","type":"AUDIO","role":"ASSISTANT"}}}`,
		`{"event":{"audioOutput":{"contentId":"a1","content":"AAAA"}}}`,
		`{"event":{"contentEnd":{"contentId":"a1","type":"AUDIO","stopReason":"INTERRUPTED"}}}`,
		`{"event":{"contentStart":{"contentId":"a2","type":"TEXT","role":"ASSISTANT","additionalModelFields":"{\"generationStage\":\"FINAL\"}"}}}`,
		`{"event":{"textOutput":{"contentId":"a2","role":"ASSISTANT","content":"{ \"interrupted\" : true }"}}}`,
		`{"event":{"contentEnd":{"contentId":"a2","type":"TEXT","stopReason":"INTERRUPTED"}}}`,
	)
	findClientEvent(t, out, "input_audio_buffer.speech_started")
	done := findClientEvent(t, out, "response.done")
	if done.Get("response.status").String() != "cancelled" || done.Get("response.status_details.reason").String() != "turn_detected" {
		t.Fatalf("barge-in response.done = %s", done.Raw)
	}
	if strings.Contains(findClientEvent(t, out, "response.output_audio_transcript.done").Get("transcript").String(), "interrupted") {
		t.Fatal("the interruption marker must not leak into the transcript")
	}
}

func TestSonicToolUseEndsResponseWithFunctionCall(t *testing.T) {
	s := newSonicSession("amazon.nova-2-sonic-v1:0")
	out := feedModel(s,
		`{"event":{"contentStart":{"contentId":"t1","type":"TOOL","role":"TOOL"}}}`,
		`{"event":{"toolUse":{"contentId":"t1","toolName":"get_weather","toolUseId":"tool-1","content":"{\"city\":\"Paris\"}"}}}`,
		`{"event":{"contentEnd":{"contentId":"t1","type":"TOOL","stopReason":"TOOL_USE"}}}`,
	)
	want := "response.created,response.output_item.added,response.function_call_arguments.done,response.output_item.done,response.done"
	if got := strings.Join(clientTypes(out), ","); got != want {
		t.Fatalf("client events = %s", got)
	}
	message := realtimeCodec.ExtractRealtimeTurnOutput([]byte(findClientEvent(t, out, "response.done").Raw))
	if message == nil || message.ChatAssistantMessage == nil || len(message.ChatAssistantMessage.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %+v", message)
	}
	call := message.ChatAssistantMessage.ToolCalls[0]
	if *call.ID != "tool-1" || *call.Function.Name != "get_weather" || call.Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("unexpected tool call: %+v", call)
	}
}

func TestSonicNewUserTurnClosesOpenResponse(t *testing.T) {
	s := newSonicSession("amazon.nova-2-sonic-v1:0")
	out := feedModel(s,
		`{"event":{"contentStart":{"contentId":"a1","type":"AUDIO","role":"ASSISTANT"}}}`,
		`{"event":{"contentStart":{"contentId":"u2","type":"TEXT","role":"USER","additionalModelFields":"{\"generationStage\":\"FINAL\"}"}}}`,
	)
	if got := clientTypes(out); got[len(got)-2] != "response.done" || got[len(got)-1] != "input_audio_buffer.committed" {
		t.Fatalf("a new user turn must close the open response first, got %v", got)
	}
}

// The typed audio delta must keep the bytes the map-based client events produce.
func TestSonicAudioDeltaMatchesSortedMap(t *testing.T) {
	typed, err := providerUtils.MarshalSorted(BedrockSonicAudioDelta{
		Type: "response.output_audio.delta", EventID: "event_1", ResponseID: "resp_1", ItemID: "item_1", OutputIndex: 2, Delta: "AAAA",
	})
	if err != nil {
		t.Fatal(err)
	}
	fromMap, err := providerUtils.MarshalSorted(map[string]any{
		"type": "response.output_audio.delta", "event_id": "event_1", "response_id": "resp_1", "item_id": "item_1",
		"output_index": 2, "content_index": 0, "delta": "AAAA",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(typed) != string(fromMap) {
		t.Fatalf("typed delta %s, want %s", typed, fromMap)
	}
}

// BenchmarkSonicAudioFrame states the per-frame translation cost on both directions of a live
// session, at the frame sizes seen live: 32 ms of client audio and Nova's 80 ms output chunks.
func BenchmarkSonicAudioFrame(b *testing.B) {
	b.Run("client audio upstream", func(b *testing.B) {
		s := newSonicSession("amazon.nova-2-sonic-v1:0")
		frame := []byte(`{"type":"input_audio_buffer.append","audio":"` + strings.Repeat("A", 2048) + `"}`)
		s.clientEvent(frame) // opens the session
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			s.clientEvent(frame)
		}
	})
	b.Run("model audio to client", func(b *testing.B) {
		s := newSonicSession("amazon.nova-2-sonic-v1:0")
		frame := []byte(`{"event":{"audioOutput":{"promptName":"p","contentId":"c","content":"` + strings.Repeat("A", 5120) + `"}}}`)
		s.modelEvent(frame) // opens the response and its message item
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			s.modelEvent(frame)
		}
	})
}
