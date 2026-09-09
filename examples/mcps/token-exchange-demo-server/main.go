package main

// token-exchange-demo-server is a plain MCP resource server for exercising
// Bifrost's `token_exchange` MCP auth type end-to-end against a REAL identity
// provider (Okta, Entra, Keycloak, Auth0, or any generic OIDC provider).
//
// Unlike oauth-demo-server, this server is NOT an authorization server and
// implements no OAuth dance of its own. `token_exchange` moves all of that
// logic into Bifrost + your identity provider; this server's only job is to
// validate whatever bearer token arrives on each request — exactly what a
// real internal MCP server behind token exchange would do — and prove which
// caller identity it received.
//
// Validation is full OIDC discovery + JWKS signature verification + issuer +
// audience checks via github.com/coreos/go-oidc/v3. Nothing here trusts an
// unverified token.
//
// Setup
// ─────
//  1. At your identity provider, register a dedicated application authorized
//     to perform token exchange (or the on-behalf-of grant) for an audience —
//     see docs/mcp/auth/token-exchange.mdx for provider-specific notes.
//  2. Start this server with TOKEN_EXCHANGE_ISSUER_URL and
//     TOKEN_EXCHANGE_AUDIENCE set to that provider's issuer and the audience
//     you registered.
//  3. In Bifrost, create an MCP client with auth_type "token_exchange"
//     pointing at this server, using the SAME audience and the exchange
//     app's client_id/client_secret (see the printed config example below).
//  4. Call the `whoami` tool as different identity-authenticated callers and
//     confirm each response names the correct caller — that's the delegated
//     identity proving it made it all the way through the exchange.
//
// A request with no bearer token, an invalid signature, the wrong issuer, or
// the wrong audience is rejected with HTTP 401 before the MCP server ever
// sees it — there is no fallback identity here, matching token_exchange's
// "delegated only" design.
import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ctxKey is a private type so context values don't collide with other packages'.
type ctxKey string

const claimsCtxKey ctxKey = "token_exchange_claims"

// oidcNetworkTimeout bounds every outbound call this server makes to the
// identity provider: startup discovery, JWKS refreshes, and per-request
// token verification.
const oidcNetworkTimeout = 10 * time.Second

// maxRequestBodyBytes bounds every incoming request body this server reads,
// since NewStreamableHTTPServer buffers the body upfront with no built-in limit.
const maxRequestBodyBytes = 1 << 20 // 1 MiB

// callerClaims is the subset of the exchanged token's claims the demo surfaces.
// Field names follow standard OIDC/OAuth claim conventions; providers vary in
// which of the optional ones they actually populate.
type callerClaims struct {
	Subject  string `json:"sub"`
	Email    string `json:"email,omitempty"`
	Name     string `json:"name,omitempty"`
	Audience any    `json:"aud,omitempty"`
	Issuer   string `json:"iss,omitempty"`
	Expiry   int64  `json:"exp,omitempty"`
}

func main() {
	issuerURL := strings.TrimSpace(os.Getenv("TOKEN_EXCHANGE_ISSUER_URL"))
	audience := strings.TrimSpace(os.Getenv("TOKEN_EXCHANGE_AUDIENCE"))
	if issuerURL == "" || audience == "" {
		log.Fatal("TOKEN_EXCHANGE_ISSUER_URL and TOKEN_EXCHANGE_AUDIENCE are both required")
	}

	port := "3004"
	if p := os.Getenv("MCP_SERVER_PORT"); p != "" {
		port = p
	}
	addr := fmt.Sprintf("localhost:%s", port)

	// OIDC discovery happens once at startup, reading the identity provider's
	// standard discovery document (/.well-known/openid-configuration). If
	// this fails, the issuer URL is wrong or unreachable; fix that before
	// anything else, since every request would 401 otherwise.
	//
	// The finite-timeout client is passed through oidc.ClientContext so
	// go-oidc reuses it for JWKS refreshes too (Provider.remoteKeySet stores
	// the client captured here and rebuilds it with context.Background() —
	// a bare discovery-context deadline alone would not bound that refresh).
	oidcHTTPClient := &http.Client{Timeout: oidcNetworkTimeout}
	discoveryCtx, discoveryCancel := context.WithTimeout(context.Background(), oidcNetworkTimeout)
	defer discoveryCancel()
	provider, err := oidc.NewProvider(oidc.ClientContext(discoveryCtx, oidcHTTPClient), issuerURL)
	if err != nil {
		log.Fatalf("OIDC discovery against %s failed: %v", issuerURL, err)
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: audience})

	mcpServer := server.NewMCPServer("token-exchange-demo-server", "1.0.0")

	whoamiTool := mcp.NewTool(
		"whoami",
		mcp.WithDescription("Returns the identity claims from the caller's exchanged token — proves whose identity actually reached this server"),
	)
	mcpServer.AddTool(whoamiTool, whoamiHandler)

	echoTool := mcp.NewTool(
		"echo",
		mcp.WithDescription("Echo back the input message, tagged with the caller's identity"),
		mcp.WithString("message", mcp.Required(), mcp.Description("Message to echo")),
	)
	mcpServer.AddTool(echoTool, echoHandler)

	httpServer := server.NewStreamableHTTPServer(mcpServer)
	handler := bearerVerifyMiddleware(verifier)(httpServer)

	log.Printf("token-exchange-demo-server listening on http://%s/", addr)
	log.Printf("Validating bearer tokens against issuer=%s audience=%s", issuerURL, audience)
	log.Printf("\nBifrost config:")
	log.Printf(`
{
  "name": "token_exchange_demo",
  "connection_type": "http",
  "connection_string": "http://%s/",
  "auth_type": "token_exchange",
  "token_exchange": {
    "audience": "%s",
    "client_id": "<your exchange application's client_id>",
    "client_secret": "<your exchange application's client_secret>"
  },
  "tools_to_execute": ["*"]
}
`, addr, audience)

	srv := &http.Server{
		Addr:              addr,
		Handler:           http.MaxBytesHandler(handler, maxRequestBodyBytes),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// bearerVerifyMiddleware rejects any request without a bearer token that
// verifies against the configured identity provider: valid signature,
// matching issuer, matching audience, and unexpired. This is the entire auth
// surface of the server — there is no fallback credential, mirroring
// token_exchange's delegated-only design on the Bifrost side.
func bearerVerifyMiddleware(verifier *oidc.IDTokenVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			unauthorized := func(reason string) {
				log.Printf("[mcp] %s %s -> 401 (%s)", r.Method, r.URL.Path, reason)
				http.Error(w, "unauthorized: "+reason, http.StatusUnauthorized)
			}

			h := r.Header.Get("Authorization")
			if h == "" {
				unauthorized("missing Authorization header")
				return
			}
			if !strings.HasPrefix(h, "Bearer ") {
				unauthorized("Authorization scheme must be Bearer")
				return
			}
			rawToken := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))

			verifyCtx, verifyCancel := context.WithTimeout(r.Context(), oidcNetworkTimeout)
			defer verifyCancel()
			idToken, err := verifier.Verify(verifyCtx, rawToken)
			if err != nil {
				// Covers a bad signature, wrong issuer, wrong audience, and
				// expiry — the verifier enforces all of them.
				unauthorized("token verification failed: " + err.Error())
				return
			}

			var claims callerClaims
			if err := idToken.Claims(&claims); err != nil {
				unauthorized("failed to parse token claims: " + err.Error())
				return
			}
			claims.Issuer = idToken.Issuer
			claims.Expiry = idToken.Expiry.Unix()

			// Caller identity (sub/email) is deliberately not logged here:
			// this is a routine authentication log, and those are stable,
			// directly-identifying claims. whoami/echo still surface the
			// full identity in their tool responses, where it's the
			// point of the demo rather than an incidental log line.
			log.Printf("[mcp] %s %s authed (token expires %s)",
				r.Method, r.URL.Path, time.Until(idToken.Expiry).Round(time.Second))

			ctx := context.WithValue(r.Context(), claimsCtxKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// whoamiHandler returns the identity claims from the caller's exchanged
// token — the main point of this server: proving that each caller's own
// identity reached the upstream server, not a shared credential.
func whoamiHandler(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	claims, ok := ctx.Value(claimsCtxKey).(callerClaims)
	if !ok {
		return mcp.NewToolResultError("no caller claims in context — this should be unreachable past bearerVerifyMiddleware"), nil
	}
	body, err := json.MarshalIndent(claims, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to marshal claims: %v", err)), nil
	}
	return mcp.NewToolResultText(string(body)), nil
}

// echoHandler echoes the message back tagged with the caller's identity, so
// you can see the delegated identity attached to an otherwise ordinary tool
// call, not just the dedicated whoami tool.
func echoHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Message string `json:"message"`
	}
	if err := parseArgs(req, &args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	claims, _ := ctx.Value(claimsCtxKey).(callerClaims)
	who := claims.Subject
	if claims.Email != "" {
		who = claims.Email
	}
	return mcp.NewToolResultText(fmt.Sprintf("[as %s] Echo: %s", who, args.Message)), nil
}

// parseArgs decodes the tool call's arguments into dst via a JSON round-trip,
// matching the pattern used by the other example servers in this directory.
func parseArgs(req mcp.CallToolRequest, dst any) error {
	argBytes, err := json.Marshal(req.Params.Arguments)
	if err != nil {
		return fmt.Errorf("failed to parse arguments: %w", err)
	}
	if err := json.Unmarshal(argBytes, dst); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}
