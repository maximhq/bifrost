package lib

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/postgresconn"
)

// governanceSyncAdvisoryLockKey serializes the startup governance reconcile
// across replicas sharing one config store. Keys 1000001-1000049 are taken by
// other configstore and logstore locks.
const governanceSyncAdvisoryLockKey int64 = 1000060

const (
	// governanceSyncLockRetryInterval is the wait between attempts to take the
	// sync lock. Kept short because the holder's whole reconcile is usually a
	// few seconds and every replica of a rolling deploy queues on it.
	governanceSyncLockRetryInterval = time.Second
	// governanceSyncLockTimeout bounds the wait while the holder looks idle,
	// the signature of a crashed replica's lingering session.
	governanceSyncLockTimeout = 2 * time.Minute
	// governanceSyncLockBusyTimeout bounds the wait while the holder is
	// visibly writing, so one slow reconcile does not fail every queued replica.
	governanceSyncLockBusyTimeout = 10 * time.Minute

	// governanceWriteMaxAttempts bounds how often a governance write
	// transaction is re-run after a transient lock failure before boot fails.
	governanceWriteMaxAttempts = 6
	// governanceWriteInitialBackoff and governanceWriteMaxBackoff shape the
	// wait between attempts: long enough for a running node's usage dump batch
	// to commit, short enough that a rolling deploy is not visibly slowed.
	governanceWriteInitialBackoff = 500 * time.Millisecond
	governanceWriteMaxBackoff     = 8 * time.Second
)

// governanceSyncSleep waits between write attempts; a variable so tests can
// skip the backoff.
var governanceSyncSleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// withGovernanceSyncLock runs fn while holding a cluster-wide session advisory
// lock, so replicas starting together reconcile governance one at a time. When
// they run concurrently, each holds row locks on the entire governance table set
// in one long transaction. The ones behind queue on those locks until the
// statement timeout fires, or lock the rows in a different order and deadlock.
// Under the lock, each replica reads the store after the previous one has
// committed and so plans its writes against current state.
//
// The lock lives on a dedicated connection and is released when fn returns, or
// by Postgres when the process exits. Other dialects run fn directly: SQLite
// serializes writers on its own.
func withGovernanceSyncLock(ctx context.Context, store configstore.ConfigStore, fn func()) error {
	if store == nil {
		fn()
		return nil
	}
	db := store.DB()
	if db == nil || db.Dialector == nil || db.Dialector.Name() != "postgres" {
		fn()
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to get sql.DB for governance sync lock: %w", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to get dedicated connection for governance sync lock: %w", err)
	}
	defer conn.Close()
	if err := postgresconn.WaitForAdvisoryLock(ctx, conn, logger, postgresconn.AdvisoryWaitOptions{
		Key:           governanceSyncAdvisoryLockKey,
		Label:         "governance config sync",
		LogPrefix:     "[config]",
		RetryInterval: governanceSyncLockRetryInterval,
		Timeout:       governanceSyncLockTimeout,
		BusyTimeout:   governanceSyncLockBusyTimeout,
	}); err != nil {
		return err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", governanceSyncAdvisoryLockKey); err != nil {
			logger.Warn("failed to release governance sync lock (released when the connection closes): %v", err)
		}
	}()
	fn()
	return nil
}

// retryTransientGovernanceWrite runs write, re-running it with jittered
// exponential backoff while it fails with a transient lock error (deadlock,
// serialization failure, lock or statement timeout). These come from running
// replicas persisting usage counters on the same rows, and fail only because
// of timing. restore runs before every retry to undo any in-memory changes the
// failed attempt made to its inputs, so each attempt starts from the same plan.
// A non-transient error, or the last attempt's error, is returned as is.
func retryTransientGovernanceWrite(ctx context.Context, write func() error, restore func()) error {
	backoff := governanceWriteInitialBackoff
	for attempt := 1; ; attempt++ {
		err := write()
		if err == nil {
			if attempt > 1 {
				logger.Info("governance config sync succeeded on attempt %d", attempt)
			}
			return nil
		}
		if !postgresconn.IsTransientLockError(err) || attempt >= governanceWriteMaxAttempts || ctx.Err() != nil {
			return err
		}
		wait := backoff/2 + rand.N(backoff/2+1)
		logger.Warn("governance config sync hit a transient lock error (attempt %d of %d), retrying in %s: %v",
			attempt, governanceWriteMaxAttempts, wait.Round(time.Millisecond), err)
		if sleepErr := governanceSyncSleep(ctx, wait); sleepErr != nil {
			return err
		}
		if restore != nil {
			restore()
		}
		backoff = min(backoff*2, governanceWriteMaxBackoff)
	}
}

// governanceWriteInputs snapshots the change sets updateGovernanceConfigInStore
// mutates in place. Customers and virtual keys to add have association fields
// stripped while they are written, and the provider and MCP configs of a new
// key are shared with config.json's own slices, so they receive generated ids
// and have their limits stripped and restored mid-write. An attempt that fails
// partway leaves them half-rewritten, so a retry must start from this copy.
type governanceWriteInputs struct {
	customersToAdd    []configstoreTables.TableCustomer
	customersToUpdate []configstoreTables.TableCustomer
	virtualKeysToAdd  []configstoreTables.TableVirtualKey
	providerConfigs   [][]configstoreTables.TableVirtualKeyProviderConfig
	mcpConfigs        [][]configstoreTables.TableVirtualKeyMCPConfig
}

// snapshotGovernanceWriteInputs copies the mutable parts of the write inputs:
// the top-level structs, and the elements of each new key's provider and MCP
// config slices (not just the slice headers, which share backing arrays).
func snapshotGovernanceWriteInputs(customersToAdd, customersToUpdate []configstoreTables.TableCustomer, virtualKeysToAdd []configstoreTables.TableVirtualKey) governanceWriteInputs {
	snapshot := governanceWriteInputs{
		customersToAdd:    append([]configstoreTables.TableCustomer(nil), customersToAdd...),
		customersToUpdate: append([]configstoreTables.TableCustomer(nil), customersToUpdate...),
		virtualKeysToAdd:  append([]configstoreTables.TableVirtualKey(nil), virtualKeysToAdd...),
		providerConfigs:   make([][]configstoreTables.TableVirtualKeyProviderConfig, len(virtualKeysToAdd)),
		mcpConfigs:        make([][]configstoreTables.TableVirtualKeyMCPConfig, len(virtualKeysToAdd)),
	}
	for i := range virtualKeysToAdd {
		snapshot.providerConfigs[i] = append([]configstoreTables.TableVirtualKeyProviderConfig(nil), virtualKeysToAdd[i].ProviderConfigs...)
		snapshot.mcpConfigs[i] = append([]configstoreTables.TableVirtualKeyMCPConfig(nil), virtualKeysToAdd[i].MCPConfigs...)
	}
	return snapshot
}

// restoreInto writes the snapshot back over the live inputs in place, so callers
// holding the same slices (including config.json's provider and MCP configs)
// see the state from before the failed attempt.
func (s governanceWriteInputs) restoreInto(customersToAdd, customersToUpdate []configstoreTables.TableCustomer, virtualKeysToAdd []configstoreTables.TableVirtualKey) {
	copy(customersToAdd, s.customersToAdd)
	copy(customersToUpdate, s.customersToUpdate)
	copy(virtualKeysToAdd, s.virtualKeysToAdd)
	for i := range virtualKeysToAdd {
		// The restored struct carries the original slice headers again, which
		// point at the shared backing arrays, so restoring their elements
		// repairs config.json's own copies as well.
		copy(virtualKeysToAdd[i].ProviderConfigs, s.providerConfigs[i])
		copy(virtualKeysToAdd[i].MCPConfigs, s.mcpConfigs[i])
	}
}

// rateLimitNeedsFileSync reports whether a stored rate limit must be rewritten
// from config.json. In split mode the stored config_hash decides. When config.json
// is the source of truth, that hash can't be trusted, because UI/API edits don't
// update it. In that case the stored row's own content is hashed instead: only a
// row that actually differs from the file is rewritten, so a restart with an
// unchanged file doesn't lock every rate limit. GenerateRateLimitHash covers every
// configuration column of the row; only runtime counters are left out. If the
// stored row can't be hashed, it is rewritten.
func rateLimitNeedsFileSync(forceFileSync bool, existing configstoreTables.TableRateLimit, fileHash string) bool {
	if !forceFileSync {
		return existing.ConfigHash != fileHash
	}
	liveHash, err := configstore.GenerateRateLimitHash(existing)
	return err != nil || liveHash != fileHash
}

// sortedByID returns a copy of rows ordered by id, leaving the caller's slice
// untouched. Stable, so rows sharing an id keep their relative order.
func sortedByID[T any](rows []T, id func(T) string) []T {
	sorted := append([]T(nil), rows...)
	slices.SortStableFunc(sorted, func(a, b T) int { return strings.Compare(id(a), id(b)) })
	return sorted
}
