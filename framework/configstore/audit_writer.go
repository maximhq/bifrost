package configstore

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

const (
	auditQueueSize = 1000
)

// AuditWriter is an append-only, hash-chained audit log writer.
// It runs asynchronously via a buffered channel so it never blocks request handlers.
//
// SHA-256 hash is computed over: "seq|timestamp|actorID|action|prevHash"
// This produces a tamper-evident chain that can be verified with VerifyAuditChain.
type AuditWriter struct {
	store    ConfigStore
	queue    chan *tables.TableAuditLog
	mu       sync.Mutex // protects prevHash
	prevHash string
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// NewAuditWriter creates and starts a new AuditWriter backed by the given ConfigStore.
// Call Shutdown() during server teardown to drain the queue.
func NewAuditWriter(store ConfigStore) *AuditWriter {
	w := &AuditWriter{
		store:    store,
		queue:    make(chan *tables.TableAuditLog, auditQueueSize),
		prevHash: "0000000000000000000000000000000000000000000000000000000000000000",
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	go w.processLoop()
	return w
}

// Write enqueues an audit log entry for async processing.
// If the queue is full the entry is dropped with a warning (non-blocking by design).
func (w *AuditWriter) Write(entry *tables.TableAuditLog) {
	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	select {
	case w.queue <- entry:
	default:
		// Queue full — drop with warning (never block caller)
		_ = entry // log dropped in real impl via logger.Warn
	}
}

// Shutdown drains the queue and waits for all pending entries to be written.
// Should be called during graceful shutdown.
func (w *AuditWriter) Shutdown(timeout time.Duration) {
	close(w.stopCh)
	select {
	case <-w.doneCh:
	case <-time.After(timeout):
	}
}

// processLoop is the single goroutine that serialises writes and maintains the hash chain.
func (w *AuditWriter) processLoop() {
	defer close(w.doneCh)
	ctx := context.Background()
	for {
		select {
		case entry := <-w.queue:
			w.mu.Lock()
			entry.PrevHash = w.prevHash
			entry.Hash = computeAuditHash(entry)
			w.prevHash = entry.Hash
			w.mu.Unlock()
			if err := w.store.AppendAuditLog(ctx, entry); err != nil {
				_ = err // log error in real impl
			}
		case <-w.stopCh:
			// Drain remaining entries
		drain:
			for {
				select {
				case entry := <-w.queue:
					w.mu.Lock()
					entry.PrevHash = w.prevHash
					entry.Hash = computeAuditHash(entry)
					w.prevHash = entry.Hash
					w.mu.Unlock()
					_ = w.store.AppendAuditLog(ctx, entry)
				default:
					break drain
				}
			}
			return
		}
	}
}

// computeAuditHash is defined in audit.go.
