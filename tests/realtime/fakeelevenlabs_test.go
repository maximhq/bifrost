package realtime

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

// fakeEleven stands in for ElevenLabs in fake mode, on the fake listener: Scribe's real-time transcription socket
// (/v1/speech-to-text/realtime), the text-to-dialogue socket that serves incremental speech
// (/v1/text-to-dialogue/stream-input) and the conversational agent socket (/v1/convai/conversation). Like the
// Soniox fake, it understands no speech: Scribe answers any speech with fakeTranscript, and the text socket
// answers text with silent audio.
//
// A session is scripted from the client's side. Scribe takes no free text, so its language_code carries the
// marker and directive, as "fake-<marker>" or "fake-<marker>-drop" or "fake-<marker>-error". The text socket reads
// them from its voice, in the same form, and the agent socket from its agent id, which must start with
// "fake-agent" or ElevenLabs closes the session with its own code 3000, as it does for an agent it does not know.
type fakeEleven struct {
	mu     sync.Mutex
	scribe []*elevenScribe
	tts    []*elevenTTS
	agents []*elevenAgent

	authRefusals atomic.Int64
}

const (
	fakeElevenKey    = "fake-elevenlabs-api-key" // the key the fake profile's elevenlabs provider holds
	fakeElevenTTSNew = "eleven_v4_turbo"         // the one model the text-to-dialogue socket serves
	fakeElevenChunk  = 20                        // characters of text each audio frame speaks
)

func newFakeEleven(mux *http.ServeMux) *fakeEleven {
	f := &fakeEleven{}
	mux.HandleFunc("/v1/speech-to-text/realtime", f.serveScribe)
	mux.HandleFunc("/v1/text-to-dialogue/stream-input", f.serveTTS)
	mux.HandleFunc("/v1/convai/conversation", f.serveAgent)
	return f
}

func (f *fakeEleven) stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.scribe {
		_ = s.conn.Close()
	}
	for _, s := range f.tts {
		_ = s.conn.Close()
	}
	for _, s := range f.agents {
		_ = s.conn.Close()
	}
}

func (f *fakeEleven) authorized(r *http.Request) bool {
	return r.Header.Get("xi-api-key") == fakeElevenKey
}

// elevenMarker splits "fake-<marker>-<directive>" into its marker and directive.
func elevenMarker(value string) (marker, directive string) {
	rest, ok := strings.CutPrefix(value, "fake-")
	if !ok {
		return "", ""
	}
	marker, directive, _ = strings.Cut(rest, "-")
	return marker, directive
}

// Scribe.

// elevenScribe is one Scribe real-time connection.
type elevenScribe struct {
	fakeSocket
	header http.Header
	query  url.Values
	done   chan struct{}

	mu      sync.Mutex
	audio   int // bytes of audio received
	commits int // segments committed
}

func (s *elevenScribe) carries(marker string) bool {
	got, _ := elevenMarker(s.query.Get("language_code"))
	return got != "" && got == marker
}

// Query is the query string the gateway dialled with, which carries the session's settings.
func (s *elevenScribe) Query() url.Values { return s.query }

// Header is a header of the upgrade request the gateway made.
func (s *elevenScribe) Header(name string) string { return s.header.Get(name) }

// AudioBytes is the audio the gateway has forwarded.
func (s *elevenScribe) AudioBytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.audio
}

// WaitEnded waits for the connection to end, as it does when the gateway closes it.
func (s *elevenScribe) WaitEnded(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(within):
		t.Fatalf("upstream scribe session still open after %s", within)
	}
}

// WaitScribe returns the Scribe connection whose language_code carries a marker.
func (f *fakeEleven) WaitScribe(t *testing.T, marker string) *elevenScribe {
	t.Helper()
	deadline := time.Now().Add(frameTimeout)
	for {
		f.mu.Lock()
		for _, s := range f.scribe {
			if s.carries(marker) {
				f.mu.Unlock()
				return s
			}
		}
		f.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no upstream scribe session with marker %q within %s", marker, frameTimeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *fakeEleven) serveScribe(w http.ResponseWriter, r *http.Request) {
	conn, err := liveUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s := &elevenScribe{fakeSocket: fakeSocket{conn: conn}, header: r.Header.Clone(), query: r.URL.Query(), done: make(chan struct{})}
	if !f.authorized(r) {
		f.authRefusals.Add(1)
		_ = s.sendJSON(map[string]any{"message_type": "auth_error", "error": "Invalid API key."})
		s.closeWith(websocket.ClosePolicyViolation, "")
		close(s.done)
		return
	}
	f.mu.Lock()
	f.scribe = append(f.scribe, s)
	f.mu.Unlock()
	s.run()
}

func (s *elevenScribe) run() {
	defer close(s.done)
	defer s.conn.Close()
	if model := s.query.Get("model_id"); model != "scribe_v2_realtime" && model != "scribe_v3_live" {
		_ = s.sendJSON(map[string]any{"message_type": "invalid_request", "error": "Unknown model: " + model})
		s.closeWith(websocket.ClosePolicyViolation, "")
		return
	}
	_, directive := elevenMarker(s.query.Get("language_code"))
	rate := 24000
	if format := s.query.Get("audio_format"); strings.HasPrefix(format, "pcm_") {
		rate, _ = strconv.Atoi(strings.TrimPrefix(format, "pcm_"))
	} else if strings.HasPrefix(format, "ulaw_") {
		rate = 4000 // ulaw is a byte a sample, so 8000 bytes a second is 4000 "16-bit samples"
	}
	bytesPerSecond := rate * 2
	if strings.HasPrefix(s.query.Get("audio_format"), "ulaw_") {
		bytesPerSecond = 8000
	}
	threshold := 1000
	if secs, err := strconv.ParseFloat(s.query.Get("vad_silence_threshold_secs"), 64); err == nil {
		threshold = int(secs * 1000)
	}
	strategy := s.query.Get("commit_strategy")
	timestamps := s.query.Get("include_timestamps") == "true"
	words := strings.Fields(fakeTranscript)

	_ = s.sendJSON(map[string]any{"message_type": "session_started", "session_id": "fake-scribe-session", "config": map[string]any{
		"sample_rate": rate, "audio_format": s.query.Get("audio_format"), "language_code": s.query.Get("language_code"),
		"model_id": s.query.Get("model_id"), "commit_strategy": strategy, "include_timestamps": timestamps,
	}})

	var speech, silent, shown int
	active := false
	frames := 0
	commit := func() bool {
		if !active {
			return true
		}
		active, speech, silent, shown = false, 0, 0, 0
		s.mu.Lock()
		s.commits++
		s.mu.Unlock()
		if s.sendJSON(map[string]any{"message_type": "committed_transcript", "text": fakeTranscript}) != nil {
			return false
		}
		if timestamps {
			entries := make([]map[string]any, 0, 2*len(words))
			for i, word := range words {
				if i > 0 {
					entries = append(entries, map[string]any{"text": " ", "start": float64(i) * 0.25, "end": float64(i) * 0.25, "type": "spacing"})
				}
				entries = append(entries, map[string]any{"text": word, "start": float64(i) * 0.25, "end": float64(i)*0.25 + 0.24, "type": "word", "logprob": -0.05})
			}
			return s.sendJSON(map[string]any{"message_type": "committed_transcript_with_timestamps", "text": fakeTranscript, "language_code": "en", "words": entries}) == nil
		}
		return true
	}

	for {
		_, raw, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		frame := gjson.ParseBytes(raw)
		if frame.Get("message_type").Str != "input_audio_chunk" {
			continue
		}
		pcm := base64Decoded(frame.Get("audio_base_64").Str)
		s.mu.Lock()
		s.audio += len(pcm)
		s.mu.Unlock()
		chunkMs := len(pcm) * 1000 / bytesPerSecond
		loud := pcmIsLoud(pcm)
		if loud && directive == "error" {
			_ = s.sendJSON(map[string]any{"message_type": "quota_exceeded", "error": "fake: the quota is exhausted"})
			s.closeWith(websocket.ClosePolicyViolation, "")
			return
		}
		switch {
		case loud:
			active = true
			speech += chunkMs
			silent = 0
		case active:
			silent += chunkMs
		}
		if active {
			if reveal := min(len(words), speech/fakeWordMs); reveal > shown {
				shown = reveal
				if s.sendJSON(map[string]any{"message_type": "partial_transcript", "text": strings.Join(words[:reveal], " ")}) != nil {
					return
				}
				frames++
				if directive == "drop" && frames == 1 {
					s.drop()
					return
				}
			}
		}
		committed := frame.Get("commit").Bool() || (strategy != "manual" && active && silent >= threshold)
		if committed && !commit() {
			return
		}
	}
}

// Text-to-dialogue, the socket of incremental speech.

// elevenTTS is one text-to-dialogue connection.
type elevenTTS struct {
	fakeSocket
	header http.Header
	query  url.Values
	done   chan struct{}

	mu           sync.Mutex
	voices       []string // the registration frame's voices
	registration gjson.Result
	inputs       []string // the text of each input, in order
	newTurns     int      // inputs that began a turn
	flushes      int      // flushes asked for
	flushesSent  int
	closing      bool
	pending      []rune
	spoken       int
	keepAlives   atomic.Int64 // keep-alive frames received
}

func (s *elevenTTS) carries(marker string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.voices) == 0 {
		return false
	}
	got, _ := elevenMarker(s.voices[0])
	return got != "" && got == marker
}

// Query is the query string the gateway dialled with.
func (s *elevenTTS) Query() url.Values { return s.query }

// Header is a header of the upgrade request the gateway made.
func (s *elevenTTS) Header(name string) string { return s.header.Get(name) }

// Registration is the frame that registered the session's voice.
func (s *elevenTTS) Registration() gjson.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registration
}

// Inputs lists the text of every input the gateway sent, in order.
func (s *elevenTTS) Inputs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.inputs...)
}

// KeepAlives is how many keep-alive frames the gateway has sent.
func (s *elevenTTS) KeepAlives() int { return int(s.keepAlives.Load()) }

// Turns is how many inputs began a turn.
func (s *elevenTTS) Turns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.newTurns
}

// WaitEnded waits for the connection to end.
func (s *elevenTTS) WaitEnded(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(within):
		t.Fatalf("upstream elevenlabs text-to-dialogue connection still open after %s", within)
	}
}

// WaitTTS returns the text-to-dialogue connection whose voice carries a marker. A session that was cancelled and
// spoke again has more than one, and nth picks one in the order they opened.
func (f *fakeEleven) WaitTTS(t *testing.T, marker string, nth int) *elevenTTS {
	t.Helper()
	deadline := time.Now().Add(frameTimeout)
	for {
		f.mu.Lock()
		n := nth
		for _, s := range f.tts {
			if s.carries(marker) {
				if n == 0 {
					f.mu.Unlock()
					return s
				}
				n--
			}
		}
		f.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no upstream elevenlabs text-to-dialogue connection #%d with marker %q within %s", nth, marker, frameTimeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *fakeEleven) serveTTS(w http.ResponseWriter, r *http.Request) {
	conn, err := liveUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s := &elevenTTS{fakeSocket: fakeSocket{conn: conn}, header: r.Header.Clone(), query: r.URL.Query(), done: make(chan struct{})}
	if !f.authorized(r) {
		f.authRefusals.Add(1)
		_ = s.sendJSON(map[string]any{"error": "Invalid API key.", "code": 401})
		s.closeWith(websocket.ClosePolicyViolation, "")
		close(s.done)
		return
	}
	f.mu.Lock()
	f.tts = append(f.tts, s)
	f.mu.Unlock()
	s.run()
}

func (s *elevenTTS) run() {
	defer close(s.done)
	defer s.conn.Close()
	if model := s.query.Get("model_id"); model != fakeElevenTTSNew {
		_ = s.sendJSON(map[string]any{"error": "Model " + model + " is not available on the text-to-dialogue endpoint.", "code": 1008})
		s.closeWith(websocket.ClosePolicyViolation, "")
		return
	}
	stop := make(chan struct{})
	defer close(stop)
	go s.speak(stop)
	for {
		_, raw, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		frame := gjson.ParseBytes(raw)
		s.mu.Lock()
		switch {
		case frame.Get("voices").Exists():
			s.registration = frame
			for _, voice := range frame.Get("voices").Array() {
				s.voices = append(s.voices, voice.Str)
			}
		case frame.Get("inputs").Exists():
			for _, input := range frame.Get("inputs").Array() {
				text := input.Get("text").Str
				s.inputs = append(s.inputs, text)
				if input.Get("new_turn").Bool() {
					s.newTurns++
				}
				s.pending = append(s.pending, []rune(text)...)
			}
		case frame.Get("flush").Bool():
			s.flushes++
		case frame.Get("close_socket").Bool():
			s.closing = true
		case frame.Get("keep_alive").Bool():
			s.keepAlives.Add(1)
		}
		s.mu.Unlock()
	}
}

// speak sends the audio of the text received, a frame at a time, and then the end of a turn or of the session.
func (s *elevenTTS) speak(stop <-chan struct{}) {
	ticker := time.NewTicker(60 * time.Millisecond)
	defer ticker.Stop()
	frames := 0
	alignment := s.query.Get("sync_alignment") == "true"
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		var directive string
		if len(s.voices) > 0 {
			_, directive = elevenMarker(s.voices[0])
		}
		if directive == "error" && len(s.pending) > 0 {
			_ = s.sendJSON(map[string]any{"error": "fake: this voice is not allowed", "code": 1008})
			s.closeWith(websocket.ClosePolicyViolation, "")
			s.mu.Unlock()
			return
		}
		switch {
		case len(s.pending) > 0:
			n := min(fakeElevenChunk, len(s.pending))
			spoken := s.pending[:n]
			s.pending = s.pending[n:]
			frame := map[string]any{"audio": base64.StdEncoding.EncodeToString(make([]byte, 160*n))}
			if alignment {
				chars, starts, durations := make([]string, n), make([]float64, n), make([]float64, n)
				for i, r := range spoken {
					chars[i], starts[i], durations[i] = string(r), float64(s.spoken+i)*50, 50
				}
				frame["alignment"] = map[string]any{"chars": chars, "char_start_times_ms": starts, "char_durations_ms": durations}
			}
			s.spoken += n
			_ = s.sendJSON(frame)
			frames++
			if directive == "drop" && frames == 1 {
				s.drop()
				s.mu.Unlock()
				return
			}
		case s.flushesSent < s.flushes:
			s.flushesSent++
			_ = s.sendJSON(map[string]any{"is_final_audio_for_turn": true})
		case s.closing:
			_ = s.sendJSON(map[string]any{"is_final": true})
			s.closeWith(websocket.CloseNormalClosure, "")
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}

// Conversational agents.

// elevenAgent is one conversational agent connection.
type elevenAgent struct {
	fakeSocket
	header http.Header
	agent  string
	done   chan struct{}

	mu     sync.Mutex
	frames []gjson.Result // the client frames, as the gateway forwarded them
}

func (s *elevenAgent) carries(marker string) bool { return strings.Contains(s.agent, marker) }

// Header is a header of the upgrade request the gateway made.
func (s *elevenAgent) Header(name string) string { return s.header.Get(name) }

// Frames lists the client frames the gateway forwarded.
func (s *elevenAgent) Frames() []gjson.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]gjson.Result(nil), s.frames...)
}

// WaitEnded waits for the connection to end.
func (s *elevenAgent) WaitEnded(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(within):
		t.Fatalf("upstream elevenlabs agent session still open after %s", within)
	}
}

// WaitAgent returns the agent connection whose agent id carries a marker.
func (f *fakeEleven) WaitAgent(t *testing.T, marker string) *elevenAgent {
	t.Helper()
	deadline := time.Now().Add(frameTimeout)
	for {
		f.mu.Lock()
		for _, s := range f.agents {
			if s.carries(marker) {
				f.mu.Unlock()
				return s
			}
		}
		f.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no upstream elevenlabs agent session with marker %q within %s", marker, frameTimeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *fakeEleven) serveAgent(w http.ResponseWriter, r *http.Request) {
	conn, err := liveUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s := &elevenAgent{fakeSocket: fakeSocket{conn: conn}, header: r.Header.Clone(), agent: r.URL.Query().Get("agent_id"), done: make(chan struct{})}
	defer close(s.done)
	defer conn.Close()
	if !f.authorized(r) {
		f.authRefusals.Add(1)
		s.closeWith(websocket.ClosePolicyViolation, "Invalid API key.")
		return
	}
	if !strings.HasPrefix(s.agent, "fake-agent") {
		s.closeWith(3000, "Agent "+s.agent+" not found.")
		return
	}
	f.mu.Lock()
	f.agents = append(f.agents, s)
	f.mu.Unlock()
	_ = s.sendJSON(map[string]any{"type": "conversation_initiation_metadata", "conversation_initiation_metadata_event": map[string]any{
		"conversation_id": "conv_fake", "agent_output_audio_format": "pcm_16000", "user_input_audio_format": "pcm_16000",
	}})
	for eventID := 1; ; eventID++ {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		frame := gjson.ParseBytes(raw)
		s.mu.Lock()
		s.frames = append(s.frames, frame)
		s.mu.Unlock()
		if frame.Get("type").Str == "user_message" {
			_ = s.sendJSON(map[string]any{"type": "agent_response", "agent_response_event": map[string]any{"agent_response": "echo: " + frame.Get("text").Str}})
			_ = s.sendJSON(map[string]any{"type": "audio", "audio_event": map[string]any{"audio_base_64": base64.StdEncoding.EncodeToString(make([]byte, 640)), "event_id": eventID}})
		}
	}
}

// fakeLabel returns a language code or voice that carries a fresh marker and the directive, and the marker.
func fakeLabel(directive string) (label, marker string) {
	marker = fmt.Sprintf("m%dx%d", time.Now().UnixNano(), markerSeq.Add(1))
	label = "fake-" + marker
	if directive != "" {
		label += "-" + directive
	}
	return label, marker
}
