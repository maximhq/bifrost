package realtime

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// A Gemini Live client of the gateway's native route: what the Google GenAI SDK is when its base URL is
// the gateway's /genai. It speaks Live itself, so frames are Live's, not OpenAI Realtime events.

const (
	vertexNativeV1beta1 = "/genai/ws/google.cloud.aiplatform.v1beta1.LlmBidiService/BidiGenerateContent"
	vertexNativeV1      = "/genai/ws/google.cloud.aiplatform.v1.LlmBidiService/BidiGenerateContent"
	geminiNative        = "/genai/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"

	vertexLiveModel = "gemini-3.8-live"
	geminiLiveModel = "gemini-3.1-flash-live-preview"
)

var markerSeq atomic.Int64

// liveMarker is a string unique to one session. It rides in the system instruction, which the fake reads
// to tell parallel sessions apart; a real model reads it too and ignores it.
func liveMarker() string {
	return fmt.Sprintf("rt-%d-%d", time.Now().UnixNano(), markerSeq.Add(1))
}

// liveClient is a Live client's end of a native session.
type liveClient struct {
	*client
	marker string
	handle string // the newest resumable handle in the frames read so far
}

// connectLive upgrades a client on a native route; the dial's route says which.
func connectLive(t *testing.T, d dial) *liveClient {
	t.Helper()
	return &liveClient{client: connect(t, d), marker: liveMarker()}
}

// nativeDial is a dial to a native route that presents the virtual key the way this auth method does:
// "bearer" (Authorization), "x-bf-vk", "x-goog-api-key" (what the GenAI SDK sends) or "query-key" (?key=).
func nativeDial(route, auth string, vk virtualKey) dial {
	d := dial{route: route, vk: vk}
	switch auth {
	case "bearer":
	case "x-bf-vk":
		d.anonymous, d.headers = true, map[string][]string{"X-Bf-Vk": {vk.Value}}
	case "x-goog-api-key":
		d.anonymous, d.headers = true, map[string][]string{"X-Goog-Api-Key": {vk.Value}}
	case "query-key":
		d.anonymous, d.query = true, url.Values{"key": {vk.Value}}.Encode()
	default:
		panic("unknown auth method " + auth)
	}
	return d
}

// liveSetup is the first frame of a session. The extra fields are merged into the setup object.
func liveSetup(model, marker string, extra ...map[string]any) map[string]any {
	setup := map[string]any{
		"model":                    model,
		"generationConfig":         map[string]any{"responseModalities": []string{"AUDIO"}},
		"systemInstruction":        map[string]any{"parts": []any{map[string]any{"text": "Answer in five words or fewer. Session " + marker}}},
		"inputAudioTranscription":  map[string]any{},
		"outputAudioTranscription": map[string]any{},
	}
	for _, fields := range extra {
		for name, value := range fields {
			setup[name] = value
		}
	}
	return map[string]any{"setup": setup}
}

// sendLive writes Live frames to the gateway, as text, in order.
func (c *liveClient) sendLive(frames ...map[string]any) {
	c.t.Helper()
	for _, frame := range frames {
		raw, err := json.Marshal(frame)
		require.NoError(c.t, err)
		require.NoError(c.t, c.write(websocket.TextMessage, raw), "client send")
	}
}

// sendLiveQuiet writes one Live frame and returns the error, for a background sender that outlives the test body.
func (c *liveClient) sendLiveQuiet(frame map[string]any) error {
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return c.write(websocket.TextMessage, raw)
}

// open sends a setup frame and waits for setupComplete, which it returns.
func (c *liveClient) open(model string, extra ...map[string]any) gjson.Result {
	c.t.Helper()
	c.sendLive(liveSetup(model, c.marker, extra...))
	frames := c.untilField("setupComplete")
	return frames[len(frames)-1].Get("setupComplete")
}

// untilField reads frames up to and including the first that has this field, and returns them all.
func (c *liveClient) untilField(path string) []gjson.Result {
	c.t.Helper()
	return c.untilPredicate("frame with "+path, func(frame gjson.Result) bool { return frame.Get(path).Exists() })
}

// turnDone accepts the frame that ends a turn. A model that will go on speaking ends it with IN_PROGRESS.
func turnDone(frame gjson.Result) bool {
	return frame.Get("serverContent.turnComplete").Bool() && frame.Get("serverContent.interactionStatus").Str != "IN_PROGRESS"
}

func TestLiveTurnDoneIsNotAnInterimTurnComplete(t *testing.T) {
	for frame, want := range map[string]bool{
		`{"serverContent":{"turnComplete":true}}`:                                   true,
		`{"serverContent":{"turnComplete":true,"interactionStatus":"IDLE"}}`:        true,
		`{"serverContent":{"turnComplete":true,"interactionStatus":"IN_PROGRESS"}}`: false,
		`{"serverContent":{"modelTurn":{"parts":[]}}}`:                              false,
	} {
		require.Equal(t, want, turnDone(gjson.Parse(frame)), frame)
	}
}

// A handle that arrives before the frame a wait stops at is kept, and one the session cannot resume from is not.
func TestLiveClientKeepsTheNewestResumableHandle(t *testing.T) {
	c := &liveClient{client: &client{t: t, frames: make(chan gjson.Result, 4)}}
	for _, frame := range []string{
		`{"sessionResumptionUpdate":{"newHandle":"h1","resumable":true}}`,
		`{"sessionResumptionUpdate":{"newHandle":"mid-generation","resumable":false}}`,
		`{"serverContent":{"turnComplete":true}}`,
	} {
		c.frames <- gjson.Parse(frame)
	}

	c.untilPredicate("the end of the turn", turnDone)

	require.Equal(t, "h1", c.handle)
}

// liveReply is the frames of one model turn.
type liveReply []gjson.Result

// say sends a user turn as text and returns the model's reply up to its turnComplete.
func (c *liveClient) say(text string) liveReply {
	c.t.Helper()
	c.sendLive(userTurn(text))
	return liveReply(c.untilPredicate("the end of the turn", turnDone))
}

// userTurn is a complete text turn from the user.
func userTurn(text string) map[string]any {
	return map[string]any{"clientContent": map[string]any{
		"turns":        []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": text}}}},
		"turnComplete": true,
	}}
}

// audioFrame is a chunk of 16 kHz PCM, the format Live takes.
func audioFrame(pcm []byte) map[string]any {
	return map[string]any{"realtimeInput": map[string]any{"audio": map[string]any{
		"data": base64.StdEncoding.EncodeToString(pcm), "mimeType": "audio/pcm;rate=16000",
	}}}
}

// transcript is what the model said, as its output transcription spells it.
func (r liveReply) transcript() string {
	var text strings.Builder
	for _, frame := range r {
		text.WriteString(frame.Get("serverContent.outputTranscription.text").Str)
	}
	return strings.TrimSpace(text.String())
}

// inputTranscript is what the model heard, as its input transcription spells it.
func (r liveReply) inputTranscript() string {
	var text strings.Builder
	for _, frame := range r {
		text.WriteString(frame.Get("serverContent.inputTranscription.text").Str)
	}
	return strings.TrimSpace(text.String())
}

// audioBytes counts the bytes of audio the model sent.
func (r liveReply) audioBytes() int {
	n := 0
	for _, frame := range r {
		for _, part := range frame.Get("serverContent.modelTurn.parts").Array() {
			n += len(base64Decoded(part.Get("inlineData.data").Str))
		}
	}
	return n
}

// usage is the usageMetadata of the frame that ended the turn.
func (r liveReply) usage() liveTokens {
	last := r[len(r)-1].Get("usageMetadata")
	response := last.Get("responseTokenCount").Int()
	if response == 0 {
		response = last.Get("candidatesTokenCount").Int()
	}
	return liveTokens{
		input:  int(last.Get("promptTokenCount").Int()),
		output: int(response + last.Get("thoughtsTokenCount").Int()),
	}
}

// untilAnswered reads frames until the model has spoken and its turn is complete: a turnComplete that follows
// output. Vertex ends the turn that makes a function call with a turnComplete of its own, which is not the answer.
func (c *liveClient) untilAnswered() liveReply {
	c.t.Helper()
	spoke := false
	return liveReply(c.untilPredicate("a completed answer", func(frame gjson.Result) bool {
		if frame.Get("serverContent.modelTurn").Exists() {
			spoke = true
		}
		return spoke && turnDone(frame)
	}))
}

// liveTokens is a turn's usage as the gateway logs it: thinking counts as output.
type liveTokens struct{ input, output int }

// expectCloseCode asserts the gateway closes the connection with this code, ignoring any frames before it.
func (c *liveClient) expectCloseCode(code int) {
	c.t.Helper()
	deadline := time.After(frameTimeout)
	for {
		select {
		case _, open := <-c.frames:
			if !open {
				require.EqualValues(c.t, code, c.closeCode.Load(), "close code")
				return
			}
		case <-deadline:
			c.t.Fatalf("connection still open after %s, want close code %d", frameTimeout, code)
		}
	}
}

// speech16k reads the suite's spoken question as 16 kHz PCM, the rate Live takes. The recording is 24 kHz,
// so each three samples are interpolated into two.
func speech16k(t *testing.T) []byte {
	t.Helper()
	wav, err := os.ReadFile("testdata/ask-weather.wav")
	require.NoError(t, err)
	dataAt := strings.Index(string(wav), "data")
	require.Positive(t, dataAt, "the recording has no data chunk")
	pcm := wav[dataAt+8:]
	samples := make([]int16, len(pcm)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
	}
	var out []byte
	for i := 0; i+2 < len(samples); i += 3 {
		a := int16((int(samples[i])*2 + int(samples[i+1])) / 3)
		b := int16((int(samples[i+1]) + int(samples[i+2])*2) / 3)
		out = binary.LittleEndian.AppendUint16(out, uint16(a))
		out = binary.LittleEndian.AppendUint16(out, uint16(b))
	}
	return out
}

// streamAudio plays 16 kHz PCM into the session at real-time pace and then the given silence, as an open
// microphone does; Live's activity detection needs the silence to notice the speaker has finished. It
// returns the bytes sent.
func (c *liveClient) streamAudio(pcm []byte, silence time.Duration) int {
	c.t.Helper()
	const chunkLen = 16000 * 2 * 32 / 1000
	pcm = append(append([]byte(nil), pcm...), make([]byte, int(silence/(32*time.Millisecond))*chunkLen)...)
	ticker := time.NewTicker(32 * time.Millisecond)
	defer ticker.Stop()
	for offset := 0; offset < len(pcm); offset += chunkLen {
		c.sendLive(audioFrame(pcm[offset:min(offset+chunkLen, len(pcm))]))
		<-ticker.C
	}
	return len(pcm)
}

// untilPredicate reads frames up to and including the first the predicate accepts, and returns them all.
func (c *liveClient) untilPredicate(what string, accept func(gjson.Result) bool) []gjson.Result {
	c.t.Helper()
	var got []gjson.Result
	deadline := time.After(turnTimeout)
	for {
		select {
		case frame, open := <-c.frames:
			require.True(c.t, open, "connection closed (code %d, %q) while waiting for %s after %d frames", c.closeCode.Load(), c.closeReason(), what, len(got))
			got = append(got, frame)
			if update := frame.Get("sessionResumptionUpdate"); update.Get("resumable").Bool() && update.Get("newHandle").Str != "" {
				c.handle = update.Get("newHandle").Str
			}
			if accept(frame) {
				return got
			}
		case <-deadline:
			c.t.Fatalf("no %s within %s (after %d frames)", what, turnTimeout, len(got))
		}
	}
}
