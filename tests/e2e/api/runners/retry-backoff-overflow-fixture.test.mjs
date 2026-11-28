import assert from "node:assert/strict";
import { Readable } from "node:stream";
import test from "node:test";
import { createRetryBackoffOverflowFixture } from "./retry-backoff-overflow-fixture.mjs";

const request = (stream = false, nonce = "test-nonce") => ({
	model: stream ? "retry-backoff-overflow-stream" : "retry-backoff-overflow-json",
	stream,
	messages: [{ role: "user", content: nonce }],
});

async function send(handler, body, overrides = {}) {
	const req = Object.assign(Readable.from([JSON.stringify(body)]), {
		method: "POST", url: "/v1/chat/completions", headers: { authorization: "Bearer fixture-only" },
	}, overrides);
	const response = { code: null, headers: {}, body: "" };
	await handler(req, {
		writeHead(code, headers) { response.code = code; response.headers = headers; },
		write(value) { response.body += value; },
		end(value = "") { response.body += value; },
	});
	return response;
}

test("the first overflowing retry is upstream call 39, not call 38", async () => {
	let clock = 0;
	const handler = createRetryBackoffOverflowFixture({ now: () => clock });
	for (let call = 1; call <= 38; call++) {
		const res = await send(handler, request());
		assert.equal(res.code, 503, `upstream call ${call}`);
		assert.equal(res.headers["Retry-After"], undefined);
		clock += call === 38 ? 0 : 80;
	}
	const early = await send(handler, request());
	assert.equal(early.code, 400);
	assert.match(early.body, /retry-backoff-overflow-early/);
});

for (const stream of [false, true]) {
	test(`42 sufficiently delayed calls produce the exact ${stream ? "SSE" : "JSON"} success marker`, async () => {
		let clock = 0;
		const handler = createRetryBackoffOverflowFixture({ now: () => clock });
		let res;
		for (let call = 1; call <= 42; call++) {
			res = await send(handler, request(stream));
			assert.equal(res.code, call === 42 ? 200 : 503, `upstream call ${call}`);
			clock += 80;
		}
		assert.doesNotMatch(res.body, /retry-backoff-overflow-early/);
		if (stream) {
			assert.equal(res.headers["Content-Type"], "text/event-stream");
			const events = res.body.trim().split("\n\n").map((line) => line.slice(6));
			assert.equal(JSON.parse(events[0]).choices[0].delta.content, "backoff-overflow-ok:42");
			assert.equal(JSON.parse(events[1]).choices[0].finish_reason, "stop");
			assert.equal(events[2], "[DONE]");
		} else {
			assert.equal(JSON.parse(res.body).choices[0].message.content, "backoff-overflow-ok:42");
		}
		assert.equal((await send(handler, request(stream))).code, 503, "completed chains are removed");
	});
}

test("converted unary requests may omit stream and still complete all 42 calls", async () => {
	let clock = 0;
	const handler = createRetryBackoffOverflowFixture({ now: () => clock });
	const body = request();
	delete body.stream;
	let res;
	for (let call = 1; call <= 42; call++) {
		res = await send(handler, body);
		assert.equal(res.code, call === 42 ? 200 : 503, `upstream call ${call}`);
		clock += 80;
	}
	assert.equal(JSON.parse(res.body).choices[0].message.content, "backoff-overflow-ok:42");
});

test("streaming requires true and unary rejects nonboolean stream values", async () => {
	const handler = createRetryBackoffOverflowFixture({ now: () => 0 });
	for (const stream of [undefined, false, null, 0, "true"]) {
		assert.equal((await send(handler, { ...request(true), stream })).code, 400);
	}
	for (const stream of [true, null, 0, "false"]) {
		assert.equal((await send(handler, { ...request(), stream })).code, 400);
	}
});

test("independent nonce and stream bodies never inherit a late attempt counter", async () => {
	let clock = 0;
	const handler = createRetryBackoffOverflowFixture({ now: () => clock });
	for (let call = 1; call <= 38; call++) {
		await send(handler, request());
		clock += 80;
	}
	assert.equal((await send(handler, request(false, "other-nonce"))).code, 503);
	assert.equal((await send(handler, request(true))).code, 503);
	clock += 80;
	assert.equal((await send(handler, request())).code, 503);
});

test("fixture rejects wrong routes, models and real credentials before creating a retry chain", async () => {
	const handler = createRetryBackoffOverflowFixture({ now: () => 0 });
	assert.equal((await send(handler, request(), { url: "/v1/v1/chat/completions" })).code, 404);
	assert.equal((await send(handler, { ...request(), model: "gpt-4o-mini" })).code, 400);
	assert.equal((await send(handler, request(), { headers: { authorization: "Bearer wrong-key" } })).code, 400);
	assert.equal((await send(handler, request())).code, 503);
});
