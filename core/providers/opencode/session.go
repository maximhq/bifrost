package opencode

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// opencodeSessionHeader is the upstream backend-affinity header OpenCode uses
// to pin a conversation to one backend so its prompt cache stays warm.
const opencodeSessionHeader = "X-Opencode-Session"

// namespaceOpencodeSession scopes a captured session value to one Bifrost
// tenant. Any caller can assert any session string, and tenants multiplex
// onto few provider keys, so forwarding the raw value would let one tenant
// ride or poison another tenant's cache warmth and hotspot backends. The
// namespace input is already tenant-scoped (see opencodeNamespaceInput) and
// is hashed before use, so no credential or identifier leaves the gateway.
// The namespaced value stays stable per (tenant, conversation), preserving
// per-tenant warmth. Without a tenant scope there is nothing to separate,
// and the raw value is used unchanged.
func namespaceOpencodeSession(namespaceInput, session string) string {
	if namespaceInput == "" {
		return session
	}
	sum := sha256.Sum256([]byte(namespaceInput + "\x00" + session))
	return hex.EncodeToString(sum[:16]) + ":" + session
}

// opencodeNamespaceInput derives the tenant scope a session value is
// namespaced under. It reads the request's settled Grant identity first -
// the resolved virtual-key row ID, else the attributed user ID - so both
// plain virtual-key callers and enterprise users get separated sessions.
// Row/user IDs are attribution identifiers, not bearer secrets, and are
// hashed by namespaceOpencodeSession before anything reaches the wire.
// A request with no settled identity falls back to the presented virtual-key
// string, preserving behavior on paths where no Grant is installed. A
// settled identity that names neither is treated as no scope: per the
// PresentedVirtualKey precedent, whatever the context also carries under the
// key's own name did not authenticate this request.
func opencodeNamespaceInput(ctx *schemas.BifrostContext) string {
	if ctx == nil {
		return ""
	}
	if grant := ctx.Grant(); grant != nil {
		if identity := grant.Identity(); identity != nil {
			if vk := identity.VirtualKey(); vk != nil && vk.ID != "" {
				return vk.ID
			}
			if user := identity.User(); user != nil && user.ID != "" {
				return user.ID
			}
			return ""
		}
	}
	virtualKey, _ := ctx.Value(schemas.BifrostContextKeyVirtualKey).(string)
	return virtualKey
}

// opencodeSessionValue returns the upstream header value for this request, or ""
// when the transport captured no session. The value is namespaced per tenant.
func opencodeSessionValue(ctx *schemas.BifrostContext) string {
	if ctx == nil {
		return ""
	}
	session, _ := ctx.Value(schemas.BifrostContextKeyOpencodeSession).(string)
	if session == "" {
		return ""
	}
	return namespaceOpencodeSession(opencodeNamespaceInput(ctx), session)
}

// opencodeRequestExtraHeaders returns the extra-header map to hand a shared
// OpenAI inference handler, with the per-request affinity value folded in.
//
// The header rides the ordinary extra-headers map rather than a BodySigner
// because the shared handlers build the upstream request on more than one path.
// A signer is invoked with the exact body the handler marshaled, which does not
// exist on the large-payload passthrough (there the transport's deferred reader
// is streamed straight to upstream) - and that path is precisely where a long
// conversation with a large document most needs its cache warmth. extra_headers
// is applied by every one of those paths, so the header cannot be dropped by
// request shape.
//
// The provider's configured map is never mutated: with no captured session the
// shared map is returned as-is, and otherwise a copy is built with every case
// variant of the header dropped, so the resolved value wins over a static
// network_config.extra_headers entry regardless of map iteration order.
func opencodeRequestExtraHeaders(ctx *schemas.BifrostContext, networkHeaders map[string]string) map[string]string {
	value := opencodeSessionValue(ctx)
	if value == "" {
		return networkHeaders
	}
	merged := make(map[string]string, len(networkHeaders)+1)
	for name, configured := range networkHeaders {
		if strings.EqualFold(name, opencodeSessionHeader) {
			continue
		}
		merged[name] = configured
	}
	merged[opencodeSessionHeader] = value
	return merged
}
