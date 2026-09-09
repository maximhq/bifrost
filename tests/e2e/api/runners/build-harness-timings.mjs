#!/usr/bin/env node
// Builds the per-request timing table that the sub-shard planner and slicer run on.
//
// Reads every newman JSON report a sweep left behind and writes {"<request name>": <median ms>}.
// filter-collection.mjs --timings consumes it in two places: --plan sizes each (provider, class)
// cell from it, and --shard k/n fills each slice from it (see lib/shard-cost.mjs for why both).
//
// Usage:
//   node build-harness-timings.mjs --dir tmp --out tmp/harness-timings.json
//   node build-harness-timings.mjs --dir tmp --out tmp/harness-timings.json --prior tests/e2e/api/collections/harness-timings.json
//
// Run at the END of a sweep, after the reports are merged. Never fails the run: a sweep that
// already produced its results must not go red because a cache file could not be refreshed.
import { existsSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { aggregateTimings } from "./lib/shard-cost.mjs";

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, cur, i, arr) => {
    if (cur.startsWith("--")) {
      const next = arr[i + 1];
      acc.push([cur.slice(2), next && !next.startsWith("--") ? next : "true"]);
    }
    return acc;
  }, [])
);

const DIR = args.dir || "tmp";
const OUT = args.out || "tmp/harness-timings.json";
// Where to merge onto. Defaults to OUT itself so repeated sweeps accumulate: a run scoped with
// PROVIDER=anthropic measures one provider, and without the merge it would erase the other seven
// and send the next full sweep back to round-robin for most of the grid.
const PRIOR = args.prior || OUT;

const die = (msg) => {
  console.error(`[build-harness-timings] ${msg}`);
  process.exit(0); // 0 on purpose - see the header.
};

if (!existsSync(DIR)) die(`report directory ${DIR} does not exist - nothing to measure`);

// Every per-shard report plus the merged one. Reading the merged report as well as its parts is
// harmless: aggregateTimings medians the samples per name, and a duplicated sample cannot move a
// median off a value it already sat on.
const files = readdirSync(DIR).filter((f) => /^newman-report.*\.json$/.test(f));
if (!files.length) die(`no newman-report*.json files in ${DIR} - nothing to measure`);

const reports = [];
let unreadable = 0;
for (const f of files) {
  try {
    reports.push(JSON.parse(readFileSync(join(DIR, f), "utf8")));
  } catch {
    // A shard killed mid-write leaves a truncated report. One bad file is not a reason to discard
    // the other 140.
    unreadable++;
  }
}

let prior = {};
if (PRIOR && existsSync(PRIOR)) {
  try {
    const raw = JSON.parse(readFileSync(PRIOR, "utf8"));
    if (raw && typeof raw === "object") prior = raw;
  } catch {
    console.error(`[build-harness-timings] prior table at ${PRIOR} is unreadable - starting fresh`);
  }
}

const table = aggregateTimings(reports, prior);
const measured = Object.keys(table).length;
if (!measured) die("no usable executions found - leaving the existing table alone");

const fresh = measured - Object.keys(prior).length;
try {
  // Sorted keys so a committed baseline produces a reviewable diff rather than a reshuffle.
  const sorted = Object.fromEntries(Object.keys(table).sort().map((k) => [k, table[k]]));
  writeFileSync(OUT, `${JSON.stringify(sorted, null, 2)}\n`);
} catch (e) {
  die(`could not write ${OUT}: ${e.message}`);
}

const totalSec = Math.round(Object.values(table).reduce((s, ms) => s + ms, 0) / 1000);
console.error(
  `[build-harness-timings] wrote ${OUT}: ${measured} request(s), ${fresh > 0 ? `+${fresh} new, ` : ""}${totalSec}s of measured request time, from ${reports.length} report(s)${unreadable ? ` (${unreadable} unreadable)` : ""}`
);
