package queue

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Postgres advisory lock keys owned by the queue. The logstore uses 1000011,
// 1000012 and 1000015; configstore uses 1000001 and 1000002.
const (
	pgMigrationLockKey int64 = 1000020
	pgJanitorLockKey   int64 = 1000021
	// pgLaneLockClass namespaces the key-lane locks, taken with the two-int
	// form of pg_advisory_xact_lock so they cannot collide with the above.
	pgLaneLockClass = 1000022
	// pgLaneLockStripes bounds how many lane locks one transaction takes.
	// Advisory locks live in Postgres's fixed-size shared lock table, so one
	// per key could exhaust it on a large publish; lanes that share a stripe
	// only serialise with each other.
	pgLaneLockStripes = 256
	// pgGroupLockClass namespaces the per-group locks capped claims take.
	pgGroupLockClass = 1000023
)

// pgLockTimeout bounds waiting for an advisory lock another session holds;
// a variable so tests can shorten it.
var pgLockTimeout = 2 * time.Minute

const pgLockRetryInterval = 250 * time.Millisecond

// pgUnlockSQL releases a session advisory lock; a variable so a test can make
// the release fail.
var pgUnlockSQL = "SELECT pg_advisory_unlock($1)"

// withPGSessionLock runs fn while holding a session advisory lock, on the
// same dedicated connection: fn gets a handle bound to it, so it never needs
// a second connection (a one-connection pool would otherwise deadlock), and
// the lock and its release always run on one session. Acquisition polls
// pg_try_advisory_lock rather than blocking, so a lock held by a crashed
// instance's lingering session fails startup with a clear error instead of
// hanging it.
func withPGSessionLock(ctx context.Context, db *gorm.DB, key int64, fn func(conn *gorm.DB) error) error {
	return db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		deadline := time.Now().Add(pgLockTimeout)
		for {
			var acquired bool
			if err := conn.Raw("SELECT pg_try_advisory_lock(?)", key).Scan(&acquired).Error; err != nil {
				return fmt.Errorf("queue: try advisory lock %d: %w", key, err)
			}
			if acquired {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("queue: advisory lock %d still held by another session after %s; "+
					"find it with: SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND objid = %d AND granted", key, pgLockTimeout, key)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pgLockRetryInterval):
			}
		}
		defer func() {
			// Release even if ctx was cancelled. If the release cannot be
			// confirmed, the session may still hold the lock: discard the
			// physical connection rather than return it to the pool, where it
			// would keep every other instance from starting.
			unlock := conn.Session(&gorm.Session{Context: context.WithoutCancel(ctx)})
			var released bool
			if err := unlock.Raw(pgUnlockSQL, key).Scan(&released).Error; err != nil || !released {
				discardConn(conn)
			}
		}()
		return fn(conn)
	})
}

// discardConn marks the dedicated connection behind conn as broken, so the
// pool closes it instead of reusing it.
func discardConn(conn *gorm.DB) {
	if c, ok := conn.Statement.ConnPool.(*sql.Conn); ok {
		_ = c.Raw(func(any) error { return driver.ErrBadConn })
	}
}

// laneStripes maps lanes to their lock stripes, deduplicated and ascending, so
// every transaction takes them in the same order.
func laneStripes(lanes []laneKey) []int {
	var stripes []int
	for _, l := range lanes {
		h := fnv.New32a()
		_, _ = h.Write([]byte(l.topic + "\x00" + l.group + "\x00" + l.key))
		stripe := int(h.Sum32() % pgLaneLockStripes)
		if !slices.Contains(stripes, stripe) {
			stripes = append(stripes, stripe)
		}
	}
	slices.Sort(stripes)
	return stripes
}

// lockLanes takes the transaction-scoped lane locks for lanes in one round
// trip, in ascending stripe order.
func lockLanes(tx *gorm.DB, lanes []laneKey) error {
	stripes := laneStripes(lanes)
	if len(stripes) == 0 {
		return nil
	}
	values := make([]string, len(stripes))
	for i, st := range stripes {
		values[i] = strconv.Itoa(st)
	}
	// A loop over the sorted array takes the locks in exactly that order;
	// SQL guarantees no evaluation order for a set-based query.
	return tx.Exec(fmt.Sprintf("DO $$ DECLARE s int; BEGIN FOREACH s IN ARRAY ARRAY[%s]::int[] LOOP "+
		"PERFORM pg_advisory_xact_lock(%d, s); END LOOP; END $$", strings.Join(values, ","), pgLaneLockClass)).Error
}

// lockGroupClaims serialises capped claims of one consumer group for the rest
// of tx: each one counts the group's leases and claims the headroom while no
// other capped claim of the group can.
func lockGroupClaims(tx *gorm.DB, topic, group string) error {
	h := fnv.New32a()
	_, _ = h.Write([]byte(topic + "\x00" + group))
	var n int64
	return tx.Raw("SELECT count(pg_advisory_xact_lock(?, ?))", pgGroupLockClass, int32(h.Sum32())).Scan(&n).Error
}
