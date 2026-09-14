package opencode

import (
	"crypto/sha256"
	"encoding/hex"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
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

// opencodeSessionSigner returns a BodySigner emitting the transport-captured
// affinity value from BifrostContextKeyOpencodeSession. It returns nil when no
// session was captured, leaving the request untouched. The shared OpenAI
// handlers apply signer headers after SetExtraHeaders, so the per-request
// value always wins over any static network_config.extra_headers entry.
func opencodeSessionSigner(ctx *schemas.BifrostContext) providerUtils.BodySigner {
	if ctx == nil {
		return nil
	}
	session, _ := ctx.Value(schemas.BifrostContextKeyOpencodeSession).(string)
	if session == "" {
		return nil
	}
	headerValue := namespaceOpencodeSession(opencodeNamespaceInput(ctx), session)
	return func(_ []byte) (map[string]string, *schemas.BifrostError) {
		return map[string]string{opencodeSessionHeader: headerValue}, nil
	}
}
