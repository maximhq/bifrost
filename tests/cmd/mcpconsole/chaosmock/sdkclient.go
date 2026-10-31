package chaosmock

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// The fleet can also drive a real mcp-go client against an outside MCP server that takes OAuth, so
// a test meets that server the way an SDK client does: discovery, dynamic registration,
// authorization with PKCE, the code exchange and every refresh after it. Consent is the test's to
// give, through whatever the server offers, so the flow is split where a browser would take over:
//
//	POST   /client/<name>/connect     {"url", "redirect_uri", "client_name"}: discovery and
//	                                  registration; answers the authorization URL
//	POST   /client/<name>/authorized  {"code", "state"}: the code exchange, then the handshake and
//	                                  tools/list; answers the tool names
//	POST   /client/<name>/call        {"tool", "arguments"}: one tools/call, refreshing an expired
//	                                  token first
//	GET    /client/<name>             every HTTP request the client made
//	DELETE /client/<name>             closes the client
//
// A step the client cannot complete answers 502 with the SDK's error.

// sdkClientTimeout bounds each step a driven client takes.
const sdkClientTimeout = 30 * time.Second

// SDKRequest is one HTTP request a driven client made, with the grant type of a token request.
type SDKRequest struct {
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	GrantType string `json:"grant_type,omitempty"`
}

// sdkClient is one driven client and the authorization it waits on.
type sdkClient struct {
	client   *client.Client
	oauth    *transport.OAuthHandler
	verifier string

	mu       sync.Mutex
	requests []SDKRequest
}

// sdkClients holds a fleet's driven clients by name.
type sdkClients struct {
	mu sync.Mutex
	m  map[string]*sdkClient
}

// newSDKClients returns an empty set of driven clients.
func newSDKClients() *sdkClients { return &sdkClients{m: map[string]*sdkClient{}} }

// closeAll closes and forgets every driven client.
func (cs *sdkClients) closeAll() {
	cs.mu.Lock()
	clients := cs.m
	cs.m = map[string]*sdkClient{}
	cs.mu.Unlock()
	for _, c := range clients {
		_ = c.client.Close()
	}
}

// recordRequests is a driven client's transport: it passes each request on and records it.
type recordRequests struct {
	next http.RoundTripper
	c    *sdkClient
}

// RoundTrip records r, reading the grant type out of a form body.
func (t recordRequests) RoundTrip(r *http.Request) (*http.Response, error) {
	entry := SDKRequest{Method: r.Method, Path: r.URL.Path}
	if r.Body != nil && strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			return nil, err
		}
		r = r.Clone(r.Context())
		r.Body = io.NopCloser(bytes.NewReader(body))
		if form, err := url.ParseQuery(string(body)); err == nil {
			entry.GrantType = form.Get("grant_type")
		}
	}
	resp, err := t.next.RoundTrip(r)
	if resp != nil {
		entry.Status = resp.StatusCode
	}
	t.c.mu.Lock()
	t.c.requests = append(t.c.requests, entry)
	t.c.mu.Unlock()
	return resp, err
}

// routeClient serves /client/<name>/<verb>.
func (f *Fleet) routeClient(w http.ResponseWriter, r *http.Request) {
	name, verb, ok := splitServerPath(r.URL.Path, "/client/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch {
	case verb == "connect" && r.Method == http.MethodPost:
		f.clients.connect(w, r, name)
	case verb == "authorized" && r.Method == http.MethodPost:
		f.clients.authorized(w, r, name)
	case verb == "call" && r.Method == http.MethodPost:
		f.clients.call(w, r, name)
	case verb == "" && r.Method == http.MethodGet:
		if c := f.clients.lookup(w, name); c != nil {
			c.mu.Lock()
			requests := append([]SDKRequest{}, c.requests...)
			c.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"requests": requests})
		}
	case verb == "" && r.Method == http.MethodDelete:
		f.clients.mu.Lock()
		c, found := f.clients.m[name]
		delete(f.clients.m, name)
		f.clients.mu.Unlock()
		if found {
			_ = c.client.Close()
		}
		writeJSON(w, http.StatusOK, map[string]any{"closed": found})
	case verb == "connect" || verb == "authorized" || verb == "call" || verb == "":
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	default:
		http.NotFound(w, r)
	}
}

// lookup returns the named client, or answers 404 and returns nil.
func (cs *sdkClients) lookup(w http.ResponseWriter, name string) *sdkClient {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	c, ok := cs.m[name]
	if !ok {
		http.Error(w, "no such client: "+name, http.StatusNotFound)
		return nil
	}
	return c
}

// sdkFailed answers 502 with the error a driven client met.
func sdkFailed(w http.ResponseWriter, step string, err error) {
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": step + ": " + err.Error()})
}

// initializeRequest is the handshake a driven client sends.
func initializeRequest() mcp.InitializeRequest {
	var req mcp.InitializeRequest
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "chaosmock-sdk-client", Version: "1.0.0"}
	return req
}

// connect starts a client against url, which holds no token and so must be sent to authorize,
// registers it and answers the URL the user would be sent to. A client already under name is
// replaced.
func (cs *sdkClients) connect(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		URL         string `json:"url"`
		RedirectURI string `json:"redirect_uri"`
		ClientName  string `json:"client_name"`
	}
	if err := decodeBody(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.URL == "" || body.RedirectURI == "" {
		http.Error(w, "url and redirect_uri are required", http.StatusBadRequest)
		return
	}
	if body.ClientName == "" {
		body.ClientName = "chaosmock-sdk-client"
	}
	c := &sdkClient{}
	httpClient := &http.Client{Timeout: sdkClientTimeout, Transport: recordRequests{next: http.DefaultTransport, c: c}}
	mc, err := client.NewOAuthStreamableHttpClient(body.URL, client.OAuthConfig{
		RedirectURI: body.RedirectURI,
		PKCEEnabled: true,
		TokenStore:  client.NewMemoryTokenStore(),
		HTTPClient:  httpClient,
	}, transport.WithHTTPBasicClient(httpClient))
	if err != nil {
		sdkFailed(w, "create", err)
		return
	}
	// The client outlives this request, so it is started on its own context.
	if err := mc.Start(context.Background()); err != nil {
		sdkFailed(w, "start", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sdkClientTimeout)
	defer cancel()
	_, err = mc.Initialize(ctx, initializeRequest())
	if !client.IsOAuthAuthorizationRequiredError(err) {
		_ = mc.Close()
		if err == nil {
			err = errors.New("the server answered without asking for authorization")
		}
		sdkFailed(w, "handshake", err)
		return
	}
	handler := client.GetOAuthHandler(err)
	if err := handler.RegisterClient(ctx, body.ClientName); err != nil {
		_ = mc.Close()
		sdkFailed(w, "registration", err)
		return
	}
	verifier, err := client.GenerateCodeVerifier()
	if err != nil {
		_ = mc.Close()
		sdkFailed(w, "code verifier", err)
		return
	}
	state, err := client.GenerateState()
	if err != nil {
		_ = mc.Close()
		sdkFailed(w, "state", err)
		return
	}
	authURL, err := handler.GetAuthorizationURL(ctx, state, client.GenerateCodeChallenge(verifier))
	if err != nil {
		_ = mc.Close()
		sdkFailed(w, "authorization URL", err)
		return
	}
	c.client, c.oauth, c.verifier = mc, handler, verifier

	cs.mu.Lock()
	previous := cs.m[name]
	cs.m[name] = c
	cs.mu.Unlock()
	if previous != nil {
		_ = previous.client.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{"authorize_url": authURL, "client_id": handler.GetClientID()})
}

// authorized exchanges the code the authorization returned, then completes the handshake and
// lists the tools.
func (cs *sdkClients) authorized(w http.ResponseWriter, r *http.Request, name string) {
	c := cs.lookup(w, name)
	if c == nil {
		return
	}
	var body struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if err := decodeBody(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sdkClientTimeout)
	defer cancel()
	if err := c.oauth.ProcessAuthorizationResponse(ctx, body.Code, body.State, c.verifier); err != nil {
		sdkFailed(w, "code exchange", err)
		return
	}
	init, err := c.client.Initialize(ctx, initializeRequest())
	if err != nil {
		sdkFailed(w, "handshake", err)
		return
	}
	list, err := c.client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		sdkFailed(w, "tools/list", err)
		return
	}
	names := make([]string, 0, len(list.Tools))
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"protocol_version": init.ProtocolVersion, "tools": names})
}

// call runs one tools/call and answers whether it was an error and its text.
func (cs *sdkClients) call(w http.ResponseWriter, r *http.Request, name string) {
	c := cs.lookup(w, name)
	if c == nil {
		return
	}
	var body struct {
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := decodeBody(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sdkClientTimeout)
	defer cancel()
	var req mcp.CallToolRequest
	req.Params.Name = body.Tool
	req.Params.Arguments = body.Arguments
	res, err := c.client.CallTool(ctx, req)
	if err != nil {
		sdkFailed(w, "tools/call", err)
		return
	}
	var text []string
	for _, content := range res.Content {
		if t, ok := content.(mcp.TextContent); ok {
			text = append(text, t.Text)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"is_error": res.IsError, "text": strings.Join(text, "\n")})
}
