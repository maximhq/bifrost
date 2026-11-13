package traceingest

import (
	"context"
	"errors"
	"slices"
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
	// matched maps a span id to the gateway rows found for it and how. replaces is set when the
	// match took the place of a stored fingerprint link, whose rows must be unlinked first.
	type match struct {
		method   string
		kind     string
		logIDs   []string
		replaces bool
	}
	matched := make(map[string]*match)
	takenBy := make(map[string]string) // gateway row id to the span it stands for
	// cleared holds the spans that lost every row of a fingerprint link to stronger evidence for
	// another span: their stored links and snapshot are dropped.
	cleared := make(map[string]struct{})
	for _, link := range existing {
		m := matched[link.SpanID]
		if m == nil {
			m = &match{method: link.Method, kind: link.LogKind}
			matched[link.SpanID] = m
		}
		m.logIDs = append(m.logIDs, link.LogID)
		takenBy[link.LogID] = link.SpanID
	}
	// heldFirmly reports whether a gateway row stands for a span by more than a fingerprint. A row
	// held only by fingerprint is offered again to a named or response-id match for another span.
	heldFirmly := func(logID string) bool {
		owner, used := takenBy[logID]
		if !used {
			return false
		}
		m := matched[owner]
		return m == nil || m.method != LinkMethodFingerprint
	}
	// replaceable reports whether a span is free for a named or response-id match: it has none, or
	// only a fingerprint link, the weakest evidence, which stronger evidence takes over.
	replaceable := func(span *logstore.TraceSpan) bool {
		m := matched[span.SpanID]
		return m == nil || m.method == LinkMethodFingerprint
	}
	claim := func(span *logstore.TraceSpan, call *logstore.GatewayCall, method string) {
		if owner, used := takenBy[call.ID]; used {
			if owner == span.SpanID || method == LinkMethodFingerprint || heldFirmly(call.ID) {
				return
			}
			// The row stands for another span only by fingerprint: stronger evidence for this span
			// takes it, and the other span keeps whatever rows it has left.
			previous := matched[owner]
			previous.logIDs = slices.DeleteFunc(previous.logIDs, func(id string) bool { return id == call.ID })
			previous.replaces = true
			if len(previous.logIDs) == 0 {
				delete(matched, owner)
				cleared[owner] = struct{}{}
			}
			delete(takenBy, call.ID)
		}
		m := matched[span.SpanID]
		if m != nil && m.method == LinkMethodFingerprint && method != LinkMethodFingerprint {
			// Stronger evidence replaces the fingerprint link: its rows are free for other spans.
			for _, id := range m.logIDs {
				delete(takenBy, id)
			}
			m = &match{method: method, kind: call.LogKind, replaces: true}
			matched[span.SpanID] = m
		}
		if m == nil {
			m = &match{method: method, kind: call.LogKind}
			matched[span.SpanID] = m
		}
		m.logIDs = append(m.logIDs, call.ID)
		takenBy[call.ID] = span.SpanID
	}

	// 1. Gateway rows that named this trace. Every call that names its span, or a span inside it,
	// is placed before any call is placed by inference from a parent above its span: a call that
	// names a span directly is better evidence than an older one that names the span's parent.
	named, err := s.store.FindGatewayCallsByTraceID(ctx, key.TraceID)
	if err != nil {
		return false, err
	}
	for _, inferDown := range []bool{false, true} {
		for _, call := range named {
			if heldFirmly(call.ID) || call.ParentRequestID != nil {
				continue // a fallback attempt is reached through its first attempt
			}
			kindFits := linksLLM
			if call.LogKind == logstore.TraceLinkedLogKindMCP {
				kindFits = func(kind string) bool { return kind == logstore.TraceSpanKindTool }
			}
			// One gateway call per span: a span already standing for a call is not offered again.
			wants := func(span *logstore.TraceSpan) bool { return kindFits(span.Kind) && replaceable(span) }
			method := LinkMethodHeader
			if call.ParentSpanID != nil && len(*call.ParentSpanID) == 16 && len(key.TraceID) == 32 {
				// A W3C-sized pair came from traceparent unless a header said otherwise; either way the
				// evidence is the same, so the label only records the likelier source.
				method = LinkMethodTraceparent
			}
			target := spanAboveGatewayCall(call, byID, wants)
			if inferDown {
				target = spanBelowGatewayCall(call, children, wants)
			}
			if target != nil {
				claim(target, call, method)
			}
		}
	}

	// 2. Provider response id.
	byResponseID := make(map[string]*logstore.TraceSpan)
	var responseIDs []string
	for _, span := range spans {
		if replaceable(span) && linksLLM(span.Kind) && span.ProviderResponseID != "" {
			byResponseID[span.ProviderResponseID] = span
			responseIDs = append(responseIDs, span.ProviderResponseID)
		}
	}
	calls, err := s.store.FindGatewayCallsByProviderResponseIDs(ctx, responseIDs)
	if err != nil {
		return false, err
	}
	for _, call := range calls {
		if call.ProviderResponseID == nil {
			continue
		}
		span := byResponseID[*call.ProviderResponseID]
		if span == nil {
			continue
		}
		if call.ParentRequestID != nil {
			// The id is a fallback attempt's. The call is its chain, reached through the first
			// attempt: claiming the root links the whole chain, and a root another span already
			// holds is not claimed twice.
			root := *call
			root.ID, root.ParentRequestID = *call.ParentRequestID, nil
			call = &root
		}
		claim(span, call, LinkMethodResponseID)
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
		candidates, err := s.store.FindGatewayCallsByFingerprint(ctx, fingerprint, span.StartTime.Add(-matchSkew), end.Add(matchSkew), nil)
		if err != nil {
			return false, err
		}
		if len(candidates) >= logstore.MaxFingerprintCandidates {
			continue // a full page cannot show that a fit is unique
		}
		var fits []*logstore.GatewayCall
		for _, call := range candidates {
			if _, used := takenBy[call.ID]; used || call.ParentRequestID != nil {
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

	// A span whose fingerprint rows all went to stronger evidence for other spans drops its links
	// and snapshot, so its rollup falls back to its own SDK numbers. A span that found a new match
	// in the same pass has its stored links replaced by the write below instead.
	for spanID := range cleared {
		if m := matched[spanID]; m != nil {
			m.replaces = true
			continue
		}
		if err := s.store.DeleteTraceSpanLinks(ctx, key.ProjectID, key.TraceID, spanID); err != nil {
			return false, err
		}
		if byID[spanID] == nil {
			continue
		}
		if err := s.updateEnrichment(ctx, key.ProjectID, key.TraceID, spanID, logstore.TraceSpanEnrichment{ClearLink: true}); err != nil {
			return false, err
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
		if len(chain) == 0 || (span.LinkSnapshotParsed != nil && missesAny(chain, m.logIDs)) {
			// Retention removed some or all of the gateway rows. The span keeps the numbers it
			// copied from the whole chain instead of shrinking to the rows that are left.
			if span.LinkSnapshotParsed != nil && !span.LinkedLogExpired {
				if err := s.updateEnrichment(ctx, key.ProjectID, key.TraceID, spanID, logstore.TraceSpanEnrichment{LinkedLogExpired: new(true)}); err != nil {
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
			if call.ProviderResponseID != nil && *call.ProviderResponseID != "" {
				snapshot.ResponseIDs = append(snapshot.ResponseIDs, *call.ProviderResponseID)
			}
			// The chain is oldest first, so the attempt that answered names the model.
			snapshot.Model, snapshot.Provider = call.Model, call.Provider
		}
		// Sorted so the stored snapshot does not change with the order rows come back in.
		slices.Sort(snapshot.ResponseIDs)
		snapshot.ResponseIDs = slices.Compact(snapshot.ResponseIDs)
		if priced {
			snapshot.Cost = &cost
		}
		if timed {
			snapshot.LatencyMs = &latency
		}
		if m.replaces {
			if err := s.store.DeleteTraceSpanLinks(ctx, key.ProjectID, key.TraceID, spanID); err != nil {
				return false, err
			}
		}
		if err := s.store.CreateTraceSpanLinks(ctx, links); err != nil {
			return false, err
		}
		if !final {
			// The call is still running: its tokens and cost are not known yet. A span whose old link
			// was just replaced drops that link's numbers now, and counts its own until this call is
			// final, instead of showing the numbers of a row it no longer links.
			retry = true
			if m.replaces {
				if err := s.updateEnrichment(ctx, key.ProjectID, key.TraceID, spanID, logstore.TraceSpanEnrichment{ClearLink: true}); err != nil {
					return false, err
				}
			}
			continue
		}
		if err := s.updateEnrichment(ctx, key.ProjectID, key.TraceID, spanID, logstore.TraceSpanEnrichment{LinkMethod: &m.method, LinkSnapshot: snapshot, LinkedLogExpired: new(false)}); err != nil {
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
			enrichment := logstore.TraceSpanEnrichment{Cost: &cost, CostEstimated: new(true), PricedFrom: new(logstore.TraceSpanPricingInputsOf(span))}
			if span.Provider == "" {
				enrichment.Provider = &provider
			}
			if err := s.updateEnrichment(ctx, key.ProjectID, key.TraceID, span.SpanID, enrichment); err != nil {
				if !errors.Is(err, logstore.ErrTraceSpanChanged) {
					return false, err
				}
				retry = true // a fold changed the span after it was read: price it again on the next pass
			}
		}
	}
	return retry, nil
}

// spanAboveGatewayCall finds the span a gateway row that named this trace belongs to when the row's
// parent span id points at the model span itself or at a span below it (an HTTP client span made by
// the SDK's instrumentation).
func spanAboveGatewayCall(call *logstore.GatewayCall, byID map[string]*logstore.TraceSpan, wants func(span *logstore.TraceSpan) bool) *logstore.TraceSpan {
	if call.ParentSpanID == nil || *call.ParentSpanID == "" {
		return nil
	}
	seen := 0
	for span := byID[*call.ParentSpanID]; span != nil && seen < 64; span, seen = byID[span.ParentSpanID], seen+1 {
		if wants(span) {
			return span
		}
		if span.Kind != logstore.TraceSpanKindHTTP && span.Kind != logstore.TraceSpanKindUnknown {
			break // a task, chain or agent span is not "inside" a model call: stop climbing
		}
	}
	return nil
}

// spanBelowGatewayCall finds the span a gateway row that named this trace belongs to when the row's
// parent span id points at the span the model span sits under (a Braintrust span.export() of the
// enclosing span): the model span directly under it whose time window holds the call.
func spanBelowGatewayCall(call *logstore.GatewayCall, children map[string][]*logstore.TraceSpan, wants func(span *logstore.TraceSpan) bool) *logstore.TraceSpan {
	if call.ParentSpanID == nil || *call.ParentSpanID == "" {
		return nil
	}
	var under []*logstore.TraceSpan
	for _, child := range children[*call.ParentSpanID] {
		if wants(child) {
			under = append(under, child)
		}
	}
	if len(under) == 0 {
		return nil
	}
	// Take the call under the parent that started closest to the gateway call, among those whose
	// time window holds it. A single child is checked too: its sibling may not have arrived yet, and
	// the first gateway call to look would otherwise take it. Calls made one after another are told apart this way; two
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
		// A model prefixed with that same provider ("openai/gpt-4o") is priced by its bare name. A
		// prefix naming another provider is part of the model name and stays.
		if i := strings.Index(model, "/"); i > 0 && strings.ToLower(model[:i]) == name {
			model = model[i+1:]
		}
	} else if i := strings.Index(model, "/"); i > 0 {
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

// missesAny reports whether some linked row id is absent from the chain read back. A chain can lose
// a row to retention and gain a later attempt in the same pass, so its size alone does not show it.
func missesAny(chain []*logstore.GatewayCall, ids []string) bool {
	found := make(map[string]struct{}, len(chain))
	for _, call := range chain {
		found[call.ID] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			return true
		}
	}
	return false
}

// updateEnrichment writes enrichment onto one span while holding the span write lock, which the
// fold commit holds too. On ClickHouse the write reads the span, patches it and inserts it again;
// a commit between the two would be replaced by the stale copy. The lock covers the write only, not
// the gateway lookups before it, so ingest does not wait on them.
func (s *Service) updateEnrichment(ctx context.Context, projectID, traceID, spanID string, enrichment logstore.TraceSpanEnrichment) error {
	s.spanWriteMu.Lock()
	defer s.spanWriteMu.Unlock()
	return s.store.UpdateTraceSpanEnrichment(ctx, projectID, traceID, spanID, enrichment)
}
