package lib

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// TestSSEStreamReaderHeartbeatNeverSplitsDataLine is the regression test for #5905.
//
// handlePassthroughStream forwards raw upstream network reads (core's passthrough producer
// reads into a fixed 4096-byte buffer), so a chunk is an arbitrary slice of the TCP stream
// and is NOT guaranteed to end on an SSE line boundary. A large `data:` JSON value therefore
// spans many chunks. Because the heartbeat goroutine writes through the same reader, a tick
// landing between two of those chunks splices `: heartbeat\n` into the middle of a half-written
// `data:` line.
//
// Per the SSE spec (https://html.spec.whatwg.org/multipage/server-sent-events.html) a comment
// is only a comment when the colon is the FIRST character of a line -- and a comment is then
// ignored without resetting or dispatching buffers, so start-of-line is sufficient and a full
// event boundary is not required. Spliced mid-line it is not a comment at all: its `\n`
// terminates the `data:` line early, and the JSON remainder becomes an unrecognized field that
// decoders silently discard (openai-python `_streaming.py`: `pass  # Field is ignored.`),
// yielding truncated JSON and a JSONDecodeError at the exact byte offset of the split.
//
// No heartbeat frame SHAPE can fix this -- #5883's trailing-blank-line fix is orthogonal.
// The reader must refuse to emit a heartbeat unless it is at a line boundary.
func TestSSEStreamReaderHeartbeatNeverSplitsDataLine(t *testing.T) {
	// One valid SSE event whose data payload is split across two raw chunks, mirroring a
	// large response.in_progress event delivered over multiple 4096-byte reads.
	payload := `{"type":"response.in_progress","content":"` + strings.Repeat("A", 64) + `"}`
	full := "event: response.in_progress\ndata: " + payload + "\n\n"
	splitAt := len("event: response.in_progress\ndata: ") + 20 // mid-JSON, mid-line
	chunk1, chunk2 := full[:splitAt], full[splitAt:]

	r := NewSSEStreamReader()

	readDone := make(chan []byte, 1)
	go func() {
		b, err := io.ReadAll(r)
		if err != nil {
			t.Errorf("ReadAll: %v", err)
		}
		readDone <- b
	}()

	// Deterministic interleaving: the heartbeat tick lands exactly between the two chunks,
	// which is the race the production heartbeat goroutine hits nondeterministically.
	r.Send([]byte(chunk1))
	r.SendHeartbeat()
	r.Send([]byte(chunk2))
	r.Done()

	got := <-readDone

	// 1. Structural: every heartbeat emitted must start its own line.
	for idx := 0; ; {
		i := bytes.Index(got[idx:], sseHeartbeatFrame)
		if i < 0 {
			break
		}
		at := idx + i
		if at != 0 && got[at-1] != '\n' {
			t.Errorf("heartbeat spliced mid-line at offset %d; preceding byte is %q, want '\\n'.\n"+
				"SSE comments are only comments at start-of-line; here it corrupts the data: line.\n"+
				"stream:\n%s", at, got[at-1], got)
		}
		idx = at + len(sseHeartbeatFrame)
	}

	// 2. Payload integrity: stripping heartbeat lines must recover the original bytes exactly.
	stripped := bytes.ReplaceAll(got, sseHeartbeatFrame, nil)
	if string(stripped) != full {
		t.Errorf("provider bytes not forwarded 1:1 after removing heartbeats.\ngot:  %q\nwant: %q", stripped, full)
	}

	// 3. Client-observable: the data payload must still parse as JSON, which is what the
	//    reporter's OpenAI SDK client failed to do (JSONDecodeError at char 95636).
	var data string
	for _, line := range strings.Split(string(got), "\n") {
		if after, ok := strings.CutPrefix(line, "data: "); ok {
			data += after
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(data), &decoded); err != nil {
		t.Errorf("client-visible data payload is not valid JSON: %v\npayload: %q", err, data)
	}
}

// TestSSEStreamReaderHeartbeatStillSentAtLineBoundary guards against over-correcting #5905 by
// simply disabling heartbeats. Disconnect detection depends on a real write happening during
// idle gaps (see SendHeartbeat's doc and #5010), so when the stream IS at a line boundary --
// which is always the case on the typed paths, where Bifrost frames each event itself and
// every Send ends in "\n\n" -- the heartbeat must still be emitted.
func TestSSEStreamReaderHeartbeatStillSentAtLineBoundary(t *testing.T) {
	r := NewSSEStreamReader()

	readDone := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		readDone <- b
	}()

	r.Send([]byte("data: {\"a\":1}\n\n")) // complete frame: reader is at a line boundary
	if !r.SendHeartbeat() {
		t.Fatal("SendHeartbeat returned false (disconnect) on a live reader at a line boundary")
	}
	r.Send([]byte("data: {\"b\":2}\n\n"))
	r.Done()

	got := <-readDone
	if !bytes.Contains(got, sseHeartbeatFrame) {
		t.Errorf("heartbeat suppressed at a valid line boundary; disconnect detection would regress.\nstream: %q", got)
	}
}

// TestSSEStreamReaderHeartbeatBoundaryMatrix walks every way a write can leave the stream
// positioned, and pins whether a heartbeat may follow. The rule is start-of-line, NOT
// end-of-event: per the SSE spec a comment neither dispatches nor resets the event buffers,
// so it is legal between an "event:" line and its "data:" line, or between two "data:" lines
// of a multi-line payload.
func TestSSEStreamReaderHeartbeatBoundaryMatrix(t *testing.T) {
	cases := []struct {
		name  string
		write string
		want  bool // may a heartbeat be emitted after this write?
	}{
		{"complete event", "data: {\"a\":1}\n\n", true},
		{"single line terminated", "event: ping\n", true},
		{"between multi-line data fields", "data: line one\ndata: line two\n", true},
		{"mid JSON value", "data: {\"a\":\"partial", false},
		{"mid field name", "dat", false},
		{"after colon only", "data:", false},
		{"CRLF terminated", "data: {\"a\":1}\r\n", true},
		// A bare CR is a legal SSE line terminator, but we only recognize '\n'. The result is
		// a suppressed heartbeat, never a corrupted line -- degrade safely, don't guess.
		{"bare CR terminated", "data: {\"a\":1}\r", false},
		{"trailing space after newline", "data: x\n ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewSSEStreamReader()
			go func() { _, _ = io.ReadAll(r) }()
			r.Send([]byte(tc.write))
			if got := r.atLineBoundary; got != tc.want {
				t.Errorf("after writing %q: atLineBoundary=%v, want %v", tc.write, got, tc.want)
			}
			r.Done()
		})
	}
}

// TestSSEStreamReaderWrapperMethodsLeaveBoundary covers the typed paths (handleStreaming,
// handleStreamingResponse), which never call Send directly -- they use these wrappers, each of
// which emits a complete frame. Every one must leave the stream at a boundary, otherwise the
// #5905 gate would start suppressing heartbeats on paths that were never affected and silently
// regress the disconnect detection from #5010.
func TestSSEStreamReaderWrapperMethodsLeaveBoundary(t *testing.T) {
	cases := []struct {
		name string
		send func(*SSEStreamReader) bool
	}{
		{"SendEvent with type", func(r *SSEStreamReader) bool { return r.SendEvent("delta", []byte(`{"a":1}`)) }},
		{"SendEvent without type", func(r *SSEStreamReader) bool { return r.SendEvent("", []byte(`{"a":1}`)) }},
		{"SendEvent empty data", func(r *SSEStreamReader) bool { return r.SendEvent("ping", nil) }},
		{"SendError", func(r *SSEStreamReader) bool { return r.SendError([]byte(`{"error":"x"}`)) }},
		{"SendDone", func(r *SSEStreamReader) bool { return r.SendDone() }},
		{"SendHeartbeat", func(r *SSEStreamReader) bool { return r.SendHeartbeat() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewSSEStreamReader()
			go func() { _, _ = io.ReadAll(r) }()
			if !tc.send(r) {
				t.Fatal("send returned false on a live reader")
			}
			if !r.atLineBoundary {
				t.Error("wrapper method left the stream mid-line; heartbeats would be suppressed " +
					"on a typed path that never had #5905")
			}
			if !r.SendHeartbeat() {
				t.Error("heartbeat refused after a complete frame")
			}
			r.Done()
		})
	}
}

// TestSSEStreamReaderZeroLengthSendKeepsBoundary covers the guard in sendLocked: a zero-length
// send writes no bytes, so it cannot move the write position. Reading event[len-1] without the
// guard would panic.
func TestSSEStreamReaderZeroLengthSendKeepsBoundary(t *testing.T) {
	r := NewSSEStreamReader()
	go func() { _, _ = io.ReadAll(r) }()

	r.Send([]byte("data: partial")) // mid-line
	r.Send([]byte{})                // must not reset the position
	if r.atLineBoundary {
		t.Error("empty Send wrongly reported a line boundary")
	}
	r.Send([]byte("\n")) // now terminated
	r.Send(nil)          // must not reset it either
	if !r.atLineBoundary {
		t.Error("nil Send wrongly cleared the line boundary")
	}
	r.Done()
}

// TestSSEStreamReaderClosedMidLineReportsDisconnect pins the ordering inside SendHeartbeat: a
// real disconnect outranks a mid-line skip. If the boundary check ran first, a client that
// disconnected while the stream sat mid-line would return true ("skipped"), StartSSEHeartbeat
// would never call onDisconnect, and the proactive detection from #5010 would be dead exactly
// on the passthrough path #5905 affects.
func TestSSEStreamReaderClosedMidLineReportsDisconnect(t *testing.T) {
	r := NewSSEStreamReader()
	go func() { _, _ = io.ReadAll(r) }()

	r.Send([]byte("data: {\"partial\":\"no newline")) // leave the stream mid-line
	r.Close()                                         // client disconnects

	if r.SendHeartbeat() {
		t.Error("mid-line heartbeat on a CLOSED reader returned true; the disconnect would " +
			"go undetected because StartSSEHeartbeat only reacts to false")
	}
}

// TestSSEStreamReaderHeartbeatRaceAcrossRandomSplits is the full-fidelity reproduction: a real
// StartSSEHeartbeat goroutine ticking against a producer that forwards one SSE stream in
// arbitrary, uneven chunks -- the actual shape of handlePassthroughStream, where core's
// StreamPassthrough hands over whatever a 4096-byte Read returned.
//
// The assertions are timing-independent: they hold for any number of heartbeats at any
// interleaving, so this cannot flake on a loaded CI worker. Run under -race, since the whole
// point is two goroutines writing one stream.
func TestSSEStreamReaderHeartbeatRaceAcrossRandomSplits(t *testing.T) {
	var original strings.Builder
	for i := range 12 {
		payload := fmt.Sprintf(`{"seq":%d,"blob":%q}`, i, strings.Repeat("X", 512))
		fmt.Fprintf(&original, "event: chunk.%d\ndata: %s\n\n", i, payload)
	}
	full := original.String()

	// Uneven, co-prime-ish sizes so splits land mid-token, mid-JSON, and occasionally on a
	// terminator -- mimicking arbitrary TCP segmentation rather than tidy frame boundaries.
	sizes := []int{7, 1, 131, 13, 4096, 2, 997, 64, 3}

	r := NewSSEStreamReader()
	readDone := make(chan []byte, 1)
	go func() {
		b, err := io.ReadAll(r)
		if err != nil {
			t.Errorf("ReadAll: %v", err)
		}
		readDone <- b
	}()

	// A deliberately aggressive interval: maximize the number of interleaving attempts. The
	// callback is wrapped so the producer can rendezvous with it below -- a sleep would only
	// hope the ticker fires, leaving the assertions vacuous on a stalled worker. The signal
	// send is non-blocking: blocking here would stall the heartbeat goroutine inside send(),
	// which is StopSSEHeartbeat's job to rescue, not this test's scenario.
	var callbacks atomic.Int64
	beat := make(chan struct{}, 1)
	heartbeat := func() bool {
		ok := r.SendHeartbeat()
		callbacks.Add(1)
		select {
		case beat <- struct{}{}:
		default:
		}
		return ok
	}
	done, exited := StartSSEHeartbeat(50*time.Microsecond, heartbeat, func() {})

	remaining := full
	for i := 0; len(remaining) > 0; i++ {
		n := min(sizes[i%len(sizes)], len(remaining))
		if !r.Send([]byte(remaining[:n])) {
			t.Fatal("Send returned false on a live reader")
		}
		remaining = remaining[n:]
		// Wait for a heartbeat callback rather than sleeping: this proves a heartbeat ran
		// between these two raw chunks, which is the interleaving under test.
		select {
		case <-beat:
		case <-time.After(5 * time.Second):
			t.Fatal("heartbeat goroutine never ticked; the interleaving under test never happened")
		}
	}

	if callbacks.Load() == 0 {
		t.Fatal("heartbeat callback never ran -- the assertions below would pass vacuously")
	}

	StopSSEHeartbeat(r, done, exited)
	r.Done()
	got := <-readDone

	// 1. Every heartbeat starts its own line.
	beats := 0
	for idx := 0; ; {
		i := bytes.Index(got[idx:], sseHeartbeatFrame)
		if i < 0 {
			break
		}
		at := idx + i
		beats++
		if at != 0 && got[at-1] != '\n' {
			t.Fatalf("heartbeat spliced mid-line at offset %d (preceding byte %q)", at, got[at-1])
		}
		idx = at + len(sseHeartbeatFrame)
	}
	t.Logf("interleaved %d heartbeats across %d bytes without corruption", beats, len(got))

	// 2. Provider bytes forwarded 1:1 once heartbeats are removed.
	if stripped := string(bytes.ReplaceAll(got, sseHeartbeatFrame, nil)); stripped != full {
		t.Errorf("provider bytes not forwarded 1:1 (got %d bytes, want %d)", len(stripped), len(full))
	}

	// 3. Every event a client would decode still carries complete, parseable JSON.
	events := 0
	for _, block := range strings.Split(string(got), "\n\n") {
		var data string
		for _, line := range strings.Split(block, "\n") {
			if after, ok := strings.CutPrefix(line, "data: "); ok {
				data += after
			}
		}
		if data == "" {
			continue
		}
		events++
		var decoded map[string]any
		if err := json.Unmarshal([]byte(data), &decoded); err != nil {
			t.Errorf("event %d payload not valid JSON: %v", events, err)
		}
	}
	if events != 12 {
		t.Errorf("decoded %d complete events, want 12", events)
	}
}

// TestSSEStreamReaderConcurrentProducersWithHeartbeat stresses the mutex itself: several
// producers plus a live heartbeat goroutine all writing at once. Nothing here asserts
// ordering between producers -- only that the line-boundary invariant holds no matter how the
// writes interleave, and that the reader neither panics nor deadlocks. Run under -race.
func TestSSEStreamReaderConcurrentProducersWithHeartbeat(t *testing.T) {
	r := NewSSEStreamReader()
	readDone := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		readDone <- b
	}()

	// The property under test is OVERLAP, not ordering: a heartbeat that fired before the
	// producers started proves nothing, and 200 SendEvent calls can easily complete inside a
	// single tick interval. So the heartbeat callback observes the producers rather than the
	// other way round -- it reads how many are mid-loop at the instant it runs, and producers
	// keep emitting past their nominal 50 events until one such overlapping beat is seen.
	// Without this the boundary scan below is a no-op loop over zero heartbeat frames.
	var concurrentBeats, live atomic.Int64
	sawConcurrent := make(chan struct{})
	var once sync.Once
	heartbeat := func() bool {
		ok := r.SendHeartbeat()
		if live.Load() > 0 {
			concurrentBeats.Add(1)
			once.Do(func() { close(sawConcurrent) })
		}
		return ok
	}
	done, exited := StartSSEHeartbeat(50*time.Microsecond, heartbeat, func() {})

	var wg sync.WaitGroup
	for p := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			live.Add(1)
			defer live.Add(-1)
			// Bounded only so a heartbeat goroutine that died can't spin here forever; the
			// 50us ticker means the loop normally exits within a handful of extra events.
			deadline := time.Now().Add(5 * time.Second)
			for i := 0; ; i++ {
				// Always complete frames, so every producer leaves a boundary behind.
				r.SendEvent(fmt.Sprintf("p%d", p), fmt.Appendf(nil, `{"i":%d}`, i))
				if i < 50 {
					continue
				}
				select {
				case <-sawConcurrent:
					return
				default:
				}
				if time.Now().After(deadline) {
					t.Error("no heartbeat callback ran while a producer was mid-loop")
					return
				}
			}
		}()
	}
	wg.Wait()

	if concurrentBeats.Load() == 0 {
		t.Fatal("no heartbeat overlapped a live producer -- the boundary scan below is vacuous")
	}
	t.Logf("%d heartbeat(s) landed while producers were mid-loop", concurrentBeats.Load())

	StopSSEHeartbeat(r, done, exited)
	r.Done()
	got := <-readDone

	for idx := 0; ; {
		i := bytes.Index(got[idx:], sseHeartbeatFrame)
		if i < 0 {
			break
		}
		at := idx + i
		if at != 0 && got[at-1] != '\n' {
			t.Fatalf("heartbeat spliced mid-line at offset %d under concurrent producers", at)
		}
		idx = at + len(sseHeartbeatFrame)
	}
}

// TestSSEStreamReaderSkippedHeartbeatIsNotDisconnect pins the contract StartSSEHeartbeat
// depends on: its send callback returning false means "the reader was closed, the client is
// gone" and triggers onDisconnect/cancel. Declining to emit a heartbeat because the stream is
// mid-line is NOT a disconnect, so it must still return true. Returning false here would abort
// every passthrough stream that happens to tick mid-line -- a far worse bug than #5905 itself.
func TestSSEStreamReaderSkippedHeartbeatIsNotDisconnect(t *testing.T) {
	r := NewSSEStreamReader()
	go func() { _, _ = io.ReadAll(r) }()

	r.Send([]byte("data: {\"partial\":\"no trailing newline")) // leaves the reader mid-line
	if !r.SendHeartbeat() {
		t.Error("mid-line heartbeat returned false; StartSSEHeartbeat reads false as a client " +
			"disconnect and would cancel a healthy stream")
	}
	r.Done()
}

func TestSSEStreamReaderSingleEventPerRead(t *testing.T) {
	r := NewSSEStreamReader()

	events := [][]byte{
		[]byte("data: {\"chunk\":1}\n\n"),
		[]byte("data: {\"chunk\":2}\n\n"),
		[]byte("data: {\"chunk\":3}\n\n"),
	}

	errCh := make(chan error, 1)
	go func() {
		for _, e := range events {
			if !r.Send(e) {
				select {
				case errCh <- fmt.Errorf("Send returned false unexpectedly"):
				default:
				}
				return
			}
		}
		r.Done()
	}()

	buf := make([]byte, 4096)
	for i, want := range events {
		n, err := r.Read(buf)
		if err != nil {
			t.Fatalf("event %d: unexpected error: %v", i, err)
		}
		got := string(buf[:n])
		if got != string(want) {
			t.Errorf("event %d: got %q, want %q", i, got, want)
		}
	}

	// Next read should return EOF
	n, err := r.Read(buf)
	if err != io.EOF {
		t.Errorf("expected io.EOF, got err=%v n=%d", err, n)
	}

	select {
	case err := <-errCh:
		t.Error(err)
	default:
	}
}

func TestSSEStreamReaderPartialRead(t *testing.T) {
	r := NewSSEStreamReader()
	event := []byte("data: {\"content\":\"hello world\"}\n\n")

	go func() {
		r.Send(event)
		r.Done()
	}()

	// Read with a small buffer (5 bytes at a time)
	var result []byte
	buf := make([]byte, 5)
	for {
		n, err := r.Read(buf)
		result = append(result, buf[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if string(result) != string(event) {
		t.Errorf("reassembled data: got %q, want %q", result, event)
	}
}

func TestSSEStreamReaderEOFOnDone(t *testing.T) {
	r := NewSSEStreamReader()
	r.Done() // Close immediately

	buf := make([]byte, 4096)
	n, err := r.Read(buf)
	if err != io.EOF {
		t.Errorf("expected io.EOF, got err=%v n=%d", err, n)
	}
}

func TestSSEStreamReaderCloseSignalsProducer(t *testing.T) {
	r := NewSSEStreamReader()

	r.Close()

	if r.Send([]byte("data: test\n\n")) {
		t.Error("Send should return false after Close")
	}
}

func TestSSEStreamReaderIdempotentClose(t *testing.T) {
	r := NewSSEStreamReader()

	// Should not panic
	r.Close()
	r.Close()
	r.Close()
}

func TestSSEStreamReaderConcurrent(t *testing.T) {
	r := NewSSEStreamReader()
	const numEvents = 100

	var wg sync.WaitGroup
	wg.Add(1)

	// Producer
	go func() {
		for i := 0; i < numEvents; i++ {
			if !r.Send([]byte("data: event\n\n")) {
				break
			}
		}
		r.Done()
	}()

	// Consumer
	errCh := make(chan error, 2)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		count := 0
		for {
			_, err := r.Read(buf)
			if err == io.EOF {
				break
			}
			if err != nil {
				select {
				case errCh <- fmt.Errorf("unexpected error: %v", err):
				default:
				}
				break
			}
			count++
		}
		if count != numEvents {
			select {
			case errCh <- fmt.Errorf("got %d events, want %d", count, numEvents):
			default:
			}
		}
	}()

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestSSEStreamReaderSendEvent(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		data      []byte
		want      string
	}{
		{
			name:      "data only",
			eventType: "",
			data:      []byte(`{"chunk":1}`),
			want:      "data: {\"chunk\":1}\n\n",
		},
		{
			name:      "with event type",
			eventType: "response.delta",
			data:      []byte(`{"delta":"hi"}`),
			want:      "event: response.delta\ndata: {\"delta\":\"hi\"}\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewSSEStreamReader()
			go func() {
				r.SendEvent(tt.eventType, tt.data)
				r.Done()
			}()

			buf := make([]byte, 4096)
			n, err := r.Read(buf)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := string(buf[:n]); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSSEStreamReaderSendError(t *testing.T) {
	r := NewSSEStreamReader()
	go func() {
		r.SendError([]byte(`{"error":"bad"}`))
		r.Done()
	}()

	buf := make([]byte, 4096)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "event: error\ndata: {\"error\":\"bad\"}\n\n"
	if got := string(buf[:n]); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSSEStreamReaderSendDone(t *testing.T) {
	r := NewSSEStreamReader()
	go func() {
		r.SendDone()
		r.Done()
	}()

	buf := make([]byte, 4096)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "data: [DONE]\n\n"
	if got := string(buf[:n]); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSSEStreamReaderSendEventAfterClose(t *testing.T) {
	r := NewSSEStreamReader()
	r.Close()

	if r.SendEvent("test", []byte("data")) {
		t.Error("SendEvent should return false after Close")
	}
	if r.SendError([]byte("err")) {
		t.Error("SendError should return false after Close")
	}
	if r.SendDone() {
		t.Error("SendDone should return false after Close")
	}
}

// TestSSEStreamReaderSendEventByteAccuracy verifies that SendEvent produces
// the exact same bytes that the old manual buffer assembly in the handlers did.
func TestSSEStreamReaderSendEventByteAccuracy(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		data      []byte
		want      []byte
	}{
		{
			name:      "standard SSE data (old inference.go pattern)",
			eventType: "",
			data:      []byte(`{"id":"chatcmpl-123","choices":[{"delta":{"content":"Hello"}}]}`),
			want: func() []byte {
				// Old code: buf = append(buf, "data: "...); buf = append(buf, chunkJSON...); buf = append(buf, '\n', '\n')
				data := []byte(`{"id":"chatcmpl-123","choices":[{"delta":{"content":"Hello"}}]}`)
				buf := make([]byte, 0, len(data)+8)
				buf = append(buf, "data: "...)
				buf = append(buf, data...)
				buf = append(buf, '\n', '\n')
				return buf
			}(),
		},
		{
			name:      "OpenAI responses format with event type (old inference.go pattern)",
			eventType: "response.output_item.added",
			data:      []byte(`{"type":"response.output_item.added","item":{"id":"item_1"}}`),
			want: func() []byte {
				// Old code: buf = append(buf, "event: "...); buf = append(buf, eventType...); buf = append(buf, "\ndata: "...); ...
				eventType := "response.output_item.added"
				data := []byte(`{"type":"response.output_item.added","item":{"id":"item_1"}}`)
				buf := make([]byte, 0, len(eventType)+len(data)+16)
				buf = append(buf, "event: "...)
				buf = append(buf, eventType...)
				buf = append(buf, "\ndata: "...)
				buf = append(buf, data...)
				buf = append(buf, '\n', '\n')
				return buf
			}(),
		},
		{
			name:      "error event (old interceptor pattern)",
			eventType: "error",
			data:      []byte(`{"error":"stream interrupted"}`),
			want: func() []byte {
				data := []byte(`{"error":"stream interrupted"}`)
				buf := make([]byte, 0, len(data)+24)
				buf = append(buf, "event: error\ndata: "...)
				buf = append(buf, data...)
				buf = append(buf, '\n', '\n')
				return buf
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewSSEStreamReader()
			go func() {
				r.SendEvent(tt.eventType, tt.data)
				r.Done()
			}()

			buf := make([]byte, 4096)
			n, err := r.Read(buf)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := buf[:n]
			if string(got) != string(tt.want) {
				t.Errorf("byte mismatch:\n  got:  %q\n  want: %q", got, tt.want)
			}
		})
	}
}

// TestSSEStreamReaderSendErrorByteAccuracy verifies SendError matches
// the old "event: error\ndata: ..." manual assembly.
func TestSSEStreamReaderSendErrorByteAccuracy(t *testing.T) {
	r := NewSSEStreamReader()
	errorJSON := []byte(`{"error":{"type":"internal_error","message":"An error occurred"}}`)

	go func() {
		r.SendError(errorJSON)
		r.Done()
	}()

	buf := make([]byte, 4096)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must match the old pattern exactly:
	// buf = append(buf, "event: error\ndata: "...)
	// buf = append(buf, errorJSON...)
	// buf = append(buf, '\n', '\n')
	want := "event: error\ndata: " + string(errorJSON) + "\n\n"
	if got := string(buf[:n]); got != want {
		t.Errorf("byte mismatch:\n  got:  %q\n  want: %q", got, want)
	}
}

// TestSSEStreamReaderMixedMethodStream simulates a realistic stream
// that uses multiple methods (like router.go does): data events,
// typed events, error, and done marker.
func TestSSEStreamReaderMixedMethodStream(t *testing.T) {
	r := NewSSEStreamReader()

	expected := []string{
		"data: {\"chunk\":1}\n\n",
		"event: response.delta\ndata: {\"delta\":\"hi\"}\n\n",
		"data: {\"chunk\":2}\n\n",
		"event: error\ndata: {\"error\":\"timeout\"}\n\n",
		"data: [DONE]\n\n",
	}

	go func() {
		r.SendEvent("", []byte(`{"chunk":1}`))
		r.SendEvent("response.delta", []byte(`{"delta":"hi"}`))
		r.SendEvent("", []byte(`{"chunk":2}`))
		r.SendError([]byte(`{"error":"timeout"}`))
		r.SendDone()
		r.Done()
	}()

	buf := make([]byte, 4096)
	for i, want := range expected {
		n, err := r.Read(buf)
		if err != nil {
			t.Fatalf("event %d: unexpected error: %v", i, err)
		}
		if got := string(buf[:n]); got != want {
			t.Errorf("event %d:\n  got:  %q\n  want: %q", i, got, want)
		}
	}

	// Should be EOF after all events
	n, err := r.Read(buf)
	if err != io.EOF {
		t.Errorf("expected EOF, got err=%v n=%d", err, n)
	}
}

// TestSSEStreamReaderRawAndWrapperMixed simulates the router.go pattern
// where raw Send (for Bedrock/passthrough) is mixed with wrapper methods.
func TestSSEStreamReaderRawAndWrapperMixed(t *testing.T) {
	r := NewSSEStreamReader()

	// Simulate: Bedrock binary event (raw), followed by SSE events, then done
	bedrockBinary := []byte{0x00, 0x00, 0x00, 0x42, 0x00, 0x00, 0x00, 0x2A} // fake binary
	preformattedSSE := []byte("event: content_block_delta\ndata: {\"delta\":\"test\"}\n\n")

	expected := [][]byte{
		bedrockBinary,
		preformattedSSE,
		[]byte("data: {\"final\":true}\n\n"),
	}

	go func() {
		r.Send(bedrockBinary)                     // raw binary passthrough
		r.Send(preformattedSSE)                   // pre-formatted SSE string
		r.SendEvent("", []byte(`{"final":true}`)) // wrapper method
		r.Done()
	}()

	buf := make([]byte, 4096)
	for i, want := range expected {
		n, err := r.Read(buf)
		if err != nil {
			t.Fatalf("event %d: unexpected error: %v", i, err)
		}
		if string(buf[:n]) != string(want) {
			t.Errorf("event %d:\n  got:  %q\n  want: %q", i, buf[:n], want)
		}
	}
}

// TestSSEStreamReaderSendEventEmptyData verifies behavior with empty data payload.
func TestSSEStreamReaderSendEventEmptyData(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		want      string
	}{
		{
			name:      "empty data no event type",
			eventType: "",
			want:      "data: \n\n",
		},
		{
			name:      "empty data with event type",
			eventType: "heartbeat",
			want:      "event: heartbeat\ndata: \n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewSSEStreamReader()
			go func() {
				r.SendEvent(tt.eventType, []byte{})
				r.Done()
			}()

			buf := make([]byte, 4096)
			n, err := r.Read(buf)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := string(buf[:n]); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSSEStreamReaderSendEventNilData verifies behavior with nil data payload.
func TestSSEStreamReaderSendEventNilData(t *testing.T) {
	r := NewSSEStreamReader()
	go func() {
		r.SendEvent("", nil)
		r.Done()
	}()

	buf := make([]byte, 4096)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// nil data should produce same as empty: "data: \n\n"
	if got := string(buf[:n]); got != "data: \n\n" {
		t.Errorf("got %q, want %q", got, "data: \n\n")
	}
}

// TestSSEStreamReaderSendEventLargePayload verifies no corruption with large JSON payloads.
func TestSSEStreamReaderSendEventLargePayload(t *testing.T) {
	r := NewSSEStreamReader()

	// Build a large JSON payload (~64KB, larger than typical ReadBufferSize)
	largeContent := make([]byte, 65536)
	for i := range largeContent {
		largeContent[i] = 'A' + byte(i%26)
	}
	data := append([]byte(`{"content":"`), largeContent...)
	data = append(data, '"', '}')

	go func() {
		r.SendEvent("response.delta", data)
		r.Done()
	}()

	// Read the entire event using small buffer to exercise partial reads
	var result []byte
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		result = append(result, buf[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	want := "event: response.delta\ndata: " + string(data) + "\n\n"
	if string(result) != want {
		t.Errorf("large payload mismatch: got len=%d, want len=%d", len(result), len(want))
		// Check prefix and suffix for debugging
		if len(result) > 40 {
			t.Errorf("  got prefix:  %q", result[:40])
			t.Errorf("  want prefix: %q", want[:40])
		}
	}
}

// TestSSEStreamReaderMidStreamDisconnect simulates a client disconnecting
// mid-stream while the producer is using SendEvent.
func TestSSEStreamReaderMidStreamDisconnect(t *testing.T) {
	r := NewSSEStreamReader()

	producerDone := make(chan int) // reports how many events were sent
	go func() {
		sent := 0
		for i := 0; i < 100; i++ {
			if !r.SendEvent("", []byte(fmt.Sprintf(`{"chunk":%d}`, i))) {
				break
			}
			sent++
		}
		close(producerDone)
	}()

	// Read a few events then simulate client disconnect
	buf := make([]byte, 4096)
	for i := 0; i < 3; i++ {
		_, err := r.Read(buf)
		if err != nil {
			t.Fatalf("event %d: unexpected error: %v", i, err)
		}
	}

	// Client disconnects
	r.Close()

	// Producer should stop promptly
	<-producerDone
}

// TestSSEStreamReaderSendErrorThenDone verifies the handler pattern
// of sending an error event and immediately closing the stream.
func TestSSEStreamReaderSendErrorThenDone(t *testing.T) {
	r := NewSSEStreamReader()

	go func() {
		// Send a few normal events
		r.SendEvent("", []byte(`{"chunk":1}`))
		r.SendEvent("", []byte(`{"chunk":2}`))
		// Error occurs, send error and stop
		r.SendError([]byte(`{"error":"rate_limit"}`))
		r.Done()
	}()

	buf := make([]byte, 4096)
	expected := []string{
		"data: {\"chunk\":1}\n\n",
		"data: {\"chunk\":2}\n\n",
		"event: error\ndata: {\"error\":\"rate_limit\"}\n\n",
	}

	for i, want := range expected {
		n, err := r.Read(buf)
		if err != nil {
			t.Fatalf("event %d: unexpected error: %v", i, err)
		}
		if got := string(buf[:n]); got != want {
			t.Errorf("event %d: got %q, want %q", i, got, want)
		}
	}

	// Should be EOF (stream ended after error, no [DONE] marker)
	n, err := r.Read(buf)
	if err != io.EOF {
		t.Errorf("expected EOF after error event, got err=%v n=%d data=%q", err, n, buf[:n])
	}
}

// TestSSEStreamReaderSendDoneByteExact verifies SendDone produces
// exactly "data: [DONE]\n\n" — the standard OpenAI SSE terminator.
func TestSSEStreamReaderSendDoneByteExact(t *testing.T) {
	r := NewSSEStreamReader()
	go func() {
		r.SendDone()
		r.Done()
	}()

	// Use exact-size buffer to verify no extra bytes
	want := []byte("data: [DONE]\n\n")
	buf := make([]byte, len(want))
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != len(want) {
		t.Errorf("expected %d bytes, got %d", len(want), n)
	}
	if string(buf[:n]) != string(want) {
		t.Errorf("got %q, want %q", buf[:n], want)
	}
}

// TestSSEStreamReaderConcurrentSendEvent verifies thread safety of SendEvent
// with multiple concurrent producers (not a real pattern but validates safety).
func TestSSEStreamReaderConcurrentSendEvent(t *testing.T) {
	r := NewSSEStreamReader()
	const numProducers = 5
	const eventsPerProducer = 20

	var wg sync.WaitGroup
	wg.Add(numProducers)

	// Launch multiple producers
	for p := 0; p < numProducers; p++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < eventsPerProducer; i++ {
				if !r.SendEvent("", []byte(fmt.Sprintf(`{"p":%d,"i":%d}`, id, i))) {
					return
				}
			}
		}(p)
	}

	// Close after all producers finish
	go func() {
		wg.Wait()
		r.Done()
	}()

	// Consume all events
	buf := make([]byte, 4096)
	count := 0
	for {
		n, err := r.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Every event must be a valid SSE data line
		got := string(buf[:n])
		if len(got) < 8 || got[:6] != "data: " || got[len(got)-2:] != "\n\n" {
			t.Errorf("event %d: invalid SSE format: %q", count, got)
		}
		count++
	}

	if count != numProducers*eventsPerProducer {
		t.Errorf("got %d events, want %d", count, numProducers*eventsPerProducer)
	}
}

// TestSSEStreamReaderSendHeartbeat verifies the heartbeat frame is a bare SSE
// comment line (starts with ':', per the WHATWG SSE spec every compliant client
// ignores a comment line) with no trailing blank line, so it never dispatches
// an event in any decoder -- including non-conforming ones that dispatch empty
// events on a blank line (#5874).
func TestSSEStreamReaderSendHeartbeat(t *testing.T) {
	r := NewSSEStreamReader()
	go func() {
		r.SendHeartbeat()
		r.Done()
	}()

	buf := make([]byte, 4096)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := string(buf[:n]), ": heartbeat\n"; got != want {
		t.Errorf("heartbeat frame = %q, want %q", got, want)
	}
}

// TestSSEStreamReaderSendHeartbeatAfterClose verifies the same disconnect
// contract as the other Send* wrappers: false once the reader is closed.
func TestSSEStreamReaderSendHeartbeatAfterClose(t *testing.T) {
	r := NewSSEStreamReader()
	r.Close()

	if r.SendHeartbeat() {
		t.Error("SendHeartbeat should return false after Close")
	}
}

// TestSSEStreamReaderDoneWhileSendBlockedDoesNotPanic is the regression test for a
// shutdown-ordering bug in handleStreamingResponse's heartbeat goroutine: the buffered
// eventCh has capacity 1, so a second concurrent Send (e.g. a heartbeat firing while a
// real event is already queued and nothing is reading) blocks on `eventCh <- event`.
// Calling Done() (which closes eventCh) while that send is still blocked races the close
// against the pending send -- Go panics on "send on closed channel" if the send resumes
// after the close. The safe sequence is: Close() first (the blocked Send's inner select
// already watches closeCh, so it unblocks there instead, safely), wait for that goroutine
// to actually exit, and only then call Done(). This proves the safe sequence never panics
// even with a send still in flight when shutdown begins.
//
// Runs inside a synctest bubble so synctest.Wait() can deterministically confirm the
// background goroutine has genuinely entered the blocking send (durably blocked on
// eventCh, a channel created inside the bubble) before shutdown starts -- a plain
// "goroutine has started" signal plus a timing guess could let Close() run first and
// silently exercise the already-covered after-close path instead of this one.
func TestSSEStreamReaderDoneWhileSendBlockedDoesNotPanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewSSEStreamReader()

		// Fill the single buffer slot so a second Send has nowhere to go and blocks.
		if !r.Send([]byte("data: first\n\n")) {
			t.Fatal("first Send should succeed (buffer has room)")
		}

		blockedSendResult := make(chan bool, 1)
		go func() {
			// This blocks: the buffer is full and nothing is reading it.
			blockedSendResult <- r.SendHeartbeat()
		}()

		// Deterministically wait until the goroutine above is durably blocked on the
		// send, not just scheduled to run -- see the doc comment above.
		synctest.Wait()

		// Safe shutdown sequence, mirroring handleStreamingResponse's cleanup: Close() first...
		r.Close()

		// ...wait for the blocked send to actually unblock and return...
		select {
		case ok := <-blockedSendResult:
			if ok {
				t.Error("blocked SendHeartbeat should return false once Close() unblocks it")
			}
		case <-time.After(time.Second):
			t.Fatal("blocked Send never unblocked after Close() -- would deadlock handleStreamingResponse's cleanup")
		}

		// ...and only now is Done() (which closes eventCh) safe to call: no other goroutine
		// can still be mid-send on it.
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("Done() panicked after the safe Close-then-wait sequence: %v", rec)
			}
		}()
		r.Done()
	})
}

func TestSSEStreamReaderCloseUnblocksProducer(t *testing.T) {
	r := NewSSEStreamReader()

	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(done)
		// Fill the channel buffer (cap=1)
		r.Send([]byte("data: first\n\n"))
		// This Send should block until Close is called
		r.Send([]byte("data: second\n\n"))
		// After Close, the next Send should return false
		if r.Send([]byte("data: third\n\n")) {
			select {
			case errCh <- fmt.Errorf("Send should return false after Close"):
			default:
			}
		}
	}()

	// Close unblocks the blocked Send
	r.Close()
	<-done

	select {
	case err := <-errCh:
		t.Error(err)
	default:
	}
}

// TestStartSSEHeartbeatFiresPeriodically verifies the extracted heartbeat goroutine
// (used by both handleStreamingResponse and router.go's handleStreaming) sends more than
// one frame while a reader is actively drained, and that StopSSEHeartbeat followed by
// Done() shuts it down cleanly.
// Runs inside a synctest bubble so the sleep below advances the bubble's fake clock
// deterministically instead of real wall-clock time -- a busy CI worker delaying the
// ticker or reader goroutine can no longer make this flaky, unlike a real time.Sleep.
func TestStartSSEHeartbeatFiresPeriodically(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewSSEStreamReader()

		var received atomic.Int32
		drainDone := make(chan struct{})
		go func() {
			defer close(drainDone)
			buf := make([]byte, 4096)
			for {
				n, err := r.Read(buf)
				if n > 0 {
					received.Add(1)
				}
				if err != nil {
					return
				}
			}
		}()

		// onDisconnect is intentionally a no-op here, not an assertion that it never fires:
		// StopSSEHeartbeat's reader.Close() call can legitimately unblock a heartbeat that's
		// mid-Send (buffer full, drain lagging) with a false return, the same as a real
		// disconnect would -- see TestSSEStreamReaderDoneWhileSendBlockedDoesNotPanic. In
		// production onDisconnect is cancel(), which is idempotent and safe to call during an
		// already-ending stream, so this is benign, not a bug.
		done, exited := StartSSEHeartbeat(5*time.Millisecond, r.SendHeartbeat, func() {})

		time.Sleep(60 * time.Millisecond) // several ticks at the 5ms interval

		StopSSEHeartbeat(r, done, exited)
		r.Done()
		<-drainDone

		if got := received.Load(); got < 2 {
			t.Errorf("expected at least 2 heartbeat frames in 60ms at a 5ms interval, got %d", got)
		}
	})
}

// TestStartSSEHeartbeatCallsOnDisconnectAfterClose verifies the goroutine detects a closed
// reader (SendHeartbeat returning false) and invokes onDisconnect exactly once, mirroring
// how handleStreamingResponse's cancel() is wired to heartbeat-discovered disconnects.
func TestStartSSEHeartbeatCallsOnDisconnectAfterClose(t *testing.T) {
	r := NewSSEStreamReader()
	r.Close() // SendHeartbeat will return false on the very first tick

	var onDisconnectCalls atomic.Int32
	_, exited := StartSSEHeartbeat(5*time.Millisecond, r.SendHeartbeat, func() {
		onDisconnectCalls.Add(1)
	})

	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("heartbeat goroutine never exited after the reader was closed")
	}

	if got := onDisconnectCalls.Load(); got != 1 {
		t.Errorf("onDisconnect calls = %d, want exactly 1", got)
	}
}
