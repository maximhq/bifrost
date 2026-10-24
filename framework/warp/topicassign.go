package warp

import (
	"context"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// Live assignment.
//
// A topics run files the requests it read, and nothing else. Every request
// indexed after it would be missing from the counts until the next run, and
// "what was asked in the past hour" would have no answer. So the indexer
// files each request as it embeds it: against the topics that exist, into the
// nearest one it is like enough to belong to, or under no topic and into the
// pool the next discovery starts from.
//
// The topics' centres are read from the log store and kept for a while: a
// read per request would put a query on the hot path for a set that changes
// when a run finishes, not between one request and the next. The rows are
// written in batches for the same reason, and because ClickHouse makes a part
// of every insert.

const (
	// topicCentresTTL is how long the indexer trusts the centres it has before
	// reading them again, which is how a run on another instance reaches it.
	topicCentresTTL = time.Minute
	// topicsAssignFlushInterval and topicsAssignFlushSize bound how long a
	// filed request waits in memory and how many wait together.
	topicsAssignFlushInterval = 2 * time.Second
	topicsAssignFlushSize     = 500
	topicsAssignFlushTimeout  = 30 * time.Second
	// topicsPoolMaxSize and topicsPoolMaxAge bound the pool of requests that
	// matched no topic. One run's worth, and no older than discovery would
	// want to build a topic from; the pool is pruned every
	// topicsPoolPruneEvery requests it takes in.
	topicsPoolMaxSize    = topicsMaxVectors
	topicsPoolMaxAge     = 30 * 24 * time.Hour
	topicsPoolPruneEvery = 500
)

// topicCentre is one topic as a request is measured against it.
type topicCentre struct {
	id     string
	centre []float32
	floor  float32
}

// topicCentres is the indexer's copy of the active topics' centres, for one
// embedding signature, read again once it is older than topicCentresTTL or
// has been told to.
type topicCentres struct {
	mu        sync.Mutex
	store     logstore.WarpTopicStore
	signature string
	centres   []topicCentre
	loadedAt  time.Time
	ttl       time.Duration
}

// get returns the centres for signature, reading them when it has none, has
// another signature's, or has held these for longer than the TTL.
func (c *topicCentres) get(ctx context.Context, signature string) ([]topicCentre, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return nil, nil
	}
	if c.signature == signature && !c.loadedAt.IsZero() && time.Since(c.loadedAt) < c.ttl {
		return c.centres, nil
	}
	rows, err := c.store.ListWarpTopics(ctx, logstore.WarpTopicFilter{
		Kinds: []string{logstore.WarpTopicKindTopic}, Statuses: []string{logstore.WarpTopicStatusActive}, EmbeddingSignature: signature,
	})
	if err != nil {
		return nil, err
	}
	centres := make([]topicCentre, 0, len(rows))
	for _, row := range rows {
		centre, err := logstore.DecodeWarpVector(row.Centroid)
		if err != nil || len(centre) == 0 {
			// A topic without a usable centre takes nothing; the rest still do.
			continue
		}
		centres = append(centres, topicCentre{id: row.ID, centre: centre, floor: float32(row.AssignFloor)})
	}
	c.signature, c.centres, c.loadedAt = signature, centres, time.Now()
	return centres, nil
}

// invalidate makes the next get read the centres again, for the process that
// just wrote new topics.
func (c *topicCentres) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadedAt = time.Time{}
}

// setStore points the cache at a store, dropping what it held from another.
func (c *topicCentres) setStore(store logstore.WarpTopicStore) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store, c.centres, c.loadedAt = store, nil, time.Time{}
}

// nearestTopic finds the topic a unit vector is most like, and reports
// whether it is like enough to be filed under it.
func nearestTopic(centres []topicCentre, vector []float32) (id string, similarity float32, matched bool) {
	best := -1
	for index := range centres {
		if len(centres[index].centre) != len(vector) {
			continue
		}
		if score := dot(vector, centres[index].centre); best < 0 || score > similarity {
			best, similarity = index, score
		}
	}
	if best < 0 {
		return "", 0, false
	}
	return centres[best].id, similarity, similarity >= centres[best].floor
}

// topicAssigner files requests in batches.
type topicAssigner struct {
	mu          sync.Mutex
	store       logstore.WarpTopicStore
	assignments []logstore.WarpTopicAssignment
	unmatched   []logstore.WarpTopicUnmatched
	// pooled counts the requests pooled since the pool was last pruned.
	pooled int
	// full is signalled when a batch has grown to flushSize, so the flush
	// loop does not wait out the interval on it.
	full chan struct{}

	flushSize  int
	poolMax    int
	poolMaxAge time.Duration
	pruneEvery int
	logger     schemas.Logger
}

func newTopicAssigner(logger schemas.Logger) *topicAssigner {
	return &topicAssigner{
		full: make(chan struct{}, 1), logger: logger,
		flushSize: topicsAssignFlushSize, poolMax: topicsPoolMaxSize, poolMaxAge: topicsPoolMaxAge, pruneEvery: topicsPoolPruneEvery,
	}
}

// setStore points the assigner at a store. What was waiting for the previous
// one is written to it, or dropped when there was none. The batch is taken and
// the store swapped under one lock, so a request filed meanwhile lands in
// exactly one of the two batches instead of being cleared unwritten.
func (a *topicAssigner) setStore(ctx context.Context, store logstore.WarpTopicStore) {
	a.write(ctx, a.swapStore(store))
}

// swapStore installs store and returns what was waiting for the previous one,
// for the caller to write once it no longer holds anything filing waits on.
func (a *topicAssigner) swapStore(store logstore.WarpTopicStore) topicBatch {
	a.mu.Lock()
	defer a.mu.Unlock()
	batch := a.takeLocked()
	a.store = store
	return batch
}

// add files one request. unmatched is non-nil for a request that goes to the
// pool as well.
func (a *topicAssigner) add(assignment logstore.WarpTopicAssignment, unmatched *logstore.WarpTopicUnmatched) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.store == nil {
		return
	}
	a.assignments = append(a.assignments, assignment)
	if unmatched != nil {
		a.unmatched = append(a.unmatched, *unmatched)
	}
	if len(a.assignments) >= a.flushSize {
		select {
		case a.full <- struct{}{}:
		default:
		}
	}
}

// pending reports how many requests wait to be written.
func (a *topicAssigner) pending() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.assignments)
}

// flush writes what is waiting. A failed write is reported and the rows are
// dropped: the next topics run files every request in its window again, and
// holding them would grow without bound against a store that is down.
func (a *topicAssigner) flush(ctx context.Context) {
	a.mu.Lock()
	batch := a.takeLocked()
	a.mu.Unlock()
	a.write(ctx, batch)
}

// topicBatch is what was waiting to be written, and the store it was for.
type topicBatch struct {
	store       logstore.WarpTopicStore
	assignments []logstore.WarpTopicAssignment
	unmatched   []logstore.WarpTopicUnmatched
	prune       bool
}

// takeLocked empties the batches and reports what they held, the store they
// were for, and whether the pool is due a prune. Callers hold a.mu.
func (a *topicAssigner) takeLocked() topicBatch {
	batch := topicBatch{store: a.store, assignments: a.assignments, unmatched: a.unmatched}
	a.assignments, a.unmatched = nil, nil
	a.pooled += len(batch.unmatched)
	batch.prune = a.pooled >= a.pruneEvery
	if batch.prune {
		a.pooled = 0
	}
	return batch
}

// write sends one taken batch to the store it was for.
func (a *topicAssigner) write(ctx context.Context, batch topicBatch) {
	store, assignments, unmatched, prune := batch.store, batch.assignments, batch.unmatched, batch.prune
	if store == nil || (len(assignments) == 0 && !prune) {
		return
	}
	if len(assignments) > 0 {
		if err := store.UpsertWarpTopicAssignments(ctx, assignments); err != nil {
			a.warnf("failed to file %d request(s) under their Warp topics: %v", len(assignments), err)
		}
	}
	if len(unmatched) > 0 {
		if err := store.AddWarpTopicUnmatched(ctx, unmatched); err != nil {
			a.warnf("failed to pool %d request(s) that matched no Warp topic: %v", len(unmatched), err)
		}
	}
	if prune {
		if _, err := store.PruneWarpTopicUnmatched(ctx, time.Now().Add(-a.poolMaxAge), a.poolMax); err != nil {
			a.warnf("failed to prune the pool of requests that matched no Warp topic: %v", err)
		}
	}
}

// run flushes on the interval, when a batch fills, and once more when done
// closes. Each flush is bounded by its timeout, not by ctx: a batch is taken
// before it is written, so a write cancelled by shutdown would drop it.
func (a *topicAssigner) run(ctx context.Context, done <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	flush := func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), topicsAssignFlushTimeout)
		a.flush(flushCtx)
		cancel()
	}
	for {
		select {
		case <-done:
			flush()
			return
		case <-ticker.C:
			flush()
		case <-a.full:
			flush()
		}
	}
}

func (a *topicAssigner) warnf(format string, args ...any) {
	if a.logger != nil {
		a.logger.Warn(format, args...)
	}
}

// assignTopic files one just-indexed request against the topics that exist.
// Nothing happens on a deployment that has no topics yet: the pool would be
// the whole index, and the first run reads the index itself.
func (i *LogIndexer) assignTopic(ctx context.Context, config *schemas.WarpConfig, item logIndexItem, embedding []float32) {
	i.topicMu.RLock()
	defer i.topicMu.RUnlock()
	signature := embeddingConfigSignature(config)
	centres, err := i.centres.get(ctx, signature)
	if err != nil {
		i.warnf("failed to read Warp topics to file log %s under: %v", item.id, err)
		return
	}
	if len(centres) == 0 {
		return
	}
	vector := unitVector(embedding)
	assignment := item.assignment
	assignment.AssignedAt = time.Now().UTC()
	id, similarity, matched := nearestTopic(centres, vector)
	assignment.Similarity = float64(similarity)
	var unmatched *logstore.WarpTopicUnmatched
	if matched {
		assignment.TopicID = id
	} else {
		unmatched = &logstore.WarpTopicUnmatched{
			LogID: item.id, Timestamp: assignment.Timestamp, LogCreatedAt: assignment.LogCreatedAt,
			Vector: logstore.EncodeWarpVector(vector), EmbeddingSignature: signature,
		}
	}
	if i.beforeFile != nil {
		i.beforeFile()
	}
	i.assigner.add(assignment, unmatched)
}

// SetTopicStore points live assignment at the log store's topic tables, or
// at nothing. The indexer is built before the reader can be rebound, so this
// follows the reader rather than the constructor. It waits out requests being
// filed, so none is filed in a store other than the one its topic came from,
// and installs the new store for both the centres and the assigner in that
// one step. What was waiting for the old store is written after, so a slow
// old store does not hold up filing against the new one, and under the flush
// timeout alone, so a swap during shutdown does not drop what it took.
func (i *LogIndexer) SetTopicStore(store logstore.WarpTopicStore) {
	i.topicMu.Lock()
	i.centres.setStore(store)
	previous := i.assigner.swapStore(store)
	i.topicMu.Unlock()
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(i.ctx), topicsAssignFlushTimeout)
	defer cancel()
	i.assigner.write(flushCtx, previous)
}

// InvalidateTopics makes live assignment read the topics again, for the
// process that has just written new ones.
func (i *LogIndexer) InvalidateTopics() {
	i.centres.invalidate()
}
