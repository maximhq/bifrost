## 🐞 Fixed

- **GIN Index Migration** — Rewrote metadata GIN index migration to run non-transactionally with CREATE INDEX CONCURRENTLY in a background goroutine, preventing pod startup deadlocks during rolling upgrades on large tables
- **Advisory Lock Refactor** — Generalized advisory lock into reusable `acquireAdvisoryLock` with dedicated GIN index lock key to serialize index builds across cluster nodes
- **Metadata Pointer Safety** — Changed `Log.Metadata` from `string` to `*string` to properly represent nullable metadata and prevent serialization errors from aborting log writes
- **Distributed Lock Nil Safety** — Added nil-store guards to `DistributedLock` methods to prevent panics when config store is unavailable
- **PostgreSQL 16 Requirement** — Added minimum PostgreSQL 16 version check at logstore startup, leveraging `IS NOT JSON OBJECT` for server-side metadata validation
