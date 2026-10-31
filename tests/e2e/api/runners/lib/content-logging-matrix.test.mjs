// Unit tests for the content-logging permutation table. Run directly:
// `node content-logging-matrix.test.mjs`. No network, no Bifrost.
//
// The rows below are written out by hand from the documented contract rather than derived from
// expectedOutcome, so a change to the rule has to change both places on purpose.
import assert from "node:assert";
import { allCases, callbackEndpoints, caseID, DEFAULT_LOG_RETENTION_DAYS, expectedOutcome, groupByConfig, headerValue, logRowFailures, normaliseRetentionDays, vkDisableValue } from "./content-logging-matrix.mjs";

let passed = 0;
function test(name, fn) {
	fn();
	passed++;
	console.log(`  ok - ${name}`);
}

const LOG = (c) => expectedOutcome(c).logStoresContent;
const OTEL = (c) => expectedOutcome(c).connectorExportsContent;
const base = { connector: "on", override: "blocked", header: "absent" };

test("the matrix is the full cross product, each case once", () => {
	const cases = allCases();
	assert.strictEqual(cases.length, 3 * 2 * 2 * 2 * 3);
	assert.strictEqual(new Set(cases.map((c) => c.id)).size, cases.length);
});

test("cases are grouped into the eight gateway configurations", () => {
	const groups = groupByConfig(allCases());
	assert.strictEqual(groups.length, 8);
	for (const g of groups) assert.strictEqual(g.cases.length, 9);
});

test("log store: the key overrides the client flag in both directions", () => {
	// vk        global  -> stored
	const rows = [
		["inherit", "on", true],
		["inherit", "off", false],
		["on", "on", true],
		["on", "off", true],
		["off", "on", false],
		["off", "off", false],
	];
	for (const [vk, global, want] of rows) {
		assert.strictEqual(LOG({ ...base, vk, global }), want, `vk=${vk} global=${global}`);
	}
});

test("log store: an allowed header overrides the key and the client flag", () => {
	for (const vk of ["inherit", "on", "off"]) {
		for (const global of ["on", "off"]) {
			const c = { vk, global, connector: "on", override: "allowed" };
			assert.strictEqual(LOG({ ...c, header: "on" }), true, `header on over vk=${vk} global=${global}`);
			assert.strictEqual(LOG({ ...c, header: "off" }), false, `header off over vk=${vk} global=${global}`);
		}
	}
});

test("log store: a blocked header is ignored", () => {
	for (const header of ["on", "off"]) {
		assert.strictEqual(LOG({ ...base, vk: "off", global: "on", header }), false);
		assert.strictEqual(LOG({ ...base, vk: "on", global: "off", header }), true);
		assert.strictEqual(LOG({ ...base, vk: "inherit", global: "off", header }), false);
	}
});

test("connector: a key set to off strips content; a key set to on never loosens it", () => {
	// vk        connector -> exported
	const rows = [
		["inherit", "on", true],
		["inherit", "off", false],
		["on", "on", true],
		["on", "off", false],
		["off", "on", false],
		["off", "off", false],
	];
	for (const [vk, connector, want] of rows) {
		assert.strictEqual(OTEL({ ...base, vk, connector, global: "on" }), want, `vk=${vk} connector=${connector}`);
	}
});

test("connector: the client flag and the request header never reach it", () => {
	for (const global of ["on", "off"]) {
		for (const override of ["allowed", "blocked"]) {
			for (const header of ["absent", "on", "off"]) {
				assert.strictEqual(OTEL({ vk: "inherit", global, connector: "on", override, header }), true);
				assert.strictEqual(OTEL({ vk: "inherit", global, connector: "off", override, header }), false);
			}
		}
	}
});

test("wire values for the key field and the header", () => {
	assert.strictEqual(vkDisableValue("inherit"), undefined);
	assert.strictEqual(vkDisableValue("on"), false);
	assert.strictEqual(vkDisableValue("off"), true);
	assert.strictEqual(headerValue("absent"), undefined);
	assert.strictEqual(headerValue("on"), "false");
	assert.strictEqual(headerValue("off"), "true");
	assert.strictEqual(
		caseID({ vk: "off", global: "on", connector: "on", override: "allowed", header: "absent" }),
		"vk-off.global-on.connector-on.override-allowed.header-absent",
	);
});

test("an unset retention is written back as the cleaner's default, never as one day", () => {
	// The log cleaner treats anything below 1 as its 365-day default; writing 1 instead would
	// make it delete everything older than a day.
	assert.strictEqual(DEFAULT_LOG_RETENTION_DAYS, 365);
	assert.strictEqual(normaliseRetentionDays(0), 365);
	assert.strictEqual(normaliseRetentionDays(undefined), 365);
	assert.strictEqual(normaliseRetentionDays(null), 365);
	assert.strictEqual(normaliseRetentionDays(30), 30);
	assert.strictEqual(normaliseRetentionDays(1), 1);
});

test("raw log row: disabled content leaves the marker in no column", () => {
	const m = "m0123456789abcdef";
	const clean = { id: "req-1", content_hidden: 1, input_history: "", output_message: null, params: "{}" };
	assert.deepStrictEqual(logRowFailures(clean, m, false), []);
	// Any column carrying the marker is a leak, named in the failure; sqlite/pg booleans both count.
	const leaked = { id: "req-1", content_hidden: true, input_history: "", raw_request: `{"messages":[{"content":"prompt-${m}"}]}` };
	const failures = logRowFailures(leaked, m, false);
	assert.strictEqual(failures.length, 1);
	assert.match(failures[0], /raw_request/);
	// A row not marked hidden is a failure of its own.
	assert.ok(logRowFailures({ id: "req-1", content_hidden: 0 }, m, false).some((f) => /content_hidden/.test(f)));
	// Buffers (sqlite blobs) are scanned too.
	assert.strictEqual(logRowFailures({ content_hidden: 1, blob: Buffer.from(`x${m}x`) }, m, false).length, 1);
});

test("raw log row: stored content holds the marker in input_history and is not hidden", () => {
	const m = "m0123456789abcdef";
	assert.deepStrictEqual(logRowFailures({ content_hidden: false, input_history: `[{"role":"user","content":"prompt-${m}"}]` }, m, true), []);
	assert.ok(logRowFailures({ content_hidden: false, input_history: "" }, m, true).some((f) => /input_history/.test(f)));
	assert.ok(logRowFailures({ content_hidden: true, input_history: `prompt-${m}` }, m, true).some((f) => /content_hidden/.test(f)));
});

test("callback endpoints default to loopback for a colocated gateway", () => {
	for (const callbackHost of [undefined, "", "127.0.0.1"]) {
		assert.deepStrictEqual(callbackEndpoints({ callbackHost, providerPort: 4001, collectorPort: 4002 }), {
			listenHost: "127.0.0.1",
			providerBaseURL: "http://127.0.0.1:4001",
			collectorURL: "http://127.0.0.1:4002/v1/traces",
		});
	}
});

test("a callback host makes the mocks reachable from a gateway on another host or container", () => {
	// The gateway dials the host it is given; the mocks must accept connections from outside
	// loopback for that to land.
	assert.deepStrictEqual(callbackEndpoints({ callbackHost: "host.docker.internal", providerPort: 4001, collectorPort: 4002 }), {
		listenHost: "0.0.0.0",
		providerBaseURL: "http://host.docker.internal:4001",
		collectorURL: "http://host.docker.internal:4002/v1/traces",
	});
});

console.log(`content-logging-matrix: ${passed} passed`);
