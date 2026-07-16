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
// markers, so a retry after a partial settlement resumes rather than redoing work.
//
// # Settlement is at-least-once, not exactly-once
//
// The aggregate cost log is genuinely idempotent: CreateIfNotExists keys on a
// deterministic id, so replaying it is a no-op.
//
// Governance reporting is not, and the gap is deliberate. If ReportBatchUsage
// succeeds but the GovernanceReportedAt marker write then fails, the job is left
// retryable with the report already applied. Two things narrow this, neither
// closes it:
//
//   - The reporter is expected to dedupe on BatchUsageReport.RequestID (a stable
//     per-batch id). The governance plugin does, but only within one process.
//   - Governance budget bumps are in-memory deltas flushed periodically, so a
//     crash inside the window usually loses the bump too, making the replay a
//     correction rather than a double-count.
//
// The uncovered case is a marker failure WITHOUT a crash: the original node
// flushes its delta, and a later sweep on a DIFFERENT node retries with an empty
// dedupe set, double-counting that batch against one budget. This is accepted:
// the window requires a marker write to fail, the impact is bounded to one
// batch, and the synchronous request path alongside it is strictly less durable
// (it has no marker at all and simply under-counts on any crash). Closing it
// properly needs either a durable dedupe key persisted with the usage mutation,
// or marker-before-bump ordering (which trades double-count for under-count).
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
