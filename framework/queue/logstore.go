package queue

import (
	"context"
	"errors"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// scopedDBStore is the logstore surface the queue builds on. RDBLogStore,
// ClickHouseLogStore and HybridLogStore all provide it.
type scopedDBStore interface {
	ScopedDB(ctx context.Context) *gorm.DB
}

// newLogStoreQueue builds the "logstore" queue type: the generic engine over
// a Store that keeps its tables in the logstore's own database.
func newLogStoreQueue(ctx context.Context, cfg *Config, deps Dependencies, logger schemas.Logger) (Queue, error) {
	var engine EngineConfig
	switch c := cfg.Config.(type) {
	case nil:
	case *LogStoreQueueConfig:
		if c != nil {
			engine = c.EngineConfig
		}
	default:
		return nil, fmt.Errorf("invalid logstore queue config: %T", cfg.Config)
	}
	store, err := newLogStoreBackend(ctx, deps, logger)
	if err != nil {
		return nil, err
	}
	return NewStoreQueue(store, engine, logger)
}

// newLogStoreBackend picks the Store implementation for the logstore's
// database dialect.
func newLogStoreBackend(ctx context.Context, deps Dependencies, logger schemas.Logger) (Store, error) {
	if deps.LogStore == nil {
		return nil, errors.New("queue: the logstore queue needs a logstore")
	}
	scoped, ok := deps.LogStore.(scopedDBStore)
	if !ok {
		return nil, fmt.Errorf("%w: logstore %T exposes no database handle", ErrUnsupported, deps.LogStore)
	}
	// A background context carries no query scope, so the queue's own tables
	// are never filtered by a caller's row visibility. Every operation rebinds
	// its own context with WithContext.
	db := scoped.ScopedDB(context.Background())
	if db == nil {
		return nil, fmt.Errorf("%w: logstore %T exposes no database handle", ErrUnsupported, deps.LogStore)
	}
	switch name := db.Dialector.Name(); name {
	case "sqlite":
		if logger != nil {
			logger.Warn("queue: the sqlite logstore queue is single-process only; use postgres or clickhouse to share it between instances")
		}
		return newSQLStore(ctx, db, logger)
	case "postgres":
		return newSQLStore(ctx, db, logger)
	default:
		return nil, fmt.Errorf("%w: logstore dialect %q", ErrUnsupported, name)
	}
}

func init() {
	Register(QueueTypeLogStore, newLogStoreQueue)
}
