// Tests provider-error-fixture.mjs through a running instance. Run directly:
// `node provider-error-fixture.test.mjs`.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
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

// These are genuine failed reads with usable HTTP headers, not complete JSON errors.
test("lifecycle rejection models send auth/rate-limit statuses before a body decoding failure", async (t) => {
  const post = await startFixture(t);
  for (const [model, status, header, hint] of [
    ["body-lifecycle-reject-401", 401, null, null],
    ["body-lifecycle-reject-403-gzip", 403, null, null],
    ["body-lifecycle-reject-429", 429, "retry-after", "7"],
    ["body-lifecycle-reject-503-gzip", 503, "retry-after-ms", "2500"],
  ]) {
    const response = await post({ model, stream: true, messages: [{ role: "user", content: "hi" }] });
    assert.equal(response.status, status, model);
    if (header) assert.equal(response.headers.get(header), hint, model);
    await assert.rejects(response.text(), undefined, `${model} must fail body decoding`);
  }
});

// Execute the actual collection scripts: JSON syntax validation alone cannot catch
// exceptions that bypass fixture cleanup. Postman records pm.test failures and continues.
test("lifecycle harness scripts remove their provider even when the response is invalid", async (t) => {
  const collection = JSON.parse(readFileSync(new URL("../../collections/provider-harness.json", import.meta.url), "utf8"));
  const folder = collection.item.find((item) => item.name === "Stream body lifecycle (stream-body-lifecycle)");
  assert.equal(folder.item.length, 7);
  for (const item of folder.item) {
    const status = Number(item.name.match(/\[EXPECT-(\d+)\]/)[1]);
    const body = item.name.includes("body-lifecycle-reject-")
      ? JSON.stringify({
        is_bifrost_error: false,
        error: { message: "failed to decode response body", error: "unexpected EOF" },
        extra_fields: { retry_after_ms: status === 429 ? 7000 : status === 503 ? 2500 : 0 },
      })
      : status === 504 ? "request timed out" : status === 500 ? "stream idle timeout" : "hello fixture cleanup error";
    const script = item.event.find((event) => event.listen === "test").script.exec.join("\n");
    for (const scenario of [
      { name: "expected response", body, status, deleteStatus: 200, passes: true },
      { name: "plain-text gateway failure", body: "Bad Gateway", status: 502, deleteStatus: 200 },
      { name: "truncated JSON", body: '{"error":', status, deleteStatus: 200 },
      { name: "JSON null", body: "null", status, deleteStatus: 200 },
      { name: "unexpected JSON shape", body: "{}", status, deleteStatus: 200 },
      { name: "wrong status with expected body", body, status: 502, deleteStatus: 200 },
      { name: "cleanup failure", body, status, deleteStatus: 503 },
    ]) {
      await t.test(`${item.name}: ${scenario.name}`, () => {
        const failures = [];
        const requests = [];
        const assertions = [];
        const pm = {
          response: {
            code: scenario.status,
            responseTime: 100,
            text: () => scenario.body,
            json: () => JSON.parse(scenario.body),
          },
          variables: { get: (key) => ({ baseUrl: "http://gateway.test", bodyLifecycleProvider: "fixture/provider" })[key] },
          test: (name, fn) => {
            assertions.push(name);
            try { fn(); } catch (error) { failures.push({ name, error }); }
          },
          expect: (actual, message) => ({ to: {
            eql: (expected) => assert.deepEqual(actual, expected, message),
            include: (expected) => assert.ok(actual.includes(expected), message),
            not: { include: (expected) => assert.ok(!actual.includes(expected), message) },
            be: { below: (expected) => assert.ok(actual < expected, message) },
          } }),
          sendRequest: (request, callback) => {
            requests.push(request);
            callback(null, { code: scenario.deleteStatus, text: () => "cleanup response" });
          },
        };
        let scriptError;
        try { new Function("pm", script)(pm); } catch (error) { scriptError = error; }
        assert.equal(requests.length, 1, "cleanup must run exactly once even when parsing or assertions fail");
        assert.equal(requests[0].method, "DELETE");
        assert.equal(requests[0].url, "http://gateway.test/api/providers/fixture%2Fprovider");
        assert.equal(scriptError, undefined, "response errors must be reported as failed assertions");
        assert.ok(assertions.includes("fixture provider removed"));
        assert.ok(assertions.includes("original upstream HTTP rejection") || assertions.includes("exact gateway status"));
        if (scenario.passes) assert.deepEqual(failures, []);
        else assert.ok(failures.length > 0, "an invalid response or failed cleanup must fail the case");
        if (scenario.deleteStatus !== 200) assert.ok(failures.some(({ name }) => name === "fixture provider removed"));
      });
    }
  }
});
