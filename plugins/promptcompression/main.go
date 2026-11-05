package promptcompression

import "github.com/maximhq/bifrost/core/schemas"

// PluginName is the name under which the plugin is registered.
const PluginName = "prompt_compression"

// Plugin compresses old tool outputs in agent-loop requests.
type Plugin struct {
	s      settings
	logger schemas.Logger
}

// Init creates the plugin. A nil config means all defaults.
func Init(config *Config, logger schemas.Logger) (schemas.LLMPlugin, error) {
	s, err := config.resolve()
	if err != nil {
		return nil, err
	}
	return &Plugin{s: s, logger: logger}, nil
}

// GetName returns the plugin name.
func (p *Plugin) GetName() string { return PluginName }

// Cleanup is a no-op; the plugin holds no resources.
func (p *Plugin) Cleanup() error { return nil }

// PreRequestHook is a pass-through.
func (p *Plugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// Context keys under which PreLLMHook records its token estimates (int values)
// so PostLLMHook can compare them with the provider-reported usage. They are
// set only when the compressed request is actually sent.
const (
	ContextKeyEstimatedBefore schemas.BifrostContextKey = "prompt-compression-estimated-before"
	ContextKeyEstimatedAfter  schemas.BifrostContextKey = "prompt-compression-estimated-after"
)

// contextKeyStream marks a compressed request as streaming so PostLLMHook,
// which core runs once per stream chunk, can log only on the final chunk.
const contextKeyStream schemas.BifrostContextKey = "prompt-compression-stream"

// The compressors are held in package vars so tests can substitute a panicking
// one and verify the fail-open path.
var (
	compressChatFn      = compressChat
	compressResponsesFn = compressResponses
)

// PreLLMHook compresses the conversation of Chat and Responses requests
// (streaming included). It never returns an error or short-circuit: any
// panic or skip condition yields the original request unchanged (fail-open).
//
// Core runs this hook on every attempt and fallback with the same request, so
// the incoming request is never written to; a compressed request is a shallow
// copy whose Input slice is the new one.
func (p *Plugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (result *schemas.BifrostRequest, sc *schemas.LLMPluginShortCircuit, err error) {
	result = req
	defer func() {
		if r := recover(); r != nil {
			p.warn("promptcompression: recovered from panic, sending original request: %v", r)
			result, sc, err = req, nil, nil
		}
	}()
	if req == nil {
		return req, nil, nil
	}
	// Other request types never touch the context: a fallback keeps the request
	// type, so only Chat and Responses attempts can follow a compressed one.
	switch req.RequestType {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest,
		schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
	default:
		return req, nil, nil
	}
	// Core reuses one ctx across MCP agent-loop iterations and fallbacks, so
	// estimates left by an earlier attempt must not describe this request.
	// Looking before clearing keeps the common case allocation-free.
	if ctx.Value(ContextKeyEstimatedBefore) != nil {
		clearEstimates(ctx)
	}
	// Raw bodies bypass the structured Input entirely; editing it would be a no-op at best.
	if raw, _ := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); raw {
		return req, nil, nil
	}
	// Large payload mode streams the body through without the structured Input.
	if large, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool); large {
		return req, nil, nil
	}

	// The compressors report savings over the eligible region only, so a huge
	// fresh (protected by age) output cannot hide a worthwhile cut, and nothing
	// is copied until the request is known to be worth sending compressed.
	var before, after int
	var out *schemas.BifrostRequest
	switch req.RequestType {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		cr := req.ChatRequest
		if cr == nil || len(cr.Input) == 0 {
			return req, nil, nil
		}
		// Automatic caching puts the breakpoint at the end of the prompt, so any edit
		// would invalidate the provider cache.
		if cr.Params != nil && cr.Params.CacheControl != nil {
			return req, nil, nil
		}
		before = estimateChat(cr.Input)
		compressed, st := compressChatFn(cr.Input, p.s)
		if after = p.afterIfWorthwhile(before, st); after < 0 {
			return req, nil, nil
		}
		cc := *cr
		cc.Input = compressed
		rc := *req
		rc.ChatRequest = &cc
		out = &rc
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		rr := req.ResponsesRequest
		if rr == nil || len(rr.Input) == 0 {
			return req, nil, nil
		}
		// Same as Chat: a request-level cache_control asks the provider for automatic
		// caching at the end of the prompt (reading a nil map is safe).
		if rr.Params != nil && rr.Params.ExtraParams["cache_control"] != nil {
			return req, nil, nil
		}
		before = estimateResponses(rr.Input)
		compressed, st := compressResponsesFn(rr.Input, p.s)
		if after = p.afterIfWorthwhile(before, st); after < 0 {
			return req, nil, nil
		}
		rc2 := *rr
		rc2.Input = compressed
		rc := *req
		rc.ResponsesRequest = &rc2
		out = &rc
	default:
		return req, nil, nil
	}

	ctx.SetValue(ContextKeyEstimatedBefore, before)
	ctx.SetValue(ContextKeyEstimatedAfter, after)
	if req.RequestType == schemas.ChatCompletionStreamRequest || req.RequestType == schemas.ResponsesStreamRequest {
		ctx.SetValue(contextKeyStream, true)
	}
	return out, nil, nil
}

// afterIfWorthwhile returns the estimated token count of the compressed
// request, or -1 when the original should be sent: nothing changed, or a
// non-zero MinSavingsTokens is set and the compression saved fewer eligible
// tokens than that.
//
// There is deliberately no "estimate got smaller" check. Every edit makes its
// output strictly shorter in bytes, so a changed request is always smaller on
// the wire, but the estimate (ceil(len/4)) can round a 1-3 byte whitespace
// edit to a saving of zero tokens. Gating on a strictly smaller estimate would
// send such an edit's original text, and then send the edit later, once a real
// trim appears: the edit would show up late and break byte stability.
//
// Stability: edits are fixed once made and the eligible set only grows, so
// while the client's protected (cache-marked) prefix does not move, saved never
// decreases between requests and a threshold never switches compression off.
// When a client moves a cache marker forward, outputs inside the new prefix
// leave the eligible set and revert to the text the client sent (they must stay
// byte-identical to the input); edits outside the prefix stay. The default
// threshold is 0 so that this can never turn the hook off; a larger value can
// switch compression on late and off again when a marker moves.
func (p *Plugin) afterIfWorthwhile(before int, st stats) int {
	if !st.Changed {
		return -1
	}
	// saved can be zero or negative: a dedupe stub replacing many tiny text
	// blocks estimates larger than the blocks. The threshold applies only when
	// set, so the default never holds back an edit.
	saved := st.EligibleBefore - st.EligibleAfter
	if p.s.MinSavingsTokens > 0 && saved < p.s.MinSavingsTokens {
		return -1
	}
	return before - saved
}

func clearEstimates(ctx *schemas.BifrostContext) {
	ctx.ClearValue(ContextKeyEstimatedBefore)
	ctx.ClearValue(ContextKeyEstimatedAfter)
	ctx.ClearValue(contextKeyStream)
}

// PostLLMHook logs estimated versus provider-reported prompt tokens when
// PreLLMHook compressed the request. The response and error pass through
// untouched; a panic here is swallowed so logging can never break a request.
func (p *Plugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	defer func() {
		if r := recover(); r != nil {
			p.warn("promptcompression: recovered from panic in PostLLMHook: %v", r)
		}
	}()
	before, ok1 := ctx.Value(ContextKeyEstimatedBefore).(int)
	after, ok2 := ctx.Value(ContextKeyEstimatedAfter).(int)
	if !ok1 || !ok2 {
		return resp, bifrostErr, nil
	}
	// Streams run this hook per chunk; usage arrives with the final one.
	if stream, _ := ctx.Value(contextKeyStream).(bool); stream {
		if final, _ := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator).(bool); !final {
			return resp, bifrostErr, nil
		}
	}
	// An error-only attempt has no usage to report. Leave the estimates in
	// place: the fallback attempt either overwrites them in PreLLMHook or
	// clears them there, and its own response logs the line once.
	if resp == nil {
		return resp, bifrostErr, nil
	}
	// The line is due: consume the estimates so a later request on the same
	// reused ctx cannot log them again.
	clearEstimates(ctx)
	var prompt, cached int
	var ru *schemas.ResponsesResponseUsage
	if rr := resp.ResponsesResponse; rr != nil {
		ru = rr.Usage
	} else if sr := resp.ResponsesStreamResponse; sr != nil && sr.Response != nil {
		// The Responses stream carries usage on its final chunk's embedded response.
		ru = sr.Response.Usage
	}
	if cr := resp.ChatResponse; cr != nil && cr.Usage != nil {
		prompt = cr.Usage.PromptTokens
		if d := cr.Usage.PromptTokensDetails; d != nil {
			cached = d.CachedReadTokens
		}
	} else if ru != nil {
		prompt = ru.InputTokens
		if d := ru.InputTokensDetails; d != nil {
			cached = d.CachedReadTokens
		}
	}
	if p.logger != nil {
		id, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
		p.logger.Info("promptcompression: request compressed request_id=%s estimated_before=%d estimated_after=%d prompt_tokens=%d cached_read_tokens=%d",
			id, before, after, prompt, cached)
	}
	return resp, bifrostErr, nil
}

func (p *Plugin) warn(msg string, args ...any) {
	if p.logger != nil {
		p.logger.Warn(msg, args...)
	}
}
