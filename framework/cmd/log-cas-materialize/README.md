# Materialized SQLite CAS benchmark

`log-cas-materialize` consumes an already-created offline SQLite snapshot and produces three compact databases in an empty work directory:

- `current.db`: the original schema and data compacted with `VACUUM INTO`.
- `field-zstd.db`: the same database plus one independently compressed row per eligible `(log_id, field)`; transformed source columns are empty. This is a benchmark schema, not a production reader format.
- `cas-manifest.db`: the same metadata, indexes, summaries and row-resident fields plus production-compatible `cas_blobs`, `cas_refs` and `cas_payloads` tables.

```bash
cd framework
go run ./cmd/log-cas-materialize \
  --db /path/to/offline-snapshot.db \
  --snapshot-method online-backup \
  --work-dir /path/to/new-empty-directory \
  --output /path/to/new-report.json
```

The source is opened read-only and immutable. The command rejects adjacent WAL/SHM files, non-empty work directories and existing report files, runs `quick_check`, and verifies the source SHA-256 before and after. It never connects to a remote service.

Transforms use 100-row transactions and update each log row once per scheme. Target databases use WAL and the production SQLite pragmas during transformation, then checkpoint and compact into the final files. After closing and reopening the files, every payload field is checked: transformed values are decompressed/reconstructed byte-for-byte from the materialized tables; NULL, empty and row-resident values are compared directly. Any mismatch exits non-zero.

After materialization, the same files can be rechecked without rewriting them:

```bash
go run ./cmd/log-cas-materialize \
  --db /path/to/offline-snapshot.db \
  --snapshot-method online-backup \
  --work-dir /path/containing/the-three-databases \
  --output /path/to/new-verification-report.json \
  --verify-existing
```

In this mode the work directory must contain exactly `current.db`, `field-zstd.db` and `cas-manifest.db`. Transformation, compaction, WAL and work-peak metrics are zero because no materialization runs; verification duration and physical size fields are freshly measured.

The JSON report contains aggregate physical file/page sizes, table row counts, transformation/compaction/verification durations and sampled WAL/work-directory peaks. When the Go SQLite build does not provide `dbstat`, `dbstat_available` is false and table/index bytes are omitted rather than reported as zero. It contains no payload, log ID or content hash. Peak disk is sampled at transaction boundaries, not continuously; process RSS, PostgreSQL behavior and online write latency remain separate acceptance work.
