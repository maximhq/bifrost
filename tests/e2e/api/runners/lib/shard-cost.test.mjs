// Unit tests for the cost model behind the sub-shard axis: lib/shard-cost.mjs.
//
// The axis itself (filter-collection.mjs --shard k/n and the Makefile launch loop) is covered by
// shard-split.test.mjs. This file covers the two decisions that sit underneath it, both of which
// are pure functions of a measured timing table:
//
//   - how MANY sub-shards a cell gets   (subshardCount)
//   - which rows land in which slice     (sliceByCost)
//
// Both were previously fixed constants: HARNESS_SUBSHARDS held a hand-written count per class, and
// the slice was `i % n`. That balances row COUNT, and the sweep's rows differ in cost by ~50x, so
// the slices came out balanced by the one measure that does not set the wall clock. A full sweep
// measured 4523 requests over 222.7 minutes of request time finishing in 10.7 minutes of wall
// clock, against 2.3 minutes if the same shards had carried equal cost.
//
// Run directly: `node shard-cost.test.mjs`.
import assert from "node:assert";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";

import {
  DEFAULT_TARGET_SECONDS,
  MIN_COVERAGE,
  aggregateTimings,
  cellCost,
  coverage,
  loadTimings,
  sliceByCost,
  subshardCount,
} from "./shard-cost.mjs";
import { RETRYABLE_CODES } from "./rate-limit-retry.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

const WORK = mkdtempSync(join(tmpdir(), "shard-cost-"));
const row = (name) => ({ name });
const rows = (n, prefix = "row") => Array.from({ length: n }, (_, i) => row(`${prefix}-${i + 1}`));
const names = (items) => items.map((i) => i.name);

// ----- fallback: no timings means the previous behaviour, exactly ---------------------------------

// The fallback is not a nicety. A fresh checkout, a CI job with no cached timings and any row added
// since the last measured run all arrive here, and the harness has to keep working - just without
// the balancing. Round-robin rather than contiguous blocks, because the collection emits its
// criss-cross grids in provider order and a contiguous cut would drop one provider's expensive
// block whole into a single slice.
test("with no timings the slice is exactly the old round-robin", () => {
  const items = rows(20);
  assert.deepEqual(names(sliceByCost(items, 4, 1, null)), ["row-1", "row-5", "row-9", "row-13", "row-17"]);
  assert.deepEqual(names(sliceByCost(items, 4, 2, null)), ["row-2", "row-6", "row-10", "row-14", "row-18"]);
});

test("an empty timing table is treated as no timings rather than as all-zero costs", () => {
  const items = rows(8);
  assert.deepEqual(names(sliceByCost(items, 2, 1, new Map())), names(sliceByCost(items, 2, 1, null)));
});

test("a shard count of 1 returns everything, timings or not", () => {
  const items = rows(5);
  assert.deepEqual(names(sliceByCost(items, 1, 1, null)), names(items));
  assert.deepEqual(names(sliceByCost(items, 1, 1, new Map([["row-1", 9000]]))), names(items));
});

// ----- the partition property -------------------------------------------------------------------

// The invariant the whole axis rests on, restated for the cost-aware path: every row still runs
// somewhere, exactly once. A balancer that drops a row makes the sweep faster AND blinder, which
// is strictly worse than not balancing at all.
test("the union of every cost-aware slice is the input, with no row twice", () => {
  const items = rows(37);
  const timings = new Map(items.map((it, i) => [it.name, (i % 7) * 1000 + 250]));
  const seen = [];
  for (let k = 1; k <= 5; k++) seen.push(...names(sliceByCost(items, 5, k, timings)));
  assert.equal(seen.length, items.length, `slices lost or gained rows: ${seen.length} vs ${items.length}`);
  assert.deepEqual([...seen].sort(), names(items).sort());
});

// Every sub-shard runs filter-collection.mjs in its OWN process and computes the whole assignment
// from scratch, keeping only its own slice. They never compare notes, so agreement can only come
// from the function being deterministic - a Math.random tie-break or a Set iteration order would
// produce slices that overlap and drop rows without anything failing loudly.
test("the assignment is deterministic across independent calls", () => {
  const items = rows(30);
  // Deliberately heavy on ties: equal costs are where a non-total ordering would show up.
  const timings = new Map(items.map((it, i) => [it.name, i % 3 === 0 ? 5000 : 1000]));
  for (let k = 1; k <= 4; k++) {
    assert.deepEqual(names(sliceByCost(items, 4, k, timings)), names(sliceByCost(items, 4, k, timings)));
  }
});

// ----- the balancing itself ----------------------------------------------------------------------

// The point of the change, stated against the split it replaces. Two expensive rows sitting at the
// same parity land in the SAME round-robin slice, which is the real shape of the problem: the
// collection emits its criss-cross grids in provider order, so equal-cost rows cluster by position.
test("slices are balanced by cost where round-robin is not", () => {
  const items = rows(6);
  const timings = new Map(items.map((it, i) => [it.name, i === 0 || i === 2 ? 60000 : 2000]));
  const load = (slice) => slice.reduce((s, it) => s + timings.get(it.name), 0);

  const rrSpread = Math.abs(load(items.filter((_, i) => i % 2 === 0)) - load(items.filter((_, i) => i % 2 === 1)));
  const costSpread = Math.abs(load(sliceByCost(items, 2, 1, timings)) - load(sliceByCost(items, 2, 2, timings)));

  assert.ok(rrSpread > 100000, `fixture is not adversarial for round-robin: ${rrSpread}ms`);
  assert.ok(costSpread <= 2000, `cost-aware slices differ by ${costSpread}ms`);
});

// The trade the change accepts, stated so it is a decision rather than a surprise: balancing cost
// means abandoning equal row counts. A slice holding one row is correct when that row is the whole
// budget, and a reviewer seeing 1-vs-4 in a shard log should read it as working, not as broken.
test("a slice may hold a single row when that row is its whole budget", () => {
  const items = rows(5);
  const timings = new Map([
    ["row-1", 40000],
    ["row-2", 10000],
    ["row-3", 10000],
    ["row-4", 10000],
    ["row-5", 10000],
  ]);
  const slices = [1, 2].map((k) => sliceByCost(items, 2, k, timings));
  assert.deepEqual(names(slices.find((s) => names(s).includes("row-1"))), ["row-1"]);
  assert.equal(slices.find((s) => !names(s).includes("row-1")).length, 4);
});

// Greedy longest-processing-time: the largest job goes to the lightest bin. Feeding jobs in
// arrival order instead would put the two 10s rows together and leave a 20s/6s split.
test("the largest rows are spread across slices, not packed into one", () => {
  const items = rows(4);
  const timings = new Map([
    ["row-1", 10000],
    ["row-2", 10000],
    ["row-3", 3000],
    ["row-4", 3000],
  ]);
  const first = names(sliceByCost(items, 2, 1, timings));
  assert.equal(first.filter((n) => n === "row-1" || n === "row-2").length, 1, `both heavy rows in one slice: ${first}`);
});

// A row added to the collection since the last measured run has no entry. Charging it 0 would make
// it free to pile up, so an unmeasured row is charged the median of what IS measured - the best
// available guess, and one that keeps a fresh row from being silently treated as weightless.
test("a row with no measurement is charged the median rather than zero", () => {
  const items = [row("known-1"), row("known-2"), row("known-3"), row("fresh")];
  const timings = new Map([
    ["known-1", 1000],
    ["known-2", 4000],
    ["known-3", 10000],
  ]);
  const slices = [1, 2].map((k) => names(sliceByCost(items, 2, k, timings)));
  assert.ok(slices.flat().includes("fresh"), "an unmeasured row was dropped from every slice");
  // The 10s row alone outweighs 1s + 4s, so "fresh" only joins it if it was charged ~0.
  const withHeavy = slices.find((s) => s.includes("known-3"));
  assert.ok(!withHeavy.includes("fresh"), "the unmeasured row was charged as free and packed onto the heaviest slice");
});

// ----- sizing ------------------------------------------------------------------------------------

test("the sub-shard count is the cell cost divided by the target", () => {
  assert.equal(subshardCount(600_000, 150), 4);
  assert.equal(subshardCount(450_000, 150), 3);
  // Rounds up: a remainder is a slice's worth of work that has to go somewhere.
  assert.equal(subshardCount(455_000, 150), 4);
});

test("a cell is never split into fewer than one shard", () => {
  assert.equal(subshardCount(0, 150), 1);
  assert.equal(subshardCount(1, 150), 1);
  assert.equal(subshardCount(-5, 150), 1);
});

// A cell can only be split down to its slowest single row, so past that point more shards buy
// nothing and cost a node process each. The sweep's slowest row measured 170s against a 150s
// target, which is exactly the case that would otherwise over-split.
test("sizing is capped by the row count, since a row cannot be split", () => {
  assert.equal(subshardCount(600_000, 150, { rowCount: 2 }), 2);
  assert.equal(subshardCount(600_000, 150, { rowCount: 99 }), 4);
});

// ----- the unmeasured row is priced LOCALLY -------------------------------------------------------

// The bug this pins, caught in validation against a real partial timing table: an openai reasoning
// cell worth ~30 minutes was priced at 147 seconds, because its unmeasured rows were charged the
// table's GLOBAL median - a median dominated by the cheap classes, which outnumber reasoning rows
// several times over. The planner then sized the sweep's most expensive cell at one shard.
test("an unmeasured row is priced from its own cell, not from the whole table", () => {
  // A table where the global median is cheap (many 1s chat rows) but this cell is expensive.
  const timings = new Map([
    ...Array.from({ length: 20 }, (_, i) => [`chat-${i}`, 1000]),
    ["slow-1", 60000],
    ["slow-2", 60000],
    ["slow-3", 60000],
  ]);
  const cell = [row("slow-1"), row("slow-2"), row("slow-3"), row("slow-unmeasured")];

  // Globally the median is 1000; locally it is 60000. The unmeasured row must be charged the latter.
  assert.equal(cellCost(cell, timings), 240000);
});

test("a set with no measured row at all falls back to the table-wide median", () => {
  const timings = new Map([["elsewhere-1", 2000], ["elsewhere-2", 4000]]);
  assert.equal(cellCost([row("a"), row("b")], timings), 8000);
});

// ----- coverage ------------------------------------------------------------------------------------

test("coverage is the measured fraction of a set", () => {
  const timings = new Map([["a", 1], ["b", 2]]);
  assert.equal(coverage([row("a"), row("b")], timings), 1);
  assert.equal(coverage([row("a"), row("b"), row("c"), row("d")], timings), 0.5);
  assert.equal(coverage([row("x")], timings), 0);
  assert.equal(coverage([], timings), 0);
  assert.equal(coverage([row("a")], null), 0);
});

// The guard has to be a real threshold, not 0 (which would trust everything) or 1 (which would
// trust nothing the moment a single row was added to the collection).
test("MIN_COVERAGE leaves room for a partly-refreshed table", () => {
  assert.ok(MIN_COVERAGE > 0 && MIN_COVERAGE < 1, `MIN_COVERAGE is ${MIN_COVERAGE}`);
});

test("the default target is a positive number of seconds", () => {
  assert.ok(DEFAULT_TARGET_SECONDS > 0 && Number.isFinite(DEFAULT_TARGET_SECONDS));
  assert.equal(subshardCount(DEFAULT_TARGET_SECONDS * 1000 * 3, DEFAULT_TARGET_SECONDS), 3);
});

test("cellCost sums the measured rows and charges the rest the median", () => {
  const timings = new Map([
    ["a", 1000],
    ["b", 3000],
  ]);
  assert.equal(cellCost([row("a"), row("b")], timings), 4000);
  // The median of {1000, 3000} is 3000 (upper of the two), so the unknown row adds that.
  assert.equal(cellCost([row("a"), row("b"), row("c")], timings), 7000);
  assert.equal(cellCost([row("a")], null), 0);
});

// ----- building the table from newman reports -----------------------------------------------------

test("aggregateTimings takes the median across repeats of the same request name", () => {
  const report = {
    run: {
      executions: [
        { item: { name: "flaky" }, response: { responseTime: 1000 } },
        { item: { name: "flaky" }, response: { responseTime: 9000 } },
        { item: { name: "flaky" }, response: { responseTime: 2000 } },
        { item: { name: "steady" }, response: { responseTime: 500 } },
      ],
    },
  };
  const t = aggregateTimings([report]);
  assert.equal(t.flaky, 2000, "a single slow outlier moved the estimate");
  assert.equal(t.steady, 500);
});

// A run scoped with PROVIDER=anthropic only measures anthropic rows. Dropping everything else would
// make the next unscoped sweep fall back to round-robin for 7 of 8 providers, so a scoped run has
// to refine the table rather than replace it.
test("aggregateTimings merges onto a prior table instead of replacing it", () => {
  const prior = { "old-row": 4321, "row-a": 1000 };
  const report = { run: { executions: [{ item: { name: "row-a" }, response: { responseTime: 2000 } }] } };
  const t = aggregateTimings([report], prior);
  assert.equal(t["old-row"], 4321, "an unmeasured row from the prior table was dropped");
  assert.equal(t["row-a"], 2000, "a freshly measured row did not supersede its prior value");
});

// The retry pass replays exactly the rows that were refused, and a refused row is rejected at the
// provider's edge in milliseconds. Sampling those would report the sweep's most expensive rows as
// its cheapest, and would do so only for the cells that actually set the wall clock.
//
// All three retryable codes are pinned, not just 429: the retry pass replays 503 and 529 onto this
// same path, so a cost model that skipped only 429 would under-price the rows an overloaded
// provider refused - the same defect, reached by a different status.
test("aggregateTimings ignores refused rows rather than recording them as fast rows", () => {
  for (const code of [429, 503, 529]) {
    const report = {
      run: {
        executions: [
          { item: { name: "throttled" }, response: { responseTime: 30, code } },
          { item: { name: "throttled" }, response: { responseTime: 45, code } },
          { item: { name: "throttled" }, response: { responseTime: 12000, code: 200 } },
        ],
      },
    };
    assert.equal(aggregateTimings([report]).throttled, 12000, `a ${code} was sampled as a fast row`);
  }
});

test("a row that was only ever refused contributes no measurement at all", () => {
  for (const code of [429, 503, 529]) {
    const report = { run: { executions: [{ item: { name: "refused" }, response: { responseTime: 30, code } }] } };
    assert.equal("refused" in aggregateTimings([report]), false, `a ${code}-only row produced a measurement`);
  }
});

// The two modules must not drift: shard-cost skips what the retry pass replays, and it reuses the
// set rather than restating it. Asserting on the shared set catches a future code added to one side
// only, which the per-code cases above cannot see.
test("the skipped set is exactly the retry pass's set", () => {
  assert.deepEqual([...RETRYABLE_CODES].sort((a, b) => a - b), [429, 503, 529]);
  const report = {
    run: {
      executions: [...RETRYABLE_CODES].map((code) => ({ item: { name: `row-${code}` }, response: { responseTime: 30, code } })),
    },
  };
  assert.deepEqual(aggregateTimings([report]), {}, "a retryable code was still sampled");
});

test("aggregateTimings ignores executions with no response", () => {
  const report = {
    run: {
      executions: [
        { item: { name: "errored" } },
        { item: { name: "errored" }, response: { responseTime: 700 } },
      ],
    },
  };
  assert.equal(aggregateTimings([report]).errored, 700);
});

// ----- loading -------------------------------------------------------------------------------------

test("loadTimings reads a table written as plain JSON", () => {
  const p = join(WORK, "timings.json");
  writeFileSync(p, JSON.stringify({ "row-1": 1500, "row-2": 250 }));
  const t = loadTimings(p);
  assert.equal(t.get("row-1"), 1500);
  assert.equal(t.get("row-2"), 250);
});

// Every failure mode here has to degrade to round-robin rather than to an exit code: the timing
// table is an optimisation, and a sweep that refuses to run because a cache file is corrupt has
// turned a performance feature into an outage.
test("a missing, empty or corrupt timing file degrades to no timings", () => {
  assert.equal(loadTimings(join(WORK, "does-not-exist.json")), null);
  assert.equal(loadTimings(""), null);
  assert.equal(loadTimings(null), null);
  const bad = join(WORK, "corrupt.json");
  writeFileSync(bad, "{not json");
  assert.equal(loadTimings(bad), null);
  const empty = join(WORK, "empty.json");
  writeFileSync(empty, "{}");
  assert.equal(loadTimings(empty), null);
});

test("loadTimings drops entries that are not positive numbers", () => {
  const p = join(WORK, "mixed.json");
  writeFileSync(p, JSON.stringify({ good: 100, zero: 0, negative: -5, text: "fast", nothing: null }));
  const t = loadTimings(p);
  assert.deepEqual([...t.keys()], ["good"]);
});

rmSync(WORK, { recursive: true, force: true });
console.log(`\n${passed} passed`);
