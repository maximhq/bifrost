// Unit tests for the generated direct-vs-Bifrost cache-parity items.
// Run directly: `node direct-cache-parity.test.mjs`.
import assert from "node:assert";
import { buildDirectCacheParityItems } from "./direct-cache-parity.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

const ITEMS = buildDirectCacheParityItems();

// The generated test script for an item, as one string.
const scriptOf = (item) =>
  (item.event || [])
    .filter((e) => e.listen === "test")
    .flatMap((e) => e.script.exec)
    .join("\n");

const itemNamed = (fragment) => {
  const found = ITEMS.filter((i) => i.name.includes(fragment));
  assert.strictEqual(found.length, 1, `expected exactly one item matching ${fragment}, got ${found.length}`);
  return found[0];
};

// Collection variables outlive a single iteration. The series variable is reset on
// round 1 for exactly that reason - the best-hit variable, which is what the
// Bifrost leg actually compares against, needs the same treatment or a second
// iteration measures against the first one's baseline.
// Matching the exact call, not the variable name: the name appears wherever the
// variable is merely READ, so a substring search would be satisfied by a script that
// never clears anything.
const UNSET_HIT = `pm.collectionVariables.unset("dcp_gemini_2_5_flash_direct_hit")`;
const RESET_SERIES = `pm.collectionVariables.set("dcp_gemini_2_5_flash_direct_series", JSON.stringify([]))`;
const ERROR_GUARD = "pm.response.code >= 400";

test("round 1 clears the best-hit variable, not just the series", () => {
  const script = scriptOf(itemNamed("gemini/gemini-2.5-flash direct round 1"));
  assert.ok(script.includes(UNSET_HIT), `round 1 never unsets the best-hit variable:\n${script}`);
  assert.ok(script.includes(RESET_SERIES), `round 1 never resets the series:\n${script}`);
});

// A write round that errors returns early. If the reset sits below that guard, an
// errored round 1 leaves the previous iteration's baseline live - the precise case
// the reset exists to prevent.
test("the round 1 reset runs before the error guard", () => {
  const script = scriptOf(itemNamed("gemini/gemini-2.5-flash direct round 1"));
  const reset = script.indexOf(UNSET_HIT);
  const guard = script.indexOf(ERROR_GUARD);
  // Both indexes must be checked BEFORE comparing: a missing reset yields -1, and
  // -1 < guard is true, so the ordering assertion would pass on a script that never
  // resets at all.
  assert.notStrictEqual(reset, -1, "the round 1 reset is missing entirely");
  assert.notStrictEqual(guard, -1, "the error guard is missing entirely");
  assert.ok(
    reset < guard,
    `the reset must precede the error guard, or an errored write round skips it (reset at ${reset}, guard at ${guard})`
  );
});

// A write round that errors must fail its own row. Without an assertion it returns
// silently, and the only downstream symptom is the read round finding nothing cached -
// which is reported as INCONCLUSIVE, not a failure. A cell whose writes always 4xx
// would stay green forever. Gemini makes this five silent rounds, not one.
test("an errored write round fails its own row", () => {
  for (const round of [1, 3]) {
    const script = scriptOf(itemNamed(`gemini/gemini-2.5-flash direct round ${round}`));
    const guard = script.indexOf(ERROR_GUARD);
    assert.notStrictEqual(guard, -1, `round ${round} has no error guard`);

    // Scope to the guard's own block. An assertion merely somewhere after the guard
    // would not run on the error path at all - it has to be inside the branch that
    // returns, between the `{` and the matching `}`.
    const open = script.indexOf("{", guard);
    const close = script.indexOf("\n}", open);
    assert.notStrictEqual(open, -1, `round ${round}'s guard has no block`);
    assert.notStrictEqual(close, -1, `round ${round}'s guard block is unterminated`);
    const block = script.slice(open, close);

    assert.ok(
      block.includes("pm.test("),
      `round ${round} returns on an error without asserting, so the failure is silent:\n${block}`
    );
    // The assertion must actually fail the row, not just run: it has to expect the
    // status code to be below 400.
    assert.ok(
      block.includes("to.be.below(400)"),
      `round ${round}'s error-path pm.test does not assert the response succeeded:\n${block}`
    );
    assert.ok(
      block.includes(`round ${round} responded`),
      `round ${round}'s failure assertion must name its own round:\n${block}`
    );
    assert.ok(block.includes("return;"), `round ${round}'s guard must still bail out:\n${block}`);
  }
});

// The reset must still come first: an errored write round has to clear the previous
// iteration's baseline before it bails out, which is why the assertion goes inside the
// guard rather than above it.
test("the reset still precedes the error assertion on round 1", () => {
  const script = scriptOf(itemNamed("gemini/gemini-2.5-flash direct round 1"));
  const reset = script.indexOf(UNSET_HIT);
  const guard = script.indexOf(ERROR_GUARD);
  assert.notStrictEqual(reset, -1, "the round 1 reset is missing entirely");
  assert.notStrictEqual(guard, -1, "the error guard is missing entirely");
  assert.ok(reset < guard, "the round 1 reset must stay above the error guard");
});

// ROUNDS_BY_SHAPE gives the gemini shape six rounds, so its read round is round 6.
// A label hardcoded to "round 2" misreports which call actually failed.
test("the read round reports its real round number", () => {
  const read = itemNamed("gemini/gemini-2.5-flash bifrost round 6");
  const script = scriptOf(read);
  assert.ok(script.includes("round 6 responded"), `read-round assertion is mislabelled:\n${script}`);
  assert.ok(!script.includes("round 2 responded"), "the hardcoded round 2 label must be gone");
});

test("the direct leg's read-round console line reports its real round number", () => {
  const script = scriptOf(itemNamed("gemini/gemini-2.5-flash direct round 6"));
  assert.ok(script.includes("direct round 6 (read)"), `console label is mislabelled:\n${script}`);
});

// A two-round shape must keep saying round 2 - the fix is to report the actual
// round, not to renumber every shape.
test("a two-round shape still reports round 2", () => {
  const script = scriptOf(itemNamed("openai/gpt-4o-mini bifrost round 2"));
  assert.ok(script.includes("round 2 responded"), `two-round shape lost its label:\n${script}`);
});

console.log(`\n${passed} passed`);
