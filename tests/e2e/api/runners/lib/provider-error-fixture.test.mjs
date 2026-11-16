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

// store-false-required and store-true-required answer only a request carrying exactly that store, on
// Azure's /openai/v1 routes as on the direct ones, so a harness case can tell which store the gateway
// forwarded from the status alone.
test("store-required models refuse any other store with a 400 naming what arrived", async (t) => {
  const post = await startFixture(t);
  const cases = [
    ["store-false-required", true, "/openai/v1/responses"],
    ["store-false-required", undefined, "/openai/v1/responses"],
    ["store-false-required", "false", "/v1/responses"],
    ["store-true-required", false, "/openai/v1/chat/completions"],
    ["store-true-required", undefined, "/v1/chat/completions"],
  ];
  for (const [model, store, path] of cases) {
    const res = await post({ model, store, input: "hi", messages: [{ role: "user", content: "hi" }] }, path);
    const label = `${model} store=${JSON.stringify(store)} on ${path}`;
    assert.equal(res.status, 400, label);
    const err = (await res.json()).error;
    assert.equal(err.code, "unexpected_store", label);
    assert.ok(err.message.includes(`got ${JSON.stringify(store)}`), `${label}: ${err.message}`);
  }
});

test("store-required models answer the matching store, streamed or not", async (t) => {
  const post = await startFixture(t);
  const unary = await post({ model: "store-false-required", store: false, input: "hi" }, "/openai/v1/responses");
  assert.equal(unary.status, 200);
  assert.equal((await unary.json()).status, "completed");

  const streamed = await post({ model: "store-false-required", store: false, stream: true, input: "hi" }, "/openai/v1/responses");
  assert.equal(streamed.status, 200);
  assert.match(streamed.headers.get("content-type"), /text\/event-stream/);
  const events = (await streamed.text()).split("\n\n").filter((l) => l.startsWith("data: {")).map((l) => JSON.parse(l.slice(6)));
  assert.equal(events[0].type, "response.created");
  assert.ok(events.some((e) => e.type === "response.output_text.delta" && e.delta === "hello"), "the text arrives as a delta");
  const last = events.at(-1);
  assert.equal(last.type, "response.completed");
  assert.equal(last.response.usage.total_tokens, 15);
  assert.deepEqual(events.map((e) => e.sequence_number), events.map((_, i) => i));

  const chat = await post({ model: "store-true-required", store: true, messages: [{ role: "user", content: "hi" }] }, "/openai/v1/chat/completions");
  assert.equal(chat.status, 200);
  assert.equal((await chat.json()).choices[0].message.content, "hello");
});
