package opencode

import (
	"strings"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
)

// OpencodeSessionHeader is the header the OpenCode Zen/Go gateways use to
// identify a conversation/session. Starting 2026-09-06 the Go gateway requires
// it and may reject requests that omit it. Bifrost resolves a session identity
// once per request (see ResolveOpencodeSession) and attaches it only on
// upstream calls to the OpenCode provider family.
const OpencodeSessionHeader = "x-opencode-session"

// isSafeOpencodeSessionValue reports whether a caller-supplied session value is
// safe to forward as an HTTP header value: it must contain no CR, LF, or any
// other control character. This is header-injection protection — a value that
// fails validation is treated as absent and the fallback chain applies instead
// of the raw value ever reaching the wire.
func isSafeOpencodeSessionValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return false
		}
	}
	return true
}

// ResolveOpencodeSession resolves the x-opencode-session value for a request
// and returns it, namespaced per virtual key. The result is resolved exactly
// once per request: the first call computes it and caches it on the context
// (BifrostContextKeyOpencodeSession), and every later call — retries,
// zen→go fallbacks, subsequent MCP agent-loop turns on the same request —
// returns the cached value, so the upstream sees one stable identity for the
// whole request.
//
// Resolution order, preserving the existing "client wins" and "poison-to-none"
// session identity rules from core/schemas:
//
//	a) the client-sent x-opencode-session request header, forwarded verbatim
//	   after charset validation (isSafeOpencodeSessionValue). A value failing
//	   validation is treated as absent.
//	b) the request's session identity as already resolved by Bifrost
//	   (BifrostContextKeySessionID): x-bf-session-id wins, then the coding
//	   harness's own session headers (Claude Code, Codex CLI, OpenCode, ...).
//	c) if no signal at all: a fresh per-request UUID. This keeps the request
//	   compliant with the gateway requirement but provides no affinity across
//	   turns — multi-turn session tracking remains the client's responsibility.
//
// The resolved value is qualified with the virtual key so sessions from
// different virtual keys cannot collide upstream.
func ResolveOpencodeSession(ctx *schemas.BifrostContext) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.GetOrStoreReservedValue(schemas.BifrostContextKeyOpencodeSession, func() any {
		return resolveOpencodeSessionValue(ctx)
	}).(string)
	return value
}

func resolveOpencodeSessionValue(ctx *schemas.BifrostContext) string {
	// a) Client-sent header wins. The HTTP transport captures every request
	// header into BifrostContextKeyRequestHeaders (lowercased keys), so the raw
	// x-opencode-session is visible here even though ingress does not otherwise
	// special-case it.
	if headers, ok := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string); ok {
		if raw := headers[OpencodeSessionHeader]; raw != "" && strings.TrimSpace(raw) != "" && isSafeOpencodeSessionValue(raw) {
			return namespaceOpencodeSession(ctx, raw)
		}
	}
	// b) BifrostContextKeySessionID already encodes the shared session identity
	// rules (x-bf-session-id wins, poison-to-none on an invalid explicit value,
	// then harness session headers), so reuse it rather than re-deriving.
	// The value is still re-checked with the same charset rule as the client
	// header: NormalizeSessionID only gates length, so a value carrying control
	// bytes would otherwise reach the upstream inside the namespaced header.
	if sessionID, ok := ctx.Value(schemas.BifrostContextKeySessionID).(string); ok && sessionID != "" && isSafeOpencodeSessionValue(sessionID) {
		return namespaceOpencodeSession(ctx, sessionID)
	}
	// c) No signal at all: synthesize a fresh per-request UUID so the request
	// stays compliant. No affinity — the caller must send its own session
	// identity for multi-turn tracking.
	return namespaceOpencodeSession(ctx, uuid.New().String())
}

// namespaceOpencodeSession qualifies a resolved session value with the request's
// virtual key, so the same client session used under two different virtual keys
// cannot collide upstream. The virtual key itself is validated with the same
// charset rule as session values; an absent or unsafe virtual key leaves the
// value unqualified rather than dropping the request.
//
// Each component is escaped before joining so the only literal ":" in the
// result is the separator we insert. Without escaping, ("a", "b:c") and
// ("a:b", "c") both produce "a:b:c" — a collision. Escaping doubles every
// backslash and prefixes every colon with a backslash, making the mapping
// injective: distinct (vk, value) pairs always yield distinct namespaced
// strings. The escaped output stays within isSafeOpencodeSessionValue's
// charset (only printable ASCII is introduced).
func namespaceOpencodeSession(ctx *schemas.BifrostContext, value string) string {
	virtualKey, ok := ctx.Value(schemas.BifrostContextKeyVirtualKey).(string)
	if !ok || strings.TrimSpace(virtualKey) == "" || !isSafeOpencodeSessionValue(virtualKey) {
		return value
	}
	return escapeSessionComponent(virtualKey) + ":" + escapeSessionComponent(value)
}

// escapeSessionComponent makes a single session component unambiguous when
// joined with ":" as a separator. Backslashes are doubled first, then colons
// are prefixed with a backslash, so the only literal ":" in the final
// namespaced string is the separator inserted by namespaceOpencodeSession.
func escapeSessionComponent(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ":", "\\:")
	return s
}
