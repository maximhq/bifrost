#!/usr/bin/env node

import fs from "node:fs";

const [, , sourcePath, outPath] = process.argv;

if (!sourcePath || !outPath) {
  console.error("Usage: add-auth-header.mjs <source-collection> <out-collection>");
  process.exit(1);
}

const collection = JSON.parse(fs.readFileSync(sourcePath, "utf8"));
const authScript = [
  "const adminAuthHeader = pm.variables.get('admin_auth_header') || pm.environment.get('admin_auth_header');",
  "if (adminAuthHeader) {",
  "  pm.request.headers.upsert({ key: 'Authorization', value: adminAuthHeader });",
  "}",
];

collection.event = Array.isArray(collection.event) ? collection.event : [];
collection.event.push({
  listen: "prerequest",
  script: {
    type: "text/javascript",
    exec: authScript,
  },
});

fs.writeFileSync(outPath, `${JSON.stringify(collection, null, 2)}\n`);
