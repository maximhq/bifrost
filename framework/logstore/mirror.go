package logstore

import (
	"context"
	"fmt"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
)

const (
	defaultMirrorWorkers       = 4
	defaultMirrorQueueSize     = 2000
	defaultMaxMirrorQueueBytes = 256 << 20 // 256 MiB
)

// mirrorRoute is one configured object storage route with its open store.
type mirrorRoute struct {
	name               string
	match              ObjectStorageRouteMatch
	store              objectstore.ObjectStore
	prefix             string
	ext                string // jsonObjectExt or parquetObjectExt
	parquetCompression string
	written            atomic.Int64
	dropped            atomic.Int64
}

// newMirrorRoute builds a route over an already opened store, taking the key
// prefix and encoding from the route's object storage config.
func newMirrorRoute(name string, match ObjectStorageRouteMatch, store objectstore.ObjectStore, cfg *objectstore.Config) *mirrorRoute {
	route := &mirrorRoute{name: name, match: match, store: store, prefix: cfg.GetPrefix(), ext: jsonObjectExt}
	if cfg.ResolvedFormat() == objectstore.FormatParquet {
		route.ext = parquetObjectExt
		route.parquetCompression = cfg.ResolvedCompression()
	}
	return route
}

// mirrorWork is one log payload to write to one route.
type mirrorWork struct {
	route   *mirrorRoute
	key     string
	logID   string
	payload []byte
	tags    map[string]string
}

// objectMirror copies the complete payload of logs that match a route into
// that route's bucket. Writes are asynchronous and best-effort, like the
// primary offload: a full queue or a failed Put drops the copy and counts it,
// so a slow route bucket never backs up log writes. Work is sharded by log id
// so a later, more complete copy of a log is always written after an earlier
// one and wins.
type objectMirror struct {
	routes       []*mirrorRoute
	logger       schemas.Logger
	queues       []chan *mirrorWork
	wg           sync.WaitGroup
	closed       atomic.Bool
	pendingBytes atomic.Int64
}

// newObjectMirror starts the mirror's workers. It returns nil when there are
// no routes, and every method treats a nil mirror as "no routes".
func newObjectMirror(routes []*mirrorRoute, logger schemas.Logger) *objectMirror {
	if len(routes) == 0 {
		return nil
	}
	m := &objectMirror{routes: routes, logger: logger, queues: make([]chan *mirrorWork, defaultMirrorWorkers)}
	for i := range m.queues {
		m.queues[i] = make(chan *mirrorWork, defaultMirrorQueueSize/defaultMirrorWorkers)
		m.wg.Add(1)
		go m.worker(m.queues[i])
	}
	return m
}

// matching returns the routes whose match l satisfies.
func (m *objectMirror) matching(l *Log) []*mirrorRoute {
	if m == nil {
		return nil
	}
	var matched []*mirrorRoute
	for _, route := range m.routes {
		if route.match.Matches(l) {
			matched = append(matched, route)
		}
	}
	return matched
}

// enqueue writes payload, the complete payload of l, to each route, and takes
// ownership of the map. The id and object type are added to the copy, since a
// route's bucket is read without the database row that normally supplies them.
func (m *objectMirror) enqueue(routes []*mirrorRoute, l *Log, payload map[string]string) {
	if m == nil || len(routes) == 0 || m.closed.Load() {
		return
	}
	payload["id"] = l.ID
	payload["object_type"] = l.Object
	data, err := sonic.Marshal(payload)
	if err != nil {
		m.logger.Warn("objectstore mirror: failed to marshal payload for log %s: %v", l.ID, err)
		for _, route := range routes {
			route.dropped.Add(1)
		}
		return
	}
	tags := BuildTags(l)
	for _, route := range routes {
		m.submit(&mirrorWork{
			route:   route,
			key:     objectKey(route.prefix, "logs", l.Timestamp, l.ID, route.ext),
			logID:   l.ID,
			payload: data,
			tags:    tags,
		})
	}
}

// submit queues one copy, dropping it when the mirror is closed, over its
// byte budget or its shard is full. The bytes are reserved before the send,
// so concurrent submitters can never jointly overshoot the budget, and the
// reservation is released whenever the copy is not queued.
func (m *objectMirror) submit(work *mirrorWork) {
	size := int64(len(work.payload))
	reserved := false
	// Close may interleave between the closed check and the send.
	defer func() {
		if r := recover(); r != nil {
			if reserved {
				m.pendingBytes.Add(-size)
			}
			work.route.dropped.Add(1)
		}
	}()
	if m.closed.Load() {
		work.route.dropped.Add(1)
		return
	}
	if !m.reserveBytes(size) {
		work.route.dropped.Add(1)
		m.logger.Warn("objectstore mirror: queue memory limit reached, dropping copy of log %s for route %s", work.logID, work.route.name)
		return
	}
	reserved = true
	select {
	case m.shard(work.logID) <- work:
	default:
		m.pendingBytes.Add(-size)
		work.route.dropped.Add(1)
		m.logger.Warn("objectstore mirror: queue full, dropping copy of log %s for route %s", work.logID, work.route.name)
	}
}

// reserveBytes adds size to pendingBytes only while the total stays within
// the byte budget.
func (m *objectMirror) reserveBytes(size int64) bool {
	for {
		current := m.pendingBytes.Load()
		if current+size > defaultMaxMirrorQueueBytes {
			return false
		}
		if m.pendingBytes.CompareAndSwap(current, current+size) {
			return true
		}
	}
}

// shard picks the queue for a log id; one id always maps to one shard.
func (m *objectMirror) shard(logID string) chan *mirrorWork {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(logID))
	return m.queues[int(hash.Sum32()%uint32(len(m.queues)))]
}

// worker writes the copies of one shard in order.
func (m *objectMirror) worker(queue chan *mirrorWork) {
	defer m.wg.Done()
	for work := range queue {
		m.process(work)
	}
}

// process writes one copy, counting it as written or dropped.
func (m *objectMirror) process(work *mirrorWork) {
	defer m.pendingBytes.Add(-int64(len(work.payload)))
	defer func() {
		if r := recover(); r != nil {
			m.logger.Error("objectstore mirror: panic writing log %s to route %s (recovered): %v", work.logID, work.route.name, r)
			work.route.dropped.Add(1)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := putEncodedPayload(ctx, work.route.store, work.key, work.payload, work.route.parquetCompression, work.tags); err != nil {
		m.logger.Warn("objectstore mirror: failed to write log %s to route %s: %v", work.logID, work.route.name, err)
		work.route.dropped.Add(1)
		return
	}
	work.route.written.Add(1)
}

// close drains the queued copies, waiting even past ctx so no store is
// closed under an in-flight write, then closes every route's store.
func (m *objectMirror) close(ctx context.Context) {
	if m == nil || !m.closed.CompareAndSwap(false, true) {
		return
	}
	for _, q := range m.queues {
		close(q)
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		m.logger.Warn("objectstore mirror: shutdown cancelled before queue drained: %v", ctx.Err())
		<-done
	}
	for _, route := range m.routes {
		if err := route.store.Close(); err != nil {
			m.logger.Warn("objectstore mirror: error closing store for route %s: %v", route.name, err)
		}
	}
}

// openMirrorRoutes opens and pings each route's store. A route whose bucket
// cannot be reached fails startup, as the primary object store does, and
// closes the stores already opened.
func openMirrorRoutes(ctx context.Context, routes []ObjectStorageRoute, logger schemas.Logger) ([]*mirrorRoute, error) {
	opened := make([]*mirrorRoute, 0, len(routes))
	closeOpened := func() {
		for _, route := range opened {
			_ = route.store.Close()
		}
	}
	for _, cfg := range routes {
		store, err := objectstore.NewObjectStore(ctx, cfg.ObjectStorage, logger)
		if err != nil {
			closeOpened()
			return nil, fmt.Errorf("object storage route %q: %w", cfg.Name, err)
		}
		if err := store.Ping(ctx); err != nil {
			_ = store.Close()
			closeOpened()
			return nil, fmt.Errorf("object storage route %q: ping: %w", cfg.Name, err)
		}
		opened = append(opened, newMirrorRoute(cfg.Name, cfg.Match, store, cfg.ObjectStorage))
	}
	return opened, nil
}
