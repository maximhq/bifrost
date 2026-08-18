// Unit tests for the generated cross-provider prompt-cache matrix items.
// Run directly: `node crossprovider-cache-matrix.test.mjs`.
import assert from "node:assert";
import { buildCrossProviderCacheMatrixItems } from "./crossprovider-cache-matrix.mjs";

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

// An implicit cell whose four rounds are known to disagree with each other. Round 2 hits and the
// last round misses, which is the exact shape gemini-2.5-pro/control produced in the run that
// exposed this.
const CELL = "gemini/gemini-2.5-pro / control";
const HIT = { prompt_tokens: 15748, prompt_tokens_details: { cached_tokens: 12254 } };
const MISS = { prompt_tokens: 15748, prompt_tokens_details: { cached_tokens: 0 } };

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
  const { report } = runSeries([MISS, HIT, MISS, MISS]);
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
  const { report } = runSeries([MISS, HIT, MISS, MISS]);
  assert.strictEqual(report.series.length, 4, `expected 4 rounds, got ${JSON.stringify(report.series)}`);
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
  assert.deepStrictEqual(runSeries([MISS, HIT, MISS, MISS]).failures, []);
  const dry = runSeries([MISS, MISS, MISS, MISS]);
  assert.strictEqual(dry.failures.length, 1, `expected exactly one failure, got ${JSON.stringify(dry.failures)}`);
  assert.match(dry.failures[0][0], /caches at least once/);
  assert.strictEqual(dry.report.read, 0, `an all-miss series must report read=0: ${JSON.stringify(dry.report)}`);
});

// Round 1 resets the series because collection variables outlive an iteration. Without it a second
// iteration appends to the first one's series and inherits its hit, reporting a pass for a run in
// which caching never engaged.
test("round 1 starts a fresh series rather than appending to the previous iteration's", () => {
  const vars = {};
  runSeries.call(null, [MISS, HIT, MISS, MISS]);
  const seriesKey = `cm_series_gemini__gemini__gemini-gemini-2-5-pro__control`;
  vars[seriesKey] = JSON.stringify([{ h: 1, r: 999, w: 0, u: 0 }]);
  let last = null;
  const usages = [MISS, MISS, MISS, MISS];
  for (let i = 0; i < usages.length; i++) {
    const round = i + 1;
    const name = round === usages.length ? `${CELL} round ${round} (verdict)` : `${CELL} round ${round}`;
    last = runRound(itemNamed(name), usages[i], vars);
  }
  assert.strictEqual(last.report.series.length, 4, `stale round leaked in: ${JSON.stringify(last.report.series)}`);
  assert.strictEqual(last.failures.length, 1, "a stale hit satisfied the verdict of an all-miss run");
});

console.log(`\n${passed} passed`);
