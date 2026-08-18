#!/usr/bin/env node
// Merges every tmp/harness-cache-parity-*.json fragment (one per newman process) written by
// newman-reporter-cache-parity, and renders the combined prompt-cache report to
// tmp/harness-cache-parity.md. Sibling of render-token-parity-report.mjs - same fragment-merge
// shape, same htmlextra injection, but two tables instead of one because two different generated
// folders emit cache measurements:
//
//   Round 34 (kind=anchor) - Mid-Conversation System Cache-Anchor Parity. Each case runs on up to
//     three legs; the `direct` leg (raw Bedrock Converse over SigV4) is the baseline the Bifrost
//     legs are compared against. This is the actual direct-vs-Bifrost CACHE parity table.
//   Round 35 (kind=matrix) - Cross-Provider Prompt-Cache Matrix. Bifrost-only cells across
//     provider x model x mechanism; scored against the floor each cell asserts.
//
// Verdicts are computed from `expectHit` / `mechanism` / `hitRateFloor` carried in the payloads
// rather than re-deriving them here, so this report cannot show a PASS/FAIL that disagrees with
// what the live run asserted.
//
// Usage:
//   node render-cache-parity-report.mjs --glob "tmp/harness-cache-parity-*.json" --out tmp/harness-cache-parity.md --html tmp/newman-report.html

import { readFileSync, writeFileSync, existsSync, readdirSync } from "node:fs";
import { dirname, basename, join } from "node:path";

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, cur, i, arr) => {
    if (cur.startsWith("--")) acc.push([cur.slice(2), arr[i + 1]]);
    return acc;
  }, [])
);

const GLOB = args.glob || "tmp/harness-cache-parity-*.json";
const OUT = args.out || "tmp/harness-cache-parity.md";
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
    console.error(`[render-cache-parity-report] failed to read ${file}: ${e.message}`);
  }
}

if (rows.length === 0) {
  console.error(
    `[render-cache-parity-report] no fragments matched ${GLOB} - run the cache matrices first ` +
      `(make run-provider-harness-test FEATURE="cache" PARALLEL=0, or pick "cache-parity" in the modality menu)`
  );
  process.exit(0);
}

// Last write wins per cell identity - RERUN_FAILED and sequential reruns can produce duplicates.
const dedupe = (list, keyFn) => {
  const m = new Map();
  for (const r of list) m.set(keyFn(r), r);
  return [...m.values()];
};

const anchors = dedupe(
  rows.filter((r) => r.kind === "anchor"),
  (r) => `${r.caseKey}/${r.leg}`
).sort((a, b) => (a.caseKey + a.leg).localeCompare(b.caseKey + b.leg));

const matrix = dedupe(
  rows.filter((r) => r.kind === "matrix"),
  (r) => `${r.cell}/${r.arm}`
).sort((a, b) => (a.provider + a.model + a.arm).localeCompare(b.provider + b.model + b.arm));

const pct = (h) => `${((h || 0) * 100).toFixed(1)}%`;
const delta = (d, b) => {
  const diff = (b || 0) - (d || 0);
  return (diff > 0 ? "+" : "") + (diff * 100).toFixed(1) + "pp";
};

// ---------------------------------------------------------------------------------------------
// Round 34: direct-vs-Bifrost cache anchor parity, pivoted so each case is one row and the legs
// are columns - the comparison is only meaningful read across a row.
// ---------------------------------------------------------------------------------------------
const LEG_ORDER = ["direct", "bifrost_messages", "bifrost_responses"];
const anchorCases = new Map();
for (const r of anchors) {
  const entry = anchorCases.get(r.caseKey) || { caseKey: r.caseKey, caseLabel: r.caseLabel, legs: {} };
  entry.caseLabel = entry.caseLabel || r.caseLabel;
  entry.expectHit = r.expectHit;
  entry.legs[r.leg] = r;
  anchorCases.set(r.caseKey, entry);
}

// A Bifrost leg passes when it reproduces the direct leg's caching behaviour. Absolute floor
// (expectHit) is what the live assertion checks; the direct-leg comparison is the extra signal a
// parity table exists to give - a Bifrost leg that reads far less than direct did on identical
// bytes means the gateway dropped a breakpoint, even if it cleared the floor.
const PARITY_TOLERANCE_PP = 15;
const legVerdict = (entry, leg) => {
  const r = entry.legs[leg];
  if (!r) return "-";
  const d = entry.legs.direct;
  const floorOk = r.expectHit ? (r.hitRate || 0) >= (r.hitRateFloor ?? 0.85) : (r.read || 0) > 0;
  if (leg === "direct") return floorOk ? "PASS" : "FAIL";
  if (!d) return floorOk ? "PASS" : "FAIL";
  const parityOk = (r.hitRate || 0) >= (d.hitRate || 0) - PARITY_TOLERANCE_PP / 100;
  return floorOk && parityOk ? "PASS" : "FAIL";
};
const anchorRowVerdict = (entry) =>
  LEG_ORDER.filter((l) => entry.legs[l]).every((l) => legVerdict(entry, l) === "PASS") ? "PASS" : "FAIL";

// A cell that recorded no cache write never observed a cache being created: {{cachePrefix}} is a
// static collection variable, so any run inside the provider's cache TTL starts warm and reads a
// cache some earlier run wrote. That still clears the floor, but it proves only the read path.
// Since hitRate = read / (read + write + uncached), a warm cell reports ~100% whether the write
// accounting is correct or silently dropping tokens - the two are indistinguishable from this row.
// Flagged rather than failed: a hard write > 0 would fail every legitimate re-run within the TTL.
const isWarmStart = (r) => (r.write || 0) === 0 && (r.read || 0) > 0;

const matrixVerdict = (r) => {
  const passed =
    r.mechanism === "implicit" ? (r.hitRate || 0) > 0 : (r.hitRate || 0) >= (r.hitRateFloor ?? 0.85);
  if (!passed) return "FAIL";
  return isWarmStart(r) ? "PASS (warm)" : "PASS";
};

const lines = [];
lines.push("## Prompt Cache Parity Report");
lines.push("");
lines.push(
  `Generated from ${fragmentFiles.length} newman process(es): ` +
    `${anchorCases.size} cache-anchor case(s) across ${anchors.length} leg(s), ${matrix.length} cross-provider matrix cell(s). ` +
    `hitRate = read / (read + write + uncached).`
);
lines.push("");

// Both generated folders always exist in the augmented collection, so an empty section means the
// rows were filtered out of THIS run (PROVIDER=/FEATURE= narrow the collection), not that the
// coverage is missing. Without this the report reads as "the anchor cases ran and were fine".
{
  const providersSeen = new Set(matrix.map((r) => r.provider));
  const scope = [];
  if (!anchorCases.size) {
    scope.push(
      `No Round 34 cache-anchor rows were captured in this run - the folder exists in the collection, ` +
        `so this means the run was filtered (e.g. PROVIDER=), not that the coverage is absent.`
    );
  }
  // else-if, not two ifs: providersSeen is derived from matrix, so an empty matrix always
  // means size === 0 and the branches are mutually exclusive by construction. The empty
  // case needs its own branch because `if (matrix.length)` below drops the whole Round 35
  // section, leaving a report that shows anchors, no matrix, and no reason why.
  if (!matrix.length) {
    scope.push(
      `No Round 35 cross-provider matrix rows were captured in this run - the folder exists in the ` +
        `collection, so this means the run was filtered (e.g. PROVIDER=), not that the coverage is absent.`
    );
  } else if (providersSeen.size === 1) {
    scope.push(
      `The Round 35 matrix covers 34 (provider, model) cells, but only \`${[...providersSeen][0]}\` ` +
        `reported here. Cross-provider parity cannot be judged from a single provider - run unfiltered for that.`
    );
  }
  if (scope.length) {
    lines.push(`> **Scope of this run.** ${scope.join(" ")}`);
    lines.push("");
  }
}

if (anchorCases.size) {
  lines.push("### Round 34 - Mid-Conversation System Cache-Anchor Parity (direct vs Bifrost)");
  lines.push("");
  lines.push(
    `Baseline is the \`direct\` leg (raw Bedrock Converse over SigV4) - identical bytes are sent on every leg, so a ` +
      `Bifrost leg reading materially less than direct did means a dropped cache breakpoint. ` +
      `A Bifrost leg passes when it clears its own floor AND lands within ${PARITY_TOLERANCE_PP}pp of direct. ` +
      `Cases marked \`expectHit=false\` deliberately expect only the system floor (read > 0), not a high hit rate.`
  );
  lines.push("");
  lines.push(
    "| Case | Expect hit | Direct hit | Bifrost /messages | Δ | Bifrost /responses | Δ | Direct r/w/u | /messages r/w/u | /responses r/w/u | Verdict |"
  );
  lines.push("|---|---|---:|---:|---:|---:|---:|---|---|---|---|");
  const rwu = (r) => (r ? `${r.read}/${r.write}/${r.uncached}` : "-");
  for (const entry of [...anchorCases.values()].sort((a, b) => a.caseKey.localeCompare(b.caseKey))) {
    const d = entry.legs.direct;
    const m = entry.legs.bifrost_messages;
    const p = entry.legs.bifrost_responses;
    lines.push(
      `| \`${entry.caseKey}\` | ${entry.expectHit ? "yes" : "no (floor only)"} ` +
        `| ${d ? pct(d.hitRate) : "-"} ` +
        `| ${m ? pct(m.hitRate) : "-"} | ${d && m ? delta(d.hitRate, m.hitRate) : "-"} ` +
        `| ${p ? pct(p.hitRate) : "-"} | ${d && p ? delta(d.hitRate, p.hitRate) : "-"} ` +
        `| ${rwu(d)} | ${rwu(m)} | ${rwu(p)} | ${anchorRowVerdict(entry)} |`
    );
  }
  lines.push("");
}

if (matrix.length) {
  lines.push("### Round 35 - Cross-Provider Prompt-Cache Matrix");
  lines.push("");
  lines.push(
    "Explicit-breakpoint cells are deterministic and asserted against their hit-rate floor. Implicit-caching cells are " +
      "best-effort at the provider, so they assert only that caching engaged at least once across the round series " +
      "(per-round hit rates shown in `Series`)."
  );
  lines.push("");
  if (matrix.some(isWarmStart)) {
    lines.push(
      `\`PASS (warm)\` marks a cell that recorded zero cache writes: it read a cache an earlier run ` +
        `created, so only the read path was exercised. {{cachePrefix}} is static, so re-running inside ` +
        `the provider's cache TTL is the normal cause. To prove the write path, run against a cold ` +
        `cache (wait out the TTL, or perturb the cached prefix).`
    );
    lines.push("");
  }
  lines.push("| Provider | Model | Shape | Mechanism | Arm | Read | Write | Uncached | Hit rate | Bar | Series | Verdict |");
  lines.push("|---|---|---|---|---|---:|---:|---:|---:|---|---|---|");
  for (const r of matrix) {
    const series = Array.isArray(r.series) ? r.series.map((h) => `${(h * 100).toFixed(0)}%`).join(" ") : "-";
    const bar = r.mechanism === "implicit" ? "> 0%" : `>= ${((r.hitRateFloor ?? 0.85) * 100).toFixed(0)}%`;
    lines.push(
      `| ${r.provider} | \`${r.model}\` | ${r.shape} | ${r.mechanism} | ${r.arm} ` +
        `| ${r.read} | ${r.write} | ${r.uncached} | ${pct(r.hitRate)} | ${bar} | ${series} | ${matrixVerdict(r)} |`
    );
  }
  lines.push("");

  const byProvider = new Map();
  for (const r of matrix) {
    const list = byProvider.get(r.provider) || [];
    list.push(r);
    byProvider.set(r.provider, list);
  }
  lines.push("#### Per-provider summary");
  lines.push("");
  lines.push("| Provider | Cells | Passing | Failing |");
  lines.push("|---|---:|---:|---:|");
  for (const [provider, list] of [...byProvider.entries()].sort()) {
    const failing = list.filter((r) => matrixVerdict(r) === "FAIL").length;
    lines.push(`| ${provider} | ${list.length} | ${list.length - failing} | ${failing} |`);
  }
  lines.push("");
}

writeFileSync(OUT, lines.join("\n") + "\n");

const anchorFail = [...anchorCases.values()].filter((e) => anchorRowVerdict(e) === "FAIL").length;
const matrixFail = matrix.filter((r) => matrixVerdict(r) === "FAIL").length;
console.error(
  `[render-cache-parity-report] wrote ${OUT} (${anchorCases.size} anchor case(s), ${anchorFail} failing; ` +
    `${matrix.length} matrix cell(s), ${matrixFail} failing)`
);

// --- htmlextra injection (sequential mode only - parallel mode doesn't emit an html report) ---
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

if (existsSync(HTML)) {
  const START = "<!-- CACHE_PARITY_REPORT_START -->";
  const END = "<!-- CACHE_PARITY_REPORT_END -->";
  const cls = (v) => (v === "FAIL" ? "cp-fail" : "cp-pass");

  const anchorRows = [...anchorCases.values()]
    .sort((a, b) => a.caseKey.localeCompare(b.caseKey))
    .map((entry) => {
      const cell = (leg) => {
        const r = entry.legs[leg];
        if (!r) return "<td>-</td><td>-</td>";
        const d = entry.legs.direct;
        const dl = leg === "direct" || !d ? "-" : delta(d.hitRate, r.hitRate);
        return `<td>${pct(r.hitRate)}</td><td>${dl}</td>`;
      };
      return `<tr class="${cls(anchorRowVerdict(entry))}">
        <td><code>${esc(entry.caseKey)}</code></td>
        <td>${entry.expectHit ? "yes" : "no (floor only)"}</td>
        ${cell("direct")}${cell("bifrost_messages")}${cell("bifrost_responses")}
        <td>${anchorRowVerdict(entry)}</td>
      </tr>`;
    })
    .join("\n");

  const matrixRows = matrix
    .map(
      (r) => `<tr class="${cls(matrixVerdict(r))}">
        <td>${esc(r.provider)}</td><td><code>${esc(r.model)}</code></td>
        <td>${esc(r.shape)}</td><td>${esc(r.mechanism)}</td><td>${esc(r.arm)}</td>
        <td>${r.read}</td><td>${r.write}</td><td>${r.uncached}</td><td>${pct(r.hitRate)}</td>
        <td>${Array.isArray(r.series) ? esc(r.series.map((h) => `${(h * 100).toFixed(0)}%`).join(" ")) : "-"}</td>
        <td>${matrixVerdict(r)}</td>
      </tr>`
    )
    .join("\n");

  const section = `${START}
<style>
  #cache-parity-report { padding: 16px 24px; font-family: system-ui, sans-serif; font-size: 13px; }
  #cache-parity-report h2 { margin: 0 0 6px; font-size: 18px; }
  #cache-parity-report h3 { margin: 18px 0 6px; font-size: 15px; }
  #cache-parity-report p { margin: 0 0 12px; opacity: 0.8; }
  #cache-parity-report table { border-collapse: collapse; width: 100%; }
  #cache-parity-report th, #cache-parity-report td { border: 1px solid rgba(128,128,128,0.4); padding: 4px 8px; text-align: right; white-space: nowrap; }
  #cache-parity-report th:nth-child(-n+2), #cache-parity-report td:nth-child(-n+2) { text-align: left; }
  #cache-parity-report tr.cp-fail { background: rgba(220,53,69,0.15); }
  #cache-parity-report tr.cp-pass { background: rgba(40,167,69,0.08); }
  body.theme-light #cache-parity-report { color: #1a1a1a; }
  body.theme-dark #cache-parity-report { color: #e6e6e6; }
</style>
<div id="cache-parity-report">
  <h2>Prompt Cache Parity Report</h2>
  <p>${fragmentFiles.length} newman process(es). Anchor: ${anchorCases.size} case(s), ${anchorFail} failing. Matrix: ${matrix.length} cell(s), ${matrixFail} failing. Full breakdown: tmp/harness-cache-parity.md</p>
  ${
    anchorCases.size
      ? `<h3>Round 34 - Mid-Conversation System Cache-Anchor Parity (direct vs Bifrost)</h3>
  <table>
    <thead><tr>
      <th>Case</th><th>Expect hit</th>
      <th>Direct hit</th><th>Δ</th>
      <th>Bifrost /messages</th><th>Δ</th>
      <th>Bifrost /responses</th><th>Δ</th>
      <th>Verdict</th>
    </tr></thead>
    <tbody>
${anchorRows}
    </tbody>
  </table>`
      : ""
  }
  ${
    matrix.length
      ? `<h3>Round 35 - Cross-Provider Prompt-Cache Matrix</h3>
  <table>
    <thead><tr>
      <th>Provider</th><th>Model</th><th>Shape</th><th>Mechanism</th><th>Arm</th>
      <th>Read</th><th>Write</th><th>Uncached</th><th>Hit rate</th><th>Series</th><th>Verdict</th>
    </tr></thead>
    <tbody>
${matrixRows}
    </tbody>
  </table>`
      : ""
  }
</div>
${END}`;

  const html = readFileSync(HTML, "utf8");
  const markerRe = new RegExp(`${START}[\\s\\S]*?${END}`);
  const injected = markerRe.test(html)
    ? html.replace(markerRe, section)
    : html.replace(/<body[^>]*>/, (m) => `${m}\n${section}`);
  if (injected === html && !markerRe.test(html)) {
    console.error(`[render-cache-parity-report] no <body> tag found in ${HTML} - skipped HTML injection`);
  } else {
    writeFileSync(HTML, injected);
    console.error(`[render-cache-parity-report] injected cache parity table into ${HTML}`);
  }
} else {
  console.error(`[render-cache-parity-report] ${HTML} not found (parallel mode doesn't emit htmlextra) - skipped HTML injection`);
}
