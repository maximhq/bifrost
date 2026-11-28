package livestore

import (
	"fmt"
	"reflect"
	"sync"
)

// Interned holds values stored once per content hash, such as a set of grants many records
// share. A collection, table or cache declares which part of its values is interned here
// (Interns): as a record publishes or an entry is written, it puts the shared copy in that part and
// counts the use, so values holding equal copies share one and readers use the part as it is. A
// copy nothing has used for the grace period is dropped by the store's sweep. The pool has no
// methods of its own: a structure's Interns does all its work.
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

// Interns declares that part of each value is interned in pool: part returns the address of the
// value's interned field, which must be a pointer, and which the writer fills with the value's own
// copy. A collection of T, a table or state table holding T or *T, and a cache of *T all accept
// it; a table holding another type refuses it at creation. As a record publishes or an entry is
// written, the structure puts the pool's shared copy in the field and counts the use, so values
// holding equal copies share one and readers use the field as it is, with no lookup. A replaced,
// removed, expired or flushed value gives its use back, and a copy nothing has used for the grace
// period is dropped. A value may intern several parts, with one Interns each. A nil field holds
// nothing, and part returns nil when the value has no such field, as when the struct holding it is
// absent:
//
//	type A struct {
//		C *C  // interned
//		D *D  // holds F, may be nil
//	}
//	type D struct{ F *F } // F interned
//
//	Interns(cPool, func(a *A) **C { return &a.C })
//	Interns(fPool, func(a *A) **F {
//		if a.D == nil {
//			return nil
//		}
//		return &a.D.F
//	})
//
// A table or cache changes a pointer value's field in place as it is written, so the writer hands
// the value over and does not change it after.
func Interns[T, P any](pool *Interned[P], part func(*T) **P) InternsOption[T, P] {
	return InternsOption[T, P]{link: internPart[T, P]{pool: pool, part: part}}
}

// InternsOption is what Interns returns: an Option for a collection of T, a TableOption for a
// table or state table of T or *T, and a CacheOption for a cache of *T.
type InternsOption[T, P any] struct{ link internPart[T, P] }

// applyCollection adds the declaration to a collection of T.
func (o InternsOption[T, P]) applyCollection(c *Collection[T]) error {
	c.interns = append(c.interns, o.link)
	return nil
}

// applyTable adds the declaration to a table's options; NewTable binds it to the table's value
// type.
func (o InternsOption[T, P]) applyTable(opts *tableOptions) {
	opts.interns = append(opts.interns, o.link)
}

// applyCache adds the declaration to a cache of *T, whose entries table counts the uses.
func (o InternsOption[T, P]) applyCache(c *Cache[*T]) {
	c.interns = append(c.interns, cacheIntern[T, P]{o.link})
}

// internLink is one Interns declaration on a collection, seen without the interned value's type.
type internLink[T any] interface {
	share(rec *T)   // put the shared copy in rec's part, counting a use
	release(rec *T) // give back the use rec's part holds
}

// internPart is one Interns declaration: pool holds the shared copies of the field part addresses.
type internPart[T, P any] struct {
	pool *Interned[P]
	part func(*T) **P
}

// share puts the pool's shared copy of rec's part in rec, counting one more use of it. A part that
// is not there (a nil address) or holds nothing (a nil field) is skipped. A field that already
// holds the shared copy is not written again: the value may be published, as one a table already
// holds is when it is stored again, and readers may be reading the field.
func (l internPart[T, P]) share(rec *T) {
	if f := l.part(rec); f != nil && *f != nil {
		if shared := l.pool.share(*f); shared != *f {
			*f = shared
		}
	}
}

// release gives back the use rec's part holds, if it holds one.
func (l internPart[T, P]) release(rec *T) {
	if f := l.part(rec); f != nil && *f != nil {
		l.pool.release(*f)
	}
}

// tableInterns returns the declaration as a table of *T and as a table of T uses it, for
// bindInterns to pick the one that matches the table's value type.
func (l internPart[T, P]) tableInterns() (forPointers, forValues any, part reflect.Type) {
	return pointerIntern[T, P]{l}, valueIntern[T, P]{l}, reflect.TypeFor[T]()
}

// tableIntern is one Interns declaration on a table of V: share puts the shared copies in v and
// returns the value to store, and release gives back the uses v holds.
type tableIntern[V any] interface {
	share(v V) V
	release(v V)
}

// tableBinder is an Interns declaration a table has yet to bind to its value type.
type tableBinder interface {
	tableInterns() (forPointers, forValues any, part reflect.Type)
}

// bindInterns binds a table's Interns declarations to its value type V, which must be the part's
// type or a pointer to it, and names the table when one does not fit.
func bindInterns[V any](name string, binders []tableBinder) ([]tableIntern[V], error) {
	if len(binders) == 0 {
		return nil, nil
	}
	out := make([]tableIntern[V], 0, len(binders))
	for _, b := range binders {
		ptr, val, part := b.tableInterns()
		if l, ok := ptr.(tableIntern[V]); ok {
			out = append(out, l)
			continue
		}
		if l, ok := val.(tableIntern[V]); ok {
			out = append(out, l)
			continue
		}
		return nil, fmt.Errorf("livestore: table %q holds %v values, but an Interns part is a field of %v: the table must hold that type or a pointer to it", name, reflect.TypeFor[V](), part)
	}
	return out, nil
}

// pointerIntern is an Interns declaration on a table of *T: the field is set in the value itself.
type pointerIntern[T, P any] struct{ l internPart[T, P] }

// share puts the shared copy in v's part and returns v.
func (p pointerIntern[T, P]) share(v *T) *T {
	if v != nil {
		p.l.share(v)
	}
	return v
}

// release gives back the use v's part holds.
func (p pointerIntern[T, P]) release(v *T) {
	if v != nil {
		p.l.release(v)
	}
}

// valueIntern is an Interns declaration on a table of T: the field is set in the copy stored.
type valueIntern[T, P any] struct{ l internPart[T, P] }

// share returns v with the shared copy in its part.
func (p valueIntern[T, P]) share(v T) T {
	p.l.share(&v)
	return v
}

// release gives back the use v's part holds.
func (p valueIntern[T, P]) release(v T) {
	p.l.release(&v)
}

// cacheIntern is an Interns declaration on a cache of *T, applied to its entries.
type cacheIntern[T, P any] struct{ l internPart[T, P] }

// share puts the shared copy in the cached value's part.
func (c cacheIntern[T, P]) share(e cacheEntry[*T]) cacheEntry[*T] {
	if e.value != nil {
		c.l.share(e.value)
	}
	return e
}

// release gives back the use the cached value's part holds.
func (c cacheIntern[T, P]) release(e cacheEntry[*T]) {
	if e.value != nil {
		c.l.release(e.value)
	}
}
