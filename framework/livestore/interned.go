package livestore

import "sync"

// Interned holds values stored once per content hash, such as a set of grants many records
// share. Loaders intern values as they build records, and records reference them by hash
// through Uses. A value is visible as soon as it is interned, so it is there before any record
// that references it is published.
type Interned[T any] struct {
	kind Kind
	hash func(*T) Hash

	mu   sync.RWMutex
	vals map[Hash]*internedEntry[T]
}

// internedEntry is one interned value and how many records use it. v is nil while records use a
// hash no value is held under; the entry keeps their uses so that the next Intern of the value
// fills it in counted.
type internedEntry[T any] struct {
	v         *T
	uses      int
	idleSince int64 // when uses last reached zero, or when the value was interned
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

// Intern stores v unless an equal value is already held, and returns its hash. Equal values
// share the first copy. A value records already use without it being held is stored with their
// uses.
func (in *Interned[T]) Intern(v *T) Hash {
	h := in.hash(v)
	at := now().UnixNano()
	in.mu.Lock()
	defer in.mu.Unlock()
	if e, ok := in.vals[h]; ok {
		if e.v == nil {
			e.v = v
		}
		if e.uses == 0 {
			e.idleSince = at // a loader is about to use it: restart the grace period
		}
		return h
	}
	in.vals[h] = &internedEntry[T]{v: v, idleSince: at}
	return h
}

// Get returns the value with hash h.
func (in *Interned[T]) Get(h Hash) (*T, bool) {
	in.mu.RLock()
	defer in.mu.RUnlock()
	e, ok := in.vals[h]
	if !ok || e.v == nil {
		return nil, false
	}
	return e.v, true
}

// acquire counts one more record using h. A use of a hash not held is kept on an entry with no
// value, so the value counts it once a later Intern stores it and no sweep drops it while the
// record uses it. The sweep keeps what a read still running interned, so a refresh never
// publishes a record whose value it interned and then lost.
func (in *Interned[T]) acquire(h Hash) {
	in.mu.Lock()
	defer in.mu.Unlock()
	e, ok := in.vals[h]
	if !ok {
		e = &internedEntry[T]{}
		in.vals[h] = e
	}
	e.uses++
}

// release counts one fewer record using h, starting its grace period when none is left.
func (in *Interned[T]) release(h Hash) {
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

// sweep drops values no record has used since before cutoff. An entry any record uses is kept,
// with or without its value.
func (in *Interned[T]) sweep(cutoff int64) {
	in.mu.Lock()
	defer in.mu.Unlock()
	for h, e := range in.vals {
		if e.uses == 0 && e.idleSince < cutoff {
			delete(in.vals, h)
		}
	}
}
