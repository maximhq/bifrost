#!/usr/bin/env node

import http from "node:http";

const baseURL = (process.env.BIFROST_E2E_BASE_URL || process.env.BIFROST_BASE_URL || "http://localhost:8080").replace(/\/+$/, "");
const adminAuthHeader = process.env.BIFROST_E2E_AUTH_HEADER || "";
const providerName = `otel-e2e-${process.pid}-${Date.now()}`;
const modelName = "hello-world";
const requestedModel = `${providerName}/${modelName}`;
const requestID = `otel-e2e-request-${process.pid}-${Date.now()}`;
const errorRequestID = `otel-e2e-error-${process.pid}-${Date.now()}`;
const streamErrorRequestID = `otel-e2e-stream-error-${process.pid}-${Date.now()}`;
const responsesRefusalRequestID = `otel-e2e-responses-refusal-${process.pid}-${Date.now()}`;

// Responses API stop_reason returned by the mock. Refusals are the case the
// OTEL check pins: before the fix the Responses path never copied stop_reason
// onto the span, so gen_ai.response.finish_reason(s) stayed null for every
// /v1/responses call and a refusal was invisible to OTEL consumers.
const RESPONSES_STOP_REASON = "refusal";

// Message marker that makes the mock provider return a 404 error body.
const ERROR_TRIGGER = "trigger-error";

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

// createOpenAIMock serves an OpenAI-compatible upstream on a local port. It
// answers /v1/chat/completions with a fixed "hello world" completion (or a
// provider-style 404 when the ERROR_TRIGGER marker is present) and
// /v1/responses with a completed response whose stop_reason is a refusal.
function createOpenAIMock() {
	return http.createServer(async (req, res) => {
		const body = await readBody(req);
		if (req.method === "POST" && req.url === "/v1/chat/completions") {
			state.mockRequests.push({
				headers: req.headers,
				body: body.toString("utf8"),
			});
			// Error scenarios: the trigger marker gets a provider-style 404, streaming
			// or not — this is the pre-first-chunk failure path.
			if (body.toString("utf8").includes(ERROR_TRIGGER)) {
				res.writeHead(404, { "content-type": "application/json" });
				res.end(
					JSON.stringify({
						error: {
							message: "The model does not exist",
							type: "invalid_request_error",
							code: "model_not_found",
						},
					}),
				);
				return;
			}
			const now = Math.floor(Date.now() / 1000);
			res.writeHead(200, { "content-type": "application/json" });
			res.end(
				JSON.stringify({
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
						// Base counts sit above the cache detail below so no normalization
						// treats a detail as an overflow. The cache detail is the point: it
						// drives the OTEL exporter's read of the span cache attributes end to
						// end, so a dead read (the exporter reading a key the framework no
						// longer emits) shows up as a missing metric instead of passing
						// silently. cached_tokens maps to cache_read and OpenAI's
						// cache_write_tokens alias maps to cache_creation (see
						// ChatPromptTokensDetails.UnmarshalJSON). Distinct values so a mis-wired
						// read surfaces as the wrong number, not just a zero.
						prompt_tokens: 30,
						completion_tokens: 10,
						total_tokens: 40,
						prompt_tokens_details: { cached_tokens: 5, cache_write_tokens: 7 },
					},
				}),
			);
			return;
		}
		if (req.method === "POST" && req.url === "/v1/responses") {
			state.mockRequests.push({
				headers: req.headers,
				body: body.toString("utf8"),
			});
			const now = Math.floor(Date.now() / 1000);
			res.writeHead(200, { "content-type": "application/json" });
			// The literal stop reason must appear nowhere else in this body: with
			// disable_content_logging on, the only way it can reach the OTEL export
			// is through the span's finish_reason attribute, so the assertion below
			// cannot pass on echoed message content.
			res.end(
				JSON.stringify({
					id: `resp_${now}`,
					object: "response",
					created_at: now,
					status: "completed",
					model: modelName,
					stop_reason: RESPONSES_STOP_REASON,
					output: [
						{
							type: "message",
							id: `msg_${now}`,
							status: "completed",
							role: "assistant",
							content: [{ type: "output_text", text: "I can't help with that.", annotations: [] }],
						},
					],
					usage: {
						input_tokens: 12,
						output_tokens: 6,
						total_tokens: 18,
					},
				}),
			);
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

// addLocalProvider registers an ephemeral keyless custom provider (OpenAI base
// type) that points at the mock upstream, allowing chat, chat streaming and
// Responses API requests.
async function addLocalProvider(mockPort) {
	await mustRequest("POST", "/api/providers", {
		provider: providerName,
		custom_provider_config: {
			base_provider_type: "openai",
			is_key_less: true,
			allowed_requests: {
				chat_completion: true,
				chat_completion_stream: true,
				responses: true,
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

// chatHelloWorld fires the happy-path chat completion whose span, metrics and
// log entry the rest of the check reconciles against each other.
async function chatHelloWorld() {
	const res = await mustRequest(
		"POST",
		"/v1/chat/completions",
		{
			model: requestedModel,
			messages: [{ role: "user", content: "hello world" }],
		},
		{
			"x-request-id": requestID,
		},
	);
	const content = res.json?.choices?.[0]?.message?.content;
	if (content !== "hello world") {
		throw new Error(`unexpected chat response content: ${JSON.stringify(content)}`);
	}
}

// responsesRefusal fires a /v1/responses call the mock answers with
// stop_reason: "refusal" so the OTEL export can be checked for the finish
// reason attributes on the Responses path.
async function responsesRefusal() {
	const res = await mustRequest(
		"POST",
		"/v1/responses",
		{
			model: requestedModel,
			input: "hello world",
		},
		{
			"x-request-id": responsesRefusalRequestID,
		},
	);
	const text = res.json?.output?.[0]?.content?.[0]?.text;
	if (text !== "I can't help with that.") {
		throw new Error(`unexpected responses output text: ${JSON.stringify(text)}`);
	}
	if (res.json?.stop_reason !== RESPONSES_STOP_REASON) {
		throw new Error(`responses stop_reason = ${JSON.stringify(res.json?.stop_reason)}, want ${JSON.stringify(RESPONSES_STOP_REASON)}`);
	}
}

// chatError fires a request the mock fails with a 404 error body; stream: true
// exercises the pre-first-chunk stream failure path.
async function chatError(id, stream) {
	const res = await request(
		"POST",
		"/v1/chat/completions",
		{
			model: requestedModel,
			messages: [{ role: "user", content: ERROR_TRIGGER }],
			...(stream ? { stream: true } : {}),
		},
		{
			"x-request-id": id,
		},
	);
	if (res.ok) {
		throw new Error(`error request (stream=${stream}) unexpectedly succeeded: ${res.text}`);
	}
	if (res.status !== 404) {
		throw new Error(`error request (stream=${stream}) status=${res.status}, want 404: ${res.text}`);
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
		"bifrost.request.id",
		"gen_ai.usage.input_tokens",
		"gen_ai.usage.output_tokens",
		"gen_ai.usage.total_tokens",
		"gen_ai.usage.cache_read.input_tokens",
		"gen_ai.usage.cache_creation.input_tokens",
		"gen_ai.response.model",
		"gen_ai.response.finish_reasons",
		"stop",
	]);
	// The plugin runs with disable_content_logging: true, so the input/output message content
	// ("hello world") must NOT reach the collector. This asserts the privacy guarantee holds
	// end-to-end (the mock upstream's chat text is "hello world" with a space; the model name
	// "hello-world" is hyphenated, so this cannot false-negative on the model bytes).
	assertBufferContainsNone("OTEL trace export", entry.body, ["hello world"]);
}

async function assertOtelMetricsReceived() {
	const entry = await poll("OTEL metrics receiver", 30000, () => state.otelMetricRequests.find((item) => item.bytes > 0));
	assertBufferContainsAll("OTEL metrics export", entry.body, [
		"bifrost-e2e",
		"bifrost_upstream_requests_total",
		"bifrost_success_requests_total",
		"bifrost_input_tokens_total",
		"bifrost_output_tokens_total",
		"bifrost_cache_read_input_tokens_total",
		"bifrost_cache_write_input_tokens_total",
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

// assertOtelErrorTrace checks the error span export carries the provider error
// attributes. The stream case pins core's deferred-span error stamping, which
// regressed silently before (spans exported with no gen_ai.error.* when a
// stream failed before its first chunk).
async function assertOtelErrorTrace(id, label) {
	const entry = await poll(`OTEL ${label} trace receiver`, 20000, () =>
		state.otelTraceRequests.find((item) => item.body.includes(Buffer.from(id))),
	);
	assertBufferContainsAll(`OTEL ${label} trace export`, entry.body, [
		id,
		"error.type",
		"invalid_request_error",
		"gen_ai.error.code",
		"model_not_found",
		"http.response.status_code",
	]);
}

// assertOtelResponsesFinishReason checks the Responses API span exports the
// finish reason. The chat path already emits gen_ai.response.finish_reasons;
// the Responses path reads the top-level stop_reason instead of per-choice
// finish reasons and used to drop it, leaving refusals invisible in OTEL.
async function assertOtelResponsesFinishReason() {
	const entry = await poll("OTEL responses trace receiver", 20000, () =>
		state.otelTraceRequests.find((item) => item.body.includes(Buffer.from(responsesRefusalRequestID))),
	);
	assertBufferContainsAll("OTEL responses trace export", entry.body, [
		responsesRefusalRequestID,
		"gen_ai.response.finish_reason",
		"gen_ai.response.finish_reasons",
		RESPONSES_STOP_REASON,
	]);
	assertBufferContainsNone("OTEL responses trace export", entry.body, ["I can't help with that."]);
}

// assertPrometheusErrorScrape checks bifrost_error_requests_total carries the
// status_code label for both the non-stream and stream error calls.
async function assertPrometheusErrorScrape() {
	await poll("Prometheus error scrape", 20000, async () => {
		const res = await request("GET", "/metrics");
		if (!res.ok) {
			throw new Error(`GET /metrics failed with ${res.status}: ${res.text}`);
		}
		const failures = [];
		for (const method of ["chat_completion", "chat_completion_stream"]) {
			const line = findPrometheusSample(res.text, "bifrost_error_requests_total", { provider: providerName, method, status_code: "404" });
			if (!line || parsePrometheusValue(line) < 1) {
				failures.push(method);
			}
		}
		if (failures.length > 0) {
			throw new Error(`bifrost_error_requests_total{status_code="404"} missing for: ${failures.join(", ")}`);
		}
		return true;
	});
}

function assertBufferContainsAll(name, body, values) {
	for (const value of values) {
		if (!body.includes(Buffer.from(value))) {
			throw new Error(`${name} is missing ${JSON.stringify(value)}`);
		}
	}
}

function assertBufferContainsNone(name, body, values) {
	for (const value of values) {
		if (body.includes(Buffer.from(value))) {
			throw new Error(`${name} unexpectedly contains ${JSON.stringify(value)} (content logging should be disabled)`);
		}
	}
}

function findPrometheusSample(metrics, metricName, labels) {
	const prefix = `${metricName}{`;
	return metrics
		.split("\n")
		.find((line) => line.startsWith(prefix) && Object.entries(labels).every(([key, value]) => line.includes(`${key}="${value}"`)));
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
			.some(
				(line) =>
					line.startsWith("bifrost_success_requests_total{") &&
					line.includes(`provider="${providerName}"`) &&
					(line.includes(`model="${modelName}"`) || line.includes(`model="${requestedModel}"`)),
			);
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
	const cacheReadLine = findPrometheusSample(metrics, "bifrost_cache_read_input_tokens_total", labels);
	const cacheWriteLine = findPrometheusSample(metrics, "bifrost_cache_write_input_tokens_total", labels);

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
	if (!cacheReadLine || parsePrometheusValue(cacheReadLine) !== 5) {
		throw new Error(`Prometheus cache_read_input_tokens = ${cacheReadLine ? parsePrometheusValue(cacheReadLine) : "missing"}, want 5`);
	}
	if (!cacheWriteLine || parsePrometheusValue(cacheWriteLine) !== 7) {
		throw new Error(`Prometheus cache_write_input_tokens = ${cacheWriteLine ? parsePrometheusValue(cacheWriteLine) : "missing"}, want 7`);
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
	if (
		!log.token_usage ||
		log.token_usage.prompt_tokens !== 30 ||
		log.token_usage.completion_tokens !== 10 ||
		log.token_usage.total_tokens !== 40
	) {
		throw new Error(`logging trace API returned incomplete token usage: ${JSON.stringify(log.token_usage)}`);
	}
	if (
		!Array.isArray(log.input_history) ||
		log.input_history.length !== 1 ||
		log.input_history[0]?.role !== "user" ||
		log.input_history[0]?.content !== "hello world"
	) {
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

// assertMetricsMatchLogs reconciles the telemetry pull-scrape counters against the
// logged usage for the SAME call. This is the exact invariant the customer reported
// violated ("Grafana dashboard != Bifrost logs usage"): the Prometheus /metrics
// counters and the logging store must agree on token counts. Because each run uses a
// unique provider+model, the counter series is scoped to this single chat call, so the
// cumulative counter equals precisely this call's usage — hence exact equality, not a
// threshold. assertPrometheusScrape and assertLoggingTrace check each side in isolation;
// only this cross-check catches a drift where both sides individually look "present".
function assertMetricsMatchLogs(metrics, log) {
	const labels = { provider: providerName, model: requestedModel, method: "chat_completion" };
	const inputLine = findPrometheusSample(metrics, "bifrost_input_tokens_total", labels);
	const outputLine = findPrometheusSample(metrics, "bifrost_output_tokens_total", labels);
	if (!inputLine || !outputLine) {
		throw new Error("metrics/logs reconciliation: /metrics is missing token counters for the LLM call");
	}
	const metricInput = parsePrometheusValue(inputLine);
	const metricOutput = parsePrometheusValue(outputLine);

	const logInput = log?.token_usage?.prompt_tokens;
	const logOutput = log?.token_usage?.completion_tokens;

	if (metricInput !== logInput) {
		throw new Error(
			`metrics/logs input-token mismatch: /metrics bifrost_input_tokens_total=${metricInput} but logs prompt_tokens=${logInput}`,
		);
	}
	if (metricOutput !== logOutput) {
		throw new Error(
			`metrics/logs output-token mismatch: /metrics bifrost_output_tokens_total=${metricOutput} but logs completion_tokens=${logOutput}`,
		);
	}
}

function assertMockProviderRequest(wantCount = 1) {
	if (state.mockRequests.length !== wantCount) {
		throw new Error(`expected ${wantCount} mock provider request(s), got ${state.mockRequests.length}`);
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
	if (
		!Array.isArray(body.messages) ||
		body.messages.length !== 1 ||
		body.messages[0]?.role !== "user" ||
		body.messages[0]?.content !== "hello world"
	) {
		throw new Error(`mock provider received incomplete messages: ${JSON.stringify(body.messages)}`);
	}
}

// main enables the telemetry and otel plugins against local receivers, runs
// the chat, error and Responses refusal scenarios, asserts every export, then
// restores the original plugin config and removes the ephemeral provider.
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
		const metrics = await assertPrometheusScrape();
		const log = await assertLoggingTrace();
		assertMetricsMatchLogs(metrics, log);

		// Error scenarios: non-stream provider 404, then a stream failing before
		// its first chunk. Both must export gen_ai.error.* span attributes and a
		// status_code-labeled error counter.
		await chatError(errorRequestID, false);
		await assertOtelErrorTrace(errorRequestID, "error");
		await chatError(streamErrorRequestID, true);
		await assertOtelErrorTrace(streamErrorRequestID, "stream-error");
		await assertPrometheusErrorScrape();

		// Responses API refusal: the span must carry finish_reason(s) just like a
		// chat completion does.
		await responsesRefusal();
		await assertOtelResponsesFinishReason();
		assertMockProviderRequest(4);

		console.log(`  OTEL trace exports received: ${state.otelTraceRequests.length}`);
		console.log(`  OTEL metric exports received: ${state.otelMetricRequests.length}`);
		console.log(`  Prometheus scrape includes provider="${providerName}" model="${requestedModel}"`);
		console.log(`  Metrics/logs token usage reconciled (scrape == logs)`);
		console.log(`  Logging trace API returned id="${requestID}"`);
		console.log(`  Error spans carry gen_ai.error.* and error counter has status_code (non-stream and stream).`);
		console.log(`  Responses API span carries gen_ai.response.finish_reason="${RESPONSES_STOP_REASON}".`);
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
			} else {
				await request("DELETE", "/api/plugins/telemetry");
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