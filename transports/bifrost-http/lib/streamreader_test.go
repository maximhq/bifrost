package lib

import (
	"fmt"
	"io"
	"sync"
	"testing"
)

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
		r.Send(bedrockBinary)                       // raw binary passthrough
		r.Send(preformattedSSE)                     // pre-formatted SSE string
		r.SendEvent("", []byte(`{"final":true}`))   // wrapper method
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
