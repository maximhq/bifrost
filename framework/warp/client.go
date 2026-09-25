package warp

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Warp calls the gateway's own Bifrost client in-process, not over HTTP.
//
// It used to run a private Bifrost instance whose one OpenAI-transport provider
// pointed at a base URL - by default this deployment's own /openai mount, as
// the browser saw it. That URL is the page origin, and a server behind
// Tailscale, a reverse proxy or split DNS frequently cannot reach its own public
// origin, so Warp failed on exactly the deployments least able to fix it with a
// setting. Calling the client directly has no address to get wrong.
//
// The full plugin pipeline still runs, deliberately: Warp's calls are logged
// (as app Warp, grouped by conversation), governed and priced like any other
// traffic, which is what the HTTP round trip gave the default setup too. The
// HTTP headers that used to carry Warp's per-call settings are set as the
// context values the HTTP transport would have derived from them - see
// warpInferenceContext.

// ResponsesExecutor is the narrow part of the gateway client Warp chats
// through. A function rather than *bifrost.Bifrost so tests can supply one
// without standing up a gateway, the same shape as EmbeddingExecutor.
type ResponsesExecutor func(*schemas.BifrostContext, *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError)

// requestTarget returns the provider and model a turn's calls go to.
//
// A model the operator typed provider-qualified ("vertex/gemini-2.5-pro") is
// routed by its own prefix, so the qualified form keeps meaning what it meant
// when it rode through the OpenAI-compatible mount. Only a known Bifrost
// provider counts as a prefix: native slugs such as "meta/llama-3-8b" carry a
// slash of their own and stay whole under the configured provider.
func requestTarget(config *schemas.WarpConfig) (schemas.ModelProvider, string) {
	return schemas.ParseModelString(config.Model, config.Provider)
}

// catalogModel strips the provider prefix for a pricing lookup.
//
// An operator may configure a qualified name, but the model catalog keys on the
// bare one - so looking up "anthropic/claude-sonnet-5"
// matches nothing and the turn silently prices at zero, which is worse than no
// figure at all because it reads as free.
func catalogModel(model string) string {
	// Only a real provider segment comes off. Cutting at the first slash turned
	// "meta/llama-3-8b" into "llama-3-8b", which matches no catalog entry - the
	// same silent zero price this function exists to prevent, arrived at from
	// the other direction.
	_, bare := schemas.ParseModelString(model, "")
	return bare
}

// costProviderFor names the provider a turn should be priced against.
//
// Pricing follows routing: requestTarget routes a provider-qualified model by
// its own prefix, so "vertex/gemini-2.5-pro" under Provider "anthropic" runs on
// Vertex - and pricing it against Anthropic's rate card reports a wrong or zero
// cost.
func costProviderFor(config *schemas.WarpConfig) schemas.ModelProvider {
	provider, _ := requestTarget(config)
	return provider
}

// ConversationHeader labels Warp's own model calls with the thread they
// belong to.
//
// The x-bf-lh- prefix is the logging plugin's own convention: everything after
// it becomes a metadata key on the log row, filterable through
// SearchFilters.MetadataFilters. A question that took five model calls would
// otherwise land as five unrelated rows with nothing tying them together. Warp
// makes no HTTP request, so this travels in the request-headers map the logging
// plugin reads, not on the wire.
const ConversationHeader = "x-bf-lh-warp-conversation-id"

// UserAgent labels Warp's traffic so the logs can tell it apart.
//
// Bifrost derives a log row's app from the User-Agent, so this is what turns
// Warp's own calls into a named client in the Logs view instead of an anonymous
// share of "API". It matters more here than for a normal integration: Warp reads
// the same table it writes to, and the indexer skips rows carrying it, so being
// able to see - and filter out - its own traffic is what keeps its answers about
// the deployment rather than about itself. Matched by schemas.Warp.
const UserAgent = "bifrost-warp/1"

// excludeMCPToolsValue is the MCP client allowlist Warp's calls carry. Warp's
// calls run through the gateway like any other client's, so without a filter
// they would pick up every MCP server this deployment has configured for real
// end-user traffic - none of which Warp ever calls. Those tool declarations are
// not bounded the way Warp's own tool results and history are, and on a
// deployment with several MCP servers they can dwarf Warp's own ~10 tools by
// fifty times or more, resent on every iteration of the research loop - enough
// on its own to exceed a 200k-token context window on a single question. The
// value names no real MCP client, so the per-client match in core/mcp excludes
// every one of them; it narrows only Warp's calls.
const excludeMCPToolsValue = "warp-excludes-all-mcp-tools"

// grantKey carries the dashboard request's grant into a turn's context.
type grantKey struct{}

// WithGrant attaches the grant the transport settled for the dashboard request
// that started a turn.
//
// Governance refuses any request that carries no grant, since one nobody
// settled an identity on would answer every access question wrongly. An HTTP
// inference request gets its grant from the transport; Warp's calls never pass
// through it, so the chat handler settles one from its own request and every
// model call of the turn carries it - the same way a realtime turn carries its
// session's grant. It rides as a plain value because the turn's context is a
// snapshot, not a BifrostContext: fasthttp recycles the request under it.
func WithGrant(ctx context.Context, g schemas.Grant) context.Context {
	if g == nil {
		return ctx
	}
	return context.WithValue(ctx, grantKey{}, g)
}

// GrantFromContext returns the grant WithGrant attached, or nil.
func GrantFromContext(ctx context.Context) schemas.Grant {
	g, _ := ctx.Value(grantKey{}).(schemas.Grant)
	return g
}

// NewChat binds the gateway client to one turn's config and conversation.
//
// The request context is the turn's own, so the query scope and caller identity
// the transport snapshotted travel with the inference call - governance and
// logging attribute Warp's spend to the dashboard user who asked.
func NewChat(executor ResponsesExecutor, config *schemas.WarpConfig, conversationID string) ChatFunc {
	return func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		bifrostCtx, cancel := warpInferenceContext(ctx, config, conversationID)
		defer cancel()
		return executor(bifrostCtx, req)
	}
}

// warpInferenceContext builds the context for one of Warp's model calls.
//
// Each value here is what the HTTP transport's context builder would have set
// from the header Warp used to send, so the plugins downstream see Warp exactly
// as they saw it over HTTP:
//
//   - request headers: User-Agent and the conversation label, which the logging
//     plugin reads only from this map (app detection and x-bf-lh- metadata).
//   - session id (x-bf-session-id): a thread stays on one provider key, so
//     prompt caches and per-key rate-limit state survive between turns.
//   - api key id (x-bf-api-key-id): the key the settings pin, if any.
//   - MCP include clients (x-bf-mcp-include-clients): see excludeMCPToolsValue.
//   - the grant the chat handler settled: see WithGrant.
//
// The deadline is Warp's per-call timeout. The provider's own network timeout
// still applies underneath it; whichever is shorter wins.
func warpInferenceContext(ctx context.Context, config *schemas.WarpConfig, conversationID string) (*schemas.BifrostContext, context.CancelFunc) {
	timeout := time.Duration(schemas.WarpDefaultRequestTimeoutSeconds) * time.Second
	if config != nil {
		timeout = time.Duration(config.EffectiveRequestTimeoutSeconds()) * time.Second
	}
	bifrostCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, timeout)
	// See WithGrant: without it governance refuses the call outright.
	if g := GrantFromContext(ctx); g != nil {
		bifrostCtx.SetGrant(g)
	}
	headers := map[string]string{"user-agent": UserAgent}
	bifrostCtx.SetValue(schemas.MCPContextKeyIncludeClients, []string{excludeMCPToolsValue})
	if conversationID != "" {
		headers[ConversationHeader] = conversationID
		bifrostCtx.SetValue(schemas.BifrostContextKeySessionID, conversationID)
	}
	if config != nil && config.APIKeyID != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyAPIKeyID, config.APIKeyID)
	}
	bifrostCtx.SetValue(schemas.BifrostContextKeyRequestHeaders, headers)
	return bifrostCtx, cancel
}
