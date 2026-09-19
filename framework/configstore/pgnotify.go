package configstore

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/postgresconn"
	"gorm.io/gorm"
)

// PGNotifyChannel is the PostgreSQL LISTEN/NOTIFY channel used for config sync.
const PGNotifyChannel = "bifrost_config_sync"

// ConfigChangeEvent describes a config mutation that other pods should know about.
type ConfigChangeEvent struct {
	Entity   string `json:"entity"`             // "provider", "provider_key", "virtual_key", etc.
	Action   string `json:"action"`             // "upsert" or "delete"
	ID       string `json:"id,omitempty"`       // entity ID (empty for bulk ops)
	Provider string `json:"provider,omitempty"` // for provider-scoped entities
}

// ConfigChangeHandler is the callback invoked when a config change event is received.
type ConfigChangeHandler func(ctx context.Context, event ConfigChangeEvent)

// ConfigChangeListenable is optionally implemented by ConfigStore backends that
// support cross-pod config change notifications (Postgres LISTEN/NOTIFY).
// SQLite stores do not implement this.
type ConfigChangeListenable interface {
	// ListenForChanges starts a background goroutine that listens for config
	// change events from other pods and dispatches them to handler. The
	// listener runs until ctx is cancelled or StopListening is called.
	ListenForChanges(ctx context.Context, handler ConfigChangeHandler) error
	// StopListening stops the background listener.
	StopListening()
}

// pgNotifier publishes config change events via PostgreSQL pg_notify.
type pgNotifier struct {
	db     func() *gorm.DB // returns the current runtime DB (follows atomic swap)
	logger schemas.Logger
}

func newPGNotifier(dbFn func() *gorm.DB, logger schemas.Logger) *pgNotifier {
	return &pgNotifier{db: dbFn, logger: logger}
}

// notify sends a config change event via pg_notify. Errors are logged and
// swallowed — a failed notification degrades sync latency but must never
// fail the originating write.
//
// When tx is supplied, pg_notify runs on that same transaction/connection
// instead of the ambient pool. This matters: Postgres only delivers a
// NOTIFY to listeners after the transaction that issued it commits (and
// never delivers it at all if that transaction rolls back). Running the
// notify on a different connection than the write loses that guarantee —
// the notify could reach listeners before the write is even visible, or
// after the write has been rolled back.
func (n *pgNotifier) notify(ctx context.Context, event ConfigChangeEvent, tx ...*gorm.DB) {
	payload, err := json.Marshal(event)
	if err != nil {
		n.logger.Warn("[pgnotify] failed to marshal event: %v", err)
		return
	}
	db := n.db()
	if len(tx) > 0 && tx[0] != nil {
		db = tx[0]
	}
	if err := db.WithContext(ctx).Exec("SELECT pg_notify(?, ?)", PGNotifyChannel, string(payload)).Error; err != nil {
		n.logger.Warn("[pgnotify] failed to send notification: %v", err)
	}
}

// pgListener uses a dedicated pgx connection to LISTEN for config change events.
type pgListener struct {
	dsn    string
	logger schemas.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// newPGListener creates a listener but does not start it.
func newPGListener(config *postgresconn.Config, logger schemas.Logger) (*pgListener, error) {
	if err := postgresconn.Validate(config, false); err != nil {
		return nil, fmt.Errorf("pgnotify listener: %w", err)
	}
	dsn := postgresconn.BuildDSN(config)
	return &pgListener{dsn: dsn, logger: logger}, nil
}

// start begins listening in a background goroutine. The listener reconnects
// with exponential backoff on connection loss. After each reconnect the
// handler receives a synthetic "full_reload" event so the server can
// re-read everything it may have missed while disconnected.
func (l *pgListener) start(ctx context.Context, handler ConfigChangeHandler) {
	ctx, l.cancel = context.WithCancel(ctx)
	l.wg.Add(1)
	go l.loop(ctx, handler)
}

func (l *pgListener) stop() {
	if l.cancel != nil {
		l.cancel()
	}
	l.wg.Wait()
}

func (l *pgListener) loop(ctx context.Context, handler ConfigChangeHandler) {
	defer l.wg.Done()

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		err := l.listenOnce(ctx, handler)
		if ctx.Err() != nil {
			return
		}
		l.logger.Warn("[pgnotify] listener disconnected: %v; reconnecting in %s", err, backoff)

		jitter := time.Duration(rand.Int64N(int64(backoff) / 2))
		select {
		case <-time.After(backoff + jitter):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (l *pgListener) listenOnce(ctx context.Context, handler ConfigChangeHandler) error {
	conn, err := pgx.Connect(ctx, l.dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "LISTEN "+PGNotifyChannel); err != nil {
		return fmt.Errorf("LISTEN: %w", err)
	}
	l.logger.Info("[pgnotify] listener connected, waiting for config change events")

	// After (re)connect, trigger a full reload so we pick up anything missed.
	handler(ctx, ConfigChangeEvent{Entity: "full_reload", Action: "reload"})

	// Reset backoff on successful connection.
	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("wait: %w", err)
		}
		var event ConfigChangeEvent
		if err := json.Unmarshal([]byte(notification.Payload), &event); err != nil {
			l.logger.Warn("[pgnotify] malformed payload: %s", notification.Payload)
			continue
		}
		handler(ctx, event)
	}
}

// notifyChange is the helper called by RDBConfigStore CRUD methods.
// It is a no-op when the store has no notifier (SQLite, or postgres before
// the notifier is wired). When the caller is writing inside a transaction,
// it MUST pass that same tx here so the notify only becomes visible to
// listeners once the transaction commits (see notify's doc comment).
func (s *RDBConfigStore) notifyChange(ctx context.Context, event ConfigChangeEvent, tx ...*gorm.DB) {
	if s.notifier != nil {
		s.notifier.notify(ctx, event, tx...)
	}
}

// ListenForChanges implements ConfigChangeListenable for the Postgres-backed store.
func (s *RDBConfigStore) ListenForChanges(ctx context.Context, handler ConfigChangeHandler) error {
	if s.listener == nil {
		return fmt.Errorf("pgnotify listener not available (not a postgres store?)")
	}
	s.listener.start(ctx, handler)
	return nil
}

// StopListening implements ConfigChangeListenable.
func (s *RDBConfigStore) StopListening() {
	if s.listener != nil {
		s.listener.stop()
	}
}
