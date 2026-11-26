// Pins the relay upstream fixture's own behaviour the harness relies on.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { after, before, test } from "node:test";
import { fileURLToPath } from "node:url";

// Unique per process, so a parallel run's tagged calls never share a counter with ours.
const inflightTag = `inflight-${process.pid}-${Date.now()}`;
let base;
let child;

// Port 0 gives this process its own fixture; the bound port comes from its startup line.
before(async () => {
	const script = fileURLToPath(new URL("../relay-upstream-fixture.mjs", import.meta.url));
	child = spawn(process.execPath, [script], { env: { ...process.env, RELAY_UPSTREAM_FIXTURE_PORT: "0", RELAY_UPSTREAM_FIXTURE_HOST: "127.0.0.1" }, stdio: ["ignore", "pipe", "inherit"] });
	const port = await new Promise((resolve, reject) => {
		let out = "";
		const timer = setTimeout(() => reject(new Error(`fixture did not report a port: ${out}`)), 5000);
		child.once("exit", (code) => reject(new Error(`fixture exited with ${code}: ${out}`)));
		child.stdout.on("data", (chunk) => {
			out += chunk;
			const m = /listening on http:\/\/127\.0\.0\.1:(\d+)/.exec(out);
			if (m) {
				clearTimeout(timer);
				resolve(Number(m[1]));
			}
		});
	});
	base = `http://127.0.0.1:${port}`;
	const health = await fetch(`${base}/__relay/health`);
	assert.equal(health.status, 200, await health.text());
});
after(() => child?.kill());

const start = (id) => fetch(`${base}/v1/jobs2/start`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id }) });

test("a job id that starts with status is read from the path", async () => {
	await start("status-1");
	assert.equal((await fetch(`${base}/v1/jobs2/check/status-1`)).status, 200);
});

test("the status endpoints still read their id from the query or a header", async () => {
	await start("j-q");
	assert.equal((await fetch(`${base}/v1/jobs2/check/status?job=j-q`)).status, 200);
	assert.equal((await fetch(`${base}/v1/jobs2/check/status-h`, { headers: { "X-Job-Id": "j-q" } })).status, 200);
});

test("an XML id is decoded once, so an escaped entity stays literal", async () => {
	await start("a&lt;b");
	const literal = await fetch(`${base}/v1/jobs2/check/status-x`, { method: "POST", headers: { "Content-Type": "application/xml" }, body: "<Get><JobId>a&amp;lt;b</JobId></Get>" });
	assert.equal(literal.status, 200);
	await start("a&b");
	const amp = await fetch(`${base}/v1/jobs2/check/status-x`, { method: "POST", headers: { "Content-Type": "application/xml" }, body: "<Get><JobId>a&amp;b</JobId></Get>" });
	assert.equal(amp.status, 200);
});

test("a body stall delays the body, not the headers", async () => {
	const began = Date.now();
	const res = await fetch(`${base}/v1/program/stall?reply=${encodeURIComponent(JSON.stringify({ stall_ms: 800 }))}`);
	const headersAfter = Date.now() - began;
	await res.text();
	assert.ok(headersAfter < 600, `headers took ${headersAfter}ms`);
	assert.ok(Date.now() - began >= 750, "the body still waits for the stall");
});

test("a chunked reply sends its headers before a delayed first chunk", async () => {
	const began = Date.now();
	const res = await fetch(`${base}/v1/program/chunked?reply=${encodeURIComponent(JSON.stringify({ chunks: [{ data: "x", delay_ms: 800 }] }))}`);
	const headersAfter = Date.now() - began;
	assert.equal(await res.text(), "x");
	assert.ok(headersAfter < 600, `headers took ${headersAfter}ms`);
	assert.ok(Date.now() - began >= 750, "the body still waits for the chunk");
});

test("an empty event stream sends its headers before the stall", async () => {
	const began = Date.now();
	const res = await fetch(`${base}/v1/program/sse?reply=${encodeURIComponent(JSON.stringify({ events: [], stall_ms: 800 }))}`);
	const headersAfter = Date.now() - began;
	await res.text();
	assert.ok(headersAfter < 600, `headers took ${headersAfter}ms`);
	assert.equal(res.headers.get("content-type"), "text/event-stream");
});

test("reply=null reads as an empty config and echoes the request", async () => {
	const res = await fetch(`${base}/v1/program/null?reply=null`);
	assert.equal(res.status, 200);
	assert.equal((await res.json()).path, "/v1/program/null");
	const viaBody = await fetch(`${base}/v1/program/null`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ __reply: null }) });
	assert.equal(viaBody.status, 200);
	assert.equal((await viaBody.json()).method, "POST");
});

test("a valid reply config sets the status and body", async () => {
	const res = await fetch(`${base}/v1/program/ok?reply=${encodeURIComponent(JSON.stringify({ status: 201, body: { ok: true } }))}`);
	assert.equal(res.status, 201);
	assert.deepEqual(await res.json(), { ok: true });
});

test("inflight counts a tagged call until it is answered", async () => {
	// Held until released, so the count is read while the call is surely open.
	const pending = fetch(`${base}/v1/slow?hold=1&tag=${inflightTag}`);
	const count = async () => (await (await fetch(`${base}/__relay/inflight?tag=${inflightTag}`)).json()).inflight;
	for (let i = 0; i < 100 && (await count()) !== 1; i++) await new Promise((r) => setTimeout(r, 10));
	assert.equal(await count(), 1);
	assert.equal((await (await fetch(`${base}/__relay/release?tag=${inflightTag}`, { method: "POST" })).json()).released, true);
	assert.equal((await pending).status, 200);
	await (await pending).text();
	for (let i = 0; i < 100 && (await count()) !== 0; i++) await new Promise((r) => setTimeout(r, 10));
	assert.equal(await count(), 0);
});

test("hits records the latest tagged body, so a case can check what was forwarded", async () => {
	const tag = `body-${process.pid}-${Date.now()}`;
	await (await fetch(`${base}/v1/jobs2/check/status-j?tag=${tag}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: '{"JobId":"x","include_usage":true}' })).text();
	const h = await (await fetch(`${base}/__relay/hits?tag=${tag}`)).json();
	assert.equal(h.hits, 1);
	assert.equal(h.last_body, '{"JobId":"x","include_usage":true}');
});

test("fetch helpers refuse a target that is not a local gateway", async () => {
	for (const route of ["send", "metrics"]) {
		const res = await fetch(`${base}/__relay/${route}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ url: "http://169.254.169.254/latest" }) });
		assert.equal(res.status, 403, await res.text());
	}
});
