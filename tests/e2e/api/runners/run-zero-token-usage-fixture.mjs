#!/usr/bin/env node
// Local OpenAI-compatible upstream for #8749. No real credentials or provider calls.
// Run in one terminal, then start Bifrost with the printed APP_DIR in another.
import http from "node:http";
import fs from "node:fs";
import path from "node:path";

const usageByModel = {
  zero: { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 },
  nonzero: { prompt_tokens: 204, completion_tokens: 2, total_tokens: 206 },
  "total-only": { total_tokens: 12 },
  "prompt-zero": { prompt_tokens: 0, total_tokens: 12 },
  "completion-zero": { completion_tokens: 0, total_tokens: 12 },
  "null-counts": { prompt_tokens: null, completion_tokens: null, total_tokens: 12 },
  "no-usage": undefined,
};
const models = Object.keys(usageByModel);
const appDir = process.argv[2];
if (!appDir || process.argv.length !== 3) {
  throw new Error("Usage: node run-zero-token-usage-fixture.mjs <new-app-directory>");
}
const profile = path.resolve(appDir);
// Require an unused directory so an existing gateway profile cannot be overwritten.
fs.mkdirSync(profile);
const server = http.createServer(async (req, res) => {
  const json = (status, body) => {
    res.writeHead(status, { "content-type": "application/json" });
    res.end(JSON.stringify(body));
  };
  if (req.method === "GET" && req.url === "/v1/models") {
    json(200, { object: "list", data: models.map(id => ({ id, object: "model", owned_by: "fixture" })) });
    return;
  }
  if (req.method !== "POST" || !["/v1/chat/completions", "/v1/completions", "/v1/embeddings", "/v1/responses"].includes(req.url)) {
    json(404, { error: { message: "unknown fixture route" } });
    return;
  }
  try {
    let raw = "";
    for await (const chunk of req) {
      raw += chunk;
      if (Buffer.byteLength(raw) > 65536) throw new Error("request too large");
    }
    const body = JSON.parse(raw);
    if (!models.includes(body.model) || req.headers.authorization !== "Bearer fixture-only") {
      throw new Error("expected fixture model and dummy key");
    }
    const usage = usageByModel[body.model];
    if (req.url === "/v1/embeddings") {
      json(200, {
        object: "list", model: body.model,
        data: [{ object: "embedding", index: 0, embedding: [0.25, 0.75] }],
        usage: { prompt_tokens: 0, total_tokens: 0 },
      });
      return;
    }
    if (req.url === "/v1/responses") {
      var responseUsage;
      if (usage) {
        responseUsage = { total_tokens: usage.total_tokens };
        if (Object.hasOwn(usage, "prompt_tokens")) responseUsage.input_tokens = usage.prompt_tokens;
        if (Object.hasOwn(usage, "completion_tokens")) responseUsage.output_tokens = usage.completion_tokens;
      }
      const response = {
        id: "resp-zero-token-fixture", object: "response", created_at: 1,
        model: body.model, status: "completed", usage: responseUsage,
        output: [{ type: "message", id: "msg-fixture", role: "assistant", status: "completed",
          content: [{ type: "output_text", text: "fixture response", annotations: [] }] }],
      };
      if (!body.stream) json(200, response);
      else {
        res.writeHead(200, { "content-type": "text/event-stream" });
        res.write(`data: ${JSON.stringify({ type: "response.created", sequence_number: 0, response: { ...response, output: [], usage: null, status: "in_progress" } })}\n\n`);
        res.write(`data: ${JSON.stringify({ type: "response.output_text.delta", sequence_number: 1, output_index: 0, content_index: 0, item_id: "msg-fixture", delta: "fixture response" })}\n\n`);
        res.end(`data: ${JSON.stringify({ type: "response.completed", sequence_number: 2, response })}\n\n`);
      }
      return;
    }
    const textCompletion = req.url === "/v1/completions";
    const envelope = { id: "chatcmpl-zero-token-fixture", created: 1, model: body.model };
    if (!body.stream) {
      const choice = textCompletion
        ? { index: 0, text: "fixture response", finish_reason: "stop" }
        : { index: 0, message: { role: "assistant", content: "fixture response" }, finish_reason: "stop" };
      json(200, { ...envelope, object: textCompletion ? "text_completion" : "chat.completion", choices: [choice], usage });
      return;
    }
    res.writeHead(200, { "content-type": "text/event-stream" });
    const frame = data => res.write(`data: ${JSON.stringify({ ...envelope, object: textCompletion ? "text_completion" : "chat.completion.chunk", ...data })}\n\n`);
    frame({ choices: [textCompletion
      ? { index: 0, text: "fixture response", finish_reason: null }
      : { index: 0, delta: { role: "assistant", content: "fixture response" }, finish_reason: null }] });
    frame({ choices: [textCompletion
      ? { index: 0, text: "", finish_reason: "stop" }
      : { index: 0, delta: {}, finish_reason: "stop" }] });
    if (usage) frame({ choices: [], usage });
    res.end("data: [DONE]\n\n");
  } catch (error) {
    if (res.headersSent) res.destroy();
    else json(400, { error: { message: error.message } });
  }
});
server.requestTimeout = 10000;
server.headersTimeout = 10000;
await new Promise((resolve, reject) => {
  server.once("error", reject);
  server.listen(0, "127.0.0.1", resolve);
});
const baseURL = `http://127.0.0.1:${server.address().port}`;
const provider = {
  keys: [{ name: "fixture", value: "fixture-only", models, weight: 1 }],
  network_config: { base_url: baseURL, allow_private_network: true, max_retries: 0 },
};
try {
  fs.writeFileSync(path.join(profile, "config.json"), JSON.stringify({
    $schema: "https://www.getbifrost.ai/schema",
    client: { enforce_auth_on_inference: false, allow_per_request_raw_override: true },
    governance: { auth_config: { is_enabled: true, admin_username: "usage-fixture", admin_password: "fixture-only-password" } },
    providers: {
      "zero-usage-fixture": { ...provider, custom_provider_config: { base_provider_type: "openai" } },
      openai: provider,
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
