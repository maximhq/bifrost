package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	a2agrpc "github.com/a2aproject/a2a-go/v2/a2agrpc/v1"
	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const agentCardPath = "/custom/cards/fixture.json"

// gatewayCard is a test convenience wrapping gatewayCardWithExtensions with no
// allowed extensions and no gRPC URL.
func gatewayCard(upstream *a2a.AgentCard, externalURL, id string, policy schemas.AgentGatewayAuthPolicy, pushSupported bool) *a2a.AgentCard {
	card, _ := gatewayCardWithExtensions(upstream, externalURL, id, policy, pushSupported, nil, "")
	return card
}

// newClientGeneration is a test convenience creating a live generation with a
// single candidate.
func newClientGeneration(card *a2a.AgentCard, client sdkClient, transport string) *clientGeneration {
	return newCandidateGeneration(card, []upstreamCandidate{{client: client, transport: transport}})
}

func TestNormalizeAgentCardSecurityJSON(t *testing.T) {
	body := []byte(`{
		"name":"fixture",
		"securitySchemes":{"bearer":{"type":"http","scheme":"bearer"}},
		"security":[{"bearer":[]}]
	}`)

	normalized, err := normalizeAgentCardSecurityJSON(body)
	require.NoError(t, err)

	var card a2a.AgentCard
	require.NoError(t, json.Unmarshal(normalized, &card))
	require.Equal(t, a2a.HTTPAuthSecurityScheme{Scheme: "bearer"}, card.SecuritySchemes["bearer"])
	require.Equal(t, a2a.SecurityRequirementsOptions{{"bearer": {}}}, card.SecurityRequirements)
}

func TestInspectCardIncludesAdvertisedMetadata(t *testing.T) {
	card := &a2a.AgentCard{
		Name:                "metadata-agent",
		Description:         "does useful work",
		Version:             "2.1.0",
		Provider:            &a2a.AgentProvider{Org: "Example Corp", URL: "https://example.com"},
		DocumentationURL:    "https://example.com/docs",
		IconURL:             "https://example.com/icon.png",
		SupportedInterfaces: []*a2a.AgentInterface{{URL: "https://example.com/a2a", ProtocolBinding: a2a.TransportProtocolJSONRPC, ProtocolVersion: a2a.Version, Tenant: "tenant-1"}},
		Capabilities: a2a.AgentCapabilities{
			Streaming:         true,
			PushNotifications: true,
			Extensions:        []a2a.AgentExtension{{URI: "https://example.com/extensions/audit", Required: true}},
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"application/json"},
		Skills: []a2a.AgentSkill{{
			ID:                   "search",
			Name:                 "Search",
			Description:          "Search records",
			Tags:                 []string{"search", "records"},
			Examples:             []string{"Find recent records"},
			InputModes:           []string{"text/plain"},
			OutputModes:          []string{"application/json"},
			SecurityRequirements: a2a.SecurityRequirementsOptions{{"bearer": {}}},
		}},
		Signatures: []a2a.AgentCardSignature{{Signature: "signature"}},
	}

	result := inspectCard(card, map[string]bool{"streaming": true, "pushNotifications": true, "futureCapability": false})
	require.Equal(t, "metadata-agent", result.Name)
	require.Equal(t, "2.1.0", result.Version)
	require.Equal(t, "Example Corp", result.Provider)
	require.Equal(t, "https://example.com", result.ProviderURL)
	require.Equal(t, "https://example.com/docs", result.DocumentationURL)
	require.Equal(t, "https://example.com/icon.png", result.IconURL)
	require.Equal(t, map[string]bool{"streaming": true, "pushNotifications": true, "futureCapability": false}, result.Capabilities)
	require.Equal(t, 1, result.SignatureCount)
	require.Equal(t, []string{"text/plain"}, result.DefaultInputModes)
	require.Equal(t, []string{"application/json"}, result.DefaultOutputModes)
	require.Equal(t, []InspectedAgentInterface{{URL: "https://example.com/a2a", ProtocolBinding: "JSONRPC", ProtocolVersion: "1.0", Tenant: "tenant-1"}}, result.Interfaces)
	require.Equal(t, []InspectedAgentSkill{{
		ID:                   "search",
		Name:                 "Search",
		Description:          "Search records",
		Tags:                 []string{"search", "records"},
		Examples:             []string{"Find recent records"},
		InputModes:           []string{"text/plain"},
		OutputModes:          []string{"application/json"},
		SecurityRequirements: [][]string{{"bearer"}},
	}}, result.Skills)
	require.Equal(t, []InspectedAgentExtension{{URI: "https://example.com/extensions/audit", Required: true}}, result.Extensions)
}

func TestWrapJSONStreamErrorReframesJSONReplyAsSSE(t *testing.T) {
	newPair := func(accept, contentType, body string, status int) (*http.Request, *http.Response) {
		req := httptest.NewRequest(http.MethodPost, "http://upstream/", nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp := &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{contentType}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
		return req, resp
	}

	// A JSON reply to a streaming request becomes one SSE data frame.
	errBody := `{"jsonrpc": "2.0", "id": 1, "error": {"code": -32001, "message": "task not found"}}`
	req, resp := newPair("text/event-stream", "application/json", errBody, http.StatusOK)
	wrapJSONStreamError(req, resp)
	framed, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	require.True(t, strings.HasPrefix(string(framed), "data: {\"jsonrpc\":"), "body: %s", framed)
	require.True(t, strings.HasSuffix(string(framed), "\n\n"))
	require.Contains(t, string(framed), `"code":-32001`)

	// A real SSE reply is untouched.
	sse := "data: {\"jsonrpc\":\"2.0\"}\n\n"
	req, resp = newPair("text/event-stream", "text/event-stream", sse, http.StatusOK)
	wrapJSONStreamError(req, resp)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, sse, string(body))

	// A unary JSON call (no SSE accept) is untouched.
	req, resp = newPair("", "application/json", errBody, http.StatusOK)
	wrapJSONStreamError(req, resp)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, errBody, string(body))
}

type memoryStore struct {
	mu         sync.Mutex
	regs       map[string]schemas.AgentRegistration
	fail       bool
	failDelete bool
}

func (s *memoryStore) CreateAgentRegistration(_ context.Context, r *schemas.AgentRegistration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return context.Canceled
	}
	s.regs[r.Name] = *r
	return nil
}
func (s *memoryStore) UpdateAgentRegistration(_ context.Context, r *schemas.AgentRegistration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return context.Canceled
	}
	if _, ok := s.regs[r.Name]; !ok {
		return ErrNotFound
	}
	s.regs[r.Name] = *r
	return nil
}
func (s *memoryStore) ListAgentRegistrations(context.Context) ([]schemas.AgentRegistration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]schemas.AgentRegistration, 0, len(s.regs))
	for _, r := range s.regs {
		out = append(out, r)
	}
	return out, nil
}
func (s *memoryStore) GetAgentRegistration(_ context.Context, id string) (*schemas.AgentRegistration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.regs[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &r, nil
}
func (s *memoryStore) DeleteAgentRegistration(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failDelete {
		return context.Canceled
	}
	delete(s.regs, id)
	return nil
}

type fixtureExecutor struct {
	calls atomic.Int64
	delay <-chan struct{}
}

func (e *fixtureExecutor) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(y func(a2a.Event, error) bool) {
		e.calls.Add(1)
		if e.delay != nil {
			select {
			case <-e.delay:
			case <-ctx.Done():
				y(nil, ctx.Err())
				return
			}
		}
		y(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(ec.Message.Parts[0].Text()+"-ok")), nil)
	}
}
func (*fixtureExecutor) Cancel(context.Context, *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(y func(a2a.Event, error) bool) { y(nil, a2a.ErrUnsupportedOperation) }
}

type cardState struct {
	mu        sync.RWMutex
	card      *a2a.AgentCard
	calls     atomic.Int64
	fail      atomic.Bool
	discovery <-chan struct{}
}

func (s *cardState) get() *a2a.AgentCard { s.mu.RLock(); defer s.mu.RUnlock(); c := *s.card; return &c }
func (s *cardState) setName(name string) { s.mu.Lock(); defer s.mu.Unlock(); s.card.Name = name }

func fixture(t *testing.T, delay <-chan struct{}, inspect func(*http.Request)) (*httptest.Server, *fixtureExecutor, *cardState) {
	t.Helper()
	exec := &fixtureExecutor{delay: delay}
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	state := &cardState{card: &a2a.AgentCard{Name: "fixture", Version: "1", SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(server.URL+"/rpc", a2a.TransportProtocolJSONRPC)}, Capabilities: a2a.AgentCapabilities{}, DefaultInputModes: []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"}}}
	mux.Handle(agentCardPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.calls.Add(1)
		if state.discovery != nil {
			select {
			case <-state.discovery:
			case <-r.Context().Done():
				return
			}
		}
		if state.fail.Load() {
			http.Error(w, "discovery failed", http.StatusBadGateway)
			return
		}
		a2asrv.NewStaticAgentCardHandler(state.get()).ServeHTTP(w, r)
	}))
	rpc := a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(exec))
	mux.Handle("/rpc", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inspect != nil {
			inspect(r)
		}
		rpc.ServeHTTP(w, r)
	}))
	return server, exec, state
}

func TestNormalizeRejectsDuplicateAndEmptyVirtualKeyIDs(t *testing.T) {
	for _, test := range []struct {
		name string
		ids  []string
		want string
	}{
		{name: "duplicate", ids: []string{"vk-2", "vk-1", "vk-2"}, want: "duplicate virtual key ID: vk-2"},
		{name: "empty", ids: []string{"vk-1", ""}, want: "virtual_key_ids cannot contain an empty ID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalize(CreateRequest{Name: "fixture", AgentCardURL: "https://example.com", VirtualKeyIDs: test.ids})
			require.EqualError(t, err, test.want)
		})
	}
}

func TestAgentCardDiscoveryResponseSizeLimit(t *testing.T) {
	card := &a2a.AgentCard{Name: "fixture", Version: "1", SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("https://agent.example/rpc", a2a.TransportProtocolJSONRPC)}}
	base, err := json.Marshal(card)
	require.NoError(t, err)
	prefix := append(base[:len(base)-1], []byte(`,"padding":"`)...)
	suffix := []byte(`"}`)
	require.Less(t, len(prefix)+len(suffix), maxAgentCardResponseSize)
	exact := append(prefix, bytes.Repeat([]byte("x"), maxAgentCardResponseSize-len(prefix)-len(suffix))...)
	exact = append(exact, suffix...)
	require.Len(t, exact, maxAgentCardResponseSize)

	for _, test := range []struct {
		name    string
		body    []byte
		wantErr error
	}{
		{name: "exact_limit", body: exact},
		{name: "overflow", body: append(append([]byte(nil), exact...), ' '), wantErr: errAgentCardTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(test.body) }))
			defer server.Close()
			m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "", nil)
			require.NoError(t, err)
			defer m.Close()
			resolved, err := m.resolveCard(context.Background(), server.URL, nil, upstreamCredentialID("fixture", "discovery"))
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				require.Nil(t, resolved)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "fixture", resolved.Name)
		})
	}
}

func TestAgentCardDiscoveryRejectsRedirectWithoutLeakingCredential(t *testing.T) {
	var targetCalls atomic.Int64
	var targetAuthorization atomic.Value
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		targetAuthorization.Store(r.Header.Get("Authorization"))
		http.Error(w, "must not be reached", http.StatusInternalServerError)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer discovery-secret", r.Header.Get("Authorization"))
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()

	configuredRedirectCalled := atomic.Bool{}
	configuredClient := &http.Client{
		Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			configuredRedirectCalled.Store(true)
			return nil
		},
	}
	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "", configuredClient)
	require.NoError(t, err)
	defer m.Close()
	_, err = m.resolveCard(context.Background(), redirect.URL, &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeHeaders, Headers: map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar("Bearer discovery-secret")}}, upstreamCredentialID("fixture", "discovery"))
	require.ErrorContains(t, err, "302 Found")
	require.False(t, configuredRedirectCalled.Load(), "discovery must override redirect following")
	require.Zero(t, targetCalls.Load())
	require.Nil(t, targetAuthorization.Load())
	require.Equal(t, 3*time.Second, configuredClient.Timeout, "configured client settings remain intact")
}

func TestValidateStrictV1Interfaces(t *testing.T) {
	valid := func() *a2a.AgentInterface {
		return a2a.NewAgentInterface("https://agent.example/rpc", a2a.TransportProtocolJSONRPC)
	}
	tests := []struct {
		name         string
		agentCardURL string
		interfaces   []*a2a.AgentInterface
		wantErr      bool
	}{
		{name: "valid", interfaces: []*a2a.AgentInterface{valid()}},
		{name: "valid_rest_only", interfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("https://agent.example/rest", a2a.TransportProtocolHTTPJSON)}},
		{name: "valid_grpc_only", interfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("agent.example:9090", a2a.TransportProtocolGRPC)}},
		{name: "valid_grpc_scheme", interfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("https://agent.example:9090", a2a.TransportProtocolGRPC)}},
		{name: "https_card_rejects_plaintext_http", agentCardURL: "https://agent.example/card", interfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("http://agent.example/rpc", a2a.TransportProtocolJSONRPC)}, wantErr: true},
		{name: "https_card_rejects_plaintext_grpc", agentCardURL: "https://agent.example/card", interfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("http://agent.example:9090", a2a.TransportProtocolGRPC)}, wantErr: true},
		{name: "https_card_allows_loopback_http", agentCardURL: "https://agent.example/card", interfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("http://127.0.0.1:8080/rpc", a2a.TransportProtocolJSONRPC)}},
		{name: "https_card_allows_loopback_grpc", agentCardURL: "https://agent.example/card", interfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("http://[::1]:9090", a2a.TransportProtocolGRPC)}},
		{name: "valid_after_unsupported", interfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("https://agent.example/rest", a2a.TransportProtocol("UNSUPPORTED")), valid()}},
		{name: "legacy_top_level_only", wantErr: true},
		{name: "nil_interface", interfaces: []*a2a.AgentInterface{nil}, wantErr: true},
		{name: "missing_version", interfaces: []*a2a.AgentInterface{{URL: "https://agent.example/rpc", ProtocolBinding: a2a.TransportProtocolJSONRPC}}, wantErr: true},
		{name: "wrong_version", interfaces: []*a2a.AgentInterface{{URL: "https://agent.example/rpc", ProtocolBinding: a2a.TransportProtocolJSONRPC, ProtocolVersion: a2a.ProtocolVersion("0.3")}}, wantErr: true},
		{name: "missing_binding", interfaces: []*a2a.AgentInterface{{URL: "https://agent.example/rpc", ProtocolVersion: a2a.Version}}, wantErr: true},
		{name: "unsupported_binding", interfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("https://agent.example/rest", a2a.TransportProtocol("UNSUPPORTED"))}, wantErr: true},
		{name: "grpc_missing_port", interfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("agent.example", a2a.TransportProtocolGRPC)}, wantErr: true},
		{name: "grpc_with_path", interfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("agent.example:9090/rpc", a2a.TransportProtocolGRPC)}, wantErr: true},
		{name: "grpc_with_userinfo", interfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("secret@agent.example:9090", a2a.TransportProtocolGRPC)}, wantErr: true},
		{name: "grpc_bad_scheme", interfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("unix://agent.sock", a2a.TransportProtocolGRPC)}, wantErr: true},
		{name: "missing_url", interfaces: []*a2a.AgentInterface{{ProtocolBinding: a2a.TransportProtocolJSONRPC, ProtocolVersion: a2a.Version}}, wantErr: true},
		{name: "relative_url", interfaces: []*a2a.AgentInterface{{URL: "/rpc", ProtocolBinding: a2a.TransportProtocolJSONRPC, ProtocolVersion: a2a.Version}}, wantErr: true},
		{name: "malformed_url", interfaces: []*a2a.AgentInterface{{URL: "://bad", ProtocolBinding: a2a.TransportProtocolJSONRPC, ProtocolVersion: a2a.Version}}, wantErr: true},
		{name: "userinfo_url", interfaces: []*a2a.AgentInterface{{URL: "https://secret@agent.example/rpc", ProtocolBinding: a2a.TransportProtocolJSONRPC, ProtocolVersion: a2a.Version}}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateStrictV1Interfaces(&a2a.AgentCard{SupportedInterfaces: test.interfaces}, test.agentCardURL)
			if test.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBuildGenerationFiltersInvalidInterfaces(t *testing.T) {
	upstream, _, state := fixture(t, nil, nil)
	defer upstream.Close()
	valid := a2a.NewAgentInterface(upstream.URL+"/rpc", a2a.TransportProtocolJSONRPC)
	state.mu.Lock()
	state.card.SupportedInterfaces = []*a2a.AgentInterface{
		nil,
		valid,
		{URL: "https://secret@agent.example/rpc", ProtocolBinding: a2a.TransportProtocolJSONRPC, ProtocolVersion: a2a.Version},
	}
	state.mu.Unlock()

	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "", nil)
	require.NoError(t, err)
	defer m.Close()
	generation, err := m.buildGeneration(context.Background(), runtimeConfig{name: "fixture", agentCardURL: upstream.URL + agentCardPath})
	require.NoError(t, err)
	require.Len(t, generation.candidates, 1)
	require.Equal(t, string(a2a.TransportProtocolJSONRPC), generation.candidates[0].transport)
	generation.retire()
}

func TestValidateAuthCardMatchRequiresOneCompleteRequirement(t *testing.T) {
	card := &a2a.AgentCard{
		SecuritySchemes: a2a.NamedSecuritySchemes{
			"apiKey": a2a.APIKeySecurityScheme{Name: "x-api-key", Location: a2a.APIKeySecuritySchemeLocationHeader},
			"bearer": a2a.HTTPAuthSecurityScheme{Scheme: "bearer"},
			"oauth":  a2a.OAuth2SecurityScheme{},
		},
		SecurityRequirements: a2a.SecurityRequirementsOptions{
			{"apiKey": {}, "bearer": {}},
			{"oauth": {}},
		},
	}

	require.NoError(t, validateAuthCardMatch(card, &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeHeaders, SecuritySchemes: []string{"bearer", "apiKey"}}))
	require.NoError(t, validateAuthCardMatch(card, &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth, SecuritySchemes: []string{"oauth"}}))
	require.ErrorContains(t, validateAuthCardMatch(card, &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeHeaders, SecuritySchemes: []string{"apiKey"}}), "one complete")
	require.ErrorContains(t, validateAuthCardMatch(card, &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeHeaders, SecuritySchemes: []string{"apiKey", "missing"}}), "not declared")
}

func TestAgentOAuthTokensAreScopedByAgentAndPhase(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		require.Equal(t, "client_credentials", r.Form.Get("grant_type"))
		require.Equal(t, "client", r.Form.Get("client_id"))
		call := calls.Add(1)
		if call < 4 {
			require.Equal(t, "secret", r.Form.Get("client_secret"))
		} else {
			require.Equal(t, "changed-secret", r.Form.Get("client_secret"))
		}
		require.Equal(t, "read write", r.Form.Get("scope"))
		require.Equal(t, "agent-api", r.Form.Get("resource"))
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("token-%d", call), "expires_in": 3600})
	}))
	defer server.Close()

	m := &Manager{httpClient: server.Client(), oauthTokens: make(map[string]oauthToken)}
	auth := &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth, OAuth: &schemas.UpstreamOAuthConfig{
		TokenURL: server.URL, ClientID: schemas.NewSecretVar("client"), ClientSecret: schemas.NewSecretVar("secret"),
		Scopes: []string{"read", "write"}, Resource: "agent-api",
	}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	discovery, err := m.resolveAuthHeaders(ctx, auth, upstreamCredentialID("agent-one", "discovery"))
	require.NoError(t, err)
	require.Equal(t, "Bearer token-1", discovery.Get("Authorization"))
	cached, err := m.resolveAuthHeaders(ctx, auth, upstreamCredentialID("agent-one", "discovery"))
	require.NoError(t, err)
	require.Equal(t, "Bearer token-1", cached.Get("Authorization"))
	runtime, err := m.resolveAuthHeaders(ctx, auth, upstreamCredentialID("agent-one", "runtime"))
	require.NoError(t, err)
	require.Equal(t, "Bearer token-2", runtime.Get("Authorization"))
	otherAgent, err := m.resolveAuthHeaders(ctx, auth, upstreamCredentialID("agent-two", "runtime"))
	require.NoError(t, err)
	require.Equal(t, "Bearer token-3", otherAgent.Get("Authorization"))
	require.Equal(t, int64(3), calls.Load())

	auth.OAuth.ClientSecret.Val = "changed-secret"
	changed, err := m.resolveAuthHeaders(ctx, auth, upstreamCredentialID("agent-one", "discovery"))
	require.NoError(t, err)
	require.Equal(t, "Bearer token-4", changed.Get("Authorization"))
	require.Equal(t, int64(4), calls.Load())
}

func TestAgentOAuthFetchesAreScopedAndCancellable(t *testing.T) {
	var slowCalls, sharedCalls atomic.Int64
	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		switch r.Form.Get("client_id") {
		case "slow":
			slowCalls.Add(1)
			select {
			case <-slowStarted:
			default:
				close(slowStarted)
			}
			<-releaseSlow
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "slow-token", "expires_in": 3600})
		case "fast":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fast-token", "expires_in": 3600})
		case "shared":
			sharedCalls.Add(1)
			<-releaseSlow
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "shared-token", "expires_in": 3600})
		default:
			http.Error(w, "unexpected client", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	m := &Manager{httpClient: server.Client(), oauthTokens: make(map[string]oauthToken)}
	auth := func(clientID string) *schemas.UpstreamAuth {
		return &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth, OAuth: &schemas.UpstreamOAuthConfig{
			TokenURL: server.URL, ClientID: schemas.NewSecretVar(clientID), ClientSecret: schemas.NewSecretVar("secret"),
		}}
	}

	slowResult := make(chan error, 1)
	go func() {
		_, err := m.oauthAccessToken(context.Background(), "slow", auth("slow"))
		slowResult <- err
	}()
	<-slowStarted

	fastToken, err := m.oauthAccessToken(context.Background(), "fast", auth("fast"))
	require.NoError(t, err)
	require.Equal(t, "fast-token", fastToken)

	ctx, cancel := context.WithCancel(context.Background())
	cancelledResult := make(chan error, 1)
	go func() {
		_, err := m.oauthAccessToken(ctx, "shared", auth("shared"))
		cancelledResult <- err
	}()
	require.Eventually(t, func() bool { return sharedCalls.Load() == 1 }, time.Second, time.Millisecond)

	waitingResult := make(chan error, 1)
	go func() {
		token, err := m.oauthAccessToken(context.Background(), "shared", auth("shared"))
		if err == nil && token != "shared-token" {
			err = fmt.Errorf("unexpected token %q", token)
		}
		waitingResult <- err
	}()
	cancel()
	require.ErrorIs(t, <-cancelledResult, context.Canceled)
	close(releaseSlow)
	require.NoError(t, <-slowResult)
	require.NoError(t, <-waitingResult)
	require.Equal(t, int64(1), slowCalls.Load())
	require.Equal(t, int64(1), sharedCalls.Load())
}

func TestOAuthDiscoveryRejectsCrossOriginTokenEndpoint(t *testing.T) {
	var tokenCalls atomic.Int64
	tokenServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { tokenCalls.Add(1) }))
	defer tokenServer.Close()
	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"token_endpoint": tokenServer.URL})
	}))
	defer discoveryServer.Close()

	m := &Manager{httpClient: discoveryServer.Client(), oauthTokens: make(map[string]oauthToken)}
	auth := &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth, OAuth: &schemas.UpstreamOAuthConfig{
		DiscoveryURL: discoveryServer.URL, ClientID: schemas.NewSecretVar("client"), ClientSecret: schemas.NewSecretVar("secret"),
	}}
	_, err := m.oauthAccessToken(context.Background(), "credential", auth)
	require.ErrorContains(t, err, "must use the discovery document origin")
	require.Zero(t, tokenCalls.Load())
}

func TestRuntimeRedirectDoesNotLeakManagedHeadersAcrossOrigins(t *testing.T) {
	var authorization, apiKey atomic.Value
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization.Store(r.Header.Get("Authorization"))
		apiKey.Store(r.Header.Get("X-API-Key"))
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	m := &Manager{httpClient: redirect.Client()}
	client, err := m.clientFor(nil, http.Header{"Authorization": {"Bearer secret"}, "X-API-Key": {"api-secret"}}, true)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodGet, redirect.URL, nil)
	require.NoError(t, err)
	_, err = client.Do(req)
	require.ErrorContains(t, err, "must remain on the original origin")
	require.Nil(t, authorization.Load())
	require.Nil(t, apiKey.Load())
}

func TestValidateUpstreamAuthRejectsUnsupportedAndIncompleteOAuth(t *testing.T) {
	for _, authType := range []schemas.MCPAuthType{
		schemas.MCPAuthTypePerUserOauth,
		schemas.MCPAuthTypePerUserHeaders,
		schemas.MCPAuthTypeTokenExchange,
	} {
		t.Run(string(authType), func(t *testing.T) {
			require.ErrorContains(t, validateUpstreamAuth(&schemas.UpstreamAuth{Type: authType}), "supported types are none, headers, and oauth")
		})
	}
	require.ErrorContains(t, validateUpstreamAuth(&schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth}), "inline client_id and client_secret")
	require.ErrorContains(t, validateUpstreamAuth(&schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth, OAuth: &schemas.UpstreamOAuthConfig{
		ClientID: schemas.NewSecretVar("client"), ClientSecret: schemas.NewSecretVar("secret"),
	}}), "requires token_url, discovery_url, or provider_url")
}

func TestValidateUpstreamAuthAllowsManagedBifrostKeyButRejectsOtherBlockedHeaders(t *testing.T) {
	require.NoError(t, validateUpstreamAuth(&schemas.UpstreamAuth{
		Type: schemas.MCPAuthTypeHeaders,
		Headers: map[string]schemas.SecretVar{
			"x-bf-vk": *schemas.NewSecretVar("configured-key"),
		},
	}))
	require.ErrorContains(t, validateUpstreamAuth(&schemas.UpstreamAuth{
		Type: schemas.MCPAuthTypeHeaders,
		Headers: map[string]schemas.SecretVar{
			"Cookie": *schemas.NewSecretVar("blocked"),
		},
	}), "forwardable")
}

func TestClientForRejectsTLSConfigWithCustomTransport(t *testing.T) {
	customTransport := &headerTransport{base: http.DefaultTransport}
	m := &Manager{httpClient: &http.Client{Transport: customTransport}}

	_, err := m.clientFor(&schemas.UpstreamAuth{TLS: &schemas.UpstreamTLSConfig{}}, nil, false)
	require.ErrorContains(t, err, "requires an *http.Transport")

	client, err := m.clientFor(nil, nil, false)
	require.NoError(t, err)
	require.Same(t, customTransport, client.Transport.(*credentialTransport).base)
}

func TestValidateUpstreamAuthRejectsInvalidTLS(t *testing.T) {
	auth := &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeNone, TLS: &schemas.UpstreamTLSConfig{CACertPEM: schemas.NewSecretVar("not a certificate")}}
	require.ErrorContains(t, validateUpstreamAuth(auth), "CA certificate is invalid")

	auth.TLS = &schemas.UpstreamTLSConfig{ClientCertPEM: schemas.NewSecretVar("certificate without key")}
	require.ErrorContains(t, validateUpstreamAuth(auth), "certificate and key must be configured together")
}

// TestManagerForwardsToGRPCUpstream proves the full upstream gRPC path with the
// official SDK on both sides: a card advertising only a plaintext gRPC
// interface is accepted at registration, a downstream JSON-RPC message is
// forwarded over gRPC, and the gateway-owned runtime credential reaches the
// upstream as request metadata while the Bifrost credential does not.
func TestManagerForwardsToGRPCUpstream(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var gotAuthorization atomic.Value
	var gotBifrostKey atomic.Value
	var unaryCalls atomic.Int64
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			unaryCalls.Add(1)
			if md, ok := metadata.FromIncomingContext(ctx); ok {
				gotAuthorization.Store(strings.Join(md.Get("authorization"), ","))
				gotBifrostKey.Store(strings.Join(md.Get("x-bf-vk"), ","))
			}
			return handler(ctx, req)
		}),
		grpc.ChainStreamInterceptor(func(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
				gotAuthorization.Store(strings.Join(md.Get("authorization"), ","))
				gotBifrostKey.Store(strings.Join(md.Get("x-bf-vk"), ","))
			}
			return handler(srv, stream)
		}),
	)
	a2apb.RegisterA2AServiceServer(grpcServer, a2agrpc.NewHandler(a2asrv.NewHandler(&fixtureExecutor{})))
	go grpcServer.Serve(listener) //nolint:errcheck
	defer grpcServer.Stop()

	card := &a2a.AgentCard{Name: "grpc-upstream", Version: "1", SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("http://"+listener.Addr().String(), a2a.TransportProtocolGRPC)}}
	cardServer := httptest.NewServer(a2asrv.NewStaticAgentCardHandler(card))
	defer cardServer.Close()

	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	_, err = m.Create(context.Background(), CreateRequest{
		Name:         "grpc-upstream",
		AgentCardURL: cardServer.URL + a2asrv.WellKnownAgentCardPath,
		RuntimeAuth:  &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeHeaders, Headers: map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar("Bearer upstream-secret")}},
	})
	require.NoError(t, err)

	protocol, ok := m.ProtocolHandler("grpc-upstream")
	require.True(t, ok)
	proxy := httptest.NewServer(protocol)
	defer proxy.Close()
	client, err := a2aclient.NewFromEndpoints(context.Background(), []*a2a.AgentInterface{a2a.NewAgentInterface(proxy.URL, a2a.TransportProtocolJSONRPC)}, a2aclient.WithJSONRPCTransport(http.DefaultClient))
	require.NoError(t, err)
	defer client.Destroy()

	ctx := a2aclient.AttachServiceParams(context.Background(), a2aclient.ServiceParams{"x-bf-vk": {"sk-bf-secret"}})
	result, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello"))})
	require.NoError(t, err)
	require.Equal(t, "hello-ok", result.(*a2a.Message).Parts[0].Text())
	require.Equal(t, "Bearer upstream-secret", gotAuthorization.Load())
	require.Equal(t, "", gotBifrostKey.Load())
	require.Positive(t, unaryCalls.Load())

	beforeStreaming := unaryCalls.Load()
	stream := client.SendStreamingMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("stream"))})
	for event, streamErr := range stream {
		require.NoError(t, streamErr)
		require.Equal(t, "stream-ok", event.(*a2a.Message).Parts[0].Text())
	}
	require.Greater(t, unaryCalls.Load(), beforeStreaming, "the SDK carries streaming over its unary gRPC method")
	require.Equal(t, "Bearer upstream-secret", gotAuthorization.Load())
	require.Equal(t, "", gotBifrostKey.Load())
}

func TestNormalizeRequiresURLSafeName(t *testing.T) {
	for _, name := range []string{"accounting-agent", "agent1", "a2a-agent-2"} {
		t.Run("accept_"+name, func(t *testing.T) {
			reg, err := normalize(CreateRequest{Name: name, AgentCardURL: "https://example.com"})
			require.NoError(t, err)
			require.Equal(t, name, reg.Name)
		})
	}

	for _, name := range []string{"", "Accounting Agent", "accounting_agent", "Accounting-Agent", "-agent", "agent-", "agent--one", "agent/one", "agent%20one"} {
		t.Run("reject_"+name, func(t *testing.T) {
			_, err := normalize(CreateRequest{Name: name, AgentCardURL: "https://example.com"})
			require.EqualError(t, err, "name must be 1-255 lowercase ASCII letters, numbers, or single hyphens, and must start and end with a letter or number")
		})
	}
}

func TestPublicAgentCardRunsOnePipelineLifecycleAndDenialSkipsDiscovery(t *testing.T) {
	upstream, _, cardState := fixture(t, nil, nil)
	defer upstream.Close()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	_, err = m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: upstream.URL + agentCardPath})
	require.NoError(t, err)

	var order []string
	observer := &recordingA2APlugin{name: "observer", order: &order}
	pipeline := &fakePipeline{plugins: []*recordingA2APlugin{observer}}
	var acquired, released atomic.Int64
	m.SetPluginPipeline(func() PluginPipeline {
		acquired.Add(1)
		return pipeline
	}, func(PluginPipeline) { released.Add(1) })

	handler, ok := m.CardHandler("fixture", "")
	require.True(t, ok)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, int64(2), cardState.calls.Load(), "one live fetch follows registration discovery")
	require.Equal(t, []string{"pre:observer", "post:observer"}, order)
	require.Equal(t, int64(1), acquired.Load())
	require.Equal(t, int64(1), released.Load())
	require.Equal(t, schemas.A2ARequestTypeGetAgentCard, observer.request.RequestType)
	require.NotEqual(t, schemas.A2ARequestTypeGetExtendedAgentCard, observer.request.RequestType)
	require.NotNil(t, observer.response)
	require.NotNil(t, observer.response.BifrostA2AGetAgentCardResponse)
	require.NotNil(t, observer.response.ResponseBody)
	require.Contains(t, *observer.response.ResponseBody, `"name":"fixture"`)

	order = nil
	denier := &recordingA2APlugin{name: "denier", order: &order, shortCircuit: &schemas.A2APluginShortCircuit{Error: &schemas.BifrostError{StatusCode: schemas.Ptr(http.StatusForbidden), Error: &schemas.ErrorField{Message: "denied"}}}}
	m.SetPluginPipeline(func() PluginPipeline { return &fakePipeline{plugins: []*recordingA2APlugin{denier}} }, func(PluginPipeline) {})
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Equal(t, int64(2), cardState.calls.Load(), "denial must precede live upstream discovery")
	require.Equal(t, []string{"pre:denier", "post:denier"}, order)
	require.Equal(t, int64(1), denier.postCalls.Load())
}

func TestManagerPersistPublishReloadLiveCardAndProxy(t *testing.T) {
	upstream, _, cardState := fixture(t, nil, nil)
	defer upstream.Close()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	view, err := m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: upstream.URL + agentCardPath, VirtualKeyIDs: []string{"vk-1"}})
	require.NoError(t, err)
	require.Equal(t, upstream.URL+agentCardPath, view.AgentCardURL)
	encodedView, err := json.Marshal(view)
	require.NoError(t, err)
	require.Contains(t, string(encodedView), `"agent_card_url"`)
	require.NotContains(t, string(encodedView), `"base_url"`)
	require.Equal(t, int64(1), cardState.calls.Load(), "create discovers and initializes exactly once")
	// Grant associations remain persistence data; the manager does not use them
	// when constructing protocol/client runtimes.
	require.Equal(t, []string{"vk-1"}, view.VirtualKeyIDs)
	stored, err := store.GetAgentRegistration(context.Background(), view.Name)
	require.NoError(t, err)
	require.Equal(t, []string{"vk-1"}, stored.VirtualKeyIDs)
	runtime, ok := m.runtimes.load(view.Name)
	require.True(t, ok)
	require.Equal(t, runtimeConfig{name: "fixture", agentCardURL: upstream.URL + agentCardPath}, runtime.config)
	m2, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m2.Close()
	reloadedRuntime, ok := m2.runtimes.load(view.Name)
	require.True(t, ok)
	require.Equal(t, runtimeConfig{name: "fixture", agentCardURL: upstream.URL + agentCardPath}, reloadedRuntime.config)
	require.Equal(t, int64(1), cardState.calls.Load(), "startup loads registrations without network discovery")
	cardState.setName("changed-live")
	cardHandler, ok := m2.CardHandler(view.Name, "")
	require.True(t, ok)
	cardServer := httptest.NewServer(cardHandler)
	defer cardServer.Close()
	card, err := agentcard.NewResolver(http.DefaultClient).Resolve(context.Background(), cardServer.URL)
	require.NoError(t, err)
	require.Equal(t, "changed-live", card.Name)
	require.Equal(t, "x-bf-vk", card.SecuritySchemes["bifrostVirtualKey"].(a2a.APIKeySecurityScheme).Name)
	var order []string
	observer := &recordingA2APlugin{name: "payload", order: &order}
	pipeline := &fakePipeline{plugins: []*recordingA2APlugin{observer}}
	m2.SetPluginPipeline(func() PluginPipeline { return pipeline }, func(PluginPipeline) {})
	result := sendThrough(t, m2, view.Name, context.Background(), nil)
	require.Equal(t, "hello-ok", result.(*a2a.Message).Parts[0].Text())
	require.NotNil(t, observer.request)
	require.Equal(t, schemas.A2ARequestTypeSendMessage, observer.request.RequestType)
	require.NotNil(t, observer.request.RequestBody)
	require.Contains(t, *observer.request.RequestBody, "hello")
	require.NotNil(t, observer.response)
	require.NotNil(t, observer.response.ResponseBody)
	require.Contains(t, *observer.response.ResponseBody, "hello-ok")
}

func TestReloadRegistrationReplacesOnlyNamedRuntime(t *testing.T) {
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{
		"first":  {Name: "first", AgentCardURL: "http://first.example/card", Enabled: true},
		"second": {Name: "second", AgentCardURL: "http://second.example/card", Enabled: true},
	}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()

	firstBefore, ok := m.runtimes.load("first")
	require.True(t, ok)
	secondBefore, ok := m.runtimes.load("second")
	require.True(t, ok)

	store.mu.Lock()
	updated := store.regs["first"]
	updated.AllowByDefault = true
	store.regs["first"] = updated
	store.mu.Unlock()

	view, err := m.ReloadRegistration(context.Background(), "first")
	require.NoError(t, err)
	require.True(t, view.AllowByDefault)

	firstAfter, ok := m.runtimes.load("first")
	require.True(t, ok)
	require.NotSame(t, firstBefore, firstAfter)
	secondAfter, ok := m.runtimes.load("second")
	require.True(t, ok)
	require.Same(t, secondBefore, secondAfter)
}

func TestReloadRegistrationUnpublishesDisabledRuntime(t *testing.T) {
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{
		"first":  {Name: "first", AgentCardURL: "http://first.example/card", Enabled: true},
		"second": {Name: "second", AgentCardURL: "http://second.example/card", Enabled: true},
	}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()

	secondBefore, ok := m.runtimes.load("second")
	require.True(t, ok)
	store.mu.Lock()
	disabled := store.regs["first"]
	disabled.Enabled = false
	store.regs["first"] = disabled
	store.mu.Unlock()

	view, err := m.ReloadRegistration(context.Background(), "first")
	require.NoError(t, err)
	require.False(t, view.Enabled)
	_, ok = m.runtimes.load("first")
	require.False(t, ok)
	secondAfter, ok := m.runtimes.load("second")
	require.True(t, ok)
	require.Same(t, secondBefore, secondAfter)
}

func TestRemoveRuntimeRemovesOnlyNamedRuntime(t *testing.T) {
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{
		"first":  {Name: "first", AgentCardURL: "http://first.example/card", Enabled: true},
		"second": {Name: "second", AgentCardURL: "http://second.example/card", Enabled: true},
	}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()

	secondBefore, ok := m.runtimes.load("second")
	require.True(t, ok)
	m.RemoveRuntime("first")

	_, ok = m.runtimes.load("first")
	require.False(t, ok)
	secondAfter, ok := m.runtimes.load("second")
	require.True(t, ok)
	require.Same(t, secondBefore, secondAfter)
}

func sendThrough(t *testing.T, m *Manager, id string, ctx context.Context, headers http.Header) a2a.SendMessageResult {
	t.Helper()
	protocol, ok := m.ProtocolHandler(id)
	require.True(t, ok)
	proxy := httptest.NewServer(protocol)
	defer proxy.Close()
	transport := http.DefaultTransport
	if headers != nil {
		transport = &headerTransport{base: http.DefaultTransport, headers: headers}
	}
	client, err := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{a2a.NewAgentInterface(proxy.URL, a2a.TransportProtocolJSONRPC)}, a2aclient.WithJSONRPCTransport(&http.Client{Transport: transport}))
	require.NoError(t, err)
	defer client.Destroy()
	result, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello"))})
	require.NoError(t, err)
	return result
}

type headerTransport struct {
	base    http.RoundTripper
	headers http.Header
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	for k, v := range t.headers {
		clone.Header[k] = append([]string(nil), v...)
	}
	return t.base.RoundTrip(clone)
}

func TestManagementViewsFullyRedactCredentialsAndPreserveMetadata(t *testing.T) {
	t.Setenv("AGENT_RUNTIME_TOKEN", "env-runtime-secret")
	previousVaultResolver := schemas.VaultResolveHook
	schemas.VaultResolveHook = func(_ context.Context, value *string) error {
		*value = "vault-discovery-secret"
		return nil
	}
	t.Cleanup(func() { schemas.VaultResolveHook = previousVaultResolver })

	upstream, _, _ := fixture(t, nil, nil)
	defer upstream.Close()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()

	created, err := m.Create(context.Background(), CreateRequest{
		Name:         "secured",
		AgentCardURL: upstream.URL + agentCardPath,
		DiscoveryAuth: &schemas.UpstreamAuth{
			Type:    schemas.MCPAuthTypeHeaders,
			Headers: map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar("vault.agent/discovery")},
		},
		RuntimeAuth: &schemas.UpstreamAuth{
			Type:    schemas.MCPAuthTypeHeaders,
			Headers: map[string]schemas.SecretVar{"x-api-key": *schemas.NewSecretVar("env.AGENT_RUNTIME_TOKEN")},
		},
	})
	require.NoError(t, err)

	listed, err := m.List(context.Background())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	got, err := m.Get(context.Background(), created.Name)
	require.NoError(t, err)
	for _, view := range []schemas.AgentRegistrationView{created, listed[0], got} {
		discoveryHeader := view.DiscoveryAuth.Headers["Authorization"]
		runtimeHeader := view.RuntimeAuth.Headers["x-api-key"]
		require.Equal(t, schemas.MCPAuthTypeHeaders, view.DiscoveryAuth.Type)
		require.Equal(t, schemas.SecretTypeVault, discoveryHeader.Type())
		require.Equal(t, "vault.agent/discovery", discoveryHeader.GetRawRef())
		require.Equal(t, "<REDACTED>", discoveryHeader.GetValue())
		require.Equal(t, schemas.MCPAuthTypeHeaders, view.RuntimeAuth.Type)
		require.Equal(t, schemas.SecretTypeEnv, runtimeHeader.Type())
		require.Equal(t, "env.AGENT_RUNTIME_TOKEN", runtimeHeader.GetRawRef())
		require.Equal(t, "<REDACTED>", runtimeHeader.GetValue())
		encoded, marshalErr := json.Marshal(view)
		require.NoError(t, marshalErr)
		require.NotContains(t, string(encoded), "vault-discovery-secret")
		require.NotContains(t, string(encoded), "env-runtime-secret")
		require.NotContains(t, string(encoded), "auth_configured")
	}

	withoutAuth := schemas.AgentRegistration{Name: "public", AgentCardURL: upstream.URL + agentCardPath}.Redacted()
	encoded, err := json.Marshal(withoutAuth)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "discovery_auth")
	require.NotContains(t, string(encoded), "runtime_auth")

	literal := (&schemas.UpstreamAuth{Type: schemas.MCPAuthTypeHeaders, Headers: map[string]schemas.SecretVar{"x-secret": *schemas.NewSecretVar("literal-secret-value")}}).Redacted()
	encoded, err = json.Marshal(literal)
	require.NoError(t, err)
	redactedHeader := literal.Headers["x-secret"]
	require.Equal(t, "<REDACTED>", redactedHeader.GetValue())
	require.Contains(t, string(encoded), "REDACTED")
	require.NotContains(t, string(encoded), "literal-secret-value")
}

func TestManagerRestartLoadsEnabledRegistrationsWithoutUpstreamDiscovery(t *testing.T) {
	var discoveryCalls atomic.Int64
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		discoveryCalls.Add(1)
		return nil, errors.New("upstream unavailable")
	})
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{
		"available-later": {Name: "available-later", AgentCardURL: "http://available.invalid", Enabled: true, AllowByDefault: true},
		"unavailable":     {Name: "unavailable", AgentCardURL: "http://unavailable.invalid", Enabled: true, AllowByDefault: true},
		"disabled":        {Name: "disabled", AgentCardURL: "http://disabled.invalid", Enabled: false},
	}}

	m, err := NewManager(context.Background(), store, nil, "http://gateway", &http.Client{Transport: transport})
	require.NoError(t, err)
	defer m.Close()
	require.Zero(t, discoveryCalls.Load(), "startup must perform no upstream discovery")
	for _, name := range []string{"available-later", "unavailable"} {
		_, found := m.CardHandler(name, "")
		require.True(t, found)
		_, found = m.ProtocolHandler(name)
		require.True(t, found)
	}
	_, found := m.CardHandler("disabled", "")
	require.False(t, found)

	cardHandler, _ := m.CardHandler("unavailable", "")
	response := httptest.NewRecorder()
	cardHandler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusBadGateway, response.Code)
	require.Equal(t, int64(1), discoveryCalls.Load(), "the first card request performs live discovery")

	protocol, _ := m.ProtocolHandler("available-later")
	proxy := httptest.NewServer(protocol)
	defer proxy.Close()
	client, clientErr := a2aclient.NewFromEndpoints(context.Background(), []*a2a.AgentInterface{a2a.NewAgentInterface(proxy.URL, a2a.TransportProtocolJSONRPC)}, a2aclient.WithJSONRPCTransport(http.DefaultClient))
	require.NoError(t, clientErr)
	defer client.Destroy()
	_, clientErr = client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello"))})
	require.Error(t, clientErr)
	require.Equal(t, int64(2), discoveryCalls.Load(), "the first message performs live discovery")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestCreateValidatesSupportedClientBeforePersisting(t *testing.T) {
	card := &a2a.AgentCard{Name: "unsupported", Version: "1", SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("http://example.invalid/rpc", a2a.TransportProtocol("UNSUPPORTED"))}}
	upstream := httptest.NewServer(a2asrv.NewStaticAgentCardHandler(card))
	defer upstream.Close()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()

	_, err = m.Create(context.Background(), CreateRequest{Name: "unsupported", AgentCardURL: upstream.URL + agentCardPath})
	require.Error(t, err)
	require.Empty(t, store.regs)
	_, found := m.ProtocolHandler("unsupported")
	require.False(t, found)
}

func TestFailedDeleteKeepsRuntimePublished(t *testing.T) {
	upstream, _, _ := fixture(t, nil, nil)
	defer upstream.Close()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	view, err := m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: upstream.URL + agentCardPath, AllowByDefault: true})
	require.NoError(t, err)
	store.failDelete = true
	require.Error(t, m.Delete(context.Background(), view.Name))
	_, found := m.ProtocolHandler(view.Name)
	require.True(t, found)
}

func TestConcurrentProxyAndTargetedMutation(t *testing.T) {
	upstream, exec, _ := fixture(t, nil, nil)
	defer upstream.Close()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	first, err := m.Create(context.Background(), CreateRequest{Name: "first", AgentCardURL: upstream.URL + agentCardPath, AllowByDefault: true})
	require.NoError(t, err)
	second, err := m.Create(context.Background(), CreateRequest{Name: "second", AgentCardURL: upstream.URL + agentCardPath, AllowByDefault: true})
	require.NoError(t, err)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); sendThrough(t, m, first.Name, context.Background(), nil) }()
	}
	require.NoError(t, m.Delete(context.Background(), second.Name))
	_, found := m.runtimes.load(second.Name)
	require.False(t, found)
	_, found = m.runtimes.load(first.Name)
	require.True(t, found)
	wg.Wait()
	require.Equal(t, int64(20), exec.calls.Load())
}

func TestAuthCompositionAndPassthroughHeaders(t *testing.T) {
	var gotMu sync.Mutex
	var got http.Header
	upstream, _, state := fixture(t, nil, func(r *http.Request) { gotMu.Lock(); got = r.Header.Clone(); gotMu.Unlock() })
	defer upstream.Close()
	state.mu.Lock()
	state.card.SecuritySchemes = a2a.NamedSecuritySchemes{
		"upstreamKey":    a2a.APIKeySecurityScheme{Name: "x-api-key", Location: a2a.APIKeySecuritySchemeLocationHeader},
		"upstreamBearer": a2a.HTTPAuthSecurityScheme{Scheme: "Bearer"},
	}
	state.card.SecurityRequirements = a2a.SecurityRequirementsOptions{{"upstreamKey": {}}, {"upstreamBearer": {}}}
	state.mu.Unlock()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	view, err := m.Create(context.Background(), CreateRequest{Name: "secured", AgentCardURL: upstream.URL + agentCardPath, AllowByDefault: true})
	require.NoError(t, err)
	cardHandler, _ := m.CardHandler(view.Name, "")
	cardServer := httptest.NewServer(cardHandler)
	defer cardServer.Close()
	card, err := agentcard.NewResolver(http.DefaultClient).Resolve(context.Background(), cardServer.URL)
	require.NoError(t, err)
	require.Equal(t, a2a.SecurityRequirementsOptions{{"upstreamKey": make(a2a.SecuritySchemeScopes, 0)}, {"upstreamBearer": make(a2a.SecuritySchemeScopes, 0)}}, card.SecurityRequirements)
	headers := http.Header{"x-bf-vk": {"bf-secret"}, "x-api-key": {"upstream-secret"}, "Authorization": {"Bearer client-token"}, "x-client-trace": {"trace"}, "Cookie": {"private=1"}, "Proxy-Authorization": {"private"}, "Connection": {"x-hop"}, "x-hop": {"drop"}, "Upgrade": {"drop"}}
	sendThrough(t, m, view.Name, context.Background(), headers)
	gotMu.Lock()
	defer gotMu.Unlock()
	require.Equal(t, "upstream-secret", got.Get("x-api-key"))
	require.Equal(t, "Bearer client-token", got.Get("Authorization"))
	require.Equal(t, "trace", got.Get("x-client-trace"))
	require.Empty(t, got.Get("x-bf-vk"))
	require.Empty(t, got.Get("Cookie"))
	require.Empty(t, got.Get("Proxy-Authorization"))
	require.Empty(t, got.Get("Connection"))
	require.Empty(t, got.Get("Upgrade"))
}

func TestCredentialTransportPreservesManagedAcceptedCredential(t *testing.T) {
	var got http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	req, err := http.NewRequest(http.MethodPost, upstream.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer upstream-secret")
	req.Header.Set("x-bf-vk", "forwarded-key")
	client := &http.Client{Transport: &credentialTransport{base: http.DefaultTransport}}
	_, err = client.Do(req)
	require.NoError(t, err)
	require.Equal(t, "Bearer upstream-secret", got.Get("Authorization"))
	require.Equal(t, "forwarded-key", got.Get("x-bf-vk"))
}

func TestSanitizeUpstreamHeadersRemovesConnectionNominatedHeaders(t *testing.T) {
	headers := http.Header{
		"Connection":   {"x-hop"},
		"x-hop":        {"drop"},
		"x-end-to-end": {"keep"},
		"x-bf-vk":      {"private"},
	}
	got := sanitizeUpstreamHeaders(headers, nil)
	require.Empty(t, got.Get("Connection"))
	require.Empty(t, got.Get("x-hop"))
	require.Empty(t, got.Get("x-bf-vk"))
	require.Equal(t, "keep", got.Get("x-end-to-end"))
}

func TestSanitizeUpstreamHeadersKeepsA2AVersion(t *testing.T) {
	// credentialTransport sanitizes the final SDK-built request, where the only
	// A2A-Version left is the gateway's own version stamp; stripping it there
	// would make strict upstreams reject every proxied call.
	headers := http.Header{
		"A2a-Version":  {"1.0"},
		"x-end-to-end": {"keep"},
	}
	got := sanitizeUpstreamHeaders(headers, nil)
	require.Equal(t, "1.0", got.Get("A2A-Version"), "the SDK client's own version stamp must reach the upstream")
	require.Equal(t, "keep", got.Get("x-end-to-end"))
}

func TestUpstreamContextDropsTransportOwnedHeaders(t *testing.T) {
	ctx, _ := a2asrv.NewCallContext(context.Background(), a2asrv.NewServiceParams(http.Header{
		"A2a-Version":  {"1.0"},
		"Accept":       {"text/event-stream"},
		"X-End-To-End": {"keep"},
	}))
	got := upstreamServiceParams(ctx, nil)
	require.Empty(t, got.Get(a2a.SvcParamVersion), "the gateway's own client stamps the upstream protocol version")
	require.Empty(t, got.Get("Accept"), "the selected upstream transport owns content negotiation")
	require.Equal(t, "keep", got.Get("X-End-To-End"))
}

func TestAcceptedCredentialForwardingOptionAndPrecedence(t *testing.T) {
	credential := schemas.AcceptedBifrostCredential{Header: "Authorization", Value: "Bearer accepted"}
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyAcceptedCredential, credential)

	require.Empty(t, withAcceptedCredential(ctx, http.Header{}, false, false).Get("Authorization"))
	require.Equal(t, "Bearer accepted", withAcceptedCredential(ctx, http.Header{}, true, false).Get("Authorization"))
	require.Equal(t, "Bearer configured", withAcceptedCredential(ctx, http.Header{"Authorization": {"Bearer configured"}}, true, false).Get("Authorization"))
	require.Equal(t, "Bearer accepted", withAcceptedCredential(ctx, http.Header{"Authorization": {"Bearer configured"}}, true, true).Get("Authorization"))
}

func TestGatewayCardUsesAbsoluteHTTPSEndpointForStrictV1URLs(t *testing.T) {
	upstream := &a2a.AgentCard{
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface("https://upstream.example/rpc", a2a.TransportProtocolJSONRPC),
			a2a.NewAgentInterface("https://upstream.example/rest", a2a.TransportProtocolHTTPJSON),
		},
	}

	card := gatewayCard(upstream, "https://bifrost.example/", "agent-1", schemas.AgentGatewayAuthPolicy{}, false)
	require.Len(t, card.SupportedInterfaces, 2, "Agent Gateway advertises both of its own protocol bindings")
	require.Equal(t, a2a.TransportProtocolJSONRPC, card.SupportedInterfaces[0].ProtocolBinding)
	require.Equal(t, a2a.TransportProtocolHTTPJSON, card.SupportedInterfaces[1].ProtocolBinding)
	require.Equal(t, "https://bifrost.example/agents/a2a/agent-1/rest", card.SupportedInterfaces[1].URL)
	gatewayURL := card.SupportedInterfaces[0].URL
	parsed, err := url.ParseRequestURI(gatewayURL)
	require.NoError(t, err)
	require.Equal(t, "https", parsed.Scheme)
	require.Equal(t, "bifrost.example", parsed.Host)
	require.Equal(t, "/agents/a2a/agent-1/json-rpc", parsed.Path)

	encoded, err := json.Marshal(card)
	require.NoError(t, err)
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &payload))
	require.NotContains(t, payload, "url", "strict A2A v1 rejects the legacy top-level URL field")

	resolved := new(a2a.AgentCard)
	require.NoError(t, json.Unmarshal(encoded, resolved), "the transformed card remains strict-v1 SDK interoperable")
	require.Equal(t, gatewayURL, resolved.SupportedInterfaces[0].URL)
	require.Equal(t, "https://upstream.example/rpc", upstream.SupportedInterfaces[0].URL, "the upstream card remains unchanged")
}

func TestGatewayCardSecurityRequirementsFollowEnforcement(t *testing.T) {
	upstream := &a2a.AgentCard{
		SecuritySchemes: a2a.NamedSecuritySchemes{
			"upstreamKey":    a2a.APIKeySecurityScheme{Name: "x-api-key", Location: a2a.APIKeySecuritySchemeLocationHeader},
			"upstreamBearer": a2a.HTTPAuthSecurityScheme{Scheme: "Bearer"},
		},
		SecurityRequirements: a2a.SecurityRequirementsOptions{{"upstreamKey": {}}, {"upstreamBearer": {}}},
	}

	off := gatewayCard(upstream, "https://gateway.example", "agent-1", schemas.AgentGatewayAuthPolicy{}, false)
	require.Contains(t, off.SecuritySchemes, a2a.SecuritySchemeName("bifrostVirtualKey"))
	require.Equal(t, upstream.SecurityRequirements, off.SecurityRequirements)

	on := gatewayCard(upstream, "https://gateway.example", "agent-1", schemas.AgentGatewayAuthPolicy{
		EnforceAuthentication: func() bool { return true },
	}, false)
	require.Equal(t, a2a.SecurityRequirementsOptions{
		{"upstreamKey": make(a2a.SecuritySchemeScopes, 0), "bifrostVirtualKey": make(a2a.SecuritySchemeScopes, 0)},
		{"upstreamBearer": make(a2a.SecuritySchemeScopes, 0), "bifrostVirtualKey": make(a2a.SecuritySchemeScopes, 0)},
	}, on.SecurityRequirements)
	require.Equal(t, a2a.SecurityRequirementsOptions{{"upstreamKey": make(a2a.SecuritySchemeScopes, 0)}, {"upstreamBearer": make(a2a.SecuritySchemeScopes, 0)}}, upstream.SecurityRequirements)
}

func TestGatewayCardSecurityRequirementsWithoutUpstreamRequirements(t *testing.T) {
	upstream := &a2a.AgentCard{}
	off := gatewayCard(upstream, "http://gateway", "agent-1", schemas.AgentGatewayAuthPolicy{}, false)
	require.Contains(t, off.SecuritySchemes, a2a.SecuritySchemeName("bifrostVirtualKey"))
	require.Empty(t, off.SecurityRequirements)

	on := gatewayCard(upstream, "http://gateway", "agent-1", schemas.AgentGatewayAuthPolicy{
		EnforceAuthentication: func() bool { return true },
	}, false)
	require.Equal(t, a2a.SecurityRequirementsOptions{{"bifrostVirtualKey": make(a2a.SecuritySchemeScopes, 0)}}, on.SecurityRequirements)
}

func TestCardHandlerReadsCurrentAuthenticationPolicy(t *testing.T) {
	upstream, _, state := fixture(t, nil, nil)
	defer upstream.Close()
	state.mu.Lock()
	state.card.SecurityRequirements = a2a.SecurityRequirementsOptions{{"upstreamKey": {}}}
	state.card.SecuritySchemes = a2a.NamedSecuritySchemes{"upstreamKey": a2a.APIKeySecurityScheme{Name: "x-api-key", Location: a2a.APIKeySecuritySchemeLocationHeader}}
	state.mu.Unlock()

	var enforce atomic.Bool
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil, ManagerConfig{AuthPolicy: schemas.AgentGatewayAuthPolicy{
		EnforceAuthentication: enforce.Load,
	}})
	require.NoError(t, err)
	defer m.Close()
	view, err := m.Create(context.Background(), CreateRequest{Name: "dynamic-card", AgentCardURL: upstream.URL + agentCardPath})
	require.NoError(t, err)
	handler, ok := m.CardHandler(view.Name, "")
	require.True(t, ok)
	server := httptest.NewServer(handler)
	defer server.Close()

	resolve := func() *a2a.AgentCard {
		card, resolveErr := agentcard.NewResolver(http.DefaultClient).Resolve(context.Background(), server.URL)
		require.NoError(t, resolveErr)
		return card
	}
	require.Equal(t, a2a.SecurityRequirementsOptions{{"upstreamKey": make(a2a.SecuritySchemeScopes, 0)}}, resolve().SecurityRequirements)
	enforce.Store(true)
	require.Equal(t, a2a.SecurityRequirementsOptions{{"upstreamKey": make(a2a.SecuritySchemeScopes, 0), "bifrostVirtualKey": make(a2a.SecuritySchemeScopes, 0)}}, resolve().SecurityRequirements)
}

func TestManagerDoesNotPublishFailedPersistence(t *testing.T) {
	upstream, _, _ := fixture(t, nil, nil)
	defer upstream.Close()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}, fail: true}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	_, err = m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: upstream.URL + agentCardPath, AllowByDefault: true})
	require.Error(t, err)
	count := 0
	m.runtimes.entries.Range(func(_, _ any) bool { count++; return true })
	require.Zero(t, count)
}

func TestCredentialTransportSeparatesBifrostCredential(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone(); w.WriteHeader(204) }))
	defer server.Close()
	req, _ := http.NewRequest("GET", server.URL, nil)
	req.Header.Set("x-bf-vk", "secret-vk")
	req.Header.Set("Cookie", "private=1")
	req.Header.Set("x-client-trace", "trace")
	req.Header.Set("x-api-key", "caller-key")
	client := &http.Client{Transport: &credentialTransport{base: http.DefaultTransport, headers: http.Header{"Authorization": {"Bearer upstream-secret"}}}}
	_, err := client.Do(req)
	require.NoError(t, err)
	// x-bf-vk on the outbound request is always the gateway-injected accepted
	// credential (the auth middleware strips the caller's copy), so the
	// transport forwards it instead of stripping it.
	require.Equal(t, "secret-vk", got.Get("x-bf-vk"))
	require.Empty(t, got.Get("Cookie"))
	require.Equal(t, "trace", got.Get("x-client-trace"))
	require.Equal(t, "caller-key", got.Get("x-api-key"))
	require.Equal(t, "Bearer upstream-secret", got.Get("Authorization"))
}

type fakeSDKClient struct {
	send         func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error)
	stream       func(context.Context, *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error]
	extendedCard func(context.Context, *a2a.GetExtendedAgentCardRequest) (*a2a.AgentCard, error)
	destroyed    atomic.Int64
}

func (c *fakeSDKClient) SendMessage(ctx context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	return c.send(ctx, req)
}
func (c *fakeSDKClient) GetTask(context.Context, *a2a.GetTaskRequest) (*a2a.Task, error) {
	return nil, a2a.ErrUnsupportedOperation
}
func (c *fakeSDKClient) ListTasks(context.Context, *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	return nil, a2a.ErrUnsupportedOperation
}
func (c *fakeSDKClient) CancelTask(context.Context, *a2a.CancelTaskRequest) (*a2a.Task, error) {
	return nil, a2a.ErrUnsupportedOperation
}
func (c *fakeSDKClient) SendStreamingMessage(ctx context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	if c.stream != nil {
		return c.stream(ctx, req)
	}
	return func(y func(a2a.Event, error) bool) { y(nil, a2a.ErrUnsupportedOperation) }
}
func (c *fakeSDKClient) SubscribeToTask(context.Context, *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	return func(y func(a2a.Event, error) bool) { y(nil, a2a.ErrUnsupportedOperation) }
}
func (c *fakeSDKClient) GetExtendedAgentCard(ctx context.Context, req *a2a.GetExtendedAgentCardRequest) (*a2a.AgentCard, error) {
	if c.extendedCard != nil {
		return c.extendedCard(ctx, req)
	}
	return nil, a2a.ErrExtendedCardNotConfigured
}
func (c *fakeSDKClient) CreateTaskPushConfig(context.Context, *a2a.PushConfig) (*a2a.PushConfig, error) {
	return nil, a2a.ErrUnsupportedOperation
}
func (c *fakeSDKClient) DeleteTaskPushConfig(context.Context, *a2a.DeleteTaskPushConfigRequest) error {
	return a2a.ErrUnsupportedOperation
}
func (c *fakeSDKClient) Destroy() error { c.destroyed.Add(1); return nil }

func failoverHandler(t *testing.T, clients ...sdkClient) (*Manager, *proxyRequestHandler, *clientGeneration) {
	t.Helper()
	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "http://gateway", nil)
	require.NoError(t, err)
	runtime := m.buildRuntime(schemas.AgentRegistration{Name: "fixture", Enabled: true})
	candidates := make([]upstreamCandidate, len(clients))
	for i, client := range clients {
		candidates[i] = upstreamCandidate{client: client, transport: string(a2a.TransportProtocolJSONRPC)}
	}
	generation := newCandidateGeneration(&a2a.AgentCard{Name: "fixture"}, candidates)
	require.NoError(t, runtime.replaceGeneration(generation))
	m.runtimes.store("fixture", runtime)
	t.Cleanup(m.Close)
	return m, &proxyRequestHandler{runtime: runtime, manager: m, config: runtime.config}, generation
}

func dialFailure() error {
	return &url.Error{Op: "Post", URL: "http://upstream", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}
}

// A traced unary forward must open internal a2a.prepare (client acquire +
// upstream context build) and a2a.encode (describe + payload re-encode) phase
// spans so the overhead breakdown attributes gateway work to named buckets.
func TestUnaryForwardEmitsPhaseSpans(t *testing.T) {
	client := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		return a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("ok")), nil
	}}
	_, handler, _ := failoverHandler(t, client)
	tracer := &spanRecordingTracer{NoOpTracer: &schemas.NoOpTracer{}}
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTracer, schemas.Tracer(tracer))

	result, err := handler.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)})
	require.NoError(t, err)
	require.NotNil(t, result)

	byName := map[string]*recordedSpan{}
	for _, span := range tracer.spans {
		byName[span.name] = span
	}
	for _, name := range []string{"a2a.prepare", "a2a.encode"} {
		span := byName[name]
		require.NotNil(t, span, "missing %s span", name)
		require.Equal(t, schemas.SpanKindInternal, span.kind)
		require.True(t, span.ended)
		require.Equal(t, schemas.SpanStatusOk, span.status)
	}
}

func TestUnaryFailoverRetriesOnlyProvenNoDeliveryAndKeepsCandidateOrder(t *testing.T) {
	var calls []string
	first := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		calls = append(calls, "first")
		return nil, dialFailure()
	}}
	second := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		calls = append(calls, "second")
		return a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("ok")), nil
	}}
	_, handler, generation := failoverHandler(t, first, second)

	result, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, []string{"first", "second"}, calls)
	require.Equal(t, 1, generation.active)
}

func TestUnaryCallerContextErrorsDoNotAdvanceCandidate(t *testing.T) {
	for _, contextErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(contextErr.Error(), func(t *testing.T) {
			first := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
				return nil, &url.Error{Op: "Post", URL: "http://upstream", Err: contextErr}
			}}
			second := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
				return a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("unexpected")), nil
			}}
			_, handler, generation := failoverHandler(t, first, second)

			_, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)})
			require.ErrorIs(t, err, contextErr)
			require.Equal(t, 0, generation.active, "caller context errors must not switch future requests to another candidate")
		})
	}
}

func TestUnaryFailoverWrapsBetweenRequestsButAttemptsEachCandidateOnce(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	firstHealthy := atomic.Bool{}
	first := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		firstCalls.Add(1)
		if firstHealthy.Load() {
			return a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("recovered")), nil
		}
		return nil, dialFailure()
	}}
	second := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		secondCalls.Add(1)
		return nil, &upstreamHTTPStatusError{StatusCode: http.StatusGone, Status: http.StatusText(http.StatusGone)}
	}}
	_, handler, generation := failoverHandler(t, first, second)

	_, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)})
	require.Error(t, err)
	require.Equal(t, int64(1), firstCalls.Load())
	require.Equal(t, int64(1), secondCalls.Load())
	require.Equal(t, 0, generation.active, "exhausting the list wraps the next request to the first candidate")

	firstHealthy.Store(true)
	result, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, int64(2), firstCalls.Load())
	require.Equal(t, int64(1), secondCalls.Load())
	require.Equal(t, 0, generation.active)
}

func TestUnaryStaleHTTPStatusAdvancesAndReplays(t *testing.T) {
	for _, statusCode := range []int{http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusGone, http.StatusUpgradeRequired} {
		t.Run(strconv.Itoa(statusCode), func(t *testing.T) {
			var firstCalls, secondCalls atomic.Int64
			first := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
				firstCalls.Add(1)
				return nil, &upstreamHTTPStatusError{StatusCode: statusCode, Status: http.StatusText(statusCode)}
			}}
			second := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
				secondCalls.Add(1)
				return a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("ok")), nil
			}}
			_, handler, generation := failoverHandler(t, first, second)

			result, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)})
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, int64(1), firstCalls.Load())
			require.Equal(t, int64(1), secondCalls.Load())
			require.Equal(t, 1, generation.active)
		})
	}
}

func TestUnaryGRPCUnimplementedAdvancesAndReplays(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	first := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		firstCalls.Add(1)
		return nil, status.Error(codes.Unimplemented, "wrong endpoint")
	}}
	second := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		secondCalls.Add(1)
		return a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("ok")), nil
	}}
	_, handler, generation := failoverHandler(t, first, second)

	result, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, int64(1), firstCalls.Load())
	require.Equal(t, int64(1), secondCalls.Load())
	require.Equal(t, 1, generation.active)
}

func TestUnaryMethodNotFoundAdvancesAndReplays(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	first := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		firstCalls.Add(1)
		return nil, fmt.Errorf("SDK translated gRPC failure: %w", a2a.ErrMethodNotFound)
	}}
	second := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		secondCalls.Add(1)
		return a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("ok")), nil
	}}
	_, handler, generation := failoverHandler(t, first, second)

	result, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, int64(1), firstCalls.Load())
	require.Equal(t, int64(1), secondCalls.Load())
	require.Equal(t, 1, generation.active)
}

func TestUnaryAmbiguousFailureAdvancesWithoutReplay(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	first := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		firstCalls.Add(1)
		return nil, io.ErrUnexpectedEOF
	}}
	second := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		secondCalls.Add(1)
		return a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("ok")), nil
	}}
	_, handler, _ := failoverHandler(t, first, second)

	_, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)})
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Equal(t, int64(1), firstCalls.Load())
	require.Zero(t, secondCalls.Load(), "ambiguous failure must not replay the current operation")

	_, err = handler.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)})
	require.NoError(t, err)
	require.Equal(t, int64(1), firstCalls.Load())
	require.Equal(t, int64(1), secondCalls.Load())
}

func TestStreamFailureBeforeFirstEventNeverReplaysAndMovesFutureTraffic(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	first := &fakeSDKClient{stream: func(context.Context, *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		firstCalls.Add(1)
		return func(yield func(a2a.Event, error) bool) {
			yield(nil, &upstreamHTTPStatusError{StatusCode: http.StatusGone, Status: http.StatusText(http.StatusGone)})
		}
	}}
	second := &fakeSDKClient{stream: func(context.Context, *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		secondCalls.Add(1)
		return func(yield func(a2a.Event, error) bool) {
			yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("ok")), nil)
		}
	}}
	_, handler, _ := failoverHandler(t, first, second)

	var streamErr error
	for _, err := range handler.SendStreamingMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)}) {
		streamErr = err
	}
	require.Error(t, streamErr)
	require.Equal(t, int64(1), firstCalls.Load())
	require.Zero(t, secondCalls.Load(), "a stream must not replay before its first event")

	for range handler.SendStreamingMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)}) {
	}
	require.Equal(t, int64(1), firstCalls.Load())
	require.Equal(t, int64(1), secondCalls.Load())
}

func TestStreamFailureNeverReplaysAndMovesFutureTraffic(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	first := &fakeSDKClient{stream: func(context.Context, *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		firstCalls.Add(1)
		return func(yield func(a2a.Event, error) bool) {
			if !yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("partial")), nil) {
				return
			}
			yield(nil, io.ErrUnexpectedEOF)
		}
	}}
	second := &fakeSDKClient{stream: func(context.Context, *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		secondCalls.Add(1)
		return func(yield func(a2a.Event, error) bool) {
			yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("ok")), nil)
		}
	}}
	_, handler, _ := failoverHandler(t, first, second)

	var events int
	var streamErr error
	for event, err := range handler.SendStreamingMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)}) {
		if event != nil {
			events++
		}
		if err != nil {
			streamErr = err
		}
	}
	require.Equal(t, 1, events)
	require.ErrorIs(t, streamErr, io.ErrUnexpectedEOF)
	require.Equal(t, int64(1), firstCalls.Load())
	require.Zero(t, secondCalls.Load(), "a stream must not replay after yielding an event")

	for range handler.SendStreamingMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)}) {
	}
	require.Equal(t, int64(1), firstCalls.Load())
	require.Equal(t, int64(1), secondCalls.Load())
}

func TestClientGenerationReplacementDrainsOldLease(t *testing.T) {
	oldClient := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) { return nil, nil }}
	newClient := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) { return nil, nil }}
	oldGeneration := newClientGeneration(&a2a.AgentCard{Name: "old"}, oldClient, string(a2a.TransportProtocolJSONRPC))
	newGeneration := newClientGeneration(&a2a.AgentCard{Name: "new"}, newClient, string(a2a.TransportProtocolJSONRPC))
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &runtimeAgent{ctx: ctx, cancel: cancel, generation: oldGeneration}
	lease, ok := oldGeneration.acquire()
	require.True(t, ok)

	require.NoError(t, runtime.replaceGeneration(newGeneration))
	require.Zero(t, oldClient.destroyed.Load(), "old generation remains alive while leased")
	newLease, err := runtime.acquire(context.Background(), nil)
	require.NoError(t, err)
	require.Same(t, newGeneration, newLease.generation)
	newLease.release()
	lease.release()
	require.Eventually(t, func() bool { return oldClient.destroyed.Load() == 1 }, time.Second, time.Millisecond)

	runtime.close()
	require.Equal(t, int64(1), oldClient.destroyed.Load())
	require.Equal(t, int64(1), newClient.destroyed.Load())
}

func TestConcurrentFirstMessagesShareInitializationAndSteadyState(t *testing.T) {
	upstream, exec, state := fixture(t, nil, nil)
	defer upstream.Close()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{"fixture": {Name: "fixture", AgentCardURL: upstream.URL + agentCardPath, Enabled: true, AllowByDefault: true}}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	require.Zero(t, state.calls.Load())

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); sendThrough(t, m, "fixture", context.Background(), nil) }()
	}
	wg.Wait()
	require.Equal(t, int64(1), state.calls.Load())
	require.Equal(t, int64(20), exec.calls.Load())
	for range 3 {
		sendThrough(t, m, "fixture", context.Background(), nil)
	}
	require.Equal(t, int64(1), state.calls.Load(), "healthy steady state performs no discovery")
}

func TestSharedInitializationFailureThenRetrySucceeds(t *testing.T) {
	upstream, _, state := fixture(t, nil, nil)
	defer upstream.Close()
	state.fail.Store(true)
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{"fixture": {Name: "fixture", AgentCardURL: upstream.URL + agentCardPath, Enabled: true}}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	runtime, _ := m.runtimes.load("fixture")
	start := make(chan struct{})
	errs := make(chan error, 10)
	for range 10 {
		go func() {
			<-start
			_, acquireErr := runtime.acquire(context.Background(), m)
			errs <- acquireErr
		}()
	}
	close(start)
	for range 10 {
		require.Error(t, <-errs)
	}
	require.Equal(t, int64(1), state.calls.Load())
	state.fail.Store(false)
	lease, err := runtime.acquire(context.Background(), m)
	require.NoError(t, err)
	lease.release()
	require.Equal(t, int64(2), state.calls.Load())
}

func TestCanceledInitializationWaiterDoesNotCancelSharedAttempt(t *testing.T) {
	release := make(chan struct{})
	upstream, _, state := fixture(t, nil, nil)
	defer upstream.Close()
	state.discovery = release
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{"fixture": {Name: "fixture", AgentCardURL: upstream.URL + agentCardPath, Enabled: true}}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	runtime, _ := m.runtimes.load("fixture")

	canceledCtx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { _, acquireErr := runtime.acquire(canceledCtx, m); first <- acquireErr }()
	require.Eventually(t, func() bool { return state.calls.Load() == 1 }, time.Second, time.Millisecond)
	go func() {
		lease, acquireErr := runtime.acquire(context.Background(), m)
		if lease != nil {
			lease.release()
		}
		second <- acquireErr
	}()
	cancel()
	require.ErrorIs(t, <-first, context.Canceled)
	close(release)
	require.NoError(t, <-second)
	require.Equal(t, int64(1), state.calls.Load())
}

func TestLazyInitializationTimeoutAllowsRetry(t *testing.T) {
	var calls atomic.Int64
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		card := &a2a.AgentCard{
			Name:                "fixture",
			Version:             "1",
			SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(server.URL+"/rpc", a2a.TransportProtocolJSONRPC)},
			DefaultInputModes:   []string{"text/plain"},
			DefaultOutputModes:  []string{"text/plain"},
		}
		a2asrv.NewStaticAgentCardHandler(card).ServeHTTP(w, r)
	}))
	defer server.Close()

	store := &memoryStore{regs: map[string]schemas.AgentRegistration{"fixture": {Name: "fixture", AgentCardURL: server.URL, Enabled: true}}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	m.buildTimeout = 20 * time.Millisecond
	runtime, _ := m.runtimes.load("fixture")

	_, err = runtime.acquire(context.Background(), m)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	lease, err := runtime.acquire(context.Background(), m)
	require.NoError(t, err)
	lease.release()
	require.Equal(t, int64(2), calls.Load())
}

func TestRefreshTimeoutAllowsRetry(t *testing.T) {
	var calls atomic.Int64
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		card := &a2a.AgentCard{
			Name:                "refreshed",
			Version:             "1",
			SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(server.URL+"/rpc", a2a.TransportProtocolJSONRPC)},
			DefaultInputModes:   []string{"text/plain"},
			DefaultOutputModes:  []string{"text/plain"},
		}
		a2asrv.NewStaticAgentCardHandler(card).ServeHTTP(w, r)
	}))
	defer server.Close()

	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	m.buildTimeout = 20 * time.Millisecond
	runtime := m.buildRuntime(schemas.AgentRegistration{Name: "fixture", AgentCardURL: server.URL, Enabled: true})
	source := newClientGeneration(&a2a.AgentCard{Name: "stale"}, &fakeSDKClient{}, string(a2a.TransportProtocolJSONRPC))
	require.NoError(t, runtime.replaceGeneration(source))

	runtime.requestRefresh(m, source)
	require.Eventually(t, func() bool {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		return calls.Load() == 1 && !runtime.refreshing
	}, time.Second, time.Millisecond)

	runtime.mu.Lock()
	runtime.lastRefresh = time.Time{}
	runtime.mu.Unlock()
	runtime.requestRefresh(m, source)
	require.Eventually(t, func() bool {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		return calls.Load() == 2 && runtime.generation != source && !runtime.refreshing
	}, time.Second, time.Millisecond)
}

func TestCloseDuringInitializationPreventsLatePublish(t *testing.T) {
	release := make(chan struct{})
	upstream, _, state := fixture(t, nil, nil)
	defer upstream.Close()
	state.discovery = release
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{"fixture": {Name: "fixture", AgentCardURL: upstream.URL + agentCardPath, Enabled: true}}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	runtime, _ := m.runtimes.load("fixture")
	done := make(chan error, 1)
	go func() { _, acquireErr := runtime.acquire(context.Background(), m); done <- acquireErr }()
	require.Eventually(t, func() bool { return state.calls.Load() == 1 }, time.Second, time.Millisecond)
	m.Close()
	close(release)
	require.Error(t, <-done)
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	require.True(t, runtime.closed)
	require.Nil(t, runtime.generation)
}

func TestCapturedCardHandlerStopsAfterDelete(t *testing.T) {
	upstream, _, state := fixture(t, nil, nil)
	defer upstream.Close()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	_, err = m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: upstream.URL + agentCardPath})
	require.NoError(t, err)
	handler, ok := m.CardHandler("fixture", "")
	require.True(t, ok)
	require.NoError(t, m.Delete(context.Background(), "fixture"))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusNotFound, response.Code)
	require.Equal(t, int64(1), state.calls.Load(), "captured closed handler must not contact upstream")
}

func TestManagerCloseWaitsForGenerationLeaseAndDestroysOnce(t *testing.T) {
	client := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) { return nil, nil }}
	generation := newClientGeneration(&a2a.AgentCard{Name: "fixture"}, client, string(a2a.TransportProtocolJSONRPC))
	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "http://gateway", nil)
	require.NoError(t, err)
	runtime := m.buildRuntime(schemas.AgentRegistration{Name: "fixture", Enabled: true})
	require.NoError(t, runtime.replaceGeneration(generation))
	m.runtimes.store("fixture", runtime)
	lease, ok := generation.acquire()
	require.True(t, ok)

	closed := make(chan struct{})
	go func() {
		m.Close()
		close(closed)
	}()
	require.Never(t, func() bool {
		select {
		case <-closed:
			return true
		default:
			return false
		}
	}, 50*time.Millisecond, time.Millisecond, "close must wait for an in-flight generation lease")
	require.Zero(t, client.destroyed.Load())

	lease.release()
	require.Eventually(t, func() bool {
		select {
		case <-closed:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	require.Equal(t, int64(1), client.destroyed.Load())
}

func TestManagerCloseCancelsBlockedUnaryCall(t *testing.T) {
	started := make(chan struct{})
	client := &fakeSDKClient{send: func(ctx context.Context, _ *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	m, handler, _ := failoverHandler(t, client)

	callDone := make(chan error, 1)
	go func() {
		_, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)})
		callDone <- err
	}()
	<-started

	closed := make(chan struct{})
	go func() {
		m.Close()
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("manager close did not cancel the blocked unary call")
	}
	require.ErrorIs(t, <-callDone, context.Canceled)
}

func TestManagerCloseAfterUnaryClientPanic(t *testing.T) {
	client := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		panic("sdk client panic")
	}}
	m, handler, _ := failoverHandler(t, client)

	_, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)})
	require.Error(t, err)

	closed := make(chan struct{})
	go func() {
		m.Close()
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("manager close blocked on a lease leaked by the panicking unary client")
	}
	require.Equal(t, int64(1), client.destroyed.Load())
}

func TestManagerCloseIsIdempotent(t *testing.T) {
	client := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) { return nil, nil }}
	generation := newClientGeneration(&a2a.AgentCard{Name: "fixture"}, client, string(a2a.TransportProtocolJSONRPC))
	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "http://gateway", nil)
	require.NoError(t, err)
	runtime := m.buildRuntime(schemas.AgentRegistration{Name: "fixture", Enabled: true})
	require.NoError(t, runtime.replaceGeneration(generation))
	m.runtimes.store("fixture", runtime)

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() { defer wg.Done(); m.Close() }()
	}
	wg.Wait()
	require.Equal(t, int64(1), client.destroyed.Load())
}

func TestLiveCardStillDiscoversEveryRequest(t *testing.T) {
	upstream, _, state := fixture(t, nil, nil)
	defer upstream.Close()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	_, err = m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: upstream.URL + agentCardPath})
	require.NoError(t, err)
	handler, _ := m.CardHandler("fixture", "")
	for range 2 {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
		require.Equal(t, http.StatusOK, response.Code)
	}
	require.Equal(t, int64(3), state.calls.Load(), "create plus each live card request discover upstream")
}

func TestUpdateTransitionsAndRedactedSecretPreservation(t *testing.T) {
	upstream, _, _ := fixture(t, nil, nil)
	defer upstream.Close()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	_, err = m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: upstream.URL + agentCardPath, Enabled: boolPtr(true), RuntimeAuth: &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeHeaders, Headers: map[string]schemas.SecretVar{"x-api-key": *schemas.NewSecretVar("stored-secret")}}})
	require.NoError(t, err)
	base := upstream.URL + agentCardPath
	view, err := m.Update(context.Background(), "fixture", UpdateRequest{AgentCardURL: &base, Enabled: boolPtr(false), RuntimeAuth: &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeHeaders, Headers: map[string]schemas.SecretVar{"x-api-key": *schemas.NewSecretVar("<REDACTED>")}}, VirtualKeyIDs: []string{"new"}})
	require.NoError(t, err)
	require.False(t, view.Enabled)
	_, found := m.ProtocolHandler("fixture")
	require.False(t, found)
	stored, _ := store.GetAgentRegistration(context.Background(), "fixture")
	storedRuntimeHeader := stored.RuntimeAuth.Headers["x-api-key"]
	require.Equal(t, "stored-secret", storedRuntimeHeader.GetValue())
	view, err = m.Update(context.Background(), "fixture", UpdateRequest{AgentCardURL: &base, Enabled: boolPtr(true)})
	require.NoError(t, err)
	require.True(t, view.Enabled)
	_, found = m.ProtocolHandler("fixture")
	require.True(t, found)
}

func boolPtr(v bool) *bool { return &v }

// TestUpdateWritesAnOAuthSecretSwitchedToAReference pins that switching an upstream
// OAuth secret to an env reference is written: every reference used to count as
// redacted, so the old plaintext was kept, and adding OAuth with a reference failed.
func TestUpdateWritesAnOAuthSecretSwitchedToAReference(t *testing.T) {
	t.Setenv("AGENT_OAUTH_SECRET_V2", "rotated")
	upstream, _, _ := fixture(t, nil, nil)
	defer upstream.Close()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	base := upstream.URL + agentCardPath
	oauth := func(id, secret string) *schemas.UpstreamAuth {
		return &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth, OAuth: &schemas.UpstreamOAuthConfig{TokenURL: "https://idp.example.com/token", ClientID: schemas.NewSecretVar(id), ClientSecret: schemas.NewSecretVar(secret)}}
	}
	_, err = m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: base, Enabled: boolPtr(false), RuntimeAuth: oauth("client", "leaked-plain")})
	require.NoError(t, err)

	_, err = m.Update(context.Background(), "fixture", UpdateRequest{AgentCardURL: &base, Enabled: boolPtr(false), RuntimeAuth: oauth("client", "<REDACTED>")})
	require.NoError(t, err)
	stored, _ := store.GetAgentRegistration(context.Background(), "fixture")
	require.Equal(t, "leaked-plain", stored.RuntimeAuth.OAuth.ClientSecret.GetValue(), "the masked placeholder keeps the stored secret")

	_, err = m.Update(context.Background(), "fixture", UpdateRequest{AgentCardURL: &base, Enabled: boolPtr(false), RuntimeAuth: oauth("client", "env.AGENT_OAUTH_SECRET_V2")})
	require.NoError(t, err)
	stored, _ = store.GetAgentRegistration(context.Background(), "fixture")
	require.True(t, stored.RuntimeAuth.OAuth.ClientSecret.IsFromSecret(), "the switch to a reference is written")
	require.Equal(t, "rotated", stored.RuntimeAuth.OAuth.ClientSecret.GetValue())

	_, err = m.Create(context.Background(), CreateRequest{Name: "fixture2", AgentCardURL: base, Enabled: boolPtr(false)})
	require.NoError(t, err)
	_, err = m.Update(context.Background(), "fixture2", UpdateRequest{AgentCardURL: &base, Enabled: boolPtr(false), RuntimeAuth: oauth("client", "env.AGENT_OAUTH_SECRET_V2")})
	require.NoError(t, err, "adding OAuth with a reference has nothing to preserve and must be accepted")
}
func TestNormalizeRejectsIncompleteCredential(t *testing.T) {
	_, err := normalize(CreateRequest{Name: "x", AgentCardURL: "https://example.com", DiscoveryAuth: &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeHeaders}})
	require.Error(t, err)
}

func TestInboundRequestCancellationPreservesSDKExecutionLifetime(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	var calls atomic.Int64
	mux := http.NewServeMux()
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	card := &a2a.AgentCard{Name: "fixture", Version: "1", SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(upstream.URL+"/rpc", a2a.TransportProtocolJSONRPC)}}
	mux.Handle(agentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	rpc := a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(&fixtureExecutor{}))
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if calls.Load() == 1 {
			close(started)
			select {
			case <-r.Context().Done():
				close(upstreamCanceled)
			case <-release:
			}
			return
		}
		rpc.ServeHTTP(w, r)
	})

	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	_, err = m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: upstream.URL + agentCardPath})
	require.NoError(t, err)
	protocol, ok := m.ProtocolHandler("fixture")
	require.True(t, ok)
	proxy := httptest.NewServer(protocol)
	defer proxy.Close()
	client, err := a2aclient.NewFromEndpoints(context.Background(), []*a2a.AgentInterface{a2a.NewAgentInterface(proxy.URL, a2a.TransportProtocolJSONRPC)}, a2aclient.WithJSONRPCTransport(http.DefaultClient))
	require.NoError(t, err)
	defer client.Destroy()

	inboundCtx, cancelInbound := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, sendErr := client.SendMessage(inboundCtx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("block"))})
		done <- sendErr
	}()
	<-started
	cancelInbound()
	require.Error(t, <-done)
	require.Never(t, func() bool {
		select {
		case <-upstreamCanceled:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, time.Millisecond, "HTTP request cancellation must not become A2A execution cancellation")
	close(release)

	result, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("healthy"))})
	require.NoError(t, err)
	require.Equal(t, "healthy-ok", result.(*a2a.Message).Parts[0].Text())
	require.Equal(t, int64(2), calls.Load())
}

func TestUpstreamTruncatedResponseFailsWithoutPartialLeakAndOtherCallsRemainHealthy(t *testing.T) {
	var calls atomic.Int64
	mux := http.NewServeMux()
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	card := &a2a.AgentCard{Name: "fixture", Version: "1", SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(upstream.URL+"/rpc", a2a.TransportProtocolJSONRPC)}}
	mux.Handle(agentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	good := a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(&fixtureExecutor{}))
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			hijacker, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, rw, err := hijacker.Hijack()
			require.NoError(t, err)
			_, err = rw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"jsonrpc\":\"2.0\",\"result\":")
			require.NoError(t, err)
			require.NoError(t, rw.Flush())
			require.NoError(t, conn.Close())
			return
		}
		good.ServeHTTP(w, r)
	})
	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	_, err = m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: upstream.URL + agentCardPath})
	require.NoError(t, err)
	protocol, _ := m.ProtocolHandler("fixture")
	proxy := httptest.NewServer(protocol)
	defer proxy.Close()
	client, err := a2aclient.NewFromEndpoints(context.Background(), []*a2a.AgentInterface{a2a.NewAgentInterface(proxy.URL, a2a.TransportProtocolJSONRPC)}, a2aclient.WithJSONRPCTransport(http.DefaultClient))
	require.NoError(t, err)
	defer client.Destroy()
	result, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("partial-secret"))})
	require.Error(t, err)
	require.Nil(t, result, "truncated upstream bytes must not become a downstream result")
	result, err = client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("healthy"))})
	require.NoError(t, err)
	require.Equal(t, "healthy-ok", result.(*a2a.Message).Parts[0].Text())
}

func TestStaleStatusRefreshesAfterAdvancingAcrossCachedCandidates(t *testing.T) {
	var firstCalls, secondCalls, discoveries atomic.Int64
	refreshStarted := make(chan struct{})
	allowRefresh := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if discoveries.Add(1) == 1 {
			close(refreshStarted)
			<-allowRefresh
		}
		card := &a2a.AgentCard{
			Name:                "fixture",
			Version:             "1",
			SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("http://127.0.0.1/new", a2a.TransportProtocolJSONRPC)},
			DefaultInputModes:   []string{"text/plain"},
			DefaultOutputModes:  []string{"text/plain"},
		}
		a2asrv.NewStaticAgentCardHandler(card).ServeHTTP(w, r)
	}))
	defer upstream.Close()
	stale := func(calls *atomic.Int64) *fakeSDKClient {
		return &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
			calls.Add(1)
			return nil, &upstreamHTTPStatusError{StatusCode: http.StatusGone, Status: http.StatusText(http.StatusGone)}
		}}
	}
	_, handler, _ := failoverHandler(t, stale(&firstCalls), stale(&secondCalls))
	handler.runtime.mu.Lock()
	handler.runtime.config.agentCardURL = upstream.URL
	handler.runtime.mu.Unlock()

	_, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("first"))})
	require.Error(t, err)
	require.Equal(t, int64(1), firstCalls.Load())
	require.Equal(t, int64(1), secondCalls.Load())
	select {
	case <-refreshStarted:
	case <-time.After(time.Second):
		t.Fatal("Agent Card refresh did not start")
	}
	close(allowRefresh)
	require.Eventually(t, func() bool {
		handler.runtime.mu.Lock()
		defer handler.runtime.mu.Unlock()
		return discoveries.Load() == 1 && !handler.runtime.refreshing
	}, time.Second, time.Millisecond)
}

// unreachableCardURL is a card URL that always fails to connect, so tests can
// prove which operations contact the upstream agent.
const unreachableCardURL = "http://127.0.0.1:1" + agentCardPath

func TestCreateDisabledRegistrationSkipsUpstreamDiscovery(t *testing.T) {
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()

	view, err := m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: unreachableCardURL, Enabled: boolPtr(false)})
	require.NoError(t, err)
	require.False(t, view.Enabled)
	stored, err := store.GetAgentRegistration(context.Background(), "fixture")
	require.NoError(t, err)
	require.False(t, stored.Enabled)
	_, found := m.ProtocolHandler("fixture")
	require.False(t, found)
	_, found = m.CardHandler("fixture", "")
	require.False(t, found)
}

func TestDisablingRegistrationSkipsUpstreamDiscovery(t *testing.T) {
	upstream, _, _ := fixture(t, nil, nil)
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	cardURL := upstream.URL + agentCardPath
	_, err = m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: cardURL, AllowByDefault: true})
	require.NoError(t, err)
	upstream.Close()

	view, err := m.Update(context.Background(), "fixture", UpdateRequest{AgentCardURL: &cardURL, Enabled: boolPtr(false)})
	require.NoError(t, err)
	require.False(t, view.Enabled)
	_, found := m.ProtocolHandler("fixture")
	require.False(t, found)
}

func TestEnablingStillValidatesUpstream(t *testing.T) {
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	_, err = m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: unreachableCardURL, Enabled: boolPtr(false)})
	require.NoError(t, err)

	url := unreachableCardURL
	_, err = m.Update(context.Background(), "fixture", UpdateRequest{AgentCardURL: &url, Enabled: boolPtr(true)})
	require.Error(t, err)
	_, found := m.ProtocolHandler("fixture")
	require.False(t, found)
	_, err = m.Create(context.Background(), CreateRequest{Name: "other", AgentCardURL: unreachableCardURL, Enabled: boolPtr(true)})
	require.Error(t, err)
}

func TestRegistrationDiscoveryUsesBuildTimeout(t *testing.T) {
	for _, operation := range []string{"create", "update"} {
		t.Run(operation, func(t *testing.T) {
			blockDiscovery := make(chan struct{})
			upstream, _, cardState := fixture(t, nil, nil)
			defer upstream.Close()
			cardState.discovery = blockDiscovery

			store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
			m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
			require.NoError(t, err)
			defer m.Close()
			m.buildTimeout = 20 * time.Millisecond
			cardURL := upstream.URL + agentCardPath

			switch operation {
			case "create":
				_, err = m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: cardURL})
			case "update":
				_, err = m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: cardURL, Enabled: boolPtr(false)})
				require.NoError(t, err)
				_, err = m.Update(context.Background(), "fixture", UpdateRequest{AgentCardURL: &cardURL, Enabled: boolPtr(true)})
			}
			require.ErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}

func TestManagerCloseWaitsForSupersededRuntimeDrain(t *testing.T) {
	upstream, _, _ := fixture(t, nil, nil)
	defer upstream.Close()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	cardURL := upstream.URL + agentCardPath
	_, err = m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: cardURL})
	require.NoError(t, err)
	old, ok := m.runtimes.load("fixture")
	require.True(t, ok)
	client := &fakeSDKClient{send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) { return nil, nil }}
	require.NoError(t, old.replaceGeneration(newClientGeneration(&a2a.AgentCard{Name: "fixture"}, client, string(a2a.TransportProtocolJSONRPC))))
	lease, ok := old.generation.acquire()
	require.True(t, ok)

	_, err = m.Update(context.Background(), "fixture", UpdateRequest{AgentCardURL: &cardURL, Enabled: boolPtr(true)})
	require.NoError(t, err, "update must not block on the superseded runtime's drain")

	closed := make(chan struct{})
	go func() { m.Close(); close(closed) }()
	require.Never(t, func() bool {
		select {
		case <-closed:
			return true
		default:
			return false
		}
	}, 50*time.Millisecond, time.Millisecond, "close must wait for a superseded runtime still draining")
	require.Zero(t, client.destroyed.Load())

	lease.release()
	require.Eventually(t, func() bool {
		select {
		case <-closed:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	require.Equal(t, int64(1), client.destroyed.Load())
}

// taskUpstream is a full a2asrv.RequestHandler fake standing in for an upstream
// agent that owns task identity, so tests can prove the gateway forwards task
// operations upstream instead of answering them from local SDK state.
type taskUpstream struct {
	mu            sync.Mutex
	methods       []string
	extensionURIs []string
	extensionMeta map[string]any
	cancelErr     error
}

func (u *taskUpstream) record(method string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.methods = append(u.methods, method)
}

func (u *taskUpstream) calls() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.methods...)
}

func (u *taskUpstream) extensionRequest() ([]string, map[string]any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.extensionURIs), maps.Clone(u.extensionMeta)
}

func (u *taskUpstream) GetTask(_ context.Context, req *a2a.GetTaskRequest) (*a2a.Task, error) {
	u.record("GetTask")
	return &a2a.Task{ID: req.ID, ContextID: "upstream-context", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}, nil
}

func (u *taskUpstream) ListTasks(context.Context, *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	u.record("ListTasks")
	return &a2a.ListTasksResponse{Tasks: []*a2a.Task{{ID: "upstream-task", ContextID: "upstream-context", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}}, TotalSize: 1}, nil
}

func (u *taskUpstream) CancelTask(_ context.Context, req *a2a.CancelTaskRequest) (*a2a.Task, error) {
	u.record("CancelTask")
	if u.cancelErr != nil {
		return nil, u.cancelErr
	}
	return &a2a.Task{ID: req.ID, ContextID: "upstream-context", Status: a2a.TaskStatus{State: a2a.TaskStateCanceled}}, nil
}

func (u *taskUpstream) SendMessage(ctx context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	u.record("SendMessage")
	u.mu.Lock()
	if callCtx, ok := a2asrv.CallContextFrom(ctx); ok {
		u.extensionURIs = callCtx.Extensions().RequestedURIs()
	}
	u.extensionMeta = maps.Clone(req.Metadata)
	u.mu.Unlock()
	return a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("ok")), nil
}

func (u *taskUpstream) SendStreamingMessage(context.Context, *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	u.record("SendStreamingMessage")
	return func(y func(a2a.Event, error) bool) {
		y(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("stream")), nil)
	}
}

func (u *taskUpstream) SubscribeToTask(context.Context, *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	u.record("SubscribeToTask")
	return func(y func(a2a.Event, error) bool) {
		y(&a2a.Task{ID: "upstream-task", ContextID: "upstream-context", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}, nil)
	}
}

func (u *taskUpstream) GetTaskPushConfig(context.Context, *a2a.GetTaskPushConfigRequest) (*a2a.PushConfig, error) {
	return nil, a2a.ErrUnsupportedOperation
}

func (u *taskUpstream) ListTaskPushConfigs(context.Context, *a2a.ListTaskPushConfigRequest) (*a2a.ListTaskPushConfigResponse, error) {
	return nil, a2a.ErrUnsupportedOperation
}

func (u *taskUpstream) CreateTaskPushConfig(context.Context, *a2a.PushConfig) (*a2a.PushConfig, error) {
	return nil, a2a.ErrUnsupportedOperation
}

func (u *taskUpstream) DeleteTaskPushConfig(context.Context, *a2a.DeleteTaskPushConfigRequest) error {
	return a2a.ErrUnsupportedOperation
}

func (u *taskUpstream) GetExtendedAgentCard(context.Context, *a2a.GetExtendedAgentCardRequest) (*a2a.AgentCard, error) {
	u.record("GetExtendedAgentCard")
	return &a2a.AgentCard{Name: "extended"}, nil
}

// taskUpstreamGateway registers a manager in front of a task-owning upstream and
// returns a client speaking to the gateway's own protocol handler.
func taskUpstreamGateway(t *testing.T) (*taskUpstream, *a2aclient.Client) {
	upstreamHandler, client, _ := taskUpstreamGatewayManager(t)
	return upstreamHandler, client
}

// taskUpstreamGatewayManager is the same fixture, additionally exposing the
// manager so a test can reach the gateway's other protocol binding.
func taskUpstreamGatewayManager(t *testing.T) (*taskUpstream, *a2aclient.Client, *Manager) {
	return extensionGatewayManager(t, nil, nil)
}

func extensionGatewayManager(t *testing.T, extensions []a2a.AgentExtension, allowed []string) (*taskUpstream, *a2aclient.Client, *Manager) {
	t.Helper()
	upstreamHandler := &taskUpstream{}
	mux := http.NewServeMux()
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)
	card := &a2a.AgentCard{Name: "tasks", Version: "1", SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(upstream.URL+"/rpc", a2a.TransportProtocolJSONRPC)}, Capabilities: a2a.AgentCapabilities{Extensions: extensions}}
	mux.Handle(agentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(upstreamHandler))

	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "http://gateway", nil)
	require.NoError(t, err)
	t.Cleanup(m.Close)
	_, err = m.Create(context.Background(), CreateRequest{Name: "tasks", AgentCardURL: upstream.URL + agentCardPath, ExtensionURIs: allowed})
	require.NoError(t, err)
	protocol, ok := m.ProtocolHandler("tasks")
	require.True(t, ok)
	proxy := httptest.NewServer(protocol)
	t.Cleanup(proxy.Close)
	client, err := a2aclient.NewFromEndpoints(context.Background(), []*a2a.AgentInterface{a2a.NewAgentInterface(proxy.URL, a2a.TransportProtocolJSONRPC)}, a2aclient.WithJSONRPCTransport(http.DefaultClient))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Destroy() })
	return upstreamHandler, client, m
}

// restGatewayClient builds an SDK HTTP+JSON client against the gateway's REST
// binding for the same fixture, so both bindings are exercised over the wire.
func restGatewayClient(t *testing.T, m *Manager) *a2aclient.Client {
	t.Helper()
	rest, ok := m.RESTHandler("tasks")
	require.True(t, ok)
	proxy := httptest.NewServer(rest)
	t.Cleanup(proxy.Close)
	client, err := a2aclient.NewFromEndpoints(context.Background(), []*a2a.AgentInterface{a2a.NewAgentInterface(proxy.URL, a2a.TransportProtocolHTTPJSON)}, a2aclient.WithRESTTransport(http.DefaultClient))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Destroy() })
	return client
}

func TestExtensionPassthroughAndValidationAcrossBindings(t *testing.T) {
	const extensionURI = "https://example.com/extensions/trace"
	extension := a2a.AgentExtension{URI: extensionURI, Description: "trace metadata"}

	for _, test := range []struct {
		name   string
		client func(*testing.T, *Manager, *a2aclient.Client) *a2aclient.Client
	}{
		{name: "JSON-RPC", client: func(_ *testing.T, _ *Manager, client *a2aclient.Client) *a2aclient.Client { return client }},
		{name: "HTTP+JSON", client: func(t *testing.T, manager *Manager, _ *a2aclient.Client) *a2aclient.Client {
			return restGatewayClient(t, manager)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream, jsonClient, manager := extensionGatewayManager(t, []a2a.AgentExtension{extension}, []string{extensionURI})
			client := test.client(t, manager, jsonClient)
			ctx := a2aclient.AttachServiceParams(context.Background(), a2aclient.ServiceParams{a2a.SvcParamExtensions: {extensionURI}})
			request := &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi")), Metadata: map[string]any{extensionURI: map[string]any{"trace_id": "abc"}}}

			_, err := client.SendMessage(ctx, request)
			require.NoError(t, err)
			require.Equal(t, []string{"SendMessage"}, upstream.calls())
			uris, metadata := upstream.extensionRequest()
			require.Equal(t, []string{extensionURI}, uris)
			require.Equal(t, request.Metadata, metadata)
		})
	}

	t.Run("unknown requested extension is rejected before upstream", func(t *testing.T) {
		upstream, client, _ := extensionGatewayManager(t, []a2a.AgentExtension{extension}, []string{extensionURI})
		ctx := a2aclient.AttachServiceParams(context.Background(), a2aclient.ServiceParams{a2a.SvcParamExtensions: {"https://example.com/extensions/unknown"}})
		_, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi"))})
		require.Error(t, err)
		require.Empty(t, upstream.calls())
	})

	t.Run("required extension is rejected for extension-unaware client", func(t *testing.T) {
		upstream, client, _ := extensionGatewayManager(t, []a2a.AgentExtension{{URI: extensionURI, Required: true}}, []string{extensionURI})
		_, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi"))})
		require.ErrorIs(t, err, a2a.ErrExtensionSupportRequired)
		require.Empty(t, upstream.calls())
	})

	t.Run("oversized extension metadata is rejected before upstream", func(t *testing.T) {
		upstream, client, _ := extensionGatewayManager(t, []a2a.AgentExtension{extension}, []string{extensionURI})
		ctx := a2aclient.AttachServiceParams(context.Background(), a2aclient.ServiceParams{a2a.SvcParamExtensions: {extensionURI}})
		request := &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi")), Metadata: map[string]any{extensionURI: strings.Repeat("x", maxExtensionPayloadSize)}}
		_, err := client.SendMessage(ctx, request)
		require.Error(t, err)
		require.Empty(t, upstream.calls())
	})
}

func TestGatewayCardAdvertisesOnlySafeExtensions(t *testing.T) {
	const allowedURI = "https://example.com/extensions/allowed"
	upstream := &a2a.AgentCard{Capabilities: a2a.AgentCapabilities{Extensions: []a2a.AgentExtension{
		{URI: allowedURI, Params: map[string]any{"limit": 10}},
		{URI: "https://example.com/extensions/denied"},
	}}}

	card, err := gatewayCardWithExtensions(upstream, "http://gateway", "agent", schemas.AgentGatewayAuthPolicy{}, false, []string{allowedURI}, "")
	require.NoError(t, err)
	require.Equal(t, []a2a.AgentExtension{{URI: allowedURI, Params: map[string]any{"limit": 10}}}, card.Capabilities.Extensions)

	upstream.Capabilities.Extensions[1].Required = true
	_, err = gatewayCardWithExtensions(upstream, "http://gateway", "agent", schemas.AgentGatewayAuthPolicy{}, false, []string{allowedURI}, "")
	require.ErrorContains(t, err, "requires denied extension")

	upstream.Capabilities.Extensions = []a2a.AgentExtension{{URI: allowedURI, Required: true, Params: map[string]any{"payload": strings.Repeat("x", maxExtensionPayloadSize)}}}
	_, err = gatewayCardWithExtensions(upstream, "http://gateway", "agent", schemas.AgentGatewayAuthPolicy{}, false, []string{allowedURI}, "")
	require.ErrorContains(t, err, "requires untranslatable extension")
}

func TestGatewayCardAdvertisesGRPCInterfaceWhenConfigured(t *testing.T) {
	m := &Manager{grpcBaseDomain: "a2a.bifrost.example", grpcPort: 9090}
	require.Equal(t, "agent-1.a2a.bifrost.example:9090", m.grpcInterfaceURL("agent-1"))
	// Names longer than one DNS label and unconfigured gateways advertise no gRPC interface.
	require.Empty(t, m.grpcInterfaceURL(strings.Repeat("a", MaxGRPCAgentNameLength+1)))
	require.Empty(t, (&Manager{}).grpcInterfaceURL("agent-1"))

	card, err := gatewayCardWithExtensions(&a2a.AgentCard{}, "http://gateway", "agent-1", schemas.AgentGatewayAuthPolicy{}, false, nil, m.grpcInterfaceURL("agent-1"))
	require.NoError(t, err)
	require.Len(t, card.SupportedInterfaces, 3)
	require.Equal(t, a2a.TransportProtocolGRPC, card.SupportedInterfaces[2].ProtocolBinding)
	require.Equal(t, "agent-1.a2a.bifrost.example:9090", card.SupportedInterfaces[2].URL)

	unconfigured, err := gatewayCardWithExtensions(&a2a.AgentCard{}, "http://gateway", "agent-1", schemas.AgentGatewayAuthPolicy{}, false, nil, "")
	require.NoError(t, err)
	require.Len(t, unconfigured.SupportedInterfaces, 2)
}

func TestGatewayCardOmitsRelativeHTTPInterfacesWithoutOrigin(t *testing.T) {
	card, err := gatewayCardWithExtensions(&a2a.AgentCard{}, "", "agent-1", schemas.AgentGatewayAuthPolicy{}, false, nil, "")
	require.NoError(t, err)
	require.Empty(t, card.SupportedInterfaces)

	card, err = gatewayCardWithExtensions(&a2a.AgentCard{}, "", "agent-1", schemas.AgentGatewayAuthPolicy{}, false, nil, "agent-1.a2a.bifrost.example:9090")
	require.NoError(t, err)
	require.Equal(t, []*a2a.AgentInterface{a2a.NewAgentInterface("agent-1.a2a.bifrost.example:9090", a2a.TransportProtocolGRPC)}, card.SupportedInterfaces)
}

func TestExtendedAgentCardUsesRequestOrigin(t *testing.T) {
	client := &fakeSDKClient{extendedCard: func(context.Context, *a2a.GetExtendedAgentCardRequest) (*a2a.AgentCard, error) {
		return &a2a.AgentCard{Name: "extended"}, nil
	}}
	manager, handler, _ := failoverHandler(t, client)
	manager.externalURL = "https://configured.example"
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyA2ARequestOrigin, "https://gateway.example")

	card, err := handler.GetExtendedAgentCard(ctx, &a2a.GetExtendedAgentCardRequest{})
	require.NoError(t, err)
	require.Len(t, card.SupportedInterfaces, 2)
	require.Equal(t, "https://gateway.example/agents/a2a/fixture/json-rpc", card.SupportedInterfaces[0].URL)
	require.Equal(t, "https://gateway.example/agents/a2a/fixture/rest", card.SupportedInterfaces[1].URL)
}

func TestRESTBindingReachesUpstreamAgent(t *testing.T) {
	upstreamHandler, _, m := taskUpstreamGatewayManager(t)
	client := restGatewayClient(t, m)

	task, err := client.GetTask(context.Background(), &a2a.GetTaskRequest{ID: "upstream-task"})
	require.NoError(t, err)
	require.Equal(t, a2a.TaskID("upstream-task"), task.ID)

	listed, err := client.ListTasks(context.Background(), &a2a.ListTasksRequest{})
	require.NoError(t, err)
	require.Len(t, listed.Tasks, 1)

	canceled, err := client.CancelTask(context.Background(), &a2a.CancelTaskRequest{ID: "upstream-task"})
	require.NoError(t, err)
	require.Equal(t, a2a.TaskStateCanceled, canceled.Status.State)

	sent, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi"))})
	require.NoError(t, err)
	require.NotNil(t, sent)

	require.Equal(t, []string{"GetTask", "ListTasks", "CancelTask", "SendMessage"}, upstreamHandler.calls(), "the REST binding forwards through the same handler as JSON-RPC")
}

// panickingUpstream makes one protocol method panic so the transport panic
// handler, not the process, decides the outcome.
type panickingUpstream struct{ taskUpstream }

func (p *panickingUpstream) GetTask(context.Context, *a2a.GetTaskRequest) (*a2a.Task, error) {
	panic("upstream exploded")
}

func TestTransportPanicHandlerReturnsErrorInsteadOfCrashing(t *testing.T) {
	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "http://gateway", nil)
	require.NoError(t, err)
	t.Cleanup(m.Close)

	option := a2asrv.WithTransportPanicHandler(m.transportPanicHandler("boom"))
	for name, binding := range map[string]http.Handler{
		"jsonrpc": a2asrv.NewJSONRPCHandler(&panickingUpstream{}, option),
		"rest":    a2asrv.NewRESTHandler(&panickingUpstream{}, option),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(binding)
			t.Cleanup(server.Close)
			protocol := a2a.TransportProtocolJSONRPC
			options := []a2aclient.FactoryOption{a2aclient.WithJSONRPCTransport(http.DefaultClient)}
			if name == "rest" {
				protocol = a2a.TransportProtocolHTTPJSON
				options = []a2aclient.FactoryOption{a2aclient.WithRESTTransport(http.DefaultClient)}
			}
			client, clientErr := a2aclient.NewFromEndpoints(context.Background(), []*a2a.AgentInterface{a2a.NewAgentInterface(server.URL, protocol)}, options...)
			require.NoError(t, clientErr)
			t.Cleanup(func() { _ = client.Destroy() })
			_, getErr := client.GetTask(context.Background(), &a2a.GetTaskRequest{ID: "any"})
			require.Error(t, getErr, "a panic surfaces as a protocol or transport error")
			if name == "jsonrpc" {
				require.ErrorIs(t, getErr, a2a.ErrInternalError)
			}
			require.NotContains(t, getErr.Error(), "upstream exploded")
		})
	}
}

func TestSuccessfulGateShortCircuitReturnsInternalErrorWithoutCallingUpstream(t *testing.T) {
	var unaryCalls, streamCalls atomic.Int64
	client := &fakeSDKClient{
		send: func(context.Context, *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
			unaryCalls.Add(1)
			return a2a.NewMessage(a2a.MessageRoleAgent), nil
		},
		stream: func(context.Context, *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
			streamCalls.Add(1)
			return func(func(a2a.Event, error) bool) {}
		},
	}
	manager, handler, _ := failoverHandler(t, client)
	var order []string
	plugin := &recordingA2APlugin{
		name:         "short-circuit",
		order:        &order,
		shortCircuit: &schemas.A2APluginShortCircuit{Response: &schemas.BifrostA2AResponse{}},
	}
	manager.SetPluginPipeline(func() PluginPipeline {
		return &fakePipeline{plugins: []*recordingA2APlugin{plugin}}
	}, func(PluginPipeline) {})

	_, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)})
	require.ErrorIs(t, err, a2a.ErrInternalError)
	require.Zero(t, unaryCalls.Load())

	var streamErr error
	for _, err := range handler.SendStreamingMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser)}) {
		streamErr = err
	}
	require.ErrorIs(t, streamErr, a2a.ErrInternalError)
	require.Zero(t, streamCalls.Load())
}

func TestGateOutcomeErrorPreservesOperationErrorWhenGateErrorIsCleared(t *testing.T) {
	opErr := errors.New("upstream failed")

	require.ErrorIs(t, gateOutcomeError(nil, nil, opErr), opErr)
}

func TestGateOutcomeErrorSanitizesInternalFailures(t *testing.T) {
	for _, test := range []struct {
		name        string
		status      int
		message     string
		wantMessage string
	}{
		{name: "internal failure", status: http.StatusInternalServerError, message: "panic in A2A post-hook for plugin secret-plugin: secret-value", wantMessage: a2a.ErrInternalError.Error()},
		{name: "unauthenticated denial", status: http.StatusUnauthorized, message: "missing required credential", wantMessage: "missing required credential"},
		{name: "unauthorized denial", status: http.StatusForbidden, message: "agent access denied", wantMessage: "agent access denied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := gateOutcomeError(nil, &schemas.BifrostError{
				StatusCode: &test.status,
				Error:      &schemas.ErrorField{Message: test.message},
			}, nil)
			require.Error(t, err)
			require.Contains(t, err.Error(), test.wantMessage)
			if test.status == http.StatusInternalServerError {
				require.NotContains(t, err.Error(), "secret-plugin")
				require.NotContains(t, err.Error(), "secret-value")
			}
		})
	}
}

func TestTaskOperationsReachUpstreamAgent(t *testing.T) {
	upstreamHandler, client := taskUpstreamGateway(t)

	task, err := client.GetTask(context.Background(), &a2a.GetTaskRequest{ID: "upstream-task"})
	require.NoError(t, err)
	require.Equal(t, a2a.TaskID("upstream-task"), task.ID, "the upstream agent owns task identity")
	require.Equal(t, "upstream-context", task.ContextID)

	listed, err := client.ListTasks(context.Background(), &a2a.ListTasksRequest{})
	require.NoError(t, err)
	require.Len(t, listed.Tasks, 1)
	require.Equal(t, a2a.TaskID("upstream-task"), listed.Tasks[0].ID)

	canceled, err := client.CancelTask(context.Background(), &a2a.CancelTaskRequest{ID: "upstream-task"})
	require.NoError(t, err)
	require.Equal(t, a2a.TaskStateCanceled, canceled.Status.State)

	require.Equal(t, []string{"GetTask", "ListTasks", "CancelTask"}, upstreamHandler.calls())
}

// ownedTaskUpstream is an upstream agent that owns exactly one task identifier and
// answers the SDK-native not-found error for anything else, which is what makes
// the forwarding assertions meaningful: the upstream, not the gateway, is the one
// deciding that an identifier does not exist.
type ownedTaskUpstream struct {
	taskUpstream
	taskID a2a.TaskID
}

func (u *ownedTaskUpstream) SendMessage(_ context.Context, _ *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	u.record("SendMessage")
	return &a2a.Task{ID: u.taskID, ContextID: "upstream-context", Status: a2a.TaskStatus{State: a2a.TaskStateWorking}}, nil
}

func (u *ownedTaskUpstream) GetTask(_ context.Context, req *a2a.GetTaskRequest) (*a2a.Task, error) {
	u.record("GetTask")
	if req.ID != u.taskID {
		return nil, a2a.ErrTaskNotFound
	}
	return &a2a.Task{ID: req.ID, ContextID: "upstream-context", Status: a2a.TaskStatus{State: a2a.TaskStateWorking}}, nil
}

func (u *ownedTaskUpstream) CancelTask(_ context.Context, req *a2a.CancelTaskRequest) (*a2a.Task, error) {
	u.record("CancelTask")
	if req.ID != u.taskID {
		return nil, a2a.ErrTaskNotFound
	}
	return &a2a.Task{ID: req.ID, ContextID: "upstream-context", Status: a2a.TaskStatus{State: a2a.TaskStateCanceled}}, nil
}

// upstreamAgentServer starts one fake upstream agent and returns its card URL.
func upstreamAgentServer(t *testing.T, name string, handler a2asrv.RequestHandler) string {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	card := &a2a.AgentCard{
		Name:                name,
		Version:             "1",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(server.URL+"/rpc", a2a.TransportProtocolJSONRPC)},
	}
	mux.Handle(agentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(handler))
	return server.URL + agentCardPath
}

// gatewayClientFor exposes one agent's JSON-RPC binding behind an SDK client.
func gatewayClientFor(t *testing.T, m *Manager, name string) *a2aclient.Client {
	t.Helper()
	protocol, ok := m.ProtocolHandler(name)
	require.True(t, ok)
	proxy := httptest.NewServer(protocol)
	t.Cleanup(proxy.Close)
	client, err := a2aclient.NewFromEndpoints(context.Background(), []*a2a.AgentInterface{a2a.NewAgentInterface(proxy.URL, a2a.TransportProtocolJSONRPC)}, a2aclient.WithJSONRPCTransport(http.DefaultClient))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Destroy() })
	return client
}

// twoAgentGateway registers two independent upstream agents behind one manager
// over a shared store.
func twoAgentGateway(t *testing.T) (*memoryStore, *Manager, map[string]string) {
	t.Helper()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	urls := map[string]string{
		"agent-a": upstreamAgentServer(t, "agent-a", &ownedTaskUpstream{taskID: "task-owned-by-a"}),
		"agent-b": upstreamAgentServer(t, "agent-b", &ownedTaskUpstream{taskID: "task-owned-by-b"}),
	}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	t.Cleanup(m.Close)
	for name, url := range urls {
		_, err = m.Create(context.Background(), CreateRequest{Name: name, AgentCardURL: url})
		require.NoError(t, err)
	}
	return store, m, urls
}

// The gateway keeps no task-ownership index, so a task identifier issued by one
// agent is simply forwarded to whichever agent is addressed, and that upstream
// answers authoritatively — here, with its own not-found error.
func TestHTTPJSONCancelTaskPreservesNativeNotFoundError(t *testing.T) {
	upstreamHandler := &taskUpstream{cancelErr: a2a.ErrTaskNotFound}
	var cardCalls atomic.Int64
	var fallbackCalls atomic.Int64
	mux := http.NewServeMux()
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)
	card := &a2a.AgentCard{
		Name:    "tasks",
		Version: "1",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(upstream.URL+"/rest/", a2a.TransportProtocolHTTPJSON),
			a2a.NewAgentInterface(upstream.URL+"/rpc", a2a.TransportProtocolJSONRPC),
		},
	}
	mux.Handle(agentCardPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cardCalls.Add(1)
		a2asrv.NewStaticAgentCardHandler(card).ServeHTTP(w, r)
	}))
	mux.Handle("/rest/", http.StripPrefix("/rest", a2asrv.NewRESTHandler(upstreamHandler)))
	mux.Handle("/rpc", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		a2asrv.NewJSONRPCHandler(upstreamHandler).ServeHTTP(w, r)
	}))

	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "http://gateway", nil)
	require.NoError(t, err)
	t.Cleanup(m.Close)
	_, err = m.Create(context.Background(), CreateRequest{Name: "tasks", AgentCardURL: upstream.URL + agentCardPath})
	require.NoError(t, err)
	initialCardCalls := cardCalls.Load()
	initialFallbackCalls := fallbackCalls.Load()

	protocol, ok := m.ProtocolHandler("tasks")
	require.True(t, ok)
	proxy := httptest.NewServer(protocol)
	t.Cleanup(proxy.Close)
	client, err := a2aclient.NewFromEndpoints(context.Background(), []*a2a.AgentInterface{a2a.NewAgentInterface(proxy.URL, a2a.TransportProtocolJSONRPC)}, a2aclient.WithJSONRPCTransport(http.DefaultClient))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Destroy() })

	_, err = client.CancelTask(context.Background(), &a2a.CancelTaskRequest{ID: "missing-task"})
	require.ErrorIs(t, err, a2a.ErrTaskNotFound)
	require.Equal(t, []string{"CancelTask"}, upstreamHandler.calls())
	require.Equal(t, initialFallbackCalls, fallbackCalls.Load(), "a protocol error must not advance to another candidate")
	require.Never(t, func() bool { return cardCalls.Load() != initialCardCalls }, 100*time.Millisecond, time.Millisecond, "a protocol error must not refresh the Agent Card")
}

func TestTaskIDIsAlwaysForwardedToTheAddressedUpstream(t *testing.T) {
	_, m, _ := twoAgentGateway(t)
	clientA := gatewayClientFor(t, m, "agent-a")
	clientB := gatewayClientFor(t, m, "agent-b")
	ctx := context.Background()

	result, err := clientA.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi"))})
	require.NoError(t, err)
	task, ok := result.(*a2a.Task)
	require.True(t, ok)
	require.Equal(t, a2a.TaskID("task-owned-by-a"), task.ID)

	_, foreignErr := clientB.GetTask(ctx, &a2a.GetTaskRequest{ID: "task-owned-by-a"})
	_, genuineErr := clientB.GetTask(ctx, &a2a.GetTaskRequest{ID: "task-that-never-existed"})
	require.ErrorIs(t, foreignErr, a2a.ErrTaskNotFound, "agent-b's upstream decides the identifier is unknown")
	require.ErrorIs(t, genuineErr, a2a.ErrTaskNotFound)
	require.Equal(t, genuineErr.Error(), foreignErr.Error())

	_, foreignCancel := clientB.CancelTask(ctx, &a2a.CancelTaskRequest{ID: "task-owned-by-a"})
	require.ErrorIs(t, foreignCancel, a2a.ErrTaskNotFound)

	// The addressed agent remains authoritative for its own identifiers.
	owned, err := clientA.GetTask(ctx, &a2a.GetTaskRequest{ID: "task-owned-by-a"})
	require.NoError(t, err)
	require.Equal(t, a2a.TaskID("task-owned-by-a"), owned.ID)
}

// A task subscription for an identifier issued by another agent is forwarded to
// the addressed upstream rather than refused locally, and the request finishes
// cleanly.
func TestForeignTaskIDIsForwardedUpstreamOnSubscribe(t *testing.T) {
	_, m, _ := twoAgentGateway(t)
	clientB := gatewayClientFor(t, m, "agent-b")
	ctx := context.Background()

	var events int
	var subscribeErr error
	for event, eventErr := range clientB.SubscribeToTask(ctx, &a2a.SubscribeToTaskRequest{ID: "task-owned-by-a"}) {
		if eventErr != nil {
			subscribeErr = eventErr
			continue
		}
		if event != nil {
			events++
		}
	}
	require.NoError(t, subscribeErr)
	require.Positive(t, events, "the subscription reached agent-b's upstream instead of being refused locally")
}

func TestProxyRequestHandlerDeferredAndClosedOperations(t *testing.T) {
	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()

	// A closed runtime must surface an error rather than a fabricated local task.
	runtime := m.buildRuntime(schemas.AgentRegistration{Name: "gone", Enabled: true})
	handler := &proxyRequestHandler{runtime: runtime, manager: m, config: runtime.config}
	runtime.close()
	_, err = handler.GetTask(context.Background(), &a2a.GetTaskRequest{ID: "x"})
	require.ErrorIs(t, err, ErrNotFound)
	_, err = handler.ListTasks(context.Background(), &a2a.ListTasksRequest{})
	require.ErrorIs(t, err, ErrNotFound)
	_, err = handler.CancelTask(context.Background(), &a2a.CancelTaskRequest{ID: "x"})
	require.ErrorIs(t, err, ErrNotFound)

	// An extended-card read on a closed runtime is refused for the same reason as
	// every other operation, with or without an identity: the gateway applies no
	// extra identity requirement of its own to the extended card.
	_, err = handler.GetExtendedAgentCard(context.Background(), &a2a.GetExtendedAgentCardRequest{})
	require.ErrorIs(t, err, ErrNotFound)
	_, err = handler.GetExtendedAgentCard(identityContext(t, "sk-bf-vk"), &a2a.GetExtendedAgentCardRequest{})
	require.ErrorIs(t, err, ErrNotFound)

	// Without a push-capable store the push operations report the SDK's native
	// push-not-supported error.
	_, err = handler.GetTaskPushConfig(context.Background(), &a2a.GetTaskPushConfigRequest{})
	require.ErrorIs(t, err, a2a.ErrPushNotificationNotSupported)
	_, err = handler.ListTaskPushConfigs(context.Background(), &a2a.ListTaskPushConfigRequest{})
	require.ErrorIs(t, err, a2a.ErrPushNotificationNotSupported)
	_, err = handler.CreateTaskPushConfig(context.Background(), &a2a.PushConfig{})
	require.ErrorIs(t, err, a2a.ErrPushNotificationNotSupported)
	require.ErrorIs(t, handler.DeleteTaskPushConfig(context.Background(), &a2a.DeleteTaskPushConfigRequest{}), a2a.ErrPushNotificationNotSupported)
}
