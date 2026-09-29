package logging

import (
	"strings"
	"sync"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// A GPT Live session bills through many units (voice windows, delegated backend calls) but is
// one conversation, so it logs as one row: the session, with its transcript and its delegations
// in a typed payload. The unit that opens the session registers the row, every unit folds its
// usage into it, and the unit that ends the session writes it.
const liveSessionObject = "live.session"

// liveSessionState is what a session's units have folded into its pending row so far.
type liveSessionState struct {
	mu           sync.Mutex
	voiceSeconds float64
	voiceCost    float64
	backendCost  float64
	usage        *schemas.BifrostLLMUsage // backend tokens, summed
	delegations  []schemas.LiveDelegationLog
	toolCalls    []schemas.ChatAssistantMessageToolCall
	toolNames    []string // every tool the backend called, function or server, for the row's filter
	failure      *schemas.BifrostError
}

// foldDelegation adds one billed response to its delegation, or starts the delegation.
func (s *liveSessionState) foldDelegation(delegation schemas.LiveDelegationLog) {
	if delegation.DelegationID != "" {
		for i := range s.delegations {
			merged := &s.delegations[i]
			if merged.DelegationID != delegation.DelegationID {
				continue
			}
			merged.ResponseIDs = append(merged.ResponseIDs, delegation.ResponseIDs...)
			merged.Output = append(merged.Output, delegation.Output...)
			if delegation.Usage != nil {
				merged.Usage = addLLMUsage(merged.Usage, delegation.Usage)
			}
			if delegation.Cost != nil {
				cost := *delegation.Cost
				if merged.Cost != nil {
					cost += *merged.Cost
				}
				merged.Cost = new(cost)
			}
			if delegation.Error != "" {
				merged.Error = delegation.Error
			}
			if merged.StartedMs == 0 {
				merged.StartedMs = delegation.StartedMs
			}
			return
		}
	}
	s.delegations = append(s.delegations, delegation)
}

func liveUnitKind(ctx *schemas.BifrostContext) string {
	kind, _ := ctx.Value(schemas.BifrostContextKeyLiveUnit).(string)
	return kind
}

func liveSessionID(ctx *schemas.BifrostContext) string {
	id, _ := ctx.Value(schemas.BifrostContextKeyLiveSessionID).(string)
	return id
}

func liveFlag(ctx *schemas.BifrostContext, key schemas.BifrostContextKey) bool {
	flag, _ := ctx.Value(key).(bool)
	return flag
}

// postLiveUnit folds one closed unit into its session's pending row, and writes the session
// once the unit that ends it closes.
func (p *LoggerPlugin) postLiveUnit(ctx *schemas.BifrostContext, kind string, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	sessionID := liveSessionID(ctx)
	pendingVal, ok := p.pendingLogsEntries.Load(sessionID)
	if !ok {
		p.logger.Warn("live session %s: no pending log entry for a %s unit, skipping", sessionID, kind)
		return result, bifrostErr, nil
	}
	pending := pendingVal.(*PendingLogData)
	pending.LastActivity.Store(time.Now().UnixNano())
	state := pending.Live
	if state == nil {
		p.logger.Warn("live session %s: pending log entry carries no session state, skipping", sessionID)
		return result, bifrostErr, nil
	}
	contentLoggingEnabled := p.contentLoggingEnabled(ctx)
	shouldStoreRaw, _ := ctx.Value(schemas.BifrostContextKeyShouldStoreRawInLogs).(bool)
	cost := p.liveUnitCost(ctx, pending, result)

	state.mu.Lock()
	switch kind {
	case "voice":
		state.voiceCost += cost
		if result != nil && result.ResponsesResponse != nil && result.ResponsesResponse.Usage != nil && result.ResponsesResponse.Usage.AudioSeconds != nil {
			state.voiceSeconds += *result.ResponsesResponse.Usage.AudioSeconds
		}
	case "backend":
		// A backend lane's open unit closes empty at session end; only calls that ran are delegations.
		if delegation, ok := liveDelegation(ctx, result, bifrostErr, cost, contentLoggingEnabled); ok {
			state.backendCost += cost
			if delegation.Usage != nil {
				state.usage = addLLMUsage(state.usage, delegation.Usage)
			}
			if result != nil && result.ResponsesResponse != nil {
				state.toolCalls = append(state.toolCalls, collectToolCalls(nil, result.ResponsesResponse.Output)...)
				for _, name := range liveToolNames(result.ResponsesResponse.Output) {
					if !containsString(state.toolNames, name) {
						state.toolNames = append(state.toolNames, name)
					}
				}
			}
			state.foldDelegation(delegation)
		}
	}
	if bifrostErr != nil {
		state.failure = bifrostErr
	}
	state.mu.Unlock()

	if !liveFlag(ctx, schemas.BifrostContextKeyLiveSessionEnd) {
		return result, bifrostErr, nil
	}
	p.pendingLogsEntries.Delete(sessionID)
	p.enqueueLogEntry(p.buildLiveSessionEntry(ctx, pending, result, contentLoggingEnabled, shouldStoreRaw), p.makePostWriteCallback(nil))
	return result, bifrostErr, nil
}

// liveUnitCost prices one unit the way its request type is priced: voice seconds for a window,
// Responses tokens for a delegation.
func (p *LoggerPlugin) liveUnitCost(ctx *schemas.BifrostContext, pending *PendingLogData, result *schemas.BifrostResponse) float64 {
	if p.pricingManager == nil || result == nil {
		return 0
	}
	scopes := modelcatalog.PricingLookupScopesFromContext(ctx, pending.InitialData.Provider)
	if breakdown := p.pricingManager.CalculateCostBreakdown(result, scopes); breakdown != nil {
		return breakdown.TotalCost
	}
	return 0
}

// liveDelegation describes one billed backend response as the delegation it ran, from the unit
// that billed it. A unit that billed nothing and produced nothing is not a delegation.
func liveDelegation(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError, cost float64, contentLoggingEnabled bool) (schemas.LiveDelegationLog, bool) {
	requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	delegationID, _ := ctx.Value(schemas.BifrostContextKeyLiveDelegationID).(string)
	startedMs, _ := ctx.Value(schemas.BifrostContextKeyLiveDelegationStartMs).(int64)
	_, _, requestedModel, resolvedModel := bifrost.GetResponseFields(result, bifrostErr)
	delegation := schemas.LiveDelegationLog{DelegationID: delegationID, RequestID: requestID, Model: resolvedModel, StartedMs: startedMs}
	if delegation.Model == "" {
		delegation.Model = requestedModel
	}
	if cost > 0 {
		delegation.Cost = new(cost)
	}
	if bifrostErr != nil && bifrostErr.Error != nil {
		delegation.Error = bifrostErr.Error.Message
	}
	if result == nil || result.ResponsesResponse == nil {
		return delegation, delegation.Error != ""
	}
	response := result.ResponsesResponse
	if response.ID != nil && *response.ID != "" {
		delegation.ResponseIDs = []string{*response.ID}
	}
	if response.Usage != nil && response.Usage.TotalTokens > 0 {
		delegation.Usage = response.Usage.ToBifrostLLMUsage()
	}
	if contentLoggingEnabled {
		delegation.Output = response.Output
	}
	ran := delegation.Usage != nil || len(response.Output) > 0 || delegation.Error != ""
	return delegation, ran
}

// liveToolNames lists the tools a delegated response called, in order: a function call by its
// name, a server tool (web_search_call, file_search_call, ...) by its kind.
func liveToolNames(output []schemas.ResponsesMessage) []string {
	var names []string
	for _, item := range output {
		if item.Type == nil {
			continue
		}
		switch {
		case *item.Type == schemas.ResponsesMessageTypeFunctionCall:
			if item.ResponsesToolMessage != nil && item.ResponsesToolMessage.Name != nil {
				names = append(names, *item.ResponsesToolMessage.Name)
			}
		case strings.HasSuffix(string(*item.Type), "_call"):
			names = append(names, strings.TrimSuffix(string(*item.Type), "_call"))
		}
	}
	return names
}

// buildLiveSessionEntry is the session's row: what the pre-hook captured, the totals its units
// folded, and the session log the ending unit carries.
func (p *LoggerPlugin) buildLiveSessionEntry(ctx *schemas.BifrostContext, pending *PendingLogData, result *schemas.BifrostResponse, contentLoggingEnabled, shouldStoreRaw bool) *logstore.Log {
	state := pending.Live
	state.mu.Lock()
	defer state.mu.Unlock()

	entry := buildCompleteLogEntryFromPending(pending)
	entry.Status = logStatusSuccess
	if state.failure != nil {
		entry.Status = logStatusForError(state.failure)
		entry.ErrorDetailsParsed = sanitizeErrorForLogging(state.failure, contentLoggingEnabled, shouldStoreRaw)
	}

	session := &schemas.LiveSessionLog{}
	if result != nil && result.LiveSession != nil {
		copied := *result.LiveSession
		session = &copied
	}
	session.VoiceSeconds = state.voiceSeconds
	if state.voiceCost > 0 {
		session.VoiceCost = new(state.voiceCost)
	}
	if state.backendCost > 0 {
		session.BackendCost = new(state.backendCost)
	}
	session.Delegations = state.delegations
	if !contentLoggingEnabled {
		session.Transcript = nil
		for i := range session.Delegations {
			session.Delegations[i].Output = nil
		}
	}
	entry.LiveSessionParsed = session

	if cost := state.voiceCost + state.backendCost; cost > 0 {
		entry.Cost = new(cost)
	}
	usage := state.usage
	if usage == nil {
		usage = &schemas.BifrostLLMUsage{}
	}
	usage.AudioSeconds = new(session.VoiceSeconds)
	entry.TokenUsageParsed = usage
	entry.PromptTokens = usage.PromptTokens
	entry.CompletionTokens = usage.CompletionTokens
	entry.TotalTokens = usage.TotalTokens
	applyToolCallsToEntry(entry, state.toolCalls, contentLoggingEnabled)
	// Server tools have no function call; they still name the row for the tool filter.
	for _, name := range state.toolNames {
		if !containsString(entry.ToolCallNames, name) {
			entry.ToolCallNames = append(entry.ToolCallNames, name)
		}
	}

	// The transcript is the conversation, in the shape every other row's history takes.
	if contentLoggingEnabled && len(session.Transcript) > 0 {
		entry.InputHistoryParsed = liveTranscriptMessages(session.Transcript)
		for i := len(entry.InputHistoryParsed) - 1; i >= 0; i-- {
			if entry.InputHistoryParsed[i].Role == schemas.ChatMessageRoleAssistant {
				entry.OutputMessageParsed = &entry.InputHistoryParsed[i]
				break
			}
		}
	} else {
		dropCapturedInput(entry)
	}

	entry.MetadataParsed = mergeRealtimeMetadata(pending.InitialData.Metadata, ctx)
	p.applyLiveContextFields(ctx, entry, time.Since(pending.Timestamp).Milliseconds())
	entry.RoutingEngineLogs = formatRoutingEngineLogs(ctx.GetRoutingEngineLogs())
	return entry
}

// applyLiveContextFields stamps the identity a unit's context carries onto a row.
func (p *LoggerPlugin) applyLiveContextFields(ctx *schemas.BifrostContext, entry *logstore.Log, latency int64) {
	get := func(key schemas.BifrostContextKey) string { return bifrost.GetStringFromContext(ctx, key) }
	applyOutputFieldsToEntry(entry,
		get(schemas.BifrostContextKeySelectedKeyID), get(schemas.BifrostContextKeySelectedKeyName),
		get(schemas.BifrostContextKeyGovernanceVirtualKeyID), get(schemas.BifrostContextKeyGovernanceVirtualKeyName),
		get(schemas.BifrostContextKeyGovernanceRoutingRuleID), get(schemas.BifrostContextKeyGovernanceRoutingRuleName),
		get(schemas.BifrostContextKeySelectedPromptID), get(schemas.BifrostContextKeySelectedPromptName), get(schemas.BifrostContextKeySelectedPromptVersion),
		get(schemas.BifrostContextKeyGovernanceTeamID), get(schemas.BifrostContextKeyGovernanceTeamName),
		get(schemas.BifrostContextKeyGovernanceCustomerID), get(schemas.BifrostContextKeyGovernanceCustomerName),
		get(schemas.BifrostContextKeyUserID), get(schemas.BifrostContextKeyUserName),
		get(schemas.BifrostContextKeyGovernanceBusinessUnitID), get(schemas.BifrostContextKeyGovernanceBusinessUnitName),
		get(schemas.BifrostContextKeyGovernanceProjectID), get(schemas.BifrostContextKeyGovernanceProjectName),
		0, latency, nil, nil, nil)
	if sessionID := get(schemas.BifrostContextKeySessionID); sessionID != "" {
		entry.SessionID = &sessionID
	}
	if nodeID, _ := p.clusterNodeID.Load().(string); nodeID != "" {
		entry.ClusterNodeID = &nodeID
	}
}

// liveTranscriptMessages turns a session's transcript into chat messages.
func liveTranscriptMessages(transcript []schemas.LiveTranscriptLine) []schemas.ChatMessage {
	messages := make([]schemas.ChatMessage, 0, len(transcript))
	for _, line := range transcript {
		role := schemas.ChatMessageRoleUser
		if strings.EqualFold(line.Role, string(schemas.ChatMessageRoleAssistant)) {
			role = schemas.ChatMessageRoleAssistant
		}
		messages = append(messages, schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: new(line.Text)}})
	}
	return messages
}

// addLLMUsage sums two usages, token details included. The total never aliases a usage's
// own details, which its delegation keeps.
func addLLMUsage(total, usage *schemas.BifrostLLMUsage) *schemas.BifrostLLMUsage {
	if total == nil {
		total = &schemas.BifrostLLMUsage{}
	}
	total.PromptTokens += usage.PromptTokens
	total.CompletionTokens += usage.CompletionTokens
	total.TotalTokens += usage.TotalTokens
	if in := usage.PromptTokensDetails; in != nil {
		if total.PromptTokensDetails == nil {
			total.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
		}
		sum := total.PromptTokensDetails
		sum.TextTokens += in.TextTokens
		sum.AudioTokens += in.AudioTokens
		sum.ImageTokens += in.ImageTokens
		sum.CachedReadTokens += in.CachedReadTokens
		sum.CachedWriteTokens += in.CachedWriteTokens
	}
	if out := usage.CompletionTokensDetails; out != nil {
		if total.CompletionTokensDetails == nil {
			total.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{}
		}
		sum := total.CompletionTokensDetails
		sum.TextTokens += out.TextTokens
		sum.AudioTokens += out.AudioTokens
		sum.ReasoningTokens += out.ReasoningTokens
		sum.AcceptedPredictionTokens += out.AcceptedPredictionTokens
		sum.RejectedPredictionTokens += out.RejectedPredictionTokens
	}
	return total
}

func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
