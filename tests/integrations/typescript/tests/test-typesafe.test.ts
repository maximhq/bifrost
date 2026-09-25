/**
 * Typesafe Integration Tests - Official SDK against Bifrost
 *
 * 🌉 SDK DROP-IN TESTING:
 * Uses the official TypeSafe JavaScript SDK (@typesafe-ai/sdk) pointed at
 * Bifrost's /typesafe prefix via baseURL, authenticating to Bifrost with the
 * suite's virtual key via the x-bf-vk header. Every call goes through the SDK.
 */

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { BadRequestError, choice, noul, score, TypeSafeClient } from "@typesafe-ai/sdk";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { getVirtualKey, isVirtualKeyConfigured } from "../src/utils/config-loader";

const STATE =
	"Customer message: I was double charged last month and nobody replied to my two emails. I want a refund today or I am cancelling.";

let client: TypeSafeClient;

beforeAll(() => {
	const baseUrl = process.env.BIFROST_BASE_URL || "http://localhost:8080";
	const vk = isVirtualKeyConfigured() ? getVirtualKey() : "";
	client = new TypeSafeClient({
		baseURL: `${baseUrl}/typesafe`,
		apiKey: vk || "dummy-key-bifrost-injects-the-real-one",
		defaultHeaders: vk ? { "x-bf-vk": vk } : undefined,
	});
});

describe("Typesafe SDK systemOne", () => {
	it("answers all three question types", async () => {
		const response = await client.systemOne({
			state: STATE,
			model: "jev-1.13.0",
			questions: {
				is_frustrated: noul("Is the customer frustrated?"),
				category: choice("Pick the ticket category", {
					billing: "charges and refunds",
					bug: "product defects",
					other: "anything else",
				}),
				urgency: score("Rate how urgently this needs a human reply", [
					"can wait a week",
					"should be answered soon",
					"needs a reply today",
				]),
			},
		});

		expect(response.model).toBe("jev-1.13.0");
		expect(response.answers.is_frustrated.noul).toBeGreaterThanOrEqual(0);
		expect(response.answers.is_frustrated.noul).toBeLessThanOrEqual(1);
		expect(["billing", "bug", "other"]).toContain(response.answers.category.choice);
		expect(typeof response.answers.urgency.score).toBe("number");
		expect(response.usage.input_tokens).toBeGreaterThan(0);
	}, 60000);

	it("resolves a model alias to its versioned id", async () => {
		const response = await client.systemOne({
			state: "Reply: Sure, sounds good, see you at 3pm.",
			model: "jev-latest",
			questions: { is_confirmation: noul("Does this reply confirm the meeting?") },
		});
		expect(response.model).toMatch(/^jev-/);
		expect(response.model).not.toBe("jev-latest");
	}, 60000);

	it("forwards structured criteria of every allowed type", async () => {
		// The API types criteria descriptions as string | object | array for
		// noul keys and score levels, plus null for choice options. One call
		// covers every allowed type in every slot; Bifrost must forward all of
		// them losslessly instead of rejecting non-string descriptions.
		const response = await client.systemOne({
			state: STATE,
			model: "jev-1.13.0",
			questions: {
				noul_obj_arr: noul("Is the customer frustrated?", {
					true: { meaning: "clearly upset", signals: ["threats", "caps"] },
					false: ["calm", "neutral tone"],
				}),
				noul_arr_obj: noul("Does the customer ask for a refund?", {
					true: ["asks for money back", "mentions refund"],
					false: { meaning: "no refund language" },
				}),
				noul_str: noul("Does the customer threaten to cancel?", {
					true: "cancellation is threatened",
					false: "no cancellation language",
				}),
				category: choice("Pick the ticket category", {
					billing: { rubric: "charges and refunds", examples: ["double charge"] },
					bug: ["crash", "product defect"],
					support: "service questions",
					other: null,
				}),
				urgency: score("Rate how urgently this needs a human reply", [
					"can wait a week",
					{ level: "should be answered soon" },
					["needs a reply today", "churn risk"],
				]),
			},
		});

		for (const name of ["noul_obj_arr", "noul_arr_obj", "noul_str"] as const) {
			expect(response.answers[name].noul).toBeGreaterThanOrEqual(0);
			expect(response.answers[name].noul).toBeLessThanOrEqual(1);
		}
		expect(["billing", "bug", "support", "other"]).toContain(response.answers.category.choice);
		expect(typeof response.answers.urgency.score).toBe("number");
		// The legend echoes each level's description verbatim - structured
		// levels come back as objects/arrays, not stringified.
		const legend = response.answers.urgency.legend as Record<string, unknown>;
		expect(Object.keys(legend ?? {})).toHaveLength(3);
		expect(legend[0]).toBe("can wait a week");
		expect(legend[1]).toEqual({ level: "should be answered soon" });
		expect(legend[2]).toEqual(["needs a reply today", "churn risk"]);
	}, 60000);
});

describe("Typesafe SDK models", () => {
	it("lists jev models with bare names", async () => {
		// The JS SDK returns the model array directly (unlike the Python SDK's
		// wrapper object).
		const listing = await client.models.list();
		const names = listing.map((m) => m.name);
		expect(names).toContain("jev-1.13.0");
		expect(names).toContain("jev-latest");
		for (const name of names) expect(name).not.toContain("typesafe/");
	}, 30000);
});

describe("Typesafe SDK errors", () => {
	it("parses Bifrost's native error body into a BadRequestError", async () => {
		// A choice question with an empty criteria map is rejected before dispatch;
		// the SDK must parse the native {"detail": {...}} body into its 400 type,
		// exactly as it would against api.typesafe.ai.
		await expect(
			client.systemOne({
				state: STATE,
				model: "jev-1.13.0",
				questions: { category: choice("Pick one", {}) },
			}),
		).rejects.toBeInstanceOf(BadRequestError);
	}, 30000);
});

// LLM fallback / emulation: any tool-capable chat model answers a decision
// request natively (primary) or as a fallback, exercised through the SDK
// (native endpoint) and the Bifrost-native /v1/decisions API (fallback).
const LLM_DECISION_MODEL = "openai/gpt-4o-mini";

describe("Decision LLM emulation", () => {
	it("an LLM emulates the decision as the primary model", async () => {
		const response = await client.systemOne({
			state: STATE,
			model: LLM_DECISION_MODEL,
			questions: {
				is_frustrated: noul("Is the customer frustrated?"),
				category: choice("Pick the ticket category", {
					billing: "charges and refunds",
					bug: "product defects",
					other: "anything else",
				}),
				urgency: score("Rate urgency", ["low", "medium", "high"]),
			},
		});
		expect(response.answers.is_frustrated.noul).toBeGreaterThanOrEqual(0);
		expect(response.answers.is_frustrated.noul).toBeLessThanOrEqual(1);
		expect(["billing", "bug", "other"]).toContain(response.answers.category.choice);
		expect(typeof response.answers.urgency.score).toBe("number");
	}, 60000);

	it("falls through to an LLM fallback when the primary fails", async () => {
		// /v1/decisions is Bifrost's native format (no upstream SDK) - HTTP directly.
		const baseUrl = process.env.BIFROST_BASE_URL || "http://localhost:8080";
		const headers: Record<string, string> = { "Content-Type": "application/json" };
		if (isVirtualKeyConfigured()) headers["x-bf-vk"] = getVirtualKey();
		const response = await fetch(`${baseUrl}/v1/decisions`, {
			method: "POST",
			headers,
			body: JSON.stringify({
				model: "openai/nonexistent-model-xyz",
				fallbacks: [LLM_DECISION_MODEL],
				state: "Reply: sounds good, see you at 3pm.",
				questions: { is_confirmation: { kind: "noul", instructions: "Does this confirm the meeting?" } },
			}),
		});
		const body = (await response.json()) as any;
		expect(response.status, JSON.stringify(body)).toBe(200);
		expect(body.answers.is_confirmation.kind).toBe("noul");
		expect(body.answers.is_confirmation.value).toBeGreaterThanOrEqual(0);
		expect(body.answers.is_confirmation.value).toBeLessThanOrEqual(1);
	}, 60000);

	it("reports the difference between native jev and LLM emulation", async () => {
		const questions = {
			is_frustrated: noul("Is the customer frustrated?"),
			category: choice("Pick the ticket category", {
				billing: "charges and refunds",
				bug: "product defects",
				other: "anything else",
			}),
			urgency: score("Rate urgency", ["low", "medium", "high"]),
		};
		const native = await client.systemOne({ state: STATE, model: "jev-1.13.0", questions });
		const emulated = await client.systemOne({ state: STATE, model: LLM_DECISION_MODEL, questions });

		const read = (r: any, name: string) => {
			const k = r.answers[name].type;
			return k === "noul" ? r.answers[name].noul : k === "choice" ? r.answers[name].choice : r.answers[name].score;
		};
		console.log("\n=== Decision: native jev vs LLM emulation ===");
		const nativeAnswers = native.answers as Record<string, { type: string }>;
		const emulatedAnswers = emulated.answers as Record<string, { type: string }>;
		for (const name of Object.keys(questions)) {
			const nv = read(native, name);
			const ev = read(emulated, name);
			console.log(`${name}: jev=${nv} gpt-4o-mini=${ev} ${nv === ev ? "=" : "DIFF"}`);
			expect(nativeAnswers[name].type).toBe(emulatedAnswers[name].type);
		}
		expect(native.model).toMatch(/^jev-/);
		expect(emulated.model).not.toBe(native.model);
	}, 60000);

	it("forwards structured criteria to the emulating model (opaque option codes)", async () => {
		// The emulation encodes criteria into the forced tool schema. The choice
		// option names here are opaque codes; only the criteria descriptions
		// (object, string, null, and array shaped) carry meaning, so a correct
		// pick on a clearly-billing state proves the rubrics reached the LLM
		// rather than being dropped or rejected as non-string.
		const response = await client.systemOne({
			state: STATE,
			model: LLM_DECISION_MODEL,
			questions: {
				approve_review: noul("Should this ticket get a billing review?", {
					true: "the customer reports a concrete billing error",
					false: ["no billing problem is described", "general inquiry"],
				}),
				pick: choice(
					"Pick the ticket category. The option names are opaque codes; use the criteria descriptions to decide.",
					{
						opt_a: { rubric: "charges and refunds" },
						opt_b: "product defects",
						opt_c: null,
						opt_d: ["installation help", "how-to setup questions"],
					},
				),
				urgency: score("Rate how urgently this needs a human reply", [
					"can wait a week",
					{ level: "should be answered soon" },
					["needs a reply today", "churn risk"],
				]),
			},
		});

		expect(response.answers.approve_review.noul).toBeGreaterThanOrEqual(0.5);
		expect(response.answers.pick.choice).toBe("opt_a");
		// Probabilities are required on native choice and score answers, so the
		// emulation must always deliver them.
		const pickProbs = Object.values(response.answers.pick.probabilities ?? {});
		expect(Math.abs(pickProbs.reduce((a, b) => a + b, 0) - 1)).toBeLessThan(0.05);
		const urgencyProbs = Object.values(response.answers.urgency.probabilities ?? {});
		expect(Math.abs(urgencyProbs.reduce((a, b) => a + b, 0) - 1)).toBeLessThan(0.05);
		expect(typeof response.answers.urgency.score).toBe("number");
		// The emulated legend must match the native shape: each level's
		// description echoed verbatim (string, object, or array).
		const legend = response.answers.urgency.legend as Record<string, unknown>;
		expect(Object.keys(legend ?? {})).toHaveLength(3);
		expect(legend[1]).toEqual({ level: "should be answered soon" });
		expect(legend[2]).toEqual(["needs a reply today", "churn risk"]);
	}, 60000);
});

// Cross-provider emulation matrix: every VK-allowed chat provider answers a
// decision through an LLM, for >=4 current models each. The scenario (one rich
// support ticket, 30 questions) and the model list are the shared source of truth
// in tests/integrations/decision_emulation_matrix.json (read by the Python suite
// and the harness generator too).
//
// jev (the native typesafe judgment model) is the ground truth: it answers the 30
// questions and every reachable model must agree with jev on at least the
// threshold fraction (choice exact, noul/score on the rounded value). A model
// below threshold FAILS. Only genuine availability (auth, model-block, not-found,
// rate limit, upstream connection) is SKIPPED; a 400 (a request we built wrong) or
// 500 (emulation returned no decision) FAILS.
const MATRIX_PATH = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "decision_emulation_matrix.json");
const MATRIX = JSON.parse(readFileSync(MATRIX_PATH, "utf-8")) as {
	providers: Record<string, string[]>;
	scenario: { state: string; questions: Record<string, { kind: string }> };
	_agreement_fail_threshold?: number;
	_split_question_providers?: string[];
};
const MATRIX_PAIRS: Array<{ provider: string; model: string }> = Object.entries(MATRIX.providers).flatMap(
	([provider, models]) => models.map((model) => ({ provider, model })),
);
// Codes meaning the model is unreachable on this account, not that our request is
// wrong. A provider that rejects a large multi-question tool schema is NOT skipped:
// the emulation falls back to one question at a time, so it answers the full set.
const AVAILABILITY_CODES = new Set([401, 403, 404, 429, 502, 503, 504]);
// Some providers report an unavailable/renamed/deprecated MODEL as a 400 rather
// than a 404. That is availability, not a request we built wrong.
const AVAILABILITY_MESSAGE_MARKERS = [
	"deprecated",
	"no longer available",
	"not supported",
	"not found",
	"does not exist",
	"invalid_model",
	"model_not_found",
	"not allowed for virtual key",
];

function isAvailability(status: number, text: string): boolean {
	if (AVAILABILITY_CODES.has(status)) return true;
	if (status === 400) {
		const low = text.toLowerCase();
		return AVAILABILITY_MESSAGE_MARKERS.some((m) => low.includes(m));
	}
	return false;
}
const MATRIX_STATE = MATRIX.scenario.state;
const MATRIX_QUESTIONS = MATRIX.scenario.questions;
const QUESTION_COUNT = Object.keys(MATRIX_QUESTIONS).length;
const AGREEMENT_FAIL_THRESHOLD = MATRIX._agreement_fail_threshold ?? 0.7;
// Providers that reject a large multi-question tool schema. The batched call is
// tried first; only if it fails with a non-availability error do these retry one
// question at a time and merge (Perplexity returns a bare "invalid request" for
// the 30-question schema but answers a single question fine).
const SPLIT_PROVIDERS = new Set(MATRIX._split_question_providers ?? []);
const JEV_MODEL = "typesafe/jev-1.13.0";

type MatrixRow = {
	modelId: string;
	status: string;
	agree: number;
	total: number;
	latencyMs: number | null;
	tokens: number | null;
	cost: string;
	detail: string;
};
const MATRIX_ROWS: MatrixRow[] = [];

type DecisionResult = { status: number; answers: any; latencyMs: number; tokens: number | null; cost: string; text: string; split: boolean };

// The provider-side request timeout is up to 300s, far above the Vitest case
// timeout, so every matrix call carries a shared client-side deadline: the
// whole fetchDecision (batched call plus any sequential split) must finish
// inside this budget, below the 90s case timeout, so a slow in-flight request
// aborts and the SKIP handling still runs instead of Vitest killing the case.
const MATRIX_CALL_BUDGET_MS = 85_000;

function isAbortError(e: unknown): boolean {
	return e instanceof Error && (e.name === "TimeoutError" || e.name === "AbortError");
}

async function postRaw(
	modelId: string,
	questions: unknown,
	deadline: number,
): Promise<{ response: Response; latencyMs: number }> {
	const baseUrl = process.env.BIFROST_BASE_URL || "http://localhost:8080";
	const headers: Record<string, string> = { "Content-Type": "application/json" };
	if (isVirtualKeyConfigured()) headers["x-bf-vk"] = getVirtualKey();
	const start = Date.now();
	const response = await fetch(`${baseUrl}/v1/decisions`, {
		method: "POST",
		headers,
		body: JSON.stringify({ model: modelId, state: MATRIX_STATE, questions }),
		signal: AbortSignal.timeout(Math.max(1, deadline - Date.now())),
	});
	return { response, latencyMs: Date.now() - start };
}

function usageCost(body: any): { tokens: number | null; cost: string } {
	const usage = body.usage ?? {};
	let cost = usage.cost;
	if (cost && typeof cost === "object") cost = cost.total ?? JSON.stringify(cost);
	return { tokens: usage.total_tokens ?? null, cost: cost != null ? String(cost) : "-" };
}

async function fetchDecision(modelId: string): Promise<DecisionResult> {
	const deadline = Date.now() + MATRIX_CALL_BUDGET_MS;
	try {
		const { response, latencyMs } = await postRaw(modelId, MATRIX_QUESTIONS, deadline);
		if (response.status === 200) {
			const body = (await response.json()) as any;
			const { tokens, cost } = usageCost(body);
			return { status: 200, answers: body.answers, latencyMs, tokens, cost, text: "", split: false };
		}
		const text = await response.text();
		if (isAvailability(response.status, text)) {
			return { status: response.status, answers: null, latencyMs, tokens: null, cost: "-", text, split: false };
		}
		const provider = modelId.split("/")[0];
		if (SPLIT_PROVIDERS.has(provider) && QUESTION_COUNT > 1) {
			return fetchDecisionSplit(modelId, latencyMs, deadline);
		}
		return { status: response.status, answers: null, latencyMs, tokens: null, cost: "-", text, split: false };
	} catch (e) {
		if (isAbortError(e)) throw budgetExceededError(modelId, false);
		throw e;
	}
}

// A request still in flight when the client budget runs out FAILS the case -
// no skip and no synthetic status; the budget exists only so the failure is
// this readable error instead of Vitest killing the case at its timeout.
function budgetExceededError(modelId: string, split: boolean): Error {
	const err = new Error(
		`client-side budget of ${MATRIX_CALL_BUDGET_MS}ms exceeded waiting on ${modelId}${split ? " (split mode)" : ""}`,
	);
	err.name = "BudgetExceededError";
	return err;
}

async function fetchDecisionSplit(modelId: string, batchedLatencyMs: number, deadline: number): Promise<DecisionResult> {
	const merged: Record<string, any> = {};
	let totalLatency = batchedLatencyMs;
	let totalTokens = 0;
	try {
		for (const [name, q] of Object.entries(MATRIX_QUESTIONS)) {
			const { response, latencyMs } = await postRaw(modelId, { [name]: q }, deadline);
			totalLatency += latencyMs;
			if (response.status !== 200) {
				const text = await response.text();
				return { status: response.status, answers: null, latencyMs: totalLatency, tokens: null, cost: "-", text, split: true };
			}
			const body = (await response.json()) as any;
			Object.assign(merged, body.answers);
			const { tokens } = usageCost(body);
			if (typeof tokens === "number") totalTokens += tokens;
		}
	} catch (e) {
		if (isAbortError(e)) throw budgetExceededError(modelId, true);
		throw e;
	}
	return { status: 200, answers: merged, latencyMs: totalLatency, tokens: totalTokens || null, cost: "-", text: "", split: true };
}

// A genuine number for the kind: booleans and numeric strings are wrong types,
// and a noul outside [0, 1] violates the contract regardless of rounding.
// Mirrors the Python suite's _valid_numeric so both matrix verdicts agree.
function validNumeric(kind: string, x: unknown): x is number {
	if (typeof x !== "number" || Number.isNaN(x)) return false;
	if (kind === "noul" && (x < 0 || x > 1)) return false;
	return true;
}

function agreement(reference: any, answers: any): { matched: number; total: number; mismatches: string[] } {
	const mismatches: string[] = [];
	for (const [name, q] of Object.entries(MATRIX_QUESTIONS)) {
		const ref = reference[name]?.value;
		const got = answers[name]?.value;
		// Math.round is half-up, the agreed rounding rule for both matrix
		// suites - the Python side's _half_up mirrors it (its round() would
		// round half to even and diverge at .5 boundaries).
		const ok =
			q.kind === "choice"
				? got === ref
				: validNumeric(q.kind, got) && Math.round(Number(ref)) === Math.round(got);
		if (!ok) mismatches.push(`${name}(jev=${ref},got=${got})`);
	}
	return { matched: QUESTION_COUNT - mismatches.length, total: QUESTION_COUNT, mismatches };
}

describe("Decision emulation matrix", () => {
	let jevReference: any;

	beforeAll(async () => {
		// jev is the ground truth for the whole matrix; if it cannot answer, the
		// comparison is meaningless, so fail loudly rather than skip.
		const r = await fetchDecision(JEV_MODEL);
		expect(r.status, `jev reference decision failed: ${r.text}`).toBe(200);
		jevReference = r.answers;
		expect(Object.keys(jevReference).length, "jev did not answer every question").toBe(QUESTION_COUNT);
		MATRIX_ROWS.push({ modelId: JEV_MODEL, status: "REF", agree: QUESTION_COUNT, total: QUESTION_COUNT, latencyMs: r.latencyMs, tokens: r.tokens, cost: r.cost, detail: "ground truth" });
		// Above MATRIX_CALL_BUDGET_MS so the abort budget fires with its
		// readable BudgetExceededError before Vitest kills the hook.
	}, 90000);

	it.each(MATRIX_PAIRS)("$provider/$model matches jev", async ({ provider, model }) => {
		const modelId = `${provider}/${model}`;
		let r: DecisionResult;
		try {
			r = await fetchDecision(modelId);
		} catch (err) {
			// A thrown request error - a blown client-side budget or a transport
			// failure - FAILS the case with its reason; it is never a skip.
			MATRIX_ROWS.push({ modelId, status: "FAIL", agree: 0, total: QUESTION_COUNT, latencyMs: null, tokens: null, cost: "-", detail: `request error: ${err}` });
			expect.fail(`${modelId}: request error: ${err}`);
			return;
		}

		const latencyMs = r.latencyMs;
		if (r.status !== 200) {
			const detail = `HTTP ${r.status}: ${r.text.slice(0, 160)}`;
			if (isAvailability(r.status, r.text)) {
				MATRIX_ROWS.push({ modelId, status: "SKIP", agree: 0, total: QUESTION_COUNT, latencyMs, tokens: null, cost: "-", detail });
				return;
			}
			// Capability-limited providers (Perplexity's sonar rejects choice/score decision
			// schemas): the split was attempted and could not complete the set, so skip rather
			// than fail - the identical schema works on every other provider.
			if (SPLIT_PROVIDERS.has(provider)) {
				MATRIX_ROWS.push({ modelId, status: "SKIP", agree: 0, total: QUESTION_COUNT, latencyMs, tokens: null, cost: "-", detail: `capability-limited - ${detail}` });
				return;
			}
			// A malformed request we built (400) or emulation returning no decision (500): defect.
			MATRIX_ROWS.push({ modelId, status: "FAIL", agree: 0, total: QUESTION_COUNT, latencyMs, tokens: null, cost: "-", detail });
			expect.fail(`${modelId}: ${detail}`);
		}

		const a = r.answers;
		const { tokens, cost } = { tokens: r.tokens, cost: r.cost };
		expect(Object.keys(a).length, `${modelId} did not answer every question`).toBe(QUESTION_COUNT);
		for (const [name, q] of Object.entries(MATRIX_QUESTIONS)) {
			// Right kind discriminator and a numeric confidence - a malformed response
			// (missing/undefined confidence, wrong kind) must not pass.
			expect(a[name].kind, `${name} kind`).toBe(q.kind);
			expect(typeof a[name].confidence, `${name} confidence not numeric`).toBe("number");
		}
		const { matched, total, mismatches } = agreement(jevReference, a);
		const row: MatrixRow = { modelId, status: "PASS", agree: matched, total, latencyMs, tokens, cost, detail: `${matched}/${total} vs jev` };
		if (matched / total < AGREEMENT_FAIL_THRESHOLD) {
			row.status = "FAIL";
			row.detail = `${matched}/${total} < threshold; diffs: ${mismatches.slice(0, 8).join(", ")}`;
			MATRIX_ROWS.push(row);
			expect.fail(`${modelId} agrees with jev on only ${matched}/${total}: ${mismatches.join(", ")}`);
		}
		MATRIX_ROWS.push(row);
	}, 90000);

	afterAll(() => {
		console.log(`\n=== Decision emulation matrix vs jev (1 scenario, ${QUESTION_COUNT} questions) ===`);
		const header = `${"combination".padEnd(50)} ${"score".padEnd(8)} ${"latency_ms".padEnd(11)} ${"tokens".padEnd(7)} ${"cost".padEnd(8)} status`;
		console.log(header);
		console.log("-".repeat(header.length));
		const rows = [...MATRIX_ROWS].sort((x, y) => (x.status === "REF" ? -1 : y.status === "REF" ? 1 : x.modelId.localeCompare(y.modelId)));
		for (const r of rows) {
			const score = `${r.agree}/${r.total}`;
			console.log(
				`${r.modelId.padEnd(50)} ${score.padEnd(8)} ${String(r.latencyMs ?? "-").padEnd(11)} ${String(r.tokens ?? "-").padEnd(7)} ${r.cost.padEnd(8)} ${r.status}`,
			);
		}
		const passed = MATRIX_ROWS.filter((r) => r.status === "PASS");
		const failed = MATRIX_ROWS.filter((r) => r.status === "FAIL");
		const skipped = MATRIX_ROWS.filter((r) => r.status === "SKIP");
		console.log(`\nscore = questions agreeing with jev out of ${QUESTION_COUNT}; PASS threshold = ${Math.round(AGREEMENT_FAIL_THRESHOLD * 100)}%`);
		console.log(`PASS=${passed.length}  FAIL=${failed.length}  SKIP=${skipped.length}  TOTAL=${MATRIX_PAIRS.length} (+jev reference)`);
	});
});
