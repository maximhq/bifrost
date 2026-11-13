import type { ModelRouterTarget } from "@/lib/types/complexityRouter";
import type { TFunction } from "i18next";
import { describe, expect, test } from "vitest";
import { addTargetTo, removeTargetAt, updateTargetAt } from "./tierTargets";
import {
	type TierTargetFormData,
	type TierTargetsUpdate,
	toTierTargetForms,
	toTierTargetPayload,
	validateTierTargets,
} from "./tierTargetsEditor";

// t answers with the key, and the values it was given, so an error names its rule.
const t = ((key: string, values?: Record<string, unknown>) =>
	values ? `${key} ${JSON.stringify(values)}` : key) as unknown as TFunction<"models">;

const PROVIDER_OR_MODEL = "routing.modelRouters.models.providerOrModel";
const WEIGHT_POSITIVE = "routing.modelRouters.models.weightPositive";
const weightsMustSum = (total: string) => `routing.modelRouters.models.weightsMustSum ${JSON.stringify({ total })}`;

const target = (overrides: Partial<TierTargetFormData> = {}): TierTargetFormData => ({
	tier: "SIMPLE",
	provider: "",
	model: "",
	key_id: "",
	weight: 1,
	fallbacks: [],
	...overrides,
});

const apply = (start: TierTargetFormData[], updates: TierTargetsUpdate[]) => updates.reduce((targets, update) => update(targets), start);

// What PUT /targets carries for a draft: the Models tab sends { targets } as JSON,
// which drops every field left undefined.
const wireBody = (targets: TierTargetFormData[]) => JSON.parse(JSON.stringify({ targets: toTierTargetPayload(targets) }));

describe("toTierTargetForms", () => {
	test("a router without targets opens with none", () => {
		expect(toTierTargetForms(undefined)).toEqual([]);
		expect(toTierTargetForms([])).toEqual([]);
	});

	test("fills what a saved target leaves out, so every field is controlled", () => {
		expect(toTierTargetForms([{ tier: "MEDIUM", model: "gpt-4o", weight: 1 }])).toEqual([
			{ tier: "MEDIUM", provider: "", model: "gpt-4o", key_id: "", weight: 1, fallbacks: [] },
		]);
	});

	test("keeps the saved order, tier, key pin and weight", () => {
		const saved: ModelRouterTarget[] = [
			{ tier: "COMPLEX", provider: "anthropic", model: "claude-opus", key_id: "key-9", weight: 0.7 },
			{ tier: "SIMPLE", provider: "openai", weight: 1 },
			{ tier: "COMPLEX", provider: "openai", model: "gpt-5", weight: 0.3 },
		];
		expect(toTierTargetForms(saved).map(({ tier, provider, model, key_id, weight }) => [tier, provider, model, key_id, weight])).toEqual([
			["COMPLEX", "anthropic", "claude-opus", "key-9", 0.7],
			["SIMPLE", "openai", "", "", 1],
			["COMPLEX", "openai", "gpt-5", "", 0.3],
		]);
	});

	test("splits each saved fallback into its provider, model and key", () => {
		const [form] = toTierTargetForms([
			{
				tier: "SIMPLE",
				model: "gpt-4o-mini",
				weight: 1,
				fallbacks: [
					"anthropic/claude-haiku",
					"openai/",
					{ provider: "azure", model: "gpt-4o", key_id: "key-2" },
					{ provider: "bedrock", key_id: "key-3" },
				],
			},
		]);
		expect(form.fallbacks).toEqual([
			{ provider: "anthropic", model: "claude-haiku", key_id: "" },
			{ provider: "openai", model: "", key_id: "" },
			{ provider: "azure", model: "gpt-4o", key_id: "key-2" },
			{ provider: "bedrock", model: "", key_id: "key-3" },
		]);
	});

	test("keeps the slashes of a fallback model that has its own", () => {
		const [form] = toTierTargetForms([{ tier: "SIMPLE", model: "m", weight: 1, fallbacks: ["openrouter/meta/llama-3"] }]);
		expect(form.fallbacks).toEqual([{ provider: "openrouter", model: "meta/llama-3", key_id: "" }]);
	});
});

describe("toTierTargetPayload", () => {
	test("sends the tier in upper case with its weight", () => {
		const payload = toTierTargetPayload([
			target({ tier: "SIMPLE", model: "a", weight: 0.25 }),
			target({ tier: "MEDIUM", model: "b" }),
			target({ tier: "COMPLEX", model: "c", weight: 0.001 }),
		]);
		expect(payload.map(({ tier, weight }) => [tier, weight])).toEqual([
			["SIMPLE", 0.25],
			["MEDIUM", 1],
			["COMPLEX", 0.001],
		]);
	});

	test("leaves an empty provider, model or key off the wire, so the request's own is kept", () => {
		expect(wireBody([target({ tier: "MEDIUM", model: "gpt-4o" })])).toEqual({
			targets: [{ tier: "MEDIUM", model: "gpt-4o", weight: 1, fallbacks: [] }],
		});
		expect(wireBody([target({ provider: "openai" })])).toEqual({
			targets: [{ tier: "SIMPLE", provider: "openai", weight: 1, fallbacks: [] }],
		});
	});

	test("sends a pinned key with its provider and model", () => {
		expect(wireBody([target({ tier: "COMPLEX", provider: "anthropic", model: "claude-opus", key_id: "key-9", weight: 1 })])).toEqual({
			targets: [{ tier: "COMPLEX", provider: "anthropic", model: "claude-opus", key_id: "key-9", weight: 1, fallbacks: [] }],
		});
	});

	test("sends an unpinned fallback as provider/model and a pinned one with its key", () => {
		const payload = toTierTargetPayload([
			target({
				model: "gpt-4o-mini",
				fallbacks: [
					{ provider: "anthropic", model: "claude-haiku", key_id: "" },
					{ provider: "azure", model: "gpt-4o", key_id: "key-2" },
					{ provider: "bedrock", model: "", key_id: "key-3" },
				],
			}),
		]);
		expect(payload[0].fallbacks).toEqual([
			"anthropic/claude-haiku",
			{ provider: "azure", model: "gpt-4o", key_id: "key-2" },
			{ provider: "bedrock", key_id: "key-3" },
		]);
	});

	test("a fallback with only a provider keeps the trailing slash the gateway parses it by", () => {
		const payload = toTierTargetPayload([target({ model: "m", fallbacks: [{ provider: "openai", model: "", key_id: "" }] })]);
		expect(payload[0].fallbacks).toEqual(["openai/"]);
	});

	test("drops a fallback row whose provider was never picked", () => {
		const payload = toTierTargetPayload([
			target({
				model: "m",
				fallbacks: [
					{ provider: "", model: "", key_id: "" },
					{ provider: "   ", model: "", key_id: "" },
					{ provider: "anthropic", model: "claude-haiku", key_id: "" },
				],
			}),
		]);
		expect(payload[0].fallbacks).toEqual(["anthropic/claude-haiku"]);
	});

	test("keeps the order of the targets and of each target's fallbacks", () => {
		const payload = toTierTargetPayload([
			target({ tier: "COMPLEX", model: "c" }),
			target({
				tier: "SIMPLE",
				model: "a",
				fallbacks: [
					{ provider: "openai", model: "second", key_id: "" },
					{ provider: "anthropic", model: "first", key_id: "" },
				],
			}),
		]);
		expect(payload.map((entry) => entry.tier)).toEqual(["COMPLEX", "SIMPLE"]);
		expect(payload[1].fallbacks).toEqual(["openai/second", "anthropic/first"]);
	});

	test("clearing every target sends an empty list, never a body without targets", () => {
		// The gateway refuses a body with no targets field; [] is how they are cleared.
		expect(toTierTargetPayload([])).toEqual([]);
		expect(wireBody([])).toEqual({ targets: [] });
	});

	test("does not send form-only fields", () => {
		const [entry] = toTierTargetPayload([target({ provider: "openai", model: "gpt-4o", key_id: "key-1", ttft_timeout_ms: 500 })]);
		expect(Object.keys(entry).sort()).toEqual(["fallbacks", "key_id", "model", "provider", "tier", "weight"]);
	});
});

describe("a saved router's targets through the form", () => {
	const saved: ModelRouterTarget[] = [
		{ tier: "SIMPLE", provider: "openai", model: "gpt-4o-mini", weight: 0.6, fallbacks: ["anthropic/claude-haiku"] },
		{ tier: "SIMPLE", model: "gpt-4o", weight: 0.4, fallbacks: [] },
		{
			tier: "COMPLEX",
			provider: "anthropic",
			model: "claude-opus",
			key_id: "key-9",
			weight: 1,
			fallbacks: [{ provider: "azure", key_id: "key-2" }],
		},
	];

	test("go back out as they came in when nothing is edited", () => {
		expect(wireBody(toTierTargetForms(saved))).toEqual({ targets: saved });
	});

	test("read the same whether the gateway sent no fallbacks or an empty list, so opening the tab leaves nothing to save", () => {
		const withoutField: ModelRouterTarget[] = [{ tier: "MEDIUM", model: "gpt-4o", weight: 1 }];
		const withEmptyList: ModelRouterTarget[] = [{ tier: "MEDIUM", model: "gpt-4o", weight: 1, fallbacks: [] }];
		expect(wireBody(toTierTargetForms(withoutField))).toEqual(wireBody(toTierTargetForms(withEmptyList)));
	});

	test("an edit to one tier changes that tier's entry alone", () => {
		const edited = apply(toTierTargetForms(saved), [updateTargetAt(2, { model: "claude-sonnet" })]);
		expect(wireBody(edited).targets).toEqual([saved[0], saved[1], { ...saved[2], model: "claude-sonnet" }]);
	});
});

describe("validateTierTargets", () => {
	test("a router with no targets is valid: every tier keeps the request's model", () => {
		expect(validateTierTargets([], t)).toEqual({});
	});

	test("a tier without targets is not checked, whatever the others hold", () => {
		expect(validateTierTargets([target({ tier: "COMPLEX", model: "claude-opus" })], t)).toEqual({});
	});

	test("a target needs a provider, a model or both", () => {
		expect(validateTierTargets([target({ provider: "openai" })], t)).toEqual({});
		expect(validateTierTargets([target({ model: "gpt-4o" })], t)).toEqual({});
		expect(validateTierTargets([target({ provider: "openai", model: "gpt-4o" })], t)).toEqual({});
		expect(validateTierTargets([target()], t)).toEqual({ SIMPLE: PROVIDER_OR_MODEL });
	});

	test("a provider and model of only spaces count as not set", () => {
		expect(validateTierTargets([target({ tier: "MEDIUM", provider: "  ", model: " " })], t)).toEqual({ MEDIUM: PROVIDER_OR_MODEL });
	});

	test("one blank target marks its tier even when the tier's other targets are set", () => {
		const targets = [target({ model: "gpt-4o", weight: 0.5 }), target({ weight: 0.5 })];
		expect(validateTierTargets(targets, t)).toEqual({ SIMPLE: PROVIDER_OR_MODEL });
	});

	test("a weight must be above zero", () => {
		expect(validateTierTargets([target({ model: "a", weight: 0 })], t)).toEqual({ SIMPLE: WEIGHT_POSITIVE });
		expect(validateTierTargets([target({ model: "a", weight: -0.5 })], t)).toEqual({ SIMPLE: WEIGHT_POSITIVE });
		// An emptied weight field.
		expect(validateTierTargets([target({ model: "a", weight: Number.NaN })], t)).toEqual({ SIMPLE: WEIGHT_POSITIVE });
	});

	test("a zero weight is reported as such, not as weights that do not sum to 1", () => {
		const targets = [target({ model: "a", weight: 1 }), target({ model: "b", weight: 0 })];
		expect(validateTierTargets(targets, t)).toEqual({ SIMPLE: WEIGHT_POSITIVE });
	});

	test("a tier's only target must carry the whole tier", () => {
		expect(validateTierTargets([target({ model: "a", weight: 1 })], t)).toEqual({});
		expect(validateTierTargets([target({ model: "a", weight: 0.5 })], t)).toEqual({ SIMPLE: weightsMustSum("0.5000") });
	});

	test("three targets of 0.33, 0.33 and 0.34 sum to 1", () => {
		// 0.33 + 0.33 + 0.34 is 1.0000000000000002 in floating point.
		const targets = [target({ model: "a", weight: 0.33 }), target({ model: "b", weight: 0.33 }), target({ model: "c", weight: 0.34 })];
		expect(validateTierTargets(targets, t)).toEqual({});
	});

	test("an even three-way split typed to three decimals is close enough", () => {
		const targets = ["a", "b", "c"].map((model) => target({ model, weight: 0.333 }));
		expect(validateTierTargets(targets, t)).toEqual({});
	});

	test("an even three-way split typed to two decimals is not, and the error shows the total", () => {
		const targets = ["a", "b", "c"].map((model) => target({ model, weight: 0.33 }));
		expect(validateTierTargets(targets, t)).toEqual({ SIMPLE: weightsMustSum("0.9900") });
	});

	test("weights above 1 in total are refused with their total", () => {
		const targets = [target({ model: "a", weight: 0.5 }), target({ model: "b", weight: 0.6 })];
		expect(validateTierTargets(targets, t)).toEqual({ SIMPLE: weightsMustSum("1.1000") });
	});

	test("allows a total within 0.001 of 1 and no further, as the gateway does", () => {
		const pair = (second: number) => [target({ model: "a", weight: 0.5 }), target({ model: "b", weight: second })];
		expect(validateTierTargets(pair(0.4995), t)).toEqual({});
		expect(validateTierTargets(pair(0.5005), t)).toEqual({});
		expect(validateTierTargets(pair(0.4985), t)).toEqual({ SIMPLE: weightsMustSum("0.9985") });
		expect(validateTierTargets(pair(0.5015), t)).toEqual({ SIMPLE: weightsMustSum("1.0015") });
	});

	test("sums each tier on its own", () => {
		const targets = [
			target({ tier: "SIMPLE", model: "a", weight: 0.5 }),
			target({ tier: "COMPLEX", model: "c", weight: 1 }),
			target({ tier: "SIMPLE", model: "b", weight: 0.5 }),
		];
		expect(validateTierTargets(targets, t)).toEqual({});
	});

	test("reports each tier's own problem beside it, and nothing for a tier that is fine", () => {
		const targets = [
			target({ tier: "SIMPLE", weight: 1 }),
			target({ tier: "MEDIUM", model: "b", weight: 1 }),
			target({ tier: "COMPLEX", model: "c", weight: 0.4 }),
			target({ tier: "COMPLEX", model: "d", weight: 0.4 }),
		];
		expect(validateTierTargets(targets, t)).toEqual({ SIMPLE: PROVIDER_OR_MODEL, COMPLEX: weightsMustSum("0.8000") });
	});

	test("shows one problem per tier, the missing model before the weights", () => {
		const targets = [target({ weight: 0 }), target({ model: "b", weight: 0.3 })];
		expect(validateTierTargets(targets, t)).toEqual({ SIMPLE: PROVIDER_OR_MODEL });
	});

	test("fallbacks do not count toward a tier's weights or stand in for its model", () => {
		const fallbacks = [{ provider: "anthropic", model: "claude-haiku", key_id: "" }];
		expect(validateTierTargets([target({ fallbacks })], t)).toEqual({ SIMPLE: PROVIDER_OR_MODEL });
		expect(validateTierTargets([target({ model: "a", fallbacks })], t)).toEqual({});
	});
});

describe("editing a router's models", () => {
	const saved: ModelRouterTarget[] = [{ tier: "SIMPLE", provider: "openai", model: "gpt-4o-mini", weight: 1 }];

	test("a second model added to a full tier has to be picked and weighted before the tier saves", () => {
		let draft = apply(toTierTargetForms(saved), [addTargetTo("SIMPLE")]);
		expect(validateTierTargets(draft, t), "nothing picked yet").toEqual({ SIMPLE: PROVIDER_OR_MODEL });

		// Picking a provider sets it and clears the model and key, as three edits.
		draft = apply(draft, [
			updateTargetAt(1, { provider: "anthropic" }),
			updateTargetAt(1, { model: "" }),
			updateTargetAt(1, { key_id: "" }),
		]);
		expect(validateTierTargets(draft, t), "the full tier left it no weight").toEqual({ SIMPLE: WEIGHT_POSITIVE });

		draft = apply(draft, [updateTargetAt(1, { model: "claude-haiku", weight: 0.3 })]);
		expect(validateTierTargets(draft, t), "the first target still holds the whole tier").toEqual({ SIMPLE: weightsMustSum("1.3000") });

		draft = apply(draft, [updateTargetAt(0, { weight: 0.7 })]);
		expect(validateTierTargets(draft, t)).toEqual({});
		expect(wireBody(draft)).toEqual({
			targets: [
				{ tier: "SIMPLE", provider: "openai", model: "gpt-4o-mini", weight: 0.7, fallbacks: [] },
				{ tier: "SIMPLE", provider: "anthropic", model: "claude-haiku", weight: 0.3, fallbacks: [] },
			],
		});
	});

	test("a model added to a tier that had none is valid as soon as it is picked", () => {
		const draft = apply(toTierTargetForms(saved), [addTargetTo("COMPLEX"), updateTargetAt(1, { model: "claude-opus" })]);
		expect(validateTierTargets(draft, t)).toEqual({});
		expect(wireBody(draft).targets[1]).toEqual({ tier: "COMPLEX", model: "claude-opus", weight: 1, fallbacks: [] });
	});

	test("removing one of two models leaves the tier short until the other takes the whole tier", () => {
		const split: ModelRouterTarget[] = [
			{ tier: "MEDIUM", model: "a", weight: 0.5 },
			{ tier: "MEDIUM", model: "b", weight: 0.5 },
		];
		let draft = apply(toTierTargetForms(split), [removeTargetAt(1)]);
		expect(validateTierTargets(draft, t)).toEqual({ MEDIUM: weightsMustSum("0.5000") });
		draft = apply(draft, [updateTargetAt(0, { weight: 1 })]);
		expect(validateTierTargets(draft, t)).toEqual({});
	});

	test("removing every model is valid and clears the router's targets", () => {
		const draft = apply(toTierTargetForms(saved), [removeTargetAt(0)]);
		expect(validateTierTargets(draft, t)).toEqual({});
		expect(wireBody(draft)).toEqual({ targets: [] });
	});

	test("clearing a target's provider takes its key pin off the wire with it", () => {
		const pinned: ModelRouterTarget[] = [{ tier: "SIMPLE", provider: "openai", model: "gpt-4o", key_id: "key-1", weight: 1 }];
		// The provider's clear button sends these three edits.
		const draft = apply(toTierTargetForms(pinned), [
			updateTargetAt(0, { provider: "" }),
			updateTargetAt(0, { model: "" }),
			updateTargetAt(0, { key_id: "" }),
			updateTargetAt(0, { model: "gpt-4o" }),
		]);
		expect(wireBody(draft)).toEqual({ targets: [{ tier: "SIMPLE", model: "gpt-4o", weight: 1, fallbacks: [] }] });
	});
});