package gemini

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The Live frames below are trimmed captures from a session against gemini-3.8-live.
const (
	liveAudioFrame      = `{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"AQADAAQAEgA="}}],"role":"model"},"outputTranscription":{"text":"Hello "}}}`
	liveAudioOnlyFrame  = `{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"BQAGAAcA"}}]}}}`
	liveTranscriptFrame = `{"serverContent":{"outputTranscription":{"text":"there."}}}`
	liveGenerationDone  = `{"serverContent":{"generationComplete":true}}`
	liveTurnDoneUsage   = `{"serverContent":{"turnComplete":true},"usageMetadata":{"promptTokenCount":548,"responseTokenCount":30,"totalTokenCount":578,"promptTokensDetails":[{"modality":"TEXT","tokenCount":306},{"modality":"AUDIO","tokenCount":222}],"responseTokensDetails":[{"modality":"AUDIO","tokenCount":30}],"thoughtsTokenCount":40}}`
	liveToolCallFrame   = `{"toolCall":{"functionCalls":[{"name":"get_weather","args":{
  "city": "Tokyo"
},"id":"call_960817"},{"name":"get_time","args":{"tz":"UTC"},"id":"call_960821"}]}}`
	liveSetupComplete = `{"setupComplete":{}}`
)

func liveClientEvent(t *testing.T, raw string) *schemas.BifrostRealtimeEvent {
	t.Helper()
	event, err := schemas.ParseRealtimeEvent([]byte(raw))
	require.NoError(t, err)
	return event
}

// liveSend runs a client event through the translator and fails the test if it is refused.
func liveSend(t *testing.T, tr *liveTranslator, raw string) schemas.RealtimeTranslation {
	t.Helper()
	out, err := tr.ToProvider(liveClientEvent(t, raw))
	require.NoError(t, err)
	return out
}

// liveReceive runs a Live frame through the translator.
func liveReceive(t *testing.T, tr *liveTranslator, frame string) schemas.RealtimeTranslation {
	t.Helper()
	out, err := tr.ToClient(json.RawMessage(frame))
	require.NoError(t, err)
	return out
}

// liveReceiveAll runs frames in order and joins what they produce.
func liveReceiveAll(t *testing.T, tr *liveTranslator, frames ...string) []json.RawMessage {
	t.Helper()
	var events []json.RawMessage
	for _, frame := range frames {
		events = append(events, liveReceive(t, tr, frame).Client...)
	}
	return events
}

func liveEventTypes(frames []json.RawMessage) []string {
	types := make([]string, len(frames))
	for i, frame := range frames {
		types[i] = gjson.GetBytes(frame, "type").String()
	}
	return types
}

func liveFind(t *testing.T, frames []json.RawMessage, eventType string) gjson.Result {
	t.Helper()
	for _, frame := range frames {
		if gjson.GetBytes(frame, "type").String() == eventType {
			return gjson.ParseBytes(frame)
		}
	}
	t.Fatalf("no %s event among %v", eventType, liveEventTypes(frames))
	return gjson.Result{}
}

func newLiveTestTranslator(t *testing.T) *liveTranslator {
	t.Helper()
	tr, err := newLiveTranslator("gemini-3.8-live")
	require.NoError(t, err)
	return tr
}

// readyLiveTranslator returns a translator whose session is set up with the given session.update.
func readyLiveTranslator(t *testing.T, session string) *liveTranslator {
	t.Helper()
	tr := newLiveTestTranslator(t)
	out := liveSend(t, tr, `{"type":"session.update","session":`+session+`}`)
	require.Len(t, out.Provider, 1)
	liveReceive(t, tr, liveSetupComplete)
	return tr
}

func TestLiveTranslatorOpenAnnouncesTheSession(t *testing.T) {
	t.Parallel()
	out := newLiveTestTranslator(t).Open()

	require.Empty(t, out.Provider, "Live takes nothing until the client has spoken")
	require.Len(t, out.Client, 1)
	created := gjson.ParseBytes(out.Client[0])
	require.Equal(t, "session.created", created.Get("type").String())
	require.Equal(t, "gemini-3.8-live", created.Get("session.model").String())
	require.Equal(t, `["audio"]`, created.Get("session.output_modalities").Raw)
	require.NotEmpty(t, created.Get("session.id").String())
	require.Equal(t, "inf", created.Get("session.max_output_tokens").String())
}

func TestLiveTranslatorSessionUpdateSendsOneSetupAndHoldsTheRest(t *testing.T) {
	t.Parallel()
	tr := newLiveTestTranslator(t)

	out := liveSend(t, tr, `{"type":"session.update","session":{"type":"realtime","instructions":"Be brief.","temperature":0.7,"max_output_tokens":200,`+
		`"audio":{"input":{"transcription":{"model":"whisper-1"}},"output":{"voice":"Kore"}},`+
		`"tools":[{"type":"function","name":"get_weather","description":"Weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]}}`)

	require.Len(t, out.Provider, 1, "the setup goes out at once")
	require.Empty(t, out.Client, "session.updated waits for Live to accept the setup")
	setup := gjson.ParseBytes(out.Provider[0]).Get("setup")
	require.Equal(t, "models/gemini-3.8-live", setup.Get("model").String())
	require.Equal(t, `["AUDIO"]`, setup.Get("generationConfig.responseModalities").Raw)
	require.Equal(t, 0.7, setup.Get("generationConfig.temperature").Float())
	require.Equal(t, int64(200), setup.Get("generationConfig.maxOutputTokens").Int())
	require.Equal(t, "Kore", setup.Get("generationConfig.speechConfig.voice_config.prebuiltVoiceConfig.voiceName").String())
	require.Equal(t, "Be brief.", setup.Get("systemInstruction.parts.0.text").String())
	require.Equal(t, "get_weather", setup.Get("tools.0.functionDeclarations.0.name").String())
	require.True(t, setup.Get("outputAudioTranscription").Exists(), "text output is the transcript of the audio")
	require.True(t, setup.Get("inputAudioTranscription").Exists(), "the client asked for input transcription")

	// Anything the client sends before Live accepts the setup is held, in order.
	require.Empty(t, liveSend(t, tr, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`).Provider)
	require.Empty(t, liveSend(t, tr, `{"type":"response.create"}`).Provider)

	ready := liveReceive(t, tr, liveSetupComplete)
	require.Equal(t, []string{"session.updated"}, liveEventTypes(ready.Client))
	require.Len(t, ready.Provider, 1)
	require.Equal(t, "hi", gjson.ParseBytes(ready.Provider[0]).Get("clientContent.turns.0.parts.0.text").String())
	require.True(t, gjson.ParseBytes(ready.Provider[0]).Get("clientContent.turnComplete").Bool())
}

func TestLiveTranslatorSetsUpFromDefaultsWhenTheClientSpeaksFirst(t *testing.T) {
	t.Parallel()
	tr := newLiveTestTranslator(t)

	out := liveSend(t, tr, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`)
	require.Len(t, out.Provider, 1, "only the default setup goes out; the item waits behind it")
	require.True(t, gjson.ParseBytes(out.Provider[0]).Get("setup").Exists())
	require.Equal(t, []string{"conversation.item.added", "conversation.item.done"}, liveEventTypes(out.Client))

	ready := liveReceive(t, tr, liveSetupComplete)
	require.Empty(t, ready.Client, "the client never sent a session.update, so none is answered")
	require.Empty(t, ready.Provider, "the item stays buffered until response.create")
}

func TestLiveTranslatorSessionUpdateAfterSetup(t *testing.T) {
	t.Parallel()
	tr := readyLiveTranslator(t, `{"type":"realtime","instructions":"Be brief."}`)

	t.Run("a different configuration is refused", func(t *testing.T) {
		_, err := tr.ToProvider(liveClientEvent(t, `{"type":"session.update","session":{"instructions":"Be verbose."}}`))
		require.ErrorContains(t, err, "fixes a session's configuration")
	})
	t.Run("the same configuration is acknowledged", func(t *testing.T) {
		out := liveSend(t, tr, `{"type":"session.update","session":{"instructions":"Be brief."}}`)
		require.Empty(t, out.Provider)
		require.Equal(t, []string{"session.updated"}, liveEventTypes(out.Client))
	})
	t.Run("what the setup does not hold can change", func(t *testing.T) {
		out := liveSend(t, tr, `{"type":"session.update","session":{"instructions":"Be brief.","output_modalities":["text"]}}`)
		require.Equal(t, `["text"]`, gjson.ParseBytes(out.Client[0]).Get("session.output_modalities").Raw)
	})
}

func TestLiveTranslatorQueueIsBounded(t *testing.T) {
	t.Parallel()
	tr := newLiveTestTranslator(t)
	chunk := `{"type":"input_audio_buffer.append","audio":"` + strings.Repeat("A", 1<<20) + `"}`

	var err error
	for range 6 {
		_, err = tr.ToProvider(liveClientEvent(t, chunk))
		if err != nil {
			break
		}
	}
	require.ErrorContains(t, err, "did not start in time")
}

func TestLiveTranslatorTextTurn(t *testing.T) {
	t.Parallel()
	tr := readyLiveTranslator(t, `{"type":"realtime"}`)

	item := liveSend(t, tr, `{"type":"conversation.item.create","item":{"id":"item_user","type":"message","role":"user","content":[{"type":"input_text","text":"Say hello."}]}}`)
	require.Empty(t, item.Provider, "Live wants a turn's items together, so they wait for response.create")
	require.Equal(t, "item_user", gjson.ParseBytes(item.Client[0]).Get("item.id").String())

	create := liveSend(t, tr, `{"type":"response.create"}`)
	require.Len(t, create.Provider, 1)
	content := gjson.ParseBytes(create.Provider[0]).Get("clientContent")
	require.Equal(t, "user", content.Get("turns.0.role").String())
	require.Equal(t, "Say hello.", content.Get("turns.0.parts.0.text").String())
	require.True(t, content.Get("turnComplete").Bool())

	events := liveReceiveAll(t, tr, `{}`, `{"serverContent":{}}`, liveAudioFrame, liveAudioOnlyFrame, liveTranscriptFrame, liveGenerationDone, liveTurnDoneUsage)
	require.Equal(t, []string{
		"response.created",
		"response.output_item.added", "conversation.item.added", "response.content_part.added",
		"response.output_audio.delta", "response.output_audio_transcript.delta",
		"response.output_audio.delta",
		"response.output_audio_transcript.delta",
		"response.output_audio.done", "response.output_audio_transcript.done", "response.content_part.done",
		"response.output_item.done", "conversation.item.done",
		"response.done",
	}, liveEventTypes(events))

	require.Equal(t, "AQADAAQAEgA=", liveFind(t, events, "response.output_audio.delta").Get("delta").String(), "audio passes through untouched")
	done := liveFind(t, events, "response.done").Get("response")
	require.Equal(t, "completed", done.Get("status").String())
	require.Equal(t, "Hello there.", done.Get("output.0.content.0.transcript").String(), "the turn log reads the assistant text from here")
	require.Equal(t, "assistant", done.Get("output.0.role").String())

	// Live counts thinking separately from the response; the chat converter folds it into output.
	usage := done.Get("usage")
	require.Equal(t, int64(548), usage.Get("input_tokens").Int())
	require.Equal(t, int64(70), usage.Get("output_tokens").Int())
	require.Equal(t, int64(618), usage.Get("total_tokens").Int())
	require.Equal(t, int64(306), usage.Get("input_token_details.text_tokens").Int())
	require.Equal(t, int64(222), usage.Get("input_token_details.audio_tokens").Int())
	require.Equal(t, int64(30), usage.Get("output_token_details.audio_tokens").Int())
	require.Equal(t, int64(40), usage.Get("output_token_details.reasoning_tokens").Int())
}

func TestLiveTranslatorTextOnlyOutput(t *testing.T) {
	t.Parallel()
	tr := readyLiveTranslator(t, `{"type":"realtime","output_modalities":["text"]}`)

	events := liveReceiveAll(t, tr, liveAudioFrame, liveGenerationDone, liveTurnDoneUsage)

	require.NotContains(t, liveEventTypes(events), "response.output_audio.delta", "text-only clients get no audio")
	require.Equal(t, "Hello ", liveFind(t, events, "response.output_text.delta").Get("delta").String())
	require.Equal(t, "Hello ", liveFind(t, events, "response.output_text.done").Get("text").String())
	require.Equal(t, "output_text", liveFind(t, events, "response.content_part.added").Get("part.type").String())
	require.Equal(t, "Hello ", liveFind(t, events, "response.done").Get("response.output.0.content.0.text").String())
}

func TestLiveTranslatorResponseCreateOverrides(t *testing.T) {
	t.Parallel()

	t.Run("instructions ride along as a turn and modalities apply to one response", func(t *testing.T) {
		tr := readyLiveTranslator(t, `{"type":"realtime"}`)
		out := liveSend(t, tr, `{"type":"response.create","response":{"instructions":"Please assist the user.","output_modalities":["text"]}}`)
		require.Equal(t, "Please assist the user.", gjson.ParseBytes(out.Provider[0]).Get("clientContent.turns.0.parts.0.text").String())

		first := liveReceiveAll(t, tr, liveAudioFrame, liveTurnDoneUsage)
		require.Contains(t, liveEventTypes(first), "response.output_text.delta")
		second := liveReceiveAll(t, tr, liveAudioFrame, liveTurnDoneUsage)
		require.Contains(t, liveEventTypes(second), "response.output_audio.delta", "the override ends with its response")
	})
	t.Run("input items are sent as turns", func(t *testing.T) {
		tr := readyLiveTranslator(t, `{"type":"realtime"}`)
		out := liveSend(t, tr, `{"type":"response.create","response":{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"What is 2+2?"}]}]}}`)
		require.Equal(t, "What is 2+2?", gjson.ParseBytes(out.Provider[0]).Get("clientContent.turns.0.parts.0.text").String())
	})
	t.Run("a bad input item leaves nothing buffered", func(t *testing.T) {
		tr := readyLiveTranslator(t, `{"type":"realtime"}`)
		_, err := tr.ToProvider(liveClientEvent(t, `{"type":"response.create","response":{"input":[`+
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"ok"}]},{"type":"message","role":"system","content":"no"}]}}`))
		require.Error(t, err)
		require.Empty(t, tr.turns)
	})
	t.Run("without anything to say it asks for an answer", func(t *testing.T) {
		tr := readyLiveTranslator(t, `{"type":"realtime"}`)
		out := liveSend(t, tr, `{"type":"response.create"}`)
		require.True(t, gjson.ParseBytes(out.Provider[0]).Get("clientContent.turnComplete").Bool())
	})
}

func TestLiveTranslatorClientItemsAreValidated(t *testing.T) {
	t.Parallel()
	tr := readyLiveTranslator(t, `{"type":"realtime"}`)

	for name, event := range map[string]string{
		"system messages":      `{"type":"conversation.item.create","item":{"type":"message","role":"system","content":[{"type":"input_text","text":"x"}]}}`,
		"audio without text":   `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_audio","audio":"AAAA"}]}}`,
		"images":               `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:"}]}}`,
		"empty messages":       `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[]}}`,
		"unknown item types":   `{"type":"conversation.item.create","item":{"type":"mcp_call"}}`,
		"outputs without call": `{"type":"conversation.item.create","item":{"type":"function_call_output","output":"x"}}`,
		"item deletion":        `{"type":"conversation.item.delete","item_id":"item_1"}`,
		"unknown events":       `{"type":"response.something_new"}`,
	} {
		_, err := tr.ToProvider(liveClientEvent(t, event))
		require.Error(t, err, name)
	}
	require.Empty(t, tr.turns, "refused items must not be buffered")
}

func TestLiveTranslatorAcceptsAdvisoryEventsQuietly(t *testing.T) {
	t.Parallel()
	tr := readyLiveTranslator(t, `{"type":"realtime"}`)

	// Clients send these on every interruption; Live already interrupted and dropped the audio.
	for _, event := range []string{`{"type":"response.cancel"}`, `{"type":"input_audio_buffer.clear"}`, `{"type":"conversation.item.truncate","item_id":"i","content_index":0,"audio_end_ms":10}`} {
		out := liveSend(t, tr, event)
		require.Empty(t, out.Provider)
		require.Empty(t, out.Client)
	}
}

func TestLiveTranslatorAudioInput(t *testing.T) {
	t.Parallel()

	t.Run("the sample rate comes from the session", func(t *testing.T) {
		tr := readyLiveTranslator(t, `{"type":"realtime","audio":{"input":{"format":{"type":"audio/pcm","rate":16000}}}}`)
		out := liveSend(t, tr, `{"type":"input_audio_buffer.append","audio":"AQID"}`)
		audio := gjson.ParseBytes(out.Provider[0]).Get("realtimeInput.audio")
		require.Equal(t, "AQID", audio.Get("data").String())
		require.Equal(t, "audio/pcm;rate=16000", audio.Get("mimeType").String())
	})
	t.Run("the default is the 24 kHz of pcm16", func(t *testing.T) {
		tr := readyLiveTranslator(t, `{"type":"realtime"}`)
		out := liveSend(t, tr, `{"type":"input_audio_buffer.append","audio":"AQID"}`)
		require.Equal(t, "audio/pcm;rate=24000", gjson.ParseBytes(out.Provider[0]).Get("realtimeInput.audio.mimeType").String())
	})
	t.Run("commit ends the stream when the server detects speech", func(t *testing.T) {
		tr := readyLiveTranslator(t, `{"type":"realtime"}`)
		liveSend(t, tr, `{"type":"input_audio_buffer.append","audio":"AQID"}`)
		out := liveSend(t, tr, `{"type":"input_audio_buffer.commit"}`)
		require.True(t, gjson.ParseBytes(out.Provider[0]).Get("realtimeInput.audioStreamEnd").Bool())
	})
	t.Run("with manual detection audio is bracketed", func(t *testing.T) {
		tr := readyLiveTranslator(t, `{"type":"realtime","audio":{"input":{"turn_detection":null}}}`)
		first := liveSend(t, tr, `{"type":"input_audio_buffer.append","audio":"AQID"}`)
		require.Len(t, first.Provider, 2)
		require.True(t, gjson.ParseBytes(first.Provider[0]).Get("realtimeInput.activityStart").Exists())
		require.True(t, gjson.ParseBytes(first.Provider[1]).Get("realtimeInput.audio").Exists())
		second := liveSend(t, tr, `{"type":"input_audio_buffer.append","audio":"BAUG"}`)
		require.Len(t, second.Provider, 1, "activityStart is sent once per utterance")
		end := liveSend(t, tr, `{"type":"input_audio_buffer.commit"}`)
		require.True(t, gjson.ParseBytes(end.Provider[0]).Get("realtimeInput.activityEnd").Exists())
	})
	t.Run("an empty buffer cannot be committed", func(t *testing.T) {
		tr := readyLiveTranslator(t, `{"type":"realtime"}`)
		_, err := tr.ToProvider(liveClientEvent(t, `{"type":"input_audio_buffer.commit"}`))
		require.ErrorContains(t, err, "empty")
	})
	t.Run("a response after audio does not interrupt the answer to it", func(t *testing.T) {
		tr := readyLiveTranslator(t, `{"type":"realtime"}`)
		liveSend(t, tr, `{"type":"input_audio_buffer.append","audio":"AQID"}`)
		liveSend(t, tr, `{"type":"input_audio_buffer.commit"}`)
		require.Empty(t, liveSend(t, tr, `{"type":"response.create"}`).Provider)
	})
}

func TestLiveTranslatorReportsSpeech(t *testing.T) {
	t.Parallel()
	tr := readyLiveTranslator(t, `{"type":"realtime","audio":{"input":{"transcription":{}}}}`)

	// Live reports the transcript just before the end of the speech.
	events := liveReceiveAll(t, tr,
		`{"voiceActivity":{"type":"ACTIVITY_START","audioOffset":"0.160s"}}`,
		`{"serverContent":{"inputTranscription":{"text":"What is the capital of France?"}}}`,
		`{"voiceActivity":{"type":"ACTIVITY_END","audioOffset":"1.366125s"}}`,
	)

	require.Equal(t, []string{
		"input_audio_buffer.speech_started",
		"input_audio_buffer.speech_stopped", "input_audio_buffer.committed", "conversation.item.added",
		"conversation.item.input_audio_transcription.completed", "conversation.item.done",
	}, liveEventTypes(events))
	require.Equal(t, int64(160), liveFind(t, events, "input_audio_buffer.speech_started").Get("audio_start_ms").Int())
	require.Equal(t, int64(1366), liveFind(t, events, "input_audio_buffer.speech_stopped").Get("audio_end_ms").Int())
	completed := liveFind(t, events, "conversation.item.input_audio_transcription.completed")
	require.Equal(t, "What is the capital of France?", completed.Get("transcript").String())
	require.Equal(t, liveFind(t, events, "input_audio_buffer.committed").Get("item_id").String(), completed.Get("item_id").String())
}

func TestLiveTranslatorReportsSpeechWithoutATranscript(t *testing.T) {
	t.Parallel()
	tr := readyLiveTranslator(t, `{"type":"realtime"}`)

	events := liveReceiveAll(t, tr,
		`{"serverContent":{},"voiceActivity":{"type":"ACTIVITY_START","audioOffset":"0.1s"}}`,
		`{"serverContent":{},"voiceActivity":{"type":"ACTIVITY_END","audioOffset":"0.9s"}}`,
	)

	require.Equal(t, []string{
		"input_audio_buffer.speech_started",
		"input_audio_buffer.speech_stopped", "input_audio_buffer.committed", "conversation.item.added", "conversation.item.done",
	}, liveEventTypes(events), "no transcript was asked for, so the item completes at the end of the speech")
}

func TestLiveTranslatorToolCalls(t *testing.T) {
	t.Parallel()
	tr := readyLiveTranslator(t, `{"type":"realtime"}`)

	events := liveReceiveAll(t, tr, liveToolCallFrame, liveGenerationDone, `{"serverContent":{"turnComplete":true},"usageMetadata":{"promptTokenCount":612,"responseTokenCount":12,"totalTokenCount":624}}`)

	require.Equal(t, []string{
		"response.created",
		"response.output_item.added", "conversation.item.added", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.output_item.done", "conversation.item.done",
		"response.output_item.added", "conversation.item.added", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.output_item.done", "conversation.item.done",
		"response.done",
	}, liveEventTypes(events))
	done := liveFind(t, events, "response.done").Get("response")
	require.Equal(t, "function_call", done.Get("output.0.type").String())
	require.Equal(t, "get_weather", done.Get("output.0.name").String())
	require.Equal(t, "call_960817", done.Get("output.0.call_id").String())
	require.Equal(t, `{"city":"Tokyo"}`, done.Get("output.0.arguments").String(), "arguments are compact JSON, as OpenAI sends them")
	require.Equal(t, "call_960821", done.Get("output.1.call_id").String(), "parallel calls arrive as separate items")

	// The client answers each call as its own item; Live wants all of them in one frame, with names.
	liveSend(t, tr, `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"call_960817","output":"{\"temp_c\":18}"}}`)
	liveSend(t, tr, `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"call_960821","output":"12:00"}}`)
	out := liveSend(t, tr, `{"type":"response.create"}`)

	require.Len(t, out.Provider, 1, "one toolResponse for the whole turn, and no turnComplete that would interrupt the answer")
	responses := gjson.ParseBytes(out.Provider[0]).Get("toolResponse.functionResponses")
	require.Len(t, responses.Array(), 2)
	require.Equal(t, "call_960817", responses.Get("0.id").String())
	require.Equal(t, "get_weather", responses.Get("0.name").String())
	require.Equal(t, int64(18), responses.Get("0.response.temp_c").Int(), "an object output is passed on as it is")
	require.Equal(t, "get_time", responses.Get("1.name").String())
	require.Equal(t, "12:00", responses.Get("1.response.output").String(), "Live needs an object, so other outputs are wrapped")
}

func TestLiveTranslatorInterruption(t *testing.T) {
	t.Parallel()
	tr := readyLiveTranslator(t, `{"type":"realtime"}`)

	events := liveReceiveAll(t, tr, liveAudioFrame, `{"serverContent":{"interrupted":true}}`, liveTurnDoneUsage)

	require.Equal(t, "incomplete", liveFind(t, events, "response.output_item.done").Get("item.status").String())
	done := liveFind(t, events, "response.done").Get("response")
	require.Equal(t, "cancelled", done.Get("status").String())
	require.Equal(t, "cancelled", done.Get("status_details.type").String())
	require.Equal(t, int64(548), done.Get("usage.input_tokens").Int(), "an interrupted turn is still billed")
}

func TestLiveTranslatorExtendedThinkingTurnsSpanTwoCompletions(t *testing.T) {
	t.Parallel()
	tr := readyLiveTranslator(t, `{"type":"realtime","reasoning":{"effort":"high"}}`)

	filler := liveReceiveAll(t, tr, liveAudioFrame, liveGenerationDone,
		`{"serverContent":{"turnComplete":true,"interactionStatus":"IN_PROGRESS"},"usageMetadata":{"promptTokenCount":100,"responseTokenCount":10,"totalTokenCount":110}}`)
	require.NotContains(t, liveEventTypes(filler), "response.done", "the model has more to say")

	answer := liveReceiveAll(t, tr, liveAudioOnlyFrame, liveGenerationDone,
		`{"serverContent":{"turnComplete":true,"interactionStatus":"IDLE"},"usageMetadata":{"promptTokenCount":150,"responseTokenCount":40,"thoughtsTokenCount":20,"totalTokenCount":210}}`)

	done := liveFind(t, answer, "response.done").Get("response")
	require.Len(t, done.Get("output").Array(), 2, "the filler and the answer are two messages of one response")
	require.Equal(t, int64(250), done.Get("usage.input_tokens").Int(), "each generation re-sends the context, and both are billed")
	require.Equal(t, int64(70), done.Get("usage.output_tokens").Int())
}

func TestLiveTranslatorKeepsUsageOfATurnWithoutOutput(t *testing.T) {
	t.Parallel()
	tr := readyLiveTranslator(t, `{"type":"realtime"}`)

	events := liveReceiveAll(t, tr, liveTurnDoneUsage)

	require.Equal(t, []string{"response.created", "response.done"}, liveEventTypes(events))
	require.Equal(t, int64(548), liveFind(t, events, "response.done").Get("response.usage.input_tokens").Int())
}

func TestLiveTranslatorIgnoresFramesItDoesNotUse(t *testing.T) {
	t.Parallel()
	tr := readyLiveTranslator(t, `{"type":"realtime"}`)

	for _, frame := range []string{`{}`, `{"serverContent":{}}`, `{"sessionResumptionUpdate":{"newHandle":"abc","resumable":true}}`, `{"goAway":{"timeLeft":"50s"}}`, `{"toolCallCancellation":{"ids":["x"]}}`} {
		out := liveReceive(t, tr, frame)
		require.Empty(t, out.Client, frame)
		require.Empty(t, out.Provider, frame)
	}
	_, err := tr.ToClient(json.RawMessage(`not json`))
	require.Error(t, err)
}

func TestLiveTranslatorCloseBecomesAnErrorEvent(t *testing.T) {
	t.Parallel()
	tr := newLiveTestTranslator(t)

	for name, tc := range map[string]struct {
		want, reason string
		closeCode    int
	}{
		"invalid key":        {want: "invalid_api_key", reason: "API key not valid. Please pass a valid API key.", closeCode: 1007},
		"no key":             {want: "invalid_api_key", reason: "Method doesn't allow unregistered callers (callers without established identity).", closeCode: 1008},
		"unknown model":      {want: "model_not_found", reason: "models/nope is not found for API version v1beta, or is not supported for bidiGenerateContent.", closeCode: 1008},
		"unsupported output": {want: "unsupported_modality", reason: "The requested combination of response modalities (AUDIO) is not supported by the model. models/gemini-3.5-transcribe-live", closeCode: 1007},
		"thinking level":     {want: "invalid_reasoning_effort", reason: "Thinking level must be specified for this model.", closeCode: 1007},
		"quota":              {want: "rate_limit_exceeded", reason: "Resource has been exhausted (e.g. check quota).", closeCode: 1011},
		"anything else":      {want: "gemini_live_closed", reason: "Deadline expired before operation could complete.", closeCode: 1011},
	} {
		out := tr.Close(tc.closeCode, tc.reason)
		require.Len(t, out.Client, 1, name)
		event := gjson.ParseBytes(out.Client[0])
		require.Equal(t, "error", event.Get("type").String(), name)
		require.Equal(t, tc.want, event.Get("error.code").String(), name)
	}

	leak := gjson.ParseBytes(tr.Close(1011, "Internal failure in models/gemini_api_beyond_live_rev25_x while serving").Client[0])
	require.Equal(t, "Internal failure in the model while serving", leak.Get("error.message").String(), "internal model ids are not shown")

	require.Empty(t, tr.Close(1000, "").Client, "a normal closure is not an error")
	require.Empty(t, tr.Close(1001, "going away").Client)
}

func TestLiveTranslatorSessionConfigurationErrors(t *testing.T) {
	t.Parallel()
	tr := newLiveTestTranslator(t)

	for name, session := range map[string]string{
		"g711 input":     `{"input_audio_format":"g711_ulaw"}`,
		"pcmu input":     `{"audio":{"input":{"format":{"type":"audio/pcmu"}}}}`,
		"output rate":    `{"audio":{"output":{"format":{"type":"audio/pcm","rate":16000}}}}`,
		"mcp tools":      `{"tools":[{"type":"mcp","server_label":"x"}]}`,
		"nameless tools": `{"tools":[{"type":"function"}]}`,
	} {
		_, err := tr.ToProvider(liveClientEvent(t, `{"type":"session.update","session":`+session+`}`))
		require.Error(t, err, name)
	}
	// A refused update must not start the session with something else.
	require.Equal(t, liveAwaitingSetup, tr.phase)
}

// BenchmarkLiveTranslatorAudioFrame measures a Live audio chunk with its transcript becoming OpenAI
// events, the frame that dominates a voice session.
func BenchmarkLiveTranslatorAudioFrame(b *testing.B) {
	tr, err := newLiveTranslator("gemini-3.8-live")
	require.NoError(b, err)
	frame := json.RawMessage(fmt.Sprintf(`{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":%q}}]},"outputTranscription":{"text":"Hello "}}}`, strings.Repeat("AQID", 4000)))
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for b.Loop() {
		if _, err := tr.ToClient(frame); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLiveTranslatorAudioAppend measures a client audio chunk becoming a Live frame.
func BenchmarkLiveTranslatorAudioAppend(b *testing.B) {
	tr, err := newLiveTranslator("gemini-3.8-live")
	require.NoError(b, err)
	session, err := schemas.ParseRealtimeEvent([]byte(`{"type":"session.update","session":{"type":"realtime"}}`))
	require.NoError(b, err)
	_, err = tr.ToProvider(session)
	require.NoError(b, err)
	_, err = tr.ToClient(json.RawMessage(liveSetupComplete))
	require.NoError(b, err)
	event, err := schemas.ParseRealtimeEvent([]byte(`{"type":"input_audio_buffer.append","audio":"` + strings.Repeat("AQID", 700) + `"}`))
	require.NoError(b, err)
	b.ReportAllocs()
	b.SetBytes(int64(len(event.RawData)))
	for b.Loop() {
		if _, err := tr.ToProvider(event); err != nil {
			b.Fatal(err)
		}
	}
}
