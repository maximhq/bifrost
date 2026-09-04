// Resolves {{...}} placeholders in a stream-cancellation test case payload.
//
// Split out of run-stream-cancellation.mjs so it can be unit tested: the runner
// has top-level side effects (arg parsing, the case loop, process.exit) and
// cannot be imported from a test.

// Objects are rebuilt by assigning keys in source order rather than through
// Object.fromEntries. AGENTS.md's "E2E Tests (tests/e2e/)" section forbids
// marshalling API payloads through an intermediate map, because field order
// matters for backend validation and snapshot comparison.
export function resolveVariables(value) {
  if (typeof value === "string") {
    return value.replaceAll(
      "{{azureDeployment}}",
      process.env.AZURE_DEPLOYMENT || process.env.BIFROST_AZURE_DEPLOYMENT || "gpt-4o-mini",
    );
  }
  if (Array.isArray(value)) return value.map(resolveVariables);
  if (value && typeof value === "object") {
    const resolved = {};
    for (const key of Object.keys(value)) {
      resolved[key] = resolveVariables(value[key]);
    }
    return resolved;
  }
  return value;
}
