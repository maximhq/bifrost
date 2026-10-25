// Package mcpvouch holds the mark core puts on an MCP tool call it vouches for,
// naming why (schemas.MCPToolCallKind): a provider-injected tool runs past the
// client and request tool filters and the tool permits, and a connect tool is
// allowed by access to its client.
//
// A plugin holds the request context, can lift BlockRestrictedWrites, and can read and
// copy every user value through GetUserValues. So the mark is not a user value: it
// lives in the context's trusted store under a key of an unexported type. Only code
// under core/ can import this package, and code that cannot name the key can neither
// plant the mark, read it, nor copy it to another context.
package mcpvouch

import (
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
)

type contextKey struct{}

type vouch struct {
	kind       schemas.MCPToolCallKind
	clientName string        // MCP client that owns the tool
	toolName   string        // prefixed tool name, "<client>-<tool>"
	connect    *connectCheck // set by SetConnect, which decides kind once the caller is known
}

// connectCheck settles whether a call named like a client's connect tool is that
// connect tool. See SetConnect.
type connectCheck struct {
	decide func(ctx *schemas.BifrostContext) (isConnect, callerKnown bool)

	mu        sync.Mutex
	settled   bool
	isConnect bool
}

func (c *connectCheck) kind(ctx *schemas.BifrostContext) schemas.MCPToolCallKind {
	c.mu.Lock()
	defer c.mu.Unlock()
	isConnect := c.isConnect
	if !c.settled {
		var callerKnown bool
		isConnect, callerKnown = c.decide(ctx)
		if callerKnown {
			c.settled, c.isConnect = true, isConnect
		}
	}
	if isConnect {
		return schemas.MCPToolCallKindConnect
	}
	return schemas.MCPToolCallKindNone
}

// Set marks ctx as running toolName of clientName, vouched for as kind.
func Set(ctx *schemas.BifrostContext, kind schemas.MCPToolCallKind, clientName, toolName string) {
	ctx.SetTrustedValue(contextKey{}, vouch{kind: kind, clientName: clientName, toolName: toolName})
}

// SetConnect marks ctx as running toolName, which is clientName's connect tool
// unless the caller's discovery holds an upstream tool of that name. Which it is
// depends on the caller, and a request may name its caller only once a policy
// plugin has resolved its credential, after the mark is set. So decide answers
// from the context it is asked with and reports whether the caller was known;
// the first answer given with the caller known is kept for the rest of the call,
// so the plugins that authorize it and the code that runs it agree.
func SetConnect(ctx *schemas.BifrostContext, clientName, toolName string, decide func(ctx *schemas.BifrostContext) (isConnect, callerKnown bool)) {
	ctx.SetTrustedValue(contextKey{}, vouch{clientName: clientName, toolName: toolName, connect: &connectCheck{decide: decide}})
}

// Kind returns why ctx's mark vouches for exactly this client's tool, or
// MCPToolCallKindNone. It never vouches for a sibling tool or another client's
// tool of the same name.
func Kind(ctx *schemas.BifrostContext, clientName, toolName string) schemas.MCPToolCallKind {
	v, ok := ctx.TrustedValue(contextKey{}).(vouch)
	if !ok || v.toolName == "" || v.toolName != toolName || v.clientName != clientName {
		return schemas.MCPToolCallKindNone
	}
	if v.connect != nil {
		return v.connect.kind(ctx)
	}
	return v.kind
}
