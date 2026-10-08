# Client-supplied Gemini tool signature recorder

Folder 184 pins #8149 P1: a native Chat assistant tool call containing
`extra_content.google.thought_signature` must retain those opaque bytes in the
outgoing Gemini `functionCall` part. Unary and streaming rows cover both Gemini and
the shared Vertex Chat conversion. Two controls retain the existing encoded call-ID
signature and unsigned replay sentinel behavior.

The signature example comes from the report. Its validity for Google is unverified;
it is used only as an opaque serialization fixture. The local upstream always
answers with canned text independently of the signature. Each row reads the
recorder and compares the outgoing `thoughtSignature` exactly, so status or a
plausible answer cannot hide a dropped signature. No signature-validator policy,
model performance, Responses conversion, or live inference is tested here.

The fixture reuses the local TLS-intercepting proxy. Both provider endpoints and
startup datasheets terminate locally; the keys are throwaway strings and Vertex
uses its API-key path, so OAuth is unnecessary. The proxy never forwards traffic to
Google. Rows skip unless `clientThoughtSignatureFixture=1`; opted-in rows require
explicit loopback recorder and gateway URLs and a successful recorder reset. Use
the isolated profile below, which contains only Gemini and Vertex fixture keys.

Run from a complete checkout with Go 1.27.0, Node.js and `openssl` available. Verify
that ports 8810 and 8811 are free before starting:

```bash
lsof -nP -iTCP:8810 -sTCP:LISTEN
lsof -nP -iTCP:8811 -sTCP:LISTEN
```

Start the fixture in one terminal:

```bash
client_signature_dir=${TMPDIR:-/tmp}/bifrost-client-signature-fixture
mkdir -p "$client_signature_dir"
node tests/e2e/api/runners/gemini-client-signature-fixture.mjs \
  --app-dir "$client_signature_dir" --port 8810
```

Start the gateway from this checkout in a second terminal. The environment proxy
tripwire rejects accidental calls that do not use the configured local fixture:

```bash
client_signature_dir=${TMPDIR:-/tmp}/bifrost-client-signature-fixture
HTTPS_PROXY=http://127.0.0.1:9 HTTP_PROXY=http://127.0.0.1:9 \
  make dev PORT=8811 HOST=127.0.0.1 APP_DIR="$client_signature_dir"
```

Run all six rows sequentially in a third terminal because each resets the same
recorder. The health and recorder checks must succeed before entering the harness
target, which reuses the gateway started above. `COMPAT=off` keeps native Chat on the
path this regression covers:

```bash
client_signature_dir=${TMPDIR:-/tmp}/bifrost-client-signature-fixture
curl -f http://127.0.0.1:8811/health && curl -f http://127.0.0.1:8810/__connects >/dev/null && make run-provider-harness-test FEATURE="client-thought-signature-fixture" COMPAT=off BASE_URL=http://127.0.0.1:8811 ENV_FILE="$client_signature_dir/environment.json" PARALLEL=0 SKIP_STREAM_CANCEL=1 RETRY_429=0 DB_VERIFY=0
```

The expected red on unchanged source is the recorded `thoughtSignature` being
`skip_thought_signature_validator` for the four extra-content rows, despite an HTTP
200 canned response. After the fix those four rows require the supplied base64
string. The two control rows pass in both versions. This is local gateway/recorder
verification, not a paid provider run. Stop the fixture and isolated gateway with
Ctrl+C after testing.

For offline structural validation only:

```bash
node tests/e2e/api/runners/augment-provider-harness.mjs \
  --source tests/e2e/api/collections/provider-harness.json --out /tmp/signature-augmented.json
node tests/e2e/api/runners/filter-collection.mjs \
  --source /tmp/signature-augmented.json --out /tmp/signature-filtered.json \
  --feature client-thought-signature-fixture
```

These two commands do not start the gateway, fixture, Newman or a provider request.
