// Unit tests for the relay permutation tables and the pricing reference. Run directly:
// `node relay-matrix.test.mjs`. No network, no Bifrost.
//
// The expected figures are worked out by hand from docs/relays/pricing.mdx, not derived from
// expectedUsage, so a change to the reference has to change both places on purpose.
import assert from "node:assert";
import {
	billingCases,
	byteLength,
	closeTo,
	effectiveBillable,
	expectedStreamed,
	expectedStreamUsage,
	expectedUsage,
	globalProxyForInference,
	globalProxyRestorable,
	KNOWN_GAPS,
	pricingCases,
	pricingRelayConfig,
	pricingRequestBody,
	pricingResponseBody,
	resolveUsage,
	routeMatchCases,
	runeCount,
	streamingCases,
	verdict,
} from "./relay-matrix.mjs";

let passed = 0;
function test(name, fn) {
	fn();
	passed++;
	console.log(`  ok - ${name}`);
}

const req = pricingRequestBody("m-1");
const resNone = pricingResponseBody({ costShape: "none", tokens: "none" });

test("the request and response texts differ in bytes and characters", () => {
	assert.strictEqual(runeCount("héllo wörld ✓"), 13);
	assert.strictEqual(byteLength("héllo wörld ✓"), 17);
	assert.strictEqual(runeCount("résumé ünïcödé ✓✓"), 17);
	assert.strictEqual(byteLength("résumé ünïcödé ✓✓"), 27);
});

test("response value: 13 units at 0.01 plus a 0.002 fee", () => {
	const u = resolveUsage({ units_source: "response", units_measure: "value", units_path: "usage.n", cost_per_unit: 0.01, cost_per_request: 0.002 });
	const got = expectedUsage({ usage: u, billable: true, status: 200, requestBody: req, responseBody: resNone });
	assert.ok(closeTo(got.cost, 0.132));
	assert.strictEqual(got.units, 13);
	assert.strictEqual(got.billedUnits, 13);
});

test("request chars: 13 characters, blocks of 4 round up to 4 blocks", () => {
	const u = resolveUsage({ units_source: "request", units_measure: "chars", units_path: "text", cost_per_unit: 0.01, units_block_size: 4 });
	const got = expectedUsage({ usage: u, billable: true, status: 200, requestBody: req, responseBody: resNone });
	assert.strictEqual(got.units, 13);
	assert.strictEqual(got.billedUnits, 4);
	assert.ok(closeTo(got.cost, 0.04));
});

test("a minimum lifts a small quantity: 7 request units billed as 25", () => {
	const u = resolveUsage({ units_source: "request", units_measure: "value", units_path: "n", cost_per_unit: 0.01, units_minimum: 25 });
	const got = expectedUsage({ usage: u, billable: true, status: 200, requestBody: req, responseBody: resNone });
	assert.strictEqual(got.billedUnits, 25);
	assert.ok(closeTo(got.cost, 0.25));
});

test("body_bytes and body_chars measure the whole body, whatever the path", () => {
	const bytes = resolveUsage({ units_source: "request", units_measure: "body_bytes", cost_per_unit: 1 });
	const chars = resolveUsage({ units_source: "request", units_measure: "body_chars", cost_per_unit: 1 });
	assert.strictEqual(expectedUsage({ usage: bytes, billable: true, status: 200, requestBody: req, responseBody: resNone }).units, Buffer.byteLength(req));
	assert.strictEqual(expectedUsage({ usage: chars, billable: true, status: 200, requestBody: req, responseBody: resNone }).units, [...req].length);
});

test("no rate means no quantity is read at all", () => {
	const u = resolveUsage({ units_source: "response", units_measure: "value", units_path: "usage.n", cost_per_request: 0.002 });
	const got = expectedUsage({ usage: u, billable: true, status: 200, requestBody: req, responseBody: resNone });
	assert.strictEqual(got.units, null);
	assert.ok(closeTo(got.cost, 0.002));
});

test("a reported cost is added, an array is summed, and ignore_upstream_cost drops it", () => {
	const u = resolveUsage({ cost_path: "usage.cost" });
	const num = pricingResponseBody({ costShape: "number", tokens: "none" });
	const arr = pricingResponseBody({ costShape: "array", tokens: "none" });
	assert.ok(closeTo(expectedUsage({ usage: u, billable: true, status: 200, requestBody: req, responseBody: num }).cost, 0.0025));
	assert.ok(closeTo(expectedUsage({ usage: u, billable: true, status: 200, requestBody: req, responseBody: arr }).cost, 0.0017));
	const ignored = expectedUsage({ usage: u, billable: true, status: 200, requestBody: req, responseBody: num, ignoreUpstreamCost: true });
	assert.strictEqual(ignored.cost, 0);
	assert.strictEqual(ignored.extractedCost, null);
});

test("tokens are logged and never priced; total is input + output when unmapped", () => {
	const u = resolveUsage({ input_tokens_path: "usage.in", output_tokens_path: "usage.out" });
	const got = expectedUsage({ usage: u, billable: true, status: 200, requestBody: req, responseBody: pricingResponseBody({ costShape: "none", tokens: "in-out" }) });
	assert.deepStrictEqual(got.tokens, { input: 40, output: 15, total: 55 });
	assert.strictEqual(got.cost, 0);
});

test("a non-2xx or non-billable call is not counted", () => {
	const u = resolveUsage({ cost_per_request: 0.002 });
	assert.strictEqual(expectedUsage({ usage: u, billable: true, status: 500, requestBody: req, responseBody: resNone }).counted, false);
	assert.strictEqual(expectedUsage({ usage: u, billable: false, status: 200, requestBody: req, responseBody: resNone }).counted, false);
});

test("route overrides replace field by field, and 0 waives the relay's value", () => {
	const merged = resolveUsage({ cost_per_request: 0.002, cost_per_unit: 0.01, units_path: "usage.n" }, { cost_per_unit: 0.03, units_block_size: 2 });
	assert.strictEqual(merged.cost_per_request, 0.002);
	assert.strictEqual(merged.cost_per_unit, 0.03);
	assert.strictEqual(merged.units_path, "usage.n");
	const waived = resolveUsage({ cost_per_request: 0.002, cost_per_unit: 0.01 }, { cost_per_request: 0, cost_per_unit: 0 });
	assert.strictEqual(waived.cost_per_request, 0);
	assert.strictEqual(waived.cost_per_unit, 0);
});

test("the pricing table covers every mapping dimension, folding the ones that never price", () => {
	const cases = pricingCases();
	// 2 sources x 4 measures x 2 rates x 2 fees x 2 blocks x 2 minimums x (1 + 2 cost shapes x 2 ignore flags)
	assert.strictEqual(cases.length, 2 * 4 * 2 * 2 * 2 * 2 * 5);
	assert.strictEqual(new Set(cases.map((c) => c.id)).size, cases.length);
	for (const dim of ["source", "measure", "costShape", "tokens", "modelSource"]) {
		assert.ok(new Set(cases.map((c) => c[dim])).size > 1, `${dim} varies`);
	}
	assert.ok(cases.every((c) => c.costShape !== "none" || !c.ignore), "ignore_upstream_cost only with a cost path");
});

test("a pricing relay carries the inherit, override and waive routes", () => {
	const cfg = pricingRelayConfig("r", "http://127.0.0.1:1", pricingCases()[0]);
	assert.deepStrictEqual(
		cfg.routes.map((r) => r.path),
		["/inherit", "/override", "/waive"],
	);
	assert.strictEqual(cfg.routes[0].usage, undefined);
});

test("streaming selection: the route flag wins, then streaming.paths, then the content type", () => {
	assert.strictEqual(expectedStreamed({ flag: "off", pathListed: true, kind: "sse" }), false);
	assert.strictEqual(expectedStreamed({ flag: "on", pathListed: false, kind: "json" }), true);
	assert.strictEqual(expectedStreamed({ flag: "unset", pathListed: true, kind: "json" }), true);
	assert.strictEqual(expectedStreamed({ flag: "unset", pathListed: false, kind: "sse" }), true);
	assert.strictEqual(expectedStreamed({ flag: "unset", pathListed: false, kind: "octet" }), false);
	assert.strictEqual(streamingCases().length, 3 * 2 * 3 * 2);
});

test("stream pricing: SSE keeps the last value, a forced non-SSE stream is unread, bytes always count", () => {
	assert.ok(closeTo(expectedStreamUsage({ streamed: true, kind: "sse", measureName: "value" }).cost, 0.001 + 0.003 + 0.05));
	assert.ok(closeTo(expectedStreamUsage({ streamed: true, kind: "json", measureName: "value" }).cost, 0.001));
	assert.ok(closeTo(expectedStreamUsage({ streamed: false, kind: "sse", measureName: "value" }).cost, 0.001));
	assert.ok(closeTo(expectedStreamUsage({ streamed: false, kind: "json", measureName: "value" }).cost, 0.001 + 0.003 + 0.05));
	assert.ok(closeTo(expectedStreamUsage({ streamed: false, kind: "octet", measureName: "body_bytes" }).cost, 0.001 + 10 * 0.01));
});

test("only POST is billable by default; a route flag overrides either way", () => {
	assert.deepStrictEqual(
		["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"].map((m) => effectiveBillable(m, "unset")),
		[false, true, false, false, false, false],
	);
	assert.strictEqual(effectiveBillable("GET", "true"), true);
	assert.strictEqual(effectiveBillable("POST", "false"), false);
	const b = billingCases();
	assert.strictEqual(b.length, 6 * 3 * 4);
	assert.ok(b.find((c) => c.id === "b-POST-unset-201").counted);
	assert.ok(!b.find((c) => c.id === "b-POST-unset-404").counted);
	assert.strictEqual(b.find((c) => c.id === "b-GET-true-500").rowStatus, "error");
});

test("route cases have unique ids and every relayed case names its route", () => {
	const cases = routeMatchCases();
	assert.strictEqual(new Set(cases.map((c) => c.id)).size, cases.length);
	for (const c of cases) {
		if (c.want.status === 200) assert.ok(typeof c.want.route === "string", c.id);
	}
});

test("a listed gap reports GAP, an unlisted difference FAIL, and a listed gap that matches STALE", () => {
	const key = Object.keys(KNOWN_GAPS)[0];
	assert.strictEqual(verdict(key, false), "GAP");
	assert.strictEqual(verdict(key, true), "STALE");
	assert.strictEqual(verdict("not-listed", false), "FAIL");
	assert.strictEqual(verdict("not-listed", true), "ok");
});

test("a global proxy snapshot with a redacted password cannot be restored", () => {
	assert.strictEqual(globalProxyRestorable(null), true, "no global proxy");
	assert.strictEqual(globalProxyRestorable({ enabled: false, url: "", password: "" }), true);
	assert.strictEqual(globalProxyRestorable({ enabled: true, url: "http://proxy:3128", username: "u", password: "<redacted>" }), false);
	assert.strictEqual(globalProxyRestorable({ enabled: true, url: "http://u:<redacted>@proxy:3128", password: "" }), false);
});

test("a global proxy is inherited by relays only when it is on for inference with a URL", () => {
	assert.strictEqual(globalProxyForInference(null), false);
	assert.strictEqual(globalProxyForInference({ enabled: true, enable_for_inference: true, url: "http://proxy:3128" }), true);
	assert.strictEqual(globalProxyForInference({ enabled: true, enable_for_inference: false, url: "http://proxy:3128" }), false);
	assert.strictEqual(globalProxyForInference({ enabled: false, enable_for_inference: true, url: "http://proxy:3128" }), false);
	assert.strictEqual(globalProxyForInference({ enabled: true, enable_for_inference: true, url: " " }), false);
});

console.log(`relay-matrix: ${passed} passed`);
