// Package livestore holds the data a Bifrost process keeps in memory: configuration records loaded
// from the config store, runtime state keyed by those records, and tables a component owns.
//
// New returns a store with a collection registered for each config store table, reached through
// Store.Collections, and the tables derived from them that several components read, reached through
// Store.Views. Configuration reaches memory three ways: LoadAll reads every registered kind from
// the database at boot, LoadRecords publishes records the caller built for a process without a
// database, and Refresh re-reads the named rows from the database after a write. A write reaches
// memory only through the refresh that follows it, so a record holds what the database held when
// its row was last read. Readers keep typed collections and read through them; nothing on the
// request path looks a collection up by name.
//
// Which structure holds a piece of data follows from what a miss means:
//
//   - Collection: every row of one config-database kind, loaded at boot and refreshed after every
//     write. A miss means the row does not exist; Load is only the safety net for a row written to
//     the database that no refresh has brought in yet.
//   - Cache: only the values someone asked for, fetched by a loader on a miss from wherever they
//     live (the config store included) and dropped by expiry or eviction. A miss means "go fetch".
//   - Table: what the process itself writes, with no loader. A miss means nothing was written yet.
//     State tables keyed by a record and tables with a generation are tables.
package livestore

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

// Kind names one record type, such as "virtual_key" or "budget". Registration, ordering and
// refreshes refer to collections by kind; reads go through the typed handle instead.
type Kind string

// Hash identifies an interned value by its content.
type Hash [16]byte

var (
	// ErrNoDatabase is returned by calls that read the database when the store was booted from
	// records (LoadRecords) rather than from a database.
	ErrNoDatabase = errors.New("livestore: no database")
	// ErrNotLoaded is returned by refreshes and loads before LoadAll or LoadRecords has run.
	ErrNotLoaded = errors.New("livestore: store not loaded")
	// ErrLoaded is returned by a registration after LoadAll or LoadRecords, and by a second
	// LoadAll or LoadRecords.
	ErrLoaded = errors.New("livestore: store already loaded")
	// ErrInSubscriber is returned when a subscriber calls a method that changes the store (a
	// refresh, a load, or creating or dropping a table) with the context it was given: it runs
	// inside a publish, so the call would deadlock or change the store under it.
	ErrInSubscriber = errors.New("livestore: called from a subscriber")
	// ErrUniqueConflict matches a UniqueConflictError: a write that would leave two records under
	// one key of a unique index, which is refused.
	ErrUniqueConflict = errors.New("livestore: unique index conflict")
	// ErrNoLiveStore is what a component that needs the live store wraps when it is given none,
	// so every such refusal reads the same and can be matched with errors.Is.
	ErrNoLiveStore = errors.New("live store is required")
)

// UniqueConflictError reports a write refused because it would leave more than one record under
// Key in the unique index Index of Kind. Nothing from that write was published.
type UniqueConflictError struct {
	Kind  Kind
	Index string
	Key   string
	IDs   []string // the records that would share the key, in ID order
}

// Error names the records and the index they would share a key in, never the key: a unique key
// can be a credential, such as a virtual key's value, and callers log this error. A caller that
// needs the key reads Key through errors.As.
func (e *UniqueConflictError) Error() string {
	return fmt.Sprintf("livestore: %s %s would share a key in unique index %s", e.Kind, strings.Join(e.IDs, ", "), e.Index)
}

// Is makes errors.Is(err, ErrUniqueConflict) match.
func (e *UniqueConflictError) Is(target error) bool { return target == ErrUniqueConflict }

const (
	// pageSize is how many rows LoadAll and RefreshKind read per page.
	pageSize = 1000
	// loadParallelism is how many kinds LoadAll reads at once. Each read holds one database
	// connection and its kind's pages until they are published.
	loadParallelism = 8
	// recordLockStripes is how many locks records are spread over; two records can share one.
	recordLockStripes = 1024
	// sweepInterval is how often expired table entries and unused interned values are removed.
	sweepInterval = 30 * time.Second
	// internedGrace is how long an interned value no record uses is kept, so a record that gives
	// its value up and takes it back soon after, as a reload or a reverted edit does, finds the
	// copy still there.
	internedGrace = 5 * time.Minute
)

// internalPrefix starts the name of every table the store creates for itself, such as the misses
// a collection's Load remembers. NewTable and DropTable refuse such names, so no component can
// create one ahead of the store or drop one from under it.
const internalPrefix = "internal:"

// now is the clock every expiry and grace period reads. Tests replace it.
var now = time.Now

// Ref names one database row and the record built from it. Refresh re-reads the row; a Remove
// ref drops the record without a read, and is for an explicit delete only. A caller that lists
// rows itself passes a held record its list leaves out as a plain ref, so the record is removed
// only when its own read finds the row gone: the list may be older than the record.
type Ref struct {
	Kind   Kind
	ID     string
	Remove bool // drop now, without reading the database: deletes
}

// MissingError reports rows a refresh with RetryMissing could not find. Their records are
// left as they were, unless the same refresh also reads a record the row is a child of: the
// parent's read of its children then settles the row's record as it settles any child's, so the
// record can be updated or removed although the row is still reported here.
type MissingError struct {
	Refs []Ref
}

// Error lists the rows that were still missing.
func (e *MissingError) Error() string {
	parts := make([]string, 0, len(e.Refs))
	for _, r := range e.Refs {
		parts = append(parts, string(r.Kind)+"/"+r.ID)
	}
	return "livestore: rows not found after retries: " + strings.Join(parts, ", ")
}

// RefreshOption changes how Refresh treats the rows it reads.
type RefreshOption func(*refreshOptions)

// refreshOptions collects the options one Refresh call was given.
type refreshOptions struct {
	keepMissing bool
	retries     int
	wait        time.Duration
	errRetries  int
	errWait     time.Duration
	// errWithin is set by RetryErrorsWithin: a failed read is retried until errUntil, which
	// Refresh sets from the context's deadline, or errBudget from its start when there is none.
	errWithin bool
	errBudget time.Duration
	errUntil  time.Time
}

// RetryMissing re-reads a missing row up to n times, wait apart. A row still missing after
// that is left as it is and reported in a MissingError, not removed (unless the same refresh
// reads its parent; see MissingError): the caller cannot know the row was deleted, since a
// message about a write can arrive before the row is readable. Deletes are Remove refs instead.
func RetryMissing(n int, wait time.Duration) RefreshOption {
	return func(o *refreshOptions) {
		o.keepMissing = true
		o.retries = n
		o.wait = wait
	}
}

// RetryErrors reads again when a refresh's read fails with an error, up to n times, wait apart,
// for a caller that cannot report the failure back, such as one applying a change made
// elsewhere. A failed read publishes nothing, and each retry reads every row again. It never
// retries once the context is cancelled or past its deadline, and returns the last read's error
// when the retries run out. It composes with RetryMissing, which handles a read that succeeds
// without finding a row. It replaces a RetryErrorsWithin given before it.
func RetryErrors(n int, wait time.Duration) RefreshOption {
	return func(o *refreshOptions) {
		o.errRetries = n
		o.errWait = wait
		o.errWithin = false
	}
}

// RetryErrorsWithin reads again when a refresh's read fails with an error, wait apart, until the
// context's deadline, or until budget has passed since Refresh was called when the context has
// none: for a caller that answers for its own write, which has to give up in time to report the
// failure. A retry starts only when it would start before that limit, never once the context is
// cancelled or past its deadline, and the last read's error is returned when retrying stops. The
// limit is checked before each retry starts; a retry that has started then waits for its record
// locks like any refresh, so its read can run past the limit and its result is returned. As
// with RetryErrors, a failed read publishes nothing and each retry reads every row again; the one
// of the two given last applies. It composes with RetryMissing, which handles a read that succeeds
// without finding a row.
func RetryErrorsWithin(budget, wait time.Duration) RefreshOption {
	return func(o *refreshOptions) {
		o.errRetries = 0
		o.errWithin = true
		o.errBudget = budget
		o.errWait = wait
	}
}

// retryError reports whether a read that failed on attempt (0 for the first read) is read again:
// while RetryErrors' count lasts or, with RetryErrorsWithin, when a read after the wait would
// start before the limit.
func (o refreshOptions) retryError(attempt int) bool {
	if o.errWithin {
		return time.Now().Add(max(o.errWait, 0)).Before(o.errUntil)
	}
	return attempt < o.errRetries
}

// withinLimit reports whether a retry may still start now. With RetryErrorsWithin it is checked
// again once the wait is over, since a wait can end later than it was asked to; with RetryErrors
// the count alone decides.
func (o refreshOptions) withinLimit() bool {
	return !o.errWithin || time.Now().Before(o.errUntil)
}

// Record is one record handed to LoadRecords. Build it with Rec.
type Record struct {
	coll  kindHandle // the collection Rec built it from, which LoadRecords checks is its own
	id    string
	value any
}

// Rec builds the Record for v in collection c. The record keeps c, so LoadRecords can refuse a
// record built from a collection another store registered instead of publishing a value of the
// wrong type.
func Rec[T any](c *Collection[T], v *T) Record {
	return Record{coll: c, id: c.loader.ID(v), value: v}
}

// Store owns every collection and table, and the one write path for configuration records.
// Collections holds the typed collection of each config store table; a build that registers
// collections of its own keeps their handles beside it. Views holds the tables the store derives
// from those collections for every component.
//
// tablesMu is separate from mu, which a publish holds while subscribers run, so a table call made
// from a subscriber can never wait on its own publish.
type Store struct {
	Collections *Collections
	Views       *Views

	mu       sync.Mutex  // the kind registry, and one publish at a time
	frozen   atomic.Bool // set by LoadAll or LoadRecords; kind registration fails after it
	loaded   atomic.Bool
	db       *gorm.DB
	kinds    map[Kind]kindHandle
	interned map[Kind]internedHandle
	order    []Kind // collections in dependency order, referenced kinds first
	children map[Kind][]childHandle

	tablesMu sync.RWMutex // guards tables and states; see Store
	tables   map[string]tableHandle
	states   map[Kind][]tableHandle

	recordLocks [recordLockStripes]sync.Mutex

	sweepOnce sync.Once
	closeOnce sync.Once
	stop      chan struct{}
}

// newStore returns a store with nothing registered. New builds on it; tests of the engine use it
// directly, with kinds of their own.
func newStore() *Store {
	return &Store{
		kinds:    make(map[Kind]kindHandle),
		interned: make(map[Kind]internedHandle),
		children: make(map[Kind][]childHandle),
		tables:   make(map[string]tableHandle),
		states:   make(map[Kind][]tableHandle),
		stop:     make(chan struct{}),
	}
}

// Close stops the background sweep of expired table entries.
func (s *Store) Close() {
	s.closeOnce.Do(func() { close(s.stop) })
}

// claimKind checks that a kind name is free for a new collection or interned set. It fails
// after the store has loaded, and when the name is empty or already taken. The caller holds s.mu.
func (s *Store) claimKind(kind Kind) error {
	if s.frozen.Load() {
		return fmt.Errorf("register %q: %w", kind, ErrLoaded)
	}
	if kind == "" {
		return errors.New("livestore: empty kind")
	}
	if _, ok := s.kinds[kind]; ok {
		return fmt.Errorf("livestore: kind %q registered twice", kind)
	}
	if _, ok := s.interned[kind]; ok {
		return fmt.Errorf("livestore: kind %q registered twice", kind)
	}
	return nil
}

// addCollection registers a collection and the parent links it declared with ChildOf. A loaded
// store refuses before taking the lock: subscribers only run once the store is loaded, and they
// run under that lock, so a registration from a subscriber fails instead of waiting forever.
func (s *Store) addCollection(kind Kind, h kindHandle, links []childHandle) error {
	if s.frozen.Load() {
		return fmt.Errorf("register %q: %w", kind, ErrLoaded)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.claimKind(kind); err != nil {
		return err
	}
	s.kinds[kind] = h
	for _, l := range links {
		s.children[l.parent] = append(s.children[l.parent], l)
	}
	return nil
}

// addInterned registers an interned set under its kind. Like addCollection, a loaded store
// refuses before taking the lock.
func (s *Store) addInterned(kind Kind, h internedHandle) error {
	if s.frozen.Load() {
		return fmt.Errorf("register %q: %w", kind, ErrLoaded)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.claimKind(kind); err != nil {
		return err
	}
	s.interned[kind] = h
	s.sweepOnce.Do(func() { go s.sweepLoop() })
	return nil
}

// freeze fixes the registry and computes the dependency order. It fails when called twice, on
// a dependency cycle, or when a dependency or ChildOf parent names an unknown kind; the registry
// stays open then, so the caller can fix it and load again.
func (s *Store) freeze() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen.Load() {
		return ErrLoaded
	}
	for parent, links := range s.children {
		if _, ok := s.kinds[parent]; !ok {
			return fmt.Errorf("livestore: %q is a child of unknown kind %q", links[0].child.kindName(), parent)
		}
	}
	names := make([]Kind, 0, len(s.kinds))
	for k := range s.kinds {
		names = append(names, k)
	}
	slices.Sort(names)
	state := make(map[Kind]int, len(names)) // 1 visiting, 2 done
	order := make([]Kind, 0, len(names))
	var visit func(k Kind, path []Kind) error
	visit = func(k Kind, path []Kind) error {
		switch state[k] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("livestore: dependency cycle: %v", append(path, k))
		}
		state[k] = 1
		deps := slices.Clone(s.kinds[k].dependencies())
		slices.Sort(deps)
		for _, d := range deps {
			if _, ok := s.interned[d]; ok {
				continue // interned values are shared as the records holding them publish
			}
			if _, ok := s.kinds[d]; !ok {
				return fmt.Errorf("livestore: %q depends on unknown kind %q", k, d)
			}
			if err := visit(d, append(path, k)); err != nil {
				return err
			}
		}
		state[k] = 2
		order = append(order, k)
		return nil
	}
	for _, k := range names {
		if err := visit(k, nil); err != nil {
			return err
		}
	}
	s.order = order
	s.frozen.Store(true)
	return nil
}

// DB is the database the store loaded from and refreshes read, nil before LoadAll and for a store
// loaded from records. A caller that has to read rows the way a refresh will, before it runs, reads
// them here.
func (s *Store) DB() *gorm.DB {
	return s.db
}

// LoadAll fixes the registry and fills every collection from db. The kinds' rows are read
// concurrently, since no loader reads another collection; the records are then published page by
// page in dependency order, IDs shared as each page is published, exactly as a kind-by-kind load
// publishes them. Subscribers registered before it receive each kind once, every record as
// created. Registration fails after it with ErrLoaded, and so does a second LoadAll or
// LoadRecords. Rows that would put two records under one key of a unique index fail it with a
// UniqueConflictError, and a read that fails fails it before anything is published.
func (s *Store) LoadAll(ctx context.Context, db *gorm.DB) error {
	if err := inSubscriber(ctx); err != nil {
		return err
	}
	if db == nil {
		return ErrNoDatabase
	}
	if err := s.freeze(); err != nil {
		return err
	}
	s.db = db
	read, err := s.readAllKinds(ctx, db)
	if err != nil {
		return err
	}
	for i, kind := range s.order {
		h := s.kinds[kind]
		collect := h.hasSubscribers()
		var changes []anyChange
		for _, recs := range read[i] {
			// Every kind these records name is published by now, so their IDs share its copies.
			h.sharePage(recs)
			page := make(map[string]any, len(recs))
			ids := make([]string, 0, len(recs))
			for _, v := range recs {
				id := h.idOf(v)
				page[id] = v
				ids = append(ids, id)
			}
			s.mu.Lock()
			if conflicts := h.uniqueConflicts(page, nil); len(conflicts) > 0 {
				s.mu.Unlock()
				return conflicts[0]
			}
			if changed := h.putAll(ids, page); collect {
				changes = append(changes, changed...)
			}
			s.mu.Unlock()
		}
		read[i] = nil
		if len(changes) > 0 {
			s.mu.Lock()
			h.notify(withSubscriber(ctx), changes)
			s.mu.Unlock()
		}
	}
	s.loaded.Store(true)
	return nil
}

// readAllKinds reads every kind's pages, loadParallelism kinds at a time, and returns them in the
// order of s.order. The first read that fails cancels the others.
func (s *Store) readAllKinds(ctx context.Context, db *gorm.DB) ([][][]any, error) {
	read := make([][][]any, len(s.order))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(loadParallelism)
	for i, kind := range s.order {
		h := s.kinds[kind]
		g.Go(func() error {
			var pages [][]any
			after := ""
			for {
				recs, next, err := h.readPage(gctx, db, after, pageSize)
				if err != nil {
					return fmt.Errorf("livestore: load %s: %w", kind, err)
				}
				pages = append(pages, recs)
				if next == "" || len(recs) == 0 {
					break
				}
				after = next
			}
			read[i] = pages
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return read, nil
}

// LoadRecords is LoadAll for a store with no database: the records come from the caller, built
// with Rec. It runs once, like LoadAll. Afterwards every call that reads the database returns
// ErrNoDatabase; Remove refs still work. A record not built with Rec on one of this store's
// collections fails it with an error before anything is published.
func (s *Store) LoadRecords(ctx context.Context, recs []Record) error {
	if err := inSubscriber(ctx); err != nil {
		return err
	}
	if err := s.freeze(); err != nil {
		return err
	}
	p := newPlan()
	for _, r := range recs {
		if r.coll == nil {
			return errors.New("livestore: record not built with Rec")
		}
		kind := r.coll.kindName()
		h, ok := s.kinds[kind]
		if !ok {
			return fmt.Errorf("livestore: record of unknown kind %q", kind)
		}
		if h != r.coll {
			return fmt.Errorf("livestore: record %s/%s was built from a collection this store did not register", kind, r.id)
		}
		p.upsert(kind, r.id, r.value)
	}
	if conflicts := s.publish(ctx, p); len(conflicts) > 0 {
		return conflicts[0]
	}
	s.loaded.Store(true)
	return nil
}

// Refresh brings the named rows into memory from the database. A row that exists replaces its
// record, and a row that is gone removes it, unless RetryMissing is given. A read that fails
// publishes nothing and returns its error, unless RetryErrors or RetryErrorsWithin is given.
// Children declared with ChildOf follow their parents: a refreshed parent's children are re-read,
// and a removed parent's children are removed or re-read as its OnDelete says. A lock per record
// is held from read to publish, for the named rows and for every child a refresh re-reads, so of
// two refreshes that read one row the later read always publishes last. Subscribers are called
// before Refresh returns; a subscriber that calls Refresh with the context it was given gets
// ErrInSubscriber.
//
// A Remove ref drops its record without a read, with the Cascade children under it, in a publish
// of their own made before anything is read, so a delete takes effect even when the database is
// unreachable and no failed read can hold it back. The SetNull children of the records it dropped
// are re-read after, with the other rows and the same options; when that read fails, Refresh
// returns its error with the removal already published, and those children keep the reference
// until their next refresh.
//
// A unique index never holds two records under one key. When the rows read would give a key to a
// record while memory still gives it to another, the other record may be stale (a rename
// no refresh has brought in yet), so Refresh re-reads it too, and in turn any record whose key
// that re-read contests, until no new holder turns up, and publishes them all. If the database
// still gives two records one key, nothing is published and Refresh returns a
// UniqueConflictError.
func (s *Store) Refresh(ctx context.Context, refs []Ref, opts ...RefreshOption) error {
	if err := inSubscriber(ctx); err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}
	if !s.loaded.Load() {
		return ErrNotLoaded
	}
	var cfg refreshOptions
	for _, o := range opts {
		o(&cfg)
	}
	needsDB := false
	for _, r := range refs {
		if _, ok := s.kinds[r.Kind]; !ok {
			return fmt.Errorf("livestore: refresh of unknown kind %q", r.Kind)
		}
		if !r.Remove {
			needsDB = true
		}
	}
	if needsDB && s.db == nil {
		return ErrNoDatabase
	}
	if cfg.errWithin {
		// Fixed once, so the passes that follow contested unique keys share one limit.
		if deadline, ok := ctx.Deadline(); ok {
			cfg.errUntil = deadline
		} else {
			cfg.errUntil = time.Now().Add(cfg.errBudget)
		}
	}

	reads, holders := s.removeFirst(ctx, refs)
	if s.db == nil {
		holders = nil // a store loaded from records has no rows to re-read them from
	}
	if len(reads) == 0 && len(holders) == 0 {
		return nil
	}
	// Each pass that conflicts adds the records memory holds under the contested keys and runs
	// again, so a chain of renames is followed to its end. Holders only grow, so this ends: once a
	// pass finds no new holder, the database itself gives a key to two records.
	missing, conflicts, err := s.refreshRetrying(ctx, reads, holders, cfg)
	for err == nil && len(conflicts) > 0 {
		more := conflictHolders(conflicts, append(slices.Clone(reads), holders...))
		if len(more) == 0 || s.db == nil {
			return conflicts[0]
		}
		holders = append(holders, more...)
		missing, conflicts, err = s.refreshRetrying(ctx, reads, holders, cfg)
	}
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return &MissingError{Refs: missing}
	}
	return nil
}

// maxLockWidenings is how many times a refresh widens its record locks to children the database
// has and memory did not, before it takes every record lock instead.
const maxLockWidenings = 2

// removeFirst publishes, before anything is read, the removal of the records refs names with
// Remove, and of the Cascade children under them and theirs. It reads nothing, so the removal
// takes effect however the database is doing. It returns the refs left to read, and the SetNull
// children of the records it removed: their rows had the reference cleared, so Refresh re-reads
// them as holders.
func (s *Store) removeFirst(ctx context.Context, refs []Ref) (reads, setNull []Ref) {
	var removes []Ref
	for _, r := range refs {
		if r.Remove {
			removes = append(removes, Ref{Kind: r.Kind, ID: r.ID})
		} else {
			reads = append(reads, r)
		}
	}
	if len(removes) == 0 {
		return reads, nil
	}
	unlock := s.lockRecords(append(slices.Clone(removes), s.childClosure(removes)...))
	defer unlock()
	p := newPlan()
	seen := make(map[Ref]struct{}, len(removes))
	for queue := slices.Clone(removes); len(queue) > 0; {
		r := queue[0]
		queue = queue[1:]
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		p.remove(r.Kind, r.ID)
		for _, l := range s.children[r.Kind] {
			for _, id := range l.childrenOf(r.ID) {
				c := Ref{Kind: l.child.kindName(), ID: id}
				if l.onDelete == Cascade {
					queue = append(queue, c)
				} else {
					setNull = append(setNull, c)
				}
			}
		}
	}
	// A plan of removals only can leave no unique key with two records, so nothing is refused.
	s.publish(ctx, p)
	return reads, setNull
}

// refreshRetrying runs refresh and, with RetryErrors or RetryErrorsWithin, runs it again after an
// error, the wait apart and outside the record locks, until it succeeds or retrying stops. An
// error once ctx has ended is returned at once: its caller has stopped waiting.
func (s *Store) refreshRetrying(ctx context.Context, refs, holders []Ref, cfg refreshOptions) ([]Ref, []*UniqueConflictError, error) {
	for attempt := 0; ; attempt++ {
		missing, conflicts, err := s.refresh(ctx, refs, holders, cfg)
		if err == nil || !cfg.retryError(attempt) || ctx.Err() != nil {
			return missing, conflicts, err
		}
		if retryWait(ctx, cfg.errWait) != nil || !cfg.withinLimit() {
			return nil, nil, err
		}
	}
}

// retryWait is the wait between a failed read and its retry. Tests replace it to wake late, as a
// busy scheduler can.
var retryWait = sleep

// refresh is one attempt of Refresh: it reads refs and holders under their record locks,
// follows children and publishes, unless the result would break a unique index, in which case
// it publishes nothing and returns the conflicts. holders are records memory holds under a
// contested unique key, or SetNull children of records a Remove ref dropped; their rows are read
// once, and a holder whose row is gone is removed whatever the options say, since it was in
// memory and so was readable before.
//
// The locks cover the children memory lists under the records read, so a child's own refresh
// cannot publish between this refresh's read of the child and its publish. A child the database
// lists that memory did not is outside those locks: the attempt publishes nothing and runs again
// with that child locked too.
func (s *Store) refresh(ctx context.Context, refs, holders []Ref, cfg refreshOptions) ([]Ref, []*UniqueConflictError, error) {
	locked := append(slices.Clone(refs), holders...)
	locked = append(locked, s.childClosure(locked)...)
	for widenings := 0; ; widenings++ {
		missing, conflicts, outside, err := s.refreshLocked(ctx, refs, holders, locked, widenings >= maxLockWidenings, cfg)
		if err != nil || len(outside) == 0 {
			return missing, conflicts, err
		}
		locked = append(locked, outside...)
		locked = append(locked, s.childClosure(outside)...)
	}
}

// childClosure lists the children memory holds under refs, and theirs, following ChildOf links.
func (s *Store) childClosure(refs []Ref) []Ref {
	var out []Ref
	seen := make(map[Ref]struct{}, len(refs))
	queue := slices.Clone(refs)
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		for _, l := range s.children[r.Kind] {
			for _, id := range l.childrenOf(r.ID) {
				c := Ref{Kind: l.child.kindName(), ID: id}
				if _, ok := seen[c]; ok {
					continue
				}
				seen[c] = struct{}{}
				out = append(out, c)
				queue = append(queue, c)
			}
		}
	}
	return out
}

// refreshLocked is one pass of refresh under the record locks of locked, or of every record when
// all is set. When the plan touches a record outside the locks it publishes nothing and returns
// those records, for the caller to lock and run again.
func (s *Store) refreshLocked(ctx context.Context, refs, holders, locked []Ref, all bool, cfg refreshOptions) ([]Ref, []*UniqueConflictError, []Ref, error) {
	var unlock func()
	if all {
		unlock = s.lockAllRecords()
	} else {
		unlock = s.lockRecords(locked)
	}
	defer unlock()

	p := newPlan()
	reads := make(map[Kind][]string)
	for _, r := range refs {
		if r.Remove {
			p.remove(r.Kind, r.ID)
			continue
		}
		reads[r.Kind] = append(reads[r.Kind], r.ID)
	}
	var missing []Ref
	for _, kind := range sortedKinds(reads) {
		h := s.kinds[kind]
		ids := dedupe(reads[kind])
		found, err := h.loadByID(ctx, s.db, ids)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("livestore: refresh %s: %w", kind, err)
		}
		lost := absent(ids, found)
		for attempt := 0; attempt < cfg.retries && len(lost) > 0; attempt++ {
			if err := sleep(ctx, cfg.wait); err != nil {
				return nil, nil, nil, err
			}
			more, err := h.loadByID(ctx, s.db, lost)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("livestore: refresh %s: %w", kind, err)
			}
			for id, v := range more {
				found[id] = v
			}
			lost = absent(lost, more)
		}
		for id, v := range found {
			p.upsert(kind, id, v)
		}
		for _, id := range lost {
			if cfg.keepMissing {
				missing = append(missing, Ref{Kind: kind, ID: id})
				continue
			}
			p.remove(kind, id)
		}
	}
	held := make(map[Kind][]string)
	for _, r := range holders {
		held[r.Kind] = append(held[r.Kind], r.ID)
	}
	for _, kind := range sortedKinds(held) {
		ids := dedupe(held[kind])
		found, err := s.kinds[kind].loadByID(ctx, s.db, ids)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("livestore: refresh %s: %w", kind, err)
		}
		for _, id := range ids {
			if v, ok := found[id]; ok {
				p.upsert(kind, id, v)
			} else {
				p.remove(kind, id)
			}
		}
	}
	if err := s.expandChildren(ctx, p); err != nil {
		return nil, nil, nil, err
	}
	if !all {
		if outside := p.outside(locked); len(outside) > 0 {
			return nil, nil, outside, nil
		}
	}
	if conflicts := s.publish(ctx, p); len(conflicts) > 0 {
		return nil, conflicts, nil, nil
	}
	return missing, nil, nil, nil
}

// conflictHolders returns the records named in conflicts that refs did not name: the ones memory
// holds under a contested key, which a retry re-reads.
func conflictHolders(conflicts []*UniqueConflictError, refs []Ref) []Ref {
	named := make(map[Ref]struct{}, len(refs))
	for _, r := range refs {
		named[Ref{Kind: r.Kind, ID: r.ID}] = struct{}{}
	}
	var out []Ref
	for _, c := range conflicts {
		for _, id := range c.IDs {
			r := Ref{Kind: c.Kind, ID: id}
			if _, ok := named[r]; !ok {
				named[r] = struct{}{}
				out = append(out, r)
			}
		}
	}
	return out
}

// RefreshKind re-reads every row of kind and publishes the difference: new and changed rows replace
// their records, and records whose rows are gone are removed. It lists and publishes under every
// record lock, so its listing is never older than a record memory holds and a record it leaves out
// is removed without a re-read; every other refresh waits meanwhile. It is meant for small kinds
// whose change messages name no row. Children are not followed. If the rows would put two records
// under one key of a unique index, nothing is published and it returns a UniqueConflictError.
func (s *Store) RefreshKind(ctx context.Context, kind Kind) error {
	if err := inSubscriber(ctx); err != nil {
		return err
	}
	if !s.loaded.Load() {
		return ErrNotLoaded
	}
	if s.db == nil {
		return ErrNoDatabase
	}
	h, ok := s.kinds[kind]
	if !ok {
		return fmt.Errorf("livestore: refresh of unknown kind %q", kind)
	}
	unlock := s.lockAllRecords()
	defer unlock()

	p := newPlan()
	seen := make(map[string]struct{})
	after := ""
	for {
		recs, next, err := h.loadPage(ctx, s.db, after, pageSize)
		if err != nil {
			return fmt.Errorf("livestore: refresh %s: %w", kind, err)
		}
		for _, v := range recs {
			id := h.idOf(v)
			seen[id] = struct{}{}
			p.upsert(kind, id, v)
		}
		if next == "" || len(recs) == 0 {
			break
		}
		after = next
	}
	for _, id := range h.ids() {
		if _, ok := seen[id]; !ok {
			p.remove(kind, id)
		}
	}
	if conflicts := s.publish(ctx, p); len(conflicts) > 0 {
		return conflicts[0]
	}
	return nil
}

// expandChildren adds to p the children of every parent p touches, following ChildOf links
// level by level until no new parent appears. A held child a refreshed parent no longer lists is
// read by ID and kept, with its state, when its row still names a parent through the link (it
// moved to another parent) or the link is SetNull, whose children outlive their parent. Otherwise
// it is removed: its row is gone, or a Cascade child was left with no parent. A child a parent's
// read returns on a Cascade link whose record names no parent through any link (its row changed
// between the read of its ID and the read of its record) is not published, and is removed when
// held: nothing would ever drop it, since no parent lists it.
func (s *Store) expandChildren(ctx context.Context, p *plan) error {
	done := make(map[Kind]map[string]struct{})
	queue := p.kinds()
	for len(queue) > 0 {
		kind := queue[0]
		queue = queue[1:]
		links := s.children[kind]
		if len(links) == 0 {
			continue
		}
		seen := done[kind]
		if seen == nil {
			seen = make(map[string]struct{})
			done[kind] = seen
		}
		var present, removed []string
		for id := range p.upserts[kind] {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				present = append(present, id)
			}
		}
		for id := range p.removes[kind] {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				removed = append(removed, id)
			}
		}
		slices.Sort(present)
		slices.Sort(removed)
		for _, l := range links {
			childKind := l.child.kindName()
			touched := false
			if len(present) > 0 && s.db != nil {
				found, err := l.byParent(ctx, s.db, present)
				if err != nil {
					return fmt.Errorf("livestore: children of %s: %w", kind, err)
				}
				for id, v := range found {
					if l.onDelete == Cascade && !l.namesAParent(v) {
						if _, held := l.child.heldID(id); held {
							p.remove(childKind, id)
							touched = true
						}
						continue
					}
					p.upsert(childKind, id, v)
					touched = true
				}
				// A held child the query no longer returns may have moved to another parent
				// rather than been deleted, so it is read by ID: a row that names a parent through
				// the link is kept under it, and a SetNull child is kept with the reference cleared.
				// A row that is gone, or a Cascade child left with no parent, removes its record.
				// These children are under the parent's record locks already.
				var left []string
				for _, pid := range present {
					for _, cid := range l.childrenOf(pid) {
						if _, ok := found[cid]; !ok {
							left = append(left, cid)
						}
					}
				}
				if len(left) > 0 {
					left = dedupe(left)
					still, err := l.child.loadByID(ctx, s.db, left)
					if err != nil {
						return fmt.Errorf("livestore: children of %s: %w", kind, err)
					}
					for _, id := range left {
						if v, ok := still[id]; ok && (l.onDelete == SetNull || l.hasParent(v)) {
							p.upsert(childKind, id, v)
						} else {
							p.remove(childKind, id)
						}
						touched = true
					}
				}
			}
			if len(removed) > 0 {
				var ids []string
				for _, pid := range removed {
					ids = append(ids, l.childrenOf(pid)...)
				}
				switch l.onDelete {
				case Cascade:
					for _, id := range ids {
						p.remove(childKind, id)
						touched = true
					}
				case SetNull:
					if s.db != nil && len(ids) > 0 {
						found, err := l.child.loadByID(ctx, s.db, ids)
						if err != nil {
							return fmt.Errorf("livestore: children of %s: %w", kind, err)
						}
						for _, id := range ids {
							if v, ok := found[id]; ok {
								p.upsert(childKind, id, v)
							} else {
								p.remove(childKind, id)
							}
							touched = true
						}
					}
				}
			}
			if touched {
				queue = append(queue, childKind)
			}
		}
	}
	return nil
}

// publish applies p under the store lock, kind by kind: the upserts in dependency order, calling
// each kind's subscribers once its records are in, then the removals in reverse order, calling
// each kind's subscribers once its records are gone. A reader therefore finds a referenced record,
// and what its subscribers derive from it, before any record that references it, and a removed
// record stays until nothing published references it. If p would leave two records under one key
// of a unique index, it applies nothing and returns the conflicts.
func (s *Store) publish(ctx context.Context, p *plan) []*UniqueConflictError {
	s.mu.Lock()
	defer s.mu.Unlock()
	var conflicts []*UniqueConflictError
	for _, kind := range s.order {
		if ups := p.upserts[kind]; len(ups) > 0 {
			conflicts = append(conflicts, s.kinds[kind].uniqueConflicts(ups, p.removes[kind])...)
		}
	}
	if len(conflicts) > 0 {
		return conflicts
	}
	sub := withSubscriber(ctx)
	for _, kind := range s.order {
		if c := s.applyUpserts(p, kind); len(c) > 0 {
			s.kinds[kind].notify(sub, c)
		}
	}
	for i := len(s.order) - 1; i >= 0; i-- {
		if c := s.applyRemovals(p, s.order[i]); len(c) > 0 {
			s.kinds[s.order[i]].notify(sub, c)
		}
	}
	return nil
}

// applyUpserts writes kind's upserts in p into its collection, in ID order, and returns what
// changed. Every record's new index keys are added before any old key is dropped (putAll). The
// caller holds s.mu.
func (s *Store) applyUpserts(p *plan, kind Kind) []anyChange {
	ups := p.upserts[kind]
	if len(ups) == 0 {
		return nil
	}
	s.tablesMu.RLock()
	defer s.tablesMu.RUnlock()
	h := s.kinds[kind]
	ids := make([]string, 0, len(ups))
	for id := range ups {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return h.putAll(ids, ups)
}

// applyRemovals drops kind's removals in p from its collection, in ID order, clearing each
// removed record's state, and returns what changed. The caller holds s.mu.
func (s *Store) applyRemovals(p *plan, kind Kind) []anyChange {
	rems := p.removes[kind]
	if len(rems) == 0 {
		return nil
	}
	s.tablesMu.RLock()
	defer s.tablesMu.RUnlock()
	h := s.kinds[kind]
	ids := make([]string, 0, len(rems))
	for id := range rems {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var changes []anyChange
	for _, id := range ids {
		old := h.remove(id)
		if old == nil {
			continue
		}
		for _, t := range s.states[kind] {
			t.deleteKey(id)
		}
		changes = append(changes, anyChange{id: id, old: old})
	}
	return changes
}

// lockRecords takes the record locks for refs, in stripe order so two callers never deadlock,
// and returns the function that releases them.
func (s *Store) lockRecords(refs []Ref) func() {
	stripes := make([]int, 0, len(refs))
	for _, r := range refs {
		stripes = append(stripes, recordStripe(r.Kind, r.ID))
	}
	slices.Sort(stripes)
	stripes = slices.Compact(stripes)
	for _, i := range stripes {
		s.recordLocks[i].Lock()
	}
	return func() {
		for j := len(stripes) - 1; j >= 0; j-- {
			s.recordLocks[stripes[j]].Unlock()
		}
	}
}

// lockAllRecords takes every record lock, for a refresh of a whole kind.
func (s *Store) lockAllRecords() func() {
	for i := range s.recordLocks {
		s.recordLocks[i].Lock()
	}
	return func() {
		for i := len(s.recordLocks) - 1; i >= 0; i-- {
			s.recordLocks[i].Unlock()
		}
	}
}

// recordStripe picks the lock stripe for one record.
func recordStripe(kind Kind, id string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(kind))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(id))
	return int(h.Sum32() % recordLockStripes)
}

// DropTable removes a table or state table and its entries, so a plugin that is removed or
// reloaded can register the same name again. Dropping a name that is not registered does
// nothing. A subscriber that calls it with the context it was given gets ErrInSubscriber, and a
// name starting with internal: is refused: the store owns those tables.
func (s *Store) DropTable(ctx context.Context, name string) error {
	if err := inSubscriber(ctx); err != nil {
		return err
	}
	if err := publicTableName(name); err != nil {
		return err
	}
	s.dropTable(name)
	return nil
}

// publicTableName refuses a name a component may not use: one in the store's internal: range.
func publicTableName(name string) error {
	if strings.HasPrefix(name, internalPrefix) {
		return fmt.Errorf("livestore: table name %q: the %s prefix is reserved for the store's own tables", name, internalPrefix)
	}
	return nil
}

// dropTable removes the table registered under name, if any, with its entries and its place in
// the state lists. It is DropTable without the checks, for the store's own tables too.
func (s *Store) dropTable(name string) {
	s.tablesMu.Lock()
	defer s.tablesMu.Unlock()
	t, ok := s.tables[name]
	if !ok {
		return
	}
	delete(s.tables, name)
	for kind, list := range s.states {
		s.states[kind] = slices.DeleteFunc(list, func(x tableHandle) bool { return x == t })
	}
	t.clear()
}

// addTable registers a table under its name, as state of kind of when of is set, and starts
// the background sweep. It fails when the name is empty or taken, or the kind is unknown. The
// kind registry is read without mu once the store is loaded, since it no longer changes then.
func (s *Store) addTable(name string, t tableHandle, of Kind) error {
	if name == "" {
		return errors.New("livestore: empty table name")
	}
	if of != "" && !s.kindKnown(of) {
		return fmt.Errorf("livestore: table %q is keyed by unknown kind %q", name, of)
	}
	s.tablesMu.Lock()
	defer s.tablesMu.Unlock()
	if _, ok := s.tables[name]; ok {
		return fmt.Errorf("livestore: table %q registered twice", name)
	}
	if of != "" {
		s.states[of] = append(s.states[of], t)
	}
	s.tables[name] = t
	s.sweepOnce.Do(func() { go s.sweepLoop() })
	return nil
}

// kindKnown reports whether a collection of kind is registered. Before the store loads it takes
// mu, since registration may still be running; after, the registry is fixed and needs no lock.
func (s *Store) kindKnown(kind Kind) bool {
	if !s.frozen.Load() {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	_, ok := s.kinds[kind]
	return ok
}

// sweepLoop runs sweep every sweepInterval until Close.
func (s *Store) sweepLoop() {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.sweep()
		}
	}
}

// sweep removes expired table entries and interned values no record has used for the grace
// period. It runs in the background so no publish pays for a scan. Interned values are shared and
// counted as records publish, so a value no record uses was used before and has been released.
func (s *Store) sweep() {
	s.tablesMu.RLock()
	tables := make([]tableHandle, 0, len(s.tables))
	for _, t := range s.tables {
		tables = append(tables, t)
	}
	s.tablesMu.RUnlock()
	at := now()
	for _, t := range tables {
		t.sweep(at.UnixNano())
	}
	s.mu.Lock()
	interned := make([]internedHandle, 0, len(s.interned))
	for _, in := range s.interned {
		interned = append(interned, in)
	}
	s.mu.Unlock()
	cutoff := at.Add(-internedGrace).UnixNano()
	for _, in := range interned {
		in.sweep(cutoff)
	}
}

// plan is the set of records one refresh will write: upserts and removals per kind. An upsert
// wins over a removal of the same record.
type plan struct {
	upserts map[Kind]map[string]any
	removes map[Kind]map[string]struct{}
}

// newPlan returns an empty plan.
func newPlan() *plan {
	return &plan{upserts: make(map[Kind]map[string]any), removes: make(map[Kind]map[string]struct{})}
}

// upsert schedules v as the record for id, cancelling a scheduled removal.
func (p *plan) upsert(kind Kind, id string, v any) {
	m := p.upserts[kind]
	if m == nil {
		m = make(map[string]any)
		p.upserts[kind] = m
	}
	m[id] = v
	delete(p.removes[kind], id)
}

// remove schedules the record for id to be dropped, unless it is already being upserted.
func (p *plan) remove(kind Kind, id string) {
	if _, ok := p.upserts[kind][id]; ok {
		return
	}
	m := p.removes[kind]
	if m == nil {
		m = make(map[string]struct{})
		p.removes[kind] = m
	}
	m[id] = struct{}{}
}

// outside lists the records p touches whose lock stripe none of locked holds.
func (p *plan) outside(locked []Ref) []Ref {
	var held [recordLockStripes]bool
	for _, r := range locked {
		held[recordStripe(r.Kind, r.ID)] = true
	}
	var out []Ref
	for kind, ups := range p.upserts {
		for id := range ups {
			if !held[recordStripe(kind, id)] {
				out = append(out, Ref{Kind: kind, ID: id})
			}
		}
	}
	for kind, rems := range p.removes {
		for id := range rems {
			if !held[recordStripe(kind, id)] {
				out = append(out, Ref{Kind: kind, ID: id})
			}
		}
	}
	return out
}

// kinds lists every kind the plan touches, in name order.
func (p *plan) kinds() []Kind {
	set := make(map[Kind]struct{})
	for k := range p.upserts {
		set[k] = struct{}{}
	}
	for k := range p.removes {
		set[k] = struct{}{}
	}
	out := make([]Kind, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// anyChange is one record's change before it is typed for a collection's subscribers.
type anyChange struct {
	id       string
	old, new any
}

// kindHandle is what the store needs from a collection without knowing its record type.
type kindHandle interface {
	kindName() Kind
	dependencies() []Kind
	loadPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]any, string, error)
	readPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]any, string, error)
	sharePage(recs []any)
	loadByID(ctx context.Context, db *gorm.DB, ids []string) (map[string]any, error)
	idOf(v any) string
	heldID(id string) (string, bool)
	putAll(ids []string, ups map[string]any) []anyChange
	remove(id string) (old any)
	uniqueConflicts(ups map[string]any, removes map[string]struct{}) []*UniqueConflictError
	ids() []string
	hasSubscribers() bool
	notify(ctx context.Context, changes []anyChange)
}

// childHandle is one ChildOf link, seen from its parent kind.
type childHandle struct {
	parent       Kind
	child        kindHandle
	onDelete     OnDelete
	byParent     func(ctx context.Context, db *gorm.DB, parentIDs []string) (map[string]any, error)
	childrenOf   func(parentID string) []string
	hasParent    func(rec any) bool // whether a child record names a parent through this link
	namesAParent func(rec any) bool // namesAParent reports whether a child record names a parent through any of its kind's links.
}

// internedHandle is what the store needs from an interned set.
type internedHandle interface {
	sweep(cutoff int64)
}

// tableHandle is what the store needs from a table without knowing its key and value types.
type tableHandle interface {
	sweep(at int64)
	clear()
	deleteKey(id string)
}

// sharedID returns the copy of id that the record of kind with that ID holds, or id when the store
// holds none. Loaders run once registration is over, so it reads the kinds map without the store
// lock, as the load paths do.
func (s *Store) sharedID(kind Kind, id string) string {
	if h, ok := s.kinds[kind]; ok {
		if held, ok := h.heldID(id); ok {
			return held
		}
	}
	return id
}

// subscriberKey marks a context passed to subscribers.
type subscriberKey struct{}

// withSubscriber marks ctx as a subscriber's context.
func withSubscriber(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, subscriberKey{}, true)
}

// inSubscriber returns ErrInSubscriber when ctx is a subscriber's context: a subscriber runs
// inside a publish and rebuilds derived data only, so it may not write records, load, or create
// or drop tables.
func inSubscriber(ctx context.Context) error {
	if ctx != nil && ctx.Value(subscriberKey{}) != nil {
		return ErrInSubscriber
	}
	return nil
}

// sortedKinds returns the keys of m in name order.
func sortedKinds[V any](m map[Kind]V) []Kind {
	out := make([]Kind, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// dedupe returns ids sorted with repeats removed.
func dedupe(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// absent returns the ids that found has no entry for.
func absent(ids []string, found map[string]any) []string {
	var out []string
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// sleep waits d and returns the context's error if it ended before or during the wait, so no
// retry starts with an ended context even when the timer and the end are ready together.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
	return ctx.Err()
}
