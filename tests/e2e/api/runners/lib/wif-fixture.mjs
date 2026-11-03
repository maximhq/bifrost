// Logic behind runners/wif-fixture.mjs, a local stand-in for a provider's workload identity
// federation: the token endpoint a federated key exchanges its OIDC identity token at, and the API
// that must then see the minted access token. The folder "Workload identity federation against a
// local token endpoint" drives it through an isolated gateway (see tests/e2e/api/README.md).
//
// Shared by every provider: identity tokens, token files, per-identity hit records and the
// behaviour a case asks for. Provider specific: a profile in PROFILES, mounted under
// /<profile name>, that says what its token endpoint must receive, what it answers, and how its
// API recognises a minted token. A provider that gains workload identity adds a profile.
//
// A case picks the behaviour through the identity token itself, in an `fx` claim:
//   exchange_status   the token endpoint answers this status instead of a token
//   expires_in        lifetime of the minted access token, in seconds (default 3600)
//   omit_access_token a 200 without access_token
//   token_type        token_type to answer with (default Bearer)
//   redirect          a 307 to /__wif/redirect-trap, which hands out a token if followed
//   upstream_status   API calls made with a token minted from this identity token get this status
// An identity token with a jti claim is single use, as Anthropic treats it: a second exchange of
// it is refused with 401.
//
// Control endpoints, none of which a gateway ever calls:
//   GET  /__wif/health                                  identifies a reusable listener
//   POST /__wif/identity-token {sub, jti?, fx?}         mints an identity token for an inline key
//   POST /__wif/token-file {name, sub, jti?, fx?}       writes (or rotates) <tokens>/<name>
//   GET  /__wif/hits?sub=<sub>                          what the fixture saw for that identity
// A static API key (x-api-key starting with "wif-fixture-static-") is accepted too, and recorded
// under the key itself, so a fallback provider or a key whose static value wins is observable.

import { createHash } from "node:crypto";
import { mkdirSync, renameSync, writeFileSync } from "node:fs";
import path from "node:path";

export const FIXTURE_ISSUER = "https://wif-fixture.invalid";
export const STATIC_KEY_PREFIX = "wif-fixture-static-";
export const FIXTURE_REPLY_TEXT = "wif fixture ok";
const TOKEN_FILE_NAME = /^[a-z0-9-]+\.jwt$/;

const b64url = (s) => Buffer.from(s).toString("base64url");

// encodeIdentityToken builds an unsigned, JWT-shaped identity token. Bifrost never verifies it;
// only the provider would, and here the fixture is the provider.
export function encodeIdentityToken(claims) {
	return `${b64url(JSON.stringify({ alg: "RS256", kid: "wif-fixture" }))}.${b64url(JSON.stringify(claims))}.${b64url("wif-fixture")}`;
}

export function decodeIdentityToken(token) {
	const parts = String(token || "").split(".");
	if (parts.length !== 3) return null;
	try {
		const claims = JSON.parse(Buffer.from(parts[1], "base64url").toString("utf8"));
		return claims && typeof claims === "object" && typeof claims.sub === "string" ? claims : null;
	} catch {
		return null;
	}
}

export const digest = (s) => createHash("sha256").update(String(s)).digest("hex").slice(0, 16);

export const betaTokens = (value) =>
	String(value || "")
		.split(",")
		.map((s) => s.trim())
		.filter(Boolean);

// identityClaims are the claims every fixture identity token carries; nonce keeps two tokens for the
// same subject distinct, so a rotation is visible as a new assertion.
export function identityClaims({ sub, jti = false, fx, nonce = `${Date.now()}-${Math.random().toString(36).slice(2, 10)}` }) {
	const claims = { iss: FIXTURE_ISSUER, sub, aud: "https://api.anthropic.com", iat: Math.floor(Date.now() / 1000), nonce };
	if (jti) claims.jti = `jti-${nonce}`;
	if (fx && typeof fx === "object") claims.fx = fx;
	return claims;
}

const anthropicError = (type, message) => JSON.stringify({ type: "error", error: { type, message }, request_id: "req_wif_fixture" });

const anthropicMessage = (model) => ({
	id: "msg_wif_fixture",
	type: "message",
	role: "assistant",
	model,
	content: [{ type: "text", text: FIXTURE_REPLY_TEXT }],
	stop_reason: "end_turn",
	stop_sequence: null,
	usage: { input_tokens: 10, output_tokens: 3 },
});

const anthropicStream = (model) => {
	const start = { ...anthropicMessage(model), content: [], stop_reason: null, usage: { input_tokens: 10, output_tokens: 0 } };
	return [
		["message_start", { type: "message_start", message: start }],
		["content_block_start", { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } }],
		["content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: FIXTURE_REPLY_TEXT } }],
		["content_block_stop", { type: "content_block_stop", index: 0 }],
		["message_delta", { type: "message_delta", delta: { stop_reason: "end_turn", stop_sequence: null }, usage: { output_tokens: 3 } }],
		["message_stop", { type: "message_stop" }],
	]
		.map(([event, data]) => `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)
		.join("");
};

const FIXTURE_MODEL = { type: "model", id: "claude-haiku-4-5", display_name: "Claude Haiku 4.5", created_at: "2025-10-01T00:00:00Z" };

// The Anthropic profile follows the wire contract in core/providers/anthropic/workloadidentity.go:
// POST /v1/oauth/token with a JSON jwt-bearer body and both betas, then the API with
// Authorization: Bearer plus the OAuth beta and no x-api-key.
export const anthropicProfile = {
	name: "anthropic",
	tokenPath: "/v1/oauth/token",
	grantType: "urn:ietf:params:oauth:grant-type:jwt-bearer",
	exchangeBetas: ["oauth-2025-04-20", "oidc-federation-2026-04-01"],
	apiBeta: "oauth-2025-04-20",

	// exchangeMismatches names every way an exchange request differs from what the endpoint accepts.
	exchangeMismatches(headers, body) {
		const out = [];
		if (!String(headers["content-type"] || "").startsWith("application/json")) out.push("content-type is not application/json");
		const betas = betaTokens(headers["anthropic-beta"]);
		for (const beta of this.exchangeBetas) if (!betas.includes(beta)) out.push(`anthropic-beta lacks ${beta}`);
		if (!body || typeof body !== "object") return [...out, "body is not a JSON object"];
		if (body.grant_type !== this.grantType) out.push(`grant_type is ${JSON.stringify(body.grant_type)}`);
		if (typeof body.assertion !== "string" || body.assertion === "") out.push("assertion is missing");
		if (!/^fdrl_\w+$/.test(body.federation_rule_id || "")) out.push("federation_rule_id is not an fdrl_ id");
		if (!/^[0-9a-f-]{36}$/.test(body.organization_id || "")) out.push("organization_id is not a UUID");
		if (!/^svac_\w+$/.test(body.service_account_id || "")) out.push("service_account_id is not an svac_ id");
		if (body.workspace_id !== undefined && !/^wrkspc_\w+$/.test(body.workspace_id)) out.push("workspace_id is not a wrkspc_ id");
		return out;
	},

	exchangeError(status, message) {
		const type = status === 401 ? "authentication_error" : status === 429 ? "rate_limit_error" : status >= 500 ? "api_error" : "invalid_request_error";
		return { status, body: anthropicError(type, message) };
	},

	exchangeSuccess({ accessToken, expiresIn, tokenType, omitAccessToken }) {
		const body = { token_type: tokenType, expires_in: expiresIn, scope: "workspace:inference" };
		if (!omitAccessToken) body.access_token = accessToken;
		return { status: 200, body: JSON.stringify(body) };
	},

	accessTokenPrefix: "sk-ant-oat01-wif-fixture-",

	// credential reads how an API call authenticated: a bearer must come with the OAuth beta and
	// without x-api-key, as Anthropic refuses either mistake.
	credential(headers) {
		const auth = String(headers.authorization || "");
		const apiKey = String(headers["x-api-key"] || "");
		if (auth.startsWith("Bearer ")) {
			if (apiKey) return { error: "a bearer request also carried x-api-key" };
			if (!betaTokens(headers["anthropic-beta"]).includes(this.apiBeta)) return { error: `a bearer request lacked the ${this.apiBeta} beta` };
			return { kind: "bearer", token: auth.slice("Bearer ".length) };
		}
		if (apiKey.startsWith(STATIC_KEY_PREFIX)) return { kind: "api-key", token: apiKey };
		return { error: "no credential the fixture accepts" };
	},

	apiError(status, message) {
		return this.exchangeError(status, message);
	},

	// api answers an authenticated call; null means the path is not served.
	api(method, route, body) {
		const model = (body && body.model) || FIXTURE_MODEL.id;
		if (method === "POST" && route === "/v1/messages") {
			if (body && body.stream === true) return { status: 200, stream: anthropicStream(model) };
			return { status: 200, body: JSON.stringify(anthropicMessage(model)) };
		}
		if (method === "POST" && route === "/v1/messages/count_tokens") return { status: 200, body: JSON.stringify({ input_tokens: 10 }) };
		if (method === "GET" && route === "/v1/models") {
			return { status: 200, body: JSON.stringify({ data: [FIXTURE_MODEL], has_more: false, first_id: FIXTURE_MODEL.id, last_id: FIXTURE_MODEL.id }) };
		}
		return null;
	},
};

export const PROFILES = { [anthropicProfile.name]: anthropicProfile };

function requestPath(target) {
	try {
		const url = new URL(target ?? "", "http://localhost");
		return { path: url.pathname, query: url.searchParams };
	} catch {
		return null;
	}
}

async function readJSON(req) {
	let raw = "";
	req.setEncoding("utf8");
	for await (const chunk of req) raw += chunk;
	if (raw === "") return {};
	return JSON.parse(raw);
}

// createWifFixture returns the request handler and the state it keeps. tokenDir is where token files
// are written; now is the clock, for tests.
export function createWifFixture({ tokenDir, now = () => Date.now() } = {}) {
	const identities = new Map();
	const accessTokens = new Map();
	const usedJti = new Set();
	let minted = 0;

	const identity = (sub) => {
		if (!identities.has(sub)) {
			identities.set(sub, { sub, exchanges: 0, jti_reuses: 0, redirect_hits: 0, assertions: [], access_tokens: [], upstream: {}, last_exchange: null, last_upstream: null });
		}
		return identities.get(sub);
	};

	const writeTokenFile = ({ name, sub, jti, fx }) => {
		if (!tokenDir) throw new Error("the fixture was started without a token directory");
		if (!TOKEN_FILE_NAME.test(String(name)) || typeof sub !== "string" || sub === "") throw new Error("token-file needs a name like x.jwt and a sub");
		const token = encodeIdentityToken(identityClaims({ sub, jti, fx }));
		const file = path.join(tokenDir, name);
		mkdirSync(tokenDir, { recursive: true });
		// Written beside and renamed over, so a reader never sees half a token, as with a kubelet rotation.
		writeFileSync(`${file}.tmp`, `${token}\n`, { mode: 0o600 });
		renameSync(`${file}.tmp`, file);
		return { path: file, assertion_digest: digest(token) };
	};

	const exchange = (profile, headers, body) => {
		const mismatches = profile.exchangeMismatches(headers, body);
		const claims = decodeIdentityToken(body && body.assertion);
		if (!claims) {
			return mismatches.length > 0 ? profile.exchangeError(400, `exchange_mismatch: ${mismatches.join("; ")}`) : profile.exchangeError(401, "Authentication failed");
		}
		const rec = identity(claims.sub);
		rec.exchanges++;
		rec.assertions.push(digest(body.assertion));
		rec.last_exchange = {
			betas: betaTokens(headers["anthropic-beta"]),
			grant_type: body.grant_type,
			federation_rule_id: body.federation_rule_id,
			organization_id: body.organization_id,
			service_account_id: body.service_account_id,
			workspace_id: body.workspace_id ?? null,
			assertion_digest: digest(body.assertion),
			mismatches,
		};
		if (mismatches.length > 0) return profile.exchangeError(400, `exchange_mismatch: ${mismatches.join("; ")}`);
		const fx = claims.fx || {};
		if (fx.redirect) return { status: 307, headers: { Location: `/__wif/redirect-trap?sub=${encodeURIComponent(claims.sub)}` }, body: "" };
		if (fx.exchange_status) {
			const out = profile.exchangeError(fx.exchange_status, `fixture answers ${fx.exchange_status}`);
			return fx.exchange_status === 429 || fx.exchange_status >= 500 ? { ...out, headers: { "Retry-After": "1" } } : out;
		}
		if (claims.jti) {
			if (usedJti.has(claims.jti)) {
				rec.jti_reuses++;
				return profile.exchangeError(401, "Authentication failed");
			}
			usedJti.add(claims.jti);
		}
		const expiresIn = Number(fx.expires_in) > 0 ? Number(fx.expires_in) : 3600;
		const accessToken = `${profile.accessTokenPrefix}${++minted}`;
		accessTokens.set(accessToken, { sub: claims.sub, profile: profile.name, expiresAt: now() + expiresIn * 1000, upstreamStatus: fx.upstream_status });
		return profile.exchangeSuccess({ accessToken, expiresIn, tokenType: fx.token_type || "Bearer", omitAccessToken: !!fx.omit_access_token });
	};

	const callAPI = (profile, method, route, headers, body) => {
		const cred = profile.credential(headers);
		if (cred.error) return profile.apiError(401, cred.error);
		let sub = cred.token;
		let upstreamStatus;
		if (cred.kind === "bearer") {
			const issued = accessTokens.get(cred.token);
			if (!issued || issued.profile !== profile.name) return profile.apiError(401, "the bearer is not a token this fixture minted");
			if (issued.expiresAt <= now()) return profile.apiError(401, "the bearer has expired");
			sub = issued.sub;
			upstreamStatus = issued.upstreamStatus;
		}
		const rec = identity(sub);
		const routeKey = `${method} ${route}`;
		rec.upstream[routeKey] = (rec.upstream[routeKey] || 0) + 1;
		if (!rec.access_tokens.includes(cred.token)) rec.access_tokens.push(cred.token);
		rec.last_upstream = {
			method,
			route,
			auth: cred.kind,
			betas: betaTokens(headers["anthropic-beta"]),
			user_agent: headers["user-agent"] || null,
			stream: !!(body && body.stream === true),
		};
		if (upstreamStatus) return profile.apiError(upstreamStatus, `fixture answers ${upstreamStatus}`);
		return profile.api(method, route, body) || profile.apiError(404, `fixture does not serve ${routeKey}`);
	};

	const control = async (req, route, query) => {
		if (req.method === "GET" && route === "/__wif/health") return { status: 200, body: { ok: true, fixture: "wif-fixture", profiles: Object.keys(PROFILES) } };
		if (req.method === "GET" && route === "/__wif/hits") {
			const sub = query.get("sub") || "";
			return { status: 200, body: identities.has(sub) ? identities.get(sub) : identity(sub) };
		}
		if (req.method === "GET" && route === "/__wif/redirect-trap") {
			identity(query.get("sub") || "").redirect_hits++;
			return { status: 200, body: { access_token: "sk-ant-oat01-wif-fixture-redirected", token_type: "Bearer", expires_in: 3600 } };
		}
		if (req.method === "POST" && route === "/__wif/redirect-trap") {
			identity(query.get("sub") || "").redirect_hits++;
			return { status: 200, body: { access_token: "sk-ant-oat01-wif-fixture-redirected", token_type: "Bearer", expires_in: 3600 } };
		}
		if (req.method === "POST" && (route === "/__wif/identity-token" || route === "/__wif/token-file")) {
			const body = await readJSON(req);
			if (typeof body.sub !== "string" || body.sub === "") return { status: 400, body: { error: "sub is required" } };
			if (route === "/__wif/identity-token") return { status: 200, body: { sub: body.sub, token: encodeIdentityToken(identityClaims(body)) } };
			try {
				return { status: 200, body: { sub: body.sub, ...writeTokenFile(body) } };
			} catch (err) {
				return { status: 400, body: { error: err.message } };
			}
		}
		return null;
	};

	// handle never throws: a malformed target or body is answered, so one bad request cannot take the
	// fixture down mid-folder.
	async function handle(req, res) {
		const send = (status, headers, body) => res.writeHead(status, { "Content-Type": "application/json", ...headers }).end(body);
		const target = requestPath(req.url);
		if (target === null) return send(404, {}, JSON.stringify({ error: "not found" }));
		try {
			if (target.path.startsWith("/__wif/")) {
				const out = await control(req, target.path, target.query);
				if (!out) return send(404, {}, JSON.stringify({ error: "not found" }));
				return send(out.status, {}, JSON.stringify(out.body));
			}
			const [, name, ...rest] = target.path.split("/");
			const profile = Object.hasOwn(PROFILES, name) ? PROFILES[name] : null;
			if (!profile) return send(404, {}, JSON.stringify({ error: `no profile serves ${target.path}` }));
			const route = `/${rest.join("/")}`;
			let body = {};
			if (req.method !== "GET") {
				try {
					body = await readJSON(req);
				} catch {
					const out = profile.apiError(400, "body is not JSON");
					return send(out.status, {}, out.body);
				}
			}
			const out = req.method === "POST" && route === profile.tokenPath ? exchange(profile, req.headers, body) : callAPI(profile, req.method, route, req.headers, body);
			if (out.stream !== undefined) {
				res.writeHead(out.status, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
				res.end(out.stream);
				return;
			}
			return send(out.status, out.headers || {}, out.body);
		} catch (err) {
			return send(500, {}, JSON.stringify({ error: `fixture error: ${err.message}` }));
		}
	}

	return { handle, writeTokenFile, identities };
}
