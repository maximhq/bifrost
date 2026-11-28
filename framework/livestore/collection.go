package livestore

import (
	"context"
	"errors"
	"fmt"
	"hash/maphash"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
)

// mapShards is how many locks a collection's records and each index are spread over.
const mapShards = 256

// Loader reads one kind's rows from the database and builds compact records from them.
// ID names a record's primary key. Page reads rows in primary-key order after the cursor
// and returns the next cursor, "" at the end. ByID reads the named rows; a row that is gone
// is simply not returned. A collection registered with ReadInBatches never hands ByID more IDs,
// or asks Page for more rows, than its batch size; its ChildOf links are declared with ChildIDsOf.
type Loader[T any] struct {
	ID   func(*T) string
	Page func(ctx context.Context, db *gorm.DB, after string, limit int) (recs []*T, next string, err error)
	ByID func(ctx context.Context, db *gorm.DB, ids []string) ([]*T, error)
}

// Change is one record's change in memory. Old is nil for a new record; New is nil for a
// removed one.
type Change[T any] struct {
	ID       string
	Old, New *T
}

// OnDelete says what removing a parent does to its children, as a foreign key does.
type OnDelete int

const (
	// Cascade removes the children with the parent.
	Cascade OnDelete = iota
	// SetNull keeps the children and re-reads them, since their reference was cleared.
	SetNull
)

// Option configures a collection at registration: UniqueIndex, MultiIndex, ChildOf, ChildIDsOf,
// ReadInBatches, ShareIDs, WithNegativeTTL and Interns make one. An invalid option returns an
// error, and Register stops there and returns it.
type Option[T any] interface{ applyCollection(*Collection[T]) error }

// optionFunc is an Option that sets up the collection itself.
type optionFunc[T any] func(*Collection[T]) error

// applyCollection applies the option to c.
func (f optionFunc[T]) applyCollection(c *Collection[T]) error { return f(c) }

// Collection holds every row of one config-database kind, keyed by primary key, with the
// indexes declared at registration. It is loaded whole at boot and refreshed after every write,
// so a miss means the row does not exist. Records are never changed after they
// are published: a write swaps the pointer, so a reader can keep a record for the length of a
// request.
type Collection[T any] struct {
	store    *Store
	kind     Kind
	deps     []Kind
	loader   Loader[T]
	records  *shardedMap[*T]
	indexes  map[string]*keyIndex[T]
	interns  []internLink[T]
	parents  []parentLink[T]
	negTTL   time.Duration
	batch    int // the most IDs one Loader.ByID call is given, 0 for no limit (ReadInBatches)
	share    func(rec *T, shared func(kind Kind, id string) string)
	negative *Table[string, struct{}]
	reads    readGroup

	subsMu sync.Mutex
	subs   []*subscriber[T]
}

// Register adds a kind and returns its typed handle. dependsOn names the kinds its records
// reference; LoadAll and Refresh write those first. It fails after LoadAll or LoadRecords
// (ErrLoaded), when the kind is taken, when the loader lacks a function, and when an option
// is invalid.
func (s *Store) Register[T any](kind Kind, dependsOn []Kind, load Loader[T], opts ...Option[T]) (*Collection[T], error) {
	if load.ID == nil || load.Page == nil || load.ByID == nil {
		return nil, fmt.Errorf("livestore: loader for %q needs ID, Page and ByID", kind)
	}
	c := &Collection[T]{
		store:   s,
		kind:    kind,
		deps:    slices.Clone(dependsOn),
		loader:  load,
		records: newShardedMap[*T](),
		indexes: make(map[string]*keyIndex[T]),
	}
	for _, o := range opts {
		if err := o.applyCollection(c); err != nil {
			return nil, err
		}
	}
	if c.negTTL > 0 {
		c.negative = newTable[string, struct{}](internalPrefix+"negative_"+string(kind), tableOptions{ttl: c.negTTL})
	}
	links := make([]childHandle, 0, len(c.parents))
	for _, pl := range c.parents {
		if pl.byParent == nil && pl.childIDs == nil {
			return nil, fmt.Errorf("livestore: link of %q to %q has no read", kind, pl.parent)
		}
		if c.batch > 0 && pl.byParent != nil {
			return nil, fmt.Errorf("livestore: %q reads in batches, so its link to %q must be declared with ChildIDsOf: a ChildOf read returns records outside the batches", kind, pl.parent)
		}
		links = append(links, c.childHandle(pl))
	}
	if err := s.addCollection(kind, c, links); err != nil {
		return nil, err
	}
	if c.negative != nil {
		if err := s.addTable(c.negative.Name(), c.negative, ""); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// UniqueIndex adds an index with at most one record per key. An empty key is not indexed. A
// write that would give a key to a second record is refused with a UniqueConflictError (see
// Refresh), so use it only where the database keeps the key unique too. A key one publish moves
// from one record to another passes in one step, so a reader never finds both under it.
func UniqueIndex[T any](name string, key func(*T) string) Option[T] {
	return optionFunc[T](func(c *Collection[T]) error {
		return c.addIndex(&keyIndex[T]{name: name, unique: true, keys: singleKey(key)})
	})
}

// MultiIndex adds an index where a key can name several records and a record can have several
// keys. Empty keys are not indexed. GetBy returns the record with the lowest ID, the one SQL's
// First would; List returns all of them in ID order.
func MultiIndex[T any](name string, keys func(*T) []string) Option[T] {
	return optionFunc[T](func(c *Collection[T]) error {
		return c.addIndex(&keyIndex[T]{name: name, keys: nonEmptyKeys(keys)})
	})
}

// ChildOf mirrors a foreign key from this kind to parent; a collection read in batches
// (ReadInBatches) declares it with ChildIDsOf instead. Refreshing a parent re-reads its
// children with byParent, so a refresh finds child rows it was never told about; on a Cascade
// link, one whose record names no parent through any link, as when its row changed while it was
// read, is not published, and is dropped when held. A held child
// byParent no longer returns is read by ID: it is dropped when its row is gone, or names no
// parent on a Cascade link, and kept otherwise, as when the
// row moved to another parent. Removing a parent removes its children (Cascade) or re-reads them
// (SetNull). ChildOf does not order kinds; dependsOn does. It indexes the children by parent
// under ParentIndex(parent).
func ChildOf[T any](parent Kind, parentID func(*T) string, byParent func(ctx context.Context, db *gorm.DB, parentIDs []string) ([]*T, error), onDelete OnDelete) Option[T] {
	return optionFunc[T](func(c *Collection[T]) error {
		name := ParentIndex(parent)
		if err := c.addIndex(&keyIndex[T]{name: name, keys: singleKey(parentID)}); err != nil {
			return err
		}
		c.parents = append(c.parents, parentLink[T]{parent: parent, index: name, parentID: parentID, byParent: byParent, onDelete: onDelete})
		return nil
	})
}

// ChildIDsOf is ChildOf for a collection read with ReadInBatches: childIDs returns only the IDs of
// the children of parentIDs, and the store reads their records through Loader.ByID, in batches.
// The rest is ChildOf's: the parent index, finding children it was never told about, re-reading a
// held child childIDs no longer returns (dropped when its row is gone, or names no parent on a
// Cascade link; kept otherwise, as when it moved to another parent), leaving out a child whose
// record names no parent once read, and Cascade or SetNull when the parent is removed. The IDs and
// the records are two reads, so a row that changes between them is read as it is at the second.
func ChildIDsOf[T any](parent Kind, parentID func(*T) string, childIDs func(ctx context.Context, db *gorm.DB, parentIDs []string) ([]string, error), onDelete OnDelete) Option[T] {
	link := ChildOf(parent, parentID, nil, onDelete)
	return optionFunc[T](func(c *Collection[T]) error {
		if childIDs == nil {
			return fmt.Errorf("livestore: link of %q to %q has no read", c.kind, parent)
		}
		if err := link.applyCollection(c); err != nil {
			return err
		}
		c.parents[len(c.parents)-1].childIDs = childIDs
		return nil
	})
}

// DefaultReadBatch is the batch size for ReadInBatches. A loader that binds one statement parameter
// per record it reads (a preload of an association does) would pass the database's limit on a
// large read: SQLite refuses a statement with more than 32,766 parameters and Postgres more than
// 65,535. A thousand records per read stays far below both, and matches the page LoadAll reads.
const DefaultReadBatch = 1000

// ReadInBatches makes the store hand this collection's Loader.ByID at most size IDs per call. It
// splits every read by ID: a refresh and its RetryMissing and RetryErrors retries, the re-read of a
// held child a parent's refresh no longer lists or of a removed parent's SetNull children, the
// re-read of records holding a contested unique key, and Load. LoadAll and RefreshKind ask Page for
// at most size rows. It is for a loader whose reads bind a parameter per record they load, so no
// read passes the statement parameter limit however many records a refresh names. Its ChildOf links
// are declared with ChildIDsOf, whose records come through the batched ByID; Register refuses a
// ChildOf link, since its read returns records outside the batches. size must be positive.
func ReadInBatches[T any](size int) Option[T] {
	return optionFunc[T](func(c *Collection[T]) error {
		if size <= 0 {
			return fmt.Errorf("livestore: ReadInBatches on %q needs a positive size, got %d", c.kind, size)
		}
		c.batch = size
		return nil
	})
}

// ShareIDs gives the records a loader reads the copies of the IDs they name that the named records
// hold, so a record naming another adds no copy of its ID. share is called on each record a read
// returns, before it is published, with shared, which returns the copy the record of kind with that
// ID holds, or the ID itself when the store holds no such record. Kinds load in dependency order,
// so a record finds the records it depends on.
func ShareIDs[T any](share func(rec *T, shared func(kind Kind, id string) string)) Option[T] {
	return optionFunc[T](func(c *Collection[T]) error {
		c.share = share
		return nil
	})
}

// ParentIndex names the index ChildOf adds on a child collection, under which List finds the
// children of one parent.
func ParentIndex(parent Kind) string { return "parent_" + string(parent) }

// WithNegativeTTL makes Load remember a miss for ttl, so an ID with no row is read at most once
// per ttl. The misses are kept in a table the store owns, named internal:negative_<kind>, which
// the background sweep clears of expired misses and no public call can create or drop.
func WithNegativeTTL[T any](ttl time.Duration) Option[T] {
	return optionFunc[T](func(c *Collection[T]) error {
		c.negTTL = ttl
		return nil
	})
}

// addIndex registers ix on c. It fails on an empty or duplicate name.
func (c *Collection[T]) addIndex(ix *keyIndex[T]) error {
	if ix.name == "" {
		return fmt.Errorf("livestore: empty index name on %q", c.kind)
	}
	if _, ok := c.indexes[ix.name]; ok {
		return fmt.Errorf("livestore: index %q registered twice on %q", ix.name, c.kind)
	}
	ix.idOf = c.loader.ID
	ix.data = newShardedMap[keySlot[T]]()
	c.indexes[ix.name] = ix
	return nil
}

// Get returns the record with this primary key.
func (c *Collection[T]) Get(id string) (*T, bool) {
	return c.records.get(id)
}

// GetBy returns the record under key in the named index. In a multi index, where several records
// can share the key, it returns the one with the lowest ID. An unknown index finds nothing.
func (c *Collection[T]) GetBy(index, key string) (*T, bool) {
	ix, ok := c.indexes[index]
	if !ok {
		return nil, false
	}
	var out *T
	ix.data.view(key, func(slot keySlot[T], ok bool) {
		if !ok {
			return
		}
		if slot.many == nil {
			out = slot.rec
		} else if len(slot.many) > 0 {
			out = slot.many[minKey(slot.many)]
		}
	})
	return out, out != nil
}

// List returns every record under key in the named index, in ID order. An unknown index finds
// nothing.
func (c *Collection[T]) List(index, key string) []*T {
	ix, ok := c.indexes[index]
	if !ok {
		return nil
	}
	type entry struct {
		id  string
		rec *T
	}
	var entries []entry
	var lone *T
	found := false
	ix.data.view(key, func(slot keySlot[T], ok bool) {
		found = ok
		if slot.many == nil {
			// One record needs no ID and no sort, so a key naming one record (the common case for
			// per-user indexes) costs one small slice.
			lone = slot.rec
			return
		}
		entries = make([]entry, 0, slot.len())
		slot.each(ix.idOf, func(id string, rec *T) {
			entries = append(entries, entry{id, rec})
		})
	})
	if !found {
		return nil
	}
	if lone != nil {
		return []*T{lone}
	}
	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.id, b.id) })
	out := make([]*T, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.rec)
	}
	return out
}

// Range calls fn for every record until fn returns false. It is for boot, admin and background
// jobs, not the request path.
func (c *Collection[T]) Range(fn func(*T) bool) {
	c.records.each(func(_ string, v *T) bool { return fn(v) })
}

// Load is Get that reads the database on a miss and publishes what it finds, with its ChildOf
// children, as Refresh does. It covers a row written to the database before a refresh brought it in. A miss is never answered by a
// read that started before the call: calls made while a read of the ID runs wait for it and share
// one follow-up read, so a burst costs at most two reads, and a row written while the first read
// ran is found. Reads of one ID run under its record lock, so an older read never publishes over
// a newer one. With WithNegativeTTL, an ID with no row is remembered for the TTL, so it is read at
// most once per TTL.
func (c *Collection[T]) Load(ctx context.Context, id string) (*T, bool, error) {
	if err := inSubscriber(ctx); err != nil {
		return nil, false, err
	}
	if v, ok := c.Get(id); ok {
		return v, true, nil
	}
	s := c.store
	if !s.loaded.Load() {
		return nil, false, ErrNotLoaded
	}
	if s.db == nil {
		return nil, false, ErrNoDatabase
	}
	if c.negative != nil {
		if _, ok := c.negative.Get(id); ok {
			return nil, false, nil
		}
	}
	err := c.reads.Do(ctx, id, func() error {
		if _, ok := c.Get(id); ok {
			return nil
		}
		// The row is read as Refresh reads it, under its record lock and with its ChildOf children,
		// so a record loaded on a miss carries the same relationships as one a refresh brought in.
		// A row that is not there leaves nothing to publish.
		err := s.Refresh(ctx, []Ref{{Kind: c.kind, ID: id}}, RetryMissing(0, 0))
		var missing *MissingError
		if errors.As(err, &missing) {
			return nil
		}
		return err
	})
	if err != nil {
		return nil, false, fmt.Errorf("livestore: load %s %s: %w", c.kind, id, err)
	}
	v, ok := c.Get(id)
	if !ok && c.negative != nil {
		c.negative.Put(id, struct{}{})
	}
	return v, ok, nil
}

// Subscribe runs fn after each Refresh that changed this kind, with only the records that
// changed: a re-read row equal to the stored record is left alone and never passed. A refresh
// publishes kind by kind. New and changed records go in first, in dependency order, referenced
// kinds first, and each kind's subscribers are called once its records are in, before any record
// that references them is published, so what a subscriber derives from a record is there when a
// reader finds a record naming it. Removed records go after, in reverse order, and each kind's
// subscribers are called once its records are gone, after the records that referenced them were
// replaced. A kind with both is called twice, its new and changed records first. Calls run before
// Refresh returns, one call at a time. LoadAll gives subscribers registered before it every
// record as created. Subscribers rebuild derived data only: they must be fast, cannot fail,
// and do no slow I/O. Refresh, RefreshKind, Load, NewTable, RegisterState and DropTable called
// with the context a subscriber was given return ErrInSubscriber, and registering a collection
// or interned set returns ErrLoaded. Table writes never notify.
func (c *Collection[T]) Subscribe(fn func(ctx context.Context, changes []Change[T])) (cancel func()) {
	sub := &subscriber[T]{fn: fn}
	c.subsMu.Lock()
	c.subs = append(c.subs, sub)
	c.subsMu.Unlock()
	return func() {
		c.subsMu.Lock()
		c.subs = slices.DeleteFunc(c.subs, func(x *subscriber[T]) bool { return x == sub })
		c.subsMu.Unlock()
	}
}

// ForgetNegative drops what Load remembers about id having no row, so the next Load reads the
// database again. It does nothing for a collection without WithNegativeTTL.
func (c *Collection[T]) ForgetNegative(id string) {
	if c.negative != nil {
		c.negative.Delete(id)
	}
}

// childHandle adapts one ChildOf or ChildIDsOf link of c for the store. A ChildIDsOf link reads its
// children's IDs and then their records through loadByID, so they come in the collection's batches.
func (c *Collection[T]) childHandle(pl parentLink[T]) childHandle {
	return childHandle{
		parent:   pl.parent,
		child:    c,
		onDelete: pl.onDelete,
		byParent: func(ctx context.Context, db *gorm.DB, parentIDs []string) (map[string]any, error) {
			if pl.childIDs != nil {
				ids, err := pl.childIDs(ctx, db, parentIDs)
				if err != nil {
					return nil, err
				}
				if len(ids) == 0 {
					return map[string]any{}, nil
				}
				return c.loadByID(ctx, db, dedupe(ids))
			}
			recs, err := pl.byParent(ctx, db, parentIDs)
			if err != nil {
				return nil, err
			}
			return c.byID(recs), nil
		},
		hasParent: func(rec any) bool { return pl.parentID(rec.(*T)) != "" },
		namesAParent: func(rec any) bool {
			for _, other := range c.parents {
				if other.parentID(rec.(*T)) != "" {
					return true
				}
			}
			return false
		},
		childrenOf: func(parentID string) []string {
			var ids []string
			c.indexes[pl.index].data.view(parentID, func(slot keySlot[T], ok bool) {
				if !ok {
					return
				}
				ids = make([]string, 0, slot.len())
				slot.each(c.loader.ID, func(id string, _ *T) {
					ids = append(ids, id)
				})
			})
			return ids
		},
	}
}

// byID keys records by primary key.
func (c *Collection[T]) byID(recs []*T) map[string]any {
	out := make(map[string]any, len(recs))
	c.shareIDs(recs)
	for _, r := range recs {
		out[c.loader.ID(r)] = r
	}
	return out
}

// kindName returns the collection's kind.
func (c *Collection[T]) kindName() Kind { return c.kind }

// dependencies returns the kinds this collection's records reference.
func (c *Collection[T]) dependencies() []Kind { return c.deps }

// loadPage reads one page of records for LoadAll and RefreshKind.
func (c *Collection[T]) loadPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]any, string, error) {
	recs, next, err := c.readPage(ctx, db, after, limit)
	if err != nil {
		return nil, "", err
	}
	c.sharePage(recs)
	return recs, next, nil
}

// readPage reads one page of records without sharing their IDs, for a caller that shares them
// later, once the kinds they name are published.
func (c *Collection[T]) readPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]any, string, error) {
	recs, next, err := c.loader.Page(ctx, db, after, c.pageLimit(limit))
	if err != nil {
		return nil, "", err
	}
	out := make([]any, 0, len(recs))
	for _, r := range recs {
		out = append(out, r)
	}
	return out, next, nil
}

// pageLimit is how many rows the store asks Page for when it wants limit: limit, or the batch size
// of a collection read in batches when that is smaller.
func (c *Collection[T]) pageLimit(limit int) int {
	if c.batch > 0 && c.batch < limit {
		return c.batch
	}
	return limit
}

// sharePage shares the IDs of records readPage read, as loadPage does right after its read.
func (c *Collection[T]) sharePage(recs []any) {
	if c.share == nil {
		return
	}
	for _, r := range recs {
		c.share(r.(*T), c.store.sharedID)
	}
}

// loadByID reads the records for the named rows, keyed by primary key. Every read by ID the store
// makes comes here, so a collection read in batches hands Loader.ByID at most its batch size of IDs
// per call, and a failed batch fails the read.
func (c *Collection[T]) loadByID(ctx context.Context, db *gorm.DB, ids []string) (map[string]any, error) {
	if c.batch <= 0 || len(ids) <= c.batch {
		recs, err := c.loader.ByID(ctx, db, ids)
		if err != nil {
			return nil, err
		}
		return c.byID(recs), nil
	}
	out := make(map[string]any, len(ids))
	for batch := range slices.Chunk(ids, c.batch) {
		recs, err := c.loader.ByID(ctx, db, batch)
		if err != nil {
			return nil, err
		}
		maps.Copy(out, c.byID(recs))
	}
	return out, nil
}

// shareIDs passes each of recs to the collection's ShareIDs function, when it has one.
func (c *Collection[T]) shareIDs(recs []*T) {
	if c.share == nil {
		return
	}
	for _, r := range recs {
		c.share(r, c.store.sharedID)
	}
}

// heldID returns the primary key of the record held under id, as the record itself holds it.
func (c *Collection[T]) heldID(id string) (string, bool) {
	rec, ok := c.records.get(id)
	if !ok {
		return "", false
	}
	return c.loader.ID(rec), true
}

// idOf returns the primary key of a record held as any.
func (c *Collection[T]) idOf(v any) string { return c.loader.ID(v.(*T)) }

// putAll publishes the records in ups in the order of ids, keeping the stored pointer of a record
// that did not change, and returns what changed. It writes every record and adds its index keys
// before it drops any record's old keys, so a key passing from one record to another in the same
// publish is never missing to a reader: until the publish drops it, a reader finds the record that
// held the key before the publish. Each record's interned parts take their shared copies, and a
// replaced record gives its uses back. The store lock is held.
func (c *Collection[T]) putAll(ids []string, ups map[string]any) []anyChange {
	var changes []anyChange
	for _, id := range ids {
		next := ups[id].(*T)
		// The record is the loader's, not yet shared, so its interned parts take their shared
		// copies in place; a record equal to the one held then compares by pointer.
		for _, l := range c.interns {
			l.share(next)
		}
		old, had := c.records.get(id)
		if had && reflect.DeepEqual(old, next) {
			for _, l := range c.interns {
				l.release(next)
			}
			continue
		}
		c.records.set(id, next)
		for _, ix := range c.indexes {
			ix.add(id, next)
		}
		ch := anyChange{id: id, new: next}
		if had {
			for _, l := range c.interns {
				l.release(old)
			}
			ch.old = old
		}
		changes = append(changes, ch)
	}
	for _, ch := range changes {
		if ch.old == nil {
			continue
		}
		for _, ix := range c.indexes {
			ix.drop(ch.id, ch.old.(*T), ch.new.(*T))
		}
	}
	return changes
}

// uniqueConflicts reports every key of a unique index that writing ups and dropping removes would
// leave with more than one record. A held record counts under its current keys unless ups
// replaces it or removes drops it. The store lock is held.
func (c *Collection[T]) uniqueConflicts(ups map[string]any, removes map[string]struct{}) []*UniqueConflictError {
	var out []*UniqueConflictError
	names := make([]string, 0, len(c.indexes))
	for name, ix := range c.indexes {
		if ix.unique {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		ix := c.indexes[name]
		claims := make(map[string][]string)
		for id, v := range ups {
			for _, k := range ix.keys(v.(*T)) {
				claims[k] = append(claims[k], id)
			}
		}
		keys := make([]string, 0, len(claims))
		for k := range claims {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			ids := claims[k]
			ix.data.view(k, func(slot keySlot[T], ok bool) {
				if !ok {
					return
				}
				slot.each(ix.idOf, func(held string, _ *T) {
					if _, replaced := ups[held]; replaced {
						return
					}
					if _, dropped := removes[held]; dropped {
						return
					}
					ids = append(ids, held)
				})
			})
			if len(ids) > 1 {
				slices.Sort(ids)
				out = append(out, &UniqueConflictError{Kind: c.kind, Index: name, Key: k, IDs: ids})
			}
		}
	}
	return out
}

// remove drops the record for id from the records, indexes and use counts, and returns it, or
// nil when there was none. The store lock is held.
func (c *Collection[T]) remove(id string) any {
	old, had := c.records.get(id)
	if !had {
		return nil
	}
	c.records.delete(id)
	for _, ix := range c.indexes {
		ix.drop(id, old, nil)
	}
	for _, l := range c.interns {
		l.release(old)
	}
	return old
}

// ids lists every primary key held.
func (c *Collection[T]) ids() []string {
	var out []string
	c.records.each(func(id string, _ *T) bool {
		out = append(out, id)
		return true
	})
	return out
}

// hasSubscribers reports whether anything subscribes to this kind.
func (c *Collection[T]) hasSubscribers() bool {
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	return len(c.subs) > 0
}

// notify types changes for this collection and calls each subscriber once.
func (c *Collection[T]) notify(ctx context.Context, changes []anyChange) {
	c.subsMu.Lock()
	subs := slices.Clone(c.subs)
	c.subsMu.Unlock()
	if len(subs) == 0 {
		return
	}
	typed := make([]Change[T], 0, len(changes))
	for _, ch := range changes {
		var o, n *T
		if ch.old != nil {
			o = ch.old.(*T)
		}
		if ch.new != nil {
			n = ch.new.(*T)
		}
		typed = append(typed, Change[T]{ID: ch.id, Old: o, New: n})
	}
	for _, sub := range subs {
		sub.fn(ctx, typed)
	}
}

// subscriber is one Subscribe call.
type subscriber[T any] struct {
	fn func(ctx context.Context, changes []Change[T])
}

// parentLink is one ChildOf or ChildIDsOf declaration: byParent reads a ChildOf link's child
// records, childIDs a ChildIDsOf link's child IDs.
type parentLink[T any] struct {
	parent   Kind
	index    string
	parentID func(*T) string // the parent a child record names through the link, "" for none
	byParent func(ctx context.Context, db *gorm.DB, parentIDs []string) ([]*T, error)
	childIDs func(ctx context.Context, db *gorm.DB, parentIDs []string) ([]string, error)
	onDelete OnDelete
}

// keyIndex maps keys to the records under them. A key with one record holds just its pointer; in a
// multi index, a second record turns the key's slot into a map of ID to record, and dropping back to one record
// turns it into a pointer again, so a key costs a pointer, not a map, in the common case. A write changes a key's map
// in place under its shard's write lock, and readers only see the slot through shardedMap.view
// under the read lock, so a write costs the same however many records share the key and no reader
// sees a map half-written.
type keyIndex[T any] struct {
	name   string
	unique bool
	keys   func(*T) []string
	idOf   func(*T) string // the collection's primary key, for a slot holding one record
	data   *shardedMap[keySlot[T]]
}

// keySlot is what an index holds under one key: one record (rec), or many records in a map once a
// second one arrives. A lone record's ID is read from the record itself.
type keySlot[T any] struct {
	rec  *T
	many map[string]*T
}

// len reports how many records the slot holds.
func (s keySlot[T]) len() int {
	if s.many != nil {
		return len(s.many)
	}
	if s.rec != nil {
		return 1
	}
	return 0
}

// each calls fn for every record in the slot, in no particular order; idOf names a lone record.
func (s keySlot[T]) each(idOf func(*T) string, fn func(id string, rec *T)) {
	if s.many == nil {
		if s.rec != nil {
			fn(idOf(s.rec), s.rec)
		}
		return
	}
	for id, rec := range s.many {
		fn(id, rec)
	}
}

// drop takes the record id out from under each key old has and next does not; next is nil when the
// record is removed. A unique key another record took in the same publish stays with that record.
func (ix *keyIndex[T]) drop(id string, old, next *T) {
	var newKeys []string
	if next != nil {
		newKeys = ix.keys(next)
	}
	for _, k := range ix.keys(old) {
		if slices.Contains(newKeys, k) {
			continue
		}
		ix.data.update(k, func(slot keySlot[T], ok bool) (keySlot[T], bool) {
			if !ok {
				return slot, false
			}
			if slot.many == nil {
				if slot.rec != nil && ix.idOf(slot.rec) == id {
					return keySlot[T]{}, false
				}
				return slot, true
			}
			delete(slot.many, id)
			if len(slot.many) == 1 {
				for _, rec := range slot.many {
					return keySlot[T]{rec: rec}, true
				}
			}
			return slot, len(slot.many) > 0
		})
	}
}

// add files next under each of its keys, replacing the record's previous version under a key it
// keeps. The keys it no longer has are dropped separately (drop), after every record of the
// publish is added.
func (ix *keyIndex[T]) add(id string, next *T) {
	for _, k := range ix.keys(next) {
		ix.data.update(k, func(slot keySlot[T], ok bool) (keySlot[T], bool) {
			if !ok || ix.unique {
				// A unique key passes to its new record in one step. Every publish is refused when it
				// would leave a unique key with two records, so a record still holding the key here
				// gives it up in the same publish, and its drop finds the key taken and leaves it.
				return keySlot[T]{rec: next}, true
			}
			if slot.many == nil {
				if cur := ix.idOf(slot.rec); cur != id {
					return keySlot[T]{many: map[string]*T{cur: slot.rec, id: next}}, true
				}
				slot.rec = next
				return slot, true
			}
			slot.many[id] = next
			return slot, true
		})
	}
}

// singleKey adapts a one-key function to the index's key-list form, dropping an empty key.
func singleKey[T any](key func(*T) string) func(*T) []string {
	return func(v *T) []string {
		k := key(v)
		if k == "" {
			return nil
		}
		return []string{k}
	}
}

// nonEmptyKeys wraps a key-list function to drop empty and repeated keys.
func nonEmptyKeys[T any](keys func(*T) []string) func(*T) []string {
	return func(v *T) []string {
		raw := keys(v)
		out := make([]string, 0, len(raw))
		for _, k := range raw {
			if k != "" && !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
		return out
	}
}

// minKey returns the lowest key of a non-empty map.
func minKey[V any](m map[string]V) string {
	first := true
	var lowest string
	for k := range m {
		if first || k < lowest {
			lowest = k
			first = false
		}
	}
	return lowest
}

// shardedMap is a string-keyed map spread over mapShards locks.
type shardedMap[V any] struct {
	seed   maphash.Seed
	shards [mapShards]mapShard[V]
}

// mapShard is one lock and its part of the map.
type mapShard[V any] struct {
	mu sync.RWMutex
	m  map[string]V
}

// newShardedMap returns an empty sharded map.
func newShardedMap[V any]() *shardedMap[V] {
	sm := &shardedMap[V]{seed: maphash.MakeSeed()}
	for i := range sm.shards {
		sm.shards[i].m = make(map[string]V)
	}
	return sm
}

// shard returns the shard that holds key.
func (sm *shardedMap[V]) shard(key string) *mapShard[V] {
	return &sm.shards[maphash.String(sm.seed, key)%mapShards]
}

// get returns the value for key.
func (sm *shardedMap[V]) get(key string) (V, bool) {
	sh := sm.shard(key)
	sh.mu.RLock()
	v, ok := sh.m[key]
	sh.mu.RUnlock()
	return v, ok
}

// view calls fn with the value under key while holding the shard's read lock, for a value that
// update changes in place. fn must not keep the value or anything it reaches past its return.
func (sm *shardedMap[V]) view(key string, fn func(V, bool)) {
	sh := sm.shard(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	v, ok := sh.m[key]
	fn(v, ok)
}

// set stores v under key.
func (sm *shardedMap[V]) set(key string, v V) {
	sh := sm.shard(key)
	sh.mu.Lock()
	sh.m[key] = v
	sh.mu.Unlock()
}

// delete removes key.
func (sm *shardedMap[V]) delete(key string) {
	sh := sm.shard(key)
	sh.mu.Lock()
	delete(sh.m, key)
	sh.mu.Unlock()
}

// update replaces the value under key with what fn returns, or removes it when fn returns
// false, all under the shard lock.
func (sm *shardedMap[V]) update(key string, fn func(V, bool) (V, bool)) {
	sh := sm.shard(key)
	sh.mu.Lock()
	cur, ok := sh.m[key]
	next, keep := fn(cur, ok)
	if keep {
		sh.m[key] = next
	} else {
		delete(sh.m, key)
	}
	sh.mu.Unlock()
}

// each calls fn for every entry until fn returns false. Each shard is copied before fn runs,
// so fn may read the map.
func (sm *shardedMap[V]) each(fn func(string, V) bool) {
	for i := range sm.shards {
		sh := &sm.shards[i]
		sh.mu.RLock()
		keys := make([]string, 0, len(sh.m))
		vals := make([]V, 0, len(sh.m))
		for k, v := range sh.m {
			keys = append(keys, k)
			vals = append(vals, v)
		}
		sh.mu.RUnlock()
		for j := range keys {
			if !fn(keys[j], vals[j]) {
				return
			}
		}
	}
}

// readGroup coalesces concurrent database reads of one key without ever handing a caller the
// result of a read that started before its call. A shared read (singleflight) would: a caller
// whose own write committed after the read began would get the pre-write row and memory would
// keep it. Do returns only after a run of fn that started after the call. Callers that arrive
// while a run is in flight wait for it, then share one follow-up run that covers every caller
// registered before it started, so a burst costs at most two runs unless callers keep arriving
// during the follow-up.
type readGroup struct {
	mu    sync.Mutex
	state map[string]*readState
	// shareInFlight lets a caller that arrives while a run is in progress share that run's result,
	// as one request in flight to an upstream does, instead of waiting for a run that starts after it.
	shareInFlight bool
	// runLockedHook, when set, runs in Do right after a caller takes the key's run lock. Tests use
	// it to hold a caller there; it is nil in production.
	runLockedHook func()
}

// readState tracks one key's request sequence and the newest finished run.
type readState struct {
	// requested is the sequence number handed to the latest caller.
	requested atomic.Uint64
	// run holds one token while a caller checks or runs for the key: it serialises runs and guards
	// covered, lastVal, lastErr and lastRunCanceled. A channel rather than a mutex, so a caller that
	// may give up waits in a select on its context.
	run chan struct{}
	// covered is the highest request sequence a finished run started after.
	covered uint64
	// lastVal and lastErr are the result of the run that set covered.
	lastVal any
	lastErr error
	// lastRunCanceled is set when that run failed with a cancellation or deadline error, which
	// comes from the runner's own context and says nothing about the store, so covered callers
	// whose context is still live run again instead of sharing it.
	lastRunCanceled bool
	// refs counts callers inside Do, so the entry is dropped once idle.
	refs int
}

// Do runs fn for key, or waits for and shares a run that started after this call. It returns the
// error of the run that served this caller. ctx is the caller's context; fn uses it for its work.
func (g *readGroup) Do(ctx context.Context, key string, fn func() error) error {
	_, err := g.do(ctx, key, false, func() (any, error) { return nil, fn() })
	return err
}

// DoValue is Do for a run that produces a value: every caller the run serves receives it. A
// caller that finds another caller's run in progress and whose ctx ends while it waits returns
// ctx.Err() without waiting further; a caller that finds the key idle runs whatever its ctx says.
func (g *readGroup) DoValue(ctx context.Context, key string, fn func() (any, error)) (any, error) {
	return g.do(ctx, key, true, fn)
}

// do is Do and DoValue: giveUp lets a waiting caller return when its ctx ends.
func (g *readGroup) do(ctx context.Context, key string, giveUp bool, fn func() (any, error)) (any, error) {
	st := g.acquire(key)
	defer g.release(key, st)

	seq := st.requested.Add(1)
	select {
	case st.run <- struct{}{}:
	default:
		if !giveUp {
			st.run <- struct{}{}
			break
		}
		select {
		case st.run <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	defer func() { <-st.run }()
	if g.runLockedHook != nil {
		g.runLockedHook()
	}
	if st.covered >= seq && (!st.lastRunCanceled || ctx.Err() != nil) {
		return st.lastVal, st.lastErr
	}
	// Every request numbered up to start was registered before fn begins, so this run serves
	// all of them.
	start := st.requested.Load()
	val, err := fn()
	if g.shareInFlight {
		// Every caller registered while fn ran shares its result too.
		start = st.requested.Load()
	}
	st.covered = start
	st.lastVal, st.lastErr = val, err
	// A deadline the run set for itself, while the runner's context is live, is an answer to share.
	// The error is checked first, so a fill that succeeded never reads the runner's context.
	st.lastRunCanceled = (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && ctx.Err() != nil
	return val, err
}

// acquire returns key's state, creating it when absent, and takes a reference to it.
func (g *readGroup) acquire(key string) *readState {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state == nil {
		g.state = make(map[string]*readState)
	}
	st := g.state[key]
	if st == nil {
		st = &readState{run: make(chan struct{}, 1)}
		g.state[key] = st
	}
	st.refs++
	return st
}

// release drops a reference and deletes key's state once no caller holds it, so the map does not
// grow with every ID ever read.
func (g *readGroup) release(key string, st *readState) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st.refs--
	if st.refs == 0 && g.state[key] == st {
		delete(g.state, key)
	}
}
