package realtime

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

// fakeSoniox stands in for Soniox in fake mode: the real-time transcription socket and the text-to-speech socket,
// on the fake listener's /transcribe-websocket and /tts-websocket. Neither understands speech. The transcription
// socket answers any speech with fakeTranscript, a word every 250 ms of it, and the TTS socket answers text with
// silent audio, a few characters at a time. Both speak the frames the provider's code was written against.
//
// A session is scripted from the client's side through client_reference_id, which Soniox carries on its start
// frame and the gateway passes through: it is also how a test finds its own session. The directives in it are
// those of fakelive_test.go:
//
//	[fake:drop]       the connection dies after the first frame of tokens or audio
//	[fake:error 402]  an error frame with that code and a close, at the first speech or text
//	[fake:silent]     no answer at all
type fakeSoniox struct {
	mu  sync.Mutex
	stt []*sonioxSTT
	tts []*sonioxTTS
	seq int

	authRefusals atomic.Int64
}

const (
	fakeSonioxKey  = "fake-soniox-api-key" // the key the fake profile's soniox provider holds
	fakeTranscript = "What's the weather like in Paris right now?"

	fakeWordMs       = 250 // speech it takes to say a word
	fakeTTSChunk     = 12  // characters of text each audio frame of the TTS socket speaks
	fakeTTSFrameWait = 50 * time.Millisecond
)

func newFakeSoniox(mux *http.ServeMux) *fakeSoniox {
	f := &fakeSoniox{}
	mux.HandleFunc("/transcribe-websocket", f.serveSTT)
	mux.HandleFunc("/tts-websocket", f.serveTTS)
	return f
}

func (f *fakeSoniox) stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.stt {
		_ = s.conn.Close()
	}
	for _, s := range f.tts {
		_ = s.conn.Close()
	}
}

func (f *fakeSoniox) authorized(r *http.Request) bool {
	return r.Header.Get("Authorization") == "Bearer "+fakeSonioxKey
}

// sonioxError is an error frame, which both sockets send before they close.
func sonioxError(code int, kind, message string) map[string]any {
	return map[string]any{"error_code": code, "error_type": kind, "error_message": message}
}

// sonioxStreamError is an error frame that names the TTS stream it is about.
func sonioxStreamError(id string, code int, kind, message string) map[string]any {
	frame := sonioxError(code, kind, message)
	frame["stream_id"] = id
	return frame
}

// WaitSTT returns the transcription session whose start frame carries a marker in client_reference_id.
func (f *fakeSoniox) WaitSTT(t *testing.T, marker string) *sonioxSTT {
	t.Helper()
	deadline := time.Now().Add(frameTimeout)
	for {
		f.mu.Lock()
		for _, s := range f.stt {
			if s.carries(marker) {
				f.mu.Unlock()
				return s
			}
		}
		f.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no upstream soniox transcription session with marker %q within %s", marker, frameTimeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// WaitTTS returns the text-to-speech connection that opened a stream with a marker in client_reference_id.
func (f *fakeSoniox) WaitTTS(t *testing.T, marker string) *sonioxTTS {
	t.Helper()
	deadline := time.Now().Add(frameTimeout)
	for {
		f.mu.Lock()
		for _, s := range f.tts {
			if s.carries(marker) {
				f.mu.Unlock()
				return s
			}
		}
		f.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no upstream soniox text-to-speech connection with marker %q within %s", marker, frameTimeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// sonioxSTT is one real-time transcription connection.
type sonioxSTT struct {
	fakeSocket
	header http.Header
	done   chan struct{}

	mu          sync.Mutex
	start       gjson.Result // the start frame
	audio       int          // bytes of audio received
	segmentEnds []int        // the audio processed, in ms, at the frame that ended each segment
	ended       bool         // the client ended the audio
}

func (s *sonioxSTT) carries(marker string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.start.Exists() && strings.Contains(s.start.Get("client_reference_id").Str, marker)
}

// Start is the start frame the gateway sent.
func (s *sonioxSTT) Start() gjson.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.start
}

// Header is a header of the upgrade request the gateway made.
func (s *sonioxSTT) Header(name string) string { return s.header.Get(name) }

// AudioBytes is the audio the gateway has forwarded.
func (s *sonioxSTT) AudioBytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.audio
}

// SegmentEndMs is the audio Soniox had processed, in ms, when each segment ended.
func (s *sonioxSTT) SegmentEndMs() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.segmentEnds...)
}

// WaitEnded waits for the connection to end, as it does when the gateway closes it.
func (s *sonioxSTT) WaitEnded(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(within):
		t.Fatalf("upstream soniox transcription session still open after %s", within)
	}
}

func (f *fakeSoniox) serveSTT(w http.ResponseWriter, r *http.Request) {
	conn, err := liveUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s := &sonioxSTT{fakeSocket: fakeSocket{conn: conn}, header: r.Header.Clone(), done: make(chan struct{})}
	if !f.authorized(r) {
		f.authRefusals.Add(1)
		_ = s.sendJSON(sonioxError(401, "unauthenticated", "Invalid API key."))
		s.closeWith(websocket.CloseNormalClosure, "")
		close(s.done)
		return
	}
	f.mu.Lock()
	f.seq++
	f.stt = append(f.stt, s)
	f.mu.Unlock()
	s.run()
}

// sonioxSegment is the speech the transcription socket is working on.
type sonioxSegment struct {
	active  bool
	startMs int // where in the audio the speech began
	speech  int // ms of speech heard
	silent  int // ms of silence since the speech
	final   int // words sent as final
	shown   int // words sent at all
}

func (s *sonioxSTT) run() {
	defer close(s.done)
	defer s.conn.Close()
	_, raw, err := s.conn.ReadMessage()
	if err != nil {
		return
	}
	start := gjson.ParseBytes(raw)
	s.mu.Lock()
	s.start = start
	s.mu.Unlock()
	if model := start.Get("model").Str; !strings.HasPrefix(model, "stt-rt-") {
		_ = s.sendJSON(sonioxError(400, "invalid_request", "Invalid model: "+model))
		s.closeWith(websocket.CloseNormalClosure, "")
		return
	}
	_, directives := parseDirectives(start.Get("client_reference_id").Str)
	bytesPerSecond := 48000
	switch start.Get("audio_format").Str {
	case "pcm_s16le":
		bytesPerSecond = int(start.Get("sample_rate").Int()) * 2
	case "mulaw", "alaw":
		bytesPerSecond = 8000
	}
	endpoints := start.Get("enable_endpoint_detection").Bool()
	delay := int(start.Get("max_endpoint_delay_ms").Int())
	if delay == 0 {
		delay = 500
	}

	words := strings.Fields(fakeTranscript)
	var segment sonioxSegment
	totalMs := 0
	frames := 0
	// send writes one frame of tokens, and carries out the directives that act on it.
	send := func(tokens []map[string]any) bool {
		frame := map[string]any{"tokens": tokens, "total_audio_proc_ms": totalMs, "final_audio_proc_ms": totalMs}
		if s.sendJSON(frame) != nil {
			return false
		}
		frames++
		if _, ok := directives["drop"]; ok && frames == 1 {
			s.drop()
			return false
		}
		return true
	}
	token := func(i int, final bool) map[string]any {
		text := words[i]
		if i > 0 {
			text = " " + text
		}
		begin := segment.startMs + i*fakeWordMs
		return map[string]any{"text": text, "start_ms": begin, "end_ms": begin + fakeWordMs - 10, "confidence": 0.98, "is_final": final}
	}
	// finish makes every word of the segment final and ends it with the special token.
	finish := func(special string) bool {
		var tokens []map[string]any
		if segment.active {
			for i := segment.final; i < len(words); i++ {
				tokens = append(tokens, token(i, true))
			}
		}
		tokens = append(tokens, map[string]any{"text": special, "is_final": true})
		segment = sonioxSegment{}
		s.mu.Lock()
		s.segmentEnds = append(s.segmentEnds, totalMs)
		s.mu.Unlock()
		return send(tokens)
	}

	for {
		messageType, message, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		if _, ok := directives["silent"]; ok {
			continue
		}
		if len(message) == 0 { // the client has no more audio
			s.mu.Lock()
			s.ended = true
			s.mu.Unlock()
			_ = s.sendJSON(map[string]any{"tokens": []any{}, "total_audio_proc_ms": totalMs, "final_audio_proc_ms": totalMs, "finished": true})
			s.closeWith(websocket.CloseNormalClosure, "")
			return
		}
		if messageType == websocket.TextMessage {
			if gjson.GetBytes(message, "type").Str == "finalize" && !finish("<fin>") {
				return
			}
			continue
		}

		s.mu.Lock()
		s.audio += len(message)
		s.mu.Unlock()
		chunkMs := len(message) * 1000 / bytesPerSecond
		loud := pcmIsLoud(message)
		totalMs += chunkMs
		if code := directives["error"]; loud && code != "" {
			var status int
			_, _ = fmt.Sscanf(code, "%d", &status)
			_ = s.sendJSON(sonioxError(status, "fake_error", "fake: the upstream failed"))
			s.closeWith(websocket.CloseNormalClosure, "")
			return
		}
		switch {
		case loud:
			if !segment.active {
				segment = sonioxSegment{active: true, startMs: totalMs - chunkMs}
			}
			segment.speech += chunkMs
			segment.silent = 0
		case segment.active:
			segment.silent += chunkMs
		}
		if segment.active {
			if reveal := min(len(words), segment.speech/fakeWordMs); reveal > segment.shown {
				var tokens []map[string]any
				for i := segment.final; i < reveal-1; i++ {
					tokens = append(tokens, token(i, true))
				}
				segment.final = max(segment.final, reveal-1)
				tokens = append(tokens, token(reveal-1, false))
				segment.shown = reveal
				if !send(tokens) {
					return
				}
			}
		}
		if endpoints && segment.active && segment.silent >= delay && !finish("<end>") {
			return
		}
	}
}

// sonioxTTS is one text-to-speech connection, which carries up to five streams.
type sonioxTTS struct {
	fakeSocket
	header http.Header
	done   chan struct{}

	mu         sync.Mutex
	streams    []*sonioxTTSStream
	configs    []gjson.Result // the config frame of each stream, in order
	texts      map[string]string
	keepAlives atomic.Int64 // keep-alive frames received
}

// KeepAlives is how many keep-alive frames the gateway has sent.
func (s *sonioxTTS) KeepAlives() int { return int(s.keepAlives.Load()) }

// sonioxTTSStream is one stream: a segment of the session.
type sonioxTTSStream struct {
	id         string
	config     gjson.Result
	pending    []rune
	spoken     int
	ended      bool
	terminated bool
}

func (s *sonioxTTS) carries(marker string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, config := range s.configs {
		if strings.Contains(config.Get("client_reference_id").Str, marker) {
			return true
		}
	}
	return false
}

// Configs lists the config frame of every stream the gateway opened.
func (s *sonioxTTS) Configs() []gjson.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]gjson.Result(nil), s.configs...)
}

// Text is the text the gateway sent on a stream.
func (s *sonioxTTS) Text(streamID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.texts[streamID]
}

// Header is a header of the upgrade request the gateway made.
func (s *sonioxTTS) Header(name string) string { return s.header.Get(name) }

// WaitEnded waits for the connection to end.
func (s *sonioxTTS) WaitEnded(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(within):
		t.Fatalf("upstream soniox text-to-speech connection still open after %s", within)
	}
}

func (f *fakeSoniox) serveTTS(w http.ResponseWriter, r *http.Request) {
	conn, err := liveUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s := &sonioxTTS{fakeSocket: fakeSocket{conn: conn}, header: r.Header.Clone(), done: make(chan struct{}), texts: map[string]string{}}
	if !f.authorized(r) {
		f.authRefusals.Add(1)
		_ = s.sendJSON(sonioxError(401, "unauthenticated", "Invalid API key."))
		s.closeWith(websocket.CloseNormalClosure, "")
		close(s.done)
		return
	}
	f.mu.Lock()
	f.seq++
	f.tts = append(f.tts, s)
	f.mu.Unlock()
	s.run()
}

func (s *sonioxTTS) run() {
	defer close(s.done)
	defer s.conn.Close()
	stop := make(chan struct{})
	defer close(stop)
	go s.speak(stop)
	for {
		_, message, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		frame := gjson.ParseBytes(message)
		if frame.Get("keep_alive").Bool() {
			s.keepAlives.Add(1)
			continue
		}
		id := frame.Get("stream_id").Str
		s.mu.Lock()
		stream := s.find(id)
		switch {
		case frame.Get("cancel").Bool():
			if stream != nil && !stream.terminated {
				stream.terminated, stream.pending = true, nil
				_ = s.sendJSON(map[string]any{"stream_id": id, "terminated": true})
			}
		case frame.Get("text").Exists():
			if stream == nil {
				_ = s.sendJSON(sonioxStreamError(id, 400, "invalid_request", "Unknown stream."))
				break
			}
			if !stream.terminated {
				text := frame.Get("text").Str
				stream.pending = append(stream.pending, []rune(text)...)
				s.texts[id] += text
				stream.ended = stream.ended || frame.Get("text_end").Bool()
			}
		case frame.Get("model").Exists():
			if open := s.openStreams(); open >= 5 {
				_ = s.sendJSON(sonioxStreamError(id, 429, "too_many_streams", "Too many open streams."))
			} else if model := frame.Get("model").Str; !strings.HasPrefix(model, "tts-rt-") {
				_ = s.sendJSON(sonioxStreamError(id, 400, "invalid_request", "Invalid model: "+model))
			} else {
				s.streams = append(s.streams, &sonioxTTSStream{id: id, config: frame})
				s.configs = append(s.configs, frame)
			}
		}
		s.mu.Unlock()
	}
}

func (s *sonioxTTS) find(id string) *sonioxTTSStream {
	for _, stream := range s.streams {
		if stream.id == id {
			return stream
		}
	}
	return nil
}

func (s *sonioxTTS) openStreams() int {
	open := 0
	for _, stream := range s.streams {
		if !stream.terminated {
			open++
		}
	}
	return open
}

// speak sends the audio of every open stream, a frame each at a time, and ends the streams whose text is all spoken.
func (s *sonioxTTS) speak(stop <-chan struct{}) {
	ticker := time.NewTicker(fakeTTSFrameWait)
	defer ticker.Stop()
	frames := 0
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		for _, stream := range s.streams {
			if stream.terminated {
				continue
			}
			_, directives := parseDirectives(stream.config.Get("client_reference_id").Str)
			if _, silent := directives["silent"]; silent {
				continue
			}
			if code := directives["error"]; code != "" && len(stream.pending) > 0 {
				var status int
				_, _ = fmt.Sscanf(code, "%d", &status)
				_ = s.sendJSON(sonioxStreamError(stream.id, status, "fake_error", "fake: the upstream failed"))
				s.closeWith(websocket.CloseNormalClosure, "")
				s.mu.Unlock()
				return
			}
			if n := min(fakeTTSChunk, len(stream.pending)); n > 0 {
				spoken := stream.pending[:n]
				stream.pending = stream.pending[n:]
				frame := map[string]any{"stream_id": stream.id, "audio": base64.StdEncoding.EncodeToString(make([]byte, 160*n))}
				if stream.config.Get("return_timestamps").Bool() {
					starts, ends, characters := make([]float64, n), make([]float64, n), make([]string, n)
					for i, r := range spoken {
						characters[i] = string(r)
						starts[i] = float64(stream.spoken+i) * 0.05
						ends[i] = starts[i] + 0.05
					}
					frame["timestamps"] = map[string]any{"characters": characters, "character_start_times_seconds": starts, "character_end_times_seconds": ends}
				}
				stream.spoken += n
				_ = s.sendJSON(frame)
				frames++
				if _, ok := directives["drop"]; ok && frames == 1 {
					s.drop()
					s.mu.Unlock()
					return
				}
				continue
			}
			if stream.ended {
				stream.terminated = true
				_ = s.sendJSON(map[string]any{"stream_id": stream.id, "audio_end": true})
				_ = s.sendJSON(map[string]any{"stream_id": stream.id, "terminated": true})
			}
		}
		s.mu.Unlock()
	}
}
