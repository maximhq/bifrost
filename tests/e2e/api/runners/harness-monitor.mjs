#!/usr/bin/env node
// Live terminal progress monitor for `make run-provider-harness-test`.
//
// Tails per-provider newman CLI logs (parallel mode) or the single CLI log
// (sequential mode) and renders ONE thing: a provider x total/pass/failed
// table. That is deliberately the entire output surface of a harness run -
// per-request chatter, folder breakdowns and failure text all live in the
// artifacts (tmp/newman-cli*.log, tmp/harness-failures.md), not on the
// terminal, so a running harness is readable at a glance.
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
//     --log tmp/newman-cli.log \
//     --collection tmp/harness-cache-filtered.json
//
// --collection pins the denominator source (defaults per mode below). It
// matters for the deferred passes - cache-parity runs its own filtered
// collection, so without it the Total column would be taken from the main
// pass's collection and be wrong.
//
// Add --ci for GitHub Actions: no alternate screen buffer and no in-place
// redraw (impossible in an append-only job log). Reprints the same table
// every --ci-interval seconds (default 5) and once more at teardown, into
// stdout + $GITHUB_STEP_SUMMARY.

import {
  existsSync,
  readFileSync,
  statSync,
  openSync,
  readSync,
  closeSync,
  appendFileSync,
} from "node:fs";
import { join } from "node:path";
import { resolveCiIntervalMs } from "./lib/ci-interval.mjs";
import {
  makeMatchProvider,
  providerOfItem,
  providerOfLogLine,
} from "./lib/provider-attribution.mjs";

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
const COLLECTION = args.collection && args.collection !== "true" ? args.collection : null;
const TAIL_INTERVAL_MS = 250;
const RENDER_INTERVAL_MS = 1000;
const IDLE_EXIT_MS = 3000;

// CI mode: GitHub Actions logs are an append-only stream, so the alternate
// screen buffer + cursor-home redraw used interactively is impossible there.
// Instead we reprint the same table on an interval - identical content, the
// only difference being append vs redraw-in-place.
const CI = args.ci === "true" || args.ci === "1";
const CI_INTERVAL_MS = resolveCiIntervalMs(args["ci-interval"]);

if (PROVIDERS.length === 0) {
  console.error("[harness-monitor] --providers is required");
  process.exit(2);
}

// The keyword table, match order and base64 guard now live in
// lib/provider-attribution.mjs, shared with the tests that pin them.

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
        // doneRequests is not a column any more, but it is still the ETA
        // numerator - pass+fail lags it by one deferred request (see
        // finalizeRequest), which would make the ETA jitter.
        doneRequests: 0,
        pass: 0,
        fail: 0,
        currentRequest: null,
        currentRequestDone: false,
        currentRequestHadFail: false,
      },
    ])
  ),
};
let lastByteAt = Date.now();
let lastRenderLines = 0;
let sawBytes = false;

// ----- Denominator: walk the filtered collection per provider. ----------------

function walkLeaves(items, ancestors, visit) {
  if (!Array.isArray(items)) return;
  for (const node of items) {
    if (Array.isArray(node.item)) {
      walkLeaves(node.item, [...ancestors, node.name ?? "(root)"], visit);
    } else if (node.request) {
      visit(node, ancestors);
    }
  }
}

// Restricted to the providers this run tracks, so a keyword cannot assign a row
// to a provider that has no column in the table.
const matchProvider = makeMatchProvider((p) => !!state.providers[p]);

const itemProvider = (item, ancestors) => providerOfItem(item, ancestors, matchProvider);

function loadDenominators() {
  // Parallel mode already has the split done for it on disk: one filtered
  // collection per provider fork, so counting leaves is enough.
  if (MODE === "parallel" && !COLLECTION) {
    for (const p of PROVIDERS) {
      const path = join(TMP_DIR, `harness-filtered-${p}.json`);
      if (!existsSync(path)) continue;
      try {
        const data = JSON.parse(readFileSync(path, "utf8"));
        let total = 0;
        walkLeaves(data.item || [], [], () => { total += 1; });
        state.providers[p].totalRequests = total;
      } catch {
        // Partially-written file mid-fork; the retry timer picks it up.
      }
    }
    return;
  }

  // Sequential mode runs one collection covering every provider, so the split
  // has to be recomputed here by keyword. Leaves that match no provider (auth
  // matrix, management APIs) are intentionally uncounted - nothing in the CLI
  // log would attribute them either, so counting them would leave every run
  // stuck short of its own total.
  const candidates = COLLECTION
    ? [COLLECTION]
    : [
        join(TMP_DIR, "harness-filtered.json"),
        "tests/e2e/api/collections/provider-harness.json",
      ];
  for (const path of candidates) {
    if (!existsSync(path)) continue;
    try {
      const data = JSON.parse(readFileSync(path, "utf8"));
      const counts = Object.fromEntries(PROVIDERS.map((p) => [p, 0]));
      walkLeaves(data.item || [], [], (node, ancestors) => {
        const p = itemProvider(node, ancestors);
        if (p && counts[p] !== undefined) counts[p] += 1;
      });
      for (const p of PROVIDERS) state.providers[p].totalRequests = counts[p];
      break;
    } catch {
      // ignore - try next candidate
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
    sawBytes = true;
  }
}

// ----- Parsing ----------------------------------------------------------------

const RE_PREFIX = /^\[([a-z_]+)\]\s?(.*)$/;
const RE_FOLDER = /^❏\s+(.+?)\s*$/;
const RE_REQUEST = /^↳\s+(.+?)\s*$/;
// The line newman prints under ↳: "POST http://host/path [200 OK, 1.2kB, 340ms]".
const RE_METHOD_URL = /^(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+https?:\/\//i;
const RE_REQUEST_DONE = /\[\s*\d+(?:\s+[A-Za-z]+)?,\s*[\d.]+\s*[kMG]?B,\s*[\d.]+\s*m?s\s*\]/;
// A request that never got a response (connection refused, DNS, timeout) gets
// "[errored]" where the [status, size, duration] summary would be. Without this
// it matches no rule at all and the request is counted as neither pass nor fail
// - so a gateway that is down reads as a table of zeroes rather than failures.
const RE_REQUEST_ERRORED = /\[errored\]/;
const RE_ASSERT_FAIL = /^\s*\d+\.\s+(.+?)$/;

function inferProviderFromLine(line) {
  return providerOfLogLine(line, seqFolder, matchProvider);
}

// Sequential mode: only ❏/↳ lines name a provider. Everything after one (the
// [200 OK, …] summary, ✓/✗ assertion lines) belongs to whatever that line
// named, so the attribution is sticky and is only ever replaced by a
// successful inference - never cleared by an unattributable folder heading.
let seqProvider = null;
// Request name seen on a ↳ line, held until the URL line resolves its owner.
let seqPendingName = null;
// The last "❏ <name>" heading. This is the attribution signal that reads the
// same here as it does in the collection: newman echoes folder names verbatim,
// while it substitutes {{variables}} in URLs. Matching a URL on this side and
// the raw URL on the denominator side is what let a Vertex row be counted under
// vertex and passed under gemini - its raw URL matches "vertex" only through
// the literal {{vertexModel}}, which is gone by the time newman prints it.
let seqFolder = null;

// Newman emits per-request lines in this order: ↳ start, then the [size,duration]
// summary, then ✓ pass-assertions, then numbered fail lines. So we can't commit
// pass/fail at the summary line - we'd miss subsequent fail lines. Instead we
// defer commit until the next ↳ / ❏ / finalizeAll().
function finalizeRequest(ps) {
  if (!ps.currentRequest) return;
  if (ps.currentRequestDone) {
    if (ps.currentRequestHadFail) ps.fail += 1;
    else ps.pass += 1;
  }
  ps.currentRequest = null;
  ps.currentRequestDone = false;
  ps.currentRequestHadFail = false;
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
  } else if (!provider) {
    const t = body.trimStart();
    let m;
    if ((m = t.match(RE_FOLDER))) {
      seqFolder = m[1].trim();
      if (seqProvider) finalizeRequest(state.providers[seqProvider]);
      // A heading that names a provider re-points attribution immediately,
      // so the first row under it is attributed even before its URL line.
      const fromFolder = providerOfLogLine("", seqFolder, matchProvider);
      if (fromFolder) seqProvider = fromFolder;
      return;
    }
    // A "↳ name" line names the request but not the backend - plenty of rows are
    // called things like "Prompt caching (cache_control: ephemeral)". The URL on
    // the following METHOD line is the same string the denominator pass matched
    // on, so deferring attribution until that line is what keeps pass+fail from
    // exceeding a provider's own total.
    if ((m = t.match(RE_REQUEST))) {
      if (seqProvider) finalizeRequest(state.providers[seqProvider]);
      seqPendingName = m[1].trim();
      return;
    }
    if (RE_METHOD_URL.test(t)) {
      const inferred =
        inferProviderFromLine(t) ||
        (seqPendingName ? inferProviderFromLine(seqPendingName) : null);
      if (inferred && inferred !== seqProvider) {
        // Commit the outgoing provider's deferred request before handing over.
        if (seqProvider) finalizeRequest(state.providers[seqProvider]);
        seqProvider = inferred;
      }
      const target = state.providers[seqProvider];
      if (target && seqPendingName) {
        target.currentRequest = seqPendingName;
        target.currentRequestDone = false;
        target.currentRequestHadFail = false;
        seqPendingName = null;
      }
    }
    provider = seqProvider;
    if (!provider) return;
  }

  const ps = state.providers[provider];
  if (!ps) return;
  if (ps.status === "pending") ps.status = "running";

  const trimmed = body.trimStart();

  let m;
  if (RE_FOLDER.test(trimmed)) {
    finalizeRequest(ps);
    return;
  }
  if (MODE === "parallel" && (m = trimmed.match(RE_REQUEST))) {
    finalizeRequest(ps);
    ps.currentRequest = m[1].trim();
    ps.currentRequestDone = false;
    ps.currentRequestHadFail = false;
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
  if (RE_REQUEST_ERRORED.test(trimmed)) {
    if (ps.currentRequest && !ps.currentRequestDone) {
      ps.currentRequestDone = true;
      ps.currentRequestHadFail = true;
      ps.doneRequests += 1;
    }
    return;
  }
  // Failure text is not rendered anywhere - it belongs to tmp/harness-failures.md
  // (analyze-failures.mjs) and the per-provider CLI logs. All the table needs is
  // that this request failed.
  if (RE_ASSERT_FAIL.test(trimmed) && ps.currentRequest) {
    ps.currentRequestHadFail = true;
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
    const prev = ps.status;
    if (v === "pass") ps.status = "pass";
    else if (v === "fail") ps.status = "fail";
    // A status-file line is only written after that provider's newman process
    // exited, so its log is complete - commit the trailing deferred request
    // now, otherwise a finished provider sits one request short of its total
    // in every subsequent frame.
    if (ps.status !== prev && (ps.status === "pass" || ps.status === "fail")) {
      finalizeRequest(ps);
    }
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

// The whole output surface: one header line + one table. Identical in CI and
// interactively - CI reprints it, interactive redraws it in place.
function renderFrame() {
  let aggDone = 0, aggTotal = 0, aggPass = 0, aggFail = 0;
  for (const p of PROVIDERS) {
    const ps = state.providers[p];
    aggDone += ps.doneRequests;
    aggTotal += ps.totalRequests;
    aggPass += ps.pass;
    aggFail += ps.fail;
  }
  const elapsed = Date.now() - state.startedAt;
  // ETA off completion rate, not pass/fail, so the one deferred request per
  // provider doesn't wobble it.
  const eta =
    aggDone > 0 && aggTotal > aggDone ? elapsed * (aggTotal / aggDone - 1) : NaN;

  const out = [];
  out.push(
    `${C.bold}Bifrost Provider Harness${C.reset}` +
      `   ${C.dim}Elapsed${C.reset} ${fmtDuration(elapsed)}` +
      `   ${C.dim}ETA${C.reset} ${fmtDuration(eta)}`
  );

  // Fixed widths - the table is ~45 columns, so unlike the previous layout
  // there is nothing to fit to terminal width.
  const nameWidth = Math.max(8, "TOTAL".length, ...PROVIDERS.map((p) => p.length));
  const headers = ["", "Provider", "Total", "Pass", "Failed"];
  const widths = [1, nameWidth, 5, 4, 6];

  const sep = (left, mid, right, fill = "─") => {
    let line = left;
    for (let i = 0; i < widths.length; i++) {
      line += fill.repeat(widths[i] + 2);
      line += i === widths.length - 1 ? right : mid;
    }
    return line;
  };

  out.push(sep("┌", "┬", "┐"));
  out.push(rowWithRawCells(headers, widths));
  out.push(sep("├", "┼", "┤"));

  const failCell = (n, w) =>
    n > 0 ? `${C.red}${padLeft(n, w)}${C.reset}` : padLeft(n, w);

  for (const p of PROVIDERS) {
    const ps = state.providers[p];
    out.push(
      rowWithRawCells(
        [
          statusGlyph(ps.status),
          p,
          padLeft(ps.totalRequests, widths[2]),
          padLeft(ps.pass, widths[3]),
          failCell(ps.fail, widths[4]),
        ],
        widths
      )
    );
  }

  out.push(sep("├", "┼", "┤"));
  out.push(
    rowWithRawCells(
      [
        "",
        `${C.bold}TOTAL${C.reset}`,
        padLeft(aggTotal, widths[2]),
        padLeft(aggPass, widths[3]),
        failCell(aggFail, widths[4]),
      ],
      widths
    )
  );
  out.push(sep("└", "┴", "┘"));

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

// ----- CI render: append-only, no cursor control. -----------------------------

// Reprint the table. Colour is stripped: an Actions log renders ANSI, but the
// table is also the thing people copy out of it, and a colourless one diffs
// cleanly between runs.
function drawCi() {
  process.stdout.write(renderFrame().map(stripAnsi).join("\n") + "\n\n");
}

// Final plain-text table. Goes to stdout so it lands in the job log, and to
// $GITHUB_STEP_SUMMARY (when set) so it renders on the workflow summary page.
function ciFinalReport() {
  const plain = renderFrame().map(stripAnsi);
  process.stdout.write("\n" + plain.join("\n") + "\n");
  const summaryPath = process.env.GITHUB_STEP_SUMMARY;
  if (!summaryPath) return;
  try {
    appendFileSync(
      summaryPath,
      "## Provider harness\n\n```\n" + plain.join("\n") + "\n```\n\n"
    );
  } catch {
    // Summary is best-effort - never fail the run over it.
  }
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
    // lastRenderLines only advances in interactive mode (CI never calls draw()),
    // so in CI use "we have seen at least one log byte" as the equivalent guard.
    const started = CI ? sawBytes : lastRenderLines > 0;
    if (Date.now() - lastByteAt > IDLE_EXIT_MS * 2 && started) {
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
  if (CI) {
    ciFinalReport();
    process.exit(code);
  }
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
// Skipped in CI, where there is no terminal to take over.
if (!CI) process.stdout.write("\x1b[?1049h\x1b[H\x1b[2J\x1b[?25l");

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

if (CI) {
  // Exit checks still run at the interactive cadence; only the (much noisier)
  // snapshot printing is throttled to CI_INTERVAL_MS.
  setInterval(() => {
    if (shouldExit()) teardown(0);
  }, RENDER_INTERVAL_MS);
  setInterval(drawCi, CI_INTERVAL_MS);
  // First table immediately, same as the interactive path, so a job log shows
  // the provider list without waiting a full interval.
  drawCi();
} else {
  setInterval(() => {
    draw();
    if (shouldExit()) teardown(0);
  }, RENDER_INTERVAL_MS);

  // Draw a first frame immediately so the user sees something.
  draw();
}
