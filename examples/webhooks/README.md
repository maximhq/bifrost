# Async Webhooks Receiver

A minimal, dependency-free Go receiver for Bifrost async inference webhooks. Its
only job is to demonstrate the one thing every receiver must get right:
**verifying the signature before trusting a delivery.**

When an async inference job reaches a terminal state, Bifrost POSTs a signed
event to every subscribed endpoint. The signature proves the request came from
your Bifrost instance and that the body was not altered in transit.

## What Bifrost sends

Each delivery is a `POST` with a JSON body and three signing headers:

| Header              | Meaning                                                         |
| ------------------- | -------------------------------------------------------------- |
| `webhook-id`        | Unique id for this delivery — also the **dedupe key**.         |
| `webhook-timestamp` | Unix seconds the payload was signed at.                        |
| `webhook-signature` | Space-separated list of `v1,<base64>` signatures.              |

The body looks like:

```json
{
  "event": "async_job.completed",
  "created_at": "2026-07-16T10:00:00Z",
  "data": {
    "job_id": "job_abc123",
    "request_type": "chat_completion",
    "status": "completed",
    "status_code": 200,
    "result_url": "/v1/async/chat/completions/job_abc123",
    "result_expires_at": "2026-07-17T10:00:00Z"
  }
}
```

Events are `async_job.completed` and `async_job.failed`. The `data` fields
beyond `job_id` and `status` are best-effort:

- `result_url` — GET this (through Bifrost, with your auth) to fetch the full
  result. Valid until `result_expires_at`.
- `response` — the full response, inlined **only** if the endpoint opted in and
  it fits the size limit.
- `response_omitted: true` — the response was too large to inline; fetch it via
  `result_url` instead.
- `result_expired: true` — the job's result was already gone when this delivery
  fired. The outcome is known, but there is nothing left to fetch.

## Verifying deliveries

The signature is HMAC-SHA256 over the exact bytes `{id}.{timestamp}.{body}`,
keyed with your endpoint's signing secret, encoded as `v1,<base64>`. To verify:

1. Recompute the HMAC from the secret and the received `id`, `timestamp`, and
   raw body, and compare it (in constant time) against every candidate in the
   `webhook-signature` header. Accept if **any** matches — the header can carry
   more than one signature during secret rotation.
2. Reject if `webhook-timestamp` is outside a tolerance window (this example
   uses 5 minutes) to blunt replay attacks.
3. **Dedupe on `webhook-id`.** Delivery is at-least-once: retries reuse the same
   id, so you can receive the same event more than once.

The secret (`whsec_...`) is shown **once** when you create the endpoint, and can
only be changed by rotating it. Store it somewhere your receiver can read it;
never hard-code it.

See [`main.go`](./main.go) for the full implementation — `verify` and `sign`
are ~40 lines of standard library.

## Requiring custom headers

Bifrost endpoints can be configured to send custom headers with every delivery
(for example an `Authorization` value). The signature alone already proves
authenticity, but checking such headers is cheap defense-in-depth: it rejects
unwanted traffic before any crypto runs.

Set `REQUIRED_HEADERS` to comma-separated `Name=Value` pairs matching the
headers configured on the endpoint:

```bash
WEBHOOK_SECRET=whsec_... REQUIRED_HEADERS='Authorization=Bearer s3cret,X-Env=prod' go run .
```

Deliveries missing any pair — or carrying a different value — are rejected with
a generic `401` before signature verification. Values are compared in constant
time, since they are often bearer credentials. Values may contain `=` but not
`,`.

## Run it

```bash
WEBHOOK_SECRET=whsec_your_secret_here go run .
```

The receiver listens on `:8080` (override with `ADDR`) and accepts deliveries at
`POST /webhook`. Point a Bifrost webhook endpoint at `http://<host>:8080/webhook`
and complete a job to see verified deliveries logged.

> Plain `http://` endpoints are only accepted by Bifrost when the endpoint has
> `allow_private_network` set. Use `https://` in production.

## Test

```bash
go test ./...
```

The tests pin the canonical Standard Webhooks reference vector — the same one
Bifrost's own signer pins — so a passing run proves this receiver verifies
byte-for-byte what Bifrost signs.
