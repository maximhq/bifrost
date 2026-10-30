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

`-seed <file.json>` builds a fleet with per-identity tool views, auth,
instructions and starting faults in one step, for scenarios that need more
than names.

## Transports

All three speak the protocol for real, and they are not interchangeable. The
differences below are the point: a client that handles one correctly can still
mishandle another.

| Transport | Endpoint | What is distinct about it |
|---|---|---|
| Streamable HTTP | `/s/<name>/mcp` | Response returns in the reply to the POST. A `GET` on the same path opens the standalone listening stream, which only a client that opted in ever does. |
| Legacy HTTP+SSE | `/s/<name>/sse` | Response arrives **out-of-band** on a stream opened separately; the POST is only acknowledged with 202. The stream is also the session, so it exists before `initialize` rather than being created by it. |
| STDIO | subprocess | Spawned by the system under test, so it runs in its own process. Served by a thin bridge (see below). |

The SSE split is what makes a dropped connection mean something different
there. Under streamable HTTP a drop fails the request in flight; under SSE it
**strands** it, because the POST already succeeded and the answer has nowhere
to land. `sse_test.go` pins that a client gives up on its own deadline rather
than being told.

### The STDIO bridge

A STDIO server cannot share memory with the fleet, since the process under
test spawns it. Rather than reimplement the state there, and end up with two
tool sets and two sets of counters to keep in step, the child is deliberately
thin: it reads newline-delimited JSON-RPC from stdin, forwards each message to
the fleet over HTTP, and writes the reply to stdout. Every control verb keeps
working unchanged, and what is actually under test on this transport
(subprocess lifecycle, pipe framing, respawn after death) is exercised for
real.

```sh
mcpchaos -stdio -fleet http://127.0.0.1:8400 -server alpha
```

The child registers its process id with the fleet at startup, before any
protocol traffic, so `POST /control/<s>/stdio/kill` can kill it. That is the
one fault this transport has that cannot be simulated, and the registration
clears on kill so a later respawn is distinguishable from one that never
happened.

It registers its environment too, but only the variables named `CHAOS_*`. The
child inherits the whole environment of the process that spawned it,
credentials included, and the control API has no authentication, so nothing
else leaves the process. A tool with `reply_env` answers with one of those
values, which is how a scenario checks what environment the system under test
spawned its subprocess with.

An injected HTTP status has no equivalent on a pipe, so the bridge reports it
as a JSON-RPC error: a client should see a failed call, not a dead server. A
fleet that becomes unreachable does end the process, which is the closest
analogue to an upstream dying.

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
| `POST /control/<s>/instructions` | `{"text":"..."}` / `{"clear":true}` | set or remove the instructions served in the initialize result |
| `POST /control/<s>/faults` | `{"faults":{...}}` | set the fault state |
| `POST /control/<s>/faults/clear` | | return to healthy |
| `POST /control/<s>/auth` | `{"auth":{...}}` | set auth requirements |
| `POST /control/<s>/auth/grant` | `{"token":"...","identity":"..."}` | add a bearer token |
| `POST /control/<s>/auth/revoke` | `{"token":"..."}` | revoke one token, leaving others working |
| `POST /control/<s>/sessions/kill` | | invalidate sessions without touching the transport |
| `POST /control/<s>/streams/drop` | | close listening streams without killing sessions |
| `POST /control/<s>/notify/tools-changed` | | push `notifications/tools/list_changed` |
| `POST /control/<s>/stdio/register` | `{"pid":N,"env":{...}}` | record the bridging child process and its shared environment (the child does this itself) |
| `POST /control/<s>/stdio/kill` | | kill the bridging child process |
| `POST /control/<s>/counters/reset` | | zero the tallies, keeping live facts (OAuth counters and log too) |
| `POST /control/<s>/oauth/expire` | `{"identity":"..."}` / `{}` | that identity's live access tokens (everyone's when empty) now fail at the server |
| `POST /control/<s>/oauth/revoke` | `{"identity":"..."}` / `{"token":"..."}` / `{}` | end whole grants: access tokens fail, refresh tokens get `invalid_grant` |
| `POST /control/<s>/oauth/fail` | `{"fail":{"endpoint","grant_type","status","error","count"}}` | the next `count` matching authorization server requests fail |

Clients read instructions during the handshake, so changing them reaches only
clients that initialize afterwards.

### Tools

A tool is `{"name","description","inputSchema"}` plus optional fields. Only
`annotations` goes on the wire with the first three; the rest are instructions
to the mock, shown in the state readback and never sent to a client.

| Field | Effect |
|---|---|
| `annotations` | sent verbatim in tools/list, e.g. `{"readOnlyHint":true}` |
| `reply` | the CallToolResult tools/call returns, verbatim: several content blocks, `structuredContent`, `isError`. An empty result is `{"content":[]}`; clients reject a missing `content`. |
| `reply_env` | answer with this variable from the stdio bridge's registered environment |
| `fault` | a fault set that replaces the server-wide one for calls to this tool (see below) |

Without `reply` or `reply_env`, a call echoes `"<tool>:<arguments>"`. A
definition that would misbehave quietly is rejected with 400: annotations or a
reply that is not an object, both `reply` and `reply_env`, or a tool fault
field that cannot apply to one tool.

## OAuth

`POST /control/<s>/auth {"auth":{"oauth":{...}}}` makes a server an OAuth 2.1
protected resource with its own authorization server, so a client's whole
upstream OAuth path runs for real: discovery (RFC 9728, RFC 8414), dynamic
registration (RFC 7591), authorization with PKCE S256 (RFC 7636), code exchange
and refresh (RFC 6749, with RFC 8707 resource indicators).

| Path | Serves |
|---|---|
| `/.well-known/oauth-protected-resource/s/<s>/mcp` (or `/sse`) | protected resource metadata |
| `/.well-known/oauth-authorization-server/s/<s>/as` | authorization server metadata |
| `/s/<s>/as/register`, `/authorize`, `/token` | the authorization server |

Only the path-inserted forms exist: one fleet hosts many servers, so the root
documents are 404. Every published URL uses the host the request arrived on.

Consent is approved automatically. A test follows the authorization URL
without following redirects, reads the code from the `Location` header, and
picks who approves by adding `login_hint=<identity>` (otherwise `approve_as`,
default `user`). That identity is what calls are attributed to, so
`calls_by_identity` shows per-user isolation.

| `oauth` field | Effect |
|---|---|
| `challenge_scope` | adds `scope="..."` to the 401 challenge |
| `challenge` | `"bare"` sends a challenge without `resource_metadata` |
| `resource` | the resource the metadata publishes (default: the endpoint's URL) |
| `registration` | `false` removes dynamic registration |
| `clients` | pre-registered clients `{client_id, client_secret, redirect_uris}` |
| `approve_as` | the identity that approves without a `login_hint` |
| `deny` | every authorization ends in `access_denied` |
| `access_ttl` | access token lifetime (default `1h`) |
| `rotate_refresh` | a refresh issues a new refresh token and retires the old one |

With OAuth on, POST and GET on the MCP endpoint need an access token from this
server, or a static `bearer` token. A refusal is 401 with a plain-text body
(mcp-go reads a JSON error body as a JSON-RPC response) and
`WWW-Authenticate: Bearer resource_metadata="..."`. DELETE stays open: ending a
session needs only its id, and mcp-go sends it without credentials.

`GET /control/<s>` gains `oauth`: clients, grants with their current tokens,
counters (`prm`, `metadata`, `register`, `authorize`, `token_code`,
`token_refresh`, `token_errors`, `rejected`), queued failures, and a log of the
last 500 requests with the raw secrets, verifier result (`pkce`), resource and
tokens presented and issued. A grant's earlier access tokens stay valid until
they expire, as with a real server. Changing the configuration keeps clients
and grants, and `faults/clear` drops queued failures.

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
| Scope | `methods`, `message` |

`methods` limits the whole set to the listed JSON-RPC methods. Every other
request, and every stream opened with GET, sees a healthy server, so
`{"status":503,"methods":["tools/call"]}` fails calls without also failing the
handshake in front of them. A one-shot status is spent only by a request in
scope. `message` is the text an injected failure carries: the body of `status`
and `err_kind` http (sent as plain text, since a client may read a JSON object
as a JSON-RPC response), the JSON-RPC error of `err_kind` protocol,
`init_fail`, `ping_fail` and `list_fail`, and the text of an `err_kind` tool
result. `err_kind` needs `err_rate` above 0 to fire.

`err_kind` distinguishes failures that are not interchangeable: `tool` is a
successful exchange reporting a failed tool, `protocol` is a failed exchange,
and `http` is a bare 500 with no JSON-RPC body. A client that conflates them
looks correct under one and wrong under another.

A tool's `fault` replaces the server-wide set entirely for calls to that tool,
so one broken tool on an otherwise healthy server is expressible. It applies
once the call is known to reach the tool, after credentials and session
checks, and honours the transport, HTTP, timing and call fields. Its one-shot
status is spent without changing the tool-set revision. Fields that act on
something other than one call (`methods`, `init_fail`, `bad_version`,
`no_ping`, `ping_fail`, `list_fail`, `kill_session`, `drop_sse`) are rejected.

## Counters, and what they can honestly prove

| Counter | Proves |
|---|---|
| `calls` | keyed by the tool the server **resolved** the request to, not the name the caller sent. A call stopped in front of the tool, by an injected status, a 401 or a 404, is not counted here but is in `methods`. A client that maps a caller-facing name to the wrong upstream tool returns a correct-looking response and a wrong counter, and nothing else catches it. |
| `calls_by_identity` | that revoking one caller's credential did not affect another's |
| `sessions` | the reconnect signal for HTTP (see below) |
| `open_streams` / `max_streams` | stream-level leak detection |
| `methods` | every JSON-RPC request received, counted as it arrives, before any fault, credential or session check can answer it. A call answered with an injected status, a 401 or a 404 still counts, so retries are countable. Unimplemented methods count too, so a surprise surfaces. |
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
