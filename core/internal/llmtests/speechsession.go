package llmtests

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	ws "github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

const speechSessionEventTimeout = 45 * time.Second

// RunSpeechSessionTest opens an incremental speech session on the provider's own WebSocket and drives
// it with text, a flush, a cancel and an end.
func RunSpeechSessionTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SpeechSession {
		t.Logf("Speech session not supported for provider %s", testConfig.Provider)
		return
	}
	if strings.TrimSpace(testConfig.SpeechSessionModel) == "" {
		t.Logf("Speech session enabled but SpeechSessionModel is not configured for provider %s; skipping", testConfig.Provider)
		return
	}

	t.Run("SpeechSession", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}
		provider, ok := client.GetProviderByKey(testConfig.Provider).(schemas.SpeechSessionProvider)
		if !ok {
			t.Skipf("provider %s does not implement SpeechSessionProvider", testConfig.Provider)
		}
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		defer bfCtx.Cancel()
		key, err := client.SelectKeyForProviderRequestType(bfCtx, schemas.SpeechStreamRequest, testConfig.Provider, testConfig.SpeechSessionModel)
		if err != nil {
			t.Fatalf("failed to select key for provider %s: %v", testConfig.Provider, err)
		}
		voice := GetProviderVoice(testConfig.Provider, "primary")
		RunSpeechSession(t, bfCtx, provider, key, &schemas.BifrostSpeechRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.SpeechSessionModel,
			Input:    &schemas.SpeechInput{},
			Params:   &schemas.SpeechParameters{VoiceConfig: &schemas.SpeechVoiceInput{Voice: &voice}, ResponseFormat: "pcm"},
		})
	})
}

// RunSpeechSession drives live sessions of a provider through the flows of the speech session protocol.
func RunSpeechSession(t *testing.T, ctx *schemas.BifrostContext, provider schemas.SpeechSessionProvider, key schemas.Key, request *schemas.BifrostSpeechRequest) {
	dial := func(dialCtx *schemas.BifrostContext, url string, headers map[string]string) (schemas.RealtimeUpstream, *schemas.BifrostError) {
		httpHeaders := http.Header{}
		for name, value := range headers {
			httpHeaders.Set(name, value)
		}
		dialer := ws.Dialer{HandshakeTimeout: 15 * time.Second}
		conn, resp, err := dialer.DialContext(dialCtx, url, httpHeaders)
		if err != nil {
			status := 502
			if resp != nil {
				status = resp.StatusCode
				resp.Body.Close()
			}
			return nil, &schemas.BifrostError{StatusCode: &status, Error: &schemas.ErrorField{Message: "failed to dial the speech session: " + err.Error()}}
		}
		return conn, nil
	}
	open := func(t *testing.T) schemas.SpeechSession {
		t.Helper()
		session, bifrostErr := provider.OpenSpeechSession(ctx, key, request, dial)
		if bifrostErr != nil {
			t.Fatalf("failed to open the speech session: %s", bifrostErr.GetErrorString())
		}
		t.Cleanup(func() { _ = session.Close() })
		return session
	}

	t.Run("FlushAndEnd", func(t *testing.T) {
		session := open(t)
		format, sampleRate := session.AudioFormat()
		t.Logf("session audio: %s at %d Hz", format, sampleRate)

		mustSend(t, session.SendText("Hello there, "))
		mustSend(t, session.SendText("this is a short test of the incremental speech session. "))
		mustSend(t, session.Flush())
		first := collectSpeechSegment(t, session, 1)
		if first.audioBytes == 0 {
			t.Fatal("the flushed segment carried no audio")
		}

		mustSend(t, session.SendText("And one more sentence after the flush."))
		mustSend(t, session.End())
		second := collectSpeechSegment(t, session, 2)
		if second.audioBytes == 0 {
			t.Fatal("the closing segment carried no audio")
		}
		expectSpeechSessionEnd(t, session)
		t.Logf("segment 1: %d bytes in %d deltas, segment 2: %d bytes in %d deltas", first.audioBytes, first.deltas, second.audioBytes, second.deltas)
	})

	t.Run("Cancel", func(t *testing.T) {
		session := open(t)
		mustSend(t, session.SendText("This is a long sentence that takes a while to say out loud, so there is plenty of audio still to come when it is cancelled. "))
		mustSend(t, session.Flush())
		// Wait for audio to start before cancelling it.
		event := nextSpeechEvent(t, session)
		if event.Type != schemas.SpeechSessionEventAudioDelta {
			t.Fatalf("expected audio first, got %s", event.Type)
		}
		mustSend(t, session.Cancel())

		var cancelled int
		for cancelled == 0 {
			event = nextSpeechEvent(t, session)
			if event.Type == schemas.SpeechSessionEventCancelled {
				cancelled = event.Segment
			}
		}
		// Nothing of the cancelled segment follows, and the session still speaks.
		mustSend(t, session.SendText("Back after the cancel."))
		mustSend(t, session.End())
		sawAudio := false
		for {
			event = nextSpeechEvent(t, session)
			if event.Type == schemas.SpeechSessionEventAudioDone {
				break
			}
			if event.Segment <= cancelled {
				t.Fatalf("%s of segment %d arrived after segment %d was cancelled", event.Type, event.Segment, cancelled)
			}
			sawAudio = sawAudio || (event.Type == schemas.SpeechSessionEventAudioDelta && event.Audio != "")
		}
		if !sawAudio {
			t.Fatal("the session carried no audio after the cancel")
		}
	})
}

func mustSend(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("speech session call failed: %v", err)
	}
}

// nextSpeechEvent waits for the session's next event.
func nextSpeechEvent(t *testing.T, session schemas.SpeechSession) *schemas.BifrostSpeechSessionEvent {
	t.Helper()
	select {
	case event, open := <-session.Events():
		if !open {
			if bifrostErr := session.Err(); bifrostErr != nil {
				t.Fatalf("the speech session failed: %s", bifrostErr.GetErrorString())
			}
			t.Fatal("the speech session ended before the event the test waited for")
		}
		return event
	case <-time.After(speechSessionEventTimeout):
		t.Fatal("timed out waiting for a speech session event")
		return nil
	}
}

type speechSegmentSummary struct {
	audioBytes int
	deltas     int
}

// collectSpeechSegment reads events until speech.segment.done for segment, checking that its audio is
// tagged with it and decodes.
func collectSpeechSegment(t *testing.T, session schemas.SpeechSession, segment int) speechSegmentSummary {
	t.Helper()
	var summary speechSegmentSummary
	for {
		event := nextSpeechEvent(t, session)
		switch event.Type {
		case schemas.SpeechSessionEventAudioDelta:
			if event.Segment != segment {
				t.Fatalf("audio of segment %d arrived while collecting segment %d", event.Segment, segment)
			}
			audio, err := base64.StdEncoding.DecodeString(event.Audio)
			if err != nil {
				t.Fatalf("audio is not base64: %v", err)
			}
			summary.audioBytes += len(audio)
			summary.deltas++
		case schemas.SpeechSessionEventSegmentDone:
			if event.Segment != segment {
				t.Fatalf("segment %d finished while collecting segment %d", event.Segment, segment)
			}
			return summary
		default:
			t.Fatalf("unexpected %s while collecting segment %d", event.Type, segment)
		}
	}
}

// expectSpeechSessionEnd reads the events that end a session: speech.audio.done, then the channel closes.
func expectSpeechSessionEnd(t *testing.T, session schemas.SpeechSession) {
	t.Helper()
	event := nextSpeechEvent(t, session)
	if event.Type != schemas.SpeechSessionEventAudioDone {
		t.Fatalf("expected %s, got %s", schemas.SpeechSessionEventAudioDone, event.Type)
	}
	select {
	case extra, open := <-session.Events():
		if open {
			t.Fatalf("event %s followed %s", extra.Type, schemas.SpeechSessionEventAudioDone)
		}
	case <-time.After(speechSessionEventTimeout):
		t.Fatal("the speech session's events stayed open after speech.audio.done")
	}
	if bifrostErr := session.Err(); bifrostErr != nil {
		t.Fatalf("the speech session reported %s after finishing", bifrostErr.GetErrorString())
	}
}
