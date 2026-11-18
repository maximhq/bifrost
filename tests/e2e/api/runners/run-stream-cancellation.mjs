#!/usr/bin/env node
// Exercises Bifrost's server-side stream cancellation path by opening a stream,
// reading the first bytes, then aborting the downstream request.

import { writeFileSync } from "node:fs";
import { generatedCharsInData, cancelledUsageFailure, charThresholdFor } from "./lib/cancel-stream-usage.mjs";
import { createRequire } from "node:module";
import { evaluateProbeStream } from "./lib/stream-probe-verdict.mjs";
import { evaluateNonStreamCancel, TERMINAL_STATUSES } from "./lib/nonstream-cancel-verdict.mjs";
import { evaluatePreStreamCancel } from "./lib/prestream-cancel-verdict.mjs";
import { resolveVariables } from "./lib/resolve-variables.mjs";

const require = createRequire(import.meta.url);

// Shared with newman-reporter-dbverify so provider alias normalization stays in sync.
const { resolvePricingEntry, expectedCostFromRow } = require("../lib/pricing");
const { readLogsDbUrl } = require("../lib/logs-db-url");
const { openSqliteReadonly } = require("../lib/sqlite-readonly");

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, cur, i, arr) => {
    if (cur.startsWith("--")) {
      const key = cur.slice(2);
      const next = arr[i + 1];
      acc.push([key, next && !next.startsWith("--") ? next : "true"]);
    }
    return acc;
  }, []),
);

const baseUrl = args["base-url"] || process.env.BASE_URL || "http://localhost:8080";
const providerFilter = (args.provider || "").toLowerCase();
const out = args.out || "tmp/stream-cancel-report.json";
const readTimeoutMs = Number(args["read-timeout-ms"] || 20000);
const abortAfterBytes = Number(args["abort-after-bytes"] || 1);
const expectEstimation = args["expect-estimation"] !== "false";
const abortAfterChars = Number(args["abort-after-chars"] || (expectEstimation ? 128 : 0));
const EARLY_USAGE_PROVIDERS = new Set(["anthropic"]);
// Non-streaming cancellation: abort this many ms after dispatch (before the full
// response can return) to force a client-disconnect on a non-streaming request,
// exercising the non-streaming client-disconnect billing path (#3357).
const nonStreamAbortMs = Number(args["nonstream-abort-ms"] || 300);
// Repeat the non-streaming abort this many times per provider. The worker hands the
// finished result to the caller through a select between a send into a cap-1 channel
// and ctx.Done(); with the caller gone both are ready and Go picks at random, so a
// single trial let a 50% race pass about half the time (#6972). Six trials miss such
// a race with probability 2^-6 per provider. The same trials also guard #7308 (a
// claimed delivery discarded on that coin flip, stranding the committed caller and
// leaving the row in `processing` forever); that window additionally needs the
// worker to win the claim race, so its per-trial catch rate is lower - a clean run
// here is necessary but not sufficient, and the deterministic pin lives in
// core/abandonedstream_test.go.
const parsedNonStreamTrials = Number(args["nonstream-trials"] || 6);
if (!Number.isSafeInteger(parsedNonStreamTrials) || parsedNonStreamTrials < 1) {
  // A bare `--nonstream-trials` parses as "true" and Number("true") is NaN, which
  // would silently produce zero trials; fail loudly instead.
  throw new Error(`--nonstream-trials must be a positive integer, got ${JSON.stringify(args["nonstream-trials"])}`);
}
const nonStreamTrials = parsedNonStreamTrials;
// Pre-header stream cancellation: abort this many ms after dispatch, before the
// upstream's first chunk lets Bifrost send headers. That path has no provider
// goroutine to end the request, so it leaked bifrost_active_requests and left
// the log row in `processing`. Trials are per route and provider. The telemetry
// transport hook alone balances the gauge, so only the log-row check proves the core fix.
const preStreamAbortMs = Number(args["prestream-abort-ms"] || 150);
if (!Number.isSafeInteger(preStreamAbortMs) || preStreamAbortMs < 1) {
  // NaN makes setTimeout fire at once, aborting before the request reaches Bifrost.
  throw new Error(`--prestream-abort-ms must be a positive integer, got ${JSON.stringify(args["prestream-abort-ms"])}`);
}
const parsedPreStreamTrials = Number(args["prestream-trials"] || 4);
if (!Number.isSafeInteger(parsedPreStreamTrials) || parsedPreStreamTrials < 1) {
  throw new Error(`--prestream-trials must be a positive integer, got ${JSON.stringify(args["prestream-trials"])}`);
}
const preStreamTrials = parsedPreStreamTrials;
// Cost verification: after aborting a stream mid-flight, confirm the logs DB row
// for that request carries a cost (#3357). Needs a logs DB. Skip with --no-cost-check.
const skipCostCheck = args["no-cost-check"] === "true";
const logsDbUrlArg = args["logs-db-url"] || process.env.BIFROST_LOGS_DB_URL || "";
const configPathArg = args["config"] || process.env.BIFROST_CONFIG_PATH || "config.json";
const serverWorkingDir = args["server-working-dir"] || process.cwd();
const pricingUrl =
  args["pricing-url"] || process.env.BIFROST_PRICING_URL || "https://getbifrost.ai/datasheet";
// Every provider must account for output actually received before cancellation.
const BILLED_CANCEL_COUNTERS = ["bifrost_error_requests_total", "bifrost_input_tokens_total", "bifrost_output_tokens_total", "bifrost_cost_total"];

// /metrics sits behind the API auth chain: the OSS setup lock, or dashboard credentials once auth is on.
const adminAuthHeader = process.env.BIFROST_E2E_AUTH_HEADER || "";
const setupToken = (process.env.BIFROST_E2E_SETUP_TOKEN || process.env.BIFROST_SETUP_TOKEN || "bifrost-e2e-setup-token").trim();
const metricsHeaders = {
  ...(adminAuthHeader ? { Authorization: adminAuthHeader } : {}),
  ...(setupToken ? { "X-Bifrost-Setup-Token": setupToken } : {}),
};

// Sums each BILLED_CANCEL_COUNTERS family across every series labelled with provider.
async function scrapeProviderCounters(provider) {
  const res = await fetch(`${baseUrl}/metrics`, { headers: metricsHeaders });
  const text = await res.text();
  if (!res.ok) throw new Error(`GET /metrics returned HTTP ${res.status}: ${text.slice(0, 300)}`);
  const sums = Object.fromEntries(BILLED_CANCEL_COUNTERS.map((n) => [n, 0]));
  const providerLabel = new RegExp(`(^|,)provider="${provider}"(,|$)`);
  for (const line of text.split("\n")) {
    const m = /^(\w+)\{([^}]*)\}\s+(\S+)/.exec(line);
    if (m && m[1] in sums && providerLabel.test(m[2])) sums[m[1]] += Number(m[3]);
  }
  return sums;
}

// Reads bifrost_active_requests for one method label; undefined when the series does not exist yet.
async function scrapeActiveRequests(method) {
  const res = await fetch(`${baseUrl}/metrics`, { headers: metricsHeaders });
  const text = await res.text();
  if (!res.ok) throw new Error(`GET /metrics returned HTTP ${res.status}: ${text.slice(0, 300)}`);
  const methodLabel = new RegExp(`(^|,)method="${method}"(,|$)`);
  let value;
  for (const line of text.split("\n")) {
    const m = /^bifrost_active_requests\{([^}]*)\}\s+(\S+)/.exec(line);
    if (m && methodLabel.test(m[1])) value = (value ?? 0) + Number(m[2]);
  }
  return value;
}

// Waits up to ~10s for the gauge to go idle (0, or no series yet) and returns the last reading.
// A slot still held by an earlier case or other traffic fails the verdict rather than becoming the baseline.
async function idleActiveRequests(method) {
  let value = await scrapeActiveRequests(method);
  for (let i = 0; i < 20 && (value ?? 0) > 0; i++) {
    await new Promise((r) => setTimeout(r, 500));
    value = await scrapeActiveRequests(method);
  }
  return value;
}

// Polls the gauge until it settles back to before (abandoned streams end asynchronously in the worker).
async function pollActiveRequests(method, before) {
  let value;
  for (const ms of [300, 700, 1200, 2000, 3000]) {
    await new Promise((r) => setTimeout(r, ms));
    value = await scrapeActiveRequests(method);
    if (value !== undefined && value === (before ?? 0)) return value;
  }
  return value;
}

// Polls /metrics until the cancel's input tokens and cost land (the telemetry
// post-hook records asynchronously). Returns an error string, or undefined on success.
async function verifyCancelMetrics(provider, before) {
  let delta = {};
  for (const ms of [300, 700, 1200, 2000]) {
    await new Promise((r) => setTimeout(r, ms));
    const after = await scrapeProviderCounters(provider);
    delta = Object.fromEntries(BILLED_CANCEL_COUNTERS.map((n) => [n, after[n] - before[n]]));
    if (delta.bifrost_input_tokens_total > 0 && (!expectEstimation || delta.bifrost_output_tokens_total > 0) && delta.bifrost_cost_total > 0) return undefined;
  }
  return `cancelled ${provider} stream billed usage missing from /metrics: ${BILLED_CANCEL_COUNTERS.map((n) => `${n} +${delta[n]}`).join(", ")}`;
}

// ─── logs DB + datasheet helpers (cost verification) ──────────────────────────
function resolveLogsDbUrl() {
  return logsDbUrlArg || readLogsDbUrl(configPathArg, serverWorkingDir);
}

async function connectLogsDb(url) {
  if (/^postgres/i.test(url)) {
    const pg = require("pg");
    const client = new pg.Client({ connectionString: url });
    await client.connect();
    return {
      query: async (sql, p) => (await client.query(sql, p)).rows,
      close: () => client.end().catch(() => {}),
    };
  }
  const sdb = openSqliteReadonly(url.replace(/^sqlite:\/\//i, ""));
  return {
    query: async (sql, p) => sdb.prepare(sql.replace(/\$\d+/g, "?")).all(...p),
    close: () => {
      try {
        sdb.close();
      } catch (_) {}
    },
  };
}

// Polls until the row reaches a terminal status; returns the last row seen.
async function pollTerminalLogRow(db, id) {
  let last = null;
  for (const ms of [300, 700, 1200, 2000]) {
    await new Promise((r) => setTimeout(r, ms));
    const rows = await db.query("SELECT status FROM logs WHERE id = $1", [id]);
    if (rows && rows.length) {
      last = rows[0];
      if (TERMINAL_STATUSES.has(last.status)) return last;
    }
  }
  return last;
}

async function pollLogRow(db, id) {
  const sql =
    "SELECT cost, prompt_tokens, completion_tokens, total_tokens, cached_read_tokens, token_usage, model, provider, status, service_tier FROM logs WHERE id = $1";
  let last = null;
  for (const ms of [300, 700, 1200, 2000]) {
    await new Promise((r) => setTimeout(r, ms));
    const rows = await db.query(sql, [id]);
    if (rows && rows.length) {
      last = rows[0];
      if (last.cost != null && Number(last.cost) > 0) return last;
    }
  }
  return last;
}

// Shared with the dbverify reporter via lib/pricing: recomputes at the rates of
// the served tier recorded in the row's service_tier column.
function expectedCost(entry, row) {
  return expectedCostFromRow(entry, row);
}

// A streaming cancel is logged status=cancelled (dedicated status since #4930;
// older builds logged error). Non-streaming trials are judged by
// lib/nonstream-cancel-verdict.mjs instead.
function statusIsCancelOutcome(status) {
  return status === "cancelled" || status === "error";
}

// Compare the logged cost against the datasheet-recomputed cost. Returns
// {verdict, detail, fail}; accuracy is skipped (PASS) when no datasheet entry resolves
// or the request is tiered (>128k tokens).
function costAccuracyVerdict(sheet, row, cost, tokens, kind) {
  const entry = resolvePricingEntry(sheet, row.model, row.provider);
  if (entry && tokens <= 128000) {
    const exp = expectedCost(entry, row),
      tol = Math.max(1e-9, 1e-4 * exp);
    if (Math.abs(cost - exp) > tol) {
      return {
        verdict: "FAIL",
        detail: `${kind} inaccurate logged=$${cost} expected=$${exp}`,
        fail: true,
      };
    }
    return {
      verdict: "PASS",
      detail: `${kind} cost=$${cost} accurate (expected=$${exp})`,
      fail: false,
    };
  }
  return {
    verdict: "PASS",
    detail: `${kind} cost=$${cost} (accuracy skipped: no datasheet entry / tiered)`,
    fail: false,
  };
}

const streamCases = [
  {
    provider: "openai",
    name: "openai/gpt-4o-mini chat stream",
    path: "/v1/chat/completions",
    body: {
      model: "openai/gpt-4o-mini",
      messages: [{ role: "user", content: "Count from 1 to 100 slowly." }],
      stream: true,
    },
  },
  {
    provider: "anthropic",
    name: "anthropic/claude-haiku-4-5 chat stream",
    path: "/v1/chat/completions",
    body: {
      model: "anthropic/claude-haiku-4-5",
      messages: [{ role: "user", content: "Count from 1 to 100 slowly." }],
      stream: true,
      max_tokens: 512,
    },
  },
  {
    provider: "bedrock",
    name: "bedrock/nova-lite chat stream",
    path: "/v1/chat/completions",
    body: {
      model: "bedrock/us.amazon.nova-lite-v1:0",
      messages: [{ role: "user", content: "Count from 1 to 100 slowly." }],
      stream: true,
    },
  },
  {
    provider: "gemini",
    name: "gemini/gemini-2.5-flash chat stream",
    path: "/v1/chat/completions",
    body: {
      model: "gemini/gemini-2.5-flash",
      messages: [{ role: "user", content: "Count from 1 to 100 slowly." }],
      stream: true,
    },
  },
  {
    provider: "vertex",
    name: "vertex/gemini-2.5-flash chat stream",
    path: "/v1/chat/completions",
    body: {
      model: "vertex/gemini-2.5-flash",
      messages: [{ role: "user", content: "Count from 1 to 100 slowly." }],
      stream: true,
    },
  },
  {
    provider: "azure",
    name: "azure deployment chat stream",
    path: "/v1/chat/completions",
    body: {
      model: "azure/{{azureDeployment}}",
      messages: [{ role: "user", content: "Count from 1 to 100 slowly." }],
      stream: true,
    },
  },
];

// Derive a non-streaming cancellation case per provider: same model, stream:false,
// aborted shortly after dispatch (before the response returns). A higher max_tokens
// makes generation last long enough that the abort lands while the request is still
// in flight, producing a real client-disconnect on a non-streaming call.
const nonStreamCases = streamCases.flatMap((c) =>
  Array.from({ length: nonStreamTrials }, (_, i) => ({
    provider: c.provider,
    name: `${c.body.model} non-stream cancel #${i + 1}/${nonStreamTrials}`,
    path: c.path,
    nonStream: true,
    body: { ...c.body, stream: false, max_tokens: Math.max(Number(c.body.max_tokens) || 0, 1024) },
  })),
);

const responsesStreamCases = streamCases.map((c) => ({
  provider:c.provider, name:`${c.body.model} Responses cancelled usage`, path:"/v1/responses",
  body:{model:c.body.model,input:"Count from 1 to 200, one number per line.",stream:true,max_output_tokens:1024},
}));
const nativeAnthropicStreamCase = {
  provider:"anthropic", name:"Anthropic native messages cancelled usage", path:"/anthropic/v1/messages",
  body:{model:"claude-haiku-4-5",messages:[{role:"user",content:"Count from 1 to 200, one number per line."}],stream:true,max_tokens:1024},
};
// One pre-header abort group per provider and route; the customer-visible leak was mostly responses_stream.
const preStreamCases = [
  ...streamCases.map((c) => ({ ...c, method: "chat_completion_stream" })),
  ...responsesStreamCases.map((c) => ({ ...c, method: "responses_stream" })),
].map((c) => ({
  provider: c.provider,
  name: `${c.body.model} ${c.path} pre-header cancel x${preStreamTrials}`,
  path: c.path,
  body: c.body,
  method: c.method,
  preStream: true,
}));
const cases = [...streamCases, ...responsesStreamCases, nativeAnthropicStreamCase, ...nonStreamCases, ...preStreamCases].filter(
  (c) => !providerFilter || c.provider === providerFilter,
);

// Non-streaming cancel: dispatch the request, then abort the socket before the full
// response returns. If the response comes back before the abort fires, the request
// completed and we couldn't induce a cancellation → flagged racedToCompletion, which
// the verdict treats as a failed trial (it never exercised a disconnect).
async function runNonStreamCase(testCase) {
  const controller = new AbortController();
  const requestId = `nonstream-cancel-${testCase.provider}-${crypto.randomUUID()}`;
  const timer = setTimeout(
    () => controller.abort(new Error("intentional non-stream abort before response")),
    nonStreamAbortMs,
  );
  try {
    const response = await fetch(`${baseUrl}${testCase.path}`, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        "x-request-id": requestId,
        "x-bf-expect-cost": "1",
      },
      body: JSON.stringify(resolveVariables(testCase.body)),
      signal: controller.signal,
    });
    // fetch() resolves once the status and headers are in, possibly before the body.
    // Keep the abort armed until the body is drained: an abort that lands mid-body
    // rejects text() with AbortError and is handled as an aborted trial below.
    await response.text();
    // Reaching here means the whole response returned before our abort fired.
    clearTimeout(timer);
    return {
      ...testCase,
      requestId,
      ok: true,
      status: response.status,
      aborted: false,
      racedToCompletion: true,
      error: "response returned before abort (could not induce cancellation)",
    };
  } catch (err) {
    return {
      ...testCase,
      requestId,
      ok: controller.signal.aborted,
      aborted: controller.signal.aborted,
      error: err?.message || String(err),
    };
  } finally {
    clearTimeout(timer);
  }
}

// Pre-header stream cancel: abort each trial before the response headers arrive,
// then require bifrost_active_requests for the route's method to go from idle back
// to idle. A trial whose headers win the race is aborted anyway and still counts
// toward the gauge, but does not exercise the pre-header path.
async function runPreStreamCase(testCase) {
  const requestIds = [];
  const errors = [];
  let abortedBeforeHeaders = 0;
  let gaugeBefore;
  try {
    gaugeBefore = await idleActiveRequests(testCase.method);
  } catch (err) {
    return { ...testCase, ok: false, error: err?.message || String(err) };
  }
  for (let i = 0; i < preStreamTrials; i++) {
    const controller = new AbortController();
    const requestId = `prestream-cancel-${testCase.provider}-${crypto.randomUUID()}`;
    const timer = setTimeout(
      () => controller.abort(new Error("intentional stream abort before headers")),
      preStreamAbortMs,
    );
    try {
      const response = await fetch(`${baseUrl}${testCase.path}`, {
        method: "POST",
        headers: { "content-type": "application/json", "x-request-id": requestId },
        body: JSON.stringify(resolveVariables(testCase.body)),
        signal: controller.signal,
      });
      // Headers beat the abort: disconnect now, outside the path under test.
      clearTimeout(timer);
      if (!response.ok) errors.push(`trial ${i + 1}: HTTP ${response.status}`);
      controller.abort(new Error("headers arrived before the abort"));
      await response.body?.cancel().catch(() => {});
    } catch (err) {
      if (controller.signal.aborted) {
        abortedBeforeHeaders++;
        requestIds.push(requestId);
      } else {
        errors.push(`trial ${i + 1}: ${err?.message || String(err)}`);
      }
    } finally {
      clearTimeout(timer);
    }
  }
  let gaugeAfter;
  try {
    gaugeAfter = await pollActiveRequests(testCase.method, gaugeBefore);
  } catch (err) {
    errors.push(err?.message || String(err));
  }
  const v = evaluatePreStreamCancel({
    method: testCase.method,
    trials: preStreamTrials,
    abortedBeforeHeaders,
    gaugeBefore,
    gaugeAfter,
  });
  const ok = v.verdict === "PASS" && errors.length === 0;
  return {
    ...testCase,
    requestIds,
    abortedBeforeHeaders,
    gaugeBefore,
    gaugeAfter,
    ok,
    error: ok ? undefined : [v.verdict === "PASS" ? undefined : v.detail, ...errors].filter(Boolean).join("; "),
  };
}

async function runCase(testCase) {
  if (testCase.nonStream) return runNonStreamCase(testCase);
  if (testCase.preStream) return runPreStreamCase(testCase);
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(new Error("stream read timeout")), readTimeoutMs);
  let bytesRead = 0;
  // Pin a known request id so the cost-verification pass can locate the log row.
  const requestId = `stream-cancel-${testCase.provider}-${crypto.randomUUID()}`;

  try {
    const metricsBefore = expectEstimation || EARLY_USAGE_PROVIDERS.has(testCase.provider) ? await scrapeProviderCounters(testCase.provider) : undefined;
    const response = await fetch(`${baseUrl}${testCase.path}`, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        "x-request-id": requestId,
        "x-bf-expect-cost": "1",
      },
      body: JSON.stringify(resolveVariables(testCase.body)),
      signal: controller.signal,
    });

    const contentType = response.headers.get("content-type") || "";
    if (!response.ok) {
      const body = await response.text().catch(() => "");
      return {
        ...testCase,
        ok: false,
        status: response.status,
        contentType,
        error: `stream returned HTTP ${response.status}: ${body.slice(0, 500)}`,
      };
    }
    if (!/event-stream|vnd\.amazon\.eventstream|octet-stream/i.test(contentType)) {
      return {
        ...testCase,
        ok: false,
        status: response.status,
        contentType,
        error: `expected streaming content-type, got ${contentType || "<empty>"}`,
      };
    }
    if (!response.body) {
      return {
        ...testCase,
        ok: false,
        status: response.status,
        contentType,
        error: "response body is not readable",
      };
    }

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let pending = "";
    let generatedChars = 0;
    const charThreshold = charThresholdFor(contentType, abortAfterChars);
    while (bytesRead < abortAfterBytes || generatedChars < charThreshold) {
      const { done, value } = await reader.read();
      if (done) throw new Error(`stream completed before cancellation threshold (${generatedChars} generated characters)`);
      bytesRead += value?.byteLength || 0;
      pending += decoder.decode(value, {stream:true});
      const lines = pending.split("\n");
      pending = lines.pop();
      for (const line of lines) {
        if (!line.startsWith("data:")) continue;
        const payload = line.slice(5).trim();
        if (!payload || payload === "[DONE]") continue;
        generatedChars += generatedCharsInData(payload);
      }
    }

    controller.abort(new Error("intentional downstream abort after first stream bytes"));
    await reader.cancel("intentional downstream abort").catch(() => {});

    const metricsError =
      bytesRead > 0 && metricsBefore ? await verifyCancelMetrics(testCase.provider, metricsBefore) : undefined;
    return {
      ...testCase,
      requestId,
      ok: bytesRead > 0 && !metricsError,
      status: response.status,
      contentType,
      bytesRead,
      aborted: true,
      error: bytesRead > 0 ? metricsError : "stream ended before any bytes were read",
    };
  } catch (err) {
    return {
      ...testCase,
      requestId,
      ok: false,
      bytesRead,
      aborted: controller.signal.aborted,
      error: err?.message || String(err),
    };
  } finally {
    clearTimeout(timer);
  }
}

// ─── Pool-health probe: a cancelled stream must not poison the next request ───
// Cancelling a stream used to run fasthttp's stream-close callback while the SSE
// reader was still inside (*requestStream).Read, returning the *requestStream and
// the connection's *bufio.Reader to sync.Pools underneath it. A later request then
// acquired an aliased object and read another request's bytes, surfacing as
// "provider returned non-SSE response for streaming request" and persisting until
// the process restarted (maximhq/bifrost#6143).
//
// The abort cases above exercise the cancellation itself but never check what it
// left behind. This probe does: after the cancellations, a plain uncancelled stream
// against the same gateway must still complete with well-formed SSE.
async function runPoolHealthProbe(testCase) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(new Error("probe read timeout")), readTimeoutMs);
  const requestId = `stream-pool-probe-${testCase.provider}-${crypto.randomUUID()}`;
  // Keep the probe cheap: it only has to prove the connection is usable.
  const body = { ...resolveVariables(testCase.body), max_tokens: 16 };

  try {
    const response = await fetch(`${baseUrl}${testCase.path}`, {
      method: "POST",
      headers: { "content-type": "application/json", "x-request-id": requestId },
      body: JSON.stringify(body),
      signal: controller.signal,
    });

    const contentType = response.headers.get("content-type") || "";
    if (!response.ok) {
      const text = await response.text().catch(() => "");
      return {
        provider: testCase.provider,
        name: `${testCase.name} - pool health after cancellation`,
        requestId,
        ok: false,
        status: response.status,
        contentType,
        error: `probe returned HTTP ${response.status}: ${text.slice(0, 500)}`,
      };
    }

    // Decoded unconditionally, including for the binary streaming types: the
    // verdict below ignores the text for those, but a failing probe still has to
    // report what came back instead, and that body sample is the whole
    // diagnostic. Counting bytes separately keeps bytesRead honest - decoding
    // collapses multi-byte sequences, so text.length understates a binary frame.
    let text = "";
    let bytesRead = 0;
    if (response.body) {
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        bytesRead += value?.byteLength || 0;
        text += decoder.decode(value, { stream: true });
      }
    }

    // The regression signature: a 200 whose body is not SSE, because the reader was
    // handed a connection another request had already consumed from.
    const { ok, error } = evaluateProbeStream({ contentType, bytesRead, text });

    return {
      provider: testCase.provider,
      name: `${testCase.name} - pool health after cancellation`,
      requestId,
      ok,
      status: response.status,
      contentType,
      bytesRead,
      error,
    };
  } catch (err) {
    return {
      provider: testCase.provider,
      name: `${testCase.name} - pool health after cancellation`,
      requestId,
      ok: false,
      error: err?.message || String(err),
    };
  } finally {
    clearTimeout(timer);
  }
}

if (cases.length === 0) {
  console.error(`[stream-cancel] no cases selected for provider=${providerFilter || "-"}`);
  process.exit(2);
}

const results = [];
for (const testCase of cases) {
  process.stderr.write(`[stream-cancel] ${testCase.name} ... `);
  const result = await runCase(testCase);
  results.push(result);
  process.stderr.write(result.ok ? "ok\n" : `failed (${result.error})\n`);
}

// One probe per selected streaming provider, after all of its aborts have landed.
const probeCases = streamCases.filter(
  (c) => !providerFilter || c.provider === providerFilter,
);
const poolProbes = [];
for (const testCase of probeCases) {
  process.stderr.write(`[stream-cancel] pool health ${testCase.provider} ... `);
  const probe = await runPoolHealthProbe(testCase);
  poolProbes.push(probe);
  process.stderr.write(probe.ok ? "ok\n" : `failed (${probe.error})\n`);
}
const poolProbeFailures = poolProbes.filter((p) => !p.ok).length;

const report = {
  baseUrl,
  provider: providerFilter || null,
  total: results.length,
  failed: results.filter((r) => !r.ok).length,
  results,
  poolProbes,
  poolProbeFailures,
};

// ─── Cost verification: a cancelled stream's log row must still carry cost (#3357) ───
let costFailures = 0;
if (skipCostCheck) {
  console.error("[stream-cancel] cost verification skipped (--no-cost-check)");
} else {
  const logsUrl = resolveLogsDbUrl();
  if (!logsUrl) {
    console.error(
      "[stream-cancel] no logs DB configured (set BIFROST_LOGS_DB_URL or a config.json logs_store); skipping cost verification",
    );
  } else {
    let db = null;
    try {
      db = await connectLogsDb(logsUrl);
    } catch (e) {
      console.error(
        `[stream-cancel] could not connect to logs DB: ${e.message}; skipping cost verification`,
      );
    }
    if (db) {
      let sheet = null;
      try {
        const resp = await fetch(pricingUrl);
        sheet = resp.ok ? await resp.json() : null;
      } catch (_) {
        sheet = null;
      }
      if (!sheet)
        console.error(
          `[stream-cancel] pricing datasheet (${pricingUrl}) unavailable; cost-accuracy checks skipped`,
        );
      for (const r of results) {
        if (r.preStream) {
          // Each stream aborted before headers must still reach a terminal log status.
          const stuck = [];
          for (const id of r.requestIds || []) {
            const v = evaluateNonStreamCancel({ row: await pollTerminalLogRow(db, id), aborted: true });
            if (v.verdict === "FAIL") stuck.push(`${id}: ${v.detail}`);
          }
          r.costCheck = stuck.length ? "FAIL" : (r.requestIds || []).length ? "PASS" : "SKIP";
          r.costDetail = stuck.length
            ? `${stuck.length}/${(r.requestIds || []).length} pre-header abort(s) without a terminal log row - ${stuck[0]}`
            : `${(r.requestIds || []).length} pre-header abort(s) logged with a terminal status`;
          if (stuck.length) costFailures++;
          console.error(`[stream-cancel] log status ${r.provider} (pre-header): ${r.costCheck} — ${r.costDetail}`);
          continue;
        }
        const kind = r.nonStream ? "non-stream" : "stream";
        if (r.nonStream) {
          // Every abandoned non-streaming request must reach a terminal log status,
          // whether or not usage was recorded (#6972); see lib/nonstream-cancel-verdict.mjs.
          const row = r.aborted && r.requestId ? await pollLogRow(db, r.requestId) : null;
          const v = evaluateNonStreamCancel({
            row,
            racedToCompletion: r.racedToCompletion,
            aborted: r.aborted,
          });
          r.costCheck = v.verdict;
          r.costDetail = v.detail;
          if (v.verdict === "FAIL") costFailures++;
          if (v.verdict === "PASS") {
            // Cost presence is not required (the upstream call is usually cut with a
            // 499), but a cost that WAS recorded must still be accurate.
            const cost = Number(row.cost || 0),
              tokens = Number(row.total_tokens || 0);
            if (cost > 0 && tokens > 0) {
              const cv = costAccuracyVerdict(sheet, row, cost, tokens, kind);
              if (cv.fail) costFailures++;
              r.costCheck = cv.verdict;
              r.costDetail = `${v.detail}; ${cv.detail}`;
            }
          }
          console.error(
            `[stream-cancel] cost ${r.provider} (${kind}): ${r.costCheck}${r.costDetail ? " — " + r.costDetail : ""}`,
          );
          continue;
        }
        if (!r.aborted || !r.requestId) {
          r.costCheck = "SKIP";
          continue;
        }
        let row = await pollLogRow(db, r.requestId);
        // Raced to completion: a success row WITH terminal usage means the upstream
        // stream finished before the abort was observed - Gemini-family usage only
        // arrives in the terminal chunk, so the provider generated and billed the
        // full response and success is the honest log for that attempt (seen live
        // on vertex/gemini-2.5-flash 2026-09-18: 325 completion tokens, whole tail
        // in one burst, no SSE write left to fail). Retry the abort; a real
        // disconnect-handling regression logs success on every attempt. A success
        // row WITHOUT usage stays an immediate FAIL - that would be a mislabel,
        // not a lost race.
        let racedRetries = 0;
        while (
          row &&
          row.status === "success" &&
          Number(row.total_tokens) > 0 &&
          racedRetries < 2
        ) {
          racedRetries++;
          const rerun = await runCase({
            provider: r.provider,
            name: r.name,
            path: r.path,
            body: r.body,
          });
          if (!(rerun.ok && rerun.aborted && rerun.requestId)) break;
          r.requestId = rerun.requestId;
          row = await pollLogRow(db, rerun.requestId);
        }
        if (racedRetries > 0) r.racedToCompletionRetries = racedRetries;
        if (!row) {
          r.costCheck = "FAIL";
          r.costDetail = `no log row for ${r.requestId}`;
          costFailures++;
        } else if (!statusIsCancelOutcome(row.status)) {
          // A streaming cancel logs status=cancelled (#4831; error on older builds).
          r.costCheck = "FAIL";
          r.costDetail = `status=${row.status}, unexpected for ${kind} cancel`;
          costFailures++;
        } else {
          const cost = Number(row.cost || 0),
            tokens = Number(row.total_tokens || 0);
          const failure = expectEstimation ? cancelledUsageFailure(row, r.provider) : EARLY_USAGE_PROVIDERS.has(r.provider) && !(cost > 0 && tokens > 0) ? `cancelled ${r.provider} logged no usage` : undefined;
          if (failure) {
            r.costCheck = "FAIL";
            r.costDetail = failure;
            costFailures++;
          } else if (!expectEstimation && !(cost > 0 && tokens > 0)) {
            r.costCheck = "PASS";
            r.costDetail = "Estimation disabled; provider usage not available before cancellation";
          } else {
            const v = costAccuracyVerdict(sheet, row, cost, tokens, kind);
            if (v.fail) costFailures++;
            r.costDetail = v.detail;
            r.costCheck = v.verdict;
          }
        }
        console.error(
          `[stream-cancel] cost ${r.provider} (${kind}): ${r.costCheck}${r.costDetail ? " — " + r.costDetail : ""}`,
        );
      }
      await db.close();
    }
  }
}
report.costFailures = costFailures;

writeFileSync(out, `${JSON.stringify(report, null, 2)}\n`);

if (report.failed > 0 || costFailures > 0 || poolProbeFailures > 0) {
  console.error(
    `[stream-cancel] ${report.failed}/${report.total} stream case(s) failed, ${costFailures} cost check(s) failed, ${poolProbeFailures}/${poolProbes.length} pool-health probe(s) failed; report: ${out}`,
  );
  process.exit(1);
}

console.error(
  `[stream-cancel] all ${report.total} case(s) passed (incl. cost checks); report: ${out}`,
);
