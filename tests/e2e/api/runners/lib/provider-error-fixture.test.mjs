// Tests provider-error-fixture.mjs through a running instance. Run directly:
// `node provider-error-fixture.test.mjs`.
import assert from "node:assert/strict";
import { execFile, spawn } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const FIXTURE = join(dirname(fileURLToPath(import.meta.url)), "..", "provider-error-fixture.mjs");
const execFileAsync = promisify(execFile);

// startFixture boots the fixture on a random port and resolves once it listens. The returned post
// sends one chat completion and, when the fixture does not answer, fails with what it printed on its
// way out.
async function startFixture(t) {
  const port = 20000 + Math.floor(Math.random() * 20000);
  const child = spawn(process.execPath, [FIXTURE], {
    env: { ...process.env, PROVIDER_ERROR_FIXTURE_PORT: String(port) },
    stdio: ["ignore", "pipe", "pipe"],
  });
  t.after(() => child.kill());
  let output = "";
  child.stderr.on("data", (c) => (output += c));
  await new Promise((resolve, reject) => {
    child.stdout.on("data", (c) => {
      output += c;
      if (output.includes("listening")) resolve();
    });
    child.on("exit", (code) => reject(new Error(`the fixture exited (${code}) before it listened: ${output}`)));
  });
  const post = async (body, path = "/v1/chat/completions", headers = {}) => {
    try {
      return await fetch(`http://127.0.0.1:${port}${path}`, {
        method: "POST",
        headers: { "Content-Type": "application/json", ...headers },
        body: JSON.stringify(body),
      });
    } catch (err) {
      assert.fail(`the fixture stopped answering (${err.cause?.code || err.message}): ${output}`);
    }
  };
  return post;
}

// The request's model picks the fixture's behaviour. A model that is not a string names none, so it
// gets the unknown-model answer; it must not stop the one fixture every folder-185 request shares.
test("a model that is not a string is an unknown model and the fixture keeps serving", async (t) => {
  const post = await startFixture(t);
  for (const model of [1, true, { name: "upstream-ok" }, ["upstream-ok"]]) {
    const res = await post({ model, messages: [{ role: "user", content: "hi" }] });
    assert.equal(res.status, 404, `model ${JSON.stringify(model)}`);
    assert.equal((await res.json()).error.code, "model_not_found", `model ${JSON.stringify(model)}`);
  }
  const ok = await post({ model: "upstream-ok", messages: [{ role: "user", content: "hi" }] });
  assert.equal(ok.status, 200, "a valid request after the malformed ones");
});

test("Gemini embedding fixtures require the selected key and keep per-input witnesses", async (t) => {
  const post = await startFixture(t);
  const path = "/models/gemini-embedding-001:batchEmbedContents";
  const body = (text) => ({ requests: [{ content: { parts: [{ text }] } }] });
  const headers = { "x-goog-api-key": "test-key" };
  for (const selected of [{}, { "x-goog-api-key": "wrong-key" }]) {
    const refused = await post(body("fixture-empty-embedding-usage:isolated"), path, selected);
    assert.equal(refused.status, 401);
  }
  for (const malformedBody of [null, {}, [], { requests: [] }]) {
    const malformed = await post(malformedBody, path, headers);
    assert.equal(malformed.status, 400);
  }
  const measured = await post(body("fixture-empty-embedding-usage:isolated"), path, headers);
  assert.equal(measured.status, 200);
  assert.deepEqual(await measured.json(), { embeddings: [], usageMetadata: { promptTokenCount: 12, totalTokenCount: 12 } });
  const unmeasured = await post(body("fixture-unmeasured-empty-embedding:isolated"), path, headers);
  assert.equal(unmeasured.status, 200);
  assert.deepEqual(await unmeasured.json(), { embeddings: [] });
  const valid = await post(body("fixture-valid-embedding:isolated"), path, headers);
  assert.equal(valid.status, 200);
  assert.deepEqual((await valid.json()).embeddings, [{ values: [0.1, 0.2] }]);
  const witness = await fetch(new URL("/__hits", measured.url));
  assert.equal(witness.status, 200);
  assert.deepEqual((await witness.json()).data[0].hits, {
    "embedding:fixture-empty-embedding-usage:isolated": 1,
    "embedding:fixture-unmeasured-empty-embedding:isolated": 1,
    "embedding:fixture-valid-embedding:isolated": 1,
  });
});

test("Vertex embedding fixture rejects null before serving the next valid request", { timeout: 20000 }, async (t) => {
  const directory = mkdtempSync(join(tmpdir(), "bifrost-vertex-embedding-fixture-"));
  const port = 20000 + Math.floor(Math.random() * 20000);
  const fixture = join(dirname(FIXTURE), "vertex-endpoint-fixture.mjs");
  const child = spawn(process.execPath, [fixture, "--app-dir", directory, "--port", String(port)], { stdio: ["ignore", "pipe", "pipe"] });
  t.after(() => { child.kill(); rmSync(directory, { recursive: true, force: true }); });
  let output = "";
  child.stderr.on("data", (chunk) => (output += chunk));
  await new Promise((resolve, reject) => {
    child.stdout.on("data", (chunk) => { output += chunk; if (output.includes("listening")) resolve(); });
    child.on("exit", (code) => reject(new Error(`Vertex fixture exited (${code}): ${output}`)));
  });
  const config = JSON.parse(readFileSync(join(directory, "config.json"), "utf8"));
  const ca = join(directory, "fixture-ca.pem");
  writeFileSync(ca, config.providers.vertex.proxy_config.ca_cert_pem);
  const endpoint = "https://us-central1-aiplatform.googleapis.com/v1/projects/fixture-project/locations/us-central1/publishers/google/models/gemini-embedding-2-preview:embedContent?key=test-key";
  const post = async (body) => {
    // Force every request through the local intercept proxy, even with NO_PROXY set.
    const { stdout } = await execFileAsync("curl", ["--silent", "--show-error", "--max-time", "5", "--proxy", `http://127.0.0.1:${port}`, "--noproxy", "", "--cacert", ca, "--header", "Content-Type: application/json", "--data-binary", JSON.stringify(body), "--write-out", "\n%{http_code}", endpoint]);
    const split = stdout.lastIndexOf("\n");
    return { status: Number(stdout.slice(split + 1)), body: JSON.parse(stdout.slice(0, split)) };
  };
  for (const malformed of [null, {}, []]) {
    const response = await post(malformed);
    assert.equal(response.status, 400);
    assert.equal(response.body.error.status, "INVALID_ARGUMENT");
  }
  const response = await post({ content: { parts: [{ text: "fixture-empty-embedding-usage:after-null" }] } });
  assert.equal(response.status, 200);
  assert.deepEqual(response.body.usageMetadata, { promptTokenCount: 12, totalTokenCount: 12 });
  const witness = await fetch(`http://127.0.0.1:${port}/__connects`);
  assert.equal(witness.status, 200);
  assert.equal((await witness.json()).data[0].requests.length, 4);
});

test("filtered Live create checks accounting events and unchanged command permissions", async (t) => {
  const post = await startFixture(t);
  const top = ["session.started", "session.updated", "session.closed", "session.usage.updated", "session.delegation.created", "session.input_transcript.delta", "session.output_transcript.delta", "error"];
  const nested = ["response.output_item.done", "response.completed", "response.incomplete", "response.failed"];
  const required = [...top.map((type) => ({ type })), ...nested.map((type) => ({ type: "response.event", response_event: type }))];
  const create = (events, commands = ["session.close"]) => post({
    session: {
      model: "gpt-live-1",
      instructions: "filtered-live-create-test",
      client: { data_channel: { allowed_server_events: events, allowed_client_events: commands } },
    },
    transport: { type: "webrtc", sdp: "offer" },
  }, "/v1/live/sessions");
  for (const events of [[], required.filter((event) => event.response_event !== "response.completed")]) {
    const response = await create(events);
    assert.equal(response.status, 400);
    assert.equal((await response.json()).error.code, "missing_gateway_events");
  }
  const changed = await create(required, "all");
  assert.equal(changed.status, 400);
  assert.equal((await changed.json()).error.code, "changed_client_permissions");
  const complete = await create(required);
  assert.equal(complete.status, 201);
  assert.equal((await complete.json()).transport.sdp, "invalid SDP answer");
});
