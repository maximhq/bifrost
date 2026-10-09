# Log upload outbox container regression

Runs the gateway built from the worktree with a local mock provider and S3
service. No paid provider calls are made. Ports 18693 and 18694 must be free.

The test waits for the real one-minute ticker and checks:

- Successful normal uploads.
- HTTP 403 writes saved to disk, retained across a failed periodic retry,
  and recovered with complete request history and response after S3 recovers.
- HTTP 503 writes and broken connections saved after SDK retries, then recovered
  after killing the gateway with SIGKILL and starting it again.
- Deleting a pending log clears its disk payload immediately, keeps cleanup
  metadata while S3 rejects deletes, then removes that metadata after recovery.
- A successfully uploaded log is deleted while S3 rejects DELETE. Its cleanup
  record survives quota eviction, container recreation, and SIGKILL/restart;
  recovery deletes the remote object even though its database row is gone.
- A configured byte limit evicts the oldest pending file and recovers the two
  newer logs. The evicted log retains its database preview. Deletion identifiers
  are excluded from the upload budget and remain until remote cleanup succeeds.

Build the gateway with the repository's Go toolchain and embedded UI first:

```bash
cd transports
go build -tags sqlite_static,netgo,osusergo -ldflags "-extldflags '-static'" -o ../tmp/outbox-container/main ./bifrost-http
cd ..
python3 tests/e2e/api/fixtures/log-upload-outbox/run.py --binary tmp/outbox-container/main --keep
```

The script creates a fresh temporary directory and prints its artifact path.
Use `--work-dir tmp/outbox-e2e` to choose a fresh directory explicitly. A run
takes around six minutes. Omit `--keep` to stop its containers at completion.
The runtime image supplies the packaging; the mounted binary supplies the code
under test. Its UID 1000/GID 0 needs write access to the test's data/outbox mounts.

Three fixture-dependent `[PREVIEW]` cases in `provider-harness.json` assert all
four recovered messages, the response, and remote deletion cleanup. Run the
runner's fast unit tests and structural validators without live provider requests:

```bash
python3 -m unittest discover -s tests/e2e/api/fixtures/log-upload-outbox -p test_fixture.py
node tests/e2e/api/runners/augment-provider-harness.mjs --source tests/e2e/api/collections/provider-harness.json --out tmp/outbox-augmented.json
node tests/e2e/api/runners/filter-collection.mjs --source tmp/outbox-augmented.json --out tmp/outbox-filtered.json --provider openai --feature 'disk outbox'
```

The container runner exports `results/newman.env.json` with the recovered/deleted
IDs, the mock S3 URL, and `include_preview=1`. Pass it to Newman with the filtered collection while
the fixture is still running. These cases fail against the previous behavior:
failed uploads have no response and only one request message after S3 recovers.
