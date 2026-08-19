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
  readdirSync,
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

// Report each shard's own totals the moment it finishes, instead of only folding them into the
// provider row. With the sweep split into ~80 (provider x class) shards, "openai is at 812/34"
// does not say which slice is in trouble, and the answer is otherwise only recoverable by reading
// 80 separate tmp/newman-cli-*.log files after the run.
//
// Deliberately per SHARD, not per request: a line per request is ~3300 lines of interleaved output
// for a full sweep, which buries the failures it exists to surface. ~80 lines does not.
//
// These join the live table rather than replacing it. Interactively they are rendered INSIDE the
// frame (see renderFrame) because the table is drawn by homing the cursor over the alt-screen
// buffer, so anything printed alongside it would be wiped on the next redraw. In CI, where the log
// is append-only, they are emitted as they happen.
const SHARD_LINES = args["shard-lines"] !== "0" && args["shard-lines"] !== "false";
const ALT_SCREEN = !CI;

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
  // A parallel pass already has the split done for it on disk, so counting leaves is enough. A
  // provider's fork is now SEVERAL files though - harness-filtered-<provider>--<class>.json, one
  // per modality shard - so its denominator is their sum. Reading only the unsharded
  // harness-filtered-<provider>.json leaves every total at 0, which shows up as "187/0 done" and
  // an ETA that never resolves, because the ETA needs a denominator to project against.
  if (pass.mode === "parallel" && !pass.collection) {
    let sawAny = false;
    let filesRead = 0;
    let names = [];
    try {
      names = readdirSync(TMP_DIR);
    } catch {
      return false; // tmp/ not populated yet; the retry timer picks it up.
    }
    for (const p of PROVIDERS) {
      const prefix = `harness-filtered-${p}`;
      const shardFiles = names.filter(
        (n) =>
          n.endsWith(".json") &&
          // Retry collections are a SUBSET replay of rows already counted in their shard's own
          // file. Counting them would inflate the denominator and make the run look like it had
          // more to do than it did, which the ETA would then chase.
          !/-retry\d+\.json$/.test(n) &&
          // Exact match covers CLASS_SHARDS=0; the "--" form covers the class shards. The
          // separator is required so "bedrock" cannot claim "bedrock_mantle"'s files.
          (n === `${prefix}.json` || n.startsWith(`${prefix}--`))
      );
      if (!shardFiles.length) continue;
      let total = 0;
      let ok = false;
      for (const f of shardFiles) {
        try {
          total += countLeaves(JSON.parse(readFileSync(join(TMP_DIR, f), "utf8")));
          filesRead += 1;
          ok = true;
        } catch {
          // Partially-written file mid-fork; the retry timer picks it up.
        }
      }
      if (ok) {
        setPassTotal(p, pass.index, total);
        sawAny = true;
      }
    }
    // Not final until every launched shard's collection has been read. Shards are filtered inside
    // the launch loop, so the files appear progressively; latching after the first readable one
    // would freeze the denominator at whatever fraction happened to exist on that tick.
    if (pass.launched && filesRead < pass.launched) return false;
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
      if (h.seqProvider) finalizeRequest(h, state.providers[h.seqProvider]);
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

// The shard this log belongs to ("openai--tools", or plain "openai" under CLASS_SHARDS=0), used
// only as the streaming feed's line prefix. Derived from the filename because the shard label is
// the Makefile's own naming and never reaches the monitor any other way.
const shardOfLogPath = (path) => {
  const base = path.split("/").pop() || path;
  const m = base.match(/^newman-cli-(.+)\.log$/);
  return m ? m[1] : base;
};

function ensureTail(path, { provider, mode }) {
  if (tails.has(path)) return;
  tails.set(path, {
    provider,
    mode,
    shard: shardOfLogPath(path),
    // A 429 retry pass replays rows the main pass ALREADY counted. Its log is still tailed - that
    // keeps lastByteAt fresh so the run does not look idle mid-retry - but its requests must not
    // increment any counter, or a recovered row lands as an extra pass while its original fail
    // stays put and pass+fail drifts above the shard's real total. lib/newman-merge.jq performs
    // the equivalent supersession for the final report by keeping the last attempt per item id.
    retry: /-retry\d+$/.test(shardOfLogPath(path)),
    offset: 0,
    buf: "",
    // Sequential attribution state, held per tail rather than per process.
    // One monitor can now be tailing two sequential logs at once (a PARALLEL=0
    // main pass and the deferred cache-parity pass), and sharing these would let
    // a folder heading in one log attribute the next request in the other.
    seqProvider: null,
    seqPendingName: null,
    seqFolder: null,
    // Per-shard tallies, so a shard can report its own totals on completion.
    pass: 0,
    fail: 0,
    // Deferred per-request commit state, per stream. See finalizeRequest.
    currentRequest: null,
    currentRequestDone: false,
    currentRequestHadFail: false,
  });
}

// Every log a provider's forks write this run: the unsharded newman-cli-<p>.log plus one
// newman-cli-<p>--<class>.log per class shard. Matched with an explicit "--" separator so
// provider names that prefix one another cannot cross-claim (there are none today, but the
// roster is a Makefile variable and "gemini" / "gemini_v2" is one edit away). Returns the
// unsharded path even when nothing exists yet, so a tail is registered before the fork's
// first byte lands and the "log has not appeared" case stays identical to before.
function shardLogsFor(provider) {
  const plain = `newman-cli-${provider}.log`;
  const found = new Set([plain]);
  try {
    for (const name of readdirSync(TMP_DIR)) {
      // A single "-" after the provider, not "--": that covers both the class shards
      // (newman-cli-openai--tools.log) and the 429 retry passes
      // (newman-cli-openai--tools-retry1.log, and newman-cli-openai-retry1.log under
      // CLASS_SHARDS=0, which the "--" form would have missed). The separator is still
      // required so "bedrock" cannot claim a differently-named provider's log.
      if (name.startsWith(`newman-cli-${provider}-`) && name.endsWith(".log")) found.add(name);
    }
  } catch {
    // tmp/ not created yet - the unsharded fallback below is enough to start with.
  }
  return [...found].map((name) => join(TMP_DIR, name));
}

// Shards are launched over time, not all at once: HARNESS_JOBS caps concurrency, so a shard's
// log file can appear minutes after its pass started. Re-scanning each tick is what keeps those
// late shards counted; ensureTail is idempotent, so re-registering an existing tail is a no-op
// and offsets are never rewound.
function refreshParallelTails() {
  for (const pass of passes) {
    if (pass.ended || pass.mode !== "parallel") continue;
    for (const p of PROVIDERS) {
      for (const path of shardLogsFor(p)) {
        if (tails.has(path)) continue;
        ensureTail(path, { provider: p, mode: "parallel" });
        pass.tailPaths.push(path);
      }
    }
  }
}

// A parallel pass writes one prefixed log per provider fork; a sequential pass
// writes a single shared log with the provider inferred per line.
function setupTailsForPass(pass) {
  if (pass.mode === "parallel") {
    for (const p of PROVIDERS) {
      // A provider fork is sharded again by modality class, so its output is spread over
      // newman-cli-<provider>--<class>.log rather than a single newman-cli-<provider>.log.
      // Every shard is tailed under the SAME provider label, which is what keeps the status
      // table provider-keyed: the class axis is a scheduling detail, not something the run
      // summary should grow a dimension for. The unsharded name is still matched, so
      // CLASS_SHARDS=0 and any older tmp/ layout keep working unchanged.
      for (const path of shardLogsFor(p)) {
        ensureTail(path, { provider: p, mode: "parallel" });
        pass.tailPaths.push(path);
      }
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
//
// The in-flight cursor lives on the TAIL (h), not on the provider: a provider is now covered by
// several concurrent newman shards, all feeding one provider row, so a "↳" from the tools shard
// would otherwise commit the vision shard's half-read request and the pass/fail counts would
// drift. Counters stay on the provider (ps) because the table is per provider; only the
// "which request am I mid-way through" state is per stream, which is where it was always
// conceptually anchored - the sequential path already kept its own per-tail attribution state
// for exactly this reason.
function finalizeRequest(h, ps) {
  if (!h || !ps || !h.currentRequest) return;
  if (h.currentRequestDone) {
    // Provider totals skip retry tails: a retry replays rows the main pass already scored, so
    // counting them again would report more requests than the collection holds.
    if (!h.retry) {
      if (h.currentRequestHadFail) ps.fail += 1;
      else ps.pass += 1;
    }
    // Counted per stream as well as per provider, so a shard can report its own totals when it
    // finishes. Incremented here rather than at the "done" line because the verdict is not known
    // until now: newman prints the [status, size, duration] summary BEFORE its numbered assertion
    // failures, so a request that looks done can still turn into a fail two lines later.
    //
    // Deliberately OUTSIDE the !h.retry guard above, unlike the provider counters. A retry tail's
    // outcome is exactly what recordShardCompletion needs to move a recovered row out of the
    // shard's fail column; while these were gated too, every retry tail held 0 pass / 0 fail and
    // that migration could never fire.
    if (h.currentRequestHadFail) h.fail += 1;
    else h.pass += 1;
  }
  h.currentRequest = null;
  h.currentRequestDone = false;
  h.currentRequestHadFail = false;
}

// Shards that have reported a verdict, oldest first: {shard, total, pass, fail}.
const completedShards = [];
const completedShardIds = new Set();

// A shard's denominator, read from the filtered collection the Makefile wrote for it. Counting
// the '"request":' keys is the same cheap check the Makefile's own zero-item guard uses, and it
// avoids parsing a collection that can run to tens of megabytes. Falls back to pass+fail, which
// is right whenever the file is unreadable and never claims MORE coverage than was observed.
function shardTotal(shard, observed) {
  try {
    const raw = readFileSync(join(TMP_DIR, `harness-filtered-${shard}.json`), "utf8");
    return (raw.match(/"request":/g) || []).length || observed;
  } catch {
    return observed;
  }
}

const SHARD_LABEL_WIDTH = 24;
const fmtShardLine = ({ shard, total, pass, fail }) =>
  `${`[${shard}]`.padEnd(SHARD_LABEL_WIDTH)} ${String(total).padStart(4)} total  ` +
  `${String(pass).padStart(4)} pass  ${String(fail).padStart(4)} fail`;

// Called once per shard, when its status line lands. Sums the tails belonging to that shard: a
// shard's 429 retry pass writes a SECOND log (newman-cli-<shard>-retry1.log), and its requests
// belong to the same shard's totals.
//
// A retry tail is NOT simply added in, because its rows are replays of rows the main tail already
// counted - adding both would report each retried request twice. What a retry row carries is a
// CORRECTION: the Makefile replays only rows that came back 429 (--rerun-rate-limited), so every
// row that passes on retry is one the main tail booked as a failure. Moving it across the columns
// is what makes this line agree with the Makefile's shard verdict and with newman-merge.jq, which
// supersedes the 429 with its successful retry. Before this, a shard that recovered still printed
// "0 pass 1 fail" while the other two views called it passed.
function recordShardCompletion(shard) {
  if (!SHARD_LINES || completedShardIds.has(shard)) return;
  completedShardIds.add(shard);
  let pass = 0;
  let fail = 0;
  let recovered = 0;
  for (const [path, h] of tails) {
    const owner = shardOfLogPath(path);
    if (owner === shard) {
      pass += h.pass;
      fail += h.fail;
    } else if (owner.startsWith(`${shard}-retry`)) {
      // Summed across every attempt, which does not double-count: attempt N+1 replays only the
      // rows still failing after attempt N, so a row can pass at most once across the whole chain.
      recovered += h.pass;
    }
  }
  // Clamped to the failures actually on the books. The subset property above should make this a
  // no-op, but a shard whose main log was rotated or partially read must never print a negative
  // fail count.
  recovered = Math.min(recovered, fail);
  pass += recovered;
  fail -= recovered;
  const record = { shard, total: shardTotal(shard, pass + fail), pass, fail };
  completedShards.push(record);
  // In CI the log is append-only, so emit now. Interactively the frame renders the list instead,
  // because a redraw would immediately overwrite anything printed here.
  if (CI) process.stdout.write(`[harness] ${fmtShardLine(record)}\n`);
}

// Drains every open stream rather than every provider: with N shards per provider there can be N
// in-flight requests for one provider row, and a provider-keyed loop would commit only one.
function finalizeAll() {
  for (const h of tails.values()) {
    const p = h.mode === "parallel" ? h.provider : h.seqProvider;
    if (p) finalizeRequest(h, state.providers[p]);
  }
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
      if (h.seqProvider) finalizeRequest(h, state.providers[h.seqProvider]);
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
      if (h.seqProvider) finalizeRequest(h, state.providers[h.seqProvider]);
      h.seqPendingName = m[1].trim();
      return;
    }
    if (RE_METHOD_URL.test(t)) {
      const inferred =
        inferProviderFromLine(t, h) ||
        (h.seqPendingName ? inferProviderFromLine(h.seqPendingName, h) : null);
      if (inferred && inferred !== h.seqProvider) {
        // Commit the outgoing provider's deferred request before handing over.
        if (h.seqProvider) finalizeRequest(h, state.providers[h.seqProvider]);
        h.seqProvider = inferred;
      }
      if (state.providers[h.seqProvider] && h.seqPendingName) {
        h.currentRequest = h.seqPendingName;
        h.currentRequestDone = false;
        h.currentRequestHadFail = false;
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
    finalizeRequest(h, ps);
    return;
  }
  if (h.mode === "parallel" && (m = trimmed.match(RE_REQUEST))) {
    finalizeRequest(h, ps);
    h.currentRequest = m[1].trim();
    h.currentRequestDone = false;
    h.currentRequestHadFail = false;
    return;
  }
  // Disambiguate request-done summary from assertion-fail; check done first.
  if (RE_REQUEST_DONE.test(trimmed)) {
    if (h.currentRequest && !h.currentRequestDone) {
      h.currentRequestDone = true;
      // Not for a retry tail: those rows were already counted done by the main pass, and the ETA
      // divides doneRequests by a denominator that has no retry rows in it.
      if (!h.retry) ps.doneRequests += 1;
    }
    return;
  }
  if (RE_REQUEST_ERRORED.test(trimmed)) {
    if (h.currentRequest && !h.currentRequestDone) {
      h.currentRequestDone = true;
      h.currentRequestHadFail = true;
      if (!h.retry) ps.doneRequests += 1;
    }
    return;
  }
  // Failure text is not rendered anywhere - it belongs to tmp/harness-failures.md
  // (analyze-failures.mjs) and the per-provider CLI logs. All the table needs is
  // that this request failed.
  if (RE_ASSERT_FAIL.test(trimmed) && h.currentRequest) {
    h.currentRequestHadFail = true;
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
      const [shard, v] = ln.split(":");
      // Lines are keyed by shard ("openai--tools"), but the table is keyed by provider. Fold the
      // class axis away here so a provider is green only when every one of its shards finished
      // clean: "fail" is sticky via failedAnyPass, and the guard below stops a later passing
      // shard from repainting a provider that already had one fail.
      const p = shard.split("--")[0];
      const ps = state.providers[p];
      if (!ps) continue;
      if (v === "pass") {
        if (!ps.failedAnyPass) ps.status = "pass";
      } else if (v === "fail") {
        ps.status = "fail";
        // Remembered separately because a later pass will flip status back to
        // "running" the moment that provider emits its first line, and the
        // final table must still show the fork that failed as failed.
        ps.failedAnyPass = true;
      }
      // A status-file line is only written after THAT SHARD's newman process exited, so its log
      // is complete - commit its trailing deferred request now, otherwise a finished shard sits
      // one request short of its total in every subsequent frame. Scoped to the shard's own tail
      // rather than the provider: the provider's other shards are very likely still running, and
      // draining their in-flight requests here would commit them before their verdict lines
      // arrive, turning failures into passes.
      // Idempotent: finalizeRequest returns immediately once the cursor is null, and this line is
      // re-read every tick because the status file is append-only.
      finalizeRequest(tails.get(join(TMP_DIR, `newman-cli-${shard}.log`)), ps);
      // Ordered after the finalize so the shard's last request is included in its own totals.
      recordShardCompletion(shard);
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

  // Completed shards, newest last. Only the tail is shown: a full sweep finishes ~80 of them and
  // the frame is clamped to the terminal height by draw(), so an unbounded list would push the
  // table itself off the top - the opposite of what this section is for.
  // Not in CI: there the lines were already emitted one at a time as each shard finished, and
  // repeating all ~80 in the final frame is exactly the append-only log bloat the one-line
  // heartbeat replaced.
  if (SHARD_LINES && !CI && completedShards.length) {
    const budget = Math.max(0, (process.stdout.rows || 40) - out.length - 3);
    const shown = completedShards.slice(-Math.max(1, budget));
    const hidden = completedShards.length - shown.length;
    out.push("");
    out.push(
      `${C.dim}Completed shards (${completedShards.length})` +
        `${hidden > 0 ? `, showing last ${shown.length}` : ""}${C.reset}`
    );
    for (const record of shown) {
      out.push(record.fail > 0 ? `${C.red}${fmtShardLine(record)}${C.reset}` : fmtShardLine(record));
    }
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
    // lastRenderLines only advances when draw() runs, which is the alt-screen path alone: CI
    // never calls it, and the streaming feed replaces the periodic frame with per-request lines.
    // Both of those use "we have seen at least one log byte" as the equivalent guard - without
    // this the streaming path would never satisfy `started` and the monitor would hang forever
    // on a sequential pass.
    const started = ALT_SCREEN ? lastRenderLines > 0 : sawBytes;
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
if (ALT_SCREEN) process.stdout.write("\x1b[?1049h\x1b[H\x1b[2J\x1b[?25l");

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
  refreshParallelTails();
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
