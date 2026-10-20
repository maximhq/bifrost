#!/usr/bin/env node

// Adds a collection-level prerequest that sends the OSS setup token on every
// request. While dashboard auth is not active, the OSS gateway locks /api behind
// X-Bifrost-Setup-Token, so the unauthenticated pass of a collection needs it.
// The value comes from the `setup_token` variable (pass it with
// `--env-var setup_token=...`); when unset, no header is added.
//
// Requests that must reach the gateway without the header (for example a case
// asserting the lockout itself) call
// pm.request.headers.remove('X-Bifrost-Setup-Token') in their own prerequest,
// which runs after this collection-level one.

import fs from "node:fs";

const [, , sourcePath, outPath] = process.argv;

if (!sourcePath || !outPath) {
  console.error("Usage: add-setup-token-header.mjs <source-collection> <out-collection>");
  process.exit(1);
}

const collection = JSON.parse(fs.readFileSync(sourcePath, "utf8"));
const setupTokenScript = [
  "const setupToken = pm.variables.get('setup_token') || pm.environment.get('setup_token');",
  "if (setupToken) {",
  "  pm.request.headers.upsert({ key: 'X-Bifrost-Setup-Token', value: setupToken });",
  "}",
];

collection.event = Array.isArray(collection.event) ? collection.event : [];
// Prepend so request-level prerequests (which run after collection-level ones)
// can still remove the header for lockout cases.
collection.event.unshift({
  listen: "prerequest",
  script: {
    type: "text/javascript",
    exec: setupTokenScript,
  },
});

fs.writeFileSync(outPath, `${JSON.stringify(collection, null, 2)}\n`);
