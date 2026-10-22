package lib

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	failBackoff  time.Duration // how long a failed probe is remembered, so a dead backend costs one probe per window instead of one per request
	staleCap     time.Duration // a reused reading older than this stops counting as authoritative
	fields       *schemas.ResidencyFieldMap
	tlsConfig    *tls.Config
	allowPrivate bool
	slotKey      string // provider|baseURL, built once so a cached probe reads a map without building a key
	identity     uint64 // fingerprint of what the probe sends, so a refresh can tell a reconfigured endpoint from an identical rebuild
}

const (
	// residencyDefaultFailBackoff bounds how often one endpoint is retried after a failed probe.
	// It is a floor: an operator who sets a long cache_ttl_ms accepts staler data, and a
	// remembered failure is exactly that.
	residencyDefaultFailBackoff = time.Second

	// residencyMinStaleAge floors how old a reused reading may be and still be believed. Ten
	// cache windows is the rule, but the default window is zero, and ten times zero would discard
	// a seconds-old reading the moment a probe failed.
	residencyMinStaleAge = time.Minute
)

// residencySlot holds one endpoint's last reading plus the mutex that coalesces
// concurrent probes. The lock is per endpoint, not per process: probing ollama must
// not stall a request waiting on llama.cpp.
type residencySlot struct {
	mu          sync.Mutex
	adapter     schemas.ResidencyAdapter
	snapshot    *schemas.ResidencySnapshot
	expiresAt   time.Time
	failedUntil time.Time // end of the backoff window after a probe failure
	flight      *residencyFlight
}

type residencyFlight struct {
	done     chan struct{}
	snapshot *schemas.ResidencySnapshot
}

// residencyRegistry is an immutable snapshot of the virtual model set. The whole
// registry is replaced on a config change, so a request that looked a name up sees
// that definition and its endpoint for the rest of the request — never a half
// applied edit from another goroutine.
type residencyRegistry struct {
	entries map[string]*residencyEntry
}

// VirtualModelService implements schemas.VirtualModelResolver. The registry is an atomic pointer
// rather than a mutex: written once at install, read on every request, so a read lock would be
// contention across cores for a swap that happens at most when config changes.
type VirtualModelService struct {
	registry atomic.Pointer[residencyRegistry]
	created  atomic.Int64 // when the current definitions were installed, in Unix seconds
	slots    sync.Map
	factory  *network.HTTPClientFactory
	logger   schemas.Logger
}

// installedRegistry reads the current definition set in a single atomic load.
func (s *VirtualModelService) installedRegistry() *residencyRegistry {
	return s.registry.Load()
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
		s.registry.Store(nil)
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
		// Providers are stored under a lowercased key by loadProviders, so the reference is put in
		// the same form before the lookup: an operator who spells their provider "Local-OMLX" has
		// named a provider that is present, and startup must not refuse the gateway for the case of
		// a letter. The normalized value is the one kept on the entry, so the probe target, the
		// owned_by in listings and the routing comparison all carry the form the rest of the stack uses.
		vm.Provider = schemas.ModelProvider(strings.ToLower(string(vm.Provider)))
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
		key := strings.ToLower(vm.Name)
		if _, exists := registry.entries[key]; exists {
			return fmt.Errorf("duplicate virtual model name %q", vm.Name)
		}
		vm.OnProbeError = schemas.NormalizeModelErrorPolicy(string(vm.OnProbeError))
		registry.entries[key] = &residencyEntry{model: &vm, target: target}
	}

	s.registry.Store(registry)
	s.created.Store(time.Now().Unix())
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

// refreshVirtualModelProbes rebuilds the residency probe endpoints from the current provider
// configs. Install runs once at boot, so a base URL change, a rotated key or a deleted provider
// would otherwise leave every probe on the boot-time settings. Callers hold c.Mu.
func (c *Config) refreshVirtualModelProbes() {
	if c == nil || c.VirtualModels == nil {
		return
	}
	c.VirtualModels.Refresh(c.Providers)
}

// VirtualModelResolver hands core the resolver, or a nil interface when nothing is configured.
// The check is on the concrete pointer because a nil *VirtualModelService placed in the
// interface compares non-nil: core would then call through it on every request and panic, which
// is the state a gateway with no virtual_models is in. Only this side of the boundary can see it.
func (c *Config) VirtualModelResolver() schemas.VirtualModelResolver {
	if c == nil || c.VirtualModels == nil {
		return nil
	}
	return c.VirtualModels
}

// residencyHeaderSet reports whether the operator already set this header, matched the way HTTP
// matches names. Providers that authenticate with a custom scheme keep it: replacing it with a
// bearer fails every probe, and on the default on_probe_error of error that turns every
// virtual-model request into a 502 for a reason the operator cannot see in their own config.
func residencyHeaderSet(headers map[string]string, name string) bool {
	for existing := range headers {
		if strings.EqualFold(existing, name) {
			return true
		}
	}
	return false
}

// buildResidencyTarget turns provider network config into a probe target.
func buildResidencyTarget(provider schemas.ModelProvider, providerCfg configstore.ProviderConfig) (*residencyTarget, error) {
	networkCfg := providerCfg.NetworkConfig
	baseURL := ""
	allowPrivate := false
	extraHeaders := map[string]string{}
	timeout := schemas.DefaultResidencyProbeTimeoutMS

	var residency *schemas.ModelResidencyConfig
	if networkCfg != nil {
		baseURL = strings.TrimRight(strings.TrimSpace(networkCfg.BaseURL), "/")
		allowPrivate = networkCfg.AllowPrivateNetwork
		for k, v := range networkCfg.ExtraHeaders {
			extraHeaders[k] = v.GetValue()
		}
		// Residency rides with the network config: same endpoint, credentials and TLS settings.
		residency = networkCfg.ModelResidency
	}
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
	if bearer := firstEnabledKeyValue(providerCfg.Keys); bearer != "" && !residencyHeaderSet(extraHeaders, "Authorization") {
		extraHeaders["Authorization"] = "Bearer " + bearer
	}

	tlsConfig, err := residencyTLSConfig(networkCfg)
	if err != nil {
		return nil, err
	}
	caPEM := ""
	if networkCfg != nil && networkCfg.CACertPEM != nil {
		caPEM = networkCfg.CACertPEM.GetValue()
	}

	// A failure is remembered for at least a second, or cache_ttl_ms if longer: someone who
	// tolerates a 30s-old reading tolerates knowing for 30s that the backend did not answer.
	// Without this a down backend makes every virtual-model request wait out the probe timeout.
	failBackoff := residencyDefaultFailBackoff
	if ttl > failBackoff {
		failBackoff = ttl
	}
	// A reused reading is believed for ten cache windows, floored at a minute (ten times the
	// default window of zero would believe nothing).
	staleCap := 10 * ttl
	if staleCap < residencyMinStaleAge {
		staleCap = residencyMinStaleAge
	}

	return &residencyTarget{
		provider:     provider,
		adapter:      adapter,
		baseURL:      baseURL,
		path:         path,
		headers:      extraHeaders,
		timeout:      time.Duration(timeout) * time.Millisecond,
		ttl:          ttl,
		failBackoff:  failBackoff,
		staleCap:     staleCap,
		fields:       fields,
		tlsConfig:    tlsConfig,
		allowPrivate: allowPrivate,
		slotKey:      string(provider) + "|" + baseURL,
		identity:     probeIdentity(provider, adapter, baseURL, path, extraHeaders, fields, timeout, ttl, failBackoff, staleCap, allowPrivate, caPEM),
	}, nil
}

// probeIdentity fingerprints everything that decides what a probe sends and how long its answer is
// trusted. Refresh compares it with the previous target's: without that, editing any one provider
// would drop the failure backoff and the detected adapter every other endpoint had earned, and an
// auto adapter would re-run its detection pass against every engine on the box.
func probeIdentity(provider schemas.ModelProvider, adapter schemas.ResidencyAdapter, baseURL, path string, headers map[string]string, fields *schemas.ResidencyFieldMap, timeout int, ttl, failBackoff, staleCap time.Duration, allowPrivate bool, caPEM string) uint64 {
	hash := fnv.New64a()
	write := func(value string) {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	write(string(provider))
	write(string(adapter))
	write(baseURL)
	write(path)
	write(caPEM)
	if allowPrivate {
		write("private")
	}
	if fields != nil {
		write(fields.Items)
		write(fields.ID)
		write(fields.Loaded)
		write(fields.Loading)
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		write(key)
		write(headers[key])
	}
	write(fmt.Sprint(timeout, ttl, failBackoff, staleCap))
	return hash.Sum64()
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
// Lookup is nil-safe: a nil service means no virtual models, which is the common case and never
// an error, and callers reach it through an interface that cannot express "absent".
func (s *VirtualModelService) Lookup(name string) (*schemas.VirtualModel, bool) {
	if s == nil {
		return nil, false
	}
	registry := s.installedRegistry()
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
	if s == nil {
		return nil
	}
	registry := s.installedRegistry()
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

// Created is when the current definitions were installed, in Unix seconds. Listings report it as
// the model's created timestamp: a virtual model has no creation time of its own, and stamping
// time.Now per call made the field change on every listing.
func (s *VirtualModelService) Created() int64 {
	if s == nil {
		return 0
	}
	return s.created.Load()
}

// Refresh rebuilds the probe endpoints from the current provider configs, keeping the definitions.
// Install runs once at boot, so without this a base URL change, a rotated key or a new TLS setting
// leaves probes on the boot-time values: every probe answers 401 and, on the default on_probe_error
// of error, every virtual-model request 502s until restart.
//
// A virtual model whose provider was deleted keeps its definition and loses its endpoint. The
// registry holds the only copy of a definition, so dropping the entry would strand the name until
// restart even after the provider came back, and deleting then re-creating a provider is a normal
// way to fix a bad config in the UI. Keeping the entry means the name is still advertised in
// listings and Resolve refuses with a 503 that says why, rather than the bare name escaping to
// whichever provider the catalog happens to pick.
//
// Rebuilding an endpoint whose fingerprint changed also forgets what its slot remembered: a rotated
// credential lands on the same slot (keyed by provider and base URL) and would otherwise still be
// inside the failure backoff it earned with the old key, so the fix would not take effect for a
// whole window. An endpoint the edit did not touch keeps its state.
func (s *VirtualModelService) Refresh(providers map[schemas.ModelProvider]configstore.ProviderConfig) {
	if s == nil {
		return
	}
	current := s.installedRegistry()
	if current == nil || len(current.entries) == 0 {
		return
	}
	next := &residencyRegistry{entries: make(map[string]*residencyEntry, len(current.entries))}
	for key, entry := range current.entries {
		vm := entry.model
		providerCfg, ok := providers[vm.Provider]
		if !ok {
			if entry.target != nil {
				s.slots.Delete(entry.target.slotKey)
			}
			next.entries[key] = &residencyEntry{model: vm}
			if s.logger != nil {
				s.logger.Warn("virtual model %q lost its provider %q: requests to this name are refused until it is configured again", vm.Name, vm.Provider)
			}
			continue
		}
		target, err := buildResidencyTarget(vm.Provider, providerCfg)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("virtual model %q cannot read residency from provider %q after the edit: %v; keeping the previous endpoint", vm.Name, vm.Provider, err)
			}
			next.entries[key] = entry
			continue
		}
		s.forgetProbeState(entry.target, target)
		next.entries[key] = &residencyEntry{model: vm, target: target}
	}
	s.registry.Store(next)
}

// forgetProbeState drops the failure backoff and the detected adapter a slot remembers about an
// endpoint that was just reconfigured. The slot is keyed by provider and base URL, so a rotated
// key, an edited header or a changed adapter all land on the slot the old config dirtied. A target
// whose fingerprint matches the one it replaces was rebuilt but not changed, and keeps its state.
func (s *VirtualModelService) forgetProbeState(previous, target *residencyTarget) {
	if previous != nil && previous.identity == target.identity {
		return
	}
	existing, ok := s.slots.Load(target.slotKey)
	if !ok {
		return
	}
	slot := existing.(*residencySlot)
	slot.mu.Lock()
	slot.failedUntil = time.Time{}
	// The configured adapter wins over a previously detected one: an operator who switched the
	// adapter wants the switch honoured, not the answer auto-detection gave under the old config.
	slot.adapter = target.adapter
	slot.mu.Unlock()
}

// Enabled reports whether any virtual model is configured, so the listing handlers can
// skip their work entirely on a gateway that does not use this feature.
func (s *VirtualModelService) Enabled() bool {
	if s == nil {
		return false
	}
	registry := s.installedRegistry()
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
	if !ok {
		// The definition came from somewhere other than the live registry: a config reload replaced
		// it mid-flight. Refusing beats guessing at what the name meant.
		return nil, schemas.NewVirtualModelError(503, fmt.Sprintf(
			"virtual model %q is not installed; retry after the next config reload", vm.Name))
	}
	if entry.target == nil {
		// Kept across a provider deletion, so the name is still advertised but has nothing to probe.
		return nil, schemas.NewVirtualModelError(503, fmt.Sprintf(
			"virtual model %q has no probe endpoint: provider %q is not configured", vm.Name, vm.Provider))
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
	if entry.target == nil {
		return entry, nil, fmt.Errorf("virtual model %q has no probe endpoint: provider %q is not configured", entry.model.Name, entry.model.Provider)
	}
	return entry, s.snapshot(ctx, entry.target), nil
}

func (s *VirtualModelService) lookupEntry(name string) (*residencyEntry, bool) {
	registry := s.installedRegistry()
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
	now := time.Now()
	if slot.snapshot != nil && now.Before(slot.expiresAt) {
		snapshot := slot.snapshot
		slot.mu.Unlock()
		return snapshot
	}
	if flight := slot.flight; flight != nil {
		slot.mu.Unlock()
		select {
		case <-flight.done:
			return flight.snapshot
		case <-ctx.Done():
			return &schemas.ResidencySnapshot{Provider: target.provider, ObservedAt: now}
		}
	}
	if now.Before(slot.failedUntil) {
		// A probe failed moments ago. Making this request wait out another doomed probe turns one
		// dead backend into a timeout on every virtual-model request, so answer from what is known:
		// the last good reading inside the stale cap, otherwise residency unknown.
		previous := slot.snapshot
		slot.mu.Unlock()
		return unknownAfterFailure(previous, now, target, s.logger)
	}
	flight := &residencyFlight{done: make(chan struct{})}
	slot.flight = flight
	known := slot.adapter
	previous := slot.snapshot
	slot.mu.Unlock()

	// The probe is shared, so it must not inherit the fate of whichever caller arrived first: a
	// client disconnecting mid-probe would fail it for everyone waiting on the result. The timeout
	// still bounds the call, so nothing runs unbounded.
	parent := context.Context(context.Background())
	if ctx != nil {
		parent = context.WithoutCancel(ctx)
	}
	probeCtx, cancel := context.WithTimeout(parent, target.timeout)
	defer cancel()

	snapshot, adapter, err := probeResidency(probeCtx, target, known, s.httpClient(target))
	if err != nil {
		if previous != nil {
			snapshot = unknownAfterFailure(previous, now, target, s.logger)
		} else {
			if s.logger != nil {
				s.logger.Warn("residency probe of %s failed with no earlier reading: %v", target.baseURL, err)
			}
			snapshot = &schemas.ResidencySnapshot{Provider: target.provider, ObservedAt: now}
		}
	}

	slot.mu.Lock()
	if err == nil {
		slot.adapter = adapter
		slot.snapshot = snapshot
		slot.expiresAt = time.Now().Add(target.ttl)
		slot.failedUntil = time.Time{}
	} else {
		// Remember the failure, not just the stale reading, so the next request answers at once.
		slot.failedUntil = time.Now().Add(target.failBackoff)
	}
	flight.snapshot = snapshot
	slot.flight = nil
	close(flight.done)
	slot.mu.Unlock()
	return snapshot
}

// unknownAfterFailure decides what a backend that did not answer means. A reading inside the stale
// cap still counts: the backend really did say it, and ObservedAt says how old it is. Beyond the
// cap the copy is marked non-authoritative, so the request refuses (or takes the first candidate
// per on_probe_error) instead of routing or cold-loading on an observation from before the outage.
// The stored snapshot is untouched, so the next success restores it.
func unknownAfterFailure(previous *schemas.ResidencySnapshot, now time.Time, target *residencyTarget, logger schemas.Logger) *schemas.ResidencySnapshot {
	if previous == nil {
		return &schemas.ResidencySnapshot{Provider: target.provider, ObservedAt: now}
	}
	staleCap := target.staleCap
	if staleCap <= 0 {
		// A target built without a window (tests, or any future path that skips the builder) must
		// not read as "believe nothing".
		staleCap = residencyMinStaleAge
	}
	if age := now.Sub(previous.ObservedAt); previous.Authoritative && age > staleCap {
		demoted := *previous
		demoted.Authoritative = false
		if logger != nil {
			logger.Warn("residency of %s last observed %s ago, past the %s stale window: treating it as unknown", target.baseURL, age, staleCap)
		}
		return &demoted
	}
	if logger != nil {
		logger.Debug("residency probe of %s failed, reusing a reading observed %s ago", target.baseURL, now.Sub(previous.ObservedAt))
	}
	// Inside the cap the reading is untouched, so hand back the same snapshot rather than a copy.
	return previous
}

// slotFor returns the per-endpoint slot, created on first use. The key was built when the
// target was installed, so a cached read costs a map load and no allocation.
func (s *VirtualModelService) slotFor(target *residencyTarget) *residencySlot {
	key := target.slotKey
	if key == "" {
		// Only a hand-built target (tests) reaches here; one from buildResidencyTarget always
		// carries a key, since an empty base URL is refused at install.
		key = string(target.provider) + "|" + target.baseURL
	}
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
	} else {
		if target.adapter != "" && target.adapter != schemas.ResidencyAdapterAuto {
			known = target.adapter
		}
		if known != "" && known != schemas.ResidencyAdapterAuto {
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
	}

	var lastErr error
	for _, probe := range candidates {
		path := probe.path
		if target.path != "" {
			path = target.path
		}
		body, err := fetchResidencyJSON(ctx, target, client, path)
		if err != nil {
			lastErr = err
			continue
		}
		snapshot := &schemas.ResidencySnapshot{Provider: target.provider, ObservedAt: time.Now(), States: map[string]schemas.ResidencyState{}, MissingState: schemas.ResidencyUnknown}
		if probe.membership {
			snapshot.MissingState = schemas.ResidencyUnloaded
		}
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
		// Register every name the entry offers. oMLX serves an alias that differs from
		// the id - embeddinggemma-2-bf16 is aliased embeddinggemma-2 - and a candidate
		// may legitimately be written either way.
		state := schemas.ResidencyUnloaded
		_, hasLoaded := entry["loaded"].(bool)
		_, hasLoading := entry["is_loading"].(bool)
		understood := hasLoaded || hasLoading
		switch strings.ToLower(firstString(entry, "value", "state", "status")) {
		case "loaded", "ready", "ready_to_use":
			state, understood = schemas.ResidencyReady, true
		case "loading", "no_slot":
			state, understood = schemas.ResidencyLoading, true
		case "unloaded":
			understood = true
		}
		if !understood {
			return fmt.Errorf("model entry has no recognized residency state")
		}
		if boolField(entry, "loaded") {
			state = schemas.ResidencyReady
		}
		if boolField(entry, "is_loading") {
			state = schemas.ResidencyLoading
		}
		registered := false
		for _, key := range []string{"id", "model", "model_alias", "name"} {
			name := stringField(entry, key)
			if name == "" {
				continue
			}
			snapshot.SetState(name, state)
			registered = true
		}
		if registered {
			seen = true
		}
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
	snapshot.MissingState = schemas.ResidencyUnloaded
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
				snapshot.SetState(name, schemas.ResidencyReady)
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
	if fields.Loaded == "" && fields.Loading == "" {
		snapshot.MissingState = schemas.ResidencyUnloaded
	}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := stringField(entry, fields.ID)
		if name == "" {
			return fmt.Errorf("custom residency entry has no model identifier")
		}
		state := schemas.ResidencyReady
		if fields.Loaded != "" || fields.Loading != "" {
			understood := false
			for _, field := range []string{fields.Loaded, fields.Loading} {
				if value, exists := entry[field]; field != "" && exists {
					if _, ok := value.(bool); !ok {
						return fmt.Errorf("custom residency field %q is not a boolean", field)
					}
					understood = true
				}
			}
			if !understood {
				return fmt.Errorf("custom residency entry has no mapped state")
			}
			state = schemas.ResidencyUnloaded
			if fields.Loaded != "" && boolField(entry, fields.Loaded) {
				state = schemas.ResidencyReady
			}
			if fields.Loading != "" && boolField(entry, fields.Loading) {
				state = schemas.ResidencyLoading
			}
		}
		snapshot.SetState(name, state)
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
