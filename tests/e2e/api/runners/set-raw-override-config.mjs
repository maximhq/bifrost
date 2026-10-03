#!/usr/bin/env node
// Ensures client_config.allow_per_request_raw_override on the target gateway before a
// harness run. Without it core/bifrost.go ignores x-bf-send-back-raw-request/-response,
// so every row that asserts on extra_fields.raw_request fails with "raw_request missing"
// instead of naming the real defect. The harness config.json already enables it for a
// gateway the runner starts itself; this covers a gateway that was already running.

const baseURL = (process.env.BIFROST_E2E_BASE_URL || process.env.BIFROST_BASE_URL || "http://localhost:8080").replace(/\/+$/, "");
const mode = process.argv[2];
const authHeader = process.env.BIFROST_E2E_AUTH_HEADER || "";

if (mode !== "enable" && mode !== "disable") {
  console.error("Usage: set-raw-override-config.mjs <enable|disable>");
  process.exit(1);
}

async function request(method, path, body) {
  const headers = { "content-type": "application/json" };
  if (authHeader) {
    headers.Authorization = authHeader;
  }
  const res = await fetch(`${baseURL}${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  if (!res.ok) {
    throw new Error(`${method} ${path} failed with ${res.status}: ${text}`);
  }
  return text ? JSON.parse(text) : null;
}

const current = await request("GET", "/api/config");
const want = mode === "enable";

if (!current.client_config) {
  throw new Error(`GET /api/config returned no client_config: ${JSON.stringify(current).slice(0, 300)}`);
}

if (!!current.client_config.allow_per_request_raw_override === want) {
  console.log(`allow_per_request_raw_override already ${mode}d at ${baseURL}`);
  process.exit(0);
}

// The server reports log_retention_days:0 by default but rejects that on write
// (it must be >= 1), so a straight round-trip of client_config would 400.
if (!(current.client_config.log_retention_days >= 1)) {
  current.client_config.log_retention_days = 1;
}
current.client_config.allow_per_request_raw_override = want;

// auth_config is omitted on purpose: the handler takes it as a pointer and leaves
// auth untouched when it is absent, so this never has to resend admin credentials.
await request("PUT", "/api/config", {
  client_config: current.client_config,
  framework_config: current.framework_config,
});

console.log(`allow_per_request_raw_override ${mode}d at ${baseURL}`);
