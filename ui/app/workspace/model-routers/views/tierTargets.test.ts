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