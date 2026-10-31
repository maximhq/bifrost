package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
)

// PluginPipeline is the narrow view of the core plugin pipeline the relay needs, declared here so
// this package never imports the core bifrost package. It is the Relays analogue of
// agent.PluginPipeline.
type PluginPipeline interface {
	RunRelayPreHooks(ctx *schemas.BifrostContext, req *schemas.BifrostRelayRequest, entered func(int)) (*schemas.BifrostRelayRequest, *schemas.RelayPluginShortCircuit, int)
	RunRelayPostHooks(ctx *schemas.BifrostContext, resp *schemas.BifrostRelayResponse, bifrostErr *schemas.BifrostError, runFrom int) (*schemas.BifrostRelayResponse, *schemas.BifrostError)
}

// Store is the persistence the manager reads relays from and writes them to, satisfied by the
// config store. It is declared here so this package never imports the framework, as agent.Store is.
type Store interface {
	ListRelays(ctx context.Context) ([]*schemas.RelayConfig, error)
	GetRelay(ctx context.Context, name string) (*schemas.RelayConfig, error)
	CreateRelay(ctx context.Context, cfg *schemas.RelayConfig, configHash string) error
	UpdateRelay(ctx context.Context, cfg *schemas.RelayConfig, configHash string) error
	DeleteRelay(ctx context.Context, name string) error
	GetRelayGrantedVirtualKeyIDs(ctx context.Context, name string) ([]string, error)
}

var (
	// ErrStoreDisabled is returned by every store-backed method of a manager built without a store.
	ErrStoreDisabled = errors.New("config store is disabled: relays cannot be managed")
	// ErrInvalidConfig wraps a config that does not validate or compile, so a handler can answer
	// 400 without parsing the message.
	ErrInvalidConfig = errors.New("invalid relay")
)

// Manager holds the configured endpoints and relays calls to them. Reads take one atomic load;
// writes build a new map and swap it in, so a request never sees a half-updated endpoint.
// Mutations that touch the store (Create, Update, Delete, Reload, ReloadRelay) are serialized by
// mutations, so the live endpoint, the stored row and a rollback after a failed save always come
// from the same call, the way agent.Manager serializes registration changes.
type Manager struct {
	endpoints atomic.Pointer[map[string]*endpoint]
	writeMu   sync.Mutex
	mutations sync.Mutex
	store     Store

	pipelineMu      sync.RWMutex
	acquirePipeline func() PluginPipeline
	releasePipeline func(PluginPipeline)

	// globalProxy returns the proxy an endpoint without its own inherits, nil for none.
	globalProxy func() *schemas.ProxyConfig
	logger      schemas.Logger
}

// NewManager returns an empty manager. store may be nil, in which case endpoints can only be made
// live through Upsert and Replace and every store-backed method returns ErrStoreDisabled.
func NewManager(store Store, logger schemas.Logger) *Manager {
	m := &Manager{store: store, logger: logger}
	empty := map[string]*endpoint{}
	m.endpoints.Store(&empty)
	return m
}

// SetPluginPipeline wires the relay to the core plugin pipeline. With none set, calls are relayed
// without hooks (no governance, no logging).
func (m *Manager) SetPluginPipeline(acquire func() PluginPipeline, release func(PluginPipeline)) {
	m.pipelineMu.Lock()
	defer m.pipelineMu.Unlock()
	m.acquirePipeline, m.releasePipeline = acquire, release
}

// SetGlobalProxy sets the source of the proxy inherited by endpoints with no proxy_config. It takes
// effect on the next Upsert of each endpoint.
func (m *Manager) SetGlobalProxy(fn func() *schemas.ProxyConfig) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	m.globalProxy = fn
}

// Upsert validates and compiles cfg and makes it live, replacing an endpoint of the same name.
func (m *Manager) Upsert(cfg *schemas.RelayConfig) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	if cfg == nil {
		return fmt.Errorf("relay config is required")
	}
	proxy := cfg.ProxyConfig
	if proxy == nil && m.globalProxy != nil {
		proxy = m.globalProxy()
	}
	e, err := newEndpoint(cfg, proxy)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	old := *m.endpoints.Load()
	next := make(map[string]*endpoint, len(old)+1)
	for k, v := range old {
		next[k] = v
	}
	next[cfg.Name] = e
	m.endpoints.Store(&next)
	return nil
}

// RemoveRuntime takes an endpoint out of service without touching the store, after its row was
// deleted here or on another node. Calls already in flight finish on the old instance.
func (m *Manager) RemoveRuntime(name string) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	old := *m.endpoints.Load()
	if _, ok := old[name]; !ok {
		return
	}
	next := make(map[string]*endpoint, len(old))
	for k, v := range old {
		if k != name {
			next[k] = v
		}
	}
	m.endpoints.Store(&next)
}

// Replace swaps the whole set, for a resync from the config store. Endpoints that fail to compile
// are skipped and reported; the rest go live.
func (m *Manager) Replace(cfgs []*schemas.RelayConfig) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	next := make(map[string]*endpoint, len(cfgs))
	var errs []string
	for _, cfg := range cfgs {
		if cfg == nil {
			continue
		}
		proxy := cfg.ProxyConfig
		if proxy == nil && m.globalProxy != nil {
			proxy = m.globalProxy()
		}
		e, err := newEndpoint(cfg, proxy)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", cfg.Name, err))
			continue
		}
		next[cfg.Name] = e
	}
	m.endpoints.Store(&next)
	if len(errs) > 0 {
		return fmt.Errorf("relays skipped: %s", strings.Join(errs, "; "))
	}
	return nil
}

// List returns every stored relay.
func (m *Manager) List(ctx context.Context) ([]*schemas.RelayConfig, error) {
	if m.store == nil {
		return nil, ErrStoreDisabled
	}
	return m.store.ListRelays(ctx)
}

// Get returns one stored relay; the store's not-found error passes through.
func (m *Manager) Get(ctx context.Context, name string) (*schemas.RelayConfig, error) {
	if m.store == nil {
		return nil, ErrStoreDisabled
	}
	return m.store.GetRelay(ctx, name)
}

// Create validates cfg, stores it (so the vault and encryption hooks see the credential) and makes
// the stored row live. A row that cannot be read back or compiled is removed again, so a retry
// never meets a conflict for a relay that never served. It returns the stored, resolved config.
func (m *Manager) Create(ctx context.Context, cfg *schemas.RelayConfig) (*schemas.RelayConfig, error) {
	if m.store == nil {
		return nil, ErrStoreDisabled
	}
	if cfg == nil {
		return nil, fmt.Errorf("%w: config is required", ErrInvalidConfig)
	}
	cfg.Name = strings.TrimSpace(cfg.Name)
	if err := cfg.ValidateEndpoint(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	m.mutations.Lock()
	defer m.mutations.Unlock()
	if err := m.store.CreateRelay(ctx, cfg, ""); err != nil {
		return nil, err
	}
	stored, err := m.store.GetRelay(ctx, cfg.Name)
	if err != nil {
		m.rollbackCreate(cfg.Name)
		return nil, err
	}
	if err := m.Upsert(stored); err != nil {
		// The row is valid by construction; a compile failure here (a bad CA, a proxy that does not
		// parse) is reported, and the stored row is rolled back so the two never disagree.
		m.rollbackCreate(cfg.Name)
		return nil, err
	}
	return stored, nil
}

// rollbackCreate removes a just-created row that could not go live. It runs under its own bounded
// context, since the caller's may be what timed out, and logs a failure rather than hide it: the
// row would then stay stored but unserved until it is deleted.
func (m *Manager) rollbackCreate(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.store.DeleteRelay(ctx, name); err != nil && m.logger != nil {
		m.logger.Error("relay %s: create failed and removing the stored row also failed: %v", name, err)
	}
}

// Update replaces the relay named with cfg. A secret sent back in its redacted form keeps the
// stored value, so a client can save an edit without ever holding the credential. The new config
// goes live before the row is written, so a config that does not compile is refused without a
// write; a write that fails restores the previous config.
func (m *Manager) Update(ctx context.Context, name string, cfg *schemas.RelayConfig) (*schemas.RelayConfig, error) {
	if m.store == nil {
		return nil, ErrStoreDisabled
	}
	if cfg == nil {
		return nil, fmt.Errorf("%w: config is required", ErrInvalidConfig)
	}
	if cfg.Name != "" && cfg.Name != name {
		return nil, fmt.Errorf("%w: a relay's name cannot be changed", ErrInvalidConfig)
	}
	m.mutations.Lock()
	defer m.mutations.Unlock()
	previous, err := m.store.GetRelay(ctx, name)
	if err != nil {
		return nil, err
	}
	cfg.Name = name
	cfg.MergeRedacted(previous)
	if err := cfg.ValidateEndpoint(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if err := m.Upsert(cfg); err != nil {
		return nil, err
	}
	if err := m.store.UpdateRelay(ctx, cfg, ""); err != nil {
		if rbErr := m.Upsert(previous); rbErr != nil && m.logger != nil {
			m.logger.Error("relay %s: save failed and restoring the previous live config also failed: %v", name, rbErr)
		}
		return nil, err
	}
	return cfg, nil
}

// Delete removes the stored row and takes the relay out of service. It returns the ids of the
// virtual keys whose grants the delete cascaded away, so the caller can refresh their in-memory
// copies; the store's not-found error passes through when there is no such relay.
func (m *Manager) Delete(ctx context.Context, name string) ([]string, error) {
	if m.store == nil {
		return nil, ErrStoreDisabled
	}
	m.mutations.Lock()
	defer m.mutations.Unlock()
	if _, err := m.store.GetRelay(ctx, name); err != nil {
		return nil, err
	}
	// Read the grants before deleting, since the cascade removes them.
	granted, err := m.store.GetRelayGrantedVirtualKeyIDs(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("read relay grants: %w", err)
	}
	if err := m.store.DeleteRelay(ctx, name); err != nil {
		return nil, err
	}
	m.RemoveRuntime(name)
	return granted, nil
}

// Reload recompiles every relay from the store, under the same lock as the other mutations so a
// rebuild never installs a snapshot older than a save that ran alongside it. It runs at startup
// and after the global proxy changes, since a relay without its own proxy resolved the inherited
// one when built.
func (m *Manager) Reload(ctx context.Context) error {
	if m.store == nil {
		return ErrStoreDisabled
	}
	m.mutations.Lock()
	defer m.mutations.Unlock()
	cfgs, err := m.store.ListRelays(ctx)
	if err != nil {
		return err
	}
	return m.Replace(cfgs)
}

// ReloadRelay brings one live relay in line with its stored row, which is what another node
// does when told the relay was created or updated here. A relay deleted elsewhere is removed
// with RemoveRuntime.
func (m *Manager) ReloadRelay(ctx context.Context, name string) (*schemas.RelayConfig, error) {
	if m.store == nil {
		return nil, ErrStoreDisabled
	}
	m.mutations.Lock()
	defer m.mutations.Unlock()
	stored, err := m.store.GetRelay(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := m.Upsert(stored); err != nil {
		return nil, err
	}
	return stored, nil
}

// Names lists the live endpoints, sorted.
func (m *Manager) Names() []string {
	current := *m.endpoints.Load()
	names := make([]string, 0, len(current))
	for name := range current {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (m *Manager) get(name string) *endpoint {
	return (*m.endpoints.Load())[name]
}

// Request is one caller request to /relay/{Endpoint}{Path}. Headers are lower-cased with the
// caller's credentials and gateway headers already stripped.
type Request struct {
	Endpoint string
	Method   string
	Path     string // decoded, rooted
	RawQuery string
	Headers  map[string]string
	Body     []byte
}

// Result is a relayed call. Exactly one of Response (buffered, post-hooks done) and Stream
// (post-hooks run when the stream ends) is set.
type Result struct {
	Response *schemas.BifrostRelayResponse
	Stream   *Stream
}

func statusError(status int, message string) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     schemas.Ptr(status),
		Error:          &schemas.ErrorField{Message: message},
	}
}

// Serve relays one caller request: it matches the route, runs the Relays pre-hooks
// (which may refuse the call), sends the request, prices the reply and runs the post-hooks. A
// refusal before the hooks (unknown endpoint, no route, unmeasurable body) is returned as an error
// with its status and reaches no hook.
func (m *Manager) Serve(ctx *schemas.BifrostContext, in Request) (*Result, *schemas.BifrostError) {
	e := m.get(in.Endpoint)
	if e == nil {
		return nil, statusError(http.StatusNotFound, fmt.Sprintf("relay %q not found", in.Endpoint))
	}
	if name, dup := ambiguousHeader(in.Headers); dup {
		return nil, statusError(http.StatusBadRequest, fmt.Sprintf("header %s is set under two spellings; send it once", name))
	}
	// The model is read under the content type the upstream receives: an operator's Content-Type
	// replaces the caller's on the wire.
	contentType := in.Headers["content-type"]
	if ct, ok := e.operatorHeaders["content-type"]; ok {
		contentType = ct
	}
	p := e.plan(in.Method, in.Path, in.Headers)
	if !p.allowed {
		if p.route != nil {
			return nil, statusError(http.StatusNotFound, fmt.Sprintf("route %s %s on relay %q is disabled", in.Method, in.Path, in.Endpoint))
		}
		return nil, statusError(http.StatusNotFound, fmt.Sprintf("no route for %s %s on relay %q", in.Method, in.Path, in.Endpoint))
	}
	model, err := ReadRequestModel(p.usage.modelSource, in.Body, contentType, in.RawQuery, p.usage.effectiveModelPath())
	if err != nil {
		return nil, statusError(http.StatusBadRequest, err.Error())
	}
	if p.billing {
		if err := p.usage.requestBodyError(in.Body); err != nil {
			return nil, statusError(http.StatusBadRequest, err.Error())
		}
	}
	req := &schemas.BifrostRelayRequest{
		RelayName:  e.cfg.Name,
		Method:     strings.ToUpper(in.Method),
		Path:       in.Path,
		RawQuery:   in.RawQuery,
		Headers:    in.Headers,
		Body:       in.Body,
		Model:      model,
		RouteIndex: p.index,
		Billable:   p.billing,
		Streams:    p.stream == streamOn,
	}
	if p.route != nil {
		req.RoutePath = e.cfg.Routes[p.index].Path
		req.RouteHeaders = p.route.matchedHeaders(in.Headers, e.operatorLogHeaders)
	}
	if requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string); requestID == "" {
		ctx.SetValue(schemas.BifrostContextKeyRequestID, uuid.New().String())
	}
	return m.run(ctx, e, p, req)
}

// run is the hook gate: pre-hooks, the relay, then post-hooks for exactly the plugins whose
// pre-hooks ran. A streamed call keeps the pipeline until the stream ends.
func (m *Manager) run(ctx *schemas.BifrostContext, e *endpoint, p plan, req *schemas.BifrostRelayRequest) (result *Result, finalErr *schemas.BifrostError) {
	// Latency is the wall-clock the call spends in Bifrost, from the pre-hooks to the end of the
	// reply, and the upstream socket time accumulates on the context as it does for an LLM call
	// (DoHTTPRequest adds to it), so the row and the trace can split the two. A call the pre-hooks
	// answer (an access denial) never reaches the upstream and still records the time it took.
	start := time.Now()
	ctx.ResetUpstreamLatency()
	entrySpanID := ctx.Value(schemas.BifrostContextKeySpanID)
	var span *relaySpan
	m.pipelineMu.RLock()
	acquire, release := m.acquirePipeline, m.releasePipeline
	m.pipelineMu.RUnlock()
	var pipeline PluginPipeline
	if acquire != nil {
		pipeline = acquire()
	}
	released := false
	releaseOnce := func() {
		if !released && pipeline != nil && release != nil {
			released = true
			release(pipeline)
		}
	}

	preCount := 0
	postRun := false
	runPost := func(resp *schemas.BifrostRelayResponse, bErr *schemas.BifrostError) (*schemas.BifrostRelayResponse, *schemas.BifrostError) {
		if postRun {
			return resp, bErr
		}
		postRun = true
		if resp != nil {
			resp.Latency = time.Since(start).Milliseconds()
			if upstream, ok := schemas.GetUpstreamLatency(ctx); ok {
				resp.UpstreamLatency = upstream.Milliseconds()
			}
		}
		// The call is timed and its span ended whether or not hooks run: a manager with no plugin
		// pipeline still traces. The operation span ends before the post-hooks, as for MCP tools,
		// so its duration is the call and the hooks nest after it under the same parent.
		span.end(ctx, resp, bErr)
		// The root span takes the upstream figure so the tracer can stamp Bifrost's overhead and
		// the logging plugin can backfill the row from the trace, as it does for LLM rows.
		ctx.StampUpstreamLatency()
		if pipeline == nil {
			return resp, bErr
		}
		return pipeline.RunRelayPostHooks(ctx, resp, bErr, preCount)
	}
	streaming := false
	defer func() {
		if r := recover(); r != nil {
			if m.logger != nil {
				m.logger.Error("recovered from panic in relay %s: %v", req.RelayName, r)
			}
			bErr := statusError(http.StatusInternalServerError, fmt.Sprintf("panic in relay: %v", r))
			bErr.ExtraFields.Latency = time.Since(start).Milliseconds()
			_, postErr := runPost(nil, bErr)
			if postErr == nil {
				postErr = bErr
			}
			result, finalErr = nil, postErr
			streaming = false
		}
		if !streaming {
			releaseOnce()
		}
	}()

	if pipeline != nil {
		var shortCircuit *schemas.RelayPluginShortCircuit
		var preReq *schemas.BifrostRelayRequest
		preReq, shortCircuit, preCount = pipeline.RunRelayPreHooks(ctx, req, func(entered int) { preCount = entered })
		if shortCircuit != nil {
			if shortCircuit.Response != nil {
				shortCircuit.Response.RelayName = req.RelayName
			}
			if shortCircuit.Error != nil && shortCircuit.Error.ExtraFields.Latency == 0 {
				shortCircuit.Error.ExtraFields.Latency = time.Since(start).Milliseconds()
			}
			// A refused call still gets its operation span, so the relay metrics derived from it
			// count denied grants, exhausted budgets and rate limits in the request and error
			// totals, as the MCP post-hook counts a refused tool call.
			span = startRelaySpan(ctx, entrySpanID, req)
			resp, bErr := runPost(shortCircuit.Response, shortCircuit.Error)
			if bErr != nil {
				return nil, bErr
			}
			if resp == nil {
				return nil, statusError(http.StatusInternalServerError, "relay short-circuit carried no response")
			}
			return &Result{Response: resp}, nil
		}
		if preReq != nil {
			req = preReq
		}
	}

	span = startRelaySpan(ctx, entrySpanID, req)
	upstream := upstreamRequest{method: req.Method, path: req.Path, rawQuery: req.RawQuery, headers: req.Headers, body: req.Body}
	requestUnits := p.usage.requestUnits(req.Body)

	var reply *bufferedResult
	var bErr *schemas.BifrostError
	if p.stream == streamOff {
		reply, bErr = e.doBuffered(ctx, upstream)
	} else {
		resp, start, releaseOpen, openErr := e.openStream(ctx, upstream)
		if openErr != nil {
			return recovered(runPost(nil, openErr))
		}
		if p.stream == streamOn || isEventStream(resp.Header.Get("Content-Type")) {
			streaming = true
			stream := newStream(ctx, e, resp, start, p.usage, p.billing, requestUnits, func(final *schemas.BifrostRelayResponse, bErr *schemas.BifrostError) {
				final.RelayName = req.RelayName
				runPost(final, bErr)
				releaseOpen()
				releaseOnce()
			})
			return &Result{Stream: stream}, nil
		}
		// Not an event stream: read and price it as a buffered reply, whatever the caller asked for.
		reply, bErr = e.readBuffered(resp, start, true)
		releaseOpen()
	}
	if bErr != nil {
		return recovered(runPost(nil, bErr))
	}
	resp := &schemas.BifrostRelayResponse{
		RelayName:  req.RelayName,
		StatusCode: reply.status,
		Headers:    reply.headers,
		Body:       reply.body,
		BytesOut:   int64(len(reply.body)),
		Latency:    reply.latency.Milliseconds(),
	}
	if p.billing && reply.status >= 200 && reply.status < 300 {
		resp.Usage = extractUsage(p.usage, reply.contentType, reply.body, requestUnits)
		// A route that starts a job hands the job id and its settlement snapshot to the logging
		// plugin, which records it for the sweeper. The start call is still counted here.
		resp.Usage.Job = p.route.jobHandle(reply.contentType, reply.body)
	}
	// A post-hook that clears the response without an error is refused like an empty recovery.
	return recovered(runPost(resp, nil))
}

// recovered turns a post-hook outcome into a result: a post-hook may replace an error with a
// response, which is then returned as a buffered reply, and an outcome with neither is refused.
func recovered(resp *schemas.BifrostRelayResponse, bErr *schemas.BifrostError) (*Result, *schemas.BifrostError) {
	if bErr != nil {
		return nil, bErr
	}
	if resp == nil {
		return nil, statusError(http.StatusBadGateway, "relay post-hooks returned neither a response nor an error")
	}
	return &Result{Response: resp}, nil
}

// JobPollResponse is the upstream reply to a job poll.
type JobPollResponse struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

// PollJob sends a job poll for the sweeper with the endpoint's credential. It skips the hooks and
// the route check: the poll is the operator's own configured request, not a caller's, and is not
// billed by itself.
func (m *Manager) PollJob(ctx *schemas.BifrostContext, endpointName string, poll JobPollRequest) (*JobPollResponse, *schemas.BifrostError) {
	e := m.get(endpointName)
	if e == nil {
		return nil, statusError(http.StatusNotFound, fmt.Sprintf("relay %q not found", endpointName))
	}
	headers := make(map[string]string, len(poll.Headers))
	for k, v := range poll.Headers {
		headers[k] = v
	}
	reply, bErr := e.doBuffered(ctx, upstreamRequest{method: poll.Method, path: poll.Path, rawQuery: poll.RawQuery, headers: headers, body: poll.Body, internal: true})
	if bErr != nil {
		return nil, bErr
	}
	return &JobPollResponse{StatusCode: reply.status, ContentType: reply.contentType, Body: reply.body}, nil
}
