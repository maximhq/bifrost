# Rewind: record, replay and fork agent runs

A Go plugin that records every request an agent sends through Bifrost (chat completions and relay calls such as Tavily search), replays a recorded run without calling any provider, and forks a run at step k with one change.

It uses only the HTTP transport hooks: `HTTPTransportPreHook` decides whether to forward a request, answer it from the recording or apply the fork's change, and `HTTPTransportPostHook` stores the live response.

## Build

```bash
go build -buildmode=plugin -o rewind.so .
```

Bifrost must be a dynamically linked build (see `docs/plugins/building-dynamic-binary.mdx`) compiled against the same `core` version as the plugin.

## Configure

```json
{
  "plugins": [
    {
      "enabled": true,
      "name": "rewind",
      "path": "/path/to/rewind.so",
      "placement": "pre_builtin",
      "config": { "data_dir": "./data/cassettes", "control_addr": "127.0.0.1:8099" }
    }
  ]
}
```

Requests are only touched when they carry the header `x-rewind-run: <run id>`. Everything else passes through.

## Use

Start a run, then send requests with the header. A run the plugin hasn't seen is recorded automatically.

```bash
# record
curl -X POST localhost:8099/rewind/runs -d '{"run_id":"r1","mode":"record"}'
# replay r1 exactly; a request that doesn't match returns 409 with a field-level diff
curl -X POST localhost:8099/rewind/runs -d '{"run_id":"r1-replay","mode":"replay","source":"r1","match":"strict"}'
# fork r1 at step 2 with a simulated provider outage
curl -X POST localhost:8099/rewind/runs -d '{"run_id":"r1-outage","mode":"fork","source":"r1","fork_at":2,"mutation":{"type":"outage","status":503}}'
# read a cassette
curl localhost:8099/rewind/runs/r1
```

Fork mutations: `model_swap` (rewrites `model` and forwards), `outage` and `tool_error` (returns the status without calling the provider), `poison` (appends text to the first result of a recorded search response), `edit` (returns a given body).

Steps are matched by position and by a SHA-256 fingerprint of the request after timestamps, UUIDs, tool-call ids, long numbers and the `id`, `stream`, `user` and `metadata` fields are normalised.

## Limits

Only traffic that passes through the gateway is recorded. Streaming responses are recorded but not replayed as streams yet. The run API listens on its own address because plugins can't add routes to the HTTP router.

## Tests

```bash
go test ./...
```

A larger example with two benchmark agents, a harness and a viewer is at https://github.com/YASASWIP012/rewind.
