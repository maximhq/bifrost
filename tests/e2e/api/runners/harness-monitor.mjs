#!/usr/bin/env node
// Live terminal progress monitor for `make run-provider-harness-test`.
//
// Tails per-provider newman CLI logs (parallel passes) or a single shared CLI
// log (sequential passes) and renders ONE thing: a provider x total/pass/failed
// table. That is deliberately the entire output surface of a harness run -
// per-request chatter, folder breakdowns and failure text all live in the
// artifacts (tmp/newman-cli*.log, tmp/harness-failures.md), not on the
// terminal, so a running harness is readable at a glance.
//
// ONE table per `make` invocation, not one per newman invocation. A full sweep
// runs newman more than once - the main pass, then the deferred sequential
// cache-parity pass - and each used to get its own monitor process with its own
// zeroed counters, so the terminal ended up showing two unrelated tables and the
// second read as if the first had been discarded. Instead the Makefile appends
// one JSON line per newman invocation to a pass manifest and a single monitor
// process follows it for the lifetime of the target, accumulating counters
// across passes. See lib/monitor-passes.mjs for the line shape.
//
// Usage (manifest mode - what the Makefile does):
//   node harness-monitor.mjs \
//     --providers "openai anthropic bedrock gemini vertex azure passthrough" \
//     --tmp-dir tmp \
//     --passes tmp/harness-monitor-passes.jsonl
//
// Usage (legacy single-pass mode - the flags below synthesize a one-entry
// manifest, which is also the only mode that self-exits on idle):
//   node harness-monitor.mjs --mode parallel --providers "..." \
//     --tmp-dir tmp --status-file tmp/parallel-status --launched 7
//
//   node harness-monitor.mjs --mode sequential --providers "openai anthropic" \
//     --tmp-dir tmp --log tmp/newman-cli.log \
//     --collection tmp/harness-cache-filtered.json
//
// --collection pins the denominator source (defaults per mode below). It
// matters for the deferred passes - cache-parity runs its own filtered
// collection, so without it the Total column would be taken from the main
// pass's collection and be wrong.
//
// Add --ci for GitHub Actions: no alternate screen buffer and no in-place
// redraw (impossible in an append-only job log). Emits a one-line heartbeat
// every --ci-interval seconds and the full table exactly once, at teardown,
// into stdout + $GITHUB_STEP_SUMMARY. --ci-reprint-table restores the old
// behaviour of reprinting the whole table on every interval.

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
import {
  parseManifest,
  countLeaves,
  countByProvider,
  sumPassTotals,
} from "./lib/monitor-passes.mjs";
import {
  RE_PREFIX,
  RE_FOLDER,
  RE_REQUEST,
  RE_METHOD_URL,
  RE_REQUEST_DONE,
  RE_REQUEST_ERRORED,
  RE_ASSERT_FAIL,
} from "./lib/newman-log-lines.mjs";

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
// Manifest mode: follow this file for the whole run instead of being told about
// a single newman invocation up front. Absent means legacy single-pass mode.
const PASSES_FILE = args.passes && args.passes !== "true" ? args.passes : null;
const TAIL_INTERVAL_MS = 250;
const RENDER_INTERVAL_MS = 1000;
const IDLE_EXIT_MS = 3000;

// CI mode: GitHub Actions logs are an append-only stream, so the alternate
// screen buffer + cursor-home redraw used interactively is impossible there.
// Instead we reprint the same table on an interval - identical content, the
// only difference being append vs redraw-in-place.
const CI = args.ci === "true" || args.ci === "1";
const CI_INTERVAL_MS = resolveCiIntervalMs(args["ci-interval"]);
// Interval output in CI is a one-line heartbeat by default: reprinting the whole
// table every few seconds leaves hundreds of stale copies in an append-only
// Actions log, and the copy that matters is the last one. The job carries a
// timeout-minutes budget rather than an idle-output timeout, so a quiet run is
// never killed for being quiet. This restores the old reprint for debugging.
const CI_REPRINT_TABLE =
  args["ci-reprint-table"] === "true" || args["ci-reprint-table"] === "1";

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
  providers: Object.fromEntries(
    PROVIDERS.map((p) => [
      p,
      {
        status: "pending",
        // Denominator contributions keyed by pass index, summed into
        // totalRequests. Keyed rather than accumulated because
        // loadDenominators re-reads on a timer until the filtered collections
        // land on disk, so the same pass is counted many times over a run.
        totalsByPass: {},
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

// Every newman invocation this run has been told about, in manifest order.
const passes = [];
// How many manifest records have been applied. Records are a mix of pass /
// pass-end / note, so this is not passes.length.
let manifestConsumed = 0;

let lastByteAt = Date.now();
let lastRenderLines = 0;
let sawBytes = false;

// ----- Denominator: walk each pass's filtered collection per provider. --------

// Restricted to the providers this run tracks, so a keyword cannot assign a row
// to a provider that has no column in the table.
const matchProvider = makeMatchProvider((p) => !!state.providers[p]);

const itemProvider = (item, ancestors) => providerOfItem(item, ancestors, matchProvider);

// Record one pass's contribution to a provider's denominator and refresh the
// sum. Passes never overlap: whenever the cache-parity pass is deferred, the
// main pass is filtered with --exclude-feature-any cache-parity, so those rows
// are removed from it and adding the two denominators cannot double-count.
function setPassTotal(provider, passIndex, count) {
  const ps = state.providers[provider];
  if (!ps) return;
  ps.totalsByPass[passIndex] = count;
  ps.totalRequests = sumPassTotals(ps.totalsByPass);
}

function loadDenominatorsForPass(pass) {
  // A parallel pass already has the split done for it on disk: one filtered
  // collection per provider fork, so counting leaves is enough.
  if (pass.mode === "parallel" && !pass.collection) {
    let sawAny = false;
    for (const p of PROVIDERS) {
      const path = join(TMP_DIR, `harness-filtered-${p}.json`);
      if (!existsSync(path)) continue;
      try {
        const data = JSON.parse(readFileSync(path, "utf8"));
        setPassTotal(p, pass.index, countLeaves(data));
        sawAny = true;
      } catch {
        // Partially-written file mid-fork; the retry timer picks it up.
      }
    }
    return sawAny;
  }

  // A sequential pass runs one collection covering every provider, so the split
  // has to be recomputed here by keyword.
  const candidates = pass.collection
    ? [pass.collection]
    : [
        join(TMP_DIR, "harness-filtered.json"),
        "tests/e2e/api/collections/provider-harness.json",
      ];
  for (const path of candidates) {
    if (!existsSync(path)) continue;
    try {
      const data = JSON.parse(readFileSync(path, "utf8"));
      const counts = countByProvider(data, PROVIDERS, itemProvider);
      for (const p of PROVIDERS) setPassTotal(p, pass.index, counts[p]);
      return true;
    } catch {
      // ignore - try next candidate
    }
  }
  return false;
}

// Retry every pass whose collections had not landed yet. Denominators are
// written by a filter step that races the monitor's own startup, so a pass can
// be announced before its collection exists.
function loadDenominators() {
  for (const pass of passes) {
    if (pass.denomsLoaded) continue;
    if (loadDenominatorsForPass(pass)) pass.denomsLoaded = true;
  }
}

// ----- Pass manifest ----------------------------------------------------------

function registerPass(entry) {
  const pass = {
    index: passes.length,
    id: entry.id || `pass-${passes.length}`,
    mode: entry.mode === "sequential" ? "sequential" : "parallel",
    log: entry.log && entry.log !== "true" ? entry.log : null,
    collection: entry.collection && entry.collection !== "true" ? entry.collection : null,
    statusFile: entry.statusFile || null,
    launched: Number.isFinite(entry.launched) ? entry.launched : null,
    denomsLoaded: false,
    ended: false,
    tailPaths: [],
  };
  passes.push(pass);
  setupTailsForPass(pass);
  loadDenominators();
  if (CI) ciLog(`pass "${pass.id}" started (${pass.mode})`);
  return pass;
}

// Ending a pass drains and then CLOSES its tails. That is not tidiness, it is
// what stops a double count: Makefile appends the cache-parity log onto
// tmp/newman-cli.log once that pass finishes, and under PARALLEL=0 the main pass
// is tailing exactly that file. Leaving its tail open would replay every
// cache-parity request a second time into the same counters.
function endPass(id) {
  const pass = passes.find((p) => p.id === id && !p.ended);
  if (!pass) return;
  readNewBytes();
  pass.ended = true;
  for (const path of pass.tailPaths) {
    const h = tails.get(path);
    // Only close a tail no still-open pass is reading.
    const stillUsed = passes.some((p) => !p.ended && p.tailPaths.includes(path));
    if (h && !stillUsed) {
      if (h.seqProvider) finalizeRequest(state.providers[h.seqProvider]);
      tails.delete(path);
    }
  }
  if (CI) ciLog(`pass "${pass.id}" finished`);
}

function pollManifest() {
  if (!PASSES_FILE || !existsSync(PASSES_FILE)) return;
  let text;
  try {
    text = readFileSync(PASSES_FILE, "utf8");
  } catch {
    return;
  }
  const entries = parseManifest(text);
  for (let i = manifestConsumed; i < entries.length; i++) {
    const entry = entries[i];
    const kind = entry.t || "pass";
    if (kind === "pass") registerPass(entry);
    else if (kind === "pass-end") endPass(entry.id);
    else if (kind === "note" && CI && entry.text) ciLog(entry.text);
  }
  manifestConsumed = entries.length;
}

// ----- Tail: poll-based incremental read of newman CLI logs. ------------------

const tails = new Map(); // path -> handle

function ensureTail(path, { provider, mode }) {
  if (tails.has(path)) return;
  tails.set(path, {
    provider,
    mode,
    offset: 0,
    buf: "",
    // Sequential attribution state, held per tail rather than per process.
    // One monitor can now be tailing two sequential logs at once (a PARALLEL=0
    // main pass and the deferred cache-parity pass), and sharing these would let
    // a folder heading in one log attribute the next request in the other.
    seqProvider: null,
    seqPendingName: null,
    seqFolder: null,
  });
}

// A parallel pass writes one prefixed log per provider fork; a sequential pass
// writes a single shared log with the provider inferred per line.
function setupTailsForPass(pass) {
  if (pass.mode === "parallel") {
    for (const p of PROVIDERS) {
      const path = join(TMP_DIR, `newman-cli-${p}.log`);
      ensureTail(path, { provider: p, mode: "parallel" });
      pass.tailPaths.push(path);
    }
    return;
  }
  const path = pass.log || join(TMP_DIR, "newman-cli.log");
  ensureTail(path, { provider: null, mode: "sequential" });
  pass.tailPaths.push(path);
}

function readNewBytes() {
  for (const [path, h] of tails) {
    let st;
    try {
      st = statSync(path);
    } catch {
      continue;
    }
    // A log truncated and regrown would otherwise stall this tail forever: the
    // offset sits past the new end and every later poll reads size <= offset and
    // skips. Unreachable while each monitor process outlived a single newman
    // invocation (every `: > <log>` ran before its monitor started), but a
    // monitor that spans the whole run is alive across those truncations.
    if (st.size < h.offset) {
      h.offset = 0;
      h.buf = "";
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
    for (const raw of lines) handleLine(stripAnsi(raw), h);
    lastByteAt = Date.now();
    sawBytes = true;
  }
}

// ----- Parsing ----------------------------------------------------------------

// The line shapes live in lib/newman-log-lines.mjs so they can be pinned by
// tests - they are the entire basis of this table's numbers.

function inferProviderFromLine(line, h) {
  return providerOfLogLine(line, h.seqFolder, matchProvider);
}

// Sequential attribution, all held on the tail handle (see ensureTail):
//
//   h.seqProvider   Only ❏/↳ lines name a provider. Everything after one (the
//                   [200 OK, …] summary, ✓/✗ assertion lines) belongs to
//                   whatever that line named, so attribution is sticky and is
//                   only ever replaced by a successful inference - never
//                   cleared by an unattributable folder heading.
//   h.seqPendingName Request name seen on a ↳ line, held until the URL line
//                   resolves its owner.
//   h.seqFolder     The last "❏ <name>" heading. This is the attribution signal
//                   that reads the same here as it does in the collection:
//                   newman echoes folder names verbatim, while it substitutes
//                   {{variables}} in URLs. Matching a URL on this side and the
//                   raw URL on the denominator side is what let a Vertex row be
//                   counted under vertex and passed under gemini - its raw URL
//                   matches "vertex" only through the literal {{vertexModel}},
//                   which is gone by the time newman prints it.

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

function handleLine(line, h) {
  let provider = h.provider;
  let body = line;

  if (h.mode === "parallel") {
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
      h.seqFolder = m[1].trim();
      if (h.seqProvider) finalizeRequest(state.providers[h.seqProvider]);
      // A heading that names a provider re-points attribution immediately,
      // so the first row under it is attributed even before its URL line.
      const fromFolder = providerOfLogLine("", h.seqFolder, matchProvider);
      if (fromFolder) h.seqProvider = fromFolder;
      return;
    }
    // A "↳ name" line names the request but not the backend - plenty of rows are
    // called things like "Prompt caching (cache_control: ephemeral)". The URL on
    // the following METHOD line is the same string the denominator pass matched
    // on, so deferring attribution until that line is what keeps pass+fail from
    // exceeding a provider's own total.
    if ((m = t.match(RE_REQUEST))) {
      if (h.seqProvider) finalizeRequest(state.providers[h.seqProvider]);
      h.seqPendingName = m[1].trim();
      return;
    }
    if (RE_METHOD_URL.test(t)) {
      const inferred =
        inferProviderFromLine(t, h) ||
        (h.seqPendingName ? inferProviderFromLine(h.seqPendingName, h) : null);
      if (inferred && inferred !== h.seqProvider) {
        // Commit the outgoing provider's deferred request before handing over.
        if (h.seqProvider) finalizeRequest(state.providers[h.seqProvider]);
        h.seqProvider = inferred;
      }
      const target = state.providers[h.seqProvider];
      if (target && h.seqPendingName) {
        target.currentRequest = h.seqPendingName;
        target.currentRequestDone = false;
        target.currentRequestHadFail = false;
        h.seqPendingName = null;
      }
    }
    provider = h.seqProvider;
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
  if (h.mode === "parallel" && (m = trimmed.match(RE_REQUEST))) {
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

function readStatusFiles() {
  let seen = 0;
  for (const pass of passes) {
    if (!pass.statusFile || !existsSync(pass.statusFile)) continue;
    let content;
    try {
      content = readFileSync(pass.statusFile, "utf8");
    } catch {
      continue;
    }
    const lines = content.trim().split("\n").filter(Boolean);
    seen += lines.length;
    for (const ln of lines) {
      const [p, v] = ln.split(":");
      const ps = state.providers[p];
      if (!ps) continue;
      const prev = ps.status;
      if (v === "pass") ps.status = "pass";
      else if (v === "fail") {
        ps.status = "fail";
        // Remembered separately because a later pass will flip status back to
        // "running" the moment that provider emits its first line, and the
        // final table must still show the fork that failed as failed.
        ps.failedAnyPass = true;
      }
      // A status-file line is only written after that provider's newman process
      // exited, so its log is complete - commit the trailing deferred request
      // now, otherwise a finished provider sits one request short of its total
      // in every subsequent frame.
      if (ps.status !== prev && (ps.status === "pass" || ps.status === "fail")) {
        finalizeRequest(ps);
      }
    }
  }
  return { lines: seen };
}

// Resolve every provider still mid-flight into a verdict for the final table.
//
// Only parallel passes write a status file, so after a sequential pass (the
// PARALLEL=0 main run, or the deferred cache-parity pass) a provider would
// otherwise be left showing the "running" glyph forever. Its own counters carry
// the answer.
function finalizeStatuses() {
  for (const p of PROVIDERS) {
    const ps = state.providers[p];
    if (ps.failedAnyPass || ps.fail > 0) {
      ps.status = "fail";
      continue;
    }
    if (ps.status === "running" || ps.status === "pending") {
      if (ps.pass > 0) ps.status = "pass";
      else if (ps.totalRequests === 0) ps.status = "skipped";
    }
  }
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
function aggregate() {
  let done = 0, total = 0, pass = 0, fail = 0;
  for (const p of PROVIDERS) {
    const ps = state.providers[p];
    done += ps.doneRequests;
    total += ps.totalRequests;
    pass += ps.pass;
    fail += ps.fail;
  }
  const elapsed = Date.now() - state.startedAt;
  // ETA off completion rate, not pass/fail, so the one deferred request per
  // provider doesn't wobble it.
  const eta = done > 0 && total > done ? elapsed * (total / done - 1) : NaN;
  return { done, total, pass, fail, elapsed, eta };
}

function renderFrame() {
  const { done: aggDone, total: aggTotal, pass: aggPass, fail: aggFail, elapsed, eta } =
    aggregate();

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
// cleanly between runs. Only reachable via --ci-reprint-table now; see drawCi's
// replacement below for why.
function drawCiTable() {
  process.stdout.write(renderFrame().map(stripAnsi).join("\n") + "\n\n");
}

function ciLog(message) {
  process.stdout.write(`[harness] ${message}\n`);
}

// The default CI cadence output: one greppable line, not a fresh copy of the
// whole table.
//
// An Actions log is append-only, so the old behaviour left one 14-line table per
// interval - roughly 6,700 lines over a 40-minute run, of which only the last
// table was worth reading. Emitting a single line instead is a ~93% cut and
// leaves exactly one table in the log (printed once at teardown).
//
// This is for observability, not liveness: the test-core job is bounded by
// timeout-minutes, and GitHub Actions imposes no idle-output timeout, so a
// silent run is never killed for being silent. But a log that has not moved in
// 40 minutes is indistinguishable from a hung one, and "is bedrock stuck or just
// slow" is the question people open the log to answer.
function drawCi() {
  if (CI_REPRINT_TABLE) {
    drawCiTable();
    return;
  }
  const { done, total, pass, fail, elapsed, eta } = aggregate();
  const running = PROVIDERS.filter((p) => state.providers[p].status === "running");
  const active = passes.filter((p) => !p.ended).map((p) => p.id);
  ciLog(
    `${fmtDuration(elapsed)} elapsed  eta ${fmtDuration(eta)}  ` +
      `${done}/${total} done  pass ${pass}  fail ${fail}  ` +
      `active=${active.join(",") || "-"}  running: ${running.join(",") || "-"}`
  );
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

// The parent that launched us. In manifest mode the recipe shell's SIGTERM is
// the terminator, so this is the only self-exit: if that shell dies without
// running its EXIT trap (SIGKILL), we get reparented and would otherwise tail an
// abandoned run for the rest of the session.
const BOOT_PPID = process.ppid;

function shouldExit() {
  // Manifest mode spans every pass, which makes "idle" indistinguishable from
  // "between passes" - the idle heuristics below would fire in the gap after the
  // main pass and take the table down before the cache-parity pass ran.
  if (PASSES_FILE) return process.ppid !== BOOT_PPID;

  const only = passes[0];
  if (!only) return false;
  if (only.mode === "parallel") {
    const { lines } = readStatusFiles();
    const launched = only.launched ?? PROVIDERS.length;
    if (lines >= launched && Date.now() - lastByteAt > IDLE_EXIT_MS) return true;
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
  finalizeStatuses();
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

// Legacy single-pass invocation: the flags describe exactly one newman run, so
// synthesize the one-entry manifest they stand for. Everything downstream then
// has a single code path.
if (!PASSES_FILE) {
  registerPass({
    id: "adhoc",
    mode: MODE,
    log: SEQ_LOG,
    collection: COLLECTION,
    statusFile: MODE === "parallel" ? STATUS_FILE : null,
    launched: LAUNCHED,
  });
}

// Denominators are written by filter steps that race the monitor's own startup,
// so keep re-reading any pass whose collections have not landed yet.
setInterval(loadDenominators, 1000);

setInterval(() => {
  pollManifest();
  readNewBytes();
  readStatusFiles();
}, TAIL_INTERVAL_MS);

if (CI) {
  // Exit checks still run at the interactive cadence; only the (much noisier)
  // snapshot printing is throttled to CI_INTERVAL_MS.
  setInterval(() => {
    if (shouldExit()) teardown(0);
  }, RENDER_INTERVAL_MS);
  setInterval(drawCi, CI_INTERVAL_MS);
  // First heartbeat immediately, same as the interactive path draws a first
  // frame, so a job log shows the run is alive without waiting a full interval.
  drawCi();
} else {
  setInterval(() => {
    draw();
    if (shouldExit()) teardown(0);
  }, RENDER_INTERVAL_MS);

  // Draw a first frame immediately so the user sees something.
  draw();
}
