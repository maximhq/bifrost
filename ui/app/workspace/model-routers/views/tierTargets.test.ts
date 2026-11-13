import { describe, expect, test } from "vitest";
import { addTargetTo, removeTargetAt, TierTargetFormData, TierTargetsUpdate, updateTargetAt } from "./tierTargets";

const target = (overrides: Partial<TierTargetFormData> = {}): TierTargetFormData => ({
	tier: "SIMPLE",
	provider: "",
	model: "",
	key_id: "",
	weight: 1,
	fallbacks: [],
	...overrides,
});

// apply runs updates the way React runs queued state updates: each one on the
// result of the one before.
const apply = (start: TierTargetFormData[], updates: TierTargetsUpdate[]) => updates.reduce((targets, update) => update(targets), start);

describe("tier target updates", () => {
	test("picking a provider keeps it while clearing the model and key", () => {
		// The provider picker sends these three edits in a row; built from the
		// list the form last rendered, the last one used to undo the first.
		const start = [target({ model: "old-model", key_id: "old-key" })];
		const picked = apply(start, [
			updateTargetAt(0, { provider: "openai" }),
			updateTargetAt(0, { model: "" }),
			updateTargetAt(0, { key_id: "" }),
		]);
		expect(picked).toEqual([target({ provider: "openai" })]);
		expect(start[0].model, "the previous list is not changed").toBe("old-model");
	});

	test("edits a target by its place in the whole list", () => {
		const start = [target(), target({ tier: "COMPLEX" })];
		expect(apply(start, [updateTargetAt(1, { model: "claude-opus" })])[1].model).toBe("claude-opus");
		expect(apply(start, [updateTargetAt(1, { model: "claude-opus" })])[0]).toEqual(start[0]);
	});

	test("adds a target with what the tier's weights leave, and removes one", () => {
		const start = [target({ weight: 0.25 }), target({ tier: "COMPLEX", weight: 1 })];
		const added = apply(start, [addTargetTo("SIMPLE")]);
		expect(added).toHaveLength(3);
		expect(added[2]).toMatchObject({ tier: "SIMPLE", weight: 0.75, fallbacks: [] });
		expect(apply(added, [removeTargetAt(0)]).map((t) => t.weight)).toEqual([1, 0.75]);
	});
});

describe("adding a target to a tier", () => {
	const weightsIn = (targets: TierTargetFormData[], tier: TierTargetFormData["tier"]) =>
		targets.filter((t) => t.tier === tier).map((t) => t.weight);

	test("the first target of a tier takes the whole tier, with nothing picked yet", () => {
		expect(apply([], [addTargetTo("MEDIUM")])).toEqual([target({ tier: "MEDIUM", weight: 1 })]);
	});

	test("a third target takes exactly what two of 0.33 leave", () => {
		// 1 - 0.66 is 0.33999999999999997 in floating point; the weight field must show 0.34.
		const start = [target({ weight: 0.33 }), target({ weight: 0.33 })];
		expect(weightsIn(apply(start, [addTargetTo("SIMPLE")]), "SIMPLE")).toEqual([0.33, 0.33, 0.34]);
	});

	test("does not carry floating point noise from the tier's weights into the new one", () => {
		// 0.1 + 0.2 is 0.30000000000000004.
		const start = [target({ weight: 0.1 }), target({ weight: 0.2 })];
		expect(apply(start, [addTargetTo("SIMPLE")])[2].weight).toBe(0.7);
	});

	test("a full tier leaves the new target nothing, and an over-full one never a negative weight", () => {
		expect(apply([target({ weight: 1 })], [addTargetTo("SIMPLE")])[1].weight).toBe(0);
		expect(apply([target({ weight: 0.7 }), target({ weight: 0.6 })], [addTargetTo("SIMPLE")])[2].weight).toBe(0);
	});

	test("counts only the tier's own targets", () => {
		const start = [target({ weight: 0.4 }), target({ tier: "COMPLEX", weight: 1 })];
		const added = apply(start, [addTargetTo("MEDIUM"), addTargetTo("SIMPLE")]);
		expect(weightsIn(added, "MEDIUM")).toEqual([1]);
		expect(weightsIn(added, "SIMPLE")).toEqual([0.4, 0.6]);
		expect(weightsIn(added, "COMPLEX")).toEqual([1]);
	});

	test("a target whose weight field was emptied counts as nothing", () => {
		const start = [target({ weight: Number.NaN }), target({ weight: 0.25 })];
		expect(apply(start, [addTargetTo("SIMPLE")])[2].weight).toBe(0.75);
	});

	test("adding twice in a row splits what is left once, not twice", () => {
		// Two clicks before a render: the second builds on the first's result.
		const added = apply([target({ weight: 0.6 })], [addTargetTo("SIMPLE"), addTargetTo("SIMPLE")]);
		expect(weightsIn(added, "SIMPLE")).toEqual([0.6, 0.4, 0]);
	});

	test("puts the new target last and leaves the others as they were", () => {
		const start = [target({ provider: "openai", model: "gpt-4o-mini", weight: 0.5 }), target({ tier: "COMPLEX", model: "claude-opus" })];
		const added = apply(start, [addTargetTo("SIMPLE")]);
		expect(added.slice(0, 2)).toEqual(start);
		expect(added[0], "an untouched target is the same object").toBe(start[0]);
		expect(start, "the previous list is not changed").toHaveLength(2);
	});

	test("gives each new target its own fallbacks", () => {
		const added = apply([], [addTargetTo("SIMPLE"), addTargetTo("COMPLEX")]);
		expect(added[0].fallbacks).toEqual([]);
		expect(added[0].fallbacks).not.toBe(added[1].fallbacks);
	});
});

describe("removing a target", () => {
	test("removes only that target and leaves the rest of the tier's weights for the operator", () => {
		const start = [
			target({ model: "a", weight: 0.5 }),
			target({ tier: "MEDIUM", model: "b", weight: 1 }),
			target({ model: "c", weight: 0.5 }),
		];
		const removed = apply(start, [removeTargetAt(0)]);
		expect(removed).toEqual([start[1], start[2]]);
		expect(removed[1].weight, "the tier is left at 0.5 until it is rebalanced").toBe(0.5);
		expect(start, "the previous list is not changed").toHaveLength(3);
	});

	test("the weight a removed target frees goes to the next one added", () => {
		const start = [target({ model: "a", weight: 0.3 }), target({ model: "b", weight: 0.7 })];
		const replaced = apply(start, [removeTargetAt(1), addTargetTo("SIMPLE")]);
		expect(replaced.map((t) => t.weight)).toEqual([0.3, 0.7]);
		expect(replaced[1].model, "the new target starts blank").toBe("");
	});

	test("removing a tier's only target leaves the tier with none", () => {
		const start = [target({ tier: "MEDIUM", model: "b" }), target({ tier: "COMPLEX", model: "c" })];
		expect(apply(start, [removeTargetAt(0)])).toEqual([start[1]]);
		expect(apply(start, [removeTargetAt(1), removeTargetAt(0)])).toEqual([]);
	});

	test("a place the list no longer has removes nothing", () => {
		const start = [target({ model: "a" })];
		expect(apply(start, [removeTargetAt(3)])).toEqual(start);
		expect(apply(start, [removeTargetAt(-1)])).toEqual(start);
	});
});

describe("editing a target", () => {
	test("changes only the fields named, in that target alone", () => {
		const start = [
			target({
				provider: "openai",
				model: "gpt-4o",
				key_id: "key-1",
				weight: 0.5,
				fallbacks: [{ provider: "anthropic", model: "", key_id: "" }],
			}),
			target({ provider: "openai", model: "gpt-4o-mini", weight: 0.5 }),
			target({ tier: "COMPLEX", model: "claude-opus" }),
		];
		const edited = apply(start, [updateTargetAt(0, { weight: 0.2 })]);
		expect(edited[0]).toEqual({ ...start[0], weight: 0.2 });
		expect(edited[1], "the tier's other target is untouched").toBe(start[1]);
		expect(edited[2], "another tier's target is untouched").toBe(start[2]);
	});

	test("replaces a target's fallbacks without touching another target's", () => {
		const shared = [{ provider: "anthropic", model: "claude-haiku", key_id: "" }];
		const start = [target({ fallbacks: shared }), target({ tier: "MEDIUM", fallbacks: shared })];
		const edited = apply(start, [updateTargetAt(0, { fallbacks: [...shared, { provider: "openai", model: "", key_id: "" }] })]);
		expect(edited[0].fallbacks).toHaveLength(2);
		expect(edited[1].fallbacks).toEqual(shared);
	});

	test("a place the list no longer has changes nothing", () => {
		const start = [target({ model: "a" })];
		expect(apply(start, [updateTargetAt(1, { model: "b" })])).toEqual(start);
	});

	test("an edit queued behind a removal lands on the target now in that place", () => {
		// Edits address a target by its place, so they must run on the latest list.
		const start = [target({ model: "a" }), target({ model: "b" }), target({ model: "c" })];
		const result = apply(start, [removeTargetAt(0), updateTargetAt(0, { weight: 0.4 })]);
		expect(result).toEqual([target({ model: "b", weight: 0.4 }), target({ model: "c" })]);
	});
});