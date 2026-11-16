package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/maximhq/bifrost/framework/postgresconn"
	"gorm.io/gorm"
)

// pgNotifyChannel carries the topic of every committed publish, so instances
// can wake their consumers instead of waiting for the next poll.
const pgNotifyChannel = "bifrost_queue"

// Reconnect delays for the LISTEN session; variables so tests can shorten
// them.
var (
	pgListenRetryMin = time.Second
	pgListenRetryMax = 30 * time.Second
)

// pgListenCloseTimeout bounds closing the LISTEN connection, so a stalled
// server cannot keep the listener from returning.
const pgListenCloseTimeout = 5 * time.Second

// WakeSource is implemented by stores that can report publishes made by
// other instances. The engine runs Listen for the queue's lifetime and wakes
// local subscriptions of each reported topic. It is a latency optimisation
// only: polling still finds every message if notifications are lost.
type WakeSource interface {
	// Listen calls wake with a topic each time a message is published to it,
	// until ctx is done. It returns ErrUnsupported when the store has no way
	// to listen.
	Listen(ctx context.Context, wake func(topic string)) error
}

// notifyTopics queues one notification per topic. Postgres delivers them only
// when tx commits, so listeners never wake for a rolled-back publish.
func notifyTopics(tx *gorm.DB, topics []string) error {
	for _, t := range topics {
		if err := tx.Exec("SELECT pg_notify(?, ?)", pgNotifyChannel, t).Error; err != nil {
			return fmt.Errorf("notify %s: %w", t, err)
		}
	}
	return nil
}

// listenPostgres holds one dedicated connection in LISTEN, reconnecting with
// backoff when it drops, until ctx is done.
func listenPostgres(ctx context.Context, db *gorm.DB, wake func(topic string), warn func(string, ...any)) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return listenLoop(ctx, func(ctx context.Context, listening func()) error {
		return listenOnce(ctx, sqlDB, wake, listening)
	}, warn)
}

// listenLoop runs session until ctx is done. Between attempts that fail to
// reach LISTEN it waits a delay that doubles up to pgListenRetryMax; after a
// session that was listening it starts again from pgListenRetryMin, so one
// drop after a long healthy run is retried quickly. session calls listening
// once it is subscribed.
func listenLoop(ctx context.Context, session func(ctx context.Context, listening func()) error, warn func(string, ...any)) error {
	retry := pgListenRetryMin
	for {
		listened := false
		err := session(ctx, func() { listened = true })
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrUnsupported) {
			return err
		}
		if listened {
			retry = pgListenRetryMin
		}
		warn("queue: postgres LISTEN dropped, retrying in %s: %v", retry, err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(retry):
		}
		retry = min(retry*2, pgListenRetryMax)
	}
}

// listenOnce runs one LISTEN session until it fails or ctx is done. It uses a
// connection of its own rather than one from sqlDB's pool: the session lasts
// as long as the queue, and holding a pooled connection that long could
// starve the queue's own work on a small pool.
func listenOnce(ctx context.Context, sqlDB *sql.DB, wake func(topic string), listening func()) error {
	pc, err := pgListenConn(ctx, sqlDB)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pgListenCloseTimeout)
		defer cancel()
		_ = pc.Close(closeCtx)
	}()
	if _, err := pc.Exec(ctx, "LISTEN "+pgNotifyChannel); err != nil {
		return err
	}
	listening()
	for {
		n, err := pc.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		wake(n.Payload)
	}
}

// pgListenConn opens a connection outside sqlDB's pool. A pool that fetches
// its password with a command opens it the way it opens its own, so a
// short-lived password is fetched again; any other pool's settings are copied
// from one of its connections.
func pgListenConn(ctx context.Context, sqlDB *sql.DB) (*pgx.Conn, error) {
	if pc, ok, err := postgresconn.ConnectUnpooled(ctx, sqlDB); ok {
		return pc, err
	}
	cfg, err := pgConnConfig(ctx, sqlDB)
	if err != nil {
		return nil, err
	}
	return pgx.ConnectConfig(ctx, cfg)
}

// pgConnConfig returns the settings sqlDB's connections are opened with,
// borrowing one of them only long enough to read its configuration.
func pgConnConfig(ctx context.Context, sqlDB *sql.DB) (*pgx.ConnConfig, error) {
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var cfg *pgx.ConnConfig
	err = conn.Raw(func(driverConn any) error {
		sc, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("%w: postgres driver connection %T", ErrUnsupported, driverConn)
		}
		cfg = sc.Conn().Config()
		// The copy still routes notifications to the pooled connection's
		// buffer; cleared, pgx wires the new connection's own.
		cfg.OnNotification = nil
		return nil
	})
	return cfg, err
}
