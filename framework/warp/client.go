package warp

import (
	"context"
	"maps"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/streaming"
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

// ResponsesStreamExecutor is the gateway client's streaming responses path.
type ResponsesStreamExecutor func(*schemas.BifrostContext, *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError)

// StreamedResponses runs Warp's calls over the streaming responses path and
// hands back the whole response, so the agent loop, topic labelling and theme
// naming keep deciding on a complete step exactly as before.
//
// Streaming is about the call, not the panel: the loop still holds a step's
// text until it has decided whether to show it at all (see Agent.Run). What it
// buys is the stream's own behaviour upstream - a long step is a stream of
// events rather than one silent response, and a provider or model served only
// over streaming can run Warp.
//
// A call that arrives without a trace gets one from the gateway, and that trace
// is this call's to end: see completeOwnTrace.
func StreamedResponses(stream ResponsesStreamExecutor) ResponsesExecutor {
	return func(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		if traceID, _ := ctx.Value(schemas.BifrostContextKeyTraceID).(string); traceID == "" {
			defer completeOwnTrace(ctx)
		}
		chunks, bifrostErr := stream(ctx, req)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		return collectResponsesStream(ctx, chunks)
	}
}

// completeOwnTrace ends the trace the gateway opened for one of Warp's streamed
// calls, once the stream has closed.
//
// The gateway opens a trace for a stream that arrives without one, and the
// logging plugin holds the call's finished row under it until the trace ends.
// Over HTTP the transport ends it; an in-process call has no transport, so
// without this the row waits for the stale sweep, which drops it unwritten and
// Warp's own calls never reach the logs. It runs after the channel has closed,
// when the provider's post-hooks have parked the row.
func completeOwnTrace(ctx *schemas.BifrostContext) {
	tracer, traceID, err := bifrost.GetTracerFromContext(ctx)
	if err != nil {
		return
	}
	if logs := ctx.DrainPluginLogs(); len(logs) > 0 {
		tracer.AttachPluginLogs(traceID, logs)
	}
	tracer.CompleteAndFlushTrace(traceID)
}

// collectResponsesStream reads a responses stream to its end and assembles the
// response it describes.
//
// The output is rebuilt from the events with the same builder the logs use, so
// what Warp reasons over is what its log shows. A provider's terminal event is
// not relied on for it: some carry the full output there and some only usage.
// It is the fallback when the events rebuilt nothing.
//
// The channel is always read until it closes, error or not. The provider's
// goroutine finishes its post-hooks - the log and cost of Warp's own call -
// before closing it, and abandoning the channel would leave that work blocked.
func collectResponsesStream(ctx *schemas.BifrostContext, stream chan *schemas.BifrostStreamChunk) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	var (
		events    []*streaming.ResponsesStreamChunk
		terminal  *schemas.BifrostResponsesResponse
		usage     *schemas.ResponsesResponseUsage
		streamErr *schemas.BifrostError
	)
	for chunk := range stream {
		if chunk == nil || streamErr != nil {
			continue
		}
		if chunk.BifrostError != nil {
			streamErr = chunk.BifrostError
			continue
		}
		event := chunk.BifrostResponsesStreamResponse
		if event == nil {
			continue
		}
		if event.Type == schemas.ResponsesStreamResponseTypeError {
			streamErr = streamEventError(event.Message, event.Error)
			continue
		}
		// Indexed by arrival, which is the stream's order. The builder sorts by
		// index, and providers that leave ChunkIndex unset would otherwise tie.
		events = append(events, &streaming.ResponsesStreamChunk{StreamResponse: event, ChunkIndex: len(events)})
		if event.Response == nil {
			continue
		}
		if event.Response.Usage != nil {
			usage = event.Response.Usage
		}
		switch event.Type {
		case schemas.ResponsesStreamResponseTypeCompleted, schemas.ResponsesStreamResponseTypeIncomplete:
			terminal = event.Response
		case schemas.ResponsesStreamResponseTypeFailed:
			streamErr = streamEventError(nil, event.Response.Error)
		}
	}
	if streamErr != nil {
		return nil, streamErr
	}
	// A stream cut short by a cancelled or expired call is not a short answer.
	// Without a terminal event it could end inside a tool call's arguments, and
	// the loop would run that tool on half of them.
	if terminal == nil && ctx != nil && ctx.Err() != nil {
		return nil, streamEventError(new(ctx.Err().Error()), nil)
	}
	response := &schemas.BifrostResponsesResponse{}
	if terminal != nil {
		copied := *terminal
		response = &copied
	}
	if output := streaming.BuildResponsesMessagesFromChunks(events); len(output) > 0 {
		response.Output = output
	}
	if usage != nil {
		response.Usage = usage
	}
	return response, nil
}

// streamEventError turns an error carried inside the stream into the error a
// non-streaming call would have returned.
func streamEventError(message *string, responseErr *schemas.ResponsesResponseError) *schemas.BifrostError {
	field := &schemas.ErrorField{Message: "the model stream failed"}
	if responseErr != nil && responseErr.Message != "" {
		field.Message = responseErr.Message
		if responseErr.Code != "" {
			field.Code = new(responseErr.Code)
		}
	} else if message != nil && *message != "" {
		field.Message = *message
	}
	return &schemas.BifrostError{Error: field, ExtraFields: schemas.BifrostErrorExtraFields{RequestType: schemas.ResponsesStreamRequest}}
}

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

// grantKey carries the dashboard request's grant settler into a turn's context.
type grantKey struct{}

// WithGrant attaches how to settle a grant for the dashboard request that
// started a turn.
//
// Governance refuses any request that carries no grant, since one nobody
// settled an identity on would answer every access question wrongly. An HTTP
// inference request gets its grant from the transport; Warp's calls never pass
// through it, so the chat handler hands over a settler built from its own
// request. It rides as a plain value because the turn's context is a snapshot,
// not a BifrostContext: fasthttp recycles the request under it.
//
// A settler rather than one grant, because each model call of a turn is its
// own request. Governance resolves access once per grant and stamps the
// caller's name, teams and customer onto the context only while doing so; a
// grant shared across the turn was resolved on the first call, so every call
// after a tool result was logged with a bare user id and nothing above it.
func WithGrant(ctx context.Context, settle func() schemas.Grant) context.Context {
	if settle == nil {
		return ctx
	}
	return context.WithValue(ctx, grantKey{}, settle)
}

// NewGrantFromContext settles a fresh grant with the settler WithGrant
// attached, or returns nil when there is none.
func NewGrantFromContext(ctx context.Context) schemas.Grant {
	settle, _ := ctx.Value(grantKey{}).(func() schemas.Grant)
	if settle == nil {
		return nil
	}
	return settle()
}

type backgroundWorkKey struct{}

// withBackgroundWork marks a context as Warp's own work, done from a job with
// no dashboard caller behind it.
//
// Such a call has nobody to settle a grant for, and governance refuses a
// request without one. It is not a user's request to govern: it is the
// gateway naming its own clusters, the same standing the embedding calls of
// indexing have. So it skips the plugin pipeline as they do, which also keeps
// it out of the logs it would otherwise be indexed from and clustered with.
func withBackgroundWork(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundWorkKey{}, true)
}

// isBackgroundWork reports whether the context was marked by withBackgroundWork.
func isBackgroundWork(ctx context.Context) bool {
	marked, _ := ctx.Value(backgroundWorkKey{}).(bool)
	return marked
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
//     The same map is presented to guardrail rules as `headers`, which the
//     guardrails HTTP pre-hook would otherwise have captured - without it a
//     rule conditioned on the User-Agent can neither target nor exempt Warp.
//   - session id (x-bf-session-id): a thread stays on one provider key, so
//     prompt caches and per-key rate-limit state survive between turns.
//   - api key id (x-bf-api-key-id): the key the settings pin, if any.
//   - MCP include clients (x-bf-mcp-include-clients): see excludeMCPToolsValue.
//   - a grant of its own, settled for the dashboard caller: see WithGrant.
//
// The deadline is Warp's per-call timeout. The provider's own network timeout
// still applies underneath it; whichever is shorter wins.
func warpInferenceContext(ctx context.Context, config *schemas.WarpConfig, conversationID string) (*schemas.BifrostContext, context.CancelFunc) {
	timeout := time.Duration(schemas.WarpDefaultRequestTimeoutSeconds) * time.Second
	if config != nil {
		timeout = time.Duration(config.EffectiveRequestTimeoutSeconds()) * time.Second
	}
	bifrostCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, timeout)
	// Before the values below, because it clears the pinned key among them.
	if isBackgroundWork(ctx) {
		bifrost.PrepareContextForInternalRequest(bifrostCtx)
	}
	// See WithGrant: without it governance refuses the call outright.
	if g := NewGrantFromContext(ctx); g != nil {
		bifrostCtx.SetGrant(g)
	}
	// A signed-in user nothing grants model access to is still served. Reaching
	// the chat route already proves their role allows Warp, and on a deployment
	// without access profiles no user would ever hold a permit, so refusing them
	// would make Warp admin-only there. Governance reads this as "treat such a
	// user as a key-less request": one who does hold a profile is governed by it
	// as before, and a virtual key the request presents still has to resolve.
	bifrostCtx.SetValue(schemas.BifrostContextKeyAdmitUngrantedUser, true)
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
	bifrostCtx.SetValue(schemas.BifrostContextKeyGuardrailHeaders, maps.Clone(headers))
	return bifrostCtx, cancel
}
