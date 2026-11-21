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
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
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

// A Files API call on the OpenAI-compatible route names its real provider in a "provider" form
// field (upload) or query parameter (delete). The URL still contains "/openai", so without reading
// the declared provider an openai run would claim the Azure pair too: it runs twice, and the openai
// fork needs Azure credentials. The delete consumes the upload's id, so if it stayed in the openai
// partition, chain expansion would pull the upload back in with it.
const declaredProviderFixture = {
  info: { name: "declared provider fixture", schema: "https://schema.getpostman.com/json/collection/v2.1.0/collection.json" },
  item: [
    {
      name: "Files on the OpenAI-compatible route",
      item: [
        {
          name: "upload a file to the openai route",
          event: [{ listen: "test", script: { exec: ["pm.collectionVariables.set('fxOpenaiFileId', pm.response.json().id);"] } }],
          request: { method: "POST", url: { raw: "{{baseUrl}}/openai/v1/files" }, body: { mode: "formdata", formdata: [{ key: "purpose", value: "user_data", type: "text" }] } },
        },
        {
          name: "upload a file to azure through the openai route",
          event: [{ listen: "test", script: { exec: ["pm.collectionVariables.set('fxAzureFileId', pm.response.json().id);"] } }],
          request: {
            method: "POST",
            url: { raw: "{{baseUrl}}/openai/v1/files" },
            body: { mode: "formdata", formdata: [{ key: "purpose", value: "user_data", type: "text" }, { key: "provider", value: "azure", type: "text" }] },
          },
        },
        {
          name: "delete the azure file through the openai route",
          request: {
            method: "DELETE",
            url: { raw: "{{baseUrl}}/openai/v1/files/{{fxAzureFileId}}?provider=azure", query: [{ key: "provider", value: "azure" }] },
          },
        },
      ],
    },
  ],
};

const declaredPartitions = {};
const declaredScratch = mkdtempSync(join(tmpdir(), "filter-collection-declared-"));
try {
  const source = join(declaredScratch, "fixture.json");
  writeFileSync(source, JSON.stringify(declaredProviderFixture));
  for (const provider of ["openai", "azure"]) {
    const out = join(declaredScratch, `${provider}.json`);
    execFileSync("node", [FILTER, "--source", source, "--out", out, "--provider", provider], { stdio: "pipe" });
    declaredPartitions[provider] = rowNames(JSON.parse(readFileSync(out, "utf8"))).map((n) => n.split(" / ")[1]);
  }
} finally {
  rmSync(declaredScratch, { recursive: true, force: true });
}

test("a declared provider puts an upload and its delete only in that provider's partition", () => {
  const azurePair = ["upload a file to azure through the openai route", "delete the azure file through the openai route"];
  assert.deepEqual(declaredPartitions.azure, azurePair, "the azure partition must hold the azure upload and its delete");
  assert.deepEqual(declaredPartitions.openai, ["upload a file to the openai route"], "the openai partition must not claim the azure pair");
});

// The Interactions folders mix three kinds of rows: native Gemini creates on /genai, translated creates
// whose raw model names openai/, and a Gemini turn sent through the OpenAI SDK route. Each must land in
// the partition of the provider it reaches, and a filtered run must keep the cleanup step that deletes
// what its producer stored, unless the caller excluded cleanup outright.
const interactionsScratch = mkdtempSync(join(tmpdir(), "filter-collection-interactions-"));
const interactions = {};
try {
  const augmented = join(interactionsScratch, "augmented.json");
  execFileSync("node", [AUGMENT, "--source", SOURCE, "--out", augmented], { stdio: "pipe" });
  const run = (label, args) => {
    const out = join(interactionsScratch, `${label}.json`);
    execFileSync("node", [FILTER, "--source", augmented, "--out", out, ...args], { stdio: "pipe" });
    interactions[label] = rowNames(JSON.parse(readFileSync(out, "utf8"))).map((n) => n.split(" / ")[1].split(" ")[0]);
  };
  run("gemini", ["--provider", "gemini"]);
  run("openai", ["--provider", "openai"]);
  run("cancel", ["--feature", "372.10 gemini"]);
  run("cancelNoCleanup", ["--feature", "372.10 gemini", "--exclude-feature-any", "interactions cleanup:"]);
} finally {
  rmSync(interactionsScratch, { recursive: true, force: true });
}

test("translated Interactions creates run only in the openai partition", () => {
  for (const id of ["372.13", "372.14", "372.15", "372.21"]) {
    assert.ok(interactions.openai.includes(id), `${id} missing from the openai partition`);
    assert.ok(!interactions.gemini.includes(id), `${id} leaked into the gemini partition`);
  }
});

test("a Gemini turn on the OpenAI SDK route runs only in the gemini partition", () => {
  assert.ok(interactions.gemini.includes("373.16"), "373.16 missing from the gemini partition");
  assert.ok(!interactions.openai.includes("373.16"), "373.16 leaked into the openai partition");
});

test("a filtered Interactions run keeps the cleanup of what its producer stored", () => {
  assert.ok(interactions.cancel.includes("372.9"), "the producer is kept");
  assert.ok(interactions.cancel.includes("372.19"), "the cleanup that deletes the producer's run is kept");
});

test("an excluded cleanup step is not added back", () => {
  assert.ok(interactions.cancelNoCleanup.includes("372.10"), "the selected case is kept");
  assert.ok(!interactions.cancelNoCleanup.includes("372.19"), "--exclude-feature-any must drop the cleanup step");
});

console.log(`${passed} passed`);
