// Unit tests for withSetupToken. Run directly: `node setup-token.test.mjs`.
import assert from "node:assert";
import { SETUP_TOKEN_HEADER, withSetupToken } from "./setup-token.mjs";

let passed = 0;
function test(name, fn) {
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

const env = { BIFROST_SETUP_TOKEN: "tok" };

test("adds the header to management API paths", () => {
  assert.deepStrictEqual(withSetupToken({ a: "1" }, "/api/providers", env), { a: "1", [SETUP_TOKEN_HEADER]: "tok" });
});
test("leaves inference and other paths alone", () => {
  assert.deepStrictEqual(withSetupToken({}, "/v1/chat/completions", env), {});
  assert.deepStrictEqual(withSetupToken({}, "/health", env), {});
});
test("does nothing without a token", () => {
  assert.deepStrictEqual(withSetupToken({}, "/api/providers", {}), {});
});
test("never overrides a header the caller set", () => {
  const h = { "x-bifrost-setup-token": "explicit" };
  assert.strictEqual(withSetupToken(h, "/api/providers", env), h);
});
test("does not modify the input", () => {
  const h = {};
  withSetupToken(h, "/api/config", env);
  assert.deepStrictEqual(h, {});
});

console.log(`setup-token: ${passed} passed`);
