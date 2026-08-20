#!/usr/bin/env node
// Merges every tmp/harness-token-parity-*.json fragment (one per newman process - the harness
// forks one newman per provider by default, see PARALLEL in the run-provider-harness-test
// Makefile target) written by newman-reporter-token-parity, and renders the combined
// direct-provider-vs-Bifrost token usage table to tmp/harness-token-parity.md. If an htmlextra
// report is present (--html, sequential mode / PARALLEL=0 only - htmlextra isn't emitted in
// parallel mode), the same table is also injected as a section right after <body> so it's
// visible without leaving the interactive report; the injection is idempotent (marked with an
// HTML comment pair) so reruns replace rather than duplicate it.
//
// Usage:
//   node render-token-parity-report.mjs --glob "tmp/harness-token-parity-*.json" --out tmp/harness-token-parity.md --html tmp/newman-report.html

import { readFileSync, writeFileSync, existsSync, readdirSync } from "node:fs";
import { dirname, basename, join } from "node:path";
// The census depends on the same env the matrix builder read, so a renderer invoked without it
// would report every gated cell as missing. Tolerate the census being unavailable rather than
// losing the whole report over the summary table.
//
// Imported dynamically, not with a static `import`: ESM resolves and evaluates every static
// import before the module body runs, so a failure there throws before any try block is entered
// and the guard below could never catch it. `await import(...)` moves the load inside the guard,
// which is the only arrangement where the fallback actually runs.
// Returns { cells, error }. An empty census and a FAILED census are not the same thing: with
// [] every backend has zero expected cells, so `missing` computes to 0, no backend row is
// emitted and no coverage warning fires -- a broken import renders as full coverage. The error
// is carried into the report instead (see the coverage-accounting banner below) rather than
// left in a console line the Makefile's `|| true` throws away.
const expectedCells = async () => {
  try {
    const { expectedTokenParityCells } = await import("./lib/token-parity-matrix.mjs");
    return { cells: expectedTokenParityCells(), error: null };
  } catch (err) {
    console.error(`[render-token-parity-report] cell census unavailable: ${err.message}`);
    return { cells: [], error: err.message };
  }
};

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, cur, i, arr) => {
    if (cur.startsWith("--")) acc.push([cur.slice(2), arr[i + 1]]);
    return acc;
  }, []),
);

const GLOB = args.glob || "tmp/harness-token-parity-*.json";
const OUT = args.out || "tmp/harness-token-parity.md";
const HTML = args.html || "tmp/newman-report.html";

const dir = dirname(GLOB);
const pattern = new RegExp("^" + basename(GLOB).replace(/[.]/g, "\\.").replace(/\*/g, ".*") + "$");

const fragmentFiles = existsSync(dir) ? readdirSync(dir).filter((f) => pattern.test(f)) : [];

const rows = [];
for (const file of fragmentFiles) {
  try {
    const parsed = JSON.parse(readFileSync(join(dir, file), "utf8"));
    if (Array.isArray(parsed)) rows.push(...parsed);
  } catch (e) {
    console.error(`[render-token-parity-report] failed to read ${file}: ${e.message}`);
  }
}

if (rows.length === 0) {
  console.error(
    `[render-token-parity-report] no fragments matched ${GLOB} - run the parity matrix first (FOLDER="Cross-Cut Round 33: Direct-Provider vs Bifrost Token Parity Matrix (generated)")`,
  );
  process.exit(0);
}

// Last write wins per (backend, modality) - RERUN_FAILED / sequential reruns can produce dupes.
const byKey = new Map();
for (const r of rows) byKey.set(`${r.backend}/${r.modality}`, r);
const merged = [...byKey.values()].sort((a, b) =>
  (a.backend + a.modality).localeCompare(b.backend + b.modality),
);

// Must mirror token-parity-matrix.mjs's TOLERANCE_PCT/COMPLETION_ABS_FLOOR exactly - this
// report's verdict column and the actual pm.test assertions embedded in the generated Postman
// items need to agree, or the report can show PASS/FAIL that disagrees with what the live run
// actually asserted. Not imported directly since this file has no dependency on the generator
// module; keep these in sync by hand if either changes.
//
// direct.prompt/cached and bifrost.prompt/cached are round-1 numbers (the only clean
// comparison); direct.completion/total and bifrost.completion/total are round-3 cumulative
// numbers - see token-parity-matrix.mjs's round-1-capture and finalStore comments for why.
const TOLERANCE_PCT = 20;
const COMPLETION_ABS_FLOOR = 10;

const withinPct = (d, b, pct) => {
  if (d === 0 && b === 0) return true;
  const base = Math.max(Math.abs(d), Math.abs(b), 1);
  return (Math.abs(d - b) / base) * 100 <= pct;
};
const tokenDelta = (d, b) => {
  const diff = b - d;
  return diff === 0 ? "0" : (diff > 0 ? "+" : "") + diff;
};
// INFO (not PASS/FAIL) for reasoning_on/reasoning_on_streaming: numbers are still shown, but a
// thinkingBudget is a cap on how much a model CAN think, not how much it DOES on a given call,
// so completion swings there reflect real per-call stochasticity no tolerance band can absorb
// meaningfully - counting these toward pass/fail would just be noise in the summary counts.
const verdict = (r) => {
  if (r.skippedFromVerdict) return "INFO";
  const okPrompt = withinPct(r.direct.prompt, r.bifrost.prompt, TOLERANCE_PCT);
  const okCached = withinPct(r.direct.cached, r.bifrost.cached, TOLERANCE_PCT);
  const okCompletion =
    withinPct(r.direct.completion, r.bifrost.completion, TOLERANCE_PCT) ||
    Math.abs(r.direct.completion - r.bifrost.completion) <= COMPLETION_ABS_FLOOR;
  return okPrompt && okCached && okCompletion ? "PASS" : "FAIL";
};

const lines = [];
lines.push("## Direct-Provider vs Bifrost Token Parity Report");
lines.push("");
lines.push(
  `Generated from ${fragmentFiles.length} newman process(es), ${merged.length} backend/modality cell(s). ` +
    `Tolerance: prompt within ${TOLERANCE_PCT}% of round 1; completion within ${TOLERANCE_PCT}% or ${COMPLETION_ABS_FLOOR} tokens of round 3 cumulative, whichever is looser. ` +
    `Cached is reported, not asserted: no body here sets a cache_control breakpoint, so a non-zero count can only come from implicit caching - which is best-effort, and which the two legs cannot share because they deliberately send different bytes (direct posts each backend's native shape, Bifrost normalizes onto one). ` +
    `reasoning_on/reasoning_on_streaming are reported as INFO, not asserted (see token-parity-matrix.mjs). Δ = bifrost - direct.`,
);
lines.push("");
lines.push(
  "| Backend | Modality | Direct model | Bifrost model | Bifrost baseUrl | Bifrost endpoint | Direct prompt | Bifrost prompt | Δ | Direct cached | Bifrost cached | Δ | Direct completion | Bifrost completion | Δ | Direct total | Bifrost total | Δ | Verdict |",
);
lines.push(
  "|---|---|---|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|",
);
for (const r of merged) {
  const endpoint = r.bifrost.endpoint || "{{baseUrl}}/v1/chat/completions";
  const directModel = r.direct.model || "-";
  const bifrostModel = r.bifrost.model || "-";
  const baseUrl = r.bifrost.baseUrl || "-";
  lines.push(
    `| ${r.backend} | ${r.modality} | \`${directModel}\` | \`${bifrostModel}\` | \`${baseUrl}\` | \`${endpoint}\` | ${r.direct.prompt} | ${r.bifrost.prompt} | ${tokenDelta(r.direct.prompt, r.bifrost.prompt)} ` +
      `| ${r.direct.cached} | ${r.bifrost.cached} | ${tokenDelta(r.direct.cached, r.bifrost.cached)} ` +
      `| ${r.direct.completion} | ${r.bifrost.completion} | ${tokenDelta(r.direct.completion, r.bifrost.completion)} ` +
      `| ${r.direct.total} | ${r.bifrost.total} | ${tokenDelta(r.direct.total, r.bifrost.total)} ` +
      `| ${verdict(r)} |`,
  );
}
lines.push("");

// Both legs read the same provider field for `total`, and prompt/completion are asserted while
// total is not. So a cell whose prompt and completion agree but whose total does not is reporting
// something neither asserted column can see -- gemini/audio came back +3903 on total with
// completion identical at 15. Not scored (the cause is unknown and may be legitimate, e.g. a
// modality count the two legs bill differently), but it must not sit in an all-green table
// unremarked, which is how it survived the run that produced it.
const TOTAL_DIVERGENCE_FLOOR = 100;
const unexplainedTotals = merged.filter((r) => {
  const totalGap = Math.abs((r.bifrost.total || 0) - (r.direct.total || 0));
  if (totalGap < TOTAL_DIVERGENCE_FLOOR) return false;
  return (
    withinPct(r.direct.prompt, r.bifrost.prompt, TOLERANCE_PCT) &&
    withinPct(r.direct.completion, r.bifrost.completion, TOLERANCE_PCT)
  );
});
if (unexplainedTotals.length > 0) {
  lines.push(
    "> **Unexplained total-token divergence.** These cells agree on prompt and completion -- the two",
  );
  lines.push(
    "> asserted columns -- but disagree on total by more than " +
      TOTAL_DIVERGENCE_FLOOR +
      " tokens. Total is reported,",
  );
  lines.push(
    "> not asserted, so this does not fail; it is surfaced because a gap the asserted columns cannot",
  );
  lines.push("> explain is worth a look before it is assumed benign.");
  lines.push("");
  for (const r of unexplainedTotals) {
    lines.push(
      `> - ${r.backend}/${r.modality}: direct ${r.direct.total} vs bifrost ${r.bifrost.total} (${tokenDelta(r.direct.total, r.bifrost.total)})`,
    );
  }
  lines.push("");
}

const byBackend = new Map();
for (const r of merged) {
  const list = byBackend.get(r.backend) || [];
  list.push(r);
  byBackend.set(r.backend, list);
}

// The summary is built from the matrix census, not just from the cells that reported data. A
// backend whose every cell errored out (bad credentials, retired model id) returns no fragments at
// all, so summarizing only what came back rendered it as absent -- and an all-green report missing
// three of its seven backends reads as healthier than a red one. Missing cells are counted and
// named here instead.
const { cells: census, error: censusError } = await expectedCells();
const expectedByBackend = new Map();
for (const c of census) {
  const list = expectedByBackend.get(c.backend) || [];
  list.push(c);
  expectedByBackend.set(c.backend, list);
}

lines.push("### Per-model summary");
lines.push("");
lines.push(
  "| Backend | Expected | Reported | Passing | Failing | Info (not asserted) | Skipped (documented) | Missing |",
);
lines.push("|---|---:|---:|---:|---:|---:|---:|---:|");
const backendNames = [...new Set([...expectedByBackend.keys(), ...byBackend.keys()])].sort();
const gaps = [];
for (const backend of backendNames) {
  const list = byBackend.get(backend) || [];
  const expected = expectedByBackend.get(backend) || [];
  const runnable = expected.filter((c) => c.status === "run");
  const skipped = expected.filter((c) => c.status === "skip");
  const gated = expected.filter((c) => c.status === "gated");
  const failing = list.filter((r) => verdict(r) === "FAIL").length;
  const info = list.filter((r) => verdict(r) === "INFO").length;
  // Gated cells were deliberately not built this run, so they are neither expected nor missing.
  //
  // Missing is computed from cell KEYS, not counts. Subtracting list.length from
  // runnable.length let one genuinely absent expected modality be cancelled out by one
  // unexpected reported modality, so the report claimed zero missing cells while coverage
  // was actually wrong. The keys are named in the gap line so the absent cell is identifiable.
  const reportedKeys = new Set(list.map((r) => `${r.backend}/${r.modality}`));
  const missingCells = runnable.filter((c) => !reportedKeys.has(`${c.backend}/${c.modality}`));
  const missing = missingCells.length;
  if (missing > 0) {
    gaps.push(
      `${backend}: ${missing} cell(s) expected but absent from the report - ${missingCells
        .map((c) => c.modality)
        .join(", ")}`,
    );
  }
  const unexpected = list.filter((r) => !runnable.some((c) => c.modality === r.modality));
  if (unexpected.length > 0) {
    gaps.push(
      `${backend}: ${unexpected.length} reported cell(s) not in the census - ${unexpected
        .map((r) => r.modality)
        .join(", ")}`,
    );
  }
  if (gated.length > 0) {
    gaps.push(`${backend}: ${gated.length} cell(s) not run - ${gated[0].reason}`);
  }
  lines.push(
    `| ${backend} | ${runnable.length} | ${list.length} | ${list.length - failing - info} | ${failing} | ${info} | ${skipped.length} | ${missing} |`,
  );
}
lines.push("");
if (censusError) {
  // Loud, and above the gaps block: with no census every Missing count below is 0 and every
  // backend row is derived from reported data alone, so the numbers cannot be read as coverage.
  lines.push(
    "> **:rotating_light: Coverage accounting FAILED.** The expected-cell census could not be loaded:",
  );
  lines.push(`> \`${censusError}\``);
  lines.push(
    "> Every `Expected` and `Missing` figure below is therefore 0 by default, and a backend whose",
  );
  lines.push(
    "> cells all errored will not appear at all. Treat this report as unable to account for coverage,",
  );
  lines.push("> not as evidence of it, until the census import is fixed.");
  lines.push("");
}
if (gaps.length > 0) {
  lines.push(
    "> **Coverage gaps this run.** A cell counted under `Missing` was expected to produce a row and did not.",
  );
  lines.push(
    "> Either its requests all errored (check `tmp/harness-failures.md`) or the run was filtered and never",
  );
  lines.push(
    "> reached them (`PROVIDER=`/`FEATURE=`/`SMOKE=`). Whichever it was, the cell was not measured -- so it is",
  );
  lines.push("> not a pass, and the verdict counts to its left do not cover it.");
  lines.push("");
  for (const g of gaps) {
    lines.push(`> - ${g}`);
  }
  lines.push("");
}

writeFileSync(OUT, lines.join("\n") + "\n");
const totalFail = merged.filter((r) => verdict(r) === "FAIL").length;
const totalInfo = merged.filter((r) => verdict(r) === "INFO").length;
console.error(
  `[render-token-parity-report] wrote ${OUT} (${merged.length} cells, ${totalFail} failing, ${totalInfo} info)`,
);

// --- htmlextra injection ---
const esc = (s) =>
  String(s).replace(
    /[&<>"']/g,
    (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c],
  );

if (existsSync(HTML)) {
  const START = "<!-- TOKEN_PARITY_REPORT_START -->";
  const END = "<!-- TOKEN_PARITY_REPORT_END -->";
  const cellRows = merged
    .map(
      (
        r,
      ) => `<tr class="${verdict(r) === "FAIL" ? "tp-fail" : verdict(r) === "INFO" ? "tp-info" : "tp-pass"}">
        <td>${esc(r.backend)}</td><td>${esc(r.modality)}</td>
        <td><code>${esc(r.direct.model || "-")}</code></td><td><code>${esc(r.bifrost.model || "-")}</code></td>
        <td><code>${esc(r.bifrost.baseUrl || "-")}</code></td><td><code>${esc(r.bifrost.endpoint || "{{baseUrl}}/v1/chat/completions")}</code></td>
        <td>${r.direct.prompt}</td><td>${r.bifrost.prompt}</td><td>${tokenDelta(r.direct.prompt, r.bifrost.prompt)}</td>
        <td>${r.direct.cached}</td><td>${r.bifrost.cached}</td><td>${tokenDelta(r.direct.cached, r.bifrost.cached)}</td>
        <td>${r.direct.completion}</td><td>${r.bifrost.completion}</td><td>${tokenDelta(r.direct.completion, r.bifrost.completion)}</td>
        <td>${r.direct.total}</td><td>${r.bifrost.total}</td><td>${tokenDelta(r.direct.total, r.bifrost.total)}</td>
        <td>${verdict(r)}</td>
      </tr>`,
    )
    .join("\n");
  const section = `${START}
<style>
  #token-parity-report { padding: 16px 24px; font-family: system-ui, sans-serif; font-size: 13px; }
  #token-parity-report h2 { margin: 0 0 6px; font-size: 18px; }
  #token-parity-report p { margin: 0 0 12px; opacity: 0.8; }
  #token-parity-report table { border-collapse: collapse; width: 100%; }
  #token-parity-report th, #token-parity-report td { border: 1px solid rgba(128,128,128,0.4); padding: 4px 8px; text-align: right; white-space: nowrap; }
  #token-parity-report th:nth-child(-n+6), #token-parity-report td:nth-child(-n+6) { text-align: left; }
  #token-parity-report tr.tp-fail { background: rgba(220,53,69,0.15); }
  #token-parity-report tr.tp-pass { background: rgba(40,167,69,0.08); }
  #token-parity-report tr.tp-info { background: rgba(128,128,128,0.12); }
  body.theme-light #token-parity-report { color: #1a1a1a; }
  body.theme-dark #token-parity-report { color: #e6e6e6; }
</style>
<div id="token-parity-report">
  <h2>Direct-Provider vs Bifrost Token Parity Report</h2>
  <p>${fragmentFiles.length} newman process(es), ${merged.length} cell(s), ${totalFail} failing, ${totalInfo} info (not asserted). Tolerance: prompt within ${TOLERANCE_PCT}% of round 1; completion within ${TOLERANCE_PCT}% or ${COMPLETION_ABS_FLOOR} tokens of round 3. Cached is reported, not asserted (implicit caching; the legs send different bytes by design). Full breakdown: tmp/harness-token-parity.md</p>
  <table>
    <thead><tr>
      <th>Backend</th><th>Modality</th>
      <th>Direct model</th><th>Bifrost model</th><th>Bifrost baseUrl</th><th>Bifrost endpoint</th>
      <th>Direct prompt</th><th>Bifrost prompt</th><th>Δ</th>
      <th>Direct cached</th><th>Bifrost cached</th><th>Δ</th>
      <th>Direct completion</th><th>Bifrost completion</th><th>Δ</th>
      <th>Direct total</th><th>Bifrost total</th><th>Δ</th>
      <th>Verdict</th>
    </tr></thead>
    <tbody>
${cellRows}
    </tbody>
  </table>
</div>
${END}`;

  const html = readFileSync(HTML, "utf8");
  const markerRe = new RegExp(`${START}[\\s\\S]*?${END}`);
  const injected = markerRe.test(html)
    ? html.replace(markerRe, section)
    : html.replace(/<body[^>]*>/, (m) => `${m}\n${section}`);
  if (injected === html && !markerRe.test(html)) {
    console.error(
      `[render-token-parity-report] no <body> tag found in ${HTML} - skipped HTML injection`,
    );
  } else {
    writeFileSync(HTML, injected);
    console.error(`[render-token-parity-report] injected token parity table into ${HTML}`);
  }
} else {
  console.error(
    `[render-token-parity-report] ${HTML} not found (parallel mode doesn't emit htmlextra) - skipped HTML injection`,
  );
}