// Package logging provides a GORM-based logging plugin for Bifrost.
// This plugin stores comprehensive logs of all requests and responses with search,
// filter, and pagination capabilities.
package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/batchaccounting"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/mcpcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/streaming"
)

const (
	PluginName = "logging"
)

// LogOperation represents the type of logging operation
type LogOperation string

const (
	LogOperationCreate       LogOperation = "create"
	LogOperationUpdate       LogOperation = "update"
	LogOperationStreamUpdate LogOperation = "stream_update"
)

// UpdateLogData contains data for log entry updates
type UpdateLogData struct {
	Status                 string
	TokenUsage             *schemas.BifrostLLMUsage
	Cost                   *float64        // Cost in dollars from pricing plugin
	ListModelsOutput       []schemas.Model // For list models requests
	ChatOutput             *schemas.ChatMessage
	ResponsesOutput        []schemas.ResponsesMessage
	EmbeddingOutput        []schemas.EmbeddingData
	RerankOutput           []schemas.RerankResult
	OCROutput              *schemas.BifrostOCRResponse // For OCR responses
	ErrorDetails           *schemas.BifrostError
	SpeechOutput           *schemas.BifrostSpeechResponse          // For non-streaming speech responses
	TranscriptionOutput    *schemas.BifrostTranscriptionResponse   // For non-streaming transcription responses
	ImageGenerationOutput  *schemas.BifrostImageGenerationResponse // For non-streaming image generation responses
	VideoGenerationOutput  *schemas.BifrostVideoGenerationResponse // For non-streaming video generation responses
	VideoRetrieveOutput    *schemas.BifrostVideoGenerationResponse // For non-streaming video retrieve responses
	VideoDownloadOutput    *schemas.BifrostVideoDownloadResponse   // For non-streaming video download responses
	VideoListOutput        *schemas.BifrostVideoListResponse       // For non-streaming video list responses
	VideoDeleteOutput      *schemas.BifrostVideoDeleteResponse     // For non-streaming video delete responses
	RawRequest             any
	RawResponse            any
	IsLargePayloadRequest  bool // When true, RawRequest is a truncated preview string (skip sonic.Marshal)
	IsLargePayloadResponse bool // When true, RawResponse is a truncated preview string (skip sonic.Marshal)
}

// applyLargePayloadPreviews reads large payload/response preview strings from context
// and overrides RawRequest/RawResponse on updateData for truncated logging.
func applyLargePayloadPreviews(ctx *schemas.BifrostContext, updateData *UpdateLogData) {
	if isLargePayload, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool); ok && isLargePayload {
		if preview, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadRequestPreview).(string); ok && preview != "" {
			updateData.RawRequest = preview
			updateData.IsLargePayloadRequest = true
		}
	}
	if isLargeResponse, ok := ctx.Value(schemas.BifrostContextKeyLargeResponseMode).(bool); ok && isLargeResponse {
		if preview, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadResponsePreview).(string); ok && preview != "" {
			updateData.RawResponse = preview
			updateData.IsLargePayloadResponse = true
		}
	}
}

// applyLargePayloadPreviewsToEntry applies the large payload preview values from
// the context to the log entry, if they are available and content logging is enabled.
func applyLargePayloadPreviewsToEntry(ctx *schemas.BifrostContext, entry *logstore.Log, contentLoggingEnabled bool) {
	if ctx == nil || entry == nil {
		return
	}

	updateData := &UpdateLogData{}
	applyLargePayloadPreviews(ctx, updateData)
	shouldStoreRaw, _ := ctx.Value(schemas.BifrostContextKeyShouldStoreRawInLogs).(bool)

	if updateData.IsLargePayloadRequest {
		entry.IsLargePayloadRequest = true
		if shouldStoreRaw && contentLoggingEnabled {
			if preview, ok := updateData.RawRequest.(string); ok {
				entry.RawRequest = preview
			}
		}
	}
	if updateData.IsLargePayloadResponse {
		entry.IsLargePayloadResponse = true
		if shouldStoreRaw && contentLoggingEnabled {
			if preview, ok := updateData.RawResponse.(string); ok {
				entry.RawResponse = preview
			}
		}
	}
}

// redactionDataForLogging returns an owned request snapshot for asynchronous log writers.
func redactionDataForLogging(ctx *schemas.BifrostContext, contentLoggingEnabled bool) *schemas.RedactionData {
	if ctx == nil || !contentLoggingEnabled {
		return nil
	}
	if data, ok := schemas.RedactionDataFromContext(ctx); ok {
		snapshot := data.Clone()
		return &snapshot
	}
	return nil
}

// attachLogRedactionData copies guardrail redaction data into an LLM log entry.
func attachLogRedactionData(ctx *schemas.BifrostContext, entry *logstore.Log, contentLoggingEnabled bool) {
	if entry == nil {
		return
	}
	if snapshot := redactionDataForLogging(ctx, contentLoggingEnabled); snapshot != nil {
		entry.RedactionData = snapshot
	}
}

// attachMCPLogRedactionData copies guardrail redaction data into an MCP tool log entry.
func attachMCPLogRedactionData(ctx *schemas.BifrostContext, entry *logstore.MCPToolLog, contentLoggingEnabled bool) {
	if entry == nil {
		return
	}
	if snapshot := redactionDataForLogging(ctx, contentLoggingEnabled); snapshot != nil {
		entry.RedactionData = snapshot
	}
}

// sanitizeErrorForLogging returns a shallow copy of err with ExtraFields.RawRequest and
// RawResponse cleared when raw-byte persistence is disabled, preventing raw bytes from
// leaking into the store via JSON serialization.
//
// Every assignment to ErrorDetailsParsed (Log and MCPToolLog alike) must go through this
// function: logstore's SerializeFields, which runs on every write path (BeforeCreate hook,
// hybrid store, rdb batch writes), serializes ErrorDetailsParsed into the error_details
// column and overwrites anything a caller put in ErrorDetails. Callers set only the
// sanitized ErrorDetailsParsed and leave the string serialization to SerializeFields.
func sanitizeErrorForLogging(err *schemas.BifrostError, contentLoggingEnabled, shouldStoreRaw bool) *schemas.BifrostError {
	if err == nil {
		return nil
	}
	if contentLoggingEnabled && shouldStoreRaw {
		return err
	}
	cloned := *err
	cloned.ExtraFields.RawRequest = nil
	cloned.ExtraFields.RawResponse = nil
	return &cloned
}

// contentPolicy is the resolved per-request content handling decision.
type contentPolicy struct {
	// storeContent: content (messages, params, tool results) is populated on
	// the log entry and persisted.
	storeContent bool
	// hidden: the entry is stamped ContentHidden — the hybrid store keeps the
	// DB row content-free and the payload is only retained in object storage,
	// never hydrated back on reads.
	hidden bool
}

// visible reports whether logged content is served back through the API/UI.
func (c contentPolicy) visible() bool { return c.storeContent && !c.hidden }

// resolveContentPolicy resolves content handling for this request. Content
// logging is disabled either by the static disable_content_logging config or
// by the x-bf-disable-content-logging header (honored only when
// BifrostContextKeyAllowPerRequestStorageOverride is true in context, set by
// ConvertToBifrostContext from allow_per_request_content_storage_override
// config). What "disabled" means depends on retain_content_in_object_storage:
//   - off (default) → content is not persisted anywhere.
//   - on → content is offloaded to object storage as hidden: the DB row stays
//     content-free and reads never hydrate the payload back, but the object
//     store keeps the full payload. Requires an object-storage-backed log
//     store; degrades to not-persisted otherwise.
func (p *LoggerPlugin) resolveContentPolicy(ctx *schemas.BifrostContext) contentPolicy {
	disabled := p.disableContentLogging != nil && *p.disableContentLogging
	if ctx != nil {
		if perRequestAllowed, _ := ctx.Value(schemas.BifrostContextKeyAllowPerRequestStorageOverride).(bool); perRequestAllowed {
			if override, ok := ctx.Value(schemas.BifrostContextKeyDisableContentLogging).(bool); ok {
				disabled = override
			}
		}
	}
	if !disabled {
		return contentPolicy{storeContent: true}
	}
	if p.retainContentInObjectStorage != nil && *p.retainContentInObjectStorage {
		if p.objectStorageEnabled {
			return contentPolicy{storeContent: true, hidden: true}
		}
		p.retainWarnOnce.Do(func() {
			p.logger.Warn("retain_content_in_object_storage is enabled but the log store has no object storage configured; content-disabled requests are dropped entirely")
		})
	}
	return contentPolicy{}
}

// contentLoggingEnabled returns true if content (messages, params, tool results) should be
// recorded on the log entry for this request.
func (p *LoggerPlugin) contentLoggingEnabled(ctx *schemas.BifrostContext) bool {
	return p.resolveContentPolicy(ctx).storeContent
}

// applyMCPGovernanceFieldsToEntry stamps MCP log ownership from the request context.
func applyMCPGovernanceFieldsToEntry(ctx *schemas.BifrostContext, entry *logstore.MCPToolLog) {
	if ctx == nil || entry == nil {
		return
	}
	userID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserID)
	teamID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceTeamID)
	customerID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceCustomerID)
	businessUnitID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceBusinessUnitID)
	if userID != "" {
		entry.UserID = &userID
	}
	if teamID != "" {
		entry.TeamID = &teamID
	}
	if customerID != "" {
		entry.CustomerID = &customerID
	}
	if businessUnitID != "" {
		entry.BusinessUnitID = &businessUnitID
	}
}

// scheduleDeferredUsageUpdate schedules a deferred usage update for the request.
// applyErrorBillingFromBilledUsage backfills a failed/cancelled request's log
// entry from the usage the provider already processed before the failure
// (carried on BifrostError.ExtraFields.BilledUsage). Token usage is only filled
// when stream accumulation didn't already capture it, but cost is (re)computed
// whenever it is still missing - independent of whether tokens were already
// parsed, since a streaming error can populate usage without a cost.
func (p *LoggerPlugin) applyErrorBillingFromBilledUsage(ctx *schemas.BifrostContext, entry *logstore.Log, billed *schemas.BifrostLLMUsage, requestType schemas.RequestType) {
	if billed == nil {
		return
	}
	if entry.TokenUsageParsed == nil {
		entry.TokenUsageParsed = billed.DeepCopy()
		entry.PromptTokens = billed.PromptTokens
		entry.CompletionTokens = billed.CompletionTokens
		entry.TotalTokens = billed.TotalTokens
	}
	if entry.Cost == nil && p.pricingManager != nil {
		pricingScopes := modelcatalog.PricingLookupScopesFromContext(ctx, string(entry.Provider))
		if bd := p.pricingManager.CalculateCostBreakdownForUsage(billed, schemas.ModelProvider(entry.Provider), entry.Model, requestType, pricingScopes); bd != nil && bd.TotalCost > 0 {
			total := bd.TotalCost
			entry.Cost = &total
			// Attach the breakdown to the stored usage so SerializeFields
			// denormalizes the input/output/additional split, not just the total.
			if entry.TokenUsageParsed != nil && entry.TokenUsageParsed.Cost == nil {
				entry.TokenUsageParsed.Cost = bd
			}
		}
	}
}

// guardrailDebugForLog returns the request's guardrail debug snapshot.
func guardrailDebugForLog(ctx *schemas.BifrostContext, result *schemas.BifrostResponse) *schemas.BifrostGuardrailDebug {
	if debug, ok := schemas.GuardrailDebugFromContext(ctx); ok {
		return debug
	}
	if result == nil {
		return nil
	}
	return result.GetExtraFields().GuardrailDebug.Clone()
}

// applyInternalCallCosts adds sidecar costs when no response exists to carry them.
func (p *LoggerPlugin) applyInternalCallCosts(ctx *schemas.BifrostContext, entry *logstore.Log, guardrailDebug *schemas.BifrostGuardrailDebug) {
	if entry == nil || p.pricingManager == nil {
		return
	}
	pricingScopes := modelcatalog.PricingLookupScopesFromContext(ctx, string(entry.Provider))
	var cacheCost, guardrailCost float64
	if cacheDebug, ok := schemas.CacheDebugFromContext(ctx); ok {
		cacheCost = p.pricingManager.CalculateCacheEmbeddingCost(cacheDebug, pricingScopes)
	}
	if guardrailDebug != nil {
		guardrailCost = p.pricingManager.CalculateGuardrailCost(guardrailDebug, pricingScopes)
	}
	cost := cacheCost + guardrailCost
	if cost <= 0 {
		return
	}
	if entry.Cost == nil {
		entry.Cost = &cost
	} else {
		*entry.Cost += cost
	}

	// Both are additional-side sidecar costs. Merge onto the usage carrier's
	// breakdown when one exists (SerializeFields denormalizes from it); otherwise
	// write the additional_cost column directly. Don't synthesize a carrier: that
	// suppresses the deferred-usage watcher.
	sidecar := &schemas.BifrostCost{TotalCost: cost}
	if cacheCost > 0 || guardrailCost > 0 {
		sidecar.AdditionalCost = cacheCost + guardrailCost
		sidecar.AdditionalCostDetails = &schemas.AdditionalCostDetails{
			SemanticCacheCost: cacheCost,
			GuardrailCost:     guardrailCost,
		}
	}
	if entry.TokenUsageParsed != nil {
		entry.TokenUsageParsed.Cost = entry.TokenUsageParsed.Cost.Add(sidecar)
	} else {
		entry.AdditionalCost += cacheCost + guardrailCost
	}
}

const (
	// maxDeferredUsageWatchers caps goroutines parked waiting for trailing usage on
	// large-payload requests. Generous, because each one is idle on a channel receive;
	// it exists so a pathological burst cannot grow the goroutine set without limit.
	maxDeferredUsageWatchers = 2048
	// deferredUsageRetries / deferredUsageBackoff control how long we wait for the
	// batch writer to land the row before giving up. Backoff doubles per attempt:
	// 250ms, 500ms, 1s.
	deferredUsageRetries   = 3
	deferredUsageBackoff   = 250 * time.Millisecond
	deferredUsageDBTimeout = 10 * time.Second
)

// sleepCtx sleeps for d, returning false if the plugin context is cancelled first.
// Cleanup waits on p.wg, so an uncancellable sleep here delays shutdown.
func (p *LoggerPlugin) sleepCtx(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-p.ctx.Done():
		return false
	}
}

// batchAccountingTimeout bounds inline batch settlement on the /results response
// path so a stalled store or reporter cannot hang the caller. Anything that times
// out is not lost: the job stays due and the sweeper re-drives it.
const batchAccountingTimeout = 30 * time.Second

// batchRunnerProcessID distinguishes this process when no cluster node id is set.
// Deliberately random rather than PID-based: two replicas on different hosts can
// share a PID, and this identity is what keeps their batch claims apart.
var batchRunnerProcessID = newBatchRunnerProcessID()

func newBatchRunnerProcessID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand is effectively infallible here; a time-based value still
		// separates processes far better than a shared constant would.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// batchRunnerID builds the ownership-fence identity for a batch accounting worker.
//
// Batch claims are fenced on this value, so it must differ between workers that
// could contend for the same job. SetClusterNodeID supplies a stable id in
// clustered deployments, but it is never called in OSS — so without the
// per-process fallback every replica sharing a database would present the same
// identity ("logging" / "batch-sweeper") and the fence would not tell them apart.
// A per-process id is the right granularity: the fence only needs to separate
// live workers, and a restarted process should not inherit its predecessor's
// claims — those are recovered through the staleness path instead.
func (p *LoggerPlugin) batchRunnerID(prefix string) string {
	if nodeID, _ := p.clusterNodeID.Load().(string); nodeID != "" {
		return prefix + ":" + nodeID
	}
	return prefix + ":" + batchRunnerProcessID
}

func (p *LoggerPlugin) accountBatchResults(entry *logstore.Log, result *schemas.BifrostResponse, pricingScopes *modelcatalog.PricingLookupScopes) {
	if result == nil || result.BatchResultsResponse == nil || entry == nil || p.pricingManager == nil || p.batchStore == nil {
		return
	}
	batchResp := result.BatchResultsResponse
	if batchResp.BatchID == "" {
		return
	}

	// This runs inline on the /results response path, and settlement touches the
	// config store, the log store and the governance reporter. p.ctx is the
	// plugin's lifetime context and has no deadline, so any one of those stalling
	// would hang the caller's HTTP response indefinitely. Bound the whole
	// settlement instead; the sweeper re-drives anything that times out, since a
	// job left un-accounted stays due.
	ctx, cancel := context.WithTimeout(p.ctx, batchAccountingTimeout)
	defer cancel()

	claimedBy := p.batchRunnerID("logging")
	p.mu.Lock()
	usageReporter := p.batchUsageReporter
	p.mu.Unlock()

	summary, err := batchaccounting.AccountBatchResults(ctx, p.batchStore, p.store, p.pricingManager, batchaccounting.Request{
		Provider:      schemas.ModelProvider(entry.Provider),
		BatchID:       batchResp.BatchID,
		FallbackModel: entry.Model,
		Endpoint:      batchResp.Endpoint,
		Results:       batchResp.Results,
		ParseErrors:   batchResp.ExtraFields.ParseErrors,
		BatchJob:      batchJobFromEntry(entry, batchResp.BatchID, entry.Model, string(batchResp.Endpoint), string(schemas.BatchStatusCompleted)),
		BaseLog:       entry,
		Emitter:       p,
		UsageReporter: usageReporter,
		ClaimedBy:     claimedBy,
		Scopes:        pricingScopes,
	})
	if err != nil {
		p.logger.Warn("failed to account batch results for provider=%s batch_id=%s: %v", entry.Provider, batchResp.BatchID, err)
		return
	}
	if summary != nil && summary.Accounted {
		p.logger.Info("accounted batch results for provider=%s batch_id=%s cost=%f log_id=%s", entry.Provider, batchResp.BatchID, summary.Cost, summary.LogID)
	}
	attachBatchResultsDisplay(entry, batchResp, summary)
}

func attachBatchResultsDisplay(entry *logstore.Log, batchResp *schemas.BifrostBatchResultsResponse, summary *batchaccounting.Summary) {
	debug := &schemas.BifrostBatchDebug{BatchID: batchResp.BatchID}
	if counts := schemas.BatchRequestCountsFromResults(batchResp.Results); !counts.IsZero() {
		debug.RequestCounts = &counts
	}
	if summary != nil {
		debug.Status = summary.Status
		if summary.Complete || len(summary.ModelBreakdowns) > 0 {
			accounting := &schemas.BatchAccountingDebug{
				ModelBreakdowns: summary.ModelBreakdowns,
				Incomplete:      !summary.Complete,
				Echo:            true,
			}
			if summary.Complete {
				cost := summary.Cost
				accounting.Cost = &cost
			}
			debug.Accounting = accounting
		}
	}
	if debug.IsZero() {
		return
	}
	entry.BatchDebugParsed = debug
}

func (p *LoggerPlugin) EmitBatchAggregateLog(ctx context.Context, entry *logstore.Log) {
	p.makePostWriteCallback(nil)(entry)
}

func (p *LoggerPlugin) recordBatchJobLifecycle(entry *logstore.Log, result *schemas.BifrostResponse) {
	if entry == nil || result == nil || p.batchStore == nil {
		return
	}

	var job *tables.TableBatchJob
	now := time.Now().UTC()
	switch {
	case result.BatchCreateResponse != nil:
		resp := result.BatchCreateResponse
		job = batchJobFromEntry(entry, resp.ID, entry.Model, resp.Endpoint, string(resp.Status))
		job.InputFileID = resp.InputFileID
		job.OutputFileID = resp.OutputFileID
		job.ErrorFileID = resp.ErrorFileID
		job.ResultsURL = resp.ResultsURL
		addBatchDetailToLog(entry, resp.ID, string(resp.Status), resp.RequestCounts)
	case result.BatchRetrieveResponse != nil:
		resp := result.BatchRetrieveResponse
		job = batchJobFromEntry(entry, resp.ID, entry.Model, resp.Endpoint, string(resp.Status))
		job.InputFileID = resp.InputFileID
		job.OutputFileID = resp.OutputFileID
		job.ErrorFileID = resp.ErrorFileID
		job.ResultsURL = resp.ResultsURL
		addBatchDetailToLog(entry, resp.ID, string(resp.Status), resp.RequestCounts)
	default:
		return
	}

	if job.BatchID == "" {
		return
	}
	if !tables.IsTerminalBatchProviderStatus(job.ProviderStatus) {
		next := now.Add(time.Minute)
		job.NextCheckAt = &next
	} else if job.ProviderStatus == string(schemas.BatchStatusCompleted) ||
		job.ProviderStatus == string(schemas.BatchStatusEnded) {
		job.NextCheckAt = &now
	}
	if err := p.batchStore.UpsertBatchJob(p.ctx, job); err != nil {
		p.logger.Warn("failed to record batch job lifecycle for provider=%s batch_id=%s: %v", job.Provider, job.BatchID, err)
	}
}

// addBatchDetailToLog records which batch a batch_create / batch_retrieve row
// addressed and the provider's progress counts for it.
func addBatchDetailToLog(entry *logstore.Log, batchID string, status string, counts schemas.BatchRequestCounts) {
	if entry == nil {
		return
	}
	debug := &schemas.BifrostBatchDebug{BatchID: batchID, Status: status}
	if !counts.IsZero() {
		debug.RequestCounts = &counts
	}
	if debug.IsZero() {
		return
	}
	entry.BatchDebugParsed = debug
}

func batchJobFromEntry(entry *logstore.Log, batchID string, model string, endpoint string, status string) *tables.TableBatchJob {
	job := &tables.TableBatchJob{
		Provider:         entry.Provider,
		BatchID:          batchID,
		Model:            model,
		Endpoint:         endpoint,
		ProviderStatus:   status,
		AccountingStatus: tables.BatchJobAccountingStatusPending,
		SelectedKeyID:    entry.SelectedKeyID,
		VirtualKeyID:     entry.VirtualKeyID,
		UserID:           entry.UserID,
		TeamID:           entry.TeamID,
		CustomerID:       entry.CustomerID,
		BudgetIDs:        stringSlicePtr(entry.BudgetIDsParsed),
		RateLimitIDs:     stringSlicePtr(entry.RateLimitIDsParsed),
	}
	if entry.ID != "" {
		sourceLogID := entry.ID
		job.SourceLogID = &sourceLogID
	}
	if job.ID == "" && job.Provider != "" && job.BatchID != "" {
		job.ID = tables.BatchJobID(job.Provider, job.BatchID)
	}
	return job
}

func (p *LoggerPlugin) scheduleDeferredUsageUpdate(ctx *schemas.BifrostContext, requestID string, usageAlreadyPresent bool) {
	if usageAlreadyPresent || ctx == nil {
		return
	}

	deferredChan, ok := ctx.Value(schemas.BifrostContextKeyDeferredUsage).(<-chan *schemas.BifrostLLMUsage)
	if !ok || deferredChan == nil {
		return
	}
	// Cap the watcher goroutines themselves. deferredUsageSem below only limits how
	// many updates touch the DB concurrently; the goroutine is created before it is
	// acquired and then parks on the channel receive, so without this the live
	// goroutine count tracks in-flight large-payload requests rather than the limit.
	select {
	case p.deferredUsageWatchSem <- struct{}{}:
	default:
		if n := p.droppedDeferredUsage.Add(1); n == 1 || n%1000 == 0 {
			p.logger.Warn("deferred usage update dropped for request %s: %d watchers already parked (%d dropped so far)", requestID, maxDeferredUsageWatchers, n)
		}
		return
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() { <-p.deferredUsageWatchSem }()
		// Large-response phase B closes this channel after trailing usage extraction completes.
		var deferredUsage *schemas.BifrostLLMUsage
		var chanOpen bool
		select {
		case deferredUsage, chanOpen = <-deferredChan:
		case <-p.ctx.Done():
			// Plugin is shutting down; stop waiting for trailing usage.
			return
		}
		if !chanOpen || deferredUsage == nil {
			return
		}

		// Acquire semaphore — drop if all slots busy to prevent unbounded goroutines
		// from exhausting DB connections when Postgres is slow
		select {
		case p.deferredUsageSem <- struct{}{}:
			defer func() { <-p.deferredUsageSem }()
		default:
			p.droppedDeferredUsage.Add(1)
			p.logger.Warn("deferred usage update dropped for request %s: semaphore full", requestID)
			return
		}
		usageUpdates := map[string]interface{}{
			"prompt_tokens":     deferredUsage.PromptTokens,
			"completion_tokens": deferredUsage.CompletionTokens,
			"total_tokens":      deferredUsage.TotalTokens,
		}
		tempEntry := &logstore.Log{TokenUsageParsed: deferredUsage}
		if serErr := tempEntry.SerializeFields(); serErr == nil {
			usageUpdates["token_usage"] = tempEntry.TokenUsage
			usageUpdates["cached_read_tokens"] = tempEntry.CachedReadTokens
		}

		// Wait for the batch writer to land the row, then patch usage onto it.
		// This whole loop holds a deferredUsageSem slot, and dropping is immediate
		// once the slots are full, so the phase is bounded twice over. The backoff is
		// short: the previous 2s/4s/8s schedule meant a handful of slow lookups
		// stalled every deferred update for 14s and dropped the rest. And a single
		// deadline covers all attempts rather than one per attempt, because the ctx
		// also covers waiting for a pool connection: a per-attempt budget let a slow
		// store hold a slot for ~30s and drop everything arriving in that window.
		retryCtx, cancelRetry := context.WithTimeout(p.ctx, deferredUsageDBTimeout)
		defer cancelRetry()
		var found bool
		for attempt := range deferredUsageRetries {
			presentResult, findErr := p.store.IsLogEntryPresent(retryCtx, requestID)
			if findErr != nil {
				p.logger.Warn("failed to check if log entry is present for request %s: %v", requestID, findErr)
			} else if presentResult {
				found = true
				break
			}
			// Budget spent: the remaining attempts would fail instantly, so don't
			// sleep through backoffs that cannot help.
			if retryCtx.Err() != nil {
				break
			}
			// Sleep on the error path too, otherwise a DB error burns every retry
			// in microseconds, exactly when the store needs time to recover.
			if attempt < deferredUsageRetries-1 && !p.sleepCtx(deferredUsageBackoff<<attempt) {
				return
			}
		}
		if !found {
			p.logger.Warn("log entry not found for request %s after %d retries. failed to update deferred usage for large payload request", requestID, deferredUsageRetries)
			return
		}

		updCtx, cancel := context.WithTimeout(p.ctx, deferredUsageDBTimeout)
		defer cancel()
		if updErr := p.store.Update(updCtx, requestID, usageUpdates); updErr != nil {
			p.logger.Warn("failed to update deferred usage for request %s: %v", requestID, updErr)
		}
	}()
}

// RecalculateCostResult represents summary stats from a cost backfill operation
type RecalculateCostResult struct {
	TotalMatched int64 `json:"total_matched"`
	Updated      int   `json:"updated"`
	Skipped      int   `json:"skipped"`
	// Unpriceable is the subset of Skipped left alone because their pricing inputs
	// could not be recovered (for example, a failed object-storage fetch) rather than
	// because there was nothing to charge. Surfaced separately so
	// a caller can distinguish "no cost applies" from "we refused to write a number
	// we knew would be wrong".
	Unpriceable int   `json:"unpriceable,omitempty"`
	Remaining   int64 `json:"remaining"`
}

// RecalculateCostProgress represents a progress event from a cost backfill operation.
type RecalculateCostProgress struct {
	TotalMatched int64  `json:"total_matched"`
	Processed    int    `json:"processed"`
	Updated      int    `json:"updated"`
	Skipped      int    `json:"skipped"`
	Remaining    *int64 `json:"remaining,omitempty"`
	Done         bool   `json:"done"`
}

// LogMessage represents a message in the logging queue
type LogMessage struct {
	Operation          LogOperation
	RequestID          string                             // Unique ID for the request
	ParentRequestID    string                             // Unique ID for the parent request (used for fallback requests)
	NumberOfRetries    int                                // Number of retries
	FallbackIndex      int                                // Fallback index
	SelectedKeyID      string                             // Selected key ID
	SelectedKeyName    string                             // Selected key name
	AttemptTrail       []schemas.KeyAttemptRecord         // Per-attempt key selection history
	VirtualKeyID       string                             // Virtual key ID
	VirtualKeyName     string                             // Virtual key name
	RoutingEnginesUsed []string                           // List of routing engines used
	RoutingRuleID      string                             // Routing rule ID
	RoutingRuleName    string                             // Routing rule name
	Timestamp          time.Time                          // Of the preHook/postHook call
	Latency            int64                              // For latency updates
	InitialData        *InitialLogData                    // For create operations
	SemanticCacheDebug *schemas.BifrostCacheDebug         // For semantic cache operations
	UpdateData         *UpdateLogData                     // For update operations
	StreamResponse     *streaming.ProcessedStreamResponse // For streaming delta updates
	RoutingEngineLogs  string                             // Formatted routing engine decision logs
}

// InitialLogData contains data for initial log entry creation
type InitialLogData struct {
	Status                 string
	Provider               string
	Model                  string
	Object                 string
	InputHistory           []schemas.ChatMessage
	ResponsesInputHistory  []schemas.ResponsesMessage
	Params                 any
	SpeechInput            *schemas.SpeechInput
	TranscriptionInput     *schemas.TranscriptionInput
	OCRInput               *schemas.OCRDocument
	ImageGenerationInput   *schemas.ImageGenerationInput
	ImageEditInput         *schemas.ImageEditInput
	ImageVariationInput    *schemas.ImageVariationInput
	VideoGenerationInput   *schemas.VideoGenerationInput
	VideoEditInput         *schemas.VideoEditInput
	Tools                  []schemas.ChatTool
	RoutingEngineUsed      []string
	Metadata               map[string]any
	PassthroughRequestBody string // Raw body for passthrough requests (UTF-8)
	UserAgent              string // Raw HTTP User-Agent of the calling client; mapped to a client app in the UI
	App                    string // Backend-detected client app derived from UserAgent
}

// LogCallback is a function that gets called when a new log entry is created
type LogCallback func(ctx context.Context, logEntry *logstore.Log)

// MCPToolLogCallback is a function that gets called when a new MCP tool log entry is created or updated
type MCPToolLogCallback func(*logstore.MCPToolLog)

// Config controls logging plugin behavior.
type Config struct {
	DisableContentLogging        *bool                  `json:"disable_content_logging"`
	RetainContentInObjectStorage *bool                  `json:"retain_content_in_object_storage"` // Pointer to live config value; when true, content-disabled requests are offloaded to object storage as hidden instead of dropped
	LoggingHeaders               *[]string              `json:"logging_headers"`                  // Pointer to live config slice; changes are reflected immediately without restart
	Writer                       *logstore.WriterConfig `json:"writer,omitempty"`
	ObjectStorageEnabled         bool                   `json:"-"` // Set by the server from the logstore config; required for retain_content_in_object_storage to take effect
}

func validateWriterConfig(config logstore.WriterConfig) error {
	if config.MaxBatchSize <= 0 {
		return fmt.Errorf("writer max_batch_size must be greater than 0")
	}
	if config.BatchInterval == "" {
		return fmt.Errorf("writer batch_interval is required")
	}
	batchInterval, err := time.ParseDuration(config.BatchInterval)
	if err != nil {
		return fmt.Errorf("writer batch_interval must be a valid Go duration: %w", err)
	}
	if batchInterval <= 0 {
		return fmt.Errorf("writer batch_interval must be greater than 0")
	}
	if config.MaxBatchBytes <= 0 {
		return fmt.Errorf("writer max_batch_bytes must be greater than 0")
	}
	if config.WriteQueueCapacity <= 0 {
		return fmt.Errorf("writer write_queue_capacity must be greater than 0")
	}
	if config.DeferredUsageConcurrency <= 0 {
		return fmt.Errorf("writer deferred_usage_concurrency must be greater than 0")
	}
	return nil
}

type compiledUserAgentMapping struct {
	Pattern   string
	MatchType schemas.UserAgentMappingMatchType
	App       string
	Regex     *regexp.Regexp
}

// LoggerPlugin implements the schemas.LLMPlugin and schemas.MCPPlugin interfaces
type LoggerPlugin struct {
	ctx                          context.Context
	store                        logstore.LogStore
	batchStore                   batchaccounting.SweepStore // configstore-backed mutable batch coordination state (nil disables batch accounting)
	disableContentLogging        *bool
	retainContentInObjectStorage *bool     // Pointer to live config value; when true, content-disabled requests are stored hidden instead of dropped
	objectStorageEnabled         bool      // Log store offloads payloads to object storage; required for retain_content_in_object_storage
	retainWarnOnce               sync.Once // Warns once when retention is configured without object storage
	loggingHeaders               *[]string // Pointer to live config slice for headers to capture in metadata
	pricingManager               *modelcatalog.ModelCatalog
	mcpCatalog                   *mcpcatalog.MCPCatalog // MCP catalog for tool cost calculation
	mu                           sync.Mutex
	done                         chan struct{}
	cleanupOnce                  sync.Once // Ensures cleanup only runs once
	wg                           sync.WaitGroup
	logger                       schemas.Logger
	logCallback                  LogCallback
	batchUsageReporter           batchaccounting.UsageReporter
	mcpToolLogCallback           MCPToolLogCallback // Callback for MCP tool log entries
	droppedRequests              atomic.Int64
	cleanupTicker                *time.Ticker          // Ticker for cleaning up old processing logs
	logMsgPool                   sync.Pool             // Pool for reusing LogMessage structs
	updateDataPool               sync.Pool             // Pool for reusing UpdateLogData structs
	pendingLogsEntries           sync.Map              // Maps requestID -> *PendingLogData (PreLLMHook input data awaiting PostLLMHook)
	pendingLogsToInject          sync.Map              // Maps traceID -> *pendingInjectEntries (log entries to inject, supports multiple per trace)
	pendingMCPLogsToInject       sync.Map              // Maps mcpLogID -> *logstore.MCPToolLog (PreMCPHook input data awaiting PostMCPHook)
	writerConfig                 logstore.WriterConfig // Resolved async writer queue and batch settings
	writeQueue                   chan *writeQueueEntry // Buffered channel for batch write queue
	closed                       atomic.Bool           // Set during cleanup to prevent sends on closed writeQueue
	deferredUsageSem             chan struct{}         // Limits concurrent deferred usage DB updates
	deferredUsageWatchSem        chan struct{}         // Caps goroutines parked on a deferred-usage channel (see scheduleDeferredUsageUpdate)
	droppedDeferredUsage         atomic.Int64          // Deferred usage updates dropped because a semaphore was full
	clusterNodeID                atomic.Value          // Cluster node ID (string) for log attribution in clustered deployments
	batchCtx                     context.Context       // Cancelled by Cleanup to stop the batchWriter goroutine before any further DB work
	batchCancel                  context.CancelFunc    // Cancels batchCtx
	batchSweeperCancel           context.CancelFunc    // Cancels the batch accounting sweeper, when enabled
	batchWriterDone              chan struct{}         // Closed by batchWriter on exit; receiving from it transfers writeQueue ownership to Cleanup
	recoveredBatch               []*writeQueueEntry    // batchWriter parks its in-memory batch here before exiting; safe to read after batchWriterDone closes (happens-before)
	userAgentMappings            atomic.Value          // []compiledUserAgentMapping, read from request hot paths
	userAgentMappingMu           sync.Mutex            // serializes user-agent mapping write+reload sequences to keep the cache consistent
}

// Init creates new logger plugin with given log store. batchStore is the
// configstore-backed coordination store for delayed batch accounting; it may be
// nil, which disables batch accounting.
func Init(ctx context.Context, config *Config, logger schemas.Logger, logsStore logstore.LogStore, batchStore batchaccounting.SweepStore, pricingManager *modelcatalog.ModelCatalog, mcpCatalog *mcpcatalog.MCPCatalog) (*LoggerPlugin, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if logsStore == nil {
		return nil, fmt.Errorf("logs store cannot be nil")
	}
	if pricingManager == nil {
		logger.Warn("logging plugin requires model catalog to calculate cost, all LLM cost calculations will be skipped.")
	}
	if mcpCatalog == nil {
		logger.Warn("logging plugin requires MCP catalog to calculate cost, all MCP cost calculations will be skipped.")
	}

	writerConfig := config.Writer.WithDefaults()
	if err := validateWriterConfig(writerConfig); err != nil {
		return nil, err
	}
	logger.Info("initializing logging writer settings: max_batch_size=%d batch_interval=%s max_batch_bytes=%d write_queue_capacity=%d deferred_usage_concurrency=%d",
		writerConfig.MaxBatchSize,
		writerConfig.BatchInterval,
		writerConfig.MaxBatchBytes,
		writerConfig.WriteQueueCapacity,
		writerConfig.DeferredUsageConcurrency,
	)

	batchCtx, batchCancel := context.WithCancel(ctx)
	plugin := &LoggerPlugin{
		ctx:                          ctx,
		store:                        logsStore,
		batchStore:                   batchStore,
		pricingManager:               pricingManager,
		mcpCatalog:                   mcpCatalog,
		disableContentLogging:        config.DisableContentLogging,
		retainContentInObjectStorage: config.RetainContentInObjectStorage,
		objectStorageEnabled:         config.ObjectStorageEnabled,
		loggingHeaders:               config.LoggingHeaders,
		done:                         make(chan struct{}),
		logger:                       logger,
		writerConfig:                 writerConfig,
		writeQueue:                   make(chan *writeQueueEntry, writerConfig.WriteQueueCapacity),
		deferredUsageSem:             make(chan struct{}, writerConfig.DeferredUsageConcurrency),
		deferredUsageWatchSem:        make(chan struct{}, maxDeferredUsageWatchers),
		batchCtx:                     batchCtx,
		batchCancel:                  batchCancel,
		batchWriterDone:              make(chan struct{}),
		logMsgPool: sync.Pool{
			New: func() any {
				return &LogMessage{}
			},
		},
		updateDataPool: sync.Pool{
			New: func() any {
				return &UpdateLogData{}
			},
		},
	}

	// Prewarm the pools for better performance at startup
	for range 1000 {
		plugin.logMsgPool.Put(&LogMessage{})
		plugin.updateDataPool.Put(&UpdateLogData{})
	}

	if err := plugin.ReloadUserAgentMappings(ctx); err != nil {
		logger.Warn("failed to load user agent mappings: %v", err)
		plugin.userAgentMappings.Store([]compiledUserAgentMapping{})
	}

	// Start cleanup ticker (runs every 1 minute)
	plugin.cleanupTicker = time.NewTicker(1 * time.Minute)
	plugin.wg.Add(1)
	go plugin.cleanupWorker()

	// Start the batch writer goroutine (single writer for all DB writes)
	plugin.wg.Add(1)
	go plugin.batchWriter()

	return plugin, nil
}

// ReloadUserAgentMappings refreshes the in-memory custom User-Agent mapping cache.
func (p *LoggerPlugin) ReloadUserAgentMappings(ctx context.Context) error {
	mappings, err := p.store.ListUserAgentMappings(ctx, true)
	if err != nil {
		return err
	}
	compiled := make([]compiledUserAgentMapping, 0, len(mappings))
	for _, mapping := range mappings {
		matchType := schemas.UserAgentMappingMatchType(mapping.MatchType)
		entry := compiledUserAgentMapping{
			Pattern:   mapping.Pattern,
			MatchType: matchType,
			App:       mapping.App,
		}
		if matchType == schemas.UserAgentMappingMatchTypeRegex {
			re, err := regexp.Compile(mapping.Pattern)
			if err != nil {
				p.logger.Warn("skipping invalid user agent mapping regex %q: %v", mapping.Pattern, err)
				continue
			}
			entry.Regex = re
		}
		compiled = append(compiled, entry)
	}
	p.userAgentMappings.Store(compiled)
	return nil
}

func (p *LoggerPlugin) detectAppFromUserAgent(userAgent string) string {
	if strings.TrimSpace(userAgent) == "" {
		return ""
	}
	if mappings, ok := p.userAgentMappings.Load().([]compiledUserAgentMapping); ok {
		for _, mapping := range mappings {
			if mapping.App == "" || mapping.Pattern == "" {
				continue
			}
			if mapping.Regex != nil {
				if mapping.Regex.MatchString(userAgent) {
					return mapping.App
				}
				continue
			}
			if schemas.MatchUserAgent(userAgent, mapping.Pattern, mapping.MatchType) {
				return mapping.App
			}
		}
	}
	return schemas.DetectAppFromUserAgent(userAgent)
}

// SetClusterNodeID sets the cluster node ID that will be attached to all log entries.
// Used in clustered deployments to attribute log entries to specific nodes for
// disconnected node usage recovery. Uses atomic.Value since it is written at
// startup and read concurrently from request hot paths.
func (p *LoggerPlugin) SetClusterNodeID(nodeID string) {
	p.clusterNodeID.Store(nodeID)
}

// cleanupWorker periodically removes old processing logs
func (p *LoggerPlugin) cleanupWorker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.cleanupTicker.C:
			p.cleanupOldProcessingLogs()
		case <-p.done:
			return
		}
	}
}

// cleanupOldProcessingLogs removes processing logs older than 30 minutes
// and stale pending log entries from the in-memory map
func (p *LoggerPlugin) cleanupOldProcessingLogs() {
	// Calculate timestamp for 30 minutes ago in UTC to match log entry timestamps
	thirtyMinutesAgo := time.Now().UTC().Add(-1 * 30 * time.Minute)

	// Delete LLM processing logs older than 30 minutes
	if err := p.store.Flush(p.ctx, thirtyMinutesAgo); err != nil {
		p.logger.Warn("failed to cleanup old processing LLM logs: %v", err)
	}

	// Delete MCP tool processing logs older than 30 minutes
	if err := p.store.FlushMCPToolLogs(p.ctx, thirtyMinutesAgo); err != nil {
		p.logger.Warn("failed to cleanup old processing MCP tool logs: %v", err)
	}

	// Clean up stale pending log entries (requests where PostLLMHook never fired)
	p.cleanupStalePendingLogs()
}

// SetLogCallback sets a callback function that will be called for each log entry
func (p *LoggerPlugin) SetLogCallback(callback LogCallback) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.logCallback = callback
}

func (p *LoggerPlugin) SetBatchUsageReporter(reporter batchaccounting.UsageReporter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.batchUsageReporter = reporter
}

func (p *LoggerPlugin) StartBatchAccountingSweeper(fetcher batchaccounting.BatchResultFetcher, interval time.Duration, kvStore schemas.KVStore) context.CancelFunc {
	if fetcher == nil || p.store == nil || p.batchStore == nil || p.pricingManager == nil {
		if p.logger != nil {
			p.logger.Warn("batch accounting sweeper not started: missing fetcher, store, batch store, or pricing manager")
		}
		return func() {}
	}
	if kvStore == nil && p.logger != nil {
		p.logger.Debug("batch accounting sweeper starting without KV store")
	}
	ctx, cancel := context.WithCancel(p.ctx)
	p.mu.Lock()
	// Cleanup sets p.closed and cancels the sweeper under this same lock before it
	// reaches p.wg.Wait(), so checking it here is what keeps the wg.Add below from
	// racing that Wait — a reload wiring a sweeper onto an instance already shutting
	// down would otherwise violate the WaitGroup contract and start a goroutine
	// writing through a closed plugin.
	if p.closed.Load() {
		p.mu.Unlock()
		cancel()
		if p.logger != nil {
			p.logger.Warn("batch accounting sweeper not started: logging plugin is shutting down")
		}
		return func() {}
	}
	if p.batchSweeperCancel != nil {
		p.batchSweeperCancel()
	}
	p.batchSweeperCancel = cancel
	usageReporter := p.batchUsageReporter
	p.mu.Unlock()
	claimedBy := p.batchRunnerID("batch-sweeper")
	sweeper := batchaccounting.NewSweeper(p.batchStore, p.store, p.pricingManager, fetcher, p, usageReporter, batchaccounting.SweeperConfig{
		Interval:  interval,
		ClaimedBy: claimedBy,
		KVStore:   kvStore,
		Logger:    p.logger,
	})
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		sweeper.Run(ctx)
	}()
	return cancel
}

// GetName returns the name of the plugin
func (p *LoggerPlugin) GetName() string {
	return PluginName
}

// HTTPTransportPreAuthHook is a no-op: this plugin does no credential work, so it has
// nothing to do before the transport authenticates the request (HTTPTransportPlugin interface).
func (*LoggerPlugin) HTTPTransportPreAuthHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

// HTTPTransportPreHook is not used for this plugin
func (p *LoggerPlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

// HTTPTransportPostHook is not used for this plugin
func (p *LoggerPlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes through streaming chunks unchanged
func (p *LoggerPlugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// userAgentFromContext returns the raw HTTP User-Agent of the calling client from
// the request header map, or "" when absent. Keys in the map are lowercased, so
// the lookup is case-insensitive. The value is stored verbatim on the log entry;
// mapping it to a client app happens in the UI.
func userAgentFromContext(ctx *schemas.BifrostContext) string {
	allHeaders, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	if allHeaders != nil {
		if ua := allHeaders["user-agent"]; ua != "" {
			return ua
		}
		for key, value := range allHeaders {
			if strings.EqualFold(key, "user-agent") && value != "" {
				return value
			}
		}
	}
	ua, _ := ctx.Value(schemas.BifrostContextKeyUserAgent).(string)
	return ua
}

// captureLoggingHeaders extracts configured logging headers and x-bf-lh-* prefixed headers
// from the request context. Returns a new metadata map, or nil if no headers were captured.
// System entries (e.g. isAsyncRequest) should be set AFTER calling this so they take precedence.
func (p *LoggerPlugin) captureLoggingHeaders(ctx *schemas.BifrostContext) map[string]interface{} {
	allHeaders, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	if allHeaders == nil {
		return nil
	}

	var metadata map[string]any

	// Check configured logging headers (supports wildcard patterns like "x-custom-*")
	if p.loggingHeaders != nil {
		for _, h := range *p.loggingHeaders {
			pattern := strings.ToLower(strings.TrimSpace(h))
			for hKey, hVal := range allHeaders {
				if schemas.MatchHeaderPattern(hKey, pattern) {
					if metadata == nil {
						metadata = make(map[string]any)
					}
					metadata[hKey] = hVal
				}
			}
		}
	}

	// Check x-bf-lh-* prefixed headers
	for key, val := range allHeaders {
		if labelName, ok := strings.CutPrefix(key, "x-bf-lh-"); ok && labelName != "" {
			if metadata == nil {
				metadata = make(map[string]any)
			}
			metadata[labelName] = val
		}
	}

	// Include x-bf-dim-* dimensions in metadata.
	if dims, ok := ctx.Value(schemas.BifrostContextKeyDimensions).(map[string]string); ok {
		for k, v := range dims {
			if metadata == nil {
				metadata = make(map[string]any)
			}
			if _, exists := metadata[k]; !exists {
				metadata[k] = v
			}
		}
	}

	return metadata
}

// PreRequestHook implements schemas.LLMPlugin (no-op — required for plugin indexing).
func (p *LoggerPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// PreLLMHook is called before a request is processed - FULLY ASYNC, NO DATABASE I/O
// Parameters:
//   - ctx: The Bifrost context
//   - req: The Bifrost request
//
// Returns:
//   - *schemas.BifrostRequest: The processed request
//   - *schemas.LLMPluginShortCircuit: The plugin short circuit if the request is not allowed
//   - error: Any error that occurred during processing
func (p *LoggerPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if ctx == nil {
		// Log error but don't fail the request
		p.logger.Error("context is nil in PreLLMHook")
		return req, nil, nil
	}

	// Extract request ID from context
	requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		p.logger.Error("request-id not found in context or is empty")
		return req, nil, nil
	}

	createdTimestamp := time.Now().UTC()

	p.logger.Debug("PreLLMHook: request %s type=%q", requestID, req.RequestType)

	// If request type is streaming we create a stream accumulator via the tracer
	if bifrost.IsStreamRequestType(req.RequestType) {
		tracer, traceID, err := bifrost.GetTracerFromContext(ctx)
		if err == nil && tracer != nil && traceID != "" {
			tracer.CreateStreamAccumulator(traceID, createdTimestamp)
		}
	}

	provider, model, _ := req.GetRequestFields()

	initialData := &InitialLogData{
		Provider: string(provider),
		Model:    model,
		Object:   string(req.RequestType),
	}
	if req.RequestType == schemas.RealtimeRequest {
		initialData.Object = "realtime.turn"
	}

	// Capture the raw User-Agent of the calling client (stored verbatim; the UI
	// maps it to a client app such as Claude Code, Codex, or Cursor).
	initialData.UserAgent = userAgentFromContext(ctx)
	initialData.App = p.detectAppFromUserAgent(initialData.UserAgent)
	if appKey := schemas.AppKeyFromName(initialData.App); appKey != "" {
		ctx.SetValue(schemas.BifrostContextKeyApp, appKey)
	}

	if p.contentLoggingEnabled(ctx) {
		inputHistory, responsesInputHistory := p.extractInputHistory(req)
		initialData.InputHistory = inputHistory
		initialData.ResponsesInputHistory = responsesInputHistory

		switch req.RequestType {
		case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
			initialData.Params = req.TextCompletionRequest.Params
		case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
			initialData.Params = req.ChatRequest.Params
			initialData.Tools = req.ChatRequest.Params.Tools
		case schemas.ResponsesRequest, schemas.ResponsesStreamRequest, schemas.WebSocketResponsesRequest:
			initialData.Params = req.ResponsesRequest.Params

			var tools []schemas.ChatTool
			for _, tool := range req.ResponsesRequest.Params.Tools {
				tools = append(tools, *tool.ToChatTool())
			}
			initialData.Tools = tools
		case schemas.RealtimeRequest:
			if req.ResponsesRequest != nil {
				initialData.Params = req.ResponsesRequest.Params
				if req.ResponsesRequest.Params != nil {
					var tools []schemas.ChatTool
					for _, tool := range req.ResponsesRequest.Params.Tools {
						tools = append(tools, *tool.ToChatTool())
					}
					initialData.Tools = tools
				}
			}
		case schemas.EmbeddingRequest:
			initialData.Params = req.EmbeddingRequest.Params
		case schemas.RerankRequest:
			initialData.Params = req.RerankRequest.Params
		case schemas.OCRRequest:
			initialData.Params = req.OCRRequest.Params
			initialData.OCRInput = &req.OCRRequest.Document
		case schemas.SpeechRequest, schemas.SpeechStreamRequest:
			initialData.Params = req.SpeechRequest.Params
			initialData.SpeechInput = req.SpeechRequest.Input
		case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
			initialData.Params = req.TranscriptionRequest.Params
			input := req.TranscriptionRequest.Input
			if input != nil {
				reqThreshold, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadRequestThreshold).(int64)
				if reqThreshold > 0 && int64(len(input.File)) > reqThreshold {
					// Strip binary file content when it exceeds the large payload threshold
					// to avoid serializing multi-MB audio into the log database.
					logInput := *input
					logInput.File = nil
					initialData.TranscriptionInput = &logInput
				} else {
					initialData.TranscriptionInput = input
				}
			}
		case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest:
			initialData.Params = req.ImageGenerationRequest.Params
			initialData.ImageGenerationInput = req.ImageGenerationRequest.Input
		case schemas.ImageEditRequest, schemas.ImageEditStreamRequest:
			params := req.ImageEditRequest.Params
			input := req.ImageEditRequest.Input
			if input != nil {
				reqThreshold, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadRequestThreshold).(int64)
				if reqThreshold > 0 {
					var totalSize int64
					for _, img := range input.Images {
						totalSize += int64(len(img.Image))
					}
					if totalSize > reqThreshold {
						logInput := *input
						logInput.Images = nil
						initialData.ImageEditInput = &logInput
					} else {
						initialData.ImageEditInput = input
					}
					if params != nil && int64(len(params.Mask)) > reqThreshold {
						logParams := *params
						logParams.Mask = nil
						initialData.Params = &logParams
					} else {
						initialData.Params = params
					}
				} else {
					initialData.ImageEditInput = input
					initialData.Params = params
				}
			} else {
				initialData.Params = params
			}
		case schemas.ImageVariationRequest:
			initialData.Params = req.ImageVariationRequest.Params
			input := req.ImageVariationRequest.Input
			if input != nil {
				reqThreshold, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadRequestThreshold).(int64)
				if reqThreshold > 0 && int64(len(input.Image.Image)) > reqThreshold {
					logInput := *input
					logInput.Image = schemas.ImageInput{}
					initialData.ImageVariationInput = &logInput
				} else {
					initialData.ImageVariationInput = input
				}
			}
		case schemas.VideoGenerationRequest:
			initialData.Params = req.VideoGenerationRequest.Params
			initialData.VideoGenerationInput = req.VideoGenerationRequest.Input
		case schemas.VideoEditRequest:
			initialData.Params = req.VideoEditRequest.Params
			input := req.VideoEditRequest.Input
			if input != nil {
				// An uploaded source video is the largest thing in the request by far, so drop the
				// bytes past the threshold and keep the prompt and any reference.
				reqThreshold, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadRequestThreshold).(int64)
				if reqThreshold > 0 && int64(len(input.Video.Video)) > reqThreshold {
					logInput := *input
					logInput.Video.Video = nil
					initialData.VideoEditInput = &logInput
				} else {
					initialData.VideoEditInput = input
				}
			}
		case schemas.VideoRemixRequest:
			initialData.Params = &schemas.VideoLogParams{
				VideoID: req.VideoRemixRequest.ID,
			}
			initialData.VideoGenerationInput = req.VideoRemixRequest.Input
		case schemas.VideoRetrieveRequest:
			initialData.Params = &schemas.VideoLogParams{
				VideoID: req.VideoRetrieveRequest.ID,
			}
		case schemas.VideoDownloadRequest:
			initialData.Params = &schemas.VideoLogParams{
				VideoID: req.VideoDownloadRequest.ID,
			}
		case schemas.VideoDeleteRequest:
			initialData.Params = &schemas.VideoLogParams{
				VideoID: req.VideoDeleteRequest.ID,
			}
		case schemas.PassthroughRequest, schemas.PassthroughStreamRequest:
			initialData.Params = &schemas.PassthroughLogParams{
				Method:   req.PassthroughRequest.Method,
				Path:     req.PassthroughRequest.Path,
				RawQuery: req.PassthroughRequest.RawQuery,
				Model:    req.PassthroughRequest.Model,
			}
			if len(req.PassthroughRequest.Body) > 0 {
				ct := strings.ToLower(req.PassthroughRequest.SafeHeaders["content-type"])
				if strings.Contains(ct, "application/json") {
					initialData.PassthroughRequestBody = string(req.PassthroughRequest.Body)
				}
			}
		}
	}

	// Capture configured logging headers and x-bf-lh-* headers into metadata first
	initialData.Metadata = mergeRealtimeMetadata(p.captureLoggingHeaders(ctx), ctx)

	// System entries are set after so they take precedence over dynamic header values
	if isAsync, ok := ctx.Value(schemas.BifrostIsAsyncRequest).(bool); ok && isAsync {
		if initialData.Metadata == nil {
			initialData.Metadata = make(map[string]interface{})
		}
		initialData.Metadata["isAsyncRequest"] = true
	}

	// If fallback request ID is present, use it instead of the primary request ID
	// Determine effective request ID (fallback override)
	effectiveRequestID := requestID
	var parentRequestID string
	if directParentRequestID, ok := ctx.Value(schemas.BifrostContextKeyParentRequestID).(string); ok && directParentRequestID != "" {
		parentRequestID = directParentRequestID
	}
	fallbackRequestID, ok := ctx.Value(schemas.BifrostContextKeyFallbackRequestID).(string)
	if ok && fallbackRequestID != "" {
		effectiveRequestID = fallbackRequestID
		// A fallback attempt always nests under the primary request, even when the
		// client supplied a session id via baggage. Leaving the session id here
		// would point the attempt at a string that is not a log row, so the
		// grouped log view could not collapse the chain — and if that string
		// happened to collide with a real request id, the attempt would nest under
		// an unrelated request. The session id stays on the primary row, which is
		// what the session view lists.
		parentRequestID = requestID
	}

	fallbackIndex := bifrost.GetIntFromContext(ctx, schemas.BifrostContextKeyFallbackIndex)
	// Get routing engines array
	routingEngines := []string{}
	if engines, ok := ctx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string); ok {
		routingEngines = engines
	}

	initialData.RoutingEngineUsed = routingEngines
	initialData.Status = logStatusProcessing

	// Store input data in pendingLogs for later combination with PostLLMHook output.
	// No DB write here - the write is deferred to PostLLMHook to halve total writes.
	pending := &PendingLogData{
		RequestID:          effectiveRequestID,
		ParentRequestID:    parentRequestID,
		Timestamp:          createdTimestamp,
		FallbackIndex:      fallbackIndex,
		RoutingEnginesUsed: routingEngines,
		InitialData:        initialData,
		CreatedAt:          time.Now(),
		Status:             logStatusProcessing,
	}
	// Seed LastActivity so the first idle-eviction check has a baseline even if no
	// PostLLMHook chunk has fired yet.
	pending.LastActivity.Store(pending.CreatedAt.UnixNano())
	p.pendingLogsEntries.Store(effectiveRequestID, pending)
	// Call callback synchronously for immediate UI feedback (WebSocket "processing" notification).
	// The entry does not exist in the DB yet - it will be written when PostLLMHook fires.
	p.mu.Lock()
	callback := p.logCallback
	p.mu.Unlock()
	if callback != nil {
		callback(p.ctx, buildInitialLogEntry(pending))
	}
	return req, nil, nil
}

// PostLLMHook is called after a response is received - FULLY ASYNC, NO DATABASE I/O
// Parameters:
//   - ctx: The Bifrost context
//   - result: The Bifrost response to be processed
//   - bifrostErr: The Bifrost error to be processed
//
// Returns:
//   - *schemas.BifrostResponse: The processed response
//   - *schemas.BifrostError: The processed error
//   - error: Any error that occurred during processing
func (p *LoggerPlugin) PostLLMHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if ctx == nil {
		// Log error but don't fail the request
		p.logger.Error("context is nil in PostLLMHook")
		return result, bifrostErr, nil
	}
	requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		p.logger.Error("request-id not found in context or is empty")
		return result, bifrostErr, nil
	}
	// If fallback request ID is present, use it instead of the primary request ID
	fallbackRequestID, ok := ctx.Value(schemas.BifrostContextKeyFallbackRequestID).(string)
	if ok && fallbackRequestID != "" {
		requestID = fallbackRequestID
	}
	requestType, _, originalModelRequested, resolvedModelUsed := bifrost.GetResponseFields(result, bifrostErr)
	resolvedKeyAlias := bifrost.GetResponseRoutingInfo(result, bifrostErr).ResolvedKeyAlias
	shouldStoreRaw, _ := ctx.Value(schemas.BifrostContextKeyShouldStoreRawInLogs).(bool)
	contentLoggingEnabled := p.contentLoggingEnabled(ctx)
	guardrailDebug := guardrailDebugForLog(ctx, result)
	if result != nil && guardrailDebug != nil {
		result.GetExtraFields().GuardrailDebug = guardrailDebug.Clone()
	}

	isFinalChunk := bifrost.IsFinalChunk(ctx)

	p.logger.Debug("PostLLMHook: request %s type=%q isFinalChunk=%v hasError=%v", requestID, requestType, isFinalChunk, bifrostErr != nil)

	// Retrieve pending input data from PreLLMHook
	var pendingVal any
	var hasPending bool
	if !bifrost.IsStreamRequestType(requestType) || isFinalChunk || bifrostErr != nil {
		pendingVal, hasPending = p.pendingLogsEntries.LoadAndDelete(requestID)
	} else {
		pendingVal, hasPending = p.pendingLogsEntries.Load(requestID)
	}

	p.logger.Debug("PostLLMHook: pending data lookup for request %s: found=%v", requestID, hasPending)

	if !hasPending {
		// If we have an error (e.g., cancellation/timeout), still write a minimal error entry
		// so the error is visible in logs. Without PreLLMHook's DB insert, silently returning
		// here means the error is completely lost.
		if bifrostErr != nil {
			p.logger.Warn("no pending log data found for request %s, writing minimal error entry", requestID)
			entry := &logstore.Log{
				ID:        requestID,
				Provider:  string(bifrostErr.ExtraFields.Provider),
				Status:    logStatusForError(bifrostErr),
				Object:    string(requestType),
				Stream:    bifrost.IsStreamRequestType(requestType),
				Timestamp: time.Now().UTC(),
				CreatedAt: time.Now().UTC(),
			}
			if ua := userAgentFromContext(ctx); ua != "" {
				entry.UserAgent = &ua
				if app := p.detectAppFromUserAgent(ua); app != "" {
					entry.App = &app
				}
			}
			entry.MetadataParsed = mergeRealtimeMetadata(p.captureLoggingHeaders(ctx), ctx)
			if isAsync, ok := ctx.Value(schemas.BifrostIsAsyncRequest).(bool); ok && isAsync {
				if entry.MetadataParsed == nil {
					entry.MetadataParsed = make(map[string]interface{})
				}
				entry.MetadataParsed["isAsyncRequest"] = true
			}
			applyModelAlias(entry, originalModelRequested, resolvedModelUsed)
			applyResolvedAliasInfo(entry, resolvedKeyAlias)
			entry.ErrorDetailsParsed = sanitizeErrorForLogging(bifrostErr, contentLoggingEnabled, shouldStoreRaw)
			entry.GuardrailDebugParsed = guardrailDebug
			p.applyInternalCallCosts(ctx, entry, guardrailDebug)
			if nodeID, _ := p.clusterNodeID.Load().(string); nodeID != "" {
				entry.ClusterNodeID = &nodeID
			}
			applyLargePayloadPreviewsToEntry(ctx, entry, contentLoggingEnabled)
			p.storeOrEnqueueEntry(ctx, entry, p.makePostWriteCallback(nil))
		} else {
			p.logger.Warn("no pending log data found for request %s, skipping log write", requestID)
		}
		return result, bifrostErr, nil
	}

	pending := pendingVal.(*PendingLogData)

	// Refresh the idle clock on every PostLLMHook call (notably each streaming
	// chunk) so a long-running stream is not evicted by cleanupStalePendingLogs
	// before it finishes. Safe to mutate in place: pending is a pointer held in
	// the sync.Map, and LastActivity is atomic.
	pending.LastActivity.Store(time.Now().UnixNano())

	// Should never happen, but just in case
	// Fallback to request type from pending data if request type is not set
	if requestType == "" {
		requestType = schemas.RequestType(pending.InitialData.Object)
		p.logger.Warn("PostLLMHook: request type missing from response extra fields for request %s, falling back to pre-hook value %q", requestID, requestType)
	}

	var tracer schemas.Tracer
	var traceID string
	if bifrost.IsStreamRequestType(requestType) && requestType != schemas.RealtimeRequest {
		var err error
		tracer, traceID, err = bifrost.GetTracerFromContext(ctx)
		if err != nil {
			p.logger.Debug("tracer not available in logging plugin posthook: %v", err)
			// Continue with nil tracer — the rest of the code handles this gracefully
			// via `if tracer != nil && traceID != ""` guards
		}
	}

	// For non-final streaming chunks, process the accumulator synchronously
	// and skip the write queue entirely. The accumulator work (ProcessStreamingChunk)
	// is fast (mutex + append). Only final chunks, errors, and non-streaming
	// responses need a DB write.
	if bifrost.IsStreamRequestType(requestType) && requestType != schemas.RealtimeRequest && !isFinalChunk && result != nil && bifrostErr == nil {
		if tracer != nil && traceID != "" {
			tracer.ProcessStreamingChunk(ctx, traceID, false, result, bifrostErr)
		}
		return result, bifrostErr, nil
	}

	// Governance/key/prompt fields, needed only by the final/error/non-streaming
	// paths below (applyOutputFieldsToEntry). Read here, after the non-final-chunk
	// fast path, so intermediate chunks skip these ~19 locked context lookups.
	selectedKeyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyID)
	selectedKeyName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyName)
	virtualKeyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	virtualKeyName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyName)
	routingRuleID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceRoutingRuleID)
	routingRuleName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceRoutingRuleName)
	selectedPromptName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedPromptName)
	selectedPromptVersion := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedPromptVersion)
	selectedPromptID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedPromptID)
	teamID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceTeamID)
	teamName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceTeamName)
	customerID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceCustomerID)
	customerName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceCustomerName)
	userID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserID)
	userName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserName)
	businessUnitID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceBusinessUnitID)
	businessUnitName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceBusinessUnitName)
	numberOfRetries := bifrost.GetIntFromContext(ctx, schemas.BifrostContextKeyNumberOfRetries)
	attemptTrail, _ := ctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)

	// Extract routing engine logs from context before entering goroutine
	routingEngineLogs := formatRoutingEngineLogs(ctx.GetRoutingEngineLogs())
	if requestType == schemas.RealtimeRequest {
		if resolvedRealtimeSessionID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRealtimeSessionID); resolvedRealtimeSessionID != "" {
			pending.ParentRequestID = resolvedRealtimeSessionID
		}
		pending.InitialData.Metadata = mergeRealtimeMetadata(pending.InitialData.Metadata, ctx)
		if routingEngines, ok := ctx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string); ok {
			pending.InitialData.RoutingEngineUsed = routingEngines
			pending.RoutingEnginesUsed = routingEngines
		}
	}

	// Build the complete log entry with input (from PreLLMHook) + output (from PostLLMHook)
	entry := buildCompleteLogEntryFromPending(pending)
	entry.GuardrailDebugParsed = guardrailDebug
	// Apply common output fields. For cache hits, prefer the cache-serve
	// latency stamped by the semantic cache plugin over the original provider
	// latency preserved in the cached response.
	var latency int64
	var upstreamLatency, overheadLatency *int64
	if result != nil {
		ef := result.GetExtraFields()
		latency = ef.Latency
		upstreamLatency = ef.UpstreamLatency
		overheadLatency = ef.OverheadLatency
		// Model that actually served the turn when the provider swapped models inside
		// one call. entry.Model still names what the caller asked for.
		if ef.RoutingInfo.ServerSideFallbackModel != nil {
			served := *ef.RoutingInfo.ServerSideFallbackModel
			entry.ServerSideFallbackModel = &served
		}
		if ef.CacheDebug != nil && ef.CacheDebug.CacheHit && ef.CacheDebug.CacheHitLatency != nil {
			latency = *ef.CacheDebug.CacheHitLatency
		}
	} else if bifrostErr != nil {
		latency = bifrostErr.ExtraFields.Latency
	}

	if entry.ServerSideFallbackModel == nil && bifrostErr != nil && bifrostErr.ExtraFields.BilledUsage != nil {
		if served := bifrostErr.ExtraFields.BilledUsage.ServerSideFallbackModel; served != nil {
			m := *served
			entry.ServerSideFallbackModel = &m
		}
	}
	applyOutputFieldsToEntry(entry, selectedKeyID, selectedKeyName, virtualKeyID, virtualKeyName, routingRuleID, routingRuleName, selectedPromptID, selectedPromptName, selectedPromptVersion, teamID, teamName, customerID, customerName, userID, userName, businessUnitID, businessUnitName, numberOfRetries, latency, upstreamLatency, overheadLatency, attemptTrail)
	applyResolvedAliasInfo(entry, resolvedKeyAlias)
	// Attach cluster governance metadata for disconnected node usage recovery
	if nodeID, _ := p.clusterNodeID.Load().(string); nodeID != "" {
		entry.ClusterNodeID = &nodeID
	}
	if budgetIDs, ok := ctx.Value(schemas.BifrostContextKeyGovernanceBudgetIDs).([]string); ok && len(budgetIDs) > 0 {
		entry.BudgetIDsParsed = budgetIDs
	}
	if rateLimitIDs, ok := ctx.Value(schemas.BifrostContextKeyGovernanceRateLimitIDs).([]string); ok && len(rateLimitIDs) > 0 {
		entry.RateLimitIDsParsed = rateLimitIDs
	}
	if teamIDs, ok := ctx.Value(schemas.BifrostContextKeyGovernanceTeamIDs).([]string); ok && len(teamIDs) > 0 {
		entry.TeamIDsParsed = teamIDs
	}
	if teamNames, ok := ctx.Value(schemas.BifrostContextKeyGovernanceTeamNames).([]string); ok && len(teamNames) > 0 {
		entry.TeamNamesParsed = teamNames
	}
	if buIDs, ok := ctx.Value(schemas.BifrostContextKeyGovernanceBusinessUnitIDs).([]string); ok && len(buIDs) > 0 {
		entry.BusinessUnitIDsParsed = buIDs
	}
	if buNames, ok := ctx.Value(schemas.BifrostContextKeyGovernanceBusinessUnitNames).([]string); ok && len(buNames) > 0 {
		entry.BusinessUnitNamesParsed = buNames
	}
	if customerIDs, ok := ctx.Value(schemas.BifrostContextKeyGovernanceCustomerIDs).([]string); ok && len(customerIDs) > 0 {
		entry.CustomerIDsParsed = customerIDs
	}
	if customerNames, ok := ctx.Value(schemas.BifrostContextKeyGovernanceCustomerNames).([]string); ok && len(customerNames) > 0 {
		entry.CustomerNamesParsed = customerNames
	}
	entry.MetadataParsed = pending.InitialData.Metadata
	entry.MetadataParsed = mergeRealtimeMetadata(entry.MetadataParsed, ctx)
	entry.RoutingEngineLogs = routingEngineLogs

	// Branch based on response type to populate output-specific fields

	// Path A: Error with nil result
	if result == nil && bifrostErr != nil {
		entry.Status = logStatusForError(bifrostErr)
		applyModelAlias(entry, originalModelRequested, resolvedModelUsed)
		if bifrost.IsStreamRequestType(requestType) {
			entry.Stream = true
		}

		// For streaming errors, finalize and read accumulated chunks so logs retain pre-error stream metadata
		if bifrost.IsStreamRequestType(requestType) &&
			requestType != schemas.RealtimeRequest &&
			tracer != nil &&
			traceID != "" {
			if accResult := tracer.ProcessStreamingChunk(ctx, traceID, true, result, bifrostErr); accResult != nil {
				if streamResponse := convertToProcessedStreamResponse(accResult, requestType); streamResponse != nil {
					p.applyStreamingOutputToEntry(entry, streamResponse, shouldStoreRaw, contentLoggingEnabled)
				}
			}
			tracer.CleanupStreamAccumulator(traceID)
		}

		entry.ErrorDetailsParsed = sanitizeErrorForLogging(bifrostErr, contentLoggingEnabled, shouldStoreRaw)
		if shouldStoreRaw && contentLoggingEnabled {
			if bifrostErr.ExtraFields.RawRequest != nil {
				rawReqBytes, err := sonic.Marshal(bifrostErr.ExtraFields.RawRequest)
				if err == nil {
					entry.RawRequest = string(rawReqBytes)
				}
			}

			if entry.RawResponse == "" && bifrostErr.ExtraFields.RawResponse != nil {
				rawRespBytes, err := sonic.Marshal(bifrostErr.ExtraFields.RawResponse)
				if err == nil {
					entry.RawResponse = string(rawRespBytes)
				}
			}
		}
		// The request failed/was cancelled but the provider still
		// processed tokens (carried on BilledUsage). Record cost + tokens so the
		// logs DB reflects what we were actually billed, mirroring the governance
		// budget.
		p.applyErrorBillingFromBilledUsage(ctx, entry, bifrostErr.ExtraFields.BilledUsage, requestType)
		p.applyInternalCallCosts(ctx, entry, guardrailDebug)
		applyLargePayloadPreviewsToEntry(ctx, entry, contentLoggingEnabled)
		p.storeOrEnqueueEntry(ctx, entry, p.makePostWriteCallback(nil))
		p.scheduleDeferredUsageUpdate(ctx, requestID, entry.TokenUsageParsed != nil)
		return result, bifrostErr, nil
	}

	// Path B: Streaming final chunk
	if bifrost.IsStreamRequestType(requestType) && requestType != schemas.RealtimeRequest {
		var streamResponse *streaming.ProcessedStreamResponse
		if tracer != nil && traceID != "" {
			accResult := tracer.ProcessStreamingChunk(ctx, traceID, isFinalChunk, result, bifrostErr)
			if accResult != nil {
				streamResponse = convertToProcessedStreamResponse(accResult, requestType)
			}
		}

		if bifrostErr != nil {
			entry.Status = logStatusForError(bifrostErr)
			entry.Stream = true
			applyModelAlias(entry, originalModelRequested, resolvedModelUsed)
			entry.ErrorDetailsParsed = sanitizeErrorForLogging(bifrostErr, contentLoggingEnabled, shouldStoreRaw)
			// Backfill raw request/response on streaming-error path so cancellation/timeout
			// log entries still carry raw payloads when content logging + raw storage are
			// enabled. Mirrors the non-streaming Path A pattern at line 872. Prefer the
			// accumulator-captured raw bytes (streamResponse), then fall back to whatever
			// the provider attached to the BifrostError.
			if shouldStoreRaw && contentLoggingEnabled {
				if entry.RawRequest == "" {
					if streamResponse != nil && streamResponse.RawRequest != nil && *streamResponse.RawRequest != nil {
						switch raw := (*streamResponse.RawRequest).(type) {
						case string:
							entry.RawRequest = raw
						default:
							if rawReqBytes, err := sonic.Marshal(raw); err == nil {
								entry.RawRequest = string(rawReqBytes)
							}
						}
					} else if bifrostErr.ExtraFields.RawRequest != nil {
						if rawReqBytes, err := sonic.Marshal(bifrostErr.ExtraFields.RawRequest); err == nil {
							entry.RawRequest = string(rawReqBytes)
						}
					}
				}
				if entry.RawResponse == "" {
					if streamResponse != nil && streamResponse.Data != nil && streamResponse.Data.RawResponse != nil {
						entry.RawResponse = *streamResponse.Data.RawResponse
					} else if bifrostErr.ExtraFields.RawResponse != nil {
						if rawRespBytes, err := sonic.Marshal(bifrostErr.ExtraFields.RawResponse); err == nil {
							entry.RawResponse = string(rawRespBytes)
						}
					}
				}
			}
			// A stream error can arrive with a response chunk, bypassing Path A.
			// Preserve provider-billed usage and the sidecar calls in that case.
			p.applyErrorBillingFromBilledUsage(ctx, entry, bifrostErr.ExtraFields.BilledUsage, requestType)
			p.applyInternalCallCosts(ctx, entry, guardrailDebug)
		} else if streamResponse == nil {
			// tracer or traceID not available, or accumulator returned nil - still write what we have
			entry.Status = logStatusSuccess
			entry.Stream = true
			applyModelAlias(entry, originalModelRequested, resolvedModelUsed)
			// Without an accumulated response, CalculateCost cannot see cache or
			// guardrail debug. Normal accumulated streams already include both.
			p.applyInternalCallCosts(ctx, entry, guardrailDebug)
		} else if isFinalChunk {
			// Apply streaming output fields to the entry
			entry.Stream = true
			p.applyStreamingOutputToEntry(entry, streamResponse, shouldStoreRaw, contentLoggingEnabled)
			// Read off the raw final-chunk ExtraFields, not the rebuilt streamResponse.
			if result != nil {
				applyUpstreamOverheadToEntry(entry, result.GetExtraFields())
			}
		}
		if entry.ErrorDetailsParsed != nil {
			entry.Status = logStatusForError(entry.ErrorDetailsParsed)
		}
		// Backfill passthrough status_code from response (streaming path)
		if result != nil && result.PassthroughResponse != nil {
			if params, ok := entry.ParamsParsed.(*schemas.PassthroughLogParams); ok {
				params.StatusCode = result.PassthroughResponse.StatusCode
			}
			if contentLoggingEnabled && len(result.PassthroughResponse.Body) > 0 {
				entry.PassthroughResponseBody = string(result.PassthroughResponse.Body)
			}
			// Flip status for passthrough error responses (4xx/5xx from provider)
			if isPassthroughErrorResponse(result) {
				entry.Status = logStatusError
			}
			// Compute cost for streaming passthrough using StreamUsage set by the accumulator.
			if entry.Cost == nil && p.pricingManager != nil && result.PassthroughResponse.PassthroughUsage != nil {
				pricingScopes := modelcatalog.PricingLookupScopesFromContext(ctx, string(entry.Provider))
				if cost := p.pricingManager.CalculateCost(result, pricingScopes); cost > 0 {
					entry.Cost = &cost
				}
			}
		}
		applyLargePayloadPreviewsToEntry(ctx, entry, contentLoggingEnabled)
		if tracer != nil && traceID != "" {
			tracer.CleanupStreamAccumulator(traceID)
		}
		// Attach the per-category cost split to the accumulated stream usage so
		// log detail views can surface input / output / cache costs.
		p.attachCostBreakdown(ctx, entry, result)
		p.storeOrEnqueueEntry(ctx, entry, p.makePostWriteCallback(nil))
		p.scheduleDeferredUsageUpdate(ctx, requestID, entry.TokenUsageParsed != nil)
		return result, bifrostErr, nil
	}

	// Path C: Non-streaming response
	if bifrostErr != nil {
		entry.Status = logStatusForError(bifrostErr)
		applyModelAlias(entry, originalModelRequested, resolvedModelUsed)
		entry.ErrorDetailsParsed = sanitizeErrorForLogging(bifrostErr, contentLoggingEnabled, shouldStoreRaw)
		// Realtime turns that fail mid-stream still need their input transcript
		// surfaced — backfill from bifrostErr.ExtraFields.RawRequest if present.
		if requestType == schemas.RealtimeRequest {
			applyRealtimeRawRequestBackfill(entry, bifrostErr.ExtraFields.RawRequest, contentLoggingEnabled, shouldStoreRaw)
		}
	} else if result != nil {
		entry.Status = logStatusSuccess
		extraFields := result.GetExtraFields()
		applyModelAlias(entry, extraFields.OriginalModelRequested, extraFields.ResolvedModelUsed)
		if requestType == schemas.RealtimeRequest {
			p.applyRealtimeOutputToEntry(entry, result, shouldStoreRaw, contentLoggingEnabled)
		} else {
			p.applyNonStreamingOutputToEntry(entry, result, shouldStoreRaw, contentLoggingEnabled)
		}
		// Flip status for passthrough error responses (4xx/5xx from provider)
		if isPassthroughErrorResponse(result) {
			entry.Status = logStatusError
		}
	}
	applyLargePayloadPreviewsToEntry(ctx, entry, contentLoggingEnabled)
	if bifrostErr == nil && (requestType == schemas.BatchCreateRequest || requestType == schemas.BatchRetrieveRequest) {
		p.recordBatchJobLifecycle(entry, result)
	}

	// Calculate cost
	var cacheDebug *schemas.BifrostCacheDebug
	if result != nil {
		cacheDebug = result.GetExtraFields().CacheDebug
	}
	entry.CacheDebugParsed = cacheDebug
	if p.pricingManager != nil {
		pricingScopes := modelcatalog.PricingLookupScopesFromContext(ctx, string(entry.Provider))
		if breakdown := p.pricingManager.CalculateCostBreakdown(result, pricingScopes); breakdown != nil && breakdown.TotalCost > 0 {
			cost := breakdown.TotalCost
			entry.Cost = &cost
			// Attach the per-category split (input / output / cache) to the
			// stored usage so log detail views can surface it. Preserve any
			// provider-supplied breakdown.
			if entry.TokenUsageParsed != nil && entry.TokenUsageParsed.Cost == nil {
				entry.TokenUsageParsed.Cost = breakdown
			} else if entry.TokenUsageParsed == nil {
				// No usage carrier: OCRUsageInfo has no tokens, so OCR is never
				// aliased into TokenUsageParsed. SerializeFields skips its cost
				// block when TokenUsageParsed is nil, so denormalize the split
				// directly here for the columns to reconcile to the cost column.
				entry.InputCost = breakdown.InputCost
				entry.OutputCost = breakdown.OutputCost
				entry.AdditionalCost = breakdown.AdditionalCost
			}
		}
		if bifrostErr == nil &&
			requestType == schemas.BatchResultsRequest &&
			result != nil &&
			result.BatchResultsResponse != nil {
			p.accountBatchResults(entry, result, pricingScopes)
		}
	}

	// Pre-apply denormalized fields for WebSocket callback enrichment
	if entry.SelectedKeyID != "" && entry.SelectedKeyName != "" {
		entry.SelectedKey = &schemas.Key{
			ID:   entry.SelectedKeyID,
			Name: entry.SelectedKeyName,
		}
	}
	if entry.VirtualKeyID != nil && entry.VirtualKeyName != nil && *entry.VirtualKeyID != "" && *entry.VirtualKeyName != "" {
		entry.VirtualKey = &tables.TableVirtualKey{
			ID:   *entry.VirtualKeyID,
			Name: *entry.VirtualKeyName,
		}
	}
	if entry.RoutingRuleID != nil && entry.RoutingRuleName != nil && *entry.RoutingRuleID != "" && *entry.RoutingRuleName != "" {
		entry.RoutingRule = &tables.TableRoutingRule{
			ID:   *entry.RoutingRuleID,
			Name: *entry.RoutingRuleName,
		}
	}
	p.storeOrEnqueueEntry(ctx, entry, p.makePostWriteCallback(nil))
	p.scheduleDeferredUsageUpdate(ctx, requestID, entry.TokenUsageParsed != nil)
	return result, bifrostErr, nil
}

// Cleanup is called when the plugin is being shut down. It stops the
// batchWriter goroutine before it issues any further DB writes, takes over
// ownership of the write queue, and drains whatever is pending under a
// bounded wall-clock deadline (cleanupDrainTimeout). Any entries that do not
// finish within the deadline are dropped so that a slow or wedged log store
// cannot wedge the server's overall 30s shutdown budget.
func (p *LoggerPlugin) Cleanup() error {
	p.cleanupOnce.Do(func() {
		p.mu.Lock()
		// Under the same lock as the sweeper cancel, and before anything below can
		// reach p.wg.Wait(): StartBatchAccountingSweeper reads this flag while holding
		// p.mu, so once it is set no further wg.Add can slip in behind the Wait.
		p.closed.Store(true)
		if p.batchSweeperCancel != nil {
			p.batchSweeperCancel()
			p.batchSweeperCancel = nil
		}
		p.mu.Unlock()
		if p.cleanupTicker != nil {
			p.cleanupTicker.Stop()
		}
		// Signal the cleanup worker to stop.
		close(p.done)
		// p.closed was already set above (it doubles as the sweeper-start guard),
		// which is what stops new producers before we kill batchWriter so the queue
		// does not grow while we drain it. Any producer that raced past that check is
		// absorbed by the enqueue recover path.
		// Kill batchWriter. Its current in-memory batch is handed back via
		// p.recoveredBatch; it does not issue any further DB writes.
		p.batchCancel()
		// Receiving from batchWriterDone is the ownership handoff: after this
		// point, no other goroutine reads from p.writeQueue, so we can drain
		// it ourselves. This wait is microseconds (no DB work involved).
		<-p.batchWriterDone
		// Drain p.recoveredBatch and whatever is still buffered in
		// p.writeQueue under a bounded deadline.
		p.drainPending()
		// Close the channel as hygiene. The defer/recover in enqueueLogEntry
		// (writer.go:254-259) absorbs any racing producer send.
		close(p.writeQueue)
		// wg.Wait covers the cleanupWorker (exited via close(p.done)) and
		// any in-flight deferred usage updater goroutines. batchWriter has
		// already called wg.Done before closing batchWriterDone above.
		p.wg.Wait()
	})
	return nil
}

// drainPending processes p.recoveredBatch followed by any entries still
// buffered in p.writeQueue. Runs synchronously under a wall-clock deadline;
// remaining entries past the deadline are counted as dropped.
func (p *LoggerPlugin) drainPending() {
	deadline := time.Now().Add(cleanupDrainTimeout)
	batch := p.recoveredBatch
	p.recoveredBatch = nil

	// Pull everything currently buffered in the channel. Non-blocking — we
	// only want what is there right now; new sends are already blocked by
	// p.closed.
drainQueue:
	for {
		select {
		case entry := <-p.writeQueue:
			batch = append(batch, entry)
		default:
			break drainQueue
		}
	}

	// Process in chunks of writerConfig.MaxBatchSize, checking the wall-clock deadline
	// between chunks so a single slow processBatch cannot consume the whole
	// budget and starve later chunks.
	for len(batch) > 0 {
		if time.Now().After(deadline) {
			p.droppedRequests.Add(int64(len(batch)))
			p.logger.Warn("logging plugin cleanup deadline reached; dropping %d entries", len(batch))
			return
		}
		chunkSize := p.writerConfig.MaxBatchSize
		if chunkSize > len(batch) {
			chunkSize = len(batch)
		}
		p.safeProcessBatch(batch[:chunkSize])
		batch = batch[chunkSize:]
	}
}

// storeOrEnqueueEntry stores a log entry in pendingLogs keyed by traceID for later
// retrieval by Inject(), or enqueues directly if no traceID is available (Go SDK path).
// Multiple entries per traceID are supported (e.g. fallback/retry attempts within the same trace).
func (p *LoggerPlugin) storeOrEnqueueEntry(ctx *schemas.BifrostContext, entry *logstore.Log, callback func(entry *logstore.Log)) {
	policy := p.resolveContentPolicy(ctx)
	// ContentHidden marks entries whose content the API/UI never serves back —
	// both the retained-in-object-storage case and the dropped-entirely case.
	entry.ContentHidden = !policy.visible()
	// Redaction mappings exist to reveal redacted content on permitted UI
	// reads; hidden entries serve no content back, so attach only when the
	// content is actually visible.
	attachLogRedactionData(ctx, entry, policy.visible())
	traceID, _ := ctx.Value(schemas.BifrostContextKeyTraceID).(string)
	if traceID != "" {
		// Append to slice for Inject() to pick up — supports multiple attempts per trace
		existing, loaded := p.pendingLogsToInject.LoadOrStore(traceID, &pendingInjectEntries{entries: []*logstore.Log{entry}, createdAt: time.Now()})
		if !loaded {
			return
		}
		pending := existing.(*pendingInjectEntries)
		pending.mu.Lock()
		pending.entries = append(pending.entries, entry)
		pending.mu.Unlock()
	} else {
		// Fallback: no tracing (Go SDK path), enqueue directly
		p.enqueueLogEntry(entry, callback)
	}
}

// ConsumesOverheadSpans opts this plugin into receiving the internal overhead-breakdown
// spans (implements schemas.OverheadSpanConsumer). computeOverheadBreakdown needs them;
// every other connector gets a trace with those spans stripped.
func (p *LoggerPlugin) ConsumesOverheadSpans() bool { return true }

// Inject receives a completed trace and writes the log entries with plugin logs to DB.
// This implements the ObservabilityPlugin interface.
func (p *LoggerPlugin) Inject(_ context.Context, trace *schemas.Trace) error {
	if trace == nil {
		return nil
	}
	// Retrieve pending log entries built by PostLLMHook. They are keyed by the
	// trace's store key (BifrostContextKeyTraceID carries it), which is
	// trace.InternalID — unique per request even when concurrent requests share
	// an inherited W3C TraceID. Fall back to TraceID for traces created by
	// paths that predate InternalID.
	joinKey := trace.InternalID
	if joinKey == "" {
		joinKey = trace.TraceID
	}
	entryVal, ok := p.pendingLogsToInject.LoadAndDelete(joinKey)
	if !ok {
		return nil
	}
	pending, ok := entryVal.(*pendingInjectEntries)
	if !ok {
		return nil
	}
	// Serialize plugin logs once for all entries
	pluginLogsJSON := serializePluginLogs(trace.PluginLogs)
	// Backfill upstream/overhead from the authoritative values the tracer stamped on
	// the root span at completion, so the log matches the trace connectors exactly.
	// Supersedes the mid-request PostLLMHook estimate. Only overwrite when present.
	var upstreamMs, overheadMs float64
	var upOK, ovOK bool
	if trace.RootSpan != nil && trace.RootSpan.Attributes != nil {
		upstreamMs, upOK = traceAttrFloatMs(trace.RootSpan.Attributes, schemas.AttrBifrostUpstreamDurationMs)
		overheadMs, ovOK = traceAttrFloatMs(trace.RootSpan.Attributes, schemas.AttrBifrostOverheadDurationMs)
	}
	// Per-span self-time decomposition of overhead, attached to the same terminal
	// row that receives the overhead number below.
	overheadBreakdown, measuredOverheadMs, isStreaming := computeOverheadBreakdown(trace, overheadMs, ovOK, upstreamMs, upOK)

	p.logger.Debug("Inject: enqueuing %d log entries", len(pending.entries))
	// Upstream/overhead are request-level: put them on one row per trace, not all.
	// A trace can log many rows (list_models fans out per provider; fallbacks add a
	// row per attempt), and stamping every one would count the same overhead N times
	// in the graphs. Clear the rest, keeping their per-row latency.
	//
	// Pick the target row deterministically (latest Timestamp, tie-broken by ID)
	// rather than by slice position: list_models fans out concurrently under one
	// trace, so entries append in nondeterministic completion order and a positional
	// "last" would attach trace latency to a random provider row. For sequential
	// fallbacks the latest Timestamp is still the terminal attempt.
	stampIdx := 0
	for i, entry := range pending.entries {
		best := pending.entries[stampIdx]
		if entry.Timestamp.After(best.Timestamp) ||
			(entry.Timestamp.Equal(best.Timestamp) && entry.ID > best.ID) {
			stampIdx = i
		}
	}
	for i, entry := range pending.entries {
		entry.PluginLogs = pluginLogsJSON
		if i == stampIdx {
			// Clear both provisional components before backfilling. A partial root
			// span (only one attribute present) would otherwise leave the other as
			// a stale PostLLMHook estimate, mixing the breakdown's two sources.
			if upOK || ovOK {
				entry.UpstreamLatency = nil
				entry.OverheadLatency = nil
			}
			if isStreaming && upOK && ovOK {
				// Streaming: overhead is the measured Bifrost CPU (the breakdown buckets),
				// not total-upstream. The remainder (total - upstream - measured) is the
				// off-CPU relay/scheduler wait the request goroutine spends parked between
				// provider chunks — not Bifrost work — so it is folded into upstream. This
				// makes the overhead number reflect actual Bifrost cost while keeping
				// latency = upstream + overhead and the breakdown buckets summing to overhead.
				total := upstreamMs + overheadMs
				measured := measuredOverheadMs
				if measured > overheadMs {
					measured = overheadMs // measurement skew: never exceed total-upstream
				}
				if measured < 0 {
					measured = 0
				}
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
				// Latency = full-request wall-clock = upstream + overhead. Summing (not
				// the raw span duration) keeps latency >= upstream when overhead clamps
				// to zero, so the breakdown always adds up.
				if upOK && ovOK {
					total := upstreamMs + overheadMs
					entry.Latency = &total
				}
			}
			if len(overheadBreakdown) > 0 {
				entry.OverheadBreakdownParsed = overheadBreakdown
			}
		} else if upOK || ovOK {
			entry.UpstreamLatency = nil
			entry.OverheadLatency = nil
		}
		p.logger.Debug("Inject: enqueuing log entry %s", entry.ID)
		p.enqueueLogEntry(entry, p.makePostWriteCallback(nil))
	}
	return nil
}

// serializePluginLogs groups plugin logs by plugin name for persistence and UI rendering.
func serializePluginLogs(logs []schemas.PluginLogEntry) string {
	if len(logs) == 0 {
		return ""
	}
	data, err := sonic.Marshal(schemas.GroupPluginLogsByName(logs))
	if err != nil {
		return ""
	}
	return string(data)
}

// spanWall is a span's wall-clock duration, guarding against unfinished spans
// (zero or non-monotonic EndTime) which would otherwise read as huge negatives.
func spanWall(s *schemas.Span) time.Duration {
	if s.EndTime.IsZero() || !s.EndTime.After(s.StartTime) {
		return 0
	}
	return s.EndTime.Sub(s.StartTime)
}

// spanOverlap is the duration of child that falls inside parent's time window. For a
// genuinely nested child this equals the child's wall duration, so self-time is
// unchanged; for a child re-parented by ID but running outside the parent (a
// sequential sibling in wall-clock terms) it is zero. Guards unfinished spans.
func spanOverlap(parent, child *schemas.Span) time.Duration {
	if parent.EndTime.IsZero() || !parent.EndTime.After(parent.StartTime) {
		return 0
	}
	if child.EndTime.IsZero() || !child.EndTime.After(child.StartTime) {
		return 0
	}
	start := child.StartTime
	if parent.StartTime.After(start) {
		start = parent.StartTime
	}
	end := child.EndTime
	if parent.EndTime.Before(end) {
		end = parent.EndTime
	}
	if !end.After(start) {
		return 0
	}
	return end.Sub(start)
}

// isOverheadSpanKind reports whether a span's self-time counts as attributable
// Bifrost overhead. Only spans that tightly bracket Bifrost's own code qualify:
// plugin hooks and internal operations (key.selection, etc). Deliberately excluded:
//   - llm.call/retry/fallback and the media provider kinds: the upstream side.
//   - the root http.request span: its self-time is the glue between child spans,
//     which for streaming also contains response-body socket reads that happen
//     outside any child span. That time is real upstream (and is already in the
//     upstream accumulator), so counting it here would double-report it as overhead.
//
// Excluded spans still subtract from their parent's self-time via childDur, so their
// time is removed from any bucket rather than mislabeled. The gap between the summed
// buckets and the stamped overhead number is surfaced as the reconciliation line.
func isOverheadSpanKind(kind schemas.SpanKind) bool {
	switch kind {
	case schemas.SpanKindPlugin, schemas.SpanKindInternal:
		return true
	default:
		return false
	}
}

// overheadBucketName maps an overhead-side span to its breakdown bucket.
func overheadBucketName(s *schemas.Span) string {
	if s.Kind == schemas.SpanKindPlugin {
		// plugin.<name>.<phase> -> plugin.<name>, collapsing the hook phases.
		n := strings.TrimPrefix(s.Name, "plugin.")
		if i := strings.LastIndex(n, "."); i > 0 {
			n = n[:i]
		}
		return "plugin." + n
	}
	return s.Name // key.selection and other internal spans keep their name
}

// computeOverheadBreakdown decomposes Bifrost overhead across spans by self-time:
// each span's own wall duration minus the wall duration of its direct children.
// Self-times across the tree are non-overlapping and sum to the root duration, so
// summing the overhead-side buckets is an independent measure of overhead that does
// not depend on the upstream socket accumulator. Only overhead-side spans produce a
// bucket; provider/upstream spans still subtract from their parent's self-time.
//
// The remaining overhead (the stamped total, minus the measured plugin/internal
// self-time) is attributed to a final "core" bucket: transport parsing, routing, and
// request/response marshalling that Bifrost does outside any plugin span. It is
// derived from overheadMs (which already excludes upstream), not from the root
// span's self-time, so it never picks up streaming socket reads. Buckets are
// returned with microsecond values, measured spans first (chronological) then core.
// computeOverheadBreakdown returns the per-phase buckets, the measured Bifrost-CPU
// total in ms (the sum of those buckets), and whether this was a streaming request.
// For streams the caller uses measuredMs as the overhead (see Inject): total-upstream
// over-counts stream overhead because it includes off-CPU relay/scheduler wait between
// chunks, which is not Bifrost work.
func computeOverheadBreakdown(trace *schemas.Trace, overheadMs float64, overheadOK bool, upstreamMs float64, upstreamOK bool) ([]logstore.OverheadBucket, float64, bool) {
	if trace == nil || len(trace.Spans) == 0 {
		return nil, 0, false
	}
	// Sum direct-children time per parent, over ALL spans (upstream ones too), so
	// excluded child spans are still removed from their parent's self-time. Only the
	// portion of a child that temporally OVERLAPS its parent counts: a child
	// re-parented for trace-hierarchy reasons but running outside the parent's window
	// (e.g. llm.call is linked under key.selection but starts after it ends) then
	// correctly subtracts nothing, instead of driving the parent's self-time negative.
	spanByID := make(map[string]*schemas.Span, len(trace.Spans))
	for _, s := range trace.Spans {
		if s != nil && s.SpanID != "" {
			spanByID[s.SpanID] = s
		}
	}
	childDur := make(map[string]time.Duration, len(trace.Spans))
	for _, s := range trace.Spans {
		if s == nil || s.ParentID == "" {
			continue
		}
		parent := spanByID[s.ParentID]
		if parent == nil {
			continue
		}
		childDur[s.ParentID] += spanOverlap(parent, s)
	}

	type agg struct {
		dur   time.Duration
		kind  schemas.SpanKind
		first time.Time
	}
	buckets := make(map[string]*agg)
	for _, s := range trace.Spans {
		if s == nil || !isOverheadSpanKind(s.Kind) {
			continue
		}
		self := spanWall(s) - childDur[s.SpanID]
		if self <= 0 {
			continue
		}
		name := overheadBucketName(s)
		b := buckets[name]
		if b == nil {
			b = &agg{kind: s.Kind, first: s.StartTime}
			buckets[name] = b
		}
		b.dur += self
		if s.StartTime.Before(b.first) {
			b.first = s.StartTime
		}
	}

	// Streaming runs no per-chunk spans: the relay loop's JSON decode, struct->unified
	// mapping, and downstream-backpressure stall are stamped as root-span attributes at
	// stream end. Fold them into the same buckets as their unary equivalents (decode ->
	// response-parse/Serialization, mapping -> convertor/Convertor) so a stream's numbers
	// read like a unary request's. Backpressure has no unary twin and is not Bifrost CPU,
	// so it gets its own bucket. Seeding the map here means measuredNs and core pick them
	// up on the existing path, with no separate bookkeeping.
	if trace.RootSpan != nil {
		attrs := trace.RootSpan.Attributes
		addStreamBucketMs := func(name string, ms float64) {
			if ms <= 0 {
				return
			}
			b := buckets[name]
			if b == nil {
				b = &agg{kind: schemas.SpanKindInternal, first: trace.RootSpan.StartTime}
				buckets[name] = b
			}
			b.dur += time.Duration(ms * float64(time.Millisecond))
		}
		if ms, ok := traceAttrFloatMs(attrs, schemas.AttrBifrostStreamParseMs); ok {
			addStreamBucketMs("response-parse", ms)
		}
		// Inbound per-chunk mapping (provider->Bifrost) is conversion work: it belongs
		// in the Convertor category, as its own member so the stream split is visible.
		if ms, ok := traceAttrFloatMs(attrs, schemas.AttrBifrostStreamConvertMs); ok {
			addStreamBucketMs("convertor.stream-in", ms)
		}
		// Backpressure is the provider-side downstream wait that IS in the overhead
		// total. Split it into (A) client-write vs (B) transport CPU using the transport
		// goroutine's concurrent measurements as weights (those run in parallel and are
		// not themselves in the total, so they weight rather than add). The (B) share is
		// the outbound per-chunk mapping (Bifrost->client) -- also conversion work, so it
		// joins the Convertor category as the outbound member. The (A) share is the client
		// socket write and stays its own bucket. No transport timing (raw passthrough, or a
		// client that disconnected) falls back to a single undifferentiated bucket.
		if bp, ok := traceAttrFloatMs(attrs, schemas.AttrBifrostStreamBackpressureMs); ok && bp > 0 {
			cpuMs, _ := traceAttrFloatMs(attrs, schemas.AttrBifrostStreamTransportCPUMs)
			writeMs, _ := traceAttrFloatMs(attrs, schemas.AttrBifrostStreamClientWriteMs)
			if total := cpuMs + writeMs; total > 0 {
				addStreamBucketMs("stream-client-write", bp*writeMs/total)
				addStreamBucketMs("convertor.stream-out", bp*cpuMs/total)
			} else {
				addStreamBucketMs("stream-backpressure", bp)
			}
		}
		// Worker->caller goroutine-hop latency (unary path): scheduling wall-time
		// inside the overhead window that sits on no span. Carve it into its own
		// bucket so it stops inflating "core". The reverse hop is the queue-wait span.
		if ms, ok := traceAttrFloatMs(attrs, schemas.AttrBifrostWorkerHandoffMs); ok {
			addStreamBucketMs("worker-handoff", ms)
		}
	}

	// Provider-agnostic catch-all. Every provider call runs inside an llm.call span
	// (SpanKindLLMCall), which is not itself a bucket but envelops the upstream network
	// call plus ALL provider-side glue: request conversion/marshal/signing, response
	// read/decompress/parse, header and endpoint work. Its self-time (wall minus the
	// child phase spans) is therefore exactly upstream + whatever provider work no phase
	// span captured. Subtracting the measured upstream leaves that uncaptured remainder,
	// surfaced as "provider-internal" so a brand-new provider — or an unspanned step in
	// an existing one — can never silently inflate "core"; it lands here instead, and its
	// size tells us a provider needs finer spans. Summed across attempts: retries create
	// one llm.call span each, and upstream latency likewise accumulates across them.
	//
	// STREAMING IS EXCLUDED. For a streamed response the llm.call span is DEFERRED — it
	// covers the entire stream (ended on the final chunk), not just setup — while upstream
	// is only time-to-first-byte. So llm.call self - upstream would capture the whole
	// per-chunk relay, which is instead decomposed by the stream phases above
	// (response-parse / convertor / backpressure via the stream accumulator). Computing
	// provider-internal there would double-count that work and mislabel it. Detect
	// streaming by the presence of any stream-overhead attribute on the root span.
	isStreaming := false
	if trace.RootSpan != nil && trace.RootSpan.Attributes != nil {
		a := trace.RootSpan.Attributes
		for _, k := range []string{schemas.AttrBifrostStreamParseMs, schemas.AttrBifrostStreamConvertMs, schemas.AttrBifrostStreamBackpressureMs} {
			if _, ok := a[k]; ok {
				isStreaming = true
				break
			}
		}
	}
	if upstreamOK && !isStreaming {
		var llmSelfNs int64
		var firstLLM time.Time
		for _, s := range trace.Spans {
			if s == nil || s.Kind != schemas.SpanKindLLMCall {
				continue
			}
			if self := spanWall(s) - childDur[s.SpanID]; self > 0 {
				llmSelfNs += self.Nanoseconds()
			}
			if firstLLM.IsZero() || s.StartTime.Before(firstLLM) {
				firstLLM = s.StartTime
			}
		}
		// llmSelfNs and upstream are both provider-side wall time; the difference is the
		// uninstrumented glue. Guard on a small floor so measurement skew (upstream
		// stamped slightly larger than the enveloping span) never emits a noise bucket.
		providerInternalUs := float64(llmSelfNs)/1000.0 - upstreamMs*1000.0
		if providerInternalUs > 0.5 {
			b := buckets["provider-internal"]
			if b == nil {
				b = &agg{kind: schemas.SpanKindInternal, first: firstLLM}
				buckets["provider-internal"] = b
			}
			b.dur += time.Duration(providerInternalUs * float64(time.Microsecond))
		}
	}

	out := make([]logstore.OverheadBucket, 0, len(buckets)+1)
	var measuredNs int64
	for name, b := range buckets {
		measuredNs += b.dur.Nanoseconds()
		out = append(out, logstore.OverheadBucket{
			Name:       name,
			Kind:       string(b.kind),
			DurationUs: float64(b.dur.Nanoseconds()) / 1000.0,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return buckets[out[i].Name].first.Before(buckets[out[j].Name].first)
	})

	measuredMs := float64(measuredNs) / float64(time.Millisecond)

	// Unary requests: whatever overhead is left over after every instrumented phase is
	// the residual between phases — goroutine-scheduling latency (the request hops across
	// the HTTP, core-pipeline and provider-worker goroutines) plus any not-yet-spanned
	// transport edge. Now that the code phases are instrumented, this is small and
	// dominated by scheduling, so it is surfaced as "scheduling" rather than an opaque
	// "core". Skip when measured spans already exceed the total (upstream over-counting):
	// a negative value is a diagnostic signal, not a bucket, surfaced in the UI footer.
	//
	// STREAMING IS EXCLUDED. For a stream, total-upstream is NOT Bifrost overhead: it
	// includes the off-CPU relay/scheduler wait the request goroutine spends parked
	// between provider chunks (confirmed ~2% CPU under load). All actual Bifrost CPU is
	// already measured in the buckets above (parse/convert accumulators, aggregated
	// per-chunk plugin timing, transport marshal/write). Emitting a residual bucket there
	// would resurrect the misleading "scheduling = 95% of overhead" figure. Instead the
	// caller takes measuredMs as the stream's overhead and folds the off-CPU remainder
	// into upstream, so latency = upstream + overhead still holds.
	if overheadOK && !isStreaming {
		schedulingUs := overheadMs*1000.0 - float64(measuredNs)/1000.0
		if schedulingUs > 0.5 {
			out = append(out, logstore.OverheadBucket{Name: "scheduling", Kind: "scheduling", DurationUs: schedulingUs})
		}
	}
	if len(out) == 0 {
		return nil, measuredMs, isStreaming
	}
	return out, measuredMs, isStreaming
}

// traceAttrFloatMs reads a millisecond span attribute, tolerating int/int64/float64.
func traceAttrFloatMs(attrs map[string]any, key string) (float64, bool) {
	switch v := attrs[key].(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	case int:
		return float64(v), true
	default:
		return 0, false
	}
}

// MCP Plugin Interface Implementation

// SetMCPToolLogCallback sets a callback function that will be called for each MCP tool log entry
func (p *LoggerPlugin) SetMCPToolLogCallback(callback MCPToolLogCallback) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mcpToolLogCallback = callback
}

// PreMCPHook is called before an MCP tool execution - creates initial log entry
// Parameters:
//   - ctx: The Bifrost context
//   - req: The MCP request containing tool call information
//
// Returns:
//   - *schemas.BifrostMCPRequest: The unmodified request
//   - *schemas.MCPPluginShortCircuit: nil (no short-circuiting)
//   - error: nil (errors are logged but don't fail the request)
func (p *LoggerPlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	if ctx == nil {
		p.logger.Error("context is nil in PreMCPHook")
		return req, nil, nil
	}

	// Only log for tool execute requests
	if !req.RequestType.IsExecuteTool() {
		return req, nil, nil
	}

	requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		p.logger.Error("request-id not found in context or is empty in PreMCPHook")
		return req, nil, nil
	}

	// Get parent request ID if this MCP call is part of a larger LLM request (using the MCP agent original request ID)
	parentRequestID, _ := ctx.Value(schemas.BifrostMCPAgentOriginalRequestID).(string)

	createdTimestamp := time.Now().UTC()

	// Extract tool name and arguments from the request
	var toolName string
	var serverLabel string

	fullToolName := req.GetToolName()
	arguments := req.GetToolArguments()

	// Extract server label from tool name (format: {client}-{tool_name})
	// The first part before hyphen is the client/server label
	if fullToolName != "" {
		if idx := strings.Index(fullToolName, "-"); idx > 0 {
			serverLabel = fullToolName[:idx]
			toolName = fullToolName[idx+1:]
		} else {
			toolName = fullToolName
		}
		switch toolName {
		case mcp.ToolTypeListToolFiles, mcp.ToolTypeReadToolFile, mcp.ToolTypeExecuteToolCode:
			if serverLabel == "" {
				serverLabel = "codemode"
			}
		}
	}
	// Skip logging for codemode meta-tools. Check both the full name (bare,
	// e.g. "executeToolCode") and the suffix after the client prefix (e.g.
	// "myclient-executeToolCode") so PreMCP and PostMCP agree on what to skip
	// and we never leave an orphan pending row to expire via the TTL path.
	if bifrost.IsCodemodeTool(fullToolName) || bifrost.IsCodemodeTool(toolName) {
		return req, nil, nil
	}

	// Get virtual key information from context - using same method as normal LLM logging
	virtualKeyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	virtualKeyName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyName)

	// Use the per-tool-call unique MCP log ID (set by agent executor per goroutine) as the
	// primary key. Fall back to requestID if not set (e.g. direct single tool call).
	mcpLogID, ok := ctx.Value(schemas.BifrostContextKeyMCPLogID).(string)
	if !ok || mcpLogID == "" {
		mcpLogID = requestID
	}

	entry := &logstore.MCPToolLog{
		ID:          mcpLogID,
		RequestID:   requestID,
		Timestamp:   createdTimestamp,
		ToolName:    toolName,
		ServerLabel: serverLabel,
		Status:      "processing",
		CreatedAt:   createdTimestamp,
	}

	if parentRequestID != "" {
		entry.LLMRequestID = &parentRequestID
	}

	if virtualKeyID != "" {
		entry.VirtualKeyID = &virtualKeyID
	}
	if virtualKeyName != "" {
		entry.VirtualKeyName = &virtualKeyName
	}
	applyMCPGovernanceFieldsToEntry(ctx, entry)

	// Capture the raw User-Agent of the calling client (stored verbatim; the UI
	// maps it to a client app such as Claude Code, Codex, or Cursor).
	if ua := userAgentFromContext(ctx); ua != "" {
		entry.UserAgent = &ua
		if app := p.detectAppFromUserAgent(ua); app != "" {
			entry.App = &app
		}
	}

	// Set arguments if content logging is enabled
	if p.contentLoggingEnabled(ctx) {
		entry.ArgumentsParsed = arguments
	}

	// Capture configured logging headers and x-bf-lh-* headers into metadata
	entry.MetadataParsed = p.captureLoggingHeaders(ctx)

	p.pendingMCPLogsToInject.Store(mcpLogID, entry)

	p.mu.Lock()
	callback := p.mcpToolLogCallback
	p.mu.Unlock()
	if callback != nil {
		callback(entry)
	}

	return req, nil, nil
}

// PostMCPHook is called after an MCP tool execution - updates the log entry with results
// Parameters:
//   - ctx: The Bifrost context
//   - resp: The MCP response containing tool execution result
//   - bifrostErr: Any error that occurred during execution
//
// Returns:
//   - *schemas.BifrostMCPResponse: The unmodified response
//   - *schemas.BifrostError: The unmodified error
//   - error: nil (errors are logged but don't fail the request)
func (p *LoggerPlugin) PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	if ctx == nil {
		p.logger.Error("context is nil in PostMCPHook")
		return resp, bifrostErr, nil
	}

	// Skip non tool-execute envelopes (Ping/ListTools). The MCP gate stamps
	// MCPRequestType on both the success response and the error, so a single check
	// covers both paths — no pending MCP log entry was created in PreMCPHook for
	// anything but execute-tool requests.
	mcpReqType := schemas.MCPRequestType("")
	if resp != nil {
		mcpReqType = resp.ExtraFields.MCPRequestType
	} else if bifrostErr != nil {
		mcpReqType = bifrostErr.ExtraFields.MCPRequestType
	}
	if !mcpReqType.IsExecuteTool() {
		return resp, bifrostErr, nil
	}
	// Skip logging for codemode tools (executeToolCode, listToolFiles, readToolFile)
	if resp != nil && bifrost.IsCodemodeTool(resp.ExtraFields.ToolName) {
		return resp, bifrostErr, nil
	}

	requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		p.logger.Error("request-id not found in context or is empty in PostMCPHook")
		return resp, bifrostErr, nil
	}

	// Use the per-tool-call unique MCP log ID to find the correct log entry.
	mcpLogID, ok := ctx.Value(schemas.BifrostContextKeyMCPLogID).(string)
	if !ok || mcpLogID == "" {
		mcpLogID = requestID
	}

	// Extract virtual key ID and name from context (set by governance plugin)
	virtualKeyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	virtualKeyName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyName)

	pendingVal, hasPending := p.pendingMCPLogsToInject.LoadAndDelete(mcpLogID)
	var entry *logstore.MCPToolLog
	if hasPending {
		if pending, ok := pendingVal.(*logstore.MCPToolLog); ok {
			entry = pending
		}
	}
	if entry == nil {
		entry = &logstore.MCPToolLog{
			ID:        mcpLogID,
			RequestID: requestID,
			Timestamp: time.Now().UTC(),
			Status:    "processing",
			CreatedAt: time.Now().UTC(),
		}
	}

	if virtualKeyID != "" {
		entry.VirtualKeyID = &virtualKeyID
	}
	if virtualKeyName != "" {
		entry.VirtualKeyName = &virtualKeyName
	}
	applyMCPGovernanceFieldsToEntry(ctx, entry)
	if resp != nil {
		latency := float64(resp.ExtraFields.Latency)
		entry.Latency = &latency
	}

	success := resp != nil && bifrostErr == nil
	if success && p.mcpCatalog != nil && resp.ExtraFields.ClientName != "" && resp.ExtraFields.ToolName != "" {
		if pricingEntry, ok := p.mcpCatalog.GetPricingData(resp.ExtraFields.ClientName, resp.ExtraFields.ToolName); ok {
			toolCost := pricingEntry.CostPerExecution
			entry.Cost = &toolCost
			p.logger.Debug("MCP tool cost for %s.%s: $%.6f", resp.ExtraFields.ClientName, resp.ExtraFields.ToolName, toolCost)
		}
	}

	if bifrostErr != nil {
		entry.Status = "error"
		shouldStoreRaw, _ := ctx.Value(schemas.BifrostContextKeyShouldStoreRawInLogs).(bool)
		entry.ErrorDetailsParsed = sanitizeErrorForLogging(bifrostErr, p.resolveContentPolicy(ctx).visible(), shouldStoreRaw)
	} else if resp != nil {
		entry.Status = "success"
		// MCP tool logs have no hidden-content mode, so content is only
		// stored when it is also visible.
		if p.resolveContentPolicy(ctx).visible() {
			var result interface{}
			if resp.ChatMessage != nil {
				if resp.ChatMessage.Content != nil && resp.ChatMessage.Content.ContentStr != nil {
					contentStr := *resp.ChatMessage.Content.ContentStr
					var parsedContent interface{}
					if err := sonic.Unmarshal([]byte(contentStr), &parsedContent); err == nil {
						result = parsedContent
					} else {
						result = resp.ChatMessage
					}
				} else {
					result = resp.ChatMessage
				}
			} else if resp.ResponsesMessage != nil {
				result = resp.ResponsesMessage
			}
			if result != nil {
				entry.ResultParsed = result
			}
		}
	} else {
		entry.Status = "error"
		entry.ErrorDetailsParsed = &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "MCP tool execution returned nil response",
			},
		}
	}

	p.mu.Lock()
	callback := p.mcpToolLogCallback
	p.mu.Unlock()
	attachMCPLogRedactionData(ctx, entry, p.contentLoggingEnabled(ctx))
	entry.PluginLogs = serializePluginLogs(ctx.GetPluginLogs())
	p.enqueueMCPToolLogEntry(entry, callback)

	return resp, bifrostErr, nil
}
