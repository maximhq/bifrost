// Command mcp-test-client is a small interactive MCP client for exercising a
// running Bifrost /mcp endpoint under any inbound-auth configuration.
//
// It speaks streamable HTTP and supports the two credential styles Bifrost's
// MCP server accepts:
//
//   - header credentials: a virtual key (x-bf-vk / Authorization: Bearer /
//     x-api-key) or a session id (x-bf-mcp-session-id), used for the
//     `headers` and `both` server auth modes; and
//   - OAuth: full RFC 9728/8414 discovery + dynamic client registration + PKCE
//     authorization-code flow, used for the `both` and `oauth` server modes.
//
// Toggle the server-side knobs (mcp_server_auth_mode, enforce_auth_on_inference,
// disable_vk_identity, virtual-key state, ...) however you like on the running
// instance, then `reconnect` here and `list` / `call` tools to observe the
// effect. The OAuth token is held in memory for the session, so `reconnect`
// after a knob flip does not re-prompt unless the token is actually rejected.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// oauthHTTPTimeout bounds each outbound OAuth request (discovery, dynamic
// registration, authorization-URL build, token exchange) so a stalled IdP
// surfaces a recoverable error instead of wedging the CLI.
const oauthHTTPTimeout = 30 * time.Second

type config struct {
	url          string
	auth         string // "headers" or "oauth"
	headers      map[string]string
	scopes       []string
	callbackPort string
	clientID     string
	tokenStore   client.TokenStore // persisted across reconnects so OAuth is not re-run needlessly
}

// headerList collects repeated -header "Key: Value" flags.
type headerList map[string]string

func (h headerList) String() string { return fmt.Sprintf("%v", map[string]string(h)) }
func (h headerList) Set(v string) error {
	k, val, ok := strings.Cut(v, ":")
	if !ok {
		return fmt.Errorf("expected \"Key: Value\", got %q", v)
	}
	k = strings.TrimSpace(k)
	if k == "" {
		return fmt.Errorf("expected non-empty header name in %q", v)
	}
	h[k] = strings.TrimSpace(val)
	return nil
}

func main() {
	extra := headerList{}
	url := flag.String("url", "http://localhost:8080/mcp", "Bifrost /mcp endpoint")
	auth := flag.String("auth", "headers", "credential style: headers | oauth")
	vk := flag.String("vk", "", "virtual key, sent as x-bf-vk (auth=headers)")
	bearer := flag.String("bearer", "", "virtual key sent as Authorization: Bearer (auth=headers)")
	apiKey := flag.String("api-key", "", "virtual key sent as x-api-key (auth=headers)")
	session := flag.String("session", "", "session id, sent as x-bf-mcp-session-id (auth=headers)")
	flag.Var(extra, "header", "extra header \"Key: Value\" (repeatable, any auth)")
	scope := flag.String("scope", "mcp", "comma-separated OAuth scopes (auth=oauth)")
	cbPort := flag.String("callback-port", "8585", "local port for the OAuth redirect callback (auth=oauth)")
	clientID := flag.String("client-id", "", "pre-registered OAuth client id; empty uses dynamic registration (auth=oauth)")
	once := flag.String("once", "", "run a single command and exit, e.g. -once list or -once 'call echo {\"text\":\"hi\"}'")
	flag.Parse()

	headers := map[string]string{}
	if *vk != "" {
		headers["x-bf-vk"] = *vk
	}
	if *bearer != "" {
		headers["Authorization"] = "Bearer " + *bearer
	}
	if *apiKey != "" {
		headers["x-api-key"] = *apiKey
	}
	if *session != "" {
		headers["x-bf-mcp-session-id"] = *session
	}
	for k, v := range extra {
		headers[k] = v
	}

	cfg := &config{
		url:          *url,
		auth:         strings.ToLower(*auth),
		headers:      headers,
		scopes:       splitNonEmpty(*scope),
		callbackPort: *cbPort,
		clientID:     *clientID,
		tokenStore:   client.NewMemoryTokenStore(),
	}

	c, err := connect(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect failed: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	if *once != "" {
		if err := dispatch(c, *once); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		return
	}

	printInfo(cfg)
	fmt.Println("Type 'help' for commands.")
	repl(cfg, c)
}

// connect builds a client for the current config, starts the transport and runs
// the initialize handshake, driving the OAuth browser flow on demand.
func connect(cfg *config) (*client.Client, error) {
	var c *client.Client
	var err error
	switch cfg.auth {
	case "oauth":
		oauthCfg := client.OAuthConfig{
			ClientID:    cfg.clientID,
			RedirectURI: fmt.Sprintf("http://localhost:%s/oauth/callback", cfg.callbackPort),
			Scopes:      cfg.scopes,
			TokenStore:  cfg.tokenStore,
			PKCEEnabled: true,
			// Dedicated client (not http.DefaultClient) so a stalled IdP during
			// discovery / registration / token exchange surfaces as an error
			// instead of wedging the CLI.
			HTTPClient: &http.Client{Timeout: oauthHTTPTimeout},
		}
		c, err = client.NewOAuthStreamableHttpClient(cfg.url, oauthCfg, transport.WithHTTPHeaders(cfg.headers))
	case "headers", "":
		c, err = client.NewStreamableHttpClient(cfg.url, transport.WithHTTPHeaders(cfg.headers))
	default:
		return nil, fmt.Errorf("unknown -auth %q (want headers | oauth)", cfg.auth)
	}
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	if err := withAuthRetry(cfg, c.Start(ctx), func() error { return c.Start(ctx) }); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	res, err := c.Initialize(ctx, initRequest())
	if err != nil {
		if err = withAuthRetry(cfg, err, func() error {
			var e error
			res, e = c.Initialize(ctx, initRequest())
			return e
		}); err != nil {
			return nil, fmt.Errorf("initialize: %w", err)
		}
	}
	fmt.Printf("connected to %s %s\n", res.ServerInfo.Name, res.ServerInfo.Version)
	return c, nil
}

// withAuthRetry runs the OAuth authorization flow if err signals it, then calls
// retry once. For non-OAuth errors it returns err unchanged.
func withAuthRetry(cfg *config, err error, retry func() error) error {
	if err == nil {
		return nil
	}
	if !client.IsOAuthAuthorizationRequiredError(err) {
		return err
	}
	if aerr := authorize(cfg, client.GetOAuthHandler(err)); aerr != nil {
		return aerr
	}
	return retry()
}

func initRequest() mcp.InitializeRequest {
	var req mcp.InitializeRequest
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "bifrost-mcp-test-client", Version: "0.1.0"}
	req.Params.Capabilities = mcp.ClientCapabilities{}
	return req
}

func repl(cfg *config, c *client.Client) {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for {
		fmt.Print("mcp> ")
		if !sc.Scan() {
			return
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		switch firstWord(line) {
		case "quit", "exit":
			return
		case "help":
			printHelp()
		case "info":
			printInfo(cfg)
		case "reconnect":
			c.Close()
			nc, err := connect(cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "reconnect failed: %v\n", err)
				continue
			}
			c = nc
		case "set":
			if err := setHeader(cfg, line); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
			}
		case "unset":
			delete(cfg.headers, strings.TrimSpace(rest(line)))
			fmt.Println("(run 'reconnect' to apply)")
		default:
			if err := dispatch(c, line); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
			}
		}
	}
}

// dispatch runs a single tool command (list / call / desc).
func dispatch(c *client.Client, line string) error {
	ctx := context.Background()
	switch firstWord(line) {
	case "list", "tools":
		res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
		if err != nil {
			return err
		}
		if len(res.Tools) == 0 {
			fmt.Println("(no tools exposed for this credential)")
		}
		for _, t := range res.Tools {
			fmt.Printf("- %s\t%s\n", t.Name, firstLine(t.Description))
		}
		return nil
	case "desc":
		name := strings.TrimSpace(rest(line))
		res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
		if err != nil {
			return err
		}
		for _, t := range res.Tools {
			if t.Name == name {
				b, _ := json.MarshalIndent(t.InputSchema, "", "  ")
				fmt.Printf("%s\n%s\n", t.Description, b)
				return nil
			}
		}
		return fmt.Errorf("tool %q not found", name)
	case "call":
		name, argStr := splitFirst(rest(line))
		if name == "" {
			return fmt.Errorf("usage: call <tool> [json-args]")
		}
		var args any
		if s := strings.TrimSpace(argStr); s != "" {
			if err := json.Unmarshal([]byte(s), &args); err != nil {
				return fmt.Errorf("invalid JSON args: %w", err)
			}
		}
		var req mcp.CallToolRequest
		req.Params.Name = name
		req.Params.Arguments = args
		res, err := c.CallTool(ctx, req)
		if err != nil {
			return err
		}
		printToolResult(res)
		return nil
	default:
		return fmt.Errorf("unknown command %q (try 'help')", firstWord(line))
	}
}

func printToolResult(res *mcp.CallToolResult) {
	if res.IsError {
		fmt.Println("[tool returned isError=true]")
	}
	for _, content := range res.Content {
		if tc, ok := mcp.AsTextContent(content); ok {
			fmt.Println(tc.Text)
		} else {
			b, _ := json.Marshal(content)
			fmt.Println(string(b))
		}
	}
	if res.StructuredContent != nil {
		b, _ := json.MarshalIndent(res.StructuredContent, "", "  ")
		fmt.Printf("structuredContent: %s\n", b)
	}
}

func setHeader(cfg *config, line string) error {
	kv := strings.TrimSpace(rest(line))
	// Split on whichever separator appears first — colon or space — so the
	// "Key: Value" colon form (matching the -header flag syntax) is stored as
	// key "Authorization", not "Authorization:". Header names contain neither a
	// colon nor a space, so the first one is always the name/value boundary.
	i := strings.IndexAny(kv, ": \t")
	if i < 0 {
		return fmt.Errorf("usage: set <header-name> <value>")
	}
	k := strings.TrimSpace(kv[:i])
	v := strings.TrimSpace(kv[i+1:])
	if k == "" {
		return fmt.Errorf("usage: set <header-name> <value>")
	}
	cfg.headers[k] = v
	fmt.Println("(run 'reconnect' to apply)")
	return nil
}

// authorize runs the interactive OAuth authorization-code flow: register a
// client if needed, open the browser, capture the callback, exchange the code.
func authorize(cfg *config, h *transport.OAuthHandler) error {
	fmt.Println("OAuth authorization required — starting the flow...")

	codeVerifier, err := client.GenerateCodeVerifier()
	if err != nil {
		return err
	}
	codeChallenge := client.GenerateCodeChallenge(codeVerifier)
	state, err := client.GenerateState()
	if err != nil {
		return err
	}

	if h.GetClientID() == "" {
		regCtx, cancel := context.WithTimeout(context.Background(), oauthHTTPTimeout)
		err := h.RegisterClient(regCtx, "bifrost-mcp-test-client")
		cancel()
		if err != nil {
			return fmt.Errorf("dynamic client registration: %w", err)
		}
	}

	urlCtx, cancel := context.WithTimeout(context.Background(), oauthHTTPTimeout)
	authURL, err := h.GetAuthorizationURL(urlCtx, state, codeChallenge)
	cancel()
	if err != nil {
		return err
	}

	cbChan := make(chan map[string]string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		params := map[string]string{}
		for k, vs := range r.URL.Query() {
			if len(vs) > 0 {
				params[k] = vs[0]
			}
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><h1>Authorization received</h1>You can close this tab.<script>window.close()</script></body></html>"))
		cbChan <- params
	})
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	ln, err := net.Listen("tcp", "localhost:"+cfg.callbackPort)
	if err != nil {
		return fmt.Errorf("listen on callback port %s: %w", cfg.callbackPort, err)
	}
	defer srv.Close()
	go srv.Serve(ln)

	fmt.Printf("Open this URL to authorize:\n  %s\n", authURL)
	openBrowser(authURL)

	// Bound the wait so a callback that never arrives (browser didn't open, tab
	// closed, or the AS errored before redirecting) surfaces a recoverable error
	// and lets the deferred srv.Close run, instead of hanging until the user
	// kills the process.
	var params map[string]string
	select {
	case params = <-cbChan:
	case <-time.After(3 * time.Minute):
		return fmt.Errorf("timed out waiting for the OAuth callback; re-run the command to retry")
	}
	if e := params["error"]; e != "" {
		return fmt.Errorf("authorization error: %s %s", e, params["error_description"])
	}
	if params["state"] != state {
		return fmt.Errorf("state mismatch (possible CSRF)")
	}
	code := params["code"]
	if code == "" {
		return fmt.Errorf("no authorization code in callback")
	}
	exCtx, cancel := context.WithTimeout(context.Background(), oauthHTTPTimeout)
	err = h.ProcessAuthorizationResponse(exCtx, code, state, codeVerifier)
	cancel()
	if err != nil {
		return fmt.Errorf("code exchange: %w", err)
	}
	fmt.Println("authorization complete.")
	return nil
}

func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	_ = exec.Command(cmd, append(args, url)...).Start()
}

func printInfo(cfg *config) {
	fmt.Printf("url:  %s\nauth: %s\n", cfg.url, cfg.auth)
	if cfg.auth == "oauth" {
		fmt.Printf("scopes: %v  callback: http://localhost:%s/oauth/callback\n", cfg.scopes, cfg.callbackPort)
	}
	if len(cfg.headers) > 0 {
		fmt.Println("headers:")
		for k, v := range cfg.headers {
			fmt.Printf("  %s: %s\n", k, mask(v))
		}
	}
}

func printHelp() {
	fmt.Print(`commands:
  list | tools            list tools visible to the current credential
  desc <tool>             show a tool's description + input schema
  call <tool> [json]      call a tool, e.g. call echo {"text":"hi"}
  set <header> <value>    change/add a header (then 'reconnect' to apply)
  unset <header>          remove a header (then 'reconnect' to apply)
  reconnect               redo start+initialize (use after toggling server knobs)
  info                    show current url / auth / headers
  help                    this text
  quit | exit             leave
`)
}

// helpers

func splitNonEmpty(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstWord(s string) string {
	w, _ := splitFirst(s)
	return w
}

func rest(s string) string {
	_, r := splitFirst(s)
	return r
}

func splitFirst(s string) (string, string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// mask redacts a header value for display. Every header this client sends is a
// credential or test input the user typed, so all values are masked rather than
// matching a substring allowlist (which would leak custom auth headers like
// Cookie or X-Token); the prefix/suffix still lets the user confirm the value.
func mask(val string) string {
	if len(val) <= 8 {
		return "****"
	}
	return val[:4] + "..." + val[len(val)-4:]
}
