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
  const post = async (body, path = "/v1/chat/completions", signal) => {
    try {
      return await fetch(`http://127.0.0.1:${port}${path}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
        signal,
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
  for (const [model, status, header, hint, readErrors] of [
    ["body-lifecycle-reject-401", 401, null, null, ["UND_ERR_SOCKET"]],
    ["body-lifecycle-reject-403-gzip", 403, null, null, ["Z_DATA_ERROR"]],
    ["body-lifecycle-reject-429", 429, "retry-after", "7", ["UND_ERR_SOCKET"]],
    ["body-lifecycle-reject-503-gzip", 503, "retry-after-ms", "2500", ["UND_ERR_SOCKET", "Z_BUF_ERROR"]],
  ]) {
    for (const path of ["/v1/chat/completions", "/v1/responses"]) {
      const deadline = AbortSignal.timeout(3000);
      const response = await post({ model, stream: true, messages: [{ role: "user", content: "hi" }] }, path, deadline);
      assert.equal(response.status, status, model);
      if (header) assert.equal(response.headers.get(header), hint, model);
      if (status === 429) assert.equal(response.headers.get("transfer-encoding"), "chunked", model);
      await assert.rejects(response.text(), (err) => !deadline.aborted && readErrors.includes(err.cause?.code),
        `${path} must fail body decoding for ${model}`);
    }
  }
});

// Execute the actual collection scripts: JSON syntax validation alone cannot catch
// exceptions that bypass fixture cleanup. Postman records pm.test failures and continues.
test("lifecycle harness skips disabled requests and cleans up enabled providers", async (t) => {
  const collection = JSON.parse(readFileSync(new URL("../../collections/provider-harness.json", import.meta.url), "utf8"));
  const folder = collection.item.find((item) => item.name.includes("Stream body lifecycle (stream-body-lifecycle)"));
  assert.equal(folder.item.length, 8);
  await t.test("skips before setting up a provider unless the fixture is enabled", () => {
    const event = folder.event.find((event) => event.listen === "prerequest");
    const setup = new Function("pm", event.script.exec.join("\n"));
    for (const variables of [
      new Map(),
      new Map([["providerErrorFixture", "0"]]),
      new Map([["providerErrorFixtureUrl", "http://fixture.test"]]),
    ]) {
      let skipped = false;
      setup({
        variables,
        execution: { skipRequest: () => { skipped = true; } },
        sendRequest: () => assert.fail("disabled fixture must not create a provider"),
      });
      assert.ok(skipped);
      assert.ok(!variables.has("bodyLifecycleProvider"));
    }
  });
  for (const item of folder.item) {
    const status = Number(item.name.match(/\[EXPECT-(\d+)\]/)[1]);
    const body = item.name.includes("body-lifecycle-large-unary")
      ? JSON.stringify({ choices: [{ message: { content: "x".repeat(2 * 1024 * 1024 - 4) + "TAIL" } }] })
      : item.name.includes("body-lifecycle-reject-")
      ? JSON.stringify({
        is_bifrost_error: false,
        error: { message: "failed to decode response body", error: "unexpected EOF" },
        extra_fields: { retry_after_ms: status === 429 ? 7000 : status === 503 ? 2500 : 0 },
      })
      : status === 504 ? "request timed out" : status === 500 ? "stream idle timeout" : "hello fixture cleanup error";
    const script = (phase) => [...(folder.event || []), ...(item.event || [])]
      .filter((event) => event.listen === phase).map((event) => event.script.exec.join("\n")).join("\n");
    for (const scenario of [
      { name: "expected response", body, status, deleteStatus: 200, passes: true },
      { name: "custom fixture URL", body, status, deleteStatus: 200, passes: true, fixtureUrl: "http://fixture.test:9876" },
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
        const variables = new Map([["baseUrl", "http://gateway.test"], ["providerErrorFixture", "1"]]);
        if (scenario.fixtureUrl) variables.set("providerErrorFixtureUrl", scenario.fixtureUrl);
        const pm = {
          response: {
            code: scenario.status,
            responseTime: 100,
            text: () => scenario.body,
            json: () => JSON.parse(scenario.body),
          },
          variables,
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
            if (requests.length === 1) {
              assert.equal(request.method, "POST");
              assert.equal(request.url, "http://gateway.test/api/providers");
            }
            callback(null, { code: requests.length === 1 ? 200 : scenario.deleteStatus, text: () => "fixture response" });
          },
        };
        new Function("pm", script("prerequest"))(pm);
        assert.equal(requests.length, 1, "create one fixture provider per request");
        const config = JSON.parse(requests[0].body.raw);
        assert.equal(config.network_config.base_url, scenario.fixtureUrl ?? "http://127.0.0.1:8791");
        const provider = config.provider;
        const request = JSON.parse(item.request.body.raw.replace(/\{\{(\w+)\}\}/g, (_, key) => variables.get(key)));
        assert.equal(request.model, provider + "/" + item.name.split(" ")[0].slice("openai/".length));
        let scriptError;
        try { new Function("pm", script("test"))(pm); } catch (error) { scriptError = error; }
        assert.equal(requests.length, 2, "cleanup must run exactly once even when parsing or assertions fail");
        assert.equal(requests[1].method, "DELETE");
        assert.equal(requests[1].url, "http://gateway.test/api/providers/" + encodeURIComponent(provider));
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

// The response status alone passed before the fix; the OAuth exchange count must catch the stale cache.
test("Vertex lifecycle harness detects a retained rejected token source", async (t) => {
  const collection = JSON.parse(readFileSync(new URL("../../collections/provider-harness.json", import.meta.url), "utf8"));
  const folder = collection.item.find((item) => item.name.includes("Vertex error-body credential eviction (vertex body lifecycle)"));
  assert.equal(folder.item.length, 4);
  const script = folder.event.find((event) => event.listen === "test").script.exec.join("\n");
  for (const item of folder.item) {
    for (const refreshed of [false, true]) {
      await t.test(`${item.name}: refreshed=${refreshed}`, () => {
        const failures = [];
        let calls = 0;
        const scope = new Map(collection.variable.map(({ key, value }) => [key, value]));
        scope.set("baseUrl", "http://gateway.test");
        const counterUrl = refreshed ? "http://vertex-fixture.test:18792/__tokens" : "http://127.0.0.1:8792/__tokens";
        if (refreshed) scope.set("vertexOAuthCounterUrl", counterUrl);
        const replaceIn = (s) => s.replace(/\{\{(\w+)\}\}/g, (_, key) => scope.get(key));
        const headers = new Map(item.request.header.map(({ key, value }) => [key, value]));
        headers.set("Content-Length", String(Buffer.byteLength(item.request.body.raw)));
        const pm = {
          info: { requestName: item.name },
          variables: { get: (k) => scope.get(k), replaceIn },
          request: { url: { toString: () => item.request.url.raw }, body: item.request.body, headers },
          response: { code: Number(item.name.match(/\[EXPECT-(\d+)\]/)[1]), text: () => "gzip: invalid header" },
          test: (name, fn) => { try { fn(); } catch (error) { failures.push({ name, error }); } },
          expect: (actual, message) => ({ to: { equal: (expected) => assert.equal(actual, expected, message), include: (expected) => assert.ok(actual.includes(expected), message) } }),
          sendRequest: (req, cb) => {
            calls++;
            if (calls === 2) {
              assert.equal(req.method, item.request.method);
              assert.equal(req.url, pm.variables.replaceIn(item.request.url.raw));
              assert.deepEqual(req.header, { "Content-Type": "application/json", "x-bf-api-key": "vertex-us-central1" });
              const { model } = JSON.parse(replaceIn(item.request.body.raw));
              const expected = item.request.url.path.includes("responses")
                ? { model, stream: false, input: "hi" }
                : { model, stream: false, messages: [{ role: "user", content: "hi" }] };
              assert.deepEqual(JSON.parse(req.body.raw), expected);
              cb(null, { code: 200, text: () => "hello" });
            } else {
              assert.equal(req, counterUrl);
              cb(null, { code: 200, json: () => ({ data: [{ requests: calls === 1 ? 5 : refreshed ? 6 : 5 }] }) });
            }
          },
        };
        new Function("pm", script)(pm);
        assert.equal(calls, 3);
        assert.equal(failures.length, refreshed ? 0 : 1);
        if (!refreshed) assert.equal(failures[0].name, "the rejected token source was rebuilt exactly once");
      });
    }
  }
});

test("upstream-zero-cost reports a zero usage.cost", async (t) => {
  const post = await startFixture(t);
  const plain = await post({ model: "upstream-zero-cost", messages: [{ role: "user", content: "hi" }] });
  assert.equal(plain.status, 200);
  assert.equal((await plain.json()).usage.cost, 0);
  const streamed = await post({ model: "upstream-zero-cost", messages: [{ role: "user", content: "hi" }], stream: true, stream_options: { include_usage: true } });
  assert.equal(streamed.status, 200);
  const usage = (await streamed.text()).split("\n")
    .filter((line) => line.startsWith("data: {"))
    .map((line) => JSON.parse(line.slice(6)))
    .find((chunk) => chunk.usage);
  assert.equal(usage && usage.usage.cost, 0);
});
