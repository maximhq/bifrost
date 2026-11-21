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
const server = http.createServer(async (req, res) => {
  if (req.method === "GET" && req.url === "/v1/models") {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ object: "list", data: ["gzip-fixture", "identity-fixture", "gzip-error-fixture"].map(id => ({ id, object: "model", owned_by: "fixture" })) }));
    return;
  }
  if (req.method !== "POST" || !["/v1/chat/completions", "/v1/responses"].includes(req.url)) {
    res.writeHead(404).end();
    return;
  }
  let raw = "";
  for await (const chunk of req) raw += chunk;
  const body = JSON.parse(raw);
  if (!["gzip-fixture", "identity-fixture", "gzip-error-fixture"].includes(body.model) || req.headers.authorization !== "Bearer fixture-only") {
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
  const sse = req.url === "/v1/responses" && body.stream;
  const encoded = body.model === "gzip-fixture";
  const plain = Buffer.from(sse ? terminal : JSON.stringify(chat));
  const wire = encoded ? gzipSync(plain) : plain;
  res.writeHead(200, {
    "Content-Type": sse ? "text/event-stream" : "application/json",
    ...(encoded ? { "Content-Encoding": "gzip" } : {}),
    "X-Request-ID": "encoding-fixture",
    "Set-Cookie": "fixture-secret=value",
  });
  // Hold an encoded SSE body beyond the one-second gateway heartbeat interval.
  if (sse && encoded) {
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
try {
  fs.writeFileSync(path.join(profile, "config.json"), JSON.stringify({
    $schema: "https://www.getbifrost.ai/schema",
    client: { enforce_auth_on_inference: false },
    governance: { auth_config: { is_enabled: true, admin_username: "encoding-fixture", admin_password: "fixture-only-password" } },
    providers: { openai: {
      keys: [{ name: "fixture", value: "fixture-only", models: ["gzip-fixture", "identity-fixture", "gzip-error-fixture"], weight: 1 }],
      network_config: { base_url: baseURL, allow_private_network: true, max_retries: 0 },
    } },
  }, null, 2) + "\n", { flag: "wx", mode: 0o600 });
} catch (error) {
  server.close();
  throw error;
}
console.log(`Upstream: ${baseURL}\nAPP_DIR=${profile}`);
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.once(signal, () => { server.close(); server.closeAllConnections(); });
}
