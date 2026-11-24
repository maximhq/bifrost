// Unit tests for provider attribution. Run directly: `node provider-attribution.test.mjs`.
// No test framework needed (the tests/e2e/api dir has no test runner configured), same shape as
// chained-vars.test.mjs next door.
import assert from "node:assert";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { makeMatchProvider, providerOfFolder, providerOfItem, providerOfLogLine } from "./provider-attribution.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

// The providers a sequential run tracks.
const match = makeMatchProvider((p) => ["openai", "anthropic", "bedrock", "gemini", "vertex", "azure", "huggingface"].includes(p));

// A direct Vertex row, exactly as provider-harness.json carries it: the model
// is a collection variable, and 46 rows look like this.
const vertexRow = {
  name: "vertex/gemini-2.5-pro",
  request: { url: { raw: "{{baseUrl}}/genai/v1beta/models/{{vertexModel}}:generateContent" } },
};
const VERTEX_FOLDER = "7. Vertex (via /genai)";

// What newman prints for that same row once the variables are substituted. The
// literal "vertex" the raw URL matched on is simply gone from this string.
const vertexLogLine = "post http://localhost:8080/genai/v1beta/models/gemini-2.5-pro:generatecontent [200 ok, 1.2kb, 340ms]";

// The bug this file exists for: totals counted under one provider, results
// recorded under another, so vertex can never reach its own total and gemini
// reports passes for requests it never made.
test("a vertex row is attributed the same before and after variable substitution", () => {
  const denominator = providerOfItem(vertexRow, [VERTEX_FOLDER], match);
  const numerator = providerOfLogLine(vertexLogLine, VERTEX_FOLDER, match);
  assert.strictEqual(denominator, "vertex", "denominator should own the row");
  assert.strictEqual(numerator, denominator, "numerator and denominator disagree, so the row is double-booked");
});

// Gemini's own rows travel the same /genai path, so the fix must not simply
// hand every /genai row to vertex.
test("a genuine gemini row is still gemini", () => {
  const geminiRow = {
    name: "gemini/gemini-2.5-flash",
    request: { url: { raw: "{{baseUrl}}/genai/v1beta/models/gemini-2.5-flash:generateContent" } },
  };
  const folder = "10. Feature Variations (per-provider) / Gemini";
  const denominator = providerOfItem(geminiRow, ["Gemini", "GenAI Features"], match);
  const numerator = providerOfLogLine(
    "post http://localhost:8080/genai/v1beta/models/gemini-2.5-flash:generatecontent [200 ok, 1.2kb, 12ms]",
    "GenAI Features",
    match
  );
  assert.strictEqual(denominator, "gemini");
  assert.strictEqual(numerator, "gemini");
  assert.ok(folder);
});

// A row under a heading that names no provider has to keep working off its own
// text, or every cross-cut folder loses attribution entirely.
test("an unattributable folder falls back to the row's own signals", () => {
  const row = {
    name: "openai/gpt-4o-mini",
    request: { url: { raw: "{{baseUrl}}/openai/v1/chat/completions" } },
  };
  const folder = "8.1 Text Chat (non-streaming)";
  assert.strictEqual(providerOfItem(row, [folder], match), "openai");
  assert.strictEqual(
    providerOfLogLine("post http://localhost:8080/openai/v1/chat/completions [200 ok, 1kb, 9ms]", folder, match),
    "openai"
  );
});

test("a Hugging Face folder is attributed to its harness partition", () => {
  const folder = "61. Hugging Face Baseten model discovery (#6631)";
  const row = {
    name: "Hugging Face Baseten models appear in /v1/models - #6631",
    request: { url: { raw: "{{baseUrl}}/v1/models?provider=huggingface" } },
  };
  assert.strictEqual(providerOfItem(row, [folder], match), "huggingface");
  assert.strictEqual(providerOfLogLine("get http://localhost:8080/v1/models?provider=huggingface [200 ok]", folder, match), "huggingface");
});

// Ordering still has to keep the more specific provider ahead of the broader
// one when both keywords are present in a single heading.
test("a Vertex heading that also mentions Gemini stays with vertex", () => {
  assert.strictEqual(
    providerOfLogLine("post http://x/y [200 ok, 1kb, 9ms]", "Vertex Backlog Round 2 (Gemini-on-Vertex variants)", match),
    "vertex"
  );
});

// Baseten rows reach the gateway through /anthropic and name OpenAI-only fields; they belong to
// the baseten partition filter-collection.mjs forks, while Hugging Face's Baseten-hosted rows stay
// with huggingface.
test("Baseten rows stay with baseten and Hugging Face Baseten rows with huggingface", () => {
  const withBaseten = makeMatchProvider((p) => ["openai", "anthropic", "huggingface", "baseten"].includes(p));
  const folder = "337. Baseten provider (baseten provider)";
  const anthropicRoute = {
    name: "/anthropic/v1/messages - Anthropic SDK shape routed to baseten/zai-org/GLM-5.3",
    request: { url: { raw: "{{baseUrl}}/anthropic/v1/messages" } },
  };
  assert.strictEqual(providerOfItem(anthropicRoute, [folder], withBaseten), "baseten");
  assert.strictEqual(providerOfLogLine("post http://localhost:8080/anthropic/v1/messages [200 ok]", folder, withBaseten), "baseten");
  assert.strictEqual(withBaseten("/v1/chat/completions - openai-only fields are dropped - baseten/zai-org/glm-5.3"), "baseten");
  assert.strictEqual(providerOfFolder("61. Hugging Face Baseten model discovery (#6631)", withBaseten), "huggingface");
});

test("Nace SDK routes are attributed to Nace rather than their ingress provider", () => {
  const matchNace = makeMatchProvider((p) => ["nace", "openai"].includes(p));
  const folder = "Nace native decisions - #9739";
  const row = { name: "nace drex-max", request: { url: "http://localhost:8080/openai/v1/decisions" } };
  assert.strictEqual(providerOfItem(row, [folder], matchNace), "nace");
  assert.strictEqual(providerOfLogLine("post http://localhost:8080/openai/v1/decisions [200 ok]", folder, matchNace), "nace");
});

test("Nace is matched as a whole word, not inside prompt words", () => {
  const match = makeMatchProvider((p) => ["nace", "openai"].includes(p));
  assert.strictEqual(match("explain how a furnace works gpt-4o"), "openai");
  assert.strictEqual(match("a menace on /openai/v1/chat/completions"), "openai");
  assert.strictEqual(match("model nace/drex-v2.0"), "nace");
  assert.strictEqual(match("https://console.nace.ai/v1/systemone"), "nace");
  assert.strictEqual(match("drex-max-v1.1"), "nace");
});

test("prompt prose and test scripts do not claim Nace attribution", () => {
  const withNace = makeMatchProvider((p) => ["nace", "openai"].includes(p));
  const row = (model) => ({
    name: "decision through the unified endpoint",
    request: {
      url: "{{baseUrl}}/v1/decisions",
      body: { mode: "raw", raw: JSON.stringify({ model, input: "Compare Nace and OpenAI, including drex-v2.0." }) },
    },
    event: [{ listen: "test", script: { exec: ["// Assert the Nace-shaped fields."] } }],
  });
  assert.strictEqual(providerOfItem(row("openai/gpt-4o"), ["Unified decision requests"], withNace), "openai");
  assert.strictEqual(providerOfItem(row("nace/drex-max-v1.1"), ["Unified decision requests"], withNace), "nace");
});

test("root and neutral-folder Nace rows agree with logged requests on OpenAI ingress", () => {
  const withNace = makeMatchProvider((p) => ["nace", "openai"].includes(p));
  const row = { name: "nace/drex-max-v1.1 image decision", request: { url: "{{baseUrl}}/openai/v1/decisions" } };
  for (const folder of ["", "Decision requests"]) {
    const denominator = providerOfItem(row, folder ? [folder] : [], withNace);
    const numerator = providerOfLogLine("POST http://localhost:8080/openai/v1/decisions [200 OK]", folder, withNace, row.name);
    assert.strictEqual(denominator, "nace");
    assert.strictEqual(numerator, denominator);
  }
});

test("the sequential monitor counts root Nace and OpenAI requests separately", () => {
  const dir = mkdtempSync(join(tmpdir(), "nace-attribution-"));
  try {
    const collection = join(dir, "collection.json");
    const log = join(dir, "newman.log");
    writeFileSync(collection, JSON.stringify({ item: [
      { name: "nace/drex-max-v1.1 decision", request: { url: "{{baseUrl}}/openai/v1/decisions" } },
      { name: "openai/gpt-4o chat", request: { url: "{{baseUrl}}/openai/v1/chat/completions" } },
    ] }));
    writeFileSync(log,
      "↳ nace/drex-max-v1.1 decision\nPOST http://localhost:8080/openai/v1/decisions [200 OK, 1kB, 1ms]\n" +
      "↳ openai/gpt-4o chat\nPOST http://localhost:8080/openai/v1/chat/completions [200 OK, 1kB, 1ms]\n");
    const out = execFileSync(process.execPath, [
      fileURLToPath(new URL("../harness-monitor.mjs", import.meta.url)),
      "--ci", "--mode", "sequential", "--providers", "nace openai", "--tmp-dir", dir,
      "--collection", collection, "--log", log,
    ], { encoding: "utf8", timeout: 10000 });
    for (const provider of ["nace", "openai"]) {
      assert.match(out, new RegExp(`│\\s*[^│]*│\\s*${provider}\\s*│\\s*1\\s*│\\s*1\\s*│\\s*0\\s*│`));
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

console.log(`\n${passed} passed`);
