// The cost model behind the provider harness's sub-shard axis.
//
// The axis splits a (provider, class) cell into n newman processes. Two numbers decide how well it
// works, and both used to be constants:
//
//   n           - HARNESS_SUBSHARDS held a hand-written count per CLASS (reasoning=4, chat=3, ...)
//   which rows  - filter-collection.mjs sliced with `i % n`, i.e. equal row COUNT per slice
//
// Neither tracks cost, and the sweep's rows differ in cost by ~50x (a deepseek reasoning row costs
// ~170s against an embeddings row's ~0.3s). A measured full sweep: 4523 requests, 222.7 minutes of
// total request time, 10.7 minutes of wall clock - against 2.3 minutes had the same shards carried
// equal cost. 77% of that wall clock went to the last 16% of the requests, with the run dropping
// from 98 concurrent shards to 1 while `openai--reasoning-s4` finished alone.
//
// Both numbers are derived here instead, from a timing table measured on previous runs
// (build-harness-timings.mjs writes it; the Makefile passes it to every shard's filter call):
//
//   subshardCount  - n = ceil(cellCost / target), so a cell is split until its slices fit the
//                    target rather than until a hand-written constant says stop
//   sliceByCost    - greedy longest-processing-time: largest row to the lightest slice, which is
//                    within 4/3 of the optimal makespan and needs one sort plus a linear scan
//
// Simulated against that same measured sweep: LPT alone (keeping the static counts) takes the worst
// shard from 9.4 to 7.4 minutes, because four slices cannot balance 30 minutes of work below 7.5.
// Both together take it to 2.8 minutes using 136 shards, which is FEWER than the 148 the static
// table produces - the counts move from where they were guessed to where the cost actually is.
// 2.8 minutes is the floor: it is the sweep's single slowest request, which no split can divide.
//
// EVERY function here must be a pure, total function of its arguments. The n sub-shards each run
// filter-collection.mjs in a separate process and recompute the whole assignment from scratch,
// keeping only their own slice. They never compare notes, so a non-deterministic tie-break would
// produce slices that silently overlap and drop rows with nothing failing loudly.

import { readFileSync } from "node:fs";

import { RETRYABLE_CODES } from "./rate-limit-retry.mjs";

// Wall-clock a single sub-shard should aim for. 150s was picked by simulating the measured sweep at
// 120/150/180/240: 150 is the smallest target that still reaches the 170s single-request floor
// (2.8m worst shard) without buying shards that cannot help - 120s costs 21 more processes for the
// same 2.8m, and 180s saves 16 processes for +0.2m.
export const DEFAULT_TARGET_SECONDS = 150;

// Median rather than mean, everywhere a set of measurements is collapsed. Provider latency has a
// long right tail (a 429 retry, a cold model, one 170s reasoning row) and a mean lets a single
// outlier resize a whole cell. Upper median on even counts, which keeps the estimate conservative.
const median = (values) => {
  if (!values.length) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.floor(sorted.length / 2)];
};

// Reads a timing table written by build-harness-timings.mjs. Returns a Map, or null for "no usable
// timings" - a missing file, an unreadable one, malformed JSON, or a table with no positive entry.
//
// Null rather than a throw for every one of those. The table is an optimisation over a sweep that
// worked without it, so a corrupt cache file has to degrade to round-robin. Failing the run would
// turn a performance feature into an outage, and it would do so at the least convenient moment:
// the file is written by the previous run, so a crash mid-write breaks the NEXT sweep.
export const loadTimings = (path) => {
  if (!path) return null;
  let raw;
  try {
    raw = JSON.parse(readFileSync(path, "utf8"));
  } catch {
    return null;
  }
  if (!raw || typeof raw !== "object") return null;
  const out = new Map();
  for (const [name, ms] of Object.entries(raw)) {
    // Positive finite numbers only. A 0 would make a row free to pile up and a string would make
    // every comparison against it false, so both are better treated as "not measured" - which
    // charges them the median below instead of quietly weighting them wrong.
    if (typeof ms === "number" && Number.isFinite(ms) && ms > 0) out.set(name, ms);
  }
  return out.size ? out : null;
};

// What an unmeasured row is charged.
//
// LOCAL to the set being costed, not the whole table, and that distinction is the difference
// between a useful estimate and a harmful one. The table's global median is dominated by the cheap
// classes (chat and embeddings rows outnumber reasoning rows several times over), so charging a
// half-measured reasoning cell the global median tells the planner that the sweep's most expensive
// cell is one of its cheapest - and it gets that wrong in the direction that matters, under-
// sharding exactly the cells that set the wall clock. Observed doing precisely that: an openai
// reasoning cell of ~30 real minutes priced at 147 seconds.
//
// Falls back to the global median only when the set has no measured row at all, where there is
// nothing local to reason from.
const fallbackCost = (items, timings) => {
  const local = items.map((item) => timings.get(item?.name)).filter((ms) => ms !== undefined);
  return local.length ? median(local) : median([...timings.values()]);
};

// Fraction of a set that carries a real measurement, 0..1. Callers use it to decide whether an
// estimate is worth acting on - see MIN_COVERAGE.
export const coverage = (items, timings) => {
  if (!timings || !timings.size || !items.length) return 0;
  return items.filter((item) => timings.has(item?.name)).length / items.length;
};

// How much of a cell must be measured before its cost estimate may size that cell.
//
// Below this the estimate is mostly fallbackCost applied to itself, which is a guess wearing a
// number's clothes. The planner omits such cells entirely rather than sizing them, which hands
// them back to the static HARNESS_SUBSHARDS table - a worse balance, but one that has actually run
// the sweep before. The asymmetry is deliberate: over-sharding a cheap cell wastes a node process,
// while under-sharding an expensive one puts the whole tail back.
export const MIN_COVERAGE = 0.6;

// Total cost of a set of rows. 0 when there are no timings at all, which is the signal callers use
// to fall back rather than to act on a made-up number.
export const cellCost = (items, timings) => {
  if (!timings || !timings.size) return 0;
  const unknown = fallbackCost(items, timings);
  return items.reduce((sum, item) => sum + (timings.get(item?.name) ?? unknown), 0);
};

// How many sub-shards a cell of `costMs` total work should be split into.
//
// rowCount caps it, because a cell can only be split down to its slowest single row: past that
// point each extra shard is a node process that adds startup cost and takes zero work off the
// critical path. The measured sweep's slowest row is 170s against a 150s target, which is exactly
// the case that would otherwise over-split.
export const subshardCount = (costMs, targetSeconds = DEFAULT_TARGET_SECONDS, { rowCount } = {}) => {
  const target = Math.max(1, Number(targetSeconds) || DEFAULT_TARGET_SECONDS) * 1000;
  const n = Math.ceil((Number(costMs) || 0) / target);
  const capped = rowCount ? Math.min(n, rowCount) : n;
  return Math.max(1, capped);
};

// The previous behaviour, kept as the fallback path rather than deleted.
//
// Round-robin rather than contiguous blocks: the harness emits its criss-cross grids in provider
// order, so a contiguous cut drops one provider's whole expensive block into a single slice and
// leaves the tail exactly where it was, with the sweep only looking more parallel.
const roundRobin = (items, count, index) => items.filter((_, i) => i % count === index - 1);

// Greedy longest-processing-time assignment: repeatedly put the largest remaining row into the
// lightest slice. Returns the rows belonging to slice `index` (1-based), in their original
// collection order - newman runs a slice top to bottom, and reordering rows would reorder requests
// that the collection's own chained-variable flow depends on.
//
// Both tie-breaks are total and positional, which is what makes n independent processes agree:
// equal costs order by original index, and equally-loaded slices resolve to the lowest slice
// number. Nothing here reads the clock, the filesystem, or a hash.
export const sliceByCost = (items, count, index, timings) => {
  if (!Array.isArray(items) || count <= 1) return items || [];
  if (!timings || !timings.size) return roundRobin(items, count, index);

  const unknown = fallbackCost(items, timings);
  const order = items
    .map((item, i) => ({ i, cost: timings.get(item?.name) ?? unknown }))
    .sort((a, b) => b.cost - a.cost || a.i - b.i);

  const loads = new Array(count).fill(0);
  const assigned = new Array(items.length);
  for (const { i, cost } of order) {
    let best = 0;
    for (let s = 1; s < count; s++) if (loads[s] < loads[best]) best = s;
    loads[best] += cost;
    assigned[i] = best;
  }
  return items.filter((_, i) => assigned[i] === index - 1);
};

// Builds a timing table from newman JSON reports, merged onto a prior table.
//
// Merged rather than replaced because a scoped run (PROVIDER=anthropic, FEATURE=..., a 429 retry
// pass) only measures the rows it ran. Replacing would leave the next unscoped sweep falling back
// to round-robin for seven of eight providers, so every scoped run would erase most of the table
// it depends on. Freshly measured rows supersede their prior values; untouched rows keep them.
export const aggregateTimings = (reports, prior = {}) => {
  const samples = new Map();
  for (const report of reports || []) {
    for (const exec of report?.run?.executions || []) {
      const name = exec?.item?.name;
      const ms = exec?.response?.responseTime;
      // An execution with no response never reached the provider (a connection error, a cancelled
      // stream probe). It measures the harness's failure path, not the row's cost.
      if (!name || typeof ms !== "number" || !Number.isFinite(ms) || ms <= 0) continue;
      // Every retryable status is refused at the provider's edge in milliseconds, so it measures the
      // refusal rather than the work. Sampling one would tell the planner that the harness's single
      // most expensive rows are its cheapest - and those rows are refused precisely BECAUSE they are
      // expensive, so the mis-estimate would concentrate on exactly the cells that set the wall
      // clock. Shared with the retry pass rather than restated: the retry pass replays all three
      // codes onto this same path, so a set that disagreed would silently under-price whichever
      // codes it omitted. See RETRYABLE_CODES in rate-limit-retry.mjs for why the set is 429/503/529
      // and deliberately not 500/502/504.
      if (RETRYABLE_CODES.has(exec?.response?.code)) continue;
      const list = samples.get(name) || [];
      list.push(ms);
      samples.set(name, list);
    }
  }
  const out = { ...prior };
  for (const [name, list] of samples) out[name] = median(list);
  return out;
};
