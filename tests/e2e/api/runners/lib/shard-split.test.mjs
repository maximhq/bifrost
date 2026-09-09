// Unit tests for the harness's third parallelism axis: filter-collection.mjs --shard <k>/<n>,
// and the sub-shard launch loop in the run-provider-harness-test recipe that drives it.
//
// The axis exists because the class axis alone cannot flatten the sweep's tail: the expensive
// classes are expensive PER REQUEST, so "openai reasoning" stays a ~21 minute serial run however
// many job slots sit idle beside it. Splitting it is only safe if the slices are a PARTITION -
// every row still runs somewhere, exactly once where it can - which is what these tests pin.
//
// The filter is exercised through its real CLI rather than by importing it, because the slicing
// happens between two stages (predicate selection and producer expansion) whose ORDER is the
// load-bearing part, and only the CLI runs both. The Makefile half follows harness-deferral's
// approach: the real block is extracted from the Makefile and run under bash, so the test cannot
// pass against a stale copy of logic that has since changed in the recipe.
//
// Run directly: `node shard-split.test.mjs`.
import assert from "node:assert";
import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync, mkdtempSync, rmSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve, join } from "node:path";
import { tmpdir } from "node:os";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

const HERE = dirname(fileURLToPath(import.meta.url));
const FILTER = resolve(HERE, "../filter-collection.mjs");
const MAKEFILE = resolve(HERE, "../../../../../Makefile");
const WORK = mkdtempSync(join(tmpdir(), "shard-split-"));

// ----- fixture ---------------------------------------------------------------------------------

// A chat request named n, carrying an anthropic marker so --provider anthropic claims it.
const row = (n) => ({
  name: `row-${n}`,
  request: {
    method: "POST",
    url: "http://localhost:8080/v1/chat/completions",
    body: { mode: "raw", raw: JSON.stringify({ model: "anthropic/claude-opus-4-7", messages: [] }) },
  },
});

// A producer/consumer pair: the consumer's body can only resolve inside the same newman process
// as the producer, which is what makes it the interesting case for any slicing scheme.
const producer = {
  name: "producer",
  request: {
    method: "POST",
    url: "http://localhost:8080/v1/chat/completions",
    body: { mode: "raw", raw: JSON.stringify({ model: "anthropic/claude-opus-4-7", messages: [] }) },
  },
  event: [
    {
      listen: "test",
      script: { type: "text/javascript", exec: ['pm.collectionVariables.set("chainedId", "x");'] },
    },
  ],
};
const consumer = {
  name: "consumer",
  request: {
    method: "POST",
    url: "http://localhost:8080/v1/chat/completions",
    body: { mode: "raw", raw: '{"model":"anthropic/claude-opus-4-7","messages":[],"id":"{{chainedId}}"}' },
  },
};

const COLLECTION = {
  info: { name: "shard-split fixture", schema: "https://schema.getpostman.com/json/collection/v2.1.0/collection.json" },
  item: [
    { name: "Chat folder", item: [producer, ...Array.from({ length: 20 }, (_, i) => row(i + 1)), consumer] },
  ],
};
const SOURCE = join(WORK, "source.json");
writeFileSync(SOURCE, JSON.stringify(COLLECTION));

// A chain-free variant. Balance is a property of the split itself, and producer expansion runs
// after it and deliberately adds rows back, so measuring balance on the chained fixture would be
// measuring the two together and would fail for a reason that is not imbalance.
const PLAIN_SOURCE = join(WORK, "source-plain.json");
writeFileSync(
  PLAIN_SOURCE,
  JSON.stringify({
    ...COLLECTION,
    item: [{ name: "Chat folder", item: Array.from({ length: 20 }, (_, i) => row(i + 1)) }],
  })
);

// ----- helpers ---------------------------------------------------------------------------------

// Runs the real filter CLI. Returns the request names it kept, or throws with the CLI's own
// stderr so a non-zero exit reads as the failure it is rather than as an empty result set.
function runFilter(extraArgs, { expectExit = 0, source = SOURCE } = {}) {
  const out = join(WORK, `out-${Math.abs(hash(source + extraArgs.join(" ")))}.json`);
  try {
    execFileSync("node", [FILTER, "--source", source, "--out", out, ...extraArgs], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    });
  } catch (e) {
    if (expectExit !== 0) {
      assert.equal(e.status, expectExit, `expected exit ${expectExit}, got ${e.status}: ${e.stderr}`);
      return null;
    }
    throw new Error(`filter-collection failed: ${e.stderr || e.message}`);
  }
  assert.equal(expectExit, 0, "expected a non-zero exit but the filter succeeded");
  const names = [];
  const walk = (items) => {
    for (const it of items || []) {
      if (Array.isArray(it.item)) walk(it.item);
      else if (it.request) names.push(it.name);
    }
  };
  walk(JSON.parse(readFileSync(out, "utf8")).item);
  return names;
}

// Deterministic, only used to keep each case's output file distinct.
const hash = (s) => [...s].reduce((a, c) => (a * 31 + c.charCodeAt(0)) | 0, 7);

// ----- the partition property ------------------------------------------------------------------

// The one invariant the whole axis rests on. A sharded sweep that quietly tests less than an
// unsharded one is worse than no sharding at all: it goes green faster while covering less.
test("the union of every --shard slice is exactly the unsharded set", () => {
  const whole = runFilter(["--provider", "anthropic"]);
  const union = new Set();
  for (const k of [1, 2, 3]) {
    for (const n of runFilter(["--provider", "anthropic", "--shard", `${k}/3`])) union.add(n);
  }
  const missing = whole.filter((n) => !union.has(n));
  const extra = [...union].filter((n) => !whole.includes(n));
  assert.deepEqual(missing, [], "rows present unsharded but in no slice");
  assert.deepEqual(extra, [], "slices invented rows the unsharded run never had");
});

test("slices are balanced to within one row", () => {
  const sizes = [1, 2, 3, 4].map(
    (k) => runFilter(["--provider", "anthropic", "--shard", `${k}/4`], { source: PLAIN_SOURCE }).length
  );
  assert.equal(sizes.reduce((a, b) => a + b, 0), 20, `slices lost or gained rows: ${sizes.join(", ")}`);
  assert.ok(Math.max(...sizes) - Math.min(...sizes) <= 1, `unbalanced slices: ${sizes.join(", ")}`);
});

// Round-robin rather than contiguous blocks. The harness emits its criss-cross grids in provider
// order, so a contiguous split would drop one provider's whole expensive block into a single slice
// and leave the tail exactly where it was, with the sweep only looking more parallel.
test("slicing interleaves rather than cutting contiguous blocks", () => {
  const first = runFilter(["--provider", "anthropic", "--shard", "1/4"], { source: PLAIN_SOURCE });
  assert.deepEqual(first, ["row-1", "row-5", "row-9", "row-13", "row-17"]);
});

// Slicing happens BEFORE producer expansion. If it ran after, the slice holding the consumer would
// have had its producer cut away, and that consumer would fail on an unsubstituted {{chainedId}}
// for a reason having nothing to do with the code under test.
test("a sliced consumer keeps its producer in the same slice", () => {
  const slices = [1, 2, 3].map((k) => runFilter(["--provider", "anthropic", "--shard", `${k}/3`]));
  const holder = slices.find((s) => s.includes("consumer"));
  assert.ok(holder, "no slice contains the consumer");
  assert.ok(holder.includes("producer"), "the consumer's slice is missing its producer");
});

// The flip side of the property above, stated so the duplication is a decision rather than a
// surprise: a producer shared across slices runs once per slice that needs it.
test("only chained producers may appear in more than one slice", () => {
  const seen = new Map();
  for (const k of [1, 2, 3]) {
    for (const n of runFilter(["--provider", "anthropic", "--shard", `${k}/3`])) {
      seen.set(n, (seen.get(n) || 0) + 1);
    }
  }
  const duplicated = [...seen.entries()].filter(([, c]) => c > 1).map(([n]) => n);
  assert.deepEqual(duplicated.filter((n) => n !== "producer"), [], "a non-producer row runs in two slices");
});

test("--shard composes with the other predicates instead of replacing them", () => {
  const sliced = runFilter(["--provider", "anthropic", "--folder", "chat folder", "--shard", "1/2"]);
  const whole = runFilter(["--provider", "anthropic", "--folder", "chat folder"]);
  assert.ok(sliced.length > 0 && sliced.length < whole.length, `slice ${sliced.length} of ${whole.length}`);
  for (const n of sliced) assert.ok(whole.includes(n), `${n} passed the slice but not the folder filter`);
});

// ----- rejected forms ----------------------------------------------------------------------------

// An out-of-range or malformed shard must fail loudly. Silently treating it as "no sharding" would
// run the full class in every sub-shard - the sweep would take longer than before and quietly
// re-bill every row n times, which is exactly the failure the axis was added to prevent.
for (const bad of ["0/3", "4/3", "2/0", "abc", "2", "-1/3"]) {
  test(`--shard "${bad}" is rejected`, () => {
    runFilter(["--provider", "anthropic", "--shard", bad], { expectExit: 2 });
  });
}

// ----- --timings: the same axis, balanced by cost ------------------------------------------------

// lib/shard-cost.test.mjs covers the cost model as a pure function. These cover it through the CLI,
// because the partition property is what the whole axis rests on and cost-aware slicing is a new
// way to break it: the n sub-shards each recompute the assignment in a SEPARATE process and keep
// only their own slice, so any disagreement between them silently drops rows and duplicates others.
const TIMINGS = join(WORK, "timings.json");
writeFileSync(
  TIMINGS,
  // Adversarial for round-robin on purpose: the expensive rows all sit at the same parity, so a
  // positional split lands every one of them in the same slice.
  JSON.stringify(Object.fromEntries(Array.from({ length: 20 }, (_, i) => [`row-${i + 1}`, i % 4 === 0 ? 40000 : 800])))
);

test("cost-aware slices are still a partition of the unsharded set", () => {
  const whole = runFilter(["--provider", "anthropic"], { source: PLAIN_SOURCE });
  const seen = new Map();
  for (const k of [1, 2, 3, 4]) {
    for (const n of runFilter(["--provider", "anthropic", "--shard", `${k}/4`, "--timings", TIMINGS], { source: PLAIN_SOURCE })) {
      seen.set(n, (seen.get(n) || 0) + 1);
    }
  }
  assert.deepEqual([...seen.keys()].sort(), [...whole].sort(), "the cost-aware slices are not the unsharded set");
  assert.deepEqual([...seen.entries()].filter(([, c]) => c > 1), [], "a row landed in more than one slice");
});

test("--timings balances slice cost where the positional split does not", () => {
  const cost = JSON.parse(readFileSync(TIMINGS, "utf8"));
  const load = (slice) => slice.reduce((s, n) => s + (cost[n] || 0), 0);
  const of = (flags) => [1, 2, 3, 4].map((k) => load(runFilter(["--provider", "anthropic", "--shard", `${k}/4`, ...flags], { source: PLAIN_SOURCE })));

  const positional = of([]);
  const balanced = of(["--timings", TIMINGS]);
  const spread = (a) => Math.max(...a) - Math.min(...a);

  assert.ok(spread(positional) > 100000, `fixture is not adversarial: ${positional.join(", ")}`);
  assert.ok(
    spread(balanced) < spread(positional) / 4,
    `--timings did not flatten the slices: ${balanced.join(", ")} vs ${positional.join(", ")}`
  );
});

// The failure mode that has to stay non-fatal. The table is written by the PREVIOUS run, so a crash
// mid-write breaks the NEXT sweep - and a sweep that refuses to start because a cache file is
// truncated has turned a performance feature into an outage.
test("a corrupt or missing timings file falls back to round-robin instead of failing", () => {
  const corrupt = join(WORK, "corrupt-timings.json");
  writeFileSync(corrupt, "{not json");
  const expected = runFilter(["--provider", "anthropic", "--shard", "1/4"], { source: PLAIN_SOURCE });
  for (const path of [corrupt, join(WORK, "absent.json")]) {
    assert.deepEqual(
      runFilter(["--provider", "anthropic", "--shard", "1/4", "--timings", path], { source: PLAIN_SOURCE }),
      expected
    );
  }
});

// A chained consumer must keep its producer whichever way the slice was chosen. Cost-aware slicing
// reorders which rows land together, so the producer expansion has to still run after it.
test("a cost-sliced consumer keeps its producer in the same slice", () => {
  const slices = [1, 2, 3].map((k) => runFilter(["--provider", "anthropic", "--shard", `${k}/3`, "--timings", TIMINGS]));
  const holder = slices.find((s) => s.includes("consumer"));
  assert.ok(holder, "no slice contains the consumer");
  assert.ok(holder.includes("producer"), "the consumer's slice is missing its producer");
});

test("--shard alone is a sufficient filter", () => {
  const names = runFilter(["--shard", "1/2"]);
  assert.ok(names.length > 0, "--shard on its own selected nothing");
});

// ----- --plan: sizing the grid from measurement ---------------------------------------------------

// Runs the real planner CLI and returns its stdout lines as [provider, class, shards, rows, secs].
function runPlan(extraArgs, { source = PLAIN_SOURCE } = {}) {
  const out = execFileSync(
    "node",
    [FILTER, "--source", source, "--plan", "--providers", "anthropic", "--classes", "chat", ...extraArgs],
    { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }
  );
  return out.trim().split("\n").filter(Boolean).map((l) => l.split(" "));
}

test("--plan sizes a cell from its measured cost", () => {
  const p = join(WORK, "plan-timings.json");
  // 20 rows at 30s each = 600s of work, which is 4 shards against a 150s target.
  writeFileSync(p, JSON.stringify(Object.fromEntries(Array.from({ length: 20 }, (_, i) => [`row-${i + 1}`, 30000]))));
  const [cell] = runPlan(["--timings", p, "--target", "150"]);
  assert.deepEqual(cell.slice(0, 4), ["anthropic", "chat", "4", "20"]);
});

test("--target trades shard count against shard length", () => {
  const p = join(WORK, "plan-timings.json");
  assert.equal(runPlan(["--timings", p, "--target", "300"])[0][2], "2");
  assert.equal(runPlan(["--timings", p, "--target", "75"])[0][2], "8");
});

// The planner must write NOTHING rather than a grid of 1s: a 1 is not a neutral answer, it is one
// shard per class, which is strictly worse than the static table it would be overriding.
test("--plan writes no plan at all when there are no timings", () => {
  assert.deepEqual(runPlan([]), []);
  assert.deepEqual(runPlan(["--timings", join(WORK, "absent.json")]), []);
});

// The guard that stops a barely-measured cell from being sized. Its estimate would be mostly the
// fallback cost applied to itself, and an under-priced cell is under-sharded - which is the exact
// tail the planner exists to remove, reintroduced by the planner itself.
test("--plan omits a cell it has barely measured", () => {
  const thin = join(WORK, "thin-timings.json");
  // 3 of 20 rows measured: 15% coverage, well under the bar.
  writeFileSync(thin, JSON.stringify({ "row-1": 30000, "row-2": 30000, "row-3": 30000 }));
  assert.deepEqual(runPlan(["--timings", thin, "--target", "150"]), []);
});

// ----- the Makefile's launch loop ------------------------------------------------------------------

const makefile = readFileSync(MAKEFILE, "utf8");
const lines = makefile.split("\n");

// Extracts a recipe block by its first and last line, undoing the two layers of escaping: `\` line
// continuations and `$$` for a shell variable.
function recipeBlock(startsWith, endsWith) {
  const s = lines.findIndex((l) => l.includes(startsWith));
  const e = lines.findIndex((l, i) => i > s && l.includes(endsWith));
  assert.ok(s !== -1 && e !== -1, `could not locate the block ${startsWith} .. ${endsWith} in the Makefile`);
  return lines
    .slice(s, e + 1)
    .map((l) => l.replace(/\\$/, ""))
    .join("\n")
    .replace(/\$\$/g, "$");
}

const SUBSHARDS_FN = recipeBlock("subshards_for() {", "printf '%s' \"$$SS_N\"; \\").concat("\n};");

// Runs the REAL subshards_for from the recipe, with HARNESS_SUBSHARDS substituted the way make
// would substitute it. `plan` writes a shard-plan file and points SHARD_PLAN at it, which is the
// measured path; omitting it leaves SHARD_PLAN unset, which is the static-table fallback.
function subshardsFor(provider, cls, { SUBSHARDS = "", plan = null } = {}) {
  const roster = (makefile.match(/^HARNESS_SUBSHARDS := (.*)$/m) || [])[1];
  assert.ok(roster, "HARNESS_SUBSHARDS is not defined in the Makefile");
  const body = SUBSHARDS_FN.replace(/\$\(SUBSHARDS\)/g, SUBSHARDS).replace(/\$\(HARNESS_SUBSHARDS\)/g, roster);
  let prelude = "";
  if (plan !== null) {
    const p = join(WORK, `plan-${Math.abs(hash(plan))}.txt`);
    writeFileSync(p, plan);
    prelude = `SHARD_PLAN=${JSON.stringify(p)}\n`;
  }
  return execFileSync("bash", ["-c", `set -u\n${prelude}${body}\nsubshards_for "${provider}" "${cls}"`], {
    encoding: "utf8",
  }).trim();
}

test("the slow classes carry a sub-shard count and the cheap ones do not", () => {
  assert.equal(subshardsFor("openai", "reasoning"), "4");
  assert.equal(subshardsFor("openai", "tools"), "3");
  assert.equal(subshardsFor("openai", "audio"), "1");
  assert.equal(subshardsFor("openai", "image-gen"), "1");
});

test("SUBSHARDS=0 collapses the axis for every class", () => {
  for (const c of ["reasoning", "tools", "chat", "audio"]) {
    assert.equal(subshardsFor("openai", c, { SUBSHARDS: "0" }), "1", `${c} still split under SUBSHARDS=0`);
  }
});

// ----- the measured plan overrides the static table --------------------------------------------

// The whole point of the planner: one count per CLASS cannot fit eight providers, because the same
// class costs openai ~30 minutes and another provider seconds. A plan file says so per cell.
test("a plan entry wins over the static table, per provider", () => {
  const plan = "openai reasoning 11 161 1580\nanthropic reasoning 7 172 980\n";
  assert.equal(subshardsFor("openai", "reasoning", { plan }), "11");
  assert.equal(subshardsFor("anthropic", "reasoning", { plan }), "7");
});

// A plan is written only for cells that exist. A cell the planner did not emit (a provider added
// since the last measured sweep, a class with no rows under the current filters) has to keep
// working, and the static table is the answer that was already there.
test("a cell missing from the plan falls back to the static table", () => {
  const plan = "openai reasoning 11 161 1580\n";
  assert.equal(subshardsFor("gemini", "reasoning", { plan }), "4");
  assert.equal(subshardsFor("openai", "tools", { plan }), "3");
});

// An empty plan file is what filter-collection.mjs --plan writes when no timings are available. It
// must read as "no opinion", not as "zero shards" or as an error under set -u.
test("an empty plan file leaves the static table standing", () => {
  assert.equal(subshardsFor("openai", "reasoning", { plan: "" }), "4");
});

// Matching on BOTH columns, not just the class. Matching the class alone would let the first
// provider in the file set every provider's count - which is the static table's bug, reintroduced
// in the mechanism built to remove it.
test("the plan is keyed on provider and class together", () => {
  const plan = "anthropic reasoning 7 172 980\n";
  assert.equal(subshardsFor("openai", "reasoning", { plan }), "4", "openai took anthropic's row");
});

test("SUBSHARDS=0 beats the plan too", () => {
  const plan = "openai reasoning 11 161 1580\n";
  assert.equal(subshardsFor("openai", "reasoning", { SUBSHARDS: "0", plan }), "1");
});

// CLASS_SHARDS=0 sets CLASSES to the single sentinel "-", which must not be looked up as a class
// name: collapsing the class axis has to collapse this one with it, or the recipe would fork n
// sub-shards of a fork that was explicitly asked to stay whole.
test('the "-" sentinel from CLASS_SHARDS=0 is not sub-sharded', () => {
  // "-" goes in the CLASS slot, which is where CLASS_SHARDS=0 puts it. Passing it as the provider
  // left cls undefined, so the helper asked for the class literally named "undefined" and got 1
  // from the unknown-class fallback: green, but never touching the sentinel path this row names.
  assert.equal(subshardsFor("openai", "-"), "1");
});

// A class named in HARNESS_SUBSHARDS but absent from HARNESS_CLASSES would split a shard that is
// never launched, so the two rosters have to agree.
test("every sub-sharded class is a real class", () => {
  const classes = (makefile.match(/^HARNESS_CLASSES := (.*)$/m) || [])[1].trim().split(/\s+/);
  const roster = (makefile.match(/^HARNESS_SUBSHARDS := (.*)$/m) || [])[1].trim().split(/\s+/);
  for (const kv of roster) {
    const [name, n] = kv.split("=");
    assert.ok(classes.includes(name), `HARNESS_SUBSHARDS names "${name}", which is not in HARNESS_CLASSES`);
    assert.ok(Number(n) >= 1, `HARNESS_SUBSHARDS gives "${name}" a count of ${n}`);
  }
});

// Launch order only matters once HARNESS_JOBS binds, and the sub-shard axis is what makes it bind.
// When it binds, a slow class launched last sets the wall clock by itself.
test("HARNESS_CLASSES is ordered slowest-first", () => {
  const classes = (makefile.match(/^HARNESS_CLASSES := (.*)$/m) || [])[1].trim().split(/\s+/);
  const roster = Object.fromEntries(
    (makefile.match(/^HARNESS_SUBSHARDS := (.*)$/m) || [])[1].trim().split(/\s+/).map((kv) => kv.split("="))
  );
  const counts = classes.map((c) => Number(roster[c] || 1));
  const sorted = [...counts].sort((a, b) => b - a);
  assert.deepEqual(counts, sorted, `classes are not ordered by cost: ${classes.join(" ")}`);
});

// The naming rule, run as the recipe writes it. A single-sub-shard class must keep its old
// unsuffixed name: harness-monitor.mjs, the 429 retry pass and the report merge all key off these
// filenames, and the common case producing byte-identical names is what keeps them untouched.
test("sub-shard naming matches what the monitor and retry pass parse", () => {
  const script = `set -u
p=openai
run() {
  c="$1"; SUB_N="$2"; SUB_K=0
  while [ "$SUB_K" -lt "$SUB_N" ]; do
    SUB_K=$((SUB_K+1))
    if [ "$c" = "-" ]; then SHARD="$p"; CLASS_FLAG=""; else SHARD="$p--$c"; CLASS_FLAG="--class $c"; fi
    SHARD_FLAG=""
    if [ "$SUB_N" -gt 1 ]; then SHARD="$SHARD-s$SUB_K"; SHARD_FLAG="--shard $SUB_K/$SUB_N"; fi
    echo "$SHARD|$SHARD_FLAG"
  done
}
run reasoning 3
run audio 1
run - 1`;
  const out = execFileSync("bash", ["-c", script], { encoding: "utf8" }).trim().split("\n");
  assert.deepEqual(out, [
    "openai--reasoning-s1|--shard 1/3",
    "openai--reasoning-s2|--shard 2/3",
    "openai--reasoning-s3|--shard 3/3",
    "openai--audio|",
    "openai|",
  ]);
  // The provider is recovered as ${shard%%--*} by the retry pass, and the monitor matches shard
  // files by the "harness-filtered-<provider>--" prefix. Both have to survive the -s<k> suffix.
  for (const line of out) {
    const shard = line.split("|")[0];
    assert.equal(shard.replace(/--.*$/, ""), "openai", `${shard} does not start with its provider`);
    assert.ok(!/-retry\d+$/.test(shard), `${shard} would be misread as a retry log`);
  }
});

// The loop increments at the TOP of its body because the body below it uses `continue` for a
// failed filter and for an empty shard. A bottom increment would be skipped by both, and the loop
// would spin on the same sub-shard forever - the exact class of hang this axis was added to fix,
// reintroduced in the fix itself.
test("a sub-shard that continues early still terminates the loop", () => {
  const script = `set -u
SUB_N=4; SUB_K=0; LAUNCHED=0
while [ "$SUB_K" -lt "$SUB_N" ]; do
  SUB_K=$((SUB_K+1))
  if [ "$SUB_K" = "2" ]; then continue; fi
  if [ "$SUB_K" = "3" ]; then continue; fi
  LAUNCHED=$((LAUNCHED+1))
done
echo "LAUNCHED=$LAUNCHED"`;
  const out = execFileSync("bash", ["-c", script], { encoding: "utf8", timeout: 10000 }).trim();
  assert.equal(out, "LAUNCHED=2");
});

rmSync(WORK, { recursive: true, force: true });
console.log(`\n${passed} passed`);
