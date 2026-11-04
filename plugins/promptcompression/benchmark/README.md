# prompt_compression benchmark

Two things live here:

- `estimates.md`: offline per-turn estimates (chars÷4 on the synthetic fixture, not provider-reported usage, cache effects not modelled). Regenerate with
  `PROMPTCOMPRESSION_WRITE_ESTIMATES=1 go test -run TestFixturePerTurnEstimates .` from the plugin directory.
- `run.mjs`: a live replay against a real provider. It has not been run yet because it costs money; the steps below are how to run it later.

## Live run

Requires Node 18+ and a provider key configured in the gateway. The script replays `testdata/agent_session_chat.json`
as 13 requests, each ending right before an assistant message (like a real agent loop), with `max_tokens: 1`.
A minimal `tools` array is derived from the function names in the history so providers that validate tool history accept it.

Check the script without network first:

```sh
node --check benchmark/run.mjs
BENCH_MODEL=openai/gpt-4o-mini node benchmark/run.mjs --dry-run   # prints request count and body sizes
```

1. Build and start the gateway from the repo root with the plugin **disabled**. In the gateway `config.json` leave the
   `prompt_compression` entry out or set `"enabled": false`, or toggle it at runtime with
   `POST /api/plugins` (enable) and `DELETE /api/plugins/prompt_compression` (disable).
   Static builds on some glibc systems need `GODEBUG=netdns=go` for DNS resolution:

   ```sh
   GODEBUG=netdns=go ./tmp/bifrost-http -app-dir ./data -port 8080
   ```

2. Run 1, plugin off:

   ```sh
   BENCH_MODEL=openai/gpt-4o-mini node plugins/promptcompression/benchmark/run.mjs --label off
   ```

3. Enable the plugin (config + restart, or `POST /api/plugins`), then run 2:

   ```sh
   BENCH_MODEL=openai/gpt-4o-mini node plugins/promptcompression/benchmark/run.mjs --label on
   ```

   Env: `BASE_URL` (default `http://localhost:8080`), `BENCH_API_KEY` (optional bearer token).
   Each run writes `benchmark/results-<label>.json` with `turn`, `prompt_tokens`, `cached_read_tokens`
   (from `usage.prompt_tokens_details.cached_tokens` or Bifrost's `cached_read_tokens`). Delete the file to start over.

4. Join the two files into `benchmark/results.md`:

   ```sh
   BENCH_MODEL=openai/gpt-4o-mini node plugins/promptcompression/benchmark/run.mjs --report
   ```

## Reading the results

Results depend on the provider's caching behaviour. Providers cache by prefix, so when an old tool output ages past
`tool_output_min_age_turns` and is trimmed, everything after it in the prompt changes and is re-billed once as uncached.
Expect `cached on` to dip on the turns where a new output gets trimmed and recover on the following turns.
`prompt_tokens` is what the provider counted for the whole request; the savings column compares that between the two runs.
Provider-side nondeterminism and cache TTLs mean single runs are noisy; repeat a run before drawing conclusions.
