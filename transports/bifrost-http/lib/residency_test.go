package lib

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
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
	// /api/ps is the complete resident set; absence means unloaded.
	if got := populated.StateOf("llama3:8b"); got != schemas.ResidencyUnloaded {
		t.Fatalf("absent candidate state = %q, want %q", got, schemas.ResidencyUnloaded)
	}
	if got := empty.StateOf("llama3:8b"); got != schemas.ResidencyUnloaded {
		t.Fatalf("empty process list state = %q, want unloaded", got)
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

// A provider that authenticates with its own scheme keeps it. Replacing extra_headers with a
// bearer fails every probe against that backend, and on the default on_probe_error of error every
// virtual-model request then answers 502 for a cause the operator cannot see in their own config.
func TestProbeKeepsOperatorAuthorizationHeader(t *testing.T) {
	network := func(headers map[string]string) *schemas.NetworkConfig {
		extra := map[string]schemas.SecretVar{}
		for k, v := range headers {
			extra[k] = *schemas.NewSecretVar(v)
		}
		return &schemas.NetworkConfig{
			BaseURL:             "http://127.0.0.1:1",
			AllowPrivateNetwork: true,
			ExtraHeaders:        extra,
		}
	}
	keys := []schemas.Key{{Value: *schemas.NewSecretVar("sk-local")}}

	kept := buildProbeTarget(t, configstore.ProviderConfig{
		NetworkConfig: network(map[string]string{"authorization": "Custom op-token"}),
		Keys:          keys,
	})
	if got := kept.headers["authorization"]; got != "Custom op-token" {
		t.Fatalf("operator authorization = %q, want it preserved untouched", got)
	}
	if got := kept.headers["Authorization"]; got != "" {
		t.Fatalf("a second Authorization header appeared: %q", got)
	}

	filled := buildProbeTarget(t, configstore.ProviderConfig{
		NetworkConfig: network(nil),
		Keys:          keys,
	})
	if got := filled.headers["Authorization"]; got != "Bearer sk-local" {
		t.Fatalf("authorization = %q, want the provider key as a bearer", got)
	}
}

func buildProbeTarget(t *testing.T, providerCfg configstore.ProviderConfig) *residencyTarget {
	t.Helper()
	target, err := buildResidencyTarget("omlx", providerCfg)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	return target
}

// With no virtual models configured the service is never installed, and a nil pointer sitting in
// the VirtualModelResolver interface compares non-nil - which is the trap, since core checks
// != nil and would then call through it on every request. The accessor must hand back a real nil.
func TestVirtualModelResolverIsNilWhenNothingIsConfigured(t *testing.T) {
	var absent *Config
	if absent.VirtualModelResolver() != nil {
		t.Fatal("nil config returned a resolver")
	}

	cfg := &Config{}
	trap := schemas.VirtualModelResolver(cfg.VirtualModels)
	if trap == nil {
		t.Fatal("control failed: a nil pointer placed in an interface is non-nil, which is the bug being guarded")
	}
	if _, ok := trap.Lookup("anything"); ok {
		t.Fatal("control failed: the typed-nil resolver claimed to know a model")
	}

	if got := cfg.VirtualModelResolver(); got != nil {
		t.Fatalf("unconfigured gateway returned a non-nil resolver: %v", got)
	}

	service := NewVirtualModelService(nil, nil)
	if err := service.Install(map[string]schemas.VirtualModel{
		"local-chat": {Name: "local-chat", Provider: "omlx", Models: []string{"flash"}},
	}, map[schemas.ModelProvider]configstore.ProviderConfig{
		"omlx": {NetworkConfig: &schemas.NetworkConfig{BaseURL: "http://127.0.0.1:1", AllowPrivateNetwork: true}},
	}); err != nil {
		t.Fatalf("install failed: %v", err)
	}
	cfg.VirtualModels = service
	if cfg.VirtualModelResolver() == nil {
		t.Fatal("a configured gateway returned no resolver")
	}
}

// The service is reached through an interface, so a nil receiver has to read as "no virtual
// models" rather than panic: that is the ordinary state of a gateway that does not use this.
func TestNilVirtualModelServiceIsInert(t *testing.T) {
	var service *VirtualModelService
	if service.Enabled() {
		t.Fatal("nil service reported enabled")
	}
	if vm, ok := service.Lookup("local-chat"); ok || vm != nil {
		t.Fatalf("nil service resolved a model: %v %v", vm, ok)
	}
	if got := service.List(); got != nil {
		t.Fatalf("nil service listed %v", got)
	}
}

// loadProviders stores every provider under a lowercased key, so a virtual model that names
// "Local-OMLX" refers to a provider that is present. Startup must not refuse the gateway for the
// case of a letter, and the entry has to keep the normalized form so the probe target, the owned_by
// in listings and the routing comparison all agree with the rest of the stack.
func TestInstallNormalizesProviderCase(t *testing.T) {
	service := NewVirtualModelService(nil, nil)
	err := service.Install(map[string]schemas.VirtualModel{
		"local-chat": {Name: "local-chat", Provider: "Local-OMLX", Models: []string{"flash"}},
	}, map[schemas.ModelProvider]configstore.ProviderConfig{
		"local-omlx": {NetworkConfig: &schemas.NetworkConfig{BaseURL: "http://127.0.0.1:1", AllowPrivateNetwork: true}},
	})
	if err != nil {
		t.Fatalf("mixed-case provider reference was refused: %v", err)
	}

	vm, ok := service.Lookup("local-chat")
	if !ok {
		t.Fatal("virtual model not installed")
	}
	if vm.Provider != "local-omlx" {
		t.Fatalf("provider kept as %q, want the normalized local-omlx", vm.Provider)
	}

	// A reference to a provider that genuinely does not exist is still refused.
	if err := service.Install(map[string]schemas.VirtualModel{
		"local-chat": {Name: "local-chat", Provider: "Not-Configured", Models: []string{"flash"}},
	}, map[schemas.ModelProvider]configstore.ProviderConfig{
		"local-omlx": {NetworkConfig: &schemas.NetworkConfig{BaseURL: "http://127.0.0.1:1", AllowPrivateNetwork: true}},
	}); err == nil {
		t.Fatal("unknown provider was accepted")
	}
}

// Residency is configured under network_config, next to the base_url it probes.
func TestProbeTargetReadsResidencyFromNetworkConfig(t *testing.T) {
	target := buildProbeTarget(t, configstore.ProviderConfig{
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:             "http://127.0.0.1:1",
			AllowPrivateNetwork: true,
			ModelResidency: &schemas.ModelResidencyConfig{
				Adapter:    schemas.ResidencyAdapterCustom,
				Path:       "/internal/serving",
				TimeoutMS:  1500,
				CacheTTLMS: 250,
				Fields:     &schemas.ResidencyFieldMap{Items: "models", ID: "name"},
			},
		},
	})
	if target.adapter != schemas.ResidencyAdapterCustom || target.path != "/internal/serving" {
		t.Fatalf("residency was not read from network_config: adapter=%q path=%q", target.adapter, target.path)
	}
	if target.timeout != 1500*time.Millisecond || target.ttl != 250*time.Millisecond {
		t.Fatalf("timeout/ttl = %v/%v, want 1.5s/250ms", target.timeout, target.ttl)
	}

	// Unset stays auto-detect, and the derived windows sit at their floors.
	def := buildProbeTarget(t, configstore.ProviderConfig{
		NetworkConfig: &schemas.NetworkConfig{BaseURL: "http://127.0.0.1:1", AllowPrivateNetwork: true},
	})
	if def.adapter != schemas.ResidencyAdapterAuto {
		t.Fatalf("default adapter = %q, want auto", def.adapter)
	}
	if def.failBackoff != residencyDefaultFailBackoff || def.staleCap != residencyMinStaleAge {
		t.Fatalf("defaults: failBackoff=%v staleCap=%v", def.failBackoff, def.staleCap)
	}

	// A long cache window moves both windows with it, above the stale floor.
	long := buildProbeTarget(t, configstore.ProviderConfig{
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:             "http://127.0.0.1:1",
			AllowPrivateNetwork: true,
			ModelResidency:      &schemas.ModelResidencyConfig{CacheTTLMS: 10000},
		},
	})
	if long.failBackoff != 10*time.Second || long.staleCap != 100*time.Second {
		t.Fatalf("with a 10s ttl: failBackoff=%v staleCap=%v, want 10s/100s", long.failBackoff, long.staleCap)
	}

	// Below the floor the floor wins: ten windows of a short ttl would believe nothing.
	short := buildProbeTarget(t, configstore.ProviderConfig{
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:             "http://127.0.0.1:1",
			AllowPrivateNetwork: true,
			ModelResidency:      &schemas.ModelResidencyConfig{CacheTTLMS: 5000},
		},
	})
	if short.failBackoff != 5*time.Second || short.staleCap != residencyMinStaleAge {
		t.Fatalf("with a 5s ttl: failBackoff=%v staleCap=%v, want 5s and the %v floor", short.failBackoff, short.staleCap, residencyMinStaleAge)
	}
}

// The probe is shared, so the caller that happened to arrive first must not be able to kill it for
// everyone else by disconnecting. Before WithoutCancel the probe inherited that caller's context.
func TestSharedProbeSurvivesCallerDisconnect(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[{"id":"flash","loaded":true,"is_loading":false}]}`)
	}))
	defer server.Close()

	service := NewVirtualModelService(nil, nil)
	providers := map[schemas.ModelProvider]configstore.ProviderConfig{
		"omlx": {NetworkConfig: &schemas.NetworkConfig{
			BaseURL:             server.URL,
			AllowPrivateNetwork: true,
			ModelResidency:      &schemas.ModelResidencyConfig{Adapter: schemas.ResidencyAdapterOMLX},
		}},
	}
	if err := service.Install(map[string]schemas.VirtualModel{
		"local-chat": {Name: "local-chat", Provider: "omlx", Models: []string{"flash"}},
	}, providers); err != nil {
		t.Fatalf("install: %v", err)
	}
	target := service.installedRegistry().entries["local-chat"].target

	owner := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	waiter := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer owner.Cancel()
	defer waiter.Cancel()

	ownerDone := make(chan *schemas.ResidencySnapshot, 1)
	go func() { ownerDone <- service.snapshot(owner, target) }()
	<-entered

	waiterDone := make(chan *schemas.ResidencySnapshot, 1)
	go func() { waiterDone <- service.snapshot(waiter, target) }()
	time.Sleep(50 * time.Millisecond) // let the waiter join the in-flight probe

	owner.Cancel() // the client that started the probe goes away
	close(release)

	got := <-waiterDone
	if !got.Authoritative {
		t.Fatalf("the shared probe died with the caller that started it: %+v", got)
	}
	if state := got.StateOf("flash"); state != schemas.ResidencyReady {
		t.Fatalf("resident model state = %q, want ready", state)
	}
	<-ownerDone // the disconnected caller is served too; it just does not get to cancel
}

// A dead backend must cost one probe per backoff window, not one per request: otherwise every
// virtual-model request waits out the full timeout on a call that is already known to fail.
func TestFailedProbeIsRemembered(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	service := NewVirtualModelService(nil, nil)
	target := buildProbeTarget(t, configstore.ProviderConfig{
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:             server.URL,
			AllowPrivateNetwork: true,
			ModelResidency:      &schemas.ModelResidencyConfig{Adapter: schemas.ResidencyAdapterOMLX},
		},
	})

	first := service.snapshot(schemas.NewBifrostContext(nil, schemas.NoDeadline), target)
	if first.Authoritative {
		t.Fatal("a backend that failed every reader must not be authoritative")
	}

	start := time.Now()
	second := service.snapshot(schemas.NewBifrostContext(nil, schemas.NoDeadline), target)
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("second request waited %v on a probe whose failure was already known", elapsed)
	}
	if second.Authoritative {
		t.Fatal("the backoff window answered authoritative")
	}
	if hits != 1 {
		t.Fatalf("backend saw %d probes within the backoff window, want 1", hits)
	}
}

// Beyond the stale window an old reading is not a fact about the present, so it must stop gating
// routing. The stored snapshot is left alone, so the next success restores authority.
func TestStaleReadingIsNotAuthoritativeBeyondTheCap(t *testing.T) {
	target := &residencyTarget{provider: "omlx", staleCap: time.Minute}
	now := time.Now()

	fresh := &schemas.ResidencySnapshot{Provider: "omlx", ObservedAt: now.Add(-10 * time.Second), Authoritative: true}
	if got := unknownAfterFailure(fresh, now, target, nil); !got.Authoritative {
		t.Fatal("a reading inside the cap lost its authority")
	}

	aged := &schemas.ResidencySnapshot{Provider: "omlx", ObservedAt: now.Add(-5 * time.Minute), Authoritative: true}
	got := unknownAfterFailure(aged, now, target, nil)
	if got.Authoritative {
		t.Fatal("a reading from before the outage is still authoritative")
	}
	if !got.ObservedAt.Equal(aged.ObservedAt) {
		t.Fatalf("observation time = %v, want the original %v", got.ObservedAt, aged.ObservedAt)
	}
	if !aged.Authoritative {
		t.Fatal("the stored snapshot was mutated; the next success must still be believed")
	}
}

// Provider edits land after boot, so the probe endpoints have to follow: a rotated key left every
// probe answering 401 until restart, and a deleted provider kept being routed to.
// Deleting a provider must not cost the definition. The registry holds the only copy, so dropping
// the entry would strand the name until a restart even after the provider came back, and deleting
// then re-creating a provider is a normal way to fix a bad config through the UI.
func TestRefreshRebuildsProbeTargetsAndSurvivesADeletedProvider(t *testing.T) {
	providers := map[schemas.ModelProvider]configstore.ProviderConfig{
		"omlx": {
			NetworkConfig: &schemas.NetworkConfig{BaseURL: "http://127.0.0.1:1", AllowPrivateNetwork: true},
			Keys:          []schemas.Key{{Value: *schemas.NewSecretVar("sk-old")}},
		},
	}
	service := NewVirtualModelService(nil, nil)
	if err := service.Install(map[string]schemas.VirtualModel{
		"local-chat": {Name: "local-chat", Provider: "omlx", Models: []string{"flash"}},
	}, providers); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got := service.installedRegistry().entries["local-chat"].target.headers["Authorization"]; got != "Bearer sk-old" {
		t.Fatalf("initial probe credential = %q, want Bearer sk-old", got)
	}

	providers["omlx"] = configstore.ProviderConfig{
		NetworkConfig: &schemas.NetworkConfig{BaseURL: "http://127.0.0.1:2", AllowPrivateNetwork: true},
		Keys:          []schemas.Key{{Value: *schemas.NewSecretVar("sk-rotated")}},
	}
	service.Refresh(providers)

	after := service.installedRegistry().entries["local-chat"].target
	if after.baseURL != "http://127.0.0.1:2" {
		t.Fatalf("base URL after refresh = %q, want the edited one", after.baseURL)
	}
	if got := after.headers["Authorization"]; got != "Bearer sk-rotated" {
		t.Fatalf("probe credential after rotation = %q, want Bearer sk-rotated", got)
	}

	service.Refresh(map[schemas.ModelProvider]configstore.ProviderConfig{})
	vm, ok := service.Lookup("local-chat")
	if !ok {
		t.Fatal("the definition was lost with its provider: the name no longer resolves at all")
	}
	if service.installedRegistry().entries["local-chat"].target != nil {
		t.Fatal("the endpoint of a deleted provider is still installed")
	}
	if _, err := service.Resolve(schemas.NewBifrostContext(nil, schemas.NoDeadline), vm); err == nil || *err.StatusCode != 503 {
		t.Fatalf("resolving a virtual model with no provider = %v, want a 503", err)
	}
	if _, snapshot, err := service.MemberStatus(schemas.NewBifrostContext(nil, schemas.NoDeadline), "local-chat"); err == nil || snapshot != nil {
		t.Fatalf("member status with no endpoint = (snapshot %v, err %v), want an error and no snapshot", snapshot, err)
	}

	service.Refresh(providers)
	restored := service.installedRegistry().entries["local-chat"].target
	if restored == nil || restored.baseURL != "http://127.0.0.1:2" {
		t.Fatalf("endpoint after the provider was re-added = %+v, want it rebuilt without a restart", restored)
	}
}

// An edit to one provider must not reset what the others earned: without the fingerprint check every
// provider save would clear every backoff and re-run every adapter detection pass on the box.
func TestRefreshKeepsStateForProvidersTheEditDidNotTouch(t *testing.T) {
	var untouchedHits, editedHits atomic.Int32
	untouched := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		untouchedHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer untouched.Close()
	edited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editedHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer edited.Close()

	provider := func(url string, headers map[string]schemas.SecretVar) configstore.ProviderConfig {
		return configstore.ProviderConfig{NetworkConfig: &schemas.NetworkConfig{
			BaseURL:             url,
			AllowPrivateNetwork: true,
			ExtraHeaders:        headers,
			ModelResidency:      &schemas.ModelResidencyConfig{Adapter: schemas.ResidencyAdapterOMLX, CacheTTLMS: 30000},
		}}
	}

	service := NewVirtualModelService(nil, nil)
	if err := service.Install(map[string]schemas.VirtualModel{
		"untouched": {Name: "untouched", Provider: "omlx", Models: []string{"flash"}},
		"edited":    {Name: "edited", Provider: "llamacpp", Models: []string{"flash"}},
	}, map[schemas.ModelProvider]configstore.ProviderConfig{
		"omlx":     provider(untouched.URL, nil),
		"llamacpp": provider(edited.URL, nil),
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()

	for _, name := range []string{"untouched", "edited"} {
		vm, _ := service.Lookup(name)
		if _, err := service.Resolve(ctx, vm); err == nil {
			t.Fatalf("%s resolved against a backend that fails every probe", name)
		}
	}
	if untouchedHits.Load() != 1 || editedHits.Load() != 1 {
		t.Fatalf("initial probes: untouched=%d edited=%d, want one each", untouchedHits.Load(), editedHits.Load())
	}

	// A save that changes only llamacpp, handing over omlx's config as a fresh but identical value
	// the way the caller does after any provider edit.
	service.Refresh(map[schemas.ModelProvider]configstore.ProviderConfig{
		"omlx":     provider(untouched.URL, nil),
		"llamacpp": provider(edited.URL, map[string]schemas.SecretVar{"X-Trace": *schemas.NewSecretVar("1")}),
	})

	for _, name := range []string{"untouched", "edited"} {
		vm, _ := service.Lookup(name)
		if _, err := service.Resolve(ctx, vm); err == nil {
			t.Fatalf("%s resolved against a backend that still fails every probe", name)
		}
	}
	if got := untouchedHits.Load(); got != 1 {
		t.Fatalf("another provider's edit cost this endpoint %d extra probe(s): the remembered failure was dropped", got-1)
	}
	if got := editedHits.Load(); got != 2 {
		t.Fatalf("the endpoint that was edited saw %d probes, want its backoff cleared and a fresh one sent", got)
	}
}

// The probe slot is keyed by provider and base URL, so a rotated credential lands on the slot the
// old key dirtied. Unless the remembered failure is cleared, fixing a 401 is not believed until
// that window runs out: with a 30s cache the first working probe is 30 seconds late.
func TestRefreshClearsAFailureRememberedUnderTheOldKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-new" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"models":[{"name":"flash"}]}`)
	}))
	defer server.Close()

	providerWith := func(key string) configstore.ProviderConfig {
		return configstore.ProviderConfig{
			NetworkConfig: &schemas.NetworkConfig{
				BaseURL:             server.URL,
				AllowPrivateNetwork: true,
				ModelResidency:      &schemas.ModelResidencyConfig{Adapter: schemas.ResidencyAdapterOllama, CacheTTLMS: 30000},
			},
			Keys: []schemas.Key{{Value: *schemas.NewSecretVar(key)}},
		}
	}

	service := NewVirtualModelService(nil, nil)
	if err := service.Install(map[string]schemas.VirtualModel{
		"local-chat": {Name: "local-chat", Provider: "omlx", Models: []string{"flash"}},
	}, map[schemas.ModelProvider]configstore.ProviderConfig{"omlx": providerWith("sk-old")}); err != nil {
		t.Fatalf("install: %v", err)
	}
	vm, _ := service.Lookup("local-chat")
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()

	if _, err := service.Resolve(ctx, vm); err == nil || *err.StatusCode != 502 {
		t.Fatalf("a probe made with the stale credential = %v, want the 502 the default policy gives", err)
	}

	service.Refresh(map[schemas.ModelProvider]configstore.ProviderConfig{"omlx": providerWith("sk-new")})

	resolution, err := service.Resolve(ctx, vm)
	if err != nil {
		t.Fatalf("the rotated credential was refused for the whole window the old key earned: %v", err)
	}
	if resolution.Model != "flash" {
		t.Fatalf("resolved model = %q, want flash", resolution.Model)
	}
}
