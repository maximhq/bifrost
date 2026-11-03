package logging

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/overhead"
)

var _ schemas.RelayPlugin = (*LoggerPlugin)(nil)

// pendingRelayLog is the request side of a relay call awaiting its post-hook, the relay analogue
// of PendingLogData. The row is embedded by value so the whole pending state is one allocation.
// lastActivity is refreshed by core/relay on every forwarded stream chunk (through
// BifrostContextKeyRelayActivity), so the stale sweep keeps a live stream's entry the way
// PostLLMHook's per-chunk LastActivity keeps an LLM stream's. provisionalInput and policy record
// what the body was captured under, so an eviction drops what the final policy may not allow.
type pendingRelayLog struct {
	entry            logstore.RelayLog
	lastActivity     atomic.Int64
	evictionAttempts int
	provisionalInput bool
	policy           contentPolicy
}

// evictedRelayPartial marks a request whose pending row was persisted as processing at eviction.
// It carries the row id (relay rows have a generated id, not the request id) and the path the
// partial stored, so a late post-hook can reconcile onto that row and re-apply the content policy
// to the path's query.
type evictedRelayPartial struct {
	rowID     string
	path      string
	relayName string
	routePath string
	streamed  bool
	at        time.Time
}

// relayInjectEntry is a completed relay row parked until its trace ends: written as an insert, or,
// when it reconciles a row persisted at eviction, as an update of the output columns.
type relayInjectEntry struct {
	entry           *logstore.RelayLog
	reconcile       bool
	dropRequestBody bool
}

// PreRelayHook records the request side of a relay call. The row is completed and written by the
// post-hook, which runs once the reply (or the stream) has ended.
func (p *LoggerPlugin) PreRelayHook(ctx *schemas.BifrostContext, req *schemas.BifrostRelayRequest) (*schemas.BifrostRelayRequest, *schemas.RelayPluginShortCircuit, error) {
	if ctx == nil || req == nil {
		return req, nil, nil
	}
	requestID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRequestID)
	if requestID == "" {
		return req, nil, nil
	}
	pending := &pendingRelayLog{}
	entry := &pending.entry
	// The row id is generated, as for Agent logs: the request id can come from the caller's
	// x-request-id, so two calls may share it. It is kept for correlation only.
	*entry = logstore.RelayLog{
		ID:        uuid.New().String(),
		RequestID: clampString(requestID, 255),
		Timestamp: time.Now().UTC(),
		RelayName: clampString(req.RelayName, 64),
		Kind:      logstore.RelayLogKindCall,
		Method:    clampString(req.Method, 10),
		Path:      req.Path,
		RoutePath: req.RoutePath,
		Billable:  req.Billable,
		Streamed:  req.Streams,
		Model:     clampString(req.Model, 255),
		Status:    logStatusProcessing,
	}
	if req.RawQuery != "" {
		entry.Path += "?" + redactQuery(req.RawQuery)
	}
	// Recorded now as well as in the post-hook, as PreLLMHook captures them, so a row persisted at
	// eviction (a call still running when the sweep runs) carries its correlation.
	applyRelayCorrelation(ctx, entry, requestID)
	// The raw User-Agent is stored verbatim and mapped to a client app, as on LLM and MCP rows,
	// and the app key is stamped for the plugins that run after this one, as PreLLMHook stamps it.
	if ua := userAgentFromContext(ctx); ua != "" {
		// Clamped to the column, as every other log kind does, so an oversized header never costs
		// the row (and its cost and attribution) at insert time.
		entry.UserAgent = new(clampString(ua, maxPersistedUserAgentLen))
		if app := p.detectAppFromUserAgent(ua); app != "" {
			entry.App = new(clampString(app, maxPersistedAppLen))
			if appKey := schemas.AppKeyFromName(app); appKey != "" {
				ctx.SetValue(schemas.BifrostContextKeyApp, appKey)
			}
		}
	}
	entry.ClientIP = optionalString(p.clientIPFromContext(ctx))
	// The same metadata LLM and MCP rows carry: the configured logging headers, every x-bf-lh-*
	// label, the x-bf-dim-* dimensions, and the virtual key's metadata on top.
	entry.MetadataParsed = mergeVirtualKeyMetadata(p.captureLoggingHeaders(ctx), ctx)
	entry.RequestHeadersParsed = relayMatchedHeaders(req)
	// The body is captured under the policy as the context stands now, or provisionally while
	// governance has yet to resolve the presented key or the named project (its pre-hook runs
	// after this one), exactly as PreLLMHook gates input capture. PostRelayHook applies the final
	// policy and drops what it does not allow, and an eviction drops provisional input.
	pending.provisionalInput = governanceDecisionPending(ctx)
	pending.policy = p.resolveContentPolicy(ctx)
	capture := pending.policy.storeContent || pending.provisionalInput
	if capture && len(req.Body) > 0 {
		entry.RequestBody = textBody(req.Body)
	}
	// A streamed reply has no body until core/relay keeps it as it forwards bytes; the same
	// decision tells it whether to, so a stream nobody logs the body of never buffers one.
	ctx.SetValue(schemas.BifrostContextKeyRelayContentCapture, capture)
	pending.lastActivity.Store(entry.Timestamp.UnixNano())
	ctx.SetValue(schemas.BifrostContextKeyRelayActivity, &pending.lastActivity)
	p.pendingEndpointLogs.Store(requestID, pending)
	return req, nil, nil
}

// PostRelayHook completes and writes the Relay Logs row, with the governance attribution and the
// cost breakdown, and records the job row a job route started so the sweeper settles it against
// the same budgets. A call whose pending entry was evicted into a partial processing row (see
// persistEvictedRelayPartial) reconciles its outcome onto that row with an update, as PostLLMHook
// reconciles onto an evicted LLM partial.
func (p *LoggerPlugin) PostRelayHook(ctx *schemas.BifrostContext, resp *schemas.BifrostRelayResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostRelayResponse, *schemas.BifrostError, error) {
	if ctx == nil {
		return resp, bifrostErr, nil
	}
	requestID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRequestID)
	// The claim is serialized with the stale sweep, as for LLM rows: either this hook takes the
	// pending entry, or the sweep has persisted it and left its marker, never neither.
	lock := p.pendingClaimLock(requestID)
	lock.Lock()
	pendingVal, hasPending := p.pendingEndpointLogs.LoadAndDelete(requestID)
	var marker evictedRelayPartial
	var evicted bool
	if !hasPending {
		if val, ok := p.evictedRelayPartials.LoadAndDelete(requestID); ok {
			marker, evicted = val.(evictedRelayPartial)
		}
	}
	lock.Unlock()
	pending, _ := pendingVal.(*pendingRelayLog)
	if !hasPending && !evicted && requestID != "" {
		// A row persisted by a previous process has no in-memory marker: a bounded probe on the
		// plugin's own context, since the request's may already be cancelled.
		marker, evicted = p.probeEvictedRelayPartial(requestID)
	}
	policy := p.resolveContentPolicy(ctx)
	var entry *logstore.RelayLog
	switch {
	case pending != nil:
		entry = &pending.entry
	case evicted:
		// The update names the output columns only, so the partial row keeps its request side; the
		// marker carries what the update also writes, so a reply-less finish does not blank it.
		entry = &logstore.RelayLog{ID: marker.rowID, RequestID: clampString(requestID, 255), Timestamp: time.Now().UTC(), Kind: logstore.RelayLogKindCall, Path: marker.path, RelayName: marker.relayName, RoutePath: marker.routePath, Streamed: marker.streamed}
	default:
		entry = &logstore.RelayLog{ID: uuid.New().String(), RequestID: clampString(requestID, 255), Timestamp: time.Now().UTC(), Kind: logstore.RelayLogKindCall}
	}
	entry.ApplyGovernanceContext(ctx)
	applyRelayGrantAttribution(ctx, entry)
	// Merged again here, as PostLLMHook does: the key's metadata is stamped by governance, which may
	// run after this plugin's pre-hook, and the key's values win over caller-sent labels.
	entry.MetadataParsed = mergeVirtualKeyMetadata(entry.MetadataParsed, ctx)
	// The pre-hook already recorded the correlation on a pending row; only a row built here (an
	// evicted call's update, or a call the pre-hook never saw) reads it again.
	if pending == nil {
		applyRelayCorrelation(ctx, entry, requestID)
	}
	if resp != nil {
		if entry.RelayName == "" {
			entry.RelayName = clampString(resp.RelayName, 64)
		}
		entry.StatusCode = resp.StatusCode
		entry.Streamed = resp.Streamed
		// The body is a preview: the reply passed the large-payload response threshold.
		entry.IsLargePayloadResponse = resp.LargeResponse
		entry.BytesOut = resp.BytesOut
		latency := float64(resp.Latency)
		entry.Latency = &latency
		// Provisional split, as PostLLMHook records one; Inject replaces it with the root span's
		// figures once the trace completes.
		if resp.UpstreamLatency > 0 && resp.UpstreamLatency <= resp.Latency {
			upstream := float64(resp.UpstreamLatency)
			overhead := latency - upstream
			entry.UpstreamLatency = &upstream
			entry.OverheadLatency = &overhead
		}
		// Copied only when the row keeps content: a call with content logging off must not pay for
		// a copy of its reply that is dropped a few lines below.
		if len(resp.Body) > 0 && policy.storeContent {
			entry.ResponseBody = textBody(resp.Body)
		}
		applyEndpointUsage(entry, resp.Usage)
	}
	if entry.Latency == nil {
		// No reply to time: the upstream failed or a pre-hook refused the call. The error carries
		// the time spent (SetErrorLatency on transport errors, the relay manager on short-circuits);
		// failing that, the row's own start is the measure, so a refusal still records how long
		// Bifrost held the call, as LLM rows do.
		switch {
		case bifrostErr != nil && bifrostErr.ExtraFields.Latency > 0:
			latency := float64(bifrostErr.ExtraFields.Latency)
			entry.Latency = &latency
		case pending != nil:
			latency := float64(time.Since(entry.Timestamp).Milliseconds())
			entry.Latency = &latency
		}
	}
	// The same status rule as LLM rows: a cancelled, hung-up or client-deadline call is cancelled,
	// any other failure or an upstream 4xx/5xx is an error.
	switch {
	case bifrostErr != nil:
		entry.Status = logStatusForError(bifrostErr)
	case resp != nil && resp.StatusCode >= 400:
		entry.Status = logStatusError
	default:
		entry.Status = logStatusSuccess
	}
	if bifrostErr != nil {
		shouldStoreRaw, _ := ctx.Value(schemas.BifrostContextKeyShouldStoreRawInLogs).(bool)
		entry.ErrorDetailsParsed = sanitizeErrorForLogging(bifrostErr, policy.visible(), shouldStoreRaw)
		// Content retained as hidden keeps the error in full, raw request and response included,
		// for the object alone, as an LLM row's offloaded error_details does; the database row
		// keeps it without them.
		if policy.storeContent && policy.hidden {
			entry.RetainedErrorDetails = sanitizeErrorForLogging(bifrostErr, true, shouldStoreRaw)
		}
		if entry.StatusCode == 0 && bifrostErr.StatusCode != nil {
			entry.StatusCode = *bifrostErr.StatusCode
		}
	}
	// As on LLM rows: the flag says the row carries no content, whatever the reason. Content
	// retained as hidden keeps its bodies for the hybrid store to offload, as on A2A rows; the
	// database row then holds none and reads never hydrate them. Query values are caller content too.
	entry.ContentHidden = !policy.visible()
	// Metadata and the matched headers stay when content logging is off, as on LLM and MCP rows:
	// the operator chose them; only the bodies and the query string are content.
	if !policy.storeContent {
		entry.RequestBody, entry.ResponseBody = nil, nil
	}
	if !policy.visible() {
		// The query string is content: hidden, it is kept in the object only, as the bodies are.
		if policy.storeContent && strings.Contains(entry.Path, "?") {
			entry.RetainedPath = schemas.Ptr(entry.Path)
		}
		entry.Path = stripQuery(entry.Path)
	}
	if resp != nil && resp.Usage != nil && resp.Usage.Job != nil {
		p.recordEndpointJob(entry, resp.Usage.Job)
	}
	p.storeOrEnqueueRelayEntry(ctx, relayInjectEntry{entry: entry, reconcile: evicted, dropRequestBody: !policy.storeContent})
	return resp, bifrostErr, nil
}

// applyRelayCorrelation records the caller's correlation on a relay row, as an LLM row records it:
// the session identity resolved at ingress, a parent named through baggage (never the call itself)
// and the agent correlation header.
func applyRelayCorrelation(ctx *schemas.BifrostContext, entry *logstore.RelayLog, requestID string) {
	if sessionID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySessionID); sessionID != "" {
		entry.SessionID = schemas.Ptr(clampString(sessionID, 255))
	}
	if parentRequestID, ok := ctx.Value(schemas.BifrostContextKeyParentRequestID).(string); ok && parentRequestID != "" && parentRequestID != requestID {
		entry.ParentRequestID = schemas.Ptr(clampString(parentRequestID, 255))
	}
	if agentCorrelationID := requestHeaderFromContext(ctx, "x-bf-agent-correlation-id"); agentCorrelationID != "" {
		entry.AgentCorrelationID = schemas.Ptr(clampString(agentCorrelationID, maxPersistedAgentCorrelationIDLen))
	}
}

// probeEvictedRelayPartial looks for a processing row persisted for the request by a previous
// process, bounded by evictedPartialProbeTimeout as PostLLMHook's probe is.
func (p *LoggerPlugin) probeEvictedRelayPartial(requestID string) (evictedRelayPartial, bool) {
	if p.store == nil {
		return evictedRelayPartial{}, false
	}
	probeCtx, cancel := context.WithTimeout(p.ctx, evictedPartialProbeTimeout)
	defer cancel()
	result, err := p.store.SearchRelayLogs(probeCtx, logstore.RelayLogFilters{
		RequestID: requestID, Statuses: []string{logstore.RelayLogStatusProcessing}, Kinds: []string{logstore.RelayLogKindCall},
	}, logstore.PaginationOptions{Limit: 1, SkipCount: true})
	if err != nil || result == nil || len(result.Logs) == 0 {
		return evictedRelayPartial{}, false
	}
	row := result.Logs[0]
	return evictedRelayPartial{rowID: row.ID, path: row.Path, relayName: row.RelayName, routePath: row.RoutePath, streamed: row.Streamed, at: time.Now()}, true
}

// stripQuery drops the query string from a stored path.
func stripQuery(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[:i]
	}
	return path
}

// relayLogUpdateColumns is the reconciling update for a row persisted as processing at eviction:
// every column PostRelayHook sets after the pending entry is taken, keyed by its database name,
// so the row ends up as a row written in one shot would. Metadata is left as the partial stored
// it, as the LLM reconcile leaves it. The bodies are named explicitly so a body the partial
// stored and the final policy forbids is cleared.
func relayLogUpdateColumns(entry *logstore.RelayLog, dropRequestBody bool) map[string]any {
	if err := entry.SerializeFields(); err != nil {
		return nil
	}
	updates := map[string]any{
		"status":                    entry.Status,
		"status_code":               entry.StatusCode,
		"streamed":                  entry.Streamed,
		"is_large_payload_response": entry.IsLargePayloadResponse,
		"bytes_out":                 entry.BytesOut,
		"latency":                   entry.Latency,
		"upstream_latency":          entry.UpstreamLatency,
		"overhead_latency":          entry.OverheadLatency,
		"overhead_breakdown":        entry.OverheadBreakdown,
		"cost":                      entry.Cost,
		"cost_per_request":          entry.CostPerRequest,
		"cost_per_unit":             entry.CostPerUnit,
		"extracted_cost":            entry.ExtractedCost,
		"units":                     entry.Units,
		"billed_units":              entry.BilledUnits,
		"input_tokens":              entry.InputTokens,
		"output_tokens":             entry.OutputTokens,
		"total_tokens":              entry.TotalTokens,
		"job_id":                    entry.JobID,
		"job_status":                entry.JobStatus,
		"user_id":                   entry.UserID,
		"user_name":                 entry.UserName,
		"user_email":                entry.UserEmail,
		"virtual_key_id":            entry.VirtualKeyID,
		"virtual_key_name":          entry.VirtualKeyName,
		"team_id":                   entry.TeamID,
		"team_name":                 entry.TeamName,
		"customer_id":               entry.CustomerID,
		"customer_name":             entry.CustomerName,
		"business_unit_id":          entry.BusinessUnitID,
		"business_unit_name":        entry.BusinessUnitName,
		"project_id":                entry.ProjectID,
		"project_name":              entry.ProjectName,
		"session_id":                entry.SessionID,
		"parent_request_id":         entry.ParentRequestID,
		"agent_correlation_id":      entry.AgentCorrelationID,
		"team_ids":                  entry.TeamIDs,
		"team_names":                entry.TeamNames,
		"customer_ids":              entry.CustomerIDs,
		"customer_names":            entry.CustomerNames,
		"business_unit_ids":         entry.BusinessUnitIDs,
		"business_unit_names":       entry.BusinessUnitNames,
		"budget_ids":                entry.BudgetIDs,
		"rate_limit_ids":            entry.RateLimitIDs,
		"response_body":             entry.ResponseBody,
		"error_details":             entry.ErrorDetails,
		"plugin_logs":               entry.PluginLogs,
		"content_hidden":            entry.ContentHidden,
	}
	if dropRequestBody {
		updates["request_body"] = nil
	}
	if entry.ContentHidden && entry.Path != "" {
		// The partial stored the full path; the final policy hides the query with the bodies.
		updates["path"] = stripQuery(entry.Path)
	}
	// Hidden content with no body column rides along for the hybrid store's object; every store
	// strips these keys before writing the row.
	if entry.RetainedPath != nil {
		updates[logstore.RelayRetainedPathUpdate] = entry.RetainedPath
	}
	if entry.RetainedErrorDetails != nil {
		updates[logstore.RelayRetainedErrorDetailsUpdate] = entry.RetainedErrorDetails
	}
	return updates
}

// writeRelayEntry hands a completed row to the writer: an insert, or the reconciling update of a
// row persisted at eviction.
func (p *LoggerPlugin) writeRelayEntry(item relayInjectEntry) {
	if !item.reconcile {
		p.enqueueEndpointLogEntry(item.entry)
		return
	}
	updates := relayLogUpdateColumns(item.entry, item.dropRequestBody)
	if updates == nil {
		p.logger.Warn("failed to serialize relay log %s for reconcile", item.entry.ID)
		p.countDrop()
		return
	}
	p.enqueueEndpointLogUpdate(&relayLogUpdate{id: item.entry.ID, updates: updates})
}

// persistEvictedRelayPartial is the relay half of persistEvictedPartial: a pending relay entry idle
// past pendingLogTTL is written as a processing row, so a call that finishes late reconciles onto
// it instead of losing its request side, and a marker tells PostRelayHook where to. The claim lock
// keeps the sweep and a terminal post-hook from both taking the entry.
func (p *LoggerPlugin) persistEvictedRelayPartial(key any, pending *pendingRelayLog) {
	id, ok := key.(string)
	if !ok {
		p.pendingEndpointLogs.Delete(key)
		return
	}
	lock := p.pendingClaimLock(id)
	lock.Lock()
	defer lock.Unlock()
	if _, claimed := p.pendingEndpointLogs.LoadAndDelete(key); !claimed {
		return
	}
	// A copy, as persistEvictedPartial builds its own row: the pending entry stays as the hook
	// captured it, so a sweep that could not queue the partial hands PostRelayHook an intact one.
	copied := pending.entry
	partial := &copied
	partial.Status = logStatusProcessing
	// CreatedAt restarts the processing-row flush window; Timestamp keeps the call's start.
	partial.CreatedAt = time.Now().UTC()
	policy := pending.policy
	if pending.provisionalInput || !policy.storeContent {
		partial.RequestBody = nil
	}
	if pending.provisionalInput || !policy.visible() {
		// Content retained as hidden keeps the query in the row's object, as PostRelayHook does.
		if !pending.provisionalInput && policy.storeContent && strings.Contains(partial.Path, "?") {
			partial.RetainedPath = schemas.Ptr(partial.Path)
		}
		partial.Path = stripQuery(partial.Path)
	}
	partial.ContentHidden = !policy.visible()
	if !p.tryEnqueueEndpointLogEntry(partial) {
		// Nothing persisted: keep the entry so its request side survives. The next sweep retries,
		// or a completion takes the entry and writes the full row.
		pending.evictionAttempts++
		if pending.evictionAttempts < maxEvictionAttempts {
			p.pendingEndpointLogs.Store(key, pending)
			return
		}
		p.countDrop()
		p.logger.Warn("log write queue full, dropping evicted relay request %s after %d attempts to persist its partial row", id, pending.evictionAttempts)
		return
	}
	// Queued before the marker is visible, so a finish that sees the marker enqueues its update
	// behind the insert; processBatch also applies inserts before updates within a batch.
	p.evictedRelayPartials.Store(id, evictedRelayPartial{rowID: partial.ID, path: partial.Path, relayName: partial.RelayName, routePath: partial.RoutePath, streamed: partial.Streamed, at: time.Now()})
}

// storeOrEnqueueRelayEntry parks the row for Inject() to backfill upstream, overhead and the
// breakdown from the completed trace, as storeOrEnqueueAgentEntry does for A2A rows. A row with
// no trace, or whose trace has already ended, is written as it is.
func (p *LoggerPlugin) storeOrEnqueueRelayEntry(ctx *schemas.BifrostContext, item relayInjectEntry) {
	traceID, _ := ctx.Value(schemas.BifrostContextKeyTraceID).(string)
	if traceID == "" || p.traceAlreadyEnded(ctx, traceID) {
		p.writeRelayEntryUntraced(ctx, item)
		return
	}
	existing, loaded := p.pendingRelayLogsToInject.LoadOrStore(traceID, &pendingRelayInjectEntries{entries: []relayInjectEntry{item}, createdAt: time.Now()})
	if !loaded {
		// The trace may have ended between the check above and this store; reclaim the slot.
		if p.traceAlreadyEnded(ctx, traceID) {
			if val, ok := p.pendingRelayLogsToInject.LoadAndDelete(traceID); ok {
				orphaned := val.(*pendingRelayInjectEntries)
				orphaned.mu.Lock()
				orphaned.drained = true
				late := orphaned.entries
				orphaned.mu.Unlock()
				for _, e := range late {
					p.writeRelayEntryUntraced(ctx, e)
				}
			}
		}
		return
	}
	pending := existing.(*pendingRelayInjectEntries)
	pending.mu.Lock()
	if pending.drained {
		pending.mu.Unlock()
		p.writeRelayEntryUntraced(ctx, item)
		return
	}
	pending.entries = append(pending.entries, item)
	pending.mu.Unlock()
}

// writeRelayEntryUntraced writes a row no trace will backfill, with the plugin logs on its context.
// A parked row takes the trace's instead (injectRelayEntries), which also hold the lines later
// post-hooks and the transport plugins add, so the logs are serialized once either way. Uncapped,
// as on LLM rows.
func (p *LoggerPlugin) writeRelayEntryUntraced(ctx *schemas.BifrostContext, item relayInjectEntry) {
	item.entry.PluginLogs = serializePluginLogs(ctx.GetPluginLogs())
	p.writeRelayEntry(item)
}

// injectRelayEntries drains the Relay rows parked for a completed trace and backfills upstream,
// overhead and the per-span breakdown from the root span, mirroring injectAgentEntries. A trace
// carries one relay call, so every parked row takes the request-level figures.
func (p *LoggerPlugin) injectRelayEntries(trace *schemas.Trace, joinKey string) {
	val, ok := p.pendingRelayLogsToInject.LoadAndDelete(joinKey)
	if !ok {
		return
	}
	pending, ok := val.(*pendingRelayInjectEntries)
	if !ok {
		return
	}
	pending.mu.Lock()
	pending.drained = true
	entries := pending.entries
	pending.mu.Unlock()
	if len(entries) == 0 {
		return
	}
	var upstreamMs, overheadMs float64
	var upOK, ovOK bool
	if trace.RootSpan != nil && trace.RootSpan.Attributes != nil {
		upstreamMs, upOK = overhead.TraceAttrFloatMs(trace.RootSpan.Attributes, schemas.AttrBifrostUpstreamDurationMs)
		overheadMs, ovOK = overhead.TraceAttrFloatMs(trace.RootSpan.Attributes, schemas.AttrBifrostOverheadDurationMs)
	}
	overheadBreakdown, measuredOverheadMs, isStreaming := overhead.Compute(trace, overheadMs, ovOK, upstreamMs, upOK)
	// The trace's plugin logs, as an LLM row takes them: they include what the post-hooks after
	// this plugin's and the transport plugins logged, which the post-hook copy cannot see.
	pluginLogsJSON := serializePluginLogs(trace.PluginLogs)
	for _, item := range entries {
		entry := item.entry
		if pluginLogsJSON != "" {
			entry.PluginLogs = pluginLogsJSON
		}
		if upOK || ovOK {
			entry.UpstreamLatency = nil
			entry.OverheadLatency = nil
		}
		if isStreaming && upOK && ovOK {
			// Streaming: overhead is the measured Bifrost CPU; the off-CPU wait between chunks
			// folds into upstream so latency = upstream + overhead still holds.
			total := upstreamMs + overheadMs
			measured := min(max(measuredOverheadMs, 0), overheadMs)
			up := total - measured
			entry.UpstreamLatency = &up
			entry.OverheadLatency = &measured
			entry.Latency = &total
		} else {
			if upOK {
				u := upstreamMs
				entry.UpstreamLatency = &u
			}
			if ovOK {
				o := overheadMs
				entry.OverheadLatency = &o
			}
			if upOK && ovOK {
				total := upstreamMs + overheadMs
				entry.Latency = &total
			}
		}
		if len(overheadBreakdown) > 0 {
			entry.OverheadBreakdownParsed = overheadBreakdown
		}
		p.writeRelayEntry(item)
	}
}

// applyEndpointUsage copies a counted call's cost breakdown onto its row.
func applyEndpointUsage(entry *logstore.RelayLog, usage *schemas.RelayUsage) {
	if usage == nil {
		return
	}
	entry.Cost = schemas.Ptr(usage.Cost)
	entry.CostPerRequest = schemas.Ptr(usage.CostPerRequest)
	entry.CostPerUnit = schemas.Ptr(usage.CostPerUnit)
	entry.ExtractedCost = usage.ExtractedCost
	entry.Units = usage.Units
	entry.BilledUnits = usage.BilledUnits
	entry.InputTokens, entry.OutputTokens, entry.TotalTokens = usage.InputTokens, usage.OutputTokens, usage.TotalTokens
	if usage.Job != nil {
		entry.JobID = schemas.Ptr(usage.Job.ID)
		entry.JobStatus = schemas.Ptr("started")
	}
}

// maxEndpointJobGenerations bounds the dedupe lookup for one upstream job id reused after settlement;
// past it each start gets a row keyed by its own request, which is settled but no longer deduped.
const maxEndpointJobGenerations = 64

// recordEndpointJob writes the job row the sweeper settles. The row is keyed by endpoint and upstream
// job id, so a caller retrying an idempotent start (the upstream answers with the same id while the
// job runs) records nothing new and is billed once. An id the upstream reuses after the first job was
// settled is a new job: it takes the next generation slot, so retries of that job dedupe against the
// same slot instead of each start opening a row of its own.
func (p *LoggerPlugin) recordEndpointJob(entry *logstore.RelayLog, job *schemas.RelayJobHandle) {
	if p.batchStore == nil || job == nil || job.ID == "" {
		return
	}
	kind := tables.ProviderJobKindRelay
	var scoped, id string
	for gen := 1; ; gen++ {
		if gen > maxEndpointJobGenerations {
			scoped = schemas.RelayScopedJobID(entry.RequestID, job.ID)
			id = tables.ProviderJobID(kind, entry.RelayName, scoped)
			break
		}
		prefix := ""
		if gen > 1 {
			prefix = "g" + strconv.Itoa(gen)
		}
		scoped = schemas.RelayScopedJobID(prefix, job.ID)
		id = tables.ProviderJobID(kind, entry.RelayName, scoped)
		existing, err := p.batchStore.GetProviderJob(p.ctx, id)
		if errors.Is(err, configstore.ErrNotFound) || (err == nil && existing == nil) {
			break // a free slot
		}
		if err != nil {
			// Recording on a lookup error could open a second row for an idempotent retry.
			p.logger.Warn("failed to look up relay job endpoint=%s job_id=%s: %v", entry.RelayName, job.ID, err)
			return
		}
		if existing.AccountingStatus != tables.ProviderJobAccountingStatusAccounted && existing.AccountingStatus != tables.ProviderJobAccountingStatusUnpriceable {
			return // an idempotent retry of a start whose job is still open
		}
	}
	next := time.Now().UTC().Add(time.Duration(max(job.IntervalSeconds, 1)) * time.Second)
	params := job.Params
	row := &tables.TableProviderJob{
		ID:             id,
		Kind:           kind,
		Provider:       entry.RelayName,
		JobID:          scoped,
		Endpoint:       entry.RoutePath,
		Params:         &params,
		ProviderStatus: "started",
		NextCheckAt:    &next,
		VirtualKeyID:   entry.VirtualKeyID,
		UserID:         entry.UserID,
		TeamID:         entry.TeamID,
		CustomerID:     entry.CustomerID,
		SourceLogID:    schemas.Ptr(entry.ID),
	}
	if len(entry.BudgetIDsParsed) > 0 {
		if data, err := sonic.MarshalString(entry.BudgetIDsParsed); err == nil {
			row.BudgetIDs = &data
		}
	}
	if len(entry.RateLimitIDsParsed) > 0 {
		if data, err := sonic.MarshalString(entry.RateLimitIDsParsed); err == nil {
			row.RateLimitIDs = &data
		}
	}
	if err := p.batchStore.UpsertProviderJob(p.ctx, row); err != nil {
		p.logger.Warn("failed to record relay job endpoint=%s job_id=%s: %v", entry.RelayName, job.ID, err)
	}
}

// sensitiveQueryKeys are query parameter names that carry credentials but no header pattern names.
var sensitiveQueryKeys = map[string]struct{}{"key": {}, "token": {}, "apikey": {}, "password": {}, "pwd": {}, "sig": {}, "signature": {}}

// redactQuery masks the values of credential-bearing query parameters (api_key, access_token, ...)
// before a query is stored with the path, keeping the parameter order and names. A key is
// sensitive when the header rules name it (with "_" read as "-") or it is a well-known credential
// parameter.
func redactQuery(raw string) string {
	parts := strings.Split(raw, "&")
	for i, part := range parts {
		name, _, hasValue := strings.Cut(part, "=")
		if !hasValue {
			continue
		}
		key, err := url.QueryUnescape(name)
		if err != nil {
			key = name
		}
		normalized := strings.ToLower(strings.ReplaceAll(key, "_", "-"))
		if _, ok := sensitiveQueryKeys[strings.ReplaceAll(normalized, "-", "")]; ok || schemas.IsSensitiveHeader(normalized) {
			parts[i] = name + "=" + url.QueryEscape(schemas.RedactedAttrValue)
		}
	}
	return strings.Join(parts, "&")
}

// textBody keeps a body as text, the way LLM rows keep passthrough bodies (omitBinaryBody): a
// binary body becomes a "<binary content omitted: N bytes>" placeholder, and a capture that ends
// inside a multi-byte character (a caller that disconnected mid-stream) drops that character.
func textBody(body []byte) *string {
	s := omitBinaryBody(string(body))
	return &s
}

// relayMatchedHeaders keeps the request headers the matched route matches on, since they are what
// told the relay which route (and price) applied; they are relay-specific, so they live beside
// the row's metadata rather than in it. Nothing else is kept here, and the reply's headers never.
func relayMatchedHeaders(req *schemas.BifrostRelayRequest) map[string]string {
	if len(req.RouteHeaders) == 0 {
		return nil
	}
	// The route layer hands over the values that selected the route (an operator-set header's
	// value, else the caller's), operator secrets already redacted. A route may also match on a
	// credential-like header the caller sends; its value is redacted as the gateway redacts such
	// headers everywhere else, so a matcher never turns the row into a credential store.
	kept := make(map[string]string, len(req.RouteHeaders))
	for name, value := range req.RouteHeaders {
		kept[strings.ToLower(name)] = value
	}
	// Values are stored whole, as LLM rows store their logging headers.
	return schemas.RedactSensitiveHeaders(kept)
}
