package complexity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"strings"
)

// Request capture keeps a sample of the vectors the semantic classifier
// computes, so phrase suggestions can cluster real traffic in the router's
// own embedding space without embedding anything a second time.
//
// The classifier already holds the request's vector, its nearest phrase and
// that phrase's tier when it finishes classifying. Capture hands those to a
// bounded queue with a non-blocking send and returns; one worker writes them
// to a vector store namespace of their own. Nothing on the request path waits
// on the store, and a full queue drops the sample rather than slowing the
// request.
//
// Only the vector and routing metadata are stored, under a point ID derived
// from the request ID (see capturePointID), with the request ID itself, which
// is also the request's log ID, in the metadata. The text stays in the logs and is read from
// there when suggestions run, so a log that is deleted, or whose content is
// hidden, takes its text with it. Forget removes the vectors of deleted logs
// and Sweep removes vectors older than the logs' retention, in every capture
// namespace.
//
// The namespace is keyed by the embedding space (provider, model, dimension),
// not by the phrase generation: editing phrases does not change what a request
// vector means, so captured traffic survives phrase edits, while a new
// embedding model starts a fresh namespace because old vectors are not
// comparable with new ones. The old namespace is not deleted when the space
// changes: around a switch the writer still holds samples of the old space,
// and another node may still serve it, so its vectors age out through Sweep
// instead.

// CaptureNamespacePrefix starts every capture namespace. It must not share a
// prefix with SemanticVectorStoreNamespace: the generation registry reclaims
// unclaimed namespaces under that prefix.
const CaptureNamespacePrefix = "BifrostComplexityRequests"

// captureQueueSize bounds samples waiting to be written. Each holds one
// vector (6 KB at 1536 dimensions), so a full queue retains about 6 MB.
const captureQueueSize = 1024

// captureWriteTimeout bounds one vector store write.
const captureWriteTimeout = 5 * time.Second

// captureDrainBudget is how long Close lets queued samples finish writing
// before it cancels them.
const captureDrainBudget = 5 * time.Second

// captureDeleteRetryBatch is how many recorded deletes one RetryDeletes pass
// takes on.
const captureDeleteRetryBatch = 500

// captureRecordTimeout bounds recording failed deletes.
const captureRecordTimeout = 10 * time.Second

// Capture metadata properties.
const (
	captureMetadataKind      = "kind"
	captureMetadataKindValue = "request"
	captureMetadataTimestamp = "timestamp" // unix seconds
	captureMetadataLogID     = "log_id"    // the request ID, which the point ID may not be
)

var captureProperties = map[string]vectorstore.VectorStoreProperties{
	captureMetadataKind:      {DataType: vectorstore.VectorStorePropertyTypeString, Description: "record kind"},
	captureMetadataTimestamp: {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "capture time, unix seconds"},
	captureMetadataLogID:     {DataType: vectorstore.VectorStorePropertyTypeString, Description: "request and log ID"},
}

// captureIDSpace seeds the point IDs of request IDs that are not UUIDs. It
// must never change: the IDs already written were derived from it.
var captureIDSpace = uuid.MustParse("6f1c7a52-3b9e-4d0a-9c1e-2a8b5f4d7e31")

// capturePointID maps a request ID to the point ID its vector is stored
// under. Qdrant and Weaviate accept only UUIDs, while a request ID can be any
// x-request-id the client sent, so a non-UUID is mapped to a name-based UUID:
// the same request ID always gives the same point, so write, Forget and
// RetryDeletes address it alike. A UUID request ID, the gateway's default, is
// used in canonical form.
func capturePointID(requestID string) string {
	if u, err := uuid.Parse(requestID); err == nil {
		return u.String()
	}
	return uuid.NewSHA1(captureIDSpace, []byte(requestID)).String()
}

// CaptureConfig turns request capture on. A nil config turns it off.
type CaptureConfig struct {
	// SampleRate is the share of classified requests captured, in (0, 1].
	SampleRate float64
}

// CaptureStats counts what capture did since it started.
type CaptureStats struct {
	Enabled   bool
	Queued    int64 // accepted onto the queue
	Dropped   int64 // sampled but dropped because the queue was full
	Written   int64
	Failed    int64
	Forgotten int64 // accepted, but its log was deleted before or while it was written
}

// captureInputVersion names how request text is turned into the embedded
// input (SemanticInputText and the extraction feeding it). Bump it when that
// changes: vectors of the old input are not comparable with the new.
const captureInputVersion = 1

// CaptureNamespace names the capture namespace of one model router in one
// embedding space: the embedding model, and the slice of each request it
// embeds. A router's namespaces are its own, so its suggestion runs read only
// the requests it classified, and deleting a router drops its captures; a new
// embedding space starts a fresh namespace, because old vectors are not
// comparable with new ones. An empty router ID names nothing: only model
// routers capture.
//
// A captured vector carries no routing verdict (tier, similarity): those are
// the router's view of the request at the time, they are on its log, and a
// suggestion run recomputes them against the current phrase bank. The
// underscore + hexadecimal parts are accepted by every backend.
func CaptureNamespace(routerID string, provider schemas.ModelProvider, model string, dimension, messageHistoryCount int) string {
	if routerID == "" {
		return ""
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "complexity-capture-v3\x00%s\x00%s\x00%d\x00%d\x00%d",
		provider, model, dimension, max(1, messageHistoryCount), captureInputVersion))
	return CaptureRouterPrefix(routerID) + hex.EncodeToString(sum[:16])
}

// CaptureRouterPrefix starts every capture namespace of one model router.
func CaptureRouterPrefix(routerID string) string {
	return CaptureNamespacePrefix + "_" + CaptureRouterKey(routerID) + "_"
}

// CaptureRouterKey stands for a router ID in its namespace names: a
// fixed-width hash, since an ID can hold characters a backend refuses in a
// name.
func CaptureRouterKey(routerID string) string {
	sum := sha256.Sum256([]byte("complexity-capture-router\x00" + routerID))
	return hex.EncodeToString(sum[:8])
}

// CaptureNamespaceRouterKey returns the router key of a capture namespace,
// and false for one that is not a router's: one written before captures were
// per router.
func CaptureNamespaceRouterKey(namespace string) (string, bool) {
	rest, ok := strings.CutPrefix(namespace, CaptureNamespacePrefix+"_")
	if !ok {
		return "", false
	}
	key, space, ok := strings.Cut(rest, "_")
	if !ok || len(key) != 16 || len(space) != 32 || !isHex(key) || !isHex(space) {
		return "", false
	}
	return key, true
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

type captureItem struct {
	id        string
	vector    []float32
	store     vectorstore.VectorStore
	namespace string
	dimension int
	at        int64
}

// RequestCapture samples classified requests into a vector store.
type RequestCapture struct {
	logger schemas.Logger
	// threshold is the sampling cut on a uniform uint64: 0 is off,
	// math.MaxUint64 captures everything. Read on the request path.
	threshold atomic.Uint64
	queue     chan captureItem
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	// ctx scopes every write to the capture's lifetime; cancel interrupts
	// what is still running once Close's drain budget is spent.
	ctx    context.Context
	cancel context.CancelFunc
	// drainBudget is how long Close lets queued samples finish writing.
	drainBudget time.Duration
	// closeMu serializes offer against Close: a sample is either accepted
	// while the writer still drains the queue, or refused.
	closeMu sync.RWMutex
	closed  bool
	// deletes keeps the deletes Forget could not do, for RetryDeletes.
	// Optional: without it a failed delete is logged and left to Sweep.
	deletes CaptureDeleteRecorder

	queued, dropped, written, failed, forgotten atomic.Int64

	// pendingMu guards pending (request IDs queued or being written) and
	// forgottenIDs (those of them Forget was asked to delete); see writeOne.
	pendingMu    sync.Mutex
	pending      map[string]int
	forgottenIDs map[string]struct{}

	// mu guards which namespace the writer has created.
	mu        sync.Mutex
	store     vectorstore.VectorStore
	namespace string
	ensured   bool
}

// NewRequestCapture returns a capture that is off until Configure turns it on,
// with its writer running.
func NewRequestCapture(logger schemas.Logger) *RequestCapture {
	ctx, cancel := context.WithCancel(context.Background())
	c := &RequestCapture{
		logger:       logger,
		queue:        make(chan captureItem, captureQueueSize),
		done:         make(chan struct{}),
		ctx:          ctx,
		cancel:       cancel,
		drainBudget:  captureDrainBudget,
		pending:      make(map[string]int),
		forgottenIDs: make(map[string]struct{}),
	}
	c.wg.Go(c.run)
	return c
}

// Configure applies a config; nil or a non-positive rate turns capture off.
func (c *RequestCapture) Configure(cfg *CaptureConfig) {
	if c == nil {
		return
	}
	var t uint64
	switch {
	case cfg == nil || cfg.SampleRate <= 0 || math.IsNaN(cfg.SampleRate):
		t = 0
	case cfg.SampleRate >= 1:
		t = math.MaxUint64
	default:
		t = uint64(cfg.SampleRate * float64(math.MaxUint64))
	}
	c.threshold.Store(t)
}

// Enabled reports whether capture is sampling.
func (c *RequestCapture) Enabled() bool {
	return c != nil && c.threshold.Load() != 0
}

// sampledAt reports whether a request falls in the sample a threshold cuts.
// The draw is a hash of the request ID, not a random number, so the same
// request is always in or out of a router's sample: a suggestion run's
// backfill from logs then picks exactly the requests live capture would
// have, and fills the gaps of a window without sampling it twice. FNV-1a,
// finished with SplitMix64 so IDs that differ only in their last characters
// still spread evenly, without allocating on the request path.
func sampledAt(requestID string, t uint64) bool {
	if t == math.MaxUint64 {
		return true
	}
	h := uint64(14695981039346656037)
	for i := 0; i < len(requestID); i++ {
		h ^= uint64(requestID[i])
		h *= 1099511628211
	}
	h ^= h >> 30
	h *= 0xbf58476d1ce4e5b9
	h ^= h >> 27
	h *= 0x94d049bb133111eb
	h ^= h >> 31
	return h < t
}

// CaptureSampled reports whether live capture at rate would keep the request,
// for a backfill that must pick the same requests.
func CaptureSampled(requestID string, rate float64) bool {
	if rate <= 0 || math.IsNaN(rate) {
		return false
	}
	if rate >= 1 {
		return sampledAt(requestID, math.MaxUint64)
	}
	return sampledAt(requestID, uint64(rate*float64(math.MaxUint64)))
}

// HoldForBackfill marks requests a backfill is about to read and write as
// pending, as a queued live sample is, so a Forget that runs while their
// backfill is in flight is applied to it: WriteBackfilled skips a request
// forgotten before its write, and deletes one forgotten while it was being
// written. Hold before the request's text is read, and give every held
// request back through WriteBackfilled or ReleaseBackfill.
func (c *RequestCapture) HoldForBackfill(ids []string) {
	if c == nil || len(ids) == 0 {
		return
	}
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for _, id := range ids {
		c.pending[id]++
	}
}

// ReleaseBackfill gives back held requests the backfill will not write.
func (c *RequestCapture) ReleaseBackfill(ids []string) {
	if c == nil {
		return
	}
	for _, id := range ids {
		c.donePending(id)
	}
}

// WriteBackfilled stores the vectors of requests embedded after the fact from
// their logs, under the same point IDs and metadata live capture uses, so a
// later backfill or a live write of the same request overwrites rather than
// doubles it. Each request is stamped with when it was logged, which is what
// windows and the age sweep read. Every request must have been held with
// HoldForBackfill; each is given back here, whether or not it was written, so
// a request whose log was deleted in the meantime leaves no vector behind.
func (c *RequestCapture) WriteBackfilled(ctx context.Context, store vectorstore.VectorStore, namespace string, dimension int, requests []CapturedRequest) error {
	if len(requests) == 0 {
		return nil
	}
	if c == nil {
		return errors.New("no request capture to backfill into")
	}
	written := 0
	defer func() {
		for _, r := range requests[written:] {
			c.donePending(r.LogID)
		}
	}()
	if store == nil || namespace == "" {
		return nil
	}
	if err := c.ensure(ctx, store, namespace, dimension); err != nil {
		return err
	}
	for i, r := range requests {
		if c.isForgotten(r.LogID) {
			written = i + 1
			c.donePending(r.LogID)
			c.forgotten.Add(1)
			continue
		}
		err := store.Add(ctx, namespace, capturePointID(r.LogID), r.Vector, map[string]any{
			captureMetadataKind:      captureMetadataKindValue,
			captureMetadataTimestamp: r.At.Unix(),
			captureMetadataLogID:     r.LogID,
		})
		if err != nil {
			c.mu.Lock()
			c.ensured = false
			c.mu.Unlock()
			return fmt.Errorf("add backfilled vector: %w", err)
		}
		written = i + 1
		if c.donePending(r.LogID) {
			c.undo(captureItem{id: r.LogID, store: store, namespace: namespace})
			c.forgotten.Add(1)
		}
	}
	return nil
}

// offer is called on the request path once a request has been classified. It
// never blocks and never allocates on its own: off is one atomic load, and a
// sampled request costs a random draw, a context lookup and a channel send.
// vector must not be modified afterwards; the classifier allocates a fresh one
// per request.
func (c *RequestCapture) offer(ctx context.Context, store vectorstore.VectorStore, namespace string, dimension int, vector []float32) {
	if c == nil {
		return
	}
	t := c.threshold.Load()
	if t == 0 || store == nil || namespace == "" || len(vector) == 0 {
		return
	}
	id, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if id == "" || !sampledAt(id, t) {
		return
	}
	item := captureItem{id: id, vector: vector, store: store, namespace: namespace, dimension: dimension, at: time.Now().Unix()}
	c.closeMu.RLock()
	defer c.closeMu.RUnlock()
	if c.closed {
		return
	}
	// The send and the pending mark happen under one lock, so the writer
	// cannot take the item before Forget can see it is pending.
	c.pendingMu.Lock()
	select {
	case c.queue <- item:
		c.pending[id]++
		c.pendingMu.Unlock()
		c.queued.Add(1)
	default:
		c.pendingMu.Unlock()
		c.dropped.Add(1)
	}
}

func (c *RequestCapture) run() {
	for {
		select {
		case <-c.done:
			// Drain what was accepted: offer told the request its sample was
			// taken, and the write context still bounds each one.
			for {
				select {
				case item := <-c.queue:
					c.writeOne(item)
				default:
					return
				}
			}
		case item := <-c.queue:
			c.writeOne(item)
		}
	}
}

// writeOne writes one sample unless its log was deleted in the meantime. Forget
// marks a request ID that is still pending as forgotten before it deletes, so
// whichever runs first, the vector does not outlive the delete: a sample
// forgotten before its write is skipped, and one forgotten while its write was
// running is deleted again here once the write lands, in the namespace it
// went to. The marks are cleared when the ID's last pending sample is done,
// so they never outnumber the queue.
func (c *RequestCapture) writeOne(item captureItem) {
	if c.isForgotten(item.id) {
		c.donePending(item.id)
		c.forgotten.Add(1)
		return
	}
	err := c.write(item)
	if c.donePending(item.id) {
		if err == nil {
			c.undo(item)
		}
		c.forgotten.Add(1)
		return
	}
	if err != nil {
		c.failed.Add(1)
		if c.logger != nil {
			c.logger.Debug("[Routing] complexity request capture write failed: %v", err)
		}
		return
	}
	c.written.Add(1)
}

func (c *RequestCapture) isForgotten(id string) bool {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	_, ok := c.forgottenIDs[id]
	return ok
}

// donePending marks one sample of id as no longer pending and reports whether
// id was forgotten.
func (c *RequestCapture) donePending(id string) bool {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	_, forgotten := c.forgottenIDs[id]
	if c.pending[id] <= 1 {
		delete(c.pending, id)
		delete(c.forgottenIDs, id)
	} else {
		c.pending[id]--
	}
	return forgotten
}

// undo deletes a vector whose log was deleted while it was being written.
func (c *RequestCapture) undo(item captureItem) {
	ctx, cancel := context.WithTimeout(c.ctx, captureWriteTimeout)
	defer cancel()
	err := item.store.Delete(ctx, item.namespace, capturePointID(item.id))
	if err == nil || errors.Is(err, vectorstore.ErrNotFound) {
		return
	}
	c.recordDeletes(ctx, []CaptureDelete{{LogID: item.id, Namespace: item.namespace}}, err)
}

func (c *RequestCapture) write(item captureItem) error {
	ctx, cancel := context.WithTimeout(c.ctx, captureWriteTimeout)
	defer cancel()
	if err := c.ensure(ctx, item.store, item.namespace, item.dimension); err != nil {
		return err
	}
	err := item.store.Add(ctx, item.namespace, capturePointID(item.id), item.vector, map[string]any{
		captureMetadataKind:      captureMetadataKindValue,
		captureMetadataTimestamp: item.at,
		captureMetadataLogID:     item.id,
	})
	if err != nil {
		// The namespace may have been removed underneath (a peer dropped an
		// old embedding space, an operator cleaned up): create it again on
		// the next write.
		c.mu.Lock()
		c.ensured = false
		c.mu.Unlock()
		return fmt.Errorf("add captured vector: %w", err)
	}
	return nil
}

// ensure creates the namespace once per (store, namespace). It never touches
// other capture namespaces; Sweep ages those out.
func (c *RequestCapture) ensure(ctx context.Context, store vectorstore.VectorStore, namespace string, dimension int) error {
	c.mu.Lock()
	same := c.ensured && c.store == store && c.namespace == namespace
	c.mu.Unlock()
	if same {
		return nil
	}
	if err := store.CreateNamespace(ctx, namespace, dimension, captureProperties); err != nil {
		return fmt.Errorf("create capture namespace: %w", err)
	}
	c.mu.Lock()
	c.store, c.namespace, c.ensured = store, namespace, true
	c.mu.Unlock()
	return nil
}

// CaptureDelete is a deleted log whose captured vector could not be removed
// from one namespace. A row is identified by the pair: one log can have rows
// for several namespaces.
type CaptureDelete struct {
	LogID     string
	Namespace string // empty when the namespaces could not be listed: retried in every capture namespace
}

// CaptureDeleteRecorder keeps failed captured-vector deletes so they are
// retried until they go. The log store implements it.
type CaptureDeleteRecorder interface {
	AddCaptureDeletes(ctx context.Context, rows []CaptureDelete) error
	// ListCaptureDeletes returns up to limit rows, oldest first.
	ListCaptureDeletes(ctx context.Context, limit int) ([]CaptureDelete, error)
	// DeleteCaptureDeletes removes exactly the given (LogID, Namespace) rows;
	// a row of the same log in another namespace stays.
	DeleteCaptureDeletes(ctx context.Context, rows []CaptureDelete) error
}

// SetDeleteRecorder supplies where failed deletes are kept for retry.
func (c *RequestCapture) SetDeleteRecorder(r CaptureDeleteRecorder) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.deletes = r
	c.mu.Unlock()
}

// Forget deletes the captured vectors of deleted logs, in every capture
// namespace of store: the serving one (empty when nothing is serving) and
// those of earlier embedding spaces, which still hold vectors until Sweep ages
// them out. store is the configured vector store, whatever is serving; nil
// means none is configured, and captures are never written anywhere else.
// Samples of these logs still queued or being written are dropped too (see
// writeOne).
//
// It never fails the log delete that was asked for, which has already
// happened. A vector already gone is not an error; one it cannot remove - the
// store is down, the request was cancelled - is recorded for RetryDeletes, and
// anything that cannot even be recorded still ages out through Sweep.
// Suggestions skip vectors whose log is gone, so a vector left behind is never
// shown, only kept too long.
func (c *RequestCapture) Forget(ctx context.Context, store vectorstore.VectorStore, namespace string, ids []string) {
	if c == nil || len(ids) == 0 {
		return
	}
	c.markForgotten(ids)
	if store == nil {
		return
	}
	var failed []CaptureDelete
	namespaces, err := captureNamespaces(ctx, store, namespace)
	if err != nil {
		for _, id := range ids {
			failed = append(failed, CaptureDelete{LogID: id})
		}
		c.recordDeletes(ctx, failed, err)
		return
	}
	pairs := make([]CaptureDelete, 0, len(namespaces)*len(ids))
	for _, ns := range namespaces {
		for _, id := range ids {
			pairs = append(pairs, CaptureDelete{LogID: id, Namespace: ns})
		}
	}
	if failed, err := deleteCaptured(ctx, store, pairs); len(failed) > 0 {
		c.recordDeletes(ctx, failed, err)
	}
}

// captureForgetWorkers bounds the deletes one Forget runs at once.
const captureForgetWorkers = 8

// deleteCaptured deletes each (log, namespace) pair, a few at a time, and
// returns the pairs it did not delete with the first error. A vector already
// gone counts as deleted. The first pair goes alone, so a store that is down
// is asked once; after that, once any delete fails the pairs not yet started
// are returned without trying: the store that refused one, or the context
// that ended, refuses them all. Deletes already running when one fails finish
// and count as they come out.
func deleteCaptured(ctx context.Context, store vectorstore.VectorStore, pairs []CaptureDelete) ([]CaptureDelete, error) {
	del := func(p CaptureDelete) error {
		err := store.Delete(ctx, p.Namespace, capturePointID(p.LogID))
		if errors.Is(err, vectorstore.ErrNotFound) {
			return nil
		}
		return err
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	if err := del(pairs[0]); err != nil {
		return pairs, err
	}
	rest := pairs[1:]
	var (
		mu       sync.Mutex
		failed   []CaptureDelete
		firstErr error
		next     int
		wg       sync.WaitGroup
	)
	// take hands out the next pair, or reports there is none to start: all
	// were handed out, or one failed and the rest are recorded untried.
	take := func() (CaptureDelete, bool) {
		mu.Lock()
		defer mu.Unlock()
		if next >= len(rest) {
			return CaptureDelete{}, false
		}
		if firstErr != nil {
			failed = append(failed, rest[next:]...)
			next = len(rest)
			return CaptureDelete{}, false
		}
		p := rest[next]
		next++
		return p, true
	}
	for range min(captureForgetWorkers, len(rest)) {
		wg.Go(func() {
			for {
				p, ok := take()
				if !ok {
					return
				}
				if err := del(p); err != nil {
					mu.Lock()
					failed = append(failed, p)
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
			}
		})
	}
	wg.Wait()
	return failed, firstErr
}

// markForgotten marks the IDs among ids that still have a sample pending, so
// the writer drops it. The lock is released between chunks so a large delete
// does not hold up offer.
func (c *RequestCapture) markForgotten(ids []string) {
	const chunk = 1024
	for start := 0; start < len(ids); start += chunk {
		c.pendingMu.Lock()
		if len(c.pending) == 0 {
			c.pendingMu.Unlock()
			return
		}
		for _, id := range ids[start:min(start+chunk, len(ids))] {
			if c.pending[id] > 0 {
				c.forgottenIDs[id] = struct{}{}
			}
		}
		c.pendingMu.Unlock()
	}
}

// captureNamespaces lists every capture namespace in store, plus serving when
// it is set and not listed yet.
func captureNamespaces(ctx context.Context, store vectorstore.VectorStore, serving string) ([]string, error) {
	namespaces, err := store.ListNamespaces(ctx, CaptureNamespacePrefix+"_")
	if err != nil {
		return nil, fmt.Errorf("list capture namespaces: %w", err)
	}
	if serving != "" && !slices.Contains(namespaces, serving) {
		namespaces = append(namespaces, serving)
	}
	return namespaces, nil
}

// recordDeletes keeps the deletes Forget could not do, on a context of its
// own: the caller's may be the reason they failed.
func (c *RequestCapture) recordDeletes(ctx context.Context, failed []CaptureDelete, cause error) {
	c.mu.Lock()
	recorder := c.deletes
	c.mu.Unlock()
	if recorder == nil {
		c.warnf("failed to remove the captured vectors of %d deleted log(s), and there is nowhere to retry them from; they age out with retention: %v", len(failed), cause)
		return
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), captureRecordTimeout)
	defer cancel()
	if err := recorder.AddCaptureDeletes(recordCtx, failed); err != nil {
		c.warnf("failed to remove the captured vectors of %d deleted log(s) (%v), and could not record them to retry: %v", len(failed), cause, err)
		return
	}
	c.warnf("failed to remove the captured vectors of %d deleted log(s); upkeep will retry: %v", len(failed), cause)
}

// RetryDeletes retries recorded deletes, oldest first, and reports how many
// rows are now done. A row recorded without a namespace is retried in every
// capture namespace (namespace is the serving one, empty when nothing
// serves). It stops at the first failure: the store that refused one is most
// likely refusing all of them, and the rest wait for the next pass.
func (c *RequestCapture) RetryDeletes(ctx context.Context, store vectorstore.VectorStore, namespace string) (int, error) {
	if c == nil || store == nil {
		return 0, nil
	}
	c.mu.Lock()
	recorder := c.deletes
	c.mu.Unlock()
	if recorder == nil {
		return 0, nil
	}
	rows, err := recorder.ListCaptureDeletes(ctx, captureDeleteRetryBatch)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	done := make([]CaptureDelete, 0, len(rows))
	// A ClickHouse-backed recorder can list a row recorded twice until it
	// merges. A row is its (log, namespace) pair: the same log in another
	// namespace is a different delete.
	seen := make(map[CaptureDelete]struct{}, len(rows))
	var all []string // every capture namespace, listed once for unscoped rows
	var failure error
rows:
	for _, row := range rows {
		if _, repeat := seen[row]; repeat {
			continue
		}
		seen[row] = struct{}{}
		targets := []string{row.Namespace}
		if row.Namespace == "" {
			if all == nil {
				if all, failure = captureNamespaces(ctx, store, namespace); failure != nil {
					break
				}
			}
			targets = all
		}
		for _, ns := range targets {
			if err := store.Delete(ctx, ns, capturePointID(row.LogID)); err != nil && !errors.Is(err, vectorstore.ErrNotFound) {
				failure = err
				break rows
			}
		}
		done = append(done, row)
	}
	if len(done) > 0 {
		if err := recorder.DeleteCaptureDeletes(ctx, done); err != nil {
			return 0, fmt.Errorf("clear retried capture deletes: %w", err)
		}
	}
	if failure != nil {
		return len(done), fmt.Errorf("retry capture delete: %w", failure)
	}
	return len(done), nil
}

func (c *RequestCapture) warnf(format string, args ...any) {
	if c.logger != nil {
		c.logger.Warn("[Routing] "+format, args...)
	}
}

// Sweep deletes captured vectors older than cutoff, in the serving namespace
// and in every other capture namespace in the store, and reports how many
// went. Namespaces of earlier embedding spaces are emptied this way rather
// than dropped, so one still being written (by this node's queue or by a peer
// on that space) never loses a vector younger than retention. A namespace that
// fails, wholly or for some of its deletes, does not stop the rest: the count
// is of vectors actually deleted, returned with every failure joined.
func (c *RequestCapture) Sweep(ctx context.Context, store vectorstore.VectorStore, namespace string, cutoff time.Time) (int, error) {
	if store == nil {
		return 0, nil
	}
	namespaces, err := captureNamespaces(ctx, store, namespace)
	if err != nil {
		return 0, err
	}
	return SweepCaptureNamespaces(ctx, store, namespaces, cutoff)
}

// SweepCaptureNamespaces deletes captured vectors older than cutoff in the
// given namespaces, for an upkeep that keeps each router's captures for its
// own lookback. A namespace that is gone is skipped.
func SweepCaptureNamespaces(ctx context.Context, store vectorstore.VectorStore, namespaces []string, cutoff time.Time) (int, error) {
	if store == nil {
		return 0, nil
	}
	var total int
	var errs []error
	for _, ns := range namespaces {
		results, err := store.DeleteAll(ctx, ns, []vectorstore.Query{
			{Field: captureMetadataKind, Operator: vectorstore.QueryOperatorEqual, Value: captureMetadataKindValue},
			{Field: captureMetadataTimestamp, Operator: vectorstore.QueryOperatorLessThan, Value: cutoff.Unix()},
		})
		if errors.Is(err, vectorstore.ErrNotFound) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("sweep capture namespace %s: %w", ns, err))
			continue
		}
		var failed int
		var firstFailure string
		for _, r := range results {
			if r.Status == vectorstore.DeleteStatusSuccess {
				total++
				continue
			}
			if failed == 0 {
				firstFailure = r.Error
			}
			failed++
		}
		if failed > 0 {
			errs = append(errs, fmt.Errorf("sweep capture namespace %s: %d of %d deletes failed: %s", ns, failed, len(results), firstFailure))
		}
	}
	return total, errors.Join(errs...)
}

// ListCaptureNamespaces lists every capture namespace in store.
func ListCaptureNamespaces(ctx context.Context, store vectorstore.VectorStore) ([]string, error) {
	return captureNamespaces(ctx, store, "")
}

// CaptureStore returns where captured requests may be kept: store, unless it
// is missing or Chromem (see SemanticClassifier.captureStoreLocked).
func CaptureStore(store vectorstore.VectorStore) vectorstore.VectorStore {
	if store == nil || isLocalChromemStore(store) {
		return nil
	}
	return store
}

// MarkForgotten makes the writer drop samples of ids it still has queued,
// whose logs were deleted, without deleting anything from the store: a
// plugin with a capture per router marks every router's, then deletes once.
func (c *RequestCapture) MarkForgotten(ids []string) {
	if c == nil || len(ids) == 0 {
		return
	}
	c.markForgotten(ids)
}

// DropRouter deletes every capture namespace of one model router, for a
// router that was deleted.
func (c *RequestCapture) DropRouter(ctx context.Context, store vectorstore.VectorStore, routerID string) error {
	if store == nil || routerID == "" {
		return nil
	}
	namespaces, err := store.ListNamespaces(ctx, CaptureRouterPrefix(routerID))
	if err != nil {
		return fmt.Errorf("list capture namespaces of router %s: %w", routerID, err)
	}
	var errs []error
	for _, ns := range namespaces {
		if err := store.DeleteNamespace(ctx, ns); err != nil && !errors.Is(err, vectorstore.ErrNotFound) {
			errs = append(errs, fmt.Errorf("drop capture namespace %s: %w", ns, err))
		}
	}
	if c != nil {
		c.mu.Lock()
		c.ensured = false
		c.mu.Unlock()
	}
	return errors.Join(errs...)
}

// Stats reports what capture has done since it started.
func (c *RequestCapture) Stats() CaptureStats {
	if c == nil {
		return CaptureStats{}
	}
	return CaptureStats{Enabled: c.Enabled(), Queued: c.queued.Load(), Dropped: c.dropped.Load(), Written: c.written.Load(), Failed: c.failed.Load(), Forgotten: c.forgotten.Load()}
}

// Close stops sampling and lets the writer finish what was already queued,
// within a budget; past it, writes still running are cancelled.
func (c *RequestCapture) Close() {
	if c == nil {
		return
	}
	c.Configure(nil)
	c.closeMu.Lock()
	c.closed = true
	c.closeMu.Unlock()
	c.closeOnce.Do(func() { close(c.done) })

	drained := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(c.drainBudget):
		c.warnf("request capture did not finish writing within %s; cancelling the rest", c.drainBudget)
		c.cancel()
		<-drained
	}
	c.cancel()
}

// CapturedRequest is one captured request read back for a suggestion run.
type CapturedRequest struct {
	LogID  string
	Vector []float32
	At     time.Time
}

// captureReadPage is how many captured vectors one read returns: about 3 MB
// at 1536 dimensions.
const captureReadPage = 500

// captureSearchCeiling is how many rows a read without vectors can return
// before it may have stopped short: Redis serves that read by search, which
// answers nothing past MAXSEARCHRESULTS.
const captureSearchCeiling = vectorstore.RedisMaxSearchResults

// EachCaptured reads the requests captured from from through to from
// namespace, with their vectors, a page at a time, in whatever order the
// store pages them. Capture times are whole seconds, so both ends include
// their second: a window ending now includes what was captured this second.
// Entries without a vector or log ID are skipped; fn's error stops the read.
//
// The namespace is read whole, unfiltered, and the window applied here.
// Filtered reads stop at a ceiling on Redis (MAXSEARCHRESULTS) and Weaviate
// (QUERY_MAXIMUM_RESULTS), 10,000 by default, past which Redis reports
// nothing more, which reads as the end of the window. The unfiltered read is
// each backend's complete walk, and it costs little extra: upkeep sweeps a
// capture namespace down to the lookback, which is the window a run reads. A
// walk can hand an entry back twice (a Redis SCAN while capture writes), so
// each log ID is taken once.
func EachCaptured(ctx context.Context, store vectorstore.VectorStore, namespace string, from, to time.Time, fn func([]CapturedRequest) error) error {
	if store == nil || namespace == "" {
		return nil
	}
	ctx = vectorstore.WithIncludeVectors(ctx)
	fromUnix, toUnix := from.Unix(), to.Unix()
	fields := []string{captureMetadataKind, captureMetadataTimestamp, captureMetadataLogID}
	seen := map[string]struct{}{}
	var cursor *string
	for {
		results, next, err := store.GetAll(ctx, namespace, nil, fields, cursor, captureReadPage)
		// A namespace missing from the start holds nothing yet. One that goes
		// after a page was read (dropped with the routing plugin, or by an
		// operator) leaves the window partial, and a run that took it as
		// whole would supersede suggestions on traffic it never saw.
		if errors.Is(err, vectorstore.ErrNotFound) && cursor == nil {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read captured requests: %w", err)
		}
		page := make([]CapturedRequest, 0, len(results))
		for _, r := range results {
			// The point ID is derived from the log ID (capturePointID); the
			// log ID itself is in the metadata.
			logID, _ := r.Properties[captureMetadataLogID].(string)
			if len(r.Vector) == 0 || logID == "" {
				continue
			}
			// Only capture writes here; a kind is checked where the store
			// returns one, never required.
			if kind, ok := r.Properties[captureMetadataKind].(string); ok && kind != captureMetadataKindValue {
				continue
			}
			at := captureUnix(r.Properties[captureMetadataTimestamp])
			if at < fromUnix || at > toUnix {
				continue
			}
			if _, repeat := seen[logID]; repeat {
				continue
			}
			seen[logID] = struct{}{}
			page = append(page, CapturedRequest{LogID: logID, Vector: r.Vector, At: time.Unix(at, 0).UTC()})
		}
		if len(page) > 0 {
			if err := fn(page); err != nil {
				return err
			}
		}
		if next == nil || *next == "" || len(results) == 0 {
			return nil
		}
		cursor = next
	}
}

// captureUnix reads a stored unix-seconds property, which backends hand back
// as whichever numeric type they keep.
func captureUnix(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

// CountCaptured counts the requests captured from from through to, reading
// only their kind and timestamp, so a run can be sized before it reads any
// vector.
//
// The namespace is read unfiltered and the window applied here, as
// EachCaptured does: Weaviate pages a filtered read by offset and fails it
// past QUERY_MAXIMUM_RESULTS, 10,000 by default, which a busy router's window
// passes.
//
// Not every store reads a namespace out whole without its vectors. Pinecone's
// read returns one page and no cursor, and Redis serves it by search, filtered
// or not, which answers nothing past MAXSEARCHRESULTS, 10,000 by default. So
// a full page with no cursor, or a read that returned that many rows, does
// not prove the count complete, and the window is counted again through the
// vector read, which every store pages to the end (EachCaptured relies on
// it). Only that recount reads vectors, a page at a time; a store whose plain
// read is complete past 10,000 rows pays for it too.
func CountCaptured(ctx context.Context, store vectorstore.VectorStore, namespace string, from, to time.Time) (int, error) {
	if store == nil || namespace == "" {
		return 0, nil
	}
	return countCaptured(ctx, store, namespace, from, to, captureSearchCeiling)
}

// countCaptured is CountCaptured with the plain read's ceiling given.
func countCaptured(ctx context.Context, store vectorstore.VectorStore, namespace string, from, to time.Time, ceiling int) (int, error) {
	count, complete, err := countCapturedPages(ctx, store, namespace, from, to, ceiling)
	if err != nil || complete {
		return count, err
	}
	count, _, err = countCapturedPages(vectorstore.WithIncludeVectors(ctx), store, namespace, from, to, ceiling)
	return count, err
}

// countCapturedPages counts by paging GetAll. complete is false when a read
// without vectors ended on a full page with no cursor, which a store that
// cannot page that read also returns, or returned ceiling rows or more, where
// a search-backed read may have stopped. The vector read is a walk that can
// hand an entry back twice (see EachCaptured), so it takes each log ID once.
func countCapturedPages(ctx context.Context, store vectorstore.VectorStore, namespace string, from, to time.Time, ceiling int) (count int, complete bool, err error) {
	fromUnix, toUnix := from.Unix(), to.Unix()
	vectors := vectorstore.IncludeVectorsRequested(ctx)
	fields := []string{captureMetadataKind, captureMetadataTimestamp}
	var seen map[string]struct{}
	if vectors {
		fields = append(fields, captureMetadataLogID)
		seen = map[string]struct{}{}
	}
	rows := 0
	var cursor *string
	for {
		results, next, err := store.GetAll(ctx, namespace, nil, fields, cursor, captureReadPage)
		// As in EachCaptured: a namespace missing from the start holds
		// nothing yet, but one that goes after a page was read leaves the
		// count partial, which is not passed off as the whole window.
		if errors.Is(err, vectorstore.ErrNotFound) && cursor == nil {
			return count, true, nil
		}
		if err != nil {
			return count, false, fmt.Errorf("count captured requests: %w", err)
		}
		rows += len(results)
		for _, r := range results {
			// As in EachCaptured: a kind is checked where the store returns
			// one, never required.
			if kind, ok := r.Properties[captureMetadataKind].(string); ok && kind != captureMetadataKindValue {
				continue
			}
			if at := captureUnix(r.Properties[captureMetadataTimestamp]); at < fromUnix || at > toUnix {
				continue
			}
			if logID, _ := r.Properties[captureMetadataLogID].(string); seen != nil && logID != "" {
				if _, repeat := seen[logID]; repeat {
					continue
				}
				seen[logID] = struct{}{}
			}
			count++
		}
		if len(results) == 0 || next == nil || *next == "" {
			if vectors {
				return count, true, nil
			}
			return count, len(results) < captureReadPage && rows < ceiling, nil
		}
		cursor = next
	}
}
