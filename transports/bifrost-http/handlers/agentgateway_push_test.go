package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/agent"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

// agentPushStore layers an in-memory push store over the registration store so
// the ingress route test runs against a push-enabled manager.
type agentPushStore struct {
	agentAuthStore
	mu         sync.Mutex
	configs    []schemas.AgentPushConfig
	deliveries map[string]schemas.AgentPushDelivery
}

func (s *agentPushStore) SaveAgentPushConfig(_ context.Context, config *schemas.AgentPushConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs = append(s.configs, *config)
	return nil
}

func (s *agentPushStore) BindAgentPushConfigTask(_ context.Context, agentName, ingressTokenHash, pendingTaskID, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, config := range s.configs {
		if config.AgentName == agentName && config.IngressTokenHash == ingressTokenHash && config.TaskID == pendingTaskID {
			s.configs[i].TaskID = taskID
		}
	}
	return nil
}

func (s *agentPushStore) GetAgentPushConfig(_ context.Context, agentName, taskID, configID string) (*schemas.AgentPushConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, config := range s.configs {
		if config.AgentName == agentName && config.TaskID == taskID && config.ConfigID == configID {
			result := config
			return &result, nil
		}
	}
	return nil, nil
}

func (s *agentPushStore) GetAgentPushConfigByIngressTokenHash(_ context.Context, agentName, hash string) (*schemas.AgentPushConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, config := range s.configs {
		if config.AgentName == agentName && config.IngressTokenHash == hash {
			result := config
			return &result, nil
		}
	}
	return nil, nil
}

func (s *agentPushStore) ListAgentPushConfigs(_ context.Context, agentName, taskID string) ([]schemas.AgentPushConfig, error) {
	return nil, nil
}

func (s *agentPushStore) ListAgentPushConfigsPaginated(context.Context, schemas.AgentPushConfigQuery) ([]schemas.AgentPushConfig, int64, error) {
	return nil, 0, nil
}

func (s *agentPushStore) ListAgentPushConfigAgentNames(context.Context) ([]string, error) {
	return nil, nil
}

func (s *agentPushStore) DeleteAgentPushConfig(_ context.Context, agentName, taskID, configID string) (bool, error) {
	return false, nil
}

func (s *agentPushStore) DeletePendingAgentPushConfig(_ context.Context, agentName, ingressTokenHash, pendingTaskID string) (bool, error) {
	return false, nil
}

func (s *agentPushStore) CreateAgentPushDeliveryIfNotExists(_ context.Context, delivery *schemas.AgentPushDelivery) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deliveries == nil {
		s.deliveries = map[string]schemas.AgentPushDelivery{}
	}
	if _, ok := s.deliveries[delivery.ID]; ok {
		return false, nil
	}
	s.deliveries[delivery.ID] = *delivery
	return true, nil
}

func (s *agentPushStore) ListDueAgentPushDeliveries(context.Context, time.Time, int) ([]schemas.AgentPushDelivery, error) {
	// The relay is exercised in core tests; the transport test only verifies
	// ingress acceptance, so nothing is ever handed to the relay here.
	return nil, nil
}

func (s *agentPushStore) ClaimAgentPushDelivery(context.Context, string, string, time.Time) (bool, error) {
	return false, nil
}

func (s *agentPushStore) UpdateAgentPushDeliveryOutcome(_ context.Context, delivery *schemas.AgentPushDelivery, runnerID string, leaseUntil time.Time) error {
	return nil
}

func (s *agentPushStore) PruneAgentPushDeliveries(context.Context, time.Time) error {
	return nil
}

func TestAgentGatewayPushIngressRoute(t *testing.T) {
	card := &a2a.AgentCard{Name: "push-fixture", Version: "1", SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("http://unused/rpc", a2a.TransportProtocolJSONRPC)}}
	upstream := httptest.NewServer(a2asrv.NewStaticAgentCardHandler(card))
	defer upstream.Close()

	token := "ingress-token"
	hash := sha256.Sum256([]byte(token))
	store := &agentPushStore{configs: []schemas.AgentPushConfig{{
		AgentName:        "push-fixture",
		TaskID:           "task-1",
		ConfigID:         "cfg-1",
		URL:              "http://client.example/callback",
		IngressTokenHash: hex.EncodeToString(hash[:]),
	}}}
	manager, err := agent.NewManager(
		context.Background(),
		store,
		nil,
		"",
		http.DefaultClient,
		agent.ManagerConfig{ExternalURLProvider: func() string { return "http://gateway" }},
	)
	require.NoError(t, err)
	defer manager.Close()
	_, err = manager.Create(context.Background(), agent.CreateRequest{Name: "push-fixture", AgentCardURL: upstream.URL + a2asrv.WellKnownAgentCardPath})
	require.NoError(t, err)

	h := NewAgentGatewayHandler(manager, nil, &lib.Config{}, nil)
	r := router.New()
	// Registered without the Agent Gateway authentication middleware, exactly
	// as the server wires it: the per-config token is the only credential.
	h.RegisterPushIngressRoute(r)

	server := &fasthttp.Server{Handler: r.Handler}
	ln := fasthttputil.NewInmemoryListener()
	go server.Serve(ln) //nolint:errcheck
	defer ln.Close()
	defer server.Shutdown()
	client := &fasthttp.Client{
		Dial:         func(string) (net.Conn, error) { return ln.Dial() },
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	payload, err := json.Marshal(a2a.StreamResponse{Event: &a2a.TaskStatusUpdateEvent{
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Status:    a2a.TaskStatus{State: a2a.TaskStateCompleted},
	}})
	require.NoError(t, err)

	post := func(agentName, token string) (int, string) {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		req.Header.SetMethod(fasthttp.MethodPost)
		req.SetRequestURI("http://test.local/agents/a2a/" + agentName + agent.GatewayPushCallbackPathSuffix)
		if token != "" {
			req.Header.Set(agent.PushNotificationTokenHeader, token)
		}
		req.SetBody(payload)
		require.NoError(t, client.Do(req, resp))
		return resp.StatusCode(), string(resp.Body())
	}

	status, body := post("push-fixture", token)
	require.Equal(t, fasthttp.StatusAccepted, status, body)
	status, _ = post("push-fixture", "wrong-token")
	require.Equal(t, fasthttp.StatusUnauthorized, status)
	status, _ = post("push-fixture", "")
	require.Equal(t, fasthttp.StatusUnauthorized, status)
	status, _ = post("no-such-agent", token)
	require.Equal(t, fasthttp.StatusNotFound, status)

	store.mu.Lock()
	require.Len(t, store.deliveries, 1)
	store.mu.Unlock()
}
