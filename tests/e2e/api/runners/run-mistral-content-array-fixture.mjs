#!/usr/bin/env node
// Deterministic local upstream for #7906; never calls a gateway or a real provider.
// node tests/e2e/api/runners/run-mistral-content-array-fixture.mjs --env-out tmp/mistral-content-array.env.json
// Full fresh auth-profile setup and scoped gateway commands are in collection folder 143's description.
import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

export function createMistralContentArrayFixture() {
  const requests = new Map();
  const server = http.createServer(async (req, res) => {
    const pathname = new URL(req.url, "http://localhost").pathname;
    const json = (status, body) => {
      res.writeHead(status, { "content-type": "application/json" });
      res.end(JSON.stringify(body));
    };
    if (req.method === "GET" && pathname === "/__health") {
      json(200, { fixture: "mistral-content-array-7906", version: 1 });
      return;
    }
    const witness = /^\/__requests\/([a-zA-Z0-9_-]+)$/.exec(pathname);
    if (witness && req.method === "GET") {
      json(200, { fixture: "mistral-content-array-7906", requests: requests.get(witness[1]) || [] });
      return;
    }
    if (witness && req.method === "DELETE") {
      requests.delete(witness[1]);
      json(200, { deleted: witness[1] });
      return;
    }
    if (req.method === "GET" && pathname === "/v1/models") {
      json(200, { object: "list", data: [{ id: "mistral-array-fixture", object: "model", owned_by: "fixture" }] });
      return;
    }
    if (req.method !== "POST" || pathname !== "/v1/chat/completions") {
      json(404, { error: { message: "unknown fixture route" } });
      return;
    }
    try {
      req.setEncoding("utf8");
      let raw = "";
      for await (const chunk of req) {
        raw += chunk;
        if (Buffer.byteLength(raw) > 65536) throw new Error("fixture request too large");
      }
      const body = JSON.parse(raw);
      const contentIn = body.messages?.find((item) => item.role === "user")?.content;
      const message = Array.isArray(contentIn) && contentIn.every((item) => item.type === "text" && typeof item.text === "string")
        ? contentIn.map((item) => item.text).join("") : contentIn;
      const match = typeof message === "string" && /^mistral-content-array:(text-chat|text-responses|text-anthropic|thinking-chat|mixed-responses|reference-anthropic):([a-zA-Z0-9_-]+)$/.exec(message);
      if (!match || body.model !== "mistral-array-fixture" || body.stream !== true || req.headers.authorization !== "Bearer mistral-array-fixture-key") {
        json(400, { error: { message: "invalid fixture model, scenario, stream flag or dummy key" } });
        return;
      }
      const [, scenario, nonce] = match;
      const text = [{ type: "text", text: "mistral array " }, { type: "text", text: "fixture " + nonce }];
      const thinking = { type: "thinking", thinking: [{ type: "text", text: "private reasoning " + nonce }], signature: "fixture-signature", closed: false };
      const content = scenario.startsWith("text-") ? text
        : scenario === "thinking-chat" ? [thinking]
          : scenario === "mixed-responses" ? [text[0], thinking]
            : [{ type: "reference", reference_ids: [7, "fixture-" + nonce] }];
      const envelope = { id: "mistral-array-" + nonce, object: "chat.completion.chunk", created: 1, model: body.model };
      const frame = { ...envelope, choices: [{ index: 0, delta: { content }, finish_reason: null }] };
      const record = { scenario, method: req.method, path: pathname, model: body.model, stream: body.stream, frame };
      const rows = requests.get(nonce) || [];
      rows.push(record);
      requests.set(nonce, rows);
      res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
      // Commit the stream before the array, so unsupported arrays must produce a late SSE error.
      res.write(`data: ${JSON.stringify({ ...envelope, choices: [{ index: 0, delta: { role: "assistant" }, finish_reason: null }] })}\n\n`);
      res.write(`data: ${JSON.stringify(frame)}\n\n`);
      res.write(`data: ${JSON.stringify({ ...envelope, choices: [{ index: 0, delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 5, completion_tokens: 2, total_tokens: 7 } })}\n\n`);
      res.end("data: [DONE]\n\n");
    } catch (error) {
      if (res.headersSent) res.destroy();
      else json(400, { error: { message: error.message } });
    }
  });
  server.requestTimeout = 10000;
  server.headersTimeout = 10000;
  server.keepAliveTimeout = 1000;
  return server;
}

async function main() {
  const args = process.argv.slice(2);
  if (args.length && (args.length !== 2 || args[0] !== "--env-out" || !args[1])) {
    throw new Error("Usage: run-mistral-content-array-fixture.mjs [--env-out path.json]");
  }
  const port = Number(process.env.MISTRAL_ARRAY_FIXTURE_PORT || 0);
  if (!Number.isInteger(port) || port < 0 || port > 65535) throw new Error("MISTRAL_ARRAY_FIXTURE_PORT must be 0..65535");
  const server = createMistralContentArrayFixture();
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, "127.0.0.1", resolve);
  });
  const baseURL = `http://127.0.0.1:${server.address().port}`;
  const envPath = args[1] && path.resolve(args[1]);
  let ownedEnvironment;
  try {
    if (envPath) {
      ownedEnvironment = JSON.stringify({ name: "Mistral array local fixture", values: [
        { key: "mistralArrayMockBaseUrl", value: baseURL, enabled: true },
        { key: "mistralArrayUsername", value: "mistral_array_harness", enabled: true },
        { key: "mistralArrayPassword", value: "harness-only-password", enabled: true },
      ] }, null, 2) + "\n";
      fs.mkdirSync(path.dirname(envPath), { recursive: true });
      fs.writeFileSync(envPath, ownedEnvironment, { flag: "wx", mode: 0o600 });
    }
  } catch (error) {
    server.close();
    throw error;
  }
  console.log(`Mistral content-array fixture: ${baseURL}; use a fresh auth-enabled gateway profile on the same host`);
  if (envPath) console.log(`Postman environment: ${envPath}`);
  let closing = false;
  const shutdown = () => {
    if (closing) return;
    closing = true;
    if (envPath && ownedEnvironment) {
      try { if (fs.readFileSync(envPath, "utf8") === ownedEnvironment) fs.unlinkSync(envPath); } catch (error) {
        if (error.code !== "ENOENT") console.error(`Could not remove owned environment file: ${error.code}`);
      }
    }
    server.close();
    server.closeAllConnections();
  };
  process.once("SIGINT", shutdown);
  process.once("SIGTERM", shutdown);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => { console.error(error.message); process.exitCode = 1; });
}
