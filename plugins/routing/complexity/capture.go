package complexity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
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
// Only the vector and routing metadata are stored, under the request ID, which
// is also the request's log ID. The text stays in the logs and is read from
// there when suggestions run, so a log that is deleted, or whose content is
// hidden, takes its text with it. Forget removes the vectors of deleted logs
// and Sweep removes vectors older than the logs' retention.
//
// The namespace is keyed by the embedding space (provider, model, dimension),
// not by the phrase generation: editing phrases does not change what a request
// vector means, so captured traffic survives phrase edits, while a new
// embedding model starts a fresh namespace because old vectors are not
// comparable with new ones.

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
)

var captureProperties = map[string]vectorstore.VectorStoreProperties{
	captureMetadataKind:      {DataType: vectorstore.VectorStorePropertyTypeString, Description: "record kind"},
	captureMetadataTimestamp: {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "capture time, unix seconds"},
}

// CaptureConfig turns request capture on. A nil config turns it off.
type CaptureConfig struct {
	// SampleRate is the share of classified requests captured, in (0, 1].
	SampleRate float64
}

// CaptureStats counts what capture did since it started.
type CaptureStats struct {
	Enabled bool
	Queued  int64 // accepted onto the queue
	Dropped int64 // sampled but dropped because the queue was full
	Written int64
	Failed  int64
}

// captureInputVersion names how request text is turned into the embedded
// input (SemanticInputText and the extraction feeding it). Bump it when that
// changes: vectors of the old input are not comparable with the new.
const captureInputVersion = 1

// CaptureNamespace names the capture namespace for one embedding space: the
// embedding model, and the slice of each request it embeds. Two routers share
// captured vectors only when both match, because only then is the vector the
// same; a router that embeds more turns of history writes to its own space.
//
// A captured vector carries no routing verdict (tier, similarity): those are
// one router's view of the request, they are on its log, and a suggestion run
// recomputes them against its router's phrase bank. The underscore +
// hexadecimal suffix is accepted by every backend.
func CaptureNamespace(provider schemas.ModelProvider, model string, dimension, messageHistoryCount int) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "complexity-capture-v2\x00%s\x00%s\x00%d\x00%d\x00%d",
		provider, model, dimension, max(1, messageHistoryCount), captureInputVersion))
	return CaptureNamespacePrefix + "_" + hex.EncodeToString(sum[:16])
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
	// liveNamespaces names every capture namespace a serving router still
	// writes to; ensure keeps those and deletes the rest. Nil means only the
	// namespace being ensured is live.
	liveNamespaces func() []string
	// deletes keeps the deletes Forget could not do, for RetryDeletes.
	// Optional: without it a failed delete is logged and left to Sweep.
	deletes CaptureDeleteRecorder

	queued, dropped, written, failed atomic.Int64

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
		logger:      logger,
		queue:       make(chan captureItem, captureQueueSize),
		done:        make(chan struct{}),
		ctx:         ctx,
		cancel:      cancel,
		drainBudget: captureDrainBudget,
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
	if t == 0 || store == nil || len(vector) == 0 {
		return
	}
	if t != math.MaxUint64 && rand.Uint64() >= t {
		return
	}
	id, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if id == "" {
		return
	}
	item := captureItem{id: id, vector: vector, store: store, namespace: namespace, dimension: dimension, at: time.Now().Unix()}
	c.closeMu.RLock()
	defer c.closeMu.RUnlock()
	if c.closed {
		return
	}
	select {
	case c.queue <- item:
		c.queued.Add(1)
	default:
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

func (c *RequestCapture) writeOne(item captureItem) {
	if err := c.write(item); err != nil {
		c.failed.Add(1)
		if c.logger != nil {
			c.logger.Debug("[Routing] complexity request capture write failed: %v", err)
		}
		return
	}
	c.written.Add(1)
}

func (c *RequestCapture) write(item captureItem) error {
	ctx, cancel := context.WithTimeout(c.ctx, captureWriteTimeout)
	defer cancel()
	if err := c.ensure(ctx, item.store, item.namespace, item.dimension); err != nil {
		return err
	}
	err := item.store.Add(ctx, item.namespace, item.id, item.vector, map[string]any{
		captureMetadataKind:      captureMetadataKindValue,
		captureMetadataTimestamp: item.at,
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

// ensure creates the namespace once per (store, namespace). When the
// embedding space changes, the namespaces of earlier spaces are deleted:
// their vectors cannot be compared with the new ones, so suggestions could
// never use them.
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
	if existing, err := store.ListNamespaces(ctx, CaptureNamespacePrefix); err == nil {
		live := map[string]bool{namespace: true}
		c.mu.Lock()
		liveFn := c.liveNamespaces
		c.mu.Unlock()
		if liveFn != nil {
			for _, ns := range liveFn() {
				live[ns] = true
			}
		}
		for _, ns := range existing {
			if !live[ns] {
				if err := store.DeleteNamespace(ctx, ns); err != nil && c.logger != nil {
					c.logger.Debug("[Routing] could not delete stale capture namespace %s: %v", ns, err)
				}
			}
		}
	}
	c.mu.Lock()
	c.store, c.namespace, c.ensured = store, namespace, true
	c.mu.Unlock()
	return nil
}

// SetLiveNamespaces tells capture which namespaces serving routers still use,
// so switching one router's embedding space never deletes another's. With
// one router the namespace being written is the only live one, which is the
// default.
func (c *RequestCapture) SetLiveNamespaces(live func() []string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.liveNamespaces = live
	c.ensured = false // re-check what to keep on the next write
	c.mu.Unlock()
}

// CaptureDelete is a deleted log whose captured vector could not be removed.
type CaptureDelete struct {
	LogID     string
	Namespace string // empty when the namespace was unknown; resolved on retry
}

// CaptureDeleteRecorder keeps failed captured-vector deletes so they are
// retried until they go. The log store implements it.
type CaptureDeleteRecorder interface {
	AddCaptureDeletes(ctx context.Context, rows []CaptureDelete) error
	// ListCaptureDeletes returns up to limit rows, oldest first.
	ListCaptureDeletes(ctx context.Context, limit int) ([]CaptureDelete, error)
	DeleteCaptureDeletes(ctx context.Context, logIDs []string) error
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

// Forget deletes the captured vectors of deleted logs. It never fails the
// log delete that was asked for, which has already happened. A vector already
// gone is not an error; one it cannot remove - the store is down, the request
// was cancelled - is recorded for RetryDeletes, and anything that cannot even
// be recorded still ages out through Sweep. Suggestions skip vectors whose log
// is gone, so a vector left behind is never shown, only kept too long.
func (c *RequestCapture) Forget(ctx context.Context, store vectorstore.VectorStore, namespace string, ids []string) {
	if c == nil || store == nil || len(ids) == 0 {
		return
	}
	var err error
	if namespace == "" {
		err = errors.New("no serving embedding space")
	}
	var failed []CaptureDelete
	for _, id := range ids {
		if err == nil {
			err = store.Delete(ctx, namespace, id)
			if err == nil || errors.Is(err, vectorstore.ErrNotFound) {
				err = nil
				continue
			}
		}
		// Once one fails the rest are recorded without trying: the store that
		// refused one, or the context that ended, refuses them all.
		failed = append(failed, CaptureDelete{LogID: id, Namespace: namespace})
	}
	if len(failed) > 0 {
		c.recordDeletes(ctx, failed, err)
	}
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
// vectors are now gone. Rows recorded without a namespace use namespace. It
// stops at the first failure: the store that refused one is most likely
// refusing all of them, and the rest wait for the next pass.
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
	done := make([]string, 0, len(rows))
	// A ClickHouse-backed recorder can list a log recorded twice until it merges.
	seen := make(map[string]struct{}, len(rows))
	var failure error
	for _, row := range rows {
		if _, repeat := seen[row.LogID]; repeat {
			continue
		}
		seen[row.LogID] = struct{}{}
		ns := row.Namespace
		if ns == "" {
			ns = namespace
		}
		if ns == "" {
			failure = errors.New("no serving embedding space to retry against")
			break
		}
		if err := store.Delete(ctx, ns, row.LogID); err != nil && !errors.Is(err, vectorstore.ErrNotFound) {
			failure = err
			break
		}
		done = append(done, row.LogID)
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

// Sweep deletes captured vectors older than cutoff and reports how many went.
func (c *RequestCapture) Sweep(ctx context.Context, store vectorstore.VectorStore, namespace string, cutoff time.Time) (int, error) {
	if store == nil || namespace == "" {
		return 0, nil
	}
	deleted, err := store.DeleteAll(ctx, namespace, []vectorstore.Query{
		{Field: captureMetadataKind, Operator: vectorstore.QueryOperatorEqual, Value: captureMetadataKindValue},
		{Field: captureMetadataTimestamp, Operator: vectorstore.QueryOperatorLessThan, Value: cutoff.Unix()},
	})
	if errors.Is(err, vectorstore.ErrNotFound) {
		return 0, nil
	}
	return len(deleted), err
}

// Stats reports what capture has done since it started.
func (c *RequestCapture) Stats() CaptureStats {
	if c == nil {
		return CaptureStats{}
	}
	return CaptureStats{Enabled: c.Enabled(), Queued: c.queued.Load(), Dropped: c.dropped.Load(), Written: c.written.Load(), Failed: c.failed.Load()}
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

// EachCaptured reads the requests captured from from through to from
// namespace, with their vectors, a page at a time, in whatever order the
// store pages them. Capture times are whole seconds, so both ends include
// their second: a window ending now includes what was captured this second.
// Entries without a vector are skipped; fn's error stops the read.
func EachCaptured(ctx context.Context, store vectorstore.VectorStore, namespace string, from, to time.Time, fn func([]CapturedRequest) error) error {
	if store == nil || namespace == "" {
		return nil
	}
	ctx = vectorstore.WithIncludeVectors(ctx)
	queries := []vectorstore.Query{
		{Field: captureMetadataKind, Operator: vectorstore.QueryOperatorEqual, Value: captureMetadataKindValue},
		{Field: captureMetadataTimestamp, Operator: vectorstore.QueryOperatorGreaterThanOrEqual, Value: from.Unix()},
		{Field: captureMetadataTimestamp, Operator: vectorstore.QueryOperatorLessThanOrEqual, Value: to.Unix()},
	}
	var cursor *string
	for {
		results, next, err := store.GetAll(ctx, namespace, queries, []string{captureMetadataTimestamp}, cursor, captureReadPage)
		if errors.Is(err, vectorstore.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read captured requests: %w", err)
		}
		page := make([]CapturedRequest, 0, len(results))
		for _, r := range results {
			if len(r.Vector) == 0 {
				continue
			}
			page = append(page, CapturedRequest{LogID: r.ID, Vector: r.Vector, At: time.Unix(captureUnix(r.Properties[captureMetadataTimestamp]), 0).UTC()})
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
// only their timestamps, so a run can be sized before it reads any vector.
func CountCaptured(ctx context.Context, store vectorstore.VectorStore, namespace string, from, to time.Time) (int, error) {
	if store == nil || namespace == "" {
		return 0, nil
	}
	queries := []vectorstore.Query{
		{Field: captureMetadataKind, Operator: vectorstore.QueryOperatorEqual, Value: captureMetadataKindValue},
		{Field: captureMetadataTimestamp, Operator: vectorstore.QueryOperatorGreaterThanOrEqual, Value: from.Unix()},
		{Field: captureMetadataTimestamp, Operator: vectorstore.QueryOperatorLessThanOrEqual, Value: to.Unix()},
	}
	var count int
	var cursor *string
	for {
		results, next, err := store.GetAll(ctx, namespace, queries, []string{captureMetadataTimestamp}, cursor, captureReadPage)
		if errors.Is(err, vectorstore.ErrNotFound) {
			return count, nil
		}
		if err != nil {
			return count, fmt.Errorf("count captured requests: %w", err)
		}
		count += len(results)
		if next == nil || *next == "" || len(results) == 0 {
			return count, nil
		}
		cursor = next
	}
}
