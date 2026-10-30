# chaosmock

A fleet of controllable MCP servers for finding out how an MCP client handles a
server that changes underneath it.

Each virtual server speaks the protocol for real, so an ordinary client connects
without knowing it is a mock. Each also exposes an HTTP control API that mutates
its tools, credentials and failure behaviour while that client stays connected.
The counters every server keeps are the point: they record what was actually
asked of it, which is the ground truth to compare against whatever the client
reports about itself.

## Why the protocol is hand-rolled

The mock implements JSON-RPC and the MCP methods itself rather than wrapping an
MCP server library. That is deliberate. Its job is to violate the protocol on
demand: malformed bodies, bare HTTP statuses where a JSON-RPC error belongs,
truncated responses, invalidated sessions, a version the client never asked for.
A library that guarantees well-formed output cannot produce any of those.

The cost of that freedom is that compatibility has to be proven rather than
assumed, which is what `compat_test.go` does: it points a real `mcp-go` client,
the same library and version the system under test uses, at the mock and checks
that a healthy server is accepted normally.

## Running it

```sh
GOWORK=off go build -o ./tmp/mcpchaos ./cmd/mcpchaos
./tmp/mcpchaos -servers alpha,beta -tools echo_tool,add_tool
```

```
mcpchaos listening on http://127.0.0.1:8400
  fleet control  http://127.0.0.1:8400/control
  alpha        mcp http://127.0.0.1:8400/s/alpha/mcp   control .../control/alpha
```

Paths are derivable from a server's name, so a driver can compute a URL without
asking the fleet: `/s/<name>/mcp` for streamable HTTP, `/s/<name>/sse` for the
legacy transport, `/control/<name>/...` to mutate.

`-seed <file.json>` builds a fleet with per-identity tool views, auth and
starting faults in one step, for scenarios that need more than names.

## Transports

Both speak the protocol for real, and they are not interchangeable. The
differences below are the point: a client that handles one correctly can still
mishandle another.

| Transport | Endpoint | What is distinct about it |
|---|---|---|
| Streamable HTTP | `/s/<name>/mcp` | Response returns in the reply to the POST. A `GET` on the same path opens the standalone listening stream, which only a client that opted in ever does. |
| Legacy HTTP+SSE | `/s/<name>/sse` | Response arrives **out-of-band** on a stream opened separately; the POST is only acknowledged with 202. The stream is also the session, so it exists before `initialize` rather than being created by it. |

The SSE split is what makes a dropped connection mean something different
there. Under streamable HTTP a drop fails the request in flight; under SSE it
**strands** it, because the POST already succeeded and the answer has nowhere
to land. `sse_test.go` pins that a client gives up on its own deadline rather
than being told.

## Control API

Every mutating verb answers with the server's state after the change, so a
scenario step is self-documenting in a transcript. Unknown verbs and unknown
JSON fields are rejected with 400: a typo that silently did nothing would look
exactly like the system under test behaving correctly.

| Verb | Body | Effect |
|---|---|---|
| `GET /control` | | every server's state, plus fleet connection stats |
| `POST /control` | `{"add":"name"}` / `{"remove":"name"}` | add or remove a server |
| `GET /control/<s>` | | that server's state |
| `POST /control/<s>/tools/add` | `{"tool":{...}}` | add or replace a tool |
| `POST /control/<s>/tools/remove` | `{"name":"..."}` | remove a tool |
| `POST /control/<s>/tools/set` | `{"tools":[...]}` | replace the whole set |
| `POST /control/<s>/tools/rename` | `{"from":"...","to":"..."}` | rename, preserving schema and description |
| `POST /control/<s>/tools/describe` | `{"name":"...","text":"..."}` | change only the description |
| `POST /control/<s>/tools/schema` | `{"name":"...","schema":{...}}` | change only the input schema |
| `POST /control/<s>/tools/overlay` | `{"identity":"...","tools":[...]}` | give one identity its own tool list |
| `POST /control/<s>/faults` | `{"faults":{...}}` | set the fault state |
| `POST /control/<s>/faults/clear` | | return to healthy |
| `POST /control/<s>/auth` | `{"auth":{...}}` | set auth requirements |
| `POST /control/<s>/auth/grant` | `{"token":"...","identity":"..."}` | add a bearer token |
| `POST /control/<s>/auth/revoke` | `{"token":"..."}` | revoke one token, leaving others working |
| `POST /control/<s>/sessions/kill` | | invalidate sessions without touching the transport |
| `POST /control/<s>/streams/drop` | | close listening streams without killing sessions |
| `POST /control/<s>/notify/tools-changed` | | push `notifications/tools/list_changed` |
| `POST /control/<s>/counters/reset` | | zero the tallies, keeping live facts |

## Faults

Grouped by the layer they break at, because that grouping is what a client
should attribute a failure to, and attribution is one of the things worth
testing. All are independently settable, so a server can be slow and
intermittently rate-limited and refusing ping at once, which is what a degrading
upstream actually looks like.

| Layer | Fields |
|---|---|
| Transport | `refuse`, `reset`, `drop_sse` |
| HTTP | `status`, `status_once`, `retry_after`, `www_auth` |
| Protocol | `init_fail`, `bad_version`, `no_ping`, `ping_fail`, `list_fail`, `kill_session` |
| Timing | `hang`, `latency`, `jitter` (duration strings, e.g. `"1500ms"`) |
| Call | `err_rate`, `err_kind`, `malformed` |

`err_kind` distinguishes failures that are not interchangeable: `tool` is a
successful exchange reporting a failed tool, `protocol` is a failed exchange,
and `http` is a bare 500 with no JSON-RPC body. A client that conflates them
looks correct under one and wrong under another.

A per-tool `Fault` overrides the server-wide set entirely, so one broken tool on
an otherwise healthy server is expressible.

## Counters, and what they can honestly prove

| Counter | Proves |
|---|---|
| `calls` | keyed by the tool the server **resolved** the request to, not the name the caller sent. A client that maps a caller-facing name to the wrong upstream tool returns a correct-looking response and a wrong counter, and nothing else catches it. |
| `calls_by_identity` | that revoking one caller's credential did not affect another's |
| `sessions` | the reconnect signal for HTTP (see below) |
| `open_streams` / `max_streams` | stream-level leak detection |
| `methods` | every method seen, including unimplemented ones, so a surprise surfaces |
| `faulted` | how many responses were injected failures rather than real ones |

**Connection counts are not the HTTP reconnect signal.** Every mcp-go streamable
HTTP transport is built with a bare `&http.Client{}`, whose nil Transport
resolves to the process-wide `http.DefaultTransport` pool, so separate clients to
one host share keep-alive connections. Session count is the reliable measure,
since every initialize mints a new session id regardless of which socket carried
it. Fleet-level connection stats are still tracked, for genuine fleet-wide leaks
and for scenarios that deliberately disable pooling. `connections_test.go` pins
both behaviours so a change to how clients are built surfaces there rather than
silently invalidating an assertion that rests on it.

## Push notifications

Streamable HTTP has two server-to-client channels: the stream returned in
response to a POST, and a standalone long-lived GET stream. Only the second can
carry an unsolicited notification, and a client only has one if it opted in with
`WithContinuousListening()`. `listening_test.go` pins both sides: a listening
client receives `notifications/tools/list_changed`, and a non-listening one
opens no stream at all, so the notification reaches nobody and the change is
only discoverable by asking again.

## Testing

```sh
GOWORK=off go test ./chaosmock/ -race
```
