// Unit tests for item→haystack reduction. Run directly: `node haystack.test.mjs`.
import assert from "node:assert";
import { buildHaystack } from "./haystack.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

// The exact shape that mis-routed the cache matrix: a Gemini cell whose only
// mention of "anthropic" is a comment inside its generated assertion script.
const geminiCellWithAnthropicComment = () => ({
  name: "Cache matrix: gemini/gemini-2.5-pro / control round 1",
  event: [
    {
      listen: "test",
      script: {
        type: "text/javascript",
        exec: [
          "var cached = j.usage.prompt_tokens_details.cached_tokens || 0;",
          "// prompt_tokens is the total with cached reads included; subtract them back out so",
          '// "uncached" means the same thing it does on the anthropic shape.',
        ],
      },
    },
  ],
  request: {
    method: "POST",
    body: { mode: "raw", raw: '{"model":"gemini/gemini-2.5-pro","messages":[]}' },
    url: { raw: "{{baseUrl}}/v1/chat/completions" },
  },
});

test("a provider named only in test-script prose does not enter the haystack", () => {
  const h = buildHaystack(geminiCellWithAnthropicComment(), [
    "12. Backlog Coverage (auto-added missing cases)",
    "Cross-Cut Round 35: Cross-Provider Prompt-Cache Matrix (generated)",
  ]);
  assert.ok(!h.includes("anthropic"), `script prose leaked into routing text: ${h.slice(0, 200)}`);
});

test("the row's real identity still does", () => {
  const h = buildHaystack(geminiCellWithAnthropicComment(), [
    "Cross-Cut Round 35: Cross-Provider Prompt-Cache Matrix (generated)",
  ]);
  // Model and URL are what the row actually calls.
  assert.ok(h.includes("gemini/gemini-2.5-pro"), "model must remain matchable");
  assert.ok(h.includes("/v1/chat/completions"), "url must remain matchable");
  // Name and ancestor folders are how feature aliases select generated rows.
  assert.ok(h.includes("cache matrix"), "item name must remain matchable");
  assert.ok(h.includes("prompt-cache matrix"), "ancestor folder must remain matchable");
});

// A row that genuinely targets a provider must still be claimed by it - the fix
// must narrow routing, not break it.
test("a genuine provider row still matches", () => {
  const h = buildHaystack(
    {
      name: "anthropic/claude-opus-5 prompt caching",
      request: { body: { mode: "raw", raw: '{"model":"anthropic/claude-opus-5"}' }, url: { raw: "{{baseUrl}}/anthropic/v1/messages" } },
    },
    ["10. Feature Variations (per-provider)", "Anthropic Features"]
  );
  assert.ok(h.includes("anthropic"));
  assert.ok(h.includes("claude-"));
});

test("missing fields do not throw", () => {
  assert.strictEqual(typeof buildHaystack({}, undefined), "string");
  assert.ok(buildHaystack({ name: "Folder" }, []).includes("folder"));
});

// Postman's own export format writes descriptions as { content, type }, and
// --extra-collection pushes external items in verbatim - so which encoding a
// description happens to use must not decide whether a row is routable.
test("an object-form description contributes the same text as a string one", () => {
  const asObject = buildHaystack(
    { name: "External row", description: { content: "targets vertex", type: "text/markdown" } },
    []
  );
  const asString = buildHaystack({ name: "External row", description: "targets vertex" }, []);
  assert.ok(asObject.includes("targets vertex"), `object-form description was dropped: ${asObject}`);
  assert.strictEqual(asObject, asString, "both encodings must reduce to the same identity");
});

// Asserting only that a string comes back would pass an implementation that
// serialized the whole object into the haystack - which is the failure mode that
// matters here, since a nested blob could carry a provider name and route the row.
// So assert the content is genuinely absent, against the no-description baseline.
test("a non-string description content is still ignored", () => {
  const baseline = buildHaystack({ name: "x" }, []);

  assert.strictEqual(
    buildHaystack({ name: "x", description: { type: "text/markdown" } }, []),
    baseline,
    "a description object with no content must reduce to the no-description case"
  );

  const nested = buildHaystack({ name: "x", description: { content: { nested: "zzsentinelzz" } } }, []);
  assert.ok(!nested.includes("zzsentinelzz"), `non-string description content leaked: ${nested}`);
  assert.strictEqual(nested, baseline, "a non-string content must reduce to the no-description case");
});

console.log(`\n${passed} passed`);
