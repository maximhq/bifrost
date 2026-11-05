#!/usr/bin/env node
// Live benchmark for the prompt_compression plugin. Replays the Chat fixture
// turn by turn against a running Bifrost gateway and records provider usage.
//
//   BENCH_MODEL=openai/gpt-4o-mini node run.mjs --label off
//   BENCH_MODEL=openai/gpt-4o-mini node run.mjs --label on
//   node run.mjs --report
//   node run.mjs --dry-run            (no network; prints request sizes)
//
// Env: BENCH_MODEL (required unless --dry-run/--report), BASE_URL
// (default http://localhost:8080), BENCH_API_KEY (optional bearer header).
import { readFileSync, writeFileSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const fixturePath = join(here, "..", "testdata", "agent_session_chat.json");
const resultsPath = (label) => join(here, `results-${label}.json`);

const args = process.argv.slice(2);
const flag = (name) => args.includes(name);
const opt = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};

// Each request ends right before an assistant message, like a real agent loop.
function buildTurns(messages) {
  const turns = [];
  messages.forEach((m, i) => {
    if (m.role === "assistant") turns.push(messages.slice(0, i));
  });
  return turns;
}

// Providers that validate tool history (e.g. Anthropic via Bifrost) need the
// tools declared; derive a minimal definition per function name in the history.
function deriveTools(messages) {
  const names = new Set();
  for (const m of messages)
    for (const c of m.tool_calls ?? []) names.add(c.function.name);
  return [...names].sort().map((name) => ({
    type: "function",
    function: {
      name,
      description: name,
      parameters: { type: "object", properties: {}, additionalProperties: true },
    },
  }));
}

function buildBody(model, messages, tools) {
  return { model, max_tokens: 1, messages, tools };
}

function cachedTokens(usage) {
  return (
    usage?.prompt_tokens_details?.cached_tokens ??
    usage?.prompt_tokens_details?.cached_read_tokens ??
    usage?.cached_read_tokens ??
    0
  );
}

function report() {
  const load = (l) => {
    if (!existsSync(resultsPath(l))) throw new Error(`missing ${resultsPath(l)}; run --label ${l} first`);
    const data = JSON.parse(readFileSync(resultsPath(l), "utf8"));
    if (Array.isArray(data) || !data.model || !Array.isArray(data.rows)) {
      throw new Error(`${resultsPath(l)} has no model recorded; delete it and rerun --label ${l}`);
    }
    return data;
  };
  const offRun = load("off");
  const onRun = load("on");
  if (offRun.model !== onRun.model) {
    throw new Error(`off and on runs used different models (${offRun.model} vs ${onRun.model}); rerun both with the same BENCH_MODEL`);
  }
  const expectedTurns = buildTurns(JSON.parse(readFileSync(fixturePath, "utf8"))).length;
  for (const [l, run] of [["off", offRun], ["on", onRun]]) {
    const have = new Set(run.rows.map((r) => r.turn));
    const missing = [];
    for (let t = 1; t <= expectedTurns; t++) if (!have.has(t)) missing.push(t);
    if (missing.length) throw new Error(`${l} run is missing turns ${missing.join(", ")} of ${expectedTurns}; rerun --label ${l}`);
    for (const r of run.rows) {
      if (!Number.isFinite(r.prompt_tokens) || r.prompt_tokens <= 0) {
        throw new Error(`${l} run turn ${r.turn} has no prompt_tokens; rerun --label ${l}`);
      }
    }
  }
  const off = offRun.rows;
  const on = onRun.rows;
  const byTurn = (rows) => new Map(rows.map((r) => [r.turn, r]));
  const o = byTurn(off), n = byTurn(on);
  const turns = [...new Set([...o.keys(), ...n.keys()])].sort((a, b) => a - b);
  const pct = (a, b) => (a > 0 ? (100 * (a - b)) / a : 0);
  let sOff = 0, sOn = 0, cOff = 0, cOn = 0, sumPct = 0;
  const lines = [
    "# Live benchmark results",
    "",
    `Model: ${offRun.model}. Provider-reported usage; results depend on the provider's caching behaviour.`,
    "",
    "| Turn | prompt_tokens off | prompt_tokens on | cached off | cached on | % saved |",
    "|---:|---:|---:|---:|---:|---:|",
  ];
  for (const t of turns) {
    const a = o.get(t), b = n.get(t);
    if (!a || !b) continue;
    const p = pct(a.prompt_tokens, b.prompt_tokens);
    sOff += a.prompt_tokens; sOn += b.prompt_tokens;
    cOff += a.cached_read_tokens; cOn += b.cached_read_tokens;
    sumPct += p;
    lines.push(`| ${t} | ${a.prompt_tokens} | ${b.prompt_tokens} | ${a.cached_read_tokens} | ${b.cached_read_tokens} | ${p.toFixed(1)}% |`);
  }
  const count = turns.filter((t) => o.has(t) && n.has(t)).length || 1;
  lines.push(`| **Total** | **${sOff}** | **${sOn}** | **${cOff}** | **${cOn}** | **${pct(sOff, sOn).toFixed(1)}%** |`, "", `Mean per-turn % saved: **${(sumPct / count).toFixed(1)}%**`, "");
  writeFileSync(join(here, "results.md"), lines.join("\n"));
  console.log(lines.join("\n"));
}

async function main() {
  if (flag("--report")) return report();

  const messages = JSON.parse(readFileSync(fixturePath, "utf8"));
  const turns = buildTurns(messages);
  const tools = deriveTools(messages);
  const dry = flag("--dry-run");
  const model = process.env.BENCH_MODEL ?? (dry ? "dry-run/model" : undefined);
  if (!model) throw new Error("BENCH_MODEL is required, e.g. BENCH_MODEL=openai/gpt-4o-mini");

  if (dry) {
    console.log(`requests: ${turns.length}`);
    turns.forEach((msgs, i) => {
      const bytes = Buffer.byteLength(JSON.stringify(buildBody(model, msgs, tools)));
      console.log(`turn ${i + 1}: messages=${msgs.length} body_bytes=${bytes}`);
    });
    return;
  }

  const label = opt("--label");
  if (label !== "off" && label !== "on") throw new Error("--label off|on is required");
  const base = process.env.BASE_URL ?? "http://localhost:8080";
  const headers = { "Content-Type": "application/json" };
  if (process.env.BENCH_API_KEY) headers.Authorization = `Bearer ${process.env.BENCH_API_KEY}`;

  const file = resultsPath(label);
  let rows = [];
  if (existsSync(file)) {
    const prev = JSON.parse(readFileSync(file, "utf8"));
    if (Array.isArray(prev) || prev.model !== model) {
      throw new Error(`${file} was recorded for a different model (${prev.model ?? "unknown"}); delete it to start over with ${model}`);
    }
    rows = prev.rows;
  }
  for (let i = 0; i < turns.length; i++) {
    const res = await fetch(`${base}/v1/chat/completions`, {
      method: "POST",
      headers,
      body: JSON.stringify(buildBody(model, turns[i], tools)),
    });
    const text = await res.text();
    if (!res.ok) throw new Error(`turn ${i + 1}: HTTP ${res.status}: ${text.slice(0, 500)}`);
    let usage;
    try {
      usage = JSON.parse(text).usage;
    } catch {
      throw new Error(`turn ${i + 1}: response is not JSON: ${text.slice(0, 500)}`);
    }
    if (!Number.isFinite(usage?.prompt_tokens) || usage.prompt_tokens <= 0) {
      throw new Error(`turn ${i + 1}: response has no usage.prompt_tokens: ${text.slice(0, 500)}`);
    }
    const row = { turn: i + 1, prompt_tokens: usage.prompt_tokens, cached_read_tokens: cachedTokens(usage) };
    // Replace any earlier row for this turn so reruns do not duplicate.
    const idx = rows.findIndex((r) => r.turn === row.turn);
    if (idx >= 0) rows[idx] = row; else rows.push(row);
    writeFileSync(file, JSON.stringify({ model, rows }, null, 2) + "\n");
    console.log(`turn ${row.turn}: prompt_tokens=${row.prompt_tokens} cached=${row.cached_read_tokens}`);
  }
}

main().catch((e) => {
  console.error(e.message);
  process.exit(1);
});
