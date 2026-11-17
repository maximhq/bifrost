package bedrock

import (
	"fmt"
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
	for _, tc := range []struct{ name, event, code string }{
		{"g711 GA format", `{"type":"session.update","session":{"audio":{"input":{"format":{"type":"audio/pcmu"}}}}}`, "invalid_session_config"},
		{"unsupported rate", `{"type":"session.update","session":{"audio":{"output":{"format":{"type":"audio/pcm","rate":44100}}}}}`, "invalid_session_config"},
		{"g711 beta format", `{"type":"session.update","session":{"input_audio_format":"g711_ulaw"}}`, "invalid_session_config"},
		{"non-function tool", `{"type":"session.update","session":{"tools":[{"type":"mcp","server_label":"x"}]}}`, "invalid_session_config"},
		// Nova Sonic's live limits; OpenAI allows temperatures up to 1.2.
		{"temperature above 1", `{"type":"session.update","session":{"temperature":1.1}}`, "invalid_session_config"},
		{"negative temperature", `{"type":"session.update","session":{"temperature":-0.1}}`, "invalid_session_config"},
		{"zero max tokens", `{"type":"session.update","session":{"max_output_tokens":0}}`, "invalid_session_config"},
		{"max tokens above 65535", `{"type":"session.update","session":{"max_output_tokens":65536}}`, "invalid_session_config"},
		// Forcing a tool on an unrelated question leaves live Nova Sonic silent, so only auto and none are served.
		{"required tool choice", `{"type":"session.update","session":{"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}],"tool_choice":"required"}}`, "invalid_session_config"},
		{"named tool choice", `{"type":"session.update","session":{"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"get_weather"}}}`, "invalid_session_config"},
		{"mcp tool choice", `{"type":"session.update","session":{"tool_choice":{"type":"mcp","server_label":"x"}}}`, "invalid_session_config"},
		// Nova Sonic refuses a prompt without audio output, so text-only replies cannot be served.
		{"text output", `{"type":"session.update","session":{"output_modalities":["text"]}}`, "unsupported_modalities"},
		{"beta text modality", `{"type":"session.update","session":{"modalities":["text"]}}`, "unsupported_modalities"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSonicSession("amazon.nova-2-sonic-v1:0")
			upstream, replies := s.clientEvent([]byte(tc.event))
			if len(upstream) != 0 || len(replies) != 1 || gjson.GetBytes(replies[0], "error.code").String() != tc.code {
				t.Fatalf("got upstream=%d replies=%s", len(upstream), replies)
			}
			if s.config.inputRate != sonicDefaultSampleRate || s.config.outputRate != sonicDefaultSampleRate || s.config.tools != nil ||
				s.config.temperature != nil || s.config.maxTokens != sonicDefaultMaxTokens {
				t.Fatal("a rejected session.update must not change the configuration")
			}
		})
	}
}

// Nova Sonic refuses a conversation whose first content is not SYSTEM, so a session without instructions gets a default.
func TestSonicSessionWithoutInstructionsStartsWithSystemPrompt(t *testing.T) {
	for _, event := range []string{"", `{"type":"session.update","session":{"instructions":""}}`} {
		s := newSonicSession("amazon.nova-2-sonic-v1:0")
		if event != "" {
			s.clientEvent([]byte(event))
		}
		upstream, _ := s.clientEvent([]byte(`{"type":"input_audio_buffer.append","audio":"AAAA"}`))
		system := gjson.ParseBytes(upstream[2]).Get("event.contentStart")
		if system.Get("role").String() != "SYSTEM" || gjson.ParseBytes(upstream[3]).Get("event.textInput.content").String() == "" {
			t.Fatalf("%q: first content after promptStart = %s %s", event, upstream[2], upstream[3])
		}
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

// The boundaries live Nova Sonic accepts; "inf" keeps the default.
func TestSonicSessionAcceptsInferenceLimits(t *testing.T) {
	for _, tc := range []struct {
		event       string
		temperature float64
		maxTokens   int
	}{
		{`{"type":"session.update","session":{"temperature":0,"max_output_tokens":1}}`, 0, 1},
		{`{"type":"session.update","session":{"temperature":1,"max_output_tokens":65535}}`, 1, 65535},
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
	want := []string{"TEXT/SYSTEM/false", "TEXT/USER/false", "TEXT/ASSISTANT/false", "AUDIO/USER/true", "TEXT/USER/true"}
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

func TestSonicResponseCreateOutputModalities(t *testing.T) {
	s := newSonicSession("amazon.nova-2-sonic-v1:0")
	upstream, replies := s.clientEvent([]byte(`{"type":"response.create","response":{"output_modalities":["text"]}}`))
	if len(upstream) != 0 || len(replies) != 1 || gjson.GetBytes(replies[0], "error.code").String() != "unsupported_modalities" || s.started {
		t.Fatalf("a text-only response must be refused before the stream opens, got upstream=%d replies=%s", len(upstream), replies)
	}
	for _, event := range []string{
		`{"type":"session.update","session":{"output_modalities":["audio"]}}`,
		`{"type":"session.update","session":{"modalities":["text","audio"]}}`,
	} {
		if _, replies := s.clientEvent([]byte(event)); gjson.GetBytes(replies[0], "type").String() != "session.updated" {
			t.Fatalf("%s must be accepted, got %s", event, replies)
		}
	}
	if upstream, replies := s.clientEvent([]byte(`{"type":"response.create","response":{"output_modalities":["audio"]}}`)); len(upstream) == 0 || len(replies) != 0 {
		t.Fatalf("an audio response must start the stream, got upstream=%d replies=%s", len(upstream), replies)
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
		"conversation.item.added",
		"conversation.item.done",
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
	// The user's audio item is announced like OpenAI does, so the transcript has an item to land on.
	for _, eventType := range []string{"conversation.item.added", "conversation.item.done"} {
		item := findClientEvent(t, out, eventType).Get("item")
		if item.Get("id").String() != committed.Get("item_id").String() || item.Get("role").String() != "user" ||
			item.Get("content.0.type").String() != "input_audio" {
			t.Fatalf("unexpected %s item: %s", eventType, item.Raw)
		}
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

// A multi-sentence reply in live wire order: Nova marks only the first FINAL segment END_TURN, after the audio ended.
func TestSonicMultiSegmentReplyIsOneResponse(t *testing.T) {
	s := newSonicSession("amazon.nova-2-sonic-v1:0")
	var events []string
	for i, text := range []string{"One.", " Two.", " Three."} {
		stop := "PARTIAL_TURN"
		if i == 2 {
			stop = "END_TURN"
		}
		events = append(events,
			`{"event":{"contentStart":{"contentId":"s`+fmt.Sprint(i)+`","type":"TEXT","role":"ASSISTANT","additionalModelFields":"{\"generationStage\":\"SPECULATIVE\"}"}}}`,
			`{"event":{"textOutput":{"contentId":"s`+fmt.Sprint(i)+`","role":"ASSISTANT","content":"`+text+`"}}}`,
			`{"event":{"contentEnd":{"contentId":"s`+fmt.Sprint(i)+`","type":"TEXT","stopReason":"PARTIAL_TURN"}}}`,
			`{"event":{"contentStart":{"contentId":"a`+fmt.Sprint(i)+`","type":"AUDIO","role":"ASSISTANT"}}}`,
			`{"event":{"audioOutput":{"contentId":"a`+fmt.Sprint(i)+`","content":"AAAA"}}}`,
			`{"event":{"contentEnd":{"contentId":"a`+fmt.Sprint(i)+`","type":"AUDIO","stopReason":"`+stop+`"}}}`,
		)
	}
	for i, text := range []string{"One.", " Two.", " Three."} {
		stop := "PARTIAL_TURN"
		if i == 0 {
			stop = "END_TURN"
		}
		events = append(events,
			`{"event":{"contentStart":{"contentId":"f`+fmt.Sprint(i)+`","type":"TEXT","role":"ASSISTANT","additionalModelFields":"{\"generationStage\":\"FINAL\"}"}}}`,
			`{"event":{"textOutput":{"contentId":"f`+fmt.Sprint(i)+`","role":"ASSISTANT","content":"`+text+`"}}}`,
			`{"event":{"contentEnd":{"contentId":"f`+fmt.Sprint(i)+`","type":"TEXT","stopReason":"`+stop+`"}}}`,
		)
	}
	out := feedModel(s, events...)
	types := clientTypes(out)
	if n := strings.Count(strings.Join(types, ","), "response.created"); n != 1 {
		t.Fatalf("one reply must be one response, got %d: %v", n, types)
	}
	if types[len(types)-1] != "response.done" {
		t.Fatalf("late FINAL text must not open another response: %v", types)
	}
	if got := findClientEvent(t, out, "response.done").Get("response.output.0.content.0.transcript").String(); got != "One. Two. Three." {
		t.Fatalf("response.done transcript = %q, want the whole reply", got)
	}
}

// After barge-in, the interrupted reply's late FINAL text belongs to the cancelled response, not a new one.
func TestSonicBargeInLateFinalTextOpensNoResponse(t *testing.T) {
	s := newSonicSession("amazon.nova-2-sonic-v1:0")
	out := feedModel(s,
		`{"event":{"contentStart":{"contentId":"s1","type":"TEXT","role":"ASSISTANT","additionalModelFields":"{\"generationStage\":\"SPECULATIVE\"}"}}}`,
		`{"event":{"textOutput":{"contentId":"s1","role":"ASSISTANT","content":"Once upon a time"}}}`,
		`{"event":{"contentEnd":{"contentId":"s1","type":"TEXT","stopReason":"PARTIAL_TURN"}}}`,
		`{"event":{"contentStart":{"contentId":"a1","type":"AUDIO","role":"ASSISTANT"}}}`,
		`{"event":{"audioOutput":{"contentId":"a1","content":"AAAA"}}}`,
		`{"event":{"contentEnd":{"contentId":"a1","type":"AUDIO","stopReason":"INTERRUPTED"}}}`,
		`{"event":{"contentStart":{"contentId":"i1","type":"TEXT","role":"ASSISTANT","additionalModelFields":"{\"generationStage\":\"FINAL\"}"}}}`,
		`{"event":{"textOutput":{"contentId":"i1","role":"ASSISTANT","content":"{ \"interrupted\" : true }"}}}`,
		`{"event":{"contentEnd":{"contentId":"i1","type":"TEXT","stopReason":"INTERRUPTED"}}}`,
		`{"event":{"contentStart":{"contentId":"f1","type":"TEXT","role":"ASSISTANT","additionalModelFields":"{\"generationStage\":\"FINAL\"}"}}}`,
		`{"event":{"textOutput":{"contentId":"f1","role":"ASSISTANT","content":"Once upon a time"}}}`,
		`{"event":{"contentEnd":{"contentId":"f1","type":"TEXT","stopReason":"PARTIAL_TURN"}}}`,
		`{"event":{"contentStart":{"contentId":"u2","type":"TEXT","role":"USER","additionalModelFields":"{\"generationStage\":\"FINAL\"}"}}}`,
	)
	types := strings.Join(clientTypes(out), ",")
	if n := strings.Count(types, "response.done"); n != 1 || strings.Count(types, "response.created") != 1 {
		t.Fatalf("barge-in must leave exactly one (cancelled) response, got %s", types)
	}
	if status := findClientEvent(t, out, "response.done").Get("response.status").String(); status != "cancelled" {
		t.Fatalf("status = %q", status)
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
	if got := clientTypes(out); strings.Join(got[len(got)-4:], ",") != "response.done,input_audio_buffer.committed,conversation.item.added,conversation.item.done" {
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
