package livestore

import "sync"

// Interned holds values stored once per content hash, such as a set of grants many records
// share. A collection declares which part of its records is interned here (Interns): as a record
// publishes, the collection puts the shared copy in that part and counts the use, so records
// holding equal values share one copy and readers use the part as it is. A value no record has
// used for the grace period is dropped by the store's sweep.
type Interned[T any] struct {
	kind Kind
	hash func(*T) Hash

	mu   sync.RWMutex
	vals map[Hash]*internedEntry[T]
}

// internedEntry is one shared copy and how many records use it.
type internedEntry[T any] struct {
	v         *T
	uses      int
	idleSince int64 // when uses last reached zero
}

// RegisterInterned adds an interned set under kind. hash computes a value's hash, or returns
// one already stored with the value. It fails after LoadAll or LoadRecords (ErrLoaded), which
// covers a call from a subscriber, and when the kind is already taken.
func (s *Store) RegisterInterned[T any](kind Kind, hash func(*T) Hash) (*Interned[T], error) {
	in := &Interned[T]{kind: kind, hash: hash, vals: make(map[Hash]*internedEntry[T])}
	if err := s.addInterned(kind, in); err != nil {
		return nil, err
	}
	return in, nil
}

// share returns the copy held under v's hash, storing v when there is none, and counts one more
// use of it.
func (in *Interned[T]) share(v *T) *T {
	h := in.hash(v)
	in.mu.Lock()
	defer in.mu.Unlock()
	e, ok := in.vals[h]
	if !ok {
		e = &internedEntry[T]{v: v}
		in.vals[h] = e
	}
	e.uses++
	return e.v
}

// release counts one fewer use of the copy under v's hash, starting its grace period when none is
// left.
func (in *Interned[T]) release(v *T) {
	h := in.hash(v)
	in.mu.Lock()
	defer in.mu.Unlock()
	e, ok := in.vals[h]
	if !ok {
		return
	}
	e.uses--
	if e.uses <= 0 {
		e.uses = 0
		e.idleSince = now().UnixNano()
	}
}

// sweep drops the copies no record has used since before cutoff.
func (in *Interned[T]) sweep(cutoff int64) {
	in.mu.Lock()
	defer in.mu.Unlock()
	for h, e := range in.vals {
		if e.uses == 0 && e.idleSince < cutoff {
			delete(in.vals, h)
		}
	}
}
