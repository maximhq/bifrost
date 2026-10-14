package lib

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// Virtual models decouple the name a client sends from the model that answers. Core
// owns the vocabulary and the selection rule; this file owns the only thing core
// cannot do: ask a running backend what it currently holds in memory.
//
// Every decision here comes from measuring a real oMLX, llama.cpp and ollama on one
// shared Apple Silicon box, which is the configuration that makes this feature
// necessary — three engines, one pool of unified memory, and a request that names a
// model which is not loaded evicts whatever was loaded to fetch it.

// residencyMaxBodyBytes caps a probe response. A populated engine list is tens of
// kilobytes; the cap is a backstop against an endpoint that answers with something
// enormous instead of the model list.
const residencyMaxBodyBytes = 4 << 20

// residencyEntry is a virtual model paired with the endpoint its residency is read
// from. Both are resolved once when config is installed, so a request never walks
// provider config, never decrypts a key, and never re-infers an adapter.
type residencyEntry struct {
	model  *schemas.VirtualModel
	target *residencyTarget
}

// residencyTarget is everything needed to probe one provider instance.
type residencyTarget struct {
	provider     schemas.ModelProvider
	adapter      schemas.ResidencyAdapter
	baseURL      string
	path         string
	headers      map[string]string
	timeout      time.Duration
	ttl          time.Duration
	fields       *schemas.ResidencyFieldMap
	tlsConfig    *tls.Config
	allowPrivate bool
}

// residencySlot holds one endpoint's last reading plus the mutex that coalesces
// concurrent probes. The lock is per endpoint, not per process: probing ollama must
// not stall a request waiting on llama.cpp.
type residencySlot struct {
	mu        sync.Mutex
	adapter   schemas.ResidencyAdapter
	snapshot  *schemas.ResidencySnapshot
	expiresAt time.Time
}

// residencyRegistry is an immutable snapshot of the virtual model set. The whole
// registry is replaced on a config change, so a request that looked a name up sees
// that definition and its endpoint for the rest of the request — never a half
// applied edit from another goroutine.
type residencyRegistry struct {
	entries map[string]*residencyEntry
}

// VirtualModelService implements schemas.VirtualModelResolver.
type VirtualModelService struct {
	registry atomicRegistry
	slots    sync.Map
	factory  *network.HTTPClientFactory
	logger   schemas.Logger
}

// atomicRegistry wraps the pointer so the swap is a single store. Kept explicit
// rather than a bare atomic.Pointer field for readability at the two call sites.
type atomicRegistry struct {
	mu      sync.RWMutex
	current *residencyRegistry
}

func (r *atomicRegistry) load() *residencyRegistry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current
}

func (r *atomicRegistry) store(next *residencyRegistry) {
	r.mu.Lock()
	r.current = next
	r.mu.Unlock()
}

// NewVirtualModelService builds the resolver. A nil factory falls back to
// http.DefaultClient, which loses proxy awareness; the transport always supplies one.
func NewVirtualModelService(factory *network.HTTPClientFactory, logger schemas.Logger) *VirtualModelService {
	return &VirtualModelService{factory: factory, logger: logger}
}

// Install validates the virtual model set and resolves each one's probe endpoint
// against the provider config, then swaps it in atomically.
//
// Validation is all-or-nothing and returns on the first problem. A half-installed
// set would route some virtual names to a backend nobody checked, which is worse
// than the operator's typo and a clear error.
func (s *VirtualModelService) Install(virtualModels map[string]schemas.VirtualModel, providers map[schemas.ModelProvider]configstore.ProviderConfig) error {
	if len(virtualModels) == 0 {
		s.registry.store(nil)
		return nil
	}

	registry := &residencyRegistry{entries: make(map[string]*residencyEntry, len(virtualModels))}
	names := make([]string, 0, len(virtualModels))
	for name := range virtualModels {
		names = append(names, name)
	}
	// Sorted so a config with two bad definitions reports the same error every time.
	sort.Strings(names)

	for _, name := range names {
		vm := virtualModels[name]
		vm.Name = strings.TrimSpace(name)
		if err := vm.Validate(); err != nil {
			return err
		}
		providerCfg, ok := providers[vm.Provider]
		if !ok {
			return fmt.Errorf("virtual model %q references provider %q which is not configured", vm.Name, vm.Provider)
		}
		target, err := buildResidencyTarget(vm.Provider, providerCfg)
		if err != nil {
			return fmt.Errorf("virtual model %q: %w", vm.Name, err)
		}
		registry.entries[strings.ToLower(vm.Name)] = &residencyEntry{model: &vm, target: target}
	}

	s.registry.store(registry)
	if s.logger != nil {
		s.logger.Info("virtual models installed: %d", len(registry.entries))
	}
	return nil
}

// installVirtualModels builds the resolver and installs the configured set. When
// nothing is configured the config keeps a nil resolver, which is the fast path: core
// then does one atomic load per request and no map lookup at all.
func installVirtualModels(config *Config, virtualModels map[string]schemas.VirtualModel, logger schemas.Logger) error {
	service := NewVirtualModelService(config.HTTPClientFactory, logger)
	if err := service.Install(virtualModels, config.Providers); err != nil {
		return err
	}
	if len(virtualModels) == 0 {
		return nil
	}
	config.VirtualModels = service
	return nil
}

// buildResidencyTarget turns provider network config into a probe target.
func buildResidencyTarget(provider schemas.ModelProvider, providerCfg configstore.ProviderConfig) (*residencyTarget, error) {
	networkCfg := providerCfg.NetworkConfig
	baseURL := ""
	allowPrivate := false
	extraHeaders := map[string]string{}
	timeout := schemas.DefaultResidencyProbeTimeoutMS

	if networkCfg != nil {
		baseURL = strings.TrimRight(strings.TrimSpace(networkCfg.BaseURL), "/")
		allowPrivate = networkCfg.AllowPrivateNetwork
		for k, v := range networkCfg.ExtraHeaders {
			extraHeaders[k] = v
		}
	}

	residency := providerCfg.ModelResidency
	adapter := schemas.ResidencyAdapterAuto
	path := ""
	ttl := time.Duration(schemas.DefaultResidencyCacheTTLMS) * time.Millisecond
	var fields *schemas.ResidencyFieldMap
	if residency != nil {
		adapter = schemas.NormalizeResidencyAdapter(string(residency.Adapter))
		path = strings.TrimSpace(residency.Path)
		ttl = residency.CacheTTL()
		timeout = int(residency.Timeout() / time.Millisecond)
		fields = residency.Fields
	}

	if baseURL == "" {
		// Ollama has a fixed default port and is required to have a base URL in
		// Bifrost's own config, so an empty base URL means a custom provider whose
		// endpoint only lives in request_path_overrides, which cannot be probed.
		return nil, fmt.Errorf("no base_url to probe for model residency; set network_config.base_url")
	}
	if adapter == schemas.ResidencyAdapterCustom && (path == "" || fields == nil || fields.Items == "" || fields.ID == "") {
		return nil, fmt.Errorf("custom residency needs path and fields.items and fields.id")
	}

	// The first enabled key is the probe credential. Residency is a property of the
	// backend rather than of a key, and every key on one local engine endpoint reads
	// the same memory, so there is nothing to distribute.
	if bearer := firstEnabledKeyValue(providerCfg.Keys); bearer != "" {
		extraHeaders["Authorization"] = "Bearer " + bearer
	}

	tlsConfig, err := residencyTLSConfig(networkCfg)
	if err != nil {
		return nil, err
	}

	return &residencyTarget{
		provider:     provider,
		adapter:      adapter,
		baseURL:      baseURL,
		path:         path,
		headers:      extraHeaders,
		timeout:      time.Duration(timeout) * time.Millisecond,
		ttl:          ttl,
		fields:       fields,
		tlsConfig:    tlsConfig,
		allowPrivate: allowPrivate,
	}, nil
}

// firstEnabledKeyValue returns the first usable API key value, or "".
func firstEnabledKeyValue(keys []schemas.Key) string {
	for _, key := range keys {
		if key.Enabled != nil && !*key.Enabled {
			continue
		}
		if key.Value.IsRedacted() || key.Value.IsMaskedPlaceholder() {
			continue
		}
		if value := strings.TrimSpace(key.Value.GetValue()); value != "" {
			return value
		}
	}
	return ""
}

// residencyTLSConfig is built once per provider and reused, because the client
// factory keys its cache on the *tls.Config pointer.
func residencyTLSConfig(networkCfg *schemas.NetworkConfig) (*tls.Config, error) {
	if networkCfg == nil || (!networkCfg.InsecureSkipVerify && (networkCfg.CACertPEM == nil || networkCfg.CACertPEM.GetValue() == "")) {
		return nil, nil
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: networkCfg.InsecureSkipVerify}
	if pem := networkCfg.CACertPEM.GetValue(); pem != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(pem)) {
			return nil, fmt.Errorf("ca_cert_pem is not a valid PEM certificate bundle")
		}
		config.RootCAs = pool
	}
	return config, nil
}

// Lookup is memory-only, as schemas.VirtualModelResolver requires: it is called on
// every request to decide whether the request is even virtual.
func (s *VirtualModelService) Lookup(name string) (*schemas.VirtualModel, bool) {
	registry := s.registry.load()
	if registry == nil {
		return nil, false
	}
	entry, ok := registry.entries[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, false
	}
	return entry.model, true
}

// List returns every configured virtual model, sorted by name.
func (s *VirtualModelService) List() []*schemas.VirtualModel {
	registry := s.registry.load()
	if registry == nil {
		return nil
	}
	out := make([]*schemas.VirtualModel, 0, len(registry.entries))
	for _, entry := range registry.entries {
		out = append(out, entry.model)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Enabled reports whether any virtual model is configured, so the listing handlers can
// skip their work entirely on a gateway that does not use this feature.
func (s *VirtualModelService) Enabled() bool {
	registry := s.registry.load()
	return registry != nil && len(registry.entries) > 0
}

// Resolve asks the backend what it holds and picks the candidate. It never rewrites
// the definition it was handed: the returned resolution carries the concrete model,
// and the caller records the virtual name separately.
func (s *VirtualModelService) Resolve(ctx *schemas.BifrostContext, vm *schemas.VirtualModel) (*schemas.VirtualModelResolution, *schemas.BifrostError) {
	if vm == nil {
		return nil, schemas.NewVirtualModelError(500, "virtual model is nil")
	}
	entry, ok := s.lookupEntry(vm.Name)
	if !ok || entry.target == nil {
		// The definition came from somewhere other than the live registry — a config
		// reload replaced it mid-flight. There is no endpoint to probe, so refusing
		// beats guessing.
		return nil, schemas.NewVirtualModelError(503, fmt.Sprintf(
			"virtual model %q is not installed; retry after the next config reload", vm.Name))
	}
	return schemas.SelectVirtualModelTarget(vm, s.snapshot(ctx, entry.target))
}

// MemberStatus is the residency of every candidate of one virtual model, for the UI
// and for an operator asking why a request went where it went.
func (s *VirtualModelService) MemberStatus(ctx *schemas.BifrostContext, name string) (*residencyEntry, *schemas.ResidencySnapshot, error) {
	entry, ok := s.lookupEntry(name)
	if !ok {
		return nil, nil, fmt.Errorf("virtual model %q is not configured", name)
	}
	return entry, s.snapshot(ctx, entry.target), nil
}

func (s *VirtualModelService) lookupEntry(name string) (*residencyEntry, bool) {
	registry := s.registry.load()
	if registry == nil {
		return nil, false
	}
	entry, ok := registry.entries[strings.ToLower(strings.TrimSpace(name))]
	return entry, ok
}

// snapshot returns the freshest reading available, probing only when the cached one
// has expired.
//
// Two behaviours matter more than the rest. Concurrent callers coalesce on the
// endpoint's mutex, so a cache miss at 50 concurrent requests is one probe rather
// than fifty. And when a refresh fails, the last good reading is returned unchanged —
// it is still authoritative, because the backend really did say it, and ObservedAt
// tells the caller how old it is. Only an endpoint that has never answered yields a
// non-authoritative snapshot, which is what makes a stopped backend refuse to route
// rather than cold-load every candidate in turn.
func (s *VirtualModelService) snapshot(ctx *schemas.BifrostContext, target *residencyTarget) *schemas.ResidencySnapshot {
	slot := s.slotFor(target)

	slot.mu.Lock()
	defer slot.mu.Unlock()

	now := time.Now()
	if slot.snapshot != nil && now.Before(slot.expiresAt) {
		return slot.snapshot
	}

	probeCtx, cancel := context.WithTimeout(ctx, target.timeout)
	defer cancel()

	snapshot, adapter, err := probeResidency(probeCtx, target, slot.adapter, s.httpClient(target))
	if err != nil {
		if slot.snapshot != nil {
			if s.logger != nil {
				s.logger.Debug("residency probe of %s failed, reusing a reading observed %s ago: %v",
					target.baseURL, now.Sub(slot.snapshot.ObservedAt), err)
			}
			return slot.snapshot
		}
		if s.logger != nil {
			s.logger.Warn("residency probe of %s failed with no earlier reading: %v", target.baseURL, err)
		}
		return &schemas.ResidencySnapshot{Provider: target.provider, ObservedAt: now}
	}

	slot.adapter = adapter
	slot.snapshot = snapshot
	slot.expiresAt = now.Add(target.ttl)
	return snapshot
}

// slotFor returns the per-endpoint slot, created on first use.
func (s *VirtualModelService) slotFor(target *residencyTarget) *residencySlot {
	key := string(target.provider) + "|" + target.baseURL
	if existing, ok := s.slots.Load(key); ok {
		return existing.(*residencySlot)
	}
	slot := &residencySlot{}
	actual, _ := s.slots.LoadOrStore(key, slot)
	return actual.(*residencySlot)
}

// httpClient honours the global proxy for API-purpose traffic, since residency reads
// are metadata calls like list-models rather than inference.
func (s *VirtualModelService) httpClient(target *residencyTarget) *http.Client {
	if s.factory == nil {
		return http.DefaultClient
	}
	if target.tlsConfig != nil {
		return s.factory.HTTPClientWithTLS(network.ClientPurposeAPI, target.tlsConfig)
	}
	return s.factory.GetHTTPClient(network.ClientPurposeAPI)
}

// residencyProbe is one way of reading residency from one kind of backend.
type residencyProbe struct {
	adapter schemas.ResidencyAdapter
	path    string
	// membership marks a list-of-loaded-models endpoint, where appearing in the list
	// means resident and absence means unloaded. A status endpoint is the opposite: it
	// enumerates every model the backend knows about, resident or not, so a candidate
	// missing from it is unknown, not free.
	membership bool
	parse      func(body map[string]any, target *residencyTarget, snapshot *schemas.ResidencySnapshot) error
}

// residencyProbes is the auto-detect order. oMLX first because its endpoint answers
// with per-model residency and memory accounting; llama.cpp next as it is the common
// OpenAI-compatible router with a status field on every entry; ollama last as its
// residency surface is native and its list is empty when nothing is loaded, which
// would otherwise read as a successful probe of an idle machine.
var residencyProbes = []residencyProbe{
	{
		adapter: schemas.ResidencyAdapterOMLX,
		path:    "/v1/models/status",
		parse:   parseStatusList,
	},
	{
		adapter: schemas.ResidencyAdapterLlamaCPP,
		path:    "/v1/models",
		parse:   parseStatusList,
	},
	{
		adapter:    schemas.ResidencyAdapterOllama,
		path:       "/api/ps",
		membership: true,
		parse:      parseOllamaProcesses,
	},
}

// probeResidency fetches and decodes a reading, trying the known engines in order
// when the adapter is auto and remembering which one answered.
//
// Auto-detect costs at most one GET per engine, once per endpoint: the winner is
// stored on the slot and every later probe goes straight to it.
func probeResidency(ctx context.Context, target *residencyTarget, known schemas.ResidencyAdapter, client *http.Client) (*schemas.ResidencySnapshot, schemas.ResidencyAdapter, error) {
	candidates := residencyProbes
	if target.adapter == schemas.ResidencyAdapterCustom {
		candidates = []residencyProbe{{adapter: schemas.ResidencyAdapterCustom, path: target.path, parse: parseCustom}}
	} else if known != "" && known != schemas.ResidencyAdapterAuto {
		// A slot that already answered goes straight back to that reader; auto-detect
		// is a per-endpoint once, not a per-request retry.
		for _, probe := range residencyProbes {
			if probe.adapter == known {
				candidates = []residencyProbe{probe}
				break
			}
		}
		if len(candidates) != 1 || candidates[0].adapter != known {
			return nil, "", fmt.Errorf("no residency reader for adapter %q", known)
		}
	}

	var lastErr error
	for _, probe := range candidates {
		body, err := fetchResidencyJSON(ctx, target, client, probe.path)
		if err != nil {
			lastErr = err
			continue
		}
		snapshot := &schemas.ResidencySnapshot{Provider: target.provider, ObservedAt: time.Now(), States: map[string]schemas.ResidencyState{}}
		if err := probe.parse(body, target, snapshot); err != nil {
			lastErr = fmt.Errorf("%s: %w", probe.adapter, err)
			continue
		}
		return snapshot, probe.adapter, nil
	}
	return nil, "", fmt.Errorf("no residency reader answered for %s: %w", target.baseURL, lastErr)
}

// fetchResidencyJSON GETs one residency endpoint.
func fetchResidencyJSON(ctx context.Context, target *residencyTarget, client *http.Client, path string) (map[string]any, error) {
	endpoint := target.baseURL + path
	// The target is operator-supplied provider config rather than user input, so the
	// private-network decision is the provider's own allow_private_network.
	if err := bifrost.ValidateExternalURL(endpoint, target.allowPrivate); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	for key, value := range target.headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, residencyMaxBodyBytes))
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("GET %s returned unreadable JSON: %w", path, err)
	}
	return out, nil
}

// parseStatusList reads an endpoint that carries an explicit residency field per
// entry — oMLX's loaded and is_loading, llama.cpp's status.value.
func parseStatusList(body map[string]any, _ *residencyTarget, snapshot *schemas.ResidencySnapshot) error {
	entries := findEntryArray(body, "models", "data")
	if entries == nil {
		return fmt.Errorf("no models array in the response")
	}
	seen := false
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := firstString(entry, "id", "model", "model_alias", "name")
		if name == "" {
			continue
		}
		seen = true
		state := schemas.ResidencyUnloaded
		switch strings.ToLower(firstString(entry, "value", "state", "status")) {
		case "loaded", "ready", "ready_to_use":
			state = schemas.ResidencyReady
		case "loading", "no_slot":
			state = schemas.ResidencyLoading
		}
		if boolField(entry, "loaded") {
			state = schemas.ResidencyReady
		}
		if boolField(entry, "is_loading") {
			state = schemas.ResidencyLoading
		}
		snapshot.States[strings.ToLower(name)] = state
	}
	if !seen {
		return fmt.Errorf("no model entries the reader understood")
	}
	snapshot.Authoritative = true
	return nil
}

// parseOllamaProcesses reads ollama's native /api/ps, the only place ollama reports
// residency. Its OpenAI-compatible /v1/models carries no residency field at all,
// which is why this feature reaches outside the OpenAI surface for ollama.
func parseOllamaProcesses(body map[string]any, _ *residencyTarget, snapshot *schemas.ResidencySnapshot) error {
	entries, ok := body["models"].([]any)
	if !ok {
		return fmt.Errorf("no models array in the response")
	}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		// ollama reports both name (tagged, e.g. "qwen3:30b") and model (a digest
		// when the pull was untagged); accepting either keeps a candidate that
		// references the digest working.
		for _, name := range []string{stringField(entry, "name"), stringField(entry, "model")} {
			if name != "" {
				snapshot.States[strings.ToLower(name)] = schemas.ResidencyReady
			}
		}
	}
	snapshot.Authoritative = true
	return nil
}

// parseCustom reads an arbitrary engine's JSON through the operator's field mapping.
// Two shapes are covered and the mapping states which applies: with fields.loaded
// and/or fields.loading it is a status endpoint; with only fields.items and fields.id
// the list itself means resident, which is how an endpoint that lists only its loaded
// models has to be read. Nothing is guessed beyond what the operator wrote.
func parseCustom(body map[string]any, target *residencyTarget, snapshot *schemas.ResidencySnapshot) error {
	fields := target.fields
	if fields == nil {
		return fmt.Errorf("no field mapping for the custom adapter")
	}
	entries := walkPath(body, fields.Items)
	if entries == nil {
		return fmt.Errorf("no array at %q in the response", fields.Items)
	}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := stringField(entry, fields.ID)
		if name == "" {
			continue
		}
		state := schemas.ResidencyReady
		if fields.Loaded != "" || fields.Loading != "" {
			state = schemas.ResidencyUnloaded
			if fields.Loaded != "" && boolField(entry, fields.Loaded) {
				state = schemas.ResidencyReady
			}
			if fields.Loading != "" && boolField(entry, fields.Loading) {
				state = schemas.ResidencyLoading
			}
		}
		snapshot.States[strings.ToLower(name)] = state
	}
	snapshot.Authoritative = true
	return nil
}

// walkPath resolves a dot path to a JSON array, so both {"models":[...]} and
// {"data":{"models":[...]}} are reachable without shipping an adapter per backend.
func walkPath(body map[string]any, path string) []any {
	if path == "" {
		return nil
	}
	var current any = body
	for _, part := range strings.Split(path, ".") {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = obj[part]
		if !ok {
			return nil
		}
	}
	arr, _ := current.([]any)
	return arr
}

// findEntryArray returns the first value that is a JSON array under any of the given
// keys, searching one level down so both {"models":[...]} and {"data":{"models":[...]}}
// resolve.
func findEntryArray(body map[string]any, keys ...string) []any {
	for _, key := range keys {
		if arr, ok := body[key].([]any); ok {
			return arr
		}
		if nested, ok := body[key].(map[string]any); ok {
			for _, inner := range []string{"models", "data", "items"} {
				if arr, ok := nested[inner].([]any); ok {
					return arr
				}
			}
		}
	}
	return nil
}

func firstString(entry map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringField(entry, key); value != "" {
			return value
		}
		if nested, ok := entry[key].(map[string]any); ok {
			for _, inner := range []string{"value", "state", "name"} {
				if value := stringField(nested, inner); value != "" {
					return value
				}
			}
		}
	}
	return ""
}

func stringField(entry map[string]any, key string) string {
	value, _ := entry[key].(string)
	return strings.TrimSpace(value)
}

func boolField(entry map[string]any, key string) bool {
	value, _ := entry[key].(bool)
	return value
}
