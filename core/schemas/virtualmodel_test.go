package schemas

import (
	"testing"
	"time"
)

func testVirtualModel() *VirtualModel {
	return &VirtualModel{
		Name:         "qwen38",
		Provider:     "omlx",
		Models:       []string{"Flash", "27B"},
		OnProbeError: VirtualModelErrorOnProbeError,
	}
}

func resident(states map[string]ResidencyState) *ResidencySnapshot {
	return &ResidencySnapshot{
		Provider:      "omlx",
		Authoritative: true,
		ObservedAt:    time.Now(),
		States:        states,
	}
}

func TestVirtualModelValidate(t *testing.T) {
	tests := []struct {
		name    string
		model   *VirtualModel
		wantErr bool
	}{
		{"valid", testVirtualModel(), false},
		{"nil", nil, true},
		{"empty name", &VirtualModel{Provider: "omlx", Models: []string{"a"}}, true},
		{"name with slash", &VirtualModel{Name: "a/b", Provider: "omlx", Models: []string{"a"}}, true},
		{"no provider", &VirtualModel{Name: "qwen38", Models: []string{"a"}}, true},
		{"no candidates", &VirtualModel{Name: "qwen38", Provider: "omlx"}, true},
		{"empty candidate", &VirtualModel{Name: "qwen38", Provider: "omlx", Models: []string{"a", " "}}, true},
		{"duplicate candidate", &VirtualModel{Name: "qwen38", Provider: "omlx", Models: []string{"A", "a"}}, true},
		{"name equals provider", &VirtualModel{Name: "omlx", Provider: "omlx", Models: []string{"a"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.model.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestVirtualModelMatches(t *testing.T) {
	model := testVirtualModel()
	for _, name := range []string{"qwen38", "Qwen38", " QWEN38 "} {
		if !model.Matches(name) {
			t.Errorf("Matches(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "other", "qwen"} {
		if model.Matches(name) {
			t.Errorf("Matches(%q) = true, want false", name)
		}
	}
}

// Resident beats configured order, which is the whole point: the already-loaded
// model wins so nothing resident gets evicted.
func TestSelectPrefersResidentOverOrder(t *testing.T) {
	res, err := SelectVirtualModelTarget(testVirtualModel(),
		resident(map[string]ResidencyState{"Flash": ResidencyUnloaded, "27B": ResidencyReady}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "27B" {
		t.Fatalf("Model = %q, want %q", res.Model, "27B")
	}
	if res.ObservedAt.IsZero() {
		t.Fatal("ObservedAt is zero, want the snapshot's observation time")
	}
	if res.Reason != "first resident candidate" {
		t.Fatalf("Reason = %q, want the resident-candidate path", res.Reason)
	}
}

func TestSelectOrderBreaksTies(t *testing.T) {
	res, err := SelectVirtualModelTarget(testVirtualModel(),
		resident(map[string]ResidencyState{"Flash": ResidencyReady, "27B": ResidencyReady}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "Flash" {
		t.Fatalf("Model = %q, want %q", res.Model, "Flash")
	}
}

func TestSelectJoinsLoadingBeforeColdLoading(t *testing.T) {
	res, err := SelectVirtualModelTarget(testVirtualModel(),
		resident(map[string]ResidencyState{"Flash": ResidencyUnloaded, "27B": ResidencyLoading}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "27B" {
		t.Fatalf("Model = %q, want the in-progress candidate %q", res.Model, "27B")
	}
}

func TestSelectColdLoadsFirstWhenNoneResident(t *testing.T) {
	res, err := SelectVirtualModelTarget(testVirtualModel(),
		resident(map[string]ResidencyState{"Flash": ResidencyUnloaded, "27B": ResidencyUnloaded}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "Flash" {
		t.Fatalf("Model = %q, want first candidate %q", res.Model, "Flash")
	}
}

// An unanswered probe must not read as "nothing is loaded" — that would evict
// whatever is resident. It errors, and only falls back when configured to.
func TestSelectUnansweredProbeRefusesThenFallsBackByPolicy(t *testing.T) {
	probed := &ResidencySnapshot{Provider: "omlx", States: map[string]ResidencyState{}}

	if _, err := SelectVirtualModelTarget(testVirtualModel(), probed); err == nil {
		t.Fatal("expected an error for a non-authoritative snapshot")
	}

	model := testVirtualModel()
	model.OnProbeError = VirtualModelFirstOnProbeError
	res, err := SelectVirtualModelTarget(model, probed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "Flash" {
		t.Fatalf("Model = %q, want first candidate %q", res.Model, "Flash")
	}
	if !res.ObservedAt.IsZero() {
		t.Fatalf("ObservedAt = %v, want zero for an unobserved decision", res.ObservedAt)
	}
}

// Candidates the adapter did not report stay unknown, never resident, so the choice falls through
// to the first candidate rather than treating the silence as availability.
func TestSelectTreatsAbsentCandidateAsUnknown(t *testing.T) {
	snapshot := resident(map[string]ResidencyState{"Flash": ResidencyUnloaded})
	res, err := SelectVirtualModelTarget(testVirtualModel(), snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := snapshot.StateOf("27B"); got != ResidencyUnknown {
		t.Fatalf("absent candidate state = %q, want %q", got, ResidencyUnknown)
	}
	if res.Model != "Flash" {
		t.Fatalf("Model = %q, want the first candidate, not the unreported one", res.Model)
	}
}

func TestSelectNilSnapshotAndEmptyCandidates(t *testing.T) {
	if _, err := SelectVirtualModelTarget(testVirtualModel(), nil); err == nil {
		t.Fatal("expected an error for a nil snapshot")
	}
	if _, err := SelectVirtualModelTarget(&VirtualModel{Name: "x", Provider: "omlx"}, nil); err == nil {
		t.Fatal("expected an error for a virtual model with no candidates")
	}
}

// A reader that keyed its entries the way the backend wrote them, rather than
// lowercased, must still resolve. Without this the miss is silent and reads as an idle
// GPU, so the gateway refuses or cold-loads a model that is plainly resident.
func TestStateOfIsCaseInsensitive(t *testing.T) {
	snapshot := &ResidencySnapshot{
		Provider:      "omlx",
		Authoritative: true,
		States:        map[string]ResidencyState{"Qwen3.8-Flash-Next-oQ4e-mtp": ResidencyReady},
	}
	if got := snapshot.StateOf("qwen3.8-flash-next-oq4e-mtp"); got != ResidencyReady {
		t.Fatalf("StateOf(lowercase) = %q, want %q", got, ResidencyReady)
	}
	model := &VirtualModel{Name: "qwen38", Provider: "omlx", Models: []string{"qwen3.8-flash-next-oq4e-mtp"}, OnProbeError: VirtualModelErrorOnProbeError}
	res, err := SelectVirtualModelTarget(model, snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "qwen3.8-flash-next-oq4e-mtp" {
		t.Fatalf("Model = %q, want the resident candidate", res.Model)
	}
}

// A reader that keyed an entry the way the backend wrote it, bypassing SetState, is reached by the
// fallback scan: neither the exact key nor the lowercase form of the candidate matches it.
func TestStateOfScansAKeyStoredUnnormalized(t *testing.T) {
	snapshot := &ResidencySnapshot{Provider: "llama.cpp", States: map[string]ResidencyState{"Flash": ResidencyReady}}
	if got := snapshot.StateOf("flash"); got != ResidencyReady {
		t.Fatalf("StateOf = %q, want the scan to find the entry stored as %q", got, "Flash")
	}
	if got := snapshot.StateOf("Flash"); got != ResidencyReady {
		t.Fatalf("StateOf = %q, want the exact-key hit", got)
	}
	if got := snapshot.StateOf("nothing"); got != ResidencyUnknown {
		t.Fatalf("StateOf = %q, want unknown for a candidate nobody reported", got)
	}
}

func TestSetStateNormalizesKeys(t *testing.T) {
	snapshot := &ResidencySnapshot{Provider: "ollama"}
	snapshot.SetState("  Qwen3:30B ", ResidencyReady)
	if got := snapshot.StateOf("qwen3:30b"); got != ResidencyReady {
		t.Fatalf("StateOf = %q, want %q", got, ResidencyReady)
	}
	if _, ok := snapshot.States["qwen3:30b"]; !ok {
		t.Fatalf("SetState stored the key unnormalized: %v", snapshot.States)
	}
}

func TestParseResidencyAdapter(t *testing.T) {
	for _, raw := range []string{"", "  ", "auto", "OLLAMA", " llamacpp ", "omlx", "Custom"} {
		if _, err := ParseResidencyAdapter(raw); err != nil {
			t.Fatalf("ParseResidencyAdapter(%q) = %v, want it accepted", raw, err)
		}
	}
	if got, err := ParseResidencyAdapter(""); err != nil || got != ResidencyAdapterAuto {
		t.Fatalf("an omitted adapter = (%q, %v), want auto", got, err)
	}
	for _, raw := range []string{"lmstudio", "vllm", "definitely-not-an-adapter"} {
		if got, err := ParseResidencyAdapter(raw); err == nil {
			t.Fatalf("ParseResidencyAdapter(%q) = %q, want it refused", raw, got)
		}
	}
}

func TestNormalizeModelErrorPolicy(t *testing.T) {
	if got := NormalizeModelErrorPolicy(" FIRST "); got != VirtualModelFirstOnProbeError {
		t.Fatalf("NormalizeModelErrorPolicy(\" FIRST \") = %q, want %q", got, VirtualModelFirstOnProbeError)
	}
	for _, raw := range []string{"", "error", "nonsense"} {
		if got := NormalizeModelErrorPolicy(raw); got != VirtualModelErrorOnProbeError {
			t.Fatalf("NormalizeModelErrorPolicy(%q) = %q, want %q", raw, got, VirtualModelErrorOnProbeError)
		}
	}
}

// Production shape: readers store keys lowercased and candidate models are usually written in
// lowercase, so each lookup is an exact hit and selection allocates only the resolution itself.
func BenchmarkSelectVirtualModelTarget(b *testing.B) {
	model := &VirtualModel{Name: "qwen38", Provider: "omlx", Models: []string{"flash", "27b"}, OnProbeError: VirtualModelErrorOnProbeError}
	states := resident(map[string]ResidencyState{"flash": ResidencyUnloaded, "27b": ResidencyReady})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := SelectVirtualModelTarget(model, states); err != nil {
			b.Fatal(err)
		}
	}
}

// Keys written the way a reader that bypassed SetState would write them, so neither lookup is an
// exact hit: "FLASH" is reachable only through the EqualFold scan, "27b" only after normalizing the
// candidate. That is what a snapshot keyed in mixed case costs per selection.
func BenchmarkSelectVirtualModelTargetCaseFolded(b *testing.B) {
	model := testVirtualModel()
	states := resident(map[string]ResidencyState{"FLASH": ResidencyUnloaded, "27b": ResidencyReady})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := SelectVirtualModelTarget(model, states); err != nil {
			b.Fatal(err)
		}
	}
}
