# PostgreSQL materialized-view pressure and accuracy comparison

Measured on 2026-09-19 against PostgreSQL 16.13 (Alpine, aarch64), with
`shared_buffers=128MB`, `work_mem=4MB`, and no container CPU or memory cap.
The Docker host was shared. These are synthetic workload measurements, not
production capacity estimates or CPU-utilization measurements.

The baseline uses full concurrent hourly refreshes and ordinary raw-log retention.
The changed implementation uses hourly snapshot-and-merge publication, transactional
change tracking, archive finalization before retention, and adaptive scheduling.
Both use identical GORM-created raw tables/indexes and the same filter matviews.

## Results

Arrows show **full refresh → snapshot-and-merge**. Refresh execution time is the median of three trials.

| Total logs | Refresh scope | Server time (s) | Buffer hits | Buffer reads | Temp writes (MiB) | WAL (MiB) |
|---:|---|---:|---:|---:|---:|---:|
| 110,000 | Hourly view | 0.55 → 0.75 | 5,136 → 27,080 | 0 → 1 | 23.64 → 11.78 | 0.07 → 5.47 |
| 110,000 | All matviews | 1.31 → 1.66 | 43,540 → 65,525 | 3 → 1 | 23.65 → 11.78 | 0.44 → 5.84 |
| 1,010,000 | Hourly view | 3.20 → 0.79 | 2,907 → 30,389 | 24,816 → 5 | 250.87 → 11.87 | 0.06 → 5.49 |
| 1,010,000 | All matviews | 5.05 → 2.84 | 28,803 → 124,470 | 218,391 → 127,420 | 285.40 → 46.82 | 0.46 → 5.90 |

At 1,010,000 logs, hourly refresh execution time fell **75.3%**, and the complete pass fell **43.7%**. The full pass wrote **83.6% less temporary data** and made **41.7% fewer shared-buffer reads**, but produced **12.8× more WAL**. Buffer hits increased; fewer raw tuples does not imply fewer accesses to every PostgreSQL data structure.

At 110,000 logs, hourly refresh was **35.8% slower** and the complete pass **26.1% slower**. This fixture has many aggregate groups relative to raw rows; copying and comparing those groups outweighs the saved aggregation work. The change is not a universal reduction in database pressure.

The indexed range plan visited about **10,276** raw tuples at the smaller size and **11,388** at the larger size, versus 110,000 and 1,010,000 total rows. The active window can advance during a run. Old saved aggregate rows are still copied and compared every refresh; this is not constant-time incremental storage.

### Ingestion and retention costs

| Historical rows | Phase | Server time (s), full → changed | Client wall time (s), full → changed | WAL (MiB), full → changed |
|---:|---|---:|---:|---:|
| 100,000 | Bulk load | 3.20 → 3.25 | 3.22 → 3.26 | 189.93 → 189.26 |
| 100,000 | Append 10,000 logs in 100 batches | 0.23 → 0.40 | 0.50 → 0.80 | 20.93 → 19.01 |
| 100,000 | Expire entire backlog + refresh | 0.34 → 12.24 | 0.49 → 12.83 | 19.91 → 24.70 |
| 1,000,000 | Bulk load | 43.35 → 35.06 | 43.38 → 35.08 | 1,943.20 → 1,936.23 |
| 1,000,000 | Append 10,000 logs in 100 batches | 0.80 → 0.26 | 1.26 → 0.72 | 23.54 → 18.77 |
| 1,000,000 | Expire entire backlog + refresh | 3.02 → 151.05 | 4.19 → 152.73 | 197.88 → 248.96 |

Ingestion timing is a single observation and changes direction across sizes; it does **not** establish an ingestion speedup. Ongoing ingestion performs roughly **4–6% more logical buffer accesses** with the tracking triggers. Checkpoints/cache state also affect WAL totals. Concurrent writers to the same hour were not pressure-tested.

The largest regression is retention: finalizing and deleting 765,260 old requests took **151.05 s of server execution versus 3.02 s** for deletion plus full refresh. It generated **248.96 MiB versus 197.88 MiB of WAL**. The comparison includes preserving history, which the baseline does not do. Repeated finalization eligibility checks and archive safeguards make the backlog cleanup expensive. Adaptive scheduling governs periodic refreshes; it does **not** throttle retention finalization. This needs a separate retention optimization before claiming lower overall database load.

### Accuracy

All-column comparisons were exact at both sizes after the refresh trials. After retention:

| Initial logs | Full-refresh historical count | Snapshot-and-merge historical count | Full-refresh historical cost | Snapshot-and-merge historical cost | Changed aggregate rows vs pre-expiry reference |
|---:|---:|---:|---:|---:|---:|
| 110,000 | 33,490 | **110,000** | 4,187.00 | **13,750.75** | 11,020 → **0** |
| 1,010,000 | 244,740 | **1,010,000** | 30,593.25 | **126,250.75** | 11,020 → **0** |

Snapshot-and-merge preserved **100% of the pre-expiry counts, costs, and every stored aggregate column**. Full refresh remained correct for the surviving raw logs, but lost the expired historical buckets. Accuracy here is relative to finalized hourly history: changes arriving after an hour freezes are deliberately excluded, and unsupported per-request filters still require retained raw data.

### Adaptive scheduling

The first refresh and later passes use the same rule: wait at least the configured interval after completion; if a pass exceeds the current cooldown, raise the cooldown to twice its measured duration. The cooldown persists in PostgreSQL and is enforced across replicas. A pre-pass lease prevents immediate retry after connection loss; timeouts and successful passes both record their cooldown when the connection survives. Fast passes do not automatically reduce a learned cooldown.

In the actual five-second-floor run, the large scheduled pass took **2.55 s**, so it kept the five-second cooldown. An immediate second attempt consumed **0.10 ms of server execution and no refresh work**. This workload did not require an interval increase. Separate scheduler regressions verify first-pass growth, persistence across replica instances, and cancellation protection.

For a pass that takes 75 seconds against a 60-second interval, the old ticker could run continuously. The new scheduler waits 150 seconds after completion, starting the next pass at 225 seconds or later. That changes refresh wall-time duty from approximately 100% to 33%; this is a scheduling calculation, **not a measured CPU-utilization reduction**. The refresh timeout remains unchanged.

## Method

- Load 100,000 or 1,000,000 terminal requests across 720 hours, ten models/users,
  and success/error statuses. Then append 10,000 current-hour requests in 100
  batches of 100. Costs use binary-exact fractions for strict equality checks.
- Warm up hourly views, then measure three hourly refreshes and three complete
  refresh passes per mode. Each trial corrects a recent request's cost. Alternate
  mode order across trials. Tables above report medians for repeated phases.
- Measure bootstrap ingestion and ongoing ingestion separately. These are single
  observations, so differences include cache, checkpoint, and host variance.
- Measure an actual scheduled pass with the supported five-second interval floor,
  followed immediately by another replica-style attempt during its cooldown.
- Expire raw requests older than seven days in 5,000-row batches, then refresh.
  This phase includes finalization and deletion of the entire backlog, not merely
  one cleanup batch or steady-state daily retention.
- Compare every stored aggregate column with `EXCEPT ALL` after the refresh trials.
  After expiry, compare against a separate pre-expiry materialized reference.
  Include counts, costs, token metrics, cache counters, and stored percentiles.
- Inspect the real refresh SELECT with `EXPLAIN ANALYZE` to ensure raw reads are
  bounded to selected hours. Plan row counters can round per-loop averages.

`pg_stat_statements` measures top-level server execution time, shared-buffer hits
and reads, dirtied blocks, temporary blocks written, and generated WAL. Nested
statements are excluded to avoid double counting. Monitoring and counter-reset
queries are excluded. Block size is 8 KiB; a shared-buffer read may still be served
by the operating-system cache and is not necessarily a physical device read.
Client wall time is recorded separately in [results.json](results.json).
See the [PostgreSQL statistics documentation](https://www.postgresql.org/docs/16/pgstatstatements.html).

The experiment excludes initial materialized-view construction from refresh
timings. It does not measure concurrent writer contention, production replica
counts, p95/p99 request latency, or long-term disk growth. Stored hourly percentile
equality preserves the existing metric definition; weighted hourly percentiles
remain an approximation of a percentile over a larger raw population.

## Reproduce

Run against a disposable, otherwise idle PostgreSQL instance. The opt-in test
creates and drops UUID-named schemas and resets statement statistics for the
specified database. With no benchmark DSN, it skips during ordinary test runs.

```sh
docker run -d --name bifrost-matview-pressure \
  -p 127.0.0.1:5433:5432 \
  -e POSTGRES_USER=bifrost -e POSTGRES_PASSWORD=bifrost_password \
  -e POSTGRES_DB=bifrost postgres:16-alpine \
  -c shared_preload_libraries=pg_stat_statements \
  -c pg_stat_statements.track=all -c track_io_timing=on

docker exec bifrost-matview-pressure psql -U bifrost -d bifrost \
  -c 'CREATE EXTENSION IF NOT EXISTS pg_stat_statements'

BIFROST_MATVIEW_PRESSURE_DSN='host=localhost port=5433 user=bifrost password=bifrost_password dbname=bifrost sslmode=disable' \
BIFROST_MATVIEW_PRESSURE_OUTPUT=/tmp/matview-pressure.json \
go test ./framework/logstore -run '^TestMatViewPressureComparison$' \
  -count=1 -failfast -timeout 30m -v
```

Add `BIFROST_MATVIEW_PRESSURE_SMOKE=1` to exercise all phases with 1,000 historical
rows before running the full comparison. The test asserts accuracy and bounded
raw scans; it deliberately has no machine-dependent speed assertion.
