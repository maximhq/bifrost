#!/usr/bin/env node
// Local-only fixture for #8073. Requires Node 22.12+ (use --experimental-sqlite on Node 22.12).
// Run against a fresh profile; never seed or alter an existing gateway DB.
// node --experimental-sqlite tests/e2e/api/runners/run-model-parameters-sync-fixture.mjs --app-dir tmp/model-parameters-sync --port 8797
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { DatabaseSync } from "node:sqlite";

const args = process.argv.slice(2);
if (args.length !== 4 || args[0] !== "--app-dir" || args[2] !== "--port") {
  throw new Error("Usage: run-model-parameters-sync-fixture.mjs --app-dir NEW_DIRECTORY --port PORT");
}
const appDir = path.resolve(args[1]);
const port = Number(args[3]);
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error("port must be 1..65535");
if (fs.existsSync(appDir)) throw new Error("app-dir must not exist; use a fresh isolated profile");
const baseURL = `http://127.0.0.1:${port}`;
const model = "custom-params-model";
const provider = "custom-anthropic-sync";
let sequence = 0;
let feedMode = "valid";

const server = http.createServer(async (req, res) => {
  const route = new URL(req.url, baseURL).pathname;
  const json = (status, body) => {
    res.writeHead(status, { "content-type": "application/json" });
    res.end(JSON.stringify(body));
  };
  if (req.method === "GET" && route === "/api/fixture-health") {
    return json(200, { fixture: "model-parameters-sync-8073" });
  }
  if (req.method === "GET" && route === "/pricing.json") return json(200, {});
  if (req.method === "GET" && route === "/params.json") {
    if (feedMode === "invalid") {
      // An unusable feed must not overwrite the warmed custom row or flush
      // its capabilities just because other valid rows remain in SQLite.
      return json(200, { [`${provider}/${model}`]: { provider, max_output_tokens: "invalid" } });
    }
    // Same bare model, a different provider: it cannot answer the custom
    // provider's lookup. The qualified custom row exists only in SQLite.
    return json(200, { [model]: { provider: "anthropic", max_output_tokens: 16000 } });
  }
  if (req.method === "GET" && route === "/v1/models") {
    return json(200, { data: [{ id: model, type: "model", display_name: model, created_at: "2026-01-01T00:00:00Z" }], has_more: false });
  }
  if (req.method !== "POST" || (route !== "/v1/messages" && route !== "/api/params-feed")) {
    return json(404, { error: { message: "unknown fixture route" } });
  }
  try {
    let raw = "";
    req.setEncoding("utf8");
    for await (const chunk of req) {
      raw += chunk;
      if (Buffer.byteLength(raw) > 65536) throw new Error("fixture request too large");
    }
    const body = JSON.parse(raw);
    if (route === "/api/params-feed") {
      if (body.mode !== "valid" && body.mode !== "invalid") return json(400, { error: { message: "invalid feed mode" } });
      feedMode = body.mode;
      return json(200, { mode: feedMode });
    }
    if (body.model !== model || req.headers["x-api-key"] !== "fixture-key" || !Number.isInteger(body.max_tokens)) {
      return json(400, { error: { message: "invalid fixture model, key or max_tokens" } });
    }
    // The response witnesses the actual upstream field, including 4096 on
    // the unfixed gateway. Returning HTTP 200 does not hide the regression.
    const text = `fixture max_tokens=${body.max_tokens}`;
    const message = { id: `msg_fixture_${++sequence}`, type: "message", role: "assistant", model: body.model,
      content: [{ type: "text", text }], stop_reason: "end_turn", stop_sequence: null,
      usage: { input_tokens: 1, output_tokens: 1 } };
    if (!body.stream) return json(200, message);
    res.writeHead(200, { "content-type": "text/event-stream" });
    const frames = [
      { type: "message_start", message: { ...message, content: [], stop_reason: null, usage: { input_tokens: 1, output_tokens: 0 } } },
      { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } },
      { type: "content_block_delta", index: 0, delta: { type: "text_delta", text } },
      { type: "content_block_stop", index: 0 },
      { type: "message_delta", delta: { stop_reason: "end_turn", stop_sequence: null }, usage: { output_tokens: 1 } },
      { type: "message_stop" },
    ];
    for (const frame of frames) res.write(`event: ${frame.type}\ndata: ${JSON.stringify(frame)}\n\n`);
    res.end();
  } catch (err) {
    if (res.headersSent) res.destroy();
    else json(400, { error: { message: err.message } });
  }
});
await new Promise((resolve, reject) => {
  server.once("error", reject);
  server.listen(port, "127.0.0.1", resolve);
});
try {
  fs.mkdirSync(path.dirname(appDir), { recursive: true });
  fs.mkdirSync(appDir);
  const dbPath = path.join(appDir, "config.db");
  const db = new DatabaseSync(dbPath);
  try {
    // Mirrors framework/configstore/tables/modelparameters.go; the gateway
    // migrates all remaining tables itself on first startup.
    db.exec("CREATE TABLE governance_model_parameters (id INTEGER PRIMARY KEY AUTOINCREMENT, model VARCHAR(255) NOT NULL, data TEXT NOT NULL); CREATE UNIQUE INDEX idx_model_params_model ON governance_model_parameters(model)");
    db.prepare("INSERT INTO governance_model_parameters (model, data) VALUES (?, ?)")
      .run(`${provider}/${model}`, JSON.stringify({ provider, max_output_tokens: 32000 }));
  } finally { db.close(); }
  const config = {
    setup_token: "bifrost-e2e-setup-token",
    client: {
      allow_per_request_raw_override: true, enforce_auth_on_inference: false,
      // Keep the explicit-limit control independent of compat's allowlist.
      compat: { convert_text_to_chat: false, convert_chat_to_responses: false,
        should_drop_params: false, should_convert_params: false, azure_deepseek: false,
        force_reasoning_only_models_to_responses: false },
    },
    config_store: { enabled: true, type: "sqlite", config: { path: dbPath } },
    logs_store: { enabled: false },
    framework: { pricing: { pricing_url: `${baseURL}/pricing.json`, model_parameters_url: `${baseURL}/params.json`, mcp_library_sync_interval: 0 } },
    providers: { [provider]: {
      custom_provider_config: { base_provider_type: "anthropic" },
      keys: [{ name: "fixture", value: "fixture-key", models: ["*"], weight: 1 }],
      network_config: { base_url: baseURL, allow_private_network: true, max_retries: 0 },
    } },
  };
  fs.writeFileSync(path.join(appDir, "config.json"), JSON.stringify(config, null, 2) + "\n", { flag: "wx" });
  fs.writeFileSync(path.join(appDir, "fixture.env.json"), JSON.stringify({ name: "Model parameters sync local fixture", values: [
    { key: "modelParamsFixtureURL", value: baseURL, enabled: true },
  ] }, null, 2) + "\n", { flag: "wx" });
  console.log(`Fixture: ${baseURL}; fresh gateway profile: ${appDir}`);
} catch (err) {
  server.close();
  throw err;
}
const shutdown = () => { server.close(); server.closeAllConnections(); };
process.once("SIGINT", shutdown);
process.once("SIGTERM", shutdown);
