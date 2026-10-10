// Offline reachability checks for the checked-in provider harness.
// A request folder under `event` is valid JSON but is invisible to the runner's
// item traversal. Count executable items, not serialized "request" keys.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { after, test } from "node:test";
import { fileURLToPath } from "node:url";
import { walkRequests } from "./chained-vars.mjs";

const REPO = resolve(dirname(fileURLToPath(import.meta.url)), "../../../../..");
const SOURCE = join(REPO, "tests/e2e/api/collections/provider-harness.json");
const collection = JSON.parse(readFileSync(SOURCE, "utf8"));
const keyword = "tool-result cache breakpoint";
const routes = [
  "{{baseUrl}}/v1/responses",
  "{{baseUrl}}/anthropic/v1/messages",
  "{{baseUrl}}/v1/chat/completions",
];
const scratch = mkdtempSync(join(tmpdir(), "provider-harness-reachability-"));
after(() => rmSync(scratch, { recursive: true, force: true }));

function assertToolResultCases(value) {
  const items = walkRequests(value.item).filter(({ item }) => item.name.includes(keyword));
  assert.deepEqual(items.map(({ item }) => item.request.url.raw), routes,
    "all three tool-result cache regression routes must be reachable once and in order");
}

test("collection events contain scripts instead of misplaced request folders", () => {
  for (const [index, event] of collection.event.entries()) {
    assert.equal(typeof event.listen, "string", `collection.event[${index}] is not a script event`);
    assert.equal(typeof event.script, "object", `collection.event[${index}] has no script`);
    assert.ok(!event.item && !event.request, `collection.event[${index}] contains requests outside the item tree`);
  }
});

test("the source item tree exposes all three tool-result cache cases", () => {
  assertToolResultCases(collection);
});

let augmented;
function augmentedSource() {
  if (!augmented) {
    const output = join(scratch, "augmented.json");
    execFileSync(process.execPath, [
      join(REPO, "tests/e2e/api/runners/augment-provider-harness.mjs"),
      "--source", SOURCE, "--out", output,
    ], { encoding: "utf8" });
    augmented = output;
  }
  return augmented;
}

for (const [label, flags] of [
  ["feature", ["--feature", keyword]],
  ["provider and feature", ["--provider", "openai", "--feature", keyword]],
  ["folder", ["--folder", "Tool-result cache marker survives translation"]],
]) {
  test(`augment and ${label} filtering retain the three executable tool-result cases`, () => {
    const output = join(scratch, `${label.replaceAll(" ", "-")}.json`);
    execFileSync(process.execPath, [
      join(REPO, "tests/e2e/api/runners/filter-collection.mjs"),
      "--source", augmentedSource(), "--out", output, ...flags,
    ], { encoding: "utf8" });
    assertToolResultCases(JSON.parse(readFileSync(output, "utf8")));
  });
}
