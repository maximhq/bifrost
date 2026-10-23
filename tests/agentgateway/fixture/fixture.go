package fixture

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
)

const (
	textMode          = "text/plain"
	streamStepDelay   = 2 * time.Second
	cancelWindowDelay = 30 * time.Second
	lisbonImageURL    = "https://images.unsplash.com/photo-1555881400-74d7acaacd8b?auto=format&fit=crop&w=1280&q=80"
	belemImageURL     = "https://images.unsplash.com/photo-1548707309-dcebeab9ea9b?auto=format&fit=crop&w=1280&q=80"
	lisbonVideoURL    = "https://samplelib.com/mp4/sample-5s.mp4"
)

const itinerary = `Lisbon — 3-day itinerary

Day 1: Alfama and the river
- Morning: Miradouro de Santa Luzia and Alfama lanes
- Lunch: traditional Portuguese meal near Sé
- Afternoon: Praça do Comércio and the Tagus waterfront
- Evening: live fado in Alfama

Day 2: Belém
- Morning: Jerónimos Monastery
- Lunch: Pastéis de Belém
- Afternoon: Belém Tower and waterfront cycle
- Evening: sunset at MAAT

Day 3: Bairro Alto and Sintra option
- Morning: Tram 28 and Chiado
- Afternoon: choose LX Factory or a half-day Sintra trip
- Evening: Bairro Alto dinner
`

type executor struct {
	streamStepDelay   time.Duration
	cancelWindowDelay time.Duration
	// gate, when non-nil, holds the stream before its second checkpoint until
	// it is closed, so slow callers do not race a fixed timer.
	gate <-chan struct{}
}

// newExecutor creates an executor with production fixture delays.
func newExecutor() *executor {
	return &executor{
		streamStepDelay:   streamStepDelay,
		cancelWindowDelay: cancelWindowDelay,
	}
}

// Execute emits deterministic lifecycle states and artifacts for each fixture command.
func (e *executor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if execCtx.StoredTask == nil && !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
			return
		}
		if !yield(status(execCtx, a2a.TaskStateWorking, "fixture accepted the request"), nil) {
			return
		}

		text := messageText(execCtx.Message)
		switch text {
		case "start lifecycle":
			artifact := a2a.NewArtifactEvent(execCtx, a2a.NewTextPart("draft itinerary: Lisbon"))
			artifact.Artifact.Name = "itinerary"
			artifact.Artifact.Description = "A static itinerary draft"
			if !yield(artifact, nil) {
				return
			}
			yield(status(execCtx, a2a.TaskStateInputRequired, "choose a trip length"), nil)
		case "continue: 3 days":
			for _, artifact := range lisbonArtifacts(execCtx) {
				if !yield(artifact, nil) {
					return
				}
			}
			yield(status(execCtx, a2a.TaskStateCompleted, "Your three-day Lisbon itinerary and travel pack are ready."), nil)
		case "fail task":
			yield(status(execCtx, a2a.TaskStateFailed, "fixture task failed"), nil)
		case "reject task":
			yield(status(execCtx, a2a.TaskStateRejected, "fixture task rejected"), nil)
		case "interruptible stream":
			artifact := a2a.NewArtifactEvent(execCtx, a2a.NewTextPart("stream checkpoint 1"))
			artifact.Artifact.Name = "stream-progress"
			if !yield(artifact, nil) {
				return
			}
			if e.gate != nil {
				select {
				case <-ctx.Done():
					return
				case <-e.gate:
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(e.streamStepDelay):
			}
			if !yield(a2a.NewArtifactUpdateEvent(execCtx, artifact.Artifact.ID, a2a.NewTextPart("; checkpoint 2")), nil) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(e.streamStepDelay):
			}
			yield(status(execCtx, a2a.TaskStateCompleted, "stream work completed"), nil)
		case "cancel me":
			select {
			case <-ctx.Done():
				return
			case <-time.After(e.cancelWindowDelay):
				yield(status(execCtx, a2a.TaskStateCompleted, "cancel window elapsed"), nil)
			}
		default:
			yield(status(execCtx, a2a.TaskStateCompleted, "static fixture response"), nil)
		}
	}
}

// Cancel transitions a fixture task to the canceled terminal state.
func (*executor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(status(execCtx, a2a.TaskStateCanceled, "task canceled by client"), nil)
	}
}

// lisbonArtifacts returns a deterministic travel pack covering inline files and URL-backed visual media.
func lisbonArtifacts(info a2a.TaskInfoProvider) []*a2a.TaskArtifactUpdateEvent {
	itineraryPart := a2a.NewRawPart([]byte(itinerary))
	itineraryPart.Filename = "lisbon-itinerary.txt"
	itineraryPart.MediaType = "text/plain"

	arrivalImage := a2a.NewFileURLPart(a2a.URL(lisbonImageURL), "image/jpeg")
	arrivalImage.Filename = "lisbon-arrival.jpg"
	belemImage := a2a.NewFileURLPart(a2a.URL(belemImageURL), "image/jpeg")
	belemImage.Filename = "belem-afternoon.jpg"
	video := a2a.NewFileURLPart(a2a.URL(lisbonVideoURL), "video/mp4")
	video.Filename = "lisbon-city-preview.mp4"

	return []*a2a.TaskArtifactUpdateEvent{
		artifact(info, "Lisbon itinerary", "A downloadable three-day itinerary", itineraryPart),
		artifact(info, "Day 1 — Lisbon arrival", "Morning inspiration for Alfama and the Tagus", arrivalImage),
		artifact(info, "Day 2 — Belém", "Waterfront inspiration for the second afternoon", belemImage),
		artifact(info, "Lisbon city preview", "A short city travel preview", video),
	}
}

// artifact creates one named fixture artifact containing the supplied A2A part.
func artifact(info a2a.TaskInfoProvider, name, description string, part *a2a.Part) *a2a.TaskArtifactUpdateEvent {
	event := a2a.NewArtifactEvent(info, part)
	event.Artifact.Name = name
	event.Artifact.Description = description
	return event
}

// status creates a task status event with a human-readable Agent message.
func status(info a2a.TaskInfoProvider, state a2a.TaskState, text string) *a2a.TaskStatusUpdateEvent {
	return a2a.NewStatusUpdateEvent(info, state, a2a.NewMessageForTask(a2a.MessageRoleAgent, info, a2a.NewTextPart(text)))
}

// messageText returns the trimmed first text part from a message.
func messageText(message *a2a.Message) string {
	if message == nil || len(message.Parts) == 0 {
		return ""
	}
	return strings.TrimSpace(message.Parts[0].Text())
}

type fixtureAuth struct {
	a2asrv.PassthroughCallInterceptor
}

// Call records the upstream identity and decision without retaining credentials.
type Call struct {
	Caller string
	Method string
	Error  string
}

type credentialAuth struct {
	a2asrv.PassthroughCallInterceptor
	mu      sync.Mutex
	callers map[string]string
	store   *push.InMemoryPushConfigStore
	shared  map[a2a.TaskID]map[string]map[string]bool
	calls   []Call
}

func (a *credentialAuth) Before(ctx context.Context, call *a2asrv.CallContext, req *a2asrv.Request) (context.Context, any, error) {
	key := ""
	if call.ServiceParams() != nil {
		for header, values := range call.ServiceParams().List() {
			if strings.EqualFold(header, "x-bf-vk") && len(values) == 1 {
				key = values[0]
			}
		}
	}
	caller := a.callers[key]
	if caller == "" {
		return ctx, nil, a2a.ErrUnauthenticated
	}
	call.User = a2asrv.NewAuthenticatedUser(caller, nil)
	a.mu.Lock()
	defer a.mu.Unlock()
	switch request := req.Payload.(type) {
	case *a2a.GetTaskPushConfigRequest:
		if a.shared[request.TaskID][caller][request.ID] {
			config, err := a.store.Get(ctx, request.TaskID, request.ID)
			if err != nil {
				return ctx, nil, err
			}
			if config == nil {
				return ctx, nil, push.ErrPushConfigNotFound
			}
			return ctx, config, nil
		}
	case *a2a.ListTaskPushConfigRequest:
		if allowed := a.shared[request.TaskID][caller]; len(allowed) > 0 {
			configs, err := a.store.List(ctx, request.TaskID)
			if err != nil {
				return ctx, nil, err
			}
			result := &a2a.ListTaskPushConfigResponse{Configs: []*a2a.PushConfig{}}
			for _, config := range configs {
				if allowed[config.ID] {
					result.Configs = append(result.Configs, config)
				}
			}
			return ctx, result, nil
		}
	}
	return ctx, nil, nil // The SDK's task store enforces ownership for normal operations.
}

func (a *credentialAuth) After(_ context.Context, call *a2asrv.CallContext, response *a2asrv.Response) error {
	record := Call{Caller: call.User.Name, Method: call.Method()}
	if response.Err != nil {
		record.Error = response.Err.Error()
	}
	a.mu.Lock()
	a.calls = append(a.calls, record)
	a.mu.Unlock()
	return nil
}

// Before marks fixture calls as authenticated without imposing test credentials.
func (*fixtureAuth) Before(ctx context.Context, callCtx *a2asrv.CallContext, _ *a2asrv.Request) (context.Context, any, error) {
	callCtx.User = &a2asrv.User{Name: "lifecycle-fixture", Authenticated: true}
	return ctx, nil, nil
}

// card describes the lifecycle fixture's supported protocol capabilities.
func card(baseURL string) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:        "Strict v1 Lifecycle Fixture",
		Description: "Static single-context task lifecycle fixture",
		Version:     "1.0.0",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(baseURL+"/jsonrpc", a2a.TransportProtocolJSONRPC),
		},
		DefaultInputModes:  []string{textMode},
		DefaultOutputModes: []string{textMode},
		Capabilities: a2a.AgentCapabilities{
			Streaming:         true,
			PushNotifications: true,
			ExtendedAgentCard: true,
		},
		Skills: []a2a.AgentSkill{{
			ID:          "itinerary-lifecycle",
			Name:        "Itinerary lifecycle",
			Description: "Builds a static itinerary through continuation, streaming, push, and cancellation",
			Tags:        []string{"fixture", "lifecycle"},
		}},
	}
}

// Server is a running lifecycle fixture Agent.
type Server struct {
	// CardURL is the Agent Card URL to register with the gateway.
	CardURL string
	server  *http.Server
	gate    chan struct{}
	once    sync.Once
	auth    *credentialAuth
}

// ReleaseCheckpoint lets a held stream proceed to its second checkpoint. Call
// it once the client is ready to observe the remaining events.
func (s *Server) ReleaseCheckpoint() { s.once.Do(func() { close(s.gate) }) }

// Start serves the lifecycle fixture on bind (for example "127.0.0.1:0") and
// advertises host in its Agent Card so a gateway that cannot reach loopback can
// still dial it. Close shuts the listener down.
func Start(bind, host string) (*Server, error) {
	return start(bind, host, nil)
}

// StartWithCredentials authenticates forwarded virtual keys as distinct callers.
func StartWithCredentials(bind, host string, callers map[string]string) (*Server, error) {
	if callers == nil {
		callers = map[string]string{}
	}
	return start(bind, host, maps.Clone(callers))
}

// SharePushConfigs grants only push-read access, without granting task-content access.
func (s *Server) SharePushConfigs(task a2a.TaskID, caller string, ids ...string) {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	if s.auth.shared[task] == nil {
		s.auth.shared[task] = map[string]map[string]bool{}
	}
	allowed := map[string]bool{}
	for _, id := range ids {
		allowed[id] = true
	}
	s.auth.shared[task][caller] = allowed
}

// Calls returns a snapshot of actual upstream caller identities and outcomes.
func (s *Server) Calls() []Call {
	if s.auth == nil {
		return nil
	}
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	return append([]Call(nil), s.auth.calls...)
}

func start(bind, host string, callers map[string]string) (*Server, error) {
	listener, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, fmt.Errorf("listen for lifecycle fixture: %w", err)
	}
	baseURL := fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)))
	publicCard := card(baseURL)
	store := push.NewInMemoryStore()
	var auth *credentialAuth
	var interceptor a2asrv.CallInterceptor = &fixtureAuth{}
	if callers != nil {
		auth = &credentialAuth{callers: callers, store: store, shared: map[a2a.TaskID]map[string]map[string]bool{}}
		interceptor = auth
		publicCard.SecuritySchemes = a2a.NamedSecuritySchemes{"callerKey": a2a.APIKeySecurityScheme{Location: a2a.APIKeySecuritySchemeLocationHeader, Name: "x-bf-vk"}}
		publicCard.SecurityRequirements = a2a.SecurityRequirementsOptions{{"callerKey": {}}}
	}
	extendedCard := *publicCard
	extendedCard.Description = "Extended static lifecycle fixture card"
	exec := newExecutor()
	gate := make(chan struct{})
	exec.gate = gate
	handler := a2asrv.NewHandler(
		exec,
		a2asrv.WithCapabilityChecks(&publicCard.Capabilities),
		a2asrv.WithExtendedAgentCard(&extendedCard),
		a2asrv.WithCallInterceptors(interceptor),
		a2asrv.WithPushNotifications(
			store,
			push.NewHTTPPushSender(&push.HTTPSenderConfig{AllowPrivateNetworks: true, Timeout: 10 * time.Second}),
		),
	)
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(publicCard))
	mux.Handle("/jsonrpc", a2asrv.NewJSONRPCHandler(handler))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	return &Server{CardURL: baseURL + a2asrv.WellKnownAgentCardPath, server: srv, gate: gate, auth: auth}, nil
}

// Close stops the fixture server and drops open connections.
func (s *Server) Close() error { return s.server.Close() }

// NewLifecycleHandler exposes the existing finite lifecycle fixture for alternate transport tests.
func NewLifecycleHandler(publicCard *a2a.AgentCard) (a2asrv.RequestHandler, func()) {
	extended := *publicCard
	extended.Description = "Extended static lifecycle fixture card"
	gate := make(chan struct{})
	exec := newExecutor()
	exec.gate = gate
	handler := a2asrv.NewHandler(exec, a2asrv.WithCapabilityChecks(&publicCard.Capabilities), a2asrv.WithExtendedAgentCard(&extended), a2asrv.WithCallInterceptors(&fixtureAuth{}))
	return handler, sync.OnceFunc(func() { close(gate) })
}
