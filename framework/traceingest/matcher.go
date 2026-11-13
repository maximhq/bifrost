package traceingest

import (
	"context"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const (
	// matchSkew widens a span's time window when looking for its gateway call by fingerprint, for
	// the clock difference between the SDK's host and Bifrost's.
	matchSkew = 5 * time.Second
	// gatewayStatusProcessing is a gateway row whose call has not finished: its numbers are not final.
	gatewayStatusProcessing = "processing"
)

// matchRetryDelays are when a trace with unlinked model or tool calls is looked at again. A gateway
// row is written after its call ends and through a batching writer, so it can land after the span.
// The last pass also refreshes snapshots, which picks up a cost the gateway computed late.
var matchRetryDelays = []time.Duration{2 * time.Second, 10 * time.Second, 60 * time.Second}

// Pricer prices token usage from the model catalog. *modelcatalog.ModelCatalog implements it.
type Pricer interface {
	CalculateCostForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *modelcatalog.PricingLookupScopes) float64
	GetProvidersForModel(model string) []schemas.ModelProvider
}

// SetPricer sets the catalog used to estimate the cost of spans whose SDK sent none.
func (s *Service) SetPricer(pricer Pricer) {
	s.pricer = pricer
}

// linksLLM reports whether a span of this kind is a model call a gateway LLM row can stand for.
func linksLLM(kind string) bool {
	return kind == logstore.TraceSpanKindLLM || kind == logstore.TraceSpanKindEmbedding
}

// enrichTrace links a trace's model and tool spans to the gateway calls behind them and prices the
// ones that stay unlinked. It reports whether the trace should be looked at again later: some call
// is still unlinked, or a linked gateway row was not final yet.
//
// A span is matched by the strongest evidence available, in this order:
//
//  1. The gateway request named this trace, through a link header or traceparent. Its parent span
//     id points at the model span, at a span below it (an HTTP client span), or at the span the
//     model span sits under.
//  2. The provider's response id is the same on the span and on the gateway row.
//  3. One gateway row, and only one, has the same model and last user message inside the span's
//     time window, limited to the tracing key's virtual keys when it is bound to any.
func (s *Service) enrichTrace(ctx context.Context, key logstore.TraceKey) (retry bool, err error) {
	spans, err := s.store.ListTraceSpans(ctx, key.ProjectID, key.TraceID)
	if err != nil {
		return false, err
	}
	byID := make(map[string]*logstore.TraceSpan, len(spans))
	children := make(map[string][]*logstore.TraceSpan)
	for _, span := range spans {
		byID[span.SpanID] = span
		children[span.ParentSpanID] = append(children[span.ParentSpanID], span)
	}
	existing, err := s.store.ListTraceSpanLinks(ctx, key.ProjectID, key.TraceID)
	if err != nil {
		return false, err
	}
	// matched maps a span id to the gateway rows found for it and how.
	type match struct {
		method string
		kind   string
		logIDs []string
	}
	matched := make(map[string]*match)
	taken := make(map[string]struct{}) // gateway row ids already standing for a span
	for _, link := range existing {
		m := matched[link.SpanID]
		if m == nil {
			m = &match{method: link.Method, kind: link.LogKind}
			matched[link.SpanID] = m
		}
		m.logIDs = append(m.logIDs, link.LogID)
		taken[link.LogID] = struct{}{}
	}
	claim := func(span *logstore.TraceSpan, call *logstore.GatewayCall, method string) {
		if _, used := taken[call.ID]; used {
			return
		}
		m := matched[span.SpanID]
		if m == nil {
			m = &match{method: method, kind: call.LogKind}
			matched[span.SpanID] = m
		}
		m.logIDs = append(m.logIDs, call.ID)
		taken[call.ID] = struct{}{}
	}

	// 1. Gateway rows that named this trace.
	named, err := s.store.FindGatewayCallsByTraceID(ctx, key.TraceID)
	if err != nil {
		return false, err
	}
	for _, call := range named {
		if _, used := taken[call.ID]; used || call.ParentRequestID != nil {
			continue // a fallback attempt is reached through its first attempt
		}
		kindFits := linksLLM
		if call.LogKind == logstore.TraceLinkedLogKindMCP {
			kindFits = func(kind string) bool { return kind == logstore.TraceSpanKindTool }
		}
		// One gateway call per span: a span already standing for a call is not offered again.
		wants := func(span *logstore.TraceSpan) bool { return kindFits(span.Kind) && matched[span.SpanID] == nil }
		method := LinkMethodHeader
		if call.ParentSpanID != nil && len(*call.ParentSpanID) == 16 && len(key.TraceID) == 32 {
			// A W3C-sized pair came from traceparent unless a header said otherwise; either way the
			// evidence is the same, so the label only records the likelier source.
			method = LinkMethodTraceparent
		}
		if target := spanForGatewayCall(call, byID, children, wants); target != nil {
			claim(target, call, method)
		}
	}

	// 2. Provider response id.
	byResponseID := make(map[string]*logstore.TraceSpan)
	var responseIDs []string
	for _, span := range spans {
		if matched[span.SpanID] == nil && linksLLM(span.Kind) && span.ProviderResponseID != "" {
			byResponseID[span.ProviderResponseID] = span
			responseIDs = append(responseIDs, span.ProviderResponseID)
		}
	}
	calls, err := s.store.FindGatewayCallsByProviderResponseIDs(ctx, responseIDs)
	if err != nil {
		return false, err
	}
	for _, call := range calls {
		if call.ProviderResponseID != nil {
			if span := byResponseID[*call.ProviderResponseID]; span != nil {
				claim(span, call, LinkMethodResponseID)
			}
		}
	}

	// 3. Fingerprint, accepted only when exactly one gateway row fits.
	for _, span := range spans {
		if matched[span.SpanID] != nil || !linksLLM(span.Kind) || span.ContentHidden {
			continue
		}
		fingerprint := SpanInputFingerprint(decodeValue(span.Input))
		if fingerprint == "" {
			continue
		}
		end := span.StartTime
		if span.EndTime != nil {
			end = *span.EndTime
		}
		candidates, err := s.store.FindGatewayCallsByFingerprint(ctx, fingerprint, span.StartTime.Add(-matchSkew), end.Add(matchSkew), s.virtualKeysFor(ctx, span.TracingKeyID))
		if err != nil {
			return false, err
		}
		var fits []*logstore.GatewayCall
		for _, call := range candidates {
			if _, used := taken[call.ID]; used || call.ParentRequestID != nil {
				continue // a fallback attempt is reached through its first attempt
			}
			if span.Model != "" && !sameModel(span.Model, call.Model) {
				continue
			}
			if span.TotalTokens > 0 && call.TotalTokens > 0 && span.TotalTokens != call.TotalTokens {
				continue
			}
			fits = append(fits, call)
		}
		if len(fits) == 1 {
			claim(span, fits[0], LinkMethodFingerprint)
		}
	}

	// Write the links and the snapshot of each linked span.
	for spanID, m := range matched {
		span := byID[spanID]
		if span == nil {
			continue
		}
		chain, err := s.store.FindGatewayCallChain(ctx, m.kind, m.logIDs)
		if err != nil {
			return false, err
		}
		if len(chain) == 0 {
			// Retention removed the gateway rows. The span keeps the numbers it copied.
			if span.LinkSnapshotParsed != nil && !span.LinkedLogExpired {
				if err := s.store.UpdateTraceSpanEnrichment(ctx, key.ProjectID, key.TraceID, spanID, logstore.TraceSpanEnrichment{LinkedLogExpired: new(true)}); err != nil {
					return false, err
				}
			}
			continue
		}
		links := make([]*logstore.TraceSpanLink, 0, len(chain))
		snapshot := &logstore.TraceLinkSnapshot{}
		final := true
		var cost, latency float64
		priced, timed := false, false
		for _, call := range chain {
			links = append(links, &logstore.TraceSpanLink{ProjectID: key.ProjectID, TraceID: key.TraceID, SpanID: spanID, LogKind: call.LogKind, LogID: call.ID, Method: m.method, ExpiresAt: span.ExpiresAt})
			if call.Status == gatewayStatusProcessing {
				final = false
			}
			snapshot.PromptTokens += call.PromptTokens
			snapshot.CompletionTokens += call.CompletionTokens
			snapshot.TotalTokens += call.TotalTokens
			snapshot.CachedReadTokens += call.CachedReadTokens
			if call.Cost != nil {
				cost += *call.Cost
				priced = true
			}
			if call.Latency != nil {
				latency += *call.Latency
				timed = true
			}
			// The chain is oldest first, so the attempt that answered names the model.
			snapshot.Model, snapshot.Provider = call.Model, call.Provider
		}
		if priced {
			snapshot.Cost = &cost
		}
		if timed {
			snapshot.LatencyMs = &latency
		}
		if err := s.store.CreateTraceSpanLinks(ctx, links); err != nil {
			return false, err
		}
		if !final {
			// The call is still running: its tokens and cost are not known yet.
			retry = true
			continue
		}
		if err := s.store.UpdateTraceSpanEnrichment(ctx, key.ProjectID, key.TraceID, spanID, logstore.TraceSpanEnrichment{LinkMethod: &m.method, LinkSnapshot: snapshot, LinkedLogExpired: new(false)}); err != nil {
			return false, err
		}
	}

	// Price what stayed unlinked and came with no cost of its own.
	for _, span := range spans {
		if matched[span.SpanID] != nil {
			continue
		}
		if linksLLM(span.Kind) || span.Kind == logstore.TraceSpanKindTool {
			retry = true
		}
		if !linksLLM(span.Kind) || span.Cost != nil || span.TotalTokens == 0 || span.Model == "" {
			continue
		}
		if cost, provider, ok := s.estimateCost(span); ok {
			enrichment := logstore.TraceSpanEnrichment{Cost: &cost, CostEstimated: new(true)}
			if span.Provider == "" {
				enrichment.Provider = &provider
			}
			if err := s.store.UpdateTraceSpanEnrichment(ctx, key.ProjectID, key.TraceID, span.SpanID, enrichment); err != nil {
				return false, err
			}
		}
	}
	return retry, nil
}

// spanForGatewayCall finds the span a gateway row that named this trace belongs to. The row's
// parent span id can point at the model span itself, at a span below it (an HTTP client span made
// by the SDK's instrumentation), or at the span the model span sits under (a Braintrust
// span.export() of the enclosing span).
func spanForGatewayCall(call *logstore.GatewayCall, byID map[string]*logstore.TraceSpan, children map[string][]*logstore.TraceSpan, wants func(span *logstore.TraceSpan) bool) *logstore.TraceSpan {
	if call.ParentSpanID == nil || *call.ParentSpanID == "" {
		return nil
	}
	// Up: the parent itself, then its ancestors.
	seen := 0
	for span := byID[*call.ParentSpanID]; span != nil && seen < 64; span, seen = byID[span.ParentSpanID], seen+1 {
		if wants(span) {
			return span
		}
		if span.Kind != logstore.TraceSpanKindHTTP && span.Kind != logstore.TraceSpanKindUnknown {
			break // a task, chain or agent span is not "inside" a model call: stop climbing
		}
	}
	// Down: the model spans directly under the named parent.
	var under []*logstore.TraceSpan
	for _, child := range children[*call.ParentSpanID] {
		if wants(child) {
			under = append(under, child)
		}
	}
	if len(under) <= 1 {
		if len(under) == 1 {
			return under[0]
		}
		return nil
	}
	// Several calls under one parent: take the one that started closest to the gateway call, among
	// those whose time window holds it. Calls made one after another are told apart this way; two
	// that started at the same instant are not, and stay unlinked.
	var best *logstore.TraceSpan
	var bestGap time.Duration
	tied := false
	for _, child := range under {
		end := child.StartTime
		if child.EndTime != nil {
			end = *child.EndTime
		}
		if call.Timestamp.Before(child.StartTime.Add(-matchSkew)) || call.Timestamp.After(end.Add(matchSkew)) {
			continue
		}
		if child.Model != "" && call.LogKind == logstore.TraceLinkedLogKindLLM && !sameModel(child.Model, call.Model) {
			continue
		}
		gap := call.Timestamp.Sub(child.StartTime).Abs()
		switch {
		case best == nil || gap < bestGap:
			best, bestGap, tied = child, gap, false
		case gap == bestGap:
			tied = true
		}
	}
	if tied {
		return nil
	}
	return best
}

// sameModel compares model names ignoring a provider prefix: "openai/gpt-4o" is "gpt-4o".
func sameModel(a, b string) bool {
	bare := func(name string) string {
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		return strings.ToLower(name)
	}
	return bare(a) == bare(b)
}

// virtualKeysFor returns the virtual keys a tracing key is bound to, which narrow the fingerprint
// search to that key's own gateway traffic.
func (s *Service) virtualKeysFor(ctx context.Context, tracingKeyID string) []string {
	if tracingKeyID == "" {
		return nil
	}
	key, err := s.config.GetTracingKey(ctx, tracingKeyID)
	if err != nil {
		return nil
	}
	return key.VirtualKeyIDs
}

// estimateCost prices a span from the model catalog. The provider is the one the span names, else
// the first catalog provider that serves the model. ok is false when nothing prices it: the span
// stays unpriced rather than getting a guessed number.
func (s *Service) estimateCost(span *logstore.TraceSpan) (cost float64, provider string, ok bool) {
	if s.pricer == nil {
		return 0, "", false
	}
	usage := &schemas.BifrostLLMUsage{PromptTokens: span.PromptTokens, CompletionTokens: span.CompletionTokens, TotalTokens: span.TotalTokens}
	if span.CachedReadTokens > 0 || span.CachedWriteTokens > 0 {
		usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{CachedReadTokens: span.CachedReadTokens, CachedWriteTokens: span.CachedWriteTokens}
	}
	requestType := schemas.ChatCompletionRequest
	if span.Kind == logstore.TraceSpanKindEmbedding {
		requestType = schemas.EmbeddingRequest
	}
	model := span.Model
	var providers []schemas.ModelProvider
	if span.Provider != "" {
		// SDKs name providers loosely ("openai.chat", "OpenAI"): take the leading word, lowercased.
		name := strings.ToLower(span.Provider)
		if i := strings.IndexAny(name, ". "); i > 0 {
			name = name[:i]
		}
		providers = append(providers, schemas.ModelProvider(name))
	}
	if i := strings.Index(model, "/"); i > 0 && span.Provider == "" {
		providers = append(providers, schemas.ModelProvider(strings.ToLower(model[:i])))
		model = model[i+1:]
	}
	providers = append(providers, s.pricer.GetProvidersForModel(model)...)
	for _, candidate := range providers {
		if cost := s.pricer.CalculateCostForUsage(usage, candidate, model, requestType, nil); cost > 0 {
			return cost, string(candidate), true
		}
	}
	return 0, "", false
}
