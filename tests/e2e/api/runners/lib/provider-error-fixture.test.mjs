// Tests provider-error-fixture.mjs through a running instance. Run directly:
// `node provider-error-fixture.test.mjs`.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const FIXTURE = join(dirname(fileURLToPath(import.meta.url)), "..", "provider-error-fixture.mjs");

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
  const post = async (body, path = "/v1/chat/completions") => {
    try {
      return await fetch(`http://127.0.0.1:${port}${path}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
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

test("usage fixtures preserve ordered payloads and missing versus zero receipts", async (t) => {
  const post = await startFixture(t);
  for (const [model, expected] of [
    ["upstream-usage-missing", undefined],
    ["upstream-usage-zero", { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 }],
    ["upstream-ok", { prompt_tokens: 10, completion_tokens: 1, total_tokens: 11 }],
  ]) {
    const response = await post({ model, messages: [{ role: "user", content: "hi" }] });
    assert.equal(response.status, 200);
    const completion = await response.json();
    assert.deepEqual(Object.keys(completion), ["id", "object", "created", "model", "choices", ...(expected ? ["usage"] : [])]);
    assert.equal(completion.model, model);
    assert.deepEqual(completion.usage, expected);
    assert.equal(completion.choices[0].message.content, "hello");

    const streamed = await post({ model, messages: [{ role: "user", content: "hi" }], stream: true, stream_options: { include_usage: true } });
    assert.equal(streamed.status, 200);
    const raw = await streamed.text();
    assert.ok(raw.endsWith("data: [DONE]\n\n"));
    const receipts = raw.split(/\r?\n\r?\n/).flatMap((frame) => {
      const data = frame.startsWith("data:") ? frame.slice(5).trim() : "";
      if (!data || data === "[DONE]") return [];
      const chunk = JSON.parse(data);
      return chunk.usage === undefined ? [] : [chunk.usage];
    });
    assert.deepEqual(receipts, expected ? [expected] : []);
  }
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
