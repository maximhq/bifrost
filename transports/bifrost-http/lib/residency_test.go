package lib

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// The fixtures here are the shapes recorded from a live oMLX, llama.cpp router and
// ollama sharing one Apple Silicon box, including the parts that make the readers
// differ: oMLX carries a boolean per model, llama.cpp carries a status object, and
// ollama's OpenAI surface carries nothing at all so /api/ps must be read instead.

func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	body := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}
	return body
}

func newSnapshot() *schemas.ResidencySnapshot {
	return &schemas.ResidencySnapshot{Provider: "omlx", States: map[string]schemas.ResidencyState{}}
}

func TestParseStatusListOMLX(t *testing.T) {
	// Field names and the loaded/is_loading pair are what oMLX actually returns.
	body := decode(t, `{"model_count":6,"loaded_count":1,"final_ceiling":176639860712,
		"current_model_memory":111609397878,"models":[
			{"id":"Qwen3.8-Flash-Next-oQ4e-mtp","model_alias":"qwen38-flash","loaded":true,"is_loading":false,"engine_type":"mlx","model_context_length":262144},
			{"id":"Qwen3.8-27B-oQ8e-fp16-mtp","model_alias":"qwen38-27b","loaded":false,"is_loading":false,"model_context_length":131072}]}`)

	snapshot := newSnapshot()
	if err := parseStatusList(body, &residencyTarget{provider: "omlx"}, snapshot); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !snapshot.Authoritative {
		t.Fatal("snapshot is not authoritative, want authoritative")
	}
	if got := snapshot.StateOf("Qwen3.8-Flash-Next-oQ4e-mtp"); got != schemas.ResidencyReady {
		t.Fatalf("loaded model state = %q, want %q", got, schemas.ResidencyReady)
	}
	if got := snapshot.StateOf("Qwen3.8-27B-oQ8e-fp16-mtp"); got != schemas.ResidencyUnloaded {
		t.Fatalf("unloaded model state = %q, want %q", got, schemas.ResidencyUnloaded)
	}
	// Aliases are registered too, so a candidate may name either form.
	if got := snapshot.StateOf("qwen38-flash"); got != schemas.ResidencyReady {
		t.Fatalf("alias state = %q, want %q", got, schemas.ResidencyReady)
	}
}

func TestParseStatusListLlamaCPP(t *testing.T) {
	// llama.cpp router puts residency in a nested status object.
	body := decode(t, `{"object":"list","data":[
		{"id":"snowflake-arctic-embed-l-v2.0","object":"model","status":{"value":"loaded","args":["--host","0.0.0.0"],"preset":"x"}},
		{"id":"Qwen3.8-Flash-Next:Q4_K_XL","object":"model","status":{"value":"unloaded","args":["--host","0.0.0.0"],"preset":"y"}}]}`)

	snapshot := newSnapshot()
	if err := parseStatusList(body, &residencyTarget{provider: "llamacpp"}, snapshot); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := snapshot.StateOf("snowflake-arctic-embed-l-v2.0"); got != schemas.ResidencyReady {
		t.Fatalf("state = %q, want %q", got, schemas.ResidencyReady)
	}
	if got := snapshot.StateOf("Qwen3.8-Flash-Next:Q4_K_XL"); got != schemas.ResidencyUnloaded {
		t.Fatalf("state = %q, want %q", got, schemas.ResidencyUnloaded)
	}
}

// An empty ollama /api/ps is a real answer, not a failed probe: it means nothing is
// loaded. Treating it as unreadable would refuse to route instead of cold-loading.
func TestParseOllamaProcesses(t *testing.T) {
	empty := newSnapshot()
	if err := parseOllamaProcesses(decode(t, `{"models":[]}`), &residencyTarget{}, empty); err != nil {
		t.Fatalf("unexpected error on an empty process list: %v", err)
	}
	if !empty.Authoritative {
		t.Fatal("an empty process list must still be authoritative")
	}

	populated := newSnapshot()
	body := decode(t, `{"models":[{"name":"qwen3:30b","model":"qwen3:30b","size_vram":31205679104,"expires_at":"2026-10-14T12:00:00Z"}]}`)
	if err := parseOllamaProcesses(body, &residencyTarget{}, populated); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := populated.StateOf("qwen3:30b"); got != schemas.ResidencyReady {
		t.Fatalf("state = %q, want %q", got, schemas.ResidencyReady)
	}
	// A candidate the list does not mention stays unknown rather than becoming an
	// affirmative "not loaded": this reader only enumerates what is resident, so it has
	// no standing to deny the rest. The selector still cold-loads the first candidate
	// from an authoritative snapshot, so the decision is the same - it is the reasoning
	// that stays honest.
	if got := populated.StateOf("llama3:8b"); got != schemas.ResidencyUnknown {
		t.Fatalf("absent candidate state = %q, want %q", got, schemas.ResidencyUnknown)
	}
}

func TestParseCustomBothShapes(t *testing.T) {
	// With loaded/ loading mapped it is read as a status endpoint.
	status := newSnapshot()
	target := &residencyTarget{fields: &schemas.ResidencyFieldMap{Items: "data.models", ID: "name", Loaded: "in_memory", Loading: "warming"}}
	body := decode(t, `{"data":{"models":[{"name":"a","in_memory":true},{"name":"b","warming":true}]}}`)
	if err := parseCustom(body, target, status); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := status.StateOf("a"); got != schemas.ResidencyReady {
		t.Fatalf("a = %q, want %q", got, schemas.ResidencyReady)
	}
	if got := status.StateOf("b"); got != schemas.ResidencyLoading {
		t.Fatalf("b = %q, want %q", got, schemas.ResidencyLoading)
	}

	// With only items and id, the list itself is what is resident.
	members := newSnapshot()
	membersTarget := &residencyTarget{fields: &schemas.ResidencyFieldMap{Items: "items", ID: "model"}}
	if err := parseCustom(decode(t, `{"items":[{"model":"x"}]}`), membersTarget, members); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := members.StateOf("x"); got != schemas.ResidencyReady {
		t.Fatalf("x = %q, want %q", got, schemas.ResidencyReady)
	}

	// An unmapped path reports the miss rather than inventing an empty-but-answered
	// snapshot, which would read as "nothing loaded" and evict what is resident.
	missing := newSnapshot()
	missingTarget := &residencyTarget{fields: &schemas.ResidencyFieldMap{Items: "nope.there", ID: "name"}}
	if err := parseCustom(decode(t, `{"other":[]}`), missingTarget, missing); err == nil {
		t.Fatal("expected an error when the mapped path does not exist")
	}
}

// Auto-detection must fall through to the reader that actually answers, and stop there.
// The order matters: ollama is last because a machine with nothing loaded answers it
// with an empty list, which would otherwise win and mask the other engines.
func TestProbeResidencyAutoDetectFallsThrough(t *testing.T) {
	var hits []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		switch r.URL.Path {
		case "/v1/models/status":
			w.WriteHeader(http.StatusNotFound)
			return
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"model-a","status":{"value":"loaded"}}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	target := &residencyTarget{
		provider:     "custom-local",
		adapter:      schemas.ResidencyAdapterAuto,
		baseURL:      server.URL,
		timeout:      2 * time.Second,
		allowPrivate: true,
	}

	snapshot, adapter, err := probeResidency(t.Context(), target, "", http.DefaultClient)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adapter != schemas.ResidencyAdapterLlamaCPP {
		t.Fatalf("adapter = %q, want %q", adapter, schemas.ResidencyAdapterLlamaCPP)
	}
	if got := snapshot.StateOf("model-a"); got != schemas.ResidencyReady {
		t.Fatalf("state = %q, want %q", got, schemas.ResidencyReady)
	}

	// Once a reader has answered, the next probe goes straight to it.
	hits = nil
	if _, adapter, err := probeResidency(t.Context(), target, adapter, http.DefaultClient); err != nil {
		t.Fatalf("unexpected error on the second probe: %v", err)
	} else if adapter != schemas.ResidencyAdapterLlamaCPP {
		t.Fatalf("adapter = %q, want %q", adapter, schemas.ResidencyAdapterLlamaCPP)
	}
	if len(hits) != 1 || hits[0] != "/v1/models" {
		t.Fatalf("second probe hit %v, want only /v1/models", hits)
	}
}

// An endpoint that answers nothing must yield a probe error, which the snapshot layer
// turns into a non-authoritative reading and the selector refuses to cold-load from.
func TestProbeResidencyAllReadersFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	target := &residencyTarget{
		provider:     "dead",
		adapter:      schemas.ResidencyAdapterAuto,
		baseURL:      server.URL,
		timeout:      2 * time.Second,
		allowPrivate: true,
	}
	if _, _, err := probeResidency(t.Context(), target, "", http.DefaultClient); err == nil {
		t.Fatal("expected an error when every reader fails")
	}
}
