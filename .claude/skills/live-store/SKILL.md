---
name: live-store
description: Use when adding or changing anything a Bifrost process keeps in memory (a config-database kind, a cache, runtime state, a derived view) or any write path, reload or handler that changes configuration. Walks through choosing the structure, registering it, the three write rules, request-path reads, boot loading and the tests each needs, and lists existing code that does not follow the rules yet and must not be copied.
---

# Live store

Everything a Bifrost process keeps in memory lives in `framework/livestore`: configuration loaded from the config store, runtime state keyed by those records, caches, and tables a component writes. Components keep only handles (connections, goroutines, locks, compiled programs) and per-request buffers. This skill turns the AGENTS.md rules into steps; read those first:

- AGENTS.md "Data lives in the live store" and "Adding a config.json section"
- AGENTS.md "Writes reach memory the same way for every entity"
- AGENTS.md "Every change states its cost on the request flow" and the audit rule ("Audit changes")

Cite names, not line numbers: the examples below are function and file names on `dev`.

## 1. Pick the structure by what a miss means

| Structure | Holds | A miss means | Create with |
|---|---|---|---|
| Collection | every row of one config-database kind, loaded at boot, refreshed after every write | the row does not exist | a `Register` call in `registerCollections` (`framework/livestore/registry.go`) |
| Cache | only what was asked for, fetched by a loader | go fetch | `live.NewCache` |
| Table | what this process writes itself, no loader | nothing was written yet | `live.NewTable` (`KeyedBy(kind)` for state per entity) |
| View | state derived from collections that several packages read | the derived value is empty (a provider with no keys has no `ProviderKeyLists` entry, so `Get` returns `nil, false`) | a table in `Store.Views`, rebuilt by a subscriber |

Where each lives:
- `Store.Collections` holds config-store collections only. No convenience methods on it that merge records or hide state.
- `Store.Views` holds global derived tables, created in `livestore.New` and rebuilt by a collection subscriber once per publish (`ProviderKeyLists`, rebuilt by `rebuildProviderKeyLists`).
- A table only one component uses stays with that component (`WSTicketStore` in `handlers/ws_ticket.go`; the routing rules `Store` in `plugins/routing/rules/store.go`, which keeps compiled programs in a state table marked `HoldsHandles`).
- **In-flight working state may stay in a map**: state for one request, stream or run, keyed by its request, stream or job ID and removed when that ends (stream accumulators, pending log entries, traces being built, running jobs). It still needs an `inflight` entry in `tests/rawmaps/allowlist.txt` with its reason, and every path that ends the request, stream or run, errors and cancellation included, must remove it. Anything that outlives the request (a cache, configuration, state the next request reuses) is not in-flight and belongs in the live store.
- `TestRepositoryKeepsDataInTheLiveStore` (section 8) fails on the shapes AGENTS.md lists: a `sync.Map` field or package-level `sync.Map`, a map on a struct with a mutex, maps behind an atomic pointer, and a package-level map a function writes. It does not see every raw map (an unguarded map field, for one), so a passing lint does not prove a component keeps none.

## 2. Register a collection

Copy the Prompts and PromptVersions registration in `registerCollections`: it shows a loader, a `Cascade` child, a unique index and a multi index on one kind.

1. **Record type and loader** in `framework/livestore/registry/<kind>.go`: a record type plus `Loader{ID, Page, ByID}` (see `ProviderName`, `ProvidersPage`, `ProvidersByName`). `Page` pages by primary key; `ByID` binds the IDs as one argument (`queryscope.InStrings`).
2. **Records hold only what their loader fills.** Use a narrow record type rather than reusing a wide one with fields nobody sets. Settings and keys are separate values: the `Provider` record has no keys, keys are their own collection, and a function that needs keys takes them as a parameter.
3. **Kind constant** next to the registration, and the collection's field on `Collections`.
4. **Dependencies** (the kind list `Register` takes after the kind) set the publish order for `LoadAll` and `Refresh`: list the kinds a record refers to.
5. **Children:** `ChildOf(parent, field, reader, Cascade)` for owned children, removed with the parent in the same publish (provider keys under a provider); `SetNull` for holders that survive the parent and are re-read when it goes (teams under a customer). Use `ChildIDsOf` when the reader works in batches.
6. **Indexes:** `UniqueIndex` only where the database also keeps the key unique; `MultiIndex` for lookups by a non-unique field.
7. **Order** is a record field (`ProviderKey.Order`) that a view sorts by; there is no ordered-link option.

## 3. Caches and tables

- **Cache:** `live.NewCache` with `WithCacheTTL`, and `WithSharedFills` when a load's answer does not depend on when it started (callers arriving during a load share it). Example: `rssfeed.NewManager`; the OAuth2 signing-key cache in `lib/livestore.go`.
- **Table:** `live.NewTable[K, V](ctx, name, opts...)`, with `WithTTL` for expiring entries (`NewWSTicketStore`).
- **State keyed by a record:** `live.NewTable[string, *V](ctx, name, livestore.KeyedBy(kind))`, with any other option listed after it (`livestore.KeyedBy(kind), livestore.WithTTL(ttl)`): an entry is dropped when its record is removed (governance usage per budget and rate limit in `plugins/governance/store.go`).
- **Memo of derived values:** a table with `WithGeneration`, invalidated by bumping the generation (governance `permitViews`).

## 4. Write paths

### Delete
1. Read the holders (records that refer to the entity) **inside the delete's transaction**, and refuse the delete if that read fails.
2. After the commit, remove the record with a `Remove` ref (`livestore.Ref{Kind, ID, Remove: true}`), which reads nothing for the record itself; its `Cascade` children go in the same publish. Its `SetNull` children are re-read after the removal publishes, so the `Refresh` can still return that read's error, with the removal already published.
3. Evict the caches bound to it.
4. Re-read the holders with `lib.AdminRetry()`; if that still fails, answer 500: "<thing> deleted in database but failed to reload in-memory state".

The read-free removal is `Config.RemoveProviderKey` (`lib/config.go`); the retried holder re-read is `refreshRows` (`server/server.go`), reached through `reloadGrantingVirtualKeys` (`handlers/utils.go`). Record the audit event right after the commit, before the holder re-read that can still answer 500.

### Create or update
Write, then re-read the written rows with `lib.AdminRetry()` (bounded by the request deadline, or a fixed budget when there is none). On failure memory keeps the previous record and the handler answers 500 ("saved but not reloaded").
- Helpers: `Config.refreshRef` (`lib/config.go`), `refreshRow` / `refreshRows` (`server/server.go`, `AdminRetry` by default).
- Errors: `lib.SavedNotAppliedError` / `lib.IsSavedNotApplied`, `handlers.ErrSavedButNotReloaded`.
- Example: `Config.AddProviderKey` / `UpdateProviderKey`, and the `updateProviderKey` handler.

### Never write configuration from memory
- A partial update merges onto a **database read**, not the live record (`updateProviderKey`, `updateProvider`).
- A settings save never rewrites child rows (`RDBConfigStore.UpdateProvider` writes settings columns only).
- State the process owns (usage, last reset, reset epochs, discovery status) is exempt.

### Also
- **Only an explicit delete removes a record without reading it.** A reload or reconcile that lists rows itself and finds a held record missing from that list re-reads it with a plain ref; the list may be older than the record. `RefreshKind` does the listing under every record lock, so its removals stand: use it for a small kind whose change message names no row, and `Refresh` with plain refs when you know which records changed.
- **A change another process made** is applied with `lib.DBLookupRetry()` (waits for a row not readable yet, never removes a missing one): `AgentGatewayHandler.ReloadRegistration`.
- **Audit:** record the change right after the write commits, before the reload that can still fail (AGENTS.md audit rule), and refine it after.
- **Survey every caller** before shaping a new method or field, and change an existing method rather than adding a parallel one.

## 5. Request-path reads

- Read through the typed handle (`Get`, `GetBy`, `List`, a view's `Get`); never look a collection up by name.
- Shared values are read-only: do not modify a slice or record you got from the store; copy when a caller needs to change it (`GetProviderKeys` copies, `BaseAccount.GetKeysForProvider` filters into a new slice).
- Build derived data that is the same for every request once per publish in a subscriber (`Subscribe`: fast, no I/O, cannot fail), not per request. Work that depends on the request, such as filtering for one caller, stays on the request path and reads the shared data without modifying it.
- Request-path changes ship a benchmark with `-benchmem` and before/after numbers (AGENTS.md cost rule). `TestProviderKeyListsHoldEachProvidersKeysInCreationOrder` pins a zero-allocation read.

## 6. Boot and extension points

- config.json content follows AGENTS.md "Adding a config.json section": it waits in `bootConfig` (`lib/livestore.go`) until `loadLiveStore`, loaders parse and sync and never write the store, file-only mode goes through `bootConfig.records`, and `TestConfigJSONContentPerMode` pins each mode.
- A build that adds its own kinds registers them on the store `livestore.New` returns, before `LoadAll` or `LoadRecords` runs, may pass `StoreOption`s such as `LimitOwners`, hands the store over with `SetLiveStore`, and prepares its own content in a `RegisterLiveStoreLoadHook` hook. State these contracts generically in OSS code and docs.

## 7. Primitives

What `framework/livestore` offers, by type: when to use each and the contract its godoc on `dev` states. Read the godoc before relying on an edge case. `NumericID`, `ContentHasher` and `SealGrantSet` are in `framework/livestore/registry`; `AdminRetry` and `DBLookupRetry` in `transports/bifrost-http/lib`.

### Store and refresh

- `New(opts ...StoreOption)`: a store with every collection and `Views` registered and nothing loaded; a build registers its own kinds on it before loading. `LimitOwners` is the option for a build's own holders of budgets and rate limits. Tests use `livestoretest.New(t)`, closed when the test ends.
- `Store.Register[T](kind, dependsOn, Loader[T]{ID, Page, ByID}, opts...)`: the typed collection handle. `dependsOn` lists the kinds its records reference, which `LoadAll` and `Refresh` write first. `Page` reads in primary-key order after the cursor and returns the next one ("" at the end); `ByID` leaves out a row that is gone. Fails with `ErrLoaded` after a load, and on a taken kind, a loader missing a function or an invalid option (`registerCollections`).
- `LoadAll(ctx, db)`: fixes the registry, reads every kind concurrently and publishes in dependency order, once. A failed read or a duplicate unique key fails it before anything is published (`loadLiveStore`).
- `LoadRecords(ctx, recs)` with `Rec(c, v)`: `LoadAll` for a process with no database. Each record must be built with `Rec` on this store's collections; afterwards every database read returns `ErrNoDatabase`, while `Remove` refs still work (`Collections.FileRecords`).
- `Refresh(ctx, refs, opts...)` with `Ref{Kind, ID, Remove}`: re-reads the named rows. A row that is gone removes its record unless `RetryMissing` is given; a failed read publishes nothing; a refreshed parent re-reads its children; a lock per record makes the later of two reads of one row publish last. `Remove` refs publish first, with their `Cascade` children; their `SetNull` children are re-read after, and when that read fails `Refresh` returns its error with the removal already published. When the rows read give a unique key to a record while memory still gives it to another, that other record is re-read too (`Config.refreshRef`, `refreshRows`).
- `RefreshKind(ctx, kind)`: re-reads every row of a small kind whose change messages name no row, and removes records whose rows are gone. Children are not followed, and every record lock is held (`AgentGatewayHandler.ResyncFromStore`, `ModelCatalog.ForceReloadPricing`).
- `RefreshOption`: `RetryErrors(n, wait)` reads again after a failed read up to n times, for a caller that cannot report the failure back; `RetryErrorsWithin(budget, wait)` reads again until the context's deadline, or `budget` when it has none, for a caller answering for its own write (of the two, the one given last applies); `RetryMissing(n, wait)` re-reads a missing row up to n times, then leaves its record and reports it in a `MissingError`. None retries once the context has ended.
- `lib.AdminRetry()` is `RetryErrorsWithin(DBLookupMaxRetries*DBLookupDelay, 2*DBLookupDelay)` alone: a row the writer cannot find was deleted since, so its record is removed. `lib.DBLookupRetry()` is `RetryMissing` and `RetryErrors`, each `DBLookupMaxRetries` times `2*DBLookupDelay` apart, so a missing row is never removed. Spread them: `Refresh(ctx, refs, lib.AdminRetry()...)`.
- `DropTable(ctx, name)`: removes a table or state table with its entries, so a plugin that is removed or reloaded can register the name again. An unknown name does nothing; `internal:` names are refused (`LocalGovernanceStore.dropTables`).
- `Close()` stops the background sweep (`Config.Close`). `DB()` is the database `LoadAll` read, nil before it and on a store loaded from records: a caller that must read rows the way a refresh will reads them there.

### Collection options

- `ChildOf(parent, parentID, byParent, onDelete)`: section 2 says when. It indexes the children under `ParentIndex(parent)` and does not order kinds (`dependsOn` does). A held child `byParent` no longer returns is read by ID: dropped when its row is gone or, on a `Cascade` link, names no parent; kept otherwise, as when it moved to another parent. `ChildIDsOf` is the same link for a collection read in batches: it returns only child IDs, and the store reads the records through `ByID`.
- `OnDelete`: `Cascade` removes the children with the parent; `SetNull` keeps them and re-reads them, since their reference was cleared.
- `UniqueIndex(name, key)`: at most one record per key; an empty key is not indexed. A write that would give a key to a second record is refused with a `UniqueConflictError`; a key one publish moves between records passes in one step.
- `MultiIndex(name, keys)`: a key names several records and a record several keys; empty keys are dropped. `GetBy` returns the lowest ID (SQL's `First`), `List` every record in ID order (`VirtualKeyByCreator`).
- `ParentIndex(parent)`: the index `ChildOf` adds; `List(ParentIndex(parent), id)` lists one parent's children (`rebuildProviderKeyLists`).
- `ReadInBatches(size)`: every read by ID hands `ByID` at most `size` IDs and `Page` is asked for at most `size` rows, for a loader that binds a parameter per record (a preload does). Use `DefaultReadBatch`; the collection's links must be `ChildIDsOf`, since `Register` refuses `ChildOf` (virtual keys, routing rules).
- `WithNegativeTTL(ttl)`: `Load` remembers a miss for `ttl` in the store's own table `internal:negative_<kind>`, so an ID with no row is read at most once per `ttl`.
- `Interns(pool, part)`: `part` returns the address of a field of the record that is interned in `pool` (`func(k *VirtualKey) **GrantSet { return &k.Grants }`). The loader fills the field with the record's own copy; as the record publishes, the collection puts the pool's shared copy in it and counts the use. Readers use the field as it is, with no lookup. One `Interns` per interned part; a nil field holds nothing (`VirtualKeys` on `GrantSets`). The same `Interns` value works on a table, state table or cache (see Table).
- `ShareIDs(share)`: before a record publishes, `share` swaps each ID it names for the copy the named record holds (`shared(kind, id)`, or the ID itself when none is held), so an ID is one string in memory. Kinds load in dependency order, so list the named kinds in `dependsOn`.

### Collection methods

- `Get(id)`, `GetBy(index, key)`, `List(index, key)`: the request-path reads (section 5); an unknown index finds nothing. A published record is never changed: a write swaps the pointer, so a reader may keep a record for the request.
- `Range(fn)`: every record until `fn` returns false, for boot, admin and background jobs, never the request path (`LocalGovernanceStore.GetGovernanceData`).
- `Load(ctx, id)`: `Get` that reads the database on a miss and publishes the row with its children, as `Refresh` does. It is the safety net for a row written before a refresh brought it in. A caller never gets a read that started before its call, and a burst costs at most two reads.
- `ForgetNegative(id)`: drops the miss `WithNegativeTTL` remembers, so the next `Load` reads again; a no-op without it. `Load` checks `Get` first, so a record a refresh brought in is found whatever the miss says.
- `Subscribe(fn) (cancel)` with `Change[T]{ID, Old, New}` (`Old` nil for a new record, `New` nil for a removed one): called after each publish that changed the kind, with only the records that changed. Upserts publish in dependency order and removals in reverse, and a kind with both is called twice, its new and changed records first; calls run one at a time before `Refresh` returns, and `LoadAll` hands subscribers registered before it every record as created. A store-changing call made with the subscriber's context (`Refresh`, `RefreshKind`, `Load`, `NewTable`, `NewCache`, `DropTable`) returns `ErrInSubscriber`. Table writes never notify (`LocalGovernanceStore.follow`).

### Interning

Intern a part of a record when many records hold equal copies of it (a grant set, a discovery): the pool keeps one copy, and every record points at it.

1. **Make the part a pointer field.** Only a pointer can be shared: `Grants *GrantSet`, not `Grants GrantSet`; to intern a slice or map, wrap it in a named type and hold a pointer to it.
2. **Give the value type a `PoolKey`, and register a pool per value type** before `LoadAll`, keyed by it: `pool, err := s.RegisterInterned[GrantSet](KindGrantSet, func(g *GrantSet) Hash { return Hash(g.PoolKey()) })`. `PoolKey() [16]byte` is a method on the value type, beside its struct in `framework/livestore/registry`, never a function in `framework/livestore`: it is the value's content hash, either stored on the value when it is built (`GrantSet.PoolKey` returns the `ID` that `SealGrantSet` sets) or read from a hash its rows carry (`MCPUserDiscoveryContent.PoolKey` reads the stored SHA-256 digest). Equal keys share one copy, so the key must be collision resistant: SHA-256, never md5 or a fast non-cryptographic hash. Write a content hash with `registry.ContentHasher` (`NewContentHasher`, then `Num`, `Flag`, `Count`, `Str`, `Strs`, and `Sum`), and hash every field the value is shared by: a field left out makes two different values share one copy. Pin that with a test that walks the struct's fields with reflection, as `TestEveryGrantSetFieldChangesTheID` (`framework/livestore/registry`) and `TestDiscoveryHash_CoversEveryFieldADiscoveryServes` (`framework/mcpdiscovery`) do, and test `PoolKey` beside it in the registry package. Several collections may share one pool.
3. **Declare it on the collection:** `Interns(pool, part)`, where `part` returns the field's address: `func(k *VirtualKey) **GrantSet { return &k.Grants }`. A nested field works the same (`return &a.D.F`); when the struct holding it may be absent, return nil (`if a.D == nil { return nil }`). One `Interns` per part. The same value works on a table, state table or cache (see Table).
4. **Loaders fill the field with the record's own copy,** never interning by hand. As the record publishes, the collection swaps it for the shared copy and counts the use; the loader's copy is garbage after that.
5. **Readers use the field as it is.** A `Get` returns the record, whose field already points at the shared copy; there is no lookup and nothing is attached on read. Never change a shared value: every record holding it sees the change. A change arrives as a new record pointing at another copy.
6. **Memo by the value's ID,** not the record's, to share derived data across holders (`LocalGovernanceStore.permitViewOf` keys on `GrantSet.ID`).

Memory: the shared value once, one pointer per record, and one pool entry per distinct value. The pool is bookkeeping only, never read on a request: it finds the existing copy for equal content as a record publishes, and counts uses so it can let go. A copy no record has used for `internedGrace` (5 minutes) is dropped by the sweep, which runs every 30 seconds; nothing is interned before its record publishes, so no read in progress can lose a value. The pool has no methods of its own.

- `registry.SealGrantSet(g)`: sets a `GrantSet`'s ID from its content. Seal once, after building it and before the record publishes.
- Grant sets: the grant-set pool holds each virtual key's grants once per content; `VirtualKey.Grants` holds the key's shared copy.

### Cache

- `Store.NewCache[V](ctx, name, opts...)`: its two tables are the store's own (`internal:<name>`, `internal:<name>_index`). A cache has no capacity: entries stay until evicted or expired.
- `Fill(ctx, key, load)` with `CacheLoader[V]` returning `(value, indexKey, err)`: without `WithSharedFills`, a caller never gets a load that started before its call, and callers arriving during a load share one follow-up load. Errors are shared but never cached, a load an eviction or flush overtook is not installed, and no lock is held across the loader, so it may do I/O.
- `Get(key)`: a hit, or a miss when the validator rejects the entry, which is removed.
- `Evict(key)`, `EvictByIndex(indexKey)` (the index key is usually the row's primary key, for a write path that knows only the row), `EvictWhere(pred)` (a linear sweep for rare bulk invalidations), `Flush()`: each moves the generation, so a fill racing it never re-installs what it removed. `Expiry(key)` and `Len()` report.
- Options: `WithCacheTTL(fn)` (zero keeps the entry; negative means already expired, and it is not installed); `WithCacheNegativeTTL(negative, ttl)` keeps the results `negative` matches for `ttl` only; `WithSharedFills()` lets callers share a running load, for answers that do not depend on when the load started (an upstream call); `WithValidator(fn)` checks every hit. `newUserTokenCache` (`framework/oauth2/usertokencache.go`) shows all but the negative TTL, with `EvictByIndex` and `EvictWhere`.

### Table

- `Store.NewTable[K, V](ctx, name, opts...)`: a table the component writes. It starts empty, so a plugin may create it after the load. Fails on a taken or `internal:` name, and on write times together with a generation.
- `KeyedBy(kind)`: a state table, keyed by IDs of a registered `kind`, so its keys must be `string`; removing a record removes its entry (governance budget and rate limit usage). Its values are pointers by convention, never changed once stored and new for every write, so `CompareAndSwap` can tell whether anyone wrote since a read; storing a pointer that was stored before makes a read of it compare equal again. `NewTable` refuses an empty or unknown kind and non-string keys.
- Writes: `Put`, `PutWithTTL` (zero never expires), `Delete`. `PutIfAbsent(k, v, ttl)` stores only when no live value is there, checking only this table, so a later `PutIfNewer` can still replace it (`Sweeper.acquireProviderPollLease`). `CompareAndSwap(t, k, old, next)` is a function comparing with `==`; a nil `old` inserts only when absent. `GetAndDelete(k)` hands a value to exactly one caller (`WSTicketStore.Consume`). `GetOrCompute(k, compute)` may run `compute` concurrently for one key, so its results must be equivalent; in a memo it stores under the generation read before `compute` ran (`permitViewOf`).
- Reads: `Get`, `Expiry`; `Range` copies each shard first, so `fn` may write; `Len` visits every shard and is not for the request path. `Flush` drops every entry without telling the delete observer, for a component resetting its own state.
- `WithTTL(ttl)`: `Put` entries expire, read as missing and are swept in the background. `WithGeneration(gen)`: a memo; an entry stored under an older generation reads as missing, so bump the generation to invalidate.
- `Interns(pool, part)`: interning works on tables, state tables and caches as on collections (Interning, steps 1 to 3 and 5): a pointer field, a pool with a content hash, and the same `Interns(pool, part)` value. A table or state table accepts it when it holds `T` or `*T` (another value type is refused at creation, naming the table); a cache accepts it when it holds `*T`. Every write (`Put`, `PutWithTTL`, `PutIfAbsent`, `CompareAndSwap`, `GetOrCompute`, `PutIfNewer`) shares the part as it stores the value and counts it, and every delete, expiry, stale generation, `Flush` or `DropTable` gives the use back, in one count with the collections using the pool; a conditional write that stores nothing counts nothing. A pointer value's field is set in place as it is written, so a writer hands the value over and never changes it after; a struct value's stored copy takes the shared part, and `GetOrCompute` returns the value it stored. `Get` costs what it costs on any table: the entry already holds the shared copy. Use it for values that repeat across keys instead of copying them per entry; no table uses it yet. A table without `Interns` pays one inlined branch per write.
- `HoldsHandles()`: the values hold live handles (compiled programs, clients). It changes nothing in memory; a backing that keeps tables outside the heap keeps these in memory.
- `WithWriteTimes()` keeps each entry's Unix-nanosecond write time (`KeepsWriteTimes` reports it) and cannot combine with `WithGeneration`. `PutIfNewer(k, v, writtenAt, expiresAt)` and `DeleteIfNewer(k, deletedAt)` apply unless the entry was written later (equal times apply), and on a table without write times change nothing and report false. The tables in `SharedTables` (`lib/sharedtables.go`) keep write times.
- `OnSet(fn)`, `OnDelete(fn)`: one observer each, replaced by a later call and removed by nil, run on the writer's goroutine after the write, outside every lock. `OnSet` hears `Put`, `PutWithTTL`, `PutIfAbsent`, `CompareAndSwap` and `GetOrCompute`; `OnDelete` hears a `Delete` that found the key, expired or not, and a `GetAndDelete` that returned a value. `PutIfNewer`, `DeleteIfNewer` and `Flush` reach neither, so applying a write made elsewhere never echoes it.

### Keys

- `EncodeKey(parts...)` / `DecodeKey(key, n)`: a composite key from caller-controlled strings, length-prefixed so no part can forge a boundary; `DecodeKey` reports false for a key not built from `n` parts. Use it for any tuple key, above all one an `EvictWhere` predicate parses back (`headerCredentialCache.EvictByMCPClient`).
- `registry.NumericID(id)`: the record ID of a row with a numeric primary key, zero-padded to 20 digits so ID order is numeric order and a `List` comes back in creation order. Every kind keyed by a numeric row ID uses it (`PromptVersionID`, `VirtualMCPID`).

### Errors

- `ErrUniqueConflict` matches a `*UniqueConflictError{Kind, Index, Key, IDs}`: a load or refresh refused because it would leave two records under one unique key (a refresh first re-reads the other holders). Nothing from that write was published.
- `*MissingError{Refs}`: rows a `RetryMissing` refresh still could not find. Their records stay as they were, unless the same refresh read their parent. `refreshRows` wraps it so it also matches `configstore.ErrNotFound`.
- `ErrNoLiveStore`: what a component that needs the store wraps when it is given none, so every such refusal matches with `errors.Is` (`newSharedTables`).
- Also: `ErrLoaded` (a registration or load after the load), `ErrNotLoaded` (a refresh or `Load` before it), `ErrNoDatabase` (a read on a store loaded from records), `ErrInSubscriber`.

## 8. Tests every change needs

- **Failed re-read** on each write path that re-reads, showing its outcome (retry then success; exhausted retry keeps the previous record and answers 500). Inject with `failingReads` (a gorm `Before("gorm:query")` callback) or `writeThenFailReads` (fails reads right after the write commits), both in `lib/livestore_test.go`: `TestProviderKeyWritesRetryAFailedReread`.
- **Delete without a read:** `TestRemoveProviderKeyDropsTheKeyWithoutReadingTheDatabase`.
- **Holder read fails, nothing is deleted**, and **holder re-read fails, 500:** `TestDeleteVirtualMCPRefusesWhenAssignedKeysCannotBeListed`, `TestDeleteVirtualMCPAnswers500WhenAssignedKeysCannotReload`.
- **Another process's change:** `TestDBLookupRetryRereadsAFailedRead`.
- **Boot:** extend `TestConfigJSONContentPerMode` for a new config.json kind.
- **Raw-map lint:** `cd tests/rawmaps && GOWORK=off go test -count=1 ./...` (without `-count=1` Go reports a cached pass). When a map genuinely cannot move, add it to `tests/rawmaps/allowlist.txt` with its group and reason.
