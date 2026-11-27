package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// The GenAI Interactions routes serve Google's Interactions API (POST /interactions and the
// /interactions/{id} lifecycle) as typed Bifrost requests: governance, logging, pricing and
// fallbacks apply, which /genai_passthrough cannot offer.
//
// A request for Gemini or Vertex forwards the caller's body as it is and answers with Google's
// bytes (the native path), so agents, environments, webhooks and every built-in tool step
// survive. A request for any other provider (a provider/model prefix, a routing rule, or a
// fallback) is translated to the Responses schema and its answer rendered back as an Interaction.

// genAIInteractionsStreamStateKey holds the id of a translated stream's state in genAIInteractionsStreams.
const genAIInteractionsStreamStateKey schemas.BifrostContextKey = "bifrost-genai-interactions-stream-state"

// genAIInteractionsStreams holds the state of each translated stream in progress, which grows with its
// output, by an id of the stream's own: a request id is the caller's and can repeat. The router's stream
// cleanup drops an entry when its stream ends, however it ends.
var genAIInteractionsStreams sync.Map // string -> *gemini.BifrostToGeminiInteractionsStreamState

// genAIInteractionsStreamState returns the state of ctx's translated stream, opened on first use.
func genAIInteractionsStreamState(ctx *schemas.BifrostContext) *gemini.BifrostToGeminiInteractionsStreamState {
	if id, _ := ctx.Value(genAIInteractionsStreamStateKey).(string); id != "" {
		if state, ok := genAIInteractionsStreams.Load(id); ok {
			return state.(*gemini.BifrostToGeminiInteractionsStreamState)
		}
	}
	id, state := uuid.NewString(), gemini.NewBifrostToGeminiInteractionsStreamState()
	state.ClientToolSearchName, _ = ctx.Value(genAIInteractionsClientSearchNameKey).(string)
	genAIInteractionsStreams.Store(id, state)
	ctx.SetValue(genAIInteractionsStreamStateKey, id)
	return state
}

// releaseGenAIInteractionsStreamState drops the state of ctx's stream, if it opened one.
func releaseGenAIInteractionsStreamState(ctx *schemas.BifrostContext) {
	if id, _ := ctx.Value(genAIInteractionsStreamStateKey).(string); id != "" {
		genAIInteractionsStreams.Delete(id)
	}
}

// genAIInteractionsClientSearchNameKey holds the name the request declared its client tool_search
// under, set only when it declares one.
const genAIInteractionsClientSearchNameKey schemas.BifrostContextKey = "bifrost-genai-interactions-client-search-name"

// genAIInteractionsBasePaths are the Gemini API and Vertex shapes of the collection.
func genAIInteractionsBasePaths(pathPrefix string) []string {
	return []string{
		pathPrefix + "/v1beta/interactions",
		pathPrefix + "/v1/interactions",
		pathPrefix + "/v1/projects/{project}/locations/{location}/interactions",
		pathPrefix + "/v1beta1/projects/{project}/locations/{location}/interactions",
	}
}

// CreateGenAIInteractionsRouteConfigs creates the Interactions routes under pathPrefix.
func CreateGenAIInteractionsRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	var routes []RouteConfig
	for _, base := range genAIInteractionsBasePaths(pathPrefix) {
		routes = append(routes,
			RouteConfig{
				Type:   RouteConfigTypeGenAI,
				Path:   base,
				Method: "POST",
				GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
					return schemas.ResponsesRequest
				},
				GetRequestTypeInstance: func(ctx context.Context) interface{} {
					return &gemini.GeminiInteractionsRequest{}
				},
				PreCallback:                prepareGenAIInteractionsCreate(handlerStore),
				RequestConverter:           convertGenAIInteractionsCreate,
				ResponsesResponseConverter: genAIInteractionsResponseConverter,
				ErrorConverter:             genAIInteractionsErrorConverter,
				StreamConfig:               genAIInteractionsStreamConfig(),
			},
			RouteConfig{
				Type:   RouteConfigTypeGenAI,
				Path:   base + "/{interaction_id}",
				Method: "GET",
				GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
					return schemas.ResponsesRetrieveRequest
				},
				GetRequestTypeInstance: func(ctx context.Context) interface{} {
					return &schemas.BifrostResponsesRetrieveRequest{}
				},
				PreCallback: prepareGenAIInteractionsLifecycle(handlerStore),
				RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
					if r, ok := req.(*schemas.BifrostResponsesRetrieveRequest); ok {
						return &schemas.BifrostRequest{RequestType: schemas.ResponsesRetrieveRequest, ResponsesRetrieveRequest: r}, nil
					}
					return nil, errors.New("invalid interactions retrieve request")
				},
				ResponsesResponseConverter: genAIInteractionsResponseConverter,
				ErrorConverter:             genAIInteractionsErrorConverter,
				StreamConfig:               genAIInteractionsStreamConfig(),
			},
			RouteConfig{
				Type:   RouteConfigTypeGenAI,
				Path:   base + "/{interaction_id}",
				Method: "DELETE",
				GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
					return schemas.ResponsesDeleteRequest
				},
				GetRequestTypeInstance: func(ctx context.Context) interface{} {
					return &schemas.BifrostResponsesDeleteRequest{}
				},
				PreCallback: prepareGenAIInteractionsLifecycle(handlerStore),
				RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
					if r, ok := req.(*schemas.BifrostResponsesDeleteRequest); ok {
						return &schemas.BifrostRequest{RequestType: schemas.ResponsesDeleteRequest, ResponsesDeleteRequest: r}, nil
					}
					return nil, errors.New("invalid interactions delete request")
				},
				// Google answers a delete with an empty object.
				ResponsesDeleteResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesDeleteResponse) (interface{}, error) {
					return map[string]interface{}{}, nil
				},
				ErrorConverter: genAIInteractionsErrorConverter,
			},
			RouteConfig{
				Type:   RouteConfigTypeGenAI,
				Path:   base + "/{interaction_id}/cancel",
				Method: "POST",
				GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
					return schemas.ResponsesCancelRequest
				},
				GetRequestTypeInstance: func(ctx context.Context) interface{} {
					return &schemas.BifrostResponsesCancelRequest{}
				},
				PreCallback: prepareGenAIInteractionsLifecycle(handlerStore),
				RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
					if r, ok := req.(*schemas.BifrostResponsesCancelRequest); ok {
						return &schemas.BifrostRequest{RequestType: schemas.ResponsesCancelRequest, ResponsesCancelRequest: r}, nil
					}
					return nil, errors.New("invalid interactions cancel request")
				},
				ResponsesResponseConverter: genAIInteractionsResponseConverter,
				ErrorConverter:             genAIInteractionsErrorConverter,
			},
		)
	}
	return routes
}

// isGoogleInteractionsProvider reports whether provider serves the Interactions API natively: Gemini,
// Vertex, or a custom provider backed by one of them.
func isGoogleInteractionsProvider(handlerStore lib.HandlerStore, provider schemas.ModelProvider) bool {
	if provider == schemas.Gemini || provider == schemas.Vertex {
		return true
	}
	if handlerStore == nil {
		return false
	}
	base, _, ok := handlerStore.GetCustomProviderPassthrough(provider)
	return ok && (base == schemas.Gemini || base == schemas.Vertex)
}

// genAIInteractionsDefaultProvider is the provider a request that names none goes to: the
// x-model-provider header, else Vertex for a Vertex-shaped path or bearer token, else Gemini.
func genAIInteractionsDefaultProvider(ctx *fasthttp.RequestCtx, model string) schemas.ModelProvider {
	if header := getProviderFromHeader(ctx, ""); header != "" {
		return header
	}
	return detectProviderFromGenAIRequest(ctx, model)
}

// enableGenAIInteractionsNative turns on the native path: the caller's body goes upstream as it
// is (with its text fields still open to guardrail redaction) and Google's response comes back.
// Core turns the raw body off again for any attempt on a provider outside Google's family.
func enableGenAIInteractionsNative(bifrostCtx *schemas.BifrostContext) {
	bifrostCtx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	bifrostCtx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
	// The route needs Google's bytes to answer at all, so its raw-response request is a passthrough
	// override that core honors without the transport's per-request raw override, as on /anthropic.
	bifrostCtx.SetValue(schemas.BifrostContextKeyPassthroughOverridesPresent, true)
	bifrostCtx.SetValue(
		schemas.BifrostContextKeyRawRequestBodyTextRewriter,
		schemas.RawRequestBodyTextRewriter(rewriteGenAIInteractionsRawRequestBody),
	)
}

// forwardGenAIInteractionsHeaders passes the Api-Revision header, which pins the Interactions
// API's response shape, through to the upstream.
func forwardGenAIInteractionsHeaders(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) {
	revision := strings.TrimSpace(string(ctx.Request.Header.Peek("Api-Revision")))
	if revision == "" {
		return
	}
	headers, _ := bifrostCtx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	merged := make(map[string][]string, len(headers)+1)
	for k, v := range headers {
		merged[k] = v
	}
	merged["Api-Revision"] = []string{revision}
	bifrostCtx.SetValue(schemas.BifrostContextKeyExtraHeaders, merged)
}

// setGenAIInteractionsVertexPath records the project and location a Vertex-shaped path names, so key
// selection prefers a key for them. The paths without them record nothing.
func setGenAIInteractionsVertexPath(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) {
	if location, _ := ctx.UserValue("location").(string); location != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyInteractionsLocation, location)
	}
	if project, _ := ctx.UserValue("project").(string); project != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyInteractionsProject, project)
	}
}

func prepareGenAIInteractionsCreate(handlerStore lib.HandlerStore) PreRequestCallback {
	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		r, ok := req.(*gemini.GeminiInteractionsRequest)
		if !ok {
			return errors.New("invalid interactions request")
		}
		modelName := r.Model
		if modelName == "" {
			modelName = r.Agent
		}
		if modelName == "" {
			return providerUtils.InvalidRequestErrorf("interactions request names neither a model nor an agent")
		}
		defaultProvider := genAIInteractionsDefaultProvider(ctx, modelName)
		provider, _ := schemas.ParseModelString(modelName, defaultProvider)
		// A fallback named without a provider runs on the provider the request's model names (the
		// default provider when the model has no prefix). Spell that out once here, so the native-path
		// check below and the converted fallback list core runs agree.
		for i, fallback := range r.Fallbacks {
			if fallbackProvider, fallbackModel := schemas.ParseModelString(fallback, ""); fallbackProvider == "" && fallbackModel != "" {
				r.Fallbacks[i] = string(provider) + "/" + fallbackModel
			}
		}
		bifrostCtx.SetValue(bifrostContextKeyProvider, defaultProvider)
		bifrostCtx.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
		setGenAIInteractionsVertexPath(ctx, bifrostCtx)
		// A translated answer names the caller's search after this, as the Gemini path does.
		if name := gemini.GeminiInteractionsRequestClientToolSearchName(r.Tools); name != "" {
			bifrostCtx.SetValue(genAIInteractionsClientSearchNameKey, name)
		}

		headers := extractHeadersFromRequest(ctx)
		schemas.ExtractAndSetUserAgentFromHeaders(headers, bifrostCtx)
		forwardGenAIInteractionsHeaders(ctx, bifrostCtx)

		if isGoogleInteractionsProvider(handlerStore, provider) {
			// A fallback outside Google's family runs translated, which would drop agent-only fields
			// silently; refuse them up front, as for a request that names that provider directly.
			if genAIInteractionsHasNonGoogleFallback(handlerStore, r.Fallbacks, defaultProvider) {
				if err := r.ValidateTranslatable(); err != nil {
					return err
				}
				// History the translated attempt cannot carry (Google's built-in tool steps) would
				// be dropped by the fallback, so it is refused here too.
				if _, err := r.ToBifrostResponsesRequest(); err != nil {
					return err
				}
			}
			enableGenAIInteractionsNative(bifrostCtx)
			return nil
		}
		return r.ValidateTranslatable()
	}
}

// genAIInteractionsHasNonGoogleFallback reports whether any fallback the request lists runs
// outside Google's family. A fallback with no provider prefix goes to defaultProvider.
func genAIInteractionsHasNonGoogleFallback(handlerStore lib.HandlerStore, fallbacks []string, defaultProvider schemas.ModelProvider) bool {
	for _, fallback := range fallbacks {
		provider, _ := schemas.ParseModelString(fallback, defaultProvider)
		if !isGoogleInteractionsProvider(handlerStore, provider) {
			return true
		}
	}
	return false
}

func convertGenAIInteractionsCreate(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
	r, ok := req.(*gemini.GeminiInteractionsRequest)
	if !ok {
		return nil, errors.New("invalid interactions request")
	}
	responsesReq, err := r.ToBifrostResponsesRequest()
	native, _ := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
	if native {
		// Routing can still pick a provider this request cannot be translated for; core refuses it then.
		refusal := err
		if refusal == nil {
			refusal = r.ValidateTranslatable()
		}
		if refusal != nil {
			ctx.SetValue(schemas.BifrostContextKeyGenAITranslationRefusal, refusal.Error())
		}
	}
	if err != nil {
		// On the native path Google gets the caller's raw body; the converted request only drives
		// routing, governance and logging. Tools and steps with no Responses form (Google's own
		// built-in tool steps, google_maps, step types Bifrost does not model) are left out of
		// that copy rather than failing the request. A translated request stays strict.
		if !native {
			return nil, err
		}
		responsesReq, err = genAIInteractionsRoutingCopy(r).ToBifrostResponsesRequest()
		if err != nil {
			return nil, err
		}
	}
	if responsesReq.Provider == "" {
		if provider, ok := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider); ok && provider != "" {
			responsesReq.Provider = provider
		} else {
			responsesReq.Provider = schemas.Gemini
		}
	}
	return &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: responsesReq}, nil
}

// genAIInteractionsRoutingCopy returns a shallow copy of r keeping only the tools and input steps
// that have a Responses form, each checked on its own. It runs only after a native request failed
// strict conversion. The caller's request, and the raw body sent upstream, are untouched.
func genAIInteractionsRoutingCopy(r *gemini.GeminiInteractionsRequest) *gemini.GeminiInteractionsRequest {
	out := *r
	if len(r.Tools) > 0 {
		tools := make([]gemini.GeminiInteractionsTool, 0, len(r.Tools))
		for _, tool := range r.Tools {
			probe := gemini.GeminiInteractionsRequest{Model: r.Model, Agent: r.Agent, Tools: []gemini.GeminiInteractionsTool{tool}}
			if _, err := probe.ToBifrostResponsesRequest(); err == nil {
				tools = append(tools, tool)
			}
		}
		out.Tools = tools
	}
	if r.Input != nil && len(r.Input.Items) > 0 {
		items := make([]gemini.GeminiInteractionsItem, 0, len(r.Input.Items))
		for _, item := range r.Input.Items {
			probe := gemini.GeminiInteractionsRequest{Model: r.Model, Agent: r.Agent, Input: &gemini.GeminiInteractionsInput{Items: []gemini.GeminiInteractionsItem{item}}}
			if _, err := probe.ToBifrostResponsesRequest(); err == nil {
				items = append(items, item)
			}
		}
		out.Input = &gemini.GeminiInteractionsInput{Items: items}
	}
	return &out
}

// prepareGenAIInteractionsLifecycle fills the interaction id and provider of a get, cancel or
// delete. The provider comes from ?provider=, the x-model-provider header, or the path and token
// shape, as on create; a get with ?stream=true replays the run, resuming after last_event_id.
func prepareGenAIInteractionsLifecycle(handlerStore lib.HandlerStore) PreRequestCallback {
	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		id, _ := ctx.UserValue("interaction_id").(string)
		if id == "" {
			return errors.New("interaction id is required")
		}
		provider := schemas.ModelProvider(string(ctx.QueryArgs().Peek("provider")))
		if provider == "" {
			provider = genAIInteractionsDefaultProvider(ctx, "")
		}
		bifrostCtx.SetValue(bifrostContextKeyProvider, provider)
		bifrostCtx.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
		setGenAIInteractionsVertexPath(ctx, bifrostCtx)
		forwardGenAIInteractionsHeaders(ctx, bifrostCtx)
		if isGoogleInteractionsProvider(handlerStore, provider) {
			bifrostCtx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
			bifrostCtx.SetValue(schemas.BifrostContextKeyPassthroughOverridesPresent, true)
			bifrostCtx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		}

		switch r := req.(type) {
		case *schemas.BifrostResponsesRetrieveRequest:
			r.ResponseID, r.Provider = id, provider
			if raw := ctx.QueryArgs().Peek("stream"); len(raw) > 0 {
				stream := string(raw) == "true" || string(raw) == "1"
				r.Stream = &stream
			}
			if raw := ctx.QueryArgs().Peek("last_event_id"); len(raw) > 0 {
				last := string(raw)
				r.LastEventID = &last
			}
		case *schemas.BifrostResponsesDeleteRequest:
			r.ResponseID, r.Provider = id, provider
		case *schemas.BifrostResponsesCancelRequest:
			r.ResponseID, r.Provider = id, provider
		default:
			return errors.New("invalid interactions lifecycle request")
		}
		return nil
	}
}

// genAIInteractionsNative reports whether a response came back on the native path: the attempt
// still forwarded the caller's body (core clears that for a provider outside Google's family) and
// the provider's own bytes are attached.
func genAIInteractionsNative(ctx *schemas.BifrostContext, raw interface{}) bool {
	if raw == nil {
		return false
	}
	useRaw, _ := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
	return useRaw
}

func genAIInteractionsResponseConverter(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesResponse) (interface{}, error) {
	if genAIInteractionsNative(ctx, resp.ExtraFields.RawResponse) && !genAIInteractionsCachedOtherAPI(ctx, resp.ExtraFields.RawResponse) {
		return resp.ExtraFields.RawResponse, nil
	}
	name, _ := ctx.Value(genAIInteractionsClientSearchNameKey).(string)
	return gemini.ToGeminiInteractionWithClientSearch(resp, name), nil
}

func genAIInteractionsErrorConverter(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
	return gemini.ToGeminiInteractionsErrorBody(err)
}

// genAIInteractionsCachedOtherAPI reports whether raw was served from the cache and is not an Interaction, as when
// generateContent answered the request that filled the entry. Only a cache hit is looked into.
func genAIInteractionsCachedOtherAPI(ctx *schemas.BifrostContext, raw interface{}) bool {
	lookup, ok := schemas.CacheLookupFromContext(ctx)
	if !ok || !lookup.CacheHit {
		return false
	}
	var body []byte
	switch v := raw.(type) {
	case string:
		body = []byte(v)
	case []byte:
		body = v
	case json.RawMessage:
		body = v
	default:
		encoded, err := providerUtils.MarshalSorted(v)
		if err != nil {
			return true
		}
		body = encoded
	}
	fields := gjson.GetManyBytes(body, "event_type", "steps", "status", "output")
	return !fields[0].Exists() && !fields[1].Exists() && (!fields[2].Exists() || fields[3].Exists())
}

// genAIInteractionsFrames writes the upstream events raw holds, each with its own event name. A stream captured
// for logs joins the events no chunk stood for onto the next one.
func genAIInteractionsFrames(raw string) string {
	var sb strings.Builder
	write := func(event string) {
		eventType := gjson.Get(event, "event_type").String()
		sb.Grow(len(event) + len(eventType) + 24)
		sb.WriteString("event: ")
		sb.WriteString(eventType)
		sb.WriteString("\ndata: ")
		sb.WriteString(event)
		sb.WriteString("\n\n")
		if eventType == gemini.GeminiInteractionsEventCompleted {
			sb.WriteString(genAIInteractionsDoneFrame)
		}
	}
	if !strings.Contains(raw, "\n") {
		write(raw)
		return sb.String()
	}
	gjson.ForEachLine(raw, func(event gjson.Result) bool {
		write(event.Raw)
		return true
	})
	return sb.String()
}

// genAIInteractionsDoneFrame closes an Interactions stream the way Google does.
const genAIInteractionsDoneFrame = "event: done\ndata: [DONE]\n\n"

func genAIInteractionsStreamConfig() *StreamConfig {
	return &StreamConfig{
		// Google's SDKs parse every line of an Interactions stream; a heartbeat comment would
		// break them, as on the generateContent route.
		HeartbeatFraming:                 lib.SSEHeartbeatNone,
		ResponsesStreamResponseConverter: genAIInteractionsStreamConverter,
		Cleanup:                          releaseGenAIInteractionsStreamState,
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			body := gemini.ToGeminiInteractionsErrorBody(err)
			payload, marshalErr := providerUtils.MarshalSorted(map[string]interface{}{"error": body.Error, "event_type": gemini.GeminiInteractionsEventError})
			if marshalErr != nil {
				return body
			}
			return "event: error\ndata: " + string(payload) + "\n\n"
		},
	}
}

// genAIInteractionsTranslatedReplayKey holds whether a cached stream is rendered from its normalized chunks,
// decided on its first chunk.
const genAIInteractionsTranslatedReplayKey schemas.BifrostContextKey = "bifrost-genai-interactions-translated-replay"

// genAIInteractionsTranslatedReplay reports whether a native stream is a cache hit on an entry stored without
// Google's Interactions frames, by a route that had none or another API's. Its first chunk tells: a stored native
// stream opens with a frame, and its chunks without one repeat an event as they did live.
func genAIInteractionsTranslatedReplay(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesStreamResponse) bool {
	if decided, ok := ctx.Value(genAIInteractionsTranslatedReplayKey).(bool); ok {
		return decided
	}
	lookup, ok := schemas.CacheLookupFromContext(ctx)
	translated := ok && lookup.CacheHit && (resp.ExtraFields.RawResponse == nil || genAIInteractionsCachedOtherAPI(ctx, resp.ExtraFields.RawResponse))
	ctx.SetValue(genAIInteractionsTranslatedReplayKey, translated)
	return translated
}

// genAIInteractionsStreamConverter writes one stream chunk. On the native path it replays the
// upstream event the chunk carries (chunks without one are skipped, so every event goes out
// exactly once); otherwise it renders the chunk as Interactions events.
func genAIInteractionsStreamConverter(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
	if raw, ok := resp.ExtraFields.RawResponse.(string); ok && genAIInteractionsNative(ctx, resp.ExtraFields.RawResponse) && !genAIInteractionsTranslatedReplay(ctx, resp) {
		return "", genAIInteractionsFrames(raw), nil
	}
	if useRaw, _ := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); useRaw && resp.ExtraFields.RawResponse == nil && !genAIInteractionsTranslatedReplay(ctx, resp) {
		if resp.Type == schemas.ResponsesStreamResponseTypeCompleted && resp.ExtraFields.RawResponseFormat != "" && resp.Response != nil {
			// A guardrail removed the completion it could not rewrite. The run still has to end for the
			// client, so it is rendered from the response the guardrail did rewrite.
			payload, err := providerUtils.MarshalSorted(&gemini.GeminiInteractionsEvent{EventType: gemini.GeminiInteractionsEventCompleted, Interaction: gemini.ToGeminiInteraction(resp.Response)})
			if err != nil {
				return "", nil, fmt.Errorf("failed to encode interactions event: %w", err)
			}
			return "", "event: " + gemini.GeminiInteractionsEventCompleted + "\ndata: " + string(payload) + "\n\n" + genAIInteractionsDoneFrame, nil
		}
		// A native stream chunk that repeats an event already sent.
		return "", nil, nil
	}

	events := genAIInteractionsStreamState(ctx).ToGeminiInteractionsStreamEvents(resp)
	if len(events) == 0 {
		return "", nil, nil
	}
	var sb strings.Builder
	for _, event := range events {
		payload, err := providerUtils.MarshalSorted(event)
		if err != nil {
			return "", nil, fmt.Errorf("failed to encode interactions event: %w", err)
		}
		sb.WriteString("event: ")
		sb.WriteString(event.EventType)
		sb.WriteString("\ndata: ")
		sb.Write(payload)
		sb.WriteString("\n\n")
		if event.EventType == gemini.GeminiInteractionsEventCompleted {
			sb.WriteString(genAIInteractionsDoneFrame)
		}
	}
	return "", sb.String(), nil
}

// rewriteGenAIInteractionsRawRequestBody applies guardrail replacements to the text an
// Interactions body carries: the system instruction, a string input, and the text of content
// blocks and steps (including function results).
func rewriteGenAIInteractionsRawRequestBody(rawBody []byte, replacements map[string]string) ([]byte, error) {
	return rewriteRawRequestTextFields(rawBody, replacements, collectGenAIInteractionsTextPaths)
}

func collectGenAIInteractionsTextPaths(root gjson.Result, replacements map[string]string) ([]string, error) {
	var paths []string
	if err := appendRawRequestStringPath(&paths, root.Get("system_instruction"), "system_instruction"); err != nil {
		return nil, err
	}
	input := root.Get("input")
	switch {
	case !input.Exists() || input.Type == gjson.Null:
	case input.Type == gjson.String:
		paths = append(paths, "input")
	case input.IsObject():
		if err := collectGenAIInteractionsItemTextPaths(&paths, input, "input"); err != nil {
			return nil, err
		}
	case input.IsArray():
		var collectErr error
		input.ForEach(func(index, item gjson.Result) bool {
			collectErr = collectGenAIInteractionsItemTextPaths(&paths, item, rawRequestArrayPath("input", int(index.Int())))
			return collectErr == nil
		})
		if collectErr != nil {
			return nil, collectErr
		}
	default:
		return nil, fmt.Errorf("raw interactions input must be a string, an object, or an array")
	}
	return paths, nil
}

// collectGenAIInteractionsItemTextPaths adds the text paths of one content block or step.
func collectGenAIInteractionsItemTextPaths(paths *[]string, item gjson.Result, path string) error {
	if !item.IsObject() {
		return nil
	}
	if err := appendRawRequestStringPath(paths, item.Get("text"), rawRequestObjectPath(path, "text")); err != nil {
		return err
	}
	for _, key := range []string{"content", "summary", "result"} {
		child := item.Get(key)
		switch {
		case !child.Exists() || child.Type == gjson.Null:
		case child.Type == gjson.String && key == "result":
			*paths = append(*paths, rawRequestObjectPath(path, key))
		case (child.IsObject() || child.IsArray()) && key == "result":
			// A structured result is sent upstream as JSON, so all its string leaves are redacted.
			if err := collectRawRequestArgumentStringPaths(paths, child, rawRequestObjectPath(path, key)); err != nil {
				return err
			}
		case child.IsArray():
			var collectErr error
			childPath := rawRequestObjectPath(path, key)
			child.ForEach(func(index, block gjson.Result) bool {
				collectErr = collectGenAIInteractionsItemTextPaths(paths, block, rawRequestArrayPath(childPath, int(index.Int())))
				return collectErr == nil
			})
			if collectErr != nil {
				return collectErr
			}
		}
	}
	if err := collectRawRequestArgumentStringPaths(paths, item.Get("arguments"), rawRequestObjectPath(path, "arguments")); err != nil {
		return err
	}
	return nil
}
