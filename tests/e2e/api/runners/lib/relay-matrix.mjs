// Relay permutation matrix: the pure half of run-relay-matrix.mjs. No network.
//
// It holds the case tables (every usage mapping, streaming selection, route and billing
// combination) and an independent reference of the relay pricing contract, written from the
// documented rules rather than from the Go code, so a live gateway is checked against what the
// docs promise. relay-matrix.test.mjs pins this reference with hand-written rows.
//
// Pricing contract (docs/relays/pricing.mdx, core/relay/usage.go):
//   counted   = billable && 2xx
//   units     = the quantity read per units_source/units_measure, only when cost_per_unit > 0
//   billed    = max(ceil(units / block) when a block is set, else units; minimum)
//   cost      = cost_per_request + reported cost (cost_path, unless ignore_upstream_cost)
//               + billed * cost_per_unit
//   tokens    = logged, never priced; total = input + output when no total path is mapped
// A route's usage overrides the relay's field by field; a route value of 0 waives the relay's.

export const MEASURES = ["value", "chars", "body_bytes", "body_chars"];
export const SOURCES = ["response", "request"];

// The fixed bodies every pricing case exchanges. Multi-byte characters make bytes and characters
// differ, so a body_bytes/body_chars mix-up cannot pass.
export const PRICING_REQUEST_TEXT = "héllo wörld ✓";
export const PRICING_RESPONSE_TEXT = "résumé ünïcödé ✓✓";
export const PRICING_REQUEST_N = 7;
export const PRICING_RESPONSE_N = 13;
export const REPORTED_COST = 0.0025;
export const REPORTED_COST_ARRAY = [0.001, 0.0005, 0.0002];

export function runeCount(s) {
	return [...s].length;
}

export function byteLength(s) {
	return Buffer.byteLength(s, "utf8");
}

// pricingRequestBody is what the caller sends on a pricing case: the model (for model_source body),
// a number and a string for request-side value/chars quantities.
export function pricingRequestBody(model) {
	return JSON.stringify({ model, n: PRICING_REQUEST_N, text: PRICING_REQUEST_TEXT });
}

// pricingResponseBody is what the upstream answers. costShape picks what sits at usage.cost:
// nothing, a number, or an array of numbers (summed by the relay).
export function pricingResponseBody({ costShape, tokens }) {
	const usage = { n: PRICING_RESPONSE_N, text: PRICING_RESPONSE_TEXT };
	if (costShape === "number") usage.cost = REPORTED_COST;
	if (costShape === "array") usage.cost = REPORTED_COST_ARRAY;
	if (tokens === "in-out") {
		usage.in = 40;
		usage.out = 15;
	}
	if (tokens === "total") usage.total = 77;
	return JSON.stringify({ data: [{ id: 1 }], usage });
}

// resolveUsage merges a route's usage over the relay's, field by field, as the relay does.
export function resolveUsage(base = {}, override = {}) {
	const out = { units_source: "response", units_measure: "value", cost_per_request: 0, cost_per_unit: 0, units_minimum: 0 };
	for (const u of [base, override]) {
		if (!u) continue;
		for (const k of ["model_path", "model_source", "input_tokens_path", "output_tokens_path", "total_tokens_path", "cost_path", "units_path", "units_source", "units_measure"]) {
			if (u[k]) out[k] = u[k];
		}
		for (const k of ["cost_per_request", "cost_per_unit", "units_block_size", "units_minimum"]) {
			if (u[k] !== undefined && u[k] !== null) out[k] = u[k];
		}
	}
	return out;
}

// getPath reads a dotted path (no gjson modifiers: the cases only use plain paths).
export function getPath(obj, path) {
	if (!path) return undefined;
	let v = obj;
	for (const part of path.split(".")) {
		if (v == null || typeof v !== "object") return undefined;
		v = v[part];
	}
	return v;
}

function finiteNonNegative(n) {
	return typeof n === "number" && Number.isFinite(n) && n >= 0;
}

function parseJSON(text) {
	try {
		return JSON.parse(text);
	} catch {
		return undefined;
	}
}

function billsUnits(u) {
	return u.cost_per_unit > 0 && (Boolean(u.units_path) || u.units_measure === "body_bytes" || u.units_measure === "body_chars");
}

// measure reads the quantity from one body per the measure. json says whether the body is read as
// JSON (value and chars need it; whole-body measures do not).
function measure(u, text, json) {
	if (u.units_measure === "body_bytes") return byteLength(text);
	if (u.units_measure === "body_chars") return runeCount(text);
	if (!json) return undefined;
	const v = getPath(parseJSON(text), u.units_path);
	if (u.units_measure === "chars") return typeof v === "string" ? runeCount(v) : undefined;
	return finiteNonNegative(v) ? v : undefined;
}

export function billedUnits(u, raw) {
	let units = raw;
	if (u.units_block_size > 0) units = Math.ceil(raw / u.units_block_size);
	if (units < u.units_minimum) units = u.units_minimum;
	return units;
}

// expectedUsage is the reference: what a relay row and the budget charge must show for one call.
// responseIsJSON says whether the reply is read as JSON (its content type contains "json").
export function expectedUsage({ usage, billable, status, requestBody, responseBody, responseIsJSON = true, ignoreUpstreamCost = false }) {
	const counted = billable && status >= 200 && status < 300;
	if (!counted) return { counted: false, cost: null };
	const u = usage;
	const f = {};
	if (billsUnits(u)) {
		const q = u.units_source === "request" ? measure(u, requestBody, true) : measure(u, responseBody, responseIsJSON);
		if (q !== undefined) f.units = q;
	}
	if (responseIsJSON) {
		const parsed = parseJSON(responseBody);
		const num = (p) => {
			const v = getPath(parsed, p);
			return finiteNonNegative(v) ? v : undefined;
		};
		f.input = num(u.input_tokens_path);
		f.output = num(u.output_tokens_path);
		f.total = num(u.total_tokens_path);
		if (u.cost_path && !ignoreUpstreamCost) {
			const v = getPath(parsed, u.cost_path);
			if (finiteNonNegative(v)) f.cost = v;
			else if (Array.isArray(v)) {
				const nums = v.filter(finiteNonNegative);
				if (nums.length > 0) f.cost = nums.reduce((a, b) => a + b, 0);
			}
		}
	}
	let total = f.total;
	if (total === undefined && (f.input !== undefined || f.output !== undefined)) total = (f.input ?? 0) + (f.output ?? 0);
	const tokens = total !== undefined && total > 0 ? { input: f.input ?? 0, output: f.output ?? 0, total } : null;
	let cost = u.cost_per_request;
	let extractedCost = null;
	if (f.cost !== undefined) {
		cost += f.cost;
		extractedCost = f.cost;
	}
	let units = null;
	let billed = null;
	if (f.units !== undefined) {
		units = f.units;
		billed = billedUnits(u, f.units);
		if (u.cost_per_unit > 0) cost += billed * u.cost_per_unit;
	}
	return { counted: true, cost, extractedCost, units, billedUnits: billed, tokens };
}

// --- Pricing cases -------------------------------------------------------------------------------

// Each pricing relay carries three routes so route overrides are covered on every mapping:
//   /inherit   the relay's usage as is
//   /override  cost_per_unit and units_block_size replaced
//   /waive     cost_per_request and cost_per_unit set to 0 (a route waiving the relay's charges)
export const ROUTE_VARIANTS = {
	inherit: null,
	override: { cost_per_unit: 0.03, units_block_size: 2 },
	waive: { cost_per_request: 0, cost_per_unit: 0 },
};

const TOKEN_VARIANTS = ["none", "in-out", "total"];
const MODEL_SOURCES = ["body", "query"];

// pricingCases is the cross product of every usage mapping dimension. Two dimensions only matter
// in combination with another and are folded rather than multiplied: ignore_upstream_cost only
// with a cost path, and token paths / model source rotate across cases (they never change cost).
export function pricingCases() {
	const cases = [];
	let i = 0;
	for (const source of SOURCES) {
		for (const measureName of MEASURES) {
			for (const perUnit of [0, 0.01]) {
				for (const perRequest of [undefined, 0.002]) {
					for (const block of [undefined, 4]) {
						for (const minimum of [undefined, 25]) {
							for (const costShape of ["none", "number", "array"]) {
								for (const ignore of costShape === "none" ? [false] : [false, true]) {
									const tokens = TOKEN_VARIANTS[i % TOKEN_VARIANTS.length];
									const modelSource = MODEL_SOURCES[i % MODEL_SOURCES.length];
									const usage = { units_source: source, units_measure: measureName, cost_per_unit: perUnit };
									if (perRequest !== undefined) usage.cost_per_request = perRequest;
									if (block !== undefined) usage.units_block_size = block;
									if (minimum !== undefined) usage.units_minimum = minimum;
									if (measureName === "value") usage.units_path = source === "request" ? "n" : "usage.n";
									if (measureName === "chars") usage.units_path = source === "request" ? "text" : "usage.text";
									if (costShape !== "none") usage.cost_path = "usage.cost";
									if (tokens === "in-out") {
										usage.input_tokens_path = "usage.in";
										usage.output_tokens_path = "usage.out";
									}
									if (tokens === "total") usage.total_tokens_path = "usage.total";
									usage.model_source = modelSource;
									usage.model_path = "model";
									const id = `p${String(i).padStart(4, "0")}`;
									cases.push({ id, source, measure: measureName, perUnit, perRequest, block, minimum, costShape, ignore, tokens, modelSource, usage });
									i++;
								}
							}
						}
					}
				}
			}
		}
	}
	return cases;
}

// pricingRelayConfig is the relay a pricing case creates.
export function pricingRelayConfig(name, baseURL, c) {
	return {
		name,
		base_url: baseURL,
		auth: { type: "none" },
		ignore_upstream_cost: c.ignore,
		usage: c.usage,
		routes: Object.entries(ROUTE_VARIANTS).map(([variant, override]) => {
			const route = { method: "POST", path: `/${variant}` };
			if (override) route.usage = override;
			return route;
		}),
	};
}

// --- Streaming selection -------------------------------------------------------------------------

export const STREAM_FLAGS = ["unset", "on", "off"];
export const UPSTREAM_KINDS = ["sse", "json", "octet"];

// expectedStreamed is the selection rule: a route's stream flag wins, then streaming.paths, then
// the upstream's Content-Type (text/event-stream streams, anything else is buffered).
export function expectedStreamed({ flag, pathListed, kind }) {
	if (flag === "on") return true;
	if (flag === "off") return false;
	if (pathListed) return true;
	return kind === "sse";
}

// The stream bodies. SSE carries usage on its first and last event, with the last one winning
// per field; the JSON body carries the same final numbers; the octet body is not UTF-8.
export const SSE_BODY = 'data: {"usage":{"n":2}}\n\ndata: {"delta":"x"}\n\ndata: {"usage":{"n":5,"cost":0.003}}\n\n';
export const JSON_STREAM_BODY = JSON.stringify({ usage: { n: 5, cost: 0.003 } });
export const OCTET_BODY = Buffer.from([0xff, 0xfe, 0x00, 0x01, 0x80, 0x81, 0xc3, 0x28, 0xa0, 0xa1]);

export function streamBody(kind) {
	if (kind === "sse") return { contentType: "text/event-stream", body: Buffer.from(SSE_BODY) };
	if (kind === "json") return { contentType: "application/json", body: Buffer.from(JSON_STREAM_BODY) };
	return { contentType: "application/octet-stream", body: OCTET_BODY };
}

// expectedStreamUsage prices one streaming-selection case. measureName is "value" (usage.n, read
// from JSON or from SSE events) or "body_bytes" (the bytes forwarded, any body).
//   streamed SSE        each mapped field keeps the last value any event carried
//   streamed non-SSE    forwarded unread: only a whole-body measure is counted
//   buffered            read whole; only a JSON content type yields fields
export function expectedStreamUsage({ streamed, kind, measureName, fee = 0.001, perUnit = 0.01 }) {
	const { body } = streamBody(kind);
	let units;
	let reported;
	if (measureName === "body_bytes") units = body.length;
	if (streamed && kind === "sse") {
		if (measureName === "value") units = 5;
		reported = 0.003;
	} else if (!streamed && kind === "json") {
		if (measureName === "value") units = 5;
		reported = 0.003;
	}
	let cost = fee;
	if (reported !== undefined) cost += reported;
	if (units !== undefined) cost += units * perUnit;
	return { cost, units: units ?? null, extractedCost: reported ?? null };
}

export function streamingCases() {
	const cases = [];
	for (const flag of STREAM_FLAGS) {
		for (const pathListed of [false, true]) {
			for (const kind of UPSTREAM_KINDS) {
				for (const measureName of ["value", "body_bytes"]) {
					const streamed = expectedStreamed({ flag, pathListed, kind });
					cases.push({
						id: `s-${flag}-${pathListed ? "listed" : "unlisted"}-${kind}-${measureName}`,
						flag,
						pathListed,
						kind,
						measureName,
						streamed,
						expected: expectedStreamUsage({ streamed, kind, measureName }),
					});
				}
			}
		}
	}
	return cases;
}

// --- Methods, billing and statuses ---------------------------------------------------------------

export const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"];
export const BILLABLE_FLAGS = ["unset", "true", "false"];
export const UPSTREAM_STATUSES = [200, 201, 404, 500];

// effectiveBillable: only POST is billable unless the route says otherwise.
export function effectiveBillable(method, flag) {
	if (flag === "true") return true;
	if (flag === "false") return false;
	return method === "POST";
}

export function billingCases() {
	const cases = [];
	for (const method of METHODS) {
		for (const flag of BILLABLE_FLAGS) {
			for (const status of UPSTREAM_STATUSES) {
				const billable = effectiveBillable(method, flag);
				const counted = billable && status >= 200 && status < 300;
				cases.push({
					id: `b-${method}-${flag}-${status}`,
					method,
					flag,
					status,
					billable,
					counted,
					rowStatus: status >= 400 ? "error" : "success",
				});
			}
		}
	}
	return cases;
}

// --- Route matching ------------------------------------------------------------------------------

// routeMatchCases each name a route table, a request, and the outcome: the status the caller gets
// (404 when refused before relaying), and for a relayed call the route glob the row records.
export function routeMatchCases() {
	return [
		{ id: "literal", routes: [{ method: "POST", path: "/v1/search" }], method: "POST", path: "/v1/search", want: { status: 200, route: "/v1/search" } },
		{ id: "literal-miss", routes: [{ method: "POST", path: "/v1/search" }], method: "POST", path: "/v1/searches", want: { status: 404 } },
		{ id: "segment-star", routes: [{ method: "GET", path: "/v1/items/*" }], method: "GET", path: "/v1/items/42", want: { status: 200, route: "/v1/items/*" } },
		{ id: "segment-star-depth", routes: [{ method: "GET", path: "/v1/items/*" }], method: "GET", path: "/v1/items/42/parts", want: { status: 404 } },
		{ id: "double-star", routes: [{ method: "GET", path: "/v1/**" }], method: "GET", path: "/v1/a/b/c", want: { status: 200, route: "/v1/**" } },
		{ id: "double-star-empty", routes: [{ method: "GET", path: "/v1/**" }], method: "GET", path: "/v1", want: { status: 200, route: "/v1/**" } },
		{ id: "trailing-slash-literal", routes: [{ method: "GET", path: "/v1/items" }], method: "GET", path: "/v1/items/", want: { status: 404 } },
		{ id: "trailing-slash-double-star", routes: [{ method: "GET", path: "/v1/items/**" }], method: "GET", path: "/v1/items/", want: { status: 200, route: "/v1/items/**" } },
		{ id: "wrong-method", routes: [{ method: "POST", path: "/v1/search" }], method: "GET", path: "/v1/search", want: { status: 404 } },
		{ id: "method-case", routes: [{ method: "post", path: "/v1/search" }], method: "POST", path: "/v1/search", want: { status: 200, route: "/v1/search" } },
		{
			id: "header-match",
			routes: [{ method: "POST", path: "/v1/op", headers: { "X-Tier": "gold" } }],
			method: "POST",
			path: "/v1/op",
			headers: { "x-tier": "gold" },
			want: { status: 200, route: "/v1/op", matched: { "x-tier": "gold" } },
		},
		{ id: "header-mismatch", routes: [{ method: "POST", path: "/v1/op", headers: { "X-Tier": "gold" } }], method: "POST", path: "/v1/op", headers: { "x-tier": "silver" }, want: { status: 404 } },
		{ id: "header-value-case", routes: [{ method: "POST", path: "/v1/op", headers: { "X-Tier": "gold" } }], method: "POST", path: "/v1/op", headers: { "x-tier": "GOLD" }, want: { status: 404 } },
		{
			id: "header-glob",
			routes: [{ method: "POST", path: "/v1/op", headers: { "X-Amz-Target": "Textract.Analyze*" } }],
			method: "POST",
			path: "/v1/op",
			headers: { "x-amz-target": "Textract.AnalyzeDocument" },
			want: { status: 200, route: "/v1/op" },
		},
		// Header precedence follows a provider passthrough call: the operator's extra header is sent
		// (and matched) when the caller sends none, and the caller's own header replaces it.
		{
			id: "header-from-operator",
			extraHeaders: { "X-Tier": "gold" },
			routes: [{ method: "POST", path: "/v1/op", headers: { "X-Tier": "gold" } }],
			method: "POST",
			path: "/v1/op",
			want: { status: 200, route: "/v1/op", matched: { "x-tier": "gold" } },
		},
		{
			id: "header-caller-over-operator",
			extraHeaders: { "X-Tier": "gold" },
			routes: [{ method: "POST", path: "/v1/op", headers: { "X-Tier": "gold" } }],
			method: "POST",
			path: "/v1/op",
			headers: { "x-tier": "silver" },
			want: { status: 404 },
		},
		{
			id: "first-match-wins",
			routes: [
				{ method: "POST", path: "/v1/*", usage: { cost_per_request: 0.004 } },
				{ method: "POST", path: "/v1/search", usage: { cost_per_request: 0.009 } },
			],
			method: "POST",
			path: "/v1/search",
			want: { status: 200, route: "/v1/*", cost: 0.004 },
		},
		{
			id: "disabled-shadows",
			routes: [
				{ method: "POST", path: "/v1/search", enabled: false },
				{ method: "POST", path: "/v1/*" },
			],
			method: "POST",
			path: "/v1/search",
			want: { status: 404 },
		},
		{ id: "skip-route-check-relays", skipRouteCheck: true, routes: [], method: "POST", path: "/anything/at/all", want: { status: 200, route: "", cost: 0.001 } },
		{ id: "skip-route-check-get-free", skipRouteCheck: true, routes: [], method: "GET", path: "/anything", want: { status: 200, route: "", cost: null } },
		{
			id: "skip-route-check-disabled-still-refuses",
			skipRouteCheck: true,
			routes: [{ method: "POST", path: "/v1/search", enabled: false }],
			method: "POST",
			path: "/v1/search",
			want: { status: 404 },
		},
	];
}

// --- Known parity gaps ---------------------------------------------------------------------------

// KNOWN_GAPS lists the places relay rows deliberately differ from LLM rows. A parity check whose
// difference is listed here reports GAP instead of FAIL; a listed gap that no longer reproduces
// fails the run so the list cannot go stale.
export const KNOWN_GAPS = {
	"filter:user_agents": "LLM logs filter by raw user agent; relay logs filter by detected app only",
	"filter:metadata": "LLM logs filter by metadata_<key>=; relay logs have no metadata filter",
};

// globalProxyRestorable reports whether a GET /api/proxy-config snapshot can be PUT back as it was.
// GET redacts a stored password (the field, or the one in the URL), and a PUT that sends the
// placeholder back keeps whatever is stored at that moment: after a suite has installed its own
// global proxy, that is the suite's password, not the original.
export function globalProxyRestorable(snapshot) {
	return snapshot?.password !== "<redacted>" && !String(snapshot?.url ?? "").includes("<redacted>");
}

// globalProxyForInference reports whether a GET /api/proxy-config snapshot is a global proxy that
// inference traffic, relays included, goes through: on, on for inference, and with a URL. A relay
// with no proxy of its own (or type none) then inherits it rather than connecting directly.
export function globalProxyForInference(snapshot) {
	return !!(snapshot?.enabled && snapshot?.enable_for_inference && String(snapshot?.url ?? "").trim());
}

// verdict classifies one parity check: equal is ok; a difference is a GAP when listed, else FAIL.
export function verdict(key, equal) {
	if (equal) return KNOWN_GAPS[key] ? "STALE" : "ok";
	return KNOWN_GAPS[key] ? "GAP" : "FAIL";
}

// close compares money and latency figures that round-trip through float64 and JSON.
export function closeTo(a, b, eps = 1e-9) {
	if (a == null || b == null) return a == null && b == null;
	return Math.abs(Number(a) - Number(b)) <= eps * Math.max(1, Math.abs(Number(a)), Math.abs(Number(b)));
}
