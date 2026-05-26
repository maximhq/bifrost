---
name: release-checklist
description: Pre-release safety audit for the Bifrost repo. Scans database migrations changed in a release for high-scale deadlock / lock-contention risks and for work that blocks application boot time, then produces a pass/warn/fail report with a concrete remediation plan. Invoked with /release-checklist [git-ref-range]. Built to grow - new checks are appended to the Checks Registry.
allowed-tools: Read, Grep, Glob, Bash, Task, AskUserQuestion
---

# Release Checklist

A pre-release safety audit. Given a set of changes destined for release, run every
check in the **Checks Registry** below and produce one consolidated report. This skill
is **read-only**: it diagnoses, recommends a concrete fix for every finding, and never edits files. Applying a fix is a
separate, explicitly-approved step.

The registry currently holds two migration-safety checks. It is designed to grow - see
[Adding a New Check](#adding-a-new-check).

## Scope: what "the release" means

Determine the change set to audit, in this order:

1. If the user passed a git ref or range (e.g. `/release-checklist v1.4.0..HEAD` or
   `/release-checklist origin/dev`), use it.
2. Otherwise default to everything not yet on the main branch: diff `origin/dev...HEAD`
   (three-dot) and also include uncommitted working-tree changes.
3. If that range is empty, tell the user and ask for an explicit range.

Gather the raw material once, up front:

```bash
git fetch origin --quiet
git diff --stat origin/dev...HEAD
git diff origin/dev...HEAD -- '**/migrations.go' '**/matviews.go'
git status --porcelain
```

Migrations here are **Go-defined**, not `.sql` files. They live in:

- `framework/configstore/migrations.go` - config DB (providers, keys, virtual keys, budgets)
- `framework/logstore/migrations.go` - log DB (request logs; the high-volume table)
- `framework/logstore/matviews.go` - materialized views over the log DB

A release with **no diff in these files has no migration risk** - record both migration
checks as `PASS (no migrations changed)` and move on.

## Migration system facts (needed by both checks)

- **All migrations run synchronously at boot.** `triggerMigrations()` executes the full
  ordered migration list during store init, before the process serves traffic.
- **Supported databases: PostgreSQL and SQLite only.** No MySQL. Lock behavior differs
  sharply between the two - judge both.
- **Migrations are cluster-serialized** behind Postgres advisory lock `1000001` with a
  5-minute acquisition timeout. A slow migration on one pod stalls every other pod's boot.
- **Each migration func runs in a transaction by default** (`Options.UseTransaction = true`).
  A transaction holds every lock until the func returns - migration duration *is* lock duration.
- **Established escape hatch for heavy work:** index builds and materialized views run in
  post-startup background goroutines under separate advisory locks (`1000002` for indexes,
  `1000005`/`1000006` for matviews) - see `framework/logstore/postgres.go`,
  `ensurePerformanceIndexes()`, `ensureMatViews()`. `migrationAddProviderHistogramIndex`
  is intentionally a near-no-op that defers the real `CREATE INDEX CONCURRENTLY` there.
  This deferral pattern is the correct fix for anything heavy.

---

## Checks Registry

### Check 1 - Migrations that can deadlock or pile up under high-scale data

**Goal:** catch migrations whose locking is safe on a laptop but catastrophic on a
production table with hundreds of millions of rows (notably the logstore request-log table).

For every added/modified migration func in the diff, flag:

| Signal | Why it is dangerous at scale |
|---|---|
| `CREATE INDEX` without `CONCURRENTLY` | `SHARE` lock for the whole build - all writes block for minutes/hours on a large table. |
| `CREATE INDEX CONCURRENTLY` with `UseTransaction = true` | Postgres forbids `CONCURRENTLY` in a transaction - runtime error. Needs `UseTransaction = false` or the background path. |
| `ALTER TABLE` (add/drop column, add constraint, change type) on a hot table | Takes `ACCESS EXCLUSIVE`; queues behind in-flight queries, then every new query queues behind it - a cluster-wide stall. |
| `ADD COLUMN` with a volatile / non-constant `DEFAULT` | Rewrites the whole table under `ACCESS EXCLUSIVE`. A constant default is metadata-only and fine on PG11+. |
| `ADD ... FOREIGN KEY` / `ADD CONSTRAINT` validated immediately | Locks both tables and scans the child. Prefer `NOT VALID` then a separate `VALIDATE CONSTRAINT`. |
| Bulk `UPDATE`/`DELETE`/backfill over the whole table in one transaction | Holds row locks until commit; collides with live writes; bloats the table. Must be batched. |
| Order-dependent backfill, or migrations locking tables A-then-B vs B-then-A | Classic deadlock: two transactions grab the same locks in opposite order. |
| SQLite drop-column path (`CREATE TABLE ... AS SELECT` + `DROP` + `RENAME`) on a large table | Full table copy under SQLite's single global write lock - blocks every writer. |

**Report per flag:** func name, file:line, the exact signal, realistic production impact,
and a concrete remedy (add `CONCURRENTLY` + `UseTransaction = false`, batch the backfill,
defer to `ensurePerformanceIndexes`, add the FK as `NOT VALID`, etc.).

**Severity:** `FAIL` if writes to a high-volume table (logstore logs) can be blocked or a
deadlock is plausible; `WARN` for config-DB tables (low row counts, but still flag).

### Check 2 - Migrations that block boot-up time

**Goal:** because `triggerMigrations()` runs synchronously before the process serves
traffic, any migration whose runtime grows with row count delays - or past the 5-minute
advisory-lock timeout, breaks - every pod's startup.

Flag any added/modified migration whose cost scales with data volume:

| Signal | Why it blocks boot |
|---|---|
| Any non-`CONCURRENTLY` `CREATE INDEX` | Build time scales with row count; runs inside boot. |
| `CREATE INDEX CONCURRENTLY` placed directly in `triggerMigrations` | Even concurrent builds take minutes/hours on big tables; belongs in the background goroutine path. |
| Table rewrite: volatile-default `ADD COLUMN`, type change, SQLite drop-column copy | Rewrites/copies every row during boot. |
| Data backfill loop / bulk `UPDATE` over an unbounded row set | Runtime = O(rows); unbounded backfills have no ceiling. |
| `matviews.go` - creating or fully refreshing a matview in the synchronous path | Matview build scans the base table; must use the `ensureMatViews()` background path. |
| Anything that could realistically exceed the 5-minute advisory-lock timeout | Other pods fail to acquire lock `1000001` and crash-loop. |

**Expected safe pattern:** heavy work is a near-no-op migration; the real build runs
post-startup in a background goroutine. Compare each new heavy migration against
`migrationAddProviderHistogramIndex` - if it does the heavy lifting inline, that is the finding.

**Report per flag:** func name, file:line, why runtime scales with data, risk at production
row counts, and the remedy (move to the background path / batch it / make the default constant).

**Severity:** `FAIL` if the operation can plausibly exceed the 5-minute lock timeout on a
production-sized table; `WARN` otherwise.

---

## Report format

Output one consolidated report. Do not edit any files - this skill recommends
fixes, it does not apply them.

```
# Release Checklist - <ref range>

Audited: <N> files changed, <M> migration func(s) added/modified
Migration files touched: <list, or "none">

## Check 1 - High-scale deadlock / lock contention
Status: PASS | WARN | FAIL
<findings: severity - func name - file:line - impact - remedy>

## Check 2 - Boot-time-blocking migrations
Status: PASS | WARN | FAIL
<findings ...>

## Remediation Plan
<one table row per WARN/FAIL finding from every check; see rules below>

## Summary
<overall: SHIP / SHIP WITH WARNINGS / DO NOT SHIP>
<one line per FAIL that must be resolved before release>
```

### Remediation Plan table

Collect every `WARN` and `FAIL` finding from all checks into one table - this is
the actionable outcome of the audit. If every check is `PASS`, drop the table and
write `No remediation needed - all checks passed.` instead.

| # | Impacted migration (func @ file:line) | Check | Severity | Offending operation / query | Recommended change |
|---|---|---|---|---|---|

- **Impacted migration** - the migration func and its `file:line`.
- **Offending operation / query** - the exact SQL or migrator call that triggers
  the signal (e.g. `CREATE INDEX idx_logs_foo ON logs(foo)`), quoted verbatim or
  tightly paraphrased. This is *what is wrong*.
- **Recommended change** - the precise fix: the corrected statement, the option to
  flip (`UseTransaction = false`), or the path to move to (`ensurePerformanceIndexes`).
  This is *what to do instead*.

When a fix needs more than a table cell (multi-line SQL, a Go code change), keep
the table row short and add a `### Fix <#> - <func name>` block below the table
with a before/after the reader can apply directly:

```
### Fix 1 - migrationAddFooIndex   (framework/logstore/migrations.go:1234)
- // current - blocks all writes on logs for the whole build
- CREATE INDEX idx_logs_foo ON logs(foo)
+ // append to performanceIndexes; built CONCURRENTLY off the boot path
+ CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_foo ON logs(foo)
Why: keeps the boot path O(1); the real build runs in ensurePerformanceIndexes.
```

Rules: overall is `DO NOT SHIP` if any check is `FAIL`, `SHIP WITH WARNINGS` if any
`WARN`, else `SHIP`. Always show every check even when it passes - a visible
`PASS (no migrations changed)` is a real result. Never silently drop a check, and
never drop the Remediation Plan when there is at least one WARN/FAIL.

## Adding a new check

This skill is meant to grow. To add a check:

1. Add a `### Check N - <title>` subsection under **Checks Registry**, following Checks 1
   and 2: a one-line **Goal**, a **signal table**, a per-finding **report** instruction,
   and a **severity** rule.
2. If it needs background facts, add them once under "Migration system facts" (or a new
   facts heading) so they are stated once and reused.
3. Add the check's heading to the **Report format** template. Its `WARN`/`FAIL` findings
   flow into the shared **Remediation Plan** table automatically - no per-check table needed.
4. Keep checks independent - one check failing must not stop the others from running.
