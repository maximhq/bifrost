/**
 * Typesafe Integration Tests - Official SDK against Bifrost
 *
 * 🌉 SDK DROP-IN TESTING:
 * Uses the official TypeSafe JavaScript SDK (@typesafe-ai/sdk) pointed at
 * Bifrost's /typesafe prefix via baseURL, authenticating to Bifrost with the
 * suite's virtual key via the x-bf-vk header. Every call goes through the SDK.
 */

import { BadRequestError, choice, noul, score, TypeSafeClient } from "@typesafe-ai/sdk";
import { beforeAll, describe, expect, it } from "vitest";
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
