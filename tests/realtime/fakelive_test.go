package realtime

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

// fakeLive stands in for Google's Live API in fake mode: the Gemini Developer API endpoint. It speaks the wire
// the real service was probed to speak: JSON in binary frames, an empty serverContent first, then
// generationComplete, then turnComplete and usageMetadata in one frame. Vertex AI has no fake, because it needs
// OAuth against Google's own host and a host the gateway derives from the key's region; its tests run against
// the real service only.
//
// A session is scripted from the client's side, through its prompt text, so a test reaches the fake
// through the gateway alone. A prompt that echoes back as "echo: <prompt>" may carry directives:
//
//	[fake:slow]               one word every 100 ms, so a test can barge in
//	[fake:drop]               the TCP connection dies after the first word
//	[fake:close CODE reason]  a close frame after the first word
//	[fake:goaway]             a goAway with 1 s left before the reply, and a close once it runs out
//	[fake:silent]             no reply at all
//
// A prompt naming the weather calls the first function the setup declared; "what did I say" and "what code
// did I ask you to remember" are answered from the session's history, which a resumption handle carries to
// the next connection.
type fakeLive struct {
	ln       net.Listener
	srv      *http.Server
	mu       sync.Mutex
	sessions []*liveUpstream
	handles  map[string][]string // resumption handle -> the prompts of the turns it covers
	seq      int

	authRefusals atomic.Int64

	soniox *fakeSoniox // the Soniox sockets of the same listener
	eleven *fakeEleven // the ElevenLabs sockets of the same listener
}

const (
	fakeGeminiLivePath = "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"

	fakeGeminiKey = "fake-gemini-api-key" // the key the fake profile's gemini provider holds

	// What Google closes a session with once a goAway's time has run out: the Developer API aborts it for a
	// client that did not close first. A close reason is at most 123 bytes, so Google's is cut short, and so is
	// this one.
	fakeGeminiGoAwayCode, fakeGeminiGoAwayReason = 1008, "Connection aborted because the client failed to close the connection after receiving a GoAway signal once the session durat"
)

// liveModels are the models the fake serves; any other model closes the session as Google does.
var liveModels = map[string]bool{"gemini-3.8-live": true, "gemini-3.1-flash-live-preview": true}

// fakeUnknownModel is a model the fake profile's key lets through and the fake refuses, as Google refuses
// a model the project cannot use.
const fakeUnknownModel = "gemini-fake-unknown"

var fakeDirective = regexp.MustCompile(`\[fake:([a-z-]+)(?: ([^\]]*))?\]`)

// fakeRecall matches a question about an earlier turn, which the fake answers from the session's history.
var fakeRecall = regexp.MustCompile(`(?i)what\b.*\bdid i (say|ask you to remember)`)

var liveUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func startFakeLive(addr string) (*fakeLive, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	f := &fakeLive{ln: ln, handles: map[string][]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc(fakeGeminiLivePath, f.serve)
	f.soniox = newFakeSoniox(mux)
	f.eleven = newFakeEleven(mux)
	f.srv = &http.Server{Handler: mux}
	go func() { _ = f.srv.Serve(ln) }()
	return f, nil
}

func (f *fakeLive) Stop() {
	_ = f.srv.Close()
	f.soniox.stop()
	f.eleven.stop()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.sessions {
		if u.conn != nil {
			_ = u.conn.Close()
		}
	}
}

func (f *fakeLive) serve(w http.ResponseWriter, r *http.Request) {
	u := &liveUpstream{fake: f, header: r.Header.Clone(), query: r.URL.RawQuery, done: make(chan struct{})}
	if r.Header.Get("x-goog-api-key") != fakeGeminiKey {
		f.authRefusals.Add(1)
		http.Error(w, `{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`, http.StatusUnauthorized)
		return
	}
	conn, err := liveUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	u.conn = conn
	f.mu.Lock()
	f.seq++
	u.id = fmt.Sprintf("gemini-%d", f.seq)
	f.sessions = append(f.sessions, u)
	f.mu.Unlock()
	u.run()
}

// WaitSession returns the upstream session whose setup carries a marker, which a test puts in its
// system instruction; it fails if none connects.
func (f *fakeLive) WaitSession(t *testing.T, marker string) *liveUpstream {
	t.Helper()
	deadline := time.Now().Add(frameTimeout)
	for {
		if u := f.find(marker, 0); u != nil {
			return u
		}
		if time.Now().After(deadline) {
			t.Fatalf("no upstream live session with marker %q within %s", marker, frameTimeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// WaitNthSession is WaitSession for the nth (0-based) session carrying the marker, as a reconnect makes.
func (f *fakeLive) WaitNthSession(t *testing.T, marker string, n int) *liveUpstream {
	t.Helper()
	deadline := time.Now().Add(frameTimeout)
	for {
		if u := f.find(marker, n); u != nil {
			return u
		}
		if time.Now().After(deadline) {
			t.Fatalf("fewer than %d upstream live sessions with marker %q within %s", n+1, marker, frameTimeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// SessionCount counts the upstream sessions carrying a marker.
func (f *fakeLive) SessionCount(marker string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, u := range f.sessions {
		if u.carries(marker) {
			n++
		}
	}
	return n
}

func (f *fakeLive) find(marker string, nth int) *liveUpstream {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.sessions {
		if u.carries(marker) {
			if nth == 0 {
				return u
			}
			nth--
		}
	}
	return nil
}

// liveUsage is the token count the fake gives a turn: ten tokens and two a word in, three a word out.
type liveUsage struct{ Prompt, Output int }

func (u liveUsage) Total() int { return u.Prompt + u.Output }

// liveTurn is one turn the fake answered, for a test to compare with what the gateway logged.
type liveTurn struct {
	Prompt      string
	Reply       string
	Usage       liveUsage
	Interrupted bool
}

// liveUpstream is one session of the fake, as the gateway opened it.
type liveUpstream struct {
	fake   *fakeLive
	id     string
	header http.Header
	query  string
	conn   *websocket.Conn
	done   chan struct{} // closed when the session ends

	wmu sync.Mutex // serialises writes to conn

	mu     sync.Mutex
	setup  gjson.Result
	frames []gjson.Result // the client frames after setup, as the gateway forwarded them
	turns  []liveTurn

	// Owned by the session's loop.
	history     []string
	pending     []string
	audioIn     int
	heardSpeech bool // speech has been heard since the last answer
	silentBytes int  // silence since the speech ended
	manual      bool
	resumable   bool
	handleSeq   int
	gen         *liveGeneration
	toolWait    *liveToolWait
}

type liveGeneration struct {
	cancel chan struct{}
	done   chan struct{}
}

type liveToolWait struct {
	prompt string
	slow   bool
}

func (u *liveUpstream) carries(marker string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.setup.Exists() && strings.Contains(u.setup.Raw, marker)
}

// Setup is the setup object the gateway sent.
func (u *liveUpstream) Setup() gjson.Result {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.setup
}

// Header is a header of the upgrade request the gateway made.
func (u *liveUpstream) Header(name string) string { return u.header.Get(name) }

// Frames lists the client frames the gateway forwarded after setup.
func (u *liveUpstream) Frames() []gjson.Result {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]gjson.Result(nil), u.frames...)
}

// Turns lists the turns the fake answered.
func (u *liveUpstream) Turns() []liveTurn {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]liveTurn(nil), u.turns...)
}

// WaitTurns waits until the fake has answered n turns.
func (u *liveUpstream) WaitTurns(t *testing.T, n int) []liveTurn {
	t.Helper()
	deadline := time.Now().Add(turnTimeout)
	for {
		if turns := u.Turns(); len(turns) >= n {
			return turns
		}
		if time.Now().After(deadline) {
			t.Fatalf("upstream answered %d turns within %s, want %d", len(u.Turns()), turnTimeout, n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// WaitFrame waits for a forwarded client frame that has a field, and returns it.
func (u *liveUpstream) WaitFrame(t *testing.T, path string) gjson.Result {
	t.Helper()
	deadline := time.Now().Add(frameTimeout)
	for {
		for _, frame := range u.Frames() {
			if frame.Get(path).Exists() {
				return frame
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the gateway forwarded no frame with %q within %s; got %v", path, frameTimeout, u.Frames())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// WaitEnded waits for the session to end, as it does when the gateway closes it.
func (u *liveUpstream) WaitEnded(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-u.done:
	case <-time.After(within):
		t.Fatalf("upstream live session %s still open after %s", u.id, within)
	}
}

func (u *liveUpstream) write(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	u.wmu.Lock()
	defer u.wmu.Unlock()
	return u.conn.WriteMessage(websocket.BinaryMessage, raw)
}

func (u *liveUpstream) closeWith(code int, reason string) {
	if len(reason) > 123 { // a close frame's payload is 125 bytes, two of them the code
		reason = reason[:123]
	}
	u.wmu.Lock()
	defer u.wmu.Unlock()
	_ = u.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
	_ = u.conn.Close()
}

// run is the session's loop: the setup, then client frames, the end of speech and finished generations.
func (u *liveUpstream) run() {
	defer close(u.done)
	defer u.conn.Close()
	_, raw, err := u.conn.ReadMessage()
	if err != nil {
		return
	}
	first := gjson.ParseBytes(raw)
	if !first.Get("setup").Exists() {
		u.closeWith(1007, "Invalid first message: expected setup")
		return
	}
	u.mu.Lock()
	u.setup = first.Get("setup")
	u.mu.Unlock()
	if code, reason := u.checkModel(); code != 0 {
		u.closeWith(code, reason)
		return
	}
	u.manual = u.setup.Get("realtimeInputConfig.automaticActivityDetection.disabled").Bool()
	if resumption := u.setup.Get("sessionResumption"); resumption.Exists() {
		u.resumable = true
		u.fake.mu.Lock()
		u.history = append(u.history, u.fake.handles[resumption.Get("handle").Str]...)
		u.fake.mu.Unlock()
	}
	if u.write(map[string]any{"setupComplete": map[string]any{}}) != nil {
		return
	}

	frames := make(chan []byte)
	go func() {
		defer close(frames)
		for {
			_, raw, err := u.conn.ReadMessage()
			if err != nil {
				return
			}
			frames <- raw
		}
	}()
	for raw := range frames {
		frame := gjson.ParseBytes(raw)
		u.mu.Lock()
		u.frames = append(u.frames, frame)
		u.mu.Unlock()
		if frame.Get("realtimeInput.audio").Exists() {
			u.hear(base64Decoded(frame.Get("realtimeInput.audio.data").Str))
		}
		u.onFrame(frame)
	}
	u.stopGeneration()
}

// hear takes in a chunk of 16 kHz PCM. With automatic activity detection on, the speaker has finished once
// 700 ms of silence follows speech, as the provider's detection decides it, and the fake answers.
func (u *liveUpstream) hear(pcm []byte) {
	u.audioIn += len(pcm)
	loud := pcmIsLoud(pcm)
	switch {
	case loud:
		u.heardSpeech, u.silentBytes = true, 0
	case u.heardSpeech:
		u.silentBytes += len(pcm)
	}
	if !u.manual && u.heardSpeech && u.silentBytes >= 16000*2*700/1000 {
		u.heardSpeech, u.silentBytes = false, 0
		u.endSpeech()
	}
}

// checkModel refuses a model the fake does not serve, or one not addressed as models/<name>.
func (u *liveUpstream) checkModel() (int, string) {
	model := u.setup.Get("model").Str
	if !strings.HasPrefix(model, "models/") {
		return 1007, "Invalid model name: " + model
	}
	if !liveModels[strings.TrimPrefix(model, "models/")] {
		return 1008, "Publisher model `" + model + "` was not found or your project does not have access to it."
	}
	return 0, ""
}

func base64Decoded(s string) []byte {
	raw, _ := base64.StdEncoding.DecodeString(s)
	return raw
}

// onFrame dispatches one client frame.
func (u *liveUpstream) onFrame(frame gjson.Result) {
	switch {
	case frame.Get("toolResponse").Exists():
		u.onToolResponse(frame.Get("toolResponse"))
	case frame.Get("clientContent").Exists():
		var text []string
		for _, turn := range frame.Get("clientContent.turns").Array() {
			for _, part := range turn.Get("parts").Array() {
				if part.Get("text").Exists() {
					text = append(text, part.Get("text").Str)
				}
			}
		}
		u.pending = append(u.pending, text...)
		if frame.Get("clientContent.turnComplete").Bool() {
			prompt := strings.Join(u.pending, " ")
			u.pending = nil
			u.startTurn(prompt, false)
		}
	case frame.Get("realtimeInput.text").Exists():
		u.startTurn(frame.Get("realtimeInput.text").Str, false)
	case frame.Get("realtimeInput.activityEnd").Exists(), frame.Get("realtimeInput.audioStreamEnd").Exists():
		u.endSpeech()
	case frame.Get("realtimeInput.activityStart").Exists():
		u.interrupt()
	}
}

// endSpeech answers the audio received so far, as a model does when it hears the speaker stop.
func (u *liveUpstream) endSpeech() {
	if u.audioIn == 0 {
		return
	}
	prompt := fmt.Sprintf("heard %d bytes", u.audioIn)
	u.audioIn = 0
	u.startTurn(prompt, true)
}

type liveInput struct {
	prompt string
	audio  bool
	slow   bool
}

func parseDirectives(prompt string) (clean string, directives map[string]string) {
	directives = map[string]string{}
	clean = fakeDirective.ReplaceAllStringFunc(prompt, func(m string) string {
		sub := fakeDirective.FindStringSubmatch(m)
		directives[sub[1]] = sub[2]
		return ""
	})
	return strings.Join(strings.Fields(clean), " "), directives
}

// interrupt stops a running generation and tells the client, as a model does when the user speaks over it.
func (u *liveUpstream) interrupt() {
	if u.gen == nil {
		return
	}
	select {
	case <-u.gen.done:
		u.gen = nil
		return
	default:
	}
	u.stopGeneration()
	_ = u.write(map[string]any{"serverContent": map[string]any{"interrupted": true}})
}

func (u *liveUpstream) stopGeneration() {
	if u.gen == nil {
		return
	}
	close(u.gen.cancel)
	<-u.gen.done
	u.gen = nil
}

// startTurn begins answering a prompt. A prompt that arrives while the model is speaking interrupts it.
func (u *liveUpstream) startTurn(rawPrompt string, audio bool) {
	u.interrupt()
	prompt, directives := parseDirectives(rawPrompt)
	_, slow := directives["slow"]
	if _, silent := directives["silent"]; silent {
		return
	}
	in := liveInput{prompt: prompt, audio: audio, slow: slow}
	if function := u.setup.Get("tools.0.functionDeclarations.0.name").Str; function != "" && strings.Contains(strings.ToLower(prompt), "weather") {
		u.toolWait = &liveToolWait{prompt: prompt, slow: slow}
		_ = u.write(map[string]any{"toolCall": map[string]any{"functionCalls": []any{
			map[string]any{"id": "call-" + u.id, "name": function, "args": map[string]any{"city": "Paris"}},
		}}})
		return
	}
	reply := "echo: " + prompt
	if fakeRecall.MatchString(prompt) {
		reply = "you said nothing"
		if len(u.history) > 0 {
			reply = "you said: " + u.history[0]
		}
	}
	u.speak(in, reply, directives)
}

func (u *liveUpstream) onToolResponse(response gjson.Result) {
	if u.toolWait == nil {
		return
	}
	wait := u.toolWait
	u.toolWait = nil
	u.interrupt()
	result := response.Get("functionResponses.0.response").Raw
	u.speak(liveInput{prompt: wait.prompt, slow: wait.slow}, "tool said: "+result, nil)
}

// speak answers in the session's output modality, a word at a time, in a goroutine so that a new
// client frame can interrupt it.
func (u *liveUpstream) speak(in liveInput, reply string, directives map[string]string) {
	g := &liveGeneration{cancel: make(chan struct{}), done: make(chan struct{})}
	u.gen = g
	history := append([]string(nil), u.history...)
	u.history = append(u.history, in.prompt)
	go func() {
		defer close(g.done)
		u.deliver(g, in, reply, directives, history)
	}()
}

func (u *liveUpstream) deliver(g *liveGeneration, in liveInput, reply string, directives map[string]string, history []string) {
	audio := true
	modalities := u.setup.Get("generationConfig.responseModalities").Array()
	if len(modalities) > 0 {
		audio = false
		for _, m := range modalities {
			if strings.EqualFold(m.Str, "AUDIO") {
				audio = true
			}
		}
	}
	transcribeOut := u.setup.Get("outputAudioTranscription").Exists()
	transcribeIn := u.setup.Get("inputAudioTranscription").Exists()

	_ = u.write(map[string]any{"serverContent": map[string]any{}})
	if in.audio && transcribeIn {
		_ = u.write(map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]any{"text": in.prompt}}})
	}
	if _, ok := directives["goaway"]; ok {
		_ = u.write(map[string]any{"goAway": map[string]any{"timeLeft": "1s"}})
		time.AfterFunc(time.Second, func() { u.closeWith(fakeGeminiGoAwayCode, fakeGeminiGoAwayReason) })
	}
	words := strings.Fields(reply)
	usage := liveUsage{Prompt: 10 + 2*len(strings.Fields(in.prompt)), Output: 3 * len(words)}
	record := func(interrupted bool) {
		u.mu.Lock()
		u.turns = append(u.turns, liveTurn{Prompt: in.prompt, Reply: reply, Usage: usage, Interrupted: interrupted})
		u.mu.Unlock()
	}
	for i, word := range words {
		select {
		case <-g.cancel:
			record(true)
			return
		default:
		}
		chunk := word
		if i < len(words)-1 {
			chunk += " "
		}
		content := map[string]any{}
		if audio {
			content["modelTurn"] = map[string]any{"parts": []any{map[string]any{"inlineData": map[string]any{
				"mimeType": "audio/pcm;rate=24000", "data": base64.StdEncoding.EncodeToString(make([]byte, 480)),
			}}}}
			if transcribeOut {
				content["outputTranscription"] = map[string]any{"text": chunk}
			}
		} else {
			content["modelTurn"] = map[string]any{"parts": []any{map[string]any{"text": chunk}}}
		}
		if u.write(map[string]any{"serverContent": content}) != nil {
			return
		}
		if i == 0 {
			if _, ok := directives["drop"]; ok {
				_ = u.conn.UnderlyingConn().Close()
				return
			}
			if spec, ok := directives["close"]; ok {
				code, reason := 1011, "fake close"
				if parts := strings.SplitN(strings.TrimSpace(spec), " ", 2); len(parts) > 0 {
					if n, err := strconv.Atoi(parts[0]); err == nil {
						code = n
					}
					if len(parts) == 2 {
						reason = parts[1]
					}
				}
				u.closeWith(code, reason)
				return
			}
		}
		if in.slow {
			select {
			case <-g.cancel:
				record(true)
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	record(false)
	_ = u.write(map[string]any{"serverContent": map[string]any{"generationComplete": true}})
	_ = u.write(map[string]any{"serverContent": map[string]any{"turnComplete": true}, "usageMetadata": u.usageFrame(usage, in.audio, audio)})
	if u.resumable {
		u.handleSeq++
		handle := fmt.Sprintf("handle-%s-%d", u.id, u.handleSeq)
		u.fake.mu.Lock()
		u.fake.handles[handle] = append(append([]string(nil), history...), in.prompt)
		u.fake.mu.Unlock()
		_ = u.write(map[string]any{"sessionResumptionUpdate": map[string]any{"newHandle": handle, "resumable": true}})
	}
}

// usageFrame names the counts as the Developer API does, "response*".
func (u *liveUpstream) usageFrame(usage liveUsage, audioIn, audioOut bool) map[string]any {
	in, out := "TEXT", "TEXT"
	if audioIn {
		in = "AUDIO"
	}
	if audioOut {
		out = "AUDIO"
	}
	return map[string]any{
		"promptTokenCount":      usage.Prompt,
		"responseTokenCount":    usage.Output,
		"totalTokenCount":       usage.Total(),
		"promptTokensDetails":   []any{map[string]any{"modality": in, "tokenCount": usage.Prompt}},
		"responseTokensDetails": []any{map[string]any{"modality": out, "tokenCount": usage.Output}},
	}
}

// pcmIsLoud reports whether 16-bit little-endian PCM holds speech, which the fakes tell from silence by amplitude.
func pcmIsLoud(pcm []byte) bool {
	for i := 0; i+1 < len(pcm); i += 2 {
		if sample := int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8); sample > 500 || sample < -500 {
			return true
		}
	}
	return false
}

// fakeSocket is the server end of a WebSocket the other fakes share: writes are serialised, and a connection can
// be ended with a close frame or by dying.
type fakeSocket struct {
	conn *websocket.Conn
	wmu  sync.Mutex
}

// sendJSON writes a JSON text frame.
func (s *fakeSocket) sendJSON(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.conn.WriteMessage(websocket.TextMessage, raw)
}

// closeWith ends the connection with a close frame, whose reason is cut to the 123 bytes a frame has room for.
func (s *fakeSocket) closeWith(code int, reason string) {
	if len(reason) > 123 {
		reason = reason[:123]
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
	_ = s.conn.Close()
}

// drop kills the TCP connection without a close frame.
func (s *fakeSocket) drop() {
	_ = s.conn.UnderlyingConn().Close()
}
