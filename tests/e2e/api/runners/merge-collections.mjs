#!/usr/bin/env node
// Merges extension Postman collections into a base API e2e collection.

import { readFileSync, writeFileSync } from "node:fs";

const args = process.argv.slice(2);
const extras = [];
let source = "";
let out = "";

for (let i = 0; i < args.length; i += 1) {
  const arg = args[i];
  if (arg === "--source") {
    source = args[++i] || "";
  } else if (arg === "--out") {
    out = args[++i] || "";
  } else if (arg === "--extra") {
    extras.push(args[++i] || "");
  } else {
    console.error(`[merge-collections] unknown argument: ${arg}`);
    process.exit(2);
  }
}

if (!source || !out || extras.length === 0 || extras.some((extra) => !extra)) {
  console.error("[merge-collections] --source, --out, and at least one --extra are required");
  process.exit(2);
}

const normalizeItems = (extension, path) => {
  if (Array.isArray(extension)) return extension;
  if (Array.isArray(extension.item)) return extension.item;
  if (extension.request || extension.item) return [extension];
  console.error(`[merge-collections] extension ${path} did not contain any Postman items`);
  process.exit(1);
};

const base = JSON.parse(readFileSync(source, "utf8"));
if (base.item == null) {
  base.item = [];
} else if (!Array.isArray(base.item)) {
  console.error(`[merge-collections] base collection ${source} has non-array "item"`);
  process.exit(1);
}
let requestCount = 0;

for (const extra of extras) {
  const extension = JSON.parse(readFileSync(extra, "utf8"));
  const items = normalizeItems(extension, extra);
  requestCount += JSON.stringify(items).match(/"request":/g)?.length || 0;
  base.item.push({
    name: extension.info?.name || extension.name || "External API E2E Extension",
    description: `Loaded from ${extra}. This folder is only present when the API runner receives --extra-collection.`,
    item: items,
  });
}

writeFileSync(out, `${JSON.stringify(base, null, 2)}\n`);
console.error(`[merge-collections] wrote ${out} with ${requestCount} extension requests`);
