package realtime

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The model is what tells a Scribe transcription session from an agent session: Scribe's frames are
// translated to OpenAI Realtime, an agent's are relayed as they are.
func TestElevenlabs_ScribeSessionIsTranslatedAndLogged(t *testing.T) {
	for _, model := range []string{"scribe_v2_realtime", "scribe_v3_live"} {
		t.Run(model, func(t *testing.T) {
			requireGateway(t)
			t.Parallel()
			c := connect(t, dial{query: "intent=transcription"})

			c.send(sessionUpdate(map[string]any{
				"type": "transcription",
				"audio": map[string]any{"input": map[string]any{
					"transcription":  map[string]any{"model": "elevenlabs/" + model, "language": "en"},
					"turn_detection": map[string]any{"type": "server_vad", "silence_duration_ms": 600},
				}},
			}))
			c.until("session.updated")
			c.streamAudio("testdata/ask-weather.wav")
			frames := c.until("conversation.item.input_audio_transcription.completed")

			completed := frames[len(frames)-1]
			transcript := completed.Get("transcript").Str
			assert.Contains(t, strings.ToLower(transcript), "weather")
			assert.Equal(t, "duration", completed.Get("usage.type").Str, "Scribe reports no usage, so the translator reports the audio it sent")
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
			assert.Equal(t, "elevenlabs", row.Provider)
			assert.Equal(t, model, row.Model)
			assert.Equal(t, c.vk.ID, row.VirtualKeyID)
			assert.Contains(t, row.raw.Raw, transcript)
		})
	}
}

// An agent id is not a Scribe model, so its session keeps the ElevenLabs frames and close codes.
func TestElevenlabs_AgentSessionIsRelayedAsIs(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	c := connect(t, dial{query: "model=elevenlabs/agent_that_does_not_exist"})

	c.expectClosed()

	assert.Equal(t, int64(3000), c.closeCode.Load(), "ElevenLabs' own close code reaches the client")
}

// A Scribe model dialed as an agent id would fail with a confusing agent error, so it is refused.
func TestElevenlabs_ScribeModelNeedsTheTranscriptionIntent(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	c := connect(t, dial{query: "model=elevenlabs/scribe_v2_realtime"})

	c.expectError("invalid_request_error", "intent=transcription")
}

// Scribe fixes its settings when the connection opens; a session.update cannot change them.
func TestElevenlabs_ScribeSettingsCannotChangeAfterConnecting(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	c := connect(t, dial{query: "intent=transcription"})
	settings := func(language string) event {
		return sessionUpdate(map[string]any{
			"type": "transcription",
			"audio": map[string]any{"input": map[string]any{
				"transcription": map[string]any{"model": "elevenlabs/scribe_v2_realtime", "language": language},
			}},
		})
	}
	c.send(settings("en"))
	c.until("session.updated")

	c.send(settings("en"))
	c.until("session.updated") // repeating the settings is acknowledged
	c.send(settings("hi"))
	c.expectError(anyType, "fixes a session's settings when it connects")
	c.send(settings("en"))
	c.until("session.updated") // and the connection is still open
}

// The extension events are opt-in: a session that does not ask sees OpenAI event types only, and one that
// does gets the data Scribe sends after the segment as neutral Bifrost events that name the segment.
func TestElevenlabs_ScribeExtensionEventsAreOptIn(t *testing.T) {
	session := func(extensions bool) event {
		update := map[string]any{
			"type": "transcription",
			"audio": map[string]any{"input": map[string]any{
				"transcription":  map[string]any{"model": "elevenlabs/scribe_v2_realtime", "language": "en", "include_timestamps": true},
				"turn_detection": map[string]any{"type": "server_vad", "silence_duration_ms": 600},
			}},
		}
		if extensions {
			update["bifrost"] = map[string]any{"extensions": true}
		}
		return sessionUpdate(update)
	}

	t.Run("not asked for", func(t *testing.T) {
		requireGateway(t)
		t.Parallel()
		c := connect(t, dial{query: "intent=transcription"})
		c.send(session(false))
		c.until("session.updated")
		c.streamAudio("testdata/ask-weather.wav")
		c.until("conversation.item.input_audio_transcription.completed")

		for _, frame := range c.drain(2 * time.Second) {
			assert.False(t, strings.HasPrefix(frame.Get("type").Str, "bifrost."), "an extension event the session did not ask for: %s", frame.Raw)
		}
	})

	t.Run("asked for", func(t *testing.T) {
		requireGateway(t)
		t.Parallel()
		c := connect(t, dial{query: "intent=transcription"})
		c.send(session(true))
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
		confidence := event.Get("words.0.confidence").Float()
		assert.Positive(t, confidence)
		assert.LessOrEqual(t, confidence, 1.0)
		assert.False(t, event.Get("words.0.characters").Exists(), "per-character timings are not forwarded")
	})
}
