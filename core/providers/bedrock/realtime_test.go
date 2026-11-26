package bedrock

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

var sonicTestCreds = aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"}

// fakeSonicServer is an HTTP/2 Bedrock stand-in that verifies the chained event signatures
// and records each Nova Sonic input event it receives.
type fakeSonicServer struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	inputs  []string
	sawEnd  bool
	handled chan struct{}
	// respond runs once per input event and may write model events back.
	respond func(w io.Writer, flush func(), name string)
	// reject, when non-zero, refuses the request with this status before reading events.
	reject int
	// credentialCalls counts how often the connection resolved credentials.
	credentialCalls int
}

func (f *fakeSonicServer) credentials() (aws.Credentials, *schemas.BifrostError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.credentialCalls++
	return sonicTestCreds, nil
}

func newFakeSonicServer(t *testing.T) *fakeSonicServer {
	f := &fakeSonicServer{t: t, handled: make(chan struct{})}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	f.srv.EnableHTTP2 = true
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSonicServer) serve(w http.ResponseWriter, r *http.Request) {
	defer close(f.handled)
	if f.reject != 0 {
		w.WriteHeader(f.reject)
		_, _ = w.Write([]byte(`{"message":"User is not authorized to perform: bedrock:InvokeModelWithBidirectionalStream"}`))
		return
	}
	if r.ProtoMajor != 2 || r.URL.Path != "/model/amazon.nova-2-sonic-v1:0/invoke-with-bidirectional-stream" ||
		r.Header.Get("X-Amz-Content-Sha256") != sonicStreamingPayloadHash || r.Header.Get("Content-Type") != "application/vnd.amazon.eventstream" {
		f.t.Errorf("unexpected request %s %s proto=%d headers=%v", r.Method, r.URL.Path, r.ProtoMajor, r.Header)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	seed, err := v4.GetSignedRequestSignature(r)
	if err != nil {
		f.t.Errorf("request is not signed: %v", err)
		return
	}
	verifier := v4.NewStreamSigner(sonicTestCreds, bedrockSigningService, "us-east-1", seed)
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)
	flusher.Flush()

	decoder := eventstream.NewDecoder()
	for {
		outer, err := decoder.Decode(r.Body, nil)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				f.t.Errorf("decode outer frame: %v", err)
			}
			return
		}
		date, _ := outer.Headers.Get(":date").Get().(time.Time)
		var dateHeader bytes.Buffer
		var dateOnly eventstream.Headers
		dateOnly.Set(":date", eventstream.TimestampValue(date))
		_ = eventstream.EncodeHeaders(&dateHeader, dateOnly)
		want, _ := verifier.GetSignature(context.Background(), dateHeader.Bytes(), outer.Payload, date)
		if got, _ := outer.Headers.Get(":chunk-signature").Get().([]byte); !bytes.Equal(got, want) {
			f.t.Errorf("chunk signature mismatch")
			return
		}
		if len(outer.Payload) == 0 {
			f.mu.Lock()
			f.sawEnd = true
			f.mu.Unlock()
			continue
		}
		inner, err := eventstream.NewDecoder().Decode(bytes.NewReader(outer.Payload), nil)
		if err != nil || headerString(inner, ":event-type") != "chunk" || headerString(inner, ":message-type") != "event" {
			f.t.Errorf("bad inner frame: %v", err)
			return
		}
		var chunk struct {
			Bytes []byte `json:"bytes"`
		}
		if err := json.Unmarshal(inner.Payload, &chunk); err != nil {
			f.t.Errorf("bad chunk payload: %v", err)
			return
		}
		name := sonicInputNames(f.t, [][]byte{chunk.Bytes})[0]
		f.mu.Lock()
		f.inputs = append(f.inputs, name)
		f.mu.Unlock()
		if f.respond != nil {
			f.respond(w, flusher.Flush, name)
		}
	}
}

func (f *fakeSonicServer) received() ([]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.inputs...), f.sawEnd
}

// writeSonicEvent writes one model event frame the way Bedrock does.
func writeSonicEvent(w io.Writer, event string) {
	var msg eventstream.Message
	msg.Headers.Set(":message-type", eventstream.StringValue("event"))
	msg.Headers.Set(":event-type", eventstream.StringValue("chunk"))
	msg.Headers.Set(":content-type", eventstream.StringValue("application/json"))
	msg.Payload = []byte(`{"bytes":"` + base64.StdEncoding.EncodeToString([]byte(event)) + `"}`)
	_ = eventstream.NewEncoder().Encode(w, msg)
}

func (f *fakeSonicServer) conn(t *testing.T) *bedrockRealtimeConn {
	provider := &BedrockProvider{realtimeClient: f.srv.Client()}
	conn := newBedrockRealtimeConn(context.Background(), provider, f.srv.URL+"/model/amazon.nova-2-sonic-v1:0/invoke-with-bidirectional-stream", "us-east-1", f.credentials, "amazon.nova-2-sonic-v1:0")
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// readUntil reads client events until one of the given type arrives.
func readUntil(t *testing.T, conn *bedrockRealtimeConn, eventType string) []string {
	t.Helper()
	var seen []string
	deadline := time.After(5 * time.Second)
	for {
		type frame struct {
			data []byte
			err  error
		}
		ch := make(chan frame, 1)
		go func() {
			_, data, err := conn.ReadMessage()
			ch <- frame{data, err}
		}()
		select {
		case f := <-ch:
			if f.err != nil {
				t.Fatalf("read failed before %s (seen %v): %v", eventType, seen, f.err)
			}
			seen = append(seen, gjson.GetBytes(f.data, "type").String())
			if seen[len(seen)-1] == eventType {
				return seen
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s (seen %v)", eventType, seen)
		}
	}
}

func TestBedrockRealtimeConnStreamsSignedEventsOverHTTP2(t *testing.T) {
	f := newFakeSonicServer(t)
	f.respond = func(w io.Writer, flush func(), name string) {
		if name != "audioInput" {
			return
		}
		for _, event := range novaVoiceTurn {
			writeSonicEvent(w, event)
		}
		flush()
	}
	conn := f.conn(t)
	readUntil(t, conn, "session.created")

	if err := conn.WriteMessage(realtimeTextMessage, []byte(`{"type":"session.update","session":{"instructions":"Be brief."}}`)); err != nil {
		t.Fatal(err)
	}
	readUntil(t, conn, "session.updated")
	if inputs, _ := f.received(); len(inputs) != 0 {
		t.Fatalf("session.update must not open the stream, server saw %v", inputs)
	}
	f.mu.Lock()
	early := f.credentialCalls
	f.mu.Unlock()
	if early != 0 {
		t.Fatalf("credentials must be resolved when the stream opens, not before; resolved %d times", early)
	}

	if err := conn.WriteMessage(realtimeTextMessage, []byte(`{"type":"input_audio_buffer.append","audio":"AAAA"}`)); err != nil {
		t.Fatal(err)
	}
	seen := readUntil(t, conn, "response.done")
	if seen[0] != "input_audio_buffer.committed" {
		t.Fatalf("model events = %v", seen)
	}

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.handled:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not see the stream end")
	}
	inputs, sawEnd := f.received()
	want := "sessionStart,promptStart,contentStart,textInput,contentEnd,contentStart,audioInput,contentEnd,promptEnd,sessionEnd"
	if strings.Join(inputs, ",") != want || !sawEnd {
		t.Fatalf("server inputs = %v (end frame %v), want %s and an end frame", inputs, sawEnd, want)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("reads after Close must fail")
	}
}

func TestBedrockRealtimeConnReportsStreamException(t *testing.T) {
	f := newFakeSonicServer(t)
	f.respond = func(w io.Writer, flush func(), name string) {
		if name != "audioInput" {
			return
		}
		var msg eventstream.Message
		msg.Headers.Set(":message-type", eventstream.StringValue("exception"))
		msg.Headers.Set(":exception-type", eventstream.StringValue("ValidationException"))
		msg.Payload = []byte(`{"message":"Invalid voiceId alloy"}`)
		_ = eventstream.NewEncoder().Encode(w, msg)
		flush()
		panic(http.ErrAbortHandler)
	}
	conn := f.conn(t)
	readUntil(t, conn, "session.created")
	if err := conn.WriteMessage(realtimeTextMessage, []byte(`{"type":"input_audio_buffer.append","audio":"AAAA"}`)); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil || gjson.GetBytes(data, "error.code").String() != "ValidationException" || !strings.Contains(gjson.GetBytes(data, "error.message").String(), "Invalid voiceId alloy") {
		t.Fatalf("expected a ValidationException error event, got %s (%v)", data, err)
	}
	if _, _, err := conn.ReadMessage(); !errors.Is(err, io.EOF) {
		t.Fatalf("stream must end after an exception, got %v", err)
	}
}

func TestBedrockRealtimeConnReportsRejectedRequest(t *testing.T) {
	f := newFakeSonicServer(t)
	f.reject = http.StatusForbidden
	conn := f.conn(t)
	readUntil(t, conn, "session.created")
	if err := conn.WriteMessage(realtimeTextMessage, []byte(`{"type":"input_audio_buffer.append","audio":"AAAA"}`)); err != nil {
		t.Fatalf("a rejected stream is reported in-band, not as a write error: %v", err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil || gjson.GetBytes(data, "error.type").String() != "invalid_request_error" || !strings.Contains(string(data), "not authorized") {
		t.Fatalf("expected the 403 as an error event, got %s (%v)", data, err)
	}
	if _, _, err := conn.ReadMessage(); !errors.Is(err, io.EOF) {
		t.Fatalf("stream must end after a rejected request, got %v", err)
	}
}

func TestBedrockConnectRealtimeRejectsUnsupportedSetups(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	provider := &BedrockProvider{realtimeClient: http.DefaultClient}
	for _, tc := range []struct {
		name   string
		key    schemas.Key
		intent string
		want   string
	}{
		{"transcription intent", schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{}}, "transcription", "intent=transcription"},
		{"bedrock api key", schemas.Key{Value: *schemas.NewSecretVar("ABSK-key")}, "", "requires IAM credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := provider.ConnectRealtime(ctx, tc.key, "amazon.nova-2-sonic-v1:0", tc.intent)
			if conn != nil || err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusBadRequest || !strings.Contains(err.Error.Message, tc.want) {
				t.Fatalf("got conn=%v err=%+v", conn, err)
			}
		})
	}
}

// TestBedrockRealtimeCodecCarriesClientAudio pins the handler path: client JSON is parsed into
// the canonical event, re-encoded by the provider and translated without losing the audio.
func TestBedrockRealtimeCodecCarriesClientAudio(t *testing.T) {
	event, err := schemas.ParseRealtimeEvent([]byte(`{"type":"input_audio_buffer.append","event_id":"c1","audio":"UklGRiQAAABXQVZF"}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := (&BedrockProvider{}).ToProviderRealtimeEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	upstream, _ := newSonicSession("amazon.nova-2-sonic-v1:0").clientEvent(encoded)
	if got := gjson.GetBytes(upstream[len(upstream)-1], "event.audioInput.content").String(); got != "UklGRiQAAABXQVZF" {
		t.Fatalf("audioInput content = %q", got)
	}
}

// TestBedrockRealtimeTurnHooksReadTranslatedEvents pins the provider's turn usage and output hooks on Nova output.
func TestBedrockRealtimeTurnHooksReadTranslatedEvents(t *testing.T) {
	out := feedModel(newSonicSession("amazon.nova-2-sonic-v1:0"), novaVoiceTurn...)
	done := []byte(findClientEvent(t, out, "response.done").Raw)
	provider := &BedrockProvider{}
	if usage := provider.ExtractRealtimeTurnUsage(done); usage == nil || usage.TotalTokens != 97 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	if message := provider.ExtractRealtimeTurnOutput(done); message == nil || message.Content == nil || *message.Content.ContentStr != "It is sunny." {
		t.Fatalf("unexpected turn output: %+v", message)
	}
}
