import assert from "node:assert/strict";
import fs from "node:fs";
import { Script } from "node:vm";
import { replyToRecordedGeminiRequest } from "./gemini-client-signature-fixture.mjs";

// Offline component/script checks only: no listener, gateway, Newman or provider call.
const collection = JSON.parse(fs.readFileSync(new URL("../collections/provider-harness.json", import.meta.url), "utf8"));
const folder = collection.item.find((item) => item.name.includes("(client-thought-signature-fixture)"));
assert.ok(folder);
assert.equal(folder.item.length, 6);
const expectedSignature = "AQJyZWFsLXNpZ25hdHVyZS1ieXRlcwM=";

function expectation(actual, message, inverse = false) {
	const check = (passed, detail) => assert.ok(inverse ? !passed : passed, message || detail);
	const result = {
		equal: (expected) => check(Object.is(actual, expected), `${actual} != ${expected}`),
		eql: (expected) => {
			let equal = true;
			try { assert.deepEqual(actual, expected); } catch { equal = false; }
			check(equal, `${JSON.stringify(actual)} != ${JSON.stringify(expected)}`);
		},
		include: (expected) => check(typeof actual === "string" && actual.includes(expected), `missing ${expected}`),
	};
	Object.defineProperty(result, "to", { get: () => result });
	Object.defineProperty(result, "not", { get: () => expectation(actual, message, !inverse) });
	return result;
}

function exercise(row, { prerequest = false, enabled = true, local = true, resetStatus = 200, resetMarker = true, resetError = null, recorderStatus = 200, upstreamStatus = 200, dropped = false, extraCall = false, rawBody } = {}) {
	const failures = [];
	let tests = 0;
	let skipped = false;
	let requests = 0;
	const incoming = JSON.parse(row.request.body.raw);
	const streaming = incoming.stream === true;
	const vertex = incoming.model.startsWith("vertex/");
	const toolCall = incoming.messages[1].tool_calls[0];
	let signature = toolCall.extra_content ? expectedSignature : toolCall.id.includes("_ts_") ? expectedSignature : "skip_thought_signature_validator";
	if (dropped && toolCall.extra_content) signature = "skip_thought_signature_validator";
	const path = (vertex ? "/v1/projects/fixture-project/locations/us-central1/publishers/google/models/" : "/v1beta/models/") + "gemini-2.5-flash:" + (streaming ? "streamGenerateContent?alt=sse" : "generateContent");
	const call = {
		host: vertex ? "us-central1-aiplatform.googleapis.com" : "generativelanguage.googleapis.com",
		method: "POST",
		path,
		body: rawBody || JSON.stringify({ contents: [{ role: "model", parts: [{ thoughtSignature: signature, functionCall: { id: toolCall.id, name: "get_weather", args: { city: "Paris" } } }] }] }),
	};
	const capture = { data: [{ requests: extraCall ? [call, call] : [call] }] };
	const response = (code, json) => ({ code, json: () => json, text: () => JSON.stringify(json) });
	const pm = {
		request: { body: { raw: row.request.body.raw } },
		variables: { get: (key) => ({ clientThoughtSignatureFixture: enabled ? "1" : "", clientThoughtSignatureControlUrl: local ? "http://127.0.0.1:8810" : "https://example.com", baseUrl: "http://127.0.0.1:8811" })[key] },
		execution: { skipRequest: () => { skipped = true; } },
		expect: expectation,
		test: (name, fn) => { tests++; try { fn(); } catch (err) { failures.push({ name, message: err.message }); } },
		response: { code: upstreamStatus, text: () => streaming ? 'data: {"choices":[{"delta":{"content":"fixture completion"}}]}\n\ndata: [DONE]\n\n' : '{"choices":[{"message":{"content":"fixture completion"}}]}' },
		sendRequest: (request, callback) => {
			requests++;
			if (request.url.endsWith("/__reset")) callback(resetError, response(resetStatus, { data: [{ reset: resetMarker }] }));
			else callback(null, response(recorderStatus, capture));
		},
	};
	const event = folder.event.find((item) => item.listen === (prerequest ? "prerequest" : "test"));
	const script = new Script(event.script.exec.join("\n"), { filename: row.name });
	try { script.runInNewContext({ pm }, { timeout: 1000 }); } catch (err) { failures.push({ name: "script exception", message: err.message }); }
	return { tests, failures, skipped, requests };
}

const receipts = [];
for (const event of folder.event) new Script(event.script.exec.join("\n"));
for (const row of folder.item) {
	const green = exercise(row);
	assert.equal(green.failures.length, 0, JSON.stringify(green));
	assert.ok(green.tests >= 6);
	const red = exercise(row, { dropped: true });
	const isExtraContent = JSON.parse(row.request.body.raw).messages[1].tool_calls[0].extra_content != null;
	assert.equal(red.failures.length, isExtraContent ? 2 : 0, JSON.stringify(red));
	for (const failure of red.failures) assert.match(failure.name, /signature bytes|validator sentinel/);
	assert.ok(exercise(row, { extraCall: true }).failures.some((failure) => /exactly one converted/.test(failure.name)));
	assert.ok(exercise(row, { upstreamStatus: 401 }).failures.some((failure) => /fixture answered/.test(failure.name)));
	assert.ok(exercise(row, { recorderStatus: 503 }).failures.some((failure) => /recorder is reachable/.test(failure.name)));
	const reset = exercise(row, { prerequest: true });
	assert.equal(reset.failures.length, 0);
	assert.equal(reset.requests, 1);
	const disabled = exercise(row, { prerequest: true, enabled: false });
	assert.equal(disabled.skipped, true);
	assert.equal(disabled.requests, 0);
	const remote = exercise(row, { prerequest: true, local: false });
	assert.equal(remote.skipped, true);
	assert.equal(remote.requests, 0);
	assert.equal(remote.failures.length, 1);
	const refusedReset = exercise(row, { prerequest: true, resetStatus: 503 });
	assert.equal(refusedReset.skipped, true);
	assert.equal(refusedReset.failures.length, 1);
	const missingReset = exercise(row, { prerequest: true, resetMarker: false });
	assert.equal(missingReset.skipped, true);
	assert.equal(missingReset.failures.length, 1);
	const failedReset = exercise(row, { prerequest: true, resetError: new Error("fixture offline") });
	assert.equal(failedReset.skipped, true);
	assert.equal(failedReset.failures.length, 1);
	receipts.push({ name: row.name, green_assertions: green.tests, red_failures: red.failures.map((failure) => failure.name) });
}

for (const host of ["generativelanguage.googleapis.com", "us-central1-aiplatform.googleapis.com"]) {
	const modelPath = host.startsWith("us-") ? "/v1/projects/fixture-project/locations/us-central1/publishers/google/models/gemini-2.5-flash" : "/v1beta/models/gemini-2.5-flash";
	for (const operation of ["generateContent", "streamGenerateContent"]) {
		const request = { method: "POST", url: modelPath + ":" + operation + "?key=fixture", socket: { fixtureHost: host } };
		const signed = replyToRecordedGeminiRequest(request, '{"contents":[{"parts":[{"thoughtSignature":"' + expectedSignature + '"}]}]}');
		const unsigned = replyToRecordedGeminiRequest(request, '{"contents":[]}');
		assert.deepEqual(signed, unsigned, "fixture response must not make signature preservation appear green");
		assert.equal(signed.status, 200);
		if (operation === "streamGenerateContent") {
			assert.equal(signed.headers["Content-Type"], "text/event-stream");
			assert.ok(signed.body.startsWith("data: "));
		} else assert.equal(JSON.parse(signed.body).candidates[0].content.parts[0].text, "fixture completion");
		assert.equal(replyToRecordedGeminiRequest(request, "bad-json").status, 400);
	}
}
assert.equal(replyToRecordedGeminiRequest({ method: "POST", url: "/v1beta/models/gemini-2.5-flash:generateContent", socket: { fixtureHost: "unexpected.googleapis.com" } }, "{}").status, 404);

// Optional exact serializer-output witnesses, written by Go regression tests.
// These are a separate evidence tier from the synthetic mutation controls above.
if (process.argv[2]) {
	const witnesses = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
	for (const witness of witnesses) {
		const row = folder.item.find((item) => item.name === witness.row_name);
		assert.ok(row, witness.row_name);
		const result = exercise(row, { rawBody: witness.body });
		assert.equal(result.failures.length, witness.expected_red ? 2 : 0, JSON.stringify(result));
		for (const failure of result.failures) assert.match(failure.name, /signature bytes|validator sentinel/);
		receipts.push({ name: witness.row_name, exact_serializer_witness: witness.source_sha, source_binding: witness.source_binding, expected_red: witness.expected_red, failures: result.failures.map((failure) => failure.name) });
	}
}
console.log(JSON.stringify({ synthetic_script_rows: 6, signature_loss_red_rows: 4, canned_response_scenarios: 4, gateway_newman_or_provider_calls: 0, receipts }, null, 2));
