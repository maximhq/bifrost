package realtime

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func sonioxSession(model string, input map[string]any, extensions bool) event {
	input["transcription"] = map[string]any{"model": "soniox/" + model, "language": "en"}
	update := map[string]any{"type": "transcription", "audio": map[string]any{"input": input}}
	if extensions {
		update["bifrost"] = map[string]any{"extensions": true}
	}
	return sessionUpdate(update)
}

// Soniox's frames are translated to OpenAI Realtime, and the segment Soniox ends on its own is committed,
// completed with the audio it processed as duration usage, and logged as a turn.
func TestSoniox_TranscriptionSessionIsTranslatedAndLogged(t *testing.T) {
	for _, model := range []string{"stt-rt-v5", "stt-rt-v6"} {
		t.Run(model, func(t *testing.T) {
			requireGateway(t)
			t.Parallel()
			c := connect(t, dial{query: "intent=transcription"})

			c.send(sonioxSession(model, map[string]any{}, false))
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
		})
	}
}

// With endpoint detection off the client decides where a segment ends: a commit makes Soniox finalize, and
// its answer completes the segment the client committed.
func TestSoniox_ACommitEndsTheSegment(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	c := connect(t, dial{query: "intent=transcription"})

	c.send(sonioxSession("stt-rt-v5", map[string]any{"turn_detection": nil}, false))
	c.until("session.updated")
	c.streamAudio("testdata/ask-weather.wav")
	time.Sleep(3 * time.Second) // the question lasts under two seconds; silence follows it
	c.send(event{"type": "input_audio_buffer.commit"})
	frames := c.until("conversation.item.input_audio_transcription.completed")

	completed := frames[len(frames)-1]
	assert.Contains(t, strings.ToLower(completed.Get("transcript").Str), "weather")
	assert.Equal(t, completed.Get("item_id").Str, findType(frames, "input_audio_buffer.committed").Get("item_id").Str, "the commit the client made is the segment that completes")
}

// Soniox fixes a session's settings in its start frame; a session.update cannot change them afterwards.
func TestSoniox_SettingsCannotChangeAfterStarting(t *testing.T) {
	requireGateway(t)
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
		requireGateway(t)
		t.Parallel()
		c := connect(t, dial{query: "intent=transcription"})
		c.send(sonioxSession("stt-rt-v5", map[string]any{}, false))
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
		c.send(sonioxSession("stt-rt-v5", map[string]any{}, true))
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
	requireGateway(t)
	t.Parallel()
	c := connect(t, dial{query: "intent=transcription"})

	c.send(sonioxSession("stt-async-v6", map[string]any{}, false))

	c.expectError(anyType, "stt-rt")
}
