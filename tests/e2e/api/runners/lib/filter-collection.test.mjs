// Guards provider partitions where a model id names another vendor.
//
// MachGen rows carry the original vendor in the model id ("machgen/MiniMax-H3",
// "machgen/FLUX.2-dev"), so the minimax and replicate keyword partitions would claim
// them too and send those runs to MachGen without its key. filter-collection.mjs routes
// them exclusively to machgen; this runs the real augment + filter steps and checks
// every partition, so a keyword change that breaks that routing fails here.
//
// No network, no Bifrost, no credentials. Run directly: `node filter-collection.test.mjs`.
import assert from "node:assert";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

let passed = 0;
// Runs one check and counts it; a failed assertion throws and stops the file.
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "../../../../..");
const SOURCE = join(REPO, "tests/e2e/api/collections/provider-harness.json");
const AUGMENT = join(REPO, "tests/e2e/api/runners/augment-provider-harness.mjs");
const FILTER = join(REPO, "tests/e2e/api/runners/filter-collection.mjs");

// Lists every request in a collection as "folder / name".
const rowNames = (collection) => {
  const names = [];
  const walk = (items, folder = "") => {
    for (const it of items) {
      if (Array.isArray(it.item)) walk(it.item, it.name);
      else names.push(`${folder} / ${it.name}`);
    }
  };
  walk(collection.item || []);
  return names;
};

const scratch = mkdtempSync(join(tmpdir(), "filter-collection-"));
const partitions = {};
let machgenRows;
try {
  const augmented = join(scratch, "augmented.json");
  execFileSync("node", [AUGMENT, "--source", SOURCE, "--out", augmented], { stdio: "pipe" });
  machgenRows = rowNames(JSON.parse(readFileSync(augmented, "utf8"))).filter((n) => n.includes("MachGen Provider Coverage"));
  for (const provider of ["machgen", "minimax", "replicate", "runware"]) {
    const out = join(scratch, `${provider}.json`);
    execFileSync("node", [FILTER, "--source", augmented, "--out", out, "--provider", provider], { stdio: "pipe" });
    partitions[provider] = rowNames(JSON.parse(readFileSync(out, "utf8")));
  }
} finally {
  rmSync(scratch, { recursive: true, force: true });
}

test("the harness has MachGen rows to route", () => {
  assert.ok(machgenRows.length > 0, "no rows found in the MachGen Provider Coverage folder");
});

test("the machgen partition selects every MachGen row", () => {
  const missing = machgenRows.filter((n) => !partitions.machgen.includes(n));
  assert.deepEqual(missing, [], "MachGen rows dropped from the machgen partition");
});

test("MachGen rows stay out of the partitions their model ids name", () => {
  for (const provider of ["minimax", "replicate", "runware"]) {
    const leaked = machgenRows.filter((n) => partitions[provider].includes(n));
    assert.deepEqual(leaked, [], `MachGen rows claimed by the ${provider} partition`);
  }
});

console.log(`${passed} passed`);
