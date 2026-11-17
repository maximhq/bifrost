package routing

import (
	"container/heap"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// Backfill from logs. A router captures its requests only from when its
// suggestions were turned on, so a run (or an impact replay) over a window
// that reaches further back would see part of it. Before reading, the window
// is filled from the router's logs: the requests it took (model_router_id),
// sampled exactly as live capture samples them (complexity.CaptureSampled, a
// function of the request ID), less those already captured, are embedded with
// the router's model and written into its capture namespace under their log
// time. They are then ordinary captured requests: later runs reuse them, the
// age sweep and log deletes reach them, and none is embedded twice. A log that
// is gone, hides its content, or predates model_router_id gives nothing.

// ComplexitySuggestionRouterLogs pages the requests a model router took. The
// log store implements it; a suggestion store without it backfills nothing.
type ComplexitySuggestionRouterLogs interface {
	EachComplexityRouterLog(ctx context.Context, routerID string, from, to time.Time, fn func([]logstore.ComplexityRouterLog) error) error
}

// backfillTextBatch is how many logs one text read asks for, and
// backfillTextWorkers how many reads run at once: each log is read by ID,
// which also fetches a payload offloaded to object storage.
const (
	backfillTextBatch   = 64
	backfillTextWorkers = 8
	backfillEmbedBatch  = 256
)

// backfillCandidateFactor sizes the candidates a plan keeps against the
// backfill's limit: a log whose text cannot be read (it is gone, or hides its
// content) is skipped, and the next newest takes its place.
const backfillCandidateFactor = 2

// backfillPlan is what a backfill of one window would do.
type backfillPlan struct {
	candidates []logstore.ComplexityRouterLog // sampled, not captured; newest first; at most backfillCandidateFactor times the limit
	missing    int                            // every sampled request with no captured vector
}

// planBackfill finds the window's sampled requests that have no captured
// vector, newest first: a run reads the recent part of a window most, and what
// is left is filled by the next run. It keeps only the newest candidates as
// it pages the logs, so its memory is the window's captured IDs and a bounded
// candidate list, not every request the router took.
func (p *RoutingPlugin) planBackfill(ctx context.Context, router suggestionRouter, space *complexity.SuggestionSpace, logs ComplexitySuggestionRouterLogs, from, to time.Time, limit int) (backfillPlan, error) {
	if logs == nil || space == nil || space.Store == nil || router.config == nil || router.config.Suggestions == nil || limit <= 0 {
		return backfillPlan{}, nil
	}
	rate := router.config.Suggestions.SampleRate
	if rate <= 0 {
		rate = configstore.DefaultComplexitySuggestionsSampleRate
	}
	// Read through the whole walk, vectors and all: a filtered read stops at
	// a result ceiling on Weaviate (it fails) and Redis (it ends early), and
	// a captured request read as missing is embedded again.
	captured := map[string]struct{}{}
	if err := space.EachCaptured(ctx, from, to, func(page []complexity.CapturedRequest) error {
		for _, r := range page {
			captured[r.LogID] = struct{}{}
		}
		return ctx.Err()
	}); err != nil {
		return backfillPlan{}, fmt.Errorf("read captured requests: %w", err)
	}
	keep := newestLogs{max: backfillCandidateFactor * limit, held: map[string]struct{}{}}
	var plan backfillPlan
	if err := logs.EachComplexityRouterLog(ctx, router.id, from, to, func(page []logstore.ComplexityRouterLog) error {
		for _, l := range page {
			if _, done := captured[l.ID]; done || !complexity.CaptureSampled(l.ID, rate) {
				continue
			}
			if keep.offer(l) {
				plan.missing++
			}
		}
		return ctx.Err()
	}); err != nil {
		return backfillPlan{}, fmt.Errorf("read the router's logs: %w", err)
	}
	plan.candidates = keep.newestFirst()
	return plan, nil
}

// backfillBefore orders logs newest first, by ID within one time.
func backfillBefore(a, b logstore.ComplexityRouterLog) int {
	if c := b.Timestamp.Compare(a.Timestamp); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// newestLogs keeps the newest max logs offered, as a heap whose root is the
// one that would go first. held is the IDs it keeps, so a log offered twice is
// kept and counted once while it is kept.
type newestLogs struct {
	max  int
	logs []logstore.ComplexityRouterLog
	held map[string]struct{}
}

func (h *newestLogs) Len() int           { return len(h.logs) }
func (h *newestLogs) Less(i, j int) bool { return backfillBefore(h.logs[i], h.logs[j]) > 0 }
func (h *newestLogs) Swap(i, j int)      { h.logs[i], h.logs[j] = h.logs[j], h.logs[i] }
func (h *newestLogs) Push(x any)         { h.logs = append(h.logs, x.(logstore.ComplexityRouterLog)) }
func (h *newestLogs) Pop() any {
	last := h.logs[len(h.logs)-1]
	h.logs = h.logs[:len(h.logs)-1]
	return last
}

// offer keeps l if it is among the newest max, and reports whether it was new.
func (h *newestLogs) offer(l logstore.ComplexityRouterLog) bool {
	if _, kept := h.held[l.ID]; kept {
		return false
	}
	if len(h.logs) < h.max {
		heap.Push(h, l)
		h.held[l.ID] = struct{}{}
		return true
	}
	if backfillBefore(l, h.logs[0]) >= 0 {
		return true // older than everything kept: missing, but not a candidate
	}
	delete(h.held, h.logs[0].ID)
	h.logs[0] = l
	h.held[l.ID] = struct{}{}
	heap.Fix(h, 0)
	return true
}

// newestFirst returns what is kept, newest first.
func (h *newestLogs) newestFirst() []logstore.ComplexityRouterLog {
	out := slices.Clone(h.logs)
	slices.SortFunc(out, backfillBefore)
	return out
}

// backfillWindow fills the window from the router's logs and reports how many
// requests it wrote: up to the backfill's limit, the newest first, passing
// over logs whose text cannot be read. One backfill of a router runs at a
// time on this node, so a run and an impact replay, or two replays, here
// never embed the same requests twice: the second plans once the first has
// written. Two nodes can backfill the same router at once and embed the same
// missing requests; point IDs keep that to one vector each, and the extra
// embedding cost is bounded by the backfill's limit. Each request
// is held in the router's capture from before its text is read until it is
// written, so a log deleted in the meantime leaves no vector.
func (p *RoutingPlugin) backfillWindow(ctx context.Context, router suggestionRouter, space *complexity.SuggestionSpace, deps *complexitySuggestionDeps, from, to time.Time) (int, error) {
	if deps == nil || router.capture == nil {
		return 0, nil
	}
	release, err := p.backfillGate(ctx, router.id)
	if err != nil {
		return 0, err
	}
	defer release()
	logs, _ := deps.store.(ComplexitySuggestionRouterLogs)
	limit := backfillLimit(router)
	plan, err := p.planBackfill(ctx, router, space, logs, from, to, limit)
	if err != nil || len(plan.candidates) == 0 {
		return 0, err
	}
	var written, tried int
	for batch := range slices.Chunk(plan.candidates, backfillEmbedBatch) {
		if written >= limit {
			break
		}
		n, err := p.backfillBatch(ctx, router, space, deps, batch, limit-written)
		written += n
		tried += len(batch)
		if err != nil {
			return written, err
		}
	}
	if left := plan.missing - tried; left > 0 {
		p.logger.Info("[Routing] phrase suggestions for router %s: backfilled %d requests from logs; %d older ones are left for later runs", router.id, written, left)
	}
	return written, nil
}

// backfillBatch reads, embeds and writes one batch of candidates, at most room
// of them, and reports how many it wrote.
func (p *RoutingPlugin) backfillBatch(ctx context.Context, router suggestionRouter, space *complexity.SuggestionSpace, deps *complexitySuggestionDeps, batch []logstore.ComplexityRouterLog, room int) (int, error) {
	ids := make([]string, len(batch))
	for i, l := range batch {
		ids[i] = l.ID
	}
	router.capture.HoldForBackfill(ids)
	// Every held request is given back: written ones by WriteBackfilled, the
	// rest here.
	held := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		held[id] = struct{}{}
	}
	defer func() {
		rest := make([]string, 0, len(held))
		for id := range held {
			rest = append(rest, id)
		}
		router.capture.ReleaseBackfill(rest)
	}()
	texts, err := backfillTexts(ctx, deps.texts, batch, space.MessageHistoryCount)
	if err != nil {
		return 0, err
	}
	var readable, inputs []string
	at := make(map[string]time.Time, len(batch))
	for _, l := range batch {
		if len(readable) == room {
			break
		}
		if text := texts[l.ID]; text != "" {
			readable, inputs = append(readable, l.ID), append(inputs, text)
			at[l.ID] = l.Timestamp
		}
	}
	if len(inputs) == 0 {
		return 0, nil
	}
	vectors, err := space.Embed(ctx, inputs)
	if err != nil {
		return 0, fmt.Errorf("embed requests from their logs: %w", err)
	}
	requests := make([]complexity.CapturedRequest, 0, len(readable))
	for i, id := range readable {
		if i < len(vectors) && len(vectors[i]) == space.Dimension {
			requests = append(requests, complexity.CapturedRequest{LogID: id, Vector: vectors[i], At: at[id]})
		}
	}
	for _, r := range requests {
		delete(held, r.LogID)
	}
	if err := router.capture.WriteBackfilled(ctx, space.Store, space.CaptureNamespace, space.Dimension, requests); err != nil {
		return 0, err
	}
	return len(requests), nil
}

// backfillGate waits for the router's backfill in progress, if any, and
// returns the release of this one. Without suggestion state there is nothing
// to share it with.
func (p *RoutingPlugin) backfillGate(ctx context.Context, routerID string) (func(), error) {
	if p.suggestions == nil {
		return func() {}, nil
	}
	gate, _ := p.suggestions.backfills.LoadOrStore(routerID, make(chan struct{}, 1))
	slot := gate.(chan struct{})
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// backfillLimit bounds one backfill to the requests one run clusters, so a
// first run over a busy router's week stays a bounded number of embeddings.
func backfillLimit(router suggestionRouter) int {
	if router.config == nil || router.config.Suggestions == nil || router.config.Suggestions.MaxClusteredRequests <= 0 {
		return configstore.DefaultComplexitySuggestionsMaxClusteredRequests
	}
	return router.config.Suggestions.MaxClusteredRequests
}

// backfillTexts reads the request text of the logs, in batches, several at a
// time.
func backfillTexts(ctx context.Context, texts ComplexitySuggestionTexts, logs []logstore.ComplexityRouterLog, messageHistoryCount int) (map[string]string, error) {
	if texts == nil {
		return nil, nil
	}
	ids := make([]string, len(logs))
	for i, l := range logs {
		ids[i] = l.ID
	}
	out := make(map[string]string, len(ids))
	var mu sync.Mutex
	var firstErr error
	work := make(chan []string)
	var wg sync.WaitGroup
	for range backfillTextWorkers {
		wg.Go(func() {
			for batch := range work {
				got, err := texts(ctx, batch, messageHistoryCount)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				for id, text := range got {
					out[id] = text
				}
				mu.Unlock()
			}
		})
	}
	for batch := range slices.Chunk(ids, backfillTextBatch) {
		if ctx.Err() != nil {
			break
		}
		work <- batch
	}
	close(work)
	wg.Wait()
	if firstErr != nil {
		return nil, fmt.Errorf("read request text from logs: %w", firstErr)
	}
	return out, ctx.Err()
}
