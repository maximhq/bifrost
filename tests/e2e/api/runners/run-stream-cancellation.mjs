#!/usr/bin/env node
// Exercises Bifrost's server-side stream cancellation path by opening a stream,
// reading the first bytes, then aborting the downstream request.

import { writeFileSync, readFileSync } from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);

// Shared with newman-reporter-dbverify so provider alias normalization stays in sync.
const { resolvePricingEntry } = require("../lib/pricing");

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
// Non-streaming cancellation: abort this many ms after dispatch (before the full
// response can return) to force a client-disconnect on a non-streaming request,
// exercising the non-streaming client-disconnect billing path (#3357).
const nonStreamAbortMs = Number(args["nonstream-abort-ms"] || 300);
// Cost verification: after aborting a stream mid-flight, confirm the logs DB row
// for that request carries a cost (#3357). Needs a logs DB. Skip with --no-cost-check.
const skipCostCheck = args["no-cost-check"] === "true";
const logsDbUrlArg = args["logs-db-url"] || process.env.BIFROST_LOGS_DB_URL || "";
const configPathArg = args["config"] || process.env.BIFROST_CONFIG_PATH || "config.json";
const pricingUrl =
  args["pricing-url"] || process.env.BIFROST_PRICING_URL || "https://getbifrost.ai/datasheet";
// Providers that emit usage in the FIRST stream event → a cancel-after-first-byte
// deterministically has billable usage, so cost MUST be > 0. Only native Anthropic
// qualifies: its message_start event carries input_tokens + cache tokens immediately
// (https://platform.claude.com/docs/en/api/messages-streaming).
// Everyone else delivers usage only in a terminal event, so an early cancel may
// legitimately have none — for those we require only that the error row exists.
// Notably Bedrock (Converse) is NOT early-usage: per the ConverseStream response
// schema the `usage` field lives solely in the terminal `metadata`
// (ConverseStreamMetadataEvent), after messageStop — messageStart carries only role
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ConverseStream.html).
// So a Bedrock stream cancelled before metadata has nothing to bill, same as the
// OpenAI family — the fix still bills Bedrock whenever metadata arrives before failure.
const EARLY_USAGE_PROVIDERS = new Set(["anthropic"]);

// ─── logs DB + datasheet helpers (cost verification) ──────────────────────────
function resolveLogsDbUrl() {
  if (logsDbUrlArg) return logsDbUrlArg;
  try {
    const cfg = JSON.parse(readFileSync(configPathArg, "utf8"));
    const ls = cfg.logs_store;
    if (!ls || !ls.enabled) return "";
    if (ls.type === "sqlite") {
      const p = ls.config && ls.config.path;
      if (!p || typeof p !== "string") return "";
      const abs = path.isAbsolute(p) ? p : path.resolve(path.dirname(configPathArg), p);
      return "sqlite://" + abs;
    }
    if (ls.type === "postgres") {
      const c = ls.config || {};
      const host = c.host || "localhost",
        port = c.port || "5432",
        user = c.user || "bifrost";
      const pass = c.password || "",
        db = c.db_name || "bifrost",
        ssl = c.ssl_mode || "disable";
      return `postgresql://${user}:${encodeURIComponent(pass)}@${host}:${port}/${db}?sslmode=${ssl}`;
    }
  } catch (_) {
    /* no config / unreadable → skip */
  }
  return "";
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
  const Database = require("better-sqlite3");
  const sdb = new Database(url.replace(/^sqlite:\/\//i, ""), { readonly: true });
  return {
    query: async (sql, p) => sdb.prepare(sql.replace(/\$\d+/g, "?")).all(...p),
    close: () => {
      try {
        sdb.close();
      } catch (_) {}
    },
  };
}

async function pollLogRow(db, id) {
  const sql =
    "SELECT cost, prompt_tokens, completion_tokens, total_tokens, cached_read_tokens, token_usage, model, provider, status FROM logs WHERE id = $1";
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

function expectedCost(entry, row) {
  const input = entry.input_cost_per_token || 0,
    output = entry.output_cost_per_token || 0;
  const cr = entry.cache_read_input_token_cost || 0,
    cw = entry.cache_creation_input_token_cost || 0;
  const prompt = Number(row.prompt_tokens || 0),
    completion = Number(row.completion_tokens || 0);
  let cachedRead = Number(row.cached_read_tokens || 0),
    cachedWrite = 0;
  if (row.token_usage) {
    try {
      const d = JSON.parse(row.token_usage)?.prompt_tokens_details;
      if (d) {
        if (cachedRead === 0 && d.cached_read_tokens) cachedRead = Number(d.cached_read_tokens);
        if (d.cached_write_tokens) cachedWrite = Number(d.cached_write_tokens);
      }
    } catch (_) {
      /* ignore */
    }
  }
  cachedRead = Math.min(cachedRead, prompt);
  cachedWrite = Math.min(cachedWrite, Math.max(0, prompt - cachedRead));
  const nonCached = Math.max(0, prompt - cachedRead - cachedWrite);
  return nonCached * input + cachedRead * cr + cachedWrite * cw + completion * output;
}

// A streaming cancel is logged status=cancelled (dedicated status since #4930;
// older builds logged error); a non-streaming cancel may finish server-side
// and log success - accept any terminal cancel outcome per kind.
function statusIsCancelOutcome(status, nonStream) {
  if (nonStream) return status === "cancelled" || status === "error" || status === "success";
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
const nonStreamCases = streamCases.map((c) => ({
  provider: c.provider,
  name: `${c.body.model} non-stream cancel`,
  path: c.path,
  nonStream: true,
  body: { ...c.body, stream: false, max_tokens: Math.max(Number(c.body.max_tokens) || 0, 1024) },
}));

const cases = [...streamCases, ...nonStreamCases].filter(
  (c) => !providerFilter || c.provider === providerFilter,
);

const resolveVariables = (value) => {
  if (typeof value === "string") {
    return value.replaceAll(
      "{{azureDeployment}}",
      process.env.AZURE_DEPLOYMENT || process.env.BIFROST_AZURE_DEPLOYMENT || "gpt-4o-mini",
    );
  }
  if (Array.isArray(value)) return value.map(resolveVariables);
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, resolveVariables(v)]));
  }
  return value;
};

// Non-streaming cancel: dispatch the request, then abort the socket before the full
// response returns. If the response comes back before the abort fires, the request
// completed and we couldn't induce a cancellation → flagged racedToCompletion (SKIP).
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
    // Reaching here means the response returned before our abort fired.
    clearTimeout(timer);
    await response.text().catch(() => {});
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

async function runCase(testCase) {
  if (testCase.nonStream) return runNonStreamCase(testCase);
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(new Error("stream read timeout")), readTimeoutMs);
  let bytesRead = 0;
  // Pin a known request id so the cost-verification pass can locate the log row.
  const requestId = `stream-cancel-${testCase.provider}-${crypto.randomUUID()}`;

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
    while (bytesRead < abortAfterBytes) {
      const { done, value } = await reader.read();
      if (done) break;
      bytesRead += value?.byteLength || 0;
    }

    controller.abort(new Error("intentional downstream abort after first stream bytes"));
    await reader.cancel("intentional downstream abort").catch(() => {});

    return {
      ...testCase,
      requestId,
      ok: bytesRead > 0,
      status: response.status,
      contentType,
      bytesRead,
      aborted: true,
      error: bytesRead > 0 ? undefined : "stream ended before any bytes were read",
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

const report = {
  baseUrl,
  provider: providerFilter || null,
  total: results.length,
  failed: results.filter((r) => !r.ok).length,
  results,
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
        const kind = r.nonStream ? "non-stream" : "stream";
        if (r.racedToCompletion) {
          r.costCheck = "SKIP";
          r.costDetail = "request completed before abort could fire";
          console.error(`[stream-cancel] cost ${r.provider} (${kind}): SKIP — ${r.costDetail}`);
          continue;
        }
        if (!r.aborted || !r.requestId) {
          r.costCheck = "SKIP";
          continue;
        }
        const row = await pollLogRow(db, r.requestId);
        if (!row) {
          r.costCheck = "FAIL";
          r.costDetail = `no log row for ${r.requestId}`;
          costFailures++;
        } else if (!statusIsCancelOutcome(row.status, r.nonStream)) {
          // A streaming cancel logs status=cancelled (#4831; error on older builds).
          // A non-streaming cancel may instead finish the upstream call server-side and
          // log success — either way it must be billed, so we accept all of these for
          // non-stream and key the cost rules off the provider, not the status.
          r.costCheck = "FAIL";
          r.costDetail = `status=${row.status}, unexpected for ${kind} cancel`;
          costFailures++;
        } else {
          const cost = Number(row.cost || 0),
            tokens = Number(row.total_tokens || 0);
          // Strict cost-presence only where usage is deterministically available at the
          // moment of cancel: native Anthropic streaming (input tokens in message_start).
          // Non-streaming cancel billing is provider/timing-dependent (the upstream call
          // may or may not have completed), so we report it but don't hard-require it.
          if (!r.nonStream && EARLY_USAGE_PROVIDERS.has(r.provider)) {
            if (!(cost > 0 && tokens > 0)) {
              r.costCheck = "FAIL";
              r.costDetail = `cancelled ${r.provider} ${kind} logged no cost (cost=$${cost} tokens=${tokens})`;
              costFailures++;
            } else {
              const v = costAccuracyVerdict(sheet, row, cost, tokens, kind);
              if (v.fail) costFailures++;
              r.costDetail = v.detail;
              r.costCheck = v.verdict;
            }
          } else {
            // Presence not required. If a cost WAS recorded, still verify its accuracy.
            if (cost > 0 && tokens > 0) {
              const v = costAccuracyVerdict(sheet, row, cost, tokens, kind);
              if (v.fail) costFailures++;
              r.costDetail = v.detail;
              r.costCheck = v.verdict;
            } else {
              r.costCheck = "PASS";
              r.costDetail = `status=${row.status} cost=$${cost} (cost-presence not required for ${r.provider} ${kind})`;
            }
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

if (report.failed > 0 || costFailures > 0) {
  console.error(
    `[stream-cancel] ${report.failed}/${report.total} stream case(s) failed, ${costFailures} cost check(s) failed; report: ${out}`,
  );
  process.exit(1);
}

console.error(
  `[stream-cancel] all ${report.total} case(s) passed (incl. cost checks); report: ${out}`,
);