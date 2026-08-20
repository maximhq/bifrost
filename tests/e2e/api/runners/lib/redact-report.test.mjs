// Unit tests for published-report redaction. Run directly: `node redact-report.test.mjs`.
import assert from "node:assert";
import { redactItemsForPublic, REDACTED } from "./redact-report.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

// Every one of these is a live credential shape that the provider harness
// genuinely puts on the wire.
const SECRETS = [
  "sk-proj-abcdefghijklmnop1234",
  "AKIAIOSFODNN7EXAMPLE",
  "AIzaSyA1234567890abcdefghijklmnopqrstu",
  "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NSJ9.dBjftJeZ4CVPmB92K27uhbUJU1p1r3wa",
];

const item = () => ({
  idx: 0,
  name: "openai/gpt-4o-mini",
  folder: "8. Criss-Cross",
  method: "POST",
  url: `https://gw/genai/v1beta/models/x:generateContent?key=${SECRETS[2]}&alt=sse`,
  reqHeaders: [
    { key: "Authorization", value: `Bearer ${SECRETS[0]}`, disabled: false },
    { key: "x-api-key", value: SECRETS[0], disabled: false },
    { key: "Content-Type", value: "application/json", disabled: false },
  ],
  reqBody: `{"model":"gpt-4o-mini","auth":"Bearer ${SECRETS[0]}","aws":"${SECRETS[1]}"}`,
  respHeaders: [
    { key: "Set-Cookie", value: "session=abc123; HttpOnly" },
    { key: "Content-Type", value: "application/json" },
  ],
  respBody: `{"error":{"message":"Incorrect API key provided: ${SECRETS[0]}","trace":"${SECRETS[3]}"}}`,
  assertions: [{ name: "status is 200", passed: true, error: null }],
  failed: false,
});

// The whole point: whatever else changes, no credential may survive into the
// string that gets inlined into a world-readable page.
test("no credential survives serialization of the public report", () => {
  const serialized = JSON.stringify(redactItemsForPublic([item()]));
  for (const secret of SECRETS) {
    assert.ok(!serialized.includes(secret), `published report still contains ${secret.slice(0, 12)}...`);
  }
  assert.ok(!serialized.includes("session=abc123"), "published report still contains a session cookie");
});

// A report that hides what ran is not worth publishing, so redaction has to be
// surgical rather than wholesale.
test("the report stays legible after redaction", () => {
  const [out] = redactItemsForPublic([item()]);
  assert.strictEqual(out.name, "openai/gpt-4o-mini");
  assert.ok(out.url.startsWith("https://gw/genai/v1beta/models/x:generateContent"), out.url);
  assert.ok(out.url.includes("alt=sse"), "non-secret query parameters must survive");
  assert.ok(out.url.includes(`key=${REDACTED}`), out.url);
  // Header NAMES stay, so the report still shows what was sent.
  assert.deepStrictEqual(
    out.reqHeaders.map((h) => h.key),
    ["Authorization", "x-api-key", "Content-Type"]
  );
  assert.strictEqual(out.reqHeaders[2].value, "application/json");
  assert.strictEqual(out.reqHeaders[0].value, REDACTED);
  assert.ok(out.respBody.includes("Incorrect API key provided"), "error text must survive redaction");
  assert.deepStrictEqual(out.assertions, [{ name: "status is 200", passed: true, error: null }]);
});

test("the original items are not mutated", () => {
  const original = item();
  redactItemsForPublic([original]);
  assert.strictEqual(original.reqHeaders[0].value, `Bearer ${SECRETS[0]}`);
});

test("missing fields do not throw", () => {
  const [out] = redactItemsForPublic([{ idx: 0, name: "x" }]);
  assert.strictEqual(out.url, "");
  assert.deepStrictEqual(out.reqHeaders, []);
  assert.strictEqual(out.reqBody, "");
});

// A percent-encoded parameter name is the same parameter. Comparing the raw
// name means `api%5Fkey=...` sails past the SECRET_PARAMS check and its value is
// published verbatim.
test("percent-encoded parameter names are still redacted", () => {
  const [out] = redactItemsForPublic([
    { idx: 0, name: "x", url: `https://gw/v1/chat?api%5Fkey=${SECRETS[0]}&alt=sse` },
  ]);
  assert.ok(!out.url.includes(SECRETS[0]), `encoded param name leaked its value: ${out.url}`);
  assert.ok(out.url.includes("alt=sse"), "unrelated parameters must survive");
});

// Credentials also travel in URL userinfo, which has no query string at all - so
// the whole query-parameter path never sees them.
test("URL userinfo credentials are redacted", () => {
  const [out] = redactItemsForPublic([
    { idx: 0, name: "x", url: "https://svcuser:s3cr3t-token@gateway.example/v1/chat" },
  ]);
  assert.ok(!out.url.includes("s3cr3t-token"), `userinfo credential leaked: ${out.url}`);
  assert.ok(out.url.includes("gateway.example"), "the host must stay legible");
});

// The credential is just as often the USERNAME with no password at all - that is
// how token-in-URL auth is normally written. Preserving the username "because it
// is only a name" publishes the key.
test("a credential in the username position is redacted", () => {
  const [out] = redactItemsForPublic([
    { idx: 0, name: "x", url: "https://sk-proj-abcdefghijklmnop1234@gateway.example/v1/chat" },
  ]);
  assert.ok(!out.url.includes("sk-proj-abcdefghijklmnop1234"), `username credential leaked: ${out.url}`);
  assert.ok(out.url.includes("gateway.example"), "the host must stay legible");
});

// A signed URL is just as live inside a body as it is in the request line -
// providers return presigned S3/GCS links in response payloads routinely. The
// signature value is deliberately chosen NOT to match any BODY_SECRET_PATTERNS
// entry, so this passes only if the URL itself is redacted rather than caught by a
// credential-shape regex.
test("a signed URL embedded in a body has its credentials redacted", () => {
  const signature = "9f8e7d6c5b4a39281706abcdef01234567";
  const body = JSON.stringify({
    message: "download ready",
    url: `https://bucket.s3.amazonaws.com/report.json?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Signature=${signature}&response-content-type=application%2Fjson`,
  });
  const [out] = redactItemsForPublic([{ idx: 0, name: "x", respBody: body }]);
  assert.ok(!out.respBody.includes(signature), `signed URL leaked from a body: ${out.respBody}`);
  assert.ok(out.respBody.includes("bucket.s3.amazonaws.com"), "the host must stay legible");
  assert.ok(
    out.respBody.includes("X-Amz-Algorithm=AWS4-HMAC-SHA256"),
    "non-credential parameters must survive"
  );
});

// `\/` is a legal JSON escape for `/`, and plenty of serializers emit it, so the same
// signed URL can arrive in a body spelled https:\/\/... A matcher that only knows the
// literal form skips it entirely - and a hex signature matches none of the
// credential-shape patterns, so nothing else catches it either.
test("a slash-escaped signed URL in a body is redacted", () => {
  const signature = "0011223344556677889900aabbccddeeff";
  const body = `{"url":"https:\\/\\/bucket.s3.amazonaws.com\\/report.json?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Signature=${signature}"}`;
  const [out] = redactItemsForPublic([{ idx: 0, name: "x", respBody: body }]);
  assert.ok(!out.respBody.includes(signature), `slash-escaped signed URL leaked: ${out.respBody}`);
  assert.ok(out.respBody.includes("bucket.s3.amazonaws.com"), "the host must stay legible");
  assert.ok(out.respBody.includes("X-Amz-Algorithm=AWS4-HMAC-SHA256"), "non-credentials must survive");
  // The body has to stay parseable - redaction must not strip the JSON escaping.
  assert.doesNotThrow(() => JSON.parse(out.respBody), "redacted body must remain valid JSON");
});

// Redacting URLs inside bodies must not damage ordinary prose or plain links.
test("a body without credentials is left intact", () => {
  const body = JSON.stringify({ docs: "https://docs.example.com/guide?page=2", note: "no secrets here" });
  const [out] = redactItemsForPublic([{ idx: 0, name: "x", reqBody: body }]);
  assert.strictEqual(out.reqBody, body, "a credential-free body must be unchanged");
});

// A fragment carries credentials too - the implicit-flow OAuth response puts the
// token there by design - and a URL whose credential lives in the fragment often
// has no query string at all, so the query-parameter path never runs.
test("a credential in the fragment is redacted when there is no query string", () => {
  const [out] = redactItemsForPublic([
    { idx: 0, name: "x", url: "https://gateway.example/callback#access_token=s3cr3t-token&state=xyz" },
  ]);
  assert.ok(!out.url.includes("s3cr3t-token"), `fragment credential leaked: ${out.url}`);
  assert.ok(out.url.includes(`access_token=${REDACTED}`), out.url);
  assert.ok(out.url.includes("state=xyz"), "non-credential fragment parameters must survive");
  assert.ok(out.url.startsWith("https://gateway.example/callback"), "the endpoint must stay legible");
});

// The query string does not shield the fragment: splitting on "?" alone leaves
// "a=1#access_token=..." as a single pair named "a", which matches nothing in
// SECRET_PARAMS and publishes the token untouched.
test("a credential in the fragment is redacted when a query string is present", () => {
  const [out] = redactItemsForPublic([
    { idx: 0, name: "x", url: "https://gateway.example/callback?alt=sse#access_token=s3cr3t-token" },
  ]);
  assert.ok(!out.url.includes("s3cr3t-token"), `fragment credential leaked past the query: ${out.url}`);
  assert.ok(out.url.includes("alt=sse"), "unrelated query parameters must survive");
});

console.log(`\n${passed} passed`);
