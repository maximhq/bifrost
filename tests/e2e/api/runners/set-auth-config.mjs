#!/usr/bin/env node

const baseURL = (process.env.BIFROST_E2E_BASE_URL || process.env.BIFROST_BASE_URL || "http://localhost:8080").replace(/\/+$/, "");
const mode = process.argv[2];
const username = process.env.BIFROST_E2E_ADMIN_USERNAME || "admin";
const password = process.env.BIFROST_E2E_ADMIN_PASSWORD || "Bifrost-E2E-Admin-Pass1!";
const authHeader = process.env.BIFROST_E2E_AUTH_HEADER || "";

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
await request("PUT", "/api/config", {
  client_config: current.client_config,
  framework_config: current.framework_config,
  auth_config: mode === "enable"
    ? {
        is_enabled: true,
        admin_username: username,
        admin_password: password,
      }
    : {
        is_enabled: false,
        admin_username: username,
        admin_password: "<redacted>",
      },
});

console.log(`Auth ${mode}d at ${baseURL}`);
