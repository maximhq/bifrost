package lib

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/valyala/fasthttp"
)

// VirtualKeyHeaderSource identifies which request header supplied a Bifrost
// virtual key. Every value except VirtualKeyHeaderSourceNone is also the header
// name itself, so callers that need to strip the selected credential can use
// HeaderName instead of maintaining their own switch.
type VirtualKeyHeaderSource string

const (
	VirtualKeyHeaderSourceNone          VirtualKeyHeaderSource = "none"
	VirtualKeyHeaderSourceXBfVK         VirtualKeyHeaderSource = "x-bf-vk"
	VirtualKeyHeaderSourceAuthorization VirtualKeyHeaderSource = "authorization"
	VirtualKeyHeaderSourceXAPIKey       VirtualKeyHeaderSource = "x-api-key"
	VirtualKeyHeaderSourceXGoogAPIKey   VirtualKeyHeaderSource = "x-goog-api-key"
	VirtualKeyHeaderSourceAPIKey        VirtualKeyHeaderSource = "api-key"
)

// HeaderName returns the request header this source names, or "" when the
// source is not a header (no credential was found).
func (s VirtualKeyHeaderSource) HeaderName() string {
	switch s {
	case VirtualKeyHeaderSourceXBfVK, VirtualKeyHeaderSourceAuthorization,
		VirtualKeyHeaderSourceXAPIKey, VirtualKeyHeaderSourceXGoogAPIKey, VirtualKeyHeaderSourceAPIKey:
		return string(s)
	default:
		return ""
	}
}

// ResolveVirtualKeyFromHeaders extracts the Bifrost virtual key from a request
// and reports which header supplied it. It is the single implementation of the
// documented identity precedence, checking each header in order and returning
// the first match:
//
//  1. x-bf-vk        — taken verbatim (no prefix check; the header itself is the signal)
//  2. Authorization  — "Bearer <vk>", where <vk> must start with the VK prefix
//  3. x-api-key      — must start with the VK prefix
//  4. x-goog-api-key — must start with the VK prefix
//  5. api-key        — must start with the VK prefix
//
// The governance.VirtualKeyPrefix gate on the alias headers is what lets real
// provider credentials pass through untouched: only values shaped like a
// Bifrost virtual key are consumed as Bifrost identity, everything else stays
// available to the upstream provider or agent. There are deliberately no
// cross-header conflict checks — first recognized identity wins.
//
// Returns ("", VirtualKeyHeaderSourceNone) when no header carries a virtual key.
func ResolveVirtualKeyFromHeaders(ctx *fasthttp.RequestCtx) (string, VirtualKeyHeaderSource) {
	if value := strings.TrimSpace(string(ctx.Request.Header.Peek(string(schemas.BifrostContextKeyVirtualKey)))); value != "" {
		return value, VirtualKeyHeaderSourceXBfVK
	}

	auth := strings.TrimSpace(string(ctx.Request.Header.Peek("Authorization")))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		token := strings.TrimSpace(auth[7:])
		if strings.HasPrefix(strings.ToLower(token), governance.VirtualKeyPrefix) {
			return token, VirtualKeyHeaderSourceAuthorization
		}
	}

	for _, source := range []VirtualKeyHeaderSource{
		VirtualKeyHeaderSourceXAPIKey,
		VirtualKeyHeaderSourceXGoogAPIKey,
		VirtualKeyHeaderSourceAPIKey,
	} {
		value := strings.TrimSpace(string(ctx.Request.Header.Peek(string(source))))
		if strings.HasPrefix(strings.ToLower(value), governance.VirtualKeyPrefix) {
			return value, source
		}
	}

	return "", VirtualKeyHeaderSourceNone
}
