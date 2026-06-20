#!/usr/bin/env node

import http from "node:http";

const baseURL = (process.env.BIFROST_E2E_BASE_URL || process.env.BIFROST_BASE_URL || "http://localhost:8080").replace(/\/+$/, "");
const adminAuthHeader = process.env.BIFROST_E2E_AUTH_HEADER || "";
const providerName = `otel-e2e-${process.pid}-${Date.now()}`;
const modelName = "hello-world";
const requestedModel = `${providerName}/${modelName}`;
const requestID = `otel-e2e-request-${process.pid}-${Date.now()}`;

const state = {
  otelTraceRequests: [],
  otelMetricRequests: [],
  mockRequests: [],
};

function listen(server, host = "127.0.0.1") {
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, host, () => {
      server.off("error", reject);
      resolve(server.address().port);
    });
  });
}

function close(server) {
  return new Promise((resolve) => server.close(() => resolve()));
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    req.on("data", (chunk) => chunks.push(chunk));
    req.on("end", () => resolve(Buffer.concat(chunks)));
    req.on("error", reject);
  });
}

function createOtelReceiver() {
  return http.createServer(async (req, res) => {
    const body = await readBody(req);
    if (req.method === "POST" && req.url === "/v1/traces") {
      state.otelTraceRequests.push({
        headers: req.headers,
        bytes: body.length,
        body,
      });
      res.writeHead(200, { "content-type": "application/x-protobuf" });
      res.end("");
      return;
    }
    if (req.method === "POST" && req.url === "/v1/metrics") {
      state.otelMetricRequests.push({
        headers: req.headers,
        bytes: body.length,
        body,
      });
      res.writeHead(200, { "content-type": "application/x-protobuf" });
      res.end("");
      return;
    }
    res.writeHead(404);
    res.end("not found");
  });
}

function createOpenAIMock() {
  return http.createServer(async (req, res) => {
    const body = await readBody(req);
    if (req.method === "POST" && req.url === "/v1/chat/completions") {
      state.mockRequests.push({
        headers: req.headers,
        body: body.toString("utf8"),
      });
      const now = Math.floor(Date.now() / 1000);
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify({
        id: `chatcmpl-${now}`,
        object: "chat.completion",
        created: now,
        model: modelName,
        choices: [
          {
            index: 0,
            message: {
              role: "assistant",
              content: "hello world",
            },
            finish_reason: "stop",
          },
        ],
        usage: {
          prompt_tokens: 2,
          completion_tokens: 2,
          total_tokens: 4,
        },
      }));
      return;
    }
    res.writeHead(404);
    res.end("not found");
  });
}

async function request(method, path, body, headers = {}) {
  const requestHeaders = adminAuthHeader ? { Authorization: adminAuthHeader, ...headers } : { ...headers };
  if (body !== undefined && requestHeaders["content-type"] === undefined && requestHeaders["Content-Type"] === undefined) {
    requestHeaders["content-type"] = "application/json";
  }
  const res = await fetch(`${baseURL}${path}`, {
    method,
    headers: Object.keys(requestHeaders).length === 0 ? undefined : requestHeaders,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let json = null;
  if (text) {
    try {
      json = JSON.parse(text);
    } catch {
      json = null;
    }
  }
  return { ok: res.ok, status: res.status, text, json };
}

async function mustRequest(method, path, body, headers = {}) {
  const res = await request(method, path, body, headers);
  if (!res.ok) {
    throw new Error(`${method} ${path} failed with ${res.status}: ${res.text}`);
  }
  return res;
}

async function getPlugin(name) {
  const res = await request("GET", `/api/plugins/${encodeURIComponent(name)}`);
  if (res.status === 404) {
    return null;
  }
  if (!res.ok) {
    throw new Error(`GET /api/plugins/${name} failed with ${res.status}: ${res.text}`);
  }
  return res.json?.plugin ?? res.json ?? null;
}

function pluginUpdatePayload(plugin) {
  return {
    enabled: Boolean(plugin.enabled),
    path: plugin.path ?? null,
    config: plugin.config ?? {},
    placement: plugin.placement ?? undefined,
    order: plugin.order ?? undefined,
  };
}

async function enableBuiltinPlugin(name, config) {
  await mustRequest("PUT", `/api/plugins/${encodeURIComponent(name)}`, {
    enabled: true,
    path: null,
    config,
  });
}

async function addLocalProvider(mockPort) {
  await mustRequest("POST", "/api/providers", {
    provider: providerName,
    custom_provider_config: {
      base_provider_type: "openai",
      is_key_less: true,
      allowed_requests: {
        chat_completion: true,
      },
    },
    network_config: {
      base_url: `http://127.0.0.1:${mockPort}`,
      allow_private_network: true,
      default_request_timeout_in_seconds: 10,
      max_retries: 0,
      retry_backoff_initial: 500,
      retry_backoff_max: 5000,
    },
    concurrency_and_buffer_size: {
      concurrency: 10,
      buffer_size: 100,
    },
    keys: [],
  });
}

async function chatHelloWorld() {
  const res = await mustRequest("POST", "/v1/chat/completions", {
    model: requestedModel,
    messages: [
      { role: "user", content: "hello world" },
    ],
  }, {
    "x-request-id": requestID,
  });
  const content = res.json?.choices?.[0]?.message?.content;
  if (content !== "hello world") {
    throw new Error(`unexpected chat response content: ${JSON.stringify(content)}`);
  }
}

async function poll(name, timeoutMs, fn) {
  const started = Date.now();
  let lastError;
  while (Date.now() - started < timeoutMs) {
    try {
      const result = await fn();
      if (result) {
        return result;
      }
    } catch (err) {
      lastError = err;
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  if (lastError) {
    throw new Error(`${name} timed out: ${lastError.message}`);
  }
  throw new Error(`${name} timed out`);
}

async function assertOtelReceived() {
  const entry = await poll("OTEL trace receiver", 20000, () => state.otelTraceRequests.find((item) => item.bytes > 0));
  assertBufferContainsAll("OTEL trace export", entry.body, [
    "bifrost-e2e",
    providerName,
    modelName,
    requestID,
    "gen_ai.provider.name",
    "gen_ai.request.model",
    "gen_ai.request_id",
    "gen_ai.usage.input_tokens",
    "gen_ai.usage.output_tokens",
    "gen_ai.usage.total_tokens",
  ]);
}

async function assertOtelMetricsReceived() {
  const entry = await poll("OTEL metrics receiver", 30000, () => state.otelMetricRequests.find((item) => item.bytes > 0));
  assertBufferContainsAll("OTEL metrics export", entry.body, [
    "bifrost-e2e",
    "bifrost_upstream_requests_total",
    "bifrost_success_requests_total",
    "bifrost_input_tokens_total",
    "bifrost_output_tokens_total",
    "bifrost_upstream_latency_seconds",
    "bifrost_request_retries",
    "provider",
    providerName,
    "model",
    requestedModel,
    "method",
    "chat_completion",
  ]);
}

function assertBufferContainsAll(name, body, values) {
  for (const value of values) {
    if (!body.includes(Buffer.from(value))) {
      throw new Error(`${name} is missing ${JSON.stringify(value)}`);
    }
  }
}

function findPrometheusSample(metrics, metricName, labels) {
  const prefix = `${metricName}{`;
  return metrics
    .split("\n")
    .find((line) => line.startsWith(prefix) &&
      Object.entries(labels).every(([key, value]) => line.includes(`${key}="${value}"`)));
}

function parsePrometheusValue(line) {
  const raw = line?.trim().split(/\s+/).at(-1);
  const value = Number(raw);
  if (!Number.isFinite(value)) {
    throw new Error(`invalid Prometheus sample value in line: ${line}`);
  }
  return value;
}

async function assertPrometheusScrape() {
  const metrics = await poll("Prometheus scrape", 20000, async () => {
    const res = await request("GET", "/metrics");
    if (!res.ok) {
      throw new Error(`GET /metrics failed with ${res.status}: ${res.text}`);
    }
    const hasLLMSuccessMetric = res.text
      .split("\n")
      .some((line) => line.startsWith("bifrost_success_requests_total{") &&
        line.includes(`provider="${providerName}"`) &&
        (line.includes(`model="${modelName}"`) || line.includes(`model="${requestedModel}"`)));
    if (hasLLMSuccessMetric) {
      return res.text;
    }
    return null;
  });

  const labels = { provider: providerName, model: requestedModel, method: "chat_completion" };
  const successLine = findPrometheusSample(metrics, "bifrost_success_requests_total", labels);
  const upstreamLine = findPrometheusSample(metrics, "bifrost_upstream_requests_total", labels);
  const inputLine = findPrometheusSample(metrics, "bifrost_input_tokens_total", labels);
  const outputLine = findPrometheusSample(metrics, "bifrost_output_tokens_total", labels);

  if (!successLine || parsePrometheusValue(successLine) < 1) {
    throw new Error("Prometheus scrape is missing bifrost_success_requests_total for the LLM call");
  }
  if (!upstreamLine || parsePrometheusValue(upstreamLine) < 1) {
    throw new Error("Prometheus scrape is missing bifrost_upstream_requests_total for the LLM call");
  }
  if (!inputLine || parsePrometheusValue(inputLine) < 2) {
    throw new Error("Prometheus scrape is missing input token count for the LLM call");
  }
  if (!outputLine || parsePrometheusValue(outputLine) < 2) {
    throw new Error("Prometheus scrape is missing output token count for the LLM call");
  }

  return metrics;
}

async function assertLoggingTrace() {
  const log = await poll("logging trace API", 30000, async () => {
    const res = await request("GET", `/api/logs/${encodeURIComponent(requestID)}`);
    if (res.status === 404) {
      return null;
    }
    if (!res.ok) {
      throw new Error(`GET /api/logs/${requestID} failed with ${res.status}: ${res.text}`);
    }
    return res.json;
  });

  if (log.id !== requestID) {
    throw new Error(`logging trace API returned unexpected id: ${JSON.stringify(log.id)}`);
  }
  if (log.status !== "success") {
    throw new Error(`logging trace API returned unexpected status: ${JSON.stringify(log.status)}`);
  }
  if (log.provider !== providerName) {
    throw new Error(`logging trace API returned unexpected provider: ${JSON.stringify(log.provider)}`);
  }
  if (log.model !== modelName && log.model !== requestedModel) {
    throw new Error(`logging trace API returned unexpected model: ${JSON.stringify(log.model)}`);
  }
  if (log.object !== "chat_completion" && log.object !== "chat.completion") {
    throw new Error(`logging trace API returned unexpected object: ${JSON.stringify(log.object)}`);
  }
  if (!log.token_usage || log.token_usage.prompt_tokens !== 2 || log.token_usage.completion_tokens !== 2 || log.token_usage.total_tokens !== 4) {
    throw new Error(`logging trace API returned incomplete token usage: ${JSON.stringify(log.token_usage)}`);
  }
  if (!Array.isArray(log.input_history) || log.input_history.length !== 1 || log.input_history[0]?.role !== "user" || log.input_history[0]?.content !== "hello world") {
    throw new Error(`logging trace API returned incomplete input history: ${JSON.stringify(log.input_history)}`);
  }
  if (!log.output_message || log.output_message.role !== "assistant" || log.output_message.content !== "hello world") {
    throw new Error(`logging trace API returned incomplete output message: ${JSON.stringify(log.output_message)}`);
  }
  if (typeof log.latency !== "number" || log.latency < 0) {
    throw new Error(`logging trace API returned invalid latency: ${JSON.stringify(log.latency)}`);
  }

  return log;
}

function assertMockProviderRequest() {
  if (state.mockRequests.length !== 1) {
    throw new Error(`expected exactly one mock provider request, got ${state.mockRequests.length}`);
  }
  let body;
  try {
    body = JSON.parse(state.mockRequests[0].body);
  } catch (err) {
    throw new Error(`mock provider request body is not JSON: ${err.message}`);
  }
  if (body.model !== requestedModel && body.model !== modelName) {
    throw new Error(`mock provider received unexpected model: ${JSON.stringify(body.model)}`);
  }
  if (!Array.isArray(body.messages) || body.messages.length !== 1 || body.messages[0]?.role !== "user" || body.messages[0]?.content !== "hello world") {
    throw new Error(`mock provider received incomplete messages: ${JSON.stringify(body.messages)}`);
  }
}

async function main() {
  console.log("Running local observability API check...");
  console.log(`  Bifrost: ${baseURL}`);
  if (adminAuthHeader) {
    console.log("  Auth:    Bearer admin credentials");
  }

  const originalOtel = await getPlugin("otel");
  const originalTelemetry = await getPlugin("telemetry");

  const otelReceiver = createOtelReceiver();
  const openaiMock = createOpenAIMock();
  const otelPort = await listen(otelReceiver);
  const mockPort = await listen(openaiMock);

  let cleanupError = null;
  try {
    console.log(`  OTEL trace receiver:   http://127.0.0.1:${otelPort}/v1/traces`);
    console.log(`  OTEL metrics receiver: http://127.0.0.1:${otelPort}/v1/metrics`);
    console.log(`  Mock provider:  http://127.0.0.1:${mockPort}`);

    await enableBuiltinPlugin("telemetry", { metrics_enabled: true });
    await enableBuiltinPlugin("otel", {
      profiles: [
        {
          enabled: true,
          service_name: "bifrost-e2e",
          collector_url: `http://127.0.0.1:${otelPort}/v1/traces`,
          trace_type: "genai_extension",
          protocol: "http",
          insecure: true,
          metrics_enabled: true,
          metrics_endpoint: `http://127.0.0.1:${otelPort}/v1/metrics`,
          metrics_push_interval: 1,
          disable_content_logging: true,
        },
      ],
    });

    await addLocalProvider(mockPort);
    await chatHelloWorld();

    assertMockProviderRequest();

    await assertOtelReceived();
    await assertOtelMetricsReceived();
    await assertPrometheusScrape();
    await assertLoggingTrace();

    console.log(`  OTEL trace exports received: ${state.otelTraceRequests.length}`);
    console.log(`  OTEL metric exports received: ${state.otelMetricRequests.length}`);
    console.log(`  Prometheus scrape includes provider="${providerName}" model="${requestedModel}"`);
    console.log(`  Logging trace API returned id="${requestID}"`);
    console.log("Local observability API check passed.");
  } finally {
    try {
      await request("DELETE", `/api/providers/${encodeURIComponent(providerName)}`);
      if (originalOtel) {
        await request("PUT", "/api/plugins/otel", pluginUpdatePayload(originalOtel));
      } else {
        await request("DELETE", "/api/plugins/otel");
      }
      if (originalTelemetry) {
        await request("PUT", "/api/plugins/telemetry", pluginUpdatePayload(originalTelemetry));
      }
    } catch (err) {
      cleanupError = err;
    }
    await Promise.all([close(otelReceiver), close(openaiMock)]);
    if (cleanupError) {
      console.warn(`WARNING: observability cleanup failed: ${cleanupError.message}`);
    }
  }
}

main().catch((err) => {
  console.error(`Local observability API check failed: ${err.message}`);
  process.exit(1);
});
