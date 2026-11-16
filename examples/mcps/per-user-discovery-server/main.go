// per-user-discovery-server is a test fixture for Bifrost's per-user discovery:
// MCP clients with per_user_discovery on, which keep a separate discovery
// (tools, instructions, resources, resource templates and prompts) for every
// caller, made with that caller's own credential.
//
// One process serves the same set of caller profiles through two doors:
//
//	/oauth/mcp    per_user_oauth: a mock OAuth 2.1 authorization server
//	              (RFC 9728 and 8414 discovery, RFC 7591 registration, PKCE)
//	              issues Bearer tokens bound to the username typed at sign-in
//	/headers/mcp  per_user_headers: X-User-Key "key-<name>" names the caller,
//	              X-Workspace "beta" adds a tool
//
// The caller's name picks their profile: alice and bob discover the same
// things, carol and dave differ from them in tools or in prompts, others carry
// tool details, a tool named connect, hyphenated names, 150 paginated tools,
// a pinned legacy protocol, or a failure. A name no profile has gets alice's
// content under its own identity. /admin changes tools, injects faults,
// revokes and expires credentials, and counts calls at runtime. See README.md.
package main

import (
	"flag"
	"log"
	"net/http"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", "localhost:3005", "address to listen on")
	issuer := flag.String("issuer", "", "OAuth issuer and public base URL (default http://<addr>)")
	accessTTL := flag.Duration("access-ttl", 10*time.Minute, "lifetime of an OAuth access token")
	refreshTTL := flag.Duration("refresh-ttl", 24*time.Hour, "lifetime of an OAuth refresh token, kept across rotations")
	slowDelay := flag.Duration("slow-delay", 12*time.Second, "how long the slow profile takes to list its tools")
	flag.Parse()

	base := *issuer
	if base == "" {
		base = "http://" + *addr
	}
	f := newFixture(config{
		issuer:     strings.TrimSuffix(base, "/"),
		accessTTL:  *accessTTL,
		refreshTTL: *refreshTTL,
		slowDelay:  *slowDelay,
	})

	log.Printf("per-user-discovery-server listening on http://%s", *addr)
	log.Printf("  OAuth door:   %s/oauth/mcp    (per_user_oauth, sign in with a profile name)", f.cfg.issuer)
	log.Printf("  Headers door: %s/headers/mcp  (per_user_headers, X-User-Key: key-<profile>, X-Workspace: main|beta)", f.cfg.issuer)
	log.Printf("  Control API:  %s/admin/state", f.cfg.issuer)
	log.Printf("  Profiles:     %s", strings.Join(f.profileNames(), ", "))
	log.Printf("  Access-token TTL %s, refresh-token TTL %s, slow profile delay %s", *accessTTL, *refreshTTL, *slowDelay)

	if err := http.ListenAndServe(*addr, f.handler()); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
