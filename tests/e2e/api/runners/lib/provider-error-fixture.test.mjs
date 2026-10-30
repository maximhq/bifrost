// Tests provider-error-fixture.mjs through a running instance. Run directly:
// `node provider-error-fixture.test.mjs`.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const FIXTURE = join(dirname(fileURLToPath(import.meta.url)), "..", "provider-error-fixture.mjs");

// startFixture boots the fixture on a random port and resolves once it listens. The returned post
// sends one fixture request and, when the fixture does not answer, fails with what it printed on its
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

test("boolean-schema route fixture supplies chat and Responses in both transfer modes", async (t) => {
  const post = await startFixture(t);
  for (const path of ["/v1/chat/completions", "/v1/responses"]) {
    for (const stream of [false, true]) {
      const res = await post({ model: "upstream-ok~boolean-schema-nodes-fixture-test", stream }, path);
      assert.equal(res.status, 200, `${path} stream=${stream}`);
      if (!stream) {
        const body = await res.json();
        const text = path.endsWith("/responses")
          ? body.output.find((item) => item.type === "message").content[0].text
          : body.choices[0].message.content;
        assert.equal(text, "hello");
        continue;
      }
      assert.match(res.headers.get("content-type"), /text\/event-stream/);
      const text = await res.text();
      const events = text.split("\n").filter((line) => line.startsWith("data: "))
        .map((line) => line.slice(6)).filter((data) => data !== "[DONE]").map((data) => JSON.parse(data));
      if (path.endsWith("/responses")) {
        assert.deepEqual(events.map((event) => event.sequence_number), events.map((_, index) => index));
        assert.equal(events[0].type, "response.created");
        assert.equal(events.at(-1).type, "response.completed");
        assert.equal(events.find((event) => event.type === "response.output_text.delta").delta, "hello");
        assert.equal(events.at(-1).response.output[0].content[0].text, "hello");
      } else {
        assert.equal(events.map((event) => event.choices?.[0]?.delta?.content || "").join(""), "hello");
        assert.equal(events.at(-1).choices[0].finish_reason, "stop");
        assert.match(text, /data: \[DONE\]\n\n$/);
      }
    }
  }
});
