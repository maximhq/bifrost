package chaosmock_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// The OAuth server is proven the same way the protocol is: a real mcp-go OAuth
// client completes a flow against it. The rest pins the edges a client under
// test is expected to handle, through the same raw requests such a client
// sends.

const testRedirect = "http://127.0.0.1:1/callback"

// oauthFleet starts a fleet whose alpha server has OAuth configured through
// the control API, the way a scenario would do it.
func oauthFleet(t *testing.T, oauth map[string]any) (*chaosmock.Fleet, *chaosmock.Server) {
	t.Helper()
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "echo_tool"})
	resp, _ := post(t, fleet.URL()+"/control/alpha/auth", map[string]any{"auth": map[string]any{"oauth": oauth}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enabling oauth returned %d", resp.StatusCode)
	}
	return fleet, srv
}

// noRedirect is a client that hands back redirects instead of following them,
// which is how a test stands in for the browser.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

// authorize follows an authorization URL once and returns the query the
// authorization server redirected back with.
func authorize(t *testing.T, authURL string) url.Values {
	t.Helper()
	resp, err := noRedirect.Get(authURL)
	if err != nil {
		t.Fatalf("authorize request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected a redirect from authorize, got %d: %s", resp.StatusCode, body)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("bad Location: %v", err)
	}
	return loc.Query()
}

// token posts a form to the token endpoint and decodes the JSON answer.
func token(t *testing.T, fleet *chaosmock.Fleet, form url.Values) (int, map[string]any) {
	t.Helper()
	resp, err := http.PostForm(fleet.URL()+"/s/alpha/as/token", form)
	if err != nil {
		t.Fatalf("token request failed: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("token response is not JSON: %v", err)
	}
	return resp.StatusCode, out
}

// mcpStatus sends an initialize to alpha with the given bearer token and
// returns the status and the challenge header.
func mcpStatus(t *testing.T, fleet *chaosmock.Fleet, bearer string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, fleet.ServerURL("alpha"),
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mcp request failed: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("WWW-Authenticate")
}

// codeFor runs one authorization for the pre-registered client c1 as identity
// and returns the code and the verifier that redeems it.
func codeFor(t *testing.T, fleet *chaosmock.Fleet, identity string) (string, string) {
	t.Helper()
	verifier, err := client.GenerateCodeVerifier()
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {"c1"},
		"redirect_uri":          {testRedirect},
		"state":                 {"st"},
		"code_challenge":        {client.GenerateCodeChallenge(verifier)},
		"code_challenge_method": {"S256"},
		"login_hint":            {identity},
	}
	back := authorize(t, fleet.URL()+"/s/alpha/as/authorize?"+q.Encode())
	if back.Get("code") == "" || back.Get("state") != "st" {
		t.Fatalf("expected a code and the state back, got %v", back)
	}
	return back.Get("code"), verifier
}

// exchange redeems a code for c1 and returns the token response.
func exchange(t *testing.T, fleet *chaosmock.Fleet, code, verifier string) map[string]any {
	t.Helper()
	status, out := token(t, fleet, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier},
		"client_id": {"c1"}, "client_secret": {"s1"}, "redirect_uri": {testRedirect},
	})
	if status != http.StatusOK {
		t.Fatalf("code exchange returned %d: %v", status, out)
	}
	return out
}

func TestRealOAuthClientCompletesTheFlow(t *testing.T) {
	fleet, srv := oauthFleet(t, map[string]any{})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := client.NewOAuthStreamableHttpClient(fleet.ServerURL("alpha"), client.OAuthConfig{
		RedirectURI:           testRedirect,
		AuthServerMetadataURL: fleet.URL() + "/.well-known/oauth-authorization-server/s/alpha/as",
		PKCEEnabled:           true,
		TokenStore:            client.NewMemoryTokenStore(),
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer c.Close()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("failed to start client: %v", err)
	}

	var initReq mcp.InitializeRequest
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "chaosmock-oauth", Version: "1.0.0"}
	_, err = c.Initialize(ctx, initReq)
	if !client.IsOAuthAuthorizationRequiredError(err) {
		t.Fatalf("expected the server to require authorization, got %v", err)
	}
	handler := client.GetOAuthHandler(err)

	if err := handler.RegisterClient(ctx, "chaosmock-test"); err != nil {
		t.Fatalf("dynamic registration failed: %v", err)
	}
	verifier, _ := client.GenerateCodeVerifier()
	state, _ := client.GenerateState()
	authURL, err := handler.GetAuthorizationURL(ctx, state, client.GenerateCodeChallenge(verifier))
	if err != nil {
		t.Fatalf("authorization URL failed: %v", err)
	}
	back := authorize(t, authURL+"&login_hint=alice")
	if err := handler.ProcessAuthorizationResponse(ctx, back.Get("code"), back.Get("state"), verifier); err != nil {
		t.Fatalf("code exchange failed: %v", err)
	}

	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("initialize with the issued token failed: %v", err)
	}
	var call mcp.CallToolRequest
	call.Params.Name = "echo_tool"
	if _, err := c.CallTool(ctx, call); err != nil {
		t.Fatalf("tools/call with the issued token failed: %v", err)
	}

	// The identity that approved is the one calls are attributed to.
	if got := srv.Counters().Snapshot().CallsByIdentity["alice"]; got != 1 {
		t.Errorf("expected one call attributed to alice, got %v", srv.Counters().Snapshot().CallsByIdentity)
	}
	st := srv.State().OAuth
	if st.Counters.Register != 1 || st.Counters.TokenCode != 1 || len(st.Clients) != 1 || st.Clients[0].AuthMethod != "none" {
		t.Errorf("expected one public client registered and one code exchanged, got %+v / %+v", st.Counters, st.Clients)
	}
}

func TestProtectedResourceChallengesAndPublishesMetadata(t *testing.T) {
	fleet, srv := oauthFleet(t, map[string]any{"challenge_scope": "mcp.read"})

	status, challenge := mcpStatus(t, fleet, "")
	want := `Bearer resource_metadata="` + fleet.URL() + `/.well-known/oauth-protected-resource/s/alpha/mcp", scope="mcp.read"`
	if status != http.StatusUnauthorized || challenge != want {
		t.Fatalf("expected 401 with %s, got %d %q", want, status, challenge)
	}
	if status, challenge := mcpStatus(t, fleet, "not-a-token"); status != http.StatusUnauthorized ||
		!strings.HasSuffix(challenge, `, error="invalid_token"`) {
		t.Errorf("a presented bad token must be named invalid_token, got %d %q", status, challenge)
	}

	// The listening stream is where discovery starts, so with OAuth on it
	// challenges too instead of opening.
	resp, err := http.Get(fleet.ServerURL("alpha"))
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected the GET stream to require a token, got %d", resp.StatusCode)
	}

	getJSON := func(path string) (int, map[string]any) {
		resp, err := http.Get(fleet.URL() + path)
		if err != nil {
			t.Fatalf("GET %s failed: %v", path, err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	status, prm := getJSON("/.well-known/oauth-protected-resource/s/alpha/mcp")
	if status != http.StatusOK || prm["resource"] != fleet.ServerURL("alpha") {
		t.Errorf("unexpected resource metadata %d %v", status, prm)
	}
	status, meta := getJSON("/.well-known/oauth-authorization-server/s/alpha/as")
	if status != http.StatusOK || meta["issuer"] != fleet.URL()+"/s/alpha/as" || meta["registration_endpoint"] == nil {
		t.Errorf("unexpected server metadata %d %v", status, meta)
	}
	if _, has := meta["resource"]; has {
		t.Errorf("server metadata must not carry a resource, got %v", meta)
	}
	// One fleet hosts many servers, so the root documents name none of them.
	for _, root := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-authorization-server"} {
		if status, _ := getJSON(root); status != http.StatusNotFound {
			t.Errorf("expected %s to be 404, got %d", root, status)
		}
	}
	if got := srv.State().OAuth.Counters.Rejected; got != 3 {
		t.Errorf("expected three rejected requests, got %d", got)
	}

	// A bare challenge names no metadata, and turning registration off
	// removes it from the metadata and the server.
	post(t, fleet.URL()+"/control/alpha/auth", map[string]any{"auth": map[string]any{
		"oauth": map[string]any{"challenge": "bare", "registration": false},
	}})
	if _, challenge := mcpStatus(t, fleet, ""); challenge != `Bearer realm="chaosmock"` {
		t.Errorf("expected a bare challenge, got %q", challenge)
	}
	if _, meta := getJSON("/.well-known/oauth-authorization-server/s/alpha/as"); meta["registration_endpoint"] != nil {
		t.Errorf("expected no registration_endpoint, got %v", meta["registration_endpoint"])
	}
	resp, err = http.Post(fleet.URL()+"/s/alpha/as/register", "application/json", strings.NewReader(`{"redirect_uris":["x"]}`))
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected register to be 404 with registration off, got %d", resp.StatusCode)
	}
}

func TestAuthorizationServerEnforcesClientsCodesAndPKCE(t *testing.T) {
	fleet, srv := oauthFleet(t, map[string]any{
		"clients": []any{map[string]any{"client_id": "c1", "client_secret": "s1", "redirect_uris": []string{testRedirect}}},
	})
	base := fleet.URL() + "/s/alpha/as/authorize?"

	// An unknown client is answered on the spot, never redirected.
	resp, err := noRedirect.Get(base + url.Values{"client_id": {"nobody"}, "redirect_uri": {testRedirect}}.Encode())
	if err != nil {
		t.Fatalf("authorize failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for an unknown client, got %d", resp.StatusCode)
	}
	// PKCE is required.
	back := authorize(t, base+url.Values{"response_type": {"code"}, "client_id": {"c1"}, "redirect_uri": {testRedirect}, "state": {"st"}}.Encode())
	if back.Get("error") != "invalid_request" || back.Get("state") != "st" {
		t.Errorf("expected invalid_request with the state, got %v", back)
	}

	code, verifier := codeFor(t, fleet, "bob")
	if status, out := token(t, fleet, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier},
		"client_id": {"c1"}, "client_secret": {"wrong"}, "redirect_uri": {testRedirect},
	}); status != http.StatusUnauthorized || out["error"] != "invalid_client" {
		t.Errorf("expected invalid_client for a wrong secret, got %d %v", status, out)
	}
	if status, out := token(t, fleet, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier + "x"},
		"client_id": {"c1"}, "client_secret": {"s1"}, "redirect_uri": {testRedirect},
	}); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Errorf("expected invalid_grant for a wrong verifier, got %d %v", status, out)
	}
	issued := exchange(t, fleet, code, verifier)
	if issued["token_type"] != "Bearer" || issued["refresh_token"] == nil || issued["expires_in"] != float64(3600) {
		t.Errorf("unexpected token response %v", issued)
	}
	if status, out := token(t, fleet, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier},
		"client_id": {"c1"}, "client_secret": {"s1"}, "redirect_uri": {testRedirect},
	}); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Errorf("expected a used code to be refused, got %d %v", status, out)
	}

	if status, _ := mcpStatus(t, fleet, issued["access_token"].(string)); status != http.StatusOK {
		t.Errorf("expected the issued token to be accepted, got %d", status)
	}

	// The log keeps the raw values a client sent, which is what lets a test
	// check it sent the right secret and verifier.
	var pkce []string
	for _, e := range srv.State().OAuth.Log {
		if e.Endpoint == "token" {
			pkce = append(pkce, e.PKCE+"/"+e.ClientSecret)
		}
	}
	if got := strings.Join(pkce, ","); got != "/wrong,mismatch/s1,ok/s1,/s1" {
		t.Errorf("unexpected token log %s", got)
	}

	// Deny turns every approval into access_denied.
	post(t, fleet.URL()+"/control/alpha/auth", map[string]any{"auth": map[string]any{"oauth": map[string]any{"deny": true}}})
	back = authorize(t, base+url.Values{"response_type": {"code"}, "client_id": {"c1"}, "redirect_uri": {testRedirect},
		"state": {"st"}, "code_challenge": {"abc"}, "code_challenge_method": {"S256"}}.Encode())
	if back.Get("error") != "access_denied" || back.Get("code") != "" {
		t.Errorf("expected access_denied, got %v", back)
	}
}

func TestRefreshRotationExpiryAndRevocation(t *testing.T) {
	fleet, srv := oauthFleet(t, map[string]any{
		"rotate_refresh": true,
		"clients":        []any{map[string]any{"client_id": "c1", "client_secret": "s1"}},
	})
	code, verifier := codeFor(t, fleet, "carol")
	first := exchange(t, fleet, code, verifier)

	refresh := func(rt string) (int, map[string]any) {
		return token(t, fleet, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt},
			"client_id": {"c1"}, "client_secret": {"s1"}})
	}
	status, second := refresh(first["refresh_token"].(string))
	if status != http.StatusOK || second["refresh_token"] == first["refresh_token"] {
		t.Fatalf("expected a rotated refresh token, got %d %v", status, second)
	}
	if status, out := refresh(first["refresh_token"].(string)); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Errorf("a rotated-away refresh token must be refused, got %d %v", status, out)
	}

	// Expiring makes the access token fail at the resource while the client
	// still believes it is good, and leaves the refresh token working.
	at := second["access_token"].(string)
	post(t, fleet.URL()+"/control/alpha/oauth/expire", map[string]any{"identity": "carol"})
	if status, _ := mcpStatus(t, fleet, at); status != http.StatusUnauthorized {
		t.Errorf("expected the expired token to be refused, got %d", status)
	}
	status, third := refresh(second["refresh_token"].(string))
	if status != http.StatusOK {
		t.Fatalf("refresh after expiry should work, got %d %v", status, third)
	}

	// Revoking ends the grant: its tokens fail and its refresh is refused.
	post(t, fleet.URL()+"/control/alpha/oauth/revoke", map[string]any{"identity": "carol"})
	if status, _ := mcpStatus(t, fleet, third["access_token"].(string)); status != http.StatusUnauthorized {
		t.Errorf("expected a revoked grant's token to be refused, got %d", status)
	}
	if status, out := refresh(third["refresh_token"].(string)); status != http.StatusBadRequest ||
		out["error_description"] != "refresh token revoked" {
		t.Errorf("expected invalid_grant for a revoked refresh token, got %d %v", status, out)
	}
	if g := srv.State().OAuth.Grants; len(g) != 1 || !g[0].Revoked || g[0].Identity != "carol" {
		t.Errorf("expected one revoked grant for carol, got %+v", g)
	}

	// A verb that would change nothing is refused rather than silently ignored.
	resp, _ := post(t, fleet.URL()+"/control/alpha/oauth/expire", map[string]any{"identity": "nobody"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 when no token matches, got %d", resp.StatusCode)
	}
}

func TestQueuedOAuthFailuresFireThenClear(t *testing.T) {
	fleet, srv := oauthFleet(t, map[string]any{
		"clients": []any{map[string]any{"client_id": "c1", "client_secret": "s1"}},
	})
	code, verifier := codeFor(t, fleet, "dave")
	issued := exchange(t, fleet, code, verifier)
	rt := issued["refresh_token"].(string)

	post(t, fleet.URL()+"/control/alpha/oauth/fail", map[string]any{"fail": map[string]any{
		"endpoint": "token", "grant_type": "refresh_token", "status": 503, "error": "temporarily_unavailable", "count": 2,
	}})
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {"c1"}, "client_secret": {"s1"}}
	for i := 0; i < 2; i++ {
		if status, out := token(t, fleet, form); status != http.StatusServiceUnavailable || out["error"] != "temporarily_unavailable" {
			t.Fatalf("refresh %d: expected the queued 503, got %d %v", i+1, status, out)
		}
	}
	if status, _ := token(t, fleet, form); status != http.StatusOK {
		t.Errorf("expected the third refresh to succeed once the queue is spent, got %d", status)
	}
	if c := srv.State().OAuth.Counters; c.TokenRefresh != 3 || c.TokenErrors != 2 {
		t.Errorf("expected 3 refresh requests and 2 errors, got %+v", c)
	}

	post(t, fleet.URL()+"/control/alpha/oauth/fail", map[string]any{"fail": map[string]any{"endpoint": "register", "status": 400}})
	_, state := post(t, fleet.URL()+"/control/alpha/faults/clear", nil)
	if len(state.OAuth.PendingFails) != 0 {
		t.Errorf("expected faults/clear to drop queued failures, got %+v", state.OAuth.PendingFails)
	}

	for name, body := range map[string]map[string]any{
		"unknown endpoint": {"fail": map[string]any{"endpoint": "authorize", "status": 500}},
		"success status":   {"fail": map[string]any{"endpoint": "token", "status": 200}},
	} {
		if resp, _ := post(t, fleet.URL()+"/control/alpha/oauth/fail", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", name, resp.StatusCode)
		}
	}
}

func TestOAuthVerbsNeedOAuthAndAValidConfig(t *testing.T) {
	fleet, _ := newFleetWithTools(t, chaosmock.Tool{Name: "echo_tool"})
	if resp, _ := post(t, fleet.URL()+"/control/alpha/oauth/expire", map[string]any{}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for an oauth verb on a server without oauth, got %d", resp.StatusCode)
	}
	resp, _ := post(t, fleet.URL()+"/control/alpha/auth", map[string]any{"auth": map[string]any{"oauth": map[string]any{"challenge": "loud"}}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for an unknown challenge style, got %d", resp.StatusCode)
	}
	// Without OAuth the metadata does not exist.
	res, err := http.Get(fleet.URL() + "/.well-known/oauth-authorization-server/s/alpha/as")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for metadata with oauth off, got %d", res.StatusCode)
	}
}
