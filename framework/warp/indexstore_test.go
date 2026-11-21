package warp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// fakeIndexLedger is the governance_config key-value rows, in memory.
type fakeIndexLedger struct{ rows map[string]string }

func newFakeIndexLedger() *fakeIndexLedger { return &fakeIndexLedger{rows: map[string]string{}} }

func (l *fakeIndexLedger) GetConfig(_ context.Context, key string) (*tables.TableGovernanceConfig, error) {
	value, ok := l.rows[key]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return &tables.TableGovernanceConfig{Key: key, Value: value}, nil
}

func (l *fakeIndexLedger) UpdateConfig(_ context.Context, config *tables.TableGovernanceConfig, _ ...*gorm.DB) error {
	l.rows[config.Key] = config.Value
	return nil
}

// sharedFingerprint is the shared vector_store's identity in these tests,
// unless a test moves the store.
const sharedFingerprint = "shared-1"

// startWarp is one Bifrost startup: a service on the given store, reconciled
// against what the ledger remembers from the last one. A source that names no
// shared store is on the usual one.
func startWarp(t *testing.T, ledger *fakeIndexLedger, shared *fakeWarpVectorStore, source VectorStoreSource) *Service {
	t.Helper()
	return startWarpLocked(t, ledger, shared, source, nil)
}

// startWarpOn is a startup whose stored configuration names namespace.
func startWarpOn(t *testing.T, ledger *fakeIndexLedger, shared *fakeWarpVectorStore, source VectorStoreSource, namespace string) *Service {
	t.Helper()
	row := validWarpConfigRow()
	row.LogVectorStoreNamespace = namespace
	return startWarpWith(t, ledger, shared, source, nil, row)
}

func startWarpLocked(t *testing.T, ledger *fakeIndexLedger, shared *fakeWarpVectorStore, source VectorStoreSource, locker IndexStoreLocker) *Service {
	t.Helper()
	return startWarpWith(t, ledger, shared, source, locker, validWarpConfigRow())
}

func startWarpWith(t *testing.T, ledger *fakeIndexLedger, shared *fakeWarpVectorStore, source VectorStoreSource, locker IndexStoreLocker, row *tables.TableWarpConfig) *Service {
	t.Helper()
	if source.SharedFingerprint == "" {
		source.SharedFingerprint = sharedFingerprint
	}
	vectors := shared
	if source.Dedicated {
		vectors = newFakeWarpVectorStore()
	}
	opts := []Option{WithConfigStore(&recordingStore{row: row}), WithVectorStoreSource(source), WithIndexStoreLedger(ledger, shared, locker)}
	if !source.Failed {
		opts = append(opts, WithVectorStore(vectors))
	}
	service := NewService(nil, opts...)
	service.ReconcileIndexStore(context.Background())
	return service
}

var (
	qdrantSource = VectorStoreSource{Dedicated: true, Type: "qdrant", Fingerprint: "aaaa"}
	redisSource  = VectorStoreSource{Dedicated: true, Type: "redis", Fingerprint: "bbbb"}
)

func TestWarpReportsTheIndexLeftInTheSharedStoreAndDeletesIt(t *testing.T) {
	ledger := newFakeIndexLedger()
	shared := newFakeWarpVectorStore()
	shared.existing = []string{schemas.WarpDefaultLogVectorStoreNamespace}

	if left := startWarp(t, ledger, shared, VectorStoreSource{}).LeftBehindIndexes(); len(left) != 0 {
		t.Fatalf("on the shared store nothing is left behind, got %+v", left)
	}
	service := startWarp(t, ledger, shared, qdrantSource)
	left := service.LeftBehindIndexes()
	if len(left) != 1 || left[0].Dedicated || !left[0].Deletable || left[0].Namespace != schemas.WarpDefaultLogVectorStoreNamespace {
		t.Fatalf("moving to Warp's own store should leave the shared index, deletable: %+v", left)
	}
	view, err := service.ConfigView(context.Background())
	if err != nil || len(view.LeftBehindIndexes) != 1 {
		t.Fatalf("the settings view should carry the left-behind index: %+v, %v", view.LeftBehindIndexes, err)
	}

	if err := service.DeleteLeftBehindIndex(context.Background(), left[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(shared.deleted) != 1 || shared.deleted[0] != schemas.WarpDefaultLogVectorStoreNamespace {
		t.Fatalf("the shared store should lose Warp's namespace, deleted %v", shared.deleted)
	}
	if left := service.LeftBehindIndexes(); len(left) != 0 {
		t.Fatalf("a deleted index leaves the list, got %+v", left)
	}
	if left := startWarp(t, ledger, shared, qdrantSource).LeftBehindIndexes(); len(left) != 0 {
		t.Fatalf("the delete should be remembered across a restart, got %+v", left)
	}
}

func TestWarpReportsNothingWhenTheSharedStoreNeverHeldAnIndex(t *testing.T) {
	// Warp was never set up before the move, so the shared store has no
	// namespace to point at; a first startup with no record counts as shared.
	ledger := newFakeIndexLedger()
	if left := startWarp(t, ledger, newFakeWarpVectorStore(), qdrantSource).LeftBehindIndexes(); len(left) != 0 {
		t.Fatalf("no namespace in the shared store means nothing left behind, got %+v", left)
	}
}

func TestWarpUpgradeStraightToItsOwnStoreFindsTheSharedIndex(t *testing.T) {
	// The release that adds warp.vector_store has no record yet: a deployment
	// that sets it on its first start indexed into the shared store until now.
	shared := newFakeWarpVectorStore()
	shared.existing = []string{schemas.WarpDefaultLogVectorStoreNamespace}
	left := startWarp(t, newFakeIndexLedger(), shared, qdrantSource).LeftBehindIndexes()
	if len(left) != 1 || !left[0].Deletable {
		t.Fatalf("the shared index from before the upgrade should be reported, got %+v", left)
	}
}

func TestWarpCannotDeleteAnIndexInAStoreItNoLongerReaches(t *testing.T) {
	ledger := newFakeIndexLedger()
	shared := newFakeWarpVectorStore()
	startWarp(t, ledger, shared, qdrantSource)
	service := startWarp(t, ledger, shared, VectorStoreSource{})
	left := service.LeftBehindIndexes()
	if len(left) != 1 || !left[0].Dedicated || left[0].Deletable || left[0].Type != "qdrant" {
		t.Fatalf("moving off Warp's own store should leave it, not deletable: %+v", left)
	}
	if err := service.DeleteLeftBehindIndex(context.Background(), left[0].ID); !errors.Is(err, ErrLeftBehindIndexUnreachable) {
		t.Fatalf("deleting from a store Bifrost does not reach should be refused, got %v", err)
	}
	if len(shared.deleted) != 0 {
		t.Fatalf("nothing may be deleted from the shared store, deleted %v", shared.deleted)
	}
	if err := service.DismissLeftBehindIndex(context.Background(), left[0].ID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if left := startWarp(t, ledger, shared, VectorStoreSource{}).LeftBehindIndexes(); len(left) != 0 {
		t.Fatalf("a dismissed index stays off the list, got %+v", left)
	}
	if err := service.DismissLeftBehindIndex(context.Background(), "nope"); !errors.Is(err, ErrLeftBehindIndexNotFound) {
		t.Fatalf("an unknown id should be not found, got %v", err)
	}
}

func TestWarpReturningToAStoreTakesItOffTheList(t *testing.T) {
	ledger := newFakeIndexLedger()
	shared := newFakeWarpVectorStore()
	shared.existing = []string{schemas.WarpDefaultLogVectorStoreNamespace}
	startWarp(t, ledger, shared, qdrantSource)
	startWarp(t, ledger, shared, redisSource)
	left := startWarp(t, ledger, shared, VectorStoreSource{}).LeftBehindIndexes()
	if len(left) != 2 {
		t.Fatalf("back on the shared store, the qdrant and redis indexes are left, got %+v", left)
	}
	for _, entry := range left {
		if !entry.Dedicated {
			t.Fatalf("the shared index is live again and must not be listed: %+v", left)
		}
	}
}

func TestWarpRecordsNothingWhileItsOwnStoreIsDown(t *testing.T) {
	ledger := newFakeIndexLedger()
	shared := newFakeWarpVectorStore()
	startWarp(t, ledger, shared, qdrantSource)
	failed := qdrantSource
	failed.Fingerprint = "cccc"
	failed.Failed = true
	startWarp(t, ledger, shared, failed)
	// The index never moved: the next good startup on qdrant has nothing left.
	if left := startWarp(t, ledger, shared, qdrantSource).LeftBehindIndexes(); len(left) != 0 {
		t.Fatalf("a failed startup must not count as a move, got %+v", left)
	}
}

func TestWarpClearsASharedIndexDeletedOutsideBifrost(t *testing.T) {
	ledger := newFakeIndexLedger()
	shared := newFakeWarpVectorStore()
	shared.existing = []string{schemas.WarpDefaultLogVectorStoreNamespace}
	service := startWarp(t, ledger, shared, qdrantSource)
	if len(service.LeftBehindIndexes()) != 1 {
		t.Fatalf("precondition: the shared index is listed")
	}

	shared.listErr = errors.New("store down")
	service.RefreshLeftBehindIndexes(context.Background())
	if len(service.LeftBehindIndexes()) != 1 {
		t.Fatalf("a store that cannot be asked keeps the entry")
	}

	// Deleted by hand, or by another replica's delete.
	shared.listErr = nil
	shared.existing = nil
	service.RefreshLeftBehindIndexes(context.Background())
	if left := service.LeftBehindIndexes(); len(left) != 0 {
		t.Fatalf("a namespace that is gone clears its notice, got %+v", left)
	}
	if left := startWarp(t, ledger, shared, qdrantSource).LeftBehindIndexes(); len(left) != 0 {
		t.Fatalf("the clear should be stored, got %+v", left)
	}
}

// A vector_store that moved since the index was left there is not the store
// Bifrost connects to now: the entry is kept, but nothing is deleted from the
// new store under that namespace.
func TestWarpLeavesAnIndexInAMovedSharedStoreUndeletable(t *testing.T) {
	ledger := newFakeIndexLedger()
	shared := newFakeWarpVectorStore()
	shared.existing = []string{schemas.WarpDefaultLogVectorStoreNamespace}
	startWarp(t, ledger, shared, VectorStoreSource{})
	// Warp moves to its own store and, at the same restart, vector_store moves.
	moved := qdrantSource
	moved.SharedFingerprint = "shared-2"
	service := startWarp(t, ledger, shared, moved)
	left := service.LeftBehindIndexes()
	if len(left) != 1 || left[0].Dedicated || left[0].Deletable || left[0].Fingerprint != sharedFingerprint {
		t.Fatalf("the index in the old vector_store is listed, not deletable: %+v", left)
	}
	if err := service.DeleteLeftBehindIndex(context.Background(), left[0].ID); !errors.Is(err, ErrLeftBehindIndexUnreachable) {
		t.Fatalf("a delete must not reach the new vector_store, got %v", err)
	}
	if len(shared.deleted) != 0 {
		t.Fatalf("nothing may be deleted from the store connected now, deleted %v", shared.deleted)
	}
	service.RefreshLeftBehindIndexes(context.Background())
	if len(service.LeftBehindIndexes()) != 1 {
		t.Fatalf("an index the connected store cannot be asked about keeps its notice")
	}
}

// An entry recorded with no fingerprint cannot be told apart from another
// store, so it is kept but never deletable.
func TestWarpKeepsAnUnidentifiedSharedEntryUndeletable(t *testing.T) {
	ledger := newFakeIndexLedger()
	ledger.rows[indexStoresConfigKey] = `{"current":{"dedicated":true,"fingerprint":"aaaa","namespace":"BifrostWarpLogs"},"left_behind":[{"id":"old","dedicated":false,"namespace":"BifrostWarpLogs","moved_at":"2026-11-01T00:00:00Z"}]}`
	shared := newFakeWarpVectorStore()
	shared.existing = []string{schemas.WarpDefaultLogVectorStoreNamespace}
	service := startWarp(t, ledger, shared, qdrantSource)
	left := service.LeftBehindIndexes()
	if len(left) != 1 || left[0].Deletable {
		t.Fatalf("the unidentified entry stays, not deletable: %+v", left)
	}
	if err := service.DeleteLeftBehindIndex(context.Background(), "old"); !errors.Is(err, ErrLeftBehindIndexUnreachable) {
		t.Fatalf("got %v", err)
	}
}

// A memory-only store keeps nothing across a restart, and each start of one
// is a store of its own: moving off one, or restarting on one, leaves nothing
// to list.
func TestWarpListsNothingLeftInAMemoryOnlyStore(t *testing.T) {
	ledger := newFakeIndexLedger()
	shared := newFakeWarpVectorStore()
	memory := func(fingerprint string) VectorStoreSource {
		return VectorStoreSource{Dedicated: true, Type: "chromem", Fingerprint: fingerprint, InMemory: true}
	}
	startWarp(t, ledger, shared, memory("mem-1"))
	if left := startWarp(t, ledger, shared, memory("mem-2")).LeftBehindIndexes(); len(left) != 0 {
		t.Fatalf("a restart on a memory-only store leaves nothing, got %+v", left)
	}
	if left := startWarp(t, ledger, shared, qdrantSource).LeftBehindIndexes(); len(left) != 0 {
		t.Fatalf("moving off a memory-only store leaves nothing, got %+v", left)
	}
	// The same for a memory-only shared store.
	inMemoryShared := func(fingerprint string) VectorStoreSource {
		return VectorStoreSource{Fingerprint: fingerprint, InMemory: true, SharedFingerprint: fingerprint, SharedInMemory: true}
	}
	ledger = newFakeIndexLedger()
	startWarp(t, ledger, shared, inMemoryShared("mem-a"))
	if left := startWarp(t, ledger, shared, inMemoryShared("mem-b")).LeftBehindIndexes(); len(left) != 0 {
		t.Fatalf("a restart on a memory-only shared store leaves nothing, got %+v", left)
	}
}

// slowLedger holds every read open for a moment, so two changes started
// together both read before either writes.
type slowLedger struct {
	*fakeIndexLedger
	mu sync.Mutex
}

func (l *slowLedger) GetConfig(ctx context.Context, key string) (*tables.TableGovernanceConfig, error) {
	l.mu.Lock()
	row, err := l.fakeIndexLedger.GetConfig(ctx, key)
	l.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	return row, err
}

func (l *slowLedger) UpdateConfig(ctx context.Context, config *tables.TableGovernanceConfig, tx ...*gorm.DB) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakeIndexLedger.UpdateConfig(ctx, config, tx...)
}

// mutexLocker stands in for the config store's distributed lock.
type mutexLocker struct{ mu sync.Mutex }

func (m *mutexLocker) Acquire(ctx context.Context, _ string) (context.Context, func(), error) {
	m.mu.Lock()
	return ctx, m.mu.Unlock, nil
}

// Two replicas clearing two notices at once both have their change kept: the
// record is changed under the lock, so neither writes over the other.
func TestWarpOverlappingDismissalsAreBothKept(t *testing.T) {
	ledger := &slowLedger{fakeIndexLedger: newFakeIndexLedger()}
	ledger.rows[indexStoresConfigKey] = `{"current":{"dedicated":true,"fingerprint":"aaaa","namespace":"BifrostWarpLogs"},"left_behind":[{"id":"one","dedicated":true,"fingerprint":"x","namespace":"BifrostWarpLogs","moved_at":"2026-11-01T00:00:00Z"},{"id":"two","dedicated":true,"fingerprint":"y","namespace":"BifrostWarpLogs","moved_at":"2026-11-01T00:00:00Z"}]}`
	locker := &mutexLocker{}
	shared := newFakeWarpVectorStore()
	first := startWarpLocked(t, ledger.fakeIndexLedger, shared, qdrantSource, locker)
	second := startWarpLocked(t, ledger.fakeIndexLedger, shared, qdrantSource, locker)
	first.indexLedger, second.indexLedger = ledger, ledger

	var wg sync.WaitGroup
	for service, id := range map[*Service]string{first: "one", second: "two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := service.DismissLeftBehindIndex(context.Background(), id); err != nil {
				t.Errorf("dismiss %s: %v", id, err)
			}
		}()
	}
	wg.Wait()
	if left := startWarp(t, ledger.fakeIndexLedger, shared, qdrantSource).LeftBehindIndexes(); len(left) != 0 {
		t.Fatalf("both dismissals are kept, got %+v", left)
	}
	// The two running replicas see the other's dismissal on their next
	// settings read, not only at their next start.
	for name, service := range map[string]*Service{"first": first, "second": second} {
		service.RefreshLeftBehindIndexes(context.Background())
		if left := service.LeftBehindIndexes(); len(left) != 0 {
			t.Fatalf("%s replica still lists %+v after the other's dismissal", name, left)
		}
	}
}

// Replica A runs on Warp's own store and still lists the index it left in the
// shared store; replica B has since moved back to the shared store, where that
// index is live again. A's delete reads where the index is now from the
// record, refuses, and drops the notice, deleting nothing B writes to.
func TestWarpDoesNotDeleteASharedIndexAnotherReplicaMovedBackTo(t *testing.T) {
	ledger := newFakeIndexLedger()
	shared := newFakeWarpVectorStore()
	shared.existing = []string{schemas.WarpDefaultLogVectorStoreNamespace}
	startWarp(t, ledger, shared, VectorStoreSource{})
	replicaA := startWarp(t, ledger, shared, qdrantSource)
	left := replicaA.LeftBehindIndexes()
	if len(left) != 1 || !left[0].Deletable {
		t.Fatalf("precondition: A lists the shared index as deletable, got %+v", left)
	}
	startWarp(t, ledger, shared, VectorStoreSource{}) // replica B, back on the shared store

	if err := replicaA.DeleteLeftBehindIndex(context.Background(), left[0].ID); !errors.Is(err, ErrLeftBehindIndexInUse) {
		t.Fatalf("A's delete must be refused while the index is live again, got %v", err)
	}
	if len(shared.deleted) != 0 {
		t.Fatalf("nothing may be deleted from the store B writes to, deleted %v", shared.deleted)
	}
	// B's start moved the record off A's store, so that one is listed now;
	// the shared index, live again, is not.
	for _, entry := range replicaA.LeftBehindIndexes() {
		if !entry.Dedicated {
			t.Fatalf("the notice for the live shared index goes, got %+v", entry)
		}
	}
}

// A namespace changed in the settings is the one the record names from then
// on: when the store moves at the next restart, the index left behind is
// listed under the namespace that holds its vectors, not the one before.
func TestWarpRecordsANamespaceChangedInTheSettings(t *testing.T) {
	ledger := newFakeIndexLedger()
	shared := newFakeWarpVectorStore()
	service := startWarp(t, ledger, shared, VectorStoreSource{})
	input := validWarpConfigInput()
	input.LogVectorStoreNamespace = "WarpLogsRenamed"
	if _, err := service.SaveConfig(context.Background(), input); err != nil {
		t.Fatalf("save: %v", err)
	}
	shared.existing = []string{"WarpLogsRenamed"}

	left := startWarpOn(t, ledger, shared, qdrantSource, "WarpLogsRenamed").LeftBehindIndexes()
	if len(left) != 1 || left[0].Namespace != "WarpLogsRenamed" {
		t.Fatalf("the index left in the shared store is under the renamed namespace, got %+v", left)
	}
}
