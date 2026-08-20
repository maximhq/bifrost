// Unit tests for the harness-monitor pass manifest. Run directly:
// `node monitor-passes.test.mjs`. No test framework needed (the tests/e2e/api
// dir has no test runner configured).
import assert from "node:assert";
import {
  parseManifest,
  walkLeaves,
  countLeaves,
  countByProvider,
  sumPassTotals,
} from "./monitor-passes.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

// ----- parseManifest ----------------------------------------------------------

test("reads one entry per complete line", () => {
  const text =
    '{"mode":"parallel","launched":7}\n' +
    '{"mode":"sequential","log":"tmp/newman-cli-cache-parity.log"}\n';
  const passes = parseManifest(text);
  assert.strictEqual(passes.length, 2);
  assert.strictEqual(passes[0].mode, "parallel");
  assert.strictEqual(passes[0].launched, 7);
  assert.strictEqual(passes[1].log, "tmp/newman-cli-cache-parity.log");
});

// The monitor polls the manifest on a timer, so it can read while the Makefile
// is mid-append. A truncated tail must be left for the next poll - parsing it
// would drop that pass permanently once the rest of the line lands.
test("leaves a trailing fragment with no newline for the next poll", () => {
  const passes = parseManifest('{"mode":"parallel"}\n{"mode":"seque');
  assert.strictEqual(passes.length, 1);
  assert.strictEqual(passes[0].mode, "parallel");
});

test("skips a malformed complete line instead of throwing", () => {
  const passes = parseManifest('{"mode":"parallel"}\nnot json\n{"mode":"sequential"}\n');
  assert.strictEqual(passes.length, 2);
  assert.strictEqual(passes[1].mode, "sequential");
});

test("ignores blank lines", () => {
  assert.strictEqual(parseManifest('\n\n{"mode":"parallel"}\n\n').length, 1);
});

test("returns nothing for empty or non-string input", () => {
  assert.deepStrictEqual(parseManifest(""), []);
  assert.deepStrictEqual(parseManifest(undefined), []);
});

// ----- walkLeaves / countLeaves ----------------------------------------------

const collection = {
  item: [
    {
      name: "1. OpenAI",
      item: [
        { name: "chat", request: { url: "{{baseUrl}}/openai/v1/chat/completions" } },
        { name: "embeddings", request: { url: "{{baseUrl}}/openai/v1/embeddings" } },
      ],
    },
    {
      name: "2. Anthropic",
      item: [
        {
          name: "nested",
          item: [{ name: "messages", request: { url: "{{baseUrl}}/anthropic/v1/messages" } }],
        },
      ],
    },
  ],
};

test("counts every request-bearing leaf at any depth", () => {
  assert.strictEqual(countLeaves(collection), 3);
});

test("carries ancestor folder names down to each leaf", () => {
  const seen = [];
  walkLeaves(collection.item, [], (node, ancestors) => {
    seen.push([node.name, ancestors.join(" / ")]);
  });
  assert.deepStrictEqual(seen, [
    ["chat", "1. OpenAI"],
    ["embeddings", "1. OpenAI"],
    ["messages", "2. Anthropic / nested"],
  ]);
});

test("counts nothing for an empty or absent collection", () => {
  assert.strictEqual(countLeaves({}), 0);
  assert.strictEqual(countLeaves(undefined), 0);
});

// ----- countByProvider --------------------------------------------------------

// Stand-in for providerOfItem: attribute by the first provider name that
// appears in the leaf's folder chain.
const itemProvider = (item, ancestors) => {
  const hay = [...ancestors, item.name].join(" ").toLowerCase();
  for (const p of ["openai", "anthropic", "bedrock"]) if (hay.includes(p)) return p;
  return null;
};

test("splits a multi-provider collection by keyword", () => {
  const counts = countByProvider(collection, ["openai", "anthropic", "bedrock"], itemProvider);
  assert.deepStrictEqual(counts, { openai: 2, anthropic: 1, bedrock: 0 });
});

// A leaf that matches no provider must stay uncounted: nothing in the CLI log
// would attribute it either, so counting it would leave the run permanently
// short of its own total and the ETA would never converge.
test("leaves unattributable rows out of every provider's total", () => {
  const withOrphan = {
    item: [...collection.item, { name: "auth matrix", request: { url: "{{baseUrl}}/api/keys" } }],
  };
  const counts = countByProvider(withOrphan, ["openai", "anthropic"], itemProvider);
  assert.deepStrictEqual(counts, { openai: 2, anthropic: 1 });
});

test("never invents a column for a provider this run does not track", () => {
  const counts = countByProvider(collection, ["openai"], itemProvider);
  assert.deepStrictEqual(counts, { openai: 2 });
});

// ----- sumPassTotals ----------------------------------------------------------

test("adds every pass's contribution", () => {
  assert.strictEqual(sumPassTotals({ 0: 120, 1: 4 }), 124);
});

// loadDenominators re-reads on a timer until the filtered collections land on
// disk, so re-reporting pass 0 has to overwrite pass 0 rather than add a second
// copy of it. Keying by pass index is what makes the update idempotent.
test("re-reporting the same pass replaces rather than accumulates", () => {
  const totals = {};
  totals[0] = 120;
  totals[0] = 120;
  totals[1] = 4;
  assert.strictEqual(sumPassTotals(totals), 124);
});

test("treats an empty or absent map as zero", () => {
  assert.strictEqual(sumPassTotals({}), 0);
  assert.strictEqual(sumPassTotals(undefined), 0);
});

console.log(`\n${passed} passed`);
