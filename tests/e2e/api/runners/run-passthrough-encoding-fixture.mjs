#!/usr/bin/env node
// Local encoded upstream for #9764. The generated profile uses only dummy credentials.
import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { gzipSync } from "node:zlib";

const appDir = process.argv[2];
if (!appDir || process.argv.length !== 3) {
  throw new Error("Usage: node run-passthrough-encoding-fixture.mjs <new-app-directory>");
}
const profile = path.resolve(appDir);
fs.mkdirSync(profile);
const chat = { model: "gzip-fixture", choices: [{ message: { role: "assistant", content: "gzip fixture" }, finish_reason: "stop" }], usage: { prompt_tokens: 2, completion_tokens: 1, total_tokens: 3 } };
const terminal = 'event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}\n\n';
const models = ["gzip-fixture", "identity-fixture", "gzip-error-fixture"];
const streams = new Map([
  ["/v1/messages", { key: "x-api-key", data: 'event: message_stop\ndata: {"type":"message_stop"}\n\n' }],
  ["/openai/v1/responses", { key: "api-key", data: terminal }],
  ["/v1beta/models/gzip-fixture:streamGenerateContent?alt=sse", { key: "x-goog-api-key", data: 'data: {"candidates":[{"finishReason":"STOP"}]}\n\n' }],
  ["/v1/text-to-speech/fixture/stream/with-timestamps", { key: "xi-api-key", type: "application/x-ndjson", data: '{"audio_base64":"YQ==","alignment":null}\n' }],
]);
const server = http.createServer(async (req, res) => {
  if (req.method === "GET" && ["/pricing.json", "/model-parameters.json"].includes(req.url)) {
    res.writeHead(200, { "Content-Type": "application/json" }).end("{}");
    return;
  }
  if (req.method === "GET" && req.url === "/v1/models") {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ object: "list", data: models.map(id => ({ id, object: "model", owned_by: "fixture" })) }));
    return;
  }
  const stream = streams.get(req.url);
  if (req.method !== "POST" || (!stream && !["/v1/chat/completions", "/v1/responses"].includes(req.url))) {
    res.writeHead(404).end();
    return;
  }
  let raw = "";
  for await (const chunk of req) raw += chunk;
  const body = JSON.parse(raw);
  const valid = stream
    ? req.headers[stream.key] === "fixture-only"
    : models.includes(body.model) && req.headers.authorization === "Bearer fixture-only";
  if (!valid) {
    res.writeHead(400).end("expected fixture model and dummy key");
    return;
  }
  if (body.model === "gzip-error-fixture") {
    // Send a complete header block, then close before the promised body starts.
    res.writeHead(200, { "Content-Type": "text/event-stream", "Content-Encoding": "gzip", "Content-Length": "100", "X-Request-ID": "encoding-error-fixture" });
    res.flushHeaders();
    res.socket.end();
    return;
  }
  const streaming = !!stream || (req.url === "/v1/responses" && body.stream);
  const encoded = !!stream || body.model === "gzip-fixture";
  const plain = Buffer.from(stream?.data ?? (streaming ? terminal : JSON.stringify(chat)));
  const wire = encoded ? gzipSync(plain) : plain;
  res.writeHead(200, {
    "Content-Type": stream?.type ?? (streaming ? "text/event-stream" : "application/json"),
    ...(encoded ? { "Content-Encoding": "gzip" } : {}),
    "X-Request-ID": "encoding-fixture",
    "Set-Cookie": "fixture-secret=value",
  });
  // Hold the second chunk beyond the one-second gateway heartbeat interval.
  if (streaming && encoded) {
    res.write(wire.subarray(0, Math.floor(wire.length / 2)));
    const timer = setTimeout(() => res.end(wire.subarray(Math.floor(wire.length / 2))), 2200);
    res.on("close", () => clearTimeout(timer));
  } else {
    res.end(wire);
  }
});
await new Promise((resolve, reject) => {
  server.once("error", reject);
  server.listen(0, "127.0.0.1", resolve);
});
const baseURL = `http://127.0.0.1:${server.address().port}`;
const key = (name, allowedModels = ["gzip-fixture"]) => ({ name, value: "fixture-only", models: allowedModels, weight: 1 });
const network = { base_url: baseURL, allow_private_network: true, max_retries: 0 };
try {
  fs.writeFileSync(path.join(profile, "config.json"), JSON.stringify({
    $schema: "https://www.getbifrost.ai/schema",
    client: { enforce_auth_on_inference: false },
    governance: { auth_config: { is_enabled: true, admin_username: "encoding-fixture", admin_password: "fixture-only-password" } },
    framework: { pricing: { pricing_url: baseURL + "/pricing.json", model_parameters_url: baseURL + "/model-parameters.json", mcp_library_sync_interval: 0 } },
    providers: {
      openai: { keys: [key("openai-fixture", models)], network_config: network },
      anthropic: { keys: [key("anthropic-fixture")], network_config: network },
      azure: { keys: [{ ...key("azure-fixture"), azure_key_config: { endpoint: baseURL } }], network_config: network },
      gemini: { keys: [key("gemini-fixture")], network_config: { ...network, base_url: baseURL + "/v1beta" } },
      elevenlabs: { keys: [key("elevenlabs-fixture")], network_config: network },
    },
  }, null, 2) + "\n", { flag: "wx", mode: 0o600 });
} catch (error) {
  server.close();
  throw error;
}
console.log(`Upstream: ${baseURL}\nAPP_DIR=${profile}`);
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.once(signal, () => { server.close(); server.closeAllConnections(); });
}
