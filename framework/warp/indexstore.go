package warp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"gorm.io/gorm"
)

// When warp.vector_store is added, changed or removed, Warp's log index starts
// over in the new store and the old one keeps its vectors: nothing in Bifrost
// deletes them on the way out, and once Bifrost stops connecting to a store
// the retention sweep cannot reach it either. So at each startup Warp compares
// where its index is now with where it was at the last one, and keeps a list
// of indexes it has left behind for the settings page to show. An index left
// in the shared store, which Bifrost still connects to, can be deleted from
// there; one left in a store of Warp's own can only be described, for the
// admin to delete by hand, and dismissed.

// indexStoresConfigKey is the governance_config row holding the record. It is a
// key of its own, so a binary that predates it never reads or rewrites it.
const indexStoresConfigKey = "warp_index_stores"

// ErrLeftBehindIndexNotFound is a left-behind index id the record does not hold.
var ErrLeftBehindIndexNotFound = errors.New("no Warp index left behind with that id")

// ErrLeftBehindIndexUnreachable is a delete of an index in a store Bifrost no
// longer connects to.
var ErrLeftBehindIndexUnreachable = errors.New("that Warp index is in a store Bifrost no longer connects to; delete it there by hand")

// ErrLeftBehindIndexInUse is a delete of an index Warp is using again: another
// replica has moved back to that store since the notice was listed.
var ErrLeftBehindIndexInUse = errors.New("Warp's log index is in that store again, so it was not deleted")

// IndexStoreLedger is the config store's key-value rows, where the record of
// Warp's index locations lives.
type IndexStoreLedger interface {
	GetConfig(ctx context.Context, key string) (*tables.TableGovernanceConfig, error)
	UpdateConfig(ctx context.Context, config *tables.TableGovernanceConfig, tx ...*gorm.DB) error
}

// IndexStoreLocker serializes changes to the record across replicas: each one
// reads, changes and writes it whole, so two at once would lose one's change.
type IndexStoreLocker interface {
	Acquire(ctx context.Context, key string) (context.Context, func(), error)
}

// IndexLocation is a store and namespace Warp's log index lives in.
type IndexLocation struct {
	Dedicated   bool   `json:"dedicated"`             // a store of Warp's own, not the shared vector_store
	Type        string `json:"type,omitempty"`        // the dedicated store's type
	Fingerprint string `json:"fingerprint,omitempty"` // the store's fingerprint, shared or dedicated
	InMemory    bool   `json:"in_memory,omitempty"`   // the store kept nothing across a restart
	Namespace   string `json:"namespace"`
}

// sameStore reports whether two locations are the same store, whatever the
// namespace: a namespace change inside one store is SaveConfig's to clean up.
func (l IndexLocation) sameStore(other IndexLocation) bool {
	return l.Dedicated == other.Dedicated && l.Fingerprint == other.Fingerprint
}

// LeftBehindIndex is a Warp log index in a store Warp no longer uses.
type LeftBehindIndex struct {
	ID string `json:"id"`
	IndexLocation
	MovedAt time.Time `json:"moved_at"`
	// Deletable is whether Bifrost can delete it: only in the shared store it
	// connects to now. Worked out when the list is read, not stored, so a
	// vector_store that moves since makes it undeletable.
	Deletable bool `json:"deletable"`
}

type indexStoreRecord struct {
	Current    *IndexLocation    `json:"current,omitempty"`
	LeftBehind []LeftBehindIndex `json:"left_behind,omitempty"`
}

// WithIndexStoreLedger keeps the record of where Warp's index has lived, and
// gives Warp the shared vector store so an index left there can be deleted.
// The locker serializes changes to the record across replicas; nil leaves
// them unserialized, which only one process can afford. Without a ledger
// nothing is recorded and no left-behind index is reported.
func WithIndexStoreLedger(ledger IndexStoreLedger, shared vectorstore.VectorStore, locker IndexStoreLocker) Option {
	return func(s *Service) {
		s.indexLedger = ledger
		s.sharedVectorStore = shared
		s.indexLocker = locker
	}
}

// sharedLocation is the shared vector_store, as the record names it.
func (s *Service) sharedLocation(namespace string) IndexLocation {
	return IndexLocation{Fingerprint: s.vectorSource.SharedFingerprint, InMemory: s.vectorSource.SharedInMemory, Namespace: namespace}
}

// inConnectedSharedStore reports whether a location is the shared store
// Bifrost connects to now: a shared location recorded under another
// fingerprint is a vector_store that has since moved, and one with none
// cannot be told apart from it.
func (s *Service) inConnectedSharedStore(location IndexLocation) bool {
	return !location.Dedicated && location.Fingerprint != "" && location.Fingerprint == s.vectorSource.SharedFingerprint && s.sharedVectorStore != nil
}

// leftBehindIndexID names an entry by its store and namespace, so the same
// index left twice is one entry.
func leftBehindIndexID(location IndexLocation) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%t|%s|%s", location.Dedicated, location.Fingerprint, location.Namespace)))
	return fmt.Sprintf("%x", sum[:8])
}

// ReconcileIndexStore runs once at startup, after config.json's warp section
// is applied: it records where the index lives now and, when that is a
// different store from the last startup, adds the old one to the left-behind
// list. A dedicated store that failed to connect records nothing, since the
// index is not anywhere yet. A record that cannot be read or written is
// logged and leaves the list as it was: the banner is a reminder, and Warp
// itself does not depend on it.
func (s *Service) ReconcileIndexStore(ctx context.Context) {
	if s.indexLedger == nil || s.vectorSource.Failed || s.vectorStore == nil {
		return
	}
	view, err := s.ConfigView(ctx)
	if err != nil {
		return
	}
	namespace := normalizedNamespace(view.LogVectorStoreNamespace)
	current := s.sharedLocation(namespace)
	if s.vectorSource.Dedicated {
		current = IndexLocation{Dedicated: true, Type: s.vectorSource.Type, Fingerprint: s.vectorSource.Fingerprint, InMemory: s.vectorSource.InMemory, Namespace: namespace}
	}
	err = s.mutateIndexStoreRecord(ctx, func(record *indexStoreRecord) error {
		// Before the record existed, every Warp index was in the shared store.
		previous := s.sharedLocation(namespace)
		if record.Current != nil {
			previous = *record.Current
		}
		// Back in a store it left: that index is live again, never one to delete.
		record.LeftBehind = slices.DeleteFunc(record.LeftBehind, func(entry LeftBehindIndex) bool {
			return entry.sameStore(current)
		})
		if !previous.sameStore(current) && s.indexWasLeftIn(ctx, previous) {
			left := LeftBehindIndex{ID: leftBehindIndexID(previous), IndexLocation: previous, MovedAt: time.Now().UTC()}
			record.LeftBehind = slices.DeleteFunc(record.LeftBehind, func(entry LeftBehindIndex) bool { return entry.ID == left.ID })
			record.LeftBehind = append(record.LeftBehind, left)
		}
		record.Current = &current
		return nil
	})
	if err != nil {
		s.warnf("failed to record where Warp's log index lives: %v", err)
	}
}

// recordIndexNamespace keeps the record's namespace in step with a save that
// changes it, so a store moved at the next restart leaves its index listed
// under the namespace holding its vectors. A record with no location yet is
// left to the next startup to write.
func (s *Service) recordIndexNamespace(ctx context.Context, namespace string) {
	if s.indexLedger == nil {
		return
	}
	namespace = normalizedNamespace(namespace)
	err := s.mutateIndexStoreRecord(ctx, func(record *indexStoreRecord) error {
		if record.Current != nil {
			record.Current.Namespace = namespace
		}
		return nil
	})
	if err != nil {
		s.warnf("failed to record Warp's log index namespace %q: %v", namespace, err)
	}
}

// indexWasLeftIn reports whether the previous location may hold vectors. A
// memory-only store kept nothing, so nothing was left. Only the shared store
// Bifrost connects to can be checked: there, an index that was never built
// (Warp never configured, or no namespace yet) is no index at all. Any other
// store cannot be asked, so it counts.
func (s *Service) indexWasLeftIn(ctx context.Context, previous IndexLocation) bool {
	if previous.InMemory {
		return false
	}
	if !s.inConnectedSharedStore(previous) {
		return true
	}
	namespaces, err := s.sharedVectorStore.ListNamespaces(ctx, previous.Namespace)
	if err != nil {
		return true
	}
	return slices.Contains(namespaces, previous.Namespace)
}

// RefreshLeftBehindIndexes drops entries in the shared store whose namespace
// is gone, deleted by hand or from another replica, so their notice clears
// itself. It asks the shared store, so it runs for the settings page only, not
// for every read of the configuration (the tray polls ConfigView), and only
// when there is a shared entry. A store that cannot be asked keeps the entry.
func (s *Service) RefreshLeftBehindIndexes(ctx context.Context) {
	if s.indexLedger == nil {
		return
	}
	// The record first: another replica may have dismissed or deleted a notice
	// since this one last wrote it. Without a lock, as it only reads.
	if record, err := s.readIndexStoreRecord(ctx); err == nil {
		s.mu.Lock()
		s.leftBehind = record.LeftBehind
		s.mu.Unlock()
	} else {
		s.warnf("failed to reload Warp's left-behind log indexes: %v", err)
	}
	if s.sharedVectorStore == nil {
		return
	}
	for _, entry := range s.LeftBehindIndexes() {
		if !s.inConnectedSharedStore(entry.IndexLocation) {
			continue
		}
		namespaces, err := s.sharedVectorStore.ListNamespaces(ctx, entry.Namespace)
		if err != nil || slices.Contains(namespaces, entry.Namespace) {
			continue
		}
		if err := s.forgetLeftBehindIndex(ctx, entry.ID); err != nil {
			s.warnf("failed to clear Warp's old log index %q, which is no longer in the shared vector store: %v", entry.Namespace, err)
		}
	}
}

// LeftBehindIndexes is the list for the settings page.
func (s *Service) LeftBehindIndexes() []LeftBehindIndex {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := slices.Clone(s.leftBehind)
	for i := range list {
		list[i].Deletable = s.inConnectedSharedStore(list[i].IndexLocation)
	}
	return list
}

// DeleteLeftBehindIndex deletes a left-behind index from the shared store and
// takes it off the list. An index anywhere but the shared store Bifrost
// connects to now, by its recorded fingerprint, is refused with
// ErrLeftBehindIndexUnreachable: a vector_store that has moved since would
// otherwise have the namespace deleted from the wrong store.
func (s *Service) DeleteLeftBehindIndex(ctx context.Context, id string) error {
	entry, err := s.leftBehindIndex(id)
	if err != nil {
		return err
	}
	if !s.inConnectedSharedStore(entry.IndexLocation) {
		return ErrLeftBehindIndexUnreachable
	}
	// Never the store Warp is writing to: with Warp back on the shared store,
	// ReconcileIndexStore drops such an entry, and this holds even if the record
	// was edited by hand.
	if !s.vectorSource.Dedicated {
		return s.forgetLeftBehindIndex(ctx, id)
	}
	// Where the index is now is the record's, not this replica's: another one
	// may have moved back to the shared store since this list was read. Read
	// under the lock, with the delete, so a startup cannot move it in between.
	inUse := false
	err = s.mutateIndexStoreRecord(ctx, func(record *indexStoreRecord) error {
		if current := record.Current; current != nil && current.sameStore(entry.IndexLocation) {
			inUse = true
		} else if err := s.sharedVectorStore.DeleteNamespace(ctx, entry.Namespace); err != nil && !errors.Is(err, vectorstore.ErrNotFound) {
			return fmt.Errorf("delete Warp's old log index %q from the shared vector store: %w", entry.Namespace, err)
		}
		record.LeftBehind = slices.DeleteFunc(record.LeftBehind, func(left LeftBehindIndex) bool { return left.ID == id })
		return nil
	})
	if err == nil && inUse {
		return ErrLeftBehindIndexInUse
	}
	return err
}

// DismissLeftBehindIndex takes an index off the list without deleting it, for
// an admin who has deleted it by hand or wants to keep it.
func (s *Service) DismissLeftBehindIndex(ctx context.Context, id string) error {
	if _, err := s.leftBehindIndex(id); err != nil {
		return err
	}
	return s.forgetLeftBehindIndex(ctx, id)
}

func (s *Service) leftBehindIndex(id string) (LeftBehindIndex, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, entry := range s.leftBehind {
		if entry.ID == id {
			return entry, nil
		}
	}
	return LeftBehindIndex{}, ErrLeftBehindIndexNotFound
}

// forgetLeftBehindIndex removes an entry from the stored record and from the
// list.
func (s *Service) forgetLeftBehindIndex(ctx context.Context, id string) error {
	if s.indexLedger == nil {
		return ErrLeftBehindIndexNotFound
	}
	return s.mutateIndexStoreRecord(ctx, func(record *indexStoreRecord) error {
		record.LeftBehind = slices.DeleteFunc(record.LeftBehind, func(entry LeftBehindIndex) bool { return entry.ID == id })
		return nil
	})
}

// mutateIndexStoreRecord reads the record, changes it and writes it back under
// the locker, so a replica starting up and another clearing a notice cannot
// each write over the other's change. The list is then the record as written.
// A change that fails leaves the record unwritten.
func (s *Service) mutateIndexStoreRecord(ctx context.Context, change func(*indexStoreRecord) error) error {
	if s.indexLocker != nil {
		lockCtx, release, err := s.indexLocker.Acquire(ctx, indexStoresConfigKey)
		if err != nil {
			return fmt.Errorf("lock %s: %w", indexStoresConfigKey, err)
		}
		defer release()
		ctx = lockCtx
	}
	record, err := s.readIndexStoreRecord(ctx)
	if err != nil {
		return err
	}
	if err := change(&record); err != nil {
		return err
	}
	if err := s.writeIndexStoreRecord(ctx, record); err != nil {
		return err
	}
	s.mu.Lock()
	s.leftBehind = record.LeftBehind
	s.mu.Unlock()
	return nil
}

func (s *Service) readIndexStoreRecord(ctx context.Context) (indexStoreRecord, error) {
	var record indexStoreRecord
	row, err := s.indexLedger.GetConfig(ctx, indexStoresConfigKey)
	if errors.Is(err, configstore.ErrNotFound) || (err == nil && (row == nil || row.Value == "")) {
		return record, nil
	}
	if err != nil {
		return record, err
	}
	if err := sonic.Unmarshal([]byte(row.Value), &record); err != nil {
		return indexStoreRecord{}, fmt.Errorf("parse %s: %w", indexStoresConfigKey, err)
	}
	return record, nil
}

func (s *Service) writeIndexStoreRecord(ctx context.Context, record indexStoreRecord) error {
	value, err := sonic.Marshal(record)
	if err != nil {
		return err
	}
	return s.indexLedger.UpdateConfig(ctx, &tables.TableGovernanceConfig{Key: indexStoresConfigKey, Value: string(value)})
}
