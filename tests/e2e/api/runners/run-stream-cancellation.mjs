#!/usr/bin/env node
// Exercises Bifrost's server-side stream cancellation path by opening a stream,
// reading the first bytes, then aborting the downstream request.

import { writeFileSync } from "node:fs";

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, cur, i, arr) => {
    if (cur.startsWith("--")) {
      const key = cur.slice(2);
      const next = arr[i + 1];
      acc.push([key, next && !next.startsWith("--") ? next : "true"]);
    }
    return acc;
  }, [])
);

const baseUrl = args["base-url"] || process.env.BASE_URL || "http://localhost:8080";
const providerFilter = (args.provider || "").toLowerCase();
const out = args.out || "tmp/stream-cancel-report.json";
const readTimeoutMs = Number(args["read-timeout-ms"] || 20000);
const abortAfterBytes = Number(args["abort-after-bytes"] || 1);

const cases = [
  {
    provider: "openai",
    name: "openai/gpt-4o-mini chat stream",
    path: "/v1/chat/completions",
    body: {
      model: "openai/gpt-4o-mini",
      messages: [{ role: "user", content: "Count from 1 to 100 slowly." }],
      stream: true,
    },
  },
  {
    provider: "anthropic",
    name: "anthropic/claude-haiku-4-5 chat stream",
    path: "/v1/chat/completions",
    body: {
      model: "anthropic/claude-haiku-4-5",
      messages: [{ role: "user", content: "Count from 1 to 100 slowly." }],
      stream: true,
      max_tokens: 512,
    },
  },
  {
    provider: "bedrock",
    name: "bedrock/nova-lite chat stream",
    path: "/v1/chat/completions",
    body: {
      model: "bedrock/us.amazon.nova-lite-v1:0",
      messages: [{ role: "user", content: "Count from 1 to 100 slowly." }],
      stream: true,
    },
  },
  {
    provider: "gemini",
    name: "gemini/gemini-2.5-flash chat stream",
    path: "/v1/chat/completions",
    body: {
      model: "gemini/gemini-2.5-flash",
      messages: [{ role: "user", content: "Count from 1 to 100 slowly." }],
      stream: true,
    },
  },
  {
    provider: "vertex",
    name: "vertex/gemini-2.5-flash chat stream",
    path: "/v1/chat/completions",
    body: {
      model: "vertex/gemini-2.5-flash",
      messages: [{ role: "user", content: "Count from 1 to 100 slowly." }],
      stream: true,
    },
  },
  {
    provider: "azure",
    name: "azure deployment chat stream",
    path: "/v1/chat/completions",
    body: {
      model: "azure/{{azureDeployment}}",
      messages: [{ role: "user", content: "Count from 1 to 100 slowly." }],
      stream: true,
    },
  },
].filter((c) => !providerFilter || c.provider === providerFilter);

const resolveVariables = (value) => {
  if (typeof value === "string") {
    return value.replaceAll("{{azureDeployment}}", process.env.AZURE_DEPLOYMENT || process.env.BIFROST_AZURE_DEPLOYMENT || "gpt-4o-mini");
  }
  if (Array.isArray(value)) return value.map(resolveVariables);
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, resolveVariables(v)]));
  }
  return value;
};

async function runCase(testCase) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(new Error("stream read timeout")), readTimeoutMs);
  let bytesRead = 0;

  try {
    const response = await fetch(`${baseUrl}${testCase.path}`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(resolveVariables(testCase.body)),
      signal: controller.signal,
    });

    const contentType = response.headers.get("content-type") || "";
    if (!response.ok) {
      const body = await response.text().catch(() => "");
      return {
        ...testCase,
        ok: false,
        status: response.status,
        contentType,
        error: `stream returned HTTP ${response.status}: ${body.slice(0, 500)}`,
      };
    }
    if (!/event-stream|vnd\.amazon\.eventstream|octet-stream/i.test(contentType)) {
      return {
        ...testCase,
        ok: false,
        status: response.status,
        contentType,
        error: `expected streaming content-type, got ${contentType || "<empty>"}`,
      };
    }
    if (!response.body) {
      return { ...testCase, ok: false, status: response.status, contentType, error: "response body is not readable" };
    }

    const reader = response.body.getReader();
    while (bytesRead < abortAfterBytes) {
      const { done, value } = await reader.read();
      if (done) break;
      bytesRead += value?.byteLength || 0;
    }

    controller.abort(new Error("intentional downstream abort after first stream bytes"));
    await reader.cancel("intentional downstream abort").catch(() => {});

    return {
      ...testCase,
      ok: bytesRead > 0,
      status: response.status,
      contentType,
      bytesRead,
      aborted: true,
      error: bytesRead > 0 ? undefined : "stream ended before any bytes were read",
    };
  } catch (err) {
    return {
      ...testCase,
      ok: false,
      bytesRead,
      aborted: controller.signal.aborted,
      error: err?.message || String(err),
    };
  } finally {
    clearTimeout(timer);
  }
}

if (cases.length === 0) {
  console.error(`[stream-cancel] no cases selected for provider=${providerFilter || "-"}`);
  process.exit(2);
}

const results = [];
for (const testCase of cases) {
  process.stderr.write(`[stream-cancel] ${testCase.name} ... `);
  const result = await runCase(testCase);
  results.push(result);
  process.stderr.write(result.ok ? "ok\n" : `failed (${result.error})\n`);
}

const report = {
  baseUrl,
  provider: providerFilter || null,
  total: results.length,
  failed: results.filter((r) => !r.ok).length,
  results,
};

writeFileSync(out, `${JSON.stringify(report, null, 2)}\n`);

if (report.failed > 0) {
  console.error(`[stream-cancel] ${report.failed}/${report.total} case(s) failed; report: ${out}`);
  process.exit(1);
}

console.error(`[stream-cancel] all ${report.total} case(s) passed; report: ${out}`);
