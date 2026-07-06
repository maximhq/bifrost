// Package batchaccounting settles usage and cost for provider batch jobs after
// they complete, out of band from the original request.
//
// # Two stores, by design
//
// A batch job's lifecycle is split across two stores whose lifecycles are
// deliberately opposite:
//
//   - Coordination state (BatchJobStore, backed by the config store) is a mutable
//     state machine — pending → processing → accounted/unpriceable/error — that is
//     UPDATE-d in place many times (poll rescheduling, ownership claims, settlement
//     markers). It belongs in the relational config store alongside the sidekiq and
//     dlock tables, not in the append-only log store (whose ClickHouse backend is a
//     poor fit for in-place mutation).
//   - The cost record (AggregateLogStore, backed by the log store) is written once,
//     append-only, as a single aggregate row in the logs table — the durable
//     financial artifact, sitting next to every other request cost row.
//
// Because the two stores may be different physical databases, the aggregate-log
// write and the state markers are not transactional together; idempotency instead
// relies on CreateIfNotExists plus the AggregateLogWrittenAt / GovernanceReportedAt
// markers, so a retry after a partial settlement resumes rather than double-counts.
//
// # Ownership fencing
//
// Delayed accounting can run concurrently across nodes (the sweeper on one node,
// a user-triggered /results call on another). ClaimBatchJob transitions a job to
// "processing" under a runner id; every subsequent advance/complete is fenced on
// that runner id, and a job stuck in "processing" past a stale threshold can be
// re-claimed. This mirrors the sidekiq job runner rather than using a separate
// claim token.
//
// # Entry points
//
//   - AccountBatchResults settles one completed batch: claim → price → write the
//     aggregate cost log → report governance usage → complete. It is idempotent and
//     safe to call from both the sweeper and request post-hooks.
//   - Sweeper polls due jobs (ListDueBatchJobs), retrieves provider status, and
//     invokes AccountBatchResults once a batch completes, rescheduling with capped
//     retries and deterministic backoff/jitter until then.
package batchaccounting
