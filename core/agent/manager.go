package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	a2agrpc "github.com/a2aproject/a2a-go/v2/a2agrpc/v1"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"golang.org/x/mod/semver"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const (
	maxOutcomeText           = 256
	refreshCooldown          = time.Second
	runtimeBuildTimeout      = 30 * time.Second
	maxAgentCardResponseSize = 1 << 20 // Agent Cards are small metadata documents; cap discovery responses at 1 MiB.
	maxExtensionPayloadSize  = 64 << 10
	maxExtensionURICount     = 32
	maxExtensionURILength    = 2048
	oauthFetchTimeout        = 2 * time.Minute
)

// upstreamHTTPStatusError preserves the upstream HTTP status that the SDK client
// would otherwise flatten into an opaque transport failure. The status is what
// lets the gateway distinguish a stale cached interface URL from an ordinary
// upstream error, so it must survive as a typed error.
type upstreamHTTPStatusError struct {
	StatusCode int
	Status     string
}

// Error reports the upstream status without the response body, which may contain
// upstream-controlled or sensitive content.
func (e *upstreamHTTPStatusError) Error() string {
	return "unexpected upstream HTTP status: " + e.Status
}

// staleInterfaceHint reports whether a failure suggests the cached upstream card
// no longer describes a reachable endpoint. A pooled SDK client is built from a
// card resolved earlier, so these statuses are treated as a signal to rebuild the
// client generation rather than as a permanent request failure.
func staleInterfaceHint(err error) bool {
	var statusErr *upstreamHTTPStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	switch statusErr.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusGone, http.StatusUpgradeRequired:
		return true
	default:
		return false
	}
}

var agentNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

var (
	ErrNotFound          = errors.New("agent registration not found")
	errAgentCardTooLarge = errors.New("upstream agent card exceeds 1 MiB size limit")
)

// Store is the narrow persistence contract the Agent Gateway needs, declared here
// so core does not depend on the framework config store. Registrations must be
// durable because the runtime is rebuilt from them at startup.
type Store interface {
	CreateAgentRegistration(context.Context, *schemas.AgentRegistration) error
	UpdateAgentRegistration(context.Context, *schemas.AgentRegistration) error
	ListAgentRegistrations(context.Context) ([]schemas.AgentRegistration, error)
	GetAgentRegistration(context.Context, string) (*schemas.AgentRegistration, error)
	DeleteAgentRegistration(context.Context, string) error
}

// sdkClient is the subset of the A2A SDK client the gateway actually uses,
// isolated as an interface so tests can substitute an upstream without a real
// server. It mirrors the protocol methods the gateway forwards today, including
// the push-config registration hop the durable push relay performs. Push-config
// reads and the local delete bookkeeping are served from the gateway's own
// store, so the read methods are deliberately absent. Destroy must release the
// client's transport resources.
type sdkClient interface {
	GetTask(context.Context, *a2a.GetTaskRequest) (*a2a.Task, error)
	ListTasks(context.Context, *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error)
	CancelTask(context.Context, *a2a.CancelTaskRequest) (*a2a.Task, error)
	SendMessage(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error)
	SendStreamingMessage(context.Context, *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error]
	SubscribeToTask(context.Context, *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error]
	GetExtendedAgentCard(context.Context, *a2a.GetExtendedAgentCardRequest) (*a2a.AgentCard, error)
	CreateTaskPushConfig(context.Context, *a2a.PushConfig) (*a2a.PushConfig, error)
	DeleteTaskPushConfig(context.Context, *a2a.DeleteTaskPushConfigRequest) error
	Destroy() error
}

// clientGeneration pairs one resolved upstream card with the SDK client built
// from it, and reference-counts in-flight use of that pair. A generation exists
// so the gateway can atomically swap in a rebuilt client (after a registration
// update or a stale-interface hint) without destroying a client that a request is
// still using: retirement stops new leases and destruction happens only once the
// last lease is released.
type upstreamCandidate struct {
	client    sdkClient
	transport string
}

type clientGeneration struct {
	card       *a2a.AgentCard
	candidates []upstreamCandidate
	active     int

	mu        sync.Mutex
	cond      *sync.Cond
	leases    int
	retired   bool
	destroyed bool
}

func newCandidateGeneration(card *a2a.AgentCard, candidates []upstreamCandidate) *clientGeneration {
	g := &clientGeneration{card: card, candidates: candidates}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// acquire takes a lease for the duration of one upstream operation. It reports
// false once the generation is retired so the caller re-reads the runtime's
// current generation instead of using a client scheduled for destruction.
func (g *clientGeneration) acquire() (*generationLease, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.retired {
		return nil, false
	}
	g.leases++
	candidate := g.candidates[g.active]
	return &generationLease{generation: g, candidate: candidate, candidateIndex: g.active}, true
}

// advance moves future operations to the next candidate only when the caller
// still names the active candidate. It wraps after the final candidate so a new
// request can retry an earlier interface that may have recovered. The caller
// remains responsible for limiting one operation to one attempt per candidate.
func (g *clientGeneration) advance(candidateIndex int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.retired || g.active != candidateIndex || len(g.candidates) < 2 {
		return false
	}
	g.active = (g.active + 1) % len(g.candidates)
	return true
}

// acquireCandidate leases a specific candidate for a retry already bounded by
// the caller's request-local attempt count. It does not change the candidate
// selected for new requests.
func (g *clientGeneration) acquireCandidate(candidateIndex int) (*generationLease, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.retired || candidateIndex < 0 || candidateIndex >= len(g.candidates) {
		return nil, false
	}
	g.leases++
	return &generationLease{generation: g, candidate: g.candidates[candidateIndex], candidateIndex: candidateIndex}, true
}

// retire marks the generation unusable for new work and destroys it immediately
// when nothing holds a lease. Retiring is always safe to call on a generation
// that is still serving requests: destruction is deferred to the last release.
func (g *clientGeneration) retire() {
	g.mu.Lock()
	g.retired = true
	if g.leases == 0 {
		g.destroyLocked()
	}
	g.mu.Unlock()
}

// wait blocks until the generation's client has actually been destroyed, giving
// shutdown a point at which no upstream connection from this generation remains.
func (g *clientGeneration) wait() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for !g.destroyed {
		g.cond.Wait()
	}
}

// destroyLocked destroys the SDK client exactly once and wakes any waiters. The
// caller must hold g.mu; the destroy error is intentionally ignored because
// teardown has no recovery path.
func (g *clientGeneration) destroyLocked() {
	if g.destroyed {
		return
	}
	g.destroyed = true
	for _, candidate := range g.candidates {
		_ = candidate.client.Destroy()
	}
	g.cond.Broadcast()
}

// generationLease is a single caller's claim on a client generation. It is a
// distinct type so release is idempotent, preventing a double release (for
// example from both a deferred call and an error path) from corrupting the
// reference count and destroying a client still in use.
type generationLease struct {
	generation     *clientGeneration
	candidate      upstreamCandidate
	candidateIndex int
	once           sync.Once
}

// release drops this claim and destroys the generation if it was retired while
// the lease was held. Safe to call more than once.
func (l *generationLease) release() {
	l.once.Do(func() {
		g := l.generation
		g.mu.Lock()
		g.leases--
		if g.retired && g.leases == 0 {
			g.destroyLocked()
		}
		g.mu.Unlock()
	})
}

// initializationAttempt coalesces concurrent lazy client construction for one
// agent. Without it, a burst of first requests would each fetch the upstream card
// and build a client; instead every waiter observes the same attempt's outcome
// through done and err.
type initializationAttempt struct {
	done chan struct{}
	err  error
}

// runtimeConfig is the immutable serving configuration copied from a durable
// registration when runtime state is constructed. It contains only data needed
// to discover the upstream agent, build and authenticate its client, identify it
// in protocol behavior, and log forwarding outcomes.
type runtimeConfig struct {
	name                                   string
	agentCardURL                           string
	discoveryAuth                          *schemas.UpstreamAuth
	runtimeAuth                            *schemas.UpstreamAuth
	forwardAcceptedCredential              bool
	forwardAcceptedCredentialOverridesAuth bool
	extensionURIs                          []string
}

func upstreamCredentialID(agentName, phase string) string {
	return "agent-gateway:" + agentName + ":" + phase
}

func runtimeConfigFromRegistration(reg schemas.AgentRegistration) runtimeConfig {
	return runtimeConfig{
		name:                                   reg.Name,
		agentCardURL:                           reg.AgentCardURL,
		discoveryAuth:                          reg.DiscoveryAuth,
		runtimeAuth:                            reg.RuntimeAuth,
		forwardAcceptedCredential:              reg.ForwardAcceptedCredential,
		forwardAcceptedCredentialOverridesAuth: reg.ForwardAcceptedCredentialOverridesAuth,
		extensionURIs:                          slices.Clone(reg.ExtensionURIs),
	}
}

// runtimeAgent is the in-memory serving state for one enabled registration: the
// SDK protocol handler that fronts it and the currently valid client generation
// used to reach the upstream agent. Upstream discovery is deliberately lazy, so
// a registration reloaded at startup serves traffic without contacting the
// upstream until the first request arrives. Its context bounds every upstream
// operation, so closing the runtime cancels work in flight.
type runtimeAgent struct {
	config   runtimeConfig
	protocol http.Handler
	rest     http.Handler
	// handler is the shared RequestHandler behind every binding, exposed so the
	// transport layer can front it with additional protocol bindings (gRPC).
	handler a2asrv.RequestHandler

	mu           sync.Mutex
	generation   *clientGeneration
	initializing *initializationAttempt
	closed       bool
	ctx          context.Context
	cancel       context.CancelFunc
	lastRefresh  time.Time
	refreshing   bool
}

// runtimeRegistry is the lookup table from agent name to serving state. It exists
// so protocol traffic never queries the database and so a single registration can
// be replaced without disturbing others.
// It supports O(1) targeted replacement without cloning all registrations.
// Entries are immutable after publication, so readers can safely retain a loaded pointer.
type runtimeRegistry struct{ entries sync.Map }

// load returns the published runtime for an agent name, if it is enabled and present.
func (r *runtimeRegistry) load(id string) (*runtimeAgent, bool) {
	value, ok := r.entries.Load(id)
	if !ok {
		return nil, false
	}
	return value.(*runtimeAgent), true
}

// store publishes a runtime so subsequent requests resolve it atomically.
func (r *runtimeRegistry) store(id string, runtime *runtimeAgent) { r.entries.Store(id, runtime) }

// delete unpublishes a runtime; callers remain responsible for closing it.
func (r *runtimeRegistry) delete(id string) { r.entries.Delete(id) }

// clear unpublishes every runtime, used by full reload and shutdown before the
// removed runtimes are closed.
func (r *runtimeRegistry) clear() {
	r.entries.Range(func(key, _ any) bool { r.entries.Delete(key); return true })
}

type oauthToken struct {
	accessToken string
	expiresAt   time.Time
	fingerprint [sha256.Size]byte
}

// Manager is the Agent Gateway control plane and data-plane owner: it validates
// and persists registrations, publishes the immutable runtime state that protocol
// requests resolve, runs the A2A plugin gate that owns the authorization
// decision, and generates the gateway-facing agent card. Mutations are serialized
// so persistence and the published runtime cannot diverge.
type Manager struct {
	store              Store
	logger             schemas.Logger
	httpClient         *http.Client
	pushDeliveryClient *http.Client
	pushRelayID        string
	externalURL        string
	oauthMu            sync.Mutex
	oauthTokens        map[string]oauthToken
	oauthFingerprints  map[string][sha256.Size]byte
	oauthFetches       singleflight.Group
	// externalURLFn overrides externalURL when set; see
	// ManagerConfig.ExternalURLProvider.
	externalURLFn func() string
	authPolicy    schemas.AgentGatewayAuthPolicy
	// grpcBaseDomain and grpcPort mirror ManagerConfig; see its documentation.
	grpcBaseDomain string
	grpcPort       int
	runtimes       runtimeRegistry
	mutations      sync.Mutex
	draining       sync.WaitGroup
	ctx            context.Context
	cancel         context.CancelFunc
	closeOnce      sync.Once
	buildTimeout   time.Duration

	// pushStore and pushRelay exist only when the supplied store provides push
	// persistence. Push operations additionally require an external URL, which
	// is checked live per operation so it can be configured without a restart.
	pushStore      PushStore
	pushRelay      *pushRelay
	tracer         schemas.Tracer
	tracerProvider func() schemas.Tracer

	// Plugin pipeline providers borrow the current generation for each operation.
	pipelineMu      sync.RWMutex
	acquirePipeline func() PluginPipeline
	releasePipeline func(PluginPipeline)
}

// CreateRequest is the administrative registration input. Credentials are
// write-only, and VirtualKeyIDs lets an admin grant access from the agent side of
// the same join table the virtual-key APIs write from the other side.
type CreateRequest struct {
	Name                                   string                `json:"name"`
	AgentCardURL                           string                `json:"agent_card_url"`
	Tenant                                 string                `json:"tenant,omitempty"`
	Enabled                                *bool                 `json:"enabled,omitempty"`
	AllowByDefault                         bool                  `json:"allow_by_default"`
	ForwardAcceptedCredential              bool                  `json:"forward_accepted_credential"`
	ForwardAcceptedCredentialOverridesAuth bool                  `json:"forward_accepted_credential_overrides_auth"`
	DiscoveryAuth                          *schemas.UpstreamAuth `json:"discovery_auth,omitempty"`
	RuntimeAuth                            *schemas.UpstreamAuth `json:"runtime_auth,omitempty"`
	VirtualKeyIDs                          []string              `json:"virtual_key_ids,omitempty"`
	ExtensionURIs                          []string              `json:"extension_uris,omitempty"`
}

type InspectRequest struct {
	AgentCardURL  string                `json:"agent_card_url"`
	DiscoveryAuth *schemas.UpstreamAuth `json:"discovery_auth,omitempty"`
}

type InspectedSecurityScheme struct {
	Name             string   `json:"name"`
	Type             string   `json:"type"`
	Description      string   `json:"description,omitempty"`
	Location         string   `json:"in,omitempty"`
	Header           string   `json:"header,omitempty"`
	Scheme           string   `json:"scheme,omitempty"`
	BearerFormat     string   `json:"bearer_format,omitempty"`
	AuthorizationURL string   `json:"authorization_url,omitempty"`
	TokenURL         string   `json:"token_url,omitempty"`
	Scopes           []string `json:"scopes,omitempty"`
}

type InspectedAgentInterface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocol_binding"`
	ProtocolVersion string `json:"protocol_version"`
	Tenant          string `json:"tenant,omitempty"`
}

type InspectedAgentSkill struct {
	ID                   string     `json:"id"`
	Name                 string     `json:"name"`
	Description          string     `json:"description,omitempty"`
	Tags                 []string   `json:"tags,omitempty"`
	Examples             []string   `json:"examples,omitempty"`
	InputModes           []string   `json:"input_modes,omitempty"`
	OutputModes          []string   `json:"output_modes,omitempty"`
	SecurityRequirements [][]string `json:"security_requirements,omitempty"`
}

type InspectedAgentExtension struct {
	URI         string         `json:"uri"`
	Description string         `json:"description,omitempty"`
	Required    bool           `json:"required,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
}

type InspectResponse struct {
	Name                 string                    `json:"name,omitempty"`
	Description          string                    `json:"description,omitempty"`
	Version              string                    `json:"version,omitempty"`
	Provider             string                    `json:"provider,omitempty"`
	ProviderURL          string                    `json:"provider_url,omitempty"`
	DocumentationURL     string                    `json:"documentation_url,omitempty"`
	IconURL              string                    `json:"icon_url,omitempty"`
	Interfaces           []InspectedAgentInterface `json:"interfaces,omitempty"`
	Capabilities         map[string]bool           `json:"capabilities"`
	DefaultInputModes    []string                  `json:"default_input_modes,omitempty"`
	DefaultOutputModes   []string                  `json:"default_output_modes,omitempty"`
	Skills               []InspectedAgentSkill     `json:"skills,omitempty"`
	Extensions           []InspectedAgentExtension `json:"extensions,omitempty"`
	SignatureCount       int                       `json:"signature_count"`
	SecuritySchemes      []InspectedSecurityScheme `json:"security_schemes"`
	SecurityRequirements [][]string                `json:"security_requirements"`
	Warnings             []string                  `json:"warnings,omitempty"`
}

// UpdateRequest replaces a registration wholesale. A missing agent card URL and Enabled are
// pointers so the handler can reject a partial body outright rather than silently
// clearing fields, and a submitted credential whose secret is the masked
// placeholder preserves the stored secret instead of overwriting it.
type UpdateRequest struct {
	AgentCardURL                           *string               `json:"agent_card_url"`
	Tenant                                 string                `json:"tenant,omitempty"`
	Enabled                                *bool                 `json:"enabled"`
	AllowByDefault                         bool                  `json:"allow_by_default"`
	ForwardAcceptedCredential              bool                  `json:"forward_accepted_credential"`
	ForwardAcceptedCredentialOverridesAuth bool                  `json:"forward_accepted_credential_overrides_auth"`
	DiscoveryAuth                          *schemas.UpstreamAuth `json:"discovery_auth,omitempty"`
	RuntimeAuth                            *schemas.UpstreamAuth `json:"runtime_auth,omitempty"`
	VirtualKeyIDs                          []string              `json:"virtual_key_ids,omitempty"`
	ExtensionURIs                          []string              `json:"extension_uris,omitempty"`
}

// ManagerConfig configures optional Manager behavior.
//
// GRPCBaseDomain and GRPCPort advertise a per-agent gRPC interface on served
// cards, in the form <agent-name>.<GRPCBaseDomain>:<GRPCPort>. They only
// describe the advertised endpoint; the transport layer owns the actual gRPC
// listener. When GRPCBaseDomain is empty no gRPC interface is advertised.
type ManagerConfig struct {
	Tracer                schemas.Tracer
	TracerProvider        func() schemas.Tracer
	AuthPolicy            schemas.AgentGatewayAuthPolicy
	PluginPipelineAcquire func() PluginPipeline
	PluginPipelineRelease func(PluginPipeline)
	GRPCBaseDomain        string
	GRPCPort              int
	// ExternalURLProvider, when set, is consulted on every use of the public
	// base URL so admin configuration changes apply without a process restart,
	// matching how MCP reads the same setting per request. The constructor's
	// externalURL argument remains the fallback when unset.
	ExternalURLProvider func() string
}

// externalBaseURL returns the public base URL used for gateway-facing URLs
// (served cards and push callback endpoints). A configured provider is read
// live so admin configuration changes apply without a restart; the value
// captured at construction is the fallback.
func (m *Manager) externalBaseURL() string {
	if m.externalURLFn != nil {
		return strings.TrimRight(m.externalURLFn(), "/")
	}
	return m.externalURL
}

// NewManager builds the gateway and immediately loads durable registrations, so a
// process that starts successfully is already able to serve every previously
// registered agent. It fails rather than starting empty when the load fails. The
// manager owns a background context independent of ctx so runtime lifetimes are
// bounded by Close rather than by the startup call. Supplied plugin providers are
// installed before durable runtimes are loaded and before the manager is returned.
func NewManager(ctx context.Context, store Store, logger schemas.Logger, externalURL string, client *http.Client, configs ...ManagerConfig) (*Manager, error) {
	if store == nil {
		return nil, errors.New("agent gateway config store is required")
	}
	if client == nil {
		client = &http.Client{}
	}
	config := ManagerConfig{}
	if len(configs) > 0 {
		config = configs[0]
	}
	managerCtx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		tracer:             config.Tracer,
		tracerProvider:     config.TracerProvider,
		store:              store,
		logger:             logger,
		externalURL:        strings.TrimRight(externalURL, "/"),
		externalURLFn:      config.ExternalURLProvider,
		httpClient:         client,
		pushDeliveryClient: newPushDeliveryClient(),
		pushRelayID:        uuid.NewString(),
		oauthTokens:        make(map[string]oauthToken),
		oauthFingerprints:  make(map[string][sha256.Size]byte),
		authPolicy:         config.AuthPolicy,
		grpcBaseDomain:     strings.Trim(config.GRPCBaseDomain, "."),
		grpcPort:           config.GRPCPort,
		ctx:                managerCtx,
		cancel:             cancel,
		buildTimeout:       runtimeBuildTimeout,
		acquirePipeline:    config.PluginPipelineAcquire,
		releasePipeline:    config.PluginPipelineRelease,
	}
	if pushStore, ok := store.(PushStore); ok {
		m.pushStore = pushStore
	}
	if err := m.Reload(ctx); err != nil {
		cancel()
		return nil, fmt.Errorf("reload agent gateway: %w", err)
	}
	// The relay runs whenever push persistence exists, even while the external
	// URL is unset: pushEnabled is evaluated live, so an admin enabling the URL
	// at runtime must find the delivery worker already running. With no configs
	// or queued deliveries the relay just idles on its poll ticker.
	if m.pushStore != nil {
		m.pushRelay = newPushRelay(m)
		m.pushRelay.start()
	}
	return m, nil
}

// Reload rebuilds every enabled durable registration from local state only.
// Live upstream discovery is deferred to card and message requests.
// This makes startup independent of upstream availability. Replacement is
// published before the superseded runtimes are closed, so requests never observe
// a window with no agents; disabled registrations are intentionally omitted so
// they stop resolving.
func (m *Manager) Reload(ctx context.Context) error {
	m.mutations.Lock()
	registrations, err := m.store.ListAgentRegistrations(ctx)
	if err != nil {
		m.mutations.Unlock()
		return err
	}
	ready := make(map[string]*runtimeAgent, len(registrations))
	for i := range registrations {
		reg := registrations[i]
		if !reg.Enabled {
			continue
		}
		ready[reg.Name] = m.buildRuntime(reg)
	}
	old := make([]*runtimeAgent, 0)
	m.runtimes.entries.Range(func(_, value any) bool {
		old = append(old, value.(*runtimeAgent))
		return true
	})
	m.runtimes.clear()
	for id, runtime := range ready {
		m.runtimes.store(id, runtime)
	}
	m.mutations.Unlock()
	for _, runtime := range old {
		m.closeInBackground(runtime)
	}
	return nil
}

// ReloadRegistration replaces one runtime from its durable registration.
// Disabled registrations are unpublished.
func (m *Manager) ReloadRegistration(ctx context.Context, name string) (schemas.AgentRegistrationView, error) {
	m.mutations.Lock()
	reg, err := m.store.GetAgentRegistration(ctx, name)
	if err != nil {
		m.mutations.Unlock()
		return schemas.AgentRegistrationView{}, err
	}
	old, _ := m.runtimes.load(name)
	if reg.Enabled {
		m.runtimes.store(name, m.buildRuntime(*reg))
	} else {
		m.runtimes.delete(name)
	}
	m.mutations.Unlock()
	m.closeInBackground(old)
	return reg.Redacted(), nil
}

// RemoveRuntime unpublishes one runtime after its durable registration is
// removed. In-flight requests drain before the old runtime closes.
func (m *Manager) RemoveRuntime(name string) {
	m.mutations.Lock()
	old, _ := m.runtimes.load(name)
	m.runtimes.delete(name)
	m.mutations.Unlock()
	m.closeInBackground(old)
}

// closeInBackground drains a superseded or removed runtime without blocking the
// caller, while still registering the drain with the manager so Close cannot
// return before every in-flight upstream request has finished. The wait group is
// incremented on the calling goroutine so a shutdown that starts immediately
// afterwards always observes the pending drain.
func (m *Manager) closeInBackground(runtime *runtimeAgent) {
	if runtime == nil {
		return
	}
	generation, attempt, ok := runtime.beginClose()
	if !ok {
		return
	}
	m.draining.Go(func() {
		finishClose(generation, attempt)
	})
}

// Create validates the request, and for an enabled registration proves the
// upstream card is reachable and usable by building a client generation before
// anything is persisted, then writes the registration and publishes the runtime
// under the mutation lock. If persistence fails the freshly built generation is
// retired, so a failed create leaves no upstream client behind. A registration
// created disabled performs no upstream discovery at all and is stored but not
// published, so an unreachable agent can still be registered ahead of time.
func (m *Manager) Create(ctx context.Context, req CreateRequest) (schemas.AgentRegistrationView, error) {
	reg, err := normalize(req)
	if err != nil {
		return schemas.AgentRegistrationView{}, err
	}
	if len(reg.Name) > MaxGRPCAgentNameLength && m.logger != nil {
		m.logger.Warn("agent name %q is longer than %d characters and cannot be advertised or served over gRPC; it stays reachable over JSON-RPC and HTTP+JSON", reg.Name, MaxGRPCAgentNameLength)
	}
	var (
		generation *clientGeneration
		runtime    *runtimeAgent
	)
	if reg.Enabled {
		buildCtx, cancel := context.WithTimeout(ctx, m.buildTimeout)
		defer cancel()
		if generation, err = m.buildGeneration(buildCtx, runtimeConfigFromRegistration(reg)); err != nil {
			return schemas.AgentRegistrationView{}, err
		}
		runtime = m.buildRuntime(reg)
	}
	m.mutations.Lock()
	defer m.mutations.Unlock()
	if err := m.store.CreateAgentRegistration(ctx, &reg); err != nil {
		if generation != nil {
			generation.retire()
		}
		return schemas.AgentRegistrationView{}, err
	}
	if runtime != nil {
		if err := runtime.replaceGeneration(generation); err != nil {
			generation.retire()
			return schemas.AgentRegistrationView{}, err
		}
		m.runtimes.store(reg.Name, runtime)
	}
	return reg.Redacted(), nil
}

// Update replaces a registration under the mutation lock. It requires the full
// body, carries forward stored secrets when the caller echoes back a masked
// credential, preserves the original creation time, and, only when the new state
// is enabled, validates the new upstream card before writing. Disabling a
// registration performs no upstream discovery, so an agent whose upstream is
// unreachable can still be disabled. If the write fails the prepared replacement runtime is
// closed so no state is published for an unpersisted change; on success the old
// runtime is closed asynchronously because closing waits for in-flight requests.
func (m *Manager) Update(ctx context.Context, name string, req UpdateRequest) (schemas.AgentRegistrationView, error) {
	m.mutations.Lock()
	defer m.mutations.Unlock()
	existing, err := m.store.GetAgentRegistration(ctx, name)
	if err != nil {
		return schemas.AgentRegistrationView{}, err
	}
	if req.AgentCardURL == nil || req.Enabled == nil {
		return schemas.AgentRegistrationView{}, errors.New("agent_card_url and enabled are required")
	}
	preserveAuth := func(submitted, stored *schemas.UpstreamAuth) (*schemas.UpstreamAuth, error) {
		if submitted == nil {
			return nil, nil
		}
		for name, value := range submitted.Headers {
			if value.GetValue() != "<REDACTED>" {
				continue
			}
			if stored == nil {
				return nil, fmt.Errorf("cannot preserve missing upstream auth header %q", name)
			}
			storedValue, ok := stored.Headers[name]
			if !ok {
				return nil, fmt.Errorf("cannot preserve missing upstream auth header %q", name)
			}
			submitted.Headers[name] = storedValue
		}
		if submitted.OAuth != nil {
			if stored == nil || stored.OAuth == nil {
				if (submitted.OAuth.ClientID != nil && submitted.OAuth.ClientID.IsRedacted()) || (submitted.OAuth.ClientSecret != nil && submitted.OAuth.ClientSecret.IsRedacted()) {
					return nil, errors.New("cannot preserve missing upstream OAuth credentials")
				}
			} else {
				if submitted.OAuth.ClientID != nil && submitted.OAuth.ClientID.IsRedacted() {
					submitted.OAuth.ClientID = stored.OAuth.ClientID
				}
				if submitted.OAuth.ClientSecret != nil && submitted.OAuth.ClientSecret.IsRedacted() {
					submitted.OAuth.ClientSecret = stored.OAuth.ClientSecret
				}
			}
		}
		return submitted, nil
	}
	discoveryAuth, err := preserveAuth(req.DiscoveryAuth, existing.DiscoveryAuth)
	if err != nil {
		return schemas.AgentRegistrationView{}, err
	}
	runtimeAuth, err := preserveAuth(req.RuntimeAuth, existing.RuntimeAuth)
	if err != nil {
		return schemas.AgentRegistrationView{}, err
	}
	reg, err := normalize(CreateRequest{Name: name, AgentCardURL: *req.AgentCardURL, Tenant: req.Tenant, Enabled: req.Enabled, AllowByDefault: req.AllowByDefault, ForwardAcceptedCredential: req.ForwardAcceptedCredential, ForwardAcceptedCredentialOverridesAuth: req.ForwardAcceptedCredentialOverridesAuth, DiscoveryAuth: discoveryAuth, RuntimeAuth: runtimeAuth, VirtualKeyIDs: req.VirtualKeyIDs, ExtensionURIs: req.ExtensionURIs})
	if err != nil {
		return schemas.AgentRegistrationView{}, err
	}
	reg.CreatedAt = existing.CreatedAt
	var replacement *runtimeAgent
	if reg.Enabled {
		buildCtx, cancel := context.WithTimeout(ctx, m.buildTimeout)
		defer cancel()
		generation, buildErr := m.buildGeneration(buildCtx, runtimeConfigFromRegistration(reg))
		if buildErr != nil {
			return schemas.AgentRegistrationView{}, buildErr
		}
		replacement = m.buildRuntime(reg)
		if err = replacement.replaceGeneration(generation); err != nil {
			generation.retire()
			return schemas.AgentRegistrationView{}, err
		}
	}
	if err = m.store.UpdateAgentRegistration(ctx, &reg); err != nil {
		if replacement != nil {
			replacement.close()
		}
		return schemas.AgentRegistrationView{}, err
	}
	old, _ := m.runtimes.load(name)
	if replacement != nil {
		m.runtimes.store(name, replacement)
	} else {
		m.runtimes.delete(name)
	}
	m.closeInBackground(old)
	return reg.Redacted(), nil
}

func (m *Manager) Inspect(ctx context.Context, req InspectRequest) (InspectResponse, error) {
	if err := validateAgentCardURL(req.AgentCardURL); err != nil {
		return InspectResponse{}, err
	}
	if err := validateUpstreamAuth(req.DiscoveryAuth); err != nil {
		return InspectResponse{}, err
	}
	card, capabilities, err := m.resolveCardForInspection(ctx, req.AgentCardURL, req.DiscoveryAuth, upstreamCredentialID(req.AgentCardURL, "discovery"))
	if err != nil {
		return InspectResponse{}, err
	}
	return inspectCard(card, capabilities), nil
}

func inspectCard(card *a2a.AgentCard, capabilities map[string]bool) InspectResponse {
	response := InspectResponse{
		Name:                 card.Name,
		Description:          card.Description,
		Version:              card.Version,
		DocumentationURL:     card.DocumentationURL,
		IconURL:              card.IconURL,
		Capabilities:         capabilities,
		DefaultInputModes:    card.DefaultInputModes,
		DefaultOutputModes:   card.DefaultOutputModes,
		SignatureCount:       len(card.Signatures),
		SecuritySchemes:      make([]InspectedSecurityScheme, 0, len(card.SecuritySchemes)),
		SecurityRequirements: make([][]string, 0, len(card.SecurityRequirements)),
		Interfaces:           make([]InspectedAgentInterface, 0, len(card.SupportedInterfaces)),
		Skills:               make([]InspectedAgentSkill, 0, len(card.Skills)),
		Extensions:           make([]InspectedAgentExtension, 0, len(card.Capabilities.Extensions)),
	}
	if card.Provider != nil {
		response.Provider = card.Provider.Org
		response.ProviderURL = card.Provider.URL
	}
	for _, iface := range card.SupportedInterfaces {
		if iface == nil {
			continue
		}
		response.Interfaces = append(response.Interfaces, InspectedAgentInterface{
			URL:             iface.URL,
			ProtocolBinding: string(iface.ProtocolBinding),
			ProtocolVersion: string(iface.ProtocolVersion),
			Tenant:          iface.Tenant,
		})
	}
	for _, skill := range card.Skills {
		response.Skills = append(response.Skills, InspectedAgentSkill{
			ID:                   skill.ID,
			Name:                 skill.Name,
			Description:          skill.Description,
			Tags:                 skill.Tags,
			Examples:             skill.Examples,
			InputModes:           skill.InputModes,
			OutputModes:          skill.OutputModes,
			SecurityRequirements: inspectSecurityRequirements(skill.SecurityRequirements),
		})
	}
	for _, extension := range card.Capabilities.Extensions {
		response.Extensions = append(response.Extensions, InspectedAgentExtension{
			URI: extension.URI, Description: extension.Description, Required: extension.Required, Params: extension.Params,
		})
	}
	names := make([]string, 0, len(card.SecuritySchemes))
	for name := range card.SecuritySchemes {
		names = append(names, string(name))
	}
	sort.Strings(names)
	for _, name := range names {
		normalized := InspectedSecurityScheme{Name: name}
		switch scheme := card.SecuritySchemes[a2a.SecuritySchemeName(name)].(type) {
		case a2a.APIKeySecurityScheme:
			normalized.Type = "apiKey"
			normalized.Description = scheme.Description
			normalized.Location = string(scheme.Location)
			if scheme.Location == a2a.APIKeySecuritySchemeLocationHeader {
				normalized.Header = scheme.Name
			}
		case a2a.HTTPAuthSecurityScheme:
			normalized.Type = "http"
			normalized.Description = scheme.Description
			normalized.Scheme = scheme.Scheme
			normalized.BearerFormat = scheme.BearerFormat
		case a2a.OAuth2SecurityScheme:
			normalized.Type = "oauth2"
			normalized.Description = scheme.Description
			populateOAuthInspection(&normalized, scheme.Flows)
		case a2a.OpenIDConnectSecurityScheme:
			normalized.Type = "openIdConnect"
			normalized.Description = scheme.Description
			normalized.AuthorizationURL = scheme.OpenIDConnectURL
		case a2a.MutualTLSSecurityScheme:
			normalized.Type = "mutualTLS"
			normalized.Description = scheme.Description
		default:
			normalized.Type = "unknown"
			response.Warnings = append(response.Warnings, fmt.Sprintf("security scheme %q has an unsupported type", name))
		}
		response.SecuritySchemes = append(response.SecuritySchemes, normalized)
	}
	response.SecurityRequirements = inspectSecurityRequirements(card.SecurityRequirements)
	return response
}

func inspectSecurityRequirements(requirements a2a.SecurityRequirementsOptions) [][]string {
	result := make([][]string, 0, len(requirements))
	for _, requirement := range requirements {
		option := make([]string, 0, len(requirement))
		for name := range requirement {
			option = append(option, string(name))
		}
		sort.Strings(option)
		result = append(result, option)
	}
	slices.SortFunc(result, func(a, b []string) int {
		return strings.Compare(strings.Join(a, "\x00"), strings.Join(b, "\x00"))
	})
	return result
}

func populateOAuthInspection(target *InspectedSecurityScheme, flows a2a.OAuthFlows) {
	var scopes map[string]string
	switch flow := flows.(type) {
	case a2a.AuthorizationCodeOAuthFlow:
		target.AuthorizationURL, target.TokenURL, scopes = flow.AuthorizationURL, flow.TokenURL, flow.Scopes
	case a2a.ClientCredentialsOAuthFlow:
		target.TokenURL, scopes = flow.TokenURL, flow.Scopes
	case a2a.ImplicitOAuthFlow:
		target.AuthorizationURL, scopes = flow.AuthorizationURL, flow.Scopes
	case a2a.DeviceCodeOAuthFlow:
		target.AuthorizationURL, target.TokenURL, scopes = flow.DeviceAuthorizationURL, flow.TokenURL, flow.Scopes
	}
	for scope := range scopes {
		target.Scopes = append(target.Scopes, scope)
	}
	sort.Strings(target.Scopes)
}

// List reads registrations from the store rather than the runtime so disabled
// agents remain visible to administrators, and returns redacted projections.
func (m *Manager) List(ctx context.Context) ([]schemas.AgentRegistrationView, error) {
	regs, err := m.store.ListAgentRegistrations(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]schemas.AgentRegistrationView, 0, len(regs))
	for _, reg := range regs {
		views = append(views, reg.Redacted())
	}
	return views, nil
}

// Get returns one registration as a redacted management projection, including
// registrations that are disabled and therefore not serving traffic.
func (m *Manager) Get(ctx context.Context, name string) (schemas.AgentRegistrationView, error) {
	reg, err := m.store.GetAgentRegistration(ctx, name)
	if err != nil {
		return schemas.AgentRegistrationView{}, err
	}
	return reg.Redacted(), nil
}

// Delete removes the durable registration first and unpublishes the runtime only
// after that succeeds, so a failed delete cannot leave a reachable agent without
// a row. Closing happens in a tracked background goroutine because it waits for
// in-flight upstream calls to finish, which must not delay the caller; shutdown
// still waits for that drain.
func (m *Manager) Delete(ctx context.Context, name string) error {
	m.mutations.Lock()
	runtime, _ := m.runtimes.load(name)
	if err := m.store.DeleteAgentRegistration(ctx, name); err != nil {
		m.mutations.Unlock()
		return err
	}
	m.runtimes.delete(name)
	m.mutations.Unlock()
	m.closeInBackground(runtime)
	return nil
}

// CardHandler serves the gateway-facing public Agent Card as its own typed A2A
// operation. It borrows the normal manager plugin pipeline once, so governance,
// logging, tracing, short-circuiting, panic recovery, and reverse post-hooks use
// the same lifecycle as every protocol operation. Live upstream resolution and
// gateway transformation happen only inside the allowed operation; registration-
// time validation remains readiness evidence and is never served as current truth.
// requestOrigin lets the advertised gateway URL match the host the client reached;
// an empty value falls back to the configured external URL. The returned handler
// is bound to the runtime's lifetime: a closed runtime yields 404, and an
// unreachable upstream card yields 502 instead of a stale card.
func (m *Manager) CardHandler(name, requestOrigin string) (http.Handler, bool) {
	r, ok := m.runtimes.load(name)
	if !ok {
		return nil, false
	}
	externalURL := m.externalBaseURL()
	if requestOrigin != "" {
		externalURL = strings.TrimRight(requestOrigin, "/")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			http.NotFound(w, req)
			return
		}
		runtimeCtx := r.ctx
		r.mu.Unlock()
		ctx, cancel := context.WithCancel(req.Context())
		stop := context.AfterFunc(runtimeCtx, cancel)
		defer func() {
			stop()
			cancel()
		}()

		envelope := &schemas.BifrostA2ARequest{
			RequestType: schemas.A2ARequestTypeGetAgentCard,
			AgentName:   name,
		}
		start := time.Now()
		gateCtx := gateContext(ctx)
		var opErr error
		response, gateErr := m.RunWithPluginPipeline(gateCtx, envelope, func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
			card, err := m.resolveCard(gateCtx, r.config.agentCardURL, r.config.discoveryAuth, upstreamCredentialID(r.config.name, "discovery"))
			if err != nil {
				opErr = err
				return nil, err
			}
			card, err = gatewayCardWithExtensions(card, externalURL, name, m.authPolicy, m.pushEnabled(), r.config.extensionURIs, m.grpcInterfaceURL(name))
			if err != nil {
				opErr = err
				return nil, err
			}
			body := marshalA2APayload(card)
			if body == nil {
				err = errors.New("marshal gateway agent card")
				opErr = err
				return nil, err
			}
			return &schemas.BifrostA2AResponse{
				BifrostA2AGetAgentCardResponse: &schemas.BifrostA2AGetAgentCardResponse{},
				ResponseBody:                   body,
				ExtraFields: schemas.BifrostA2AResponseExtraFields{
					Latency: time.Since(start).Milliseconds(),
				},
			}, nil
		})
		if err := gateOutcomeError(gateCtx, gateErr, opErr); err != nil {
			status := http.StatusBadGateway
			if opErr == nil {
				status = http.StatusInternalServerError
				if gateErr != nil && gateErr.StatusCode != nil {
					status = *gateErr.StatusCode
				}
			}
			http.Error(w, http.StatusText(status), status)
			return
		}
		if response == nil || response.ResponseBody == nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, *response.ResponseBody)
	}), true
}

// RESTHandler returns the SDK HTTP+JSON handler for an agent. Like the JSON-RPC
// binding it is built once per registration and shares the exact same
// RequestHandler, so both bindings observe identical forwarding, plugin-gate and
// leasing semantics. Its routes are relative to the agent's REST mount point, so
// the caller must strip that prefix before serving.
func (m *Manager) RESTHandler(name string) (http.Handler, bool) {
	r, ok := m.runtimes.load(name)
	if !ok {
		return nil, false
	}
	return r.rest, true
}

// A2ARequestHandler returns the shared a2asrv.RequestHandler for an agent, so a
// transport-owned binding (gRPC) observes the exact same forwarding, plugin-gate
// and leasing semantics as the JSON-RPC and HTTP+JSON bindings built here.
func (m *Manager) A2ARequestHandler(name string) (a2asrv.RequestHandler, bool) {
	r, ok := m.runtimes.load(name)
	if !ok {
		return nil, false
	}
	return r.handler, true
}

// ProtocolHandler returns the SDK JSON-RPC handler for an agent. One handler is
// built per registration and shared by all callers, so no per-virtual-key
// protocol server is created; authorization happens before dispatch.
func (m *Manager) ProtocolHandler(name string) (http.Handler, bool) {
	r, ok := m.runtimes.load(name)
	if !ok {
		return nil, false
	}
	return r.protocol, true
}

// Close cancels the manager context and closes every runtime exactly once,
// blocking until each agent's upstream clients have been destroyed and until any
// superseded or removed runtime still draining in the background has finished, so
// shutdown leaves no live connections or goroutines behind.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.cancel()
		var runtimes []*runtimeAgent
		m.runtimes.entries.Range(func(_, value any) bool {
			runtimes = append(runtimes, value.(*runtimeAgent))
			return true
		})
		m.runtimes.clear()
		for _, runtime := range runtimes {
			runtime.close()
		}
		m.draining.Wait()
	})
}

// buildRuntime creates serving state for a registration without contacting the
// upstream agent, giving each runtime a cancellable child of the manager context
// and both SDK protocol bindings (JSON-RPC and HTTP+JSON) over a single instance
// of the gateway's own RequestHandler, which forwards every protocol method to
// the upstream agent under a generation lease held for the complete operation.
// Both bindings get the same panic handler, so a panic in a handler or in a
// streaming goroutine becomes an error instead of crashing the process. Both
// also get the same SSE keep-alive interval, so an otherwise idle stream still
// writes periodically and a vanished downstream client is detected by the failed
// write rather than only when the upstream happens to produce its next event.
func (m *Manager) buildRuntime(reg schemas.AgentRegistration) *runtimeAgent {
	config := runtimeConfigFromRegistration(reg)
	ctx, cancel := context.WithCancel(m.ctx)
	runtime := &runtimeAgent{config: config, ctx: ctx, cancel: cancel}
	handler := &proxyRequestHandler{runtime: runtime, manager: m, config: config, logger: m.logger}
	runtime.handler = handler
	options := []a2asrv.TransportOption{
		a2asrv.WithTransportPanicHandler(m.transportPanicHandler(config.name)),
		a2asrv.WithTransportKeepAlive(streamKeepAliveInterval),
	}
	runtime.protocol = a2asrv.NewJSONRPCHandler(handler, options...)
	runtime.rest = a2asrv.NewRESTHandler(handler, options...)
	return runtime
}

// streamKeepAliveInterval bounds how long a downstream SSE stream can stay
// completely silent. The SDK transports own the SSE write loop and emit a
// ': keep-alive' comment at this interval, so a client that disappeared while the
// upstream agent was thinking is noticed within one interval instead of never.
// It is deliberately half of DefaultKeepAliveTimeoutInSeconds (the idle timeout
// this repository already assumes for pooled HTTP connections), so a stream is
// never idle long enough for an intermediary applying that same 30s budget to
// drop it.
const streamKeepAliveInterval = time.Duration(schemas.DefaultKeepAliveTimeoutInSeconds/2) * time.Second

// transportPanicHandler converts a panic raised inside an SDK transport binding
// into an error for the client instead of letting it escape. Without it the SDK
// re-panics, and for streaming responses that panic happens on a transport-owned
// goroutine where it would take the whole process down. The recovered value is
// logged once, bounded, because it can carry upstream-controlled text.
func (m *Manager) transportPanicHandler(name string) func(any) error {
	return func(recovered any) error {
		if m.logger != nil {
			m.logger.Error("recovered from %s (agent=%s): %v", a2aPanicMessage, name, bounded(fmt.Sprint(recovered)))
		}
		return a2a.ErrInternalError
	}
}

// buildGeneration resolves the upstream card and constructs the SDK client from
// it, using the runtime credential for subsequent calls. It is the single place
// upstream reachability is proven, so it is used both for registration-time
// validation and for lazy or refreshed client construction.
func (m *Manager) buildGeneration(ctx context.Context, config runtimeConfig) (*clientGeneration, error) {
	card, err := m.resolveCard(ctx, config.agentCardURL, config.discoveryAuth, upstreamCredentialID(config.name, "discovery"))
	if err != nil {
		return nil, err
	}
	if _, err = allowedExtensions(card.Capabilities.Extensions, extensionSet(config.extensionURIs)); err != nil {
		return nil, err
	}
	if err = validateAuthCardMatch(card, config.runtimeAuth); err != nil {
		return nil, err
	}
	httpClient, err := m.clientFor(config.runtimeAuth, nil, true)
	if err != nil {
		return nil, fmt.Errorf("configure upstream HTTP client: %w", err)
	}
	interfaces := make([]*a2a.AgentInterface, 0, len(card.SupportedInterfaces))
	for _, iface := range card.SupportedInterfaces {
		if isStrictV1Interface(iface, config.agentCardURL) {
			interfaces = append(interfaces, iface)
		}
	}
	slices.SortStableFunc(interfaces, func(left, right *a2a.AgentInterface) int {
		leftVersion := "v" + strings.TrimPrefix(string(left.ProtocolVersion), "v")
		rightVersion := "v" + strings.TrimPrefix(string(right.ProtocolVersion), "v")
		return semver.Compare(rightVersion, leftVersion)
	})
	var candidates []upstreamCandidate
	var candidateErrors []error
	for _, iface := range interfaces {
		candidateCard := *card
		candidateCard.SupportedInterfaces = []*a2a.AgentInterface{iface}
		selectedTransport := ""
		client, clientErr := a2aclient.NewFromCard(ctx, &candidateCard,
			a2aclient.WithTransport(a2a.TransportProtocolJSONRPC, a2aclient.TransportFactoryFn(func(_ context.Context, _ *a2a.AgentCard, iface *a2a.AgentInterface) (a2aclient.Transport, error) {
				selectedTransport = string(a2a.TransportProtocolJSONRPC)
				return a2aclient.NewJSONRPCTransport(iface.URL, httpClient), nil
			})),
			a2aclient.WithTransport(a2a.TransportProtocolHTTPJSON, a2aclient.TransportFactoryFn(func(_ context.Context, _ *a2a.AgentCard, iface *a2a.AgentInterface) (a2aclient.Transport, error) {
				u, parseErr := url.Parse(iface.URL)
				if parseErr != nil {
					return nil, fmt.Errorf("parse upstream HTTP+JSON endpoint: %w", parseErr)
				}
				selectedTransport = string(a2a.TransportProtocolHTTPJSON)
				return a2aclient.NewRESTTransport(u, httpClient), nil
			})),
			// The runtime credential rides the sanitized service params attached per
			// call (see upstreamContext), which the SDK gRPC transport sends as
			// request metadata, so gRPC needs no equivalent of credentialTransport.
			a2aclient.WithTransport(a2a.TransportProtocolGRPC, a2aclient.TransportFactoryFn(func(_ context.Context, _ *a2a.AgentCard, iface *a2a.AgentInterface) (a2aclient.Transport, error) {
				target, creds, grpcErr := grpcUpstreamTarget(iface.URL, config.runtimeAuth)
				if grpcErr != nil {
					return nil, grpcErr
				}
				conn, grpcErr := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
				if grpcErr != nil {
					return nil, grpcErr
				}
				selectedTransport = string(a2a.TransportProtocolGRPC)
				return a2agrpc.NewGRPCTransport(conn), nil
			})),
		)
		if clientErr != nil {
			candidateErrors = append(candidateErrors, clientErr)
			continue
		}
		candidates = append(candidates, upstreamCandidate{client: client, transport: selectedTransport})
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("create upstream SDK client: %w", errors.Join(candidateErrors...))
	}
	return newCandidateGeneration(card, candidates), nil
}

// acquire returns a lease on a usable client generation, lazily building one on
// first use and coalescing concurrent first requests into a single initialization
// attempt. It retries when the generation it observed was retired concurrently,
// and fails fast if the runtime was closed or the caller's context ended while
// waiting.
func (r *runtimeAgent) acquire(ctx context.Context, manager *Manager) (*generationLease, error) {
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil, ErrNotFound
		}
		if r.generation != nil {
			lease, ok := r.generation.acquire()
			r.mu.Unlock()
			if ok {
				return lease, nil
			}
			continue
		}
		attempt := r.initializing
		if attempt == nil {
			attempt = &initializationAttempt{done: make(chan struct{})}
			r.initializing = attempt
			go r.initialize(manager, attempt)
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-attempt.done:
			if attempt.err != nil {
				return nil, attempt.err
			}
		}
	}
}

// initialize performs one coalesced client build and publishes the result to
// every waiter. It discards the new generation if the runtime closed or a newer
// attempt superseded this one, so a losing or late attempt cannot leak an
// upstream client or overwrite current state. Building happens outside the lock;
// only publication is serialized.
func (r *runtimeAgent) initialize(manager *Manager, attempt *initializationAttempt) {
	ctx, cancel := context.WithTimeout(r.ctx, manager.buildTimeout)
	defer cancel()
	generation, err := manager.buildGeneration(ctx, r.config)

	r.mu.Lock()
	if err == nil && !r.closed && r.initializing == attempt {
		old := r.generation
		r.generation = generation
		if old != nil {
			old.retire()
		}
	} else if generation != nil {
		generation.retire()
	}
	if err == nil && r.closed {
		err = ErrNotFound
	}
	attempt.err = err
	if r.initializing == attempt {
		r.initializing = nil
	}
	close(attempt.done)
	r.mu.Unlock()
}

// requestRefresh rebuilds the client in the background after a response suggested
// the cached upstream interface is stale, so recovery does not require an
// administrative update. It is throttled by a cooldown and a single-flight flag,
// and ignores requests naming a generation that is no longer current, so repeated
// failing requests cannot storm the upstream with card fetches. The superseded
// generation is retired only after the replacement is published, and the
// replacement is discarded if the runtime changed meanwhile.
func (r *runtimeAgent) requestRefresh(manager *Manager, source *clientGeneration) {
	r.mu.Lock()
	if r.closed || r.generation != source || r.refreshing || time.Since(r.lastRefresh) < refreshCooldown {
		r.mu.Unlock()
		return
	}
	r.refreshing = true
	r.lastRefresh = time.Now()
	ctx := r.ctx
	config := r.config
	r.mu.Unlock()
	go func() {
		buildCtx, cancel := context.WithTimeout(ctx, manager.buildTimeout)
		defer cancel()
		replacement, err := manager.buildGeneration(buildCtx, config)
		r.mu.Lock()
		if err == nil && !r.closed && r.generation == source {
			r.generation = replacement
			r.mu.Unlock()
			source.retire()
		} else {
			r.mu.Unlock()
			if replacement != nil {
				replacement.retire()
			}
		}
		r.mu.Lock()
		r.refreshing = false
		r.mu.Unlock()
	}()
}

// replaceGeneration installs a pre-built generation and retires the previous one
// outside the lock, so in-flight requests finish on the old client while new
// requests immediately use the new one. It fails if the runtime is already closed
// so the caller can retire the unused replacement.
func (r *runtimeAgent) replaceGeneration(replacement *clientGeneration) error {
	if replacement == nil {
		return errors.New("replacement client generation is required")
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrNotFound
	}
	old := r.generation
	r.generation = replacement
	r.mu.Unlock()
	if old != nil {
		old.retire()
	}
	return nil
}

// close makes the runtime permanently unusable, cancels its context so in-flight
// upstream work is aborted, then drains: it waits for the current generation to
// be destroyed and for any pending initialization to finish. This bounded drain
// is what lets update, delete, and shutdown guarantee no orphaned upstream client
// or goroutine. It is idempotent.
func (r *runtimeAgent) close() {
	generation, attempt, ok := r.beginClose()
	if !ok {
		return
	}
	finishClose(generation, attempt)
}

// beginClose performs the immediately observable half of closing: the runtime is
// marked unusable and its context cancelled before the call returns, so handlers
// captured earlier stop serving as soon as the mutation is published. It reports
// false when the runtime was already closed, and otherwise hands the caller the
// state that must still be drained.
func (r *runtimeAgent) beginClose() (*clientGeneration, *initializationAttempt, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil, false
	}
	r.closed = true
	r.cancel()
	generation := r.generation
	attempt := r.initializing
	r.generation = nil
	return generation, attempt, true
}

// finishClose is the blocking drain half of closing: it waits for the retired
// generation's client to be destroyed and for any pending initialization to
// finish, which is what lets update, delete, and shutdown guarantee no orphaned
// upstream client or goroutine.
func finishClose(generation *clientGeneration, attempt *initializationAttempt) {
	if generation != nil {
		generation.retire()
		generation.wait()
	}
	if attempt != nil {
		<-attempt.done
	}
}

// resolveCard fetches and parses the upstream agent card itself rather than
// through the SDK resolver, because the gateway must apply its own protections:
// the discovery credential is injected by transport, redirects are not followed,
// the body is size-limited, and the card must expose a strict A2A v1 JSON-RPC
// interface before it is accepted.
func (m *Manager) resolveCard(ctx context.Context, agentCardURL string, auth *schemas.UpstreamAuth, credentialID string) (*a2a.AgentCard, error) {
	card, _, err := m.resolveCardForInspection(ctx, agentCardURL, auth, credentialID)
	return card, err
}

type contextWithoutValues struct{ context.Context }

func (contextWithoutValues) Value(any) any { return nil }

func (m *Manager) resolveCardForInspection(ctx context.Context, agentCardURL string, auth *schemas.UpstreamAuth, credentialID string) (*a2a.AgentCard, map[string]bool, error) {
	// Discovery is an administrative operation. Preserve cancellation and
	// deadlines without letting caller identity select a per-user credential.
	requestCtx := schemas.NewBifrostContext(contextWithoutValues{ctx}, schemas.NoDeadline)
	headers, err := m.resolveAuthHeaders(requestCtx, auth, credentialID)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve upstream agent card authentication: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, agentCardURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create upstream agent card request: %w", err)
	}
	client, err := m.clientFor(auth, headers, false)
	if err != nil {
		return nil, nil, fmt.Errorf("configure upstream agent card client: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch upstream agent card: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("fetch upstream agent card: unexpected HTTP status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAgentCardResponseSize+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read upstream agent card: %w", err)
	}
	if len(body) > maxAgentCardResponseSize {
		return nil, nil, errAgentCardTooLarge
	}
	capabilities, err := inspectBooleanCapabilities(body)
	if err != nil {
		return nil, nil, fmt.Errorf("parse upstream agent card capabilities: %w", err)
	}
	body, err = normalizeAgentCardSecurityJSON(body)
	if err != nil {
		return nil, nil, fmt.Errorf("parse upstream agent card security: %w", err)
	}
	card := new(a2a.AgentCard)
	if err = json.Unmarshal(body, card); err != nil {
		return nil, nil, fmt.Errorf("parse upstream agent card: %w", err)
	}
	if err = validateStrictV1Interfaces(card, agentCardURL); err != nil {
		return nil, nil, err
	}
	return card, capabilities, nil
}

func inspectBooleanCapabilities(body []byte) (map[string]bool, error) {
	var card struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(body, &card); err != nil {
		return nil, err
	}
	capabilities := make(map[string]bool)
	for name, raw := range card.Capabilities {
		var enabled bool
		if err := json.Unmarshal(raw, &enabled); err == nil {
			capabilities[name] = enabled
		}
	}
	return capabilities, nil
}

// normalizeAgentCardSecurityJSON adapts the A2A v1/OpenAPI security representation
// to the wrapper-shaped JSON currently expected by a2a-go.
func normalizeAgentCardSecurityJSON(body []byte) ([]byte, error) {
	var card map[string]json.RawMessage
	if err := json.Unmarshal(body, &card); err != nil {
		return nil, err
	}

	if rawSchemes, ok := card["securitySchemes"]; ok {
		var schemes map[string]map[string]json.RawMessage
		if err := json.Unmarshal(rawSchemes, &schemes); err != nil {
			return nil, fmt.Errorf("decode security schemes: %w", err)
		}
		wrapped := make(map[string]json.RawMessage, len(schemes))
		for name, scheme := range schemes {
			rawScheme, err := json.Marshal(scheme)
			if err != nil {
				return nil, err
			}
			var schemeType string
			if rawType, ok := scheme["type"]; ok {
				if err := json.Unmarshal(rawType, &schemeType); err != nil {
					return nil, fmt.Errorf("decode security scheme %q type: %w", name, err)
				}
			} else {
				wrapped[name] = rawScheme
				continue
			}
			wrapperKey := map[string]string{
				"apiKey":        "apiKeySecurityScheme",
				"http":          "httpAuthSecurityScheme",
				"mutualTLS":     "mtlsSecurityScheme",
				"oauth2":        "oauth2SecurityScheme",
				"openIdConnect": "openIdConnectSecurityScheme",
			}[schemeType]
			if wrapperKey == "" {
				wrapped[name] = rawScheme
				continue
			}
			delete(scheme, "type")
			if schemeType == "apiKey" {
				if location, ok := scheme["in"]; ok {
					scheme["location"] = location
					delete(scheme, "in")
				}
			}
			wrappedScheme, err := json.Marshal(map[string]map[string]json.RawMessage{wrapperKey: scheme})
			if err != nil {
				return nil, err
			}
			wrapped[name] = wrappedScheme
		}
		encoded, err := json.Marshal(wrapped)
		if err != nil {
			return nil, err
		}
		card["securitySchemes"] = encoded
	}

	if rawSecurity, ok := card["security"]; ok {
		var requirements []map[string]json.RawMessage
		if err := json.Unmarshal(rawSecurity, &requirements); err != nil {
			return nil, fmt.Errorf("decode security requirements: %w", err)
		}
		wrapped := make([]map[string]map[string]json.RawMessage, 0, len(requirements))
		for _, requirement := range requirements {
			wrapped = append(wrapped, map[string]map[string]json.RawMessage{"schemes": requirement})
		}
		encoded, err := json.Marshal(wrapped)
		if err != nil {
			return nil, err
		}
		card["securityRequirements"] = encoded
		delete(card, "security")
	}

	return json.Marshal(card)
}

func validateAuthCardMatch(card *a2a.AgentCard, auth *schemas.UpstreamAuth) error {
	if auth == nil || auth.Type == schemas.MCPAuthTypeNone || len(auth.SecuritySchemes) == 0 {
		return nil
	}
	configured := make(map[a2a.SecuritySchemeName]struct{}, len(auth.SecuritySchemes))
	for _, scheme := range auth.SecuritySchemes {
		name := a2a.SecuritySchemeName(scheme)
		if _, ok := card.SecuritySchemes[name]; !ok {
			return fmt.Errorf("configured upstream auth security scheme %q is not declared by the agent card", scheme)
		}
		configured[name] = struct{}{}
	}
	for _, requirement := range card.SecurityRequirements {
		if len(requirement) != len(configured) {
			continue
		}
		matches := true
		for name := range requirement {
			if _, ok := configured[name]; !ok {
				matches = false
				break
			}
		}
		if matches {
			return nil
		}
	}
	return fmt.Errorf("configured upstream auth security schemes %q do not satisfy one complete agent card security requirement", auth.SecuritySchemes)
}

// validateStrictV1Interfaces rejects cards the gateway cannot actually proxy.
// An interface counts only at the exact supported protocol version and over a
// supported binding. JSON-RPC and HTTP+JSON require an absolute HTTP(S) URL
// without embedded credentials. gRPC requires a plain dialable target. An
// HTTPS card cannot downgrade runtime traffic to plaintext except on loopback.
func isStrictV1Interface(iface *a2a.AgentInterface, agentCardURL string) bool {
	if iface == nil || iface.ProtocolVersion != a2a.Version {
		return false
	}
	cardURL, err := url.Parse(agentCardURL)
	if err != nil {
		return false
	}
	requireTLS := strings.EqualFold(cardURL.Scheme, "https")
	switch iface.ProtocolBinding {
	case a2a.TransportProtocolJSONRPC, a2a.TransportProtocolHTTPJSON:
		u, err := url.ParseRequestURI(iface.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return false
		}
		return !requireTLS || u.Scheme == "https" || isLoopbackHost(u.Hostname())
	case a2a.TransportProtocolGRPC:
		target, _, err := grpcUpstreamTarget(iface.URL, nil)
		if err != nil {
			return false
		}
		plaintext := strings.HasPrefix(iface.URL, "http://")
		host, _, err := net.SplitHostPort(target)
		return err == nil && (!requireTLS || !plaintext || isLoopbackHost(host))
	default:
		return false
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateStrictV1Interfaces(card *a2a.AgentCard, agentCardURL string) error {
	for _, iface := range card.SupportedInterfaces {
		if isStrictV1Interface(iface, agentCardURL) {
			return nil
		}
	}
	return errors.New("upstream agent card has no strict A2A v1 interface the gateway can proxy (JSON-RPC, HTTP+JSON, or gRPC)")
}

// grpcUpstreamTarget validates a card's gRPC interface URL and derives the dial
// target with its transport security. Cards commonly advertise a bare
// host:port, which defaults to TLS; an explicit https:// scheme also selects
// TLS and http:// selects plaintext, mirroring the trust the card already
// expresses for the HTTP bindings. The target must carry an explicit port and
// nothing besides host and port, so it cannot embed credentials or redirect the
// dial through resolver tricks.
func grpcUpstreamTarget(raw string, auth *schemas.UpstreamAuth) (string, credentials.TransportCredentials, error) {
	scheme := "https"
	rest := raw
	if before, after, found := strings.Cut(raw, "://"); found {
		scheme = before
		rest = after
	}
	if scheme != "http" && scheme != "https" {
		return "", nil, fmt.Errorf("unsupported gRPC interface scheme %q", scheme)
	}
	if rest == "" || strings.ContainsAny(rest, "/?#@") {
		return "", nil, errors.New("gRPC interface URL must be host:port without credentials, path, or query")
	}
	host, port, err := net.SplitHostPort(rest)
	if err != nil || host == "" || port == "" {
		return "", nil, errors.New("gRPC interface URL requires an explicit host and port")
	}
	if scheme == "http" {
		return rest, insecure.NewCredentials(), nil
	}
	tlsConfig, err := upstreamTLSConfig(auth)
	if err != nil {
		return "", nil, err
	}
	return rest, credentials.NewTLS(tlsConfig), nil
}

func upstreamTLSConfig(auth *schemas.UpstreamAuth) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if auth == nil || auth.TLS == nil {
		return config, nil
	}
	config.InsecureSkipVerify = auth.TLS.InsecureSkipVerify //nolint:gosec // Explicit administrator configuration.
	if auth.TLS.CACertPEM != nil && auth.TLS.CACertPEM.GetValue() != "" {
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM([]byte(auth.TLS.CACertPEM.GetValue())) {
			return nil, errors.New("upstream TLS CA certificate is invalid")
		}
		config.RootCAs = roots
	}
	certSet := auth.TLS.ClientCertPEM != nil && auth.TLS.ClientCertPEM.GetValue() != ""
	keySet := auth.TLS.ClientKeyPEM != nil && auth.TLS.ClientKeyPEM.GetValue() != ""
	if certSet != keySet {
		return nil, errors.New("upstream TLS client certificate and key must be configured together")
	}
	if certSet {
		certificate, err := tls.X509KeyPair([]byte(auth.TLS.ClientCertPEM.GetValue()), []byte(auth.TLS.ClientKeyPEM.GetValue()))
		if err != nil {
			return nil, fmt.Errorf("parse upstream TLS client certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return config, nil
}

// clientFor derives a per-purpose HTTP client from the shared configured client so
// connection pooling and timeouts are reused while credential injection and header
// sanitization are applied per credential. Card discovery disables redirect
// following so a redirect cannot carry the upstream credential to another host.
func (m *Manager) clientFor(auth *schemas.UpstreamAuth, headers http.Header, followRedirects bool) (*http.Client, error) {
	client := *m.httpClient
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	if transport, ok := base.(*http.Transport); ok {
		clone := transport.Clone()
		tlsConfig, err := upstreamTLSConfig(auth)
		if err != nil {
			return nil, err
		}
		clone.TLSClientConfig = tlsConfig
		base = clone
	} else if auth != nil && auth.TLS != nil {
		return nil, errors.New("upstream TLS configuration requires an *http.Transport")
	}
	client.Transport = &credentialTransport{base: base, headers: headers}
	if !followRedirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	} else {
		configuredCheckRedirect := client.CheckRedirect
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && !sameOrigin(via[0].URL, req.URL) {
				return errors.New("upstream redirect must remain on the original origin")
			}
			if configuredCheckRedirect != nil {
				return configuredCheckRedirect(req, via)
			}
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		}
	}
	return &client, nil
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func (m *Manager) resolveAuthHeaders(ctx *schemas.BifrostContext, auth *schemas.UpstreamAuth, credentialID string) (http.Header, error) {
	headers := make(http.Header, len(authHeaders(auth))+1)
	for name, value := range authHeaders(auth) {
		headers.Set(name, value.GetValue())
	}
	if auth == nil || auth.Type == "" || auth.Type == schemas.MCPAuthTypeNone || auth.Type == schemas.MCPAuthTypeHeaders {
		return headers, nil
	}
	token, err := m.oauthAccessToken(ctx, credentialID, auth)
	if err != nil {
		return nil, err
	}
	headers.Set("Authorization", "Bearer "+token)
	return headers, nil
}

func authHeaders(auth *schemas.UpstreamAuth) map[string]schemas.SecretVar {
	if auth == nil {
		return nil
	}
	return auth.Headers
}

func oauthConfigFingerprint(oauth *schemas.UpstreamOAuthConfig) [sha256.Size]byte {
	var value strings.Builder
	for _, part := range []string{
		oauth.ProviderURL, oauth.DiscoveryURL, oauth.TokenURL,
		oauth.ClientID.GetValue(), oauth.ClientSecret.GetValue(),
		strings.Join(oauth.Scopes, "\x00"), oauth.Resource,
	} {
		value.WriteString(strconv.Itoa(len(part)))
		value.WriteByte(':')
		value.WriteString(part)
	}
	return sha256.Sum256([]byte(value.String()))
}

func (m *Manager) oauthAccessToken(ctx context.Context, credentialID string, auth *schemas.UpstreamAuth) (string, error) {
	oauth := auth.OAuth
	fingerprint := oauthConfigFingerprint(oauth)
	m.oauthMu.Lock()
	if m.oauthTokens == nil {
		m.oauthTokens = make(map[string]oauthToken)
	}
	if m.oauthFingerprints == nil {
		m.oauthFingerprints = make(map[string][sha256.Size]byte)
	}
	m.oauthFingerprints[credentialID] = fingerprint
	cached, ok := m.oauthTokens[credentialID]
	m.oauthMu.Unlock()
	if ok && cached.fingerprint == fingerprint && time.Until(cached.expiresAt) > 30*time.Second {
		return cached.accessToken, nil
	}

	fetchKey := fmt.Sprintf("%s:%x", credentialID, fingerprint)
	result := m.oauthFetches.DoChan(fetchKey, func() (any, error) {
		m.oauthMu.Lock()
		cached, ok := m.oauthTokens[credentialID]
		m.oauthMu.Unlock()
		if ok && cached.fingerprint == fingerprint && time.Until(cached.expiresAt) > 30*time.Second {
			return cached.accessToken, nil
		}

		fetchCtx, cancel := context.WithTimeout(context.Background(), oauthFetchTimeout)
		defer cancel()
		token, expiresAt, err := m.fetchOAuthAccessToken(fetchCtx, auth)
		if err != nil {
			return nil, err
		}
		m.oauthMu.Lock()
		if m.oauthFingerprints[credentialID] == fingerprint {
			m.oauthTokens[credentialID] = oauthToken{accessToken: token, expiresAt: expiresAt, fingerprint: fingerprint}
		}
		m.oauthMu.Unlock()
		return token, nil
	})
	select {
	case result := <-result:
		if result.Err != nil {
			return "", result.Err
		}
		return result.Val.(string), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (m *Manager) fetchOAuthAccessToken(ctx context.Context, auth *schemas.UpstreamAuth) (string, time.Time, error) {
	oauth := auth.OAuth
	tokenURL := oauth.TokenURL
	if tokenURL == "" {
		discoveryURL := oauth.DiscoveryURL
		if discoveryURL == "" {
			discoveryURL = strings.TrimRight(oauth.ProviderURL, "/") + "/.well-known/oauth-authorization-server"
		}
		discoveryClient, err := m.clientFor(auth, nil, false)
		if err != nil {
			return "", time.Time{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("create OAuth discovery request: %w", err)
		}
		resp, err := discoveryClient.Do(req)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("discover OAuth token endpoint: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", time.Time{}, fmt.Errorf("discover OAuth token endpoint: unexpected HTTP status %s", resp.Status)
		}
		var metadata struct {
			TokenURL string `json:"token_endpoint"`
		}
		if err = json.NewDecoder(io.LimitReader(resp.Body, maxAgentCardResponseSize)).Decode(&metadata); err != nil {
			return "", time.Time{}, fmt.Errorf("decode OAuth discovery response: %w", err)
		}
		tokenURL = metadata.TokenURL
		if tokenURL == "" {
			return "", time.Time{}, errors.New("OAuth discovery response missing token_endpoint")
		}
		discoveryEndpoint, _ := url.Parse(discoveryURL)
		tokenEndpoint, parseErr := validatedHTTPURL(tokenURL)
		if parseErr != nil {
			return "", time.Time{}, errors.New("OAuth discovery token_endpoint must be an absolute HTTP(S) URL without user info")
		}
		if !sameOrigin(discoveryEndpoint, tokenEndpoint) {
			return "", time.Time{}, errors.New("OAuth discovery token_endpoint must use the discovery document origin; configure token_url explicitly to use another origin")
		}
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {oauth.ClientID.GetValue()}, "client_secret": {oauth.ClientSecret.GetValue()}}
	if len(oauth.Scopes) > 0 {
		form.Set("scope", strings.Join(oauth.Scopes, " "))
	}
	if oauth.Resource != "" {
		form.Set("resource", oauth.Resource)
	}
	tokenClient, err := m.clientFor(auth, nil, false)
	if err != nil {
		return "", time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create OAuth token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := tokenClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("acquire OAuth access token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("acquire OAuth access token: unexpected HTTP status %s", resp.Status)
	}
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, maxAgentCardResponseSize)).Decode(&tokenResponse); err != nil {
		return "", time.Time{}, fmt.Errorf("decode OAuth token response: %w", err)
	}
	if tokenResponse.AccessToken == "" {
		return "", time.Time{}, errors.New("OAuth token response missing access_token")
	}
	expiresIn := time.Duration(tokenResponse.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = time.Minute
	}
	return tokenResponse.AccessToken, time.Now().Add(expiresIn), nil
}

func validatedHTTPURL(raw string) (*url.URL, error) {
	u, err := url.ParseRequestURI(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, errors.New("URL must be absolute HTTP(S) without user info")
	}
	return u, nil
}

func validateAgentCardURL(raw string) error {
	if _, err := validatedHTTPURL(raw); err != nil {
		return errors.New("agent_card_url must be an absolute HTTP(S) URL without user info")
	}
	return nil
}

// normalize is the single validation and canonicalization point for both create
// and update, so persistence, runtime state, and gateway URLs can rely on the
// result. It constrains the name to a URL-safe slug, requires an absolute HTTP(S)
// card URL without user info, requires every configured credential to have a
// secret and an allowlisted header, and sorts a private copy of the grant IDs so
// empty or duplicate entries can be rejected by comparing adjacent elements.
func normalize(req CreateRequest) (schemas.AgentRegistration, error) {
	name := strings.TrimSpace(req.Name)
	if len(name) > 255 || !agentNamePattern.MatchString(name) {
		return schemas.AgentRegistration{}, errors.New("name must be 1-255 lowercase ASCII letters, numbers, or single hyphens, and must start and end with a letter or number")
	}
	if err := validateAgentCardURL(req.AgentCardURL); err != nil {
		return schemas.AgentRegistration{}, err
	}
	for _, auth := range []*schemas.UpstreamAuth{req.DiscoveryAuth, req.RuntimeAuth} {
		if err := validateUpstreamAuth(auth); err != nil {
			return schemas.AgentRegistration{}, err
		}
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ids := slices.Clone(req.VirtualKeyIDs)
	sort.Strings(ids)
	for i, id := range ids {
		if id == "" {
			return schemas.AgentRegistration{}, errors.New("virtual_key_ids cannot contain an empty ID")
		}
		if i > 0 && ids[i-1] == id {
			return schemas.AgentRegistration{}, errors.New("duplicate virtual key ID: " + id)
		}
	}
	extensions := slices.Clone(req.ExtensionURIs)
	sort.Strings(extensions)
	if len(extensions) > maxExtensionURICount {
		return schemas.AgentRegistration{}, fmt.Errorf("extension_uris cannot contain more than %d entries", maxExtensionURICount)
	}
	for i, uri := range extensions {
		parsed, parseErr := url.ParseRequestURI(uri)
		if parseErr != nil || parsed.Scheme == "" || len(uri) > maxExtensionURILength {
			return schemas.AgentRegistration{}, errors.New("extension_uris must contain absolute URIs no longer than 2048 bytes")
		}
		if i > 0 && extensions[i-1] == uri {
			return schemas.AgentRegistration{}, errors.New("duplicate extension URI: " + uri)
		}
	}
	now := time.Now().UTC()
	return schemas.AgentRegistration{Name: name, AgentCardURL: req.AgentCardURL, Tenant: req.Tenant, Enabled: enabled, AllowByDefault: req.AllowByDefault, ForwardAcceptedCredential: req.ForwardAcceptedCredential, ForwardAcceptedCredentialOverridesAuth: req.ForwardAcceptedCredentialOverridesAuth, DiscoveryAuth: req.DiscoveryAuth, RuntimeAuth: req.RuntimeAuth, ExtensionURIs: extensions, VirtualKeyIDs: ids, CreatedAt: now, UpdatedAt: now}, nil
}

func validateUpstreamAuth(auth *schemas.UpstreamAuth) error {
	if auth == nil {
		return nil
	}
	switch auth.Type {
	case schemas.MCPAuthTypeNone, schemas.MCPAuthTypeHeaders, schemas.MCPAuthTypeOauth:
	default:
		return fmt.Errorf("unsupported Agent Gateway upstream auth type %q; supported types are none, headers, and oauth", auth.Type)
	}
	seenSchemes := make(map[string]struct{}, len(auth.SecuritySchemes))
	for _, scheme := range auth.SecuritySchemes {
		if strings.TrimSpace(scheme) == "" {
			return errors.New("upstream auth security_schemes cannot contain an empty name")
		}
		if _, ok := seenSchemes[scheme]; ok {
			return fmt.Errorf("duplicate upstream auth security scheme %q", scheme)
		}
		seenSchemes[scheme] = struct{}{}
	}
	if auth.Type == schemas.MCPAuthTypeHeaders && len(auth.Headers) == 0 {
		return errors.New("header upstream auth requires headers")
	}
	for name, value := range auth.Headers {
		canonical := http.CanonicalHeaderKey(name)
		if strings.TrimSpace(name) == "" || value.GetValue() == "" || (blockedUpstreamHeader(canonical, nil) && !strings.EqualFold(canonical, "x-bf-vk")) {
			return errors.New("upstream auth headers require a non-empty, forwardable name and value")
		}
	}
	if auth.Type == schemas.MCPAuthTypeOauth {
		if auth.OAuth == nil || auth.OAuth.ClientID == nil || auth.OAuth.ClientID.GetValue() == "" || auth.OAuth.ClientSecret == nil || auth.OAuth.ClientSecret.GetValue() == "" {
			return errors.New("OAuth upstream auth requires inline client_id and client_secret")
		}
		if auth.OAuth.TokenURL == "" && auth.OAuth.DiscoveryURL == "" && auth.OAuth.ProviderURL == "" {
			return errors.New("OAuth upstream auth requires token_url, discovery_url, or provider_url")
		}
		for _, raw := range []string{auth.OAuth.TokenURL, auth.OAuth.DiscoveryURL, auth.OAuth.ProviderURL} {
			if raw != "" {
				if err := validateAgentCardURL(raw); err != nil {
					return errors.New("OAuth upstream URLs must be absolute HTTP(S) URLs without user info")
				}
			}
		}
	}
	_, err := upstreamTLSConfig(auth)
	return err
}

// credentialTransport is the enforcement point between the two trust domains: it
// strips headers that must never cross to the upstream agent and injects the
// gateway-owned credential, on a cloned request so the caller's headers are never
// mutated. It also converts non-protocol upstream POST errors into a typed status
// error so stale-interface recovery can react to them.
type credentialTransport struct {
	base    http.RoundTripper
	headers http.Header
}

// RoundTrip sanitizes and credentials the outbound request. For HTTP+JSON, a
// JSON error response belongs to the A2A protocol and must reach the SDK so it
// can decode errors such as TaskNotFound. Other non-OK POST responses are closed
// and converted to upstreamHTTPStatusError for stale-interface recovery.
func (t *credentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	managed := t.headers.Clone()
	if managed == nil {
		managed = make(http.Header)
	}
	if values := req.Header.Values("x-bf-vk"); len(values) > 0 {
		managed[http.CanonicalHeaderKey("x-bf-vk")] = slices.Clone(values)
	}
	clone.Header = sanitizeUpstreamHeaders(req.Header, managed)
	resp, err := t.base.RoundTrip(clone)
	if err == nil && resp != nil && resp.StatusCode != http.StatusOK && clone.Method == http.MethodPost && !isHTTPJSONErrorResponse(clone, resp) {
		resp.Body.Close()
		return nil, &upstreamHTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}
	if err == nil && resp != nil {
		wrapJSONStreamError(clone, resp)
	}
	return resp, err
}

func isHTTPJSONErrorResponse(req *http.Request, resp *http.Response) bool {
	transport, _ := req.Context().Value(schemas.BifrostContextKeyA2AUpstreamTransport).(string)
	return transport == string(a2a.TransportProtocolHTTPJSON) && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json")
}

// maxWrappedStreamErrorBody bounds how much of an upstream JSON error reply is
// buffered when reframing it for the SSE parser.
const maxWrappedStreamErrorBody = 1 << 20

// wrapJSONStreamError reframes a JSON reply to a streaming request as a single
// SSE data frame. A JSON-RPC server may legally answer a streaming method with a
// plain 200 application/json error response instead of opening an SSE stream
// (the Python SDK does exactly this); the a2a-go client's SSE parser sees no
// "data:" lines in such a body and silently reports an empty stream, swallowing
// the upstream error. Reframing lets the SDK surface the error unchanged.
func wrapJSONStreamError(req *http.Request, resp *http.Response) {
	if resp.StatusCode != http.StatusOK ||
		!strings.Contains(req.Header.Get("Accept"), "text/event-stream") ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWrappedStreamErrorBody))
	resp.Body.Close()
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return
	}
	// SSE data frames are line-delimited, so the JSON must be a single line.
	var compact bytes.Buffer
	if json.Compact(&compact, body) != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return
	}
	framed := append([]byte("data: "), compact.Bytes()...)
	framed = append(framed, '\n', '\n')
	resp.Header.Set("Content-Type", "text/event-stream")
	resp.Header.Del("Content-Length")
	resp.ContentLength = int64(len(framed))
	resp.Body = io.NopCloser(bytes.NewReader(framed))
}

// sanitizeUpstreamHeaders builds the header set sent upstream. It copies
// forwardable headers, drops hop-by-hop headers (including those named by the
// request's own Connection tokens) plus Bifrost identity, cookies, and proxy
// credentials, and finally applies the gateway-managed credentials last so they
// win over anything the downstream client supplied. Authorization credentials get
// configured scheme prefix.
func sanitizeUpstreamHeaders(source, managed http.Header) http.Header {
	result := make(http.Header)
	connectionTokens := map[string]bool{}
	for _, value := range source.Values("Connection") {
		for token := range strings.SplitSeq(value, ",") {
			connectionTokens[http.CanonicalHeaderKey(strings.TrimSpace(token))] = true
		}
	}
	for key, values := range source {
		canonical := http.CanonicalHeaderKey(key)
		if blockedUpstreamHeader(canonical, connectionTokens) {
			continue
		}
		result[canonical] = slices.Clone(values)
	}
	for key, values := range managed {
		result[http.CanonicalHeaderKey(key)] = slices.Clone(values)
	}
	return result
}

// blockedUpstreamHeader reports headers that must not reach the upstream agent:
// Bifrost's own governance credential, cookies, proxy credentials, and hop-by-hop
// headers, which belong to the downstream connection rather than the upstream one.
// The downstream A2A-Version copy is dropped in upstreamContext, not here: this
// predicate also runs on the final SDK-built request (credentialTransport), where
// the only A2A-Version left is the gateway's own version stamp and must survive.
func blockedUpstreamHeader(header string, connectionTokens map[string]bool) bool {
	if connectionTokens[header] || strings.EqualFold(header, "x-bf-vk") || strings.EqualFold(header, "Cookie") || strings.EqualFold(header, "Proxy-Authorization") {
		return true
	}
	switch header {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	}
	return false
}

// MaxGRPCAgentNameLength is the longest agent name that can be served over the
// hostname-routed gRPC binding: the name becomes one DNS label of the advertised
// hostname, and DNS caps a label at 63 octets. Longer names remain valid
// registrations but are reachable only over the HTTP bindings.
const MaxGRPCAgentNameLength = 63

// grpcInterfaceURL returns the per-agent gRPC endpoint to advertise on served
// cards, or "" when gRPC is not configured or the name cannot be a DNS label.
func (m *Manager) grpcInterfaceURL(name string) string {
	if m.grpcBaseDomain == "" || m.grpcPort <= 0 || len(name) > MaxGRPCAgentNameLength {
		return ""
	}
	return name + "." + m.grpcBaseDomain + ":" + strconv.Itoa(m.grpcPort)
}

// GatewayJSONRPCPathSuffix and GatewayRESTPathSuffix identify the explicit
// agent-scoped mount points for the gateway's downstream protocol bindings. The
// SDK's HTTP+JSON routes (/tasks, /message:send, ...) hang unmodified beneath the
// REST mount.
const (
	GatewayJSONRPCPathSuffix = "/json-rpc"
	GatewayRESTPathSuffix    = "/rest"
)

// gatewayCardWithExtensions rewrites an upstream card into one that accurately describes
// Bifrost, because the served card must point clients at the gateway rather than
// the upstream agent. It advertises the gateway's own JSON-RPC and HTTP+JSON
// endpoints, reports
// only the capabilities the gateway actually proxies, drops upstream signatures since the
// signed content has changed, retains upstream security scheme descriptions so
// clients can still learn how to authenticate upstream, and adds the x-bf-vk
// scheme. When authentication is enforced the Bifrost scheme is added to every
// upstream requirement, combining both identities rather than offering the
// gateway credential as a weaker alternative.
func gatewayCardWithExtensions(upstream *a2a.AgentCard, externalURL, id string, policy schemas.AgentGatewayAuthPolicy, pushSupported bool, allowedExtensionURIs []string, grpcURL string) (*a2a.AgentCard, error) {
	card := *upstream
	card.SupportedInterfaces = nil
	if externalURL != "" {
		gatewayURL := strings.TrimRight(externalURL, "/") + "/agents/a2a/" + id
		card.SupportedInterfaces = append(card.SupportedInterfaces,
			a2a.NewAgentInterface(gatewayURL+GatewayJSONRPCPathSuffix, a2a.TransportProtocolJSONRPC),
			a2a.NewAgentInterface(gatewayURL+GatewayRESTPathSuffix, a2a.TransportProtocolHTTPJSON),
		)
	}
	if grpcURL != "" {
		card.SupportedInterfaces = append(card.SupportedInterfaces, a2a.NewAgentInterface(grpcURL, a2a.TransportProtocolGRPC))
	}
	// Streaming is forwarded verbatim rather than blanked: the SDK client silently
	// downgrades SendStreamingMessage to a unary SendMessage when the card it
	// resolved says streaming is unsupported, so blanking it would turn every
	// proxied stream into a one-shot response. The extended-card flag is likewise
	// forwarded verbatim because the gateway proxies GetExtendedAgentCard, so
	// clients must be able to discover it — and must not be told it exists when the
	// upstream does not offer it. Push notifications are advertised only when the
	// gateway's durable relay is operational and the upstream can push at all,
	// because the two-hop path needs both. Extensions are retained only when the
	// registration explicitly allows them and their declarations are safe to relay.
	extensions, err := allowedExtensions(upstream.Capabilities.Extensions, extensionSet(allowedExtensionURIs))
	if err != nil {
		return nil, err
	}
	card.Capabilities = a2a.AgentCapabilities{
		Extensions:        extensions,
		Streaming:         upstream.Capabilities.Streaming,
		ExtendedAgentCard: upstream.Capabilities.ExtendedAgentCard,
		PushNotifications: pushSupported && upstream.Capabilities.PushNotifications,
	}
	card.Signatures = nil
	card.SecuritySchemes = cloneSecuritySchemes(upstream.SecuritySchemes)
	const virtualKeyScheme = a2a.SecuritySchemeName("bifrostVirtualKey")
	card.SecuritySchemes[virtualKeyScheme] = a2a.APIKeySecurityScheme{Name: "x-bf-vk", Location: a2a.APIKeySecuritySchemeLocationHeader, Description: "Optional Bifrost virtual key for gateway governance; Bifrost-prefixed VKs are also accepted through established provider-compatible header aliases"}
	if policy.EnforceAuthentication != nil && policy.EnforceAuthentication() {
		card.SecurityRequirements = requireBifrostSecurity(upstream.SecurityRequirements, virtualKeyScheme)
	} else {
		card.SecurityRequirements = cloneSecurityRequirements(upstream.SecurityRequirements)
	}
	return &card, nil
}

// requireBifrostSecurity makes the Bifrost credential mandatory without weakening
// upstream requirements: it is added to each existing requirement option (an AND
// within that option) instead of being appended as a separate satisfiable option.
// With no upstream requirements, the Bifrost credential becomes the only one.
func requireBifrostSecurity(upstream a2a.SecurityRequirementsOptions, bifrostScheme a2a.SecuritySchemeName) a2a.SecurityRequirementsOptions {
	if len(upstream) == 0 {
		return a2a.SecurityRequirementsOptions{{bifrostScheme: make(a2a.SecuritySchemeScopes, 0)}}
	}
	combined := cloneSecurityRequirements(upstream)
	for _, requirement := range combined {
		requirement[bifrostScheme] = a2a.SecuritySchemeScopes{}
	}
	return combined
}

// cloneSecurityRequirements deep-copies requirement options, including scope
// slices, so composing gateway authentication never mutates the upstream card
// object that is also used to connect to the upstream agent. A nil scope slice is
// preserved as nil because it is semantically distinct from an empty one.
func cloneSecurityRequirements(source a2a.SecurityRequirementsOptions) a2a.SecurityRequirementsOptions {
	cloned := make(a2a.SecurityRequirementsOptions, 0, len(source))
	for _, sourceRequirement := range source {
		requirement := make(a2a.SecurityRequirements, len(sourceRequirement))
		for name, scopes := range sourceRequirement {
			if scopes == nil {
				requirement[name] = nil
			} else {
				requirement[name] = append(a2a.SecuritySchemeScopes{}, scopes...)
			}
		}
		cloned = append(cloned, requirement)
	}
	return cloned
}

// cloneSecuritySchemes copies the upstream scheme map with room for the gateway's
// own scheme, so the served card can add x-bf-vk without mutating the upstream
// card shared with the connecting client.
func cloneSecuritySchemes(source a2a.NamedSecuritySchemes) a2a.NamedSecuritySchemes {
	cloned := make(a2a.NamedSecuritySchemes, len(source)+1)
	maps.Copy(cloned, source)
	return cloned
}

func extensionSet(uris []string) map[string]struct{} {
	result := make(map[string]struct{}, len(uris))
	for _, uri := range uris {
		result[uri] = struct{}{}
	}
	return result
}

func allowedExtensions(upstream []a2a.AgentExtension, allowed map[string]struct{}) ([]a2a.AgentExtension, error) {
	result := make([]a2a.AgentExtension, 0, len(upstream))
	seen := make(map[string]struct{}, len(upstream))
	for _, extension := range upstream {
		if extension.URI == "" {
			if extension.Required {
				return nil, errors.New("upstream agent requires an extension without a URI")
			}
			continue
		}
		if _, duplicate := seen[extension.URI]; duplicate {
			return nil, fmt.Errorf("upstream agent declares duplicate extension %q", extension.URI)
		}
		seen[extension.URI] = struct{}{}
		if _, ok := allowed[extension.URI]; !ok {
			if extension.Required {
				return nil, fmt.Errorf("upstream agent requires denied extension %q", extension.URI)
			}
			continue
		}
		encoded, err := json.Marshal(extension.Params)
		if err != nil || len(encoded) > maxExtensionPayloadSize {
			if extension.Required {
				return nil, fmt.Errorf("upstream agent requires untranslatable extension %q", extension.URI)
			}
			continue
		}
		result = append(result, extension)
	}
	return result, nil
}

// validateExtensions runs before the plugin pipeline and before any upstream A2A
// operation. It leases the current generation so negotiation is checked against
// the same SDK card used for forwarding; lazy card discovery may occur, but a
// rejected request cannot invoke an upstream protocol method. The SDK exposes
// requested URIs through CallContext for both JSON-RPC and HTTP+JSON.
func (h *proxyRequestHandler) validateExtensions(ctx context.Context, payload any) error {
	extensions, ok := a2asrv.ExtensionsFrom(ctx)
	if !ok {
		return nil
	}
	lease, err := h.runtime.acquire(ctx, h.manager)
	if err != nil {
		return err
	}
	defer lease.release()

	advertised, err := allowedExtensions(lease.generation.card.Capabilities.Extensions, extensionSet(h.config.extensionURIs))
	if err != nil {
		return a2a.NewError(a2a.ErrExtensionSupportRequired, err.Error())
	}
	requestedURIs := extensions.RequestedURIs()
	supported := make(map[string]a2a.AgentExtension, len(advertised))
	for _, extension := range advertised {
		supported[extension.URI] = extension
		if extension.Required && !slices.Contains(requestedURIs, extension.URI) {
			return a2a.ErrExtensionSupportRequired
		}
	}
	for _, uri := range requestedURIs {
		if _, ok := supported[uri]; !ok {
			return a2a.NewError(a2a.ErrUnsupportedOperation, "requested extension is not supported")
		}
	}
	if carrier, ok := payload.(a2a.MetadataCarrier); ok {
		for key, value := range carrier.Meta() {
			if _, requested := supported[key]; !requested || !slices.Contains(requestedURIs, key) {
				continue
			}
			encoded, err := json.Marshal(value)
			if err != nil || len(encoded) > maxExtensionPayloadSize {
				return a2a.NewError(a2a.ErrInvalidParams, "extension payload exceeds the gateway limit")
			}
		}
	}
	for _, uri := range requestedURIs {
		extensions.Activate(&a2a.AgentExtension{URI: uri})
	}
	return nil
}

// proxyRequestHandler is the Bifrost side of the SDK server contract. It
// implements a2asrv.RequestHandler directly rather than plugging an AgentExecutor
// into the SDK's default handler, because that default handler answers task
// lookups, listings, and subscriptions from its own local task store and mints
// local task and context identifiers. For a gateway that store is always empty
// and the upstream agent, not Bifrost, owns task identity, so every protocol
// method must be forwarded to the upstream agent instead. This mirrors the
// reference proxy handler shipped with the A2A SDK while adding Bifrost's
// generation leasing, header sanitization, stale-interface recovery, and bounded
// outcome logging.
type proxyRequestHandler struct {
	runtime *runtimeAgent
	manager *Manager
	config  runtimeConfig
	logger  schemas.Logger
}

var _ a2asrv.RequestHandler = (*proxyRequestHandler)(nil)

// upstreamContext derives the context used for the upstream call. The inbound
// request's service params (the downstream HTTP headers, as captured by the SDK
// transport) are sanitized and the gateway-owned runtime credential is applied
// before they are attached, keeping the downstream and upstream trust domains
// separate. The downstream A2A-Version copy is dropped here because the gateway's
// own SDK client stamps the version it speaks upstream; forwarding the downstream
// copy would duplicate the header.
func (h *proxyRequestHandler) upstreamContext(ctx context.Context) (context.Context, error) {
	bifrostCtx, ok := ctx.(*schemas.BifrostContext)
	if !ok {
		bifrostCtx = schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	}
	headers, err := h.manager.resolveAuthHeaders(bifrostCtx, h.config.runtimeAuth, upstreamCredentialID(h.config.name, "runtime"))
	if err != nil {
		return nil, err
	}
	headers = withAcceptedCredential(bifrostCtx, headers, h.config.forwardAcceptedCredential, h.config.forwardAcceptedCredentialOverridesAuth)
	return a2aclient.AttachServiceParams(ctx, a2aclient.ServiceParams(upstreamServiceParams(ctx, headers))), nil
}

func withAcceptedCredential(ctx context.Context, headers http.Header, enabled, overrideConfigured bool) http.Header {
	if !enabled {
		return headers
	}
	credential, ok := ctx.Value(schemas.BifrostContextKeyAcceptedCredential).(schemas.AcceptedBifrostCredential)
	if ok && (overrideConfigured || headers.Get(credential.Header) == "") {
		headers.Set(credential.Header, credential.Value)
	}
	return headers
}

// upstreamServiceParams builds the sanitized header set forwarded upstream from
// the inbound call's service params.
func upstreamServiceParams(ctx context.Context, managed http.Header) http.Header {
	requestHeaders := http.Header{}
	if callCtx, ok := a2asrv.CallContextFrom(ctx); ok && callCtx.ServiceParams() != nil {
		for key, values := range callCtx.ServiceParams().List() {
			requestHeaders[http.CanonicalHeaderKey(key)] = slices.Clone(values)
		}
	}
	requestHeaders.Del(a2a.SvcParamVersion)
	// Content negotiation belongs to the upstream SDK transport. In particular,
	// forwarding a downstream SSE Accept header into the SDK's unary fallback for
	// an agent without streaming support makes the server emit SSE to a JSON
	// decoder.
	requestHeaders.Del("Accept")
	return sanitizeUpstreamHeaders(requestHeaders, managed)
}

func provesNoDelivery(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

func grpcFailureCode(err error) (codes.Code, bool) {
	grpcStatus, ok := status.FromError(err)
	if !ok {
		return codes.OK, false
	}
	return grpcStatus.Code(), true
}

func retryableEndpointFailure(err error) bool {
	grpcCode, isGRPC := grpcFailureCode(err)
	return provesNoDelivery(err) || staleInterfaceHint(err) || errors.Is(err, a2a.ErrMethodNotFound) || isGRPC && grpcCode == codes.Unimplemented
}

func interfaceFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, a2a.ErrMethodNotFound) {
		return true
	}
	var statusErr *upstreamHTTPStatusError
	if errors.As(err, &statusErr) {
		return staleInterfaceHint(err) || statusErr.StatusCode >= http.StatusInternalServerError
	}
	var urlErr *url.Error
	var netErr net.Error
	if errors.As(err, &urlErr) || errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	grpcCode, isGRPC := grpcFailureCode(err)
	if !isGRPC {
		return false
	}
	switch grpcCode {
	case codes.Unimplemented, codes.Unavailable, codes.Internal, codes.Unknown, codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}

func advanceAfterFailure(lease *generationLease, err error) bool {
	if !interfaceFailure(err) {
		return false
	}
	return lease.generation.advance(lease.candidateIndex)
}

// forwardUpstream is the shared body of every unary forwarding method, so the
// gateway's runtime guarantees are stated once instead of being re-derived per
// protocol method: the operation runs inside the A2A plugin gate (pre-hooks,
// upstream call, post-hooks exactly once, panic recovery), a client-generation
// lease is held for the whole operation so a concurrent refresh or update cannot
// destroy the client mid-call, and service params are sanitized before
// forwarding. Definitive endpoint failures advance through the remaining
// candidates and replay the operation at most once per candidate. Ambiguous
// failures advance only future traffic and return the original error unchanged.
// SDK-native errors are returned unchanged rather than translated; only a
// plugin-produced failure is surfaced as the plugin's own error.
func forwardUpstream[T any](
	ctx context.Context,
	h *proxyRequestHandler,
	envelope *schemas.BifrostA2ARequest,
	call func(context.Context, sdkClient) (T, error),
	describe func(T) *schemas.BifrostA2AResponse,
) (T, error) {
	var zero T
	var result T
	var opErr error
	opRan := false
	start := time.Now()
	gateCtx := gateContext(ctx)
	_, gateErr := h.manager.RunWithPluginPipeline(gateCtx, envelope, func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		opRan = true
		// One a2a.prepare phase span covers prepared-client acquire plus the first
		// attempt's upstream-context/auth-header build. Rare per-candidate retries
		// rebuild the context unspanned (prep is zeroed after ending).
		prep := startPhaseSpan(gateCtx, "a2a.prepare")
		lease, leaseErr := h.runtime.acquire(gateCtx, h.manager)
		if leaseErr != nil {
			endPhaseSpan(prep)
			opErr = leaseErr
			return nil, leaseErr
		}
		generation := lease.generation
		candidateCount := len(generation.candidates)
		attempts := 0
		for {
			attempts++
			gateCtx.SetValue(schemas.BifrostContextKeyA2AUpstreamTransport, lease.candidate.transport)
			callCtx, cancelCall := context.WithCancel(gateCtx)
			stopRuntimeWatch := context.AfterFunc(h.runtime.ctx, cancelCall)
			failedCandidate := lease.candidateIndex
			value, callErr, authErr := func() (T, error, error) {
				defer lease.release()
				defer cancelCall()
				defer stopRuntimeWatch()

				upstreamCtx, authErr := h.upstreamContext(callCtx)
				endPhaseSpan(prep)
				prep = phaseSpan{}
				if authErr != nil {
					return zero, nil, authErr
				}
				callStart := time.Now()
				value, callErr := call(upstreamCtx, lease.candidate.client)
				// Push config mutations measure their SDK calls separately because the
				// callback also persists local state. Other calls accumulate their full
				// upstream duration here, including each retry.
				if envelope.RequestType != schemas.A2ARequestTypeCreateTaskPushConfig && envelope.RequestType != schemas.A2ARequestTypeDeleteTaskPushConfig {
					schemas.AddUpstreamLatency(gateCtx, time.Since(callStart))
				}
				advanceAfterFailure(lease, callErr)
				if staleInterfaceHint(callErr) {
					h.runtime.requestRefresh(h.manager, generation)
				}
				return value, callErr, nil
			}()
			if authErr != nil {
				opErr = authErr
				return nil, authErr
			}
			if callErr != nil {
				if retryableEndpointFailure(callErr) && attempts < candidateCount {
					nextCandidate := (failedCandidate + 1) % candidateCount
					var ok bool
					lease, ok = generation.acquireCandidate(nextCandidate)
					if ok {
						continue
					}
				}
				opErr = callErr
				return nil, callErr
			}
			result = value
			// a2a.encode covers the response describe plus the re-encode of the
			// upstream payload into the observed response body.
			enc := startPhaseSpan(gateCtx, "a2a.encode")
			resp := describe(value)
			if resp != nil {
				resp.ResponseBody = marshalA2APayload(value)
			}
			endPhaseSpan(enc)
			if resp != nil {
				resp.ExtraFields.Latency = time.Since(start).Milliseconds()
				resp.PopulateUpstreamLatency(gateCtx)
				resp.PopulateOverheadLatency(gateCtx, time.Since(start))
			}
			return resp, nil
		}
	})
	// Stamp the accumulated upstream total on the root span so the tracer can
	// derive overhead at export and the logging plugin can backfill the log row.
	gateCtx.StampUpstreamLatency()
	err := gateOutcomeError(gateCtx, gateErr, opErr)
	if err != nil {
		return zero, err
	}
	if !opRan {
		return zero, a2a.ErrInternalError
	}
	return result, nil
}

// gateOutcomeError preserves an upstream error exactly. Only failures created by
// Bifrost or a plugin are converted at this final protocol boundary, after
// post-hooks have observed the rich BifrostError.
func gateOutcomeError(ctx *schemas.BifrostContext, gateErr *schemas.BifrostError, opErr error) error {
	if opErr != nil {
		return opErr
	}
	if gateErr == nil {
		return nil
	}

	sentinel := a2a.ErrServerError
	if gateErr.StatusCode != nil {
		switch *gateErr.StatusCode {
		case http.StatusUnauthorized:
			sentinel = a2a.ErrUnauthenticated
		case http.StatusForbidden:
			sentinel = a2a.ErrUnauthorized
		case http.StatusInternalServerError:
			sentinel = a2a.ErrInternalError
		}
	} else if gateErr.IsBifrostError {
		sentinel = a2a.ErrInternalError
	}

	message := gateErr.GetErrorString()
	if errors.Is(sentinel, a2a.ErrInternalError) {
		message = sentinel.Error()
	} else if message == "" {
		message = sentinel.Error()
	}
	metadata := map[string]string{}
	if gateErr.ExtraFields.A2AAgentName != "" {
		metadata["agentName"] = gateErr.ExtraFields.A2AAgentName
	}
	if gateErr.ExtraFields.A2ARequestType != "" {
		metadata["requestType"] = string(gateErr.ExtraFields.A2ARequestType)
	}
	if ctx != nil {
		if requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string); ok && requestID != "" {
			metadata["requestId"] = requestID
		}
	}
	native := a2a.NewError(sentinel, message)
	if len(metadata) > 0 {
		native.WithErrorInfoMeta(metadata)
	}
	return native
}

// forwardUpstreamStream is the streaming counterpart of forwardUpstream. The
// lease must outlive the method call itself, because the upstream stream is only
// consumed while the returned sequence is being iterated; it is therefore
// released when iteration finishes rather than when the method returns, which is
// what keeps the underlying client alive for the whole stream. The whole
// iteration runs inside one gate invocation, so the pre-hooks run once before the
// first event and the post-hooks run exactly once when the stream terminates —
// per-event hooks are deliberately not part of this contract.
//
// Each downstream stream owns exactly one upstream stream and exactly one
// cancellation scope. The scope is a cancellable child of the inbound request
// context (so a vanished downstream client, which the SDK transport observes as a
// failed SSE event or keep-alive write and reports by cancelling that context,
// tears the upstream stream down) that is additionally cancelled when the agent's
// runtime closes. It is cancelled on every exit from the iteration — normal
// completion, an upstream terminal error, the consumer abandoning the stream
// (yield reporting false), or a panic unwinding through the deferred cancel — so
// abandoning a subscription cannot leave an upstream stream reading forever. It
// is a per-stream scope, so cancelling one stream never disturbs another stream,
// the shared prepared client, or any other agent.
//
// Cancelling the scope ends only the subscription: it aborts the upstream
// SSE/stream read, and never issues an upstream CancelTask. Durable task
// execution is stopped exclusively by the explicit CancelTask protocol method.
// A stream failure may advance the candidate used by future operations, but this
// stream is never replayed, even when it failed before yielding its first event.
func forwardUpstreamStream(ctx context.Context, h *proxyRequestHandler, envelope *schemas.BifrostA2ARequest, call func(context.Context, sdkClient) iter.Seq2[a2a.Event, error]) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		start := time.Now()
		gateCtx := gateContext(ctx)
		// Per-event describe/observe CPU is accumulated on the shared stream
		// overhead accumulator and stamped at stream completion, exactly like the
		// LLM streaming path's per-chunk mapping.
		gateCtx.ResetStreamOverhead()
		var opErr error
		opRan := false
		var events int64
		consumerStopped := false
		_, gateErr := h.manager.RunWithPluginPipeline(gateCtx, envelope, func(_ *schemas.BifrostA2ARequest, observe func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
			opRan = true
			// a2a.prepare covers prepared-client acquire plus the upstream
			// context/auth-header build; it ends before the pull loop starts.
			prep := startPhaseSpan(gateCtx, "a2a.prepare")
			lease, leaseErr := h.runtime.acquire(gateCtx, h.manager)
			if leaseErr != nil {
				endPhaseSpan(prep)
				opErr = leaseErr
				consumerStopped = !yield(nil, leaseErr)
				return nil, leaseErr
			}
			defer lease.release()
			gateCtx.SetValue(schemas.BifrostContextKeyA2AUpstreamTransport, lease.candidate.transport)
			streamCtx, cancelStream := context.WithCancel(gateCtx)
			defer cancelStream()
			// A runtime closed mid-stream (agent deleted, updated, or shutdown)
			// must also end this stream; AfterFunc is used instead of a watcher
			// goroutine so the hook is removed as soon as the stream finishes.
			stopRuntimeWatch := context.AfterFunc(h.runtime.ctx, cancelStream)
			defer stopRuntimeWatch()
			var streamErr error
			upstreamCtx, authErr := h.upstreamContext(streamCtx)
			endPhaseSpan(prep)
			if authErr != nil {
				opErr = authErr
				yield(nil, authErr)
				return nil, authErr
			}
			// Each pull on the upstream iterator blocks on the provider socket;
			// time spent inside the loop body (yielding downstream, observing
			// events) is Bifrost/downstream work and is excluded.
			pullStart := time.Now()
			for event, eventErr := range call(upstreamCtx, lease.candidate.client) {
				schemas.AddUpstreamLatency(gateCtx, time.Since(pullStart))
				if eventErr != nil {
					streamErr = eventErr
				}
				if !yield(event, eventErr) {
					consumerStopped = true
					cancelStream()
					break
				}
				if eventErr == nil {
					events++
					if observe != nil {
						// Per-event describe (payload re-encode) + observe is Bifrost
						// mapping work; accumulate it like the LLM path's per-chunk
						// conversion instead of opening a span per event.
						observeStart := time.Now()
						observe(describeA2AEvent(envelope, events, event))
						schemas.AddStreamConvert(gateCtx, time.Since(observeStart))
					}
				}
				if eventErr != nil {
					break
				}
				pullStart = time.Now()
			}
			if streamErr != nil {
				advanceAfterFailure(lease, streamErr)
				if staleInterfaceHint(streamErr) {
					h.runtime.requestRefresh(h.manager, lease.generation)
				}
			}
			if streamErr != nil {
				opErr = streamErr
				return nil, streamErr
			}
			if consumerStopped {
				return nil, context.Canceled
			}
			resp := &schemas.BifrostA2AResponse{
				ExtraFields: schemas.BifrostA2AResponseExtraFields{Latency: time.Since(start).Milliseconds()},
			}
			resp.PopulateUpstreamLatency(gateCtx)
			resp.PopulateOverheadLatency(gateCtx, time.Since(start))
			return resp, nil
		})
		// Stamp at stream completion: the accumulators keep growing until the
		// stream drains, mirroring the LLM streaming path.
		gateCtx.StampUpstreamLatency()
		gateCtx.StampStreamOverhead()
		if consumerStopped {
			return
		}
		streamErr := gateOutcomeError(gateCtx, gateErr, opErr)
		if streamErr == nil && !opRan {
			streamErr = a2a.ErrInternalError
		}
		// A pre-hook denial, successful short-circuit, or recovered operation panic never reached a yielded
		// terminal error, so terminate the downstream stream with the native error.
		if streamErr != nil && opErr == nil {
			if !yield(nil, streamErr) {
				return
			}
		}
	}
}

func describeA2AEvent(envelope *schemas.BifrostA2ARequest, sequence int64, event a2a.Event) *schemas.BifrostA2AEvent {
	observed := &schemas.BifrostA2AEvent{Sequence: sequence, Body: marshalA2APayload(event)}
	if envelope != nil {
		observed.RequestType = envelope.RequestType
		observed.AgentName = envelope.AgentName
	}
	contentType := func(parts a2a.ContentParts) string {
		for _, part := range parts {
			if part != nil && part.MediaType != "" {
				return part.MediaType
			}
		}
		return ""
	}
	switch value := event.(type) {
	case *a2a.Message:
		observed.EventType = schemas.A2AEventTypeMessage
		observed.TaskID, observed.ContextID, observed.MessageID = string(value.TaskID), value.ContextID, value.ID
		observed.ContentType = contentType(value.Parts)
	case *a2a.Task:
		observed.EventType = schemas.A2AEventTypeTask
		observed.TaskID, observed.ContextID, observed.TaskState = string(value.ID), value.ContextID, string(value.Status.State)
		if value.Status.Message != nil {
			observed.MessageID = value.Status.Message.ID
			observed.ContentType = contentType(value.Status.Message.Parts)
		}
	case *a2a.TaskStatusUpdateEvent:
		observed.EventType = schemas.A2AEventTypeStatusUpdate
		observed.TaskID, observed.ContextID, observed.TaskState = string(value.TaskID), value.ContextID, string(value.Status.State)
		if value.Status.Message != nil {
			observed.MessageID = value.Status.Message.ID
			observed.ContentType = contentType(value.Status.Message.Parts)
		}
	case *a2a.TaskArtifactUpdateEvent:
		observed.EventType = schemas.A2AEventTypeArtifactUpdate
		observed.TaskID, observed.ContextID = string(value.TaskID), value.ContextID
		if value.Artifact != nil {
			observed.ArtifactID = string(value.Artifact.ID)
			observed.ContentType = contentType(value.Artifact.Parts)
		}
	}
	return observed
}

// envelope builds the plugin-facing request for one protocol operation. The
// agent name is always stamped because it is the resource key authorization is
// decided against.
func (h *proxyRequestHandler) envelope(requestType schemas.A2ARequestType) *schemas.BifrostA2ARequest {
	return &schemas.BifrostA2ARequest{RequestType: requestType, AgentName: h.config.name}
}

func marshalA2APayload(value any) *string {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	body := string(data)
	return &body
}

func setA2ARequestBody(envelope *schemas.BifrostA2ARequest, value any) *schemas.BifrostA2ARequest {
	envelope.RequestBody = marshalA2APayload(value)
	return envelope
}

// taskEnvelope builds the plugin-facing request for the task-scoped operations.
func (h *proxyRequestHandler) taskEnvelope(requestType schemas.A2ARequestType, request any, taskID a2a.TaskID) *schemas.BifrostA2ARequest {
	req := h.envelope(requestType)
	req.BifrostA2ATaskRequest = &schemas.BifrostA2ATaskRequest{TaskID: string(taskID)}
	return setA2ARequestBody(req, request)
}

// describeTask projects an upstream task onto the plugin-facing response so a
// post-hook can observe the outcome without depending on the A2A SDK types.
func describeTask(task *a2a.Task) *schemas.BifrostA2AResponse {
	resp := &schemas.BifrostA2AResponse{BifrostA2ATaskResponse: &schemas.BifrostA2ATaskResponse{}}
	if task != nil {
		resp.BifrostA2ATaskResponse.TaskID = string(task.ID)
		resp.BifrostA2ATaskResponse.ContextID = string(task.ContextID)
		resp.BifrostA2ATaskResponse.State = string(task.Status.State)
	}
	return resp
}

// GetTask forwards the lookup upstream because the upstream agent is the sole
// authority on task identity and state; the gateway keeps no task store and no
// task index of its own, so an identifier it has never seen is simply forwarded
// and the addressed upstream agent answers with its own not-found error.
func (h *proxyRequestHandler) GetTask(ctx context.Context, req *a2a.GetTaskRequest) (*a2a.Task, error) {
	if err := h.validateExtensions(ctx, req); err != nil {
		return nil, err
	}
	return forwardUpstream(ctx, h, h.taskEnvelope(schemas.A2ARequestTypeGetTask, req, req.ID), func(ctx context.Context, client sdkClient) (*a2a.Task, error) {
		return client.GetTask(ctx, req)
	}, describeTask)
}

// ListTasks forwards the listing upstream for the same reason as GetTask: only
// the upstream agent knows which tasks exist.
func (h *proxyRequestHandler) ListTasks(ctx context.Context, req *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	if err := h.validateExtensions(ctx, req); err != nil {
		return nil, err
	}
	envelope := h.envelope(schemas.A2ARequestTypeListTasks)
	setA2ARequestBody(envelope, req)
	return forwardUpstream(ctx, h, envelope, func(ctx context.Context, client sdkClient) (*a2a.ListTasksResponse, error) {
		return client.ListTasks(ctx, req)
	}, func(resp *a2a.ListTasksResponse) *schemas.BifrostA2AResponse {
		count := 0
		if resp != nil {
			count = len(resp.Tasks)
		}
		return &schemas.BifrostA2AResponse{BifrostA2AListTasksResponse: &schemas.BifrostA2AListTasksResponse{Count: count}}
	})
}

// CancelTask forwards the cancellation upstream so an explicit cancel actually
// stops the real execution, rather than being answered from gateway-local state
// that never held the task. As with GetTask, the upstream agent decides whether
// the identifier exists.
func (h *proxyRequestHandler) CancelTask(ctx context.Context, req *a2a.CancelTaskRequest) (*a2a.Task, error) {
	if err := h.validateExtensions(ctx, req); err != nil {
		return nil, err
	}
	return forwardUpstream(ctx, h, h.taskEnvelope(schemas.A2ARequestTypeCancelTask, req, req.ID), func(ctx context.Context, client sdkClient) (*a2a.Task, error) {
		return client.CancelTask(ctx, req)
	}, describeTask)
}

// SendMessage forwards one non-streaming message and returns the upstream result
// verbatim, preserving whatever task or message identity the upstream assigned.
func (h *proxyRequestHandler) SendMessage(ctx context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	if err := h.validateExtensions(ctx, req); err != nil {
		return nil, err
	}
	// Envelope first so plugin/log visibility reflects the client's request;
	// the rewrite below replaces any embedded push callback with Bifrost's
	// ingress and a minted credential before the request goes upstream.
	envelope := h.messageEnvelope(schemas.A2ARequestTypeSendMessage, req)
	settlePush, err := h.rewriteEmbeddedPushConfig(ctx, req)
	if err != nil {
		return nil, err
	}
	result, err := forwardUpstream(ctx, h, envelope, func(ctx context.Context, client sdkClient) (a2a.SendMessageResult, error) {
		return client.SendMessage(ctx, req)
	}, describeSendMessageResult)
	if settlePush != nil {
		if taskID := eventTaskID(result); err == nil && taskID != "" {
			settlePush(ctx, taskID)
		} else {
			settlePush(context.WithoutCancel(ctx), "")
		}
	}
	return result, err
}

// messageEnvelope builds the plugin-facing request for a message send, carrying
// correlation identifiers and the SDK's strict-v1 typed request representation.
func (h *proxyRequestHandler) messageEnvelope(requestType schemas.A2ARequestType, req *a2a.SendMessageRequest) *schemas.BifrostA2ARequest {
	envelope := h.envelope(requestType)
	envelope.BifrostA2ASendMessageRequest = &schemas.BifrostA2ASendMessageRequest{}
	if req != nil && req.Message != nil {
		envelope.BifrostA2ASendMessageRequest.MessageID = string(req.Message.ID)
		envelope.BifrostA2ASendMessageRequest.ContextID = string(req.Message.ContextID)
		envelope.BifrostA2ASendMessageRequest.TaskID = string(req.Message.TaskID)
	}
	return setA2ARequestBody(envelope, req)
}

// describeSendMessageResult projects the upstream send result onto the
// plugin-facing response. The upstream may answer with either a message or a
// task, so both shapes collapse onto the same correlation identifiers.
func describeSendMessageResult(result a2a.SendMessageResult) *schemas.BifrostA2AResponse {
	resp := &schemas.BifrostA2AResponse{BifrostA2ASendMessageResponse: &schemas.BifrostA2ASendMessageResponse{}}
	switch value := result.(type) {
	case *a2a.Message:
		if value != nil {
			resp.BifrostA2ASendMessageResponse.MessageID = string(value.ID)
			resp.BifrostA2ASendMessageResponse.ContextID = string(value.ContextID)
			resp.BifrostA2ASendMessageResponse.TaskID = string(value.TaskID)
		}
	case *a2a.Task:
		if value != nil {
			resp.BifrostA2ASendMessageResponse.TaskID = string(value.ID)
			resp.BifrostA2ASendMessageResponse.ContextID = string(value.ContextID)
		}
	}
	return resp
}

// SendStreamingMessage forwards a streaming send, holding the client-generation
// lease for the lifetime of the returned stream.
func (h *proxyRequestHandler) SendStreamingMessage(ctx context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	if err := h.validateExtensions(ctx, req); err != nil {
		return func(yield func(a2a.Event, error) bool) { yield(nil, err) }
	}
	envelope := h.messageEnvelope(schemas.A2ARequestTypeSendStreamingMessage, req)
	return func(yield func(a2a.Event, error) bool) {
		settlePush, err := h.rewriteEmbeddedPushConfig(ctx, req)
		if err != nil {
			yield(nil, err)
			return
		}
		if settlePush != nil {
			// The stream context may already be canceled when iteration ends, so
			// cleanup must not ride it. Binding below settles this closure first
			// when a task-bearing event is observed.
			defer settlePush(context.WithoutCancel(ctx), "")
		}

		upstream := forwardUpstreamStream(ctx, h, envelope, func(ctx context.Context, client sdkClient) iter.Seq2[a2a.Event, error] {
			return client.SendStreamingMessage(ctx, req)
		})
		for event, err := range upstream {
			if settlePush != nil && err == nil {
				if id := eventTaskID(event); id != "" {
					settlePush(ctx, id)
				}
			}
			if !yield(event, err) {
				return
			}
		}
	}
}

// SubscribeToTask forwards a task subscription, holding the client-generation
// lease for the lifetime of the returned stream. The upstream agent decides
// whether the subscribed task exists.
func (h *proxyRequestHandler) SubscribeToTask(ctx context.Context, req *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	if err := h.validateExtensions(ctx, req); err != nil {
		return func(yield func(a2a.Event, error) bool) { yield(nil, err) }
	}
	return forwardUpstreamStream(ctx, h, h.taskEnvelope(schemas.A2ARequestTypeSubscribeToTask, req, req.ID), func(ctx context.Context, client sdkClient) iter.Seq2[a2a.Event, error] {
		return client.SubscribeToTask(ctx, req)
	})
}

// GetExtendedAgentCard forwards the authenticated extended-card read to the
// upstream agent through the prepared SDK client and returns it after exactly the
// same gateway transformation the public card receives, so the extended card also
// points at Bifrost, advertises the Bifrost virtual-key scheme, drops the upstream
// signature, and composes security requirements from the live enforcement
// setting. An upstream that exposes no extended card surfaces the SDK-native
// error (a2a.ErrExtendedCardNotConfigured) unchanged; no card is fabricated.
//
// Access follows exactly the same rule as every other protocol operation, decided
// at the transport boundary rather than here: with enforcement enabled a request
// carrying no Bifrost identity is refused with 401, and an identified caller
// without a grant is refused with 403 by the governance plugin's PreA2A admission
// check. With enforcement disabled Bifrost does not interfere and the request is
// forwarded anonymously — the extended card remains privileged because the
// upstream agent still enforces its own authentication on it.
func (h *proxyRequestHandler) GetExtendedAgentCard(ctx context.Context, req *a2a.GetExtendedAgentCardRequest) (*a2a.AgentCard, error) {
	if err := h.validateExtensions(ctx, req); err != nil {
		return nil, err
	}
	envelope := h.envelope(schemas.A2ARequestTypeGetExtendedAgentCard)
	setA2ARequestBody(envelope, req)
	return forwardUpstream(ctx, h, envelope, func(ctx context.Context, client sdkClient) (*a2a.AgentCard, error) {
		card, err := client.GetExtendedAgentCard(ctx, req)
		if err != nil {
			return nil, err
		}
		if card == nil {
			return nil, a2a.ErrExtendedCardNotConfigured
		}
		externalURL := h.manager.externalBaseURL()
		if requestOrigin, _ := ctx.Value(schemas.BifrostContextKeyA2ARequestOrigin).(string); requestOrigin != "" {
			externalURL = requestOrigin
		}
		return gatewayCardWithExtensions(card, externalURL, h.config.name, h.manager.authPolicy, h.manager.pushEnabled(), h.config.extensionURIs, h.manager.grpcInterfaceURL(h.config.name))
	}, func(*a2a.AgentCard) *schemas.BifrostA2AResponse {
		return &schemas.BifrostA2AResponse{BifrostA2AGetAgentCardResponse: &schemas.BifrostA2AGetAgentCardResponse{}}
	})
}

// bounded truncates text destined for logs so an upstream-controlled error string
// cannot inflate log records without limit.
func bounded(s string) string {
	if len(s) > maxOutcomeText {
		return s[:maxOutcomeText]
	}
	return s
}
