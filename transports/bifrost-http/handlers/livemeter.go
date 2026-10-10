package handlers

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// liveBillingWindowSeconds is how much voice time accrues before it is billed and the
// session's budget is checked again.
const liveBillingWindowSeconds = 30.0

// Unit kinds, as BifrostContextKeyLiveUnit names them for plugins.
const (
	liveUnitVoice   = "voice"
	liveUnitBackend = "backend"
)

// liveUnitRunner is the slice of *bifrost.Bifrost the meter needs; tests substitute it.
type liveUnitRunner interface {
	RunRealtimeTurnPreHooks(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*bifrost.RealtimeTurnHooks, *schemas.BifrostError)
}

// liveBillingUnit is one admitted pass through the plugin pipeline: pre-hooks already ran and
// admitted it, and the post-hooks that close it bill whatever usage accrued under it.
type liveBillingUnit struct {
	hooks     *bifrost.RealtimeTurnHooks
	requestID string
	traceID   string
	startedAt time.Time
	preValues map[any]any
}

// liveLane is one model's rolling billing unit. The next unit is admitted before the current one
// closes, so usage always lands on a unit governance already admitted.
type liveLane struct {
	model   string
	backend bool // backend Responses tokens rather than voice seconds
	current *liveBillingUnit

	seconds     float64                         // voice: accrued, unbilled seconds
	usage       *schemas.ResponsesResponseUsage // backend: accrued, unbilled tokens
	serviceTier *schemas.BifrostServiceTier
	outputs     []schemas.ResponsesMessage // backend: what the responses billed on the current unit produced
	responseID  string                     // backend: the last response billed on the current unit
	delegation  string                     // backend: the delegation the last response ran
	delegatedMs int64                      // backend: when that delegation began, on the session timeline
}

// liveEnding is what the unit closing the session carries: what was said.
type liveEnding struct {
	transcript []schemas.LiveTranscriptLine
}

// liveMeter bills one GPT Live session: voice seconds in windows, each backend response once.
// It holds only counters, never stream data.
type liveMeter struct {
	runner   liveUnitRunner
	baseCtx  *schemas.BifrostContext
	provider schemas.ModelProvider
	key      schemas.Key
	window   float64

	sessionID string // Bifrost's id: every unit groups under it, from admission on

	mu                sync.Mutex
	providerSessionID string // OpenAI's id, known from session.started
	voice             *liveLane
	backends          map[string]*liveLane
	activeBackend     string
	reportedSeconds   float64 // latest cumulative seconds OpenAI reported
	lastUsageAt       time.Time
	billedResponses   map[string]struct{}
	allowedKeys       []string              // keys every admitted unit may use, as governance narrowed them
	keysRestricted    bool                  // allowedKeys applies; an empty list then allows no key
	refusal           *schemas.BifrostError // set once the session may not continue
	minimumSeconds    float64               // least voice time the session bills
	finished          bool
	transport         string      // websocket or webrtc, for the session's log row
	ending            *liveEnding // nil until the session ends
}

func newLiveMeter(runner liveUnitRunner, baseCtx *schemas.BifrostContext, provider schemas.ModelProvider, key schemas.Key, sessionID string) *liveMeter {
	return &liveMeter{
		runner:          runner,
		baseCtx:         baseCtx,
		provider:        provider,
		key:             key,
		window:          liveBillingWindowSeconds,
		sessionID:       sessionID,
		backends:        make(map[string]*liveLane),
		billedResponses: make(map[string]struct{}),
		lastUsageAt:     time.Now(),
	}
}

// admit opens the session's first units. The voice unit is the session's one request; the
// backend unit is admitted as a continuation, which also checks the backend model is allowed.
func (m *liveMeter) admit(voiceModel, backendModel string) *schemas.BifrostError {
	m.mu.Lock()
	defer m.mu.Unlock()
	unit, bifrostErr := m.openUnit(liveUnitVoice, voiceModel, false)
	if bifrostErr != nil {
		return bifrostErr
	}
	m.voice = &liveLane{model: voiceModel, current: unit}
	if backendModel == "" {
		return nil
	}
	if bifrostErr := m.openBackendLane(backendModel); bifrostErr != nil {
		m.finished = true
		m.closeUnitWithError(liveUnitVoice, m.voice.current, voiceModel, bifrostErr)
		m.voice = nil
		return bifrostErr
	}
	m.activeBackend = backendModel
	return nil
}

// keySelectionContext is the session's context narrowed to the keys governance allows its units.
func (m *liveMeter) keySelectionContext() *schemas.BifrostContext {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx := schemas.NewBifrostContext(m.baseCtx, schemas.NoDeadline)
	if m.keysRestricted {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys, m.allowedKeys)
	}
	return ctx
}

// setKey records the key the session was admitted on.
func (m *liveMeter) setKey(key schemas.Key) {
	m.mu.Lock()
	m.key = key
	m.mu.Unlock()
}

// setTransport records how the client is connected, for the session's log row.
func (m *liveMeter) setTransport(transport string) {
	m.mu.Lock()
	m.transport = transport
	m.mu.Unlock()
}

// setEnding records what the unit closing the session carries: the transcript. Call it before finish.
func (m *liveMeter) setEnding(transcript []schemas.LiveTranscriptLine) {
	m.mu.Lock()
	m.ending = &liveEnding{transcript: transcript}
	m.mu.Unlock()
}

// abort ends an admitted session that never ran, with the error that stopped it, so plugins see
// the session close and log why.
func (m *liveMeter) abort(bifrostErr *schemas.BifrostError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finished {
		return
	}
	m.finished = true
	for _, lane := range m.backends {
		m.closeUnitWithError(liveUnitBackend, lane.current, lane.model, bifrostErr)
		lane.current = nil
	}
	if m.voice != nil {
		m.closeUnitWithError(liveUnitVoice, m.voice.current, m.voice.model, bifrostErr)
		m.voice.current = nil
	}
}

// setProviderSessionID records OpenAI's session id once session.started names it.
func (m *liveMeter) setProviderSessionID(id string) {
	if id = strings.TrimSpace(id); id == "" {
		return
	}
	m.mu.Lock()
	m.providerSessionID = id
	m.mu.Unlock()
}

// onUsage records a cumulative session.usage.updated snapshot and bills a full window.
// It returns the refusal that ends the session, if the next window was not admitted.
func (m *liveMeter) onUsage(cumulativeSeconds float64) *schemas.BifrostError {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastUsageAt = time.Now()
	m.accrueSecondsLocked(cumulativeSeconds)
	if m.voice == nil || m.voice.seconds < m.window {
		return m.refusal
	}
	m.rotateLocked(m.voice)
	return m.refusal
}

// checkStale re-admits the voice lane when OpenAI has sent no usage for two windows, so an idle
// session still meets the budget check. Nothing is billed that OpenAI did not report.
func (m *liveMeter) checkStale(now time.Time) *schemas.BifrostError {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.voice == nil || m.finished || m.refusal != nil || now.Sub(m.lastUsageAt) < 2*time.Duration(m.window*float64(time.Second)) {
		return m.refusal
	}
	m.lastUsageAt = now
	m.rotateLocked(m.voice)
	return m.refusal
}

// onBackendResponse bills one terminal backend Responses event, once per response id, for the
// delegation it ran.
func (m *liveMeter) onBackendResponse(response *schemas.BifrostResponsesResponse, delegationID string, delegatedMs int64) *schemas.BifrostError {
	if response == nil || response.Usage == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if response.ID != nil && *response.ID != "" {
		if _, billed := m.billedResponses[*response.ID]; billed {
			return m.refusal
		}
		m.billedResponses[*response.ID] = struct{}{}
	}
	lane := m.backendLaneForLocked(response.Model)
	if lane == nil {
		return m.refusal
	}
	lane.usage = addResponsesUsage(lane.usage, response.Usage)
	lane.outputs = append(lane.outputs, response.Output...)
	if response.ID != nil {
		lane.responseID = *response.ID
	}
	if response.ServiceTier != nil {
		lane.serviceTier = response.ServiceTier
	}
	lane.delegation, lane.delegatedMs = delegationID, delegatedMs
	m.rotateLocked(lane)
	return m.refusal
}

// switchBackend admits a backend model changed by session.update. A refusal leaves the current
// backend in place.
func (m *liveMeter) switchBackend(model string) *schemas.BifrostError {
	m.mu.Lock()
	defer m.mu.Unlock()
	// finish ran the open units' post-hooks and never runs again, so a unit opened now would leak.
	if m.finished {
		return newRealtimeWireBifrostError(409, "invalid_request_error", "the live session has ended")
	}
	if m.refusal != nil {
		return m.refusal
	}
	if _, open := m.backends[model]; !open {
		if bifrostErr := m.openBackendLane(model); bifrostErr != nil {
			return bifrostErr
		}
	}
	m.activeBackend = model
	return nil
}

// setMinimumSeconds sets the least voice time the session bills. OpenAI bills a WebRTC session
// 15 seconds when it is created, credited against its running time.
func (m *liveMeter) setMinimumSeconds(seconds float64) {
	m.mu.Lock()
	m.minimumSeconds = seconds
	m.mu.Unlock()
}

// finish bills everything still accrued. finalSeconds is session.closed's usage, or the last
// reported snapshot when the session ended without one.
func (m *liveMeter) finish(finalSeconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finished {
		return
	}
	m.finished = true
	m.accrueSecondsLocked(max(finalSeconds, m.minimumSeconds))
	// The voice lane closes last: its unit carries the session's end for plugins.
	for _, lane := range m.backends {
		m.closeLaneLocked(lane)
	}
	if m.voice != nil {
		m.closeLaneLocked(m.voice)
	}
}

// lastReportedSeconds is the latest cumulative voice time OpenAI reported.
func (m *liveMeter) lastReportedSeconds() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reportedSeconds
}

func (m *liveMeter) accrueSecondsLocked(cumulativeSeconds float64) {
	// Snapshots are cumulative: only the growth since the last one is new usage.
	if m.voice == nil || cumulativeSeconds <= m.reportedSeconds {
		return
	}
	m.voice.seconds += cumulativeSeconds - m.reportedSeconds
	m.reportedSeconds = cumulativeSeconds
}

// rotateLocked admits the lane's next unit, then bills the accrued usage on the current one.
// A refusal keeps the current unit open and marks the session as ending.
func (m *liveMeter) rotateLocked(lane *liveLane) {
	if m.finished || m.refusal != nil {
		return
	}
	kind := liveUnitVoice
	if lane.backend {
		kind = liveUnitBackend
	}
	next, bifrostErr := m.openUnit(kind, lane.model, true)
	if bifrostErr != nil {
		m.refusal = bifrostErr
		return
	}
	m.closeLaneLocked(lane)
	lane.current = next
}

// closeLaneLocked bills the lane's accrued usage on its current unit and clears it.
func (m *liveMeter) closeLaneLocked(lane *liveLane) {
	if lane.current == nil {
		return
	}
	unit := lane.current
	lane.current = nil
	resp := &schemas.BifrostResponsesResponse{
		Object: "response",
		Model:  lane.model,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:            schemas.LiveRequest,
			Provider:               m.provider,
			OriginalModelRequested: lane.model,
			Latency:                time.Since(unit.startedAt).Milliseconds(),
		},
	}
	kind := liveUnitVoice
	if lane.backend {
		kind = liveUnitBackend
		resp.ExtraFields.PricingRequestType = schemas.ResponsesRequest
		resp.ServiceTier = lane.serviceTier
		resp.Usage = lane.usage
		if resp.Usage == nil {
			resp.Usage = &schemas.ResponsesResponseUsage{}
		}
		resp.Output = lane.outputs
		if lane.responseID != "" {
			resp.ID = new(lane.responseID)
		}
	}
	result := &schemas.BifrostResponse{ResponsesResponse: resp}
	postCtx := m.unitContext(unit.requestID, false, kind)
	if lane.backend {
		if lane.delegation != "" {
			postCtx.SetValue(schemas.BifrostContextKeyLiveDelegationID, lane.delegation)
			postCtx.SetValue(schemas.BifrostContextKeyLiveDelegationStartMs, lane.delegatedMs)
		}
		lane.usage, lane.outputs, lane.responseID, lane.delegation, lane.delegatedMs = nil, nil, "", "", 0
	} else {
		seconds := lane.seconds
		resp.Usage = &schemas.ResponsesResponseUsage{AudioSeconds: &seconds}
		lane.seconds = 0
	}
	if !lane.backend && m.finished {
		m.markEndLocked(postCtx)
		result.LiveSession = m.sessionLogLocked()
	}
	m.runPostHooks(unit, postCtx, result, nil)
}

func (m *liveMeter) closeUnitWithError(kind string, unit *liveBillingUnit, model string, bifrostErr *schemas.BifrostError) {
	if unit == nil {
		return
	}
	postErr := *bifrostErr
	postErr.ExtraFields.RequestType = schemas.LiveRequest
	postErr.ExtraFields.Provider = m.provider
	postErr.ExtraFields.OriginalModelRequested = model
	postCtx := m.unitContext(unit.requestID, false, kind)
	if kind == liveUnitVoice && m.finished {
		m.markEndLocked(postCtx)
	}
	m.runPostHooks(unit, postCtx, nil, &postErr)
}

// sessionLogLocked is what the closing unit reports about the session for its log row.
func (m *liveMeter) sessionLogLocked() *schemas.LiveSessionLog {
	log := &schemas.LiveSessionLog{Transport: m.transport, ProviderSessionID: m.providerSessionID}
	if m.ending != nil {
		log.Transcript = m.ending.transcript
	}
	return log
}

// markEndLocked flags the unit that closes the session.
func (m *liveMeter) markEndLocked(postCtx *schemas.BifrostContext) {
	postCtx.SetValue(schemas.BifrostContextKeyLiveSessionEnd, true)
}

func (m *liveMeter) runPostHooks(unit *liveBillingUnit, postCtx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) {
	defer func() {
		if unit.hooks.Cleanup != nil {
			unit.hooks.Cleanup()
		}
	}()
	applyRealtimeTurnContextValues(postCtx, unit.preValues)
	// The units admitted before the key was chosen bill on it too.
	if strings.TrimSpace(m.key.ID) != "" {
		postCtx.SetValue(schemas.BifrostContextKeySelectedKeyID, m.key.ID)
	}
	if strings.TrimSpace(m.key.Name) != "" {
		postCtx.SetValue(schemas.BifrostContextKeySelectedKeyName, m.key.Name)
	}
	restoreRealtimeTurnTraceContext(postCtx, unit.traceID, unit.preValues)
	setRealtimeTurnStreamContext(postCtx, unit.startedAt, true)
	if _, hookErr := unit.hooks.PostHookRunner(postCtx, resp, bifrostErr); hookErr != nil && hookErr.Error != nil {
		logger.Warn("live session %s (%s) billing post-hook returned an error: %s", m.sessionID, m.providerSessionID, hookErr.Error.Message)
	}
	completeRealtimeTurnTrace(postCtx)
}

// openBackendLane admits a backend model's lane as a continuation of the session.
func (m *liveMeter) openBackendLane(model string) *schemas.BifrostError {
	unit, bifrostErr := m.openUnit(liveUnitBackend, model, true)
	if bifrostErr != nil {
		return bifrostErr
	}
	m.backends[model] = &liveLane{model: model, backend: true, current: unit}
	return nil
}

// backendLaneForLocked picks the lane a backend response bills to: the lane whose model the
// response names (OpenAI may report a dated snapshot of it), else the active backend.
func (m *liveMeter) backendLaneForLocked(responseModel string) *liveLane {
	if lane, ok := m.backends[responseModel]; ok {
		return lane
	}
	for model, lane := range m.backends {
		if strings.HasPrefix(responseModel, model+"-") {
			return lane
		}
	}
	return m.backends[m.activeBackend]
}

// openUnit runs the plugin pre-hooks for one billing unit.
func (m *liveMeter) openUnit(kind, model string, continuation bool) (*liveBillingUnit, *schemas.BifrostError) {
	requestID := uuid.NewString()
	preCtx := m.unitContext(requestID, continuation, kind)
	if kind == liveUnitVoice && !continuation {
		preCtx.SetValue(schemas.BifrostContextKeyLiveSessionStart, true)
	}
	startedAt := time.Now()
	setRealtimeTurnStreamContext(preCtx, startedAt, false)
	req := &schemas.BifrostRequest{
		RequestType: schemas.LiveRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: m.provider,
			Model:    model,
		},
	}
	hooks, bifrostErr := m.runner.RunRealtimeTurnPreHooks(preCtx, req)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if ids, ok := preCtx.Value(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys).([]string); ok {
		m.narrowKeysLocked(ids)
	}
	traceID, _ := preCtx.Value(schemas.BifrostContextKeyTraceID).(string)
	return &liveBillingUnit{
		hooks:     hooks,
		requestID: requestID,
		traceID:   traceID,
		startedAt: startedAt,
		preValues: preCtx.GetUserValues(),
	}, nil
}

// narrowKeysLocked keeps only the keys every admitted unit may use.
func (m *liveMeter) narrowKeysLocked(ids []string) {
	if !m.keysRestricted {
		m.allowedKeys, m.keysRestricted = slices.Clone(ids), true
		return
	}
	m.allowedKeys = slices.DeleteFunc(m.allowedKeys, func(id string) bool { return !slices.Contains(ids, id) })
}

// unitContext builds a billing unit's context from the session's: same identity and grant, its
// own request id and trace, grouped under the session id. Callers hold m.mu.
func (m *liveMeter) unitContext(requestID string, continuation bool, kind string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if m.baseCtx != nil {
		for ctxKey, value := range m.baseCtx.GetUserValues() {
			// Each unit mints its own trace; a session-level trace would strand its log entry.
			if value == nil || ctxKey == schemas.BifrostContextKeyTraceID || ctxKey == schemas.BifrostContextKeyExportTraceID {
				continue
			}
			ctx.SetValue(ctxKey, value)
		}
		if g := m.baseCtx.Grant(); g != nil {
			ctx.SetGrant(g)
		}
	}
	ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.LiveRequest)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
	parentID := m.sessionID
	if m.baseCtx != nil {
		if external, ok := m.baseCtx.Value(schemas.BifrostContextKeyParentRequestID).(string); ok && strings.TrimSpace(external) != "" {
			parentID = strings.TrimSpace(external)
		}
	}
	if parentID != "" {
		ctx.SetValue(schemas.BifrostContextKeyParentRequestID, parentID)
	}
	if continuation {
		ctx.SetValue(schemas.BifrostContextKeySessionContinuation, true)
	}
	ctx.SetValue(schemas.BifrostContextKeyLiveSessionID, m.sessionID)
	ctx.SetValue(schemas.BifrostContextKeyLiveUnit, kind)
	if m.providerSessionID != "" {
		ctx.SetValue(schemas.BifrostContextKeyRealtimeProviderSessionID, m.providerSessionID)
	}
	if m.transport != "" {
		ctx.SetValue(schemas.BifrostContextKeyRealtimeTransport, m.transport)
	}
	if strings.TrimSpace(m.key.ID) != "" {
		ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, m.key.ID)
	}
	if strings.TrimSpace(m.key.Name) != "" {
		ctx.SetValue(schemas.BifrostContextKeySelectedKeyName, m.key.Name)
	}
	return ctx
}

// addResponsesUsage sums two Responses usage records.
func addResponsesUsage(total, usage *schemas.ResponsesResponseUsage) *schemas.ResponsesResponseUsage {
	if total == nil {
		copied := *usage
		if usage.InputTokensDetails != nil {
			details := *usage.InputTokensDetails
			copied.InputTokensDetails = &details
		}
		if usage.OutputTokensDetails != nil {
			details := *usage.OutputTokensDetails
			copied.OutputTokensDetails = &details
		}
		return &copied
	}
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
	total.TotalTokens += usage.TotalTokens
	if usage.InputTokensDetails != nil {
		if total.InputTokensDetails == nil {
			total.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
		}
		total.InputTokensDetails.TextTokens += usage.InputTokensDetails.TextTokens
		total.InputTokensDetails.AudioTokens += usage.InputTokensDetails.AudioTokens
		total.InputTokensDetails.ImageTokens += usage.InputTokensDetails.ImageTokens
		total.InputTokensDetails.CachedReadTokens += usage.InputTokensDetails.CachedReadTokens
		total.InputTokensDetails.CachedWriteTokens += usage.InputTokensDetails.CachedWriteTokens
	}
	if usage.OutputTokensDetails != nil {
		if total.OutputTokensDetails == nil {
			total.OutputTokensDetails = &schemas.ResponsesResponseOutputTokens{}
		}
		total.OutputTokensDetails.TextTokens += usage.OutputTokensDetails.TextTokens
		total.OutputTokensDetails.AudioTokens += usage.OutputTokensDetails.AudioTokens
		total.OutputTokensDetails.ReasoningTokens += usage.OutputTokensDetails.ReasoningTokens
	}
	return total
}
