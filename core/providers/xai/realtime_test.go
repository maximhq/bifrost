package xai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TestXAIRealtimeCapabilities requires the actual optional provider contracts rather than a route-only declaration.
func TestXAIRealtimeCapabilities(t *testing.T) {
	var provider any = &XAIProvider{}
	if _, ok := provider.(schemas.RealtimeProvider); !ok {
		t.Fatal("xAI lacks the realtime transport contract")
	}
	if _, ok := provider.(schemas.RealtimeSessionProvider); !ok {
		t.Fatal("xAI lacks actual client-secret minting")
	}
}

// TestXAIRealtimeMint checks real HTTP minting, native expiry, request projection and provider failures without production credentials.
func TestXAIRealtimeMint(t *testing.T) {
	for _, status := range []int{200, 401, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, _ := io.ReadAll(r.Body)
				if r.Method != "POST" || r.URL.Path != "/v1/realtime/client_secrets" || r.Header.Get("Authorization") != "Bearer synthetic-key" || string(body) != `{"expires_after":{"seconds":123},"future":{"n":9007199254740993}}` {
					t.Error("mint request changed", r.URL, string(body))
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status == 200 {
					io.WriteString(w, `{"value":"synthetic-native-secret","expires_at":1900000000,"future":{"n":9007199254740993}}`)
				} else {
					io.WriteString(w, `{"code":"synthetic_error","error":"actual synthetic upstream failure"}`)
				}
			}))
			defer server.Close()
			provider := &XAIProvider{client: &fasthttp.Client{}, networkConfig: schemas.NetworkConfig{BaseURL: server.URL, ExtraHeaders: map[string]string{"authorization": "wrong"}}}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			out, err := provider.CreateRealtimeClientSecret(ctx, schemas.Key{Value: *schemas.NewSecretVar("synthetic-key")}, json.RawMessage(`{"session":{"type":"realtime","model":"grok-voice-latest"},"expires_after":{"seconds":123},"future":{"n":9007199254740993}}`))
			if calls != 1 {
				t.Fatal("mint replayed or did not reach provider", calls)
			}
			if status == 200 {
				if err != nil || out == nil || string(out.Body) != `{"value":"synthetic-native-secret","expires_at":1900000000,"future":{"n":9007199254740993}}` {
					t.Fatal("mint result changed", out, err)
				}
			} else if out != nil || err == nil || err.StatusCode == nil || *err.StatusCode != status {
				t.Fatal("provider failure became a secret", out, err)
			}
		})
	}
}

// TestXAIRealtimeURLHeaders checks selected-key authentication, model escaping, native transport boundaries and absence of generated settings.
func TestXAIRealtimeURLHeaders(t *testing.T) {
	provider := &XAIProvider{networkConfig: schemas.NetworkConfig{BaseURL: "https://voice.example/base/", ExtraHeaders: map[string]string{"authorization": "wrong", "X-Trace": "test"}}}
	endpoint, err := provider.RealtimeWebSocketURL(schemas.Key{}, "grok voice+test", "")
	if err != nil || endpoint != "wss://voice.example/base/v1/realtime?model=grok+voice%2Btest" {
		t.Fatal(endpoint, err)
	}
	if _, err := provider.RealtimeWebSocketURL(schemas.Key{}, "model", "transcription"); err == nil {
		t.Fatal("unsupported intent invented")
	}
	headers, err := provider.RealtimeHeaders(nil, schemas.Key{Value: *schemas.NewSecretVar("selected")})
	if err != nil || len(headers) != 2 || headers["Authorization"] != "Bearer selected" || headers["X-Trace"] != "test" {
		t.Fatal(headers, err)
	}
	if provider.SupportsRealtimeWebRTC() {
		t.Fatal("unimplemented SDP advertised")
	}
	for _, raw := range []string{`null`, `[]`, `{"session":{"type":"transcription"}}`, `{"session":{"model":"voice","tools":[]}}`} {
		if _, err := xaiRealtimeMintBody([]byte(raw)); err == nil {
			t.Fatal("unbound session settings silently discarded", raw)
		}
	}
	body, bodyErr := xaiRealtimeMintBody([]byte(`{"model":"voice"}`))
	if bodyErr != nil || string(body) != "{}" {
		t.Fatal("expiry default invented", string(body), bodyErr)
	}
}

// TestXAIRealtimeOpaqueEvents checks native payload preservation alongside recognized input, output and tool metadata.
func TestXAIRealtimeOpaqueEvents(t *testing.T) {
	provider := &XAIProvider{}
	for _, raw := range []string{
		`{"type":"synthetic.future","delta":{"n":9007199254740993},"item":["opaque"]}`,
		`{"type":"session.updated","session":{"voice":"eve","instructions":"","audio":{"input":{"format":{"type":"audio/pcm","rate":24000},"transcription":{"model":"grok-transcribe"}},"output":{"speed":1.2}},"future":9007199254740993}}`,
		`{"type":"response.output_audio.delta","delta":"AAAB","response_id":"r1"}`,
		`{"type":"response.output_text.delta","delta":"actual text","response_id":"r1"}`,
		`{"type":"response.function_call_arguments.done","name":"send_task","call_id":"call1","arguments":"{\"request\":\"read file\"}"}`,
		`{"type":"conversation.item.added","item":{"type":"function_call_output","call_id":"call1","output":"actual result","future":9007199254740993}}`,
	} {
		event, err := provider.ToBifrostRealtimeEvent(json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		if string(event.RawData) != raw {
			t.Fatal("native bytes lost", raw)
		}
		output, err := provider.ToProviderRealtimeEvent(event)
		if err != nil || string(output) != raw {
			t.Fatal("opaque event changed", raw, string(output), err)
		}
	}
	event, err := provider.ToBifrostRealtimeEvent(json.RawMessage(`{"type":"response.output_text.delta","delta":"hello"}`))
	if err != nil || event.Delta == nil || event.Delta.Text != "hello" {
		t.Fatal("recognized text unavailable", event, err)
	}
	if provider.ShouldStartRealtimeTurn(&schemas.BifrostRealtimeEvent{Type: "synthetic.future.done"}) || provider.ShouldAccumulateRealtimeOutput("conversation.item.input_audio_transcription.updated") {
		t.Fatal("unknown completion/cumulative input became a turn or delta")
	}
}

// TestXAIRealtimeCanonicalEdits preserves explicit empty values while applying canonical policy/model changes to the original native payload.
func TestXAIRealtimeCanonicalEdits(t *testing.T) {
	const raw = `{"type":"session.update","session":{"instructions":"","model":"xai/grok-voice-latest","tools":[],"audio":{"input":{"transcription":{"model":"xai/grok-transcribe"}},"output":{"speed":0}},"future":9007199254740993}}`
	event, err := schemas.ParseRealtimeEvent([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	event.Session.Model = "grok-voice-latest"
	event.Session.ExtraParams["audio"] = json.RawMessage(`{"input":{"transcription":{"model":"grok-transcribe"}},"output":{"speed":0}}`)
	out, err := (&XAIProvider{}).ToProviderRealtimeEvent(event)
	if err != nil || !bytes.Contains(out, []byte(`"instructions":""`)) || bytes.Contains(out, []byte("xai/")) || !bytes.Contains(out, []byte(`"future":9007199254740993`)) || !bytes.Contains(out, []byte(`"speed":0`)) {
		t.Fatal("canonical edits lost clears or opaque data", string(out), err)
	}
}

// TestXAIRealtimePolicyEdits ensures retained native bytes cannot restore tools or arguments changed by governance.
func TestXAIRealtimePolicyEdits(t *testing.T) {
	provider := &XAIProvider{}
	event, err := schemas.ParseRealtimeEvent([]byte(`{"type":"session.update","session":{"id":"server-id","client_secret":{"value":"synthetic"},"tools":[{"type":"function","name":"blocked"}],"future":9007199254740993}}`))
	if err != nil {
		t.Fatal(err)
	}
	event.Session.Tools = json.RawMessage(`[]`)
	out, err := provider.ToProviderRealtimeEvent(event)
	if err != nil || bytes.Contains(out, []byte("blocked")) || bytes.Contains(out, []byte("client_secret")) || bytes.Contains(out, []byte("server-id")) || !bytes.Contains(out, []byte(`"tools":[]`)) || !bytes.Contains(out, []byte("9007199254740993")) {
		t.Fatal("policy edit bypassed", string(out), err)
	}
	event, err = schemas.ParseRealtimeEvent([]byte(`{"type":"conversation.item.create","item":{"type":"function_call","name":"read","call_id":"call1","arguments":"original","future":9007199254740993}}`))
	if err != nil {
		t.Fatal(err)
	}
	event.Item.Arguments = "filtered"
	out, err = provider.ToProviderRealtimeEvent(event)
	if err != nil || bytes.Contains(out, []byte("original")) || !bytes.Contains(out, []byte(`"arguments":"filtered"`)) || !bytes.Contains(out, []byte("9007199254740993")) {
		t.Fatal("argument edit bypassed", string(out), err)
	}
}

// TestXAIRealtimeCancelledMint rejects a cancelled request before any credential can be minted.
func TestXAIRealtimeCancelledMint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("cancelled mint reached upstream")
		w.WriteHeader(500)
	}))
	defer server.Close()
	provider := &XAIProvider{client: &fasthttp.Client{ReadTimeout: time.Second}, networkConfig: schemas.NetworkConfig{BaseURL: server.URL}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.Cancel()
	out, err := provider.CreateRealtimeClientSecret(ctx, schemas.Key{Value: *schemas.NewSecretVar("synthetic")}, json.RawMessage(`{}`))
	if err == nil || out != nil {
		t.Fatal("cancelled mint returned a credential")
	}
}

// TestXAIRealtimeActiveMintCancellation closes an in-flight configured transport without returning or replaying a secret.
func TestXAIRealtimeActiveMintCancellation(t *testing.T) {
	entered, released := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-released:
		}
	}))
	defer server.Close()
	defer close(released)
	client := providerUtils.ConfigureDialer(&fasthttp.Client{ReadTimeout: 3 * time.Second}, true)
	provider := &XAIProvider{client: client, networkConfig: schemas.NetworkConfig{BaseURL: server.URL}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	done := make(chan bool, 1)
	go func() {
		out, err := provider.CreateRealtimeClientSecret(ctx, schemas.Key{Value: *schemas.NewSecretVar("synthetic")}, json.RawMessage(`{}`))
		done <- err != nil && out == nil
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("mint did not reach upstream")
	}
	ctx.Cancel()
	select {
	case rejected := <-done:
		if !rejected {
			t.Fatal("cancelled mint returned a secret")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop the configured transport")
	}
}

// TestXAIRealtimeUnknownUsage never invents zero usage from an unfamiliar terminal accounting variant.
func TestXAIRealtimeUnknownUsage(t *testing.T) {
	provider := &XAIProvider{}
	for _, raw := range []string{`{"type":"response.done","response":{}}`, `{"type":"response.done","response":{"usage":{"future_meter":12}}}`, `{"type":"synthetic.future.done","usage":{"input_tokens":10}}`} {
		if provider.ExtractRealtimeTurnUsage([]byte(raw)) != nil {
			t.Fatal("unrecognized usage became measured tokens", raw)
		}
	}
}

// TestXAIRealtimeKnownUsage reads only measurements actually supplied in synthetic compatible terminal events.
func TestXAIRealtimeKnownUsage(t *testing.T) {
	provider := &XAIProvider{}
	usage := provider.ExtractRealtimeTurnUsage([]byte(`{"type":"response.done","response":{"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`))
	if usage == nil || usage.PromptTokens != 3 || usage.CompletionTokens != 2 || usage.TotalTokens != 5 {
		t.Fatal("actual token measurements lost", usage)
	}
	duration := provider.ExtractRealtimeTurnUsage([]byte(`{"type":"response.done","usage":{"type":"duration","seconds":1.25}}`))
	if duration == nil || duration.AudioSeconds == nil || *duration.AudioSeconds != 1.25 {
		t.Fatal("actual duration lost", duration)
	}
}
