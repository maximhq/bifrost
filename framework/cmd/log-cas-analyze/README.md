# Offline log CAS snapshot analyzer

`log-cas-analyze` reads an already-created, offline SQLite snapshot and estimates payload encoding sizes for three schemes:

- `baseline-payload`: non-empty payload bytes currently stored as serialized fields.
- `field-zstd`: each CAS-eligible field compressed independently, without deduplication.
- `cas-manifest`: production CAS chunking, manifest encoding, zstd compression and cross-row object deduplication.

The command never creates or modifies a snapshot, connects to a remote service, or prints log IDs or payload values. It refuses SQLite snapshots with adjacent WAL/SHM files, opens the database read-only and immutable, runs `quick_check`, and verifies the input SHA-256 before and after analysis.

```bash
cd framework
go run ./cmd/log-cas-analyze \
  --db /path/to/offline-snapshot.db \
  --snapshot-method online-backup \
  --exclude-field raw_request \
  --output report.json
```

`--snapshot-method` is provenance supplied by the operator; accepted values are `online-backup` and `vacuum-into`. Produce the snapshot separately using a SQLite-consistent method. Do not pass an active production database or a raw copy made without its WAL. Repeat `--exclude-field` for configured payload columns that remain row-resident; pricing fields are always treated as row-resident. Report files are created with mode 0600 and must not already exist.

Every eligible field is reconstructed immediately with the production decoder and compared byte-for-byte. A mismatch stops the command with a non-zero exit status. NULL and empty fields are counted separately.

The first version reports aggregate encoded-byte estimates. It does not build alternative SQLite databases, so estimates exclude table rows, indexes, references, page fragmentation, WAL and migration peak disk. Input file size is reported separately and must not be compared directly with encoded payload estimates. A complete Phase B run still requires materialized databases and resource measurements on an authorized offline snapshot.
