package chaosmock

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// A virtual server can be made an OAuth 2.1 protected resource with its own
// authorization server, so a client's whole upstream OAuth path runs for real:
// discovery (RFC 9728, RFC 8414), dynamic registration (RFC 7591),
// authorization with PKCE (RFC 7636), and token exchange and refresh
// (RFC 6749, with RFC 8707 resource indicators).
//
// Consent is approved automatically. A test follows the authorization URL
// with a client that does not follow redirects and reads the code off the
// Location header, and picks who approves with login_hint, as OpenID Connect
// does. No browser is involved, and the identity that approved is the one the
// server then attributes calls to, so per-user isolation is checkable from the
// counters.
//
// Paths derive from the server name, like every other mock path:
//
//	/.well-known/oauth-protected-resource/s/<name>/mcp   protected resource metadata (also /sse)
//	/.well-known/oauth-authorization-server/s/<name>/as  authorization server metadata
//	/s/<name>/as/register, /authorize, /token            the authorization server
//
// The root well-known paths stay 404: one fleet hosts many servers, so only the
// path-inserted forms can name one.

// OAuthConfig turns OAuth on for a server. It is set through Auth.OAuth.
type OAuthConfig struct {
	// Resource is the resource identifier the metadata publishes. Empty
	// means the URL of the endpoint the metadata describes.
	Resource string `json:"resource,omitempty"`
	// Challenge "bare" answers an unauthenticated request with a challenge
	// that names no metadata, so a client has to find it by convention.
	// Empty sends resource_metadata.
	Challenge string `json:"challenge,omitempty"`
	// ChallengeScope adds scope="..." to the challenge.
	ChallengeScope string `json:"challenge_scope,omitempty"`
	// Registration false removes dynamic registration: no
	// registration_endpoint in the metadata, and register answers 404.
	Registration *bool `json:"registration,omitempty"`
	// Clients are registered up front, for a client configured with a
	// client_id rather than registering itself.
	Clients []OAuthClient `json:"clients,omitempty"`
	// ApproveAs is the identity that approves an authorization request
	// that carries no login_hint. Empty means "user".
	ApproveAs string `json:"approve_as,omitempty"`
	// Deny ends every authorization request in access_denied.
	Deny bool `json:"deny,omitempty"`
	// AccessTTL is how long an access token lives. Zero means an hour.
	AccessTTL Duration `json:"access_ttl,omitempty"`
	// RotateRefresh issues a new refresh token on every refresh and retires
	// the one presented.
	RotateRefresh bool `json:"rotate_refresh,omitempty"`
}

// validate rejects a configuration the server would otherwise misread.
func (c *OAuthConfig) validate() error {
	if c.Challenge != "" && c.Challenge != "bare" {
		return fmt.Errorf(`oauth challenge must be "bare" or empty, got %q`, c.Challenge)
	}
	for _, client := range c.Clients {
		if client.ClientID == "" {
			return fmt.Errorf("every oauth client needs a client_id")
		}
	}
	return nil
}

// OAuthClient is a client the authorization server knows.
type OAuthClient struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret,omitempty"` // empty: a public client
	RedirectURIs []string `json:"redirect_uris,omitempty"` // empty: any redirect_uri is accepted
	// Registration fills in the rest; they are shown, not configured.
	AuthMethod string `json:"token_endpoint_auth_method,omitempty"`
	Scope      string `json:"scope,omitempty"`
	Dynamic    bool   `json:"dynamic,omitempty"`
}

// allows reports whether redirectURI may be used with this client.
func (c *OAuthClient) allows(redirectURI string) bool {
	if len(c.RedirectURIs) == 0 {
		return true
	}
	for _, u := range c.RedirectURIs {
		if u == redirectURI {
			return true
		}
	}
	return false
}

// OAuthFail queues failures on the authorization server: the next Count
// requests to Endpoint (prm, metadata, register or token), only token requests
// of GrantType when it is set, answer Status with an OAuth error body.
type OAuthFail struct {
	Endpoint  string `json:"endpoint"`
	GrantType string `json:"grant_type,omitempty"`
	Status    int    `json:"status"`
	Error     string `json:"error,omitempty"` // default "server_error"
	Count     int    `json:"count,omitempty"` // default 1
}

// OAuthCounters tallies what the authorization server was asked. Token
// requests count by grant type whether they succeed or not; TokenErrors counts
// the ones that failed, and Rejected counts requests the protected resource
// turned away.
type OAuthCounters struct {
	PRM          int64 `json:"prm"`
	Metadata     int64 `json:"metadata"`
	Register     int64 `json:"register"`
	Authorize    int64 `json:"authorize"`
	TokenCode    int64 `json:"token_code"`
	TokenRefresh int64 `json:"token_refresh"`
	TokenErrors  int64 `json:"token_errors"`
	Rejected     int64 `json:"rejected"`
}

// OAuthLogEntry records one authorization server request with the raw values
// it carried and issued. This is a test server, so secrets are shown: checking
// that a client sent the right secret, verifier or refresh token is the point.
type OAuthLogEntry struct {
	Seq                int    `json:"seq"`
	Endpoint           string `json:"endpoint"`
	Status             int    `json:"status"`
	GrantType          string `json:"grant_type,omitempty"`
	ClientID           string `json:"client_id,omitempty"`
	ClientSecret       string `json:"client_secret,omitempty"`
	RedirectURI        string `json:"redirect_uri,omitempty"`
	Scope              string `json:"scope,omitempty"`
	Resource           string `json:"resource,omitempty"`
	State              string `json:"state,omitempty"`
	LoginHint          string `json:"login_hint,omitempty"`
	Identity           string `json:"identity,omitempty"`
	PKCE               string `json:"pkce,omitempty"` // ok, missing or mismatch
	RefreshToken       string `json:"refresh_token,omitempty"`
	IssuedAccessToken  string `json:"issued_access_token,omitempty"`
	IssuedRefreshToken string `json:"issued_refresh_token,omitempty"`
	Error              string `json:"error,omitempty"`
}

// OAuthGrant is one approval: an identity, the client it approved, and the
// tokens currently standing for it.
type OAuthGrant struct {
	Identity        string    `json:"identity"`
	ClientID        string    `json:"client_id"`
	Scope           string    `json:"scope,omitempty"`
	Resource        string    `json:"resource,omitempty"`
	AccessToken     string    `json:"access_token"`
	AccessExpiresAt time.Time `json:"access_expires_at"`
	AccessValid     bool      `json:"access_valid"`
	RefreshToken    string    `json:"refresh_token"`
	Revoked         bool      `json:"revoked"`
}

// OAuthState is the authorization server's readable state.
type OAuthState struct {
	Clients      []OAuthClient   `json:"clients"`
	Grants       []OAuthGrant    `json:"grants"`
	Counters     OAuthCounters   `json:"counters"`
	PendingFails []OAuthFail     `json:"pending_fails"`
	Log          []OAuthLogEntry `json:"log"`
}

// oauthLogCap bounds the log; a long run keeps the most recent entries.
const oauthLogCap = 500

// oauthCodeTTL is how long an authorization code stays redeemable.
const oauthCodeTTL = 10 * time.Minute

// oauthState is one server's authorization server: its client registry,
// outstanding codes, grants and tokens. It lives as long as the server, so
// re-adding a server starts it empty, and changing the OAuth configuration
// keeps it, so a scenario can switch on Deny without losing its clients.
type oauthState struct {
	mu       sync.Mutex
	clients  map[string]*OAuthClient
	order    []string // client ids in registration order
	codes    map[string]*oauthCode
	grants   []*oauthGrant
	access   map[string]*oauthAccess
	refresh  map[string]*oauthGrant // live refresh tokens
	fails    []OAuthFail
	counters OAuthCounters
	log      []OAuthLogEntry
	seq      int
	dcr      int
}

// oauthCode is an issued authorization code and what it is bound to.
type oauthCode struct {
	clientID, redirectURI, challenge, scope, resource, identity string
	expires                                                     time.Time
	used                                                        bool
}

// oauthGrant is the live form of OAuthGrant.
type oauthGrant struct {
	identity, clientID, scope, resource string
	accessToken, refreshToken           string
	revoked                             bool
}

// oauthAccess is one access token. Earlier access tokens of a grant stay valid
// until they expire, as with a real server, so a refresh does not by itself
// break a connection still holding the previous one.
type oauthAccess struct {
	grant   *oauthGrant
	expires time.Time
}

// newOAuthState returns an empty authorization server.
func newOAuthState() *oauthState {
	return &oauthState{
		clients: map[string]*OAuthClient{},
		codes:   map[string]*oauthCode{},
		access:  map[string]*oauthAccess{},
		refresh: map[string]*oauthGrant{},
	}
}

// addClient registers c, replacing a client with the same id.
func (o *oauthState) addClient(c OAuthClient) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.putClient(c)
}

// putClient registers c. Callers hold o.mu.
func (o *oauthState) putClient(c OAuthClient) {
	if _, exists := o.clients[c.ClientID]; !exists {
		o.order = append(o.order, c.ClientID)
	}
	o.clients[c.ClientID] = &c
}

// record appends a log entry under the next sequence number. Callers hold
// o.mu.
func (o *oauthState) record(e OAuthLogEntry) {
	o.seq++
	e.Seq = o.seq
	o.log = append(o.log, e)
	if len(o.log) > oauthLogCap {
		o.log = o.log[len(o.log)-oauthLogCap:]
	}
}

// takeFail consumes one queued failure matching the request, if any.
// Callers hold o.mu.
func (o *oauthState) takeFail(endpoint, grantType string) (OAuthFail, bool) {
	for i, f := range o.fails {
		if f.Endpoint != endpoint || (f.GrantType != "" && f.GrantType != grantType) {
			continue
		}
		o.fails[i].Count--
		if o.fails[i].Count <= 0 {
			o.fails = append(o.fails[:i], o.fails[i+1:]...)
		}
		return f, true
	}
	return OAuthFail{}, false
}

// queueFail adds a failure to the queue after filling its defaults.
func (o *oauthState) queueFail(f OAuthFail) error {
	switch f.Endpoint {
	case "prm", "metadata", "register", "token":
	default:
		return fmt.Errorf("oauth fail endpoint must be prm, metadata, register or token, got %q", f.Endpoint)
	}
	if f.Status < 400 {
		return fmt.Errorf("oauth fail status must be an error status, got %d", f.Status)
	}
	if f.Error == "" {
		f.Error = "server_error"
	}
	if f.Count <= 0 {
		f.Count = 1
	}
	o.mu.Lock()
	o.fails = append(o.fails, f)
	o.mu.Unlock()
	return nil
}

// clearFails drops every queued failure.
func (o *oauthState) clearFails() {
	o.mu.Lock()
	o.fails = nil
	o.mu.Unlock()
}

// resetCounters zeroes the tallies and the log. Clients, grants and tokens
// are live facts and stay.
func (o *oauthState) resetCounters() {
	o.mu.Lock()
	o.counters = OAuthCounters{}
	o.log = nil
	o.mu.Unlock()
}

// identityFor resolves an access token to the identity that approved it, or
// gives the reason it is not accepted.
func (o *oauthState) identityFor(token string) (string, string) {
	if token == "" {
		return "", "no bearer token"
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	a, ok := o.access[token]
	switch {
	case !ok:
		return "", "unknown token"
	case a.grant.revoked:
		return "", "token revoked"
	case !time.Now().Before(a.expires):
		return "", "token expired"
	}
	return a.grant.identity, ""
}

// expire makes every live access token of identity, or of everyone when
// identity is empty, fail at the protected resource from now on. Refresh
// tokens are untouched, and a client's own idea of the expiry is not, which is
// exactly an upstream rejecting a token the client still believes is good.
func (o *oauthState) expire(identity string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	n := 0
	for _, a := range o.access {
		if (identity == "" || a.grant.identity == identity) && !a.grant.revoked && now.Before(a.expires) {
			a.expires = now
			n++
		}
	}
	return n
}

// revoke ends whole grants: their access tokens fail at the protected
// resource and their refresh tokens get invalid_grant. It matches grants by
// identity, by one of their tokens, or every grant when both are empty.
func (o *oauthState) revoke(identity, token string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, g := range o.grants {
		if g.revoked {
			continue
		}
		matches := identity == "" && token == ""
		if identity != "" && g.identity == identity {
			matches = true
		}
		if token != "" {
			if a, ok := o.access[token]; ok && a.grant == g {
				matches = true
			}
			if o.refresh[token] == g {
				matches = true
			}
		}
		if matches {
			g.revoked = true
			n++
		}
	}
	return n
}

// issueAccess mints an access token for g. Callers hold o.mu.
func (o *oauthState) issueAccess(g *oauthGrant, ttl time.Duration) string {
	token := "at_" + randomID()
	o.access[token] = &oauthAccess{grant: g, expires: time.Now().Add(ttl)}
	g.accessToken = token
	return token
}

// issueRefresh mints a refresh token for g, retiring the one it replaces.
// Callers hold o.mu.
func (o *oauthState) issueRefresh(g *oauthGrant) string {
	if g.refreshToken != "" {
		delete(o.refresh, g.refreshToken)
	}
	token := "rt_" + randomID()
	o.refresh[token] = g
	g.refreshToken = token
	return token
}

// snapshot returns the readable state.
func (o *oauthState) snapshot() *OAuthState {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := &OAuthState{
		Clients:      make([]OAuthClient, 0, len(o.order)),
		Grants:       make([]OAuthGrant, 0, len(o.grants)),
		Counters:     o.counters,
		PendingFails: append([]OAuthFail{}, o.fails...),
		Log:          append([]OAuthLogEntry{}, o.log...),
	}
	for _, id := range o.order {
		out.Clients = append(out.Clients, *o.clients[id])
	}
	now := time.Now()
	for _, g := range o.grants {
		a := o.access[g.accessToken]
		out.Grants = append(out.Grants, OAuthGrant{
			Identity:        g.identity,
			ClientID:        g.clientID,
			Scope:           g.scope,
			Resource:        g.resource,
			AccessToken:     g.accessToken,
			AccessExpiresAt: a.expires,
			AccessValid:     !g.revoked && now.Before(a.expires),
			RefreshToken:    g.refreshToken,
			Revoked:         g.revoked,
		})
	}
	return out
}

// baseURL is the scheme and host the request reached the fleet on. Every URL
// the server publishes is built from it, so a client that dialled 127.0.0.1 is
// sent back to 127.0.0.1 rather than to a name its network policy may refuse.
func baseURL(r *http.Request) string {
	if r.TLS != nil {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}

// issuer is this server's authorization server identifier.
func (s *Server) issuer(r *http.Request) string {
	return baseURL(r) + "/s/" + s.name + "/as"
}

// authenticate resolves the caller of r and answers 401 itself when the
// caller may not proceed. With OAuth on, the bearer token must be a live
// access token this server's authorization server issued, or one of the static
// bearer tokens, and a refusal carries the challenge discovery starts from.
// The 401 body is plain text: a client that receives a JSON object with an
// error status may read it as a JSON-RPC response and never see the status.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (string, bool) {
	a := s.auth.get()
	if a.OAuth == nil {
		res := s.auth.check(r.Header)
		if !res.OK {
			// A genuine credential rejection, distinct from an injected
			// 401. Both produce 401 on the wire, but only this one reflects
			// what the caller actually presented, and only this one changes
			// when a token is revoked.
			w.Header().Set("WWW-Authenticate", `Bearer realm="chaosmock"`)
			http.Error(w, res.Reason, http.StatusUnauthorized)
			return "", false
		}
		return res.Identity, true
	}

	token := bearerToken(r.Header)
	if identity, ok := a.Bearer[token]; ok && token != "" {
		return identity, true
	}
	identity, reason := s.oauth.identityFor(token)
	if reason == "" {
		return identity, true
	}

	s.oauth.mu.Lock()
	s.oauth.counters.Rejected++
	s.oauth.mu.Unlock()

	challenge := `Bearer realm="chaosmock"`
	if a.OAuth.Challenge != "bare" {
		challenge = fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource%s"`, baseURL(r), r.URL.Path)
		if a.OAuth.ChallengeScope != "" {
			challenge += fmt.Sprintf(`, scope="%s"`, a.OAuth.ChallengeScope)
		}
		if token != "" {
			challenge += `, error="invalid_token"`
		}
	}
	w.Header().Set("WWW-Authenticate", challenge)
	http.Error(w, "invalid_token: "+reason, http.StatusUnauthorized)
	return "", false
}

// failOAuth answers with a queued failure for endpoint, if one is due, and
// reports whether it did.
func (s *Server) failOAuth(w http.ResponseWriter, endpoint, grantType string, entry OAuthLogEntry) bool {
	s.oauth.mu.Lock()
	f, ok := s.oauth.takeFail(endpoint, grantType)
	if ok {
		if endpoint == "token" {
			s.oauth.counters.TokenErrors++
		}
		entry.Status, entry.Error = f.Status, f.Error
		s.oauth.record(entry)
	}
	s.oauth.mu.Unlock()
	if ok {
		writeOAuthError(w, f.Status, f.Error, "injected failure")
	}
	return ok
}

// writeOAuthError writes an RFC 6749 error response.
func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

// ServeProtectedResourceMetadata answers RFC 9728 metadata for the resource at
// resourcePath, one of this server's transport endpoints.
func (s *Server) ServeProtectedResourceMetadata(w http.ResponseWriter, r *http.Request, resourcePath string) {
	cfg := s.auth.get().OAuth
	if cfg == nil {
		http.NotFound(w, r)
		return
	}
	if s.failOAuth(w, "prm", "", OAuthLogEntry{Endpoint: "prm"}) {
		return
	}
	resource := cfg.Resource
	if resource == "" {
		resource = baseURL(r) + resourcePath
	}
	s.oauth.mu.Lock()
	s.oauth.counters.PRM++
	s.oauth.record(OAuthLogEntry{Endpoint: "prm", Status: http.StatusOK, Resource: resource})
	s.oauth.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 resource,
		"authorization_servers":    []string{s.issuer(r)},
		"bearer_methods_supported": []string{"header"},
	})
}

// ServeAuthorizationServerMetadata answers RFC 8414 metadata. It never carries
// a resource field, so a client takes the resource from the protected
// resource metadata, which is where the specification puts it.
func (s *Server) ServeAuthorizationServerMetadata(w http.ResponseWriter, r *http.Request) {
	cfg := s.auth.get().OAuth
	if cfg == nil {
		http.NotFound(w, r)
		return
	}
	if s.failOAuth(w, "metadata", "", OAuthLogEntry{Endpoint: "metadata"}) {
		return
	}
	s.oauth.mu.Lock()
	s.oauth.counters.Metadata++
	s.oauth.record(OAuthLogEntry{Endpoint: "metadata", Status: http.StatusOK})
	s.oauth.mu.Unlock()

	issuer := s.issuer(r)
	meta := map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/authorize",
		"token_endpoint":                        issuer + "/token",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
	}
	if cfg.Registration == nil || *cfg.Registration {
		meta["registration_endpoint"] = issuer + "/register"
	}
	writeJSON(w, http.StatusOK, meta)
}

// ServeOAuth handles the authorization server's own endpoints under
// /s/<name>/as/.
func (s *Server) ServeOAuth(w http.ResponseWriter, r *http.Request, endpoint string) {
	cfg := s.auth.get().OAuth
	if cfg == nil {
		http.NotFound(w, r)
		return
	}
	switch endpoint {
	case "register":
		if cfg.Registration != nil && !*cfg.Registration {
			http.NotFound(w, r)
			return
		}
		s.serveRegister(w, r)
	case "authorize":
		s.serveAuthorize(w, r, cfg)
	case "token":
		s.serveToken(w, r, cfg)
	default:
		http.NotFound(w, r)
	}
}

// serveRegister handles RFC 7591 dynamic client registration. Every call mints
// a new client, so re-registering is observable as a new client_id.
func (s *Server) serveRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.failOAuth(w, "register", "", OAuthLogEntry{Endpoint: "register"}) {
		return
	}
	var req struct {
		ClientName              string   `json:"client_name"`
		RedirectURIs            []string `json:"redirect_uris"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		Scope                   string   `json:"scope"`
	}
	// Unknown metadata is accepted, as RFC 7591 asks of a server.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "body is not a JSON object")
		return
	}
	if len(req.RedirectURIs) == 0 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required")
		return
	}
	method := req.TokenEndpointAuthMethod
	if method == "" {
		method = "client_secret_basic" // the RFC 7591 default
	}

	s.oauth.mu.Lock()
	s.oauth.dcr++
	client := OAuthClient{
		ClientID:     fmt.Sprintf("dcr-%d-%s", s.oauth.dcr, randomID()[:8]),
		RedirectURIs: req.RedirectURIs,
		AuthMethod:   method,
		Scope:        req.Scope,
		Dynamic:      true,
	}
	if method != "none" {
		client.ClientSecret = "cs_" + randomID()
	}
	s.oauth.putClient(client)
	s.oauth.counters.Register++
	s.oauth.record(OAuthLogEntry{Endpoint: "register", Status: http.StatusCreated, ClientID: client.ClientID,
		RedirectURI: req.RedirectURIs[0], Scope: req.Scope})
	s.oauth.mu.Unlock()

	resp := map[string]any{
		"client_id":                  client.ClientID,
		"client_id_issued_at":        time.Now().Unix(),
		"client_name":                req.ClientName,
		"redirect_uris":              req.RedirectURIs,
		"grant_types":                req.GrantTypes,
		"response_types":             req.ResponseTypes,
		"token_endpoint_auth_method": method,
	}
	if client.ClientSecret != "" {
		resp["client_secret"] = client.ClientSecret
	}
	if req.Scope != "" {
		resp["scope"] = req.Scope
	}
	writeJSON(w, http.StatusCreated, resp)
}

// serveAuthorize approves an authorization request at once. An unknown client
// or redirect_uri is answered here rather than redirected, as RFC 6749 4.1.2.1
// requires, since redirecting would send the error somewhere unverified.
// Everything else returns to the redirect_uri, with a code or an error.
func (s *Server) serveAuthorize(w http.ResponseWriter, r *http.Request, cfg *OAuthConfig) {
	q := r.URL.Query()
	entry := OAuthLogEntry{
		Endpoint:    "authorize",
		ClientID:    q.Get("client_id"),
		RedirectURI: q.Get("redirect_uri"),
		Scope:       q.Get("scope"),
		Resource:    q.Get("resource"),
		State:       q.Get("state"),
		LoginHint:   q.Get("login_hint"),
	}

	s.oauth.mu.Lock()
	defer s.oauth.mu.Unlock()
	s.oauth.counters.Authorize++

	client, known := s.oauth.clients[entry.ClientID]
	if !known || !client.allows(entry.RedirectURI) || (entry.RedirectURI == "" && len(client.RedirectURIs) != 1) {
		entry.Status, entry.Error = http.StatusBadRequest, "invalid_request"
		s.oauth.record(entry)
		http.Error(w, "unknown client_id or unregistered redirect_uri", http.StatusBadRequest)
		return
	}
	if entry.RedirectURI == "" {
		entry.RedirectURI = client.RedirectURIs[0]
	}

	entry.Identity = entry.LoginHint
	if entry.Identity == "" {
		entry.Identity = cfg.ApproveAs
	}
	if entry.Identity == "" {
		entry.Identity = "user"
	}

	switch {
	case q.Get("response_type") != "code":
		entry.Error = "unsupported_response_type"
	case q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256":
		entry.Error = "invalid_request"
	case cfg.Deny:
		entry.Error = "access_denied"
	}

	params := url.Values{}
	if entry.State != "" {
		params.Set("state", entry.State)
	}
	if entry.Error != "" {
		params.Set("error", entry.Error)
	} else {
		code := "code_" + randomID()
		s.oauth.codes[code] = &oauthCode{
			clientID:    client.ClientID,
			redirectURI: entry.RedirectURI,
			challenge:   q.Get("code_challenge"),
			scope:       entry.Scope,
			resource:    entry.Resource,
			identity:    entry.Identity,
			expires:     time.Now().Add(oauthCodeTTL),
		}
		params.Set("code", code)
	}
	location, err := withQuery(entry.RedirectURI, params)
	if err != nil {
		entry.Status, entry.Error = http.StatusBadRequest, "invalid_request"
		s.oauth.record(entry)
		http.Error(w, "redirect_uri is not a URL", http.StatusBadRequest)
		return
	}
	entry.Status = http.StatusFound
	s.oauth.record(entry)
	http.Redirect(w, r, location, http.StatusFound)
}

// withQuery adds params to raw, keeping the query it already has.
func withQuery(raw string, params url.Values) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for k, vs := range params {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// serveToken exchanges a code or a refresh token. Client authentication takes
// the secret from the form or from HTTP Basic, since clients differ on which
// they send.
func (s *Server) serveToken(w http.ResponseWriter, r *http.Request, cfg *OAuthConfig) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "body is not a form")
		return
	}
	form := r.PostForm
	entry := OAuthLogEntry{
		Endpoint:     "token",
		GrantType:    form.Get("grant_type"),
		ClientID:     form.Get("client_id"),
		ClientSecret: form.Get("client_secret"),
		RedirectURI:  form.Get("redirect_uri"),
		Resource:     form.Get("resource"),
		RefreshToken: form.Get("refresh_token"),
	}
	if id, secret, ok := r.BasicAuth(); ok {
		entry.ClientID, entry.ClientSecret = id, secret
	}

	s.oauth.mu.Lock()
	switch entry.GrantType {
	case "authorization_code":
		s.oauth.counters.TokenCode++
	case "refresh_token":
		s.oauth.counters.TokenRefresh++
	}
	s.oauth.mu.Unlock()

	if s.failOAuth(w, "token", entry.GrantType, entry) {
		return
	}

	s.oauth.mu.Lock()
	defer s.oauth.mu.Unlock()

	fail := func(status int, code, description string) {
		s.oauth.counters.TokenErrors++
		entry.Status, entry.Error = status, code
		s.oauth.record(entry)
		writeOAuthError(w, status, code, description)
	}

	client, known := s.oauth.clients[entry.ClientID]
	if !known || (client.ClientSecret != "" && entry.ClientSecret != client.ClientSecret) {
		fail(http.StatusUnauthorized, "invalid_client", "unknown client or wrong client_secret")
		return
	}

	ttl := time.Duration(cfg.AccessTTL)
	if ttl <= 0 {
		ttl = time.Hour
	}

	var g *oauthGrant
	issueRefresh := true
	switch entry.GrantType {
	case "authorization_code":
		code, ok := s.oauth.codes[form.Get("code")]
		switch {
		case !ok:
			fail(http.StatusBadRequest, "invalid_grant", "unknown authorization code")
			return
		case code.used:
			fail(http.StatusBadRequest, "invalid_grant", "authorization code already used")
			return
		case !time.Now().Before(code.expires):
			fail(http.StatusBadRequest, "invalid_grant", "authorization code expired")
			return
		case code.clientID != client.ClientID:
			fail(http.StatusBadRequest, "invalid_grant", "authorization code was issued to another client")
			return
		case code.redirectURI != entry.RedirectURI:
			fail(http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the authorization request")
			return
		}
		verifier := form.Get("code_verifier")
		if verifier == "" {
			entry.PKCE = "missing"
			fail(http.StatusBadRequest, "invalid_request", "code_verifier is required")
			return
		}
		sum := sha256.Sum256([]byte(verifier))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != code.challenge {
			entry.PKCE = "mismatch"
			fail(http.StatusBadRequest, "invalid_grant", "code_verifier does not match the code_challenge")
			return
		}
		entry.PKCE = "ok"
		if entry.Resource != "" && code.resource != "" && entry.Resource != code.resource {
			fail(http.StatusBadRequest, "invalid_target", "resource does not match the authorization request")
			return
		}
		code.used = true
		resource := code.resource
		if resource == "" {
			resource = entry.Resource
		}
		g = &oauthGrant{identity: code.identity, clientID: client.ClientID, scope: code.scope, resource: resource}
		s.oauth.grants = append(s.oauth.grants, g)
	case "refresh_token":
		var ok bool
		g, ok = s.oauth.refresh[entry.RefreshToken]
		switch {
		case !ok:
			fail(http.StatusBadRequest, "invalid_grant", "unknown or retired refresh token")
			return
		case g.revoked:
			fail(http.StatusBadRequest, "invalid_grant", "refresh token revoked")
			return
		case g.clientID != client.ClientID:
			fail(http.StatusBadRequest, "invalid_grant", "refresh token was issued to another client")
			return
		}
		issueRefresh = cfg.RotateRefresh
	default:
		fail(http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
		return
	}

	entry.Identity, entry.Scope = g.identity, g.scope
	entry.IssuedAccessToken = s.oauth.issueAccess(g, ttl)
	resp := map[string]any{
		"access_token": entry.IssuedAccessToken,
		"token_type":   "Bearer",
		"expires_in":   int64(math.Ceil(ttl.Seconds())),
	}
	if issueRefresh {
		entry.IssuedRefreshToken = s.oauth.issueRefresh(g)
		resp["refresh_token"] = entry.IssuedRefreshToken
	}
	if g.scope != "" {
		resp["scope"] = g.scope
	}
	entry.Status = http.StatusOK
	s.oauth.record(entry)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}
