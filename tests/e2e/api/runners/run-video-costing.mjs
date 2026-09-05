#!/usr/bin/env node
// Verifies that async video jobs are billed correctly, end to end, against a
// real Bifrost and real providers.
//
// Video is priced at SETTLEMENT, not at submission: the POST returns a queued
// job with no cost, and minutes later a settler writes a second log row carrying
// the real figure. Neither the newman collections nor a Go unit test can check
// that — one has no way to wait minutes for an out-of-band row, the other has no
// provider. So this runner does the whole loop:
//
//   submit -> poll to terminal -> wait for the settlement row -> assert the cost
//
// The expected figures in fixtures/video-costing-cases.json are the providers'
// own published rates, so a red case means Bifrost disagrees with a price list.
//
// Works against a local instance and against hosted Bifrost with a virtual key;
// everything it needs is HTTP. Direct DB access is optional and only unlocks the
// job-row checks.
//
//   node runners/run-video-costing.mjs --base-url http://localhost:8080
//   node runners/run-video-costing.mjs --base-url https://<host> --vk sk-bf-... --seed-pricing
//   node runners/run-video-costing.mjs --group Runware --concurrency 2
//   node runners/run-video-costing.mjs --list

import { writeFileSync, mkdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { randomUUID } from "node:crypto";

import { evaluateCase, mergePricingOverridesByModel, isTerminal, costsMatch } from "./lib/video-costing.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));

// ─── args ─────────────────────────────────────────────────────────────────────
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

const baseUrl = (args["base-url"] || process.env.BIFROST_BASE_URL || process.env.BASE_URL || "http://localhost:8080").replace(/\/+$/, "");
// The virtual key authenticates the inference calls. Hosted Bifrost requires one;
// a local instance with no governance usually does not.
const virtualKey = args.vk || process.env.BIFROST_VK || "";
// Management API (/api/logs) auth. Separate from the VK: on hosted these are
// different credentials, and reading logs is not something a VK is entitled to.
const apiKey = args["api-key"] || process.env.BIFROST_API_KEY || "";
const extraHeaders = parseHeaderArgs(args.header);

const casesPath = args.cases || path.join(HERE, "..", "fixtures", "video-costing-cases.json");
const outPath = args.out || path.join(HERE, "..", "newman-reports", "video-costing-report.json");
const groupFilter = (args.group || "").toLowerCase();
const providerFilter = (args.provider || "").toLowerCase();
const caseFilter = args.case || "";
const concurrency = Math.max(1, Number(args.concurrency || 3));
const seedPricing = args["seed-pricing"] === "true";
const includeOptional = args["include-optional"] === "true";
const dryRun = args["dry-run"] === "true";
const listOnly = args.list === "true";
const configDbUrl = args["config-db-url"] || process.env.BIFROST_CONFIG_DB_URL || "";
const quiet = args.quiet === "true" || !process.stdout.isTTY;

function parseHeaderArgs(raw) {
  // --header "X-Foo: bar" may repeat; the arg parser keeps only the last, so also
  // accept a comma-separated list for the multi-header case.
  if (!raw || raw === "true") return {};
  const out = {};
  for (const part of String(raw).split(/\s*,\s*/)) {
    const idx = part.indexOf(":");
    if (idx > 0) out[part.slice(0, idx).trim()] = part.slice(idx + 1).trim();
  }
  return out;
}

// ─── HTTP ─────────────────────────────────────────────────────────────────────
function inferenceHeaders(requestId) {
  const h = { "content-type": "application/json", ...extraHeaders };
  // Setting x-request-id ourselves makes the log row id deterministic from the
  // client side, which is what lets us find the settlement row by parent id
  // instead of scanning a time window and guessing.
  if (requestId) h["x-request-id"] = requestId;
  if (virtualKey) h["authorization"] = `Bearer ${virtualKey}`;
  return h;
}

// The management API authenticates a "bfst-" key as a bearer token, which is a
// different credential and a different header from the virtual key used for
// inference. Sending it as x-bf-api-key gets a bare 401 with no hint why.
function managementHeaders() {
  const h = { ...extraHeaders };
  if (apiKey) h["authorization"] = apiKey.startsWith("Bearer ") ? apiKey : `Bearer ${apiKey}`;
  return h;
}

async function httpJson(url, init = {}, timeoutMs = 60000) {
  const ac = new AbortController();
  const timer = setTimeout(() => ac.abort(), timeoutMs);
  const started = Date.now();
  try {
    const res = await fetch(url, { ...init, signal: ac.signal });
    const text = await res.text();
    let body = null;
    try {
      body = text ? JSON.parse(text) : null;
    } catch {
      body = { _raw: text.slice(0, 2000) };
    }
    return { ok: res.ok, status: res.status, body, headers: res.headers, elapsedMs: Date.now() - started };
  } catch (err) {
    return { ok: false, status: 0, body: null, error: String(err), elapsedMs: Date.now() - started };
  } finally {
    clearTimeout(timer);
  }
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Some providers refuse a submit for capacity reasons that clear on their own —
// Runware reserves credits per in-flight job and 400s the next one until an
// earlier job finishes. That is a queueing condition, not a rejected request, so
// it must not be reported as a costing failure. Matched on the message because
// the status code (400) is the same one a genuinely bad request returns.
const TRANSIENT_SUBMIT = /insufficient available balance|currently reserved|rate limit|too many requests|try again/i;

function isTransientSubmitFailure(res) {
  if (res.status === 429) return true;
  if (res.status !== 400) return false;
  const message = res.body && res.body.error && res.body.error.message;
  return typeof message === "string" && TRANSIENT_SUBMIT.test(message);
}

// status 0 means fetch itself threw — the server is down, the host is wrong, or the
// request timed out. Reporting that as "HTTP 0 null" hides the only useful fact.
function describeHttpFailure(res) {
  if (res.status === 0) return `could not reach ${baseUrl} (${res.error || "connection failed"})`;
  return `HTTP ${res.status} ${JSON.stringify(res.body).slice(0, 400)}`;
}

// ─── log lookup ───────────────────────────────────────────────────────────────
// Finds every row Bifrost wrote for one video job. Tries the parent-id filter
// first because it is exact; falls back to a time-window scan for the case where
// the settlement row is not parented (older builds, or a settler that could not
// resolve the submission row).
async function fetchRowsForVideo({ parentRequestID, videoId, since }) {
  const rows = [];
  if (parentRequestID) {
    const url = `${baseUrl}/api/logs?parent_request_id=${encodeURIComponent(parentRequestID)}&limit=50`;
    const res = await httpJson(url, { headers: managementHeaders() });
    if (res.ok && res.body && Array.isArray(res.body.logs)) rows.push(...res.body.logs);
  }
  if (!rows.some((r) => r.video_debug && r.video_debug.accounting)) {
    const start = new Date(since - 60000).toISOString();
    const url = `${baseUrl}/api/logs?start_time=${encodeURIComponent(start)}&limit=200&sort_by=timestamp&order=desc`;
    const res = await httpJson(url, { headers: managementHeaders() });
    if (res.ok && res.body && Array.isArray(res.body.logs)) {
      for (const r of res.body.logs) {
        if (r.video_debug && r.video_debug.video_id === videoId && !rows.some((x) => x.id === r.id)) {
          rows.push(r);
        }
      }
    }
  }
  return rows;
}

// ─── job row (optional, needs DB) ─────────────────────────────────────────────
// The job row lives in configstore and has no HTTP surface, so these checks are
// skipped rather than failed when no DB URL is supplied — that keeps every other
// check working against hosted Bifrost.
async function fetchJobRow(videoId) {
  if (!configDbUrl) return { skipped: "no --config-db-url" };
  if (!/^postgres(ql)?:\/\//.test(configDbUrl)) {
    return { skipped: "only postgres URLs are supported for job-row checks" };
  }
  let pg;
  try {
    const { createRequire } = await import("node:module");
    pg = createRequire(import.meta.url)("pg");
  } catch {
    return { skipped: "the pg module is not installed" };
  }
  const client = new pg.Client({ connectionString: configDbUrl });
  try {
    await client.connect();
    const r = await client.query(
      "SELECT batch_id, kind, params, accounting_status, attempts FROM batch_jobs WHERE batch_id = $1 LIMIT 1",
      [videoId],
    );
    return { row: r.rows[0] || null };
  } catch (err) {
    return { skipped: `job-row query failed: ${err.message}` };
  } finally {
    await client.end().catch(() => {});
  }
}

// ─── pricing seed ─────────────────────────────────────────────────────────────
// The resolution bands are not in the upstream datasheet feed yet, so without
// this every rate-mode case settles unpriceable and proves nothing. Seeding
// writes the case's own expected rate as a global pricing override.
async function seedPricingOverrides(cases) {
  const overrides = mergePricingOverridesByModel(cases);
  const seeded = [];

  // Re-running --seed-pricing must not stack duplicates: Bifrost picks one winning
  // override, so a stale leftover from a previous run would quietly beat the fresh one.
  const existing = await httpJson(`${baseUrl}/api/governance/pricing-overrides`, { headers: managementHeaders() });
  const prior = ((existing.body && (existing.body.pricing_overrides || existing.body)) || []).filter(
    (o) => o && typeof o.name === "string" && o.name.startsWith("video-costing-harness"),
  );
  for (const o of prior) {
    await httpJson(`${baseUrl}/api/governance/pricing-overrides/${encodeURIComponent(o.id)}`, {
      method: "DELETE",
      headers: managementHeaders(),
    });
  }
  if (prior.length) log(`  cleared ${prior.length} override(s) from a previous run`);

  for (const override of overrides) {
    const res = await httpJson(`${baseUrl}/api/governance/pricing-overrides`, {
      method: "POST",
      headers: { "content-type": "application/json", ...managementHeaders() },
      body: JSON.stringify(override),
    });
    seeded.push({ ...override, ok: res.ok, status: res.status, error: res.ok ? undefined : describeHttpFailure(res) });
    log(res.ok ? `  seeded ${override.pattern} ${JSON.stringify(override.patch)}` : `  FAILED to seed ${override.pattern}: ${describeHttpFailure(res)}`);
  }
  return seeded;
}

// ─── the per-case flow ────────────────────────────────────────────────────────
async function runCase(spec, defaults, state) {
  const started = Date.now();
  const setState = (s, detail) => {
    state.phase = s;
    state.detail = detail || "";
    render();
  };

  if (spec.mode === "retrieve_404") return runRetrieve404Case(spec, state, setState);

  // 1. submit
  setState("submit");
  const requestId = randomUUID();
  let submitRes;
  for (let attempt = 0; ; attempt++) {
    submitRes = await httpJson(`${baseUrl}${spec.endpoint}`, {
      method: "POST",
      headers: inferenceHeaders(requestId),
      body: JSON.stringify(spec.body),
    });
    if (!isTransientSubmitFailure(submitRes) || attempt >= 5) break;
    // Back off far enough for an in-flight job to finish and release its credits.
    const waitMs = 30000 * (attempt + 1);
    setState("queued", `provider at capacity, retrying in ${waitMs / 1000}s`);
    await sleep(waitMs);
  }
  if (!submitRes.ok) {
    setState("fail", `submit ${submitRes.status}`);
    return {
      id: spec.id, group: spec.group, checklist: spec.checklist, status: "fail",
      checks: [{ name: "submitted", pass: false, detail: `HTTP ${submitRes.status}` }],
      failures: [`submit: ${describeHttpFailure(submitRes)}`],
      durationMs: Date.now() - started,
    };
  }
  const videoId = submitRes.body && (submitRes.body.id || submitRes.body.video_id);
  const parentRequestID = (submitRes.headers && submitRes.headers.get("x-request-id")) || requestId;
  const submission = { videoId, requestId: parentRequestID };
  if (!videoId) {
    setState("fail", "no video id");
    return {
      id: spec.id, group: spec.group, checklist: spec.checklist, status: "fail",
      checks: [{ name: "submitted", pass: false, detail: "no video id" }],
      failures: ["submit: response carried no video id"], submission,
      durationMs: Date.now() - started,
    };
  }

  // 2. reach terminal
  //
  // settle_via=sweeper deliberately does NOT poll: our own retrieve settles the
  // job inline, so polling would make it impossible to test the sweeper path.
  // We wait for the settlement row to appear and read the terminal status off it.
  let terminal = null;
  const viaSweeper = spec.settle_via === "sweeper";
  if (!viaSweeper) {
    const pollTimeout = spec.poll_timeout_ms || defaults.poll_timeout_ms;
    const pollInterval = spec.poll_interval_ms || defaults.poll_interval_ms;
    const deadline = Date.now() + pollTimeout;
    while (Date.now() < deadline) {
      setState("poll", `${Math.round((Date.now() - started) / 1000)}s ${terminal ? terminal.status : ""}`);
      const res = await httpJson(`${baseUrl}/v1/videos/${encodeURIComponent(videoId)}`, {
        headers: inferenceHeaders(),
      });
      if (res.ok && res.body) {
        terminal = res.body;
        if (isTerminal(res.body.status)) break;
      } else if (res.status === 404 || res.status === 410) {
        // The provider forgot the job. Terminal, and the settler treats it the
        // same way, so stop rather than burning the timeout.
        terminal = { status: "failed", _gone: true, _httpStatus: res.status };
        break;
      }
      await sleep(pollInterval);
    }
  }

  // 3. wait for the settlement row
  const settleTimeout = spec.settle_timeout_ms || defaults.settle_timeout_ms;
  const settleInterval = spec.settle_interval_ms || defaults.settle_interval_ms;
  const settleDeadline = Date.now() + settleTimeout;
  let rows = [];
  while (Date.now() < settleDeadline) {
    setState("settle", `${Math.round((Date.now() - started) / 1000)}s`);
    rows = await fetchRowsForVideo({ parentRequestID, videoId, since: started });
    const settled = rows.find((r) => r.video_debug && r.video_debug.accounting);
    if (settled) {
      if (viaSweeper && !terminal) terminal = { status: settled.video_debug.status };
      break;
    }
    await sleep(settleInterval);
  }
  if (viaSweeper && !terminal) terminal = { status: "unknown" };

  // The aggregate cost row is synthesized by the settler and carries no provider
  // payload, so a sweeper-settled case has nowhere to read the provider's own cost
  // from. One retrieve after settlement supplies it; the job is already settled and
  // settlement is idempotent, so this cannot bill twice.
  if (viaSweeper && (spec.expect?.cost?.mode === "provider")) {
    const res = await httpJson(`${baseUrl}/v1/videos/${encodeURIComponent(videoId)}`, {
      headers: inferenceHeaders(),
    });
    if (res.ok && res.body) terminal = { ...res.body, status: terminal.status || res.body.status };
  }

  // 4. verdict
  const verdict = evaluateCase({ spec, submission, terminal, rows });
  verdict.durationMs = Date.now() - started;
  verdict.submission = submission;
  verdict.settledCost = pickSettledCost(rows, videoId);
  verdict.rowIds = rows.map((r) => r.id);

  // 5. optional job-row assertions
  if (spec.expect_job_params) {
    const job = await fetchJobRow(videoId);
    if (job.skipped) {
      verdict.checks.push({ name: "job-params", pass: true, skipped: true, detail: `skipped: ${job.skipped}` });
    } else if (!job.row) {
      verdict.checks.push({ name: "job-params", pass: false, detail: "no job row for this video id" });
      verdict.failures.push("job-params: no job row for this video id");
      verdict.status = "fail";
    } else {
      let params = {};
      try {
        params = JSON.parse(job.row.params || "{}");
      } catch { /* reported as a mismatch below */ }
      for (const [k, want] of Object.entries(spec.expect_job_params)) {
        const got = params[k];
        const pass = String(got) === String(want);
        verdict.checks.push({ name: `job-params.${k}`, pass, detail: `job row has "${got}", wanted "${want}"` });
        if (!pass) {
          verdict.failures.push(`job-params.${k}: job row has "${got}", wanted "${want}"`);
          verdict.status = "fail";
        }
      }
    }
  }

  setState(verdict.status, verdict.status === "pass" ? `$${verdict.settledCost}` : verdict.failures[0]);
  return verdict;
}

// A 404 must stop the poll loop, not feed it. The assertion that actually
// distinguishes the two is latency: an internally retried 404 takes far longer
// to come back than one classified as terminal on the first response.
async function runRetrieve404Case(spec, state, setState) {
  const started = Date.now();
  setState("retrieve");
  const res = await httpJson(`${baseUrl}/v1/videos/${encodeURIComponent(spec.retrieve_id)}`, {
    headers: inferenceHeaders(),
  });
  const checks = [];
  const wantStatuses = spec.expect_http_status || [404];
  checks.push({
    name: "terminal-status-code",
    pass: wantStatuses.includes(res.status),
    detail: `got HTTP ${res.status}, wanted one of ${wantStatuses.join("/")}`,
  });
  const ceiling = spec.expect_fast_ms || 15000;
  checks.push({
    name: "no-retry-loop",
    pass: res.elapsedMs <= ceiling,
    detail: `responded in ${res.elapsedMs}ms, ceiling ${ceiling}ms (a retried 404 blows through this)`,
  });
  const failures = checks.filter((c) => !c.pass).map((c) => `${c.name}: ${c.detail}`);
  const verdict = {
    id: spec.id, group: spec.group, checklist: spec.checklist,
    status: failures.length ? "fail" : "pass",
    checks, failures, durationMs: Date.now() - started,
  };
  setState(verdict.status, `${res.status} in ${res.elapsedMs}ms`);
  return verdict;
}

function pickSettledCost(rows, videoId) {
  const r = rows.find((x) => x.video_debug && x.video_debug.accounting && x.video_debug.video_id === videoId);
  return r ? r.cost : undefined;
}

// ─── monitoring ───────────────────────────────────────────────────────────────
// A live table, because these runs last tens of minutes and a silent terminal
// gives no way to tell a slow provider from a hung runner.
const PHASE_GLYPH = { submit: "⧗", poll: "◐", settle: "◑", retrieve: "⧗", pass: "✔", fail: "✘", skip: "–", queued: "·" };
let states = [];
let lastLines = 0;

function render() {
  if (quiet) return;
  const lines = states.map((s) => {
    const g = PHASE_GLYPH[s.phase] || "·";
    const id = s.id.padEnd(34);
    const grp = (s.group || "").padEnd(14);
    const phase = String(s.phase).padEnd(8);
    return `  ${g} ${id} ${grp} ${phase} ${s.detail || ""}`.slice(0, (process.stdout.columns || 160) - 1);
  });
  if (lastLines) process.stdout.write(`\x1b[${lastLines}A\x1b[0J`);
  process.stdout.write(lines.join("\n") + "\n");
  lastLines = lines.length;
}

function log(msg) {
  if (quiet) {
    console.log(msg);
    return;
  }
  // Print above the live table so progress is not scrolled away.
  if (lastLines) process.stdout.write(`\x1b[${lastLines}A\x1b[0J`);
  console.log(msg);
  lastLines = 0;
  render();
}

// ─── main ─────────────────────────────────────────────────────────────────────
async function main() {
  const fixture = JSON.parse(readFileSync(casesPath, "utf8"));
  const defaults = fixture.defaults || {};

  let cases = fixture.cases.filter((c) => {
    if (caseFilter && c.id !== caseFilter) return false;
    if (groupFilter && String(c.group).toLowerCase() !== groupFilter) return false;
    if (providerFilter && String(c.provider).toLowerCase() !== providerFilter) return false;
    if (c.optional && !includeOptional && !caseFilter) return false;
    return true;
  });

  if (listOnly) {
    for (const c of cases) {
      const flag = c.needs_rate ? " [needs_rate]" : c.optional ? " [optional]" : "";
      console.log(`${String(c.id).padEnd(36)} ${String(c.group).padEnd(14)} ${c.checklist}${flag}`);
    }
    console.log(`\n${cases.length} cases`);
    return 0;
  }

  console.log(`Bifrost video costing`);
  console.log(`  target      ${baseUrl}`);
  console.log(`  auth        ${virtualKey ? "virtual key" : "none"}${apiKey ? " + management api key" : ""}`);
  console.log(`  cases       ${cases.length} (concurrency ${concurrency})`);
  if (configDbUrl) console.log(`  job rows    enabled`);
  console.log("");

  // A case with no sourced rate cannot assert anything, and inventing a number
  // for a billing test is worse than not running it.
  const skipped = [];
  for (const c of cases) {
    const rate = c.expect && c.expect.cost && c.expect.cost.rate_per_second;
    if (c.needs_rate || (c.expect?.cost?.mode === "rate" && typeof rate !== "number")) {
      skipped.push({
        id: c.id, group: c.group, checklist: c.checklist, status: "skip",
        failures: [], checks: [],
        reason: "no official rate sourced yet — set expect.cost.rate_per_second and drop needs_rate",
      });
    }
  }
  const runnable = cases.filter((c) => !skipped.some((s) => s.id === c.id));

  if (dryRun) {
    for (const c of runnable) console.log(`would run ${c.id}: POST ${c.endpoint || "(retrieve)"} ${JSON.stringify(c.body || {})}`);
    console.log(`\n${runnable.length} runnable, ${skipped.length} skipped`);
    return 0;
  }

  const health = await httpJson(`${baseUrl}/health`, { headers: managementHeaders() }, 10000);
  if (health.status === 0) {
    console.log(`Cannot reach ${baseUrl} — ${health.error || "connection failed"}`);
    console.log("Nothing was submitted. Start Bifrost, or pass --base-url.");
    return 1;
  }

  // Reading /api/logs is how every case is verified, so a management 401 must be
  // reported as itself rather than as nine cases that "never settled".
  const logsProbe = await httpJson(`${baseUrl}/api/logs?limit=1`, { headers: managementHeaders() }, 15000);
  if (logsProbe.status === 401 || logsProbe.status === 403) {
    console.log(`Management API returned ${logsProbe.status} for /api/logs.`);
    console.log(apiKey
      ? "The --api-key was rejected. It must be a management key (starts with bfst-), not a virtual key."
      : "Pass --api-key <bfst-...>; a virtual key authenticates inference only, not log reads.");
    console.log("Nothing was submitted.");
    return 1;
  }

  // Every assertion here keys off video_debug.accounting, which distinguishes the
  // settlement from the submission. A build without it settles jobs perfectly well
  // but writes no such blob, so every case fails as "0 settlement rows" — a version
  // gap that reads exactly like a billing bug. Say so before spending money.
  const probe = await httpJson(
    `${baseUrl}/api/logs?objects=video_generation,video_retrieve&limit=25&sort_by=timestamp&order=desc`,
    { headers: managementHeaders() },
    15000,
  );
  const priorVideoRows = (probe.body && probe.body.logs) || [];
  if (priorVideoRows.length > 0 && !priorVideoRows.some((r) => r.video_debug)) {
    console.log(`WARNING: ${baseUrl} has ${priorVideoRows.length} recent video rows and none carries video_debug.`);
    console.log("That field is what marks a settlement row, so every case here would fail as \"0 settlement rows\".");
    console.log("The target is likely older than the video_debug change. Upgrade it, or run --group Runware --dry-run to confirm shapes only.");
    console.log("");
  }

  let seeded = [];
  if (seedPricing) {
    console.log("Seeding pricing overrides for the asserted bands:");
    seeded = await seedPricingOverrides(runnable);
    console.log("");
  }

  states = runnable.map((c) => ({ id: c.id, group: c.group, phase: "queued", detail: "" }));
  render();

  // Bounded concurrency: video jobs take minutes, so serial would run for an
  // hour, but every case is a real paid generation and providers rate-limit.
  const results = [];
  let cursor = 0;
  async function worker() {
    while (cursor < runnable.length) {
      const i = cursor++;
      try {
        results[i] = await runCase(runnable[i], defaults, states[i]);
      } catch (err) {
        states[i].phase = "fail";
        states[i].detail = String(err);
        results[i] = {
          id: runnable[i].id, group: runnable[i].group, checklist: runnable[i].checklist,
          status: "fail", checks: [], failures: [`runner error: ${String(err)}`],
        };
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(concurrency, runnable.length) }, worker));

  // Cross-case comparison: the two settlement paths must agree on the figure.
  for (const spec of runnable) {
    if (!spec.compare_cost_with) continue;
    const mine = results.find((r) => r && r.id === spec.id);
    const other = results.find((r) => r && r.id === spec.compare_cost_with);
    if (!mine || !other) continue;
    const pass = costsMatch(mine.settledCost, other.settledCost);
    const detail = `${spec.id} billed $${mine.settledCost}, ${spec.compare_cost_with} billed $${other.settledCost}`;
    (mine.checks ||= []).push({ name: "settlement-path-parity", pass, detail });
    if (!pass) {
      (mine.failures ||= []).push(`settlement-path-parity: ${detail}`);
      mine.status = "fail";
    }
  }

  const all = [...results.filter(Boolean), ...skipped];
  render();
  console.log("");
  report(all, seeded);

  mkdirSync(path.dirname(outPath), { recursive: true });
  writeFileSync(
    outPath,
    JSON.stringify({ baseUrl, generatedAt: new Date().toISOString(), seeded, results: all }, null, 2),
  );
  console.log(`report: ${outPath}`);

  return all.some((r) => r.status === "fail") ? 1 : 0;
}

function report(all, seeded) {
  const byGroup = {};
  for (const r of all) (byGroup[r.group] ||= []).push(r);

  for (const [group, rows] of Object.entries(byGroup)) {
    console.log(`${group}`);
    for (const r of rows) {
      const glyph = r.status === "pass" ? "✔" : r.status === "skip" ? "–" : "✘";
      const cost = r.settledCost != null ? ` $${r.settledCost}` : "";
      console.log(`  ${glyph} ${r.checklist || r.id}${cost}`);
      if (r.status === "skip" && r.reason) console.log(`      ${r.reason}`);
      for (const f of r.failures || []) console.log(`      ${f}`);
      for (const c of r.checks || []) if (c.skipped) console.log(`      ${c.name} ${c.detail}`);
    }
    console.log("");
  }

  const pass = all.filter((r) => r.status === "pass").length;
  const fail = all.filter((r) => r.status === "fail").length;
  const skip = all.filter((r) => r.status === "skip").length;
  console.log(`${pass} passed, ${fail} failed, ${skip} skipped`);
  if (seeded.some((s) => !s.ok)) console.log(`WARNING: ${seeded.filter((s) => !s.ok).length} pricing overrides failed to seed — rate cases may settle unpriceable`);
}

main()
  .then((code) => process.exit(code))
  .catch((err) => {
    console.error(err);
    process.exit(1);
  });
