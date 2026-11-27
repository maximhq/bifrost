package warp

import (
	"fmt"

	"github.com/maximhq/bifrost/framework/vectorstore"
)

// Warp keeps its log index in one of the vector stores config.json configures, named by
// warp.vector_store ("" or omitted is the primary vector_store). The choice is read at startup
// and fixed for the life of the process: changing it takes a restart, and the new store starts
// with an empty index that a backfill fills. Nothing moves or deletes the index the old store
// holds. A choice that names no configured store, or one that could not be reached at startup,
// leaves Warp with no store, never another one.

// VectorStoreResolver finds the vector stores config.json configures; *vectorstore.Registry is one.
type VectorStoreResolver interface {
	Get(name string) (*vectorstore.NamedStore, bool)
	Primary() *vectorstore.NamedStore
}

// WithVectorStores keeps Warp's log index in the store config.json names (name, "" for the
// primary), resolved among stores at startup. Without it, Warp uses the store WithVectorStore
// gives it.
func WithVectorStores(stores VectorStoreResolver, name string) Option {
	return func(s *Service) { s.vectorStores, s.vectorChoiceName = stores, name }
}

// VectorStoreUnavailableError is ErrNoVectorStore for a chosen store Warp cannot use.
type VectorStoreUnavailableError struct {
	Name   string
	Reason string // "is not configured in config.json", "could not be reached at startup", ...
}

func (e *VectorStoreUnavailableError) Error() string {
	return fmt.Sprintf("Warp's vector store %q %s", e.Name, e.Reason)
}

func (e *VectorStoreUnavailableError) Unwrap() error { return ErrNoVectorStore }

// ConnectedVectorStore is the store Warp's log index is kept in, as the settings page shows it.
type ConnectedVectorStore struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Primary bool   `json:"primary"`
}

// vectorSelection is the store config.json names: the name as written ("" is the primary) and
// what it resolves to, nil when it names no configured store.
type vectorSelection struct {
	name  string
	entry *vectorstore.NamedStore
}

// store is the open connection the selection reaches, or nil.
func (v *vectorSelection) store() vectorstore.VectorStore {
	if v == nil || v.entry == nil || v.entry.Status != vectorstore.StoreConnected {
		return nil
	}
	return v.entry.Store
}

// resolveSelection finds what a configured name reaches.
func (s *Service) resolveSelection(name string) *vectorSelection {
	selection := &vectorSelection{name: name}
	if name == "" {
		selection.entry = s.vectorStores.Primary()
	} else if entry, ok := s.vectorStores.Get(name); ok {
		selection.entry = entry
	}
	return selection
}

// noVectorStore is the error for having no store: which chosen store is unavailable and why,
// when there is one to name.
func (s *Service) noVectorStore() error {
	if s.vectorChoice == nil {
		return ErrNoVectorStore
	}
	return unavailableVectorStore(s.vectorChoice)
}

func unavailableVectorStore(selection *vectorSelection) error {
	if selection.entry == nil {
		if selection.name == "" {
			return ErrNoVectorStore // no vector_store in config.json
		}
		return &VectorStoreUnavailableError{Name: selection.name, Reason: "is not configured in config.json"}
	}
	switch selection.entry.Status {
	case vectorstore.StoreFailed:
		return &VectorStoreUnavailableError{Name: selection.entry.Name, Reason: "could not be reached at startup"}
	case vectorstore.StoreDisabled:
		return &VectorStoreUnavailableError{Name: selection.entry.Name, Reason: "is disabled in config.json"}
	}
	return ErrNoVectorStore
}

// vectorStoreStatus is what the settings view says about the store: the configured name,
// whether Warp has a store, the store when it does, and why it has none when there is more to
// say than that.
func (s *Service) vectorStoreStatus() (name string, connected bool, store *ConnectedVectorStore, reason string) {
	if s.vectorChoice == nil {
		return "", s.vectorStore != nil, nil, ""
	}
	if s.vectorStore == nil {
		if err := unavailableVectorStore(s.vectorChoice); err != ErrNoVectorStore {
			reason = err.Error()
		}
		return s.vectorChoice.name, false, nil, reason
	}
	entry := s.vectorChoice.entry
	return s.vectorChoice.name, true, &ConnectedVectorStore{Name: entry.Name, Type: string(entry.Type), Primary: entry.Primary}, ""
}

// unreachableVectorStore is why the chosen store cannot be used now when it is configured (it
// failed at startup, or is disabled): it still holds the index, so a log deleted meanwhile keeps
// its delete queued for a later start. Nil when it can be used, or is not configured at all.
func (s *Service) unreachableVectorStore() error {
	if s.vectorChoice == nil || s.vectorChoice.entry == nil || s.vectorStore != nil {
		return nil
	}
	return unavailableVectorStore(s.vectorChoice)
}

// vectorStoreID identifies the data Warp's index is kept in, for backfill checkpoints; "" with
// no choice of stores.
func (s *Service) vectorStoreID() string {
	if s.vectorChoice == nil || s.vectorChoice.entry == nil {
		return ""
	}
	return s.vectorChoice.entry.ID
}

// checkpointIsForThisStore reports whether a backfill checkpoint was written into the data
// Warp's index is kept in now. config.json can name another store, or the same name another
// endpoint, between restarts, and a memory-only store starts empty on every boot: a run into
// other data neither resumes nor counts as done. A checkpoint that names no store was written
// before stores were recorded, into the primary store.
func (s *Service) checkpointIsForThisStore(storeID string) bool {
	if s.vectorChoice == nil {
		return true
	}
	entry := s.vectorChoice.entry
	if entry == nil {
		return false
	}
	if storeID == "" {
		return entry.Primary
	}
	return storeID == entry.ID
}
