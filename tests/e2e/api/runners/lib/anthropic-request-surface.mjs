// Verdict logic for the Anthropic caller-request-surface fixture
// (runners/anthropic-request-surface-fixture.mjs).
//
// The fixture stands in for the Anthropic Messages API on loopback, so the
// cases assert what BIFROST FORWARDS. They say nothing about what the live
// service accepts: no request in this folder reaches a real provider, and a
// passing case is not evidence of provider acceptance or of any account
// entitlement.
//
// Every expectation below was measured off the request builder
// (BuildAnthropicResponsesRequestBody) on both trees, so "unchanged" is a
// pinned byte-level fact rather than the absence of one value. `stock` records
// what upstream forwards today: the cases whose `stock` differs from `expect`
// are red before the change and green after; the ones where they agree are
// invariance controls that must stay green on both trees.
//
// The baseline is upstream dev at adea078c4, re-measured after PR #7665 landed
// under this change. That PR carries thinking.type "between_tools" through the
// neutral ResponsesParametersReasoning, so the three bare between_tools cases
// below stopped being fixes and are recorded as controls -- their `stock` is
// now identical to their `expect`, which is exactly what keeps them honest.
// What #7665 does NOT carry is a sibling thinking.display: the neutral
// reasoning parameters have no field for it, so the typed path still drops it
// where the raw path forwards it, and `between-tools-display-unary` is the
// between_tools case that is genuinely red before this change.
//
// Pure and dependency-free on purpose: runners/lib/*.test.mjs runs it under
// `make test-harness-runner-lib` with no network, no Bifrost and no credentials.

export const INTERLEAVED_THINKING_BETA = "interleaved-thinking-2025-05-14";

const MESSAGES = "/v1/messages";
const COUNT_TOKENS = "/v1/messages/count_tokens";

// absent is the expected value for a field that must NOT appear on the wire. It
// is distinct from a value, so "dropped" is asserted as precisely as "0.5".
export const absent = Symbol("absent");

// present asserts that a field is on the wire without pinning its value. It is
// used in exactly one place, and only because that value is deployment data
// rather than a property of this change: the effort upstream derives from
// budget_tokens is a function of the model's catalogued max output tokens.
export const present = Symbol("present");

export const REQUEST_SURFACE_CASES = {
	"between-tools-unary": {
		path: MESSAGES,
		model: "claude-sonnet-5-5",
		expect: {
			thinking: { type: "between_tools" },
			effort: absent,
			temperature: absent,
			top_p: absent,
			top_k: absent,
			requiredBetas: [],
			forbiddenBetas: [INTERLEAVED_THINKING_BETA],
		},
		// Control since PR #7665: upstream already forwards this shape.
		stock: {},
	},
	"between-tools-stream": {
		path: MESSAGES,
		model: "claude-sonnet-5-5",
		stream: true,
		expect: {
			thinking: { type: "between_tools" },
			effort: absent,
			temperature: absent,
			top_p: absent,
			top_k: absent,
			requiredBetas: [],
			forbiddenBetas: [INTERLEAVED_THINKING_BETA],
		},
		// Control since PR #7665: upstream already forwards this shape.
		stock: {},
	},
	"between-tools-count": {
		path: COUNT_TOKENS,
		model: "claude-sonnet-5-5",
		expect: {
			thinking: { type: "between_tools" },
			effort: absent,
			temperature: absent,
			top_p: absent,
			top_k: absent,
			requiredBetas: [],
			forbiddenBetas: [INTERLEAVED_THINKING_BETA],
		},
		// Control since PR #7665: upstream already forwards this shape.
		stock: {},
	},
	"between-tools-display-unary": {
		path: MESSAGES,
		model: "claude-sonnet-5-5",
		// The half of between_tools the neutral round trip still cannot carry:
		// ResponsesParametersReasoning has a thinking.type but no
		// thinking.display, so upstream ships the bare object on the typed path
		// while the raw path forwards the caller's display. Measured on both
		// trees; this is the between_tools case that is red before the change.
		expect: {
			thinking: { type: "between_tools", display: "omitted" },
			effort: absent,
			temperature: absent,
			top_p: absent,
			top_k: absent,
			requiredBetas: [],
			forbiddenBetas: [INTERLEAVED_THINKING_BETA],
		},
		stock: { thinking: { type: "between_tools" } },
	},
	"enabled-budget-unary": {
		path: MESSAGES,
		model: "claude-sonnet-5-5",
		expect: {
			thinking: { type: "enabled", budget_tokens: 4096 },
			// The effort upstream derives from the budget is this converter's
			// invention, not the caller's: with the budget restored it goes.
			effort: absent,
			temperature: absent,
			top_p: absent,
			top_k: absent,
			// Beta headers are derived FROM the body, so the restored "enabled"
			// body must be the one they were computed from.
			requiredBetas: [INTERLEAVED_THINKING_BETA],
			forbiddenBetas: [],
		},
		stock: { thinking: { type: "adaptive", display: "summarized" }, effort: "high", betas: [] },
	},
	"enabled-budget-raw": {
		path: MESSAGES,
		model: "claude-sonnet-5-5",
		raw: true,
		expect: {
			thinking: { type: "enabled", budget_tokens: 4096 },
			effort: absent,
			temperature: absent,
			top_p: absent,
			top_k: absent,
			requiredBetas: [INTERLEAVED_THINKING_BETA],
			forbiddenBetas: [],
		},
		stock: { thinking: { type: "adaptive" }, betas: [] },
	},
	"effort-preserved-unary": {
		path: MESSAGES,
		model: "claude-sonnet-5-5",
		expect: {
			thinking: { type: "enabled", budget_tokens: 4096 },
			// An effort the caller DID send survives, exactly as sent.
			effort: "high",
			temperature: absent,
			top_p: absent,
			top_k: absent,
			requiredBetas: [INTERLEAVED_THINKING_BETA],
			forbiddenBetas: [],
		},
		stock: { thinking: { type: "adaptive", display: "summarized" }, effort: "high", betas: [] },
	},
	// The three typed sampling cases expect the STRIPPED wire, which is also what
	// stock produces: this model family is adaptive-only and the typed converter
	// drops temperature/top_p and deletes top_k for it, because upstream records
	// the provider rejecting the three. The cases are kept rather than deleted so
	// the raw/typed divergence stays visible next to "sampling-raw".
	"sampling-unary": {
		path: MESSAGES,
		model: "claude-sonnet-5-5",
		expect: {
			thinking: absent,
			effort: absent,
			temperature: absent,
			top_p: absent,
			top_k: absent,
			requiredBetas: [],
			forbiddenBetas: [INTERLEAVED_THINKING_BETA],
		},
		// An invariance control now: the expected wire IS what stock produces.
		stock: {},
	},
	"sampling-stream": {
		path: MESSAGES,
		model: "claude-sonnet-5-5",
		stream: true,
		expect: {
			thinking: absent,
			effort: absent,
			temperature: absent,
			top_p: absent,
			top_k: absent,
			requiredBetas: [],
			forbiddenBetas: [INTERLEAVED_THINKING_BETA],
		},
		// An invariance control now: the expected wire IS what stock produces.
		stock: {},
	},
	"sampling-count": {
		path: COUNT_TOKENS,
		model: "claude-sonnet-5-5",
		expect: {
			thinking: absent,
			effort: absent,
			// count_tokens deletes temperature for EVERY model on both paths, and
			// the other two are stripped for this family anyway.
			temperature: absent,
			top_p: absent,
			top_k: absent,
			requiredBetas: [],
			forbiddenBetas: [INTERLEAVED_THINKING_BETA],
		},
		// An invariance control now: the expected wire IS what stock produces.
		stock: {},
	},
	"sampling-raw": {
		path: MESSAGES,
		model: "claude-sonnet-5-5",
		raw: true,
		// Invariance control, not a regression pin: the raw path already
		// forwarded all three. It is here because "the typed path now answers
		// the same body the same way" is only an assertion if both halves are
		// measured in the same run.
		expect: {
			thinking: absent,
			effort: absent,
			temperature: 0.5,
			top_p: 0.9,
			top_k: 40,
			requiredBetas: [],
			forbiddenBetas: [INTERLEAVED_THINKING_BETA],
		},
		stock: {},
	},
	"control-sonnet-5-unary": {
		path: MESSAGES,
		model: "claude-sonnet-5",
		// Invariance control. The neighbouring model keeps upstream's exact
		// rewrite: enabled+budget becomes adaptive+summarized with an effort
		// derived from the budget, and all three sampling scalars are dropped.
		// A fix that widened past sonnet-5-5 fails here.
		expect: {
			thinking: { type: "adaptive", display: "summarized" },
			// Present, not pinned: measured "low" locally, but the value is
			// derived from the model's catalogued max output tokens, which is
			// deployment data. What the control asserts is that upstream still
			// synthesizes an effort here, and that this change did not stop it.
			effort: present,
			temperature: absent,
			top_p: absent,
			top_k: absent,
			requiredBetas: [],
			forbiddenBetas: [INTERLEAVED_THINKING_BETA],
		},
		stock: {},
	},
	"control-sonnet-5-raw": {
		path: MESSAGES,
		model: "claude-sonnet-5",
		raw: true,
		// Invariance control, and a deliberately UNFIXED asymmetry: on the raw
		// path the same body keeps its sampling scalars and loses only the
		// budget. This change does not touch it.
		expect: {
			thinking: { type: "adaptive" },
			effort: absent,
			temperature: 0.5,
			top_p: 0.9,
			top_k: 40,
			requiredBetas: [],
			forbiddenBetas: [INTERLEAVED_THINKING_BETA],
		},
		stock: {},
	},
	"control-sonnet-5-count": {
		path: COUNT_TOKENS,
		model: "claude-sonnet-5",
		expect: {
			thinking: absent,
			effort: absent,
			temperature: absent,
			top_p: absent,
			top_k: absent,
			requiredBetas: [],
			forbiddenBetas: [INTERLEAVED_THINKING_BETA],
		},
		stock: {},
	},
};

// caseKeyFromBody reads the case marker out of the first user message. The two
// paths do not agree on the shape of that message -- the typed path rebuilds it
// as content blocks, the raw/passthrough path forwards the caller's string --
// so both are read.
export function caseKeyFromBody(body) {
	const messages = body && Array.isArray(body.messages) ? body.messages : [];
	for (const message of messages) {
		const content = message && message.content;
		const texts = typeof content === "string"
			? [content]
			: Array.isArray(content)
				? content.map((block) => (block && typeof block.text === "string" ? block.text : ""))
				: [];
		for (const text of texts) {
			const match = /^surface-case:\s*([a-z0-9-]+)$/.exec(String(text).trim());
			if (match) return match[1];
		}
	}
	return null;
}

// betaTokensFromHeaders normalizes anthropic-beta, which may arrive as a
// comma-joined value, as repeated headers, or both.
export function betaTokensFromHeaders(headers) {
	const raw = headers ? headers["anthropic-beta"] : undefined;
	const values = raw === undefined || raw === null ? [] : Array.isArray(raw) ? raw : [raw];
	const tokens = new Set();
	for (const value of values) {
		for (const token of String(value).split(",")) {
			const trimmed = token.trim();
			if (trimmed) tokens.add(trimmed);
		}
	}
	return [...tokens].sort();
}

function show(value) {
	if (value === absent) return "<absent>";
	if (value === present) return "<any value>";
	return JSON.stringify(value);
}

function compare(mismatches, field, want, got) {
	if (want === absent) {
		if (got !== undefined) mismatches.push(`${field}: want <absent>, got ${JSON.stringify(got)}`);
		return;
	}
	if (want === present) {
		if (got === undefined) mismatches.push(`${field}: want <any value>, got <absent>`);
		return;
	}
	if (JSON.stringify(got) !== JSON.stringify(want)) {
		mismatches.push(`${field}: want ${show(want)}, got ${got === undefined ? "<absent>" : JSON.stringify(got)}`);
	}
}

// evaluateForwardedRequest is the whole verdict: it decides whether the bytes
// that reached this fixture are the request the caller sent. Every mismatch is
// reported, not just the first, so one run names every field that drifted.
export function evaluateForwardedRequest({ path, body, headers }) {
	const caseKey = caseKeyFromBody(body);
	if (!caseKey) {
		return { caseKey: null, mismatches: ["no surface-case marker in messages[].content"] };
	}
	// Own-property lookup, not a bare index: the case table is a plain object,
	// so every name on Object.prototype is reachable through it by inheritance.
	// "constructor" is spellable as a marker and would otherwise resolve to a
	// truthy non-case whose .expect is undefined, throwing below -- and the
	// fixture calls this outside its try/catch, so that throw is an unhandled
	// rejection that takes the fixture down instead of answering the 400.
	const spec = Object.hasOwn(REQUEST_SURFACE_CASES, caseKey) ? REQUEST_SURFACE_CASES[caseKey] : undefined;
	if (!spec) {
		return { caseKey, mismatches: [`unknown surface case ${JSON.stringify(caseKey)}`] };
	}

	const mismatches = [];
	if (path !== spec.path) mismatches.push(`path: want ${spec.path}, got ${path}`);
	if (body.model !== spec.model) {
		mismatches.push(`model: want ${JSON.stringify(spec.model)}, got ${JSON.stringify(body.model)}`);
	}
	const wantStream = spec.stream === true;
	if (Boolean(body.stream) !== wantStream) {
		mismatches.push(`stream: want ${wantStream}, got ${JSON.stringify(body.stream)}`);
	}

	const expect = spec.expect;
	compare(mismatches, "thinking", expect.thinking, body.thinking);
	compare(mismatches, "output_config.effort", expect.effort, body.output_config && body.output_config.effort);
	compare(mismatches, "temperature", expect.temperature, body.temperature);
	compare(mismatches, "top_p", expect.top_p, body.top_p);
	compare(mismatches, "top_k", expect.top_k, body.top_k);

	// Beta headers are asserted as required/forbidden tokens rather than as an
	// exact set: the only token this change can move is the interleaved-thinking
	// one, and an unrelated token added elsewhere in the gateway is not this
	// folder's business to fail on.
	const tokens = betaTokensFromHeaders(headers);
	for (const token of expect.requiredBetas) {
		if (!tokens.includes(token)) {
			mismatches.push(`anthropic-beta: want ${token} present, got [${tokens.join(", ")}]`);
		}
	}
	for (const token of expect.forbiddenBetas) {
		if (tokens.includes(token)) {
			mismatches.push(`anthropic-beta: want ${token} absent, got [${tokens.join(", ")}]`);
		}
	}
	return { caseKey, spec, mismatches };
}
