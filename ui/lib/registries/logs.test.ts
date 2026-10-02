import { describe, expect, it } from "vitest";
import { listedOptions, type LabelledMetadataFilterField } from "./logs";

const field: LabelledMetadataFilterField = {
	key: "ext_state",
	label: "State",
	options: [
		{ value: "up", label: "Up" },
		{ value: "down", label: "Down" },
		{ value: "unknown", label: "Unknown" },
	],
};

describe("listedOptions", () => {
	it("lists every option while the recorded values are unknown", () => {
		expect(listedOptions(field, undefined, undefined).map((o) => o.value)).toEqual(["up", "down", "unknown"]);
	});

	it("lists only the options rows in the range hold, in the field's order", () => {
		expect(listedOptions(field, { ext_state: ["unknown", "up"] }, undefined).map((o) => o.value)).toEqual(["up", "unknown"]);
	});

	it("keeps the checked option although no row holds it, so it can be unchecked", () => {
		expect(listedOptions(field, { ext_state: ["up"] }, "down").map((o) => o.value)).toEqual(["up", "down"]);
	});

	it("keeps a checked value the field does not name, under the value itself, while the recorded values are unknown", () => {
		expect(listedOptions(field, undefined, "sideways").map((o) => o.value)).toEqual(["up", "down", "unknown", "sideways"]);
	});

	it("keeps a checked value the field does not name although no row holds it", () => {
		expect(listedOptions(field, { ext_state: ["up"] }, "sideways")).toEqual([
			{ value: "up", label: "Up" },
			{ value: "sideways", label: "sideways" },
		]);
	});

	it("lists a checked value rows hold and the field does not name once", () => {
		expect(listedOptions(field, { ext_state: ["sideways"] }, "sideways")).toEqual([{ value: "sideways", label: "sideways" }]);
	});

	it("lists nothing for a key no row in the range holds", () => {
		expect(listedOptions(field, { other: ["x"] }, undefined)).toEqual([]);
	});

	it("lists a recorded value the field does not name under the value itself", () => {
		expect(listedOptions(field, { ext_state: ["up", "sideways"] }, undefined)).toEqual([
			{ value: "up", label: "Up" },
			{ value: "sideways", label: "sideways" },
		]);
	});
});