package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// startFixture serves a fixture on a local port; zero durations in cfg get
// test-sized defaults.
func startFixture(t *testing.T, cfg config) (*fixture, string) {
	t.Helper()
	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	if cfg.accessTTL == 0 {
		cfg.accessTTL = time.Minute
	}
	if cfg.refreshTTL == 0 {
		cfg.refreshTTL = time.Hour
	}
	if cfg.slowDelay == 0 {
		cfg.slowDelay = 200 * time.Millisecond
	}
	cfg.issuer = ts.URL
	f := newFixture(cfg)
	handler = f.handler()
	return f, ts.URL
}

type session struct {
	*client.Client
	init *mcp.InitializeResult
}

// connect opens an MCP session to url with headers.
func connect(t *testing.T, url string, headers map[string]string) (*session, error) {
	t.Helper()
	c, err := client.NewStreamableHttpClient(url, transport.WithHTTPHeaders(headers))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		return nil, err
	}
	init, err := c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{ClientInfo: mcp.Implementation{Name: "fixture-test", Version: "1.0.0"}}})
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	t.Cleanup(func() { _ = c.Close() })
	return &session{Client: c, init: init}, nil
}

func mustConnect(t *testing.T, url string, headers map[string]string) *session {
	t.Helper()
	s, err := connect(t, url, headers)
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	return s
}

func headersFor(identity string) map[string]string {
	return map[string]string{"X-User-Key": "key-" + identity, "X-Workspace": "main"}
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func toolNames(t *testing.T, s *session) []string {
	t.Helper()
	result, err := s.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := make([]string, len(result.Tools))
	for i, tool := range result.Tools {
		names[i] = tool.Name
	}
	sort.Strings(names)
	return names
}

func callText(t *testing.T, s *session, tool string, args map[string]any) (string, error) {
	t.Helper()
	result, err := s.CallTool(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: tool, Arguments: args}})
	if err != nil {
		return "", err
	}
	if len(result.Content) == 0 {
		return "", nil
	}
	text, _ := result.Content[0].(mcp.TextContent)
	return text.Text, nil
}

// admin sends a control API request and decodes its answer.
func admin(t *testing.T, base, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, base+path, reader)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

// signIn runs the OAuth flow a client such as Bifrost runs, as user.
func signIn(t *testing.T, base, user string) (access, refresh string) {
	t.Helper()
	const redirectURI = "http://localhost:9/callback"
	resp, err := http.Post(base+"/register", "application/json", strings.NewReader(`{"client_name":"fixture-test","redirect_uris":["`+redirectURI+`"]}`))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	var registered struct {
		ClientID string `json:"client_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&registered)
	resp.Body.Close()

	verifier := "verifier-" + randHex(16)
	sum := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {registered.ClientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"state":                 {"state-1"},
		"user":                  {user},
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = noFollow.Get(base + "/authorize?" + query.Encode())
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize answered %d, want 302", resp.StatusCode)
	}
	location, _ := url.Parse(resp.Header.Get("Location"))
	if got := location.Query().Get("iss"); got != base {
		t.Fatalf("authorize redirect iss = %q, want %q", got, base)
	}

	resp, err = http.PostForm(base+"/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {location.Query().Get("code")},
		"redirect_uri":  {redirectURI},
		"client_id":     {registered.ClientID},
		"code_verifier": {verifier},
	})
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	defer resp.Body.Close()
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tokens)
	if tokens.AccessToken == "" {
		t.Fatalf("token grant answered %d with no access token", resp.StatusCode)
	}
	return tokens.AccessToken, tokens.RefreshToken
}

func refreshTokens(t *testing.T, base, refresh string) (int, string, string) {
	t.Helper()
	resp, err := http.PostForm(base+"/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	defer resp.Body.Close()
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tokens)
	return resp.StatusCode, tokens.AccessToken, tokens.RefreshToken
}

func TestProfiles_EachCallerDiscoversItsProfilesTools(t *testing.T) {
	_, base := startFixture(t, config{})
	notes := []string{"create_note", "search_notes", "whoami"}
	cases := map[string][]string{
		"alice":  notes,
		"bob":    notes,
		"carol":  {"create_note", "delete_note", "search_notes", "whoami"},
		"dave":   notes,
		"erin":   notes,
		"frank":  {},
		"grace":  {"connect", "whoami"},
		"heidi":  {"fetch-item", "item-count", "whoami"},
		"legacy": notes,
		"admin":  {"admin_audit", "whoami"},
		"zoe":    notes, // no profile of that name: alice's tools
	}
	for identity, want := range cases {
		t.Run(identity, func(t *testing.T) {
			if got := toolNames(t, mustConnect(t, base+"/headers/mcp", headersFor(identity))); !slices.Equal(got, want) {
				t.Fatalf("tools = %v, want %v", got, want)
			}
		})
	}
}

func TestWhoami_NamesTheCallerOnEachDoor(t *testing.T) {
	_, base := startFixture(t, config{})
	text, err := callText(t, mustConnect(t, base+"/headers/mcp", headersFor("zoe")), "whoami", nil)
	if err != nil || text != "door=headers identity=zoe profile=alice workspace=main" {
		t.Fatalf("headers whoami = %q, %v", text, err)
	}
	access, _ := signIn(t, base, "Bob")
	text, err = callText(t, mustConnect(t, base+"/oauth/mcp", bearer(access)), "whoami", nil)
	if err != nil || text != "door=oauth identity=bob profile=bob workspace=main" {
		t.Fatalf("oauth whoami = %q, %v", text, err)
	}
	text, err = callText(t, mustConnect(t, base+"/oauth/mcp", bearer(access)), "search_notes", map[string]any{"query": "plans"})
	if err != nil || !strings.Contains(text, "tool=search_notes identity=bob") {
		t.Fatalf("a call reaches the upstream as its caller, got %q, %v", text, err)
	}
}

func TestHeaders_TheBetaWorkspaceAddsATool(t *testing.T) {
	_, base := startFixture(t, config{})
	headers := headersFor("alice")
	headers["X-Workspace"] = "beta"
	if got := toolNames(t, mustConnect(t, base+"/headers/mcp", headers)); !slices.Contains(got, "beta_search") {
		t.Fatalf("beta tools = %v, want beta_search among them", got)
	}
	if got := toolNames(t, mustConnect(t, base+"/headers/mcp", headersFor("alice"))); slices.Contains(got, "beta_search") {
		t.Fatalf("main tools = %v, want no beta_search", got)
	}
}

// A beta_search removed through the control API stays removed whether or not the beta server was
// built before the removal.
func TestHeaders_ARemovedBetaToolStaysRemovedOnALaterBetaConnection(t *testing.T) {
	_, base := startFixture(t, config{})
	admin(t, base, http.MethodDelete, "/admin/profiles/alice/tools/beta_search", nil)
	headers := headersFor("alice")
	headers["X-Workspace"] = "beta"
	if got := toolNames(t, mustConnect(t, base+"/headers/mcp", headers)); slices.Contains(got, "beta_search") {
		t.Fatalf("beta tools after removing beta_search = %v", got)
	}
}

// A beta_search added through the control API replaces the built-in one whether the beta server was
// built before or after the addition, so the order callers connect in never changes what they see.
func TestHeaders_AnAddedBetaToolWinsWhetherTheBetaServerWasBuiltBeforeOrAfter(t *testing.T) {
	const added = "The runtime beta search."
	betaHeaders := func() map[string]string {
		headers := headersFor("alice")
		headers["X-Workspace"] = "beta"
		return headers
	}
	betaSearch := func(t *testing.T, base string) string {
		t.Helper()
		result, err := mustConnect(t, base+"/headers/mcp", betaHeaders()).ListTools(context.Background(), mcp.ListToolsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		idx := slices.IndexFunc(result.Tools, func(tool mcp.Tool) bool { return tool.Name == "beta_search" })
		if idx < 0 {
			t.Fatal("no beta_search")
		}
		return result.Tools[idx].Description
	}

	t.Run("added before the first beta connection", func(t *testing.T) {
		_, base := startFixture(t, config{})
		admin(t, base, http.MethodPost, "/admin/profiles/alice/tools", map[string]string{"name": "beta_search", "description": added})
		if got := betaSearch(t, base); got != added {
			t.Fatalf("beta_search description = %q, want the added tool's", got)
		}
	})
	t.Run("added after it", func(t *testing.T) {
		_, base := startFixture(t, config{})
		toolNames(t, mustConnect(t, base+"/headers/mcp", betaHeaders())) // builds the beta server first
		admin(t, base, http.MethodPost, "/admin/profiles/alice/tools", map[string]string{"name": "beta_search", "description": added})
		if got := betaSearch(t, base); got != added {
			t.Fatalf("beta_search description = %q, want the added tool's", got)
		}
	})
}

func TestHeaders_AKeyOfTheWrongShapeIsRefused(t *testing.T) {
	_, base := startFixture(t, config{})
	if _, err := connect(t, base+"/headers/mcp", map[string]string{"X-User-Key": "alice"}); err == nil {
		t.Fatal("a key that is not key-<name> connected")
	}
}

func TestOAuth_ARequestWithoutATokenIsChallengedTowardsTheMetadata(t *testing.T) {
	_, base := startFixture(t, config{})
	resp, err := http.Post(base+"/oauth/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	challenge := resp.Header.Get("WWW-Authenticate")
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(challenge, base+"/.well-known/oauth-protected-resource/oauth/mcp") {
		t.Fatalf("got %d with WWW-Authenticate %q", resp.StatusCode, challenge)
	}
	resp, err = http.Get(base + "/.well-known/oauth-protected-resource/oauth/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var metadata struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&metadata)
	if metadata.Resource != base+"/oauth/mcp" || !slices.Equal(metadata.AuthorizationServers, []string{base}) {
		t.Fatalf("protected resource metadata = %+v", metadata)
	}
}

func TestOAuth_ExpireForcesARefreshThatRotatesTheTokens(t *testing.T) {
	_, base := startFixture(t, config{})
	access, refresh := signIn(t, base, "alice")
	mustConnect(t, base+"/oauth/mcp", bearer(access))

	if status, body := admin(t, base, http.MethodPost, "/admin/expire", map[string]string{"identity": "alice"}); status != http.StatusOK || body["expired"] != float64(1) {
		t.Fatalf("expire answered %d %v", status, body)
	}
	if _, err := connect(t, base+"/oauth/mcp", bearer(access)); err == nil {
		t.Fatal("an expired access token still connected")
	}
	status, newAccess, _ := refreshTokens(t, base, refresh)
	if status != http.StatusOK {
		t.Fatalf("refresh answered %d", status)
	}
	mustConnect(t, base+"/oauth/mcp", bearer(newAccess))
	if status, _, _ := refreshTokens(t, base, refresh); status != http.StatusBadRequest {
		t.Fatalf("a rotated refresh token answered %d, want 400", status)
	}
}

func TestRevoke_RefusesBothDoorsAndRefreshUntilRestored(t *testing.T) {
	_, base := startFixture(t, config{})
	access, refresh := signIn(t, base, "alice")
	admin(t, base, http.MethodPost, "/admin/revoke", map[string]string{"identity": "alice"})

	if _, err := connect(t, base+"/headers/mcp", headersFor("alice")); err == nil {
		t.Fatal("a revoked headers caller connected")
	}
	if _, err := connect(t, base+"/oauth/mcp", bearer(access)); err == nil {
		t.Fatal("a revoked OAuth caller connected")
	}
	mustConnect(t, base+"/headers/mcp", headersFor("bob"))
	if status, _, _ := refreshTokens(t, base, refresh); status != http.StatusBadRequest {
		t.Fatalf("a revoked caller's refresh answered %d, want 400", status)
	}

	admin(t, base, http.MethodPost, "/admin/restore", map[string]string{"identity": "alice"})
	mustConnect(t, base+"/headers/mcp", headersFor("alice"))
}

// A refresh refused during a revoke leaves the refresh token in place, so the same token refreshes
// once the revoke is lifted.
func TestRevoke_ARefreshRefusedDuringItWorksAfterRestore(t *testing.T) {
	_, base := startFixture(t, config{})
	_, refresh := signIn(t, base, "alice")
	admin(t, base, http.MethodPost, "/admin/revoke", map[string]string{"identity": "alice"})
	if status, _, _ := refreshTokens(t, base, refresh); status != http.StatusBadRequest {
		t.Fatalf("a revoked caller's refresh answered %d, want 400", status)
	}

	admin(t, base, http.MethodPost, "/admin/restore", map[string]string{"identity": "alice"})
	status, access, _ := refreshTokens(t, base, refresh)
	if status != http.StatusOK || access == "" {
		t.Fatalf("refresh after restore answered %d, want 200 with new tokens", status)
	}
	mustConnect(t, base+"/oauth/mcp", bearer(access))
}

func TestDetails_ErinsToolsCarryTitlesIconsAnnotationsSchemasAndMeta(t *testing.T) {
	_, base := startFixture(t, config{})
	result, err := mustConnect(t, base+"/headers/mcp", headersFor("erin")).ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	idx := slices.IndexFunc(result.Tools, func(tool mcp.Tool) bool { return tool.Name == "search_notes" })
	if idx < 0 {
		t.Fatal("erin has no search_notes")
	}
	tool := result.Tools[idx]
	switch {
	case tool.Title != "Search notes":
		t.Fatalf("title = %q", tool.Title)
	case len(tool.Icons) != 1 || tool.Icons[0].MIMEType != "image/svg+xml":
		t.Fatalf("icons = %+v", tool.Icons)
	case tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint:
		t.Fatalf("annotations = %+v", tool.Annotations)
	case tool.OutputSchema.Type != "object" && len(tool.RawOutputSchema) == 0:
		t.Fatal("no output schema")
	case tool.Meta == nil || tool.Meta.AdditionalFields["example.com/owner"] != "notes-team":
		t.Fatalf("_meta = %+v", tool.Meta)
	}

	plain, err := mustConnect(t, base+"/headers/mcp", headersFor("alice")).ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range plain.Tools {
		if tool.Title != "" || len(tool.Icons) > 0 {
			t.Fatalf("alice's %s carries details: %+v", tool.Name, tool)
		}
	}
}

// Erin's search_notes answers with the structured results its output schema requires.
func TestDetails_ErinsSearchNotesAnswersWithTheResultsItsSchemaRequires(t *testing.T) {
	_, base := startFixture(t, config{})
	result, err := mustConnect(t, base+"/headers/mcp", headersFor("erin")).CallTool(context.Background(),
		mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "search_notes", Arguments: map[string]any{"query": "roadmap"}}})
	if err != nil {
		t.Fatal(err)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content = %#v, want an object", result.StructuredContent)
	}
	if _, ok := structured["results"].([]any); !ok {
		t.Fatalf("structured content = %#v, want a results array", structured)
	}
	if len(result.Content) == 0 {
		t.Fatal("no text content for clients that do not read structured content")
	}
}

func TestResourcesAndPrompts_DifferByProfileAndAreNotOfferedByFrank(t *testing.T) {
	_, base := startFixture(t, config{})
	ctx := context.Background()
	lists := func(identity string) (resources, templates, prompts []string) {
		s := mustConnect(t, base+"/headers/mcp", headersFor(identity))
		res, err := s.ListResources(ctx, mcp.ListResourcesRequest{})
		if err != nil {
			t.Fatalf("%s resources/list: %v", identity, err)
		}
		for _, r := range res.Resources {
			resources = append(resources, r.URI)
		}
		tmpl, err := s.ListResourceTemplates(ctx, mcp.ListResourceTemplatesRequest{})
		if err != nil {
			t.Fatalf("%s resources/templates/list: %v", identity, err)
		}
		for _, r := range tmpl.ResourceTemplates {
			templates = append(templates, r.URITemplate.Raw())
		}
		pr, err := s.ListPrompts(ctx, mcp.ListPromptsRequest{})
		if err != nil {
			t.Fatalf("%s prompts/list: %v", identity, err)
		}
		for _, p := range pr.Prompts {
			prompts = append(prompts, p.Name)
		}
		return resources, templates, prompts
	}
	resources, templates, prompts := lists("alice")
	if !slices.Equal(resources, []string{"notes://handbook"}) || !slices.Equal(templates, []string{"notes://note/{id}"}) || !slices.Equal(prompts, []string{"summarize_notes"}) {
		t.Fatalf("alice: %v %v %v", resources, templates, prompts)
	}
	resources, _, prompts = lists("dave")
	if !slices.Equal(resources, []string{"notes://archive"}) || !slices.Equal(prompts, []string{"weekly_review"}) {
		t.Fatalf("dave: %v %v", resources, prompts)
	}

	frank := mustConnect(t, base+"/headers/mcp", headersFor("frank"))
	if caps := frank.init.Capabilities; caps.Resources != nil || caps.Prompts != nil || frank.init.Instructions != "" {
		t.Fatalf("frank offers resources %v, prompts %v, instructions %q", caps.Resources, caps.Prompts, frank.init.Instructions)
	}
	if alice := mustConnect(t, base+"/headers/mcp", headersFor("alice")); !strings.HasPrefix(alice.init.Instructions, "Notes workspace") {
		t.Fatalf("alice's instructions = %q", alice.init.Instructions)
	}
}

func TestFaults_BuiltInProfilesFailOrWaitAsNamed(t *testing.T) {
	f, base := startFixture(t, config{})
	ctx := context.Background()
	if _, err := mustConnect(t, base+"/headers/mcp", headersFor("faulty")).ListTools(ctx, mcp.ListToolsRequest{}); err == nil || !strings.Contains(err.Error(), "injected fault") {
		t.Fatalf("faulty tools/list err = %v", err)
	}

	partial := mustConnect(t, base+"/headers/mcp", headersFor("partial"))
	if got := toolNames(t, partial); len(got) != 3 {
		t.Fatalf("partial tools = %v", got)
	}
	if _, err := partial.ListResources(ctx, mcp.ListResourcesRequest{}); err == nil {
		t.Fatal("partial resources/list succeeded")
	}
	if _, err := partial.ListPrompts(ctx, mcp.ListPromptsRequest{}); err == nil {
		t.Fatal("partial prompts/list succeeded")
	}

	slow := mustConnect(t, base+"/headers/mcp", headersFor("slow"))
	start := time.Now()
	toolNames(t, slow)
	if elapsed := time.Since(start); elapsed < f.cfg.slowDelay {
		t.Fatalf("slow tools/list took %s, want at least %s", elapsed, f.cfg.slowDelay)
	}
}

func TestFaults_RuntimeFaultsHitOnlyWhatTheyNameUntilCleared(t *testing.T) {
	_, base := startFixture(t, config{})
	status, _ := admin(t, base, http.MethodPost, "/admin/faults", fault{Identity: "bob", Method: "tools/call", Tool: "search_notes", Kind: faultError})
	if status != http.StatusOK {
		t.Fatalf("adding a fault answered %d", status)
	}
	bob := mustConnect(t, base+"/headers/mcp", headersFor("bob"))
	if _, err := callText(t, bob, "search_notes", map[string]any{"query": "q"}); err == nil {
		t.Fatal("bob's faulted call succeeded")
	}
	if _, err := callText(t, bob, "whoami", nil); err != nil {
		t.Fatalf("bob's other tool failed: %v", err)
	}
	if _, err := callText(t, mustConnect(t, base+"/headers/mcp", headersFor("alice")), "search_notes", map[string]any{"query": "q"}); err != nil {
		t.Fatalf("alice's call failed: %v", err)
	}

	admin(t, base, http.MethodPost, "/admin/faults", fault{Profile: "carol", Method: "tools/list", Kind: faultHTTP401})
	if _, err := mustConnect(t, base+"/headers/mcp", headersFor("carol")).ListTools(context.Background(), mcp.ListToolsRequest{}); err == nil {
		t.Fatal("carol's tools/list succeeded under an http401 fault")
	}

	admin(t, base, http.MethodDelete, "/admin/faults", nil)
	if _, err := callText(t, bob, "search_notes", map[string]any{"query": "q"}); err != nil {
		t.Fatalf("bob's call failed after the faults were cleared: %v", err)
	}
	if status, _ := admin(t, base, http.MethodPost, "/admin/faults", fault{Kind: "explode"}); status != http.StatusBadRequest {
		t.Fatalf("an unknown fault kind answered %d, want 400", status)
	}
}

func TestAdmin_ToolChangesShowInTheProfilesNextListUntilReset(t *testing.T) {
	_, base := startFixture(t, config{})
	alice := mustConnect(t, base+"/headers/mcp", headersFor("alice"))
	toolNames(t, alice) // builds alice's server before the change

	admin(t, base, http.MethodPost, "/admin/profiles/alice/tools", map[string]string{"name": "export_notes"})
	admin(t, base, http.MethodDelete, "/admin/profiles/alice/tools/create_note", nil)
	if got := toolNames(t, alice); !slices.Equal(got, []string{"export_notes", "search_notes", "whoami"}) {
		t.Fatalf("alice's tools after the change = %v", got)
	}
	if got := toolNames(t, mustConnect(t, base+"/headers/mcp", headersFor("bob"))); !slices.Equal(got, []string{"create_note", "search_notes", "whoami"}) {
		t.Fatalf("bob's tools changed with alice's: %v", got)
	}
	if status, _ := admin(t, base, http.MethodPost, "/admin/profiles/nobody/tools", map[string]string{"name": "x"}); status != http.StatusNotFound {
		t.Fatalf("an unknown profile answered %d, want 404", status)
	}

	admin(t, base, http.MethodPost, "/admin/reset", nil)
	if got := toolNames(t, mustConnect(t, base+"/headers/mcp", headersFor("alice"))); !slices.Equal(got, []string{"create_note", "search_notes", "whoami"}) {
		t.Fatalf("alice's tools after reset = %v", got)
	}
}

func TestProtocol_LegacyFallsBackToInitializeAndOthersNegotiateTheLatest(t *testing.T) {
	_, base := startFixture(t, config{})
	if got := mustConnect(t, base+"/headers/mcp", headersFor("legacy")).ProtocolVersion(); got != mcp.LATEST_LEGACY_PROTOCOL_VERSION {
		t.Fatalf("legacy negotiated %q, want %q", got, mcp.LATEST_LEGACY_PROTOCOL_VERSION)
	}
	if got := mustConnect(t, base+"/headers/mcp", headersFor("alice")).ProtocolVersion(); got != mcp.LATEST_PROTOCOL_VERSION {
		t.Fatalf("alice negotiated %q, want %q", got, mcp.LATEST_PROTOCOL_VERSION)
	}
}

func TestPagination_IvanListsHisToolsFiftyAPage(t *testing.T) {
	_, base := startFixture(t, config{})
	ivan := mustConnect(t, base+"/headers/mcp", headersFor("ivan"))
	page, err := ivan.ListToolsByPage(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tools) != 50 || page.NextCursor == "" {
		t.Fatalf("first page holds %d tools, next cursor %q", len(page.Tools), page.NextCursor)
	}
	if got := toolNames(t, ivan); len(got) != 150 {
		t.Fatalf("every page holds %d tools, want 150", len(got))
	}
}

func TestCalls_AreCountedPerIdentityAndMethod(t *testing.T) {
	_, base := startFixture(t, config{})
	alice := mustConnect(t, base+"/headers/mcp", headersFor("alice"))
	toolNames(t, alice)
	if _, err := callText(t, alice, "whoami", nil); err != nil {
		t.Fatal(err)
	}
	_, calls := admin(t, base, http.MethodGet, "/admin/calls?identity=alice", nil)
	if calls["tools/list"] != float64(1) || calls["tools/call:whoami"] != float64(1) {
		t.Fatalf("alice's calls = %v", calls)
	}
	if _, bobCalls := admin(t, base, http.MethodGet, "/admin/calls?identity=bob", nil); len(bobCalls) != 0 {
		t.Fatalf("bob's calls = %v", bobCalls)
	}
}
