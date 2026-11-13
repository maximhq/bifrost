package realtime

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Speech sessions: text streams in over /v1/audio/speech/stream and the provider's audio streams back
// while the text is still being written. A session is one request against the virtual key's limits and
// one row, and it is billed by the characters the client sent.

const speechRoute = "/v1/audio/speech/stream"

// speechProviders are the providers that serve a session, with what their config needs.
var speechProviders = map[string]struct {
	model string
	voice string
	extra map[string]any
}{
	"elevenlabs": {model: "elevenlabs/eleven_v4_turbo", voice: envOr("SPEECH_ELEVENLABS_VOICE", "21m00Tcm4TlvDq8ikWAM")},
	"soniox":     {model: "soniox/tts-rt-v2", voice: "Daniel", extra: map[string]any{"language_code": "en"}},
}

// speechClient connects and sends speech.config, and returns the client with the session.created it got.
func speechClient(t *testing.T, provider string, d dial) (*client, gjson.Result) {
	t.Helper()
	target := speechProviders[provider]
	d.route = speechRoute
	c := connect(t, d)
	c.send(speechConfig(target.model, target.voice, target.extra))
	return c, c.expect("speech.session.created")[0]
}

// untilSegmentDone reads the frames of a segment: its audio, tagged with it, up to speech.segment.done.
func untilSegmentDone(t *testing.T, c *client, segment int) (audioBytes int) {
	t.Helper()
	for _, frame := range c.until("speech.segment.done") {
		switch frame.Get("type").Str {
		case "speech.audio.delta":
			assert.EqualValues(t, segment, frame.Get("segment").Int(), "audio of another segment: %s", frame.Raw)
			audio, err := base64.StdEncoding.DecodeString(frame.Get("audio").Str)
			require.NoError(t, err, "audio is base64")
			audioBytes += len(audio)
		case "speech.segment.done":
			assert.EqualValues(t, segment, frame.Get("segment").Int())
		}
	}
	return audioBytes
}

// A flush speaks what the provider holds and closes a segment, the end closes the last one and the
// session, and the client is told how many characters it is billed for.
func TestSpeech_FlushedSegmentsAreSpokenInOrderAndBilledByCharacters(t *testing.T) {
	for provider := range speechProviders {
		t.Run(provider, func(t *testing.T) {
			requireUpstream(t)
			t.Parallel()
			c, created := speechClient(t, provider, dial{})
			assert.Equal(t, speechProviders[provider].model, created.Get("session.model").Str)
			assert.Equal(t, "pcm", created.Get("session.response_format").Str)
			assert.NotEmpty(t, created.Get("session.id").Str)
			assert.Positive(t, created.Get("session.sample_rate").Int(), "a raw format names its sample rate")

			first, second := "Hello there, this is a short test of the gateway. ", "And one more sentence, after the flush."
			c.send(speechText("Hello there, "), speechText("this is a short test of the gateway. "), speechFlush())
			assert.Positive(t, untilSegmentDone(t, c, 1))

			c.send(speechText(second), speechEnd())
			assert.Positive(t, untilSegmentDone(t, c, 2))
			done := c.until("speech.audio.done")
			assert.EqualValues(t, utf8.RuneCountInString(first+second), done[len(done)-1].Get("usage.input_chars").Int())
			c.expectClosed()
			assert.EqualValues(t, websocket.CloseNormalClosure, c.closeCode.Load())

			row := waitSpeechRows(t, c.vk, 1)[0]
			assert.Equal(t, "success", row.Status)
			assert.Equal(t, provider, row.Provider)
			assert.Equal(t, c.vk.ID, row.VirtualKeyID)
		})
	}
}

// A cancel drops what is pending and says so; nothing of the dropped segment follows, and the session
// speaks again.
func TestSpeech_ACancelIsAcknowledgedAndTheSessionKeepsSpeaking(t *testing.T) {
	for provider := range speechProviders {
		t.Run(provider, func(t *testing.T) {
			requireUpstream(t)
			t.Parallel()
			c, _ := speechClient(t, provider, dial{})

			c.send(speechText("This is a long sentence that takes a while to say out loud, so there is plenty of audio still to come when it is cancelled. "), speechFlush())
			c.until("speech.audio.delta")
			c.send(speechCancel())
			frames := c.until("speech.cancelled")
			cancelled := frames[len(frames)-1].Get("segment").Int()
			require.Positive(t, cancelled)

			c.send(speechText("Back after the cancel."), speechEnd())
			for _, frame := range c.until("speech.audio.done") {
				if frame.Get("type").Str != "speech.audio.done" {
					assert.Greater(t, frame.Get("segment").Int(), cancelled, "a frame of the cancelled segment followed the cancel: %s", frame.Raw)
				}
			}
			c.expectClosed()
			waitSpeechRows(t, c.vk, 1)
		})
	}
}

// Whatever cannot open a session is answered in-band before the provider is called, and a refused
// upgrade is a plain HTTP status.
func TestSpeech_RefusalsComeBeforeAnythingOpens(t *testing.T) {
	requireUpstream(t)
	t.Parallel()

	status, _ := refused(t, dial{route: speechRoute, anonymous: true})
	assert.Equal(t, 401, status)

	c := connect(t, dial{route: speechRoute})
	c.send(speechText("no config first"))
	c.expectError("invalid_request_error", "must be speech.config")
	c.expectClosed()

	c = connect(t, dial{route: speechRoute})
	c.send(speechConfig("openai/tts-1", "alloy", nil))
	c.expectError("invalid_request_error", "does not support incremental speech")
	c.expectClosed()

	c = connect(t, dial{route: speechRoute})
	c.send(speechConfig("elevenlabs/eleven_flash_v2_5", speechProviders["elevenlabs"].voice, nil))
	c.expectError(anyType, "eleven_flash_v2_5")
	c.expectClosed()
}

// A session is one request against the virtual key's limits, checked when it opens.
func TestSpeech_RequestLimitStopsTheNextSession(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{maxRequests: 1})
	c, _ := speechClient(t, "elevenlabs", dial{vk: vk})
	c.send(speechText("Say first."), speechEnd())
	c.until("speech.audio.done")
	waitSpeechRows(t, vk, 1)

	second := connect(t, dial{route: speechRoute, vk: vk})
	second.send(speechConfig(speechProviders["elevenlabs"].model, speechProviders["elevenlabs"].voice, nil))
	second.expectError("rate_limit_exceeded", "")
	second.expectClosed()
}

// markedSpeech connects a speech client whose session carries a marker the fake finds it by, and a directive for
// it to act on: "", "drop" or "error". ElevenLabs reads them from the voice and Soniox from client_reference_id.
func markedSpeech(t *testing.T, provider, directive string, d dial, extra map[string]any) (c *client, marker string) {
	t.Helper()
	target := speechProviders[provider]
	voice, params := target.voice, map[string]any{}
	for name, value := range target.extra {
		params[name] = value
	}
	for name, value := range extra {
		params[name] = value
	}
	if provider == "elevenlabs" {
		if fake != nil { // the real service needs a real voice, and there is no fake to find the session
			voice, marker = fakeLabel(directive)
		}
	} else {
		marker = liveMarker()
		reference := marker
		switch directive {
		case "drop":
			reference += " [fake:drop]"
		case "error":
			reference += " [fake:error 500]"
		}
		params["client_reference_id"] = reference
	}
	d.route = speechRoute
	c = connect(t, d)
	c.send(speechConfig(target.model, voice, params))
	c.expect("speech.session.created")
	return c, marker
}

// providerText is what a fake provider was sent for one session, joined: the text of every input on ElevenLabs'
// socket, and of every stream on Soniox's.
func providerText(t *testing.T, provider, marker string) string {
	t.Helper()
	if provider == "elevenlabs" {
		return strings.Join(fake.eleven.WaitTTS(t, marker, 0).Inputs(), "")
	}
	up := fake.soniox.WaitTTS(t, marker)
	var text strings.Builder
	for _, config := range up.Configs() {
		text.WriteString(up.Text(config.Get("stream_id").Str))
	}
	return text.String()
}

// Every character the client sent reaches the provider, in order, in the segments the client flushed, with the
// gateway's credentials and the settings the client named.
func TestSpeech_ProviderReceivesTheTextAndSettingsAsSent(t *testing.T) {
	for provider := range speechProviders {
		t.Run(provider, func(t *testing.T) {
			requireFake(t)
			t.Parallel()
			c, marker := markedSpeech(t, provider, "", dial{}, nil)
			first, second := "Hello there, this is a short test of the gateway. ", "And one more sentence, after the flush."

			c.send(speechText("Hello there, "), speechText("this is a short test of the gateway. "), speechFlush())
			untilSegmentDone(t, c, 1)
			c.send(speechText(second), speechEnd())
			untilSegmentDone(t, c, 2)
			c.until("speech.audio.done")

			assert.Equal(t, first+second, providerText(t, provider, marker))
			if provider == "elevenlabs" {
				up := fake.eleven.WaitTTS(t, marker, 0)
				assert.Equal(t, fakeElevenKey, up.Header("xi-api-key"))
				requireNoLeak(t, c.vk, up.header)
				assert.Equal(t, "eleven_v4_turbo", up.Query().Get("model_id"))
				assert.Equal(t, 1, up.Turns(), "the text after the flush begins a new turn; the first text of a session needs none")
				assert.Contains(t, up.Registration().Raw, marker, "the voice is registered")
				return
			}
			up := fake.soniox.WaitTTS(t, marker)
			assert.Equal(t, "Bearer "+fakeSonioxKey, up.Header("Authorization"))
			requireNoLeak(t, c.vk, up.header)
			configs := up.Configs()
			require.Len(t, configs, 2, "each flushed segment is a stream")
			assert.Equal(t, "tts-rt-v2", configs[0].Get("model").Str)
			assert.Equal(t, "Daniel", configs[0].Get("voice").Str)
			assert.Equal(t, "en", configs[0].Get("language").Str)
			assert.Equal(t, first, up.Text(configs[0].Get("stream_id").Str))
			assert.Equal(t, second, up.Text(configs[1].Get("stream_id").Str))
		})
	}
}

// With timestamps asked for, each audio frame carries the characters it speaks and when.
func TestSpeech_AlignmentIsForwardedWhenAskedFor(t *testing.T) {
	for provider := range speechProviders {
		t.Run(provider, func(t *testing.T) {
			requireFake(t)
			t.Parallel()
			c, _ := markedSpeech(t, provider, "", dial{}, map[string]any{"with_timestamps": true})
			text := "Timing of every character."

			c.send(speechText(text), speechEnd())
			var spoken strings.Builder
			for _, frame := range c.until("speech.audio.done") {
				if frame.Get("type").Str != "speech.audio.delta" {
					continue
				}
				alignment := frame.Get("alignment")
				require.True(t, alignment.Exists(), "an audio frame without its alignment: %s", frame.Raw)
				chars := alignment.Get("characters")
				if !chars.Exists() {
					chars = alignment.Get("chars")
				}
				for _, char := range chars.Array() {
					spoken.WriteString(char.Str)
				}
				assert.Len(t, alignment.Get("char_start_times_ms").Array(), len(chars.Array()))
			}
			assert.Equal(t, text, spoken.String())
		})
	}
}

// A provider that fails mid-session ends it with an error event, and the session is a row that says so.
func TestSpeech_ProviderErrorEndsTheSession(t *testing.T) {
	for provider := range speechProviders {
		t.Run(provider, func(t *testing.T) {
			requireFake(t)
			t.Parallel()
			c, _ := markedSpeech(t, provider, "error", dial{}, nil)

			c.send(speechText("Say something that fails."), speechFlush())

			frames := c.collect("error")
			assertError(t, frames[len(frames)-1], anyType, "")
			c.drain(frameTimeout)
			assert.NotZero(t, c.closeCode.Load(), "the session ends after the error")
			row := waitSpeechRows(t, c.vk, 1)[0]
			assert.Equal(t, "error", row.Status)
			assert.Equal(t, provider, row.Provider)
		})
	}
}

// A provider connection that dies without a word ends the session with an error, not a hang.
func TestSpeech_ProviderDropEndsTheSession(t *testing.T) {
	for provider := range speechProviders {
		t.Run(provider, func(t *testing.T) {
			requireFake(t)
			t.Parallel()
			c, _ := markedSpeech(t, provider, "drop", dial{}, nil)

			c.send(speechText("Say something that is cut off."), speechFlush())

			frames := c.collect("error")
			assertError(t, frames[len(frames)-1], anyType, "")
			c.drain(frameTimeout)
			assert.NotZero(t, c.closeCode.Load(), "the session ends after the drop")
			assert.Equal(t, "error", waitSpeechRows(t, c.vk, 1)[0].Status)
		})
	}
}

// Segments reach the client in order even when the provider is speaking several at once: the audio of a later
// segment waits for the earlier ones.
func TestSpeech_SegmentsStayInOrderWhenTheProviderSpeaksThemTogether(t *testing.T) {
	for provider := range speechProviders {
		t.Run(provider, func(t *testing.T) {
			requireFake(t)
			t.Parallel()
			c, _ := markedSpeech(t, provider, "", dial{}, nil)

			for i := 1; i <= 4; i++ {
				c.send(speechText(fmt.Sprintf("Segment number %d is spoken in its turn, however long it takes. ", i)), speechFlush())
			}
			c.send(speechEnd())

			var done []int64
			last := int64(0)
			for _, frame := range c.until("speech.audio.done") {
				switch frame.Get("type").Str {
				case "speech.audio.delta":
					segment := frame.Get("segment").Int()
					assert.GreaterOrEqual(t, segment, last, "audio of an earlier segment after a later one: %s", frame.Raw)
					last = segment
				case "speech.segment.done":
					done = append(done, frame.Get("segment").Int())
				}
			}
			assert.Equal(t, []int64{1, 2, 3, 4}, done)
		})
	}
}

// ElevenLabs' socket has no cancel, so a cancel closes it and the next text dials a fresh one.
func TestSpeech_ElevenlabsCancelRedialsOnTheNextText(t *testing.T) {
	requireFake(t)
	t.Parallel()
	c, marker := markedSpeech(t, "elevenlabs", "", dial{}, nil)
	c.send(speechText("This is a long sentence that takes a while to say out loud, so there is plenty of audio still to come when it is cancelled. "), speechFlush())
	c.until("speech.audio.delta")
	first := fake.eleven.WaitTTS(t, marker, 0)

	c.send(speechCancel())
	c.until("speech.cancelled")
	c.send(speechText("Back after the cancel."), speechEnd())
	c.until("speech.audio.done")

	first.WaitEnded(t, frameTimeout)
	second := fake.eleven.WaitTTS(t, marker, 1)
	assert.Equal(t, []string{"Back after the cancel."}, second.Inputs(), "only the text after the cancel goes to the new socket")
}

// A client that hangs up takes the provider connection with it.
func TestSpeech_ClientLeavingClosesTheProviderConnection(t *testing.T) {
	for provider := range speechProviders {
		t.Run(provider, func(t *testing.T) {
			requireFake(t)
			t.Parallel()
			c, marker := markedSpeech(t, provider, "", dial{}, nil)
			c.send(speechText("Say a little."), speechFlush())
			c.until("speech.audio.delta")

			c.leave()

			if provider == "elevenlabs" {
				fake.eleven.WaitTTS(t, marker, 0).WaitEnded(t, frameTimeout)
				return
			}
			fake.soniox.WaitTTS(t, marker).WaitEnded(t, frameTimeout)
		})
	}
}

// Sessions on one gateway do not hear each other: each provider connection is sent only its own client's text.
func TestSpeech_ConcurrentSessionsStayApart(t *testing.T) {
	for provider := range speechProviders {
		for i := range 8 {
			t.Run(fmt.Sprintf("%s %d", provider, i), func(t *testing.T) {
				requireFake(t)
				t.Parallel()
				c, marker := markedSpeech(t, provider, "", dial{}, nil)
				text := fmt.Sprintf("Session number %d speaks only for itself.", i)

				c.send(speechText(text), speechEnd())
				audio := untilSegmentDone(t, c, 1)

				assert.Positive(t, audio)
				done := c.until("speech.audio.done")
				assert.EqualValues(t, utf8.RuneCountInString(text), done[len(done)-1].Get("usage.input_chars").Int())
				assert.Equal(t, text, providerText(t, provider, marker))
				assert.Equal(t, c.vk.ID, waitSpeechRows(t, c.vk, 1)[0].VirtualKeyID)
			})
		}
	}
}

// A spent budget refuses the next session, which is checked when it opens. Soniox's speech model has no price in
// the datasheet yet, so only ElevenLabs' sessions are billed.
func TestSpeech_BudgetStopsTheNextSession(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	vk := createVirtualKey(t, keySpec{budgetUSD: 1e-9})
	c, _ := speechClient(t, "elevenlabs", dial{vk: vk})
	c.send(speechText("Say first."), speechEnd())
	c.until("speech.audio.done")
	row := waitSpeechRows(t, vk, 1)[0]
	require.Positive(t, row.Cost, "the model has a price, so the session is billed")
	waitBudgetUsage(t, vk.BudgetID, row.Cost)

	second := connect(t, dial{route: speechRoute, vk: vk})
	second.send(speechConfig(speechProviders["elevenlabs"].model, speechProviders["elevenlabs"].voice, nil))

	second.expectError("insufficient_quota", "")
	second.expectClosed()
}

// The key's model allowlist is checked when a session opens, before the provider is called.
func TestSpeech_ModelOutsideTheKeysAllowlistIsRefused(t *testing.T) {
	for provider, spec := range map[string]keySpec{
		"elevenlabs": {elevenModels: []string{"scribe_v2_realtime"}},
		"soniox":     {sonioxModels: []string{"stt-rt-v5"}},
	} {
		t.Run(provider, func(t *testing.T) {
			requireUpstream(t)
			t.Parallel()
			vk := createVirtualKey(t, spec)
			target := speechProviders[provider]
			c := connect(t, dial{route: speechRoute, vk: vk})

			c.send(speechConfig(target.model, target.voice, target.extra))

			c.expectError(anyType, "is not allowed for virtual key")
			c.expectClosed()
		})
	}
}

// Soniox allows five streams on a connection. A session that flushes more segments than that holds the later text
// until a stream frees, and every segment is still spoken, in order. A real account can allow fewer concurrent
// requests than that, and Soniox then refuses the stream and the session ends with its error, so only the fake
// serves this.
func TestSpeech_MoreSegmentsThanSonioxStreamsAreAllSpoken(t *testing.T) {
	requireFake(t)
	t.Parallel()
	c, _ := speechClient(t, "soniox", dial{})

	for i := 1; i <= 8; i++ {
		c.send(speechText(fmt.Sprintf("Segment %d. ", i)), speechFlush())
	}
	c.send(speechEnd())

	var done []int64
	for _, frame := range c.until("speech.audio.done") { // an error event on the way fails the test
		if frame.Get("type").Str == "speech.segment.done" {
			done = append(done, frame.Get("segment").Int())
		}
	}
	assert.Equal(t, []int64{1, 2, 3, 4, 5, 6, 7, 8}, done)
}

// A session that has nothing to say for a while is kept open: ElevenLabs closes a socket that is quiet for 20
// seconds and Soniox one that is idle for 40, so the gateway sends keep-alives and the session speaks again.
func TestSpeech_AnIdleSessionIsKeptAlive(t *testing.T) {
	for provider := range speechProviders {
		t.Run(provider, func(t *testing.T) {
			requireUpstream(t)
			t.Parallel()
			c, marker := markedSpeech(t, provider, "", dial{}, nil)
			c.send(speechText("Say a little."), speechFlush())
			untilSegmentDone(t, c, 1)

			time.Sleep(25 * time.Second)
			c.send(speechText("Still here."), speechEnd())

			untilSegmentDone(t, c, 2)
			c.until("speech.audio.done")
			if fake == nil {
				return
			}
			if provider == "elevenlabs" {
				assert.Positive(t, fake.eleven.WaitTTS(t, marker, 0).KeepAlives(), "the quiet socket was kept alive")
				return
			}
			assert.Positive(t, fake.soniox.WaitTTS(t, marker).KeepAlives(), "the quiet connection was kept alive")
		})
	}
}

// Soniox fails a stream that gets no text for a few seconds, so the session ends the stream first: the segment
// closes without a flush, and the session speaks on.
func TestSpeech_SonioxEndsAStreamThatGetsNoMoreText(t *testing.T) {
	requireUpstream(t)
	t.Parallel()
	c, _ := speechClient(t, "soniox", dial{})

	c.send(speechText("This stream is never flushed."))
	untilSegmentDone(t, c, 1)

	c.send(speechText("But the session speaks again."), speechEnd())
	untilSegmentDone(t, c, 2)
	c.until("speech.audio.done")
}
