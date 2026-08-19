// Unit tests for the generated cross-provider prompt-cache matrix items.
// Run directly: `node crossprovider-cache-matrix.test.mjs`.
import assert from "node:assert";
import { buildCrossProviderCacheMatrixItems, IMPLICIT_ROUNDS } from "./crossprovider-cache-matrix.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

const ITEMS = buildCrossProviderCacheMatrixItems();

const scriptOf = (item, listen) =>
  (item.event || [])
    .filter((e) => e.listen === listen)
    .flatMap((e) => e.script.exec)
    .join("\n");

const itemNamed = (fragment) => {
  const found = ITEMS.filter((i) => i.name.includes(fragment));
  assert.strictEqual(found.length, 1, `expected exactly one item matching ${fragment}, got ${found.length}`);
  return found[0];
};

// Runs a generated round script against a stubbed Postman sandbox and returns what it recorded:
// the series it left in the collection variable, plus the CACHE_MATRIX_REPORT it logged. The
// script is executed rather than string-matched because the property under test is arithmetic
// across rounds, which a substring search cannot see.
function runRound(item, usage, vars) {
  const script = scriptOf(item, "test");
  const logged = [];
  const failures = [];
  const pm = {
    response: { code: 200, json: () => ({ usage }), text: () => JSON.stringify({ usage }) },
    collectionVariables: {
      get: (k) => (k in vars ? vars[k] : null),
      set: (k, v) => {
        vars[k] = v;
      },
    },
    test: (name, fn) => {
      try {
        fn();
      } catch (e) {
        failures.push([name, e.message]);
      }
    },
    expect: (value, message) => ({
      to: {
        be: {
          above: (n) => {
            if (!(value > n)) throw new Error(message);
          },
          below: (n) => {
            if (!(value < n)) throw new Error(message);
          },
        },
      },
    }),
  };
  const console_ = {
    log: (...args) => logged.push(args),
  };
  new Function("pm", "console", script)(pm, console_);
  const report = logged.find((a) => a[0] === "CACHE_MATRIX_REPORT");
  return { report: report ? JSON.parse(report[1]) : null, failures };
}

// An implicit cell whose rounds are known to disagree with each other. Round 2 hits and the last
// round misses, which is the exact shape gemini-2.5-pro/control produced in the run that exposed
// this.
const CELL = "gemini/gemini-2.5-pro / control";
const HIT = { prompt_tokens: 15748, prompt_tokens_details: { cached_tokens: 12254 } };
const MISS = { prompt_tokens: 15748, prompt_tokens_details: { cached_tokens: 0 } };

// Round counts follow IMPLICIT_ROUNDS rather than a literal, so retuning it does not silently turn
// these into tests of a round that no longer carries the verdict.
const rounds = (...hitRounds) =>
  Array.from({ length: IMPLICIT_ROUNDS }, (_, i) => (hitRounds.includes(i + 1) ? HIT : MISS));
const ONE_MID_HIT = rounds(2);
const ALL_MISS = rounds();

function runSeries(usages) {
  const vars = {};
  let last = null;
  for (let i = 0; i < usages.length; i++) {
    const round = i + 1;
    const name = round === usages.length ? `${CELL} round ${round} (verdict)` : `${CELL} round ${round}`;
    last = runRound(itemNamed(name), usages[i], vars);
  }
  return last;
}

// The reported hitRate is the best round's. Pairing it with the last round's counters produced
// rows like "Read 0 | Uncached 15748 | Hit rate 77.8% | PASS", which reads as a contradiction:
// a reader cannot tell whether the cell cached or whether the report is broken.
test("the reported counters describe the same round as the reported hit rate", () => {
  const { report } = runSeries(ONE_MID_HIT);
  assert.ok(report, "the verdict round logged no CACHE_MATRIX_REPORT");
  const billed = report.read + report.write + report.uncached;
  assert.ok(billed > 0, `report has no billed tokens: ${JSON.stringify(report)}`);
  assert.strictEqual(
    (report.read / billed).toFixed(4),
    report.hitRate.toFixed(4),
    `counters and hit rate describe different rounds: ${JSON.stringify(report)}`
  );
  assert.ok(report.read > 0, `best round hit the cache but reported read=0: ${JSON.stringify(report)}`);
});

// The series is what the report prints as the per-round column and what the verdict is taken
// from, so it has to stay a plain list of rates, one per round, in order.
test("the series stays one rate per round, in round order", () => {
  const { report } = runSeries(ONE_MID_HIT);
  assert.strictEqual(
    report.series.length,
    IMPLICIT_ROUNDS,
    `expected ${IMPLICIT_ROUNDS} rounds, got ${JSON.stringify(report.series)}`
  );
  assert.ok(
    report.series.every((h) => typeof h === "number"),
    `series must be numeric rates: ${JSON.stringify(report.series)}`
  );
  assert.strictEqual(report.series[0], 0, `round 1 missed but reported ${report.series[0]}`);
  assert.ok(report.series[1] > 0, `round 2 hit but reported ${report.series[1]}`);
  assert.strictEqual(report.hitRate, Math.max(...report.series), "hitRate is not the best round");
});

// The bar is "cached at least once", not "cached on the last round". A cell that hit only in the
// middle still passes, and a cell that never hit still fails - that is the whole signal.
test("a mid-series hit passes and an all-miss series fails", () => {
  assert.deepStrictEqual(runSeries(ONE_MID_HIT).failures, []);
  const dry = runSeries(ALL_MISS);
  assert.strictEqual(dry.failures.length, 1, `expected exactly one failure, got ${JSON.stringify(dry.failures)}`);
  assert.match(dry.failures[0][0], /caches at least once/);
  assert.strictEqual(dry.report.read, 0, `an all-miss series must report read=0: ${JSON.stringify(dry.report)}`);
});

// Round 1 resets the series because collection variables outlive an iteration. Without it a second
// iteration appends to the first one's series and inherits its hit, reporting a pass for a run in
// which caching never engaged.
test("round 1 starts a fresh series rather than appending to the previous iteration's", () => {
  const vars = {};
  runSeries.call(null, ONE_MID_HIT);
  const seriesKey = `cm_series_gemini__gemini__gemini-gemini-2-5-pro__control`;
  vars[seriesKey] = JSON.stringify([{ h: 1, r: 999, w: 0, u: 0 }]);
  let last = null;
  const usages = ALL_MISS;
  for (let i = 0; i < usages.length; i++) {
    const round = i + 1;
    const name = round === usages.length ? `${CELL} round ${round} (verdict)` : `${CELL} round ${round}`;
    last = runRound(itemNamed(name), usages[i], vars);
  }
  assert.strictEqual(
    last.report.series.length,
    IMPLICIT_ROUNDS,
    `stale round leaked in: ${JSON.stringify(last.report.series)}`
  );
  assert.strictEqual(last.failures.length, 1, "a stale hit satisfied the verdict of an all-miss run");
});

// The renderer decides "PASS" vs "PASS (warm)" from the write counters this report carries. The
// counters beside hitRate describe the BEST round, and on a cold run that is round 2 (read, no
// write) rather than round 1 (write, no read) -- so a cold run looked warm and a whole matrix
// reported that the write path had never been exercised. writeTotal sums every round instead.
test("the verdict report carries writes summed across rounds, not just the best round's", () => {
  const WRITE = { prompt_tokens: 15748, prompt_tokens_details: { cached_tokens: 0, cache_write_tokens: 15748 } };
  const { report } = runSeries([WRITE, ...rounds(2).slice(1)]);
  assert.ok(report, "the verdict round logged no CACHE_MATRIX_REPORT");
  assert.notStrictEqual(report.writeTotal, undefined, "writeTotal is missing from the report");
  assert.strictEqual(report.writeTotal, 15748, `round 1's write did not reach writeTotal: ${JSON.stringify(report)}`);
  // The point of the field: the best round wrote nothing, so a renderer reading `write` alone
  // would call this cold run warm.
  assert.strictEqual(report.write, 0, "fixture no longer exercises the best-round-is-not-round-1 case");
  assert.ok(report.writeTotal > report.write, "writeTotal must outlive the best round's write");
});

console.log(`\n${passed} passed`);
