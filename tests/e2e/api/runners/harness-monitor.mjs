#!/usr/bin/env node
// Live terminal progress monitor for `make run-provider-harness-test`.
//
// Tails per-provider newman CLI logs (parallel mode) or the merged CLI log
// (sequential mode), aggregates pass/fail/% per provider with folder breakdown,
// elapsed time + ETA, and most-recent failure text. Renders an in-place table.
//
// Usage:
//   node harness-monitor.mjs \
//     --mode parallel \
//     --providers "openai anthropic bedrock gemini vertex azure passthrough" \
//     --tmp-dir tmp \
//     --status-file tmp/parallel-status \
//     --launched 7
//
//   node harness-monitor.mjs \
//     --mode sequential \
//     --providers "openai anthropic" \
//     --tmp-dir tmp \
//     --log tmp/newman-cli.log

import { existsSync, readFileSync, statSync, openSync, readSync, closeSync } from "node:fs";
import { join } from "node:path";

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, cur, i, arr) => {
    if (cur.startsWith("--")) {
      const key = cur.slice(2);
      const next = arr[i + 1];
      acc.push([key, next && !next.startsWith("--") ? next : "true"]);
    }
    return acc;
  }, [])
);

const MODE = args.mode === "sequential" ? "sequential" : "parallel";
const PROVIDERS = (args.providers || "").trim().split(/\s+/).filter(Boolean);
const TMP_DIR = args["tmp-dir"] || "tmp";
const STATUS_FILE = args["status-file"] || join(TMP_DIR, "parallel-status");
const LAUNCHED = parseInt(args.launched || String(PROVIDERS.length), 10);
const SEQ_LOG = args.log || join(TMP_DIR, "newman-cli.log");
const TAIL_INTERVAL_MS = 250;
const RENDER_INTERVAL_MS = 1000;
const IDLE_EXIT_MS = 3000;

if (PROVIDERS.length === 0) {
  console.error("[harness-monitor] --providers is required");
  process.exit(2);
}

// Mirror filter-collection.mjs PROVIDER_KEYWORDS. Used only in sequential mode
// to route folder/request lines (which lack a [provider] prefix) to a provider.
const PROVIDER_KEYWORDS = {
  openai: ["openai", "gpt-", "o3", "o1"],
  anthropic: ["anthropic", "claude-"],
  bedrock: ["bedrock"],
  gemini: ["gemini", "genai", "googlesearch"],
  vertex: ["vertex"],
  azure: ["azure", "deployments"],
  passthrough: ["_passthrough", "passthrough"],
};

const ANSI_RE = /\x1b\[[0-9;?]*[A-Za-z]/g;
const stripAnsi = (s) => s.replace(ANSI_RE, "");

// State per provider. status transitions: pending -> running -> pass/fail/skipped.
const state = {
  startedAt: Date.now(),
  mode: MODE,
  providers: Object.fromEntries(
    PROVIDERS.map((p) => [
      p,
      {
        status: "pending",
        totalRequests: 0,
        doneRequests: 0,
        pass: 0,
        fail: 0,
        folders: {},
        folderOrder: [],
        currentFolder: null,
        currentRequest: null,
        currentRequestDone: false,
        currentRequestHadFail: false,
        currentRequestFolder: null,
        lastFailure: null,
      },
    ])
  ),
};
let lastByteAt = Date.now();
let lastRenderLines = 0;

// ----- Denominator: walk the filtered collection per provider. ----------------

function countLeaves(items, perFolder, topFolder) {
  if (!Array.isArray(items)) return 0;
  let total = 0;
  for (const node of items) {
    if (Array.isArray(node.item)) {
      const next = topFolder ?? node.name ?? "(root)";
      total += countLeaves(node.item, perFolder, next);
    } else if (node.request) {
      total += 1;
      const folder = topFolder ?? "(root)";
      if (!perFolder[folder]) perFolder[folder] = { total: 0, pass: 0, fail: 0 };
      perFolder[folder].total += 1;
    }
  }
  return total;
}

function loadDenominators() {
  for (const p of PROVIDERS) {
    const ps = state.providers[p];
    // Parallel mode writes tmp/harness-filtered-<p>.json per provider.
    // Sequential mode writes tmp/harness-filtered.json once, or falls back to the source collection.
    const candidates =
      MODE === "parallel"
        ? [join(TMP_DIR, `harness-filtered-${p}.json`)]
        : [
            join(TMP_DIR, "harness-filtered.json"),
            "tests/e2e/api/collections/provider-harness.json",
          ];
    for (const path of candidates) {
      if (!existsSync(path)) continue;
      try {
        const data = JSON.parse(readFileSync(path, "utf8"));
        const folders = {};
        const total = countLeaves(data.item || [], folders, null);
        ps.totalRequests = total;
        ps.folders = folders;
        ps.folderOrder = Object.keys(folders);
        break;
      } catch {
        // ignore - try next candidate
      }
    }
  }
}

// ----- Tail: poll-based incremental read of newman CLI logs. ------------------

const tails = new Map(); // path -> { provider, offset, buf }

function ensureTail(path, provider) {
  if (!tails.has(path)) tails.set(path, { provider, offset: 0, buf: "" });
}

function readNewBytes() {
  for (const [path, h] of tails) {
    let st;
    try {
      st = statSync(path);
    } catch {
      continue;
    }
    if (st.size <= h.offset) continue;
    const len = st.size - h.offset;
    const buf = Buffer.alloc(len);
    let fd;
    try {
      fd = openSync(path, "r");
      readSync(fd, buf, 0, len, h.offset);
    } catch {
      if (fd != null) try { closeSync(fd); } catch {}
      continue;
    }
    closeSync(fd);
    h.offset = st.size;
    h.buf += buf.toString("utf8");
    const lines = h.buf.split("\n");
    h.buf = lines.pop();
    for (const raw of lines) handleLine(stripAnsi(raw), h.provider);
    lastByteAt = Date.now();
  }
}

// ----- Parsing ----------------------------------------------------------------

const RE_PREFIX = /^\[([a-z]+)\]\s?(.*)$/;
const RE_FOLDER = /^❏\s+(.+?)\s*$/;
const RE_REQUEST = /^↳\s+(.+?)\s*$/;
const RE_REQUEST_DONE = /\[\s*\d+(?:\s+[A-Za-z]+)?,\s*[\d.]+\s*[kMG]?B,\s*[\d.]+\s*m?s\s*\]/;
const RE_ASSERT_FAIL = /^\s*\d+\.\s+(.+?)$/;

function inferProviderFromLine(line) {
  const lower = line.toLowerCase();
  for (const p of PROVIDERS) {
    const kws = PROVIDER_KEYWORDS[p] || [p];
    for (const k of kws) if (lower.includes(k)) return p;
  }
  return null;
}

// Newman emits per-request lines in this order: ↳ start, then the [size,duration]
// summary, then ✓ pass-assertions, then numbered fail lines. So we can't commit
// pass/fail at the summary line - we'd miss subsequent fail lines. Instead we
// defer commit until the next ↳ / ❏ / finalizeAll().
function finalizeRequest(ps) {
  if (!ps.currentRequest) return;
  if (ps.currentRequestDone) {
    if (ps.currentRequestHadFail) ps.fail += 1;
    else ps.pass += 1;
    const f = ps.currentRequestFolder;
    if (f && ps.folders[f]) {
      if (ps.currentRequestHadFail) ps.folders[f].fail += 1;
      else ps.folders[f].pass += 1;
    }
  }
  ps.currentRequest = null;
  ps.currentRequestDone = false;
  ps.currentRequestHadFail = false;
  ps.currentRequestFolder = null;
}

function finalizeAll() {
  for (const p of PROVIDERS) finalizeRequest(state.providers[p]);
}

function handleLine(line, taggedProvider) {
  let provider = taggedProvider;
  let body = line;

  if (MODE === "parallel") {
    const m = line.match(RE_PREFIX);
    if (m && state.providers[m[1]]) {
      provider = m[1];
      body = m[2];
    } else if (!provider) {
      return;
    }
  }

  const ps = state.providers[provider];
  if (!ps) return;
  if (ps.status === "pending") ps.status = "running";

  const trimmed = body.trimStart();

  let m;
  if ((m = trimmed.match(RE_FOLDER))) {
    finalizeRequest(ps);
    const folder = m[1].trim();
    ps.currentFolder = folder;
    if (!ps.folders[folder]) {
      ps.folders[folder] = { total: 0, pass: 0, fail: 0 };
      ps.folderOrder.push(folder);
    }
    return;
  }
  if ((m = trimmed.match(RE_REQUEST))) {
    finalizeRequest(ps);
    ps.currentRequest = m[1].trim();
    ps.currentRequestDone = false;
    ps.currentRequestHadFail = false;
    ps.currentRequestFolder = ps.currentFolder;
    return;
  }
  // Disambiguate request-done summary from assertion-fail; check done first.
  if (RE_REQUEST_DONE.test(trimmed)) {
    if (ps.currentRequest && !ps.currentRequestDone) {
      ps.currentRequestDone = true;
      ps.doneRequests += 1;
    }
    return;
  }
  if ((m = trimmed.match(RE_ASSERT_FAIL)) && ps.currentRequest) {
    ps.currentRequestHadFail = true;
    ps.lastFailure = { folder: ps.currentRequestFolder, text: m[1].trim() };
    return;
  }
}

// ----- Status file: pick up final pass/fail verdicts in parallel mode. --------

function readStatusFile() {
  if (MODE !== "parallel") return { lines: 0 };
  if (!existsSync(STATUS_FILE)) return { lines: 0 };
  let content;
  try {
    content = readFileSync(STATUS_FILE, "utf8");
  } catch {
    return { lines: 0 };
  }
  const lines = content.trim().split("\n").filter(Boolean);
  for (const ln of lines) {
    const [p, v] = ln.split(":");
    const ps = state.providers[p];
    if (!ps) continue;
    if (v === "pass") ps.status = "pass";
    else if (v === "fail") ps.status = "fail";
  }
  return { lines: lines.length };
}

// ----- Render -----------------------------------------------------------------

const C = {
  reset: "\x1b[0m",
  bold: "\x1b[1m",
  dim: "\x1b[2m",
  red: "\x1b[31m",
  green: "\x1b[32m",
  yellow: "\x1b[33m",
  cyan: "\x1b[36m",
  gray: "\x1b[90m",
};

function fmtDuration(ms) {
  if (!isFinite(ms) || ms < 0) return "--:--";
  const s = Math.floor(ms / 1000);
  const m = Math.floor(s / 60);
  const r = s % 60;
  return `${String(m).padStart(2, "0")}:${String(r).padStart(2, "0")}`;
}

function truncate(s, n) {
  if (!s) return "";
  return s.length <= n ? s : s.slice(0, n - 1) + "…";
}

function padRight(s, n) {
  const str = String(s);
  return str.length >= n ? str.slice(0, n) : str + " ".repeat(n - str.length);
}
function padLeft(s, n) {
  const str = String(s);
  return str.length >= n ? str.slice(0, n) : " ".repeat(n - str.length) + str;
}

function statusGlyph(status) {
  switch (status) {
    case "pass": return `${C.green}✓${C.reset}`;
    case "fail": return `${C.red}✗${C.reset}`;
    case "running": return `${C.cyan}●${C.reset}`;
    case "skipped": return `${C.gray}-${C.reset}`;
    default: return `${C.gray}·${C.reset}`;
  }
}

function renderFrame() {
  const cols = process.stdout.columns || 120;

  // Aggregate totals.
  let aggDone = 0, aggTotal = 0, aggPass = 0, aggFail = 0;
  for (const p of PROVIDERS) {
    const ps = state.providers[p];
    aggDone += ps.doneRequests;
    aggTotal += ps.totalRequests;
    aggPass += ps.pass;
    aggFail += ps.fail;
  }
  const elapsed = Date.now() - state.startedAt;
  const eta =
    aggDone > 0 && aggTotal > aggDone ? elapsed * (aggTotal / aggDone - 1) : NaN;

  const out = [];
  out.push(
    `${C.bold}Bifrost Provider Harness - live${C.reset}` +
      `   ${C.dim}Elapsed${C.reset} ${fmtDuration(elapsed)}` +
      `   ${C.dim}ETA${C.reset} ${fmtDuration(eta)}` +
      `   ${C.dim}Mode${C.reset} ${state.mode}`
  );

  // Table width math: each cell consumes (width + 3) chars (" content │"),
  // plus 1 leading "│". So total = 1 + 3N + sum(widths). Compute the failure
  // column from terminal width to guarantee no row wraps. Drop the column
  // entirely if there isn't even 20 chars left for it.
  const fixed = [1, 12, 9, 5, 5, 5];
  const fixedSum = fixed.reduce((a, b) => a + b, 0);
  const overheadWith7 = 1 + 3 * 7; // 22
  const overheadWith6 = 1 + 3 * 6; // 19
  const targetWidth = Math.max(40, cols - 1);
  const failColWidth = targetWidth - overheadWith7 - fixedSum;
  const showFailureCol = failColWidth >= 20;
  const headers = showFailureCol
    ? ["", "Provider", "Done", "Pass", "Fail", "%", "Last failure"]
    : ["", "Provider", "Done", "Pass", "Fail", "%"];
  const widths = showFailureCol ? [...fixed, failColWidth] : fixed;

  const sep = (left, mid, right, fill = "─") => {
    let line = left;
    for (let i = 0; i < widths.length; i++) {
      line += fill.repeat(widths[i] + 2);
      line += i === widths.length - 1 ? right : mid;
    }
    return line;
  };

  const row = (cells) => {
    let line = "│";
    for (let i = 0; i < cells.length; i++) {
      line += " " + padRight(cells[i], widths[i]) + " │";
    }
    return line;
  };

  out.push(sep("┌", "┬", "┐"));
  out.push(row(headers));
  out.push(sep("├", "┼", "┤"));

  for (const p of PROVIDERS) {
    const ps = state.providers[p];
    const pct = ps.totalRequests ? Math.floor((100 * ps.doneRequests) / ps.totalRequests) : 0;
    const doneCell = `${padLeft(ps.doneRequests, 3)}/${padRight(ps.totalRequests, 3)}`;
    const failCellRaw = ps.fail > 0 ? `${C.red}${padLeft(ps.fail, widths[4])}${C.reset}` : padLeft(ps.fail, widths[4]);
    const cells = [
      statusGlyph(ps.status),
      p,
      doneCell,
      padLeft(ps.pass, widths[3]),
      // failCell: pre-padded so the row() pad-right is a no-op for this cell
      failCellRaw,
      `${pct}%`,
    ];
    if (showFailureCol) {
      cells.push(truncate(ps.lastFailure?.text || (ps.currentRequest || "-"), widths[6]));
    }
    out.push(rowWithRawCells(cells, widths));
  }

  out.push(sep("├", "┼", "┤"));
  const totalPct = aggTotal ? Math.floor((100 * aggDone) / aggTotal) : 0;
  const totalCells = [
    "",
    `${C.bold}TOTAL${C.reset}`,
    `${padLeft(aggDone, 3)}/${padRight(aggTotal, 3)}`,
    padLeft(aggPass, widths[3]),
    aggFail > 0 ? `${C.red}${padLeft(aggFail, widths[4])}${C.reset}` : padLeft(aggFail, widths[4]),
    `${totalPct}%`,
  ];
  if (showFailureCol) totalCells.push("");
  out.push(rowWithRawCells(totalCells, widths));
  out.push(sep("└", "┴", "┘"));

  // Folder breakdown: show each running provider's currentFolder + last few folders.
  out.push("");
  out.push(`${C.bold}Current folders${C.reset}`);
  for (const p of PROVIDERS) {
    const ps = state.providers[p];
    if (ps.totalRequests === 0) continue;
    const cur = ps.currentFolder;
    if (!cur) {
      out.push(`  ${padRight(p, 12)} ${C.gray}(waiting)${C.reset}`);
      continue;
    }
    const f = ps.folders[cur] || { total: 0, pass: 0, fail: 0 };
    const doneInFolder = f.pass + f.fail;
    out.push(
      `  ${padRight(p, 12)} ${C.cyan}${truncate(cur, 40)}${C.reset}  ` +
        `${doneInFolder}/${f.total} ` +
        `(${C.green}✓ ${f.pass}${C.reset}, ${f.fail > 0 ? C.red : C.dim}✗ ${f.fail}${C.reset})`
    );
  }

  return out;
}

// Cell may contain ANSI escapes; padRight in row() would break alignment. So
// compute visible length, then pad with spaces externally.
function rowWithRawCells(cells, widths) {
  let line = "│";
  for (let i = 0; i < cells.length; i++) {
    const raw = String(cells[i]);
    const visible = raw.replace(ANSI_RE, "");
    const w = widths[i];
    const padded = visible.length >= w ? raw : raw + " ".repeat(w - visible.length);
    line += " " + padded + " │";
  }
  return line;
}

function draw() {
  const lines = renderFrame();
  const rows = process.stdout.rows || lines.length;
  // Clamp to terminal height so we don't push the title off the top.
  const visible = lines.slice(0, Math.max(1, rows - 1));
  let out = "\x1b[H"; // cursor home (alt screen, so this is the buffer origin)
  for (const ln of visible) out += ln + "\x1b[K\n";
  out += "\x1b[J"; // clear from cursor to end-of-screen (wipes prior taller frame's tail)
  process.stdout.write(out);
  lastRenderLines = visible.length;
}

// ----- Lifecycle --------------------------------------------------------------

function setupTails() {
  if (MODE === "parallel") {
    for (const p of PROVIDERS) {
      ensureTail(join(TMP_DIR, `newman-cli-${p}.log`), p);
    }
  } else {
    // Sequential: one shared log, provider inferred per-line.
    ensureTail(SEQ_LOG, null);
  }
}

function shouldExit() {
  if (MODE === "parallel") {
    const { lines } = readStatusFile();
    if (lines >= LAUNCHED && Date.now() - lastByteAt > IDLE_EXIT_MS) return true;
  } else {
    // Sequential mode: rely on signals from the Makefile. Also exit when the
    // log shows the newman "failures" summary block AND we've been idle.
    if (Date.now() - lastByteAt > IDLE_EXIT_MS * 2 && lastRenderLines > 0) {
      const allDone = PROVIDERS.every(
        (p) => state.providers[p].totalRequests === 0 ||
               state.providers[p].doneRequests >= state.providers[p].totalRequests
      );
      if (allDone) return true;
    }
  }
  return false;
}

function teardown(code = 0) {
  // Drain any pending bytes the tail timer hasn't picked up yet, then commit
  // the trailing in-flight request before the final frame.
  readNewBytes();
  finalizeAll();
  draw();
  // Snapshot the final frame to stderr so it persists on the main screen
  // after we leave the alt buffer (otherwise the user sees the table vanish).
  const finalLines = renderFrame();
  // Leave alt screen, restore cursor, then print the persistent snapshot.
  process.stdout.write("\x1b[?25h\x1b[?1049l");
  process.stderr.write(finalLines.join("\n") + "\n");
  process.exit(code);
}

process.on("SIGTERM", () => teardown(0));
process.on("SIGINT", () => teardown(130));
process.on("SIGHUP", () => teardown(0));

// Enter alt screen buffer + hide cursor + clear it. This gives us a fresh
// canvas with a known origin so cursor-home redraws are deterministic and
// the preamble (boot logs, launch messages) is preserved on the main screen.
process.stdout.write("\x1b[?1049h\x1b[H\x1b[2J\x1b[?25l");

// Initial denominator pass; retry once a second until at least one provider has totals.
loadDenominators();
const denomTimer = setInterval(() => {
  const haveAny = PROVIDERS.some((p) => state.providers[p].totalRequests > 0);
  if (!haveAny) loadDenominators();
  else clearInterval(denomTimer);
}, 1000);

setupTails();
setInterval(() => {
  readNewBytes();
  readStatusFile();
}, TAIL_INTERVAL_MS);

setInterval(() => {
  draw();
  if (shouldExit()) teardown(0);
}, RENDER_INTERVAL_MS);

// Draw a first frame immediately so the user sees something.
draw();
