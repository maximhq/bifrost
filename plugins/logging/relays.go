package logging

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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

// maxEndpointLogBodyLen bounds each body a Relay Logs row keeps. Endpoint payloads can be
// documents or audio; the row keeps enough to debug a mapping, not the payload itself.
const maxEndpointLogBodyLen = 64 * 1024

// PreRelayHook records the request side of a relay call. The row is
// completed and written by the post-hook, which runs once the reply (or the stream) has ended.
func (p *LoggerPlugin) PreRelayHook(ctx *schemas.BifrostContext, req *schemas.BifrostRelayRequest) (*schemas.BifrostRelayRequest, *schemas.RelayPluginShortCircuit, error) {
	if ctx == nil || req == nil {
		return req, nil, nil
	}
	requestID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRequestID)
	if requestID == "" {
		return req, nil, nil
	}
	// The row id is generated, as for Agent logs: the request id can come from the caller's
	// x-request-id, so two calls may share it. It is kept for correlation only.
	entry := &logstore.RelayLog{
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
		Status:    "processing",
	}
	if req.RawQuery != "" {
		entry.Path += "?" + redactQuery(req.RawQuery)
	}
	// The raw User-Agent is stored verbatim and mapped to a client app, as on LLM and MCP rows.
	if ua := userAgentFromContext(ctx); ua != "" {
		// Clamped to the column, as every other log kind does, so an oversized header never costs
		// the row (and its cost and attribution) at insert time.
		entry.UserAgent = new(clampString(ua, maxPersistedUserAgentLen))
		if app := p.detectAppFromUserAgent(ua); app != "" {
			entry.App = &app
		}
	}
	// The same metadata LLM and MCP rows carry: the configured logging headers, every x-bf-lh-*
	// label, the x-bf-dim-* dimensions, and the virtual key's metadata on top.
	entry.MetadataParsed = mergeVirtualKeyMetadata(p.captureLoggingHeaders(ctx), ctx)
	entry.RequestHeadersParsed = relayMatchedHeaders(req)
	if len(req.Body) > 0 {
		entry.RequestBody = boundedBody(req.Body)
	}
	// A streamed reply has no body until core/relay buffers it as it forwards bytes; that buffering
	// only pays its cost when the content policy would keep the body anyway. This hook runs before
	// governance's (builtin placement order 3 vs 4), so governance's per-key override
	// (BifrostContextKeyGovernanceDisableContentLogging) is not on ctx yet here — only this plugin's
	// own static setting is stamped. core/relay resolves the final decision from this plus
	// governance's and the per-request override once every pre-hook, governance included, has run
	// (see Stream's capture setup), so a virtual key that disagrees with the plugin's default is
	// never missed the way an early, fully-resolved stamp would miss it.
	// Deliberately not resolveContentPolicy(ctx): calling it here would read governance's and the
	// per-request override's absence as "not disabled" rather than "not decided yet", which is the
	// exact premature-resolution bug this comment is explaining. Only the plugin's own static
	// layer is knowable this early.
	enabledByDefault := p.disableContentLogging == nil || !*p.disableContentLogging
	ctx.SetValue(schemas.BifrostContextKeyRelayContentLoggingEnabledByDefault, enabledByDefault)
	p.pendingEndpointLogs.Store(requestID, entry)
	return req, nil, nil
}

// PostRelayHook completes and writes the Relay Logs row, with the governance
// attribution and the cost breakdown, and records the job row a job route started so the sweeper
// settles it against the same budgets.
func (p *LoggerPlugin) PostRelayHook(ctx *schemas.BifrostContext, resp *schemas.BifrostRelayResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostRelayResponse, *schemas.BifrostError, error) {
	if ctx == nil {
		return resp, bifrostErr, nil
	}
	requestID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRequestID)
	pending, _ := p.pendingEndpointLogs.LoadAndDelete(requestID)
	entry, _ := pending.(*logstore.RelayLog)
	if entry == nil {
		entry = &logstore.RelayLog{ID: uuid.New().String(), RequestID: clampString(requestID, 255), Timestamp: time.Now().UTC(), Kind: logstore.RelayLogKindCall}
	}
	entry.ApplyGovernanceContext(ctx)
	// Merged again here, as PostLLMHook does: the key's metadata is stamped by governance, which may
	// run after this plugin's pre-hook, and the key's values win over caller-sent labels.
	entry.MetadataParsed = mergeVirtualKeyMetadata(entry.MetadataParsed, ctx)
	if resp != nil {
		if entry.RelayName == "" {
			entry.RelayName = clampString(resp.RelayName, 64)
		}
		entry.StatusCode = resp.StatusCode
		entry.Streamed = resp.Streamed
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
		if len(resp.Body) > 0 {
			entry.ResponseBody = boundedBody(resp.Body)
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
	policy := p.resolveContentPolicy(ctx)
	switch {
	case bifrostErr != nil && bifrostErr.Error != nil && bifrostErr.Error.Type != nil && *bifrostErr.Error.Type == schemas.RequestCancelled:
		entry.Status = logstore.RelayLogStatusCancelled
	case bifrostErr != nil || (resp != nil && resp.StatusCode >= 400):
		entry.Status = logstore.RelayLogStatusError
	default:
		entry.Status = logstore.RelayLogStatusSuccess
	}
	if bifrostErr != nil {
		shouldStoreRaw, _ := ctx.Value(schemas.BifrostContextKeyShouldStoreRawInLogs).(bool)
		entry.ErrorDetailsParsed = sanitizeErrorForLogging(bifrostErr, policy.visible(), shouldStoreRaw)
		if entry.StatusCode == 0 && bifrostErr.StatusCode != nil {
			entry.StatusCode = *bifrostErr.StatusCode
		}
	}
	// As on LLM rows: the flag says the row carries no content, whatever the reason. Relay rows
	// have no object-storage offload yet, so content that would be retained as hidden is dropped
	// instead of kept in columns the logs API would return. Query values are caller content too.
	entry.ContentHidden = !policy.visible()
	if policy.hidden {
		p.relayRetainWarnOnce.Do(func() {
			p.logger.Warn("retain_content_in_object_storage does not apply to Relay Logs yet; content-disabled relay calls are dropped entirely")
		})
	}
	// Metadata and the matched headers stay when content logging is off, as on LLM and MCP rows:
	// the operator chose them; only the bodies and the query string are content.
	if !policy.storeContent || policy.hidden {
		entry.RequestBody, entry.ResponseBody = nil, nil
		if i := strings.IndexByte(entry.Path, '?'); i >= 0 {
			entry.Path = entry.Path[:i]
		}
	}
	entry.PluginLogs = serializePluginLogsBounded(ctx.GetPluginLogs(), maxA2APluginLogsLen)
	if resp != nil && resp.Usage != nil && resp.Usage.Job != nil {
		p.recordEndpointJob(entry, resp.Usage.Job)
	}
	p.storeOrEnqueueRelayEntry(ctx, entry)
	return resp, bifrostErr, nil
}

// storeOrEnqueueRelayEntry parks the row for Inject() to backfill upstream, overhead and the
// breakdown from the completed trace, as storeOrEnqueueAgentEntry does for A2A rows. A row with
// no trace, or whose trace has already ended, is written as it is.
func (p *LoggerPlugin) storeOrEnqueueRelayEntry(ctx *schemas.BifrostContext, entry *logstore.RelayLog) {
	traceID, _ := ctx.Value(schemas.BifrostContextKeyTraceID).(string)
	if traceID == "" || p.traceAlreadyEnded(ctx, traceID) {
		p.enqueueEndpointLogEntry(entry)
		return
	}
	existing, loaded := p.pendingRelayLogsToInject.LoadOrStore(traceID, &pendingRelayInjectEntries{entries: []*logstore.RelayLog{entry}, createdAt: time.Now()})
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
					p.enqueueEndpointLogEntry(e)
				}
			}
		}
		return
	}
	pending := existing.(*pendingRelayInjectEntries)
	pending.mu.Lock()
	if pending.drained {
		pending.mu.Unlock()
		p.enqueueEndpointLogEntry(entry)
		return
	}
	pending.entries = append(pending.entries, entry)
	pending.mu.Unlock()
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
	for _, entry := range entries {
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
		p.enqueueEndpointLogEntry(entry)
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

// boundedBody keeps up to maxEndpointLogBodyLen bytes of a body as text; a binary body is not kept.
func boundedBody(body []byte) *string {
	if len(body) > maxEndpointLogBodyLen {
		body = body[:maxEndpointLogBodyLen]
	}
	if !utf8.Valid(body) {
		// A cut can split a rune at the end; anything else invalid is a binary payload.
		trimmed := body
		for i := 0; i < utf8.UTFMax && len(trimmed) > 0 && !utf8.Valid(trimmed); i++ {
			trimmed = trimmed[:len(trimmed)-1]
		}
		if !utf8.Valid(trimmed) {
			return nil
		}
		body = trimmed
	}
	s := string(body)
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
	return boundedHeaders(schemas.RedactSensitiveHeaders(kept))
}

// relayHeaderValueLimit bounds one stored header value; the header count is bounded by the
// transport's read buffer, so the row stays a few KB at most.
const relayHeaderValueLimit = 2048

// boundedHeaders clamps each header value in place, as bodies are bounded, so a row never grows
// with an oversized header.
func boundedHeaders(headers map[string]string) map[string]string {
	for k, v := range headers {
		if len(v) > relayHeaderValueLimit {
			headers[k] = clampString(v, relayHeaderValueLimit)
		}
	}
	return headers
}
