package configstore

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"slices"
	"strings"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// The vault is not part of the database transaction, so a secret removed while the
// transaction is open stays removed when it rolls back, and the restored row then
// references a secret that no longer exists. GORM has no hook that runs after a commit:
// every model hook and callback fires around a single statement, and when the caller owns
// the transaction (ExecuteTransaction, or a tx passed into the store) the commit happens
// after all of them have returned. So the configstore's *sql.DB is wrapped: every
// transaction it begins is a vaultTx, the vault callbacks queue removals on it, and
// vaultTx.Commit / vaultTx.Rollback run the matching queue once the outcome is known.

// vaultConnPool is the configstore's connection pool. It begins every transaction as a
// vaultTx. GORM checks ConnPoolBeginner after TxBeginner, so BeginTx here must not
// return *sql.Tx, or GORM would take the embedded *sql.DB's BeginTx instead.
type vaultConnPool struct {
	*sql.DB
}

func (p *vaultConnPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &vaultTx{Tx: tx, sqlDB: p.DB, ctx: context.WithoutCancel(ctx)}, nil
}

// GetDBConn keeps gorm.DB.DB() returning the underlying *sql.DB for pool tuning and Close.
func (p *vaultConnPool) GetDBConn() (*sql.DB, error) { return p.DB, nil }

// vaultTx is a database transaction carrying the vault removals its statements queued:
// onCommit holds secrets the committed rows no longer reference, onRollback holds secrets
// this transaction wrote that a rollback leaves unreferenced.
type vaultTx struct {
	*sql.Tx
	sqlDB *sql.DB
	ctx   context.Context

	mu         sync.Mutex
	onCommit   []string
	onRollback []string
	savepoints map[string]vaultQueueMark
}

// vaultQueueMark records how long each queue was when a savepoint was taken.
type vaultQueueMark struct{ onCommit, onRollback int }

func (t *vaultTx) GetDBConn() (*sql.DB, error) { return t.sqlDB, nil }

// queue adds removals to run after the transaction commits or rolls back.
func (t *vaultTx) queue(onCommit, onRollback []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onCommit = append(t.onCommit, onCommit...)
	t.onRollback = append(t.onRollback, onRollback...)
}

// Commit commits, then removes the secrets the committed rows stopped referencing. A
// failed commit leaves the outcome unknown, so it removes nothing: the worst case is an
// unreferenced secret, never a row pointing at a removed one.
func (t *vaultTx) Commit() error {
	if err := t.Tx.Commit(); err != nil {
		return err
	}
	t.mu.Lock()
	paths := t.onCommit
	t.onCommit, t.onRollback = nil, nil
	t.mu.Unlock()
	removeVaultSecrets(t.ctx, paths)
	return nil
}

// Rollback rolls back, then removes the secrets this transaction wrote. GORM also calls
// Rollback after a failed Commit; that returns sql.ErrTxDone and removes nothing.
func (t *vaultTx) Rollback() error {
	err := t.Tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return err
	}
	t.mu.Lock()
	paths := t.onRollback
	t.onCommit, t.onRollback = nil, nil
	t.mu.Unlock()
	removeVaultSecrets(t.ctx, paths)
	return err
}

// ExecContext runs the statement and tracks the savepoints a nested gorm Transaction
// takes, which the GORM dialects issue as plain "SAVEPOINT x" / "ROLLBACK TO SAVEPOINT x".
func (t *vaultTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := t.Tx.ExecContext(ctx, query, args...)
	if err == nil {
		t.trackSavepoint(query)
	}
	return result, err
}

// trackSavepoint keeps the queues in step with a rollback to a savepoint. Removals queued
// after the savepoint were for rows that are now restored, so they are dropped. Secrets
// written after it are referenced by nothing once the rows are restored, so they are
// removed however the transaction ends.
func (t *vaultTx) trackSavepoint(query string) {
	query = strings.TrimSpace(query)
	t.mu.Lock()
	defer t.mu.Unlock()
	if name, ok := cutPrefixFold(query, "ROLLBACK TO SAVEPOINT "); ok {
		mark, ok := t.savepoints[name]
		if !ok {
			return
		}
		written := slices.Clone(t.onRollback[mark.onRollback:])
		t.onCommit = append(slices.Clone(t.onCommit[:mark.onCommit]), written...)
		t.onRollback = append(t.onRollback[:mark.onRollback], written...)
		return
	}
	if name, ok := cutPrefixFold(query, "SAVEPOINT "); ok {
		if t.savepoints == nil {
			t.savepoints = make(map[string]vaultQueueMark)
		}
		t.savepoints[name] = vaultQueueMark{onCommit: len(t.onCommit), onRollback: len(t.onRollback)}
	}
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(s[len(prefix):]), true
}

// installVaultTxPool wraps db's *sql.DB in a vaultConnPool. It is idempotent, and leaves a
// pool it does not recognize alone (vault removals queued on it are then skipped).
func installVaultTxPool(db *gorm.DB) {
	if _, ok := db.ConnPool.(*vaultConnPool); ok {
		return
	}
	sqlDB, ok := db.ConnPool.(*sql.DB)
	if !ok {
		log.Printf("vault: connection pool %T is not *sql.DB; replaced vault secrets will not be removed", db.ConnPool)
		return
	}
	pool := &vaultConnPool{DB: sqlDB}
	db.ConnPool = pool
	db.Statement.ConnPool = pool
}

// queueVaultRemovals hands a statement's removals to the transaction it ran in. With no
// transaction the statement has already committed, so onCommit runs now. In a transaction
// the wrapper did not begin the outcome cannot be observed, so nothing is removed.
func queueVaultRemovals(tx *gorm.DB, onCommit, onRollback []string) {
	if len(onCommit) == 0 && len(onRollback) == 0 {
		return
	}
	switch pool := tx.Statement.ConnPool.(type) {
	case *vaultTx:
		pool.queue(onCommit, onRollback)
	case *vaultConnPool:
		removeVaultSecrets(tx.Statement.Context, onCommit)
	}
}

// removeVaultSecrets best-effort removes each distinct path. A failure leaves an
// unreferenced secret behind, so it is logged rather than returned.
func removeVaultSecrets(ctx context.Context, paths []string) {
	if len(paths) == 0 || schemas.VaultRemoveHook == nil {
		return
	}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		if err := schemas.VaultRemoveHook(ctx, path); err != nil {
			log.Printf("vault: failed to remove secret %s: %v", path, err)
		}
	}
}
