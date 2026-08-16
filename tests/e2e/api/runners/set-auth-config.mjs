#!/usr/bin/env node

const baseURL = (process.env.BIFROST_E2E_BASE_URL || process.env.BIFROST_BASE_URL || "http://localhost:8080").replace(/\/+$/, "");
const mode = process.argv[2];
const username = process.env.BIFROST_E2E_ADMIN_USERNAME || "admin";
const password = process.env.BIFROST_E2E_ADMIN_PASSWORD || "Bifrost-E2E-Admin-Pass1!";
const authHeader = process.env.BIFROST_E2E_AUTH_HEADER || "";
// Creating the very first admin account is the one config write the server
// accepts while unauthenticated, and it demands the operator's bootstrap token.
// The server resolves that token at boot from config.json `setup_token` or
// BIFROST_SETUP_TOKEN, so it has to be set on the server process too — passing
// it here alone is not enough.
const setupToken = (process.env.BIFROST_E2E_SETUP_TOKEN || process.env.BIFROST_SETUP_TOKEN || "").trim();

// Exit code signalling "auth pass not possible here", distinct from a real failure.
const EXIT_SKIP = 3;

if (mode !== "enable" && mode !== "disable") {
  console.error("Usage: set-auth-config.mjs <enable|disable>");
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
  let json = null;
  if (text) {
    try {
      json = JSON.parse(text);
    } catch {
      json = null;
    }
  }
  if (!res.ok) {
    throw new Error(`${method} ${path} failed with ${res.status}: ${text}`);
  }
  return json;
}

const current = await request("GET", "/api/config");

// The handler omits auth_config entirely until the first admin account exists —
// that absence is the documented first-time-setup signal, so it doubles as the
// "do we still need the bootstrap token?" check.
const adminExists = !!current.auth_config;

if (mode === "enable" && !adminExists && !setupToken) {
  console.error(
    `Skipping auth pass: ${baseURL} has no admin account yet and no bootstrap token is available.\n` +
      "  Start the server with BIFROST_SETUP_TOKEN=<token> (or setup_token in config.json),\n" +
      "  then re-run with the same BIFROST_SETUP_TOKEN set for this script."
  );
  process.exit(EXIT_SKIP);
}

const authConfig = mode === "enable"
  ? {
      is_enabled: true,
      admin_username: username,
      admin_password: password,
    }
  : {
      is_enabled: false,
      admin_username: username,
      admin_password: "<redacted>",
    };

// Only meaningful while bootstrapping; the server ignores it once an admin
// exists, and it is never persisted.
if (mode === "enable" && setupToken) {
  authConfig.setup_token = setupToken;
}

await request("PUT", "/api/config", {
  client_config: current.client_config,
  framework_config: current.framework_config,
  auth_config: authConfig,
});

console.log(`Auth ${mode}d at ${baseURL}`);
