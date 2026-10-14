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
const dbPath = path.join(appDir, "config.db");
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
    if (feedMode === "mixed") {
      return json(200, {
        [model]: { provider: "anthropic", max_output_tokens: 24000 },
        [`${provider}/${model}`]: { provider, max_output_tokens: "invalid" },
      });
    }
    if (feedMode === "empty-collections") {
      return json(200, {
        [model]: { provider: "anthropic", max_output_tokens: 28000 },
        [`${provider}/${model}`]: { supported_endpoints: [], server_tools: {} },
      });
    }
    if (feedMode === "nested-empty") {
      return json(200, {
        [model]: { provider: "anthropic", max_output_tokens: 30000 },
        [`${provider}/${model}`]: { provider, reasoning_budget: {}, model_parameters: [{}], supported_endpoints: [null] },
      });
    }
    if (feedMode === "update-custom") {
      return json(200, { [`${provider}/${model}`]: { provider, max_output_tokens: 16000 } });
    }
    // Same bare model, a different provider: it cannot answer the custom
    // provider's lookup. The qualified custom row exists only in SQLite.
    return json(200, { [model]: { provider: "anthropic", max_output_tokens: 16000 } });
  }
  if (req.method === "GET" && route === "/v1/models") {
    return json(200, { data: [{ id: model, type: "model", display_name: model, created_at: "2026-01-01T00:00:00Z" }], has_more: false });
  }
  if (req.method !== "POST" || !["/v1/messages", "/api/params-feed", "/api/params-db-fault", "/api/params-peer-sync"].includes(route)) {
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
    if (route === "/api/params-peer-sync") {
      // Model a peer gateway holding the shared lease while adding a row.
      // The target gateway must wait before taking its own DB snapshot.
      const gateway = new URL(body.gateway_url);
      if (gateway.protocol !== "http:" || !["127.0.0.1", "localhost"].includes(gateway.hostname)) {
        return json(400, { error: { message: "peer-sync requires a loopback gateway" } });
      }
      const db = new DatabaseSync(dbPath);
      const lockKey = "model_catalog_params_sync";
      const holder = "model-parameters-sync-fixture-peer";
      const peerModel = body.slow === true ? "slow-peer-params-model" : "peer-params-model";
      const holdMs = body.slow === true ? 12000 : 900;
      let force;
      try {
        db.prepare("INSERT INTO distributed_locks (lock_key, holder_id, expires_at) VALUES (?, ?, ?)")
          .run(lockKey, holder, new Date(Date.now() + 30000).toISOString().replace("T", " ").replace("Z", "+00:00"));
        force = fetch(new URL("/api/pricing/force-sync", gateway), {
          method: "POST", headers: { "content-type": "application/json", "X-Bifrost-Setup-Token": "bifrost-e2e-setup-token" },
          body: "{}", signal: AbortSignal.timeout(30000),
        }).then(async (response) => ({ code: response.status, body: await response.json() }));
        // Attach a rejection handler immediately, including while the lease is held.
        const completedBeforePeer = await Promise.race([
          force.then(() => true), new Promise((resolve) => setTimeout(() => resolve(false), holdMs)),
        ]);
        db.prepare("INSERT INTO governance_model_parameters (model, data) VALUES (?, ?) ON CONFLICT(model) DO UPDATE SET data = excluded.data")
          .run(`${provider}/${peerModel}`, JSON.stringify({ provider, max_output_tokens: 32000 }));
        db.prepare("DELETE FROM distributed_locks WHERE lock_key = ? AND holder_id = ?").run(lockKey, holder);
        const result = await force;
        return json(200, { completedBeforePeer, sync: result });
      } finally {
        db.prepare("DELETE FROM distributed_locks WHERE lock_key = ? AND holder_id = ?").run(lockKey, holder);
        db.close();
      }
    }
    if (route === "/api/params-db-fault") {
      if (!["after-write", "off"].includes(body.mode)) return json(400, { error: { message: "invalid fault mode" } });
      const db = new DatabaseSync(dbPath);
      try {
        db.exec("DROP TRIGGER IF EXISTS fixture_params_read_failure; DELETE FROM governance_model_parameters WHERE id = -1 AND model = 'fixture-read-fault'");
        if (body.mode === "after-write") {
          // The normal upsert commits, but a subsequent bulk read cannot scan
          // this sentinel's negative ID into TableModelParameters.ID (uint).
          // Only this fresh fixture DB is touched; the next request removes it.
          db.exec(`CREATE TRIGGER fixture_params_read_failure AFTER UPDATE ON governance_model_parameters
            WHEN NEW.model = '${provider}/${model}' BEGIN
              INSERT OR IGNORE INTO governance_model_parameters (id, model, data) VALUES (-1, 'fixture-read-fault', '{}');
            END`);
          feedMode = "update-custom";
        }
      } finally { db.close(); }
      return json(200, { mode: body.mode });
    }
    if (route === "/api/params-feed") {
      if (!["valid", "invalid", "mixed", "empty-collections", "nested-empty"].includes(body.mode)) return json(400, { error: { message: "invalid feed mode" } });
      feedMode = body.mode;
      return json(200, { mode: feedMode });
    }
    if (![model, `anthropic/${model}`, "peer-params-model", "slow-peer-params-model"].includes(body.model) || req.headers["x-api-key"] !== "fixture-key" || !Number.isInteger(body.max_tokens)) {
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
