package realtime

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sonioxSession is a session.update for a transcription session on a Soniox model. The marker rides in
// client_reference_id, which Soniox carries on its start frame: the fake finds its session by it, and the real
// service takes it as a label.
func sonioxSession(model, marker string, input map[string]any, extensions bool) event {
	transcription := map[string]any{"model": "soniox/" + model, "language": "en"}
	if marker != "" {
		transcription["client_reference_id"] = marker
	}
	input["transcription"] = transcription
	update := map[string]any{"type": "transcription", "audio": map[string]any{"input": input}}
	if extensions {
		update["bifrost"] = map[string]any{"extensions": true}
	}
	return sessionUpdate(update)
}

// requireNoLeak fails if a credential the client holds reached an upstream in a header.
func requireNoLeak(t *testing.T, vk virtualKey, header map[string][]string) {
	t.Helper()
	for name, values := range header {
		for _, value := range values {
			assert.NotContains(t, value, vk.Value, "the client's virtual key leaked upstream in %s", name)
		}
	}
}

// Soniox's frames are translated to OpenAI Realtime, and the segment Soniox ends on its own is committed,
// completed with the audio it processed as duration usage, and logged as a turn.
func TestSoniox_TranscriptionSessionIsTranslatedAndLogged(t *testing.T) {
	for _, model := range []string{"stt-rt-v5", "stt-rt-v6"} {
		t.Run(model, func(t *testing.T) {
			requireUpstream(t)
			t.Parallel()
			marker := liveMarker()
			c := connect(t, dial{query: "intent=transcription"})

			c.send(sonioxSession(model, marker, map[string]any{}, false))
			c.until("session.updated")
			c.streamAudio("testdata/ask-weather.wav")
			frames := c.until("conversation.item.input_audio_transcription.completed")

			completed := frames[len(frames)-1]
			transcript := completed.Get("transcript").Str
			assert.Contains(t, strings.ToLower(transcript), "weather")
			assert.Equal(t, "duration", completed.Get("usage.type").Str, "Soniox reports no usage per segment, so the translator reports the audio it processed")
			assert.Positive(t, completed.Get("usage.seconds").Float())
			item := completed.Get("item_id").Str
			assert.Equal(t, item, findType(frames, "input_audio_buffer.committed").Get("item_id").Str)
			for _, frame := range frames {
				if frame.Get("type").Str == "conversation.item.input_audio_transcription.delta" {
					assert.Equal(t, item, frame.Get("item_id").Str)
					assert.True(t, frame.Get("delta").Exists() && frame.Get("transcript").Exists(), "a delta event carries the delta and the whole partial: %s", frame.Raw)
				}
			}

			row := waitTurnRows(t, c.vk, 1)[0]
			assert.Equal(t, "success", row.Status)
			assert.Equal(t, "soniox", row.Provider)
			assert.Equal(t, model, row.Model)
			assert.Equal(t, c.vk.ID, row.VirtualKeyID)
			assert.Contains(t, row.raw.Raw, transcript)

			if fake == nil {
				return
			}
			up := fake.soniox.WaitSTT(t, marker)
			assert.Equal(t, "Bearer "+fakeSonioxKey, up.Header("Authorization"), "the gateway's key reaches Soniox, and the client's does not")
			requireNoLeak(t, c.vk, up.header)
			assert.Equal(t, "soniox/"+model, "soniox/"+up.Start().Get("model").Str)
			assert.Positive(t, up.AudioBytes())
			assert.Equal(t, fakeTranscript, transcript)
			ends := up.SegmentEndMs()
			require.NotEmpty(t, ends)
			assert.InDelta(t, float64(ends[0])/1000, completed.Get("usage.seconds").Float(), 0.001, "the usage is the audio Soniox had processed when the segment ended")
		})
	}
}

// With endpoint detection off the client decides where a segment ends: a commit makes Soniox finalize, and
// its answer completes the segment the client committed.
func TestSoniox_ACommitEndsTheSegment(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	c := connect(t, dial{query: "intent=transcription"})

	c.send(sonioxSession("stt-rt-v5", "", map[string]any{"turn_detection": nil}, false))
	c.until("session.updated")
	<-c.streamAudio("testdata/ask-weather.wav")
	c.send(event{"type": "input_audio_buffer.commit"})
	frames := c.until("conversation.item.input_audio_transcription.completed")

	completed := frames[len(frames)-1]
	assert.Contains(t, strings.ToLower(completed.Get("transcript").Str), "weather")
	assert.Equal(t, completed.Get("item_id").Str, findType(frames, "input_audio_buffer.committed").Get("item_id").Str, "the commit the client made is the segment that completes")
}

// Soniox fixes a session's settings in its start frame; a session.update cannot change them afterwards.
func TestSoniox_SettingsCannotChangeAfterStarting(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	c := connect(t, dial{query: "intent=transcription"})
	settings := func(language string) event {
		return sessionUpdate(map[string]any{
			"type": "transcription",
			"audio": map[string]any{"input": map[string]any{
				"transcription": map[string]any{"model": "soniox/stt-rt-v5", "language": language},
			}},
		})
	}
	c.send(settings("en"))
	c.until("session.updated")

	c.send(settings("en"))
	c.until("session.updated") // repeating the settings is acknowledged
	c.send(settings("hi"))
	c.expectError(anyType, "fixes a session's settings when it starts")
	c.send(settings("en"))
	c.until("session.updated") // and the connection is still open
}

// The words event is opt-in: a session that does not ask sees OpenAI event types only, and one that does
// gets the words of each segment as a neutral Bifrost event that names the segment.
func TestSoniox_WordsEventIsOptIn(t *testing.T) {
	t.Run("not asked for", func(t *testing.T) {
		requireUpstream(t)
		t.Parallel()
		c := connect(t, dial{query: "intent=transcription"})
		c.send(sonioxSession("stt-rt-v5", "", map[string]any{}, false))
		c.until("session.updated")
		c.streamAudio("testdata/ask-weather.wav")
		c.until("conversation.item.input_audio_transcription.completed")

		for _, frame := range c.drain(2 * time.Second) {
			assert.False(t, strings.HasPrefix(frame.Get("type").Str, "bifrost."), "an extension event the session did not ask for: %s", frame.Raw)
		}
	})

	t.Run("asked for", func(t *testing.T) {
		requireUpstream(t)
		t.Parallel()
		c := connect(t, dial{query: "intent=transcription"})
		c.send(sonioxSession("stt-rt-v5", "", map[string]any{}, true))
		updated := c.until("session.updated")
		assert.True(t, updated[len(updated)-1].Get("session.bifrost.extensions").Bool(), "the session reports the option back")
		c.streamAudio("testdata/ask-weather.wav")
		frames := c.until("conversation.item.input_audio_transcription.completed")
		item := frames[len(frames)-1].Get("item_id").Str

		words := c.until("bifrost.transcription.words")
		event := words[len(words)-1]
		assert.Equal(t, item, event.Get("item_id").Str, "the event names the segment it describes")
		assert.Contains(t, strings.ToLower(event.Get("words.0.text").Str), "what")
		assert.Less(t, event.Get("words.0.start").Float(), event.Get("words.0.end").Float())
	})
}

// Soniox's asynchronous models cannot serve a realtime session, so one is refused with the reason.
func TestSoniox_AsyncModelIsRefusedForRealtime(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	c := connect(t, dial{query: "intent=transcription"})

	c.send(sonioxSession("stt-async-v6", "", map[string]any{}, false))

	c.expectError(anyType, "stt-rt")
}

// The settings of a session.update become Soniox's start frame, which fixes them for the connection.
func TestSoniox_SessionUpdateBecomesTheStartFrame(t *testing.T) {
	requireFake(t)
	cases := []struct {
		name          string
		input         map[string]any
		transcription map[string]any
		want          map[string]any // fields of the start frame, by path
		absent        []string
		reported      string // the turn detection type session.updated reports
	}{
		{
			name:  "defaults",
			input: map[string]any{},
			want: map[string]any{"audio_format": "pcm_s16le", "sample_rate": 24000, "num_channels": 1,
				"enable_endpoint_detection": true, "language_hints.0": "en"},
			reported: "server_vad",
		},
		{
			name:     "16 kHz audio and a semantic turn detection with a silence",
			input:    map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 16000}, "turn_detection": map[string]any{"type": "semantic_vad", "silence_duration_ms": 800}},
			want:     map[string]any{"sample_rate": 16000, "enable_endpoint_detection": true, "max_endpoint_delay_ms": 800},
			reported: "semantic_vad",
		},
		{
			name:   "a null turn detection hands segments to the client",
			input:  map[string]any{"turn_detection": nil},
			absent: []string{"enable_endpoint_detection", "max_endpoint_delay_ms"},
		},
		{
			name:  "mu-law telephony audio",
			input: map[string]any{"format": map[string]any{"type": "audio/pcmu"}},
			want:  map[string]any{"audio_format": "mulaw", "sample_rate": 8000},
		},
		{
			name:  "hints, context and diarization",
			input: map[string]any{},
			transcription: map[string]any{
				"language_hints": []string{"hi"}, "context": map[string]any{"text": "a weather question", "terms": []string{"Paris"}},
				"enable_speaker_diarization": true, "enable_language_identification": true,
			},
			want: map[string]any{"language_hints.0": "en", "language_hints.1": "hi", "context.text": "a weather question", "context.terms.0": "Paris",
				"enable_speaker_diarization": true, "enable_language_identification": true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			marker := liveMarker()
			c := connect(t, dial{query: "intent=transcription"})
			update := sonioxSession("stt-rt-v5", marker, tc.input, false)
			for name, value := range tc.transcription {
				update["session"].(map[string]any)["audio"].(map[string]any)["input"].(map[string]any)["transcription"].(map[string]any)[name] = value
			}

			c.send(update)
			updated := c.until("session.updated")

			start := fake.soniox.WaitSTT(t, marker).Start()
			assert.Equal(t, "stt-rt-v5", start.Get("model").Str)
			assert.Equal(t, marker, start.Get("client_reference_id").Str)
			for path, want := range tc.want {
				assert.Equal(t, fmt.Sprint(want), start.Get(path).String(), "start frame %s: %s", path, start.Raw)
			}
			for _, path := range tc.absent {
				assert.False(t, start.Get(path).Exists(), "start frame %s should be absent: %s", path, start.Raw)
			}
			if tc.reported != "" {
				assert.Equal(t, tc.reported, updated[len(updated)-1].Get("session.audio.input.turn_detection.type").Str)
			}
		})
	}
}

// When Soniox answers with an error frame the client gets an error event of the matching OpenAI type, and the
// session ends.
func TestSoniox_UpstreamErrorFrameBecomesAnErrorEvent(t *testing.T) {
	requireFake(t)
	t.Parallel()
	c := connect(t, dial{query: "intent=transcription"})
	c.send(sonioxSession("stt-rt-v5", liveMarker()+" [fake:error 402]", map[string]any{}, false))
	c.until("session.updated")

	c.streamAudio("testdata/ask-weather.wav")

	frames := c.collect("error")
	errObj := assertError(t, frames[len(frames)-1], "insufficient_quota", "fake: the upstream failed")
	assert.Equal(t, "fake_error", errObj.Get("code").Str, "the provider's own error type is the code")
	c.drain(frameTimeout)
	assert.NotZero(t, c.closeCode.Load(), "the session ends after the error")
}

// A Soniox connection that dies without a word ends the client's session with an error, not a hang.
func TestSoniox_UpstreamDropEndsTheSession(t *testing.T) {
	requireFake(t)
	t.Parallel()
	c := connect(t, dial{query: "intent=transcription"})
	c.send(sonioxSession("stt-rt-v5", liveMarker()+" [fake:drop]", map[string]any{}, false))
	c.until("session.updated")

	c.streamAudio("testdata/ask-weather.wav")

	frames := c.collect("error")
	assertError(t, frames[len(frames)-1], anyType, "")
	c.drain(frameTimeout) // a second error event names the broken stream, and then the connection closes
	assert.EqualValues(t, 1011, c.closeCode.Load())
}

// A client that hangs up takes the Soniox connection with it.
func TestSoniox_ClientLeavingClosesTheUpstreamSession(t *testing.T) {
	requireFake(t)
	t.Parallel()
	marker := liveMarker()
	c := connect(t, dial{query: "intent=transcription"})
	c.send(sonioxSession("stt-rt-v5", marker, map[string]any{}, false))
	c.until("session.updated")
	up := fake.soniox.WaitSTT(t, marker)

	c.leave()

	up.WaitEnded(t, frameTimeout)
}

// A segment is a turn against the key's limits, so a key allowed one request transcribes one segment.
func TestSoniox_RequestLimitStopsTheNextSegment(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{maxRequests: 1})
	first := connect(t, dial{query: "intent=transcription", vk: vk})
	first.send(sonioxSession("stt-rt-v5", "", map[string]any{}, false))
	first.until("session.updated")
	first.streamAudio("testdata/ask-weather.wav")
	first.until("conversation.item.input_audio_transcription.completed")
	waitTurnRows(t, vk, 1)

	second := connect(t, dial{query: "intent=transcription", vk: vk})
	second.send(sonioxSession("stt-rt-v5", "", map[string]any{}, false))
	second.until("session.updated")
	second.streamAudio("testdata/ask-weather.wav")

	frames := second.collect("error")
	assertError(t, frames[len(frames)-1], "rate_limit_exceeded", "")
}

// A spent budget refuses the segment after it. Any price at all overspends this one.
func TestSoniox_BudgetStopsTheNextSegment(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{budgetUSD: 1e-9})
	first := connect(t, dial{query: "intent=transcription", vk: vk})
	first.send(sonioxSession("stt-rt-v5", "", map[string]any{}, false))
	first.until("session.updated")
	first.streamAudio("testdata/ask-weather.wav")
	first.until("conversation.item.input_audio_transcription.completed")
	row := waitTurnRows(t, vk, 1)[0]
	require.Positive(t, row.Cost, "the model has a price, so the segment is billed")
	waitBudgetUsage(t, vk.BudgetID, row.Cost)

	second := connect(t, dial{query: "intent=transcription", vk: vk})
	second.send(sonioxSession("stt-rt-v5", "", map[string]any{}, false))
	second.until("session.updated")
	second.streamAudio("testdata/ask-weather.wav")

	frames := second.collect("error")
	assertError(t, frames[len(frames)-1], "insufficient_quota", "")
}

// The key's model allowlist is checked when a segment starts.
func TestSoniox_ModelOutsideTheKeysAllowlistGetsNoSegment(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{sonioxModels: []string{"stt-rt-v6"}})
	c := connect(t, dial{query: "intent=transcription", vk: vk})
	c.send(sonioxSession("stt-rt-v5", "", map[string]any{}, false))
	c.until("session.updated")

	c.streamAudio("testdata/ask-weather.wav")

	frames := c.collect("error")
	assertError(t, frames[len(frames)-1], anyType, "is not allowed for virtual key")
}

// Sessions on one gateway do not hear each other: each gets its own transcript, upstream connection and row.
func TestSoniox_ConcurrentSessionsStayApart(t *testing.T) {
	requireFake(t)
	t.Parallel()
	for i := range 8 {
		t.Run(fmt.Sprintf("session %d", i), func(t *testing.T) {
			t.Parallel()
			marker := liveMarker()
			c := connect(t, dial{query: "intent=transcription"})
			c.send(sonioxSession("stt-rt-v5", marker, map[string]any{}, false))
			c.until("session.updated")

			c.streamAudio("testdata/ask-weather.wav")
			frames := c.until("conversation.item.input_audio_transcription.completed")

			assert.Equal(t, fakeTranscript, frames[len(frames)-1].Get("transcript").Str)
			assert.Positive(t, fake.soniox.WaitSTT(t, marker).AudioBytes())
			rows := waitTurnRows(t, c.vk, 1)
			assert.Equal(t, c.vk.ID, rows[0].VirtualKeyID)
			assert.Contains(t, rows[0].raw.Raw, fakeTranscript, "the row holds this session's transcript")
		})
	}
}
