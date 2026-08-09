package handlers

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fasthttp/router"
	ws "github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
	"github.com/valyala/fasthttp"
)

// Max buffered PCM for listen session logs (play/download). Kept under the
// default 10MB large-payload strip threshold so the logging plugin keeps File.
// ~4.5 min of 16 kHz mono linear16; beyond this we still log transcript + usage.
const listenAudioLogMaxBytes = 9 << 20 // 9 MiB

// WSListenHandler proxies Deepgram-compatible live STT WebSocket connections
// at GET /v1/listen. Frames are forwarded bidirectionally (binary audio + text
// JSON control/results) with the client query string preserved upstream.
type WSListenHandler struct {
	client       *bifrost.Bifrost
	config       *lib.Config
	handlerStore lib.HandlerStore
	pool         *bfws.Pool
	sessions     *bfws.SessionManager
}

// NewWSListenHandler creates a new native listen WebSocket handler.
func NewWSListenHandler(client *bifrost.Bifrost, config *lib.Config, pool *bfws.Pool) *WSListenHandler {
	maxConns := config.WebSocketConfig.MaxConnections
	return &WSListenHandler{
		client:       client,
		config:       config,
		handlerStore: config,
		pool:         pool,
		sessions:     bfws.NewSessionManager(maxConns),
	}
}

// RegisterRoutes registers GET /v1/listen for Deepgram-compatible live STT.
func (h *WSListenHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handler := lib.ChainMiddlewares(h.handleUpgrade, middlewares...)
	r.GET("/v1/listen", handler)
}

// Close tears down active listen sessions.
func (h *WSListenHandler) Close() {
	if h == nil || h.sessions == nil {
		return
	}
	h.sessions.CloseAll()
}

func (h *WSListenHandler) handleUpgrade(ctx *fasthttp.RequestCtx) {
	path := string(ctx.Path())
	rawQuery := string(ctx.URI().QueryString())
	modelParam := string(ctx.QueryArgs().Peek("model"))
	auth := captureAuthHeaders(ctx)

	providerKey, model, err := resolveListenTarget(modelParam)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	preReqCtx, preReqCancel := createBifrostContextFromAuth(h.handlerStore, auth)
	if preReqCtx == nil {
		preReqCancel()
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to create request context")
		return
	}
	preReqCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.TranscriptionRequest)

	allHeaders := make(map[string]string)
	ctx.Request.Header.All()(func(key, value []byte) bool {
		allHeaders[strings.ToLower(string(key))] = string(value)
		return true
	})
	preReqCtx.SetValue(schemas.BifrostContextKeyRequestHeaders, allHeaders)
	if queryArgs := ctx.Request.URI().QueryArgs(); queryArgs.Len() > 0 {
		allQuery := make(map[string]string, queryArgs.Len())
		queryArgs.All()(func(key, value []byte) bool {
			allQuery[strings.ToLower(string(key))] = string(value)
			return true
		})
		preReqCtx.SetValue(schemas.BifrostContextKeyRequestQuery, allQuery)
	}

	preReq := &schemas.BifrostRequest{
		RequestType: schemas.TranscriptionRequest,
		TranscriptionRequest: &schemas.BifrostTranscriptionRequest{
			Provider: providerKey,
			Model:    model,
			Params:   listenQueryToTranscriptionParams(rawQuery),
		},
	}
	h.client.RunPreRequestHooks(preReqCtx, preReq)
	routedProvider, routedModel, _ := preReq.GetRequestFields()
	if routedProvider == "" {
		preReqCancel()
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("no provider could be resolved for model %q", model))
		return
	}
	providerKey = routedProvider
	if routedModel != "" {
		model = routedModel
	}
	for k, v := range preReqCtx.GetUserValues() {
		ctx.SetUserValue(k, v)
	}
	preReqCancel()

	provider := h.client.GetProviderByKey(providerKey)
	listenProvider, ok := provider.(schemas.ListenWebSocketProvider)
	if provider == nil || !ok || !listenProvider.SupportsListenWebSocket() {
		SendError(ctx, fasthttp.StatusBadRequest, "provider does not support listen websocket: "+string(providerKey))
		return
	}

	middlewareContextValues := snapshotRealtimeMiddlewareValues(ctx)

	upgrader := ws.FastHTTPUpgrader{
		CheckOrigin: func(reqCtx *fasthttp.RequestCtx) bool {
			origin := string(reqCtx.Request.Header.Peek("Origin"))
			if origin == "" {
				return true
			}
			return IsOriginAllowed(origin, h.config.ClientConfig.AllowedOrigins)
		},
	}
	upgradeErr := upgrader.Upgrade(ctx, func(conn *ws.Conn) {
		defer conn.Close()
		_, sessionErr := h.sessions.Create(conn)
		if sessionErr != nil {
			logger.Warn("listen websocket session create failed: %v", sessionErr)
			_ = conn.WriteMessage(ws.CloseMessage, ws.FormatCloseMessage(ws.CloseTryAgainLater, "connection limit reached"))
			return
		}
		defer h.sessions.Remove(conn)

		h.runListenSession(conn, auth, path, providerKey, model, rawQuery, middlewareContextValues)
	})
	if upgradeErr != nil {
		logger.Warn("websocket upgrade failed for %s: %v", path, upgradeErr)
	}
}

// listenSessionAccum holds per-session transcript, audio, and usage state.
// Owned by the session goroutine (local buffer — never stored in BifrostContext).
type listenSessionAccum struct {
	mu               sync.Mutex
	finalParts       []string
	pcm              []byte
	truncated        bool
	metadataDuration float64 // seconds from type=Metadata (session audio processed)
	resultAudioEnd   float64 // max(Results.start + Results.duration) — audio timeline end
	sampleRate       int
	channels         int
	encoding         string
}

func (a *listenSessionAccum) appendFinal(transcript string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.finalParts = append(a.finalParts, transcript)
}

func (a *listenSessionAccum) appendPCM(chunk []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.truncated || len(chunk) == 0 {
		return
	}
	if len(a.pcm)+len(chunk) > listenAudioLogMaxBytes {
		a.truncated = true
		logger.Warn("listen websocket audio log buffer capped at %d bytes; play/download may be incomplete", listenAudioLogMaxBytes)
		remain := listenAudioLogMaxBytes - len(a.pcm)
		if remain > 0 {
			a.pcm = append(a.pcm, chunk[:remain]...)
		}
		return
	}
	a.pcm = append(a.pcm, chunk...)
}

func (a *listenSessionAccum) observeMetadataDuration(seconds float64) {
	if seconds <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if seconds > a.metadataDuration {
		a.metadataDuration = seconds
	}
}

// observeResultAudioEnd tracks the audio timeline cursor from Results.
// Results.duration is only the window length (often ~few seconds); billed/session
// length is start+duration.
func (a *listenSessionAccum) observeResultAudioEnd(start, windowDuration float64) {
	end := start + windowDuration
	if end <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if end > a.resultAudioEnd {
		a.resultAudioEnd = end
	}
}

func (a *listenSessionAccum) snapshot() (text string, pcm []byte, metadataDuration, resultAudioEnd float64, truncated bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	text = strings.TrimSpace(strings.Join(a.finalParts, " "))
	if len(a.pcm) > 0 {
		pcm = append([]byte(nil), a.pcm...)
	}
	return text, pcm, a.metadataDuration, a.resultAudioEnd, a.truncated
}

// resolveListenBillingDuration picks the best audio-seconds estimate for cost.
// Never use Results.duration alone — that is a per-chunk window, not session length.
func resolveListenBillingDuration(metadataDuration, resultAudioEnd, pcmDuration, wallClock float64) float64 {
	best := metadataDuration
	if resultAudioEnd > best {
		best = resultAudioEnd
	}
	if pcmDuration > best {
		best = pcmDuration
	}
	if best > 0 {
		return best
	}
	if wallClock > 0 {
		return wallClock
	}
	return 0
}

func (h *WSListenHandler) runListenSession(
	clientRaw *ws.Conn,
	auth *authHeaders,
	path string,
	providerKey schemas.ModelProvider,
	model string,
	rawQuery string,
	middlewareValues map[any]any,
) {
	clientConn := newRealtimeClientConn(clientRaw)
	clientConn.startHeartbeat()
	defer clientConn.stopHeartbeat()

	bifrostCtx, cancel := createBifrostContextFromAuth(h.handlerStore, auth)
	if bifrostCtx == nil {
		cancel()
		logger.Warn("listen websocket: failed to create request context")
		return
	}
	defer cancel()

	applyRealtimeMiddlewareValues(bifrostCtx, middlewareValues)
	bifrostCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.TranscriptionRequest)
	bifrostCtx.SetValue(schemas.BifrostContextKeyRealtimeTransport, "websocket")

	provider := h.client.GetProviderByKey(providerKey)
	listenProvider, ok := provider.(schemas.ListenWebSocketProvider)
	if !ok || !listenProvider.SupportsListenWebSocket() {
		logger.Warn("listen websocket: provider %s does not support listen", providerKey)
		return
	}

	// Key selection uses TranscriptionRequest so providers that allow transcription
	// but disable transcription_stream still work for native listen proxying.
	key, err := h.client.SelectKeyForProviderRequestType(bifrostCtx, schemas.TranscriptionRequest, providerKey, model)
	if err != nil {
		logger.Warn("listen websocket key selection failed for %s/%s: %v", providerKey, model, err)
		return
	}
	model = key.Aliases.Resolve(model)
	if strings.TrimSpace(key.ID) != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeySelectedKeyID, key.ID)
	}
	if strings.TrimSpace(key.Name) != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeySelectedKeyName, key.Name)
	}

	applyRealtimeRawStorageContext(bifrostCtx, h.client.ComputeRawStorageForProvider(bifrostCtx, providerKey))

	upstreamQuery := rewriteListenModelQuery(rawQuery, model)
	wsURL := listenProvider.ListenWebSocketURL(key, upstreamQuery)
	headers, headerErr := listenProvider.ListenHeaders(bifrostCtx, key)
	if headerErr != nil {
		logger.Warn("listen websocket headers failed for %s/%s: %v", providerKey, model, headerErr)
		return
	}

	accum := newListenSessionAccum(rawQuery)
	startedAt := time.Now()

	var proxyConfig *schemas.ProxyConfig
	if providerCfg, cfgErr := h.config.GetProviderConfigRaw(providerKey); cfgErr == nil && providerCfg != nil {
		proxyConfig = providerCfg.ProxyConfig
	}

	upstream, err := h.pool.Get(bfws.PoolKey{
		Provider: providerKey,
		KeyID:    key.ID,
		Endpoint: wsURL,
	}, mapToHTTPHeader(headers), proxyConfig)
	if err != nil {
		logger.Warn("listen websocket upstream dial failed for %s/%s: %v", providerKey, model, err)
		h.finalizeListenSession(bifrostCtx, providerKey, model, rawQuery, accum, startedAt, newRealtimeWireBifrostError(502, "server_error", err.Error()))
		return
	}
	defer h.pool.Discard(upstream)

	errCh := make(chan error, 2)
	go func() {
		errCh <- relayListenClientToUpstream(clientConn, upstream, accum)
	}()
	go func() {
		errCh <- relayListenUpstreamToClient(clientConn, upstream, accum)
	}()

	firstErr := <-errCh
	_ = upstream.Close()
	_ = clientConn.Close()
	secondErr := <-errCh

	var bifrostErr *schemas.BifrostError
	if logErr := selectRealtimeRelayError(firstErr, secondErr); logErr != nil {
		logger.Warn("listen websocket relay ended for %s/%s on %s: %v", providerKey, model, path, logErr)
		bifrostErr = newRealtimeWireBifrostError(502, "server_error", logErr.Error())
	}
	h.finalizeListenSession(bifrostCtx, providerKey, model, rawQuery, accum, startedAt, bifrostErr)
}

// finalizeListenSession creates one Logs entry at session end with usage (for cost)
// and buffered audio (for play/download). PreLLMHook runs here so TranscriptionInput.File
// is available when the logging plugin snapshots input.
func (h *WSListenHandler) finalizeListenSession(
	bifrostCtx *schemas.BifrostContext,
	providerKey schemas.ModelProvider,
	model string,
	rawQuery string,
	accum *listenSessionAccum,
	startedAt time.Time,
	bifrostErr *schemas.BifrostError,
) {
	if bifrostCtx == nil {
		return
	}

	text, pcm, metadataDuration, resultAudioEnd, _ := accum.snapshot()
	pcmDuration := durationFromPCM(pcm, accum.sampleRate, accum.channels, accum.encoding)
	wallClock := time.Since(startedAt).Seconds()
	duration := resolveListenBillingDuration(metadataDuration, resultAudioEnd, pcmDuration, wallClock)

	audioFile := buildListenLogAudio(pcm, accum.sampleRate, accum.channels, accum.encoding)
	filename := "listen.wav"
	if len(audioFile) == 0 {
		filename = "listen.websocket"
	} else if !isListenLinear16(accum.encoding) {
		filename = "listen." + sanitizeListenEncodingExt(accum.encoding)
	}

	streamReq := &schemas.BifrostRequest{
		RequestType: schemas.TranscriptionRequest,
		TranscriptionRequest: &schemas.BifrostTranscriptionRequest{
			Provider: providerKey,
			Model:    model,
			Params:   listenQueryToTranscriptionParams(rawQuery),
			Input: &schemas.TranscriptionInput{
				File:     audioFile,
				Filename: filename,
			},
		},
	}
	hooks, preErr := h.client.RunStreamPreHooks(bifrostCtx, streamReq)
	if preErr != nil {
		logger.Warn("listen websocket log pre-hooks failed for %s/%s: %v", providerKey, model, preErr.Error.Message)
		return
	}
	if hooks == nil {
		logger.Warn("listen websocket log pre-hooks returned nil hooks for %s/%s", providerKey, model)
		return
	}
	defer hooks.Cleanup()

	tracer, _ := bifrostCtx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
	traceID, _ := bifrostCtx.Value(schemas.BifrostContextKeyTraceID).(string)
	if strings.TrimSpace(traceID) != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyAccumulatorID, strings.TrimSpace(traceID))
	}

	bifrostCtx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	latency := time.Since(startedAt).Milliseconds()

	durationCopy := duration
	usage := &schemas.TranscriptionUsage{
		Type:    "duration",
		Seconds: &durationCopy,
	}
	if billing, ok := h.client.GetProviderByKey(providerKey).(schemas.ListenBillingProvider); ok {
		language := listenLanguageFromQuery(rawQuery)
		if cost, ok := billing.ListenCostUSD(model, duration, language); ok {
			usage.Cost = &schemas.BifrostCost{TotalCost: cost}
		}
	}
	resp := &schemas.BifrostResponse{
		TranscriptionResponse: &schemas.BifrostTranscriptionResponse{
			Text:     text,
			Duration: &durationCopy,
			Usage:    usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.TranscriptionRequest,
				Provider:               providerKey,
				OriginalModelRequested: model,
				ResolvedModelUsed:      model,
				Latency:                latency,
			},
		},
	}

	if bifrostErr != nil {
		bifrostErr.ExtraFields.RequestType = schemas.TranscriptionRequest
		bifrostErr.ExtraFields.Provider = providerKey
		bifrostErr.ExtraFields.OriginalModelRequested = model
		bifrostErr.ExtraFields.Latency = latency
		// Still attach partial transcript/usage on error so cost can be estimated.
		if _, postErr := hooks.PostHookRunner(bifrostCtx, resp, bifrostErr); postErr != nil {
			logger.Warn("listen websocket log post-hooks failed for %s/%s: %v", providerKey, model, postErr.Error.Message)
		}
	} else if _, postErr := hooks.PostHookRunner(bifrostCtx, resp, nil); postErr != nil {
		logger.Warn("listen websocket log post-hooks failed for %s/%s: %v", providerKey, model, postErr.Error.Message)
	}

	// PostHookRunner only auto-flushes on IsFinalChunk for stream request types.
	// Listen uses TranscriptionRequest, so flush explicitly (same as realtime WS).
	if tracer != nil && strings.TrimSpace(traceID) != "" {
		tracer.CompleteAndFlushTrace(strings.TrimSpace(traceID))
	}
	logger.Info("listen websocket log finalized for %s/%s (chars=%d duration=%.2fs meta=%.2fs result_end=%.2fs pcm=%.2fs audio_bytes=%d)",
		providerKey, model, len(text), duration, metadataDuration, resultAudioEnd, pcmDuration, len(audioFile))
}

func newListenSessionAccum(rawQuery string) *listenSessionAccum {
	values, _ := url.ParseQuery(rawQuery)
	sampleRate := parseListenIntQuery(values, "sample_rate", 16000)
	channels := parseListenIntQuery(values, "channels", 1)
	encoding := strings.ToLower(strings.TrimSpace(values.Get("encoding")))
	if encoding == "" {
		encoding = "linear16"
	}
	return &listenSessionAccum{
		sampleRate: sampleRate,
		channels:   channels,
		encoding:   encoding,
	}
}

func parseListenIntQuery(values url.Values, key string, fallback int) int {
	raw := strings.TrimSpace(values.Get(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func isListenLinear16(encoding string) bool {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "linear16", "pcm", "pcm16", "pcm_s16le", "pcm_s16le_16":
		return true
	default:
		return false
	}
}

func sanitizeListenEncodingExt(encoding string) string {
	ext := strings.ToLower(strings.TrimSpace(encoding))
	if ext == "" {
		return "bin"
	}
	ext = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, ext)
	if ext == "" {
		return "bin"
	}
	return ext
}

func durationFromPCM(pcm []byte, sampleRate, channels int, encoding string) float64 {
	if len(pcm) == 0 || sampleRate <= 0 || channels <= 0 || !isListenLinear16(encoding) {
		return 0
	}
	bytesPerSample := 2 * channels
	if bytesPerSample <= 0 {
		return 0
	}
	return float64(len(pcm)) / float64(sampleRate*bytesPerSample)
}

// buildListenLogAudio wraps buffered client audio for the Logs player. linear16
// becomes a WAV so browsers can play/download without a pcm16 converter path.
func buildListenLogAudio(pcm []byte, sampleRate, channels int, encoding string) []byte {
	if len(pcm) == 0 {
		return nil
	}
	if isListenLinear16(encoding) {
		return wrapPCM16AsWAV(pcm, sampleRate, channels)
	}
	// Non-PCM encodings: store raw bytes; UI may not play them but download works.
	return append([]byte(nil), pcm...)
}

func wrapPCM16AsWAV(pcm []byte, sampleRate, channels int) []byte {
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	if channels <= 0 {
		channels = 1
	}
	const bitsPerSample = 16
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8
	dataSize := len(pcm)
	out := make([]byte, 44+dataSize)
	copy(out[0:], []byte("RIFF"))
	binary.LittleEndian.PutUint32(out[4:], uint32(36+dataSize))
	copy(out[8:], []byte("WAVE"))
	copy(out[12:], []byte("fmt "))
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1) // PCM
	binary.LittleEndian.PutUint16(out[22:], uint16(channels))
	binary.LittleEndian.PutUint32(out[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(out[28:], uint32(byteRate))
	binary.LittleEndian.PutUint16(out[32:], uint16(blockAlign))
	binary.LittleEndian.PutUint16(out[34:], bitsPerSample)
	copy(out[36:], []byte("data"))
	binary.LittleEndian.PutUint32(out[40:], uint32(dataSize))
	copy(out[44:], pcm)
	return out
}

func resolveListenTarget(modelParam string) (schemas.ModelProvider, string, error) {
	rawParam := strings.TrimSpace(modelParam)
	if rawParam == "" {
		return "", "", fmt.Errorf("model query parameter is required for listen websocket")
	}
	provider, model := schemas.ParseModelString(rawParam, schemas.Deepgram)
	if strings.TrimSpace(model) == "" {
		return "", "", fmt.Errorf("model query parameter must resolve to a model name")
	}
	return provider, model, nil
}

// rewriteListenModelQuery replaces the model query value with resolvedModel while
// preserving all other parameters. When the model is unchanged, the original
// query string is returned so encoding/order stay intact for Deepgram clients.
func rewriteListenModelQuery(rawQuery, resolvedModel string) string {
	if resolvedModel == "" {
		return rawQuery
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}
	if values.Get("model") == resolvedModel {
		return rawQuery
	}
	values.Set("model", resolvedModel)
	return values.Encode()
}

// listenQueryToTranscriptionParams maps Deepgram listen query params into
// TranscriptionParameters so they appear on the Logs page. All query keys are
// preserved in ExtraParams (pass-through); known fields are also promoted.
func listenQueryToTranscriptionParams(rawQuery string) *schemas.TranscriptionParameters {
	values, err := url.ParseQuery(rawQuery)
	if err != nil || len(values) == 0 {
		return nil
	}
	extra := make(map[string]interface{}, len(values))
	for key, vals := range values {
		if key == "model" {
			continue
		}
		if len(vals) == 1 {
			extra[key] = vals[0]
		} else {
			extra[key] = vals
		}
	}
	params := &schemas.TranscriptionParameters{ExtraParams: extra}
	if lang := strings.TrimSpace(values.Get("language")); lang != "" {
		params.Language = &lang
	}
	return params
}

func listenLanguageFromQuery(rawQuery string) string {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(values.Get("language"))
}

// deepgramListenResults is a minimal parse of Deepgram listen Results messages.
type deepgramListenResults struct {
	Type     string   `json:"type"`
	IsFinal  *bool    `json:"is_final"`
	Start    *float64 `json:"start"`
	Duration *float64 `json:"duration"`
	Channel  *struct {
		Alternatives []struct {
			Transcript string `json:"transcript"`
		} `json:"alternatives"`
	} `json:"channel"`
}

type deepgramListenMetadata struct {
	Type     string   `json:"type"`
	Duration *float64 `json:"duration"`
}

func extractListenTranscript(message []byte) (transcript string, isFinal bool) {
	var msg deepgramListenResults
	if err := json.Unmarshal(message, &msg); err != nil {
		return "", false
	}
	if !strings.EqualFold(msg.Type, "Results") || msg.Channel == nil || len(msg.Channel.Alternatives) == 0 {
		return "", false
	}
	transcript = strings.TrimSpace(msg.Channel.Alternatives[0].Transcript)
	if transcript == "" {
		return "", false
	}
	if msg.IsFinal != nil {
		isFinal = *msg.IsFinal
	}
	return transcript, isFinal
}

func observeListenUpstreamMessage(accum *listenSessionAccum, message []byte) {
	if accum == nil || len(message) == 0 {
		return
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(message, &envelope); err != nil {
		return
	}
	switch {
	case strings.EqualFold(envelope.Type, "Metadata"):
		var meta deepgramListenMetadata
		if err := json.Unmarshal(message, &meta); err != nil {
			return
		}
		if meta.Duration != nil {
			accum.observeMetadataDuration(*meta.Duration)
		}
	case strings.EqualFold(envelope.Type, "Results"):
		var msg deepgramListenResults
		if err := json.Unmarshal(message, &msg); err != nil {
			return
		}
		var start, window float64
		if msg.Start != nil {
			start = *msg.Start
		}
		if msg.Duration != nil {
			window = *msg.Duration
		}
		accum.observeResultAudioEnd(start, window)
		transcript, isFinal := extractListenTranscript(message)
		if transcript != "" && isFinal {
			accum.appendFinal(transcript)
		}
	}
}

func relayListenClientToUpstream(client *realtimeClientConn, upstream *bfws.UpstreamConn, accum *listenSessionAccum) error {
	for {
		messageType, message, err := client.ReadMessage()
		if err != nil {
			if isNormalWebSocketClosure(err) {
				return nil
			}
			return err
		}
		switch messageType {
		case ws.BinaryMessage:
			if accum != nil {
				accum.appendPCM(message)
			}
			if err := upstream.WriteMessage(messageType, message); err != nil {
				return err
			}
		case ws.TextMessage:
			if err := upstream.WriteMessage(messageType, message); err != nil {
				return err
			}
		}
	}
}

func relayListenUpstreamToClient(client *realtimeClientConn, upstream *bfws.UpstreamConn, accum *listenSessionAccum) error {
	for {
		messageType, message, err := upstream.ReadMessage()
		if err != nil {
			if isNormalWebSocketClosure(err) {
				return nil
			}
			return err
		}
		switch messageType {
		case ws.TextMessage:
			observeListenUpstreamMessage(accum, message)
			if err := client.WriteMessage(messageType, message); err != nil {
				return err
			}
		case ws.BinaryMessage:
			if err := client.WriteMessage(messageType, message); err != nil {
				return err
			}
		}
	}
}
