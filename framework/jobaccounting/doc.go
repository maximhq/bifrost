// Package jobaccounting settles usage and cost for provider-side jobs after they
// complete, out of band from the original request.
//
// A "provider job" is work the provider runs asynchronously — a batch, a video
// generation — where the price is not known when the request returns. It is
// deliberately not called an "async job": logstore.AsyncJobExecutor and the
// /v1/async/* routes already own that term for Bifrost's own fire-and-forget
// request queue, which is a different mechanism entirely.
//
// Each job kind supplies a Settler: how to poll the provider, how to turn a
// terminal job into a price, and how that kind's detail rides on the cost row.
// Everything else here — claiming, writing, reporting, retrying — is shared,
// because it is the same problem for every kind.
//
// # Two stores, by design
//
// A job's lifecycle is split across two stores whose lifecycles are deliberately
// opposite:
//
//   - Coordination state (JobStore, backed by the config store) is a mutable
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
// Governance reporting is not, and the gap is deliberate. If ReportUsage
// succeeds but the GovernanceReportedAt marker write then fails, the job is left
// retryable with the report already applied. Two things narrow this, neither
// closes it:
//
//   - The reporter is expected to dedupe on UsageReport.RequestID (a stable
//     per-job id). The governance plugin does, but only within one process.
//   - Governance budget bumps are in-memory deltas flushed periodically, so a
//     crash inside the window usually loses the bump too, making the replay a
//     correction rather than a double-count.
//
// The uncovered case is a marker failure WITHOUT a crash: the original node
// flushes its delta, and a later sweep on a DIFFERENT node retries with an empty
// dedupe set, double-counting that job against one budget. This is accepted:
// the window requires a marker write to fail, the impact is bounded to one
// job, and the synchronous request path alongside it is strictly less durable
// (it has no marker at all and simply under-counts on any crash). Closing it
// properly needs either a durable dedupe key persisted with the usage mutation,
// or marker-before-bump ordering (which trades double-count for under-count).
//
// Governance sees a job's usage only at settlement: cost recalculation is
// reporting-only and never re-reports to budgets, the same platform-wide invariant
// that holds for recalculating any ordinary request row.
//
// # Priced-at-zero is not unpriceable
//
// Settlement.Priced with Cost 0 is a final answer — a terminal job that genuinely
// owes nothing, such as a failed video generation the provider does not bill for.
// It completes the job. Unpriceable is the opposite: the work happened and we
// could not put a number on it, so the row is written with cost NULL, the job is
// parked re-drivable, and the existing missing-cost backfill can recover it later.
// Collapsing the two would either bill nothing for work that was done, or leave
// free work forever "pending" in the sweeper.
//
// # Ownership fencing
//
// Delayed accounting can run concurrently across nodes (the sweeper on one node,
// a user-triggered settlement on another). ClaimProviderJob transitions a job to
// "processing" under a runner id; every subsequent advance/complete is fenced on
// that runner id, and a job stuck in "processing" past a stale threshold can be
// re-claimed. This mirrors the sidekiq job runner rather than using a separate
// claim token.
//
// # Kinds: batch
//
// BatchSettler is the Settler for ProviderJobKindBatch. What is batch-specific:
//
//   - summarizeResults prices a batch's result rows and splits them per model. A
//     batch can span several models, so unlike a single-model job its cost row
//     carries a per-model breakdown and is labelled "mixed".
//   - Malformed rows do not abandon the batch. One bad JSONL line used to discard
//     every correctly parsed row's tokens and cost, permanently — the raw provider
//     results are not persisted anywhere else, so what is not priced here is lost
//     for good. Parsed rows price normally and the unreadable count rides on the
//     row alongside an incomplete marker, so the total is recorded as under-stating
//     rather than silently pretending to be whole.
//   - A terminal-but-not-completed batch is not an empty one. Expired and cancelled
//     batches still bill the requests that finished before the window closed, and
//     those rows are in the output file, so settlement fetches them anyway.
//   - Usage extraction is per provider (OpenAI/Azure/Bedrock/Gemini/Vertex share a
//     response-body shape; Anthropic has its own), and the two wire conventions for
//     cache tokens — inclusive of the base input count or exclusive of it — are
//     normalized apart in usageFromValue.
//
// # Entry points
//
//   - AccountJob settles one job: claim → price via the Settler → write the
//     aggregate cost log → report governance usage → complete. It is idempotent and
//     safe to call from both the sweeper and request post-hooks.
//   - Sweeper polls due jobs, asks the Settler for provider state, and invokes
//     AccountJob once a job is settleable, rescheduling with capped retries and
//     deterministic backoff/jitter until then.
//   - AccountBatchResults settles one completed batch from results the caller
//     already holds, and is safe to call from both the sweeper and the
//     /v1/batches/{id}/results post-hook. NewBatchSweeper binds Sweeper to the
//     batch settler.
package jobaccounting
