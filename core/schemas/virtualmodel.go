package schemas

import (
	"fmt"
	"strings"
	"time"
)

// Virtual models decouple the model name a client sends from the model resident on
// a backend: the client names the virtual model, Bifrost picks the candidate the
// backend already has in memory. Unlike key aliases and routing rules, which map a
// name to a target known when the config was written, the target here is only known
// at request time.
//
// Core holds the vocabulary and the selection decision, and no HTTP. The engine
// probes (ollama /api/ps, llama.cpp /v1/models, oMLX /v1/models/status) live behind
// VirtualModelResolver, implemented by the transport.

// ResidencyAdapter names the reader for a backend's residency. Auto tries the known
// engines once per provider and remembers which answered, so an operator on a stock
// oMLX or llama.cpp endpoint configures nothing.
type ResidencyAdapter string

const (
	ResidencyAdapterAuto     ResidencyAdapter = "auto"
	ResidencyAdapterOllama   ResidencyAdapter = "ollama"
	ResidencyAdapterLlamaCPP ResidencyAdapter = "llamacpp"
	ResidencyAdapterOMLX     ResidencyAdapter = "omlx"
	ResidencyAdapterCustom   ResidencyAdapter = "custom"
)

// NormalizeResidencyAdapter maps an unvalidated config string to a known adapter,
// defaulting to auto. An unrecognized name is not silently ignored: it falls back to
// auto, which reports unknown rather than pretending a model is resident.
func NormalizeResidencyAdapter(raw string) ResidencyAdapter {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(ResidencyAdapterOllama):
		return ResidencyAdapterOllama
	case string(ResidencyAdapterLlamaCPP):
		return ResidencyAdapterLlamaCPP
	case string(ResidencyAdapterOMLX):
		return ResidencyAdapterOMLX
	case string(ResidencyAdapterCustom):
		return ResidencyAdapterCustom
	default:
		return ResidencyAdapterAuto
	}
}

// ResidencyFieldMap maps an arbitrary engine's JSON onto the residency vocabulary.
// It exists so a backend nobody has shipped an adapter for still works by config
// alone; it never guesses, so a missing mapping reports unknown rather than inferring.
type ResidencyFieldMap struct {
	Items   string `json:"items,omitempty"`   // dot path to the entry array, e.g. "models" or "data.models"
	ID      string `json:"id,omitempty"`      // field on each entry naming the model
	Loaded  string `json:"loaded,omitempty"`  // boolean field, true means resident
	Loading string `json:"loading,omitempty"` // boolean field, true means load in progress
}

// ModelResidencyConfig tells the gateway how to read residency from one provider
// instance. It is per provider, not per model: a provider is one backend with one
// memory, so the reader is a property of the endpoint.
type ModelResidencyConfig struct {
	Adapter    ResidencyAdapter  `json:"adapter"`                // "auto" (default), "ollama", "llamacpp", "omlx" or "custom"
	Path       string            `json:"path,omitempty"`         // endpoint path, defaults per adapter; required for custom
	TimeoutMS  int               `json:"timeout_ms,omitempty"`   // per-probe timeout, default 2000
	CacheTTLMS int               `json:"cache_ttl_ms,omitempty"` // 0 probes every request, which is the default
	Fields     *ResidencyFieldMap `json:"fields,omitempty"`      // required when adapter is custom
}

// Residency defaults, chosen against the measured cost of a real probe: a stock
// endpoint answers in 7-31ms, so no cache is the honest default and caching is the
// opt-in for a backend whose probe is slow or rate limited.
const (
	DefaultResidencyProbeTimeoutMS = 2000
	DefaultResidencyCacheTTLMS     = 0
)

// Timeout returns the probe timeout.
func (c *ModelResidencyConfig) Timeout() time.Duration {
	if c == nil || c.TimeoutMS <= 0 {
		return DefaultResidencyProbeTimeoutMS * time.Millisecond
	}
	return time.Duration(c.TimeoutMS) * time.Millisecond
}

// CacheTTL returns how long a reading may be reused.
func (c *ModelResidencyConfig) CacheTTL() time.Duration {
	if c == nil || c.CacheTTLMS <= 0 {
		return DefaultResidencyCacheTTLMS * time.Millisecond
	}
	return time.Duration(c.CacheTTLMS) * time.Millisecond
}

// ResidencyState is the normalized answer to "is this model resident right now".
// Engine adapters map their own shapes into these four values and nothing else
// reaches the selection logic.
type ResidencyState string

const (
	ResidencyReady    ResidencyState = "ready"    // resident and usable now
	ResidencyLoading  ResidencyState = "loading"  // loading in progress
	ResidencyUnloaded ResidencyState = "unloaded" // backend answered; not resident
	ResidencyUnknown  ResidencyState = "unknown"  // backend did not answer; not the same as unloaded
)

// VirtualModelErrorPolicy decides what happens when residency cannot be established.
// Refusing is the default: sending an unresolved name upstream either 404s or
// evicts the model the caller was trying to keep resident.
type VirtualModelErrorPolicy string

const (
	VirtualModelErrorOnProbeError VirtualModelErrorPolicy = "error"
	VirtualModelFirstOnProbeError VirtualModelErrorPolicy = "first"
)

// NormalizeModelErrorPolicy maps an unvalidated config string to a known policy,
// defaulting to the conservative value.
func NormalizeModelErrorPolicy(raw string) VirtualModelErrorPolicy {
	if strings.EqualFold(strings.TrimSpace(raw), string(VirtualModelFirstOnProbeError)) {
		return VirtualModelFirstOnProbeError
	}
	return VirtualModelErrorOnProbeError
}

// VirtualModel is a user-defined name clients may send, plus the ordered candidates
// it may resolve to. Models order is the preference order and is user-visible
// behaviour, which is why it is a slice and not a set.
type VirtualModel struct {
	Name         string                    `json:"name"`         // name clients send, matched case-insensitively
	Provider     ModelProvider           `json:"provider"`       // configured provider instance, not a base_provider_type
	Models       []string                `json:"models"`         // candidates, in preference order
	OnProbeError VirtualModelErrorPolicy `json:"on_probe_error"` // "error" (default) or "first"
	Description  string                  `json:"description"`    // operator note, never used for routing
}

// Validate rejects definitions that cannot be routed, however they arrived — config
// file, API, or a config store written by another node.
func (vm *VirtualModel) Validate() error {
	if vm == nil {
		return fmt.Errorf("virtual model is nil")
	}
	if strings.TrimSpace(vm.Name) == "" {
		return fmt.Errorf("virtual model name is required")
	}
	if strings.ContainsAny(vm.Name, "/\\") {
		return fmt.Errorf("virtual model name %q must not contain '/'", vm.Name)
	}
	if vm.Provider == "" {
		return fmt.Errorf("virtual model %q requires provider", vm.Name)
	}
	if len(vm.Models) == 0 {
		return fmt.Errorf("virtual model %q requires at least one candidate model", vm.Name)
	}
	if strings.EqualFold(strings.TrimSpace(vm.Name), strings.TrimSpace(string(vm.Provider))) {
		return fmt.Errorf("virtual model name %q must differ from its provider", vm.Name)
	}
	seen := make(map[string]struct{}, len(vm.Models))
	for _, model := range vm.Models {
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("virtual model %q has an empty candidate model", vm.Name)
		}
		key := strings.ToLower(strings.TrimSpace(model))
		if _, dup := seen[key]; dup {
			return fmt.Errorf("virtual model %q lists candidate %q more than once", vm.Name, model)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// Matches reports whether name addresses this virtual model, case-insensitively.
func (vm *VirtualModel) Matches(name string) bool {
	if vm == nil || name == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(name), vm.Name)
}

// ResidencySnapshot is one reading of a backend's memory. Authoritative is the
// load-bearing field: it is true only when the backend answered in a shape the
// adapter fully understood. A timeout, a 404, or a payload whose residency field
// has moved is not authoritative, and that distinction is what stops a failed probe
// from being read as "nothing is loaded" and issued as a cold load.
type ResidencySnapshot struct {
	Provider      ModelProvider
	Authoritative bool
	ObservedAt    time.Time
	States        map[string]ResidencyState // lowercased candidate name to state
}

// StateOf reads a candidate, defaulting to unknown. Defaulting to unknown means an
// adapter that reports less can only make the gateway more conservative, never more
// eager to load.
func (s *ResidencySnapshot) StateOf(model string) ResidencyState {
	if s == nil || s.States == nil {
		return ResidencyUnknown
	}
	state, ok := s.States[strings.ToLower(strings.TrimSpace(model))]
	if !ok {
		return ResidencyUnknown
	}
	return state
}

// VirtualModelResolution is the decision plus the evidence behind it. Members and
// ObservedAt travel with it so a response, a log record, and the UI can show how
// stale the knowledge was instead of asserting a pick the backend may have changed.
type VirtualModelResolution struct {
	VirtualModel string              `json:"virtual_model"`
	Provider     ModelProvider       `json:"provider"`
	Model        string              `json:"model"`
	Reason       string              `json:"reason"`
	ObservedAt   time.Time           `json:"observed_at,omitempty"`
	Members      []VirtualMemberView `json:"members,omitempty"`
}

// VirtualMemberView is one candidate's reported state, for diagnostics.
type VirtualMemberView struct {
	Model string         `json:"model"`
	State ResidencyState `json:"state"`
}

// SelectVirtualModelTarget applies the preference order to a snapshot. Pure — no
// clock, no I/O — which is what makes the policy testable without a backend.
//
// Order: the first candidate reported ready; else the first still loading, so a
// request joins an in-flight load instead of starting a competing one; else the
// first candidate, accepting the cold load the caller asked for. A non-authoritative
// snapshot never reaches that last step, and either errors or falls back to the
// first candidate per the configured policy.
//
// It deliberately invents no fallback chain: a stale pick normally succeeds by
// cold-loading rather than 404ing, so a chain would add loads and evictions
// without making the observation any fresher.
func SelectVirtualModelTarget(vm *VirtualModel, snapshot *ResidencySnapshot) (*VirtualModelResolution, *BifrostError) {
	if vm == nil {
		return nil, NewVirtualModelError(500, "virtual model is nil")
	}
	if len(vm.Models) == 0 {
		return nil, NewVirtualModelError(500, "virtual model has no candidate models")
	}

	members := make([]VirtualMemberView, 0, len(vm.Models))
	for _, model := range vm.Models {
		members = append(members, VirtualMemberView{Model: model, State: snapshot.StateOf(model)})
	}

	if snapshot == nil || !snapshot.Authoritative {
		if vm.OnProbeError == VirtualModelFirstOnProbeError {
			return decided(vm, vm.Models[0], "residency unknown; using first candidate per on_probe_error", members, time.Time{}), nil
		}
		return nil, NewVirtualModelError(502, fmt.Sprintf(
			"cannot resolve virtual model %q: %s did not report model residency", vm.Name, vm.Provider))
	}

	for _, member := range members {
		if member.State == ResidencyReady {
			return decided(vm, member.Model, "first resident candidate", members, snapshot.ObservedAt), nil
		}
	}
	for _, member := range members {
		if member.State == ResidencyLoading {
			return decided(vm, member.Model, "no candidate resident; joining an in-progress load", members, snapshot.ObservedAt), nil
		}
	}
	return decided(vm, vm.Models[0], "no candidate resident; using first candidate", members, snapshot.ObservedAt), nil
}

// decided assembles a resolution from a chosen candidate, carrying the snapshot's
// observation time forward so callers can see how stale the decision was.
func decided(vm *VirtualModel, model, reason string, members []VirtualMemberView, observed time.Time) *VirtualModelResolution {
	return &VirtualModelResolution{
		VirtualModel: vm.Name,
		Provider:     vm.Provider,
		Model:        model,
		Reason:       reason,
		ObservedAt:   observed,
		Members:      members,
	}
}

// NewVirtualModelError builds the error returned when resolution fails. The type is
// stable so clients and dashboards can filter on it.
func NewVirtualModelError(status int, message string) *BifrostError {
	return &BifrostError{
		IsBifrostError: true,
		StatusCode:     Ptr(status),
		Error:          &ErrorField{Message: message, Type: Ptr("virtual_model_resolution_error")},
	}
}

// VirtualModelRegistry answers which virtual models exist. The transport owns the
// storage and swaps the whole set atomically on reload, so a request never observes
// a half-applied edit.
type VirtualModelRegistry interface {
	Lookup(name string) (*VirtualModel, bool)
	List() []*VirtualModel
}

// VirtualModelResolver is the seam between core and the engines. Resolve may probe
// the backend and must coalesce concurrent probes; Lookup is memory-only.
//
// A non-nil error must stop the request. Core enforces that on the request path
// rather than through the plugin pipeline, whose PreRequestHook errors are logged
// and otherwise ignored.
type VirtualModelResolver interface {
	VirtualModelRegistry
	Resolve(ctx *BifrostContext, vm *VirtualModel) (*VirtualModelResolution, *BifrostError)
}

// GetVirtualModel returns the virtual name a request was addressed by, or "".
func GetVirtualModel(ctx *BifrostContext) string {
	if ctx == nil {
		return ""
	}
	name, _ := ctx.Value(BifrostContextKeyVirtualModel).(string)
	return name
}

// GetVirtualModelResolution returns the decision recorded for a request, or nil.
func GetVirtualModelResolution(ctx *BifrostContext) *VirtualModelResolution {
	if ctx == nil {
		return nil
	}
	res, _ := ctx.Value(BifrostContextKeyVirtualModelResolution).(*VirtualModelResolution)
	return res
}
