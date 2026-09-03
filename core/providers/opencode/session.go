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
// tenant. Any caller can assert any session string, and virtual keys multiplex
// many tenants onto few provider keys, so forwarding the raw value would let
// one tenant ride or poison another tenant's cache warmth and hotspot
// backends. The namespaced value stays stable per (tenant, conversation), so
// per-tenant warmth is preserved. Without a virtual key there is no tenant to
// separate, and the raw value is used unchanged.
func namespaceOpencodeSession(virtualKey, session string) string {
	if virtualKey == "" {
		return session
	}
	sum := sha256.Sum256([]byte(virtualKey + "\x00" + session))
	return hex.EncodeToString(sum[:16]) + ":" + session
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
	virtualKey, _ := ctx.Value(schemas.BifrostContextKeyVirtualKey).(string)
	headerValue := namespaceOpencodeSession(virtualKey, session)
	return func(_ []byte) (map[string]string, *schemas.BifrostError) {
		return map[string]string{opencodeSessionHeader: headerValue}, nil
	}
}
