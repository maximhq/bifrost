import { describe, expect, it } from "vitest";
import { secretVarEquals } from "./secretVarForm";

describe("secretVarEquals", () => {
	it("treats an absent value as an empty one, as the API stores them", () => {
		expect(secretVarEquals(undefined, undefined)).toBe(true);
		expect(secretVarEquals(undefined, { value: "", ref: "" })).toBe(true);
		expect(secretVarEquals({ value: "" }, { value: "", ref: "", type: "plain_text" })).toBe(true);
	});

	it("tells values, references and types apart", () => {
		expect(secretVarEquals({ value: "https://a.example" }, { value: "https://a.example" })).toBe(true);
		expect(secretVarEquals({ value: "https://a.example" }, { value: "https://b.example" })).toBe(false);
		expect(secretVarEquals({ value: "", ref: "env.PUBLIC_URL", type: "env" }, { value: "", ref: "env.OTHER_URL", type: "env" })).toBe(
			false,
		);
		expect(secretVarEquals({ value: "x", type: "plain_text" }, { value: "x", type: "env" })).toBe(false);
		expect(secretVarEquals(undefined, { value: "https://a.example" })).toBe(false);
	});
});