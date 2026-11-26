// Unit tests for lib/anthropic-request-surface.mjs — the verdict logic behind
// runners/anthropic-request-surface-fixture.mjs.
//
// The point of these is that the fixture must REJECT a wrong forwarded request.
// A fixture that answers 200 to anything turns its harness folder into a green
// light that measures nothing, so every case is driven three ways: the request
// the caller sent is accepted, each single-field drift off it is rejected, and
// the body upstream forwards today is rejected for the cases this change fixes.
//
// Run directly: `node anthropic-request-surface.test.mjs`. No network, no
// Bifrost, no credentials.
import assert from "node:assert";
import test from "node:test";

import {
	INTERLEAVED_THINKING_BETA,
	REQUEST_SURFACE_CASES,
	absent,
	betaTokensFromHeaders,
	present,
	caseKeyFromBody,
	evaluateForwardedRequest,
} from "./anthropic-request-surface.mjs";
import { handleFixtureRequest } from "../anthropic-request-surface-fixture.mjs";

// conformingRequest renders the request a correct gateway forwards for a case:
// the marker message plus exactly the fields the case expects on the wire.
function conformingRequest(caseKey, overrides = {}) {
	const spec = REQUEST_SURFACE_CASES[caseKey];
	const body = {
		model: spec.model,
		messages: [{ role: "user", content: [{ type: "text", text: `surface-case: ${caseKey}` }] }],
	};
	if (spec.path !== "/v1/messages/count_tokens") body.max_tokens = 1024;
	if (spec.stream) body.stream = true;
	const e = spec.expect;
	// Deep-cloned: a mutation case must not reach into the declared table and
	// change the very expectation it is being measured against.
	if (e.thinking !== absent) body.thinking = structuredClone(e.thinking);
	if (e.effort === present) body.output_config = { effort: "low" };
	else if (e.effort !== absent) body.output_config = { effort: e.effort };
	if (e.temperature !== absent) body.temperature = e.temperature;
	if (e.top_p !== absent) body.top_p = e.top_p;
	if (e.top_k !== absent) body.top_k = e.top_k;
	const headers = {};
	if (e.requiredBetas.length > 0) headers["anthropic-beta"] = e.requiredBetas.join(",");
	return { path: spec.path, body: { ...body, ...overrides }, headers };
}

test("every declared case accepts the request the caller sent", () => {
	for (const caseKey of Object.keys(REQUEST_SURFACE_CASES)) {
		const verdict = evaluateForwardedRequest(conformingRequest(caseKey));
		assert.deepEqual(verdict.mismatches, [], `${caseKey} should be accepted`);
		assert.equal(verdict.caseKey, caseKey);
	}
});

test("the case marker is read on both message shapes, and nowhere else", () => {
	// The typed path rebuilds the message as content blocks; the raw path
	// forwards the caller's string. Both have to resolve to the same case.
	assert.equal(caseKeyFromBody({ messages: [{ role: "user", content: "surface-case: sampling-raw" }] }), "sampling-raw");
	assert.equal(
		caseKeyFromBody({ messages: [{ role: "user", content: [{ type: "text", text: "surface-case: sampling-raw" }] }] }),
		"sampling-raw",
	);
	assert.equal(caseKeyFromBody({ messages: [{ role: "user", content: "hello" }] }), null);
	assert.equal(caseKeyFromBody({}), null);
});

test("an unmarked or unknown case is rejected rather than silently passed", () => {
	const unmarked = evaluateForwardedRequest({
		path: "/v1/messages",
		body: { model: "claude-sonnet-5-5", messages: [{ role: "user", content: "hi" }] },
		headers: {},
	});
	assert.equal(unmarked.caseKey, null);
	assert.equal(unmarked.mismatches.length, 1);

	const unknown = evaluateForwardedRequest({
		path: "/v1/messages",
		body: { model: "claude-sonnet-5-5", messages: [{ role: "user", content: "surface-case: nope" }] },
		headers: {},
	});
	assert.equal(unknown.caseKey, "nope");
	assert.match(unknown.mismatches[0], /unknown surface case/);
});

// The case table is a plain object, so every name on Object.prototype is
// reachable through it by inheritance. "constructor" is spellable as a marker
// (the marker regex accepts lowercase letters), and resolving it yields a
// truthy non-case whose .expect is undefined -- which the verdict then reads.
// The fixture calls evaluateForwardedRequest outside its try/catch, so a throw
// here is an unhandled rejection that takes the fixture down instead of the
// promised 400. An inherited name must be an unknown case like any other.
test("a marker naming an inherited Object.prototype property is an unknown case", () => {
	for (const inherited of ["constructor", "hasownproperty", "isprototypeof"]) {
		const verdict = evaluateForwardedRequest({
			path: "/v1/messages",
			body: { model: "claude-sonnet-5-5", messages: [{ role: "user", content: `surface-case: ${inherited}` }] },
			headers: {},
		});
		assert.equal(verdict.caseKey, inherited);
		assert.match(verdict.mismatches[0], /unknown surface case/, `${inherited} must be rejected as unknown`);
	}
});

// One mutation per field the change is responsible for, on every case that
// declares it. A fixture that misses any of these cannot see the defect.
test("each single-field drift off the caller's request is rejected", () => {
	const mutations = [
		["thinking", (body) => { body.thinking = { type: "disabled" }; }],
		["thinking-dropped", (body) => { delete body.thinking; }],
		["budget-dropped", (body) => {
			if (body.thinking && body.thinking.budget_tokens !== undefined) delete body.thinking.budget_tokens;
		}],
		["effort-invented", (body) => { body.output_config = { effort: "high" }; }],
		["effort-dropped", (body) => { delete body.output_config; }],
		["temperature", (body) => { body.temperature = 0.1; }],
		["temperature-dropped", (body) => { delete body.temperature; }],
		["top_p", (body) => { body.top_p = 0.1; }],
		["top_k", (body) => { body.top_k = 1; }],
		["model", (body) => { body.model = "claude-sonnet-5-5-mutant"; }],
		["stream", (body) => { body.stream = !body.stream; }],
	];
	for (const caseKey of Object.keys(REQUEST_SURFACE_CASES)) {
		for (const [label, mutate] of mutations) {
			// A field declared present-but-unpinned is asserted for presence
			// only, so a value drift on it is not a drift. Its own removal
			// still is, and stays in the loop.
			if (label === "effort-invented" && REQUEST_SURFACE_CASES[caseKey].expect.effort === present) continue;
			const request = conformingRequest(caseKey);
			const before = JSON.stringify(request.body);
			mutate(request.body);
			if (JSON.stringify(request.body) === before) continue; // field not in this case
			const verdict = evaluateForwardedRequest(request);
			assert.ok(
				verdict.mismatches.length > 0,
				`${caseKey}/${label} drifted but the fixture accepted it`,
			);
		}
	}
});

test("a present-but-unpinned field is asserted for presence, not for its value", () => {
	// control-sonnet-5-unary is the only case using it: the effort must be
	// there, any value, and its absence is still a rejection.
	const anyValue = conformingRequest("control-sonnet-5-unary");
	anyValue.body.output_config = { effort: "medium" };
	assert.deepEqual(evaluateForwardedRequest(anyValue).mismatches, []);

	const dropped = conformingRequest("control-sonnet-5-unary");
	delete dropped.body.output_config;
	assert.match(evaluateForwardedRequest(dropped).mismatches.join(" "), /output_config\.effort: want <any value>/);
});

test("beta-header drift is rejected in both directions", () => {
	const required = conformingRequest("enabled-budget-unary");
	required.headers = {};
	assert.match(
		evaluateForwardedRequest(required).mismatches.join(" "),
		/interleaved-thinking-2025-05-14 present/,
	);

	const forbidden = conformingRequest("between-tools-unary");
	forbidden.headers = { "anthropic-beta": INTERLEAVED_THINKING_BETA };
	assert.match(
		evaluateForwardedRequest(forbidden).mismatches.join(" "),
		/interleaved-thinking-2025-05-14 absent/,
	);

	// An unrelated beta token is not this folder's business and must not fail.
	const unrelated = conformingRequest("between-tools-unary");
	unrelated.headers = { "anthropic-beta": "context-1m-2025-08-07" };
	assert.deepEqual(evaluateForwardedRequest(unrelated).mismatches, []);
});

test("anthropic-beta is normalized across comma-joined and repeated headers", () => {
	assert.deepEqual(betaTokensFromHeaders({ "anthropic-beta": "a, b" }), ["a", "b"]);
	assert.deepEqual(betaTokensFromHeaders({ "anthropic-beta": ["b", "a,b"] }), ["a", "b"]);
	assert.deepEqual(betaTokensFromHeaders({}), []);
});

// The `stock` field on each case is the body upstream forwards today, measured
// off BuildAnthropicResponsesRequestBody on unpatched dev at adea078c4. Replaying it
// must fail for every case the change fixes — that is what makes the folder red
// before the change — and the cases with no recorded drift are the invariance
// controls, which must pass on both trees.
//
// The pinned list below is the whole point of recording both columns: PR #7665
// turned the three bare between_tools cases into controls while this change was
// in review, and a table that carried only `expect` would have gone on calling
// them fixes. Adding or demoting a case has to move this list too.
test("the body upstream forwards today is rejected exactly for the fixed cases", () => {
	const redBefore = [];
	for (const [caseKey, spec] of Object.entries(REQUEST_SURFACE_CASES)) {
		const request = conformingRequest(caseKey);
		const stock = spec.stock;
		if (Object.keys(stock).length === 0) {
			assert.deepEqual(
				evaluateForwardedRequest(request).mismatches,
				[],
				`${caseKey} is declared an invariance control, so it must pass unchanged`,
			);
			continue;
		}
		if ("thinking" in stock) {
			if (stock.thinking === absent) delete request.body.thinking;
			else request.body.thinking = stock.thinking;
		}
		if ("effort" in stock) request.body.output_config = { effort: stock.effort };
		else if (stock.thinking !== undefined && REQUEST_SURFACE_CASES[caseKey].expect.effort === absent) {
			delete request.body.output_config;
		}
		for (const field of ["temperature", "top_p", "top_k"]) {
			if (field in stock) {
				if (stock[field] === absent) delete request.body[field];
				else request.body[field] = stock[field];
			}
		}
		if ("betas" in stock) request.headers = stock.betas.length ? { "anthropic-beta": stock.betas.join(",") } : {};
		assert.ok(
			evaluateForwardedRequest(request).mismatches.length > 0,
			`${caseKey} records a drift off upstream, so the stock body must be rejected`,
		);
		redBefore.push(caseKey);
	}
	// Pinned so a case silently demoted to a control is visible in review. The
	// three sampling cases left this list when the sampling restore was dropped:
	// this family is adaptive-only and the typed converter strips the three, so
	// those cases now expect the same wire stock produces and are controls.
	assert.deepEqual(redBefore, [
		"between-tools-display-unary",
		"enabled-budget-unary",
		"enabled-budget-raw",
		"effort-preserved-unary",
	]);
});

test("the declared table stays coherent with the paths and models it claims", () => {
	for (const [caseKey, spec] of Object.entries(REQUEST_SURFACE_CASES)) {
		assert.ok(["/v1/messages", "/v1/messages/count_tokens"].includes(spec.path), caseKey);
		assert.ok(["claude-sonnet-5-5", "claude-sonnet-5"].includes(spec.model), caseKey);
		// count_tokens deletes temperature for every model on both paths.
		if (spec.path === "/v1/messages/count_tokens") {
			assert.equal(spec.expect.temperature, absent, `${caseKey} must pin the count temperature deletion`);
			assert.notEqual(spec.stream, true, `${caseKey} cannot stream`);
		}
		// The interleaved beta is derived from thinking.type "enabled" and from
		// nothing else, so required/forbidden must track the expected body.
		const wantsInterleaved = spec.expect.thinking !== absent && spec.expect.thinking.type === "enabled";
		assert.deepEqual(
			spec.expect.requiredBetas,
			wantsInterleaved ? [INTERLEAVED_THINKING_BETA] : [],
			`${caseKey} required betas disagree with its expected thinking`,
		);
		assert.deepEqual(
			spec.expect.forbiddenBetas,
			wantsInterleaved ? [] : [INTERLEAVED_THINKING_BETA],
			`${caseKey} forbidden betas disagree with its expected thinking`,
		);
	}
});

// The fixture's own request boundary, which has no test file of its own. It is
// the gate every case passes through before it reaches the verdict logic above,
// so the two stubs below drive the real handler with no port bound.
function stubResponse() {
	const res = {
		status: null,
		headers: null,
		body: "",
		writeHead(status, headers = null) {
			res.status = status;
			res.headers = headers;
			return res;
		},
		flushHeaders() {},
		write(chunk) {
			res.body += chunk;
		},
		end(chunk = "") {
			res.body += chunk;
			res.ended = true;
		},
		ended: false,
	};
	return res;
}

function stubRequest(method, url, body = null, headers = {}) {
	const raw = body === null ? "" : JSON.stringify(body);
	return {
		method,
		url,
		headers,
		setEncoding() {},
		async *[Symbol.asyncIterator]() {
			if (raw !== "") yield raw;
		},
	};
}

// A handler that throws instead of answering rejects the promise
// `http.createServer` never awaits: the request gets no response at all and, on
// Node's default unhandled-rejection policy, the fixture dies mid-folder --
// turning one malformed request into a whole red folder with no verdict in it.
// `//%` and `//[` are the reproducers; Node's HTTP parser accepts both as
// request targets and `new URL(target, base)` throws ERR_INVALID_URL on both.
test("the fixture answers every request target instead of throwing on a malformed one", async () => {
	for (const target of ["//%", "//[", "/unknown", "/v1/messages/../etc", ""]) {
		const res = stubResponse();
		await handleFixtureRequest(stubRequest("POST", target), res);
		assert.equal(res.status, 404, `target ${JSON.stringify(target)} must be answered, not thrown on`);
		assert.equal(res.ended, true, `target ${JSON.stringify(target)} must get a completed response`);
	}
});

test("the fixture still serves its two paths and refuses other methods", async () => {
	const accepted = stubResponse();
	const { path, body, headers } = conformingRequest("sampling-unary");
	await handleFixtureRequest(stubRequest("POST", path, body, headers), accepted);
	assert.equal(accepted.status, 200, accepted.body);

	const wrongMethod = stubResponse();
	await handleFixtureRequest(stubRequest("GET", "/v1/messages"), wrongMethod);
	assert.equal(wrongMethod.status, 404);
});
