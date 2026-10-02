package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/fasthttp/router"
	"github.com/golang-jwt/jwt/v5"
	"github.com/maximhq/bifrost/core/agent"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

type agentAuthStore struct {
	byID          map[string]*tables.TableVirtualKey
	byValue       map[string]*tables.TableVirtualKey
	registrations map[string]schemas.AgentRegistration
}

func (s *agentAuthStore) CreateAgentRegistration(_ context.Context, reg *schemas.AgentRegistration) error {
	if s.registrations == nil {
		s.registrations = map[string]schemas.AgentRegistration{}
	}
	s.registrations[reg.Name] = *reg
	return nil
}
func (*agentAuthStore) UpdateAgentRegistration(context.Context, *schemas.AgentRegistration) error {
	return nil
}
func (*agentAuthStore) ListAgentRegistrations(context.Context) ([]schemas.AgentRegistration, error) {
	return nil, nil
}
func (s *agentAuthStore) GetAgentRegistration(_ context.Context, name string) (*schemas.AgentRegistration, error) {
	if reg, ok := s.registrations[name]; ok {
		return &reg, nil
	}
	return nil, agent.ErrNotFound
}
func (*agentAuthStore) DeleteAgentRegistration(context.Context, string) error { return nil }
func (s *agentAuthStore) GetVirtualKey(_ context.Context, id string) (*tables.TableVirtualKey, error) {
	vk := s.byID[id]
	if vk == nil {
		return nil, configstore.ErrNotFound
	}
	return vk, nil
}
func (s *agentAuthStore) GetVirtualKeyByID(_ context.Context, id string) (*tables.TableVirtualKey, bool) {
	vk := s.byID[id]
	return vk, vk != nil
}

type agentAuthCache struct {
	store    *agentAuthStore
	missByID bool
}

func (c *agentAuthCache) GetVirtualKey(_ context.Context, value string) (*tables.TableVirtualKey, bool) {
	vk := c.store.byValue[value]
	return vk, vk != nil
}

func (c *agentAuthCache) GetVirtualKeyByID(_ context.Context, id string) (*tables.TableVirtualKey, bool) {
	if c.missByID {
		return nil, false
	}
	vk := c.store.byID[id]
	return vk, vk != nil
}

func newAgentAuthMiddleware(enforce bool, store *agentAuthStore) schemas.BifrostHTTPMiddleware {
	return AgentGatewayAuthenticationMiddleware(&lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: enforce}}, &agentAuthCache{store: store}, nil)
}

func runAgentAuthentication(ctx *fasthttp.RequestCtx, middleware schemas.BifrostHTTPMiddleware) (agentAuthenticationResult, bool) {
	called := false
	middleware(func(*fasthttp.RequestCtx) { called = true })(ctx)
	authentication, _ := ctx.UserValue(agentGatewayAuthenticationContextKey).(agentAuthenticationResult)
	return authentication, called
}

func TestAgentGatewayRejectsUnsupportedA2AVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		ok      bool
	}{
		{"", true},
		{"1.0", true},
		{"1.5", true},
		{"0.3", false},
		{"99.0", false},
		{"2.0", false},
	} {
		require.Equal(t, tc.ok, supportedA2AVersion(tc.version), "version %q", tc.version)
	}

	// JSON-RPC rejection: HTTP 200 with code -32009 and the request id echoed.
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBody([]byte(`{"jsonrpc":"2.0","id":7,"method":"SendMessage"}`))
	writeVersionNotSupported(ctx, agentBindingJSONRPC)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	var rpc struct {
		ID    json.RawMessage `json:"id"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &rpc))
	require.Equal(t, -32009, rpc.Error.Code)
	require.JSONEq(t, "7", string(rpc.ID))

	// REST rejection: HTTP 400 with FAILED_PRECONDITION.
	ctx = &fasthttp.RequestCtx{}
	writeVersionNotSupported(ctx, agentBindingREST)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	var rest struct {
		Error struct {
			Status string `json:"status"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &rest))
	require.Equal(t, "FAILED_PRECONDITION", rest.Error.Status)
}

func TestAgentGatewayNormalizesJSONRPCParamKeys(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBody([]byte(`{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"t1","history_length":1,"metadata":{"snake_key":"kept"}}}`))
	normalizeJSONRPCParamKeys(ctx)
	var request struct {
		Params map[string]json.RawMessage `json:"params"`
	}
	require.NoError(t, json.Unmarshal(ctx.Request.Body(), &request))
	require.JSONEq(t, "1", string(request.Params["historyLength"]))
	require.NotContains(t, request.Params, "history_length")
	require.JSONEq(t, `{"snake_key":"kept"}`, string(request.Params["metadata"]), "nested user-controlled keys are untouched")

	// A camelCase key already present wins; the snake_case duplicate is not moved.
	ctx = &fasthttp.RequestCtx{}
	original := `{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"historyLength":2,"history_length":9}}`
	ctx.Request.SetBody([]byte(original))
	normalizeJSONRPCParamKeys(ctx)
	require.NoError(t, json.Unmarshal(ctx.Request.Body(), &request))
	require.JSONEq(t, "2", string(request.Params["historyLength"]))

	// Non-object bodies pass through untouched.
	ctx = &fasthttp.RequestCtx{}
	ctx.Request.SetBody([]byte(`not json`))
	normalizeJSONRPCParamKeys(ctx)
	require.Equal(t, "not json", string(ctx.Request.Body()))
}

func TestAgentGatewayAuthenticateHeaderPrecedence(t *testing.T) {
	active := true
	keys := func(id, value string) *tables.TableVirtualKey {
		return &tables.TableVirtualKey{ID: id, Value: *schemas.NewSecretVar(value), IsActive: &active}
	}
	one, two, three, four, five := keys("vk-1", "sk-bf-one"), keys("vk-2", "sk-bf-two"), keys("vk-3", "sk-bf-three"), keys("vk-4", "sk-bf-four"), keys("vk-5", "sk-bf-five")
	store := &agentAuthStore{
		byID:    map[string]*tables.TableVirtualKey{"vk-1": one, "vk-2": two, "vk-3": three, "vk-4": four, "vk-5": five},
		byValue: map[string]*tables.TableVirtualKey{"sk-bf-one": one, "sk-bf-two": two, "sk-bf-three": three, "sk-bf-four": four, "sk-bf-five": five},
	}
	middleware := newAgentAuthMiddleware(true, store)

	for _, tc := range []struct {
		name       string
		headers    map[string]string
		wantValue  string
		wantSource lib.VirtualKeyHeaderSource
	}{
		{"canonical only", map[string]string{"x-bf-vk": "sk-bf-one"}, "sk-bf-one", lib.VirtualKeyHeaderSourceXBfVK},
		{"bearer only", map[string]string{"Authorization": "Bearer sk-bf-two"}, "sk-bf-two", lib.VirtualKeyHeaderSourceAuthorization},
		{"x-api-key only", map[string]string{"x-api-key": "sk-bf-three"}, "sk-bf-three", lib.VirtualKeyHeaderSourceXAPIKey},
		{"x-goog-api-key only", map[string]string{"x-goog-api-key": "sk-bf-four"}, "sk-bf-four", lib.VirtualKeyHeaderSourceXGoogAPIKey},
		{"api-key only", map[string]string{"api-key": "sk-bf-five"}, "sk-bf-five", lib.VirtualKeyHeaderSourceAPIKey},
		{"canonical beats bearer", map[string]string{"x-bf-vk": "sk-bf-one", "Authorization": "Bearer sk-bf-two"}, "sk-bf-one", lib.VirtualKeyHeaderSourceXBfVK},
		{"bearer beats x-api-key", map[string]string{"Authorization": "Bearer sk-bf-two", "x-api-key": "sk-bf-three"}, "sk-bf-two", lib.VirtualKeyHeaderSourceAuthorization},
		{"x-api-key beats x-goog-api-key", map[string]string{"x-api-key": "sk-bf-three", "x-goog-api-key": "sk-bf-four"}, "sk-bf-three", lib.VirtualKeyHeaderSourceXAPIKey},
		{"x-goog-api-key beats api-key", map[string]string{"x-goog-api-key": "sk-bf-four", "api-key": "sk-bf-five"}, "sk-bf-four", lib.VirtualKeyHeaderSourceXGoogAPIKey},
		{"same value uses first source", map[string]string{"Authorization": "Bearer sk-bf-one", "x-api-key": "sk-bf-one"}, "sk-bf-one", lib.VirtualKeyHeaderSourceAuthorization},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			for header, value := range tc.headers {
				ctx.Request.Header.Set(header, value)
			}
			authentication, called := runAgentAuthentication(ctx, middleware)
			require.True(t, called)
			require.Equal(t, tc.wantValue, authentication.rawVirtualKey)
			require.Equal(t, tc.wantSource, authentication.credentialSource)
		})
	}
}

func TestAgentGatewayCapturesOnlyAcceptedForwardableCredentials(t *testing.T) {
	active := true
	vk := &tables.TableVirtualKey{ID: "vk-1", Value: *schemas.NewSecretVar("sk-bf-one"), IsActive: &active}
	store := &agentAuthStore{byID: map[string]*tables.TableVirtualKey{"vk-1": vk}, byValue: map[string]*tables.TableVirtualKey{"sk-bf-one": vk}}
	resolver := AgentGatewayIdentityResolver(func(_ context.Context, credential string) (string, bool) {
		return "user-1", credential == "Bearer valid-jwt"
	})
	middleware := AgentGatewayAuthenticationMiddleware(&lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: true}}, &agentAuthCache{store: store}, resolver)

	for _, tc := range []struct {
		name   string
		header string
		value  string
	}{
		{"canonical", "x-bf-vk", "sk-bf-one"},
		{"authorization alias", "Authorization", "Bearer sk-bf-one"},
		{"x-api-key alias", "x-api-key", "sk-bf-one"},
		{"x-goog-api-key alias", "x-goog-api-key", "sk-bf-one"},
		{"api-key alias", "api-key", "sk-bf-one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.Set(tc.header, tc.value)
			authentication, called := runAgentAuthentication(ctx, middleware)
			require.True(t, called)
			require.Equal(t, &schemas.AcceptedBifrostCredential{Header: strings.ToLower(tc.header), Value: tc.value}, authentication.acceptedCredential)
			require.Empty(t, ctx.Request.Header.Peek(tc.header))
		})
	}

	user := &fasthttp.RequestCtx{}
	user.SetUserValue(schemas.BifrostContextKeyUserID, "user-1")
	user.Request.Header.Set("Authorization", "Bearer valid-jwt")
	authentication, called := runAgentAuthentication(user, middleware)
	require.True(t, called)
	require.Equal(t, &schemas.AcceptedBifrostCredential{Header: "Authorization", Value: "Bearer valid-jwt"}, authentication.acceptedCredential)
	require.Empty(t, user.Request.Header.Peek("Authorization"))

	unvalidated := &fasthttp.RequestCtx{}
	unvalidated.SetUserValue(schemas.BifrostContextKeyUserID, "user-1")
	unvalidated.Request.Header.Set("Authorization", "Bearer invalid-jwt")
	authentication, called = runAgentAuthentication(unvalidated, middleware)
	require.True(t, called)
	require.Nil(t, authentication.acceptedCredential)
	require.Equal(t, "Bearer invalid-jwt", string(unvalidated.Request.Header.Peek("Authorization")))
}

func TestAgentGatewayInspectRouteUsesAdminMiddlewareAndNormalizesSecurity(t *testing.T) {
	card := &a2a.AgentCard{
		Name:                "inspect-fixture",
		Description:         "inspect me",
		Version:             "1",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("https://agent.example/rpc", a2a.TransportProtocolJSONRPC)},
		SecuritySchemes: a2a.NamedSecuritySchemes{
			"bearer": a2a.HTTPAuthSecurityScheme{Scheme: "bearer", BearerFormat: "JWT"},
			"apiKey": a2a.APIKeySecurityScheme{Name: "x-api-key", Location: a2a.APIKeySecuritySchemeLocationHeader},
		},
		SecurityRequirements: a2a.SecurityRequirementsOptions{{"bearer": {}, "apiKey": {}}},
	}
	upstream := httptest.NewServer(a2asrv.NewStaticAgentCardHandler(card))
	defer upstream.Close()

	manager, err := agent.NewManager(context.Background(), &agentAuthStore{}, nil, "", http.DefaultClient)
	require.NoError(t, err)
	defer manager.Close()
	h := NewAgentGatewayHandler(manager, nil, &lib.Config{}, nil)
	r := router.New()
	authenticated := false
	h.RegisterManagementRoutes(r, func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			authenticated = true
			next(ctx)
		}
	})

	server := &fasthttp.Server{Handler: r.Handler}
	listener := fasthttputil.NewInmemoryListener()
	go server.Serve(listener) //nolint:errcheck
	defer listener.Close()
	defer server.Shutdown()
	client := &fasthttp.Client{Dial: func(string) (net.Conn, error) { return listener.Dial() }}
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	req.Header.SetMethod(fasthttp.MethodPost)
	req.SetRequestURI("http://test.local/api/agents/inspect")
	req.SetBodyString(`{"agent_card_url":` + strconv.Quote(upstream.URL+a2asrv.WellKnownAgentCardPath) + `}`)
	require.NoError(t, client.Do(req, resp))

	require.True(t, authenticated)
	require.Equal(t, fasthttp.StatusOK, resp.StatusCode())
	var response agent.InspectResponse
	require.NoError(t, json.Unmarshal(resp.Body(), &response))
	require.Equal(t, "inspect-fixture", response.Name)
	require.Equal(t, [][]string{{"apiKey", "bearer"}}, response.SecurityRequirements)
	require.Equal(t, []agent.InspectedSecurityScheme{
		{Name: "apiKey", Type: "apiKey", Location: "header", Header: "x-api-key"},
		{Name: "bearer", Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
	}, response.SecuritySchemes)
}

func TestAgentGatewayManagementErrorMapsNotFoundSentinels(t *testing.T) {
	h := &AgentGatewayHandler{}
	for _, err := range []error{configstore.ErrNotFound, agent.ErrNotFound} {
		ctx := &fasthttp.RequestCtx{}
		h.managementError(ctx, err)
		require.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode())
	}

	ctx := &fasthttp.RequestCtx{}
	h.managementError(ctx, errors.New("store unavailable"))
	require.Equal(t, fasthttp.StatusInternalServerError, ctx.Response.StatusCode())
}

func TestAgentGatewayManagementHistoryRoutesUseAdminMiddlewareAndHydrateDetail(t *testing.T) {
	logs, err := logstore.NewLogStore(context.Background(), &logstore.Config{Type: logstore.LogStoreTypeSQLite, Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "agent-history.db")}}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, logs.Close(context.Background())) })
	taskID := "task-1"
	body := `{"kind":"status-update","headers":{"Authorization":"Bearer secret","X-Request-ID":"request-1"},"access_token":"token"}`
	_, err = logs.BatchCreateAgentLogsIfNotExists(context.Background(), []*logstore.AgentLog{{ID: "history-1", Timestamp: time.Now().UTC(), RecordKind: "event", Operation: "tasks/subscribe", Status: "success", AgentName: "fixture", RequestID: "request-1", TaskID: &taskID, EventBody: &body}})
	require.NoError(t, err)

	manager, err := agent.NewManager(context.Background(), &agentAuthStore{}, nil, "", http.DefaultClient, agent.ManagerConfig{})
	require.NoError(t, err)
	defer manager.Close()
	h := NewAgentGatewayHandler(manager, nil, &lib.Config{LogsStore: logs}, nil)
	r := router.New()
	authenticated := false
	adminMiddleware := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			authenticated = true
			next(ctx)
		}
	}
	h.RegisterManagementRoutes(r, adminMiddleware)

	listCtx := &fasthttp.RequestCtx{}
	listCtx.Request.Header.SetMethod(fasthttp.MethodGet)
	listCtx.Request.SetRequestURI("/api/agents/history?task_id=task-1")
	r.Handler(listCtx)
	require.True(t, authenticated)
	require.Equal(t, fasthttp.StatusOK, listCtx.Response.StatusCode())
	var listEnvelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(listCtx.Response.Body(), &listEnvelope))
	require.NotContains(t, string(listEnvelope["logs"]), "event_body")
	require.NotContains(t, string(listEnvelope["logs"]), "has_object")
	require.NotContains(t, string(listEnvelope["logs"]), "payload_reference")
	var listResult logstore.AgentLogHistoryResult
	require.NoError(t, json.Unmarshal(listCtx.Response.Body(), &listResult))
	require.Len(t, listResult.Logs, 1)

	authenticated = false
	detailCtx := &fasthttp.RequestCtx{}
	detailCtx.Request.Header.SetMethod(fasthttp.MethodGet)
	detailCtx.Request.SetRequestURI("/api/agents/history/history-1")
	r.Handler(detailCtx)
	require.True(t, authenticated)
	require.Equal(t, fasthttp.StatusOK, detailCtx.Response.StatusCode())
	var detail logstore.AgentLogDetail
	require.NoError(t, json.Unmarshal(detailCtx.Response.Body(), &detail))
	require.NotNil(t, detail.EventBody)
	require.NotContains(t, *detail.EventBody, "Bearer secret")
	require.NotContains(t, *detail.EventBody, `"access_token":"token"`)
	require.Contains(t, *detail.EventBody, schemas.RedactedAttrValue)
	require.NotContains(t, string(detailCtx.Response.Body()), "has_object")
	require.NotContains(t, string(detailCtx.Response.Body()), "payload_reference")
}

func TestParseAgentHistorySearchValidation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	validRange := "?agent_name=fixture&operation=tasks/get&user_id=user-1&virtual_key_id=vk-1&team_id=team-1&customer_id=customer-1&business_unit_id=bu-1&project_id=project-1&request_id=req-1&trace_id=trace-1&task_id=task-1&context_id=context-1&push_config_id=push-1&delivery_id=delivery-1&attempt_id=attempt-1&event_type=status-update&task_state=completed&selected_id=log-1&start_time=" + now.Format(time.RFC3339) + "&end_time=" + now.Add(time.Hour).Format(time.RFC3339) + "&limit=25&offset=5"

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/agents/history" + validRange)
	filter, pagination, err := parseAgentHistorySearch(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"fixture"}, filter.AgentName)
	require.Equal(t, []string{"tasks/get"}, filter.Operation)
	require.Equal(t, []string{"user-1"}, filter.UserID)
	require.Equal(t, []string{"vk-1"}, filter.VirtualKeyID)
	require.Equal(t, []string{"team-1"}, filter.TeamID)
	require.Equal(t, []string{"customer-1"}, filter.CustomerID)
	require.Equal(t, []string{"bu-1"}, filter.BusinessUnitID)
	require.Equal(t, []string{"project-1"}, filter.ProjectID)
	require.Equal(t, "req-1", filter.RequestID)
	require.Equal(t, "trace-1", filter.TraceID)
	require.Equal(t, "task-1", filter.TaskID)
	require.Equal(t, "context-1", filter.ContextID)
	require.Equal(t, "push-1", filter.PushConfigID)
	require.Equal(t, "delivery-1", filter.DeliveryID)
	require.Equal(t, "attempt-1", filter.AttemptID)
	require.Equal(t, []string{"status-update"}, filter.EventType)
	require.Equal(t, []string{"completed"}, filter.TaskState)
	require.Equal(t, 25, pagination.Limit)
	require.Equal(t, 5, pagination.Offset)
	require.Equal(t, "desc", pagination.Order)
	require.Equal(t, "log-1", pagination.SelectedID)

	for _, uri := range []string{
		"/api/agents/history?start_time=bad&end_time=" + now.Format(time.RFC3339),
		"/api/agents/history?task_id=task-1&limit=0",
		fmt.Sprintf("/api/agents/history?task_id=task-1&limit=%d", logstore.AgentLogHistoryMaxLimit+1),
		"/api/agents/history?task_id=task-1&offset=-1",
	} {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI(uri)
		_, _, err := parseAgentHistorySearch(ctx)
		require.Error(t, err, uri)
	}
}

func TestAgentGatewayAuthenticationPreAuthenticatedUserPreservesIdentity(t *testing.T) {
	middleware := newAgentAuthMiddleware(true, &agentAuthStore{})

	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyUserID, "user-1")
	recordedCredential := grant.NewCredential(grant.CredentialAPIKey, "api-key-1")
	ctx.SetUserValue(schemas.BifrostContextKeyAuthCredential, recordedCredential)
	authentication, called := runAgentAuthentication(ctx, middleware)
	require.True(t, called)
	require.Equal(t, "user-1", authentication.userID)
	require.Empty(t, authentication.rawVirtualKey)
	require.Empty(t, authentication.credentialSource)

	bifrostCtx, ok := ctx.UserValue(agentGatewayBifrostContextKey).(*schemas.BifrostContext)
	require.True(t, ok)
	settledGrant := bifrostCtx.Grant()
	require.NotNil(t, settledGrant)
	require.NotNil(t, settledGrant.Identity())
	require.Equal(t, recordedCredential, settledGrant.Identity().Credential())
	require.Equal(t, "user-1", settledGrant.Identity().User().ID)
	require.Nil(t, settledGrant.Identity().VirtualKey())
}

func TestAgentGatewayAuthenticationUserAndInvalidSelectedIdentity(t *testing.T) {
	active, inactive := true, false
	one := &tables.TableVirtualKey{ID: "vk-1", Value: *schemas.NewSecretVar("sk-bf-one"), IsActive: &active}
	two := &tables.TableVirtualKey{ID: "vk-2", Value: *schemas.NewSecretVar("sk-bf-two"), IsActive: &inactive}
	store := &agentAuthStore{byID: map[string]*tables.TableVirtualKey{"vk-1": one, "vk-2": two}, byValue: map[string]*tables.TableVirtualKey{"sk-bf-one": one, "sk-bf-two": two}}
	middleware := newAgentAuthMiddleware(true, store)

	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyUserID, "user-1")
	ctx.Request.Header.Set("x-bf-vk", "missing-canonical")
	ctx.Request.Header.Set("Authorization", "Bearer sk-bf-one")
	_, called := runAgentAuthentication(ctx, middleware)
	require.False(t, called, "a selected invalid credential blocks even a pre-authenticated user")
	require.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode())

	ctx = &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-vk", "not-a-real-key")
	ctx.Request.Header.Set("Authorization", "Bearer sk-bf-one")
	_, called = runAgentAuthentication(ctx, middleware)
	require.False(t, called, "malformed canonical input is selected verbatim like inference and blocks later aliases")
	require.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode())

	ctx = &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-vk", "sk-bf-two")
	ctx.Request.Header.Set("Authorization", "Bearer sk-bf-one")
	_, called = runAgentAuthentication(ctx, middleware)
	require.False(t, called)
	require.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode())

	middleware = newAgentAuthMiddleware(false, store)
	authentication, called := runAgentAuthentication(&fasthttp.RequestCtx{}, middleware)
	require.True(t, called)
	require.Empty(t, authentication.userID)

	ctx = &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-bf-vk", "missing")
	_, called = runAgentAuthentication(ctx, middleware)
	require.False(t, called, "a selected invalid canonical credential must fail even when authentication is optional")
	require.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
}

func TestAgentGatewayRejectsMCPJWTAsIdentityAndPreservesIt(t *testing.T) {
	key, priv := newTestSigningKey(t)
	oauthStore := &mockOAuth2Store{signingKey: key}
	_ = newTestOAuth2Config(oauthStore, tables.MCPServerAuthModeBoth, true)
	middleware := newAgentAuthMiddleware(true, &agentAuthStore{})
	raw := mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
		c["bf_mode"] = string(schemas.MCPAuthModeVK)
		c["sub"] = "vk-1"
	})
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer "+raw)

	_, called := runAgentAuthentication(ctx, middleware)
	require.False(t, called)
	require.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
	require.Equal(t, "Bearer "+raw, string(ctx.Request.Header.Peek("Authorization")))
}

func TestAgentGatewaySelectedIdentityRemovalPreservesUnselectedHeaders(t *testing.T) {
	allHeaders := map[string]string{
		"x-bf-vk":        "sk-bf-one",
		"Authorization":  "Bearer sk-bf-two",
		"x-api-key":      "sk-bf-three",
		"x-goog-api-key": "sk-bf-four",
		"api-key":        "sk-bf-five",
		"X-Upstream":     "ordinary-secret",
	}
	for _, tc := range []struct {
		name    string
		source  lib.VirtualKeyHeaderSource
		removed string
	}{
		{"preauthenticated user removes nothing", lib.VirtualKeyHeaderSourceNone, ""},
		{"canonical selected", lib.VirtualKeyHeaderSourceXBfVK, "x-bf-vk"},
		{"bearer selected", lib.VirtualKeyHeaderSourceAuthorization, "Authorization"},
		{"x-api-key selected", lib.VirtualKeyHeaderSourceXAPIKey, "x-api-key"},
		{"x-goog-api-key selected", lib.VirtualKeyHeaderSourceXGoogAPIKey, "x-goog-api-key"},
		{"api-key selected", lib.VirtualKeyHeaderSourceAPIKey, "api-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			for header, value := range allHeaders {
				ctx.Request.Header.Set(header, value)
			}
			removeBifrostIdentityHeader(ctx, agentAuthenticationResult{credentialSource: tc.source})
			for header, value := range allHeaders {
				if header == tc.removed {
					require.Empty(t, ctx.Request.Header.Peek(header))
				} else {
					require.Equal(t, value, string(ctx.Request.Header.Peek(header)), header)
				}
			}
		})
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer upstream-token")
	ctx.Request.Header.Set("x-api-key", "upstream-key")
	_, called := runAgentAuthentication(ctx, newAgentAuthMiddleware(true, &agentAuthStore{}))
	require.False(t, called)
	require.Equal(t, "Bearer upstream-token", string(ctx.Request.Header.Peek("Authorization")))
	require.Equal(t, "upstream-key", string(ctx.Request.Header.Peek("x-api-key")))
}

func TestBifrostIdentityContextIncludesRequestHeaders(t *testing.T) {
	fastCtx := &fasthttp.RequestCtx{}
	fastCtx.Request.Header.Set(schemas.HeaderGovernanceProjectID, "project-1")

	bifrostCtx := bifrostIdentityContext(fastCtx, context.Background(), agentAuthenticationResult{})
	defer bifrostCtx.Cancel()

	headers, ok := bifrostCtx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	require.True(t, ok)
	require.Equal(t, "project-1", headers[schemas.HeaderGovernanceProjectID])
}

// grantPipeline is a stand-in for the governance plugin: it implements the same
// narrow A2A plugin pipeline the core wires in, and denies any virtual key that is
// not in its allow set. It lets these transport tests prove the handler maps a
// plugin decision onto HTTP without depending on the real governance plugin.
type grantPipeline struct {
	mu      sync.Mutex
	granted map[string]bool
	denials atomic.Int64
}

func (p *grantPipeline) allow(rawVirtualKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.granted[rawVirtualKey] = true
}

func (p *grantPipeline) revoke(rawVirtualKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.granted, rawVirtualKey)
}

func (p *grantPipeline) RunA2APreHooks(ctx *schemas.BifrostContext, req *schemas.BifrostA2ARequest, entered func(int)) (*schemas.BifrostA2ARequest, *schemas.A2APluginShortCircuit, int) {
	if entered != nil {
		entered(1)
	}
	if req == nil {
		return req, nil, 1
	}
	rawVirtualKey, _ := ctx.Value(schemas.BifrostContextKeyVirtualKey).(string)
	if rawVirtualKey == "" {
		// Anonymous traffic never reaches the gate in the handler, but if it did
		// the grant lookup would still be skipped.
		return req, nil, 1
	}
	p.mu.Lock()
	allowed := p.granted[rawVirtualKey]
	p.mu.Unlock()
	if !allowed {
		p.denials.Add(1)
		return req, &schemas.A2APluginShortCircuit{Error: &schemas.BifrostError{
			StatusCode: schemas.Ptr(403),
			Error:      &schemas.ErrorField{Message: "agent not granted"},
		}}, 1
	}
	return req, nil, 1
}

func (p *grantPipeline) RunA2APostHooks(_ *schemas.BifrostContext, resp *schemas.BifrostA2AResponse, bErr *schemas.BifrostError, _ int) (*schemas.BifrostA2AResponse, *schemas.BifrostError) {
	return resp, bErr
}

func (p *grantPipeline) ObserveA2AEvent(_ *schemas.BifrostContext, _ *schemas.BifrostA2AEvent, _ int) {
}

func TestAgentGatewayRegisteredProtocolRouteResolvesUserAndEnforcesGrant(t *testing.T) {
	card := &a2a.AgentCard{Name: "route-fixture", Version: "1", SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("http://unused/rpc", a2a.TransportProtocolJSONRPC)}}
	upstream := httptest.NewServer(a2asrv.NewStaticAgentCardHandler(card))
	defer upstream.Close()

	active := true
	vk := &tables.TableVirtualKey{ID: "vk-1", Value: *schemas.NewSecretVar("sk-bf-one"), IsActive: &active}
	store := &agentAuthStore{byID: map[string]*tables.TableVirtualKey{"vk-1": vk}, byValue: map[string]*tables.TableVirtualKey{"sk-bf-one": vk}}
	manager, err := agent.NewManager(context.Background(), store, nil, "http://gateway", http.DefaultClient)
	require.NoError(t, err)
	defer manager.Close()
	view, err := manager.Create(context.Background(), agent.CreateRequest{Name: "route-fixture", AgentCardURL: upstream.URL + a2asrv.WellKnownAgentCardPath, VirtualKeyIDs: []string{"vk-1"}})
	require.NoError(t, err)

	// Authorization is decided by the plugin gate, not the handler, so the grant
	// under test lives in the pipeline rather than in the manager.
	pipeline := &grantPipeline{granted: map[string]bool{"sk-bf-one": true}}
	manager.SetPluginPipeline(func() agent.PluginPipeline { return pipeline }, func(agent.PluginPipeline) {})
	h := NewAgentGatewayHandler(manager, nil, &lib.Config{ClientConfig: &configstore.ClientConfig{MCPServerAuthMode: tables.MCPServerAuthModeHeaders, EnforceAuthOnInference: true}}, nil)
	r := router.New()
	credential := "sk-bf-one"
	r.GET("/agents/a2a/{name}/.well-known/agent-card.json", func(ctx *fasthttp.RequestCtx) {
		if credential != "" {
			ctx.SetUserValue(schemas.BifrostContextKeyUserID, "user-1")
			ctx.Request.Header.Set("x-bf-vk", credential)
		}
		AgentGatewayAuthenticationMiddleware(h.config, &agentAuthCache{store: store}, nil)(h.card)(ctx)
	})
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
	request := func() int {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		req.Header.SetMethod(fasthttp.MethodGet)
		req.SetRequestURI("http://test.local/agents/a2a/" + view.Name + "/.well-known/agent-card.json")
		require.NoError(t, client.Do(req, resp))
		return resp.StatusCode()
	}
	requestAgent := func(name string) int {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		req.Header.SetMethod(fasthttp.MethodGet)
		req.SetRequestURI("http://test.local/agents/a2a/" + name + "/.well-known/agent-card.json")
		require.NoError(t, client.Do(req, resp))
		return resp.StatusCode()
	}

	require.Equal(t, fasthttp.StatusOK, request())

	// A granted identity asking for an agent that does not exist gets 404, not 403:
	// the unknown-agent answer is only reached after authorization admits the call.
	require.Equal(t, fasthttp.StatusNotFound, requestAgent("no-such-agent"))

	// Card discovery constructs a typed get-agent-card operation inside the
	// manager, so it passes through the same plugin authorization gate.
	store.byID["vk-ungranted"] = &tables.TableVirtualKey{ID: "vk-ungranted", Value: *schemas.NewSecretVar("sk-bf-ungranted"), IsActive: &active}
	store.byValue["sk-bf-ungranted"] = store.byID["vk-ungranted"]
	credential = "sk-bf-ungranted"
	require.Equal(t, fasthttp.StatusForbidden, request())

	pipeline.allow("sk-bf-ungranted")
	require.Equal(t, fasthttp.StatusOK, request())
	pipeline.revoke("sk-bf-ungranted")
	require.Equal(t, fasthttp.StatusForbidden, request())

	// Missing identity under enforcement -> 401.
	credential = ""
	require.Equal(t, fasthttp.StatusUnauthorized, request())

	h.config.Mu.Lock()
	h.config.ClientConfig.EnforceAuthOnInference = false
	h.config.Mu.Unlock()
	before := pipeline.denials.Load()
	require.Equal(t, fasthttp.StatusOK, request(), "anonymous card access follows disabled global inference authentication without requiring a grant")
	require.Equal(t, before, pipeline.denials.Load(), "anonymous traffic skips the grant lookup entirely")
}

func TestAgentGatewayCardUsesSafeBifrostRequestOrigin(t *testing.T) {
	upstreamExecutor := &testAgentExecutor{execute: func(context.Context, *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) { yield(nil, a2a.ErrUnsupportedOperation) }
	}}
	mux := http.NewServeMux()
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	card := &a2a.AgentCard{Name: "card-fixture", Version: "1", SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(upstream.URL+"/rpc", a2a.TransportProtocolJSONRPC)}}
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(upstreamExecutor)))

	store := &agentAuthStore{}
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: false}}
	manager, err := agent.NewManager(context.Background(), store, nil, "", http.DefaultClient, agent.ManagerConfig{AuthPolicy: AgentGatewayAuthPolicy(config)})
	require.NoError(t, err)
	defer manager.Close()
	_, err = manager.Create(context.Background(), agent.CreateRequest{Name: "card-fixture", AgentCardURL: upstream.URL + a2asrv.WellKnownAgentCardPath})
	require.NoError(t, err)
	h := NewAgentGatewayHandler(manager, nil, config, nil)

	r := router.New()
	h.RegisterProtocolRoutes(r, AgentGatewayAuthenticationMiddleware(config, &agentAuthCache{store: store}, nil))
	server := &fasthttp.Server{Handler: r.Handler}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go server.Serve(ln) //nolint:errcheck
	defer ln.Close()
	defer server.Shutdown()

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	req.Header.SetMethod(http.MethodGet)
	req.SetRequestURI("http://" + ln.Addr().String() + "/agents/a2a/card-fixture/.well-known/agent-card.json")
	req.Header.SetHost("bifrost.example:8443")
	req.Header.Set("X-Forwarded-Proto", "https, http")
	require.NoError(t, fasthttp.Do(req, resp))
	require.Equal(t, fasthttp.StatusOK, resp.StatusCode())

	// Cards are stored privately but revalidated on every use so a live authentication
	// policy change cannot leave an anonymous response fresh in a client cache.
	require.Equal(t, "private, no-cache", string(resp.Header.Peek(fasthttp.HeaderCacheControl)))
	require.Contains(t, string(resp.Header.Peek(fasthttp.HeaderVary)), "Authorization")
	require.Contains(t, string(resp.Header.Peek(fasthttp.HeaderVary)), "x-bf-vk")
	require.NotEmpty(t, resp.Header.Peek(fasthttp.HeaderETag))
	require.NotEmpty(t, resp.Header.Peek(fasthttp.HeaderLastModified))
	etag := string(resp.Header.Peek(fasthttp.HeaderETag))

	resp.Reset()
	req.Header.Set(fasthttp.HeaderIfNoneMatch, etag)
	require.NoError(t, fasthttp.Do(req, resp))
	require.Equal(t, fasthttp.StatusNotModified, resp.StatusCode())
	require.Empty(t, resp.Body())

	config.Mu.Lock()
	config.ClientConfig.EnforceAuthOnInference = true
	config.Mu.Unlock()
	resp.Reset()
	require.NoError(t, fasthttp.Do(req, resp))
	require.Equal(t, fasthttp.StatusUnauthorized, resp.StatusCode(), "authentication policy changes must run before conditional card revalidation")

	config.Mu.Lock()
	config.ClientConfig.EnforceAuthOnInference = false
	config.Mu.Unlock()
	resp.Reset()
	req.Header.Del(fasthttp.HeaderIfNoneMatch)
	require.NoError(t, fasthttp.Do(req, resp))
	require.Equal(t, fasthttp.StatusOK, resp.StatusCode())

	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(resp.Body(), &payload))
	require.NotContains(t, payload, "url", "strict A2A v1 rejects the legacy top-level URL field")
	var strictCard a2a.AgentCard
	require.NoError(t, json.Unmarshal(resp.Body(), &strictCard))
	require.Len(t, strictCard.SupportedInterfaces, 2)
	gatewayURL := "https://" + ln.Addr().String() + "/agents/a2a/card-fixture"
	require.Equal(t, gatewayURL+agent.GatewayJSONRPCPathSuffix, strictCard.SupportedInterfaces[0].URL)
	require.Equal(t, a2a.TransportProtocolJSONRPC, strictCard.SupportedInterfaces[0].ProtocolBinding)
	require.Equal(t, gatewayURL+agent.GatewayRESTPathSuffix, strictCard.SupportedInterfaces[1].URL)
	require.Equal(t, a2a.TransportProtocolHTTPJSON, strictCard.SupportedInterfaces[1].ProtocolBinding)
}

// TestAgentGatewayRESTBindingSharesSecurityPath proves the HTTP+JSON routes are
// governed by exactly the same authentication and authorization path as JSON-RPC:
// same 401 without a credential under enforcement, same plugin-decided 403, same
// 404 for an unknown agent after authorization, and the same success otherwise.
func TestAgentGatewayRESTBindingSharesSecurityPath(t *testing.T) {
	upstreamExecutor := &testAgentExecutor{execute: func(context.Context, *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("ok")), nil)
		}
	}}
	mux := http.NewServeMux()
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	card := &a2a.AgentCard{Name: "rest-fixture", Version: "1", SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(upstream.URL+"/rpc", a2a.TransportProtocolJSONRPC)}}
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(upstreamExecutor)))

	active := true
	vk := &tables.TableVirtualKey{ID: "vk-1", Value: *schemas.NewSecretVar("sk-bf-one"), IsActive: &active}
	store := &agentAuthStore{byID: map[string]*tables.TableVirtualKey{"vk-1": vk}, byValue: map[string]*tables.TableVirtualKey{"sk-bf-one": vk}}
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: true}}
	manager, err := agent.NewManager(context.Background(), store, nil, "http://gateway", http.DefaultClient, agent.ManagerConfig{AuthPolicy: AgentGatewayAuthPolicy(config)})
	require.NoError(t, err)
	defer manager.Close()
	pipeline := &grantPipeline{granted: map[string]bool{"sk-bf-one": true}}
	manager.SetPluginPipeline(func() agent.PluginPipeline { return pipeline }, func(agent.PluginPipeline) {})
	_, err = manager.Create(context.Background(), agent.CreateRequest{Name: "rest-fixture", AgentCardURL: upstream.URL + a2asrv.WellKnownAgentCardPath, VirtualKeyIDs: []string{"vk-1"}})
	require.NoError(t, err)

	h := NewAgentGatewayHandler(manager, nil, config, nil)
	r := router.New()
	h.RegisterProtocolRoutes(r, AgentGatewayAuthenticationMiddleware(config, &agentAuthCache{store: store}, nil))
	server := &fasthttp.Server{Handler: r.Handler}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go server.Serve(ln) //nolint:errcheck
	defer ln.Close()
	defer server.Shutdown()

	call := func(name, credential string) int {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		req.Header.SetMethod(fasthttp.MethodPost)
		req.SetRequestURI("http://" + ln.Addr().String() + "/agents/a2a/" + name + agent.GatewayRESTPathSuffix + "/message:send")
		req.Header.SetContentType("application/json")
		req.SetBodyString(`{"message":{"role":"user","parts":[{"kind":"text","text":"hello"}],"messageId":"m-1","kind":"message"}}`)
		if credential != "" {
			req.Header.Set("x-bf-vk", credential)
		}
		require.NoError(t, fasthttp.Do(req, resp))
		return resp.StatusCode()
	}

	require.Equal(t, fasthttp.StatusOK, call("rest-fixture", "sk-bf-one"))
	require.Equal(t, fasthttp.StatusUnauthorized, call("rest-fixture", ""), "REST enforces authentication exactly as JSON-RPC does")
	require.Equal(t, fasthttp.StatusNotFound, call("no-such-agent", "sk-bf-one"), "an unknown agent is answered only after authorization")
	pipeline.revoke("sk-bf-one")
	require.Equal(t, fasthttp.StatusForbidden, call("rest-fixture", "sk-bf-one"), "the same plugin decision governs REST")
}

func TestAgentGatewayGovernanceDenialIsNativeAndSkipsUpstream(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstreamExecutor := &testAgentExecutor{execute: func(context.Context, *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			upstreamCalls.Add(1)
			yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("unexpected")), nil)
		}
	}}
	mux := http.NewServeMux()
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	card := &a2a.AgentCard{Name: "denied-fixture", Version: "1", SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(upstream.URL+"/rpc", a2a.TransportProtocolJSONRPC)}}
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(upstreamExecutor)))

	active := true
	vk := &tables.TableVirtualKey{ID: "vk-denied", Value: *schemas.NewSecretVar("sk-bf-denied"), IsActive: &active}
	store := &agentAuthStore{byID: map[string]*tables.TableVirtualKey{"vk-denied": vk}, byValue: map[string]*tables.TableVirtualKey{"sk-bf-denied": vk}}
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: true}}
	manager, err := agent.NewManager(context.Background(), store, nil, "http://gateway", http.DefaultClient, agent.ManagerConfig{AuthPolicy: AgentGatewayAuthPolicy(config)})
	require.NoError(t, err)
	defer manager.Close()
	manager.SetPluginPipeline(func() agent.PluginPipeline { return &grantPipeline{granted: map[string]bool{}} }, func(agent.PluginPipeline) {})
	_, err = manager.Create(context.Background(), agent.CreateRequest{Name: "denied-fixture", AgentCardURL: upstream.URL + a2asrv.WellKnownAgentCardPath})
	require.NoError(t, err)

	h := NewAgentGatewayHandler(manager, nil, config, nil)
	r := router.New()
	h.RegisterProtocolRoutes(r, AgentGatewayAuthenticationMiddleware(config, &agentAuthCache{store: store}, nil))
	server := &fasthttp.Server{Handler: r.Handler}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go server.Serve(ln) //nolint:errcheck
	defer ln.Close()
	defer server.Shutdown()

	call := func(path, body string) (int, []byte) {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		req.Header.SetMethod(fasthttp.MethodPost)
		req.SetRequestURI("http://" + ln.Addr().String() + path)
		req.Header.SetContentType("application/json")
		req.Header.Set("x-bf-vk", "sk-bf-denied")
		req.SetBodyString(body)
		require.NoError(t, fasthttp.Do(req, resp))
		return resp.StatusCode(), append([]byte(nil), resp.Body()...)
	}

	t.Run("JSON-RPC", func(t *testing.T) {
		status, body := call("/agents/a2a/denied-fixture"+agent.GatewayJSONRPCPathSuffix, `{"jsonrpc":"2.0","id":"rpc-denied","method":"SendMessage","params":{"message":{"role":"user","parts":[{"kind":"text","text":"hello"}],"messageId":"m-jsonrpc","kind":"message"}}}`)
		require.Equal(t, fasthttp.StatusOK, status, "JSON-RPC transports protocol errors in a successful HTTP envelope")
		var payload struct {
			ID    string `json:"id"`
			Error struct {
				Code int              `json:"code"`
				Data []map[string]any `json:"data"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(body, &payload))
		require.Equal(t, "rpc-denied", payload.ID)
		require.Equal(t, -31403, payload.Error.Code)
		require.NotEmpty(t, payload.Error.Data)
		require.Equal(t, "UNAUTHORIZED", payload.Error.Data[0]["reason"])
		require.Equal(t, int64(0), upstreamCalls.Load())
	})

	t.Run("REST", func(t *testing.T) {
		status, body := call("/agents/a2a/denied-fixture"+agent.GatewayRESTPathSuffix+"/message:send", `{"message":{"role":"user","parts":[{"kind":"text","text":"hello"}],"messageId":"m-rest","kind":"message"}}`)
		require.Equal(t, fasthttp.StatusForbidden, status)
		var payload struct {
			Error struct {
				Code    int              `json:"code"`
				Status  string           `json:"status"`
				Details []map[string]any `json:"details"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(body, &payload))
		require.Equal(t, fasthttp.StatusForbidden, payload.Error.Code)
		require.Equal(t, "PERMISSION_DENIED", payload.Error.Status)
		require.NotEmpty(t, payload.Error.Details)
		require.Equal(t, "UNAUTHORIZED", payload.Error.Details[0]["reason"])
		require.Equal(t, int64(0), upstreamCalls.Load())
	})
}

func TestAgentGatewayAnonymousProtocolFollowsLiveEnforcement(t *testing.T) {
	type countingExecutor struct{ calls atomic.Int64 }
	var executor countingExecutor
	execute := func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			executor.calls.Add(1)
			yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("ok")), nil)
		}
	}
	upstreamExecutor := &testAgentExecutor{execute: execute}
	mux := http.NewServeMux()
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	card := &a2a.AgentCard{Name: "protocol-fixture", Version: "1", SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(upstream.URL+"/rpc", a2a.TransportProtocolJSONRPC)}}
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(upstreamExecutor)))

	store := &agentAuthStore{}
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: false}}
	manager, err := agent.NewManager(context.Background(), store, nil, "http://gateway", http.DefaultClient, agent.ManagerConfig{AuthPolicy: AgentGatewayAuthPolicy(config)})
	require.NoError(t, err)
	defer manager.Close()
	h := NewAgentGatewayHandler(manager, nil, config, nil)
	r := router.New()
	h.RegisterProtocolRoutes(r, AgentGatewayAuthenticationMiddleware(config, &agentAuthCache{store: store}, nil))
	view, err := manager.Create(context.Background(), agent.CreateRequest{Name: "protocol-fixture", AgentCardURL: upstream.URL + a2asrv.WellKnownAgentCardPath})
	require.NoError(t, err)
	server := &fasthttp.Server{Handler: r.Handler}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go server.Serve(ln) //nolint:errcheck
	defer ln.Close()
	defer server.Shutdown()
	client, err := a2aclient.NewFromEndpoints(context.Background(), []*a2a.AgentInterface{a2a.NewAgentInterface("http://"+ln.Addr().String()+"/agents/a2a/"+view.Name+agent.GatewayJSONRPCPathSuffix, a2a.TransportProtocolJSONRPC)}, a2aclient.WithJSONRPCTransport(http.DefaultClient))
	require.NoError(t, err)
	defer client.Destroy()

	_, err = client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello"))})
	require.NoError(t, err)
	require.Equal(t, int64(1), executor.calls.Load())

	config.Mu.Lock()
	config.ClientConfig.EnforceAuthOnInference = true
	config.Mu.Unlock()
	_, err = client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("blocked"))})
	require.Error(t, err)
	require.Equal(t, int64(1), executor.calls.Load(), "authentication rejection must precede upstream effects")

	config.Mu.Lock()
	config.ClientConfig.EnforceAuthOnInference = false
	config.Mu.Unlock()
	require.NoError(t, manager.Delete(context.Background(), view.Name))
	_, err = client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("deleted"))})
	require.Error(t, err, "the generic route remains registered but the deleted runtime is immediately absent")
	require.Equal(t, int64(1), executor.calls.Load())
}

// TestAgentGatewayExtendedCardFollowsEnforcementSetting proves the extended card
// obeys the same global rule as every other protocol operation: with inference
// authentication disabled an anonymous request is forwarded and the upstream
// agent decides, and with it enabled an anonymous request is refused with 401. A
// valid identity without a grant is refused with 403 by the plugin at the
// transport boundary, and a granted identity receives the gateway-rewritten card.
func TestAgentGatewayExtendedCardFollowsEnforcementSetting(t *testing.T) {
	mux := http.NewServeMux()
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	card := &a2a.AgentCard{
		Name:                "extended-fixture",
		Version:             "1",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(upstream.URL+"/rpc", a2a.TransportProtocolJSONRPC)},
		Capabilities:        a2a.AgentCapabilities{ExtendedAgentCard: true},
	}
	extended := &a2a.AgentCard{
		Name:                "extended-fixture-private",
		Version:             "1",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(upstream.URL+"/rpc", a2a.TransportProtocolJSONRPC)},
		Capabilities:        a2a.AgentCapabilities{ExtendedAgentCard: true},
		Signatures:          []a2a.AgentCardSignature{{Protected: "p", Signature: "s"}},
	}
	upstreamExecutor := &testAgentExecutor{execute: func(context.Context, *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) { yield(nil, a2a.ErrUnsupportedOperation) }
	}}
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(upstreamExecutor,
		a2asrv.WithCapabilityChecks(&a2a.AgentCapabilities{ExtendedAgentCard: true}),
		a2asrv.WithExtendedAgentCard(extended))))

	active := true
	vk := &tables.TableVirtualKey{ID: "vk-1", Value: *schemas.NewSecretVar("sk-bf-one"), IsActive: &active}
	store := &agentAuthStore{byID: map[string]*tables.TableVirtualKey{"vk-1": vk}, byValue: map[string]*tables.TableVirtualKey{"sk-bf-one": vk}}
	// Enforcement is deliberately disabled: anonymous traffic is otherwise allowed.
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: false}}
	manager, err := agent.NewManager(context.Background(), store, nil, "http://gateway", http.DefaultClient, agent.ManagerConfig{AuthPolicy: AgentGatewayAuthPolicy(config)})
	require.NoError(t, err)
	defer manager.Close()
	pipeline := &grantPipeline{granted: map[string]bool{"sk-bf-one": true}}
	manager.SetPluginPipeline(func() agent.PluginPipeline { return pipeline }, func(agent.PluginPipeline) {})
	_, err = manager.Create(context.Background(), agent.CreateRequest{Name: "extended-fixture", AgentCardURL: upstream.URL + a2asrv.WellKnownAgentCardPath, VirtualKeyIDs: []string{"vk-1"}})
	require.NoError(t, err)

	h := NewAgentGatewayHandler(manager, nil, config, nil)
	r := router.New()
	h.RegisterProtocolRoutes(r, AgentGatewayAuthenticationMiddleware(config, &agentAuthCache{store: store}, nil))
	server := &fasthttp.Server{Handler: r.Handler}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go server.Serve(ln) //nolint:errcheck
	defer ln.Close()
	defer server.Shutdown()

	call := func(path, credential string) (int, []byte) {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		req.Header.SetMethod(fasthttp.MethodGet)
		req.SetRequestURI("http://" + ln.Addr().String() + path)
		if credential != "" {
			req.Header.Set("x-bf-vk", credential)
		}
		require.NoError(t, fasthttp.Do(req, resp))
		return resp.StatusCode(), append([]byte(nil), resp.Body()...)
	}

	publicPath := "/agents/a2a/extended-fixture/.well-known/agent-card.json"
	extendedPath := "/agents/a2a/extended-fixture" + agent.GatewayRESTPathSuffix + "/extendedAgentCard"

	status, _ := call(publicPath, "")
	require.Equal(t, fasthttp.StatusOK, status, "public discovery stays anonymous when enforcement is disabled")

	status, body := call(extendedPath, "")
	require.Equal(t, fasthttp.StatusOK, status, "with enforcement disabled the anonymous request is forwarded and the upstream decides")
	var anonymous a2a.AgentCard
	require.NoError(t, json.Unmarshal(body, &anonymous))
	require.Equal(t, "extended-fixture-private", anonymous.Name)

	config.Mu.Lock()
	config.ClientConfig.EnforceAuthOnInference = true
	config.Mu.Unlock()
	status, _ = call(extendedPath, "")
	require.Equal(t, fasthttp.StatusUnauthorized, status, "with enforcement enabled an anonymous extended-card read is refused")

	pipeline.revoke("sk-bf-one")
	status, _ = call(extendedPath, "sk-bf-one")
	require.Equal(t, fasthttp.StatusForbidden, status, "a valid identity without a grant is denied by the plugin")

	pipeline.allow("sk-bf-one")
	status, body = call(extendedPath, "sk-bf-one")
	require.Equal(t, fasthttp.StatusOK, status)
	var served a2a.AgentCard
	require.NoError(t, json.Unmarshal(body, &served))
	require.Equal(t, "extended-fixture-private", served.Name, "the upstream extended content is served")
	require.Nil(t, served.Signatures, "the upstream signature does not survive the gateway rewrite")
	require.Len(t, served.SupportedInterfaces, 2)
	require.Equal(t, "http://gateway/agents/a2a/extended-fixture"+agent.GatewayJSONRPCPathSuffix, served.SupportedInterfaces[0].URL)
	require.Contains(t, served.SecuritySchemes, a2a.SecuritySchemeName("bifrostVirtualKey"))
}

type testAgentExecutor struct {
	execute func(context.Context, *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error]
}

func (e *testAgentExecutor) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return e.execute(ctx, ec)
}
func (*testAgentExecutor) Cancel(context.Context, *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) { yield(nil, a2a.ErrUnsupportedOperation) }
}
