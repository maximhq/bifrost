# Local Retry-After regression (#6971)

These cases use a local OpenAI-compatible fixture, not a paid model. The fixture
first returns `429` and a `Retry-After` hint, then returns `400` with
`retry-before-retry-after` if Bifrost retries too soon. Correctly delayed requests
receive `hello`. A fresh GUID in each request body separates repeated runs.

1. Start the fixture from the repository root:

   ```sh
   node tests/e2e/api/runners/retry-after-fixture.mjs
   ```

2. Start an **isolated** Bifrost gateway with this OpenAI provider configuration.
   Do not replace a real provider configuration. If the gateway runs in Docker,
   use an address it can reach instead of its own loopback.

   ```json
   {
     "providers": {
       "openai": {
         "keys": [{"name": "fixture", "value": "fixture-only", "weight": 1}],
         "network_config": {
           "base_url": "http://127.0.0.1:8790/v1",
           "max_retries": 1,
           "retry_backoff_initial": 10,
           "retry_backoff_max": 5000,
           "allow_private_network": true
         }
       }
     }
   }
   ```

3. Filter and run the four opt-in cases:

   ```sh
   node tests/e2e/api/runners/augment-provider-harness.mjs --source tests/e2e/api/collections/provider-harness.json --out /tmp/retry-after-augmented.json
   node tests/e2e/api/runners/filter-collection.mjs --source /tmp/retry-after-augmented.json --out /tmp/retry-after-filtered.json --provider openai --feature '#6971'
   newman run /tmp/retry-after-filtered.json --env-var baseUrl=http://localhost:8080 --env-var retryAfterFixture=1
   ```

The cases pin seconds, HTTP-date, streaming startup, and the configured backoff
cap. A 60-second hint with a 5-second cap must return the original 429 and its
header without sending an early retry. Before the fix these cases receive 400;
after the fix the first three receive 200 and the cap case receives 429.

The folder skips requests unless `retryAfterFixture=1`. Do not enable it against
a real provider. No authentication/rate-limit error guard is used: those statuses
are part of this controlled regression. Cancellation, deadline, key rotation,
overflow, and stale-header behavior are covered by `core/retryafter_test.go`.
