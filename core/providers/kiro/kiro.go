// Package kiro implements the Kiro (AWS CodeWhisperer) OAuth subscription provider.
//
// Kiro serves chat through the CodeWhisperer streaming RPC GenerateAssistantResponse: a JSON
// conversationState in, an AWS eventstream out (there is no non-streaming mode, so a
// non-streaming request drains the same stream). Credentials are Kiro IDE / kiro-cli OAuth
// sessions stored whole in the key value; access tokens are minted from the refresh token on
// demand and cached process-wide, and a rotated refresh token is written back through the
// configured credential updater.
package kiro

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Runtime hosts. Variables so tests can point them at a local server; the region is validated
// before it reaches either.
var (
	kiroRuntimeURL = func(region string) string { return "https://runtime." + region + ".kiro.dev/" }
	kiroLegacyURL  = func(region string) string { return "https://q." + region + ".amazonaws.com/" }
)

const (
	// maxErrorBodyBytes caps how much of an error response is read.
	maxErrorBodyBytes = 512 * 1024
	// initialEventPayloadBuffer is the eventstream payload buffer reserved per response.
	initialEventPayloadBuffer = 64 * 1024
)

// KiroProvider implements the schemas.Provider interface for Kiro.
type KiroProvider struct {
	logger              schemas.Logger
	client              *fasthttp.Client
	streamingClient     *fasthttp.Client
	tokens              *tokenSource
	networkConfig       schemas.NetworkConfig
	sendBackRawRequest  bool
	sendBackRawResponse bool
}

// NewKiroProvider creates a Kiro provider. credentialUpdater (may be nil) persists credentials
// whose refresh token rotated during a refresh.
//
// NetworkConfig.BaseURL, when set, replaces the regional runtime endpoint (for a recording proxy
// or a pinned host); the legacy-host fallback is then disabled so traffic never leaves the
// configured endpoint.
func NewKiroProvider(config *schemas.ProviderConfig, logger schemas.Logger, credentialUpdater schemas.KeyCredentialUpdater) (*KiroProvider, error) {
	config.CheckAndSetDefaults()

	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)
	client := &fasthttp.Client{
		ReadTimeout:         requestTimeout,
		WriteTimeout:        requestTimeout,
		MaxConnsPerHost:     config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConnDuration: time.Second * time.Duration(config.NetworkConfig.KeepAliveTimeoutInSeconds),
		MaxConnWaitTimeout:  requestTimeout,
		MaxConnDuration:     time.Second * time.Duration(schemas.DefaultMaxConnDurationInSeconds),
		ConnPoolStrategy:    fasthttp.FIFO,
	}
	// Order is load-bearing: the dialer wraps the proxy dial, and the streaming client clones
	// the fully configured client.
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client, config.NetworkConfig.AllowPrivateNetwork)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)
	streamingClient := providerUtils.BuildStreamingClient(client)

	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &KiroProvider{
		logger:          logger,
		client:          client,
		streamingClient: streamingClient,
		tokens: &tokenSource{
			client:  newExchangeClient(client),
			updater: credentialUpdater,
			logger:  logger,
		},
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier.
func (p *KiroProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Kiro
}

// ListModels serves the static Kiro catalog for every key, filtered by each key's model lists.
func (p *KiroProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if request == nil {
		request = &schemas.BifrostListModelsRequest{Provider: schemas.Kiro}
	}
	return providerUtils.HandleMultipleListModelsRequests(ctx, keys, request,
		func(_ *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
			return listModelsForKey(key, req.Unfiltered), nil
		})
}

// kiroCall is a successful (HTTP 200) GenerateAssistantResponse exchange whose body is yet to
// be decoded.
type kiroCall struct {
	resp    *fasthttp.Response
	payload *builtPayload
	latency time.Duration
}

// execute resolves the access token, builds the payload and sends it, falling back once to the
// legacy host when the runtime host does not serve the operation, and refreshing the token once
// when the runtime rejects it.
func (p *KiroProvider) execute(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest, stream bool) (*kiroCall, *schemas.BifrostError) {
	sess, bErr := p.tokens.session(ctx, key, "")
	if bErr != nil {
		return nil, bErr
	}

	var built *builtPayload
	builtFor := ""
	refreshed := false
	for {
		profileArn, builderIDFallback := sess.creds.requestProfile()
		wire := wireClientIDE
		if builderIDFallback || profileArn == "" {
			wire = wireClientCLI
		}
		if built == nil || builtFor != profileArn {
			built, bErr = buildKiroPayload(request, profileArn, wire)
			if bErr != nil {
				return nil, bErr
			}
			builtFor = profileArn
		}
		headers := runtimeHeaders(sess.accessToken, profileArn, wire)

		primary, fallback := p.endpoints(sess.creds)
		resp, latency, sendErr, connectFailure := p.send(ctx, primary, built.body, headers, stream)
		if fallback != "" && (connectFailure || (sendErr == nil && resp.StatusCode() != http.StatusOK &&
			shouldFallbackEndpoint(resp.StatusCode(), peekErrorBody(resp, stream)))) {
			if resp != nil {
				releaseUnreadResponse(resp)
			}
			p.debug("kiro: runtime endpoint %s unavailable; retrying on %s", primary, fallback)
			resp, latency, sendErr, _ = p.send(ctx, fallback, built.body, headers, stream)
		}
		if sendErr != nil {
			return nil, providerUtils.EnrichError(ctx, sendErr, built.body, nil, p.sendBackRawRequest, p.sendBackRawResponse, latency)
		}

		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))
		status := resp.StatusCode()
		if status == http.StatusOK {
			return &kiroCall{resp: resp, payload: built, latency: latency}, nil
		}

		// Copied: the body aliases the pooled response buffer, which is released below before the
		// error is enriched with it.
		body := append([]byte(nil), peekErrorBody(resp, stream)...)
		if !refreshed && shouldRefreshAfter(status, body) {
			releaseUnreadResponse(resp)
			refreshed = true
			sess, bErr = p.tokens.session(ctx, key, sess.accessToken)
			if bErr != nil {
				return nil, bErr
			}
			continue
		}
		httpErr := newHTTPError(status, body, &resp.Header)
		releaseUnreadResponse(resp)
		return nil, providerUtils.EnrichError(ctx, httpErr, built.body, body, p.sendBackRawRequest, p.sendBackRawResponse, latency)
	}
}

// endpoints returns the runtime URL and the legacy fallback URL ("" when disabled).
func (p *KiroProvider) endpoints(creds *Credentials) (string, string) {
	if p.networkConfig.BaseURL != "" {
		return p.networkConfig.BaseURL + "/", ""
	}
	region := creds.apiRegion()
	return kiroRuntimeURL(region), kiroLegacyURL(region)
}

// send performs one POST. connectFailure reports a failure before any response arrived (DNS,
// refused or reset connection), which warrants the endpoint fallback; timeouts and cancellation
// do not.
func (p *KiroProvider) send(ctx *schemas.BifrostContext, url string, body []byte, headers map[string]string, stream bool) (resp *fasthttp.Response, latency time.Duration, bErr *schemas.BifrostError, connectFailure bool) {
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp = fasthttp.AcquireResponse()

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	providerUtils.SetExtraHeaders(ctx, req, p.networkConfig.ExtraHeaders, nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	req.SetBody(body)

	if !stream {
		var wait func()
		latency, bErr, wait = providerUtils.MakeRequestWithContext(ctx, p.client, req, resp)
		wait()
		if bErr != nil {
			fasthttp.ReleaseResponse(resp)
			connectFailure = bErr.Error != nil && bErr.Error.Type != nil && *bErr.Error.Type == schemas.ProviderConnectionFailed
			return nil, latency, bErr, connectFailure
		}
		return resp, latency, nil, false
	}

	resp.StreamBody = true
	start := time.Now()
	err := providerUtils.DoStreamingRequest(ctx, p.streamingClient, req, resp)
	latency = time.Since(start)
	if err == nil {
		return resp, latency, nil, false
	}
	fasthttp.ReleaseResponse(resp)
	switch {
	case errors.Is(err, context.Canceled):
		return nil, latency, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Type:    schemas.Ptr(schemas.RequestCancelled),
				Message: schemas.ErrRequestCancelled,
				Error:   err,
			},
		}, false
	case errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded):
		return nil, latency, providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), false
	default:
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, latency, providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), false
		}
		return nil, latency, providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, err), true
	}
}

// peekErrorBody returns a non-200 response body, reading a streamed body into the response so
// later calls see the same bytes.
func peekErrorBody(resp *fasthttp.Response, stream bool) []byte {
	if !stream {
		return resp.Body()
	}
	if bodyStream := resp.BodyStream(); bodyStream != nil {
		data, _ := io.ReadAll(io.LimitReader(bodyStream, maxErrorBodyBytes))
		// Drain a bounded remainder so a keep-alive connection is reusable, then detach the
		// stream so the buffered copy is what Body() returns.
		_, _ = io.Copy(io.Discard, io.LimitReader(bodyStream, maxErrorBodyBytes))
		resp.CloseBodyStream()
		resp.SetBody(data)
	}
	return resp.Body()
}

// releaseUnreadResponse releases a response that is not handed to a stream reader. Error
// responses have already been buffered by peekErrorBody.
func releaseUnreadResponse(resp *fasthttp.Response) {
	if resp.BodyStream() != nil {
		resp.CloseBodyStream()
	}
	fasthttp.ReleaseResponse(resp)
}

func (p *KiroProvider) debug(format string, args ...any) {
	if p.logger != nil {
		p.logger.Debug(format, args...)
	}
}

// TextCompletion is not supported by Kiro.
func (p *KiroProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, p.GetProviderKey())
}

// TextCompletionStream is not supported by Kiro.
func (p *KiroProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, p.GetProviderKey())
}

// ChatCompletion performs a chat completion by draining Kiro's event stream into one response.
func (p *KiroProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	call, bErr := p.execute(ctx, key, request, false)
	if bErr != nil {
		return nil, bErr
	}
	defer fasthttp.ReleaseResponse(call.resp)

	parser := newStreamParser(call.payload.modelID, call.payload.nameMap)
	decoder := eventstream.NewDecoder()
	reader := &frameReader{r: bytes.NewReader(call.resp.Body())}
	payloadBuf := make([]byte, 0, initialEventPayloadBuffer)
	captureRaw := providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse)
	var rawEvents []map[string]any
	var acc chatAccumulator

	for {
		msg, err := reader.decode(decoder, payloadBuf)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, providerUtils.EnrichError(ctx, truncatedError("kiro: malformed event stream: "+err.Error()),
				call.payload.body, nil, p.sendBackRawRequest, p.sendBackRawResponse, call.latency)
		}
		if captureRaw {
			rawEvents = append(rawEvents, rawEvent(msg))
		}
		outputs, bErr := parser.handle(msg)
		if bErr != nil {
			return nil, providerUtils.EnrichError(ctx, bErr, call.payload.body, msg.Payload, p.sendBackRawRequest, p.sendBackRawResponse, call.latency)
		}
		acc.add(outputs)
	}
	outputs, bErr := parser.finish()
	if bErr != nil {
		return nil, providerUtils.EnrichError(ctx, bErr, call.payload.body, nil, p.sendBackRawRequest, p.sendBackRawResponse, call.latency)
	}
	acc.add(outputs)

	finishReason := parser.finishReason()
	response := &schemas.BifrostChatResponse{
		ID:      "chatcmpl-" + uuid.NewString(),
		Object:  "chat.completion",
		Created: int(time.Now().Unix()),
		Model:   request.Model,
		Choices: []schemas.BifrostResponseChoice{{
			Index:                       0,
			FinishReason:                &finishReason,
			ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: acc.message()},
		}},
		Usage: parser.usage(call.payload.inputTokens),
	}
	response.ExtraFields.Latency = call.latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(call.resp)
	if providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&response.ExtraFields, call.payload.body)
	}
	if captureRaw {
		response.ExtraFields.RawResponse = rawEvents
	}
	return response, nil
}

// chatAccumulator assembles stream outputs into one assistant message.
type chatAccumulator struct {
	text      strings.Builder
	reasoning strings.Builder
	signature string
	redacted  string
	toolCalls []schemas.ChatAssistantMessageToolCall
	toolArgs  []strings.Builder
}

func (a *chatAccumulator) add(outputs []streamOutput) {
	for _, out := range outputs {
		switch out.kind {
		case outputText:
			a.text.WriteString(out.text)
		case outputReasoning:
			a.reasoning.WriteString(out.text)
		case outputReasoningSignature:
			a.signature = out.text
		case outputReasoningRedacted:
			a.redacted = out.text
		case outputToolStart:
			a.toolCalls = append(a.toolCalls, schemas.ChatAssistantMessageToolCall{
				Index:    uint16(out.toolIndex),
				Type:     schemas.Ptr("function"),
				ID:       schemas.Ptr(out.toolID),
				Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr(out.toolName)},
			})
			a.toolArgs = append(a.toolArgs, strings.Builder{})
		case outputToolArgs:
			if out.toolIndex < len(a.toolArgs) {
				a.toolArgs[out.toolIndex].WriteString(out.text)
			}
		}
	}
}

func (a *chatAccumulator) message() *schemas.ChatMessage {
	msg := &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant}
	if text := a.text.String(); text != "" || len(a.toolCalls) == 0 {
		msg.Content = &schemas.ChatMessageContent{ContentStr: &text}
	}
	assistant := &schemas.ChatAssistantMessage{}
	populated := false
	if reasoning := a.reasoning.String(); reasoning != "" {
		assistant.Reasoning = &reasoning
		assistant.ReasoningDetails = append(assistant.ReasoningDetails, schemas.ChatReasoningDetails{
			Index: 0, Type: schemas.BifrostReasoningDetailsTypeText, Text: &reasoning,
		})
		populated = true
	}
	if a.signature != "" {
		signature := a.signature
		assistant.ReasoningDetails = append(assistant.ReasoningDetails, schemas.ChatReasoningDetails{
			Index: 0, Type: schemas.BifrostReasoningDetailsTypeText, Signature: &signature,
		})
		populated = true
	}
	if a.redacted != "" {
		data := a.redacted
		assistant.ReasoningDetails = append(assistant.ReasoningDetails, schemas.ChatReasoningDetails{
			Index: 0, Type: schemas.BifrostReasoningDetailsTypeEncrypted, Data: &data,
		})
		populated = true
	}
	if len(a.toolCalls) > 0 {
		for i := range a.toolCalls {
			a.toolCalls[i].Function.Arguments = a.toolArgs[i].String()
		}
		assistant.ToolCalls = a.toolCalls
		populated = true
	}
	if populated {
		msg.ChatAssistantMessage = assistant
	}
	return msg
}

// rawEvent renders a decoded eventstream message for raw-response capture.
func rawEvent(msg eventstream.Message) map[string]any {
	event := map[string]any{
		"message_type": headerString(msg.Headers, ":message-type"),
		"event_type":   headerString(msg.Headers, ":event-type"),
	}
	if exception := headerString(msg.Headers, ":exception-type"); exception != "" {
		event["exception_type"] = exception
	}
	event["payload"] = string(msg.Payload)
	return event
}

// ChatCompletionStream streams a chat completion, translating Kiro events into chat chunks.
func (p *KiroProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, p.networkConfig.StreamIdleTimeoutInSeconds)
	call, bErr := p.execute(ctx, key, request, true)
	if bErr != nil {
		return nil, bErr
	}
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	go p.streamResponse(ctx, postHookRunner, postHookSpanFinalizer, request, call, responseChan)
	return responseChan, nil
}

func (p *KiroProvider) streamResponse(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	request *schemas.BifrostChatRequest,
	call *kiroCall,
	responseChan chan *schemas.BifrostStreamChunk,
) {
	jsonBody := call.payload.body
	emitter := newChunkEmitter(ctx, postHookRunner, postHookSpanFinalizer, responseChan, p.logger, request.Model, call.latency)

	defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
	defer func() {
		if ctx.Err() == context.Canceled {
			providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, p.logger, postHookSpanFinalizer, jsonBody)
		} else if ctx.Err() == context.DeadlineExceeded {
			providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, p.logger, postHookSpanFinalizer, jsonBody)
		}
		emitter.release()
		providerUtils.CloseStream(ctx, responseChan)
	}()
	defer providerUtils.ReleaseStreamingResponse(ctx, call.resp)

	bodyStream := call.resp.BodyStream()
	var source io.Reader = bodyStream
	if bodyStream == nil {
		// fasthttp buffered a short body instead of streaming it.
		source = bytes.NewReader(call.resp.Body())
	} else {
		reader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(bodyStream, bodyStream, providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, bodyStream, p.logger)
		defer stopCancellation()
		source = reader
	}

	parser := newStreamParser(call.payload.modelID, call.payload.nameMap)
	usage := &schemas.BifrostLLMUsage{PromptTokens: call.payload.inputTokens, TotalTokens: call.payload.inputTokens}
	// Registered so a mid-stream cancel or timeout bills what was already generated.
	ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, usage)

	fail := func(bErr *schemas.BifrostError, raw []byte) {
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner,
			providerUtils.EnrichError(ctx, bErr, jsonBody, raw, p.sendBackRawRequest, p.sendBackRawResponse, time.Since(emitter.start)),
			responseChan, p.logger, postHookSpanFinalizer)
	}

	decoder := eventstream.NewDecoder()
	frames := &frameReader{r: source}
	payloadBuf := make([]byte, 0, initialEventPayloadBuffer)
	captureRaw := providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse)
	for {
		if ctx.Err() != nil {
			return
		}
		msg, err := frames.decode(decoder, payloadBuf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if err == io.EOF {
				ctx.SetValue(schemas.BifrostContextKeyStreamBodyExhausted, true)
				break
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				providerUtils.SendStreamTruncatedError(ctx, postHookRunner, responseChan, p.logger, postHookSpanFinalizer, jsonBody)
				return
			}
			if errors.Is(err, errFrameTooLarge) {
				fail(protocolError(err.Error()), nil)
				return
			}
			p.logger.Warn("kiro: error decoding event stream: %v", err)
			fail(&schemas.BifrostError{
				IsBifrostError: false,
				StatusCode:     schemas.Ptr(http.StatusBadGateway),
				Error:          &schemas.ErrorField{Message: schemas.ErrProviderNetworkError, Error: err},
			}, nil)
			return
		}
		outputs, bErr := parser.handle(msg)
		if bErr != nil {
			fail(bErr, msg.Payload)
			return
		}
		if len(outputs) == 0 {
			continue
		}
		parser.fillUsage(usage, call.payload.inputTokens)
		var raw string
		if captureRaw {
			raw = string(msg.Payload)
		}
		for _, out := range outputs {
			if !emitter.sendOutput(out, raw) {
				return
			}
			raw = ""
		}
	}

	outputs, bErr := parser.finish()
	if bErr != nil {
		fail(bErr, nil)
		return
	}
	for _, out := range outputs {
		if !emitter.sendOutput(out, "") {
			return
		}
	}
	parser.fillUsage(usage, call.payload.inputTokens)
	finishReason := parser.finishReason()
	var rawRequest []byte
	if providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest) {
		rawRequest = jsonBody
	}
	emitter.sendFinal(finishReason, usage, rawRequest)
}

// Responses performs a Responses request through chat completions.
func (p *KiroProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	chatResponse, err := p.ChatCompletion(ctx, key, request.ToChatRequest())
	if err != nil {
		return nil, err
	}
	return chatResponse.ToBifrostResponsesResponse(), nil
}

// ResponsesStream performs a streaming Responses request through chat completions.
func (p *KiroProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
	return p.ChatCompletionStream(ctx, postHookRunner, postHookSpanFinalizer, key, request.ToChatRequest())
}
