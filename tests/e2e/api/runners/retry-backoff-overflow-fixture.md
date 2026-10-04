# Local retry backoff overflow regression (#6964)

These two opt-in cases exercise the high-attempt overflow noted in #6964 through
the real gateway retry loop. The cancellation fix and Retry-After hints are separate
behaviors. All upstream requests go to a local OpenAI-compatible fixture using a
synthetic key; no provider account is needed.

Configure initial and maximum backoff to 100ms and 41 retries. At upstream call 39,
`calculateBackoff(37)` previously multiplied `100000000ns * 2^37` before applying
the cap, wrapping the duration negative. Calls 41 and 42 also wrap negative. Those
timers expire immediately instead of waiting for the capped 80–100ms jitter.

The fixture returns 503 for its first 41 calls. From call 39 onward, an arrival less
than 60ms after the previous response produces nonretryable 400 with
`retry-backoff-overflow-early`. Only call 42 returns `backoff-overflow-ok:42` as
JSON or an SSE delta followed by `[DONE]`. The cases require 200 and that marker;
they never ignore unexpected error statuses. Each request carries a fresh GUID
so repeat runs and the JSON/streaming variants have independent counters.
The unary provider conversion may omit `stream`; the fixture accepts omission
or `false` for JSON and requires `true` for streaming.

Run from a checkout containing the code under test. Use separate terminals for
the fixture and the gateway. The example ports must be available; if you change
them, update the provider URL and gateway URL consistently.

1. Start the loopback fixture:

   ```sh
   node tests/e2e/api/runners/retry-backoff-overflow-fixture.mjs
   ```

2. Prepare an isolated gateway profile and Newman environment:

   ```sh
   mkdir -p tmp/retry-backoff-overflow-profile
   cat > tmp/retry-backoff-overflow-profile/config.json <<'JSON'
   {
     "providers": {
       "openai-backoff-overflow-fixture": {
         "keys": [{
           "name": "fixture",
           "value": "fixture-only",
           "models": ["retry-backoff-overflow-json", "retry-backoff-overflow-stream"],
           "weight": 1
         }],
         "custom_provider_config": {
           "base_provider_type": "openai",
           "allowed_requests": {"chat_completion": true, "chat_completion_stream": true}
         },
         "network_config": {
           "base_url": "http://127.0.0.1:8791",
           "default_request_timeout_in_seconds": 30,
           "max_retries": 41,
           "retry_backoff_initial": 100,
           "retry_backoff_max": 100
         }
       }
     }
   }
   JSON
   cat > tmp/retry-backoff-overflow-environment.json <<'JSON'
   {"name":"Local backoff overflow","values":[{"key":"backoffOverflowFixture","value":"1","enabled":true}]}
   JSON
   ```

   The base URL has no `/v1` suffix: the OpenAI implementation appends
   `/v1/chat/completions`. The fixture listens on the same host's loopback; run
   the gateway directly on that host for these commands. This profile contains
   only the custom fixture provider and synthetic credentials.

3. Start Bifrost from the code under test and run exactly the two cases:

   ```sh
   make dev PORT=8312 APP_DIR=$(pwd)/tmp/retry-backoff-overflow-profile USE_INFISICAL=0
   make run-provider-harness-test PROVIDER=openai FEATURE="retry backoff overflow" BASE_URL=http://localhost:8312 ENV_FILE=tmp/retry-backoff-overflow-environment.json PARALLEL=0 COMPAT=off DB_VERIFY=0 SKIP_STREAM_CANCEL=1 HARNESS_MAX_REQUESTS=2 USE_INFISICAL=0
   ```

   Start the gateway before the second command. `DB_VERIFY=0` keeps the shared
   harness reporter from using a different profile's logs database. The request
   names and bodies select only the custom provider. The folder skips unless
   `backoffOverflowFixture=1`. A complete run uses two gateway requests and at
   most 84 local upstream requests, estimated to take less than 9 seconds excluding
   build/startup. This estimate is not a recorded live run. Stop both terminals
   after the run.

For a directly bounded Newman run after starting the same fixture/gateway:

```sh
node tests/e2e/api/runners/augment-provider-harness.mjs --source tests/e2e/api/collections/provider-harness.json --out tmp/backoff-overflow-augmented.json
node tests/e2e/api/runners/filter-collection.mjs --source tmp/backoff-overflow-augmented.json --out tmp/backoff-overflow-filtered.json --provider openai --feature "retry backoff overflow"
newman run tmp/backoff-overflow-filtered.json --env-var baseUrl=http://localhost:8312 --env-var backoffOverflowFixture=1 --timeout-request 20000 --timeout 60000
```

Before the fix, an overflowing transition should produce 400; after the fix, both
cases receive 200 with the 42-call marker. Heavy host scheduling stalls can mask
an immediate retry, so the deterministic Go duration boundary tests remain the
primary arithmetic regression. This HTTP case pins JSON and streaming startup
through the gateway. No Retry-After header or fallback is used.

The fixture's own counter/response tests open no sockets. Standard Node runner:

```sh
node --test tests/e2e/api/runners/retry-backoff-overflow-fixture.test.mjs
```

For a sandbox that blocks the test runner's child process, use the same tests
within one Node process:

```sh
node tests/e2e/api/runners/retry-backoff-overflow-fixture.test.mjs
```
