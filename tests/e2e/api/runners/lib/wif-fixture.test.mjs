// Unit tests for lib/wif-fixture.mjs, the logic behind runners/wif-fixture.mjs.
//
// The fixture is only worth something if it refuses what the real provider refuses: an exchange
// without the federation betas, a reused single-use identity token, an API call that sends the
// minted token without the OAuth beta or beside x-api-key. A fixture that answered 200 to all of it
// would turn its folder green while measuring nothing, so each refusal is pinned here.
//
// Run directly: `node wif-fixture.test.mjs`. No network, no Bifrost, no credentials.
import assert from "node:assert";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";

import { STATIC_KEY_PREFIX, createWifFixture, decodeIdentityToken, digest, encodeIdentityToken, identityClaims } from "./wif-fixture.mjs";

function stubResponse() {
	const res = {
		status: null,
		headers: null,
		body: "",
		ended: false,
		writeHead(status, headers = null) {
			res.status = status;
			res.headers = headers;
			return res;
		},
		end(chunk = "") {
			res.body += chunk;
			res.ended = true;
		},
	};
	return res;
}

function stubRequest(method, url, body = null, headers = {}) {
	const raw = body === null ? "" : typeof body === "string" ? body : JSON.stringify(body);
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

async function call(fixture, method, url, body = null, headers = {}) {
	const res = stubResponse();
	await fixture.handle(stubRequest(method, url, body, headers), res);
	assert.equal(res.ended, true, `${method} ${url} must get a completed response`);
	return { status: res.status, headers: res.headers || {}, text: res.body, json: () => JSON.parse(res.body) };
}

const exchangeHeaders = { "content-type": "application/json", "anthropic-beta": "oauth-2025-04-20,oidc-federation-2026-04-01" };

function exchangeBody(assertion, overrides = {}) {
	return {
		grant_type: "urn:ietf:params:oauth:grant-type:jwt-bearer",
		assertion,
		federation_rule_id: "fdrl_wiffixture",
		organization_id: "00000000-0000-0000-0000-000000000000",
		service_account_id: "svac_wiffixture",
		workspace_id: "wrkspc_wiffixture",
		...overrides,
	};
}

async function mint(fixture, claims) {
	const res = await call(fixture, "POST", "/anthropic/v1/oauth/token", exchangeBody(encodeIdentityToken(claims)), exchangeHeaders);
	return { res, token: res.status === 200 ? res.json().access_token : null };
}

const bearer = (token, beta = "oauth-2025-04-20") => ({ "content-type": "application/json", authorization: `Bearer ${token}`, "anthropic-beta": beta });

test("identity tokens round-trip and rotation changes the assertion", () => {
	const a = encodeIdentityToken(identityClaims({ sub: "s", jti: true, fx: { expires_in: 6 } }));
	const b = encodeIdentityToken(identityClaims({ sub: "s" }));
	assert.equal(decodeIdentityToken(a).sub, "s");
	assert.ok(decodeIdentityToken(a).jti);
	assert.deepEqual(decodeIdentityToken(a).fx, { expires_in: 6 });
	assert.equal(decodeIdentityToken(b).jti, undefined);
	assert.notEqual(a, b);
	assert.equal(decodeIdentityToken("not.a"), null);
});

test("a well-formed exchange mints a token the API then accepts as a bearer", async () => {
	const fixture = createWifFixture();
	const { res, token } = await mint(fixture, identityClaims({ sub: "ok" }));
	assert.equal(res.status, 200, res.text);
	assert.equal(res.json().token_type, "Bearer");
	assert.equal(res.json().expires_in, 3600);
	assert.match(token, /^sk-ant-oat01-wif-fixture-/);

	const api = await call(fixture, "POST", "/anthropic/v1/messages", { model: "claude-haiku-4-5", messages: [] }, bearer(token, "oauth-2025-04-20,interleaved-thinking-2025-05-14"));
	assert.equal(api.status, 200, api.text);
	assert.equal(api.json().content[0].text, "wif fixture ok");

	const hits = (await call(fixture, "GET", "/__wif/hits?sub=ok")).json();
	assert.equal(hits.exchanges, 1);
	assert.equal(hits.upstream["POST /v1/messages"], 1);
	assert.deepEqual(hits.access_tokens, [token]);
	assert.deepEqual(hits.last_exchange.mismatches, []);
	assert.equal(hits.last_exchange.workspace_id, "wrkspc_wiffixture");
	assert.equal(hits.last_upstream.auth, "bearer");
	assert.deepEqual(hits.last_upstream.betas, ["oauth-2025-04-20", "interleaved-thinking-2025-05-14"]);
});

test("an exchange the endpoint would not accept is refused with every problem named", async () => {
	const fixture = createWifFixture();
	const assertion = encodeIdentityToken(identityClaims({ sub: "bad" }));
	const noBetas = await call(fixture, "POST", "/anthropic/v1/oauth/token", exchangeBody(assertion), { "content-type": "application/json" });
	assert.equal(noBetas.status, 400);
	assert.match(noBetas.text, /lacks oauth-2025-04-20/);
	assert.match(noBetas.text, /lacks oidc-federation-2026-04-01/);

	const badBody = await call(fixture, "POST", "/anthropic/v1/oauth/token", exchangeBody(assertion, { grant_type: "client_credentials", organization_id: "org", workspace_id: "ws" }), exchangeHeaders);
	assert.equal(badBody.status, 400);
	for (const field of ["grant_type", "organization_id", "workspace_id"]) assert.match(badBody.text, new RegExp(field));

	const notJWT = await call(fixture, "POST", "/anthropic/v1/oauth/token", exchangeBody("opaque"), exchangeHeaders);
	assert.equal(notJWT.status, 401);
});

test("a single-use identity token is exchanged once", async () => {
	const fixture = createWifFixture();
	const claims = identityClaims({ sub: "once", jti: true });
	assert.equal((await mint(fixture, claims)).res.status, 200);
	const again = await mint(fixture, claims);
	assert.equal(again.res.status, 401);
	const hits = (await call(fixture, "GET", "/__wif/hits?sub=once")).json();
	assert.equal(hits.exchanges, 2);
	assert.equal(hits.jti_reuses, 1);
});

test("the fx claim picks the token endpoint's answer", async () => {
	const fixture = createWifFixture();
	const denied = await mint(fixture, identityClaims({ sub: "d", fx: { exchange_status: 401 } }));
	assert.equal(denied.res.status, 401);
	assert.equal(denied.res.json().error.type, "authentication_error");

	const down = await mint(fixture, identityClaims({ sub: "u", fx: { exchange_status: 503 } }));
	assert.equal(down.res.status, 503);
	assert.equal(down.res.headers["Retry-After"], "1");

	const empty = await mint(fixture, identityClaims({ sub: "e", fx: { omit_access_token: true } }));
	assert.equal(empty.res.status, 200);
	assert.equal(empty.res.json().access_token, undefined);

	const short = await mint(fixture, identityClaims({ sub: "s", fx: { expires_in: 6 } }));
	assert.equal(short.res.json().expires_in, 6);

	const redirect = await mint(fixture, identityClaims({ sub: "r", fx: { redirect: true } }));
	assert.equal(redirect.res.status, 307);
	assert.equal(redirect.res.headers.Location, "/__wif/redirect-trap?sub=r");
	await call(fixture, "POST", "/__wif/redirect-trap?sub=r", exchangeBody("x"), exchangeHeaders);
	assert.equal((await call(fixture, "GET", "/__wif/hits?sub=r")).json().redirect_hits, 1, "a followed redirect is counted");

	const rejected = await mint(fixture, identityClaims({ sub: "x", fx: { upstream_status: 401 } }));
	const api = await call(fixture, "POST", "/anthropic/v1/messages", { model: "m" }, bearer(rejected.token));
	assert.equal(api.status, 401);
	assert.equal((await call(fixture, "GET", "/__wif/hits?sub=x")).json().upstream["POST /v1/messages"], 1);
});

test("the API refuses a bearer without the OAuth beta, beside x-api-key, unknown or expired", async () => {
	let clock = 1_000_000;
	const fixture = createWifFixture({ now: () => clock });
	const { token } = await mint(fixture, identityClaims({ sub: "a", fx: { expires_in: 6 } }));
	const body = { model: "m" };
	assert.equal((await call(fixture, "POST", "/anthropic/v1/messages", body, bearer(token, "context-1m-2025-08-07"))).status, 401);
	assert.equal((await call(fixture, "POST", "/anthropic/v1/messages", body, { ...bearer(token), "x-api-key": "sk-ant-api" })).status, 401);
	assert.equal((await call(fixture, "POST", "/anthropic/v1/messages", body, bearer("sk-ant-oat01-forged"))).status, 401);
	assert.equal((await call(fixture, "POST", "/anthropic/v1/messages", body, { "content-type": "application/json" })).status, 401);
	assert.equal((await call(fixture, "POST", "/anthropic/v1/messages", body, bearer(token))).status, 200);
	clock += 6000;
	assert.equal((await call(fixture, "POST", "/anthropic/v1/messages", body, bearer(token))).status, 401);
	assert.equal((await call(fixture, "GET", "/__wif/hits?sub=a")).json().upstream["POST /v1/messages"], 1, "refused calls are not counted");
});

test("a static fixture key is accepted and recorded under itself", async () => {
	const fixture = createWifFixture();
	const key = `${STATIC_KEY_PREFIX}fallback`;
	const res = await call(fixture, "POST", "/anthropic/v1/messages", { model: "m" }, { "content-type": "application/json", "x-api-key": key });
	assert.equal(res.status, 200, res.text);
	const hits = (await call(fixture, "GET", `/__wif/hits?sub=${key}`)).json();
	assert.equal(hits.upstream["POST /v1/messages"], 1);
	assert.equal(hits.last_upstream.auth, "api-key");
	assert.equal((await call(fixture, "POST", "/anthropic/v1/messages", { model: "m" }, { "x-api-key": "sk-ant-api03-real" })).status, 401);
});

test("the API serves streaming messages, count_tokens and models", async () => {
	const fixture = createWifFixture();
	const { token } = await mint(fixture, identityClaims({ sub: "routes" }));
	const stream = await call(fixture, "POST", "/anthropic/v1/messages", { model: "m", stream: true }, bearer(token));
	assert.equal(stream.headers["Content-Type"], "text/event-stream");
	assert.match(stream.text, /event: message_start/);
	assert.match(stream.text, /wif fixture ok/);
	assert.match(stream.text, /event: message_stop/);
	assert.equal((await call(fixture, "POST", "/anthropic/v1/messages/count_tokens", { model: "m" }, bearer(token))).json().input_tokens, 10);
	assert.equal((await call(fixture, "GET", "/anthropic/v1/models?limit=1000", null, bearer(token))).json().data[0].id, "claude-haiku-4-5");
	assert.equal((await call(fixture, "POST", "/anthropic/v1/unknown", {}, bearer(token))).status, 404);
	const hits = (await call(fixture, "GET", "/__wif/hits?sub=routes")).json();
	assert.equal(hits.upstream["GET /v1/models"], 1);
	assert.equal(hits.last_upstream.route, "/v1/unknown");
});

test("token files are written atomically and rotate to a new assertion", async () => {
	const dir = mkdtempSync(path.join(tmpdir(), "wif-fixture-"));
	const fixture = createWifFixture({ tokenDir: dir });
	const first = (await call(fixture, "POST", "/__wif/token-file", { name: "rot.jwt", sub: "rot" })).json();
	const token = readFileSync(first.path, "utf8").trim();
	assert.equal(first.assertion_digest, digest(token));
	const second = (await call(fixture, "POST", "/__wif/token-file", { name: "rot.jwt", sub: "rot", jti: true })).json();
	assert.equal(second.path, first.path);
	assert.notEqual(second.assertion_digest, first.assertion_digest);
	assert.ok(decodeIdentityToken(readFileSync(second.path, "utf8").trim()).jti);
	for (const name of ["../escape.jwt", "x/y.jwt", "x.txt", ""]) {
		assert.equal((await call(fixture, "POST", "/__wif/token-file", { name, sub: "rot" })).status, 400, `name ${JSON.stringify(name)} must be refused`);
	}
});

test("every request target is answered instead of thrown on", async () => {
	const fixture = createWifFixture();
	for (const [method, target] of [["POST", "//%"], ["POST", "//["], ["GET", "/"], ["POST", "/openai/v1/oauth/token"], ["GET", "/__wif/unknown"], ["POST", "/__wif/identity-token"]]) {
		const res = await call(fixture, method, target);
		assert.ok(res.status >= 400, `${method} ${target} answered ${res.status}`);
	}
	const badJSON = await call(fixture, "POST", "/anthropic/v1/oauth/token", "{not json", exchangeHeaders);
	assert.equal(badJSON.status, 400);
	assert.equal((await call(fixture, "GET", "/__wif/health")).json().fixture, "wif-fixture");
});
