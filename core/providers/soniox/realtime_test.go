package soniox

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	sessionVAD    = `{"type":"session.update","session":{"audio":{"input":{"format":{"type":"audio/pcm","rate":16000},"transcription":{"model":"soniox/stt-rt-v5"}}}}}`
	sessionManual = `{"type":"session.update","session":{"audio":{"input":{"format":{"type":"audio/pcm","rate":16000},"transcription":{"model":"soniox/stt-rt-v5"},"turn_detection":null}}}}`
)

// The sentence of a live capture, as the tokens Soniox sent: "What's the weather, like in Paris right now?"
var weatherTokens = []string{"Wh", "at", "'s", " the", " we", "ath", "er,", " l", "ike", " in", " Paris", " ", "right", " now", "?"}

func clientEvent(t testing.TB, raw string) *schemas.BifrostRealtimeEvent {
	t.Helper()
	event, err := schemas.ParseRealtimeEvent([]byte(raw))
	require.NoError(t, err)
	return event
}

func audioEvent(t testing.TB, audio []byte) *schemas.BifrostRealtimeEvent {
	t.Helper()
	return clientEvent(t, `{"type":"input_audio_buffer.append","audio":"`+base64.StdEncoding.EncodeToString(audio)+`"}`)
}

// wire builds a Soniox server frame. A token is final unless it starts with "~".
func wire(totalMs int, tokens ...string) json.RawMessage {
	list := make([]map[string]any, 0, len(tokens))
	for i, text := range tokens {
		list = append(list, map[string]any{"text": strings.TrimPrefix(text, "~"), "start_ms": 60 * (i + 1), "end_ms": 60 * (i + 2), "confidence": 1, "is_final": !strings.HasPrefix(text, "~")})
	}
	frame, _ := json.Marshal(map[string]any{"tokens": list, "final_audio_proc_ms": 0, "total_audio_proc_ms": totalMs})
	return frame
}

// nonFinal returns the tokens as the non-final ones of a frame.
func nonFinal(tokens ...string) []string {
	out := make([]string, len(tokens))
	for i, token := range tokens {
		out[i] = "~" + token
	}
	return out
}

func toProvider(t testing.TB, translator *sonioxTranslator, event *schemas.BifrostRealtimeEvent) schemas.RealtimeTranslation {
	t.Helper()
	translation, err := translator.ToProvider(event)
	require.NoError(t, err)
	return translation
}

func toClient(t testing.TB, translator *sonioxTranslator, frame json.RawMessage) schemas.RealtimeTranslation {
	t.Helper()
	translation, err := translator.ToClient(frame)
	require.NoError(t, err)
	return translation
}

// types returns the type of each event the client receives.
func types(translation schemas.RealtimeTranslation) []string {
	out := make([]string, len(translation.Client))
	for i, frame := range translation.Client {
		out[i] = gjson.GetBytes(frame, "type").String()
	}
	return out
}

// find returns the first client event of a type.
func find(t testing.TB, translation schemas.RealtimeTranslation, eventType string) gjson.Result {
	t.Helper()
	for _, frame := range translation.Client {
		if parsed := gjson.ParseBytes(frame); parsed.Get("type").String() == eventType {
			return parsed
		}
	}
	require.Failf(t, "event not found", "no %s in %v", eventType, types(translation))
	return gjson.Result{}
}

// findAll returns every client event of a type, in order.
func findAll(translation schemas.RealtimeTranslation, eventType string) []gjson.Result {
	var out []gjson.Result
	for _, frame := range translation.Client {
		if parsed := gjson.ParseBytes(frame); parsed.Get("type").String() == eventType {
			out = append(out, parsed)
		}
	}
	return out
}

// queueCommits sends audio and a commit n times without an answer from Soniox, and returns the commits' items.
func queueCommits(t testing.TB, translator *sonioxTranslator, n int) []string {
	t.Helper()
	items := make([]string, n)
	for i := range items {
		toProvider(t, translator, audioEvent(t, []byte{1, 2}))
		items[i] = find(t, toProvider(t, translator, clientEvent(t, `{"type":"input_audio_buffer.commit"}`)), "input_audio_buffer.committed").Get("item_id").String()
	}
	return items
}

// startedTranslator opens a translator and sends the session.update that starts the connection.
func startedTranslator(t testing.TB, session string) *sonioxTranslator {
	t.Helper()
	translator := newSonioxTranslator("stt-rt-v5")
	started := toProvider(t, translator, clientEvent(t, session))
	require.Len(t, started.Provider, 1, "the first session.update that names the model sends the start frame")
	return translator
}

func TestSonioxRealtimeProvider(t *testing.T) {
	provider := &SonioxProvider{
		realtimeURL: "wss://stt-rt.in.soniox.com/transcribe-websocket",
		networkConfig: schemas.NetworkConfig{ExtraHeaders: map[string]schemas.SecretVar{
			"X-Trace":       *schemas.NewSecretVar("trace-1"),
			"authorization": *schemas.NewSecretVar("Bearer extra"),
		}},
	}
	assert.True(t, provider.SupportsRealtimeAPI())
	assert.False(t, provider.SupportsRealtimeWebRTC())

	t.Run("a transcription session on an stt-rt model gets the regional socket", func(t *testing.T) {
		url, bifrostErr := provider.RealtimeWebSocketURL(nil, schemas.Key{}, "stt-rt-v5", "transcription")
		require.Nil(t, bifrostErr)
		assert.Equal(t, "wss://stt-rt.in.soniox.com/transcribe-websocket", url)
	})
	t.Run("any other intent is not served", func(t *testing.T) {
		_, bifrostErr := provider.RealtimeWebSocketURL(nil, schemas.Key{}, "stt-rt-v5", "")
		require.NotNil(t, bifrostErr)
		require.NotNil(t, bifrostErr.Error)
		assert.Contains(t, bifrostErr.Error.Message, "not supported")
	})
	t.Run("an async model is refused with the reason", func(t *testing.T) {
		_, bifrostErr := provider.RealtimeWebSocketURL(nil, schemas.Key{}, "stt-async-v5", "transcription")
		require.NotNil(t, bifrostErr)
		assert.Contains(t, bifrostErr.Error.Message, "stt-rt")
		_, bifrostErr = provider.NewRealtimeTranslator(nil, schemas.Key{}, "stt-async-v5")
		require.NotNil(t, bifrostErr)
		assert.Contains(t, bifrostErr.Error.Message, "stt-rt")
	})
	t.Run("the key is a bearer token and extra headers cannot replace it", func(t *testing.T) {
		headers, bifrostErr := provider.RealtimeHeaders(nil, schemas.Key{Value: *schemas.NewSecretVar("secret")})
		require.Nil(t, bifrostErr)
		assert.Equal(t, map[string]string{"Authorization": "Bearer secret", "X-Trace": "trace-1"}, headers)
	})
	t.Run("a translated session never uses the provider's own event conversion", func(t *testing.T) {
		_, err := provider.ToBifrostRealtimeEvent(json.RawMessage(`{}`))
		assert.Error(t, err)
		_, err = provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{})
		assert.Error(t, err)
	})
}

func TestNewSonioxSettings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session string
		want    string
	}{
		{"default is 24 kHz PCM with endpoint detection", ``,
			`{"model":"stt-rt-v5","audio_format":"pcm_s16le","sample_rate":24000,"num_channels":1,"enable_endpoint_detection":true}`},
		{"pcm rate is the client's", `{"audio":{"input":{"format":{"type":"audio/pcm","rate":22050}}}}`,
			`{"model":"stt-rt-v5","audio_format":"pcm_s16le","sample_rate":22050,"num_channels":1,"enable_endpoint_detection":true}`},
		{"pcmu is mulaw at 8 kHz", `{"audio":{"input":{"format":{"type":"audio/pcmu"}}}}`,
			`{"model":"stt-rt-v5","audio_format":"mulaw","sample_rate":8000,"num_channels":1,"enable_endpoint_detection":true}`},
		{"pcma is alaw at 8 kHz", `{"audio":{"input":{"format":{"type":"audio/pcma"}}}}`,
			`{"model":"stt-rt-v5","audio_format":"alaw","sample_rate":8000,"num_channels":1,"enable_endpoint_detection":true}`},
		{"null turn detection hands commits to the client", `{"audio":{"input":{"turn_detection":null}}}`,
			`{"model":"stt-rt-v5","audio_format":"pcm_s16le","sample_rate":24000,"num_channels":1}`},
		{"silence becomes the endpoint delay", `{"audio":{"input":{"turn_detection":{"type":"semantic_vad","silence_duration_ms":800}}}}`,
			`{"model":"stt-rt-v5","audio_format":"pcm_s16le","sample_rate":24000,"num_channels":1,"enable_endpoint_detection":true,"max_endpoint_delay_ms":800}`},
		{"transcription options reach the start frame", `{"audio":{"input":{"transcription":{"language":"hi","language_hints":["en","hi"],"prompt":"A billing call","context":{"terms":["Jio","Reliance"]},"enable_speaker_diarization":true,"enable_language_identification":true,"client_reference_id":"ref-1"}}}}`,
			`{"model":"stt-rt-v5","audio_format":"pcm_s16le","sample_rate":24000,"num_channels":1,"enable_endpoint_detection":true,"language_hints":["hi","en"],"context":{"text":"A billing call","terms":["Jio","Reliance"]},"enable_speaker_diarization":true,"enable_language_identification":true,"client_reference_id":"ref-1"}`},
		{"context text wins over the prompt", `{"audio":{"input":{"transcription":{"prompt":"from prompt","context":{"text":"from context"}}}}}`,
			`{"model":"stt-rt-v5","audio_format":"pcm_s16le","sample_rate":24000,"num_channels":1,"enable_endpoint_detection":true,"context":{"text":"from context"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, err := newSonioxSettings("stt-rt-v5", []byte(tc.session))
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(settings.frame))
		})
	}

	t.Run("what the client is told follows what it asked for", func(t *testing.T) {
		settings, err := newSonioxSettings("stt-rt-v5", []byte(`{"bifrost":{"extensions":true},"audio":{"input":{"turn_detection":{"type":"semantic_vad","silence_duration_ms":800}}}}`))
		require.NoError(t, err)
		assert.True(t, settings.extensions)
		assert.Equal(t, sonioxAudioFormat{Type: "audio/pcm", Rate: 24000}, settings.input.Format)
		assert.Equal(t, &sonioxTurnDetection{Type: "semantic_vad", SilenceDurationMs: 800}, settings.input.TurnDetection)

		manual, err := newSonioxSettings("stt-rt-v5", []byte(`{"audio":{"input":{"turn_detection":null}}}`))
		require.NoError(t, err)
		assert.Nil(t, manual.input.TurnDetection)

		unsupported, err := newSonioxSettings("stt-rt-v5", []byte(`{"audio":{"input":{"turn_detection":{"type":"disabled","silence_duration_ms":500}}}}`))
		require.NoError(t, err)
		assert.Equal(t, &sonioxTurnDetection{Type: "server_vad", SilenceDurationMs: 500}, unsupported.input.TurnDetection,
			"Soniox applies its endpoint detection whatever the type, so the client is told what is applied")
	})
	t.Run("an input format Soniox cannot take is refused", func(t *testing.T) {
		_, err := newSonioxSettings("stt-rt-v5", []byte(`{"audio":{"input":{"format":{"type":"audio/wav"}}}}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "audio/pcm")
	})
}

func TestSonioxTranslatorStartsOnTheFirstSessionUpdate(t *testing.T) {
	translator := newSonioxTranslator("stt-rt-v5")

	opened := translator.Open()
	assert.Equal(t, []string{"session.created"}, types(opened))
	assert.Equal(t, "transcription", find(t, opened, "session.created").Get("session.type").String())

	started := toProvider(t, translator, clientEvent(t, sessionVAD))
	require.Len(t, started.Provider, 1)
	assert.False(t, started.IsProviderBinary(0), "the start frame is text")
	assert.JSONEq(t, `{"model":"stt-rt-v5","audio_format":"pcm_s16le","sample_rate":16000,"num_channels":1,"enable_endpoint_detection":true}`, string(started.Provider[0]))
	updated := find(t, started, "session.updated")
	assert.Equal(t, "stt-rt-v5", updated.Get("session.audio.input.transcription.model").String())
	assert.Equal(t, "server_vad", updated.Get("session.audio.input.turn_detection.type").String())

	repeated := toProvider(t, translator, clientEvent(t, sessionVAD))
	assert.Empty(t, repeated.Provider, "repeating the settings is acknowledged, and sends nothing")
	assert.Equal(t, []string{"session.updated"}, types(repeated))

	_, err := translator.ToProvider(clientEvent(t, `{"type":"session.update","session":{"audio":{"input":{"transcription":{"language":"hi"}}}}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fixes a session's settings")
}

func TestSonioxTranslatorSessionIDIsUniquePerConnection(t *testing.T) {
	first := find(t, newSonioxTranslator("stt-rt-v5").Open(), "session.created").Get("session.id").String()
	second := find(t, newSonioxTranslator("stt-rt-v5").Open(), "session.created").Get("session.id").String()

	assert.True(t, strings.HasPrefix(first, "sess_"), first)
	assert.NotEqual(t, first, second, "the session ID is logged as the provider session ID, so two connections must not share one")
}

func TestSonioxTranslatorHoldsTheStartUntilTheModelIsNamed(t *testing.T) {
	translator := newSonioxTranslator("stt-rt-v5")

	first := toProvider(t, translator, clientEvent(t, `{"type":"session.update","session":{"audio":{"input":{"format":{"type":"audio/pcma"}}}}}`))
	assert.Empty(t, first.Provider, "a session.update that does not name the model starts nothing")

	second := toProvider(t, translator, clientEvent(t, `{"type":"session.update","session":{"audio":{"input":{"transcription":{"model":"stt-rt-v5"}}}}}`))
	require.Len(t, second.Provider, 1)
	assert.Equal(t, "alaw", gjson.GetBytes(second.Provider[0], "audio_format").String(), "the start frame builds on the first update")
}

func TestSonioxTranslatorStartsOnDefaultsWhenAudioComesFirst(t *testing.T) {
	translator := newSonioxTranslator("stt-rt-v5")
	pcm := []byte{0x01, 0x00, 0xff, 0x7f}

	sent := toProvider(t, translator, audioEvent(t, pcm))

	require.Len(t, sent.Provider, 2, "the start frame goes ahead of the first audio")
	assert.False(t, sent.IsProviderBinary(0))
	assert.True(t, sent.IsProviderBinary(1))
	assert.Equal(t, "pcm_s16le", gjson.GetBytes(sent.Provider[0], "audio_format").String())
	assert.Equal(t, int64(24000), gjson.GetBytes(sent.Provider[0], "sample_rate").Int())
	assert.Equal(t, pcm, []byte(sent.Provider[1]), "audio is sent as it was decoded")

	next := toProvider(t, translator, audioEvent(t, pcm))
	require.Len(t, next.Provider, 1)
	assert.True(t, next.IsProviderBinary(0))
}

func TestSonioxTranslatorRefusesWhatSonioxCannotDo(t *testing.T) {
	translator := startedTranslator(t, sessionManual)

	for name, raw := range map[string]string{
		"an event of another kind": `{"type":"response.create"}`,
		"audio that is missing":    `{"type":"input_audio_buffer.append"}`,
		"a commit with no audio":   `{"type":"input_audio_buffer.commit"}`,
	} {
		_, err := translator.ToProvider(clientEvent(t, raw))
		assert.Error(t, err, name)
	}

	cleared := toProvider(t, translator, clientEvent(t, `{"type":"input_audio_buffer.clear"}`))
	assert.Equal(t, []string{"input_audio_buffer.cleared"}, types(cleared), "a clear with nothing to discard is accepted")

	toProvider(t, translator, audioEvent(t, []byte{1, 2}))
	_, err := translator.ToProvider(clientEvent(t, `{"type":"input_audio_buffer.clear"}`))
	require.Error(t, err, "audio already sent cannot be taken back")
	assert.Contains(t, err.Error(), "cannot discard")
}

// sessionVAD is 16 kHz PCM, which is 32 bytes of audio for each millisecond.
func TestSonioxTranslatorEndpointRetiresTheAudioItEnded(t *testing.T) {
	translator := startedTranslator(t, sessionVAD)
	toProvider(t, translator, audioEvent(t, make([]byte, 3200)))
	toClient(t, translator, wire(100, "Hi", "<end>"))

	cleared := toProvider(t, translator, clientEvent(t, `{"type":"input_audio_buffer.clear"}`))
	assert.Equal(t, []string{"input_audio_buffer.cleared"}, types(cleared), "the endpoint used the audio, so there is nothing to discard")
	_, err := translator.ToProvider(clientEvent(t, `{"type":"input_audio_buffer.commit"}`))
	require.Error(t, err, "a commit with no audio since the endpoint would be an empty item")
	assert.Contains(t, err.Error(), "empty")

	toProvider(t, translator, audioEvent(t, make([]byte, 320)))
	_, err = translator.ToProvider(clientEvent(t, `{"type":"input_audio_buffer.clear"}`))
	require.Error(t, err, "audio sent after the endpoint is not retired by it")
}

func TestSonioxTranslatorEndpointKeepsAudioStillInFlight(t *testing.T) {
	translator := startedTranslator(t, sessionVAD)
	toProvider(t, translator, audioEvent(t, make([]byte, 3200)))
	toProvider(t, translator, audioEvent(t, make([]byte, 3200)))
	toClient(t, translator, wire(100, "Hi", "<end>")) // Soniox has processed the first 100 ms of the 200 sent

	_, err := translator.ToProvider(clientEvent(t, `{"type":"input_audio_buffer.clear"}`))
	require.Error(t, err, "the second 100 ms is still in flight")
	assert.Contains(t, err.Error(), "cannot discard")
	committed := toProvider(t, translator, clientEvent(t, `{"type":"input_audio_buffer.commit"}`))
	assert.Contains(t, types(committed), "input_audio_buffer.committed", "audio still in flight can be committed")
}

// total_audio_proc_ms counts audio Soniox has already turned into the next utterance's tokens, so an endpoint
// does not use up the audio from the first token of the next utterance on.
func TestSonioxTranslatorEndpointLeavesTheNextUtterancesAudioBuffered(t *testing.T) {
	translator := startedTranslator(t, sessionVAD)
	toProvider(t, translator, audioEvent(t, make([]byte, 6400))) // 200 ms
	// The first utterance ends with all 200 ms processed, and the next one's first word starts at 180 ms.
	toClient(t, translator, wire(200, "Hi", "<end>", "~ ne"))

	_, err := translator.ToProvider(clientEvent(t, `{"type":"input_audio_buffer.clear"}`))
	require.Error(t, err, "the next utterance's audio was sent and cannot be discarded")
	assert.Contains(t, err.Error(), "cannot discard")
	committed := toProvider(t, translator, clientEvent(t, `{"type":"input_audio_buffer.commit"}`))
	assert.Contains(t, types(committed), "input_audio_buffer.committed", "the next utterance's audio can be committed")
}

// The first token of the next utterance may already be final, and its audio is just as much still to come.
func TestSonioxTranslatorEndpointLeavesAFinalTokenOfTheNextUtterancesAudioBuffered(t *testing.T) {
	translator := startedTranslator(t, sessionVAD)
	toProvider(t, translator, audioEvent(t, make([]byte, 6400))) // 200 ms
	// The first utterance ends with all 200 ms processed, and the next one's first word starts at 180 ms and is final.
	toClient(t, translator, wire(200, "Hi", "<end>", " ne"))

	_, err := translator.ToProvider(clientEvent(t, `{"type":"input_audio_buffer.clear"}`))
	require.Error(t, err, "the next utterance's audio was sent and cannot be discarded")
	assert.Contains(t, err.Error(), "cannot discard")
	committed := toProvider(t, translator, clientEvent(t, `{"type":"input_audio_buffer.commit"}`))
	assert.Contains(t, types(committed), "input_audio_buffer.committed", "the next utterance's audio can be committed")
}

// Each endpoint of a frame uses up the audio up to the start of the utterance after it, not only up to the first one's.
func TestSonioxTranslatorEachEndpointOfAFrameUsesUpAudioToItsNextUtterance(t *testing.T) {
	translator := startedTranslator(t, sessionVAD)
	toProvider(t, translator, audioEvent(t, make([]byte, 9600))) // 300 ms
	// The second utterance starts at 180 ms and the third at 300 ms, where the audio sent ends.
	toClient(t, translator, wire(300, "A", "<end>", " B", "<end>", " C"))

	cleared := toProvider(t, translator, clientEvent(t, `{"type":"input_audio_buffer.clear"}`))
	assert.Equal(t, []string{"input_audio_buffer.cleared"}, types(cleared), "the audio of the second utterance was used up by the second endpoint")
}

func TestSonioxTranslatorEndpointDoesNotTakeACommitBack(t *testing.T) {
	translator := startedTranslator(t, sessionVAD)
	toProvider(t, translator, audioEvent(t, make([]byte, 6400)))
	toProvider(t, translator, clientEvent(t, `{"type":"input_audio_buffer.commit"}`))
	toClient(t, translator, wire(50, "Hi", "<end>")) // an endpoint behind the 200 ms the commit used

	_, err := translator.ToProvider(clientEvent(t, `{"type":"input_audio_buffer.commit"}`))
	require.Error(t, err, "the commit used all the audio sent, whatever an earlier endpoint had processed")
}

// A live capture: Soniox ends the utterance itself, so the translator commits and completes the segment.
func TestSonioxTranslatorEndpointCompletesASegment(t *testing.T) {
	translator := startedTranslator(t, sessionVAD)
	var item string
	var deltas []string
	for i, upto := range []int{1, 3, 5, 7, 9} {
		translation := toClient(t, translator, wire(600+120*i, nonFinal(weatherTokens[:upto]...)...))
		require.Equal(t, []string{"conversation.item.input_audio_transcription.delta"}, types(translation), "frame %d", i)
		delta := find(t, translation, "conversation.item.input_audio_transcription.delta")
		if item == "" {
			item = delta.Get("item_id").String()
		}
		assert.Equal(t, item, delta.Get("item_id").String())
		assert.True(t, delta.Get("delta").Exists() && delta.Get("transcript").Exists(), "a delta event carries the delta and the whole partial: %s", delta.Raw)
		deltas = append(deltas, delta.Get("delta").String())
	}
	assert.Equal(t, []string{"", "", "What's the ", "", "weather, "}, deltas, "only finished words that add to what was sent are in a delta")

	final := toClient(t, translator, wire(2160, append(append([]string(nil), weatherTokens...), "<end>")...))

	assert.Equal(t, []string{
		"input_audio_buffer.committed",
		"conversation.item.input_audio_transcription.delta",
		"conversation.item.added",
		"conversation.item.input_audio_transcription.completed",
		"conversation.item.done",
	}, types(final))
	want := "What's the weather, like in Paris right now?"
	completed := find(t, final, "conversation.item.input_audio_transcription.completed")
	assert.Equal(t, want, completed.Get("transcript").String())
	assert.Equal(t, "duration", completed.Get("usage.type").String())
	assert.InDelta(t, 2.16, completed.Get("usage.seconds").Float(), 1e-9, "the audio Soniox had processed when it ended the segment")
	for _, eventType := range []string{"input_audio_buffer.committed", "conversation.item.input_audio_transcription.delta", "conversation.item.input_audio_transcription.completed"} {
		assert.Equal(t, item, find(t, final, eventType).Get("item_id").String(), eventType)
	}
	assert.Equal(t, item, find(t, final, "conversation.item.added").Get("item.id").String())
	assert.Equal(t, item, find(t, final, "conversation.item.done").Get("item.id").String())
	assert.Equal(t, want, find(t, final, "conversation.item.done").Get("item.content.0.transcript").String())
	assert.Equal(t, want, strings.Join(append(deltas, find(t, final, "conversation.item.input_audio_transcription.delta").Get("delta").String()), ""), "the deltas add up to the final text")

	finished := toClient(t, translator, json.RawMessage(`{"tokens":[],"final_audio_proc_ms":4922,"total_audio_proc_ms":4922,"finished":true}`))
	assert.Empty(t, finished.Client, "the end of the stream is not an event")
}

// A live capture: the client commits, Soniox finalizes and answers with <fin>.
func TestSonioxTranslatorCommitIsAnsweredByFin(t *testing.T) {
	translator := startedTranslator(t, sessionManual)
	toProvider(t, translator, audioEvent(t, []byte{1, 2, 3, 4}))

	commit := toProvider(t, translator, clientEvent(t, `{"type":"input_audio_buffer.commit"}`))
	require.Len(t, commit.Provider, 1)
	assert.False(t, commit.IsProviderBinary(0))
	assert.JSONEq(t, `{"type":"finalize"}`, string(commit.Provider[0]))
	committed := find(t, commit, "input_audio_buffer.committed")
	item := committed.Get("item_id").String()

	_, err := translator.ToProvider(clientEvent(t, `{"type":"input_audio_buffer.commit"}`))
	require.Error(t, err, "audio is needed for the next commit")

	partial := toClient(t, translator, wire(1680, nonFinal(weatherTokens[:11]...)...))
	assert.Equal(t, item, find(t, partial, "conversation.item.input_audio_transcription.delta").Get("item_id").String(), "the partials are the committed segment's")

	answered := toClient(t, translator, wire(1876, append(append([]string(nil), weatherTokens...), "<fin>")...))
	assert.NotContains(t, types(answered), "input_audio_buffer.committed", "the client was told already")
	completed := find(t, answered, "conversation.item.input_audio_transcription.completed")
	assert.Equal(t, item, completed.Get("item_id").String())
	assert.Equal(t, "What's the weather, like in Paris right now?", completed.Get("transcript").String())
	assert.InDelta(t, 1.876, completed.Get("usage.seconds").Float(), 1e-9)

	trailing := toClient(t, translator, wire(1922, " "))
	assert.Empty(t, trailing.Client, "the space Soniox sends after <fin> is not a segment")
}

// A live capture: two finalize messages get two <fin> tokens, the second with nothing in it.
func TestSonioxTranslatorAnswersCommitsInOrder(t *testing.T) {
	translator := startedTranslator(t, sessionManual)
	toProvider(t, translator, audioEvent(t, []byte{1, 2}))
	first := find(t, toProvider(t, translator, clientEvent(t, `{"type":"input_audio_buffer.commit"}`)), "input_audio_buffer.committed")
	toProvider(t, translator, audioEvent(t, []byte{3, 4}))
	second := find(t, toProvider(t, translator, clientEvent(t, `{"type":"input_audio_buffer.commit"}`)), "input_audio_buffer.committed")
	assert.NotEqual(t, first.Get("item_id").String(), second.Get("item_id").String())
	assert.Equal(t, first.Get("item_id").String(), second.Get("previous_item_id").String())

	one := toClient(t, translator, wire(1876, append(append([]string(nil), weatherTokens...), "<fin>")...))
	assert.Equal(t, first.Get("item_id").String(), find(t, one, "conversation.item.input_audio_transcription.completed").Get("item_id").String())

	two := toClient(t, translator, wire(1876, "<fin>"))
	completed := find(t, two, "conversation.item.input_audio_transcription.completed")
	assert.Equal(t, second.Get("item_id").String(), completed.Get("item_id").String())
	assert.Equal(t, "", completed.Get("transcript").String())
	assert.Equal(t, 0.0, completed.Get("usage.seconds").Float(), "its audio was counted with the first")
}

func TestSonioxTranslatorEndpointAnswersAWaitingCommitOnce(t *testing.T) {
	translator := startedTranslator(t, sessionVAD)
	toProvider(t, translator, audioEvent(t, []byte{1, 2}))
	item := find(t, toProvider(t, translator, clientEvent(t, `{"type":"input_audio_buffer.commit"}`)), "input_audio_buffer.committed").Get("item_id").String()

	ended := toClient(t, translator, wire(2160, append(append([]string(nil), weatherTokens...), "<end>")...))
	assert.NotContains(t, types(ended), "input_audio_buffer.committed", "the client committed it already")
	assert.Equal(t, item, find(t, ended, "conversation.item.input_audio_transcription.completed").Get("item_id").String())

	fin := toClient(t, translator, wire(2160, "<fin>"))
	assert.Empty(t, fin.Client, "the <fin> of a commit an endpoint ended only closes the commit")
}

// A finalize covers all the audio before it, so a commit owns every utterance in its window until its <fin>.
func TestSonioxTranslatorCommitOwnsItsWholeWindow(t *testing.T) {
	const completedEvent = "conversation.item.input_audio_transcription.completed"
	translator := startedTranslator(t, sessionVAD)
	items := queueCommits(t, translator, 2)

	first := findAll(toClient(t, translator, wire(1500, "Hello", "<end>", " world", "<end>", "<fin>")), completedEvent)
	require.Len(t, first, 2)
	assert.Equal(t, items[0], first[0].Get("item_id").String())
	assert.Equal(t, "Hello", first[0].Get("transcript").String())
	assert.NotContains(t, items, first[1].Get("item_id").String(), "the second utterance of a window is not the next commit's")
	assert.Equal(t, "world", first[1].Get("transcript").String())

	second := findAll(toClient(t, translator, wire(2000, "Bye", "<fin>")), completedEvent)
	require.Len(t, second, 1)
	assert.Equal(t, items[1], second[0].Get("item_id").String(), "the next commit is answered by its own <fin>")
	assert.Equal(t, "Bye", second[0].Get("transcript").String())
}

// previous_item_id means "insert after this item", so a later utterance of a commit's window is announced after the
// last item of that window, which puts it before the commits queued behind it, as it is in the audio.
func TestSonioxTranslatorLaterUtterancesFollowTheirOwnCommitsWindow(t *testing.T) {
	const committedEvent = "input_audio_buffer.committed"
	translator := startedTranslator(t, sessionVAD)
	items := queueCommits(t, translator, 2)

	announced := findAll(toClient(t, translator, wire(2000, "A", "<end>", " B", "<end>", " C", "<end>", "<fin>")), committedEvent)
	require.Len(t, announced, 2, "the second and third utterances of the first commit's window are items of their own")
	assert.Equal(t, items[0], announced[0].Get("previous_item_id").String(), "the second utterance follows the first commit, not the one queued behind it")
	assert.Equal(t, announced[0].Get("item_id").String(), announced[1].Get("previous_item_id").String(), "the third follows the second")

	toClient(t, translator, wire(2400, "Bye", "<fin>"))
	next := findAll(toClient(t, translator, wire(3000, "More", "<end>")), committedEvent)
	require.Len(t, next, 1)
	assert.Equal(t, items[1], next[0].Get("previous_item_id").String(), "a segment after both windows follows the newest commit")
}

// A commit made after the later utterances of a window follows the last of them, whether Soniox has answered the
// window's finalize yet or not, and a commit behind a newer commit still follows that commit.
func TestSonioxTranslatorACommitAfterAWindowsLastUtteranceFollowsIt(t *testing.T) {
	const committedEvent = "input_audio_buffer.committed"
	commit := func(t *testing.T, translator *sonioxTranslator) gjson.Result {
		t.Helper()
		toProvider(t, translator, audioEvent(t, []byte{1, 2}))
		return find(t, toProvider(t, translator, clientEvent(t, `{"type":"input_audio_buffer.commit"}`)), committedEvent)
	}

	t.Run("before the window's finalize", func(t *testing.T) {
		translator := startedTranslator(t, sessionVAD)
		items := queueCommits(t, translator, 1)
		announced := findAll(toClient(t, translator, wire(2000, "A", "<end>", " B", "<end>")), committedEvent)
		require.Len(t, announced, 1, "the second utterance of the commit's window is an item of its own")
		assert.Equal(t, items[0], announced[0].Get("previous_item_id").String())

		next := commit(t, translator)

		assert.Equal(t, announced[0].Get("item_id").String(), next.Get("previous_item_id").String(), "the new commit follows the window's last utterance")
	})

	t.Run("after the window's finalize", func(t *testing.T) {
		translator := startedTranslator(t, sessionVAD)
		items := queueCommits(t, translator, 1)
		announced := findAll(toClient(t, translator, wire(2000, "A", "<end>", " B", "<fin>")), committedEvent)
		require.Len(t, announced, 1, "the text after the endpoint is an item of its own")
		assert.Equal(t, items[0], announced[0].Get("previous_item_id").String())

		next := commit(t, translator)

		assert.Equal(t, announced[0].Get("item_id").String(), next.Get("previous_item_id").String(), "the new commit follows the window's last utterance")
	})

	t.Run("behind a newer commit it still follows that commit", func(t *testing.T) {
		translator := startedTranslator(t, sessionVAD)
		items := queueCommits(t, translator, 2)
		toClient(t, translator, wire(2000, "A", "<end>", " B", "<end>")) // B belongs to the first commit's window

		next := commit(t, translator)

		assert.Equal(t, items[1], next.Get("previous_item_id").String(), "the newest window is the second commit's")
	})
}

func TestSonioxTranslatorPartialsOfALaterUtteranceAreNotTheNextCommits(t *testing.T) {
	const deltaEvent = "conversation.item.input_audio_transcription.delta"
	translator := startedTranslator(t, sessionVAD)
	items := queueCommits(t, translator, 2)

	deltas := findAll(toClient(t, translator, wire(900, "Hello", "<end>", "~ wor")), deltaEvent)
	require.NotEmpty(t, deltas)
	streaming := deltas[len(deltas)-1].Get("item_id").String()
	assert.NotContains(t, items, streaming, "the partials after the commit's endpoint are another segment")

	done := findAll(toClient(t, translator, wire(1500, " world", "<end>", "<fin>")), "conversation.item.input_audio_transcription.completed")
	require.Len(t, done, 1)
	assert.Equal(t, streaming, done[0].Get("item_id").String())
	assert.Equal(t, "world", done[0].Get("transcript").String())
}

func TestSonioxTranslatorFinCompletesTheTextAfterTheLastEndpoint(t *testing.T) {
	const completedEvent = "conversation.item.input_audio_transcription.completed"
	translator := startedTranslator(t, sessionVAD)
	items := queueCommits(t, translator, 2)

	first := findAll(toClient(t, translator, wire(1500, "Hello", "<end>", " world", "<fin>")), completedEvent)
	require.Len(t, first, 2, "the text after the last endpoint ends with the <fin>, and is not carried to the next commit")
	assert.Equal(t, items[0], first[0].Get("item_id").String())
	assert.Equal(t, "world", first[1].Get("transcript").String())
	assert.NotContains(t, items, first[1].Get("item_id").String())

	second := findAll(toClient(t, translator, wire(2000, "Bye", "<fin>")), completedEvent)
	require.Len(t, second, 1)
	assert.Equal(t, items[1], second[0].Get("item_id").String())
	assert.Equal(t, "Bye", second[0].Get("transcript").String())
}

func TestSonioxTranslatorCommitBehindALaterUtteranceIsANewItem(t *testing.T) {
	const deltaEvent = "conversation.item.input_audio_transcription.delta"
	translator := startedTranslator(t, sessionVAD)
	toProvider(t, translator, audioEvent(t, []byte{1, 2}))
	first := find(t, toProvider(t, translator, clientEvent(t, `{"type":"input_audio_buffer.commit"}`)), "input_audio_buffer.committed").Get("item_id").String()
	deltas := findAll(toClient(t, translator, wire(900, "Hello", "<end>", "~ wor")), deltaEvent)
	require.NotEmpty(t, deltas)
	streaming := deltas[len(deltas)-1].Get("item_id").String()

	toProvider(t, translator, audioEvent(t, []byte{3, 4}))
	second := find(t, toProvider(t, translator, clientEvent(t, `{"type":"input_audio_buffer.commit"}`)), "input_audio_buffer.committed").Get("item_id").String()

	assert.NotEqual(t, streaming, second, "the commit's audio is not the utterance Soniox is still transcribing")
	assert.NotEqual(t, first, second)
	done := findAll(toClient(t, translator, wire(1500, " world", "<end>", "<fin>")), "conversation.item.input_audio_transcription.completed")
	require.Len(t, done, 1)
	assert.Equal(t, streaming, done[0].Get("item_id").String())
	answered := findAll(toClient(t, translator, wire(2000, "Bye", "<fin>")), "conversation.item.input_audio_transcription.completed")
	require.Len(t, answered, 1)
	assert.Equal(t, second, answered[0].Get("item_id").String())
}

func TestSonioxTranslatorKeepsPartialsThroughAnEmptyFrame(t *testing.T) {
	translator := startedTranslator(t, sessionVAD)
	first := toClient(t, translator, wire(840, nonFinal(weatherTokens[:4]...)...))
	require.Len(t, first.Client, 1)

	progress := toClient(t, translator, json.RawMessage(`{"tokens":[],"final_audio_proc_ms":0,"total_audio_proc_ms":900}`))
	assert.Empty(t, progress.Client, "the answer to a keepalive does not clear the partial")

	same := toClient(t, translator, wire(960, nonFinal(weatherTokens[:4]...)...))
	assert.Empty(t, same.Client, "a partial the segment has shown adds nothing")
}

func TestSonioxTranslatorNeverTakesADeltaBack(t *testing.T) {
	translator := startedTranslator(t, sessionVAD)
	toClient(t, translator, wire(600, nonFinal("How", " are", " yo")...))
	revised := toClient(t, translator, wire(720, nonFinal("How", " is", " your")...))
	delta := find(t, revised, "conversation.item.input_audio_transcription.delta")
	assert.Equal(t, "", delta.Get("delta").String(), "a partial that revises what was sent adds nothing")
	assert.Equal(t, "How is your", delta.Get("transcript").String(), "but the partial itself is the revised one")

	ended := toClient(t, translator, wire(1200, "How", " is", " your", " day", "?", "<end>"))
	assert.NotContains(t, types(ended), "conversation.item.input_audio_transcription.delta", "there is no way to bring the deltas to a revised text")
	assert.Equal(t, "How is your day?", find(t, ended, "conversation.item.input_audio_transcription.completed").Get("transcript").String(), "the completed event carries the final text")
}

func TestSonioxTranslatorReportsAnErrorFrameOnce(t *testing.T) {
	translator := startedTranslator(t, sessionVAD)
	frame := json.RawMessage(`{"tokens":[],"final_audio_proc_ms":0,"total_audio_proc_ms":0,"error_code":401,"error_type":"unauthenticated","error_message":"Incorrect API key provided.","request_id":"r1"}`)

	failed := toClient(t, translator, frame)

	require.Equal(t, []string{"error"}, types(failed))
	event := find(t, failed, "error")
	assert.Equal(t, "authentication_error", event.Get("error.type").String())
	assert.Equal(t, "unauthenticated", event.Get("error.code").String())
	assert.Equal(t, "Incorrect API key provided.", event.Get("error.message").String())
	assert.Empty(t, translator.Close(1006, "going away").Client, "Soniox's error was reported, so its close is not")

	assert.Equal(t, "invalid_request_error", sonioxErrorType(400))
	assert.Equal(t, "rate_limit_exceeded", sonioxErrorType(429))
	assert.Equal(t, "insufficient_quota", sonioxErrorType(402))
	assert.Equal(t, "server_error", sonioxErrorType(408+100))
}

func TestSonioxTranslatorReportsADroppedConnection(t *testing.T) {
	translator := newSonioxTranslator("stt-rt-v5")
	assert.Empty(t, translator.Close(1000, "").Client, "a normal close is not an error")
	assert.Empty(t, translator.Close(1001, "going away").Client)

	dropped := translator.Close(1006, "connection reset")

	event := find(t, dropped, "error")
	assert.Equal(t, "soniox_close_1006", event.Get("error.code").String())
	assert.Equal(t, "connection reset", event.Get("error.message").String())
}

func TestSonioxTranslatorExtensionEvents(t *testing.T) {
	frame := json.RawMessage(`{"tokens":[
		{"text":"Hi","start_ms":60,"end_ms":120,"confidence":1,"is_final":true,"speaker":"1","language":"en"},
		{"text":" the","start_ms":120,"end_ms":180,"confidence":1,"is_final":true,"speaker":"1","language":"en"},
		{"text":"re","start_ms":180,"end_ms":240,"confidence":1,"is_final":true,"speaker":"1","language":"en"},
		{"text":"<end>","start_ms":0,"end_ms":0,"confidence":1,"is_final":true}],"final_audio_proc_ms":300,"total_audio_proc_ms":300}`)

	t.Run("a session that asked for them gets the words after the completed segment", func(t *testing.T) {
		translator := startedTranslator(t, `{"type":"session.update","session":{"bifrost":{"extensions":true},"audio":{"input":{"transcription":{"model":"stt-rt-v5","enable_speaker_diarization":true}}}}}`)
		ended := toClient(t, translator, frame)

		got := types(ended)
		require.Equal(t, "bifrost.transcription.words", got[len(got)-1])
		assert.Equal(t, "conversation.item.done", got[len(got)-2])
		words := find(t, ended, "bifrost.transcription.words")
		assert.Equal(t, find(t, ended, "conversation.item.input_audio_transcription.completed").Get("item_id").String(), words.Get("item_id").String())
		assert.Equal(t, "en", words.Get("language").String())
		assert.JSONEq(t, `[{"text":"Hi","start":0.06,"end":0.12,"speaker":"1"},{"text":"there","start":0.12,"end":0.24,"speaker":"1"}]`, words.Get("words").Raw)
	})
	t.Run("a session that did not never sees an event OpenAI does not define", func(t *testing.T) {
		translator := startedTranslator(t, sessionVAD)
		ended := toClient(t, translator, frame)

		assert.NotContains(t, types(ended), "bifrost.transcription.words")
		assert.Equal(t, "Hi there", find(t, ended, "conversation.item.input_audio_transcription.completed").Get("transcript").String())
	})
	t.Run("the option can be set after the connection started", func(t *testing.T) {
		translator := startedTranslator(t, sessionVAD)
		updated := toProvider(t, translator, clientEvent(t, `{"type":"session.update","session":{"bifrost":{"extensions":true}}}`))
		assert.True(t, find(t, updated, "session.updated").Get("session.bifrost.extensions").Bool())
		assert.Empty(t, updated.Provider, "it is not part of the start frame")
		assert.Contains(t, types(toClient(t, translator, frame)), "bifrost.transcription.words")
	})
}

func BenchmarkSonioxTranslatorAudioAppend(b *testing.B) {
	translator := newSonioxTranslator("stt-rt-v5")
	toProvider(b, translator, clientEvent(b, sessionVAD))
	event := audioEvent(b, make([]byte, 3200)) // 100 ms of 16 kHz PCM
	b.ReportAllocs()
	for b.Loop() {
		if _, err := translator.ToProvider(event); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSonioxTranslatorPartialFrame(b *testing.B) {
	translator := newSonioxTranslator("stt-rt-v5")
	toProvider(b, translator, clientEvent(b, sessionVAD))
	// Two frames that each change the partial, so every call reports a delta.
	frames := []json.RawMessage{wire(960, nonFinal(weatherTokens[:11]...)...), wire(1080, nonFinal(weatherTokens[:13]...)...)}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		if _, err := translator.ToClient(frames[i%2]); err != nil {
			b.Fatal(err)
		}
		i++
	}
}
